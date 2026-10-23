package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestConfigurationRequiresSecretsAndLoopback(t *testing.T) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"GATEWAY_DATABASE_URL": "postgres://localhost/gateway", "GATEWAY_API_TOKEN": strings.Repeat("x", 32), "GATEWAY_MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "GATEWAY_REALITY_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(key.Bytes()), "GATEWAY_SHORT_ID": "0011223344556677", "GATEWAY_LISTEN": "127.0.0.1:8080"}
	for name, value := range values {
		t.Setenv(name, value)
	}
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("GATEWAY_XRAY_PORT", "")
	t.Setenv("GATEWAY_IP2LOCATION_API_KEY", "")
	defaults, err := Load()
	if err != nil || defaults.Node.Port != 8443 || defaults.Xray.Settings.Port != 443 || defaults.IP2LocationAPIKey != "" {
		t.Fatalf("invalid default public/internal ports: %v", err)
	}
	t.Setenv("GATEWAY_IP2LOCATION_API_KEY", "private-lookup-test-key")
	withLookup, err := Load()
	if err != nil || withLookup.IP2LocationAPIKey != "private-lookup-test-key" || withLookup.Node != defaults.Node {
		t.Fatal("optional IP lookup key changed connection settings", err)
	}
	t.Setenv("GATEWAY_PORT", "443")
	t.Setenv("GATEWAY_XRAY_RESTART", `["systemctl","--user","restart","xray"]`)
	good, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Node.ConfigHash) != 64 {
		t.Fatal("missing settings hash")
	}
	// Публикуемый порт меняет адрес профиля, но не внутренний listener или хеш Xray.
	t.Setenv("GATEWAY_PORT", "8443")
	public, err := Load()
	if err != nil || public.Node.Port != 8443 || public.Xray.Settings.Port != 443 || public.Node.ConfigHash != good.Node.ConfigHash {
		t.Fatalf("public port changed the internal listener: %+v, %v", public.Node, err)
	}
	t.Setenv("GATEWAY_XRAY_PORT", "9443")
	internal, err := Load()
	if err != nil || internal.Node.Port != 8443 || internal.Xray.Settings.Port != 9443 || internal.Node.ConfigHash == good.Node.ConfigHash {
		t.Fatalf("internal port was not configured independently: %+v, %v", internal.Node, err)
	}
	for _, name := range []string{"GATEWAY_PORT", "GATEWAY_XRAY_PORT"} {
		for _, port := range []string{"0", "-1", "65536", "bad"} {
			t.Setenv(name, port)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("invalid %s accepted: %s (%v)", name, port, err)
			}
		}
		t.Setenv(name, "443")
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "localhost:8080", "127.0.0.1:0"} {
		t.Setenv("GATEWAY_LISTEN", address)
		if _, err := Load(); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
	t.Setenv("GATEWAY_ALLOW_CONTAINER_LISTEN", "1")
	t.Setenv("GATEWAY_LISTEN", "0.0.0.0:8080")
	if _, err := Load(); err != nil {
		t.Fatalf("Docker-private listen rejected: %v", err)
	}
	t.Setenv("GATEWAY_LISTEN", "127.0.0.1:8080")
	t.Setenv("GATEWAY_MASTER_KEY", "bad")
	if _, err := Load(); err == nil {
		t.Fatal("invalid master key accepted")
	}
}
