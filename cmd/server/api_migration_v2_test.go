package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

type executionJSON struct {
	ExecutionID        int64  `json:"execution_id"`
	Kind               string `json:"kind"`
	Status             string `json:"status"`
	Revision           int    `json:"revision"`
	SQL                string `json:"sql"`
	ExecutedBy         string `json:"executed_by"`
	RevertsExecutionID *int64 `json:"reverts_execution_id"`
	Version            int    `json:"version"`
	ErrorMessage       string `json:"error_message"`
}

type migrationDetailJSON struct {
	Migration struct {
		ID                 int64  `json:"id"`
		Status             string `json:"status"`
		Revision           int    `json:"revision"`
		SQLDown            string `json:"sql_down"`
		Irreversible       bool   `json:"irreversible"`
		IrreversibleReason string `json:"irreversible_reason"`
	} `json:"migration"`
	Executions               []executionJSON `json:"executions"`
	LatestAppliedExecutionID *int64          `json:"latest_applied_execution_id"`
}

func createMigrationAPI(t *testing.T, b *browser, dbID string, body map[string]any) int64 {
	t.Helper()
	rr := b.api(http.MethodPost, "/api/databases/"+dbID+"/migrations", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("buat migration: status %d (%s)", rr.Code, rr.Body.String())
	}
	return decodeBody[struct {
		ID int64 `json:"id"`
	}](t, rr).ID
}

// runViaRequest menjalankan migration dengan jenis kind lewat alur lengkap:
// request → submit → approve semua leader → enqueue → worker.
func runViaRequest(t *testing.T, e *migrationEnv, bob, carol *browser, dbID string, migID int64, kind string) {
	t.Helper()
	rr := bob.api(http.MethodPost, fmt.Sprintf("/api/databases/%s/migrations/%d/requests", dbID, migID), map[string]any{"kind": kind})
	if rr.Code != http.StatusCreated {
		t.Fatalf("request %s: status %d (%s)", kind, rr.Code, rr.Body.String())
	}
	base := fmt.Sprintf("/api/databases/%s/requests/%d", dbID, decodeBody[struct {
		ID int64 `json:"id"`
	}](t, rr).ID)
	if rr := bob.api(http.MethodPost, base+"/submit", nil); rr.Code != http.StatusOK {
		t.Fatalf("submit %s: %d (%s)", kind, rr.Code, rr.Body.String())
	}
	for _, b := range []*browser{bob, carol} {
		if rr := b.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusOK {
			t.Fatalf("approve %s: %d (%s)", kind, rr.Code, rr.Body.String())
		}
	}
	if rr := bob.api(http.MethodPost, base+"/enqueue", nil); rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue %s: %d (%s)", kind, rr.Code, rr.Body.String())
	}
	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker %s: ok=%v err=%v", kind, ok, err)
	}
}

