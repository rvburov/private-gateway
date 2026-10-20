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

func TestProfilePortSelection(t *testing.T) {
	for _, tc := range []struct {
		host, query    string
		fallback, want int
		ok             bool
	}{
		{"panel.example", "", 8443, 0, true},
		{"panel.example:8443", "", 8443, 8443, true},
		{"panel.example:8443", "?port=primary", 8443, 0, true},
		{"panel.example", "?port=8443", 8443, 8443, true},
		{"panel.example", "?port=8443", 0, 0, false},
		{"panel.example:8443", "", 0, 0, true},
		{"panel.example", "?port=22", 8443, 0, false},
		{"panel.example", "?port=", 8443, 0, false},
		{"panel.example", "?port=primary&port=8443", 8443, 0, false},
	} {
		r := httptest.NewRequest("GET", "https://"+tc.host+"/"+tc.query, nil)
		s := &Server{options: Options{FallbackPort: tc.fallback}}
		port, ok := s.profilePort(r)
		if port != tc.want || ok != tc.ok {
			t.Fatalf("%s%s fallback=%d: got %d/%v", tc.host, tc.query, tc.fallback, port, ok)
		}
	}
}

func TestFallbackProfilesPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	ctx := context.Background()
	device, err := store.Create(ctx, "Fallback test", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const token = "fallback-integration-token-with-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{
		Token: token, XrayConfigured: true, FallbackPort: 8443,
	})
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
	primary := export("?port=primary", "panel.example:8443")
	backup := export("?port=8443", "panel.example")
	automatic := export("", "panel.example:8443")
	if primary.Port != node.Port || backup.Port != 8443 || backup.FallbackPort != 8443 ||
		automatic.URI != backup.URI {
		t.Fatal("export selected the wrong port")
	}
	mainURI, _ := url.Parse(primary.URI)
	backupURI, _ := url.Parse(backup.URI)
	backupURI.Host = mainURI.Host
	if backupURI.String() != mainURI.String() {
		t.Fatal("fallback changed credentials or REALITY settings")
	}
	mainPath, _ := url.Parse(primary.SubscriptionPath)
	backupPath, _ := url.Parse(backup.SubscriptionPath)
	if mainPath.Path != backupPath.Path || backupPath.Query().Get("port") != "8443" {
		t.Fatal("fallback must reuse the subscription secret")
	}
	for _, p := range []model.Profile{primary, backup} {
		for format, want := range map[string]string{
			"uri": p.URI + "\n", "base64": base64.StdEncoding.EncodeToString([]byte(p.URI + "\n")),
			"sing-box": p.SingBoxJSON, "mihomo": p.MihomoYAML,
		} {
			w := call("GET", p.SubscriptionPath+"&format="+format, "panel.example:8443", false)
			if w.Code != 200 || w.Body.String() != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("subscription format=%s port=%d status=%d", format, p.Port, w.Code)
			}
		}
	}
	w := call("GET", backup.SubscriptionPath+"&format=html", "panel.example", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://panel.example:8443/sub/") ||
		!strings.Contains(w.Body.String(), "format%3Dmihomo%26port%3D8443") {
		t.Fatal("HTML import lost fallback port or format query")
	}
	for _, path := range []string{
		"/api/v1/devices/" + device.ID + "/profile?port=22",
		"/api/v1/devices/" + device.ID + "/profile?port=primary&port=8443",
		mainPath.Path + "?port=22",
	} {
		if w := call("GET", path, "panel.example", true); w.Code != 400 {
			t.Fatalf("unsupported port status=%d", w.Code)
		}
	}
	if w := call("POST", "/api/v1/devices/"+device.ID+"/subscription?port=22", "panel.example", true); w.Code != 400 {
		t.Fatal("invalid port rotated subscription")
	}
	if w := call("GET", primary.SubscriptionPath, "panel.example", false); w.Code != 200 {
		t.Fatal("invalid rotation changed subscription")
	}
	after, err := store.Status(ctx)
	if err != nil || before.DesiredRevision != after.DesiredRevision || before.AppliedRevision != after.AppliedRevision {
		t.Fatal("export changed configuration revisions")
	}
	w = call("POST", "/api/v1/devices/"+device.ID+"/subscription?port=8443", "panel.example", true)
	var rotated struct {
		Path string `json:"subscription_path"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rotated) != nil ||
		!strings.HasSuffix(rotated.Path, "?port=8443") {
		t.Fatal("rotation lost fallback selection")
	}
	for _, path := range []string{primary.SubscriptionPath, backup.SubscriptionPath} {
		if w := call("GET", path, "panel.example", false); w.Code != 404 {
			t.Fatal("rotation left the old subscription active")
		}
	}
	if w := call("GET", rotated.Path, "panel.example", false); w.Code != 200 {
		t.Fatal("rotated subscription unavailable")
	}
	if _, err := store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	for _, port := range []string{"primary", "8443"} {
		path, _ := url.Parse(rotated.Path)
		path.RawQuery = "port=" + port
		if w := call("GET", path.String(), "panel.example", false); w.Code != 404 {
			t.Fatal("revoked subscription still active")
		}
	}
}
