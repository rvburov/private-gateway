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
	t.Setenv("GATEWAY_PORT", "443")
	t.Setenv("GATEWAY_FALLBACK_PORT", "0")
	t.Setenv("GATEWAY_XRAY_RESTART", `["systemctl","--user","restart","xray"]`)
	good, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Node.ConfigHash) != 64 {
		t.Fatal("missing settings hash")
	}
	t.Setenv("GATEWAY_FALLBACK_PORT", "8443")
	fallback, err := Load()
	if err != nil || fallback.FallbackPort != 8443 ||
		fallback.Node.ConfigHash != good.Node.ConfigHash || fallback.Xray.Settings.Port != 443 {
		t.Fatalf("fallback must not change Xray settings: %v", err)
	}
	for _, port := range []string{"-1", "65536", "443", "bad"} {
		t.Setenv("GATEWAY_FALLBACK_PORT", port)
		if _, err := Load(); err == nil {
			t.Fatalf("invalid fallback port accepted: %s", port)
		}
	}
	t.Setenv("GATEWAY_FALLBACK_PORT", "0")
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
