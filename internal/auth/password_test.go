package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("rahasia-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("format hash tidak sesuai: %s", h)
	}
	if strings.Contains(h, "rahasia-123") {
		t.Fatal("hash mengandung plaintext")
	}
	ok, err := VerifyPassword("rahasia-123", h)
	if err != nil || !ok {
		t.Fatalf("password benar ditolak: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("salah", h)
	if err != nil || ok {
		t.Fatalf("password salah diterima: ok=%v err=%v", ok, err)
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	a, _ := HashPassword("sama")
	b, _ := HashPassword("sama")
	if a == b {
		t.Fatal("dua hash untuk password yang sama identik; salt tidak acak")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "plaintext", "$argon2id$v=19$m=1$x$y", "$bcrypt$...$$$"} {
		if ok, err := VerifyPassword("x", h); ok || err == nil {
			t.Errorf("hash rusak %q tidak ditolak (ok=%v err=%v)", h, ok, err)
		}
	}
}

func TestPasswordTooLong(t *testing.T) {
	long := strings.Repeat("a", MaxPasswordBytes+1)
	if _, err := HashPassword(long); err == nil {
		t.Fatal("password terlalu panjang seharusnya ditolak")
	}
}

func TestHashConcurrencyIsLimited(t *testing.T) {
	hash, err := HashPassword("rahasia-123")
	if err != nil {
		t.Fatal(err)
	}
	old := hashWait
	hashWait = 50 * time.Millisecond
	defer func() { hashWait = old }()

	// Semua slot dipakai, jadi hash berikutnya harus menyerah, bukan ikut jalan.
	for i := 0; i < maxConcurrentHashes; i++ {
		hashSlots <- struct{}{}
	}
	_, err = VerifyPassword("rahasia-123", hash)
	if VerifyAgainstDummy("apa saja") != ErrHashBusy || err != ErrHashBusy {
		t.Fatalf("saat semua slot terpakai: err=%v, want ErrHashBusy", err)
	}
	for i := 0; i < maxConcurrentHashes; i++ {
		<-hashSlots
	}
	if ok, err := VerifyPassword("rahasia-123", hash); !ok || err != nil {
		t.Fatalf("setelah slot kosong: ok=%v err=%v", ok, err)
	}
}
