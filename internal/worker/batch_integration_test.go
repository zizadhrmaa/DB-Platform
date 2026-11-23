package worker_test

import (
	"context"
	"database/sql"
	"runtime"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

func (e *env) seedBig(t *testing.T, dbID int64, n int) {
	t.Helper()
	db := e.targets[dbID]
	if _, err := db.Exec(`CREATE TABLE big (id BIGINT PRIMARY KEY, v BIGINT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO big SELECT g, g FROM generate_series(1, $1::int) g`, n); err != nil {
		t.Fatal(err)
	}
}

func (e *env) batchJob(t *testing.T, dbID int64) (*migration.Migration, *queue.Job) {
	t.Helper()
	m, err := e.migs.CreateDraft(context.Background(), e.user, dbID, migration.Draft{
		Title: "batch", ExecMode: migration.ExecBatch, BatchTable: "big", BatchColumn: "id", BatchSize: 1000,
		SQLUp:   "UPDATE big SET v = v + id WHERE id > $1 AND id <= $2",
		SQLDown: "UPDATE big SET v = v - id WHERE id > $1 AND id <= $2",
	})
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := e.queue.Enqueue(context.Background(), queue.EnqueueParams{
		DBID: dbID, MigrationID: m.ID, RequestedBy: e.user.ID, Mode: queue.ModeBatch, IdempotencyKey: "batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, j
}

func TestWorkerRunsBatchJobWithProgress(t *testing.T) {
	e := setup(t)
	e.seedBig(t, e.dbA, 5000)
	_, j := e.batchJob(t, e.dbA)
	processNext(t, e.worker(worker.Config{}))
	got := e.job(t, j.ID)
	if got.Status != queue.StatusSucceeded || got.BatchesCommitted != 5 || got.RowsCommitted != 5000 || got.LastCursor != "5000" {
		t.Fatalf("job batch: %+v", got)
	}
}

// Worker mati di tengah batch: lock target lepas, pemulihan membaca bukti,
// job diantrekan lagi dan dilanjutkan dari checkpoint tanpa mengulang batch
// yang sudah committed.
func TestWorkerDiesMidBatchAndJobResumesFromCheckpoint(t *testing.T) {
	e := setup(t)
	e.seedBig(t, e.dbA, 5000)
	m, j := e.batchJob(t, e.dbA)
	e.claimAsDead(t, j)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.migs.ExecuteBatch(context.Background(), e.user, e.dbA, m.ID, migration.BatchOptions{
			JobID: j.ID, Action: migration.BatchRun,
			OnStart: func(ctx context.Context, tx *sql.Tx, runID int64) error {
				return e.queue.SetRun(ctx, tx, j.ID, "mati", runID)
			},
			OnBatch: func(_ context.Context, p migration.BatchProgress) error {
				if p.Batches == 2 {
					runtime.Goexit() // proses mati: hasil tidak pernah ditulis
				}
				return nil
			},
		})
	}()
	<-done
	if got := e.migrationStatus(t, j); got != migration.StatusRunning {
		t.Fatalf("simulasi mati harus meninggalkan migration running, dapat %s", got)
	}
	e.expireLease(t, j)

	w := e.worker(worker.Config{})
	recoverOnce(t, w)
	got := e.job(t, j.ID)
	if got.Status != queue.StatusQueued {
		t.Fatalf("job batch dari worker mati harus diantrekan lagi: %+v", got)
	}
	waitUntilDue(got)
	processNext(t, w)
	got = e.job(t, j.ID)
	if got.Status != queue.StatusSucceeded || got.BatchesCommitted != 5 || got.RowsCommitted != 5000 {
		t.Fatalf("job harus selesai dari checkpoint: %+v", got)
	}
	var wrong int
	if err := e.targets[e.dbA].QueryRow(`SELECT count(*) FROM big WHERE v <> 2 * id`).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("%d row diproses dua kali atau terlewat (err=%v)", wrong, err)
	}
	var batches int
	if err := e.targets[e.dbA].QueryRow(`SELECT count(*) FROM _dbplatform_batches WHERE job_id = $1`, j.ID).Scan(&batches); err != nil || batches != 5 {
		t.Fatalf("checkpoint = %d, seharusnya 5 (batch 1-2 tidak diulang)", batches)
	}
}
