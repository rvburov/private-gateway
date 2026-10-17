package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteOptions задаёт закрытый browser worker из Docker-стека.
// Worker управляет только временным процессом браузера и никогда — сокетом Docker.
type RemoteOptions struct {
	URL       string
	Token     string
	PublicURL string
}

type Status struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
}

// DisabledManager позволяет работать локально без browser worker.
type DisabledManager struct{}

func (DisabledManager) Status(context.Context) (Status, error) { return Status{}, nil }
func (DisabledManager) Start(context.Context) (Status, error) {
	return Status{}, errors.New("browser worker is not configured")
}
func (DisabledManager) Stop(context.Context) (Status, error) { return Status{}, nil }

type RemoteManager struct {
	options RemoteOptions
	client  *http.Client
}

func NewRemoteManager(options RemoteOptions) (*RemoteManager, error) {
	worker, err := url.Parse(options.URL)
	if err != nil || worker.Scheme != "http" || worker.Hostname() == "" || worker.User != nil || worker.Path != "" || worker.RawQuery != "" || worker.Fragment != "" {
		return nil, errors.New("browser worker URL must be an internal http://host:port address")
	}
	public, err := url.Parse(options.PublicURL)
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || !strings.HasPrefix(public.Path, "/browser/") || public.User != nil || public.Fragment != "" {
		return nil, errors.New("browser public URL must be an HTTPS /browser/ address")
	}
	if len(options.Token) < 32 {
		return nil, errors.New("browser worker token must contain at least 32 characters")
	}
	return &RemoteManager{options: options, client: &http.Client{
		Timeout:       40 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (m *RemoteManager) request(ctx context.Context, method, path string) (Status, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.options.URL+path, nil)
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("Authorization", "Bearer "+m.options.Token)
	resp, err := m.client.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("browser worker unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("browser worker returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Running bool `json:"running"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&result); err != nil {
		return Status{}, errors.New("invalid browser worker response")
	}
	if !result.Running {
		return Status{URL: m.options.PublicURL}, nil
	}
	return Status{Running: true, URL: m.options.PublicURL}, nil
}

func (m *RemoteManager) Status(ctx context.Context) (Status, error) {
	return m.request(ctx, http.MethodGet, "/status")
}

func (m *RemoteManager) Start(ctx context.Context) (Status, error) {
	return m.request(ctx, http.MethodPost, "/start")
}

func (m *RemoteManager) Stop(ctx context.Context) (Status, error) {
	return m.request(ctx, http.MethodPost, "/stop")
}
