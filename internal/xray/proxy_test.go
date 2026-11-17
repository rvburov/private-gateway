package xray

import (
	"strings"
	"testing"
)

const testProxyID = "e29c3a5b-978d-4c64-91e5-09b75e63cc61"

func TestLegacyServerProxyExitIsUnavailable(t *testing.T) {
	settings := testSettings()
	settings.Proxy = &ProxySettings{Address: "127.0.0.1", Port: 1080, Username: "sample-user", Password: "sample-pass"}
	clients := []Client{{ID: testID, ProxyID: testProxyID}}
	if _, err := Render(settings, clients); err == nil {
		t.Fatal("legacy server proxy exit was accepted")
	}
	if _, err := RenderTCP(settings, clients); err == nil {
		t.Fatal("legacy server proxy exit was accepted by TCP renderer")
	}
	settings = xhttpTestSettings()
	settings.Proxy = &ProxySettings{Address: "127.0.0.1", Port: 1080}
	if _, err := RenderXHTTP(settings, clients); err == nil {
		t.Fatal("legacy server proxy exit was accepted by XHTTP renderer")
	}
	settings.Proxy = nil
	data, err := Render(settings, clients)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testProxyID) || strings.Contains(string(data), `"protocol": "socks"`) {
		t.Fatal("retired proxy credential or SOCKS outbound survived in server config")
	}
}
