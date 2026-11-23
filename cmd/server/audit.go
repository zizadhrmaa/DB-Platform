package main

import (
	"log/slog"
	"net/http"
)

type auditWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var auditPathKeys = [][2]string{
	{"userID", "target_user_id"},
	{"groupID", "group_id"},
	{"dbID", "target_database_id"},
	{"migrationID", "migration_id"},
	{"requestID", "change_request_id"},
	{"jobID", "job_id"},
}

func audit(event string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &auditWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		attrs := []any{"event", event, "http_status", rec.status}
		for _, k := range auditPathKeys {
			if v := r.PathValue(k[0]); v != "" {
				attrs = append(attrs, k[1], v)
			}
		}
		if rec.status >= 400 {
			attrs = append(attrs, "status", "failed")
			slog.WarnContext(r.Context(), "audit", attrs...)
			return
		}
		attrs = append(attrs, "status", "success")
		slog.InfoContext(r.Context(), "audit", attrs...)
	})
}
