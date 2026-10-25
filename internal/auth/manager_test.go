package auth_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// clock adalah jam palsu yang bisa dimajukan dari test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	t     *testing.T
	db    *sql.DB
	m     *auth.DBSessionManager
	clock *clock
	cfg   auth.SessionConfig
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.PlatformDB(t)
	cfg := auth.DefaultSessionConfig(true)
	m := auth.NewDBSessionManager(db, cfg)
	c := &clock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	m.SetClock(c.Now)
	return &env{t: t, db: db, m: m, clock: c, cfg: cfg}
}

// login membuat session baru untuk user dan mengembalikan nilai cookie-nya.
func (e *env) login(userID int64) string {
	e.t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	if err := e.m.Login(rr, req, userID); err != nil {
		e.t.Fatalf("login: %v", err)
	}
	c := sessionCookie(rr, e.cfg.CookieName)
	if c == nil || c.Value == "" {
		e.t.Fatal("login tidak memasang cookie")
	}
	return c.Value
}

type result struct {
	username string // kosong kalau request tidak dianggap login
	csrf     string
	cookie   *http.Cookie
}

// request mengirim GET dengan cookie session lewat Middleware.
func (e *env) request(id string) result {
	e.t.Helper()
	var res result
	h := e.m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := auth.CurrentUser(r.Context()); ok {
			res.username = u.Username
		}
		res.csrf = auth.CSRFToken(r.Context())
	}))
	req := httptest.NewRequest("GET", "/databases", nil)
	if id != "" {
		req.AddCookie(&http.Cookie{Name: e.cfg.CookieName, Value: id})
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	// Errorf, bukan Fatalf, karena fungsi ini juga dipanggil dari goroutine.
	if rr.Code != http.StatusOK {
		e.t.Errorf("middleware membalas %d", rr.Code)
	}
	res.cookie = sessionCookie(rr, e.cfg.CookieName)
	return res
}

func sessionCookie(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginSetsSecureCookie(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)

	rr := httptest.NewRecorder()
	if err := e.m.Login(rr, httptest.NewRequest("POST", "/login", nil), alice.ID); err != nil {
		t.Fatal(err)
	}
	c := sessionCookie(rr, "dbp_session")
	if c == nil {
		t.Fatal("cookie dbp_session tidak ada")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("flag cookie salah: %+v", c)
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("cookie seharusnya cookie sesi browser tanpa Max-Age/Expires: %+v", c)
	}

	// Yang tersimpan di database hanya hash-nya.
	var stored []byte
	if err := e.db.QueryRow(`SELECT id_hash FROM sessions WHERE user_id = $1`, alice.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, []byte(c.Value)) || len(stored) != 32 {
		t.Errorf("id_hash bukan sha256 dari session ID")
	}

	if res := e.request(c.Value); res.username != "alice" || res.csrf == "" {
		t.Fatalf("session tidak dikenali: %+v", res)
	}
}

func TestLoginRejectsInactiveUser(t *testing.T) {
	e := newEnv(t)
	bob := testutil.MustUser(t, e.db, "bob", false)
	if _, err := e.db.Exec(`UPDATE users SET is_active = false WHERE id = $1`, bob.ID); err != nil {
		t.Fatal(err)
	}
	err := e.m.Login(httptest.NewRecorder(), httptest.NewRequest("POST", "/login", nil), bob.ID)
	if err == nil {
		t.Fatal("user nonaktif seharusnya tidak bisa login")
	}
}

func TestUnknownCookieIsCleared(t *testing.T) {
	e := newEnv(t)
	res := e.request("session-palsu")
	if res.username != "" {
		t.Fatal("cookie palsu dianggap login")
	}
	if res.cookie == nil || res.cookie.MaxAge >= 0 {
		t.Error("cookie palsu seharusnya dihapus")
	}
}

