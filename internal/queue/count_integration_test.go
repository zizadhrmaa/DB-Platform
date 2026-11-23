package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// CountByStatus mengembalikan jumlah job saat ini untuk setiap status,
// termasuk 0 untuk status tanpa job (dipakai gauge dbp_jobs).
func TestCountByStatus(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db1 := testutil.MustDBConn(t, f.db, "satu")
	db2 := testutil.MustDBConn(t, f.db, "dua")
	f.enqueue(t, db1, mustMigration(t, f.db, db1), "a")
	f.enqueue(t, db2, mustMigration(t, f.db, db2), "b")
	if c, err := f.store.Claim(ctx, "w1", time.Minute); err != nil || c == nil {
		t.Fatalf("claim: %+v err=%v", c, err)
	}

	got, err := f.store.CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(queue.AllStatuses) {
		t.Fatalf("semua status harus ada, dapat %v", got)
	}
	if got[queue.StatusQueued] != 1 || got[queue.StatusRunning] != 1 || got[queue.StatusFailed] != 0 {
		t.Fatalf("hitungan salah: %v", got)
	}
}
