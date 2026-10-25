package view

import (
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/web"
)

func TestRendererNew(t *testing.T) {
	r, err := New(web.FS)
	if err != nil {
		t.Fatalf("New(web.FS) error: %v", err)
	}
	if r == nil || len(r.pages) == 0 {
		t.Fatal("Renderer kosong atau tidak ada halaman yang di-parse")
	}

	addFn := funcs["add"].(func(int, int) int)
	if addFn(2, 3) != 5 {
		t.Errorf("add(2, 3) = %d, want 5", addFn(2, 3))
	}

	fmtTimeFn := funcs["fmtTime"].(func(any) string)
	now := time.Date(2026, 10, 25, 21, 0, 0, 0, time.UTC)
	if s := fmtTimeFn(now); s == "-" || s == "" {
		t.Errorf("fmtTime(now) = %q", s)
	}
	if s := fmtTimeFn(nil); s != "-" {
		t.Errorf("fmtTime(nil) = %q, want -", s)
	}
}
