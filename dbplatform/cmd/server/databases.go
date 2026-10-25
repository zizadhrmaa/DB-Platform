package main

import (
	"database/sql"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
)

func (app *App) databaseList(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.CurrentUser(r.Context())
	list, err := app.Authz.AccessibleDatabases(r.Context(), u)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "databases/list", view.PageData{Title: "Database", Data: list})
}

// dbHeader adalah informasi yang tampil di bagian atas semua halaman viewer.
type dbHeader struct {
	ID    int64
	Name  string
	Level domain.AccessLevel
}

// targetDB mengambil pool ke database yang sudah lolos authz.RequireDB.
func (app *App) targetDB(w http.ResponseWriter, r *http.Request) (*sql.DB, dbHeader, bool) {
	d, _ := authz.FromContext(r.Context())
	info, err := app.Connections.Get(r.Context(), d.DBID)
	if err != nil {
		httpx.Error(w, r, err)
		return nil, dbHeader{}, false
	}
	db, err := app.Pools.Conn(r.Context(), d.DBID)
	if err != nil {
		httpx.Error(w, r, err)
		return nil, dbHeader{}, false
	}
	return db, dbHeader{ID: d.DBID, Name: info.Name, Level: d.Level}, true
}

type schemaGroup struct {
	Schema string
	Tables []dbmanager.Table
}

func (app *App) databaseShow(w http.ResponseWriter, r *http.Request) {
	db, h, ok := app.targetDB(w, r)
	if !ok {
		return
	}
	tables, err := dbmanager.ListTables(r.Context(), db)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Hasil ListTables sudah urut per schema.
	var groups []schemaGroup
	for _, t := range tables {
		if n := len(groups); n == 0 || groups[n-1].Schema != t.Schema {
			groups = append(groups, schemaGroup{Schema: t.Schema})
		}
		groups[len(groups)-1].Tables = append(groups[len(groups)-1].Tables, t)
	}
	app.View.Render(w, r, http.StatusOK, "databases/show", view.PageData{
		Title: h.Name,
		Data: struct {
			DB      dbHeader
			Schemas []schemaGroup
		}{h, groups},
	})
}

func (app *App) tableShow(w http.ResponseWriter, r *http.Request) {
	db, h, ok := app.targetDB(w, r)
	if !ok {
		return
	}
	t, err := dbmanager.DescribeTable(r.Context(), db, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "databases/table", view.PageData{
		Title: t.Schema + "." + t.Name,
		Data: struct {
			DB    dbHeader
			Table *dbmanager.TableDetail
		}{h, t},
	})
}

func (app *App) tableData(w http.ResponseWriter, r *http.Request) {
	db, h, ok := app.targetDB(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, size := dbmanager.ClampPage(q.Get("page"), q.Get("size"))
	p, err := dbmanager.TableData(r.Context(), db, r.PathValue("schema"), r.PathValue("table"), page, size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	app.View.Render(w, r, http.StatusOK, "databases/data", view.PageData{
		Title: "Data " + p.Schema + "." + p.Name,
		Data: struct {
			DB   dbHeader
			Page *dbmanager.DataPage
		}{h, p},
	})
}
