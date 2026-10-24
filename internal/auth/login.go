package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// ErrInvalidCredentials dipakai untuk username yang tidak ada, password
// salah, maupun user nonaktif, supaya penyebabnya tidak bisa ditebak.
var ErrInvalidCredentials = errors.New("auth: username atau password salah")

const maxUsernameLen = 64

func Authenticate(ctx context.Context, db *sql.DB, username, password string) (*domain.User, error) {
	if username == "" || len(username) > maxUsernameLen {
		VerifyAgainstDummy(password)
		return nil, ErrInvalidCredentials
	}

	u := &domain.User{}
	var hash string
	err := db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_platform_admin, is_active
		FROM users WHERE lower(username) = lower($1)`, username,
	).Scan(&u.ID, &u.Username, &hash, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		VerifyAgainstDummy(password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil {
		slog.Error("password hash rusak", "user_id", u.ID, "err", err)
		return nil, ErrInvalidCredentials
	}
	if !ok || !u.IsActive {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
