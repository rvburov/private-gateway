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

// TestRealXrayTrafficAndRevocation доказывает, что конфигурация сервера передаёт трафик и
// что удаление секрета действует на работающий Xray. TLS-сервер маскировки REALITY и
// HTTP-сайт работают на loopback.
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
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateBytes), Listen: "127.0.0.1", Port: serverPort, StatsPort: unusedPort(t),
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
	statsAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.StatsPort))
	deadline := time.Now().Add(4 * time.Second)
	for {
		traffic, queryErr := QueryTraffic(context.Background(), binary, statsAddress)
		if queryErr == nil && traffic[testID].UplinkBytes > 0 && traffic[testID].DownlinkBytes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Xray did not report per-device traffic: %v (%#v)", queryErr, traffic)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Переход на новую учётную запись должен сразу не дать старому устройству установить
	// новый сеанс REALITY после перезапуска сервера.
	server.stop(t)
	newID := "372d849d-1985-48a6-bcf7-3a0828f10f40"
	writeServer([]Client{{ID: newID}})
	_ = startXrayProcess(t, binary, serverPath, serverPort)
	newProxy := startRealXrayClient(t, binary, serverPort, key, settings, newID)
	assertProxyResponse(t, newProxy, origin)
	// Сначала доказать, что перезапущенный сервер передаёт трафик, и лишь потом считать сбой
	// следствием отзыва: один неудачный старый запрос мог бы значить, что сервер не работает.
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
		ErrorLog:  log.New(io.Discard, "", 0), // пробы REALITY без ключа намеренно проваливают рукопожатие TLS
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

// Runner CI с детектором гонок может несколько секунд запускать процессы Xray и устанавливать
// первое соединение REALITY после того, как открылся TCP-порт. Настоящая проверка HTTP
// остаётся, но с запасом времени на этот запуск.
const realTrafficTimeout = 10 * time.Second

func fetchViaSOCKS(proxy, origin string) (string, error) {
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialSOCKS5(ctx, proxy, address)
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

