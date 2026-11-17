package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func TestBridgeProfilesAndSubscriptionPreserveVLESSPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	node.XHTTP = &model.XHTTPNode{Port: 8444, PublicKey: base64.RawURLEncoding.EncodeToString(bytesOf(2, 32)), ShortID: "aabbccddeeff0011", Path: "/test-path-0123456789abcdef", Mode: "auto"}
	node.H3 = &model.H3Node{Port: 8445, ServerName: "h3.example", Path: "/h3-test-path-0123456789", Mode: "auto"}
	if err := store.EnsureNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	device, err := store.Create(ctx, "Bridge phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Configure(ctx, true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	direct, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if direct.ConnectionMode != "direct" || direct.Bridge != nil || direct.H3URI == "" {
		t.Fatal("wrong direct profile metadata")
	}
	before, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const admin = "bridge-test-admin-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: admin, PanelOrigin: "https://panel.example:8443"})
	call := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://panel.example:8443"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+admin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	route := "/api/v1/nodes/" + node.ID + "/connection"
	if w := call(http.MethodPut, route, `{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":9443,"xhttp_port":9444}}`, false); w.Code != 401 {
		t.Fatal("bridge assignment allowed without administrator")
	}
	if w := call(http.MethodPut, route, `{"mode":"bridge","bridge":{"host":"BRIDGE.EXAMPLE.","tcp_port":9443,"xhttp_port":9444}}`, true); w.Code != 200 {
		t.Fatalf("bridge assignment: %d %s", w.Code, w.Body.String())
	}
	bridge, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.ConnectionMode != "bridge" || bridge.Bridge == nil || bridge.Bridge.Host != "bridge.example" || bridge.Port != 9443 || bridge.XHTTPPort != 9444 || bridge.H3URI != "" || bridge.H3Port != 0 || bridge.SubscriptionPath != direct.SubscriptionPath {
		t.Fatal("bridge profile did not preserve identity or suppress unsupported H3")
	}
	for _, pair := range [][2]string{{direct.URI, bridge.URI}, {direct.XHTTPURI, bridge.XHTTPURI}} {
		original, _ := url.Parse(pair[0])
		routed, _ := url.Parse(pair[1])
		if original.User.String() != routed.User.String() || original.RawQuery != routed.RawQuery || routed.Hostname() != "bridge.example" {
			t.Fatal("bridge changed VLESS credentials, transport or REALITY settings")
		}
	}
	if !strings.Contains(bridge.SingBoxJSON, `"server": "bridge.example"`) || !strings.Contains(bridge.MihomoYAML, `server: "bridge.example"`) {
		t.Fatal("client config still advertises the direct endpoint")
	}
	w := call(http.MethodGet, direct.SubscriptionPath, "", false)
	decoded, err := base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != 200 || err != nil || !strings.Contains(string(decoded), bridge.XHTTPURI) || strings.Contains(string(decoded), "h3.example") || strings.Contains(string(decoded), "@"+node.Host+":") {
		t.Fatal("ordinary subscription did not use bridge endpoints")
	}
	for _, bad := range []string{
		`{}`, `{"mode":"bridge","bridge":null}`, `{"mode":"direct"}`, `{"mode":"other","bridge":null}`,
		`{"mode":"direct","bridge":{"host":"bridge.example","tcp_port":443}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":9443,"xhttp_port":9443}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":9443}}`,
		`{"mode":"bridge","bridge":{"host":"bad/host","tcp_port":9443,"xhttp_port":9444}}`,
		`{"mode":"bridge","bridge":{"host":"bad-.example","tcp_port":9443,"xhttp_port":9444}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":22,"xhttp_port":9444}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":9443,"xhttp_port":22}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":65536,"xhttp_port":9444}}`,
		`{"mode":"bridge","bridge":{"host":"bridge.example","tcp_port":9443,"xhttp_port":9444,"password":"must-not-be-accepted"}}`,
	} {
		if w := call(http.MethodPut, route, bad, true); w.Code != 400 {
			t.Fatalf("invalid bridge input accepted: %d", w.Code)
		}
	}
	unchanged, err := store.Export(ctx, device.ID)
	if err != nil || unchanged.URI != bridge.URI {
		t.Fatal("invalid update changed saved route")
	}
	actual, err := store.Node(ctx, node.ID)
	if err != nil || actual.Host != node.Host || actual.Port != node.Port {
		t.Fatal("bridge changed exit listener settings")
	}
	config, err := store.BridgeConfiguration(ctx, node.ID)
	if err != nil || len(config.Listeners) != 2 || config.Listeners[0].TargetHost != node.Host || config.Listeners[0].ListenPort != 9443 || config.Listeners[1].TargetPort != 8444 {
		t.Fatal("bridge install config mismatch")
	}
	data, _ := json.Marshal(config)
	parsed, _ := url.Parse(direct.URI)
	if strings.Contains(string(data), parsed.User.Username()) || strings.Contains(string(data), node.PublicKey) {
		t.Fatal("bridge config contains unnecessary VPN credentials")
	}
	after, _ := store.Status(ctx)
	if after.DesiredRevision != before.DesiredRevision || after.AppliedRevision != before.AppliedRevision {
		t.Fatal("bridge assignment changed Xray revision")
	}
	if w := call(http.MethodPut, route, `{"mode":"direct","bridge":null}`, true); w.Code != 200 {
		t.Fatal("explicit direct selection failed")
	}
	restored, err := store.Export(ctx, device.ID)
	if err != nil || restored.URI != direct.URI || restored.H3URI != direct.H3URI {
		t.Fatal("direct profile was not restored")
	}
	if _, err := store.SetNodeConnection(ctx, node.ID, "bridge", &model.BridgeNode{Host: node.Host, TCPPort: 9443, XHTTPPort: 9444}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatal("bridge accepted exit host as entry")
	}
}

