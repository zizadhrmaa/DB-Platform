package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// Connections mengelola data koneksi database target. Password disimpan
// terenkripsi dan tidak pernah dikembalikan ke pemanggil.
type Connections struct {
	DB      *sql.DB
	Secrets *dbmanager.SecretBox
}

var SSLModes = []string{"disable", "require", "verify-ca", "verify-full"}

const (
	maxConnName     = 100
	maxHostLen      = 255
	maxIdentLen     = 63 // batas nama database dan role di PostgreSQL
	maxPasswordLen  = 1000
	defaultPostgres = 5432
	defaultMySQL    = 3306
)

type ConnectionForm struct {
	Name     string
	DBType   string
	Host     string
	Port     string
	Database string
	Username string
	SSLMode  string
	IsActive bool
}

func (f ConnectionForm) Parse() (dbmanager.ConnectionInfo, error) {
	return f.parse()
}

func (f ConnectionForm) parse() (dbmanager.ConnectionInfo, error) {
	dbType := strings.ToLower(strings.TrimSpace(f.DBType))
	if dbType == "" {
		dbType = dbmanager.DBTypePostgres
	}
	if dbType != dbmanager.DBTypePostgres && dbType != dbmanager.DBTypeMySQL {
		return dbmanager.ConnectionInfo{}, invalid("Tipe database tidak didukung.")
	}
	port := defaultPostgres
	if dbType == dbmanager.DBTypeMySQL {
		port = defaultMySQL
	}
	c := dbmanager.ConnectionInfo{
		Name:     strings.TrimSpace(f.Name),
		DBType:   dbType,
		Host:     strings.TrimSpace(f.Host),
		Database: strings.TrimSpace(f.Database),
		Username: strings.TrimSpace(f.Username),
		SSLMode:  f.SSLMode,
		IsActive: f.IsActive,
		Port:     port,
	}
	switch {
	case c.Name == "":
		return c, invalid("Nama koneksi wajib diisi.")
	case utf8.RuneCountInString(c.Name) > maxConnName:
		return c, invalid(fmt.Sprintf("Nama koneksi maksimal %d karakter.", maxConnName))
	case c.Host == "" || len(c.Host) > maxHostLen || strings.ContainsAny(c.Host, " /?#@"):
		return c, invalid("Host tidak valid. Isi dengan nama host atau alamat IP saja.")
	case c.Database == "" || len(c.Database) > maxIdentLen:
		return c, invalid(fmt.Sprintf("Nama database wajib diisi, maksimal %d karakter.", maxIdentLen))
	case c.Username == "" || len(c.Username) > maxIdentLen:
		return c, invalid(fmt.Sprintf("Username wajib diisi, maksimal %d karakter.", maxIdentLen))
	}
	if p := strings.TrimSpace(f.Port); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return c, invalid("Port harus angka 1 sampai 65535.")
		}
		c.Port = port
	}
	if c.SSLMode == "" {
		c.SSLMode = "disable"
	}
	valid := false
	for _, m := range SSLModes {
		valid = valid || m == c.SSLMode
	}
	if !valid {
		return c, invalid("Mode SSL tidak dikenal.")
	}
	return c, nil
}

func (s *Connections) List(ctx context.Context) ([]dbmanager.ConnectionInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, COALESCE(db_type, 'postgres'), host, port, database_name, username, sslmode, is_active
		FROM db_connections ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []dbmanager.ConnectionInfo{}
	for rows.Next() {
		var c dbmanager.ConnectionInfo
		if err := rows.Scan(&c.ID, &c.Name, &c.DBType, &c.Host, &c.Port, &c.Database, &c.Username, &c.SSLMode, &c.IsActive); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func (s *Connections) Get(ctx context.Context, id int64) (*dbmanager.ConnectionInfo, error) {
	c := &dbmanager.ConnectionInfo{ID: id}
	err := s.DB.QueryRowContext(ctx, `
		SELECT name, COALESCE(db_type, 'postgres'), host, port, database_name, username, sslmode, is_active
		FROM db_connections WHERE id = $1`, id,
	).Scan(&c.Name, &c.DBType, &c.Host, &c.Port, &c.Database, &c.Username, &c.SSLMode, &c.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("koneksi %d: %w", id, domain.ErrNotFound)
	}
	return c, err
}

func (s *Connections) Create(ctx context.Context, f ConnectionForm, password string, createdBy int64) (int64, error) {
	c, err := f.parse()
	if err != nil {
		return 0, err
	}
	if password == "" {
		return 0, invalid("Password wajib diisi.")
	}
	cipher, err := s.encrypt(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO db_connections (name, db_type, host, port, database_name, username, password_ciphertext, sslmode, is_active, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		c.Name, c.DBType, c.Host, c.Port, c.Database, c.Username, cipher, c.SSLMode, c.IsActive, createdBy,
	).Scan(&id)
	if pgCode(err) == pgUniqueViolation {
		return 0, invalid("Nama koneksi sudah dipakai.")
	}
	return id, err
}

func (s *Connections) Update(ctx context.Context, id int64, f ConnectionForm, password string) error {
	c, err := f.parse()
	if err != nil {
		return err
	}
	var cipher any
	if password != "" {
		if cipher, err = s.encrypt(password); err != nil {
			return err
		}
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE db_connections
		SET name = $2, db_type = $3, host = $4, port = $5, database_name = $6, username = $7,
		    sslmode = $8, is_active = $9,
		    password_ciphertext = coalesce($10, password_ciphertext),
		    updated_at = now()
		WHERE id = $1`,
		id, c.Name, c.DBType, c.Host, c.Port, c.Database, c.Username, c.SSLMode, c.IsActive, cipher)
	if pgCode(err) == pgUniqueViolation {
		return invalid("Nama koneksi sudah dipakai.")
	}
	return notFoundIfNone(res, err, "koneksi", id)
}

// Delete ikut menghapus grant dan riwayat migration database tersebut.
// Pemanggil perlu memanggil Manager.Invalidate setelahnya.
func (s *Connections) Delete(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM db_connections WHERE id = $1`, id)
	return notFoundIfNone(res, err, "koneksi", id)
}

// Test mencoba terhubung memakai data koneksi yang tersimpan.
func (s *Connections) Test(ctx context.Context, id int64) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	var cipher []byte
	if err := s.DB.QueryRowContext(ctx,
		`SELECT password_ciphertext FROM db_connections WHERE id = $1`, id).Scan(&cipher); err != nil {
		return err
	}
	password, err := s.Secrets.Decrypt(cipher)
	if err != nil {
		return dbmanager.ErrConnUnavailable
	}
	return dbmanager.Ping(ctx, *c, password)
}

func (s *Connections) encrypt(password string) ([]byte, error) {
	if len(password) > maxPasswordLen {
		return nil, invalid(fmt.Sprintf("Password maksimal %d karakter.", maxPasswordLen))
	}
	return s.Secrets.Encrypt(password)
}
