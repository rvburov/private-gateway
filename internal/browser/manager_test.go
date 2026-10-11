package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeExit int

func (e fakeExit) Error() string { return fmt.Sprintf("exit %d", int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

type fakeRunner struct {
	calls     [][]string
	rootless  bool
	exists    bool
	running   bool
	managed   bool
	health    string
	healthErr error
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 2 && args[0] == "--remote" {
		args = args[3:]
	}
	switch args[0] {
	case "info":
		return []byte(fmt.Sprintf(`{"host":{"security":{"rootless":%t}}}`, f.rootless)), nil
	case "container":
		if !f.exists {
			return nil, fakeExit(1)
		}
		return nil, nil
	case "inspect":
		managed := "0"
		if f.managed {
			managed = "1"
		}
		return []byte(fmt.Sprintf(`[{"State":{"Running":%t,"Health":{"Status":%q}},"Config":{"Labels":{"%s":%q}}}]`, f.running, f.health, managedLabel, managed)), nil
	case "run":
		f.exists, f.running, f.managed = true, true, true
		return []byte("container-id"), nil
	case "healthcheck":
		return nil, f.healthErr
	case "stop":
		f.running = false
		return nil, nil
	case "rm":
		f.exists, f.running = false, false
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected command: %v", args)
	}
}

type fakeHTTP struct {
	status int
	body   string
	err    error
	calls  int
}

func (h *fakeHTTP) Do(request *http.Request) (*http.Response, error) {
	h.calls++
	if request.URL.Host != "127.0.0.1:6080" {
		return nil, errors.New("unexpected non-loopback request")
	}
	if h.err != nil {
		return nil, h.err
	}
	return &http.Response{StatusCode: h.status, Body: io.NopCloser(strings.NewReader(h.body))}, nil
}

func testManager(f *fakeRunner) *Manager {
	m := NewManager(Options{})
	m.runner = f
	m.uid = func() int { return 1000 }
	m.client = &fakeHTTP{status: http.StatusOK, body: "<html>noVNC</html>"}
	m.readyWait, m.retryWait = 10*time.Millisecond, time.Millisecond
	return m
}

func TestStartEnforcesHardenedRootlessContainer(t *testing.T) {
	f := &fakeRunner{rootless: true}
	m := testManager(f)
	status, err := m.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !strings.HasPrefix(status.URL, "http://127.0.0.1:6080/") {
		t.Fatalf("unexpected status: %+v", status)
	}
	var run []string
	for _, args := range f.calls {
		if args[0] == "run" {
			run = args
		}
	}
	for _, required := range []string{
		"--pull=never", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--read-only", "--memory=1024m", "--memory-swap=1024m", "--cpus=2",
		"--pids-limit=256", "--network=slirp4netns:allow_host_loopback=false",
		"127.0.0.1:6080:6080/tcp", "10001:10001",
	} {
		if !contains(run, required) {
			t.Errorf("missing security option %q: %v", required, run)
		}
	}
	tmpfs := 0
	for _, arg := range run {
		if arg == "--tmpfs" {
			tmpfs++
		}
		if arg == "--privileged" || arg == "--volume" || arg == "-v" || strings.Contains(arg, "unconfined") || arg == "--pid=host" {
			t.Errorf("unsafe container option %q", arg)
		}
	}
	if tmpfs != 3 {
		t.Fatalf("expected three explicit tmpfs mounts, got %d", tmpfs)
	}
}

func contains(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func TestRefusesRootfulEngineAndRootUser(t *testing.T) {
	for _, test := range []struct {
		name     string
		rootless bool
		uid      int
	}{
		{"rootful engine", false, 1000}, {"root host user", true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &fakeRunner{rootless: test.rootless}
			m := testManager(f)
			m.uid = func() int { return test.uid }
			if _, err := m.Start(context.Background()); err == nil {
				t.Fatal("unsafe engine accepted")
			}
			for _, args := range f.calls {
				if args[0] != "info" {
					t.Fatalf("mutated unsafe engine: %v", args)
				}
			}
		})
	}
}

func TestReadinessRequiresHealthyProcessesAndActualNoVNC(t *testing.T) {
	for _, test := range []struct {
		name      string
		healthErr error
		http      *fakeHTTP
	}{
		{"unhealthy process", errors.New("chromium is dead"), &fakeHTTP{status: 200, body: "noVNC"}},
		{"HTTP unavailable", nil, &fakeHTTP{err: errors.New("connection refused")}},
		{"HTTP failure", nil, &fakeHTTP{status: 503, body: "noVNC"}},
		{"different page", nil, &fakeHTTP{status: 200, body: "gateway"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &fakeRunner{rootless: true, healthErr: test.healthErr}
			m := testManager(f)
			m.client = test.http
			status, err := m.Start(context.Background())
			if err == nil || status.Running || status.URL != "" {
				t.Fatalf("false readiness: %+v %v", status, err)
			}
		})
	}
}

func TestStatusMissingAndUnhealthy(t *testing.T) {
	f := &fakeRunner{rootless: true}
	m := testManager(f)
	status, err := m.Status(context.Background())
	if err != nil || status.Running || status.URL != "" {
		t.Fatalf("missing: %+v %v", status, err)
	}
	f.exists, f.running, f.managed, f.health = true, true, true, "unhealthy"
	if status, err = m.Status(context.Background()); err == nil || status.Running {
		t.Fatalf("unhealthy status: %+v %v", status, err)
	}
}

func TestStatusRejectsStaleHealthyContainerAfterProcessFailure(t *testing.T) {
	f := &fakeRunner{
		rootless: true, exists: true, running: true, managed: true,
		health: "healthy", healthErr: errors.New("Chromium exited"),
	}
	m := testManager(f)
	if status, err := m.Status(context.Background()); err == nil || status.Running || status.URL != "" {
		t.Fatalf("stale healthy status accepted: %+v %v", status, err)
	}
}

func TestNeverChangesUnmanagedContainer(t *testing.T) {
	for _, operation := range []string{"start", "stop"} {
		t.Run(operation, func(t *testing.T) {
			f := &fakeRunner{rootless: true, exists: true, running: true}
			m := testManager(f)
			var err error
			if operation == "start" {
				_, err = m.Start(context.Background())
			} else {
				_, err = m.Stop(context.Background())
			}
			if err == nil {
				t.Fatal("unmanaged container accepted")
			}
			for _, args := range f.calls {
				if args[0] == "run" || args[0] == "stop" || args[0] == "rm" {
					t.Fatalf("changed unmanaged container: %v", args)
				}
			}
		})
	}
}

func TestStopDeletesEphemeralSessionAndIsIdempotent(t *testing.T) {
	f := &fakeRunner{rootless: true, exists: true, running: true, managed: true}
	m := testManager(f)
	for i := 0; i < 2; i++ {
		status, err := m.Stop(context.Background())
		if err != nil || status.Running || status.URL != "" {
			t.Fatalf("stop: %+v %v", status, err)
		}
	}
	if f.exists {
		t.Fatal("ephemeral browser retained after stop")
	}
}

func TestLocalWorkerSocketPrefixAndRejectsOffHostWorker(t *testing.T) {
	m := NewManager(Options{Connection: "unix:///run/user/1002/podman/podman.sock"})
	f := &fakeRunner{rootless: true}
	m.runner, m.uid = f, func() int { return 1001 }
	if _, err := m.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.calls {
		if !reflect.DeepEqual(args[:3], []string{"--remote", "--url", m.options.Connection}) {
			t.Fatalf("missing worker prefix: %v", args)
		}
	}
	for _, connection := range []string{"ssh://worker", "tcp://127.0.0.1:8080", "unix://worker/sock", "unix:relative"} {
		unsafe := NewManager(Options{Connection: connection})
		if _, err := unsafe.Start(context.Background()); err == nil {
			t.Fatalf("accepted worker %q", connection)
		}
	}
}

func TestStartExitedContainerFailsInsteadOfReadiness(t *testing.T) {
	f := &fakeRunner{rootless: true}
	m := testManager(f)
	m.runner = exitedRunner{fake: f}
	if status, err := m.Start(context.Background()); err == nil || status.Running {
		t.Fatalf("exited container ready: %+v %v", status, err)
	}
}

type exitedRunner struct{ fake *fakeRunner }

func (r exitedRunner) Run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	output, err := r.fake.Run(ctx, binary, args...)
	if args[0] == "run" {
		r.fake.running = false
	}
	return output, err
}
