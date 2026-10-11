// Package xray renders Xray's VLESS/REALITY configuration and importable profiles.
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
	ServerName string
	Target     string
	ShortID    string
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// PublicKey derives the REALITY X25519 public key without invoking Xray.
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

// Hostnames are ASCII DNS names; callers can use punycode for international names.
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

// Profile emits a VLESS URL accepted by Hiddify, including IPv6 endpoint support.
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

// Render produces the server configuration for current Xray RAW/TCP + REALITY.
func Render(settings Settings, clients []Client) ([]byte, error) {
	if _, err := PublicKey(settings.PrivateKey); err != nil {
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
		Flow  string `json:"flow"`
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
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Xray configuration: %w", err)
	}
	return append(data, '\n'), nil
}
