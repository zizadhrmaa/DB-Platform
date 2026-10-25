package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const pingTimeout = 5 * time.Second

// PoolManager menyimpan satu pool per database target. Data koneksinya
// dibaca dari tabel db_connections di database platform.
type PoolManager struct {
	platform *sql.DB
	secrets  *SecretBox

	mu    sync.Mutex
	pools map[int64]*sql.DB
}

var _ Manager = (*PoolManager)(nil)

func NewPoolManager(platform *sql.DB, secrets *SecretBox) *PoolManager {
	return &PoolManager{platform: platform, secrets: secrets, pools: map[int64]*sql.DB{}}
}

func (m *PoolManager) Conn(ctx context.Context, dbID int64) (*sql.DB, error) {
	m.mu.Lock()
	db, ok := m.pools[dbID]
	m.mu.Unlock()
	if ok {
		return db, nil
	}

	info, cipher, err := m.load(ctx, dbID)
	if err != nil {
		return nil, err
	}
	password, err := m.secrets.Decrypt(cipher)
	if err != nil {
		slog.Error("kredensial database target tidak bisa didekripsi", "db_id", dbID)
		return nil, ErrConnUnavailable
	}
	// Mutex tidak dipegang selama Ping supaya database lain yang lambat
	// tidak ikut menahan request ke database ini.
	db, err = Open(ctx, info, password)
	if err != nil {
		slog.Warn("database target tidak bisa dihubungi", "db_id", dbID)
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.pools[dbID]; ok {
		db.Close()
		return existing, nil
	}
	m.pools[dbID] = db
	return db, nil
}

func (m *PoolManager) Invalidate(dbID int64) {
	m.mu.Lock()
	db, ok := m.pools[dbID]
	delete(m.pools, dbID)
	m.mu.Unlock()
	if ok {
		db.Close()
	}
}

func (m *PoolManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for id, db := range m.pools {
		errs = append(errs, db.Close())
		delete(m.pools, id)
	}
	return errors.Join(errs...)
}

func (m *PoolManager) load(ctx context.Context, dbID int64) (ConnectionInfo, []byte, error) {
	var c ConnectionInfo
	var cipher []byte
	err := m.platform.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(db_type, 'postgres'), host, port, database_name, username, sslmode, is_active, password_ciphertext
		FROM db_connections WHERE id = $1`, dbID,
	).Scan(&c.ID, &c.Name, &c.DBType, &c.Host, &c.Port, &c.Database, &c.Username, &c.SSLMode, &c.IsActive, &cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil, ErrConnNotFound
	}
	if err != nil {
		return c, nil, err
	}
	if !c.IsActive {
		return c, nil, ErrConnInactive
	}
	return c, cipher, nil
}

func Open(ctx context.Context, info ConnectionInfo, password string) (*sql.DB, error) {
	driver := "pgx"
	if info.DBType == DBTypeMySQL {
		driver = "mysql"
	}
	db, err := sql.Open(driver, BuildDSN(info, password))
	if err != nil {
		return nil, SanitizeError(err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, SanitizeError(err)
	}
	return db, nil
}

func Ping(ctx context.Context, info ConnectionInfo, password string) error {
	db, err := Open(ctx, info, password)
	if err != nil {
		return err
	}
	return db.Close()
}

func IsMySQL(db *sql.DB) bool {
	if db == nil {
		return false
	}
	drv := fmt.Sprintf("%T", db.Driver())
	return strings.Contains(drv, "mysql") || strings.Contains(drv, "MySQL")
}
