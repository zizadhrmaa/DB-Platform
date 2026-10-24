// Package httpx berisi helper HTTP dan middleware yang dipakai bersama.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func StatusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, domain.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, domain.ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// PublicMessage tidak pernah memuat detail internal seperti SQL atau DSN.
func PublicMessage(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "Silakan login terlebih dahulu."
	case http.StatusForbidden:
		return "Anda tidak memiliki izin untuk melakukan aksi ini."
	case http.StatusNotFound:
		return "Halaman atau data tidak ditemukan."
	case http.StatusBadRequest:
		return "Permintaan tidak valid."
	case http.StatusConflict:
		return "Aksi bertentangan dengan kondisi data saat ini. Muat ulang halaman lalu coba lagi."
	case http.StatusServiceUnavailable:
		return "Database tujuan sedang tidak dapat dihubungi. Coba lagi nanti."
	default:
		return "Terjadi kesalahan pada server."
	}
}

// ErrorRenderer diganti di main supaya error tampil sebagai halaman HTML.
var ErrorRenderer = func(w http.ResponseWriter, r *http.Request, status int, msg string) {
	http.Error(w, msg, status)
}

// Error mencatat error 5xx ke log, jadi pastikan isinya tidak memuat
// password atau DSN.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	status := StatusFor(err)
	if status >= 500 {
		slog.Error("request gagal", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
	}
	if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ErrorRenderer(w, r, status, PublicMessage(status))
}

// PathID menganggap ID yang tidak valid sebagai ErrNotFound.
func PathID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%w: %s tidak valid", domain.ErrNotFound, name)
	}
	return v, nil
}

const (
	MaxFormBytes    = 64 << 10 // 64 KiB
	MaxSQLFormBytes = 1 << 20  // 1 MiB, untuk form migration
)

func ParseForm(w http.ResponseWriter, r *http.Request, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("%w: form tidak valid atau terlalu besar", domain.ErrInvalidInput)
	}
	return nil
}
