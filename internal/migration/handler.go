package migration

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/view"
)

type Handler struct {
	Service Service
	Authz   authz.Authorizer
	View    *view.Renderer
}
type Page struct {
	DBID    int64
	Items   []Migration
	Item    *Migration
	Runs    []Run
	CanEdit bool
	Title   string
	SQL     string

	Analysis *Analysis // hasil EXPLAIN, hanya terisi setelah tombol analisis ditekan
}

func (h *Handler) ids(r *http.Request) (int64, int64, error) {
	dbID, e := httpx.PathID(r, "dbID")
	if e != nil {
		return 0, 0, e
	}
	if r.PathValue("migrationID") == "" {
		return dbID, 0, nil
	}
	id, e := httpx.PathID(r, "migrationID")
	return dbID, id, e
}
func (h *Handler) editor(r *http.Request, dbID int64) bool {
	u, _ := auth.CurrentUser(r.Context())
	ok, e := h.Authz.Can(r.Context(), u, dbID, authz.ActionEditMigration)
	return e == nil && ok
}
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, e error) { httpx.Error(w, r, e) }

// conflict menampilkan halaman detail beserta alasan aksi ditolak, misalnya
// versi sebelumnya belum dijalankan atau migration sudah sukses.
func (h *Handler) conflict(w http.ResponseWriter, r *http.Request, dbID, id int64, reason error) {
	h.detail(w, r, http.StatusConflict, dbID, id, reason.Error(), nil)
}

// detail merender halaman detail migration, dengan pesan error atau hasil
// analisis query kalau ada.
func (h *Handler) detail(w http.ResponseWriter, r *http.Request, status int, dbID, id int64, errMsg string, a *Analysis) {
	m, e := h.Service.Get(r.Context(), dbID, id)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	runs, e := h.Service.Runs(r.Context(), dbID, id)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.View.Render(w, r, status, "migration/detail", view.PageData{
		Title: fmt.Sprintf("Migration v%d", m.Version),
		Error: errMsg,
		Data:  Page{DBID: dbID, Item: m, Runs: runs, CanEdit: h.editor(r, dbID), Analysis: a},
	})
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	dbID, _, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	items, e := h.Service.List(r.Context(), dbID)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.View.Render(w, r, 200, "migration/list", view.PageData{Title: "Riwayat Migration", Data: Page{DBID: dbID, Items: items, CanEdit: h.editor(r, dbID)}})
}
func (h *Handler) New(w http.ResponseWriter, r *http.Request) {
	dbID, _, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.View.Render(w, r, 200, "migration/form", view.PageData{Title: "Buat Migration", Data: Page{DBID: dbID, CanEdit: true}})
}
func (h *Handler) form(w http.ResponseWriter, r *http.Request, p Page, e error) {
	if errors.Is(e, domain.ErrInvalidInput) {
		h.View.Render(w, r, 422, "migration/form", view.PageData{Title: "Periksa SQL", Error: e.Error(), Data: p})
		return
	}
	h.fail(w, r, e)
}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	dbID, _, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	if e = httpx.ParseForm(w, r, httpx.MaxSQLFormBytes); e != nil {
		h.fail(w, r, e)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	title, sqlText := r.PostFormValue("title"), r.PostFormValue("sql")
	m, e := h.Service.Create(r.Context(), u, dbID, title, sqlText)
	if e != nil {
		h.form(w, r, Page{DBID: dbID, Title: title, SQL: sqlText, CanEdit: true}, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, m.ID), 303)
}
func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.detail(w, r, http.StatusOK, dbID, id, "", nil)
}

