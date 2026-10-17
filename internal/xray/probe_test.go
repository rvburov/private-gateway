package xray

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvburov/private-gateway/internal/model"
)

// TestTrafficProbeThroughRealXray runs the panel's server check against a real Xray gateway: a
// known device reaches an HTTPS site through it, an unknown one does not, and the temporary
// client configuration with the credential is removed every time.
func TestTrafficProbeThroughRealXray(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real VLESS/REALITY traffic checks")
	}
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "fl=1\nip=203.0.113.7\nloc=NL\n")
	}))
	defer site.Close()
	roots := x509.NewCertPool()
	roots.AddCert(site.Certificate())

	serverPort := unusedPort(t)
	privateBytes := make([]byte, 32)
	if _, err := rand.Read(privateBytes); err != nil {
		t.Fatal(err)
	}
	settings := Settings{
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateBytes), Listen: "127.0.0.1", Port: serverPort,
		ServerName: "reality.example", Target: startTLS13Target(t), ShortID: "0123456789abcdef",
	}
	key, err := PublicKey(settings.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Render(settings, []Client{{ID: testID}})
	if err != nil {
		t.Fatal(err)
	}
	serverPath := filepath.Join(t.TempDir(), "server.json")
	if err = os.WriteFile(serverPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = startXrayProcess(t, binary, serverPath, serverPort)

	probe := TrafficProbe{
		Binary: binary, Server: net.JoinHostPort("127.0.0.1", strconv.Itoa(serverPort)),
		Node:       model.Node{Host: "127.0.0.1", Port: serverPort, ServerName: settings.ServerName, PublicKey: key, ShortID: settings.ShortID},
		Credential: testID, URL: site.URL + "/cdn-cgi/trace", RootCAs: roots,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	body, err := probe.Run(ctx)
	if err != nil || !strings.Contains(body, "ip=203.0.113.7") {
		t.Fatalf("the probe through the gateway returned %q, %v", body, err)
	}
	probe.Credential = "372d849d-1985-48a6-bcf7-3a0828f10f40"
	if body, err = probe.Run(ctx); err == nil {
		t.Fatalf("a device unknown to Xray carried traffic: %q", body)
	}
	probe.Binary = filepath.Join(temporary, "missing-xray")
	if _, err = probe.Run(ctx); !errors.Is(err, ErrProbeStart) {
		t.Fatalf("a missing Xray client gave %v", err)
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
		t.Fatalf("the probe left its client configuration behind: %v, %v", entries, err)
	}
}
