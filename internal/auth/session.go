package auth

import (
	"context"
	"net/http"
)

type SessionManager interface {
	// Request tanpa session tetap diteruskan; yang menolak adalah RequireLogin.
	Middleware(next http.Handler) http.Handler

	// POST /login belum punya session, jadi di sana yang dicek Origin/Referer.
	CSRFMiddleware(next http.Handler) http.Handler

	Login(w http.ResponseWriter, r *http.Request, userID int64) error

	// Logout mencabut seluruh family session dan menghapus cookie.
	Logout(w http.ResponseWriter, r *http.Request) error

	RotateOnPrivilegeChange(w http.ResponseWriter, r *http.Request) error

	// Dipakai saat user dinonaktifkan, dihapus, atau password-nya direset.
	RevokeAllForUser(ctx context.Context, userID int64) error
}
