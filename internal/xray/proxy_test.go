package xray

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const testProxyID = "e29c3a5b-978d-4c64-91e5-09b75e63cc61"

func TestRenderProxyRoutesBothTransportsAndFailsClosed(t *testing.T) {
	settings := testSettings()
	settings.StatsPort = 10085
	settings.XHTTP = &XHTTPSettings{
		Port: 10444, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		ShortID: "1122334455667788", Path: "/proxy_test_xhttp_0123456789",
	}
	// Второй ключ REALITY должен отличаться от первого.
	settings.XHTTP.PrivateKey = base64.RawURLEncoding.EncodeToString([]byte{
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	})
	settings.Proxy = &ProxySettings{Address: "127.0.0.1", Port: 1080, Username: "sample-user", Password: "sample-pass"}
	data, err := Render(settings, []Client{{ID: testID, ProxyID: testProxyID}})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Log struct {
			Level string `json:"loglevel"`
		} `json:"log"`
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
				User    string `json:"user"`
				Pass    string `json:"pass"`
			} `json:"settings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag []string `json:"inboundTag"`
				Users      []string `json:"user"`
				Outbound   string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Log.Level != "none" || len(config.Outbounds) != 3 ||
		config.Outbounds[0].Protocol != "blackhole" || config.Outbounds[1].Tag != "direct" ||
		config.Outbounds[2].Tag != "proxy" || config.Outbounds[2].Protocol != "socks" ||
		config.Outbounds[2].Settings.Address != "127.0.0.1" ||
		config.Outbounds[2].Settings.Port != 1080 ||
		config.Outbounds[2].Settings.User != "sample-user" ||
		config.Outbounds[2].Settings.Pass != "sample-pass" {
		t.Fatal("proxy configuration is missing or default traffic is not blocked")
	}
	if len(config.Inbounds) != 3 || len(config.Inbounds[0].Settings.Clients) != 2 ||
		len(config.Inbounds[1].Settings.Clients) != 2 || len(config.Routing.Rules) != 3 ||
		len(config.Routing.Rules[0].InboundTag) != 1 ||
		len(config.Routing.Rules[1].Users) != 1 ||
		config.Routing.Rules[1].Users[0] != testID+"@private-gateway" ||
		config.Routing.Rules[1].Outbound != "direct" ||
		len(config.Routing.Rules[2].Users) != 1 ||
		config.Routing.Rules[2].Users[0] != testProxyID+"@private-gateway" ||
		config.Routing.Rules[2].Outbound != "proxy" {
		t.Fatal("TCP/XHTTP users are not routed to separate exits")
	}
	if binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY"); binary != "" {
		path := filepath.Join(t.TempDir(), "proxy.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command(binary, "run", "-test", "-config", path).Run(); err != nil {
			t.Fatal("real Xray rejected the dual-exit configuration")
		}
	}
}

func TestRenderProxyRejectsMissingCredentialAndInvalidAuthentication(t *testing.T) {
	settings := testSettings()
	settings.Proxy = &ProxySettings{Address: "127.0.0.1", Port: 1080, Username: "sample-user", Password: "sample-pass"}
	if _, err := Render(settings, []Client{{ID: testID}}); err == nil {
		t.Fatal("proxy exit accepted a missing proxy VLESS credential")
	}
	if _, err := Render(settings, []Client{{ID: testID, ProxyID: testID}}); err == nil {
		t.Fatal("proxy exit accepted a reused direct credential")
	}
	settings.Proxy.Password = ""
	if _, err := Render(settings, nil); err == nil {
		t.Fatal("proxy exit accepted incomplete SOCKS5 authentication")
	}
}
