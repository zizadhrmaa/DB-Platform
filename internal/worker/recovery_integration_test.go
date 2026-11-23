package worker_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

// claimAsDead meniru worker "mati" yang sudah mengambil job lalu berhenti:
// job running milik owner lain dengan lease yang sudah lewat.
func (e *env) claimAsDead(t *testing.T, j *queue.Job) *queue.Job {
	t.Helper()
	c, err := e.queue.Claim(context.Background(), "mati", time.Minute)
	if err != nil || c == nil || c.ID != j.ID {
		t.Fatalf("claim oleh worker mati: %+v err=%v", c, err)
	}
	return c
}

func (e *env) expireLease(t *testing.T, j *queue.Job) {
	t.Helper()
	if _, err := e.platform.Exec(`UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
}

// startAttemptAsDead mencatat attempt (migration running, migration_runs,
// jobs.run_id) seperti start() milik worker mati, tanpa menjalankan SQL.
func (e *env) startAttemptAsDead(t *testing.T, j *queue.Job) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := e.platform.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE migrations SET status='running', executed_by=$2, executed_at=now() WHERE id=$1`, j.MigrationID, e.user.ID); err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := tx.QueryRow(`INSERT INTO migration_runs (migration_id, sql_text, executed_by, status)
		SELECT id, sql_text, $2, 'running' FROM migrations WHERE id=$1 RETURNING id`, j.MigrationID, e.user.ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := e.queue.SetRun(ctx, tx, j.ID, "mati", runID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return runID
}

func recoverOnce(t *testing.T, w *worker.Worker) {
	t.Helper()
	if _, err := w.RecoverOnce(context.Background()); err != nil {
		t.Fatalf("RecoverOnce: %v", err)
	}
}

func waitUntilDue(j *queue.Job) { time.Sleep(time.Until(j.NextRunAt) + 50*time.Millisecond) }

func TestRecoveryRequeuesJobWithoutAttempt(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j)
	e.expireLease(t, j)

	w := e.worker(worker.Config{})
	recoverOnce(t, w)
	got := e.job(t, j.ID)
	if got.Status != queue.StatusQueued || got.RunID != nil {
		t.Fatalf("job tanpa attempt tercatat harus kembali ke antrean: %+v", got)
	}
	waitUntilDue(got)
	processNext(t, w)
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded || got.Attempt != 2 {
		t.Fatalf("harus berhasil di attempt 2: %+v", got)
	}
}

func TestRecoveryAfterCommitDoesNotReexecute(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j)
	// Worker mati menjalankan dan meng-commit SQL, lalu mati sebelum
	// menulis hasil job.
	_, err := e.migs.ExecuteWith(context.Background(), e.user, j.DBID, j.MigrationID, migration.ExecOptions{
		OnStart: func(ctx context.Context, tx *sql.Tx, runID int64) error {
			return e.queue.SetRun(ctx, tx, j.ID, "mati", runID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.expireLease(t, j)

	w := e.worker(worker.Config{})
	recoverOnce(t, w)
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded || got.FinishedAt == nil {
		t.Fatalf("bukti commit ada, job harus succeeded: %+v", got)
	}
	if n := e.runCount(t, j.MigrationID); n != 1 {
		t.Fatalf("migration tidak boleh dijalankan ulang, run = %d", n)
	}
	if processNext(t, w) {
		t.Fatal("tidak boleh ada job yang diambil lagi")
	}
}

func TestRecoveryBeforeCommitRequeuesThenSucceeds(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j)
	e.startAttemptAsDead(t, j)
	e.expireLease(t, j)

	w := e.worker(worker.Config{})
	recoverOnce(t, w)
	got := e.job(t, j.ID)
	if got.Status != queue.StatusQueued {
		t.Fatalf("attempt terputus sebelum commit harus kembali ke antrean: %+v", got)
	}
	runs, _ := e.migs.Runs(context.Background(), j.DBID, j.MigrationID)
	if len(runs) != 1 || runs[0].Status != migration.StatusFailed {
		t.Fatalf("attempt terputus harus tercatat failed: %+v", runs)
	}
	waitUntilDue(got)
	processNext(t, w)
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded || got.Attempt != 2 {
		t.Fatalf("harus berhasil di attempt 2: %+v", got)
	}
	if n := e.runCount(t, j.MigrationID); n != 2 {
		t.Fatalf("harus ada 2 attempt tercatat, dapat %d", n)
	}
}

func TestRecoveryFailedSQLIsNotRetried(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "INSERT INTO tidak_ada VALUES (1)")
	e.claimAsDead(t, j)
	if _, err := e.migs.ExecuteWith(context.Background(), e.user, j.DBID, j.MigrationID, migration.ExecOptions{
		OnStart: func(ctx context.Context, tx *sql.Tx, runID int64) error {
			return e.queue.SetRun(ctx, tx, j.ID, "mati", runID)
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.expireLease(t, j)

	recoverOnce(t, e.worker(worker.Config{}))
	got := e.job(t, j.ID)
	if got.Status != queue.StatusFailed || !strings.Contains(got.ErrorMessage, "tidak_ada") {
		t.Fatalf("SQL yang gagal harus menjadi failed dengan pesannya: %+v", got)
	}
}

// Lease lewat tetapi transaction lama masih memegang lock target: hasilnya
// belum bisa dipastikan. Job menjadi uncertain, migration tidak disentuh,
// dan database ditahan sampai transaction lama selesai.
func TestRecoveryUncertainWhileTargetLockHeld(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j)
	e.startAttemptAsDead(t, j)

	// Transaction lama di target: lock dipegang, SQL dan bukti sudah
	// ditulis, COMMIT belum.
	old, err := e.targets[e.dbA].Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback()
	if _, err := old.Exec(`SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, 3110, e.dbA); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE IF NOT EXISTS _dbplatform_history(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
		INSERT INTO _dbplatform_history(version) VALUES (1);
		CREATE TABLE t (id INT);`); err != nil {
		t.Fatal(err)
	}
	e.expireLease(t, j)

	w := e.worker(worker.Config{})
	recoverOnce(t, w)
	if got := e.job(t, j.ID); got.Status != queue.StatusUncertain {
		t.Fatalf("lock target masih dipegang, job harus uncertain: %+v", got)
	}
	if got := e.migrationStatus(t, j); got != migration.StatusRunning {
		t.Fatalf("migration tidak boleh disentuh, dapat %s", got)
	}

	// Selama uncertain, job lain di database yang sama tidak diambil.
	next := e.submit(t, e.dbA, "CREATE TABLE berikutnya (id INT)")
	if processNext(t, w) {
		t.Fatal("job lain di database yang sama tidak boleh diambil selama ada job uncertain")
	}
	recoverOnce(t, w) // masih dipegang: tetap uncertain
	if got := e.job(t, j.ID); got.Status != queue.StatusUncertain {
		t.Fatalf("selama lock masih dipegang job harus tetap uncertain: %+v", got)
	}

	if err := old.Commit(); err != nil {
		t.Fatal(err)
	}
	recoverOnce(t, w)
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded {
		t.Fatalf("setelah transaction lama commit, job harus succeeded: %+v", got)
	}
	if got := e.migrationStatus(t, j); got != migration.StatusSuccess {
		t.Fatalf("migration harus success, dapat %s", got)
	}
	if n := e.runCount(t, j.MigrationID); n != 1 {
		t.Fatalf("tidak boleh ada eksekusi ulang, run = %d", n)
	}

	if !processNext(t, w) {
		t.Fatal("setelah hasil pasti, job berikutnya harus bisa diambil")
	}
	if got := e.job(t, next.ID); got.Status != queue.StatusSucceeded {
		t.Fatalf("job berikutnya harus succeeded: %+v", got)
	}
}

func TestRecoveryLeavesLiveLeaseAlone(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j) // lease masih 1 menit
	n, err := e.worker(worker.Config{}).RecoverOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("job dengan lease aktif tidak boleh dipulihkan: n=%d err=%v", n, err)
	}
	if got := e.job(t, j.ID); got.Status != queue.StatusRunning || got.LeaseOwner != "mati" {
		t.Fatalf("job harus tetap milik pemegang lease: %+v", got)
	}
}

// Run menjalankan loop pemulihan sendiri: job milik worker mati diambil
// alih dan diselesaikan tanpa campur tangan.
func TestRunRecoversDeadWorkerJobs(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	e.claimAsDead(t, j)
	e.expireLease(t, j)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := e.worker(worker.Config{RecoveryInterval: 50 * time.Millisecond})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for e.job(t, j.ID).Status != queue.StatusSucceeded {
		if time.Now().After(deadline) {
			t.Fatalf("job milik worker mati tidak pernah selesai: %+v", e.job(t, j.ID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}
