// Пакет xray собирает конфигурацию VLESS/REALITY для Xray и профили для импорта в клиенты.
package xray

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/rvburov/private-gateway/internal/model"
)

type Client struct {
	ID   string
	Name string
}

type Settings struct {
	PrivateKey string
	Listen     string
	Port       int
	StatsPort  int
	ServerName string
	Target     string
	ShortID    string
	XHTTP      *XHTTPSettings
}

// XHTTPSettings включает отдельный REALITY-вход, не изменяя основной TCP/Vision.
type XHTTPSettings struct {
	Port       int
	PrivateKey string
	ShortID    string
	Path       string
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var xhttpPathPattern = regexp.MustCompile(`^/[A-Za-z0-9_-]{16,128}$`)

// PublicKey выводит открытый ключ REALITY X25519, не запуская Xray.
func PublicKey(privateKey string) (string, error) {
	key, err := decodeKey(privateKey)
	if err != nil {
		return "", errors.New("REALITY private key must be a 32-byte unpadded base64url key")
	}
	private, err := ecdh.X25519().NewPrivateKey(key)
	if err != nil {
		return "", errors.New("invalid REALITY private key")
	}
	return base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), nil
}

func decodeKey(key string) ([]byte, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(key)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid key")
	}
	return b, nil
}

func validPort(port int) bool { return port > 0 && port <= 65535 }

// Имена хостов — DNS-имена в ASCII; международные имена можно передать в punycode.
func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

func validateShortID(id string) error {
	if len(id) < 2 || len(id) > 16 || len(id)%2 != 0 {
		return errors.New("REALITY short ID must contain 2 to 16 hexadecimal characters in byte pairs")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return errors.New("REALITY short ID must be hexadecimal")
	}
	return nil
}

// Profile выдаёт стандартную ссылку VLESS, в том числе для адреса IPv6.
func Profile(node model.Node, credentialID, name string) (string, error) {
	if !uuidPattern.MatchString(credentialID) {
		return "", errors.New("VLESS credential must be a UUID")
	}
	if !validHost(node.Host) || !validPort(node.Port) {
		return "", errors.New("node requires a valid host and port")
	}
	if !validHost(node.ServerName) || net.ParseIP(node.ServerName) != nil {
		return "", errors.New("REALITY server name must be a DNS hostname")
	}
	if _, err := decodeKey(node.PublicKey); err != nil {
		return "", errors.New("REALITY public key must be a 32-byte unpadded base64url key")
	}
	if err := validateShortID(node.ShortID); err != nil {
		return "", err
	}
	params := url.Values{
		"encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "type": {"tcp"},
		"security": {"reality"}, "sni": {node.ServerName}, "fp": {"chrome"},
		"pbk": {node.PublicKey}, "sid": {strings.ToLower(node.ShortID)}, "spx": {"/"},
	}
	u := url.URL{
		Scheme: "vless", User: url.User(strings.ToLower(credentialID)),
		Host:     net.JoinHostPort(node.Host, strconv.Itoa(node.Port)),
		RawQuery: params.Encode(), Fragment: name,
	}
	return u.String(), nil
}

// XHTTPProfile выдаёт дополнительную ссылку без flow: Vision остаётся только в TCP-профиле.
func XHTTPProfile(node model.Node, credentialID, name string) (string, error) {
	if _, err := Profile(node, credentialID, name); err != nil {
		return "", err
	}
	xhttp := node.XHTTP
	if xhttp == nil {
		return "", errors.New("XHTTP is not configured")
	}
	if !validPort(xhttp.Port) || xhttp.Port == node.Port {
		return "", errors.New("XHTTP requires a valid port distinct from the TCP profile")
	}
	if _, err := decodeKey(xhttp.PublicKey); err != nil || xhttp.PublicKey == node.PublicKey {
		return "", errors.New("XHTTP requires a separate REALITY public key")
	}
	if err := validateShortID(xhttp.ShortID); err != nil || strings.EqualFold(xhttp.ShortID, node.ShortID) {
		return "", errors.New("XHTTP requires a separate valid REALITY short ID")
	}
	if !xhttpPathPattern.MatchString(xhttp.Path) {
		return "", errors.New("XHTTP path must be a slash followed by 16 to 128 URL-safe characters")
	}
	if xhttp.Mode != "" && xhttp.Mode != "auto" {
		return "", errors.New("XHTTP mode must be auto")
	}
	params := url.Values{
		"encryption": {"none"}, "type": {"xhttp"}, "security": {"reality"},
		"path": {xhttp.Path}, "mode": {"auto"}, "sni": {node.ServerName},
		"fp": {"chrome"}, "pbk": {xhttp.PublicKey}, "sid": {strings.ToLower(xhttp.ShortID)},
	}
	u := url.URL{
		Scheme: "vless", User: url.User(strings.ToLower(credentialID)),
		Host:     net.JoinHostPort(node.Host, strconv.Itoa(xhttp.Port)),
		RawQuery: params.Encode(), Fragment: name + " XHTTP",
	}
	return u.String(), nil
}

