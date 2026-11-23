package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func (e *migrationEnv) seedBig(t *testing.T, n int) {
	t.Helper()
	if _, err := e.target.Exec(`CREATE TABLE big (id BIGINT PRIMARY KEY, v BIGINT NOT NULL CHECK (v >= 0 AND v < 1000000))`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.target.Exec(`INSERT INTO big SELECT g, g FROM generate_series(1, $1::int) g`, n); err != nil {
		t.Fatal(err)
	}
}

func (e *migrationEnv) countTarget(t *testing.T, q string) int64 {
	t.Helper()
	var n int64
	if err := e.target.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Alur batch lewat API: approval → antrean → worker per batch → partial →
// resume dari checkpoint → succeeded; lalu revert job partial lain memakai
// request rollback yang disetujui.
func TestAPIBatchJobPartialResumeAndRevert(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	setLeaders(t, e)
	e.seedBig(t, 5000)
	if _, err := e.target.Exec(`UPDATE big SET v = 999999 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}

	migID := createMigrationAPI(t, bob, dbID, map[string]any{
		"title": "gandakan", "exec_mode": "batch", "batch_table": "big", "batch_column": "id", "batch_size": 1000,
		"sql_up":   "UPDATE big SET v = v + id WHERE id > $1 AND id <= $2",
		"sql_down": "UPDATE big SET v = v - id WHERE id > $1 AND id <= $2",
	})
	runViaRequest(t, e, bob, carol, dbID, migID, "up")

	jobs := decodeBody[[]jobJSON](t, bob.api(http.MethodGet, "/api/databases/"+dbID+"/jobs", nil))
	if len(jobs) != 1 || jobs[0].Mode != "batch" || jobs[0].Status != "partial" || jobs[0].BatchesCommitted != 3 || jobs[0].RowsCommitted != 3000 {
		t.Fatalf("job batch yang gagal di batch 4 harus partial dengan 3 batch: %+v", jobs)
	}
	jobPath := fmt.Sprintf("/api/databases/%s/jobs/%d", dbID, jobs[0].ID)

	if rr := carol.api(http.MethodPost, jobPath+"/resume", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only resume: status %d, want 403", rr.Code)
	}
	if _, err := e.target.Exec(`UPDATE big SET v = 3500 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}
	if rr := bob.api(http.MethodPost, jobPath+"/resume", nil); rr.Code != http.StatusAccepted {
		t.Fatalf("resume: status %d (%s)", rr.Code, rr.Body.String())
	}
	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker resume: ok=%v err=%v", ok, err)
	}
	j := decodeBody[jobJSON](t, bob.api(http.MethodGet, jobPath, nil))
	if j.Status != "succeeded" || j.BatchesCommitted != 5 || j.RowsCommitted != 5000 {
		t.Fatalf("resume harus menyelesaikan semua batch: %+v", j)
	}
	if n := e.countTarget(t, `SELECT count(*) FROM big WHERE v <> 2 * id`); n != 0 {
		t.Fatalf("%d row tidak diproses tepat sekali", n)
	}
	if rr := bob.api(http.MethodPost, jobPath+"/resume", nil); rr.Code != http.StatusConflict {
		t.Fatalf("resume job yang sudah selesai: status %d, want 409", rr.Code)
	}
}

func TestAPIBatchRevertWithApprovedRollbackRequest(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	setLeaders(t, e)
	e.seedBig(t, 4000)
	if _, err := e.target.Exec(`UPDATE big SET v = 999999 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}
	migID := createMigrationAPI(t, bob, dbID, map[string]any{
		"title": "gandakan", "exec_mode": "batch", "batch_table": "big", "batch_column": "id", "batch_size": 1000,
		"sql_up":   "UPDATE big SET v = v + id WHERE id > $1 AND id <= $2",
		"sql_down": "UPDATE big SET v = v - id WHERE id > $1 AND id <= $2",
	})
	runViaRequest(t, e, bob, carol, dbID, migID, "up")
	jobID := decodeBody[[]jobJSON](t, bob.api(http.MethodGet, "/api/databases/"+dbID+"/jobs", nil))[0].ID

	// Request rollback disetujui semua leader lalu dipasangkan ke job.
	rr := bob.api(http.MethodPost, fmt.Sprintf("/api/databases/%s/migrations/%d/requests", dbID, migID), map[string]any{"kind": "rollback"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("request rollback batch: %d (%s)", rr.Code, rr.Body.String())
	}
	reqID := decodeBody[struct {
		ID int64 `json:"id"`
	}](t, rr).ID
	base := fmt.Sprintf("/api/databases/%s/requests/%d", dbID, reqID)
	bob.api(http.MethodPost, base+"/submit", nil)
	for _, b := range []*browser{bob, carol} {
		if rr := b.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusOK {
			t.Fatalf("approve: %d (%s)", rr.Code, rr.Body.String())
		}
	}
	jobPath := fmt.Sprintf("/api/databases/%s/jobs/%d", dbID, jobID)
	if rr := bob.api(http.MethodPost, jobPath+"/revert", map[string]any{"request_id": reqID}); rr.Code != http.StatusAccepted {
		t.Fatalf("revert: %d (%s)", rr.Code, rr.Body.String())
	}
	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker revert: ok=%v err=%v", ok, err)
	}
	j := decodeBody[jobJSON](t, bob.api(http.MethodGet, jobPath, nil))
	if j.Status != "reverted" || j.BatchesCommitted != 0 {
		t.Fatalf("semua batch harus dibatalkan: %+v", j)
	}
	if n := e.countTarget(t, `SELECT count(*) FROM big WHERE id <> 3500 AND v <> id`); n != 0 {
		t.Fatalf("%d row belum kembali ke nilai awal", n)
	}
}
