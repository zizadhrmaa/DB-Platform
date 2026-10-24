package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

const origin = "http://example.com"

// browser menyimpan cookie session seperti browser sungguhan.
type browser struct {
	t      *testing.T
	h      http.Handler
	cookie string
}

func (b *browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, origin+path, nil)
	}
	req.Header.Set("Origin", origin)
	if b.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "dbp_session", Value: b.cookie})
	}
	rr := httptest.NewRecorder()
	b.h.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == "dbp_session" {
			if c.MaxAge < 0 {
				b.cookie = ""
			} else {
				b.cookie = c.Value
			}
		}
	}
	return rr
}

var csrfInput = regexp.MustCompile(`name="csrf_token" value="([^"]*)"`)

func newLoginServer(t *testing.T) *browser {
	t.Helper()
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "alice", false)

	r, err := view.New(web.FS)
	if err != nil {
		t.Fatal(err)
	}
	httpx.ErrorRenderer = r.ErrorPage
	sessions := auth.NewDBSessionManager(db, auth.DefaultSessionConfig(false))
	app := &App{DB: db, View: r, Authz: &authz.Service{DB: db}, Sessions: sessions, LoginLimiter: auth.NewLoginLimiter()}
	h := httpx.Chain(app.routes(), httpx.Recover, httpx.SecurityHeaders, sessions.Middleware, sessions.CSRFMiddleware)
	return &browser{t: t, h: h}
}

func TestLoginLogoutFlow(t *testing.T) {
	b := newLoginServer(t)

	if rr := b.do("GET", "/databases", nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("belum login: status %d, want 303", rr.Code)
	}

	rr := b.do("POST", "/login", url.Values{"username": {"alice"}, "password": {testutil.TestPassword}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/databases" {
		t.Fatalf("login: status %d location %q", rr.Code, rr.Header().Get("Location"))
	}
	if b.cookie == "" {
		t.Fatal("login tidak memasang cookie session")
	}

	rr = b.do("GET", "/databases", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("setelah login: status %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), b.cookie) {
		t.Fatal("session ID muncul di response")
	}
	m := csrfInput.FindStringSubmatch(rr.Body.String())
	if m == nil || m[1] == "" {
		t.Fatal("form logout tidak memuat token CSRF")
	}

	if rr := b.do("POST", "/logout", url.Values{}); rr.Code != http.StatusForbidden {
		t.Fatalf("logout tanpa token CSRF: status %d, want 403", rr.Code)
	}
	if rr := b.do("POST", "/logout", url.Values{"csrf_token": {m[1]}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("logout: status %d, want 303", rr.Code)
	}
	if b.cookie != "" {
		t.Fatal("cookie tidak dihapus setelah logout")
	}
	if rr := b.do("GET", "/databases", nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("setelah logout: status %d, want 303", rr.Code)
	}
}

func TestLoginFailureLooksTheSame(t *testing.T) {
	b := newLoginServer(t)

	wrongPass := b.do("POST", "/login", url.Values{"username": {"alice"}, "password": {"salah-banget"}})
	noUser := b.do("POST", "/login", url.Values{"username": {"tidakada"}, "password": {"salah-banget"}})

	for name, rr := range map[string]*httptest.ResponseRecorder{"password salah": wrongPass, "user tidak ada": noUser} {
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "Username atau password salah.") {
			t.Errorf("%s: pesan error tidak sesuai", name)
		}
		if strings.Contains(rr.Body.String(), "salah-banget") {
			t.Errorf("%s: password muncul lagi di form", name)
		}
	}
	if b.cookie != "" {
		t.Fatal("login gagal tidak boleh memasang cookie")
	}
	if !strings.Contains(noUser.Body.String(), `value="tidakada"`) {
		t.Error("username seharusnya diisi ulang di form")
	}
}

func TestLoginRequiresSameOrigin(t *testing.T) {
	b := newLoginServer(t)
	form := url.Values{"username": {"alice"}, "password": {testutil.TestPassword}}
	req := httptest.NewRequest("POST", origin+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://jahat.test")
	rr := httptest.NewRecorder()
	b.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("login dari origin lain: status %d, want 403", rr.Code)
	}
}

func TestLoginLockedAfterTooManyFailures(t *testing.T) {
	b := newLoginServer(t)
	wrong := url.Values{"username": {"alice"}, "password": {"salah-banget"}}
	for i := 0; i < auth.MaxFailsPerUsername; i++ {
		if rr := b.do("POST", "/login", wrong); rr.Code != http.StatusUnauthorized {
			t.Fatalf("percobaan ke-%d: status %d, want 401", i+1, rr.Code)
		}
	}

	// Password benar pun ditolak selama terkunci.
	rr := b.do("POST", "/login", url.Values{"username": {"Alice"}, "password": {testutil.TestPassword}})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("header Retry-After tidak ada")
	}
	if !strings.Contains(rr.Body.String(), "Terlalu banyak percobaan login") {
		t.Error("pesan penguncian tidak muncul")
	}
	if b.cookie != "" {
		t.Fatal("login yang terkunci tidak boleh memasang cookie")
	}
}

func TestLoginEmptyFields(t *testing.T) {
	b := newLoginServer(t)
	rr := b.do("POST", "/login", url.Values{"username": {""}, "password": {""}})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", rr.Code)
	}
}