// Explain menampilkan execution plan SQL migration di halaman detail.
func (h *Handler) Explain(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	if e = httpx.ParseForm(w, r, httpx.MaxFormBytes); e != nil {
		h.fail(w, r, e)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	a, e := h.Service.Explain(r.Context(), u, dbID, id, r.PostFormValue("analyze") == "on")
	switch {
	case errors.Is(e, domain.ErrConflict):
		h.conflict(w, r, dbID, id, e)
	case errors.Is(e, domain.ErrInvalidInput):
		h.detail(w, r, http.StatusUnprocessableEntity, dbID, id, e.Error(), nil)
	case e != nil:
		h.fail(w, r, e)
	default:
		h.detail(w, r, http.StatusOK, dbID, id, "", a)
	}
}
func (h *Handler) Edit(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	m, e := h.Service.Get(r.Context(), dbID, id)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	if !m.Status.Editable() {
		h.conflict(w, r, dbID, id, fmt.Errorf("%w: migration berstatus %s tidak bisa diubah", domain.ErrConflict, m.Status))
		return
	}
	h.View.Render(w, r, 200, "migration/form", view.PageData{Title: "Edit Migration", Data: Page{DBID: dbID, Item: m, Title: m.Title, SQL: m.SQL, CanEdit: true}})
}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	if e = httpx.ParseForm(w, r, httpx.MaxSQLFormBytes); e != nil {
		h.fail(w, r, e)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	title, sqlText := r.PostFormValue("title"), r.PostFormValue("sql")
	e = h.Service.Update(r.Context(), u, dbID, id, title, sqlText)
	if e != nil {
		if errors.Is(e, domain.ErrInvalidInput) {
			m, _ := h.Service.Get(r.Context(), dbID, id)
			h.form(w, r, Page{DBID: dbID, Item: m, Title: title, SQL: sqlText, CanEdit: true}, e)
			return
		}
		if errors.Is(e, domain.ErrConflict) {
			h.conflict(w, r, dbID, id, e)
			return
		}
		h.fail(w, r, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, id), 303)
}
func (h *Handler) Discard(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	if e = h.Service.Discard(r.Context(), u, dbID, id); e != nil {
		if errors.Is(e, domain.ErrConflict) {
			h.conflict(w, r, dbID, id, e)
			return
		}
		h.fail(w, r, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations", dbID), 303)
}
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	dbID, id, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	_, e = h.Service.Execute(r.Context(), u, dbID, id)
	if e != nil {
		if errors.Is(e, domain.ErrConflict) {
			h.conflict(w, r, dbID, id, e)
			return
		}
		h.fail(w, r, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/databases/%d/migrations/%d", dbID, id), 303)
}

// DiffPage berisi data halaman schema diff.
type DiffPage struct {
	DBID    int64
	Schemas []string
	Schema  string
	Desired string
	Result  *SchemaDiff
}

// DiffForm menampilkan DDL schema saat ini sebagai titik awal schema yang
// diinginkan.
func (h *Handler) DiffForm(w http.ResponseWriter, r *http.Request) {
	dbID, _, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	schemas, e := h.Service.Schemas(r.Context(), dbID)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = defaultSchema(schemas)
	}
	u, _ := auth.CurrentUser(r.Context())
	ddl, e := h.Service.CurrentSchema(r.Context(), u, dbID, schema)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.View.Render(w, r, http.StatusOK, "migration/diff", view.PageData{
		Title: "Schema diff",
		Data:  DiffPage{DBID: dbID, Schemas: schemas, Schema: schema, Desired: ddl},
	})
}

// Diff membandingkan schema saat ini dengan schema yang diinginkan dan
// menampilkan kandidat SQL migration yang masih bisa diubah.
func (h *Handler) Diff(w http.ResponseWriter, r *http.Request) {
	dbID, _, e := h.ids(r)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	if e = httpx.ParseForm(w, r, httpx.MaxSQLFormBytes); e != nil {
		h.fail(w, r, e)
		return
	}
	schemas, e := h.Service.Schemas(r.Context(), dbID)
	if e != nil {
		h.fail(w, r, e)
		return
	}
	p := DiffPage{DBID: dbID, Schemas: schemas, Schema: r.PostFormValue("schema"), Desired: r.PostFormValue("desired")}
	u, _ := auth.CurrentUser(r.Context())
	p.Result, e = h.Service.Diff(r.Context(), u, dbID, p.Schema, p.Desired)
	if errors.Is(e, domain.ErrInvalidInput) {
		h.View.Render(w, r, http.StatusUnprocessableEntity, "migration/diff", view.PageData{Title: "Schema diff", Error: e.Error(), Data: p})
		return
	}
	if e != nil {
		h.fail(w, r, e)
		return
	}
	h.View.Render(w, r, http.StatusOK, "migration/diff", view.PageData{Title: "Schema diff", Data: p})
}

// defaultSchema memilih public kalau ada, selain itu schema pertama.
func defaultSchema(schemas []string) string {
	for _, s := range schemas {
		if s == "public" {
			return s
		}
	}
	if len(schemas) > 0 {
		return schemas[0]
	}
	return "public"
}
