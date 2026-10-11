// Package browser manages the ephemeral remote browser on a rootless Podman
// worker. The container never receives gateway credentials or host mounts.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const managedLabel = "io.private-gateway.browser.managed"

// Options selects the worker and browser image. Connection, when provided,
// is a Podman remote URL (a dedicated local worker's Unix socket).
type Options struct {
	Binary     string
	Image      string
	Name       string
	Port       int
	Connection string
}

// Status reports readiness of the container processes and noVNC HTTP endpoint.
// URL is a worker-loopback address; use an SSH tunnel for remote access.
type Status struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type subprocessRunner struct{}

func (subprocessRunner) Run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).CombinedOutput()
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Manager serializes lifecycle operations to avoid concurrent start/stop races.
type Manager struct {
	options   Options
	runner    commandRunner
	client    httpDoer
	uid       func() int
	mu        sync.Mutex
	configErr error
	readyWait time.Duration
	retryWait time.Duration
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func NewManager(options Options) *Manager {
	if options.Binary == "" {
		options.Binary = "podman"
	}
	if options.Image == "" {
		options.Image = "localhost/private-gateway-browser:0.1"
	}
	if options.Name == "" {
		options.Name = "private-gateway-browser"
	}
	if options.Port == 0 {
		options.Port = 6080
	}
	m := &Manager{
		options: options,
		runner:  subprocessRunner{},
		client: &http.Client{
			Timeout:       2 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		uid:       os.Geteuid,
		readyWait: 30 * time.Second,
		retryWait: 200 * time.Millisecond,
	}
	if options.Port < 1024 || options.Port > 65535 {
		m.configErr = errors.New("browser port must be between 1024 and 65535")
	} else if !validName.MatchString(options.Name) || strings.HasPrefix(options.Image, "-") || strings.TrimSpace(options.Image) == "" {
		m.configErr = errors.New("invalid browser container name or image")
	} else if options.Connection != "" {
		connection, err := url.Parse(options.Connection)
		if err != nil || connection.Scheme != "unix" || connection.Host != "" || !strings.HasPrefix(connection.Path, "/") || connection.RawQuery != "" || connection.Fragment != "" {
			m.configErr = errors.New("browser connection must be a local unix:/// Podman worker socket; use an SSH tunnel for browser access")
		}
	}
	return m
}

func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	if m.options.Connection != "" {
		args = append([]string{"--remote", "--url", m.options.Connection}, args...)
	}
	output, err := m.runner.Run(ctx, m.options.Binary, args...)
	if err != nil {
		return output, fmt.Errorf("podman operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (m *Manager) rootless(ctx context.Context) error {
	if m.configErr != nil {
		return m.configErr
	}
	if m.uid() == 0 {
		return errors.New("browser manager must run as a non-root user")
	}
	output, err := m.run(ctx, "info", "--format", "json")
	if err != nil {
		return err
	}
	var info struct {
		Host struct {
			Security struct {
				Rootless bool `json:"rootless"`
			} `json:"security"`
		} `json:"host"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("read Podman security status: %w", err)
	}
	if !info.Host.Security.Rootless {
		return errors.New("browser worker must use rootless Podman; rootful engines are refused")
	}
	return nil
}

type containerInfo struct {
	State struct {
		Running bool `json:"Running"`
		Health  struct {
			Status string `json:"Status"`
		} `json:"Health"`
		Healthcheck struct {
			Status string `json:"Status"`
		} `json:"Healthcheck"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (m *Manager) inspect(ctx context.Context) (*containerInfo, error) {
	_, err := m.run(ctx, "container", "exists", m.options.Name)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	output, err := m.run(ctx, "inspect", "--type", "container", m.options.Name)
	if err != nil {
		return nil, err
	}
	var containers []containerInfo
	if err := json.Unmarshal(output, &containers); err != nil || len(containers) != 1 {
		return nil, errors.New("invalid Podman container inspection response")
	}
	if containers[0].Config.Labels[managedLabel] != "1" {
		return nil, errors.New("browser name belongs to an unmanaged container; refusing to change it")
	}
	return &containers[0], nil
}

func (m *Manager) ready(ctx context.Context, container *containerInfo) (Status, error) {
	if container == nil || !container.State.Running {
		return Status{}, nil
	}
	browserURL := fmt.Sprintf("http://127.0.0.1:%d/vnc.html?autoconnect=true&resize=remote", m.options.Port)
	health := container.State.Health.Status
	if health == "" {
		health = container.State.Healthcheck.Status
	}
	if health != "" && health != "healthy" {
		return Status{}, fmt.Errorf("browser container health is %s", health)
	}
	// Check the actual supervised processes even when inspect still reports
	// the previous periodic healthcheck's healthy result.
	if _, err := m.run(ctx, "healthcheck", "run", m.options.Name); err != nil {
		return Status{}, fmt.Errorf("browser process health check: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, browserURL, nil)
	if err != nil {
		return Status{}, err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return Status{}, fmt.Errorf("browser noVNC endpoint is not ready: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("browser noVNC returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil || !strings.Contains(string(body), "noVNC") {
		return Status{}, errors.New("browser endpoint did not serve the noVNC page")
	}
	return Status{Running: true, URL: browserURL}, nil
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.rootless(ctx); err != nil {
		return Status{}, err
	}
	container, err := m.inspect(ctx)
	if err != nil {
		return Status{}, err
	}
	return m.ready(ctx, container)
}

func (m *Manager) Start(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.rootless(ctx); err != nil {
		return Status{}, err
	}
	container, err := m.inspect(ctx)
	if err != nil {
		return Status{}, err
	}
	if container != nil && !container.State.Running {
		if _, err := m.run(ctx, "rm", m.options.Name); err != nil {
			return Status{}, err
		}
		container = nil
	}
	if container == nil {
		args := []string{
			"run", "--detach", "--pull=never", "--name", m.options.Name,
			"--label", managedLabel + "=1", "--user", "10001:10001",
			"--cap-drop=ALL", "--security-opt=no-new-privileges",
			"--read-only", "--memory=1024m", "--memory-swap=1024m", "--cpus=2", "--pids-limit=256",
			"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=256m,mode=1777",
			"--tmpfs", "/run:rw,nosuid,nodev,noexec,size=16m,mode=755",
			"--tmpfs", "/home/browser:rw,nosuid,nodev,noexec,size=128m,uid=10001,gid=10001,mode=700",
			"--shm-size=256m", "--network=slirp4netns:allow_host_loopback=false",
			"--publish", "127.0.0.1:" + strconv.Itoa(m.options.Port) + ":6080/tcp",
			m.options.Image,
		}
		if _, err := m.run(ctx, args...); err != nil {
			return Status{}, err
		}
	}
	readyCtx, cancel := context.WithTimeout(ctx, m.readyWait)
	defer cancel()
	var lastErr error
	for {
		container, err := m.inspect(readyCtx)
		if err == nil && container != nil && container.State.Running {
			// Explicitly check supervised processes so a stale healthy value
			// cannot hide a dead browser/display/VNC process.
			_, err = m.run(readyCtx, "healthcheck", "run", m.options.Name)
			if err == nil {
				container.State.Health.Status = "healthy"
				if status, readyErr := m.ready(readyCtx, container); readyErr == nil {
					return status, nil
				} else {
					err = readyErr
				}
			}
		} else if err == nil {
			return Status{}, errors.New("browser container exited before becoming ready; inspect its logs")
		}
		lastErr = err
		timer := time.NewTimer(m.retryWait)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			return Status{}, fmt.Errorf("browser did not become ready: %w (last check: %v)", readyCtx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func (m *Manager) Stop(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.rootless(ctx); err != nil {
		return Status{}, err
	}
	container, err := m.inspect(ctx)
	if err != nil || container == nil {
		return Status{}, err
	}
	if container.State.Running {
		if _, err := m.run(ctx, "stop", "--time", "10", m.options.Name); err != nil {
			return Status{}, err
		}
	}
	if _, err := m.run(ctx, "rm", m.options.Name); err != nil {
		return Status{}, err
	}
	return Status{}, nil
}
