package authz

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestEffectiveLevelTakesHighest(t *testing.T) {
	cases := []struct {
		in   []domain.AccessLevel
		want domain.AccessLevel
	}{
		{nil, domain.AccessNone},
		{[]domain.AccessLevel{domain.AccessView}, domain.AccessView},
		{[]domain.AccessLevel{domain.AccessView, domain.AccessEdit}, domain.AccessEdit},
		{[]domain.AccessLevel{domain.AccessEdit, domain.AccessView}, domain.AccessEdit},
		{[]domain.AccessLevel{domain.AccessNone, domain.AccessView}, domain.AccessView},
	}
	for _, c := range cases {
		if got := EffectiveLevel(c.in...); got != c.want {
			t.Errorf("EffectiveLevel(%v)=%v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	active := &domain.User{ID: 1, IsActive: true}
	inactive := &domain.User{ID: 2, IsActive: false}
	admin := &domain.User{ID: 3, IsActive: true, IsPlatformAdmin: true}

	cases := []struct {
		name   string
		u      *domain.User
		level  domain.AccessLevel
		action Action
		want   error
	}{
		{"tanpa user", nil, domain.AccessEdit, ActionViewSchema, domain.ErrUnauthenticated},
		{"user nonaktif", inactive, domain.AccessEdit, ActionViewSchema, domain.ErrUnauthenticated},
		{"tanpa akses -> 404", active, domain.AccessNone, ActionViewSchema, domain.ErrNotFound},
		{"view boleh lihat schema", active, domain.AccessView, ActionViewSchema, nil},
		{"view boleh lihat data", active, domain.AccessView, ActionViewData, nil},
		{"view boleh query read-only", active, domain.AccessView, ActionRunReadQuery, nil},
		{"view boleh lihat migration", active, domain.AccessView, ActionViewMigrations, nil},
		{"view tidak boleh create", active, domain.AccessView, ActionCreateMigration, domain.ErrForbidden},
		{"view tidak boleh edit", active, domain.AccessView, ActionEditMigration, domain.ErrForbidden},
		{"view tidak boleh execute", active, domain.AccessView, ActionExecuteMigration, domain.ErrForbidden},
		{"edit boleh execute", active, domain.AccessEdit, ActionExecuteMigration, nil},
		{"admin tanpa grant tidak otomatis akses",admin, domain.AccessNone, ActionViewSchema, domain.ErrNotFound},
		{"aksi tak dikenal ditolak", active, domain.AccessEdit, Action("db.drop"), domain.ErrForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Decide(c.u, c.level, c.action)
			if c.want == nil && err != nil {
				t.Fatalf("diharapkan boleh, dapat %v", err)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("diharapkan %v, dapat %v", c.want, err)
			}
		})
	}
}

func TestEveryActionHasPolicy(t *testing.T) {
	for _, a := range []Action{ActionViewSchema, ActionViewData, ActionRunReadQuery, ActionViewMigrations,
		ActionCreateMigration, ActionEditMigration, ActionExecuteMigration} {
		if _, ok := RequiredLevel(a); !ok {
			t.Errorf("aksi %s belum ada di tabel kebijakan", a)
		}
	}
}

func TestRequireDBMiddleware(t *testing.T) {
	u := &domain.User{ID: 1, IsActive: true}
	az := &Static{Levels: map[Key]domain.AccessLevel{{1, 10}: domain.AccessView}}

	reached := false
	h := RequireDB(az, ActionExecuteMigration, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	viewH := RequireDB(az, ActionViewSchema, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := FromContext(r.Context())
		if d.DBID != 10 || d.Level != domain.AccessView {
			t.Errorf("DBContext salah: %+v", d)
		}
		w.WriteHeader(http.StatusOK)
	}))

	mux := http.NewServeMux()
	mux.Handle("POST /databases/{dbID}/x", h)
	mux.Handle("GET /databases/{dbID}", viewH)

	do := func(method, path string, user *domain.User) int {
		req := httptest.NewRequest(method, path, nil)
		if user != nil {
			req = req.WithContext(auth.WithUser(req.Context(), user))
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	if c := do("POST", "/databases/10/x", u); c != http.StatusForbidden || reached {
		t.Errorf("View Only execute: status %d reached=%v, want 403", c, reached)
	}
	if c := do("GET", "/databases/10", u); c != http.StatusOK {
		t.Errorf("View Only lihat schema: status %d, want 200", c)
	}
	if c := do("GET", "/databases/99", u); c != http.StatusNotFound {
		t.Errorf("DB tanpa akses: status %d, want 404", c)
	}
	if c := do("GET", "/databases/abc", u); c != http.StatusNotFound {
		t.Errorf("ID tidak valid: status %d, want 404", c)
	}
	if c := do("GET", "/databases/10", nil); c != http.StatusSeeOther {
		t.Errorf("tanpa login GET: status %d, want 303 ke /login", c)
	}
}

func TestRequireAdmin(t *testing.T) {
	h := RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, c := range []struct {
		u    *domain.User
		want int
	}{
		{&domain.User{ID: 1, IsActive: true}, http.StatusForbidden},
		{&domain.User{ID: 2, IsActive: true, IsPlatformAdmin: true}, http.StatusOK},
		{&domain.User{ID: 3, IsActive: false, IsPlatformAdmin: true}, http.StatusForbidden},
	} {
		req := httptest.NewRequest("POST", "/admin/users", nil)
		req = req.WithContext(auth.WithUser(req.Context(), c.u))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("user %+v: status %d, want %d", c.u, rr.Code, c.want)
		}
	}
}