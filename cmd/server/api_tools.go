package main

import (
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
)

// JSON API untuk fitur bonus Milestone 1 (analisis query dan schema diff)
// supaya bisa dipakai dari SPA. Logikanya tetap di package migration.

type planNodeResponse struct {
	Depth        int     `json:"depth"`
	Operation    string  `json:"operation"`
	Target       string  `json:"target"`
	Condition    string  `json:"condition"`
	StartupCost  float64 `json:"startup_cost"`
	TotalCost    float64 `json:"total_cost"`
	PlanRows     float64 `json:"plan_rows"`
	HasActual    bool    `json:"has_actual"`
	ActualMs     float64 `json:"actual_ms"`
	ActualRows   float64 `json:"actual_rows"`
	Loops        float64 `json:"loops"`
	RowsRemoved  float64 `json:"rows_removed"`
	UsesIndex    bool    `json:"uses_index"`
	IsSeqScan    bool    `json:"is_seq_scan"`
	JoinStrategy string  `json:"join_strategy"`
}

type statementPlanResponse struct {
	SQL         string             `json:"sql"`
	Explainable bool               `json:"explainable"`
	Error       string             `json:"error"`
	PlanningMs  float64            `json:"planning_ms"`
	ExecutionMs float64            `json:"execution_ms"`
	Nodes       []planNodeResponse `json:"nodes"`
}

type analysisResponse struct {
	Analyze    bool                    `json:"analyze"`
	Statements []statementPlanResponse `json:"statements"`
}

func toAnalysisResponse(a *migration.Analysis) analysisResponse {
	out := analysisResponse{Analyze: a.Analyze, Statements: make([]statementPlanResponse, 0, len(a.Statements))}
	for _, s := range a.Statements {
		st := statementPlanResponse{SQL: s.SQL, Explainable: s.Explainable, Error: s.Error,
			PlanningMs: s.PlanningMs, ExecutionMs: s.ExecutionMs, Nodes: make([]planNodeResponse, 0, len(s.Nodes))}
		for _, n := range s.Nodes {
			st.Nodes = append(st.Nodes, planNodeResponse{
				Depth: n.Depth, Operation: n.Operation, Target: n.Target, Condition: n.Condition,
				StartupCost: n.StartupCost, TotalCost: n.TotalCost, PlanRows: n.PlanRows,
				HasActual: n.HasActual, ActualMs: n.ActualMs, ActualRows: n.ActualRows, Loops: n.Loops,
				RowsRemoved: n.RowsRemoved, UsesIndex: n.UsesIndex, IsSeqScan: n.IsSeqScan, JoinStrategy: n.JoinStrategy,
			})
		}
		out.Statements = append(out.Statements, st)
	}
	return out
}

// apiExplainMigration: POST /api/databases/{dbID}/migrations/{migrationID}/explain
// body {"analyze": bool}. Statement dijalankan di transaction yang selalu
// di-rollback, jadi isi database target tidak berubah.
func (app *App) apiExplainMigration(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := migrationIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req struct {
		Analyze bool `json:"analyze"`
	}
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	a, err := app.Migration.Service.Explain(r.Context(), u, dbID, id, req.Analyze)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAnalysisResponse(a))
}

type schemaResponse struct {
	Schemas []string `json:"schemas"`
	Schema  string   `json:"schema"`
	DDL     string   `json:"ddl"`
}

// apiCurrentSchema: GET /api/databases/{dbID}/schema-diff?schema=
// Daftar schema dan DDL schema terpilih saat ini sebagai titik awal.
func (app *App) apiCurrentSchema(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	schemas, err := app.Migration.Service.Schemas(r.Context(), dbID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = migration.DefaultSchema(schemas)
	}
	u, _ := auth.CurrentUser(r.Context())
	ddl, err := app.Migration.Service.CurrentSchema(r.Context(), u, dbID, schema)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, schemaResponse{Schemas: schemas, Schema: schema, DDL: ddl})
}

type schemaChangeResponse struct {
	Action      string `json:"action"`
	Object      string `json:"object"`
	Detail      string `json:"detail"`
	Destructive bool   `json:"destructive"`
}

type schemaDiffResponse struct {
	Schema  string                 `json:"schema"`
	Changes []schemaChangeResponse `json:"changes"`
	SQL     string                 `json:"sql"`
}

// apiSchemaDiff: POST /api/databases/{dbID}/schema-diff
// body {"schema", "desired"}. Mengembalikan daftar perbedaan dan kandidat
// SQL migration; tidak ada yang disimpan atau dijalankan.
func (app *App) apiSchemaDiff(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req struct {
		Schema  string `json:"schema"`
		Desired string `json:"desired"`
	}
	if err := httpx.ReadJSONLimit(w, r, &req, httpx.MaxSQLFormBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	d, err := app.Migration.Service.Diff(r.Context(), u, dbID, req.Schema, req.Desired)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := schemaDiffResponse{Schema: d.Schema, SQL: d.SQL, Changes: make([]schemaChangeResponse, 0, len(d.Changes))}
	for _, c := range d.Changes {
		out.Changes = append(out.Changes, schemaChangeResponse{Action: c.Action, Object: c.Object, Detail: c.Detail, Destructive: c.Destructive})
	}
	httpx.JSON(w, http.StatusOK, out)
}
