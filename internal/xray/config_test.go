package xray

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/model"
)

const testID = "b9ee76b7-2d56-4f5f-8a95-bb098715bbad"

func testSettings() Settings {
	return Settings{
		PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		Listen:     "127.0.0.1", Port: 10443, ServerName: "www.example.com",
		Target: "www.example.com:443", ShortID: "0123456789abcdef",
	}
}

func TestPublicKey(t *testing.T) {
	s := testSettings()
	key, err := PublicKey(s.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	private, err := ecdh.X25519().NewPrivateKey(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	want := base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	if key != want {
		t.Fatalf("public key = %q, want %q", key, want)
	}
	for _, key := range []string{"", "invalid", s.PrivateKey + "=", base64.RawURLEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := PublicKey(key); err == nil || strings.Contains(err.Error(), key) && key != "" && key != "invalid" {
			t.Error("invalid key was accepted or leaked in its error")
		}
	}
}

func TestProfileRoundTrip(t *testing.T) {
	key, _ := PublicKey(testSettings().PrivateKey)
	node := model.Node{Host: "2001:db8::1", Port: 443, ServerName: "www.example.com", PublicKey: key, ShortID: "AABB"}
	name := "Мой телефон #1 & VPN"
	profile, err := Profile(node, strings.ToUpper(testID), name)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(profile)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "vless" || u.Hostname() != node.Host || u.Port() != "443" || u.User.Username() != testID || u.Fragment != name {
		t.Fatalf("profile did not round trip: %s", profile)
	}
	q := u.Query()
	for field, want := range map[string]string{"pbk": key, "sni": node.ServerName, "sid": "aabb", "spx": "/", "flow": "xtls-rprx-vision", "fp": "chrome", "type": "tcp", "security": "reality", "encryption": "none"} {
		if q.Get(field) != want {
			t.Errorf("%s = %q, want %q", field, q.Get(field), want)
		}
	}
	node.Host = "https://example.com"
	if _, err := Profile(node, testID, name); err == nil {
		t.Error("accepted URL as host")
	}
}

func TestClientProfilesAgreeOnConnection(t *testing.T) {
	key, _ := PublicKey(testSettings().PrivateKey)
	node := model.Node{Host: "gateway.example", Port: 443, ServerName: "www.cloudflare.com", PublicKey: key, ShortID: "AABB"}
	formats, err := ClientProfiles(node, testID, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(formats.URI)
	if err != nil {
		t.Fatal(err)
	}
	if link.Scheme != "vless" || link.Hostname() != node.Host || link.Port() != "443" || link.Query().Get("security") != "reality" {
		t.Fatal("VLESS share URI is not importable")
	}
	var singBox struct {
		Outbounds []struct {
			Type       string `json:"type"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			UUID       string `json:"uuid"`
			Flow       string `json:"flow"`
			TLS        struct {
				ServerName string `json:"server_name"`
				Reality    struct {
					PublicKey string `json:"public_key"`
					ShortID   string `json:"short_id"`
				} `json:"reality"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(formats.SingBoxJSON), &singBox); err != nil {
		t.Fatal(err)
	}
	if len(singBox.Outbounds) != 1 {
		t.Fatal("sing-box config needs one outbound")
	}
	out := singBox.Outbounds[0]
	if out.Type != "vless" || out.Server != link.Hostname() || out.ServerPort != 443 ||
		out.UUID != link.User.Username() || out.Flow != link.Query().Get("flow") ||
		out.TLS.ServerName != link.Query().Get("sni") ||
		out.TLS.Reality.PublicKey != link.Query().Get("pbk") ||
		out.TLS.Reality.ShortID != link.Query().Get("sid") {
		t.Fatal("sing-box config differs from VLESS share URI")
	}
	for _, value := range []string{node.Host, testID, node.ServerName, key, "aabb"} {
		if !strings.Contains(formats.MihomoYAML, value) {
			t.Fatal("Mihomo config is missing a connection parameter")
		}
	}
	if _, err := ClientProfiles(model.Node{Host: "bad host", Port: 443}, testID, "Phone"); err == nil {
		t.Fatal("invalid connection must not be exported in any format")
	}
}

func TestRenderZeroClientsAndDuplicateCredentials(t *testing.T) {
	config, err := Render(testSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Inbounds []struct {
			Settings struct {
				Clients []json.RawMessage `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(config, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Inbounds) != 1 || result.Inbounds[0].Settings.Clients == nil || len(result.Inbounds[0].Settings.Clients) != 0 {
		t.Fatalf("empty clients must be a JSON array: %s", config)
	}
	if _, err := Render(testSettings(), []Client{{ID: testID}, {ID: strings.ToUpper(testID)}}); err == nil {
		t.Error("accepted duplicate UUID credentials")
	}
}

func TestInvalidSettings(t *testing.T) {
	for name, change := range map[string]func(*Settings){
		"port":         func(s *Settings) { s.Port = 65536 },
		"listen":       func(s *Settings) { s.Listen = "example.com" },
		"sni":          func(s *Settings) { s.ServerName = "127.0.0.1" },
		"target":       func(s *Settings) { s.Target = "example.com" },
		"target port":  func(s *Settings) { s.Target = "example.com:0" },
		"short id":     func(s *Settings) { s.ShortID = "abc" },
		"short id hex": func(s *Settings) { s.ShortID = "zz" },
	} {
		t.Run(name, func(t *testing.T) {
			s := testSettings()
			change(&s)
			if _, err := Render(s, nil); err == nil {
				t.Error("accepted invalid settings")
			}
		})
	}
}
