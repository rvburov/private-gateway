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
)

func TestLegacyProfilePortQueryValidation(t *testing.T) {
	for _, tc := range []struct {
		query string
		valid bool
	}{
		{"", true}, {"?port=primary", true}, {"?port=8443", true},
		{"?port=443", false}, {"?port=22", false}, {"?port=", false},
		{"?port=08443", false}, {"?port=primary&port=8443", false},
		{"?port=8443&port=8443", false}, {"?port=%zz", false},
	} {
		r := httptest.NewRequest("GET", "https://panel.example:8443/"+tc.query, nil)
		if got := validProfilePortQuery(r); got != tc.valid {
			t.Fatalf("%s: valid=%v", tc.query, got)
		}
	}
}

func TestPublicPortMigrationPreservesDeviceAndSubscriptionPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Port migration test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	const token = "port-migration-integration-token-with-32-characters"
	engine := &testXray{}
	handler := New(store, engine, &testBrowser{}, Options{Token: token, XrayConfigured: true})
	call := func(method, path, host string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://"+host+path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	export := func(query, host string) model.Profile {
		t.Helper()
		w := call("GET", "/api/v1/devices/"+device.ID+"/profile"+query, host, true)
		var p model.Profile
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("export status=%d", w.Code)
		}
		return p
	}
	withQuery := func(path, port, format string) string {
		t.Helper()
		parsed, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		if port != "" {
			query.Set("port", port)
		}
		if format != "" {
			query.Set("format", format)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	if w := call("POST", "/api/v1/config/apply", "panel.example", true); w.Code != 200 {
		t.Fatal("initial configuration could not be applied")
	}
	before, err := store.Status(ctx)
	if err != nil || before.DesiredRevision != before.AppliedRevision {
		t.Fatal("initial configuration not applied")
	}
	node.Port = 8443
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Status(ctx)
	if err != nil || pending.DesiredRevision <= before.DesiredRevision ||
		pending.AppliedRevision != before.AppliedRevision || pending.DesiredRevision == pending.AppliedRevision {
		t.Fatal("node migration bypassed normal configuration revision application")
	}
	current := export("", "panel.example:8443")
	if current.Port != 8443 || current.SubscriptionPath != original.SubscriptionPath {
		t.Fatal("migration changed the subscription secret or used the wrong public port")
	}
	oldURI, _ := url.Parse(original.URI)
	newURI, _ := url.Parse(current.URI)
	if oldURI.Port() != "443" || newURI.Port() != "8443" {
		t.Fatal("profile public port did not migrate")
	}
	oldURI.Host = newURI.Host
	if oldURI.String() != newURI.String() {
		t.Fatal("migration changed client UUID or REALITY parameters")
	}
	for _, host := range []string{"panel.example", "panel.example:443", "panel.example:8443"} {
		for _, legacy := range []string{"", "primary", "8443"} {
			query := ""
			if legacy != "" {
				query = "?port=" + legacy
			}
			profile := export(query, host)
			if profile.URI != current.URI || profile.SubscriptionPath != current.SubscriptionPath {
				t.Fatal("legacy selection restored port 443 or changed the secret")
			}
			for format, want := range map[string]string{
				"uri": current.URI + "\n", "base64": base64.StdEncoding.EncodeToString([]byte(current.URI + "\n")),
				"sing-box": current.SingBoxJSON, "mihomo": current.MihomoYAML,
			} {
				w := call("GET", withQuery(original.SubscriptionPath, legacy, format), host, false)
				if w.Code != 200 || w.Body.String() != want || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("subscription format=%s port=%s host=%s status=%d", format, legacy, host, w.Code)
				}
			}
		}
	}
	// Канонические ссылки приложений сохраняют рабочий 8443 даже при старом Host и port=primary.
	w := call("GET", withQuery(original.SubscriptionPath, "primary", "html"), "panel.example:443", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://panel.example:8443/sub/") ||
		!strings.Contains(w.Body.String(), "format%3Dmihomo") || strings.Contains(w.Body.String(), "port%3Dprimary") {
		t.Fatal("HTML import restored a legacy endpoint or lost its format query")
	}
	for _, query := range []string{"?port=443", "?port=22", "?port=", "?port=primary&port=8443", "?port=8443&port=8443", "?port=%zz"} {
		for _, path := range []string{"/api/v1/devices/" + device.ID + "/profile", original.SubscriptionPath} {
			if w := call("GET", path+query, "panel.example:8443", true); w.Code != 400 {
				t.Fatalf("invalid port query accepted: %s status=%d", query, w.Code)
			}
		}
		if w := call("POST", "/api/v1/devices/"+device.ID+"/subscription"+query, "panel.example:8443", true); w.Code != 400 {
			t.Fatal("invalid port query rotated subscription")
		}
	}
	if w := call("GET", original.SubscriptionPath, "panel.example:8443", false); w.Code != 200 {
		t.Fatal("invalid rotation changed subscription")
	}
	unchanged, err := store.Status(ctx)
	if err != nil || unchanged.DesiredRevision != pending.DesiredRevision || unchanged.AppliedRevision != pending.AppliedRevision || unchanged.ActiveDevices != before.ActiveDevices {
		t.Fatal("exports modified revisions or devices")
	}
	devices, err := store.Devices(ctx)
	if err != nil || len(devices) != 1 || devices[0].ID != device.ID || devices[0].RevokedAt != nil {
		t.Fatal("device did not survive migration")
	}
	if w := call("POST", "/api/v1/config/apply", "panel.example:8443", true); w.Code != 200 {
		t.Fatal("migrated configuration could not be applied")
	}
	applied, err := store.Status(ctx)
	if err != nil || applied.AppliedRevision != pending.DesiredRevision || applied.DesiredRevision != pending.DesiredRevision {
		t.Fatal("normal apply did not mark the migration applied")
	}
	if len(engine.clients) != 1 || engine.clients[0].ID != newURI.User.Username() {
		t.Fatal("normal apply changed the device credential")
	}
	w = call("POST", "/api/v1/devices/"+device.ID+"/subscription?port=primary", "panel.example:8443", true)
	var rotated struct {
		Path string `json:"subscription_path"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rotated) != nil || rotated.Path == original.SubscriptionPath || strings.Contains(rotated.Path, "?") {
		t.Fatal("rotation failed or retained the removed port selector")
	}
	for _, legacy := range []string{"", "primary", "8443"} {
		if w := call("GET", withQuery(original.SubscriptionPath, legacy, "uri"), "panel.example:8443", false); w.Code != 404 {
			t.Fatal("rotation left the old subscription active")
		}
		if w := call("GET", withQuery(rotated.Path, legacy, "uri"), "panel.example:8443", false); w.Code != 200 || w.Body.String() != current.URI+"\n" {
			t.Fatal("rotated subscription lost port 8443 or changed device credentials")
		}
	}
	if _, err := store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"", "primary", "8443"} {
		if w := call("GET", withQuery(rotated.Path, legacy, "uri"), "panel.example:8443", false); w.Code != 404 {
			t.Fatal("revoked subscription remained active")
		}
	}
}
