package xray

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvburov/private-gateway/internal/model"
)

const xhttpTestUUID = "11111111-2222-3333-4444-555555555555"

func xhttpTestSettings() Settings {
	settings := testSettings()
	settings.ServerName = "example.com"
	settings.Target = "example.org:443"
	settings.StatsPort = 10085
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	settings.XHTTP = &XHTTPSettings{
		Port: 8444, PrivateKey: base64.RawURLEncoding.EncodeToString(key),
		ShortID: "8899aabbccddeeff", Path: "/test_path_0123456789",
	}
	return settings
}

func xhttpTestNode(t *testing.T) model.Node {
	t.Helper()
	settings := xhttpTestSettings()
	primaryKey, err := PublicKey(settings.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	xhttpKey, err := PublicKey(settings.XHTTP.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return model.Node{Host: "198.51.100.10", Port: 8443, ServerName: settings.ServerName,
		PublicKey: primaryKey, ShortID: settings.ShortID,
		XHTTP: &model.XHTTPNode{Port: settings.XHTTP.Port, PublicKey: xhttpKey,
			ShortID: settings.XHTTP.ShortID, Path: settings.XHTTP.Path, Mode: "auto"}}
}

func TestXHTTPAddsIndependentInboundWithoutChangingTCP(t *testing.T) {
	settings := xhttpTestSettings()
	clients := []Client{{ID: xhttpTestUUID, Name: "Test device"}}
	withXHTTP, err := Render(settings, clients)
	if err != nil {
		t.Fatal(err)
	}
	settings.XHTTP = nil
	legacy, err := Render(settings, clients)
	if err != nil {
		t.Fatal(err)
	}
	var before, after struct {
		Inbounds []map[string]any  `json:"inbounds"`
		Log      map[string]string `json:"log"`
	}
	if err := json.Unmarshal(legacy, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(withXHTTP, &after); err != nil {
		t.Fatal(err)
	}
	if len(before.Inbounds) != 2 || len(after.Inbounds) != 3 ||
		!reflect.DeepEqual(before.Inbounds[0], after.Inbounds[0]) ||
		!reflect.DeepEqual(before.Inbounds[1], after.Inbounds[2]) {
		t.Fatal("adding XHTTP changed the TCP inbound or private statistics API")
	}
	if before.Log["loglevel"] != "warning" || after.Log["loglevel"] != "none" || after.Log["access"] != "none" {
		t.Fatal("XHTTP must disable runtime logs without changing TCP-only logging")
	}
	xhttp := after.Inbounds[1]
	stream := xhttp["streamSettings"].(map[string]any)
	if xhttp["tag"] != "vless-xhttp-reality" || xhttp["port"] != float64(8444) ||
		stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Fatal("missing dedicated XHTTP/REALITY listener")
	}
	transport := stream["xhttpSettings"].(map[string]any)
	if transport["mode"] != "auto" || transport["path"] != xhttpTestSettings().XHTTP.Path {
		t.Fatal("XHTTP transport does not use the configured path and auto mode")
	}
	account := xhttp["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if account["id"] != xhttpTestUUID || account["email"] != xhttpTestUUID+"@private-gateway" {
		t.Fatal("XHTTP changed the device identity or traffic counter key")
	}
	if _, hasFlow := account["flow"]; hasFlow {
		t.Fatal("XHTTP must omit the Vision flow")
	}
	if !reflect.DeepEqual(xhttp["sniffing"].(map[string]any)["destOverride"], []any{"http", "tls", "quic"}) {
		t.Fatal("XHTTP sniffing settings are missing")
	}
}

func TestXHTTPProfileIsAdditiveAndURLSafe(t *testing.T) {
	node := xhttpTestNode(t)
	withXHTTP, err := ClientProfiles(node, xhttpTestUUID, "Laptop #1")
	if err != nil {
		t.Fatal(err)
	}
	xhttp := node.XHTTP
	node.XHTTP = nil
	legacy, err := ClientProfiles(node, xhttpTestUUID, "Laptop #1")
	if err != nil {
		t.Fatal(err)
	}
	if withXHTTP.URI != legacy.URI || withXHTTP.SingBoxJSON != legacy.SingBoxJSON ||
		withXHTTP.MihomoYAML != legacy.MihomoYAML || legacy.XHTTPURI != "" || legacy.XHTTPPort != 0 {
		t.Fatal("XHTTP changed an existing TCP export")
	}
	u, err := url.Parse(withXHTTP.XHTTPURI)
	if err != nil {
		t.Fatal("invalid XHTTP URI")
	}
	q := u.Query()
	if u.Scheme != "vless" || u.Hostname() != node.Host || u.Port() != strconv.Itoa(xhttp.Port) ||
		u.User.Username() != xhttpTestUUID || u.Fragment != "Laptop #1 XHTTP" ||
		withXHTTP.XHTTPPort != xhttp.Port || !strings.Contains(u.RawQuery, "path=%2F") {
		t.Fatal("XHTTP URI lost a connection parameter or fragment")
	}
	for key, value := range map[string]string{"type": "xhttp", "security": "reality", "encryption": "none",
		"mode": "auto", "path": xhttp.Path, "pbk": xhttp.PublicKey, "sid": xhttp.ShortID,
		"sni": node.ServerName, "fp": "chrome"} {
		if q.Get(key) != value {
			t.Errorf("XHTTP query field %s differs", key)
		}
	}
	if q.Has("flow") || q.Has("spx") {
		t.Fatal("XHTTP URI contains TCP-only parameters")
	}
	node.XHTTP = xhttp
	node.XHTTP.Mode = "packet-up"
	if _, err := XHTTPProfile(node, xhttpTestUUID, "Test"); err == nil {
		t.Fatal("accepted a mode not supported by this server profile")
	}
}

func TestInvalidXHTTPSettingsAndProfileAreRejected(t *testing.T) {
	changes := map[string]func(*Settings){
		"invalid port":         func(s *Settings) { s.XHTTP.Port = 0 },
		"large port":           func(s *Settings) { s.XHTTP.Port = 65536 },
		"TCP collision":        func(s *Settings) { s.XHTTP.Port = s.Port },
		"statistics collision": func(s *Settings) { s.XHTTP.Port = s.StatsPort },
		"invalid key":          func(s *Settings) { s.XHTTP.PrivateKey = "invalid-test-key" },
		"shared key":           func(s *Settings) { s.XHTTP.PrivateKey = s.PrivateKey },
		"invalid short ID":     func(s *Settings) { s.XHTTP.ShortID = "not-hexadecimal" },
		"shared short ID":      func(s *Settings) { s.XHTTP.ShortID = strings.ToUpper(s.ShortID) },
		"short path":           func(s *Settings) { s.XHTTP.Path = "/short" },
		"query in path":        func(s *Settings) { s.XHTTP.Path = "/test_path_0123456789?test" },
		"path without slash":   func(s *Settings) { s.XHTTP.Path = "test_path_0123456789" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s := xhttpTestSettings()
			change(&s)
			_, err := Render(s, []Client{{ID: xhttpTestUUID}})
			if err == nil {
				t.Fatal("invalid XHTTP configuration accepted")
			}
			for _, secret := range []string{s.XHTTP.PrivateKey, s.XHTTP.Path, s.XHTTP.ShortID} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("validation error disclosed a connection parameter")
				}
			}
		})
	}
	for name, change := range map[string]func(*model.XHTTPNode){
		"invalid port":       func(n *model.XHTTPNode) { n.Port = 0 },
		"shared port":        func(n *model.XHTTPNode) { n.Port = 8443 },
		"invalid public key": func(n *model.XHTTPNode) { n.PublicKey = "invalid-test-key" },
		"invalid short ID":   func(n *model.XHTTPNode) { n.ShortID = "not-hexadecimal" },
		"short path":         func(n *model.XHTTPNode) { n.Path = "/short" },
	} {
		t.Run("profile "+name, func(t *testing.T) {
			node := xhttpTestNode(t)
			change(node.XHTTP)
			if _, err := ClientProfiles(node, xhttpTestUUID, "Test"); err == nil {
				t.Fatal("invalid XHTTP metadata exported")
			}
		})
	}
}

