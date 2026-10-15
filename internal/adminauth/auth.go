package adminauth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const iterations = 600_000

// Credentials are installed through the root-only terminal setup script.
// The password hash and TOTP seed are never returned by the HTTP API.
type Credentials struct {
	Username     string
	PasswordHash string
	TOTPSecret   string
}

func (c Credentials) Configured() bool { return c.Username != "" }

func (c Credentials) Validate() error {
	if c == (Credentials{}) {
		return nil // Existing installations stay usable until the terminal setup is run.
	}
	if len(c.Username) < 3 || len(c.Username) > 64 || strings.TrimSpace(c.Username) != c.Username || strings.ContainsAny(c.Username, "\r\n") {
		return errors.New("invalid administrator username")
	}
	parts := strings.Split(c.PasswordHash, ":")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" || parts[1] != strconv.Itoa(iterations) {
		return errors.New("invalid administrator password hash")
	}
	salt, e1 := hex.DecodeString(parts[2])
	digest, e2 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(digest) != 32 {
		return errors.New("invalid administrator password hash")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(c.TOTPSecret)
	if err != nil || len(secret) < 20 {
		return errors.New("invalid administrator TOTP secret")
	}
	return nil
}

func (c Credentials) Verify(username, password, code string, now time.Time) (int64, bool) {
	if !c.Configured() || len(password) > 1024 || len(code) != 6 {
		return 0, false
	}
	parts := strings.Split(c.PasswordHash, ":")
	salt, _ := hex.DecodeString(parts[2])
	expected, _ := hex.DecodeString(parts[3])
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(username), []byte(c.Username)) != 1 || subtle.ConstantTimeCompare(actual, expected) != 1 {
		return 0, false
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(c.TOTPSecret)
	current := now.Unix() / 30
	for _, step := range []int64{current - 1, current, current + 1} {
		if step >= 0 && subtle.ConstantTimeCompare([]byte(code), []byte(Code(secret, step))) == 1 {
			return step, true
		}
	}
	return 0, false
}

// Code implements RFC 6238 (HMAC-SHA1, 30 seconds, 6 digits).
func Code(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	n := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmtCode(n % 1_000_000)
}

func fmtCode(n uint32) string {
	s := strconv.FormatUint(uint64(n), 10)
	return strings.Repeat("0", 6-len(s)) + s
}

func NewSessionToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
