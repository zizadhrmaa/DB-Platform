package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// seedBig membuat tabel big berisi n row dengan v = id.
func (e *executeEnv) seedBig(t *testing.T, n int) {
	t.Helper()
	if _, err := e.target.Exec(`CREATE TABLE big (id BIGINT PRIMARY KEY, v BIGINT NOT NULL CHECK (v >= 0 AND v < 1000000))`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.target.Exec(`INSERT INTO big SELECT g, g FROM generate_series(1, $1::int) g`, n); err != nil {
		t.Fatal(err)
	}
}

func (e *executeEnv) batchDraft(t *testing.T, up, down string, size int) *Migration {
	t.Helper()
	m, err := e.store.CreateDraft(context.Background(), e.user, e.dbID, Draft{
		Title: "batch", SQLUp: up, SQLDown: down,
		ExecMode: ExecBatch, BatchTable: "big", BatchColumn: "id", BatchSize: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *executeEnv) batch(t *testing.T, m *Migration, job int64, action BatchAction) *BatchResult {
	t.Helper()
	r, err := e.store.ExecuteBatch(context.Background(), e.user, e.dbID, m.ID, BatchOptions{JobID: job, Action: action})
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return r
}

func (e *executeEnv) count(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.target.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	upAdd   = "UPDATE big SET v = v + id WHERE id > $1 AND id <= $2"
	downSub = "UPDATE big SET v = v - id WHERE id > $1 AND id <= $2"
)

func TestBatchProcessesAllRowsInSeparateTransactions(t *testing.T) {
	env := newExecuteEnv(t)
	env.seedBig(t, 10000)
	m := env.batchDraft(t, upAdd, downSub, 1000)

	var reports []BatchProgress
	r, err := env.store.ExecuteBatch(context.Background(), env.user, env.dbID, m.ID, BatchOptions{
		JobID: 7, Action: BatchRun,
		OnBatch: func(_ context.Context, p BatchProgress) error { reports = append(reports, p); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != BatchSucceeded || r.Progress.Batches != 10 || r.Progress.Rows != 10000 {
		t.Fatalf("hasil batch: %+v", r)
	}
	// Setiap batch punya checkpoint sendiri dengan rentang 1000 row.
	if n := env.count(t, `SELECT count(*) FROM _dbplatform_batches WHERE job_id = 7 AND rows = 1000 AND upper_bound - lower_bound = 1000`); n != 10 {
		t.Fatalf("checkpoint per batch = %d, seharusnya 10", n)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE v <> 2 * id`); n != 0 {
		t.Fatalf("%d row tidak diproses tepat sekali", n)
	}
	if len(reports) != 11 || reports[10].Batches != 10 {
		t.Fatalf("progres dilaporkan setelah tiap batch: %+v", reports)
	}
	if got := env.status(t, m.ID); got.Status != StatusSuccess {
		t.Fatalf("migration harus success, dapat %s", got.Status)
	}
	if h, _ := env.store.History(context.Background(), env.dbID, false); len(h) != 1 || h[0].Status != StatusSuccess {
		t.Fatalf("migration batch yang selesai masuk riwayat: %+v", h)
	}
}

func TestBatchFailureIsPartialAndResumeContinuesFromCheckpoint(t *testing.T) {
	env := newExecuteEnv(t)
	env.seedBig(t, 10000)
	m := env.batchDraft(t, upAdd, downSub, 1000)
	// Row di batch 4 membuat CHECK gagal.
	if _, err := env.target.Exec(`UPDATE big SET v = 999999 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}

	r := env.batch(t, m, 8, BatchRun)
	if r.Status != BatchPartial || r.Progress.Batches != 3 || r.Progress.Rows != 3000 || !strings.Contains(r.Message, "Batch 4") {
		t.Fatalf("batch 4 gagal harus partial dengan 3 batch committed: %+v", r)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE id BETWEEN 3001 AND 4000 AND id <> 3500 AND v <> id`); n != 0 {
		t.Fatalf("row batch 4 tidak boleh berubah, %d berubah", n)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE id <= 3000 AND v <> 2 * id`); n != 0 {
		t.Fatalf("batch 1-3 harus tetap committed, %d tidak", n)
	}

	// Data diperbaiki, lalu resume: mulai dari batch 4, tidak mengulang 1-3.
	if _, err := env.target.Exec(`UPDATE big SET v = 3500 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}
	r = env.batch(t, m, 8, BatchRun)
	if r.Status != BatchSucceeded || r.Progress.Batches != 10 || r.Progress.Rows != 10000 {
		t.Fatalf("resume: %+v", r)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE v <> 2 * id`); n != 0 {
		t.Fatalf("%d row diproses dua kali atau terlewat", n)
	}
	if n := env.count(t, `SELECT min(batch_no) FROM _dbplatform_batches WHERE job_id = 8 AND committed_at > (SELECT max(committed_at) FROM _dbplatform_batches WHERE job_id = 8 AND batch_no = 3)`); n != 4 {
		t.Fatalf("resume harus mulai dari batch 4, dapat %d", n)
	}
	runs, _ := env.store.Runs(context.Background(), env.dbID, m.ID)
	if len(runs) != 2 || runs[1].Status != StatusFailed || runs[0].Status != StatusSuccess {
		t.Fatalf("setiap attempt tercatat sebagai eksekusi: %+v", runs)
	}
}

func TestBatchRevertInReverseOrderAndContinuesAfterPartialFailure(t *testing.T) {
	env := newExecuteEnv(t)
	env.seedBig(t, 5000)
	m := env.batchDraft(t, upAdd, downSub, 1000)
	if _, err := env.target.Exec(`UPDATE big SET v = 999999 WHERE id = 3500`); err != nil {
		t.Fatal(err)
	}
	if r := env.batch(t, m, 9, BatchRun); r.Status != BatchPartial || r.Progress.Batches != 3 {
		t.Fatalf("persiapan partial: %+v", r)
	}
	// Pembatalan batch 2 akan gagal (v - id < 0).
	if _, err := env.target.Exec(`UPDATE big SET v = 10 WHERE id = 1500`); err != nil {
		t.Fatal(err)
	}
	r := env.batch(t, m, 9, BatchRevert)
	if r.Status != BatchPartiallyReverted || r.Progress.Batches != 2 || !strings.Contains(r.Message, "batch 2") {
		t.Fatalf("revert gagal di batch 2 harus partially_reverted dengan 2 batch tersisa: %+v", r)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE id BETWEEN 2001 AND 3000 AND v <> id`); n != 0 {
		t.Fatalf("batch 3 harus sudah dibatalkan lebih dulu, %d row belum", n)
	}

	if _, err := env.target.Exec(`UPDATE big SET v = 3000 WHERE id = 1500`); err != nil {
		t.Fatal(err)
	}
	r = env.batch(t, m, 9, BatchRevert)
	if r.Status != BatchReverted || r.Progress.Batches != 0 {
		t.Fatalf("revert lanjutan: %+v", r)
	}
	if n := env.count(t, `SELECT count(*) FROM big WHERE id <> 3500 AND v <> id`); n != 0 {
		t.Fatalf("semua batch harus kembali ke nilai awal, %d belum", n)
	}
	// Urutan pembatalan: 3, lalu 2, lalu 1.
	rows, err := env.target.Query(`SELECT batch_no FROM _dbplatform_batches WHERE job_id = 9 ORDER BY reverted_at`)
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	for rows.Next() {
		var no int
		rows.Scan(&no)
		order = append(order, no)
	}
	rows.Close()
	if len(order) != 3 || order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Fatalf("urutan pembatalan harus terbalik, dapat %v", order)
	}
}

// Lock target tetap dipegang di antara commit batch: eksekusi lain di
// database yang sama ditolak walaupun batch sebelumnya sudah commit.
func TestBatchHoldsLockBetweenCommits(t *testing.T) {
	env := newExecuteEnv(t)
	env.seedBig(t, 3000)
	m := env.batchDraft(t, upAdd, downSub, 1000)
	other := env.draft(t, "lain", "CREATE TABLE lain (id INT)", "DROP TABLE lain")

	var checked bool
	_, err := env.store.ExecuteBatch(context.Background(), env.user, env.dbID, m.ID, BatchOptions{
		JobID: 10, Action: BatchRun,
		OnBatch: func(ctx context.Context, p BatchProgress) error {
			if p.Batches != 1 {
				return nil
			}
			checked = true
			if _, err := env.store.ExecuteWith(ctx, env.user, env.dbID, other.ID, ExecOptions{}); !errors.Is(err, ErrTargetBusy) {
				t.Errorf("eksekusi biasa di antara batch harus ErrTargetBusy, dapat %v", err)
			}
			if _, err := env.store.ExecuteBatch(ctx, env.user, env.dbID, m.ID, BatchOptions{JobID: 11, Action: BatchRun}); !errors.Is(err, ErrTargetBusy) {
				t.Errorf("job batch kedua di antara batch harus ErrTargetBusy, dapat %v", err)
			}
			return nil
		},
	})
	if err != nil || !checked {
		t.Fatalf("eksekusi batch: err=%v checked=%v", err, checked)
	}
	if env.tableExists(t, "lain") {
		t.Fatal("migration lain tidak boleh jalan")
	}
	// Setelah job selesai, lock dilepas.
	if _, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, other.ID, ExecOptions{}); err != nil {
		t.Fatalf("setelah job batch selesai lock harus lepas: %v", err)
	}
}

func TestBatchDraftValidation(t *testing.T) {
	bad := []Draft{
		{Title: "x", SQLUp: "UPDATE big SET v = 1", SQLDown: downSub, ExecMode: ExecBatch, BatchTable: "big", BatchColumn: "id", BatchSize: 10},
		{Title: "x", SQLUp: upAdd + "; SELECT 1", SQLDown: downSub, ExecMode: ExecBatch, BatchTable: "big", BatchColumn: "id", BatchSize: 10},
		{Title: "x", SQLUp: upAdd, SQLDown: downSub, ExecMode: ExecBatch, BatchTable: "big; DROP", BatchColumn: "id", BatchSize: 10},
		{Title: "x", SQLUp: upAdd, SQLDown: downSub, ExecMode: ExecBatch, BatchTable: "big", BatchColumn: "id", BatchSize: 0},
		{Title: "x", SQLUp: upAdd, SQLDown: downSub, ExecMode: "aneh"},
	}
	for _, d := range bad {
		if err := d.validate(); err == nil {
			t.Errorf("seharusnya ditolak: %+v", d)
		}
	}
	ok := Draft{Title: "x", SQLUp: upAdd, SQLDown: downSub, ExecMode: ExecBatch, BatchTable: "public.big", BatchColumn: "id", BatchSize: 500}
	if err := ok.validate(); err != nil {
		t.Fatalf("draft batch valid ditolak: %v", err)
	}
}
