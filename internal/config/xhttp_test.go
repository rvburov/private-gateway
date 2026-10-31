package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/rvburov/private-gateway/internal/xray"
)

func prepareXHTTPEnvironment(t *testing.T) map[string]string {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	values := map[string]string{
		"GATEWAY_DATABASE_URL":        "postgres://localhost/gateway",
		"GATEWAY_API_TOKEN":           strings.Repeat("t", 32),
		"GATEWAY_MASTER_KEY":          base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"GATEWAY_REALITY_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"GATEWAY_SHORT_ID":            "0011223344556677",
		"GATEWAY_HOST":                "192.0.2.10",
		"GATEWAY_SERVER_NAME":         "example.com",
		"GATEWAY_REALITY_TARGET":      "example.org:443",
		"GATEWAY_LISTEN":              "127.0.0.1:8080",
		"GATEWAY_PORT":                "8443",
		"GATEWAY_XRAY_PORT":           "443",
		"GATEWAY_XRAY_STATS_PORT":     "10085",
		"GATEWAY_XHTTP_ENABLED":       "0",
		"GATEWAY_XHTTP_PORT":          "",
		"GATEWAY_XHTTP_PRIVATE_KEY":   base64.RawURLEncoding.EncodeToString(key),
		"GATEWAY_XHTTP_SHORT_ID":      "8899AABBCCDDEEFF",
		"GATEWAY_XHTTP_PATH":          "/test_path_0123456789",
		"GATEWAY_XRAY_RESTART":        "",
		"GATEWAY_ADMIN_USERNAME":      "",
		"GATEWAY_ADMIN_PASSWORD_HASH": "",
		"GATEWAY_ADMIN_TOTP_SECRET":   "",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	return values
}

func TestXHTTPConfigurationIsExplicitAndAdditive(t *testing.T) {
	values := prepareXHTTPEnvironment(t)
	legacy, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Node.XHTTP != nil || legacy.Xray.Settings.XHTTP != nil {
		t.Fatal("disabled XHTTP changed the legacy configuration")
	}
	t.Setenv("GATEWAY_XHTTP_ENABLED", "1")
	configured, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	public, private := configured.Node.XHTTP, configured.Xray.Settings.XHTTP
	if public == nil || private == nil || public.Port != 8444 || private.Port != 8444 ||
		public.Path != values["GATEWAY_XHTTP_PATH"] || private.Path != public.Path ||
		public.ShortID != "8899aabbccddeeff" || private.ShortID != public.ShortID || public.Mode != "auto" {
		t.Fatal("XHTTP metadata and server configuration differ")
	}
	key, err := xray.PublicKey(private.PrivateKey)
	if err != nil || public.PublicKey != key || public.PublicKey == configured.Node.PublicKey {
		t.Fatal("XHTTP does not have an independent matching REALITY key")
	}
	primary := configured.Node
	primary.XHTTP = nil
	primary.ConfigHash = legacy.Node.ConfigHash
	if primary != legacy.Node || configured.Node.ConfigHash == legacy.Node.ConfigHash ||
		configured.Xray.Settings.Port != legacy.Xray.Settings.Port {
		t.Fatal("XHTTP changed primary parameters or failed to change the configuration hash")
	}
	t.Setenv("GATEWAY_XHTTP_PORT", "9444")
	custom, err := Load()
	if err != nil || custom.Node.XHTTP.Port != 9444 || custom.Xray.Settings.XHTTP.Port != 9444 ||
		custom.Node.ConfigHash == configured.Node.ConfigHash {
		t.Fatal("custom XHTTP port was not applied independently")
	}
	t.Setenv("GATEWAY_XHTTP_ENABLED", "0")
	t.Setenv("GATEWAY_XHTTP_PORT", "invalid-unused-port")
	disabled, err := Load()
	if err != nil || disabled.Node != legacy.Node || disabled.Xray.Settings.XHTTP != nil {
		t.Fatal("disabled XHTTP did not preserve the legacy configuration")
	}
}

func TestXHTTPConfigurationRejectsConflictsAndIncompleteSecrets(t *testing.T) {
	values := prepareXHTTPEnvironment(t)
	t.Setenv("GATEWAY_XHTTP_ENABLED", "1")
	for _, port := range []string{"0", "-1", "65536", "invalid-port", "80", "443", "8443", "10085", "8080"} {
		t.Run("port "+port, func(t *testing.T) {
			t.Setenv("GATEWAY_XHTTP_PORT", port)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GATEWAY_XHTTP_PORT") {
				t.Fatal("accepted an invalid or conflicting XHTTP port")
			}
		})
	}
	for name, value := range map[string]string{
		"GATEWAY_PORT": "8444", "GATEWAY_XRAY_PORT": "8444", "GATEWAY_XRAY_STATS_PORT": "8444",
	} {
		t.Run("custom collision "+name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(); err == nil {
				t.Fatal("accepted a collision with a custom service port")
			}
		})
	}
	for name, invalid := range map[string][]string{
		"GATEWAY_XHTTP_ENABLED":     {"true", "yes", "2"},
		"GATEWAY_XHTTP_PRIVATE_KEY": {"", "invalid-test-key", values["GATEWAY_REALITY_PRIVATE_KEY"]},
		"GATEWAY_XHTTP_SHORT_ID":    {"", "invalid-short-id", values["GATEWAY_SHORT_ID"]},
		"GATEWAY_XHTTP_PATH":        {"", "/short", "/test_path_0123456789?query"},
	} {
		for _, value := range invalid {
			t.Run(name, func(t *testing.T) {
				t.Setenv(name, value)
				_, err := Load()
				if err == nil {
					t.Fatal("accepted incomplete or invalid XHTTP settings")
				}
				if value != "" && len(value) > 2 && strings.Contains(err.Error(), value) {
					t.Fatal("configuration error exposed an input value")
				}
			})
		}
	}
}
