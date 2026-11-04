package xray

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestRealXrayTwoExits проверяет не только JSON-маршруты: два разных VLESS UUID
// проходят через настоящий Xray, а остановка SOCKS5 не переводит прокси-профиль
// на прямой выход. Локальный SOCKS5 fixture не получает настоящие секреты.
func TestRealXrayTwoExits(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real VLESS/REALITY traffic checks")
	}
	proxy := startSOCKS5Fixture(t, "sample-user", "sample-pass")
	target := startTLS13Target(t)
	origin := startHTTPOrigin(t)
	serverPort := unusedPort(t)
	private := make([]byte, 32)
	if _, err := rand.Read(private); err != nil {
		t.Fatal(err)
	}
	settings := Settings{
		PrivateKey: base64.RawURLEncoding.EncodeToString(private), Listen: "127.0.0.1",
		Port: serverPort, ServerName: "reality.example", Target: target, ShortID: "0123456789abcdef",
		Proxy: &ProxySettings{Address: "127.0.0.1", Port: proxy.port, Username: "sample-user", Password: "sample-pass"},
	}
	key, err := PublicKey(settings.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Render(settings, []Client{{ID: testID, ProxyID: testProxyID}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = startXrayProcess(t, binary, path, serverPort)
	directClient := startRealXrayClient(t, binary, serverPort, key, settings, testID, true)
	proxyClient := startRealXrayClient(t, binary, serverPort, key, settings, testProxyID, true)
	assertProxyResponse(t, directClient, origin)
	if proxy.requests.Load() != 0 {
		t.Fatal("direct profile unexpectedly used SOCKS5")
	}
	assertProxyResponse(t, proxyClient, origin)
	if proxy.requests.Load() == 0 {
		t.Fatal("proxy profile did not use SOCKS5")
	}
	udpOrigin := startUDPEcho(t)
	if response, err := exchangeViaSOCKSUDP(directClient, udpOrigin, []byte("direct-udp")); err != nil ||
		string(response) != "direct-udp" || proxy.udpRequests.Load() != 0 {
		t.Fatal("direct UDP traffic failed or used the SOCKS5 proxy")
	}
	if response, err := exchangeViaSOCKSUDP(proxyClient, udpOrigin, []byte("proxy-udp")); err != nil ||
		string(response) != "proxy-udp" || proxy.udpRequests.Load() == 0 {
		t.Fatalf("proxy UDP traffic did not return through SOCKS5: %v", err)
	}
	proxy.close()
	if body, err := fetchViaSOCKS(proxyClient, origin); err == nil {
		t.Fatalf("proxy profile fell back to direct after SOCKS5 stopped: %q", body)
	}
	assertProxyResponse(t, directClient, origin)
	if _, err := exchangeViaSOCKSUDP(proxyClient, udpOrigin, []byte("closed-proxy")); err == nil {
		t.Fatal("proxy UDP traffic fell back to direct after SOCKS5 stopped")
	}
	if response, err := exchangeViaSOCKSUDP(directClient, udpOrigin, []byte("direct-still-works")); err != nil ||
		string(response) != "direct-still-works" {
		t.Fatal("direct UDP traffic stopped when SOCKS5 stopped")
	}
}

type socks5Fixture struct {
	listener net.Listener
	port     int
	requests atomic.Int64
	udpRequests atomic.Int64
	closed   atomic.Bool
}

func startSOCKS5Fixture(t *testing.T, username, password string) *socks5Fixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &socks5Fixture{listener: listener, port: listener.Addr().(*net.TCPAddr).Port}
	t.Cleanup(fixture.close)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go fixture.handle(connection, username, password)
		}
	}()
	return fixture
}

func (fixture *socks5Fixture) close() {
	fixture.closed.Store(true)
	_ = fixture.listener.Close()
}

