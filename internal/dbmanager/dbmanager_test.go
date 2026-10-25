package dbmanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestSecretBoxRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	box, err := NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := box.Encrypt("p@ss:w/rd?")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("p@ss")) {
		t.Fatal("ciphertext memuat plaintext")
	}
	pt, err := box.Decrypt(ct)
	if err != nil || pt != "p@ss:w/rd?" {
		t.Fatalf("decrypt = %q, %v", pt, err)
	}

	other := make([]byte, 32)
	rand.Read(other)
	box2, _ := NewSecretBox(other)
	if _, err := box2.Decrypt(ct); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("key salah: err=%v, want ErrDecrypt", err)
	}
	ct[len(ct)-1] ^= 1
	if _, err := box.Decrypt(ct); !errors.Is(err, ErrDecrypt) {
		t.Fatal("ciphertext yang dimodifikasi tidak ditolak")
	}
}

func TestSecretBoxKeyLength(t *testing.T) {
	if _, err := NewSecretBox(make([]byte, 16)); err == nil {
		t.Fatal("key 16 byte seharusnya ditolak")
	}
	if _, err := NewSecretBoxFromBase64("bukan base64!!"); err == nil {
		t.Fatal("base64 tidak valid seharusnya ditolak")
	}
}

func TestBuildDSNEscapesPassword(t *testing.T) {
	dsn := BuildDSN(ConnectionInfo{Host: "db", Port: 5432, Database: "toko", Username: "u"}, "a@b:c/d?e")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if pw, _ := u.User.Password(); pw != "a@b:c/d?e" {
		t.Fatalf("password rusak setelah escape: %q", pw)
	}
	if u.Host != "db:5432" || u.Path != "/toko" || u.Query().Get("sslmode") != "disable" {
		t.Fatalf("dsn salah: %s", dsn)
	}
}

func TestBuildDSNMySQL(t *testing.T) {
	c := ConnectionInfo{
		DBType:   DBTypeMySQL,
		Host:     "127.0.0.1",
		Port:     3306,
		Database: "kampus",
		Username: "app_user",
		SSLMode:  "disable",
	}
	dsn := BuildDSN(c, "p@ss:w/rd?")
	if !strings.Contains(dsn, "app_user:") || !strings.Contains(dsn, "@tcp(127.0.0.1:3306)/kampus") {
		t.Fatalf("format dsn mysql salah: %s", dsn)
	}
	if !strings.Contains(dsn, "multiStatements=true") || !strings.Contains(dsn, "parseTime=true") {
		t.Fatalf("parameter penting multiStatements/parseTime tidak ada: %s", dsn)
	}

	cReq := c
	cReq.SSLMode = "require"
	dsnReq := BuildDSN(cReq, "pass")
	if !strings.Contains(dsnReq, "tls=skip-verify") {
		t.Fatalf("tls require salah: %s", dsnReq)
	}

	cFull := c
	cFull.SSLMode = "verify-full"
	dsnFull := BuildDSN(cFull, "pass")
	if !strings.Contains(dsnFull, "tls=true") {
		t.Fatalf("tls verify-full salah: %s", dsnFull)
	}
}

func TestSanitizeErrorHidesDetails(t *testing.T) {
	raw := errors.New(`failed to connect to user=admin password=supersecret host=db`)
	err := SanitizeError(raw)
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatal("error masih memuat password")
	}
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("kategori error hilang")
	}
	if !errors.Is(SanitizeError(context.DeadlineExceeded), domain.ErrUnavailable) {
		t.Fatal("timeout harus ErrUnavailable")
	}
}

func TestStatic(t *testing.T) {
	m := NewStatic(nil)
	if _, err := m.Conn(context.Background(), 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err=%v, want NotFound", err)
	}
}
