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
	"fmt"
	"strconv"
	"strings"
)

type Cipher struct {
	aead         cipher.AEAD
	subscription []byte
	proxyClient  []byte
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
	// У ссылок-подписок свой ключ: сам ключ шифрования для них никогда не используется.
	subscription, err := hkdf.Key(sha256.New, key, nil, "private-gateway subscription link v1", 32)
	if err != nil {
		return nil, err
	}
	proxyClient, err := hkdf.Key(sha256.New, key, nil, "private-gateway proxy VLESS credential v1", 32)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, subscription: subscription, proxyClient: proxyClient}, nil
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

// SubscriptionToken — секретная часть ссылки-подписки устройства: 43 символа base64url без
// выравнивания. Он выводится из главного ключа, поэтому база хранит только его хеш, а новое
// поколение того же устройства даёт новую ссылку, никак не связанную с прежней.
func (c *Cipher) SubscriptionToken(deviceID string, generation int64) string {
	mac := hmac.New(sha256.New, c.subscription)
	mac.Write([]byte(strings.ToLower(deviceID) + "/" + strconv.FormatInt(generation, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ProxyCredential выводит отдельный UUID для выхода через прокси. Знание ID устройства
// или основного UUID не позволяет получить этот секрет без главного ключа сервера.
func (c *Cipher) ProxyCredential(deviceID string) string {
	mac := hmac.New(sha256.New, c.proxyClient)
	mac.Write([]byte(strings.ToLower(deviceID)))
	var id [16]byte
	copy(id[:], mac.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

// SubscriptionHash — то, что хранит база, чтобы найти устройство по его ссылке.
func SubscriptionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
