package worker_test

import (
	"context"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

// Rollback dan forward juga dijalankan worker sebagai job asynchronous.
func TestWorkerRunsRollbackAndForwardJobs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	m, err := e.migs.CreateDraft(ctx, e.user, e.dbA, migration.Draft{Title: "t", SQLUp: "CREATE TABLE t (id INT)", SQLDown: "DROP TABLE t"})
	if err != nil {
		t.Fatal(err)
	}
	w := e.worker(worker.Config{})
	for i, kind := range []queue.Kind{queue.KindUp, queue.KindRollback, queue.KindForward} {
		j, _, err := e.queue.Enqueue(ctx, queue.EnqueueParams{
			DBID: e.dbA, MigrationID: m.ID, RequestedBy: e.user.ID, Kind: kind, IdempotencyKey: string(kind) + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatal(err)
		}
		processNext(t, w)
		if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded {
			t.Fatalf("job %s harus succeeded: %+v", kind, got)
		}
		wantTable := kind != queue.KindRollback
		if e.tableExists(t, e.dbA, "t") != wantTable {
			t.Fatalf("setelah %s, tabel t ada = %v", kind, !wantTable)
		}
	}
	runs, _ := e.migs.Runs(ctx, e.dbA, m.ID)
	if len(runs) != 3 || runs[0].Kind != migration.KindForward || runs[1].Kind != migration.KindRollback {
		t.Fatalf("setiap job tercatat sebagai eksekusi baru dengan jenisnya: %+v", runs)
	}
}
