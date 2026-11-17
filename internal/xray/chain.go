package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/rvburov/private-gateway/internal/model"
)

// RenderChainClientJSON создаёт конфигурацию клиентского Xray для одного профиля.
// SOCKS5 находится перед VPN и используется только как dialerProxy; прямого
// резервного выхода нет. JSON содержит учётные данные и должен храниться закрыто.
func RenderChainClientJSON(bundle model.ChainBundle, transport string) ([]byte, error) {
	if bundle.Version != 1 || bundle.EntryProxy.Type != "socks5" ||
		!validHost(bundle.EntryProxy.Host) || !validPort(bundle.EntryProxy.Port) ||
		(bundle.EntryProxy.Username == "") != (bundle.EntryProxy.Password == "") ||
		len(bundle.EntryProxy.Username) > 255 || len(bundle.EntryProxy.Password) > 255 ||
		strings.ContainsAny(bundle.EntryProxy.Username+bundle.EntryProxy.Password, "\x00\r\n") {
		return nil, errors.New("invalid entry SOCKS5 proxy")
	}
	var selected *model.ChainProfile
	for index := range bundle.Profiles {
		profile := &bundle.Profiles[index]
		match := transport == "tcp" && profile.Transport == "tcp" && profile.Security == "reality" ||
			transport == "xhttp" && profile.Transport == "xhttp" && profile.Security == "reality" ||
			transport == "h3" && profile.Transport == "xhttp" && profile.Security == "tls"
		if match {
			if selected != nil {
				return nil, errors.New("duplicate transport in chain bundle")
			}
			selected = profile
		}
	}
	if selected == nil {
		return nil, errors.New("requested transport is unavailable")
	}
	if transport == "h3" && !bundle.EntryProxy.SupportsUDP {
		return nil, errors.New("entry SOCKS5 UDP support is required for H3")
	}
	profile := *selected
	if !validHost(profile.Host) || !validPort(profile.Port) || !uuidPattern.MatchString(profile.UUID) ||
		!validHost(profile.SNI) || net.ParseIP(profile.SNI) != nil {
		return nil, errors.New("invalid chain VLESS endpoint")
	}
	user := map[string]any{"id": strings.ToLower(profile.UUID), "encryption": "none"}
	stream := map[string]any{"sockopt": map[string]any{"dialerProxy": "entry"}}
	switch transport {
	case "tcp", "xhttp":
		if _, err := decodeKey(profile.PublicKey); err != nil {
			return nil, errors.New("invalid chain REALITY public key")
		}
		if err := validateShortID(profile.ShortID); err != nil {
			return nil, errors.New("invalid chain REALITY short ID")
		}
		if profile.Fingerprint != "chrome" {
			return nil, errors.New("unsupported chain REALITY fingerprint")
		}
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{"serverName": profile.SNI,
			"fingerprint": profile.Fingerprint, "publicKey": profile.PublicKey,
			"shortId": strings.ToLower(profile.ShortID)}
		if transport == "tcp" {
			if profile.Flow != "xtls-rprx-vision" {
				return nil, errors.New("unsupported chain TCP flow")
			}
			user["flow"] = profile.Flow
			stream["network"] = "tcp"
			stream["realitySettings"].(map[string]any)["spiderX"] = profile.SpiderX
		} else {
			if profile.Flow != "" || !xhttpPathPattern.MatchString(profile.Path) || profile.Mode != "auto" {
				return nil, errors.New("invalid chain XHTTP settings")
			}
			stream["network"] = "xhttp"
			stream["xhttpSettings"] = map[string]any{"path": profile.Path, "mode": "auto"}
		}
	case "h3":
		if profile.Flow != "" || profile.PublicKey != "" || profile.ShortID != "" ||
			!xhttpPathPattern.MatchString(profile.Path) || profile.Mode != "auto" ||
			len(profile.ALPN) != 1 || profile.ALPN[0] != "h3" {
			return nil, errors.New("invalid chain H3/TLS settings")
		}
		stream["network"] = "xhttp"
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{"serverName": profile.SNI, "alpn": []string{"h3"}}
		stream["xhttpSettings"] = map[string]any{"path": profile.Path, "mode": "auto"}
	default:
		return nil, errors.New("unknown chain transport")
	}
	entry := map[string]any{"address": bundle.EntryProxy.Host, "port": bundle.EntryProxy.Port}
	if bundle.EntryProxy.Username != "" {
		entry["users"] = []any{map[string]any{"user": bundle.EntryProxy.Username, "pass": bundle.EntryProxy.Password}}
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{"tag": "local", "listen": "127.0.0.1", "port": 2080,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{
			map[string]any{"tag": "gateway", "protocol": "vless", "settings": map[string]any{
				"vnext": []any{map[string]any{"address": profile.Host, "port": profile.Port,
					"users": []any{user}}}}, "streamSettings": stream},
			map[string]any{"tag": "entry", "protocol": "socks", "settings": map[string]any{"servers": []any{entry}}},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode chain Xray client: %w", err)
	}
	return append(data, '\n'), nil
}
