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
	t.Setenv("GATEWAY_XRAY_RESTART", `["systemctl","--user","restart","xray"]`)
	good, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Node.ConfigHash) != 64 {
		t.Fatal("missing settings hash")
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "localhost:8080", "127.0.0.1:0"} {
		t.Setenv("GATEWAY_LISTEN", address)
		if _, err := Load(); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
	t.Setenv("GATEWAY_LISTEN", "127.0.0.1:8080")
	t.Setenv("GATEWAY_MASTER_KEY", "bad")
	if _, err := Load(); err == nil {
		t.Fatal("invalid master key accepted")
	}
}
