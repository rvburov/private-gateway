package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
	"github.com/rvburov/private-gateway/internal/secure"
	"github.com/rvburov/private-gateway/internal/storage"
)

func TestSubscriptionLinkPostgreSQL(t *testing.T) {
	store, pool, node, _ := integrationStore(t)
	const token = "subscription-integration-token-with-32-characters"
	server := httptest.NewServer(New(store, &testXray{}, &testBrowser{}, Options{Token: token, XrayConfigured: true}))
	defer server.Close()
	call := func(method, path, auth string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			request.Header.Set("Authorization", "Bearer "+auth)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, data
	}
	device, err := store.Create(context.Background(), "Mom phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, data := call("GET", "/api/v1/devices/"+device.ID+"/profile", token, nil)
	var profile model.Profile
	if response.StatusCode != 200 || json.Unmarshal(data, &profile) != nil {
		t.Fatalf("profile=%d", response.StatusCode)
	}
	link := profile.SubscriptionPath
	secret := strings.TrimPrefix(link, storage.SubscriptionPathPrefix)
	if !strings.HasPrefix(link, "/sub/") || !subscriptionToken.MatchString(secret) {
		t.Fatalf("subscription path %q is not /sub/ and a 43-character token", link)
	}
	var stored []byte
	if err = pool.QueryRow(context.Background(), "SELECT subscription_hash FROM devices WHERE id=$1", device.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, secure.SubscriptionHash(secret)) {
		t.Fatal("the database must keep the hash of the shown link")
	}
	for _, table := range []string{"devices", "audit_events"} {
		var leaks int
		if err = pool.QueryRow(context.Background(),
			"SELECT count(*) FROM "+table+" WHERE strpos("+table+"::text, $1) > 0", secret).Scan(&leaks); err != nil || leaks != 0 {
			t.Fatalf("the link token is stored in %s: %v", table, err)
		}
	}
	if _, again := call("GET", "/api/v1/devices/"+device.ID+"/profile", token, nil); !bytes.Contains(again, []byte(link)) {
		t.Fatal("showing the profile again changed the link")
	}

	// The link works without the administrator session, in the format the client asks for.
	response, data = call("GET", link, "", nil)
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if response.StatusCode != 200 || err != nil || string(decoded) != profile.URI+"\n" {
		t.Fatalf("default subscription=%d, want the base64 share link", response.StatusCode)
	}
	if response.Header.Get("Profile-Title") != "base64:"+base64.StdEncoding.EncodeToString([]byte("Mom phone")) ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(response.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatal("subscription headers are missing")
	}
	for _, check := range []struct {
		path    string
		headers map[string]string
		want    string
		kind    string
	}{
		{link + "?format=uri", nil, profile.URI + "\n", "text/plain"},
		{link + "?format=sing-box", nil, profile.SingBoxJSON, "application/json"},
		{link + "?format=mihomo", nil, profile.MihomoYAML, "text/yaml"},
		{link, map[string]string{"User-Agent": "clash-verge/v2.2.3"}, profile.MihomoYAML, "text/yaml"},
		{link, map[string]string{"User-Agent": "HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box"},
			base64.StdEncoding.EncodeToString([]byte(profile.URI + "\n")), "text/plain"},
	} {
		response, data = call("GET", check.path, "", check.headers)
		if response.StatusCode != 200 || string(data) != check.want ||
			!strings.HasPrefix(response.Header.Get("Content-Type"), check.kind) {
			t.Fatalf("%s %v: status %d, type %q", check.path, check.headers, response.StatusCode, response.Header.Get("Content-Type"))
		}
	}
	response, data = call("GET", link, "", map[string]string{"Accept": "text/html,application/xhtml+xml"})
	page := string(data)
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(page, `href="hiddify://import/http://`) || !strings.Contains(page, "clash://install-config?url=") ||
		!strings.Contains(page, link) || strings.Contains(page, profile.URI) {
		t.Fatal("the browser page must offer the link to apps without showing the profile itself")
	}
	if response, _ = call("GET", link+"?format=v2ray-json", "", nil); response.StatusCode != 400 {
		t.Fatalf("unknown format=%d", response.StatusCode)
	}
	for _, path := range []string{"/sub/short", "/sub/" + strings.Repeat("A", 43), "/sub/" + strings.Repeat("A", 42) + "!"} {
		if response, _ = call("GET", path, "", nil); response.StatusCode != 404 {
			t.Fatalf("%s=%d", path, response.StatusCode)
		}
	}

	// A new link replaces the old one at once.
	if response, _ = call("POST", "/api/v1/devices/"+device.ID+"/subscription", "", nil); response.StatusCode != 401 {
		t.Fatalf("rotation without a session=%d", response.StatusCode)
	}
	response, data = call("POST", "/api/v1/devices/"+device.ID+"/subscription", token, nil)
	var rotated struct {
		Path string `json:"subscription_path"`
	}
	if response.StatusCode != 200 || json.Unmarshal(data, &rotated) != nil || rotated.Path == link ||
		!strings.HasPrefix(rotated.Path, "/sub/") {
		t.Fatalf("rotation=%d", response.StatusCode)
	}
	if response, _ = call("GET", link, "", nil); response.StatusCode != 404 {
		t.Fatal("the replaced link still works")
	}
	if response, _ = call("GET", rotated.Path, "", nil); response.StatusCode != 200 {
		t.Fatal("the new link does not work")
	}
	if _, again := call("GET", "/api/v1/devices/"+device.ID+"/profile", token, nil); !bytes.Contains(again, []byte(rotated.Path)) {
		t.Fatal("the profile shows another link than the new one")
	}
	_, audit := call("GET", "/api/v1/audit", token, nil)
	if !bytes.Contains(audit, []byte(`"action":"device.subscription_rotate"`)) || bytes.Contains(audit, []byte(secret)) {
		t.Fatal("rotation must be audited without the link")
	}

	// Revoking the device turns its link off before Xray is even reconfigured.
	if _, err = store.Revoke(context.Background(), device.ID); err != nil {
		t.Fatal(err)
	}
	if response, _ = call("GET", rotated.Path, "", nil); response.StatusCode != 404 {
		t.Fatal("the link of a revoked device still works")
	}
	if response, _ = call("POST", "/api/v1/devices/"+device.ID+"/subscription", token, nil); response.StatusCode != 410 {
		t.Fatalf("rotation for a revoked device=%d", response.StatusCode)
	}
}
