package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvburov/private-gateway/internal/xray"
)

func TestServerChecksPostgreSQL(t *testing.T) {
	store, _, node, _ := integrationStore(t)
	state := t.TempDir()
	configPath := filepath.Join(state, "xray.json")
	config := []byte(`{"inbounds":[]}`)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf(`{"mtime_ns":%d,"sha256":"%x","running":true,"error":""}`, info.ModTime().UnixNano(), sha256.Sum256(config))
	if err = os.WriteFile(filepath.Join(state, "xray.status.json"), []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	reality := startCheckedTLS(t, roots, "reality.example", tls.VersionTLS13, time.Now().Add(90*24*time.Hour))
	panel := startCheckedTLS(t, roots, "panel.example", tls.VersionTLS12, time.Now().Add(10*24*time.Hour))
	// Клиент, который сразу завершается: проверка трафика должна сообщить об этом, а не зависнуть
	// или пройти.
	failingXray := filepath.Join(t.TempDir(), "xray")
	if err = os.WriteFile(failingXray, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	const token = "server-checks-token-with-at-least-32-characters"
	handler := New(store, &testXray{}, &testBrowser{}, Options{Token: token, XrayConfigured: true, Checks: ServerChecks{
		XrayBinary: failingXray, XrayConfigPath: configPath, XrayServer: "127.0.0.1:9",
		RealityTarget: reality, RealityServerName: "reality.example",
		PanelAddress: panel, PanelDomain: "panel.example",
		TrafficURL: "https://203.0.113.1/cdn-cgi/trace", RootCAs: roots,
	}})
	run := func(auth string) (int, map[string]checkResult, []byte) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/checks", nil)
		request.RemoteAddr = "127.0.0.1:40000" // токен CLI принимается только с loopback
		if auth != "" {
			request.Header.Set("Authorization", "Bearer "+auth)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		data := recorder.Body.Bytes()
		var body struct {
			Checks []checkResult `json:"checks"`
		}
		results := map[string]checkResult{}
		if recorder.Code == 200 {
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			for _, result := range body.Checks {
				results[result.ID] = result
			}
		}
		return recorder.Code, results, data
	}
	expect := func(results map[string]checkResult, want map[string]string) {
		t.Helper()
		for id, status := range want {
			if results[id].Status != status || results[id].Detail == "" || results[id].Title == "" {
				t.Errorf("check %s = %+v, want status %s", id, results[id], status)
			}
		}
		if len(results) != len(want) {
			t.Errorf("got %d checks, want %d", len(results), len(want))
		}
	}

	if code, _, _ := run(""); code != 401 {
		t.Fatalf("checks without a session answered %d", code)
	}
	// Новый узел ещё ни разу не применялся; применяем его, как это делает панель.
	if err = store.Configure(context.Background(), true, func(context.Context, []xray.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, results, _ := run(token)
	expect(results, map[string]string{
		"database": "ok", "configuration": "ok", "xray": "ok", "reality": "ok", "certificate": "warn", "traffic": "skip",
	})

	device, err := store.Create(context.Background(), "Phone", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Export(context.Background(), device.ID)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(profile.URI)
	if err != nil {
		t.Fatal(err)
	}
	// Устройство ещё не применено, а его секрета в ответе нет.
	_, results, data := run(token)
	expect(results, map[string]string{
		"database": "ok", "configuration": "warn", "xray": "ok", "reality": "ok", "certificate": "warn", "traffic": "fail",
	})
	if credential := uri.User.Username(); credential == "" || bytes.Contains(bytes.ToLower(data), []byte(strings.ToLower(credential))) {
		t.Fatal("the checks expose the device credential")
	}

	// Конфигурация, которую Xray ещё не загрузил, и сайт маскировки с TLS 1.2 видны в ответе.
	if err = os.WriteFile(configPath, []byte(`{"inbounds":[{}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler = New(store, &testXray{}, &testBrowser{}, Options{Token: token, Checks: ServerChecks{
		XrayConfigPath: configPath, RealityTarget: panel, RealityServerName: "panel.example", RootCAs: roots,
	}})
	_, results, _ = run(token)
	expect(results, map[string]string{
		"database": "ok", "configuration": "skip", "xray": "warn", "reality": "fail", "certificate": "skip", "traffic": "skip",
	})
}

// Браузерная часть проверки обращается к этим сервисам и больше никуда за пределы панели.
func TestPanelPolicyAllowsOnlyTheCheckServices(t *testing.T) {
	recorder := httptest.NewRecorder()
	New(nil, nil, nil, Options{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "connect-src 'self' https://ipleak.net https://*.ipleak.net https://speed.cloudflare.com;") ||
		!strings.Contains(policy, "default-src 'self';") {
		t.Fatalf("Content-Security-Policy = %q", policy)
	}
	for _, broader := range []string{"connect-src *", "https://*.cloudflare.com", "https:;", "https://cloudflare.com"} {
		if strings.Contains(policy, broader) {
			t.Fatalf("Content-Security-Policy permits unrelated destinations: %q", policy)
		}
	}
}

func TestServerAddressComparesTheProfileHost(t *testing.T) {
	ctx := context.Background()
	if !serverAddress(ctx, "203.0.113.7", "203.0.113.7") || serverAddress(ctx, "203.0.113.7", "198.51.100.1") {
		t.Fatal("an IP address host must match only itself")
	}
	if !serverAddress(ctx, "localhost", "127.0.0.1") {
		t.Fatal("a domain must match the addresses it resolves to")
	}
	if serverAddress(ctx, "localhost", "") || serverAddress(ctx, "203.0.113.7", "not an address") {
		t.Fatal("a missing address must not match")
	}
}

// startCheckedTLS отдаёт самоподписанный сертификат для name, которому доверяют через roots, с
// версией TLS не выше заданной.
func startCheckedTLS(t *testing.T, roots *x509.CertPool, name string, maxVersion uint16, notAfter time.Time) string {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(certificate)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}},
			MaxVersion:   maxVersion,
		},
		Handler:  http.NotFoundHandler(),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}
