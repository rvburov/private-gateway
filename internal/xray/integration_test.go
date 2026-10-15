package xray

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rvburov/private-gateway/internal/model"
)

// TestRealXrayTrafficAndRevocation proves that server configuration carries
// traffic and that removal of a credential affects a running data plane.
// The REALITY camouflage TLS server and HTTP origin both run on loopback.
func TestRealXrayTrafficAndRevocation(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real VLESS/REALITY traffic checks")
	}

	target := startTLS13Target(t)
	origin := startHTTPOrigin(t)
	serverPort := unusedPort(t)
	privateBytes := make([]byte, 32)
	if _, err := rand.Read(privateBytes); err != nil {
		t.Fatal(err)
	}
	settings := Settings{
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateBytes), Listen: "127.0.0.1", Port: serverPort,
		ServerName: "reality.example", Target: target, ShortID: "0123456789abcdef",
	}
	key, err := PublicKey(settings.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverPath := filepath.Join(t.TempDir(), "server.json")
	writeServer := func(clients []Client) {
		data, err := Render(settings, clients)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(serverPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeServer([]Client{{ID: testID}})
	server := startXrayProcess(t, binary, serverPath, serverPort)
	oldProxy := startRealXrayClient(t, binary, serverPort, key, settings, testID)
	assertProxyResponse(t, oldProxy, origin)

	// Switching to a new account must immediately prevent the old device from
	// establishing a new REALITY session after the server has restarted.
	server.stop(t)
	newID := "372d849d-1985-48a6-bcf7-3a0828f10f40"
	writeServer([]Client{{ID: newID}})
	_ = startXrayProcess(t, binary, serverPath, serverPort)
	newProxy := startRealXrayClient(t, binary, serverPort, key, settings, newID)
	assertProxyResponse(t, newProxy, origin)
	// Prove the restarted server carries traffic before attributing failure to
	// revocation. A failed old request alone could only mean the server is down.
	if body, err := fetchViaSOCKS(oldProxy, origin); err == nil {
		t.Fatalf("revoked credential still carried HTTP traffic: %q", body)
	}
}

func startTLS13Target(t *testing.T) string {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certTemplate := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.example"},
		DNSNames: []string{"reality.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &certTemplate, &certTemplate, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}},
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "camouflage") }),
		ErrorLog:  log.New(io.Discard, "", 0), // Unauthenticated REALITY probes intentionally fail their TLS handshake.
	}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

func startHTTPOrigin(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "private-gateway-real-traffic-ok")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return "http://" + listener.Addr().String() + "/traffic"
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

type realXrayProcess struct {
	command *exec.Cmd
	done    chan error
	stopped bool
	output  *limitedOutput
}

func startXrayProcess(t *testing.T, binary, path string, port int) *realXrayProcess {
	t.Helper()
	command := exec.Command(binary, "run", "-config", path)
	output := &limitedOutput{}
	command.Stdout, command.Stderr = output, output
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &realXrayProcess{command: command, done: make(chan error, 1), output: output}
	go func() { process.done <- command.Wait() }()
	t.Cleanup(func() { process.stop(t) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-process.done:
			process.stopped = true
			t.Fatalf("Xray exited during startup: %v", err)
		default:
		}
		connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return process
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Xray did not listen within five seconds")
	return nil
}

func (p *realXrayProcess) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.command.Process.Kill()
		<-p.done
	}
}

func startRealXrayClient(t *testing.T, binary string, serverPort int, key string, settings Settings, credential string) string {
	t.Helper()
	port := unusedPort(t)
	profile, err := Profile(model.Node{
		Host: "127.0.0.1", Port: serverPort, ServerName: settings.ServerName,
		PublicKey: key, ShortID: settings.ShortID,
	}, credential, "integration")
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(profile)
	if err != nil {
		t.Fatal(err)
	}
	profileParams := uri.Query()
	config := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": uri.Hostname(), "port": serverPort,
				"users": []any{map[string]any{"id": uri.User.Username(), "encryption": profileParams.Get("encryption"), "flow": profileParams.Get("flow")}},
			}}},
			"streamSettings": map[string]any{
				"network": profileParams.Get("type"), "security": profileParams.Get("security"),
				"realitySettings": map[string]any{
					"serverName": profileParams.Get("sni"), "fingerprint": profileParams.Get("fp"), "publicKey": profileParams.Get("pbk"),
					"shortId": profileParams.Get("sid"), "spiderX": profileParams.Get("spx"),
				},
			},
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = startXrayProcess(t, binary, path, port)
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func assertProxyResponse(t *testing.T, proxy, origin string) {
	t.Helper()
	body, err := fetchViaSOCKS(proxy, origin)
	if err != nil {
		t.Fatalf("real VLESS/REALITY proxy request failed: %v", err)
	}
	if body != "private-gateway-real-traffic-ok" {
		t.Fatalf("proxy response = %q", body)
	}
}

// The race-enabled CI runner can take several seconds to schedule the Xray
// processes and complete their first REALITY connection after a TCP listener
// appears. Keep the real HTTP assertion while allowing that startup latency.
const realTrafficTimeout = 10 * time.Second

func fetchViaSOCKS(proxy, origin string) (string, error) {
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialSOCKS(ctx, proxy, address)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: realTrafficTimeout}
	response, err := client.Get(origin)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), err
}

func dialSOCKS(ctx context.Context, proxy, address string) (_ net.Conn, err error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = connection.Close()
		}
	}()
	_ = connection.SetDeadline(time.Now().Add(realTrafficTimeout))
	if _, err = connection.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err = io.ReadFull(connection, greeting); err != nil || greeting[0] != 5 || greeting[1] != 0 {
		return nil, errors.New("SOCKS authentication negotiation failed")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, []byte(host)...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = connection.Write(request); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	if _, err = io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	if header[1] != 0 {
		return nil, fmt.Errorf("SOCKS connect failed with code %d", header[1])
	}
	var remainder int
	switch header[3] {
	case 1:
		remainder = 4 + 2
	case 4:
		remainder = 16 + 2
	case 3:
		length := make([]byte, 1)
		if _, err = io.ReadFull(connection, length); err != nil {
			return nil, err
		}
		remainder = int(length[0]) + 2
	default:
		return nil, errors.New("invalid SOCKS reply address")
	}
	if _, err = io.ReadFull(connection, make([]byte, remainder)); err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}