func (fixture *socks5Fixture) handle(connection net.Conn, username, password string) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(connection, greeting[:]); err != nil || greeting[0] != 5 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return
	}
	found := false
	for _, method := range methods {
		found = found || method == 2
	}
	if !found {
		_, _ = connection.Write([]byte{5, 255})
		return
	}
	if _, err := connection.Write([]byte{5, 2}); err != nil {
		return
	}
	var auth [2]byte
	if _, err := io.ReadFull(connection, auth[:]); err != nil || auth[0] != 1 {
		return
	}
	user := make([]byte, int(auth[1]))
	if _, err := io.ReadFull(connection, user); err != nil {
		return
	}
	var length [1]byte
	if _, err := io.ReadFull(connection, length[:]); err != nil {
		return
	}
	pass := make([]byte, int(length[0]))
	if _, err := io.ReadFull(connection, pass); err != nil || string(user) != username || string(pass) != password {
		_, _ = connection.Write([]byte{1, 1})
		return
	}
	if _, err := connection.Write([]byte{1, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(connection, request[:]); err != nil || request[0] != 5 ||
		(request[1] != 1 && request[1] != 3) {
		return
	}
	var host string
	switch request[3] {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(connection, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 3:
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(connection, name); err != nil {
			return
		}
		host = string(name)
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(connection, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(connection, port[:]); err != nil {
		return
	}
	if request[1] == 3 {
		fixture.handleUDP(connection)
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))), 3*time.Second)
	if err != nil {
		_, _ = connection.Write([]byte{5, 5, 0, 1, 127, 0, 0, 1, 0, 0})
		return
	}
	defer upstream.Close()
	fixture.requests.Add(1)
	if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	_ = connection.SetDeadline(time.Time{})
	go func() { _, _ = io.Copy(upstream, connection) }()
	_, _ = io.Copy(connection, upstream)
}

func (fixture *socks5Fixture) handleUDP(connection net.Conn) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	defer relay.Close()
	port := relay.LocalAddr().(*net.UDPAddr).Port
	reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if _, err := connection.Write(reply); err != nil {
		return
	}
	packet := make([]byte, 4096)
	for !fixture.closed.Load() {
		_ = relay.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, client, err := relay.ReadFromUDP(packet)
		if err != nil {
			return
		}
		if fixture.closed.Load() || count < 10 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 || packet[3] != 1 {
			return
		}
		destination := &net.UDPAddr{IP: append(net.IP(nil), packet[4:8]...), Port: int(binary.BigEndian.Uint16(packet[8:10]))}
		upstream, err := net.DialUDP("udp", nil, destination)
		if err != nil {
			return
		}
		_ = upstream.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = upstream.Write(packet[10:count])
		if err != nil {
			_ = upstream.Close()
			return
		}
		answer := make([]byte, 4096)
		answerCount, err := upstream.Read(answer)
		_ = upstream.Close()
		if err != nil || fixture.closed.Load() {
			return
		}
		response := append([]byte{0, 0, 0, 1}, destination.IP.To4()...)
		response = binary.BigEndian.AppendUint16(response, uint16(destination.Port))
		response = append(response, answer[:answerCount]...)
		fixture.udpRequests.Add(1)
		_, _ = relay.WriteToUDP(response, client)
	}
}

func startUDPEcho(t *testing.T) *net.UDPAddr {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, peer, err := listener.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = listener.WriteToUDP(buffer[:count], peer)
		}
	}()
	return listener.LocalAddr().(*net.UDPAddr)
}

func exchangeViaSOCKSUDP(proxy string, destination *net.UDPAddr, payload []byte) ([]byte, error) {
	control, err := net.DialTimeout("tcp", proxy, realTrafficTimeout)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(realTrafficTimeout))
	if _, err = control.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(control, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
		return nil, fmt.Errorf("SOCKS5 UDP greeting: %w", err)
	}
	if _, err = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	var header [4]byte
	if _, err = io.ReadFull(control, header[:]); err != nil || header[0] != 5 || header[1] != 0 {
		return nil, fmt.Errorf("SOCKS5 UDP association rejected: %w", err)
	}
	var relayHost string
	switch header[3] {
	case 1:
		var address [4]byte
		if _, err = io.ReadFull(control, address[:]); err != nil {
			return nil, err
		}
		relayHost = net.IP(address[:]).String()
	case 4:
		var address [16]byte
		if _, err = io.ReadFull(control, address[:]); err != nil {
			return nil, err
		}
		relayHost = net.IP(address[:]).String()
	case 3:
		var length [1]byte
		if _, err = io.ReadFull(control, length[:]); err != nil {
			return nil, err
		}
		address := make([]byte, length[0])
		if _, err = io.ReadFull(control, address); err != nil {
			return nil, err
		}
		relayHost = string(address)
	default:
		return nil, errors.New("SOCKS5 returned an invalid UDP relay address")
	}
	var port [2]byte
	if _, err = io.ReadFull(control, port[:]); err != nil {
		return nil, err
	}
	if relayHost == "0.0.0.0" || relayHost == "::" {
		relayHost = "127.0.0.1"
	}
	relay, err := net.ResolveUDPAddr("udp", net.JoinHostPort(relayHost, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
	if err != nil {
		return nil, err
	}
	udp, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		return nil, err
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(realTrafficTimeout))
	request := append([]byte{0, 0, 0, 1}, destination.IP.To4()...)
	request = binary.BigEndian.AppendUint16(request, uint16(destination.Port))
	request = append(request, payload...)
	if _, err = udp.Write(request); err != nil {
		return nil, err
	}
	answer := make([]byte, 4096)
	count, err := udp.Read(answer)
	if err != nil {
		return nil, err
	}
	if count < 10 || answer[0] != 0 || answer[1] != 0 || answer[2] != 0 || answer[3] != 1 {
		return nil, errors.New("SOCKS5 returned an invalid UDP response")
	}
	return answer[10:count], nil
}
