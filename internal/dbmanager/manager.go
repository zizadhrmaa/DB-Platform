// Package dbmanager mengelola koneksi ke database target.

// Package ini tidak mengecek hak akses. Pemanggil harus sudah lewat
// authz.RequireDB sebelum memanggil Conn.
package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Manager interface {
	// Conn mengembalikan pool yang di-cache per dbID. Jangan di-Close oleh pemanggil. 
	// Error-nya ErrConnNotFound, ErrConnInactive, atau
	// ErrConnUnavailable, dan tidak boleh memuat password atau DSN.
	Conn(ctx context.Context, dbID int64) (*sql.DB, error)

	// Invalidate dipanggil setelah admin mengubah atau menghapus koneksi
	// supaya kredensial lama tidak terus dipakai.
	Invalidate(dbID int64)

	Close() error
}

var (
	ErrConnNotFound    = fmt.Errorf("%w: koneksi database tidak ditemukan", domain.ErrNotFound)
	ErrConnInactive    = fmt.Errorf("%w: koneksi database dinonaktifkan", domain.ErrNotFound)
	ErrConnUnavailable = fmt.Errorf("%w: database target tidak dapat dihubungi", domain.ErrUnavailable)
)

// ConnectionInfo sengaja tidak punya field password, jadi aman dikirim ke template.
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

// BuildDSN memakai url.URL supaya password berisi @ : / ? tetap aman.
// Hasilnya berisi password, jangan di-log.
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

// SanitizeError membuang pesan asli driver karena bisa memuat kredensial.
func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w (timeout)", ErrConnUnavailable)
	}
	return ErrConnUnavailable
}