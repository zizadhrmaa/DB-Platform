package testutil

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

const TestPassword = "password123"

var (
	hashOnce sync.Once
	testHash string
)

// PasswordHash di-cache karena Argon2 lambat.
func PasswordHash(t testing.TB) string {
	hashOnce.Do(func() {
		h, err := auth.HashPassword(TestPassword)
		if err != nil {
			panic(err)
		}
		testHash = h
	})
	return testHash
}

func MustUser(t testing.TB, db *sql.DB, username string, admin bool) *domain.User {
	t.Helper()
	u := &domain.User{Username: username, IsPlatformAdmin: admin, IsActive: true}
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, is_platform_admin) VALUES ($1,$2,$3) RETURNING id`,
		username, PasswordHash(t), admin).Scan(&u.ID)
	if err != nil {
		t.Fatalf("MustUser(%s): %v", username, err)
	}
	return u
}

func MustGroup(t testing.TB, db *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO groups (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("MustGroup(%s): %v", name, err)
	}
	return id
}

func MustAddMember(t testing.TB, db *sql.DB, groupID, userID int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO group_members (group_id, user_id) VALUES ($1,$2)`, groupID, userID); err != nil {
		t.Fatalf("MustAddMember: %v", err)
	}
}

// MustDBConn memakai password dummy, jadi koneksinya tidak bisa dibuka.
func MustDBConn(t testing.TB, db *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO db_connections (name, host, port, database_name, username, password_ciphertext)
		VALUES ($1, 'localhost', 5432, 'target', 'target_user', '\x00') RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("MustDBConn(%s): %v", name, err)
	}
	return id
}

func MustGrant(t testing.TB, db *sql.DB, groupID, dbID int64, level string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1,$2,$3)
		ON CONFLICT (group_id, db_id) DO UPDATE SET level = EXCLUDED.level`,
		groupID, dbID, level); err != nil {
		t.Fatalf("MustGrant: %v", err)
	}
}