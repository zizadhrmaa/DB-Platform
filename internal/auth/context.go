// Package auth menangani login dan session.

// Modul lain cukup memakai CurrentUser, CSRFToken, dan RequireLogin,
// jangan membaca cookie session langsung.
package auth

import (
	"context"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type ctxKey int

const (
	userKey ctxKey = iota
	csrfKey
)

// WithUser menyimpan user ke context. Selain oleh middleware session,
// juga dipakai di test supaya tidak perlu login.
func WithUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// CurrentUser mengembalikan user yang sedang login.
func CurrentUser(ctx context.Context) (*domain.User, bool) {
	u, ok := ctx.Value(userKey).(*domain.User)
	return u, ok && u != nil
}

func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey, token)
}

// CSRFToken mengembalikan "" kalau belum login.
func CSRFToken(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey).(string)
	return t
}

const CSRFFieldName = "csrf_token"

// RequireLogin mengarahkan GET/HEAD tanpa login ke /login, dan membalas
// 401 untuk method lain.
func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(r.Context())
		if !ok || !u.IsActive {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "Silakan login terlebih dahulu.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}