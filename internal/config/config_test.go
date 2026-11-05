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

func TestProxySettingsAreExplicitAndSupportPasswordOrIPAuthentication(t *testing.T) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"GATEWAY_DATABASE_URL":        "postgres://localhost/gateway",
		"GATEWAY_API_TOKEN":           strings.Repeat("x", 32),
		"GATEWAY_MASTER_KEY":          base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"GATEWAY_REALITY_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(key.Bytes()),
		"GATEWAY_SHORT_ID":            "0011223344556677",
		"GATEWAY_PROXY_ENABLED":       "0",
		"GATEWAY_PROXY_HOST":          "",
		"GATEWAY_PROXY_PORT":          "",
		"GATEWAY_PROXY_AUTH":          "",
		"GATEWAY_PROXY_USERNAME_B64":  "",
		"GATEWAY_PROXY_PASSWORD_B64":  "",
	} {
		t.Setenv(name, value)
	}
	direct, err := Load()
	if err != nil || direct.Xray.Settings.Proxy != nil || direct.Node.ProxyAvailable {
		t.Fatal("proxy settings changed the default direct connection")
	}
	t.Setenv("GATEWAY_PROXY_ENABLED", "1")
	t.Setenv("GATEWAY_PROXY_HOST", "198.51.100.10")
	t.Setenv("GATEWAY_PROXY_PORT", "1080")
	t.Setenv("GATEWAY_PROXY_AUTH", "password")
	t.Setenv("GATEWAY_PROXY_USERNAME_B64", base64.StdEncoding.EncodeToString([]byte("sample-user")))
	t.Setenv("GATEWAY_PROXY_PASSWORD_B64", base64.StdEncoding.EncodeToString([]byte("sample-password")))
	withPassword, err := Load()
	if err != nil || withPassword.Xray.Settings.Proxy == nil || !withPassword.Node.ProxyAvailable ||
		withPassword.Node.ConfigHash == direct.Node.ConfigHash ||
		withPassword.Xray.Settings.Proxy.Username != "sample-user" ||
		withPassword.Xray.Settings.Proxy.Password != "sample-password" {
		t.Fatal("valid SOCKS5 password settings were not loaded")
	}
	t.Setenv("GATEWAY_PROXY_PASSWORD_B64", "")
	if _, err = Load(); err == nil {
		t.Fatal("one-sided SOCKS5 credentials were accepted")
	}
	t.Setenv("GATEWAY_PROXY_USERNAME_B64", "")
	t.Setenv("GATEWAY_PROXY_AUTH", "none")
	withoutPassword, err := Load()
	if err != nil || withoutPassword.Xray.Settings.Proxy == nil || !withoutPassword.Node.ProxyAvailable ||
		withoutPassword.Xray.Settings.Proxy.Username != "" || withoutPassword.Xray.Settings.Proxy.Password != "" {
		t.Fatal("IP-authenticated SOCKS5 was not loaded")
	}
	t.Setenv("GATEWAY_PROXY_USERNAME_B64", "invalid-base64")
	if _, err = Load(); err == nil {
		t.Fatal("invalid encoded SOCKS5 credentials were accepted")
	}
}

func expectedIPEnvironment(t *testing.T) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"GATEWAY_DATABASE_URL":        "postgres://localhost/gateway",
		"GATEWAY_API_TOKEN":           strings.Repeat("x", 32),
		"GATEWAY_MASTER_KEY":          base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"GATEWAY_REALITY_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(key.Bytes()),
		"GATEWAY_SHORT_ID":            "0011223344556677",
		"GATEWAY_LISTEN":              "127.0.0.1:8080",
		"GATEWAY_HOST":                "8.8.8.8",
		"GATEWAY_SERVER_EXPECTED_IP":  "",
		"GATEWAY_PROXY_EXPECTED_IP":   "",
		"GATEWAY_PROXY_ENABLED":       "0",
		"GATEWAY_PROXY_HOST":          "proxy.example.com",
		"GATEWAY_PROXY_PORT":          "1080",
		"GATEWAY_PROXY_AUTH":          "none",
		"GATEWAY_PROXY_USERNAME_B64":  "",
		"GATEWAY_PROXY_PASSWORD_B64":  "",
	} {
		t.Setenv(name, value)
	}
}

