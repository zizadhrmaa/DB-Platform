package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

// last_seen_at cukup diperbarui sekali per menit, tidak perlu tiap request.
const lastSeenEvery = time.Minute

const tokenBytes = 32

type DBSessionManager struct {
	db  *sql.DB
	cfg SessionConfig
	now func() time.Time
}

var _ SessionManager = (*DBSessionManager)(nil)

func NewDBSessionManager(db *sql.DB, cfg SessionConfig) *DBSessionManager {
	return &DBSessionManager{db: db, cfg: cfg, now: time.Now}
}

type session struct {
	idHash     []byte
	familyID   string
	userID     int64
	csrfToken  string
	createdAt  time.Time
	lastSeenAt time.Time
	absExpires time.Time
	replacedAt sql.NullTime
	graceUntil sql.NullTime
	revokedAt  sql.NullTime
	privileged bool
}

func (s *session) valid(now time.Time, idle time.Duration) bool {
	switch {
	case s.revokedAt.Valid:
		return false
	case !now.Before(s.absExpires):
		return false
	case !now.Before(s.lastSeenAt.Add(idle)):
		return false
	case s.replacedAt.Valid && !now.Before(s.graceUntil.Time):
		return false
	}
	return true
}

func (m *DBSessionManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := m.current(w, r)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if s == nil {
			next.ServeHTTP(w, r)
			return
		}

		u, err := LoadActiveUser(r.Context(), m.db, s.userID)
		if errors.Is(err, domain.ErrUnauthenticated) {
			m.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			httpx.Error(w, r, err)
			return
		}

		if err := m.refresh(r.Context(), w, s, u); err != nil {
			httpx.Error(w, r, err)
			return
		}
		ctx := WithCSRFToken(WithUser(r.Context(), u), s.csrfToken)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *DBSessionManager) CSRFMiddleware(next http.Handler) http.Handler {
	return CSRFProtect(next)
}

func (m *DBSessionManager) Login(w http.ResponseWriter, r *http.Request, userID int64) error {
	ctx := r.Context()
	// Session lama di browser ini dicabut supaya tidak bisa dipakai untuk
	// session fixation.
	if c, err := r.Cookie(m.cfg.CookieName); err == nil && c.Value != "" {
		if err := m.revokeFamily(ctx, hashToken(c.Value)); err != nil {
			return err
		}
	}

	id, err := randomToken()
	if err != nil {
		return err
	}
	csrf, err := randomToken()
	if err != nil {
		return err
	}
	now := m.now()
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO sessions (id_hash, family_id, user_id, csrf_token, created_at, last_seen_at, absolute_expires_at, privileged)
		SELECT $1, gen_random_uuid(), id, $2, $3, $3, $4, is_platform_admin
		FROM users WHERE id = $5 AND is_active`,
		hashToken(id), csrf, now, now.Add(m.cfg.AbsoluteTimeout), userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return domain.ErrUnauthenticated
	}
	m.setCookie(w, id)
	return nil
}

func (m *DBSessionManager) Logout(w http.ResponseWriter, r *http.Request) error {
	defer m.clearCookie(w)
	c, err := r.Cookie(m.cfg.CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	return m.revokeFamily(r.Context(), hashToken(c.Value))
}

func (m *DBSessionManager) RotateOnPrivilegeChange(w http.ResponseWriter, r *http.Request) error {
	s, err := m.current(w, r)
	if err != nil {
		return err
	}
	if s == nil {
		return domain.ErrUnauthenticated
	}
	if s.replacedAt.Valid {
		return nil
	}
	privileged := s.privileged
	if u, ok := CurrentUser(r.Context()); ok {
		privileged = u.IsPlatformAdmin
	}
	return m.rotate(r.Context(), w, s, privileged)
}

func (m *DBSessionManager) RevokeAllForUser(ctx context.Context, userID int64) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, m.now())
	return err
}

// DeleteExpired menghapus session yang sudah tidak berlaku lebih dari sehari.
func (m *DBSessionManager) DeleteExpired(ctx context.Context) (int64, error) {
	cutoff := m.now().Add(-24 * time.Hour)
	res, err := m.db.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE absolute_expires_at < $1 OR revoked_at < $1 OR grace_until < $1 OR last_seen_at < $2`,
		cutoff, cutoff.Add(-m.cfg.IdleTimeout))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunCleanup memanggil DeleteExpired secara berkala sampai ctx selesai.
