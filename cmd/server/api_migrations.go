package main

import (
	"net/http"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
)

// migrationResponse adalah bentuk JSON migration untuk SPA. Dibuat di sini
// supaya package migration tidak perlu tahu format API.
type migrationResponse struct {
	ID                 int64      `json:"id"`
	Version            int        `json:"version"`
	Revision           int        `json:"revision"`
	Title              string     `json:"title"`
	SQL                string     `json:"sql"` // SQL up
	SQLDown            string     `json:"sql_down"`
	Irreversible       bool       `json:"irreversible"`
	IrreversibleReason string     `json:"irreversible_reason"`
	Status             string     `json:"status"`
	CreatedBy          string     `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ExecutedBy         string     `json:"executed_by"`
	ExecutedAt         *time.Time `json:"executed_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	ErrorMessage       string     `json:"error_message"`
}

func toMigrationResponse(m migration.Migration) migrationResponse {
	return migrationResponse{
		ID: m.ID, Version: m.Version, Revision: m.Revision, Title: m.Title, SQL: m.SQL,
		SQLDown: m.SQLDown, Irreversible: m.Irreversible, IrreversibleReason: m.IrreversibleReason,
		Status: string(m.Status), CreatedBy: m.CreatedBy, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		ExecutedBy: m.ExecutedBy, ExecutedAt: m.ExecutedAt, FinishedAt: m.FinishedAt,
		ErrorMessage: m.ErrorMessage,
	}
}

// executionResponse dipakai untuk attempt migration dan baris riwayat.
type executionResponse struct {
	ExecutionID        int64      `json:"execution_id"`
	MigrationID        int64      `json:"migration_id"`
	Version            int        `json:"version,omitempty"`
	Title              string     `json:"title,omitempty"`
	Kind               string     `json:"kind"`
	Revision           int        `json:"revision"`
	Status             string     `json:"status"`
	SQL                string     `json:"sql"`
	ExecutedBy         string     `json:"executed_by"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	RevertsExecutionID *int64     `json:"reverts_execution_id"`
	ErrorMessage       string     `json:"error_message"`
}

type migrationDetailResponse struct {
	Migration  migrationResponse   `json:"migration"`
	Executions []executionResponse `json:"executions"`
	// Eksekusi up/forward aktif terakhir: target rollback berikutnya.
	LatestAppliedExecutionID *int64 `json:"latest_applied_execution_id"`
}

type migrationDraftRequest struct {
	Title              string `json:"title"`
	SQLUp              string `json:"sql_up"`
	SQLDown            string `json:"sql_down"`
	Irreversible       bool   `json:"irreversible"`
	IrreversibleReason string `json:"irreversible_reason"`
}

func (r migrationDraftRequest) draft() migration.Draft {
	return migration.Draft{Title: r.Title, SQLUp: r.SQLUp, SQLDown: r.SQLDown, Irreversible: r.Irreversible, IrreversibleReason: r.IrreversibleReason}
}

func migrationIDs(r *http.Request) (int64, int64, error) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		return 0, 0, err
	}
	id, err := httpx.PathID(r, "migrationID")
	return dbID, id, err
}

// apiListMigrations: GET /api/databases/{dbID}/migrations, urut versi.
func (app *App) apiListMigrations(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, err := app.Migration.Service.List(r.Context(), dbID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]migrationResponse, 0, len(items))
	for _, m := range items {
		out = append(out, toMigrationResponse(m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// apiCreateMigration: POST /api/databases/{dbID}/migrations
func (app *App) apiCreateMigration(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req migrationDraftRequest
	if err := httpx.ReadJSONLimit(w, r, &req, httpx.MaxSQLFormBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	m, err := app.Migration.Service.CreateDraft(r.Context(), u, dbID, req.draft())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toMigrationResponse(*m))
}

// apiGetMigration: GET /api/databases/{dbID}/migrations/{migrationID}
func (app *App) apiGetMigration(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := migrationIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	m, err := app.Migration.Service.Get(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	runs, err := app.Migration.Service.Runs(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := migrationDetailResponse{Migration: toMigrationResponse(*m), Executions: make([]executionResponse, 0, len(runs))}
	for _, run := range runs {
		out.Executions = append(out.Executions, executionResponse{
			ExecutionID: run.ID, MigrationID: run.MigrationID, Kind: string(run.Kind), Revision: run.Revision,
			Status: string(run.Status), SQL: run.SQL, ExecutedBy: run.ExecutedBy, StartedAt: run.StartedAt,
			FinishedAt: run.FinishedAt, RevertsExecutionID: run.RevertsRunID, ErrorMessage: run.ErrorMessage,
		})
	}
	if last, ok := migration.LatestApplied(runs); ok && m.Status == migration.StatusSuccess {
		out.LatestAppliedExecutionID = &last.ID
	}
	httpx.JSON(w, http.StatusOK, out)
}

// apiUpdateMigration: PATCH /api/databases/{dbID}/migrations/{migrationID}
// Hanya untuk migration pending/failed yang belum masuk request atau antrean.
func (app *App) apiUpdateMigration(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := migrationIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req migrationDraftRequest
	if err := httpx.ReadJSONLimit(w, r, &req, httpx.MaxSQLFormBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	if err := app.Migration.Service.UpdateDraft(r.Context(), u, dbID, id, req.draft()); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.writeMigration(w, r, dbID, id)
}

// apiDiscardMigration: POST /api/databases/{dbID}/migrations/{migrationID}/discard
func (app *App) apiDiscardMigration(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := migrationIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	if err := app.Migration.Service.Discard(r.Context(), u, dbID, id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	app.writeMigration(w, r, dbID, id)
}

func (app *App) writeMigration(w http.ResponseWriter, r *http.Request, dbID, id int64) {
	m, err := app.Migration.Service.Get(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMigrationResponse(*m))
}

// apiHistory: GET /api/databases/{dbID}/history?hide_rollback=true
// Riwayat eksekusi: semua yang committed (tidak bisa diubah) dan attempt
// yang gagal. hide_rollback menyembunyikan pasangan eksekusi yang dibatalkan
// dan rollback suksesnya berdasarkan execution ID.
func (app *App) apiHistory(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	hide := false
	switch r.URL.Query().Get("hide_rollback") {
	case "", "false", "0":
	case "true", "1":
		hide = true
	default:
		httpx.Fail(w, r, httpx.Invalid("hide_rollback harus true atau false."))
		return
	}
	entries, err := app.Migration.Service.History(r.Context(), dbID, hide)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]executionResponse, 0, len(entries))
	for _, h := range entries {
		out = append(out, executionResponse{
			ExecutionID: h.ExecutionID, MigrationID: h.MigrationID, Version: h.Version, Title: h.Title,
			Kind: string(h.Kind), Revision: h.Revision, Status: string(h.Status), SQL: h.SQL,
			ExecutedBy: h.ExecutedBy, StartedAt: h.StartedAt, FinishedAt: h.FinishedAt,
			RevertsExecutionID: h.RevertsExecutionID, ErrorMessage: h.ErrorMessage,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}
