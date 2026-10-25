package web

import (
	"io/fs"
	"testing"
)

func TestEmbeddedFiles(t *testing.T) {
	if _, err := fs.Stat(FS, "templates/layouts/base.html"); err != nil {
		t.Fatalf("templates/layouts/base.html tidak ditemukan dalam embed.FS: %v", err)
	}
	if _, err := fs.Stat(FS, "static/style.css"); err != nil {
		t.Fatalf("static/style.css tidak ditemukan dalam embed.FS: %v", err)
	}
}
