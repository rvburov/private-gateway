package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Скомпилированный тестовый бинарник подменяет и Xray, и программу перезапуска: так
// проверяется настоящий запуск процессов без установки поддельных бинарников.
func TestMain(m *testing.M) {
	if os.Getenv("PRIVATE_GATEWAY_XRAY_TEST_HELPER") == "1" {
		os.Exit(helperCommand(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func helperCommand(args []string) int {
	if len(args) == 4 && args[0] == "run" && args[1] == "-test" && args[2] == "-config" {
		data, err := os.ReadFile(args[3])
		if err != nil || !json.Valid(data) {
			return 2
		}
		if os.Getenv("PRIVATE_GATEWAY_XRAY_TEST_ECHO_CONFIG") == "1" {
			fmt.Fprintln(os.Stderr, string(data))
			fmt.Fprintln(os.Stderr, os.Getenv("PRIVATE_GATEWAY_XRAY_TEST_ERROR_TEXT"))
			return 3
		}
		if os.Getenv("PRIVATE_GATEWAY_XRAY_TEST_INVALID") == "1" {
			fmt.Fprintln(os.Stderr, testSettings().PrivateKey, testID)
			return 3
		}
		return 0
	}
	if len(args) >= 3 && args[0] == "restart" {
		path, mode := args[1], args[2]
		data, _ := os.ReadFile(path)
		count, _ := strconv.Atoi(string(data))
		if err := os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0600); err != nil {
			return 4
		}
		if mode == "fail-first" && count == 0 || mode == "fail-always" {
			return 5
		}
		if mode == "wait" || mode == "wait-first" && count == 0 {
			time.Sleep(5 * time.Second)
		}
		return 0
	}
	return 6
}

func managerForTest(t *testing.T, mode string) (*Manager, string) {
	t.Helper()
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_HELPER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	countPath := filepath.Join(dir, "restart-count")
	m := NewManager(Options{
		Settings: testSettings(), ConfigPath: filepath.Join(dir, "xray.json"),
		Binary: binary, RestartCommand: []string{binary, "restart", countPath, mode},
	})
	return m, countPath
}

func TestValidateDoesNotReplaceConfig(t *testing.T) {
	m, _ := managerForTest(t, "success")
	old := []byte("existing configuration")
	if err := os.WriteFile(m.options.ConfigPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(context.Background(), []Client{{ID: testID}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(m.options.ConfigPath)
	if string(got) != string(old) {
		t.Fatal("validation replaced live config")
	}
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_INVALID", "1")
	err := m.Validate(context.Background(), []Client{{ID: testID}})
	if err == nil {
		t.Fatal("subprocess failure was accepted")
	}
	if strings.Contains(err.Error(), testID) || strings.Contains(err.Error(), testSettings().PrivateKey) {
		t.Fatal("validation error exposed a secret")
	}
	assertNoTemporaryFiles(t, m)
}

func TestApplyPermissionsAndSerializedRestarts(t *testing.T) {
	m, countPath := managerForTest(t, "success")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := m.Apply(context.Background(), []Client{{ID: testID}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	count, _ := os.ReadFile(countPath)
	if string(count) != "4" {
		t.Fatalf("restarts = %s, want 4 serialized restarts", count)
	}
	stat, err := os.Stat(m.options.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o, want 600", stat.Mode().Perm())
	}
	assertNoTemporaryFiles(t, m)
}

func TestApplyRollsBackAfterRestartFailure(t *testing.T) {
	m, countPath := managerForTest(t, "fail-first")
	old := []byte("previous configuration")
	if err := os.WriteFile(m.options.ConfigPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(context.Background(), []Client{{ID: testID}}); err == nil {
		t.Fatal("restart failure was accepted")
	}
	got, _ := os.ReadFile(m.options.ConfigPath)
	if string(got) != string(old) {
		t.Fatal("previous configuration was not restored")
	}
	count, _ := os.ReadFile(countPath)
	if string(count) != "2" {
		t.Fatalf("restarts = %s, want restart plus rollback restart", count)
	}
	assertNoTemporaryFiles(t, m)
}

func TestApplyRemovesNewConfigOnFailedFirstRestart(t *testing.T) {
	m, _ := managerForTest(t, "fail-first")
	if err := m.Apply(context.Background(), nil); err == nil {
		t.Fatal("restart failure was accepted")
	}
	if _, err := os.Stat(m.options.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new configuration was not removed")
	}
	assertNoTemporaryFiles(t, m)
}

func TestApplyValidationFailureLeavesLiveConfigAndDoesNotRestart(t *testing.T) {
	m, countPath := managerForTest(t, "success")
	old := []byte("previous configuration")
	if err := os.WriteFile(m.options.ConfigPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRIVATE_GATEWAY_XRAY_TEST_INVALID", "1")
	if err := m.Apply(context.Background(), nil); err == nil {
		t.Fatal("validation failure was accepted")
	}
	got, _ := os.ReadFile(m.options.ConfigPath)
	if string(got) != string(old) {
		t.Fatal("failed validation changed previous configuration")
	}
	if _, err := os.Stat(countPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed validation attempted restart")
	}
	assertNoTemporaryFiles(t, m)
}

func TestApplyCancellationRestoresConfigAndRestartsIndependently(t *testing.T) {
	m, countPath := managerForTest(t, "wait-first")
	old := []byte("previous configuration")
	if err := os.WriteFile(m.options.ConfigPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := m.Apply(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	got, _ := os.ReadFile(m.options.ConfigPath)
	if string(got) != string(old) {
		t.Fatal("caller cancellation prevented restoration")
	}
	count, _ := os.ReadFile(countPath)
	if string(count) != "2" {
		t.Fatalf("restarts = %s, want restart plus rollback restart", count)
	}
}

func TestApplyReportsFailedRollbackRestart(t *testing.T) {
	m, _ := managerForTest(t, "fail-always")
	old := []byte("previous configuration")
	if err := os.WriteFile(m.options.ConfigPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	err := m.Apply(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "restart restored Xray configuration") {
		t.Fatalf("error = %v, want rollback restart failure", err)
	}
	got, _ := os.ReadFile(m.options.ConfigPath)
	if string(got) != string(old) {
		t.Fatal("failed rollback restart prevented file restoration")
	}
}

func TestApplyRequiresRestartCommand(t *testing.T) {
	m, _ := managerForTest(t, "success")
	m.options.RestartCommand = nil
	if err := m.Apply(context.Background(), nil); err == nil {
		t.Fatal("missing restart command was accepted")
	}
	if _, err := os.Stat(m.options.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("config changed with no restart command")
	}
}

func TestApplyRejectsSymlink(t *testing.T) {
	m, _ := managerForTest(t, "success")
	target := filepath.Join(filepath.Dir(m.options.ConfigPath), "other.json")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, m.options.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(context.Background(), nil); err == nil {
		t.Fatal("symlink config was accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "unchanged" {
		t.Fatal("symlink target changed")
	}
}

func TestCanceledContextDoesNotChangeConfig(t *testing.T) {
	m, _ := managerForTest(t, "success")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Apply(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if _, err := os.Stat(m.options.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled operation changed config")
	}
}

func TestRealXrayValidation(t *testing.T) {
	binary := os.Getenv("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
	if binary == "" {
		t.Skip("set PRIVATE_GATEWAY_REAL_XRAY_BINARY to verify with a real Xray installation")
	}
	m := NewManager(Options{
		Settings: testSettings(), Binary: binary,
		ConfigPath: filepath.Join(t.TempDir(), "xray.json"),
	})
	for _, clients := range [][]Client{nil, {{ID: testID, Name: "device"}}} {
		if err := m.Validate(context.Background(), clients); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNoTemporaryFiles(t *testing.T, m *Manager) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(m.options.ConfigPath), ".private-gateway-xray-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary config files left behind: %v, %v", files, err)
	}
}
