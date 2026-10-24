package authz

import (
	"context"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

type ctxKey int

const dbCtxKey ctxKey = 1

// DBContext diisi RequireDB supaya handler tidak perlu parsing dbID lagi.
type DBContext struct {
	DBID  int64
	Level domain.AccessLevel
}

func FromContext(ctx context.Context) (DBContext, bool) {
	v, ok := ctx.Value(dbCtxKey).(DBContext)
	return v, ok
}

func WithDBContext(ctx context.Context, d DBContext) context.Context {
	return context.WithValue(ctx, dbCtxKey, d)
}

func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.CurrentUser(r.Context())
		if !ok {
			httpx.Error(w, r, domain.ErrUnauthenticated)
			return
		}
		if !IsPlatformAdmin(u) {
			httpx.Error(w, r, domain.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireDB mengecek akses user ke database {dbID} untuk action, lalu
// menyimpan DBContext. Semua route /databases/{dbID}/... harus memakainya.
func RequireDB(az Authorizer, action Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.CurrentUser(r.Context())
		if !ok {
			httpx.Error(w, r, domain.ErrUnauthenticated)
			return
		}
		dbID, err := httpx.PathID(r, "dbID")
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		lvl, err := az.Level(r.Context(), u, dbID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if err := Decide(u, lvl, action); err != nil {
			httpx.Error(w, r, err)
			return
		}
		ctx := WithDBContext(r.Context(), DBContext{DBID: dbID, Level: lvl})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}