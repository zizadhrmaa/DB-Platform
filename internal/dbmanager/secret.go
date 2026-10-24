package dbmanager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// SecretBox mengenkripsi password database target dengan AES-256-GCM.
// Kalau APP_SECRET_KEY diganti, password lama tidak bisa dibuka lagi dan
// harus diisi ulang oleh admin.
type SecretBox struct {
	aead cipher.AEAD
}

var ErrDecrypt = errors.New("dbmanager: gagal mendekripsi kredensial (APP_SECRET_KEY berubah?)")

// NewSecretBoxFromBase64 menerima base64 standar maupun URL-safe.
func NewSecretBoxFromBase64(b64 string) (*SecretBox, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(b64)
	}
	if err != nil {
		return nil, errors.New("dbmanager: APP_SECRET_KEY bukan base64 yang valid")
	}
	return NewSecretBox(key)
}

func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("dbmanager: APP_SECRET_KEY harus 32 byte (dapat %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

// Encrypt menaruh nonce di depan ciphertext.
func (s *SecretBox) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (s *SecretBox) Decrypt(data []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(data) < n {
		return "", ErrDecrypt
	}
	pt, err := s.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}