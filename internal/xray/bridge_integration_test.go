package xray

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Обычные клиенты TCP/Vision и XHTTP идут через прозрачные TCP listeners.
// Мост не получает UUID/ключи и не требует специальных outbound на устройстве.
func TestRealXrayTCPAndXHTTPThroughBridge(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real VLESS/REALITY traffic checks")
	}
	settings := xhttpTestSettings()
	settings.Listen = "127.0.0.1"
	settings.Target = startTLS13Target(t, "example.com")
	settings.Port, settings.StatsPort, settings.XHTTP.Port = unusedPort(t), unusedPort(t), unusedPort(t)
	data, err := Render(settings, []Client{{ID: xhttpTestUUID}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "exit.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = startXrayProcess(t, binary, path, settings.Port)
	tcpBridge := startRawBridge(t, settings.Port)
	xhttpBridge := startRawBridge(t, settings.XHTTP.Port)
	node := xhttpTestNode(t)
	node.Host = "127.0.0.1"
	node.Port = tcpBridge.port
	node.XHTTP.Port = xhttpBridge.port
	profile, err := XHTTPProfile(node, xhttpTestUUID, "Bridge")
	if err != nil {
		t.Fatal(err)
	}
	tcpClient := startRealXrayClient(t, binary, tcpBridge.port, node.PublicKey, settings, xhttpTestUUID)
	xhttpClient := startXHTTPClientFromURI(t, binary, profile)
	origin := startHTTPOrigin(t)
	assertProxyResponse(t, tcpClient, origin)
	assertProxyResponse(t, xhttpClient, origin)
	xhttpBridge.close()
	if _, err := fetchViaSOCKS(xhttpClient, origin); err == nil {
		t.Fatal("XHTTP silently bypassed stopped bridge")
	}
	assertProxyResponse(t, tcpClient, origin)
	tcpBridge.close()
	if _, err := fetchViaSOCKS(tcpClient, origin); err == nil {
		t.Fatal("TCP silently bypassed stopped bridge")
	}
}

type rawBridge struct {
	listener    net.Listener
	port        int
	mutex       sync.Mutex
	closed      bool
	connections map[net.Conn]bool
}

func startRawBridge(t *testing.T, targetPort int) *rawBridge {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bridge := &rawBridge{listener: listener, port: listener.Addr().(*net.TCPAddr).Port, connections: map[net.Conn]bool{}}
	t.Cleanup(bridge.close)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)), time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				bridge.mutex.Lock()
				if bridge.closed {
					bridge.mutex.Unlock()
					return
				}
				bridge.connections[connection] = true
				bridge.connections[upstream] = true
				bridge.mutex.Unlock()
				defer func() {
					bridge.mutex.Lock()
					delete(bridge.connections, connection)
					delete(bridge.connections, upstream)
					bridge.mutex.Unlock()
				}()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, connection); _ = upstream.Close(); close(done) }()
				_, _ = io.Copy(connection, upstream)
				_ = connection.Close()
				<-done
			}()
		}
	}()
	return bridge
}

func (b *rawBridge) close() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.closed = true
	_ = b.listener.Close()
	for connection := range b.connections {
		_ = connection.Close()
	}
}
