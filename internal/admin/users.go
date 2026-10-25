package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type User struct {
	ID              int64
	Username        string
	IsPlatformAdmin bool
	IsActive        bool
	CreatedAt       time.Time
	GroupCount      int
	Groups          []GroupRef // hanya diisi oleh GetUser
}

type GroupRef struct {
	ID   int64
	Name string
}

// Sama dengan CHECK constraint users_username_format.
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

func ValidateUsername(s string) error {
	if !usernameRe.MatchString(s) {
		return invalid("Username harus 3 sampai 64 karakter dan hanya berisi huruf, angka, titik, garis bawah, atau tanda minus.")
	}
	return nil
}

func ValidatePassword(s string) error {
	if len(s) < auth.MinPasswordLen {
		return invalid(fmt.Sprintf("Password minimal %d karakter.", auth.MinPasswordLen))
	}
	if len(s) > auth.MaxPasswordBytes {
		return invalid(fmt.Sprintf("Password maksimal %d karakter.", auth.MaxPasswordBytes))
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT u.id, u.username, u.is_platform_admin, u.is_active, u.created_at, count(m.group_id)
		FROM users u
		LEFT JOIN group_members m ON m.user_id = u.id
		GROUP BY u.id
		ORDER BY lower(u.username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive, &u.CreatedAt, &u.GroupCount); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	u := &User{ID: id}
	err := s.DB.QueryRowContext(ctx, `
		SELECT username, is_platform_admin, is_active, created_at FROM users WHERE id = $1`, id,
	).Scan(&u.Username, &u.IsPlatformAdmin, &u.IsActive, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %d: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT g.id, g.name FROM groups g
		JOIN group_members m ON m.group_id = g.id
		WHERE m.user_id = $1
		ORDER BY lower(g.name)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u.Groups = []GroupRef{}
	for rows.Next() {
		var g GroupRef
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		u.Groups = append(u.Groups, g)
	}
	u.GroupCount = len(u.Groups)
	return u, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, username, password string, isAdmin bool) (int64, error) {
	if err := ValidateUsername(username); err != nil {
		return 0, err
	}
	if err := ValidatePassword(password); err != nil {
		return 0, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, is_platform_admin) VALUES ($1, $2, $3) RETURNING id`,
		username, hash, isAdmin).Scan(&id)
	if pgCode(err) == pgUniqueViolation {
		return 0, invalid("Username sudah dipakai.")
	}
	return id, err
}

// UpdateUser mengubah status aktif dan admin. Kalau user dinonaktifkan,
// pemanggil juga perlu mencabut session-nya.
func (s *Store) UpdateUser(ctx context.Context, actorID, id int64, isActive, isAdmin bool) error {
	return s.withAdminLock(ctx, actorID, id, isActive && isAdmin, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE users SET is_active = $2, is_platform_admin = $3, updated_at = now() WHERE id = $1`,
			id, isActive, isAdmin)
		return err
	})
}

func (s *Store) DeleteUser(ctx context.Context, actorID, id int64) error {
	return s.withAdminLock(ctx, actorID, id, false, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
		return err
	})
}

// ResetPassword mengganti password user. Pemanggil perlu mencabut semua
// session user tersebut.
func (s *Store) ResetPassword(ctx context.Context, id int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	return notFoundIfNone(res, err, "user", id)
}

// withAdminLock menjalankan fn di dalam transaction setelah memastikan
// perubahan pada user id tidak menghilangkan Platform Admin aktif terakhir.
// staysAdmin bernilai true kalau setelah perubahan user itu masih admin
// yang aktif.
func (s *Store) withAdminLock(ctx context.Context, actorID, id int64, staysAdmin bool, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Semua admin aktif dikunci dengan urutan yang sama, jadi dua admin yang
	// saling menurunkan di waktu bersamaan akan diproses bergantian.
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM users WHERE is_platform_admin AND is_active ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	activeAdmins, targetIsAdmin := 0, false
	for rows.Next() {
		var adminID int64
		if err := rows.Scan(&adminID); err != nil {
			rows.Close()
			return err
		}
		activeAdmins++
		if adminID == id {
			targetIsAdmin = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT true FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user %d: %w", id, domain.ErrNotFound)
	} else if err != nil {
		return err
	}

	if targetIsAdmin && !staysAdmin {
		if actorID == id {
			return conflict("Anda tidak bisa menonaktifkan, menurunkan, atau menghapus akun Anda sendiri.")
		}
		if activeAdmins <= 1 {
			return conflict("Ini Platform Admin aktif terakhir. Jadikan user lain admin terlebih dahulu.")
		}
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