// ClientProfiles строит все форматы для клиентов из одного проверенного секрета.
// Каждый формат выдаёт только адрес профиля, доступный после входа.
func ClientProfiles(node model.Node, credentialID, name string) (model.Profile, error) {
	uri, err := Profile(node, credentialID, name)
	if err != nil {
		return model.Profile{}, err
	}
	credentialID = strings.ToLower(credentialID)
	shortID := strings.ToLower(node.ShortID)
	singBox := map[string]any{
		"log": map[string]any{"level": "warn"},
		"inbounds": []any{map[string]any{
			"type": "mixed", "tag": "local", "listen": "127.0.0.1", "listen_port": 2080,
		}},
		"outbounds": []any{map[string]any{
			"type": "vless", "tag": "gateway", "server": node.Host,
			"server_port": node.Port, "uuid": credentialID, "flow": "xtls-rprx-vision",
			"tls": map[string]any{
				"enabled": true, "server_name": node.ServerName,
				"utls": map[string]any{"enabled": true, "fingerprint": "chrome"},
				"reality": map[string]any{
					"enabled": true, "public_key": node.PublicKey, "short_id": shortID,
				},
			},
		}},
		"route": map[string]any{"final": "gateway"},
	}
	singBoxJSON, err := json.MarshalIndent(singBox, "", "  ")
	if err != nil {
		return model.Profile{}, fmt.Errorf("encode sing-box profile: %w", err)
	}
	// Строки в кавычках JSON — допустимые скаляры YAML: так имя устройства и хост IPv6
	// не могут изменить смысл созданной конфигурации.
	quoted := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	mihomoYAML := fmt.Sprintf(`mixed-port: 7890
allow-lan: false
mode: rule
log-level: warning
proxies:
  - name: %s
    type: vless
    server: %s
    port: %d
    uuid: %s
    network: tcp
    tls: true
    flow: xtls-rprx-vision
    servername: %s
    client-fingerprint: chrome
    reality-opts:
      public-key: %s
      short-id: %s
proxy-groups:
  - name: GATEWAY
    type: select
    proxies:
      - %s
rules:
  - MATCH,GATEWAY
`, quoted(name), quoted(node.Host), node.Port, quoted(credentialID),
		quoted(node.ServerName), quoted(node.PublicKey), quoted(shortID), quoted(name))
	profile := model.Profile{URI: uri, SingBoxJSON: string(singBoxJSON) + "\n", MihomoYAML: mihomoYAML}
	if node.XHTTP != nil {
		profile.XHTTPURI, err = XHTTPProfile(node, credentialID, name)
		if err != nil {
			return model.Profile{}, err
		}
		profile.XHTTPPort = node.XHTTP.Port
	}
	return profile, nil
}