func TestRealXrayAcceptsBothTransports(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real TCP and XHTTP validation")
	}
	data, err := Render(xhttpTestSettings(), []Client{{ID: xhttpTestUUID}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xhttp-validation.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command(binary, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatal("real Xray rejected the two-transport configuration")
	}
}

func TestRealXrayXHTTPAndTCPShareIdentityAndRevocation(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY for real XHTTP and TCP traffic checks")
	}
	settings := xhttpTestSettings()
	settings.Listen = "127.0.0.1"
	settings.Target = startTLS13Target(t, "example.com")
	settings.Port, settings.StatsPort, settings.XHTTP.Port = unusedPort(t), unusedPort(t), unusedPort(t)
	origin := startHTTPOrigin(t)
	path := filepath.Join(t.TempDir(), "two-transport-server.json")
	writeServer := func(clients []Client) {
		data, err := Render(settings, clients)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeServer([]Client{{ID: xhttpTestUUID}})
	server := startXrayProcess(t, binary, path, settings.Port)
	node := xhttpTestNode(t)
	node.Host, node.Port, node.ServerName = "127.0.0.1", settings.Port, settings.ServerName
	node.XHTTP.Port = settings.XHTTP.Port
	profile, err := XHTTPProfile(node, xhttpTestUUID, "Loopback")
	if err != nil {
		t.Fatal(err)
	}
	tcpProxy := startRealXrayClient(t, binary, settings.Port, node.PublicKey, settings, xhttpTestUUID)
	xhttpProxy := startXHTTPClientFromURI(t, binary, profile)
	assertProxyResponse(t, tcpProxy, origin)
	statsAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.StatsPort))
	readTraffic := func(after Traffic) Traffic {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			traffic, err := QueryTraffic(context.Background(), binary, statsAddress)
			if err == nil && traffic[xhttpTestUUID].UplinkBytes > after.UplinkBytes &&
				traffic[xhttpTestUUID].DownlinkBytes > after.DownlinkBytes {
				return traffic[xhttpTestUUID]
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("Xray did not combine both transports in the device traffic counters")
		return Traffic{}
	}
	tcpTraffic := readTraffic(Traffic{})
	assertProxyResponse(t, xhttpProxy, origin)
	_ = readTraffic(tcpTraffic)
	// Повторный запрос использует тот же процесс клиента и уже созданный пул XHTTP.
	assertProxyResponse(t, xhttpProxy, origin)
	server.stop(t)
	writeServer(nil)
	server = startXrayProcess(t, binary, path, settings.Port)
	for _, proxy := range []string{tcpProxy, xhttpProxy} {
		if _, err := fetchViaSOCKS(proxy, origin); err == nil {
			t.Fatal("revoked device still uses one of the server transports")
		}
	}
	// Восстановление того же заведомо тестового UUID доказывает, что отказ связан
	// с отсутствием учётной записи, а не с неработающим XHTTP или локальным сайтом.
	server.stop(t)
	writeServer([]Client{{ID: xhttpTestUUID}})
	_ = startXrayProcess(t, binary, path, settings.Port)
	assertProxyResponse(t, startRealXrayClient(t, binary, settings.Port, node.PublicKey, settings, xhttpTestUUID), origin)
	assertProxyResponse(t, startXHTTPClientFromURI(t, binary, profile), origin)
}

