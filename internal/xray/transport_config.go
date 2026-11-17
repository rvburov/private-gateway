package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RenderTCP создаёт конфигурацию только основного процесса. Удаление или ошибка
// дополнительного входа не меняет действующий TCP/Vision.
func RenderTCP(settings Settings, clients []Client) ([]byte, error) {
	settings.XHTTP = nil
	settings.H3 = nil
	return Render(settings, clients)
}

// RenderXHTTP создаёт конфигурацию второго процесса. Legacy REALITY/TCP и новый
// HTTP-вход за Caddy могут работать в нём одновременно на разных внутренних портах.
func RenderXHTTP(settings Settings, clients []Client) ([]byte, error) {
	if settings.XHTTP == nil && settings.H3 == nil {
		return nil, errors.New("XHTTP transport is not configured")
	}
	h3 := settings.H3
	settings.H3 = nil
	data, err := Render(settings, clients)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode generated Xray configuration: %w", err)
	}
	inbounds := make([]any, 0, 2)
	for _, inbound := range config["inbounds"].([]any) {
		item := inbound.(map[string]any)
		if item["tag"] == "vless-xhttp-reality" {
			inbounds = append(inbounds, item)
		}
	}
	if h3 != nil {
		if !validPort(h3.InternalPort) || h3.InternalPort == settings.Port ||
			h3.InternalPort == settings.StatsPort ||
			(settings.XHTTP != nil && h3.InternalPort == settings.XHTTP.Port) {
			return nil, errors.New("H3 internal port must be valid and distinct from other Xray ports")
		}
		if !xhttpPathPattern.MatchString(h3.Path) {
			return nil, errors.New("H3 path must be a slash followed by 16 to 128 URL-safe characters")
		}
		accounts := make([]any, 0, len(clients))
		for _, client := range clients {
			id := strings.ToLower(client.ID)
			accounts = append(accounts, map[string]any{"id": id, "email": id + "@private-gateway"})
		}
		inbounds = append(inbounds, map[string]any{
			"tag": "vless-xhttp-h3", "listen": settings.Listen, "port": h3.InternalPort,
			"protocol": "vless", "settings": map[string]any{"clients": accounts, "decryption": "none"},
			"streamSettings": map[string]any{"network": "xhttp", "security": "none",
				"xhttpSettings": map[string]any{"path": h3.Path, "mode": "auto"}},
			"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}},
		})
	}
	config["inbounds"] = inbounds
	config["log"] = map[string]any{"loglevel": "none", "access": "none"}
	delete(config, "stats")
	delete(config, "policy")
	delete(config, "api")
	delete(config, "routing")
	result, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode XHTTP configuration: %w", err)
	}
	return append(result, '\n'), nil
}