func (m *DBSessionManager) RunCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := m.DeleteExpired(ctx)
			if err != nil {
				slog.Error("hapus session kedaluwarsa gagal", "err", err)
			} else if n > 0 {
				slog.Info("session kedaluwarsa dihapus", "jumlah", n)
			}
		}
	}
}

// current membaca session dari cookie. Hasilnya nil kalau tidak ada session
// yang masih berlaku, dan cookie yang tidak berlaku sekalian dihapus.
func (m *DBSessionManager) current(w http.ResponseWriter, r *http.Request) (*session, error) {
	c, err := r.Cookie(m.cfg.CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	s, err := m.load(r.Context(), hashToken(c.Value))
	if err != nil {
		return nil, err
	}
	if s == nil || !s.valid(m.now(), m.cfg.IdleTimeout) {
		m.clearCookie(w)
		return nil, nil
	}
	return s, nil
}

func (m *DBSessionManager) load(ctx context.Context, idHash []byte) (*session, error) {
	s := &session{idHash: idHash}
	err := m.db.QueryRowContext(ctx, `
		SELECT family_id::text, user_id, csrf_token, created_at, last_seen_at, absolute_expires_at,
		       replaced_at, grace_until, revoked_at, privileged
		FROM sessions WHERE id_hash = $1`, idHash,
	).Scan(&s.familyID, &s.userID, &s.csrfToken, &s.createdAt, &s.lastSeenAt, &s.absExpires,
		&s.replacedAt, &s.graceUntil, &s.revokedAt, &s.privileged)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (m *DBSessionManager) refresh(ctx context.Context, w http.ResponseWriter, s *session, u *domain.User) error {
	// ID lama yang masih dalam grace period tetap diterima, tapi tidak
	// dirotasi lagi supaya request paralel tidak membuat rantai rotasi.
	if s.replacedAt.Valid {
		return nil
	}
	now := m.now()
	elevated := u.IsPlatformAdmin && !s.privileged
	if elevated || now.Sub(s.createdAt) >= m.cfg.RotationInterval {
		return m.rotate(ctx, w, s, u.IsPlatformAdmin)
	}
	if s.privileged != u.IsPlatformAdmin || now.Sub(s.lastSeenAt) >= lastSeenEvery {
		_, err := m.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = $2, privileged = $3 WHERE id_hash = $1`,
			s.idHash, now, u.IsPlatformAdmin)
		return err
	}
	return nil
}

func (m *DBSessionManager) rotate(ctx context.Context, w http.ResponseWriter, s *session, privileged bool) error {
	id, err := randomToken()
	if err != nil {
		return err
	}
	now := m.now()

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Kondisi replaced_at IS NULL memastikan satu ID hanya dirotasi sekali
	// walaupun ada beberapa request yang datang bersamaan.
	res, err := tx.ExecContext(ctx, `
		UPDATE sessions SET replaced_at = $2, grace_until = $3, last_seen_at = $2
		WHERE id_hash = $1 AND replaced_at IS NULL AND revoked_at IS NULL`,
		s.idHash, now, now.Add(m.cfg.GracePeriod))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return nil
	}

	// absolute_expires_at disalin apa adanya, jadi rotasi tidak
	// memperpanjang umur session.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id_hash, family_id, user_id, csrf_token, created_at, last_seen_at, absolute_expires_at, privileged)
		VALUES ($1, $2::uuid, $3, $4, $5, $5, $6, $7)`,
		hashToken(id), s.familyID, s.userID, s.csrfToken, now, s.absExpires, privileged); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.setCookie(w, id)
	return nil
}

func (m *DBSessionManager) revokeFamily(ctx context.Context, idHash []byte) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = $2
		WHERE revoked_at IS NULL
		  AND family_id = (SELECT family_id FROM sessions WHERE id_hash = $1)`,
		idHash, m.now())
	return err
}

// Cookie sengaja tanpa Max-Age supaya terhapus saat browser ditutup.
// Masa berlakunya tetap dicek di server.
func (m *DBSessionManager) setCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *DBSessionManager) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
