package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// ErrInvalidCredentials dipakai untuk username yang tidak ada, password
// salah, maupun user nonaktif, supaya penyebabnya tidak bisa ditebak.
var ErrInvalidCredentials = errors.New("auth: username atau password salah")

// ErrBusy berarti password belum bisa dicek karena server sedang sibuk.
var ErrBusy = fmt.Errorf("%w: server sedang sibuk", domain.ErrUnavailable)

const maxUsernameLen = 64

func Authenticate(ctx context.Context, db *sql.DB, username, password string) (*domain.User, error) {
	if username == "" || len(username) > maxUsernameLen {
		if VerifyAgainstDummy(password) != nil {
			return nil, ErrBusy
		}
		return nil, ErrInvalidCredentials
	}

	u := &domain.User{}
	var hash string
	err := db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_platform_admin, is_active
		FROM users WHERE lower(username) = lower($1)`, username,
	).Scan(&u.ID, &u.Username, &hash, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		if VerifyAgainstDummy(password) != nil {
			return nil, ErrBusy
		}
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	ok, err := VerifyPassword(password, hash)
	if errors.Is(err, ErrHashBusy) {
		return nil, ErrBusy
	}
	if err != nil {
		slog.Error("password hash rusak", "user_id", u.ID, "err", err)
		return nil, ErrInvalidCredentials
	}
	if !ok || !u.IsActive {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
