package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

// Chain(h, a, b, c) sama dengan a(b(c(h))).
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
				slog.Error("panic", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				ErrorRenderer(w, r, http.StatusInternalServerError, PublicMessage(http.StatusInternalServerError))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders juga memasang no-store di semua halaman selain /static/
// karena isinya bisa sensitif.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
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

// RequestLog mencatat setiap request HTTP dengan correlation ID, actor, database,
// status, durasi, dan tracing context OpenTelemetry.
func RequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = r.Header.Get("X-Correlation-ID")
		}
		if reqID == "" {
			b := make([]byte, 12)
			rand.Read(b)
			reqID = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", reqID)

		ctx := obs.WithRequestID(r.Context(), reqID)
		ctx, span := obs.StartSpan(ctx, r.Method+" "+r.URL.Path)
		defer span.End()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		actor := "anonymous"
		if CurrentActorFunc != nil {
			if a := CurrentActorFunc(r); a != "" {
				actor = a
			}
		}
		ctx = obs.WithActor(ctx, actor)

		dbID := r.PathValue("dbID")
		if dbID != "" {
			ctx = obs.WithDatabase(ctx, dbID)
		}

		logAttrs := []any{
			"event", "http_request",
			"actor", actor,
			"status", rec.status,
			"correlation_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"dur_ms", time.Since(start).Milliseconds(),
		}
		if dbID != "" {
			logAttrs = append(logAttrs, "database", dbID)
		}

		if rec.status >= 500 {
			slog.ErrorContext(ctx, "http", logAttrs...)
		} else if rec.status >= 400 {
			slog.WarnContext(ctx, "http", logAttrs...)
		} else {
			slog.InfoContext(ctx, "http", logAttrs...)
		}
	})
}
