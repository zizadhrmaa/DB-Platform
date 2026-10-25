package testutil

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// PGParams berisi data koneksi ke server test, untuk test yang perlu
// mendaftarkan database target lewat db_connections.
type PGParams struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

func ServerParams(t testing.TB) PGParams {
	t.Helper()
	cfg, err := pgx.ParseConfig(DSN(t))
	if err != nil {
		t.Fatalf("testutil: parse dsn: %v", err)
	}
	return PGParams{
		Host:     cfg.Host,
		Port:     int(cfg.Port),
		Database: cfg.Database,
		User:     cfg.User,
		Password: cfg.Password,
	}
}
