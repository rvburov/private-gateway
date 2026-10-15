package adminauth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func secretID(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ReadLastStep ignores a step recorded for an earlier TOTP seed. Other malformed
// state fails closed so a restart cannot silently re-enable code replay.
func ReadLastStep(path, secret string) (int64, error) {
	if path == "" {
		return -1, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(string(data))
	if len(parts) != 2 {
		return 0, errors.New("invalid TOTP replay state")
	}
	if parts[0] != secretID(secret) {
		return -1, nil
	}
	step, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || step < 0 {
		return 0, errors.New("invalid TOTP replay step")
	}
	return step, nil
}

func SaveLastStep(path, secret string, step int64) error {
	if path == "" {
		return nil
	} // In-memory handler tests and local-only callers.
	file, err := os.CreateTemp(filepath.Dir(path), ".admin-totp-step-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := fmt.Fprintf(file, "%s %d\n", secretID(secret), step); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