// Render сохраняет RAW/TCP + REALITY и при настройке добавляет отдельный XHTTP-вход.
func Render(settings Settings, clients []Client) ([]byte, error) {
	primaryKey, err := PublicKey(settings.PrivateKey)
	if err != nil {
		return nil, err
	}
	if settings.Listen == "" {
		settings.Listen = "0.0.0.0"
	}
	if net.ParseIP(settings.Listen) == nil || !validPort(settings.Port) {
		return nil, errors.New("Xray listen must be an IP address and port must be between 1 and 65535")
	}
	if !validHost(settings.ServerName) || net.ParseIP(settings.ServerName) != nil {
		return nil, errors.New("REALITY server name must be a DNS hostname")
	}
	host, port, err := net.SplitHostPort(settings.Target)
	if err != nil || !validHost(host) {
		return nil, errors.New("REALITY target must be a valid host:port")
	}
	p, err := strconv.Atoi(port)
	if err != nil || !validPort(p) {
		return nil, errors.New("REALITY target port must be between 1 and 65535")
	}
	if err := validateShortID(settings.ShortID); err != nil {
		return nil, err
	}
	type account struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Flow  string `json:"flow,omitempty"`
	}
	accounts := make([]account, 0, len(clients))
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		id := strings.ToLower(client.ID)
		if !uuidPattern.MatchString(id) {
			return nil, errors.New("VLESS client credential must be a UUID")
		}
		if seen[id] {
			return nil, errors.New("duplicate VLESS client credential")
		}
		seen[id] = true
		accounts = append(accounts, account{ID: id, Email: id + "@private-gateway", Flow: "xtls-rprx-vision"})
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "vless-reality", "listen": settings.Listen, "port": settings.Port, "protocol": "vless",
			"settings": map[string]any{"clients": accounts, "decryption": "none"},
			"streamSettings": map[string]any{
				"network": "raw", "security": "reality",
				"realitySettings": map[string]any{
					"show": false, "target": settings.Target, "xver": 0,
					"serverNames": []string{settings.ServerName}, "privateKey": settings.PrivateKey,
					"shortIds": []string{strings.ToLower(settings.ShortID)},
				},
			},
		}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{}}},
	}
	if xhttp := settings.XHTTP; xhttp != nil {
		// XHTTP использует секретный путь. Отключаем журналы ядра, чтобы runtime-
		// ошибки не печатали адреса, UUID или параметры транспорта в Docker logs.
		config["log"] = map[string]any{"loglevel": "none", "access": "none"}
		if !validPort(xhttp.Port) || xhttp.Port == settings.Port || xhttp.Port == settings.StatsPort {
			return nil, errors.New("XHTTP port must be valid and distinct from TCP and statistics ports")
		}
		xhttpKey, err := PublicKey(xhttp.PrivateKey)
		if err != nil || xhttpKey == primaryKey {
			return nil, errors.New("XHTTP requires a separate valid REALITY private key")
		}
		if err := validateShortID(xhttp.ShortID); err != nil || strings.EqualFold(xhttp.ShortID, settings.ShortID) {
			return nil, errors.New("XHTTP requires a separate valid REALITY short ID")
		}
		if !xhttpPathPattern.MatchString(xhttp.Path) {
			return nil, errors.New("XHTTP path must be a slash followed by 16 to 128 URL-safe characters")
		}
		xhttpAccounts := make([]account, 0, len(accounts))
		for _, client := range accounts {
			xhttpAccounts = append(xhttpAccounts, account{ID: client.ID, Email: client.Email})
		}
		config["inbounds"] = append(config["inbounds"].([]any), map[string]any{
			"tag": "vless-xhttp-reality", "listen": settings.Listen, "port": xhttp.Port, "protocol": "vless",
			"settings": map[string]any{"clients": xhttpAccounts, "decryption": "none"},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "reality",
				"xhttpSettings": map[string]any{"path": xhttp.Path, "mode": "auto"},
				"realitySettings": map[string]any{
					"show": false, "target": settings.Target, "xver": 0,
					"serverNames": []string{settings.ServerName}, "privateKey": xhttp.PrivateKey,
					"shortIds": []string{strings.ToLower(xhttp.ShortID)},
				},
			},
			"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}},
		})
	}
	if settings.StatsPort != 0 {
		if !validPort(settings.StatsPort) || settings.StatsPort == settings.Port {
			return nil, errors.New("Xray stats port must be valid and distinct from VPN port")
		}
		config["stats"] = map[string]any{}
		config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{
			"statsUserUplink": true, "statsUserDownlink": true,
		}}}
		config["api"] = map[string]any{"tag": "stats-api", "services": []string{"StatsService"}}
		config["inbounds"] = append(config["inbounds"].([]any), map[string]any{
			"tag": "stats-api", "listen": "0.0.0.0", "port": settings.StatsPort,
			"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"},
		})
		config["routing"] = map[string]any{"rules": []any{map[string]any{
			"type": "field", "inboundTag": []string{"stats-api"}, "outboundTag": "stats-api",
		}}}
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Xray configuration: %w", err)
	}
	return append(data, '\n'), nil
}
