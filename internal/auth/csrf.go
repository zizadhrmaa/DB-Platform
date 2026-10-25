package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
)

const csrfHeader = "X-CSRF-Token"

// CSRFProtect menolak request yang mengubah state tanpa token yang cocok.
// Request yang belum punya session (seperti POST /login) dicek lewat
// header Origin atau Referer.
func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		if want := CSRFToken(r.Context()); want != "" {
			got := r.Header.Get(csrfHeader)
			if got == "" {
				// Batasnya mengikuti form terbesar (form SQL migration).
				r.Body = http.MaxBytesReader(w, r.Body, httpx.MaxSQLFormBytes)
				got = r.PostFormValue(CSRFFieldName)
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				rejectCSRF(w, r)
				return
			}
		} else if !sameOrigin(r) {
			rejectCSRF(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// Request tanpa Origin maupun Referer ikut ditolak. Browser modern selalu
// mengirim Origin untuk form POST.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" || src == "null" {
		return false
	}
	u, err := url.Parse(src)
	return err == nil && u.Host != "" && u.Host == r.Host
}

func rejectCSRF(w http.ResponseWriter, r *http.Request) {
	slog.Warn("csrf ditolak", "method", r.Method, "path", r.URL.Path)
	httpx.ErrorRenderer(w, r, http.StatusForbidden,
		"Form sudah kedaluwarsa atau tidak valid. Muat ulang halaman lalu coba lagi.")
}
