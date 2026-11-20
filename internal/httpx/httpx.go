// Package httpx berisi helper HTTP dan middleware yang dipakai bersama.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// CurrentActorFunc adalah hook untuk mengambil identitas actor/pengguna dari context.
var CurrentActorFunc func(r *http.Request) string

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
// password atau DSN. Untuk path /api/ atau permintaan JSON, kembalikan JSON error.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	status := StatusFor(err)
	if status >= 500 {
		slog.Error("request gagal", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
	}

	// Untuk path /api/, kembalikan JSON seragam tanpa redirect (termasuk status 401).
	if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("Accept") == "application/json" {
		code := errorCodeForStatus(status)
		msg := PublicMessage(status)
		WriteErrorJSON(w, status, code, msg, nil)
		return
	}

	if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ErrorRenderer(w, r, status, PublicMessage(status))
}

func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_input"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal_error"
	}
}

// JSON menulis response JSON dengan HTTP status tertentu.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Error("gagal encode response json", "err", err)
		}
	}
}

// DecodeJSON mem-decode body request JSON ke dalam variabel target.
func DecodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxFormBytes))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: json tidak valid", domain.ErrInvalidInput)
	}
	return nil
}

type APIError struct {
	Error APIErrorDetail `json:"error"`
}

type APIErrorDetail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

// WriteErrorJSON menulis response error JSON dengan format seragam.
func WriteErrorJSON(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	if fields == nil {
		fields = map[string]string{}
	}
	JSON(w, status, APIError{
		Error: APIErrorDetail{
			Code:    code,
			Message: msg,
			Fields:  fields,
		},
	})
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
