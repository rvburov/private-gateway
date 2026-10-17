package xray

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rvburov/private-gateway/internal/model"
)

// TrafficProbe sends one HTTPS request through the gateway's Xray with a device's profile, the
// way a client app would, and returns the response body. Xray runs as a temporary local client.
// Its configuration holds the device credential, so it lives in a private temporary file that is
// removed afterwards, and its output is discarded rather than logged.
type TrafficProbe struct {
	Binary     string
	Server     string // host:port of the Xray inbound as seen from here
	Node       model.Node
	Credential string
	URL        string
	RootCAs    *x509.CertPool // nil means the system roots
}

const probeTimeout = 15 * time.Second

// ErrProbeStart means the local Xray client of a probe did not start, so the gateway was not tried.
var ErrProbeStart = errors.New("the Xray client did not start")

// ProbeStatusError means traffic went through the gateway, but the site answered with an error.
type ProbeStatusError struct{ Code int }

func (e ProbeStatusError) Error() string {
	return fmt.Sprintf("the site answered with status %d", e.Code)
}

func (p TrafficProbe) Run(ctx context.Context) (string, error) {
	host, portText, err := net.SplitHostPort(p.Server)
	if err != nil {
		return "", errors.New("Xray address must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || !validPort(port) {
		return "", errors.New("Xray port is invalid")
	}
	local, err := freeLocalPort()
	if err != nil {
		return "", err
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": local, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": host, "port": port,
				"users": []any{map[string]any{
					"id": strings.ToLower(p.Credential), "encryption": "none", "flow": "xtls-rprx-vision",
				}},
			}}},
			"streamSettings": map[string]any{
				"network": "raw", "security": "reality",
				"realitySettings": map[string]any{
					"serverName": p.Node.ServerName, "fingerprint": "chrome", "publicKey": p.Node.PublicKey,
					"shortId": strings.ToLower(p.Node.ShortID), "spiderX": "/",
				},
			},
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "private-gateway-probe-*.json")
	if err != nil {
		return "", errors.New("cannot create the temporary Xray client configuration")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return "", errors.New("cannot write the temporary Xray client configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout+5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, p.Binary, "run", "-config", file.Name())
	if err = command.Start(); err != nil {
		return "", ErrProbeStart
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	defer func() {
		_ = command.Process.Kill()
		<-done
	}()
	proxy := net.JoinHostPort("127.0.0.1", strconv.Itoa(local))
	if err = waitForListener(ctx, proxy, done); err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
				return dialSOCKS5(ctx, proxy, address)
			},
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request through Xray failed: %w", unwrapURLError(err))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", ProbeStatusError{Code: response.StatusCode}
	}
	return string(body), nil
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, errors.New("no free local port for the Xray client")
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitForListener(ctx context.Context, address string, exited <-chan struct{}) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return ErrProbeStart
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ErrProbeStart
}

// unwrapURLError drops the method and URL, which the caller knows, from a client error.
func unwrapURLError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err
	}
	return err
}

func dialSOCKS5(ctx context.Context, proxy, address string) (_ net.Conn, err error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = connection.Close()
		}
	}()
	_ = connection.SetDeadline(time.Now().Add(probeTimeout))
	if _, err = connection.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err = io.ReadFull(connection, greeting); err != nil || greeting[0] != 5 || greeting[1] != 0 {
		return nil, errors.New("the local SOCKS handshake failed")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || len(host) > 255 {
		return nil, errors.New("invalid destination")
	}
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = append(request, byte(port>>8), byte(port))
	if _, err = connection.Write(request); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	if _, err = io.ReadFull(connection, header); err != nil {
		return nil, errors.New("Xray did not answer the connection request")
	}
	if header[1] != 0 {
		return nil, errors.New("Xray could not reach the site through the gateway")
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
		return nil, errors.New("unexpected SOCKS answer")
	}
	if _, err = io.ReadFull(connection, make([]byte, remainder)); err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}
