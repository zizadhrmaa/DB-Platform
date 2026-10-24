package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// LoadActiveUser dipanggil tiap request supaya perubahan role atau status
// aktif langsung berlaku.
func LoadActiveUser(ctx context.Context, db *sql.DB, id int64) (*domain.User, error) {
	u := &domain.User{}
	err := db.QueryRowContext(ctx,
		`SELECT id, username, is_platform_admin, is_active FROM users WHERE id = $1 AND is_active`, id,
	).Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// DevAuthMiddleware membuat semua request login sebagai devUserID. Ini
// sementara sampai SessionManager selesai, dan config.Load menolaknya di production.
func DevAuthMiddleware(db *sql.DB, devUserID int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, err := LoadActiveUser(r.Context(), db, devUserID)
			if err == nil {
				ctx := WithUser(r.Context(), u)
				ctx = WithCSRFToken(ctx, "dev-csrf-token")
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}