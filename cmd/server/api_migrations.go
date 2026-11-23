package main

import (
	"net/http"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
)

// migrationResponse adalah bentuk JSON migration untuk SPA. Dibuat di sini
// supaya package migration tidak perlu tahu format API.
type migrationResponse struct {
	ID           int64      `json:"id"`
	Version      int        `json:"version"`
	Title        string     `json:"title"`
	SQL          string     `json:"sql"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExecutedBy   string     `json:"executed_by"`
	ExecutedAt   *time.Time `json:"executed_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ErrorMessage string     `json:"error_message"`
}

func toMigrationResponse(m migration.Migration) migrationResponse {
	return migrationResponse{
		ID: m.ID, Version: m.Version, Title: m.Title, SQL: m.SQL, Status: string(m.Status),
		CreatedBy: m.CreatedBy, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		ExecutedBy: m.ExecutedBy, ExecutedAt: m.ExecutedAt, FinishedAt: m.FinishedAt,
		ErrorMessage: m.ErrorMessage,
	}
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