func startXHTTPClientFromURI(t *testing.T, binary, profile string) string {
	t.Helper()
	u, err := url.Parse(profile)
	if err != nil {
		t.Fatal("cannot parse the test XHTTP profile")
	}
	serverPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal("invalid test XHTTP port")
	}
	q := u.Query()
	port := unusedPort(t)
	config := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": u.Hostname(), "port": serverPort,
				"users": []any{map[string]any{"id": u.User.Username(), "encryption": q.Get("encryption")}},
			}}},
			"streamSettings": map[string]any{
				"network": q.Get("type"), "security": q.Get("security"),
				"xhttpSettings": map[string]any{"path": q.Get("path"), "mode": q.Get("mode")},
				"realitySettings": map[string]any{
					"serverName": q.Get("sni"), "fingerprint": q.Get("fp"),
					"publicKey": q.Get("pbk"), "shortId": q.Get("sid"),
				},
			},
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal("cannot encode the test XHTTP client")
	}
	path := filepath.Join(t.TempDir(), "xhttp-client.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = startXrayProcess(t, binary, path, port)
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func TestXHTTPZeroClientsAndSharedRevocation(t *testing.T) {
	data, err := Render(xhttpTestSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Settings struct {
				Clients []json.RawMessage `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range config.Inbounds[:2] {
		if inbound.Settings.Clients == nil || len(inbound.Settings.Clients) != 0 {
			t.Fatal("removing all clients must clear both inbound client lists")
		}
	}
}

func TestXHTTPManagerOwnsSettingsAndHidesConnectionParameters(t *testing.T) {
	m, _ := managerForTest(t, "success")
	settings := xhttpTestSettings()
	m.options.Settings = settings
	m = NewManager(m.options)
	settings.XHTTP.Path = "/changed_path_0123456789"
	if m.options.Settings.XHTTP.Path == settings.XHTTP.Path {
		t.Fatal("caller can mutate the manager XHTTP configuration")
	}
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_ECHO_CONFIG", "1")
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_ERROR_TEXT", "unknown_endpoint=203.0.113.200 secret_extra=unrecognized-test-value /other_test_path_1234567890")
	err := m.Validate(context.Background(), []Client{{ID: xhttpTestUUID, Name: "Test device"}})
	if err == nil {
		t.Fatal("expected the helper validation failure")
	}
	for _, secret := range []string{m.options.Settings.PrivateKey, m.options.Settings.XHTTP.PrivateKey,
		m.options.Settings.XHTTP.Path, m.options.Settings.XHTTP.ShortID, xhttpTestUUID,
		"203.0.113.200", "unrecognized-test-value", "/other_test_path_1234567890"} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("subprocess error exposed a connection parameter")
		}
	}
	assertNoTemporaryFiles(t, m)
	m.options.Binary = filepath.Join(t.TempDir(), "unavailable-xray")
	err = m.Validate(context.Background(), []Client{{ID: xhttpTestUUID}})
	if err == nil || strings.Contains(err.Error(), m.options.Binary) || !strings.Contains(err.Error(), "cannot run Xray command") {
		t.Fatal("XHTTP startup failure exposed a command path or was not reported")
	}
}
