// Package view merender template di web/templates.

// Nama halaman adalah path di pages/ tanpa .html, misalnya "databases/list".
// Tiap halaman di-parse terpisah supaya blok "content" tidak bentrok.
package view

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type PageData struct {
	Title     string
	User      *domain.User
	CSRFToken string
	Flash     string
	Error     string
	Data      any // data khusus halaman
}

type Renderer struct {
	pages map[string]*template.Template
}

var funcs = template.FuncMap{
	"fmtTime": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.Local().Format("2006-01-02 15:04:05")
		case *time.Time:
			if v == nil {
				return "-"
			}
			return v.Local().Format("2006-01-02 15:04:05")
		}
		return "-"
	},
	"add": func(a, b int) int { return a + b },
}

func New(fsys fs.FS) (*Renderer, error) {
	base := template.New("").Funcs(funcs)
	for _, pattern := range []string{"templates/layouts/*.html", "templates/partials/*.html"} {
		files, err := fs.Glob(fsys, pattern)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			if base, err = base.ParseFS(fsys, files...); err != nil {
				return nil, err
			}
		}
	}

	r := &Renderer{pages: map[string]*template.Template{}}
	err := fs.WalkDir(fsys, "templates/pages", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if t, err = t.ParseFS(fsys, p); err != nil {
			return fmt.Errorf("view: parse %s: %w", p, err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		r.pages[name] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Render menulis ke buffer dulu supaya error template tidak menghasilkan
// HTML setengah jadi.
func (rd *Renderer) Render(w http.ResponseWriter, r *http.Request, status int, page string, pd PageData) {
	t, ok := rd.pages[page]
	if !ok {
		slog.Error("template tidak ada", "page", page)
		http.Error(w, "Terjadi kesalahan pada server.", http.StatusInternalServerError)
		return
	}
	if u, ok := auth.CurrentUser(r.Context()); ok {
		pd.User = u
	}
	pd.CSRFToken = auth.CSRFToken(r.Context())

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", pd); err != nil {
		slog.Error("render gagal", "page", page, "err", err)
		http.Error(w, "Terjadi kesalahan pada server.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// ErrorPage cocok dengan signature httpx.ErrorRenderer.
func (rd *Renderer) ErrorPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	rd.Render(w, r, status, "error", PageData{
		Title: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Data:  map[string]any{"Status": status, "Message": msg},
	})
}

func (rd *Renderer) Has(page string) bool {
	_, ok := rd.pages[page]
	return ok
}