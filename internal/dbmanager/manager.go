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
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

const (
	DBTypePostgres = "postgres"
	DBTypeMySQL    = "mysql"
)

type Manager interface {
	Conn(ctx context.Context, dbID int64) (*sql.DB, error)
	Invalidate(dbID int64)
	Close() error
}

var (
	ErrConnNotFound    = fmt.Errorf("%w: koneksi database tidak ditemukan", domain.ErrNotFound)
	ErrConnInactive    = fmt.Errorf("%w: koneksi database dinonaktifkan", domain.ErrNotFound)
	ErrConnUnavailable = fmt.Errorf("%w: database target tidak dapat dihubungi", domain.ErrUnavailable)
)

type ConnectionInfo struct {
	ID       int64
	Name     string
	DBType   string
	Host     string
	Port     int
	Database string
	Username string
	SSLMode  string
	IsActive bool
}

func BuildDSN(c ConnectionInfo, password string) string {
	if c.DBType == DBTypeMySQL {
		cfg := mysql.NewConfig()
		cfg.User = c.Username
		cfg.Passwd = password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
		cfg.DBName = c.Database
		cfg.Timeout = 5 * time.Second
		cfg.MultiStatements = true
		cfg.ParseTime = true
		cfg.Collation = "utf8mb4_unicode_ci"
		switch c.SSLMode {
		case "require":
			cfg.TLSConfig = "skip-verify"
		case "verify-ca", "verify-full":
			cfg.TLSConfig = "true"
		default:
			cfg.TLSConfig = "false"
		}
		return cfg.FormatDSN()
	}

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

func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w (timeout)", ErrConnUnavailable)
	}
	return ErrConnUnavailable
}
