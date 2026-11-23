package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/approval"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

type jobJSON struct {
	ID               int64      `json:"id"`
	DBID             int64      `json:"db_id"`
	MigrationID      int64      `json:"migration_id"`
	RequestID        *int64     `json:"request_id"`
	Kind             string     `json:"kind"`
	Mode             string     `json:"mode"`
	Status           string     `json:"status"`
	Attempt          int        `json:"attempt"`
	MaxAttempts      int        `json:"max_attempts"`
	BatchesCommitted int        `json:"batches_committed"`
	RowsCommitted    int64      `json:"rows_committed"`
	ErrorMessage     string     `json:"error_message"`
	CreatedAt        time.Time  `json:"created_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

// testWorker membangun worker dengan dependensi yang sama seperti main.go.
func testWorker(t *testing.T, e *migrationEnv) *worker.Worker {
	t.Helper()
	dbID, err := strconv.ParseInt(strings.Split(e.base, "/")[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	az := &authz.Service{DB: e.db}
	migs := &migration.Store{DB: e.db, Targets: dbmanager.NewStatic(map[int64]*sql.DB{dbID: e.target}), Authz: az}
	qs := &queue.Store{DB: e.db, Policy: queue.DefaultRetryPolicy()}
	approvals := &approval.Store{DB: e.db, Queue: qs, Guard: approval.DefaultGuard(migs), Authz: az}
	return newWorker(workerDeps{DB: e.db, Queue: qs, Migrations: migs, Approvals: approvals},
		worker.Config{ID: "w-test", Lease: 5 * time.Second, PollInterval: 20 * time.Millisecond})
}

// approvedJob membuat migration, request, approval lengkap, lalu enqueue.
func approvedJob(t *testing.T, e *migrationEnv, bob, carol *browser, title string) (int64, string) {
	t.Helper()
	setLeaders(t, e)
	_, base := submitRequest(t, bob, e, title)
	for _, b := range []*browser{bob, carol} {
		if rr := b.api(http.MethodPost, base+"/approve", nil); rr.Code != http.StatusOK {
			t.Fatalf("approve: status %d (%s)", rr.Code, rr.Body.String())
		}
	}
	rr := bob.api(http.MethodPost, base+"/enqueue", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue: status %d (%s)", rr.Code, rr.Body.String())
	}
	return decodeBody[struct {
		JobID int64 `json:"job_id"`
	}](t, rr).JobID, base
}

func (e *migrationEnv) tableExists(t *testing.T, name string) bool {
	t.Helper()
	var ok bool
	if err := e.target.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// Alur minimum spesifikasi: approval → queue → worker → lock → revalidasi →
// eksekusi → commit, lalu status dipantau lewat API.
func TestAPIJobEndToEnd(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	otherID := strings.Split(e.other, "/")[2]

	jobID, reqBase := approvedJob(t, e, bob, carol, "kota")
	jobPath := fmt.Sprintf("/api/databases/%s/jobs/%d", dbID, jobID)

	rr := bob.api(http.MethodGet, jobPath, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail job: status %d (%s)", rr.Code, rr.Body.String())
	}
	if j := decodeBody[jobJSON](t, rr); j.Status != "queued" || j.Attempt != 0 || j.RequestID == nil || *j.RequestID != requestIDOf(reqBase) {
		t.Fatalf("job baru: %+v", j)
	}
	for _, internal := range []string{"lease_owner", "idempotency_key", "trace_context", "lease_until"} {
		if strings.Contains(rr.Body.String(), internal) {
			t.Errorf("field internal %q tidak boleh dikirim ke client", internal)
		}
	}
	if e.tableExists(t, "kota") {
		t.Fatal("enqueue tidak boleh menjalankan SQL")
	}

	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker: ok=%v err=%v", ok, err)
	}

	j := decodeBody[jobJSON](t, carol.api(http.MethodGet, jobPath, nil))
	if j.Status != "succeeded" || j.Attempt != 1 || j.FinishedAt == nil {
		t.Fatalf("job setelah worker (dilihat View Only): %+v", j)
	}
	if !e.tableExists(t, "kota") {
		t.Fatal("migration harus ter-commit")
	}
	if got := decodeBody[struct {
		JobStatus string `json:"job_status"`
	}](t, bob.api(http.MethodGet, reqBase, nil)); got.JobStatus != "succeeded" {
		t.Fatalf("status job di request: %q", got.JobStatus)
	}

	list := decodeBody[[]jobJSON](t, bob.api(http.MethodGet, "/api/databases/"+dbID+"/jobs?status=succeeded", nil))
	if len(list) != 1 || list[0].ID != jobID {
		t.Fatalf("daftar job succeeded: %+v", list)
	}
	if list := decodeBody[[]jobJSON](t, bob.api(http.MethodGet, "/api/databases/"+dbID+"/jobs?status=queued", nil)); len(list) != 0 {
		t.Fatalf("filter queued harus kosong: %+v", list)
	}

	if rr := bob.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/jobs/%d", otherID, jobID), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("job lewat database lain: status %d, want 404", rr.Code)
	}
	if rr := dave.api(http.MethodGet, jobPath, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("tanpa akses: status %d, want 404", rr.Code)
	}
}

// Approval berubah setelah job masuk antrean (group baru mendapat akses dan
// belum menyetujui): worker menolak saat revalidasi dan SQL tidak dijalankan.
func TestAPIJobRevalidationAfterApprovalChange(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]

	jobID, _ := approvedJob(t, e, bob, carol, "auditku")
	dbIDInt, _ := strconv.ParseInt(dbID, 10, 64)
	auditors := testutil.MustGroup(t, e.db, "auditors")
	testutil.MustGrant(t, e.db, auditors, dbIDInt, "view")

	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker: ok=%v err=%v", ok, err)
	}
	j := decodeBody[jobJSON](t, bob.api(http.MethodGet, fmt.Sprintf("/api/databases/%s/jobs/%d", dbID, jobID), nil))
	if j.Status != "failed" || j.ErrorMessage == "" {
		t.Fatalf("job harus failed karena approval tidak lagi lengkap: %+v", j)
	}
	if e.tableExists(t, "auditku") {
		t.Fatal("SQL tidak boleh dijalankan")
	}
}

// Job yang tidak berasal dari request (misalnya dimasukkan langsung) tidak
// boleh dijalankan karena tidak melewati approval.
func TestWorkerRejectsJobWithoutRequest(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	loc := createMigration(t, bob, e.base, "liar", "CREATE TABLE liar (id INT)")
	migID, _ := strconv.ParseInt(loc[strings.LastIndex(loc, "/")+1:], 10, 64)
	dbID, _ := strconv.ParseInt(strings.Split(e.base, "/")[2], 10, 64)
	bobID := idOf(t, e.db, `SELECT id FROM users WHERE username = 'bob'`)
	qs := &queue.Store{DB: e.db}
	j, _, err := qs.Enqueue(context.Background(), queue.EnqueueParams{DBID: dbID, MigrationID: migID, IdempotencyKey: "langsung", RequestedBy: bobID})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := testWorker(t, e).ProcessNext(context.Background()); err != nil || !ok {
		t.Fatalf("worker: ok=%v err=%v", ok, err)
	}
	got, _ := qs.Get(context.Background(), j.ID)
	if got.Status != queue.StatusFailed || e.tableExists(t, "liar") {
		t.Fatalf("job tanpa request harus ditolak tanpa menjalankan SQL: %+v", got)
	}
}
