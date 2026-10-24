package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestStatusFor(t *testing.T) {
	cases := map[error]int{
		nil:                                      200,
		domain.ErrUnauthenticated:                401,
		domain.ErrForbidden:                      403,
		fmt.Errorf("x: %w", domain.ErrNotFound):  404,
		domain.ErrInvalidInput:                   400,
		domain.ErrConflict:                       409,
		domain.ErrUnavailable:                    503,
		errors.New("pq: password=rahasia salah"): 500,
	}
	for err, want := range cases {
		if got := StatusFor(err); got != want {
			t.Errorf("StatusFor(%v)=%d, want %d", err, got, want)
		}
	}
	if msg := PublicMessage(http.StatusInternalServerError); msg == "" {
		t.Error("pesan 500 kosong")
	}
}