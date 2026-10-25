package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Hash disimpan dalam format PHC supaya parameternya ikut tersimpan:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
	saltLen   uint32
	keyLen    uint32
}

var defaultArgon = argonParams{memoryKiB: 64 * 1024, time: 3, threads: 2, saltLen: 16, keyLen: 32}

// MaxPasswordBytes mencegah password raksasa membuat hashing lambat.
const MaxPasswordBytes = 256

const MinPasswordLen = 8

var (
	ErrInvalidHash     = errors.New("auth: format password hash tidak valid")
	ErrPasswordTooLong = errors.New("auth: password terlalu panjang")
	ErrHashBusy        = errors.New("auth: terlalu banyak proses hashing password")
)

// Setiap hash memakai 64 MiB memori. Tanpa batas, login paralel dalam
// jumlah besar bisa menghabiskan memori server.
const maxConcurrentHashes = 4

var (
	hashSlots = make(chan struct{}, maxConcurrentHashes)
	hashWait  = 10 * time.Second
)

func argonKey(password string, salt []byte, p argonParams) ([]byte, error) {
	timer := time.NewTimer(hashWait)
	defer timer.Stop()
	select {
	case hashSlots <- struct{}{}:
	case <-timer.C:
		return nil, ErrHashBusy
	}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, p.time, p.memoryKiB, p.threads, p.keyLen), nil
}

func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	p := defaultArgon
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argonKey(password, salt, p)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword mengembalikan (false, nil) untuk password salah.
// Error hanya muncul kalau hash-nya rusak.
func VerifyPassword(password, encoded string) (bool, error) {
	if len(password) > MaxPasswordBytes {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.time, &p.threads); err != nil {
		return false, ErrInvalidHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrInvalidHash
	}
	p.keyLen = uint32(len(want))
	got, err := argonKey(password, salt, p)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var dummyHash, _ = HashPassword("dummy-password-for-timing")

// VerifyAgainstDummy dipanggil saat username tidak ditemukan, supaya waktu
// respons sama dan keberadaan username tidak bisa ditebak dari timing.
// Error-nya hanya ErrHashBusy, dan harus diperlakukan sama dengan user yang
// ada supaya kondisi server sibuk juga tidak membocorkan username.
func VerifyAgainstDummy(password string) error {
	_, err := VerifyPassword(password, dummyHash)
	return err
}
