package secure

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func TestCipher(t *testing.T) {
	c, err := New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Encrypt("private-client-uuid", "record1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Encrypt("private-client-uuid", "record1")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatal("nonce reused")
	}
	got, err := c.Decrypt(a, "record1")
	if err != nil || got != "private-client-uuid" {
		t.Fatalf("round trip %q %v", got, err)
	}
	if _, err := c.Decrypt(a, "record2"); err == nil {
		t.Fatal("cross-record substitution accepted")
	}
	a[len(a)-1] ^= 1
	if _, err := c.Decrypt(a, "record1"); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err := New("bad-key"); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestSubscriptionToken(t *testing.T) {
	c, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	const device = "6f0b8c3e-2d1a-4c5b-9e7f-0123456789ab"
	token := c.SubscriptionToken(device, 1)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(token) {
		t.Fatalf("token %q is not 32 bytes of unpadded base64url", token)
	}
	if c.SubscriptionToken(strings.ToUpper(device), 1) != token {
		t.Fatal("the same device in another letter case got another link")
	}
	for name, changed := range map[string]string{
		"generation": c.SubscriptionToken(device, 2),
		"device":     c.SubscriptionToken("6f0b8c3e-2d1a-4c5b-9e7f-0123456789ac", 1),
		"master key": other.SubscriptionToken(device, 1),
	} {
		if changed == token {
			t.Fatalf("another %s gave the same link", name)
		}
	}
	hash := SubscriptionHash(token)
	if len(hash) != 32 || bytes.Contains(hash, []byte(token)) || !bytes.Equal(hash, SubscriptionHash(token)) {
		t.Fatal("the stored hash must be a stable SHA-256 that does not contain the token")
	}
}
