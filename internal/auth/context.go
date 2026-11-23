package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

type ctxKey int

const (
	userKey ctxKey = iota
	csrfKey
)

func WithUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

func CurrentUser(ctx context.Context) (*domain.User, bool) {
	u, ok := ctx.Value(userKey).(*domain.User)
	return u, ok && u != nil
}

func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey, token)
}

func CSRFToken(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey).(string)
	return t
}

const CSRFFieldName = "csrf_token"

func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(r.Context())
		if !ok || !u.IsActive {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpx.WriteErrorJSON(w, http.StatusUnauthorized, "unauthorized", httpx.PublicMessage(http.StatusUnauthorized), nil)
				return
			}
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/app/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "Silakan login terlebih dahulu.", http.StatusUnauthorized)
			return
		}
		httpx.SetLogActor(r.Context(), u.Username)
		next.ServeHTTP(w, r.WithContext(obs.WithActor(r.Context(), u.Username)))
	})
}
