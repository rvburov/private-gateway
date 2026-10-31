package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func TestXHTTPSubscriptionPreservesDevicePostgreSQL(t *testing.T) {
	store, pool, node, dsn := integrationStore(t)
	ctx := context.Background()
	node.Host, node.ServerName, node.Port = "192.0.2.10", "example.com", 8443
	node.ConfigHash = "tcp-fixture"
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "XHTTP test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Секрет тестового устройства заведомо ненастоящий и одинаков для двух входов.
	cipher, err := secure.New(base64.StdEncoding.EncodeToString(bytesOf(1, 32)))
	if err != nil {
		t.Fatal(err)
	}
	const credential = "11111111-2222-3333-4444-555555555555"
	encrypted, err := cipher.Encrypt(credential, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE devices SET credential_ciphertext=$1 WHERE id=$2", encrypted, device.ID); err != nil {
		t.Fatal(err)
	}
	before, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.XHTTPURI != "" || before.XHTTPPort != 0 {
		t.Fatal("disabled XHTTP must not appear in the profile")
	}
	node.XHTTP = &model.XHTTPNode{
		Port: 8444, PublicKey: base64.RawURLEncoding.EncodeToString(bytesOf(2, 32)),
		ShortID: "aabbccddeeff0011", Path: "/test-path-0123456789abcdef", Mode: "auto",
	}
	node.ConfigHash = "xhttp-fixture"
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	after, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SubscriptionPath != before.SubscriptionPath || after.URI != before.URI ||
		after.SingBoxJSON != before.SingBoxJSON || after.MihomoYAML != before.MihomoYAML {
		t.Fatal("enabling XHTTP changed the existing subscription, credential or client formats")
	}
	xhttpURI, err := url.Parse(after.XHTTPURI)
	if err != nil || after.XHTTPPort != 8444 || xhttpURI.User.Username() != credential ||
		xhttpURI.Query().Get("type") != "xhttp" || xhttpURI.Query().Has("flow") {
		t.Fatal("XHTTP profile does not use the shared device credential and its own transport")
	}
	// JSON-параметры должны переживать повторное чтение базы, не полагаясь на память API.
	reader, err := storage.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	nodes, err := reader.Nodes(ctx)
	if err != nil || len(nodes) != 1 || !reflect.DeepEqual(nodes[0].XHTTP, node.XHTTP) {
		t.Fatal("XHTTP node metadata did not survive storage round trip")
	}
	statusBefore, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	statusAfter, err := store.Status(ctx)
	if err != nil || statusAfter.DesiredRevision != statusBefore.DesiredRevision {
		t.Fatal("unchanged XHTTP metadata advanced the configuration revision")
	}

	const token = "xhttp-subscription-test-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: token, XrayConfigured: true})
	get := func(path, agent string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://example.org:8443"+path, nil)
		r.Header.Set("User-Agent", agent)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	// EnsureNode сохраняет желаемые параметры, но Xray ещё не подтвердил вход.
	w := get(after.SubscriptionPath, "v2rayN/Xray")
	decoded, decodeErr := base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != http.StatusOK || decodeErr != nil || string(decoded) != before.URI+"\n" {
		t.Fatal("unapplied XHTTP must not be advertised by the public subscription")
	}
	// Одной проверки конфигурации недостаточно: она не меняет применённую ревизию.
	if err = store.Configure(ctx, false, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w = get(after.SubscriptionPath, "v2rayN/Xray")
	decoded, decodeErr = base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != http.StatusOK || decodeErr != nil || string(decoded) != before.URI+"\n" {
		t.Fatal("validation alone advertised the pending XHTTP listener")
	}
	if err = store.Configure(ctx, true, func(context.Context, []xray.Client) error {
		return errors.New("test apply failed")
	}); err == nil {
		t.Fatal("expected the failed apply")
	}
	w = get(after.SubscriptionPath, "v2rayN/Xray")
	decoded, decodeErr = base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != http.StatusOK || decodeErr != nil || string(decoded) != before.URI+"\n" {
		t.Fatal("failed apply advertised the pending XHTTP listener")
	}
	if err = store.Configure(ctx, true, func(_ context.Context, clients []xray.Client) error {
		if len(clients) != 1 || clients[0].ID != credential {
			t.Fatal("the two transports must use one active device credential")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"", "v2rayN/Xray"} {
		w := get(after.SubscriptionPath, agent)
		decoded, decodeErr := base64.StdEncoding.DecodeString(w.Body.String())
		lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
		if w.Code != http.StatusOK || decodeErr != nil || len(lines) != 2 || lines[0] != after.XHTTPURI {
			t.Fatal("the common subscription must list XHTTP first and TCP second")
		}
		tcpURI, parseErr := url.Parse(lines[1])
		if parseErr != nil || tcpURI.User.Username() != credential || tcpURI.Query().Get("flow") != "xtls-rprx-vision" ||
			!strings.HasSuffix(tcpURI.Fragment, "TCP Vision") {
			t.Fatal("the TCP backup lost its credential, flow or visible transport label")
		}
	}
	for _, tc := range []struct {
		query, agent, content string
	}{
		{"", "HiddifyNext like ClashMeta sing-box", base64.StdEncoding.EncodeToString([]byte(before.URI + "\n"))},
		{"?format=base64", "Hiddify", base64.StdEncoding.EncodeToString([]byte(before.URI + "\n"))},
		{"?format=uri", "v2rayN", before.URI + "\n"},
		{"?format=sing-box", "", before.SingBoxJSON},
		{"", "sing-box", before.SingBoxJSON},
		{"?format=mihomo", "", before.MihomoYAML},
		{"", "Mihomo", before.MihomoYAML},
		{"", "Stash", before.MihomoYAML},
	} {
		w := get(after.SubscriptionPath+tc.query, tc.agent)
		if w.Code != http.StatusOK || w.Body.String() != tc.content {
			t.Fatalf("legacy format changed for agent=%q query=%q status=%d", tc.agent, tc.query, w.Code)
		}
	}
	// HTML не раскрывает ни один URI и сохраняет исходную каноническую подписку.
	w = get(after.SubscriptionPath+"?format=html", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), credential) ||
		!strings.Contains(w.Body.String(), after.SubscriptionPath) {
		t.Fatal("subscription import page changed the canonical link or exposed the credential")
	}
	if _, err = store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Export(ctx, device.ID); !errors.Is(err, storage.ErrRevoked) {
		t.Fatal("revoked device still exports transport profiles")
	}
	if w = get(after.SubscriptionPath, "v2rayN"); w.Code != http.StatusNotFound {
		t.Fatal("revoked device subscription is still available")
	}
	if err = store.Configure(ctx, true, func(_ context.Context, clients []xray.Client) error {
		if len(clients) != 0 {
			t.Fatal("revoked credential remains in the shared transport client list")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestXHTTPNodeRejectsStaleExportsPostgreSQL(t *testing.T) {
	store, _, node, dsn := integrationStore(t)
	ctx := context.Background()
	node.Host, node.ServerName = "198.51.100.10", "example.com"
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "Stale XHTTP test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secure.New(base64.StdEncoding.EncodeToString(bytesOf(1, 32)))
	if err != nil {
		t.Fatal(err)
	}
	current, err := storage.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	node.XHTTP = &model.XHTTPNode{Port: 8444, PublicKey: base64.RawURLEncoding.EncodeToString(bytesOf(3, 32)),
		ShortID: "1122334455667788", Path: "/test-xhttp-0123456789abcdef", Mode: "auto"}
	node.ConfigHash = "different-xhttp-settings"
	if err = current.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Export(ctx, device.ID); !errors.Is(err, storage.ErrStaleConfig) {
		t.Fatal("stale API process still exports profiles")
	}
	secret := strings.TrimPrefix(before.SubscriptionPath, storage.SubscriptionPathPrefix)
	if _, _, err = store.Subscription(ctx, secret); !errors.Is(err, storage.ErrStaleConfig) {
		t.Fatal("stale API process still serves the subscription")
	}
	profile, err := current.Export(ctx, device.ID)
	if err != nil || profile.XHTTPURI == "" || profile.SubscriptionPath != before.SubscriptionPath {
		t.Fatal("current API process failed to preserve the canonical subscription")
	}
	public, _, err := current.Subscription(ctx, secret)
	if err != nil || public.XHTTPURI != "" || public.XHTTPPort != 0 || public.URI != before.URI {
		t.Fatal("new API process advertised XHTTP before applying its configuration")
	}
	if err = current.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	public, _, err = current.Subscription(ctx, secret)
	if err != nil || public.XHTTPURI != profile.XHTTPURI || public.XHTTPPort != profile.XHTTPPort {
		t.Fatal("successfully applied XHTTP missing from the public subscription")
	}
	// Выключение второго входа удаляет только XHTTP-профиль, сохраняя прежний TCP и ссылку.
	node.XHTTP = nil
	node.ConfigHash = "tcp-settings-again"
	if err = current.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	profile, err = current.Export(ctx, device.ID)
	if err != nil || profile.XHTTPURI != "" || profile.XHTTPPort != 0 || profile.URI != before.URI ||
		profile.SubscriptionPath != before.SubscriptionPath {
		t.Fatal("disabling XHTTP changed the TCP profile or subscription")
	}
	public, _, err = current.Subscription(ctx, secret)
	if err != nil || public.XHTTPURI != "" || public.XHTTPPort != 0 || public.URI != before.URI {
		t.Fatal("pending disable must preserve the public TCP subscription")
	}
	encoded, err := json.Marshal(profile)
	if err != nil || strings.Contains(string(encoded), "xhttp_uri") || strings.Contains(string(encoded), "xhttp_port") {
		t.Fatal("disabled optional XHTTP fields must be omitted")
	}
}

func bytesOf(value byte, size int) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = value
	}
	return result
}
