package adminauth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialsAndTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := Code(secret, 59/30); got != "287082" {
		t.Fatalf("RFC 6238 code = %s", got)
	}
	salt := []byte("0123456789abcdef")
	derived, err := pbkdf2.Key(sha256.New, "correct horse battery", salt, iterations, 32)
	if err != nil {
		t.Fatal(err)
	}
	c := Credentials{Username: "admin", PasswordHash: "pbkdf2_sha256:600000:" + hex.EncodeToString(salt) + ":" + hex.EncodeToString(derived), TOTPSecret: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(59, 0)
	for _, candidate := range []struct {
		user, pass, code string
		valid            bool
	}{
		{"admin", "correct horse battery", "287082", true},
		{"wrong", "correct horse battery", "287082", false},
		{"admin", "wrong password", "287082", false},
		{"admin", "correct horse battery", "000000", false},
	} {
		_, ok := c.Verify(candidate.user, candidate.pass, candidate.code, now)
		if ok != candidate.valid {
			t.Errorf("verify(%q, %q): %v", candidate.user, candidate.code, ok)
		}
	}
	c.PasswordHash = strings.Replace(c.PasswordHash, "600000", "100", 1)
	if c.Validate() == nil {
		t.Fatal("weak hash accepted")
	}
}

func TestTOTPReplayStateSurvivesRestartAndSeedChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-totp.step")
	if step, err := ReadLastStep(path, "first-seed"); err != nil || step != -1 {
		t.Fatalf("new state: %d %v", step, err)
	}
	if err := SaveLastStep(path, "first-seed", 123); err != nil {
		t.Fatal(err)
	}
	if step, err := ReadLastStep(path, "first-seed"); err != nil || step != 123 {
		t.Fatalf("stored state: %d %v", step, err)
	}
	if step, err := ReadLastStep(path, "new-seed"); err != nil || step != -1 {
		t.Fatalf("new seed: %d %v", step, err)
	}
}
