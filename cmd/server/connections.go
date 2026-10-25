package main

import (
	"fmt"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
)

var connFlash = map[string]string{
	"conn-created": "Koneksi berhasil disimpan.",
	"conn-updated": "Perubahan koneksi disimpan.",
	"conn-deleted": "Koneksi berhasil dihapus.",
	"conn-ok":      "Koneksi berhasil. Database bisa dihubungi dengan data yang tersimpan.",
}

type connectionPage struct {
	ID       int64 // 0 untuk koneksi baru
	Form     admin.ConnectionForm
	SSLModes []string
}

func connPath(id int64) string { return fmt.Sprintf("/admin/databases/%d", id) }

func formFromInfo(c *dbmanager.ConnectionInfo) admin.ConnectionForm {
	return admin.ConnectionForm{
		Name:     c.Name,
		Host:     c.Host,
		Port:     fmt.Sprint(c.Port),
		Database: c.Database,
		Username: c.Username,
		SSLMode:  c.SSLMode,
		IsActive: c.IsActive,
	}
}

func connectionFormFrom(r *http.Request) admin.ConnectionForm {
	return admin.ConnectionForm{
		Name:     r.PostFormValue("name"),
		Host:     r.PostFormValue("host"),
		Port:     r.PostFormValue("port"),
		Database: r.PostFormValue("database"),
		Username: r.PostFormValue("username"),
		SSLMode:  r.PostFormValue("sslmode"),
		IsActive: r.PostFormValue("is_active") == "on",
	}
}

// Password tidak pernah dikirim balik ke form, termasuk saat validasi gagal.
func (app *App) renderConnection(w http.ResponseWriter, r *http.Request, status int, id int64, form admin.ConnectionForm, flash, errMsg string) {
	title := "Koneksi baru"
	if id != 0 {
		title = "Koneksi " + form.Name
	}
	app.View.Render(w, r, status, "admin/database_form", view.PageData{
		Title: title,
		Flash: flash,
		Error: errMsg,
		Data:  connectionPage{ID: id, Form: form, SSLModes: admin.SSLModes},
	})
}

func (app *App) adminConnections(w http.ResponseWriter, r *http.Request) {
	list, err := app.Connections.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "admin/databases", view.PageData{
		Title: "Koneksi database",
		Flash: connFlash[r.URL.Query().Get("ok")],
		Data:  list,
	})
}

func (app *App) adminNewConnection(w http.ResponseWriter, r *http.Request) {
	app.renderConnection(w, r, http.StatusOK, 0, admin.ConnectionForm{Port: "5432", IsActive: true}, "", "")
}

func (app *App) adminCreateConnection(w http.ResponseWriter, r *http.Request) {
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	form := connectionFormFrom(r)
	actor, _ := auth.CurrentUser(r.Context())
	id, err := app.Connections.Create(r.Context(), form, r.PostFormValue("password"), actor.ID)
	if msg, status, ok := formErrorStatus(err); ok {
		app.renderConnection(w, r, status, 0, form, "", msg)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	redirectOK(w, r, connPath(id), "conn-created")
}

func (app *App) adminConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	c, err := app.Connections.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.renderConnection(w, r, http.StatusOK, id, formFromInfo(c), connFlash[r.URL.Query().Get("ok")], "")
}

func (app *App) adminUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	form := connectionFormFrom(r)
	err = app.Connections.Update(r.Context(), id, form, r.PostFormValue("password"))
	if msg, status, ok := formErrorStatus(err); ok {
		app.renderConnection(w, r, status, id, form, "", msg)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Pool lama dibuang supaya kredensial dan status yang baru langsung dipakai.
	app.Pools.Invalidate(id)
	redirectOK(w, r, connPath(id), "conn-updated")
}

func (app *App) adminTestConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	c, err := app.Connections.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := app.Connections.Test(r.Context(), id); err != nil {
		app.renderConnection(w, r, http.StatusOK, id, formFromInfo(c), "",
			"Koneksi gagal. Periksa host, port, nama database, username, dan password.")
		return
	}
	redirectOK(w, r, connPath(id), "conn-ok")
}

func (app *App) adminDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := httpx.ParseForm(w, r, httpx.MaxFormBytes); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if r.PostFormValue("confirm") != "on" {
		c, err := app.Connections.Get(r.Context(), id)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		app.renderConnection(w, r, http.StatusUnprocessableEntity, id, formFromInfo(c), "",
			"Centang konfirmasi sebelum menghapus koneksi.")
		return
	}
	if err := app.Connections.Delete(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.Pools.Invalidate(id)
	redirectOK(w, r, "/admin/databases", "conn-deleted")
}