func TestIdleTimeout(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	id := e.login(alice.ID)

	e.clock.Advance(10 * time.Minute)
	if e.request(id).username == "" {
		t.Fatal("session ditolak padahal baru idle 10 menit")
	}
	e.clock.Advance(e.cfg.IdleTimeout + time.Second)
	if e.request(id).username != "" {
		t.Fatal("session masih diterima setelah idle timeout")
	}
}

func TestAbsoluteTimeoutNotExtendedByRotation(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	id := e.login(alice.ID)

	var want time.Time
	if err := e.db.QueryRow(`SELECT absolute_expires_at FROM sessions WHERE user_id = $1`, alice.ID).Scan(&want); err != nil {
		t.Fatal(err)
	}

	// Aktif terus tiap 20 menit: tidak pernah idle, dan ID dirotasi tiap kali.
	start := e.clock.Now()
	for e.clock.Now().Sub(start) < e.cfg.AbsoluteTimeout-20*time.Minute {
		e.clock.Advance(20 * time.Minute)
		res := e.request(id)
		if res.username == "" {
			t.Fatalf("session ditolak di menit ke-%v", e.clock.Now().Sub(start).Minutes())
		}
		if res.cookie != nil {
			id = res.cookie.Value
		}
	}

	var n int
	if err := e.db.QueryRow(`
		SELECT count(*) FROM sessions WHERE user_id = $1 AND absolute_expires_at <> $2`, alice.ID, want).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d baris hasil rotasi punya absolute_expires_at berbeda", n)
	}

	e.clock.Advance(20 * time.Minute)
	if e.request(id).username != "" {
		t.Fatal("session masih diterima setelah absolute timeout")
	}
}

func TestRotationWithGracePeriod(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	oldID := e.login(alice.ID)
	before := e.request(oldID)

	e.clock.Advance(e.cfg.RotationInterval)
	res := e.request(oldID)
	if res.username != "alice" || res.cookie == nil || res.cookie.Value == "" || res.cookie.Value == oldID {
		t.Fatalf("ID tidak dirotasi: %+v", res)
	}
	newID := res.cookie.Value
	if res.csrf != before.csrf {
		t.Error("token CSRF seharusnya tetap sama setelah rotasi")
	}

	// ID lama masih diterima selama grace period, tanpa dirotasi lagi.
	e.clock.Advance(e.cfg.GracePeriod - time.Second)
	again := e.request(oldID)
	if again.username != "alice" {
		t.Fatal("ID lama ditolak padahal masih dalam grace period")
	}
	if again.cookie != nil {
		t.Error("ID lama dalam grace period tidak boleh dirotasi lagi")
	}

	e.clock.Advance(2 * time.Second)
	if e.request(oldID).username != "" {
		t.Fatal("ID lama masih diterima setelah grace period")
	}
	if e.request(newID).username != "alice" {
		t.Fatal("ID baru ditolak")
	}
}

