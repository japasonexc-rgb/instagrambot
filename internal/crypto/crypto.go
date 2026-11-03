// Package crypto provides AES-256-GCM encryption at rest for sensitive
// database fields (Instagram access tokens, per-account proxy URLs).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

type Box struct {
	gcm cipher.AEAD
}

func New(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{gcm: gcm}, nil
}

// Encrypt returns base64(nonce || ciphertext).
func (b *Box) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := b.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (b *Box) EncryptString(plaintext string) (string, error) {
	return b.Encrypt([]byte(plaintext))
}

func (b *Box) Decrypt(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	nonceSize := b.gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return b.gcm.Open(nil, nonce, ciphertext, nil)
}

func (b *Box) DecryptString(encoded string) (string, error) {
	pt, err := b.Decrypt(encoded)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
