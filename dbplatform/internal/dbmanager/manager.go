// Package dbmanager mengelola koneksi ke DATABASE TARGET (Owner: Orang 3).
//
// Pemisahan tanggung jawab (sesuai spesifikasi):
//   - dbmanager TIDAK memeriksa hak akses. Pemanggil WAJIB sudah lolos
//     authz (route dibungkus authz.RequireDB) sebelum memanggil Conn.
//   - authz tidak tahu cara konek ke database target.
package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/kelompok/dbplatform/internal/domain"
)

// Manager adalah KONTRAK yang dipakai Orang 3 (viewer) dan Orang 4 (migration).
type Manager interface {
	// Conn mengembalikan pool koneksi (*sql.DB) ke database target.
	// Pool di-cache per dbID dan dipakai bersama; pemanggil TIDAK BOLEH
	// memanggil Close() pada hasilnya.
	//
	// Error (sudah dibungkus error domain agar httpx.Error tepat):
	//   ErrConnNotFound    (domain.ErrNotFound)    -> id tidak ada
	//   ErrConnInactive    (domain.ErrNotFound)    -> koneksi dinonaktifkan admin
	//   ErrConnUnavailable (domain.ErrUnavailable) -> host mati / auth DB gagal / timeout
	// Pesan error TIDAK BOLEH memuat password atau DSN lengkap.
	Conn(ctx context.Context, dbID int64) (*sql.DB, error)

	// Invalidate menutup & membuang pool dbID dari cache. WAJIB dipanggil
	// setelah admin mengubah/menonaktifkan/menghapus koneksi, supaya
	// kredensial lama tidak terus dipakai.
	Invalidate(dbID int64)

	// Close menutup semua pool (dipanggil saat server shutdown).
	Close() error
}

var (
	ErrConnNotFound    = fmt.Errorf("%w: koneksi database tidak ditemukan", domain.ErrNotFound)
	ErrConnInactive    = fmt.Errorf("%w: koneksi database dinonaktifkan", domain.ErrNotFound)
	ErrConnUnavailable = fmt.Errorf("%w: database target tidak dapat dihubungi", domain.ErrUnavailable)
)

// ConnectionInfo adalah data koneksi yang AMAN ditampilkan (tanpa password).
// Struct ini yang dikirim ke template; jangan pernah kirim ciphertext/password.
type ConnectionInfo struct {
	ID       int64
	Name     string
	Host     string
	Port     int
	Database string
	Username string
	SSLMode  string
	IsActive bool
}

// BuildDSN menyusun URL koneksi PostgreSQL dengan escaping yang benar
// (password boleh mengandung @ : / ? dll). Hasilnya RAHASIA: jangan di-log.
func BuildDSN(c ConnectionInfo, password string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.Username, password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Database,
	}
	q := url.Values{}
	sslmode := c.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	q.Set("sslmode", sslmode)
	q.Set("connect_timeout", "5")
	q.Set("application_name", "dbplatform")
	u.RawQuery = q.Encode()
	return u.String()
}

// SanitizeError membuang detail yang mungkin memuat kredensial dari error
// koneksi, tetapi mempertahankan kategori untuk httpx.
func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w (timeout)", ErrConnUnavailable)
	}
	return ErrConnUnavailable
}
