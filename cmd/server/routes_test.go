package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	r, err := view.New(web.FS)
	if err != nil {
		t.Fatalf("template gagal di-parse: %v", err)
	}
	httpx.ErrorRenderer = r.ErrorPage
	az := &authz.Static{
		Levels: map[authz.Key]domain.AccessLevel{{UserID: 1, DBID: 10}: domain.AccessView},
		Names:  map[int64]string{10: "Toko <script>"},
	}
	app := &App{View: r, Authz: az}
	return httpx.Chain(app.routes(), httpx.Recover, httpx.SecurityHeaders)
}

var (
	viewer = &domain.User{ID: 1, Username: "viewer", IsActive: true}
	admin  = &domain.User{ID: 2, Username: "root", IsActive: true, IsPlatformAdmin: true}
)

func do(h http.Handler, method, path string, u *domain.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if u != nil {
		ctx := auth.WithUser(req.Context(), u)
		ctx = auth.WithCSRFToken(ctx, "tok")
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestRoutesAccessControl(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		name   string
		method string
		path   string
		user   *domain.User
		want   int
	}{
		{"halaman login publik", "GET", "/login", nil, 200},
		{"css publik", "GET", "/static/style.css", nil, 200},
		{"tanpa login diarahkan ke /login", "GET", "/databases", nil, 303},
		{"POST tanpa login 401", "POST", "/databases/10/migrations", nil, 401},
		{"daftar DB", "GET", "/databases", viewer, 200},
		{"view only boleh lihat schema", "GET", "/databases/10", viewer, 501},
		{"view only boleh query console", "POST", "/databases/10/query", viewer, 501},
		{"view only boleh lihat riwayat", "GET", "/databases/10/migrations", viewer, 501},
		{"view only tidak boleh form baru", "GET", "/databases/10/migrations/new", viewer, 403},
		{"view only tidak boleh execute", "POST", "/databases/10/migrations/5/execute", viewer, 403},
		{"DB tanpa akses -> 404", "GET", "/databases/99", viewer, 404},
		{"admin tanpa grant -> 404", "GET", "/databases/10", admin, 404},
		{"non-admin ke /admin -> 403", "GET", "/admin/users", viewer, 403},
		{"admin ke /admin", "GET", "/admin/users", admin, 501},
		{"path asing -> 404", "GET", "/tidak-ada", viewer, 404},
		{"method tidak didukung -> 404", "DELETE", "/databases", viewer, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(h, c.method, c.path, c.user)
			if rr.Code != c.want {
				t.Fatalf("%s %s: status %d, want %d", c.method, c.path, rr.Code, c.want)
			}
		})
	}
}

func TestPagesEscapeAndHeaders(t *testing.T) {
	h := newTestServer(t)
	rr := do(h, "GET", "/databases", viewer)
	body := rr.Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("nama DB tidak di-escape (XSS)")
	}
	if !strings.Contains(body, `name="csrf_token" value="tok"`) {
		t.Error("form logout tidak memuat token CSRF")
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if rr.Header().Get(hdr) == "" {
			t.Errorf("header %s tidak ada", hdr)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Error("halaman ber-login harus no-store")
	}
}
