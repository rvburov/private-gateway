package xray

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Settings       Settings
	ConfigPath     string
	Binary         string
	RestartCommand []string
}

type Manager struct {
	options Options
	mu      sync.Mutex
}

func NewManager(options Options) *Manager {
	if options.Binary == "" {
		options.Binary = "xray"
	}
	options.RestartCommand = append([]string(nil), options.RestartCommand...)
	return &Manager{options: options}
}

// Validate вызывает собственный разбор конфигурации Xray. Рабочую конфигурацию он не меняет.
func (m *Manager) Validate(ctx context.Context, clients []Client) error {
	path, err := m.stage(ctx, clients)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	return m.validateFile(ctx, path, clients)
}

// Apply выполняет обновления по одному, проверяет их Xray и атомарно заменяет рабочую
// конфигурацию. При сбое перезапуска возвращает прежний файл, пробует перезапустить прежнюю
// конфигурацию и затем возвращает исходную ошибку.
func (m *Manager) Apply(ctx context.Context, clients []Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.options.RestartCommand) == 0 || m.options.RestartCommand[0] == "" {
		return errors.New("Xray restart command is required to apply a configuration")
	}
	path, err := m.stage(ctx, clients)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err := m.validateFile(ctx, path, clients); err != nil {
		return err
	}
	previous, existed, err := readPrevious(m.options.ConfigPath)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(path, m.options.ConfigPath); err != nil {
		return fmt.Errorf("replace Xray configuration: %w", err)
	}
	if err := m.restart(ctx, clients); err != nil {
		// Истёкшее время вызывающего не должно мешать восстановлению работающей службы.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if restoreErr := restore(m.options.ConfigPath, previous, existed); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore previous Xray configuration: %w", restoreErr))
		}
		if restartErr := m.restart(rollbackCtx, clients); restartErr != nil {
			return errors.Join(err, fmt.Errorf("restart restored Xray configuration: %w", restartErr))
		}
		return err
	}
	return nil
}

func (m *Manager) stage(ctx context.Context, clients []Client) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(m.options.ConfigPath) == "" {
		return "", errors.New("Xray configuration path is required")
	}
	data, err := Render(m.options.Settings, clients)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(m.options.ConfigPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create Xray configuration directory: %w", err)
	}
	return writeTemporary(dir, data)
}

func (m *Manager) validateFile(ctx context.Context, path string, clients []Client) error {
	return m.run(ctx, "validate Xray configuration", m.options.Binary, []string{"run", "-test", "-config", path}, clients)
}

func (m *Manager) restart(ctx context.Context, clients []Client) error {
	return m.run(ctx, "restart Xray", m.options.RestartCommand[0], m.options.RestartCommand[1:], clients)
}

func (m *Manager) run(ctx context.Context, action, binary string, args []string, clients []Client) error {
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, binary, args...)
	// Дочерний процесс, оставивший открытыми дескрипторы вывода, не должен блокировать
	// вызывающего.
	cmd.WaitDelay = time.Second
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if commandCtx.Err() != nil {
			err = commandCtx.Err()
		}
		message := output.String()
		for _, secret := range append([]string{m.options.Settings.PrivateKey}, credentialIDs(clients)...) {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
		}
		// Скрыть секреты до обрезки, с запасом байтов, чтобы узнать и секрет, который
		// пересекает границу показанного вывода.
		if len(message) > 4096 {
			message = message[:4096] + " [output truncated]"
		}
		if message == "" {
			return fmt.Errorf("%s: %w", action, err)
		}
		return fmt.Errorf("%s: %w (%s)", action, err, message)
	}
	return nil
}

func credentialIDs(clients []Client) []string {
	ids := make([]string, 0, len(clients)*2)
	for _, client := range clients {
		ids = append(ids, client.ID, strings.ToLower(client.ID))
	}
	return ids
}

type limitedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if remaining := 4096 + 64 - w.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.buffer.Write(p)
	}
	return n, nil
}

func (w *limitedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.buffer.String())
}

func readPrevious(path string) ([]byte, bool, error) {
	stat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect existing Xray configuration: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return nil, false, errors.New("existing Xray configuration must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read existing Xray configuration: %w", err)
	}
	return data, true, nil
}

func writeTemporary(dir string, data []byte) (path string, err error) {
	file, err := os.CreateTemp(dir, ".private-gateway-xray-*.json")
	if err != nil {
		return "", fmt.Errorf("create temporary Xray configuration: %w", err)
	}
	path = file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = io.Copy(file, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("write Xray configuration: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("sync Xray configuration: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close Xray configuration: %w", err)
	}
	return path, nil
}

func replaceFile(staged, path string) error { return os.Rename(staged, path) }

func restore(path string, previous []byte, existed bool) error {
	if !existed {
		return os.Remove(path)
	}
	staged, err := writeTemporary(filepath.Dir(path), previous)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	return replaceFile(staged, path)
}
