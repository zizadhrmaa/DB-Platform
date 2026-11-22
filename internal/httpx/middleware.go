package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(r.Context(), "panic", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				ErrorRenderer(w, r, http.StatusInternalServerError, PublicMessage(http.StatusInternalServerError))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'")
		if !strings.HasPrefix(r.URL.Path, "/static/") && !strings.HasPrefix(r.URL.Path, "/app/assets/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type logInfoKey struct{}

type logInfo struct {
	mu       sync.Mutex
	actor    string
	database string
}

func (l *logInfo) snapshot() (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.actor, l.database
}

func logInfoFrom(ctx context.Context) *logInfo {
	li, _ := ctx.Value(logInfoKey{}).(*logInfo)
	return li
}

func SetLogActor(ctx context.Context, actor string) {
	if li := logInfoFrom(ctx); li != nil {
		li.mu.Lock()
		li.actor = actor
		li.mu.Unlock()
	}
}

func SetLogDatabase(ctx context.Context, database string) {
	if li := logInfoFrom(ctx); li != nil {
		li.mu.Lock()
		li.database = database
		li.mu.Unlock()
	}
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID(r *http.Request) string {
	for _, name := range []string{"X-Request-ID", "X-Correlation-ID"} {
		if id := r.Header.Get(name); validRequestID(id) {
			return id
		}
	}
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func RequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := newRequestID(r)
		w.Header().Set("X-Request-ID", reqID)

		info := &logInfo{}
		ctx := context.WithValue(r.Context(), logInfoKey{}, info)
		ctx = obs.WithRequestID(ctx, reqID)
		ctx, span := obs.StartSpan(ctx, r.Method+" "+r.URL.Path)
		defer span.End()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		actor, database := info.snapshot()
		if actor == "" {
			actor = "anonymous"
		}

		attrs := []any{
			"event", "http_request",
			"actor", actor,
			"status", rec.status,
			"correlation_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"dur_ms", time.Since(start).Milliseconds(),
		}
		if database != "" {
			attrs = append(attrs, "database", database)
		}

		switch {
		case rec.status >= 500:
			slog.ErrorContext(ctx, "http", attrs...)
		case rec.status >= 400:
			slog.WarnContext(ctx, "http", attrs...)
		default:
			slog.InfoContext(ctx, "http", attrs...)
		}
	})
}
