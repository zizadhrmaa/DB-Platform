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

var ErrPayloadTooLarge = errors.New("isi permintaan terlalu besar")

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
	case errors.Is(err, ErrPayloadTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, domain.ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

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
	case http.StatusRequestEntityTooLarge:
		return "Isi permintaan terlalu besar."
	case http.StatusTooManyRequests:
		return "Terlalu banyak permintaan. Coba lagi nanti."
	case http.StatusServiceUnavailable:
		return "Database tujuan sedang tidak dapat dihubungi. Coba lagi nanti."
	default:
		return "Terjadi kesalahan pada server."
	}
}

var ErrorRenderer = func(w http.ResponseWriter, r *http.Request, status int, msg string) {
	http.Error(w, msg, status)
}

func Error(w http.ResponseWriter, r *http.Request, err error) {
	status := StatusFor(err)
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request gagal", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
	}

	if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("Accept") == "application/json" {
		WriteErrorJSON(w, status, errorCodeForStatus(status), PublicMessage(status), nil)
		return
	}

	if status == http.StatusUnauthorized && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		http.Redirect(w, r, "/app/login", http.StatusSeeOther)
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
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal_error"
	}
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Error("gagal encode response json", "err", err)
		}
	}
}

type APIError struct {
	Error APIErrorDetail `json:"error"`
}

type APIErrorDetail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

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

func PathID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%w: %s tidak valid", domain.ErrNotFound, name)
	}
	return v, nil
}

const (
	MaxFormBytes          = 64 << 10
	MaxSQLFormBytes       = 1 << 20
	MaxMigrationJSONBytes = 4 << 20
)

func ParseForm(w http.ResponseWriter, r *http.Request, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("%w: form tidak valid atau terlalu besar", domain.ErrInvalidInput)
	}
	return nil
}
