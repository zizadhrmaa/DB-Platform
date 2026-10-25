package auth

import (
	"context"
	"net/http"
)

// Catatan implementasi SessionManager:
//   - Session ID 32 byte dari crypto/rand. Di DB yang disimpan hanya
//     sha256-nya (sessions.id_hash).
//   - Cookie HttpOnly, SameSite=Lax, Path=/, tanpa Max-Age. Expiry dicek
//     di server. Session ID jangan sampai masuk URL, body, atau log.
//   - Tolak session yang revoked, lewat absolute_expires_at, idle terlalu
//     lama, atau ID lama yang sudah lewat grace_until.
//   - Rotasi saat login, saat privilege naik, dan saat umur ID mencapai
//     RotationInterval. Baris baru mewarisi family_id, csrf_token, dan
//     absolute_expires_at.
//   - ID lama yang masih dalam grace period diterima tapi tidak dirotasi
//     lagi, supaya request paralel tidak membuat rantai rotasi.
type SessionManager interface {
	// Middleware memuat user terbaru dari tabel users ke context. Request
	// tanpa session tetap diteruskan; yang menolak adalah RequireLogin.
	Middleware(next http.Handler) http.Handler

	// CSRFMiddleware membalas 403 untuk POST/PUT/PATCH/DELETE dengan token
	// yang tidak cocok. POST /login belum punya session, jadi cek
	// Origin/Referer di sana.
	CSRFMiddleware(next http.Handler) http.Handler

	// Login juga mencabut session lama di browser yang sama untuk mencegah
	// session fixation.
	Login(w http.ResponseWriter, r *http.Request, userID int64) error

	// Logout mencabut seluruh family session dan menghapus cookie.
	Logout(w http.ResponseWriter, r *http.Request) error

	RotateOnPrivilegeChange(w http.ResponseWriter, r *http.Request) error

	// RevokeAllForUser dipakai saat user dinonaktifkan, dihapus, atau
	// password-nya direset.
	RevokeAllForUser(ctx context.Context, userID int64) error
}
