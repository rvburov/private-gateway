package secure

import (
	"encoding/base64"
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
