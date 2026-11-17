package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// TransportManager управляет двумя независимыми процессами Xray. Ошибка XHTTP
// не мешает применить или сохранить конфигурацию основного TCP/Vision.
type TransportManager struct {
	tcp   *Manager
	xhttp *Manager
	mu    sync.Mutex
}

func NewTransportManager(tcp Options, xhttp *Options) *TransportManager {
	manager := &TransportManager{tcp: NewManager(tcp)}
	if xhttp != nil {
		manager.xhttp = NewManager(*xhttp)
	}
	return manager
}

func (m *TransportManager) Validate(ctx context.Context, clients []Client) error {
	tcpErr := m.tcp.Validate(ctx, clients)
	var xhttpErr error
	if m.xhttp != nil {
		xhttpErr = m.xhttp.Validate(ctx, clients)
	}
	return errors.Join(wrapTransport("TCP/Vision", tcpErr), wrapTransport("XHTTP", xhttpErr))
}

func (m *TransportManager) Apply(ctx context.Context, clients []Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Сначала применяем TCP, чтобы медленный или неисправный XHTTP не исчерпал
	// время запроса до отзыва учётных данных в рабочем TCP-процессе.
	// При первом разделении сохраняем старый XHTTP-вход в TCP только если он
	// действительно работал там до применения и новый процесс не запустился.
	hadLegacyXHTTP := m.tcp.hasLegacyXHTTP()
	var tcpErr error
	if !m.tcp.matchesRenderedTCP(clients) {
		tcpErr = m.tcp.Apply(ctx, clients)
	}
	var xhttpErr error
	if m.xhttp != nil {
		xhttpErr = m.xhttp.Apply(ctx, clients)
	}
	var fallbackErr error
	if xhttpErr != nil && m.tcp.options.Settings.XHTTP != nil && hadLegacyXHTTP {
		options := m.tcp.options
		options.Renderer = Render
		fallbackErr = NewManager(options).Apply(ctx, clients)
	}
	return errors.Join(wrapTransport("XHTTP", xhttpErr),
		wrapTransport("TCP/Vision legacy fallback", fallbackErr), wrapTransport("TCP/Vision", tcpErr))
}

// Старый XHTTP-вход можно сохранить только пока им владеет рабочий TCP-процесс.
// После разделения ошибка XHTTP не должна создавать этот вход снова.
func (m *Manager) hasLegacyXHTTP() bool {
	data, exists, err := readPrevious(m.options.ConfigPath)
	if err != nil || !exists {
		return false
	}
	var config struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(data, &config) != nil {
		return false
	}
	for _, inbound := range config.Inbounds {
		if inbound.Tag == "vless-xhttp-reality" {
			return true
		}
	}
	return false
}

func (m *Manager) matchesRenderedTCP(clients []Client) bool {
	data, exists, err := readPrevious(m.options.ConfigPath)
	if err != nil || !exists {
		return false
	}
	wanted, err := m.options.Renderer(m.options.Settings, clients)
	return err == nil && bytes.Equal(data, wanted)
}

func (m *TransportManager) ApplyTCP(ctx context.Context, clients []Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.tcp.Apply(ctx, clients); err != nil {
		return fmt.Errorf("TCP/Vision: %w", err)
	}
	return nil
}

func (m *TransportManager) ApplyXHTTP(ctx context.Context, clients []Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.xhttp == nil {
		return nil
	}
	if err := m.xhttp.Apply(ctx, clients); err != nil {
		return fmt.Errorf("XHTTP: %w", err)
	}
	return nil
}

func wrapTransport(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}
