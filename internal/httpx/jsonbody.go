package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type UserError struct {
	Msg string
	Err error
}

func (e *UserError) Error() string       { return e.Msg }
func (e *UserError) Unwrap() error       { return e.Err }
func (e *UserError) UserMessage() string { return e.Msg }

func Invalid(msg string) error   { return &UserError{Msg: msg, Err: domain.ErrInvalidInput} }
func Conflict(msg string) error  { return &UserError{Msg: msg, Err: domain.ErrConflict} }
func Forbidden(msg string) error { return &UserError{Msg: msg, Err: domain.ErrForbidden} }
func NotFound(msg string) error  { return &UserError{Msg: msg, Err: domain.ErrNotFound} }

type userMessage interface{ UserMessage() string }

func domainMessage(err error) string {
	msg := err.Error()
	for _, base := range []error{domain.ErrInvalidInput, domain.ErrConflict} {
		prefix := base.Error() + ": "
		if i := strings.Index(msg, prefix); i >= 0 {
			return msg[i+len(prefix):]
		}
	}
	return ""
}

func Fail(w http.ResponseWriter, r *http.Request, err error) {
	status := StatusFor(err)
	if status < 500 {
		var um userMessage
		if errors.As(err, &um) {
			WriteErrorJSON(w, status, errorCodeForStatus(status), um.UserMessage(), nil)
			return
		}
		if msg := domainMessage(err); msg != "" {
			WriteErrorJSON(w, status, errorCodeForStatus(status), msg, nil)
			return
		}
	}
	Error(w, r, err)
}

func decodeError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &UserError{Msg: "Isi permintaan terlalu besar.", Err: ErrPayloadTooLarge}
	}
	return Invalid("Isi permintaan bukan JSON yang valid.")
}

func ReadJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if mediaType != "application/json" {
		return Invalid("Content-Type harus application/json.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return decodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return decodeError(err)
	}
	return nil
}

func ReadJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return ReadJSONLimit(w, r, v, MaxFormBytes)
}

func APIAware(html func(http.ResponseWriter, *http.Request, int, string)) func(http.ResponseWriter, *http.Request, int, string) {
	return func(w http.ResponseWriter, r *http.Request, status int, msg string) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			WriteErrorJSON(w, status, errorCodeForStatus(status), msg, nil)
			return
		}
		html(w, r, status, msg)
	}
}
