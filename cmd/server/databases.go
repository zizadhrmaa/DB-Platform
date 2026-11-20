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

// --- JSON API Endpoints untuk Viewer ---

type apiDatabaseItem struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	DBType string `json:"db_type"`
	Level  string `json:"level"`
}

type apiDBHeader struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Level string `json:"level"`
}

type apiSchemaGroup struct {
	Schema string            `json:"schema"`
	Tables []dbmanager.Table `json:"tables"`
}

type apiDatabaseShowResponse struct {
	DB      apiDBHeader      `json:"db"`
	Schemas []apiSchemaGroup `json:"schemas"`
}

type apiTableShowResponse struct {
	DB    apiDBHeader            `json:"db"`
	Table *dbmanager.TableDetail `json:"table"`
}

type apiDataPage struct {
	Schema        string             `json:"schema"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	Columns       []string           `json:"columns"`
	Rows          [][]dbmanager.Cell `json:"rows"`
	Page          int                `json:"page"`
	Size          int                `json:"size"`
	Total         int64              `json:"total"`
	TotalEstimate bool               `json:"total_estimate"`
	HasPrev       bool               `json:"has_prev"`
	HasNext       bool               `json:"has_next"`
	PageCount     int64              `json:"page_count"`
	OrderedBy     string             `json:"ordered_by"`
}

type apiTableDataResponse struct {
	DB   apiDBHeader  `json:"db"`
	Page *apiDataPage `json:"page"`
}

func (app *App) apiDatabaseList(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.CurrentUser(r.Context())
	list, err := app.Authz.AccessibleDatabases(r.Context(), u)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res := make([]apiDatabaseItem, len(list))
	for i, d := range list {
		res[i] = apiDatabaseItem{
			ID:     d.DBID,
			Name:   d.Name,
			DBType: d.DBType,
			Level:  d.Level.String(),
		}
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (app *App) apiDatabaseShow(w http.ResponseWriter, r *http.Request) {
	db, h, ok := app.targetDB(w, r)
	if !ok {
		return
	}
	tables, err := dbmanager.ListTables(r.Context(), db)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var groups []apiSchemaGroup
	for _, t := range tables {
		if n := len(groups); n == 0 || groups[n-1].Schema != t.Schema {
			groups = append(groups, apiSchemaGroup{Schema: t.Schema})
		}
		groups[len(groups)-1].Tables = append(groups[len(groups)-1].Tables, t)
	}
	httpx.JSON(w, http.StatusOK, apiDatabaseShowResponse{
		DB:      apiDBHeader{ID: h.ID, Name: h.Name, Level: h.Level.String()},
		Schemas: groups,
	})
}

func (app *App) apiTableShow(w http.ResponseWriter, r *http.Request) {
	db, h, ok := app.targetDB(w, r)
	if !ok {
		return
	}
	t, err := dbmanager.DescribeTable(r.Context(), db, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, apiTableShowResponse{
		DB:    apiDBHeader{ID: h.ID, Name: h.Name, Level: h.Level.String()},
		Table: t,
	})
}

func (app *App) apiTableData(w http.ResponseWriter, r *http.Request) {
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
	httpx.JSON(w, http.StatusOK, apiTableDataResponse{
		DB: apiDBHeader{ID: h.ID, Name: h.Name, Level: h.Level.String()},
		Page: &apiDataPage{
			Schema:        p.Schema,
			Name:          p.Name,
			Kind:          p.Kind,
			Columns:       p.Columns,
			Rows:          p.Rows,
			Page:          p.Page,
			Size:          p.Size,
			Total:         p.Total,
			TotalEstimate: p.TotalEstimate,
			HasPrev:       p.HasPrev(),
			HasNext:       p.HasNext(),
			PageCount:     p.PageCount(),
			OrderedBy:     p.OrderedBy,
		},
	})
}