func TestConcurrentRotationOnlyOnce(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	id := e.login(alice.ID)
	e.clock.Advance(e.cfg.RotationInterval)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.request(id)
		}()
	}
	wg.Wait()

	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id = $1`, alice.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ada %d baris session, seharusnya 2 (asli + satu hasil rotasi)", n)
	}
}

func TestRotateWhenPromotedToAdmin(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	id := e.login(alice.ID)

	if res := e.request(id); res.cookie != nil {
		t.Fatal("tidak ada perubahan privilege, ID tidak boleh dirotasi")
	}
	if _, err := e.db.Exec(`UPDATE users SET is_platform_admin = true WHERE id = $1`, alice.ID); err != nil {
		t.Fatal(err)
	}
	res := e.request(id)
	if res.cookie == nil || res.cookie.Value == id {
		t.Fatal("ID tidak dirotasi setelah user dijadikan admin")
	}
	if again := e.request(res.cookie.Value); again.cookie != nil {
		t.Error("rotasi karena privilege seharusnya hanya sekali")
	}
}

func TestLogoutRevokesWholeFamily(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	oldID := e.login(alice.ID)
	e.clock.Advance(e.cfg.RotationInterval)
	newID := e.request(oldID).cookie.Value

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: e.cfg.CookieName, Value: newID})
	if err := e.m.Logout(rr, req); err != nil {
		t.Fatal(err)
	}
	if c := sessionCookie(rr, e.cfg.CookieName); c == nil || c.MaxAge >= 0 {
		t.Error("logout tidak menghapus cookie")
	}
	if e.request(newID).username != "" {
		t.Error("ID baru masih berlaku setelah logout")
	}
	if e.request(oldID).username != "" {
		t.Error("ID lama dalam grace period masih berlaku setelah logout")
	}
}

func TestLoginRevokesPreviousSessionInBrowser(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	first := e.login(alice.ID)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	req.AddCookie(&http.Cookie{Name: e.cfg.CookieName, Value: first})
	if err := e.m.Login(rr, req, alice.ID); err != nil {
		t.Fatal(err)
	}
	second := sessionCookie(rr, e.cfg.CookieName).Value
	if second == first {
		t.Fatal("login tidak membuat ID baru")
	}
	if e.request(first).username != "" {
		t.Error("session sebelumnya masih berlaku setelah login ulang")
	}
	if e.request(second).username != "alice" {
		t.Error("session baru tidak berlaku")
	}
}

func TestRevokeAllForUser(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	bob := testutil.MustUser(t, e.db, "bob", false)
	a1, a2 := e.login(alice.ID), e.login(alice.ID)
	b := e.login(bob.ID)

	if err := e.m.RevokeAllForUser(context.Background(), alice.ID); err != nil {
		t.Fatal(err)
	}
	if e.request(a1).username != "" || e.request(a2).username != "" {
		t.Error("session alice masih berlaku")
	}
	if e.request(b).username != "bob" {
		t.Error("session bob ikut tercabut")
	}
}

func TestDeactivatedUserLosesSession(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	id := e.login(alice.ID)
	if _, err := e.db.Exec(`UPDATE users SET is_active = false WHERE id = $1`, alice.ID); err != nil {
		t.Fatal(err)
	}
	if e.request(id).username != "" {
		t.Fatal("user nonaktif masih dianggap login")
	}
}

func TestDeleteExpired(t *testing.T) {
	e := newEnv(t)
	alice := testutil.MustUser(t, e.db, "alice", false)
	e.login(alice.ID)

	if n, err := e.m.DeleteExpired(context.Background()); err != nil || n != 0 {
		t.Fatalf("session aktif ikut dihapus: n=%d err=%v", n, err)
	}
	e.clock.Advance(e.cfg.AbsoluteTimeout + 25*time.Hour)
	if n, err := e.m.DeleteExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("session kedaluwarsa tidak dihapus: n=%d err=%v", n, err)
	}
}

func TestAuthenticate(t *testing.T) {
	db := testutil.PlatformDB(t)
	ctx := context.Background()
	testutil.MustUser(t, db, "Alice", false)
	off := testutil.MustUser(t, db, "carol", false)
	if _, err := db.Exec(`UPDATE users SET is_active = false WHERE id = $1`, off.ID); err != nil {
		t.Fatal(err)
	}

	if u, err := auth.Authenticate(ctx, db, "alice", testutil.TestPassword); err != nil || u.Username != "Alice" {
		t.Fatalf("login benar ditolak: u=%v err=%v", u, err)
	}
	for _, c := range []struct{ name, username, password string }{
		{"password salah", "alice", "salah"},
		{"username tidak ada", "zzz", testutil.TestPassword},
		{"user nonaktif", "carol", testutil.TestPassword},
		{"username kosong", "", testutil.TestPassword},
	} {
		if _, err := auth.Authenticate(ctx, db, c.username, c.password); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s: err=%v, want ErrInvalidCredentials", c.name, err)
		}
	}
}
