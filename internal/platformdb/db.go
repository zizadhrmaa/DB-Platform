// Package platformdb membuka database internal platform dan menjalankan
// schema-nya. Logika fitur jangan ditaruh di sini.
package platformdb

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema/*.sql
var schemaFS embed.FS

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("platformdb: open: %w", err)
	}
	ConfigurePool(db)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		// DSN tidak ikut ditulis karena berisi password.
		return nil, fmt.Errorf("platformdb: database platform tidak dapat dihubungi: %w", err)
	}
	return db, nil
}

func ConfigurePool(db *sql.DB) {
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

// Advisory lock supaya dua instance yang start bersamaan tidak menjalankan
// schema dua kali.
const platformMigrateLockKey = 7_311_0001

// Migrate menjalankan file schema/*.sql yang belum pernah dijalankan,
// urut nama file, masing-masing dalam satu transaction.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, platformMigrateLockKey); err != nil {
		return fmt.Errorf("platformdb: lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, platformMigrateLockKey) //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS platform_schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("platformdb: create tracking table: %w", err)
	}

	files, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		name := strings.TrimPrefix(f, "schema/")
		var exists bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM platform_schema_migrations WHERE filename = $1)`, name,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := schemaFS.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Tanpa argumen, pgx memakai simple protocol sehingga satu file
		// boleh berisi banyak statement.
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("platformdb: apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO platform_schema_migrations (filename) VALUES ($1)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// EnsureBootstrapAdmin membuat admin pertama kalau belum ada admin aktif.
func EnsureBootstrapAdmin(ctx context.Context, db *sql.DB, username, passwordHash string) (bool, error) {
	var hasAdmin bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE is_platform_admin AND is_active)`,
	).Scan(&hasAdmin); err != nil {
		return false, err
	}
	if hasAdmin {
		return false, nil
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, is_platform_admin)
		VALUES ($1, $2, TRUE)
		ON CONFLICT ((lower(username))) DO NOTHING`,
		username, passwordHash)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// User biasa dengan nama yang sama sengaja tidak dijadikan admin.
		return false, fmt.Errorf("platformdb: username bootstrap %q sudah dipakai user non-admin; pilih ADMIN_USERNAME lain", username)
	}
	return true, nil
}
