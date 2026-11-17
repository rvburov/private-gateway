package xray

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransportApplyUpdatesTCPWhenXHTTPFails(t *testing.T) {
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_HELPER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	settings := xhttpTestSettings()
	tcp := Options{Settings: settings, ConfigPath: filepath.Join(dir, "tcp.json"), Binary: binary,
		RestartCommand: []string{binary, "restart", filepath.Join(dir, "tcp-count"), "success"}, Renderer: RenderTCP}
	xhttp := Options{Settings: settings, ConfigPath: filepath.Join(dir, "xhttp.json"), Binary: binary,
		RestartCommand: []string{binary, "restart", filepath.Join(dir, "xhttp-count"), "fail-always"}, Renderer: RenderXHTTP}
	legacy, err := Render(settings, []Client{{ID: xhttpTestUUID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcp.ConfigPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewTransportManager(tcp, &xhttp)
	if err := manager.Apply(context.Background(), []Client{{ID: testID}}); err == nil || !strings.Contains(err.Error(), "XHTTP") {
		t.Fatalf("XHTTP failure was not reported: %v", err)
	}
	data, err := os.ReadFile(tcp.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					ID string `json:"id"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) < 2 || config.Inbounds[0].Settings.Clients[0].ID != testID ||
		config.Inbounds[1].Tag != "vless-xhttp-reality" {
		t.Fatal("TCP UUID was not applied or legacy XHTTP was removed after XHTTP failure")
	}
	manager.xhttp.options.RestartCommand[3] = "success"
	if err := manager.Apply(context.Background(), []Client{{ID: xhttpTestUUID}}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(tcp.ConfigPath)
	if err != nil || !strings.Contains(string(data), xhttpTestUUID) || strings.Contains(string(data), testID) ||
		strings.Contains(string(data), "vless-xhttp-reality") {
		t.Fatal("successful split did not update TCP independently", err)
	}
	countBefore, err := os.ReadFile(filepath.Join(dir, "tcp-count"))
	if err != nil {
		t.Fatal(err)
	}
	manager.xhttp.options.RestartCommand[3] = "fail-always"
	if err := manager.Apply(context.Background(), []Client{{ID: xhttpTestUUID}}); err == nil {
		t.Fatal("failed XHTTP apply was accepted")
	}
	countAfter, err := os.ReadFile(filepath.Join(dir, "tcp-count"))
	if err != nil || string(countAfter) != string(countBefore) {
		t.Fatal("post-split XHTTP failure restarted unchanged TCP", err)
	}
	data, err = os.ReadFile(tcp.ConfigPath)
	if err != nil || strings.Contains(string(data), "vless-xhttp-reality") {
		t.Fatal("post-split XHTTP failure restored legacy inbound", err)
	}
}

func TestTransportApplyRevokesTCPWhenLegacyFallbackIsInvalid(t *testing.T) {
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_HELPER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	settings := xhttpTestSettings()
	settings.XHTTP.Path = "invalid"
	tcp := Options{Settings: settings, ConfigPath: filepath.Join(dir, "tcp.json"), Binary: binary,
		RestartCommand: []string{binary, "restart", filepath.Join(dir, "tcp-count"), "success"}, Renderer: RenderTCP}
	xhttp := Options{Settings: settings, ConfigPath: filepath.Join(dir, "xhttp.json"), Binary: binary,
		RestartCommand: []string{binary, "restart", filepath.Join(dir, "xhttp-count"), "success"}, Renderer: RenderXHTTP}
	manager := NewTransportManager(tcp, &xhttp)
	if err := manager.Apply(context.Background(), []Client{{ID: xhttpTestUUID}}); err == nil {
		t.Fatal("invalid XHTTP was accepted")
	}
	data, err := os.ReadFile(tcp.ConfigPath)
	if err != nil || !strings.Contains(string(data), xhttpTestUUID) || strings.Contains(string(data), "vless-xhttp-reality") {
		t.Fatal("TCP UUID update was blocked by invalid XHTTP", err)
	}
}
