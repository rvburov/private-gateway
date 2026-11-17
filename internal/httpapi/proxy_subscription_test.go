package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func TestRemovedProxyExitFailsClosedAndEntryAssignmentHidesDirectPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Proxy test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Configure(ctx, true, func(_ context.Context, clients []xray.Client) error {
		if len(clients) != 1 || clients[0].ProxyID != "" {
			t.Fatal("server-side proxy credential remained in Xray configuration")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const adminToken = "proxy-exit-test-admin-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: adminToken, XrayConfigured: true, PanelOrigin: "https://panel.example:8443"})
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "https://panel.example:8443"+path, strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Content-Type", "application/json")
		if auth {
			request.Header.Set("Authorization", "Bearer "+adminToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	profilePath := "/api/v1/devices/" + device.ID + "/profile"
	w := call(http.MethodGet, profilePath, "", true)
	var direct model.Profile
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &direct) != nil {
		t.Fatal("direct profile unavailable before entry assignment")
	}
	oldProxyPath := storage.ProxySubscriptionPathPrefix + strings.TrimPrefix(direct.SubscriptionPath, storage.SubscriptionPathPrefix)
	for _, path := range []string{oldProxyPath, oldProxyPath + "?format=uri"} {
		w = call(http.MethodGet, path, "", false)
		if w.Code != http.StatusGone || strings.Contains(w.Body.String(), direct.URI) {
			t.Fatal("old server proxy subscription was exposed")
		}
	}
	for _, path := range []string{profilePath + "?exit=proxy", direct.SubscriptionPath + "?exit=proxy"} {
		w = call(http.MethodGet, path, "", strings.HasPrefix(path, "/api/"))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("old proxy exit accepted at %s: %d", path, w.Code)
		}
	}
	w = call(http.MethodPost, "/api/v1/devices/"+device.ID+"/subscription?exit=proxy", "", true)
	if w.Code != http.StatusBadRequest {
		t.Fatal("old proxy subscription rotation accepted")
	}
	w = call(http.MethodGet, direct.SubscriptionPath+"?format=uri", "", false)
	if w.Code != http.StatusOK || w.Body.String() != direct.URI+"\n" {
		t.Fatal("direct subscription unavailable before assignment")
	}
	proxy, err := store.CreateEntryProxy(ctx, storage.EntryProxyInput{Name: "Entry", Host: "proxy.example", Port: 1080,
		AuthType: "password", Username: "test-user", Password: "test-pass", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	assignPath := "/api/v1/nodes/" + node.ID + "/entry-proxy"
	w = call(http.MethodPut, assignPath, `{}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatal("missing entry_proxy_id removed assignment")
	}
	w = call(http.MethodPut, assignPath, `{"entry_proxy_id":"`+proxy.ID+`"}`, true)
	if w.Code != http.StatusOK {
		t.Fatal("entry proxy assignment failed")
	}
	w = call(http.MethodGet, direct.SubscriptionPath+"?format=uri", "", false)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), direct.URI) {
		t.Fatal("old direct link exposed assigned chain")
	}
	w = call(http.MethodGet, profilePath, "", true)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), direct.URI) {
		t.Fatal("direct export exposed assigned chain")
	}
	w = call(http.MethodPut, assignPath, `{"entry_proxy_id":null}`, true)
	if w.Code != http.StatusOK {
		t.Fatal("explicit null did not remove assignment")
	}
	w = call(http.MethodGet, direct.SubscriptionPath+"?format=uri", "", false)
	if w.Code != http.StatusOK || w.Body.String() != direct.URI+"\n" {
		t.Fatal("direct subscription not restored after explicit unassign")
	}
}
