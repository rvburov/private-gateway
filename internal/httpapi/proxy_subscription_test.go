package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func TestProxyExitSubscriptionFailsClosedPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	node.Host, node.ServerName, node.Port = "192.0.2.10", "example.com", 8443
	node.ConfigHash = "direct-fixture"
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "Proxy test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	const adminToken = "proxy-exit-test-admin-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: adminToken, XrayConfigured: true})
	call := func(method, path string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "https://example.org:8443"+path, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		if auth {
			request.Header.Set("Authorization", "Bearer "+adminToken)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		return result
	}
	profilePath := "/api/v1/devices/" + device.ID + "/profile"
	var direct model.Profile
	w := call(http.MethodGet, profilePath, true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &direct) != nil {
		t.Fatal("original direct profile is unavailable")
	}
	directURI, err := url.Parse(direct.URI)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(direct.SubscriptionPath, storage.SubscriptionPathPrefix)
	proxyPath := storage.ProxySubscriptionPathPrefix + secret
	if w = call(http.MethodGet, profilePath+"?exit=proxy", true); w.Code != http.StatusServiceUnavailable {
		t.Fatal("unconfigured proxy profile did not fail closed")
	}
	if w = call(http.MethodGet, proxyPath, false); w.Code != http.StatusServiceUnavailable {
		t.Fatal("unconfigured proxy subscription did not fail closed")
	}
	if w = call(http.MethodGet, direct.SubscriptionPath+"?exit=proxy", false); w.Code != http.StatusBadRequest {
		t.Fatal("a direct subscription accepted a proxy query that clients may strip")
	}
	if w = call(http.MethodGet, direct.SubscriptionPath, false); w.Code != http.StatusOK {
		t.Fatal("the existing direct subscription stopped working")
	}

	node.ConfigHash = "proxy-fixture"
	node.ProxyAvailable = true
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodGet, profilePath+"?exit=proxy", true); w.Code != http.StatusServiceUnavailable {
		t.Fatal("pending proxy configuration was exposed")
	}
	if w = call(http.MethodGet, proxyPath, false); w.Code != http.StatusServiceUnavailable {
		t.Fatal("pending proxy subscription was exposed")
	}
	if err = store.Configure(ctx, false, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodGet, proxyPath, false); w.Code != http.StatusServiceUnavailable {
		t.Fatal("validation alone exposed the proxy subscription")
	}
	if err = store.Configure(ctx, true, func(_ context.Context, clients []xray.Client) error {
		if len(clients) != 1 || clients[0].ProxyID == "" || clients[0].ID == clients[0].ProxyID {
			t.Fatal("Xray did not receive independent direct and proxy credentials")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var proxy model.Profile
	w = call(http.MethodGet, profilePath+"?exit=proxy", true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &proxy) != nil {
		t.Fatal("applied proxy profile is unavailable")
	}
	proxyURI, err := url.Parse(proxy.URI)
	if err != nil || proxyURI.User.Username() == directURI.User.Username() ||
		proxy.SubscriptionPath != proxyPath || proxyURI.Host != directURI.Host {
		t.Fatal("proxy profile lost its separate credential, path or VPN endpoint")
	}
	for _, check := range []struct{ suffix, want string }{
		{"?format=uri", proxy.URI + "\n"},
		{"?format=sing-box", proxy.SingBoxJSON},
		{"?format=mihomo", proxy.MihomoYAML},
		{"", base64.StdEncoding.EncodeToString([]byte(proxy.URI + "\n"))},
	} {
		w = call(http.MethodGet, proxyPath+check.suffix, false)
		if w.Code != http.StatusOK || w.Body.String() != check.want {
			t.Fatal("proxy subscription format differs from the proxy profile")
		}
	}
	w = call(http.MethodGet, proxyPath+"?format=html", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), proxyPath) ||
		strings.Contains(w.Body.String(), proxy.URI) {
		t.Fatal("proxy import page lost its path or exposed the VLESS credential")
	}
	w = call(http.MethodGet, direct.SubscriptionPath+"?format=uri", false)
	if w.Code != http.StatusOK || w.Body.String() != direct.URI+"\n" {
		t.Fatal("proxy enable changed the existing direct subscription")
	}
	w = call(http.MethodGet, "/api/v1/nodes", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"proxy_available":true`) {
		t.Fatal("applied proxy is not advertised on its node")
	}

	w = call(http.MethodPost, "/api/v1/devices/"+device.ID+"/subscription?exit=proxy", true)
	var rotated struct{ Path string `json:"subscription_path"` }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rotated) != nil ||
		!strings.HasPrefix(rotated.Path, storage.ProxySubscriptionPathPrefix) || rotated.Path == proxyPath {
		t.Fatal("proxy rotation did not return a new proxy path")
	}
	for _, path := range []string{proxyPath, direct.SubscriptionPath} {
		if w = call(http.MethodGet, path, false); w.Code != http.StatusNotFound {
			t.Fatal("rotation left an old direct or proxy link active")
		}
	}
	rotatedSecret := strings.TrimPrefix(rotated.Path, storage.ProxySubscriptionPathPrefix)
	rotatedDirect := storage.SubscriptionPathPrefix + rotatedSecret
	if w = call(http.MethodGet, rotated.Path, false); w.Code != http.StatusOK {
		t.Fatal("rotated proxy link is unavailable")
	}
	if w = call(http.MethodGet, rotatedDirect, false); w.Code != http.StatusOK {
		t.Fatal("rotated direct link is unavailable")
	}
	w = call(http.MethodGet, profilePath+"?exit=proxy", true)
	var again model.Profile
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &again) != nil ||
		again.URI != proxy.URI || again.SubscriptionPath != rotated.Path {
		t.Fatal("rotation changed proxy credential or returned a stale link")
	}

	node.ProxyAvailable = false
	node.ConfigHash = "proxy-disabled-fixture"
	if err = store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodGet, rotated.Path, false); w.Code != http.StatusServiceUnavailable {
		t.Fatal("disabled proxy link silently fell back to direct")
	}
	if w = call(http.MethodGet, rotatedDirect+"?format=uri", false); w.Code != http.StatusOK ||
		w.Body.String() != direct.URI+"\n" {
		t.Fatal("disabled proxy changed the direct link")
	}
	if _, err = store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodGet, rotated.Path, false); w.Code != http.StatusNotFound {
		t.Fatal("revoked proxy subscription is still identifiable")
	}
}