func TestBridgeListenerConflictsAndTargetChangesPostgreSQL(t *testing.T) {
	store, _, local, _ := integrationStore(t)
	ctx := context.Background()
	node, _, _, err := store.CreateRemoteNode(ctx, model.Node{Name: "Remote bridge", Host: "198.51.100.8", Port: 8443, ServerName: local.ServerName, PublicKey: local.PublicKey, ShortID: local.ShortID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetNodeConnection(ctx, local.ID, "bridge", &model.BridgeNode{Host: "bridge.example", TCPPort: 9443}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetNodeConnection(ctx, node.ID, "bridge", &model.BridgeNode{Host: "BRIDGE.EXAMPLE.", TCPPort: 9443}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("two nodes assigned same bridge listener")
	}
	if _, err = store.SetNodeConnection(ctx, node.ID, "bridge", &model.BridgeNode{Host: "bridge.example", TCPPort: 10443}); err != nil {
		t.Fatal(err)
	}
	all, err := store.BridgeConfiguration(ctx, node.ID)
	if err != nil || len(all.Listeners) != 2 {
		t.Fatal("export omitted another node on same bridge")
	}
	node.Port = 9443
	updated, err := store.UpdateRemoteNode(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConnectionMode != "unconfigured" || updated.Bridge != nil {
		t.Fatal("changed remote endpoint kept stale bridge")
	}
	if _, err = store.BridgeConfiguration(ctx, node.ID); !errors.Is(err, storage.ErrConnectionUnavailable) {
		t.Fatal("stale remote bridge exported")
	}
	if err = store.EnsureNode(ctx, local); err != nil {
		t.Fatal(err)
	}
	preserved, err := store.Node(ctx, local.ID)
	if err != nil || preserved.ConnectionMode != "bridge" || preserved.Bridge == nil {
		t.Fatal("unchanged local upsert discarded configured bridge")
	}
	local.Port++
	if err = store.EnsureNode(ctx, local); err != nil {
		t.Fatal(err)
	}
	updated, err = store.Node(ctx, local.ID)
	if err != nil || updated.ConnectionMode != "unconfigured" {
		t.Fatal("changed local endpoint kept stale bridge")
	}
}

func TestEntryChainRetirementMigrationPreservesDevicesPostgreSQL(t *testing.T) {
	store, pool, node, dsn := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Migration phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := store.Export(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	var credentialBefore []byte
	if err = pool.QueryRow(ctx, "SELECT credential_ciphertext FROM devices WHERE id=$1", device.ID).Scan(&credentialBefore); err != nil {
		t.Fatal(err)
	}
	// Воспроизводим предыдущую схему только в изолированной тестовой базе.
	_, err = pool.Exec(ctx, `ALTER TABLE nodes DROP CONSTRAINT nodes_bridge_config_valid,DROP COLUMN bridge_config,DROP COLUMN connection_mode;
 CREATE TABLE entry_proxies (id uuid PRIMARY KEY,username_ciphertext bytea,password_ciphertext bytea);
 ALTER TABLE nodes ADD COLUMN entry_proxy_id uuid REFERENCES entry_proxies(id);
 ALTER TABLE devices ADD COLUMN chain_subscription_generation bigint NOT NULL DEFAULT 0,ADD COLUMN chain_subscription_hash bytea UNIQUE;
 UPDATE schema_migrations SET version=9;`)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := uuid.NewString()
	chainToken := strings.Repeat("x", 43)
	if _, err = pool.Exec(ctx, "INSERT INTO entry_proxies VALUES ($1,$2,$3)", proxyID, []byte("old-user-ciphertext"), []byte("old-password-ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE nodes SET entry_proxy_id=$1 WHERE id=$2", proxyID, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE devices SET chain_subscription_hash=$1 WHERE id=$2", secure.SubscriptionHash(chainToken), device.ID); err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate(dsn); err != nil {
		t.Fatal(err)
	}
	var credentialAfter []byte
	var proxyTable *string
	if err = pool.QueryRow(ctx, "SELECT credential_ciphertext FROM devices WHERE id=$1", device.ID).Scan(&credentialAfter); err != nil || string(credentialAfter) != string(credentialBefore) {
		t.Fatal("migration changed active credential")
	}
	if err = pool.QueryRow(ctx, "SELECT to_regclass('entry_proxies')::text").Scan(&proxyTable); err != nil || proxyTable != nil {
		t.Fatal("old proxy ciphertext storage survived migration")
	}
	migrated, err := store.Node(ctx, node.ID)
	if err != nil || migrated.ConnectionMode != "unconfigured" {
		t.Fatal("assigned SOCKS node silently became direct")
	}
	if _, err = store.Export(ctx, device.ID); !errors.Is(err, storage.ErrConnectionUnavailable) {
		t.Fatal("unconfirmed route exported")
	}
	const admin = "retired-chain-test-admin-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: admin, PanelOrigin: "https://panel.example:8443"})
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://panel.example:8443"+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+admin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodGet, direct.SubscriptionPath); w.Code != 404 {
		t.Fatal("ordinary subscription silently became direct")
	}
	for _, path := range []string{"/sub/entry/" + chainToken, "/api/v1/devices/" + device.ID + "/chain", "/api/v1/entry-proxies"} {
		if w := call(http.MethodGet, path); w.Code != 410 || strings.Contains(w.Body.String(), "ciphertext") {
			t.Fatal("removed chain endpoint did not fail closed")
		}
	}
	if w := call(http.MethodPost, "/api/v1/devices/"+device.ID+"/chain-subscription"); w.Code != 410 {
		t.Fatal("removed chain rotation still available")
	}
	if w := call(http.MethodGet, "/sub/"+chainToken); w.Code != 404 {
		t.Fatal("chain token was promoted to standard subscription")
	}
	if _, err = store.SetNodeConnection(ctx, node.ID, "direct", nil); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Export(ctx, device.ID)
	if err != nil || restored.URI != direct.URI || restored.SubscriptionPath != direct.SubscriptionPath {
		t.Fatal("ordinary identity not preserved after explicit direct selection")
	}
	if w := call(http.MethodGet, "/sub/entry/"+chainToken); w.Code != 410 {
		t.Fatal("retired chain token revived after direct selection")
	}
}
