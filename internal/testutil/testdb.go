// Package testutil menyiapkan database untuk integration test.

// Tiap test mendapat schema acak sendiri yang dihapus setelah test selesai,
// jadi test bisa jalan paralel. Kalau TEST_DATABASE_URL kosong, integration test di-skip.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zizadhrmaa/DB-Platform/internal/platformdb"
)

func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL kosong, jalankan make test-integration")
	}
	return dsn
}

// IsolatedDB mengembalikan koneksi dengan search_path ke schema baru.
func IsolatedDB(t testing.TB) (*sql.DB, string) {
	t.Helper()
	dsn := DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("testutil: open: %v", err)
	}
	schema := "t_" + randomHex(8)
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("testutil: create schema: %v", err)
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("testutil: parse dsn: %v", err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(5)

	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("testutil: drop schema %s: %v", schema, err)
		}
		admin.Close()
	})
	return db, schema
}

func PlatformDB(t testing.TB) *sql.DB {
	t.Helper()
	db, _ := IsolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := platformdb.Migrate(ctx, db); err != nil {
		t.Fatalf("testutil: migrate platform schema: %v", err)
	}
	return db
}

// TargetDB mengembalikan database kosong. Tabel yang dibuat masuk ke
// schema yang dikembalikan, bukan public.
func TargetDB(t testing.TB) (*sql.DB, string) {
	t.Helper()
	return IsolatedDB(t)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
