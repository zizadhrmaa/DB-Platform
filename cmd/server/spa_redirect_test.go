package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
	"github.com/zizadhrmaa/DB-Platform/web"
)

// Secara default (tanpa LegacyHTML) halaman template Go Milestone 1 tidak
// dilayani lagi: URL lama dialihkan ke halaman SPA yang setara, dan form
// POST lama tidak tersedia.
func TestLegacyPagesRedirectToSPA(t *testing.T) {
	r, err := view.New(web.FS)
	if err != nil {
		t.Fatal(err)
	}
	useErrorRenderer(t, httpx.APIAware(r.ErrorPage))
	h := httpx.Chain((&App{View: r, Authz: &authz.Static{}}).routes(), httpx.Recover)

	cases := map[string]string{
		"/login":                            "/app/login",
		"/databases":                        "/app/databases",
		"/databases/3":                      "/app/databases/3",
		"/databases/3/tables/public/produk": "/app/databases/3/tables/public/produk",
		"/databases/3/tables/public/produk/data?page=2": "/app/databases/3/tables/public/produk/data?page=2",
		"/databases/3/migrations":                       "/app/databases/3/migrations",
		"/databases/3/migrations/new":                   "/app/databases/3/migrations",
		"/databases/3/migrations/diff":                  "/app/databases/3/migrations/diff",
		"/databases/3/migrations/7":                     "/app/databases/3/migrations/7",
		"/databases/3/migrations/7/edit":                "/app/databases/3/migrations/7",
		"/admin":                                        "/app/admin",
		"/admin/users/new":                              "/app/admin/users",
		"/admin/users/5":                                "/app/admin/users/5",
		"/admin/groups/2":                               "/app/admin/groups/2",
		"/admin/databases":                              "/app/admin/connections",
		"/admin/databases/new":                          "/app/admin/connections/new",
		"/admin/databases/4":                            "/app/admin/connections/4",
	}
	for from, want := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, from, nil))
		if rr.Code != http.StatusFound || rr.Header().Get("Location") != want {
			t.Errorf("GET %s: status %d Location %q, want 302 %q", from, rr.Code, rr.Header().Get("Location"), want)
		}
	}
	for _, p := range []string{"/login", "/admin/users", "/databases/3/migrations"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, p, nil))
		if rr.Code < 400 {
			t.Errorf("POST %s (form lama) harus ditolak, dapat %d", p, rr.Code)
		}
	}
}
