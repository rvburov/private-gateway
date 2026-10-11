package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

type Cipher struct{ aead cipher.AEAD }

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
	return &Cipher{aead}, nil
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
