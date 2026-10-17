package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

type Cipher struct {
	aead         cipher.AEAD
	subscription []byte
}

func New(encodedKey string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("GATEWAY_MASTER_KEY must be base64 encoding of exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Subscription links get their own key, so the encryption key itself is never used for them.
	subscription, err := hkdf.Key(sha256.New, key, nil, "private-gateway subscription link v1", 32)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, subscription: subscription}, nil
}

func (c *Cipher) Encrypt(value, recordID string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(value), []byte(recordID)), nil
}

func (c *Cipher) Decrypt(value []byte, recordID string) (string, error) {
	n := c.aead.NonceSize()
	if len(value) < n {
		return "", errors.New("invalid encrypted credential")
	}
	plaintext, err := c.aead.Open(nil, value[:n], value[n:], []byte(recordID))
	if err != nil {
		return "", errors.New("credential authentication failed; check master key")
	}
	return string(plaintext), nil
}

// SubscriptionToken is the secret part of a device's subscription link: 43 characters of
// unpadded base64url. It is derived from the master key, so the database keeps only its hash,
// and a new generation of the same device gives a new, unrelated link.
func (c *Cipher) SubscriptionToken(deviceID string, generation int64) string {
	mac := hmac.New(sha256.New, c.subscription)
	mac.Write([]byte(strings.ToLower(deviceID) + "/" + strconv.FormatInt(generation, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SubscriptionHash is what the database stores to find a device by its link.
func SubscriptionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
