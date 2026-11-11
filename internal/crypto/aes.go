package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

const encryptedPrefix = "enc:v1:"

// Cipher provides optional AES-256-GCM encryption at rest for cookie JSON payloads.
// If no key is configured, Encrypt and Decrypt act as a transparent pass-through.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher creates an AES-256-GCM cipher from a passphrase.
// If secret is empty, encryption is disabled (plaintext JSON stored).
func NewCipher(secret string) (*Cipher, error) {
	if strings.TrimSpace(secret) == "" {
		return &Cipher{aead: nil}, nil
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt encrypts plaintext into "enc:v1:<base64>" if a key is configured.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c == nil || c.aead == nil {
		return plaintext, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return encryptedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts "enc:v1:<base64>" back to plaintext.
// If the stored value does not have the encrypted prefix, it is returned as-is
// so migrating from unencrypted to encrypted storage works seamlessly.
func (c *Cipher) Decrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, encryptedPrefix) {
		return stored, nil
	}
	if c == nil || c.aead == nil {
		return "", errors.New("stored cookies are encrypted, but ENCRYPTION_KEY is not configured")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encryptedPrefix))
	if err != nil {
		return "", err
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