func TestExpectedExitIPsUsePublicConfigurationWithoutChangingRoutes(t *testing.T) {
	expectedIPEnvironment(t)
	direct, err := Load()
	if err != nil || direct.Node.ExpectedServerIP != "8.8.8.8" || direct.Node.ExpectedProxyIP != "" {
		t.Fatal("public server address was not used as the default expectation", err)
	}
	t.Setenv("GATEWAY_SERVER_EXPECTED_IP", "::ffff:1.1.1.1")
	explicit, err := Load()
	if err != nil || explicit.Node.ExpectedServerIP != "1.1.1.1" || explicit.Node.Host != direct.Node.Host || explicit.Node.ConfigHash != direct.Node.ConfigHash {
		t.Fatal("display-only server expectation changed connection settings", err)
	}
	t.Setenv("GATEWAY_PROXY_ENABLED", "1")
	withoutProxyIP, err := Load()
	if err != nil || withoutProxyIP.Node.ExpectedProxyIP != "" {
		t.Fatal("proxy entry address became an unverified exit expectation", err)
	}
	t.Setenv("GATEWAY_PROXY_EXPECTED_IP", "2606:4700:4700:0:0:0:0:1111")
	withProxyIP, err := Load()
	if err != nil || withProxyIP.Node.ExpectedProxyIP != "2606:4700:4700::1111" ||
		withProxyIP.Xray.Settings.Proxy.Address != "proxy.example.com" ||
		withProxyIP.Node.ConfigHash != withoutProxyIP.Node.ConfigHash {
		t.Fatal("display-only proxy expectation changed connection settings", err)
	}
	t.Setenv("GATEWAY_PROXY_ENABLED", "0")
	t.Setenv("GATEWAY_PROXY_EXPECTED_IP", "not-an-address")
	disabled, err := Load()
	if err != nil || disabled.Node.ExpectedProxyIP != "" || disabled.Node.ConfigHash != direct.Node.ConfigHash {
		t.Fatal("disabled proxy metadata affected the direct route", err)
	}
}

func TestExpectedServerIPIsUnknownForDomainOrNonPublicHost(t *testing.T) {
	expectedIPEnvironment(t)
	for _, host := range []string{"vpn.example.com", "localhost", "127.0.0.1", "192.168.1.2", "203.0.113.4", "2001:db8::1"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("GATEWAY_HOST", host)
			loaded, err := Load()
			if err != nil || loaded.Node.ExpectedServerIP != "" {
				t.Fatalf("non-public entry host inferred an exit IP: %q, %v", loaded.Node.ExpectedServerIP, err)
			}
		})
	}
}

func TestExpectedExitIPsRejectInvalidAndNonPublicValues(t *testing.T) {
	expectedIPEnvironment(t)
	t.Setenv("GATEWAY_PROXY_ENABLED", "1")
	for _, name := range []string{"GATEWAY_SERVER_EXPECTED_IP", "GATEWAY_PROXY_EXPECTED_IP"} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"not-an-address", "https://1.1.1.1", "1.1.1.1:443", "127.0.0.1", "192.168.1.1", "100.64.0.1", "203.0.113.1", "224.0.0.1", "::1", "fe80::1%eth0", "::ffff:127.0.0.1", "2001:db8::1", "2002::1"} {
				t.Setenv(name, value)
				if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), value) {
					t.Fatalf("invalid expected IP was not rejected safely for %s: %v", name, err)
				}
			}
			t.Setenv(name, "")
		})
	}
}