func TestAPIMigrationCreateEditDiscard(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	path := "/api/databases/" + dbID + "/migrations"

	if rr := bob.api(http.MethodPost, path, map[string]any{"title": "x", "sql_up": "CREATE TABLE x (id INT)"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("tanpa SQL down dan bukan irreversible harus 400, dapat %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := carol.api(http.MethodPost, path, map[string]any{"title": "x", "sql_up": "SELECT 1", "sql_down": "SELECT 1"}); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only membuat migration: status %d, want 403", rr.Code)
	}
	irr := createMigrationAPI(t, bob, dbID, map[string]any{
		"title": "hapus", "sql_up": "CREATE TABLE irr (id INT)", "irreversible": true, "irreversible_reason": "data dihapus",
	})
	id := createMigrationAPI(t, bob, dbID, map[string]any{
		"title": "kota", "sql_up": "CREATE TABLE kota (id INT)", "sql_down": "DROP TABLE kota",
	})

	rr := bob.api(http.MethodPatch, fmt.Sprintf("%s/%d", path, id), map[string]any{
		"title": "kota2", "sql_up": "CREATE TABLE kota (id INT, nama TEXT)", "sql_down": "DROP TABLE kota",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("edit: status %d (%s)", rr.Code, rr.Body.String())
	}
	d := decodeBody[migrationDetailJSON](t, carol.api(http.MethodGet, fmt.Sprintf("%s/%d", path, id), nil))
	if d.Migration.Revision != 2 || d.Migration.SQLDown != "DROP TABLE kota" || d.Migration.Irreversible || len(d.Executions) != 0 {
		t.Fatalf("detail setelah edit: %+v", d)
	}
	d = decodeBody[migrationDetailJSON](t, bob.api(http.MethodGet, fmt.Sprintf("%s/%d", path, irr), nil))
	if !d.Migration.Irreversible || d.Migration.IrreversibleReason != "data dihapus" {
		t.Fatalf("detail migration irreversible: %+v", d.Migration)
	}

	if rr := bob.api(http.MethodPost, fmt.Sprintf("%s/%d/discard", path, irr), nil); rr.Code != http.StatusOK {
		t.Fatalf("discard: status %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := carol.api(http.MethodPost, fmt.Sprintf("%s/%d/discard", path, id), nil); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only discard: status %d, want 403", rr.Code)
	}
	if rr := bob.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/migrations/%d", strings.Split(e.other, "/")[2], id), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("migration lewat database lain: status %d, want 404", rr.Code)
	}
}

// Alur lengkap up → rollback → forward lewat approval dan worker, lalu
// riwayat dengan dan tanpa Hide rollback.
func TestAPIRollbackForwardAndHistory(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	setLeaders(t, e)

	a := createMigrationAPI(t, bob, dbID, map[string]any{"title": "A", "sql_up": "CREATE TABLE ta (id INT)", "sql_down": "DROP TABLE ta"})
	b := createMigrationAPI(t, bob, dbID, map[string]any{"title": "B", "sql_up": "CREATE TABLE tb (id INT)", "sql_down": "DROP TABLE tb"})
	runViaRequest(t, e, bob, carol, dbID, a, "up")
	runViaRequest(t, e, bob, carol, dbID, b, "up")

	// Request rollback A boleh dibuat (A sukses dan reversible), tetapi
	// ditolak saat enqueue karena A bukan migration aktif terakhir.
	rr := bob.api(http.MethodPost, fmt.Sprintf("/api/databases/%s/migrations/%d/requests", dbID, a), map[string]any{"kind": "rollback"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("request rollback A: status %d (%s)", rr.Code, rr.Body.String())
	}
	rbA := fmt.Sprintf("/api/databases/%s/requests/%d", dbID, decodeBody[struct {
		ID int64 `json:"id"`
	}](t, rr).ID)
	if got := decodeBody[struct {
		SQL string `json:"sql"`
	}](t, bob.api(http.MethodGet, rbA, nil)); got.SQL != "DROP TABLE ta" {
		t.Fatalf("request rollback harus menampilkan SQL down untuk direview, dapat %q", got.SQL)
	}
	bob.api(http.MethodPost, rbA+"/submit", nil)
	bob.api(http.MethodPost, rbA+"/approve", nil)
	carol.api(http.MethodPost, rbA+"/approve", nil)
	if rr := bob.api(http.MethodPost, rbA+"/enqueue", nil); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "aktif terakhir") {
		t.Fatalf("enqueue rollback A sebelum B: status %d (%s), want 409", rr.Code, rr.Body.String())
	}
	if rr := bob.api(http.MethodPost, rbA+"/cancel", nil); rr.Code != http.StatusOK {
		t.Fatalf("batalkan request rollback A: status %d (%s)", rr.Code, rr.Body.String())
	}

	runViaRequest(t, e, bob, carol, dbID, b, "rollback")
	if e.tableExists(t, "tb") {
		t.Fatal("rollback B harus menjalankan SQL down")
	}
	d := decodeBody[migrationDetailJSON](t, bob.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/migrations/%d", dbID, b), nil))
	if d.Migration.Status != "rolled_back" || len(d.Executions) != 2 || d.Executions[0].Kind != "rollback" ||
		d.Executions[0].RevertsExecutionID == nil || *d.Executions[0].RevertsExecutionID != d.Executions[1].ExecutionID {
		t.Fatalf("detail B setelah rollback: %+v", d)
	}
	runViaRequest(t, e, bob, carol, dbID, b, "forward")
	if !e.tableExists(t, "tb") {
		t.Fatal("forward B harus menjalankan SQL up lagi")
	}

	full := decodeBody[[]executionJSON](t, carol.api(http.MethodGet, "/api/databases/"+dbID+"/history", nil))
	kinds := []string{}
	for _, h := range full {
		kinds = append(kinds, h.Kind)
	}
	if strings.Join(kinds, ",") != "up,up,rollback,forward" {
		t.Fatalf("riwayat lengkap: %v", kinds)
	}
	if full[0].SQL != "CREATE TABLE ta (id INT)" || full[0].ExecutedBy != "bob" || full[0].Version != 1 {
		t.Fatalf("riwayat harus memuat salinan SQL, pengguna, dan versi: %+v", full[0])
	}
	hidden := decodeBody[[]executionJSON](t, carol.api(http.MethodGet, "/api/databases/"+dbID+"/history?hide_rollback=true", nil))
	if len(hidden) != 2 || hidden[0].Kind != "up" || hidden[1].Kind != "forward" {
		t.Fatalf("hide rollback harus menyisakan up A dan forward B: %+v", hidden)
	}
	if rr := dave.api(http.MethodGet, "/api/databases/"+dbID+"/history", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("riwayat tanpa akses: status %d, want 404", rr.Code)
	}
}
