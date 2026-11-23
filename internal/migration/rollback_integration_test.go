package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func (e *executeEnv) draft(t *testing.T, title, up, down string) *Migration {
	t.Helper()
	m, err := e.store.CreateDraft(context.Background(), e.user, e.dbID, Draft{Title: title, SQLUp: up, SQLDown: down})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *executeEnv) run(t *testing.T, m *Migration, kind Kind) *Run {
	t.Helper()
	r, err := e.store.ExecuteWith(context.Background(), e.user, e.dbID, m.ID, ExecOptions{Kind: kind})
	if err != nil {
		t.Fatalf("%s %s: %v", kind, m.Title, err)
	}
	return r
}

func (e *executeEnv) columnExists(t *testing.T, table, column string) bool {
	t.Helper()
	var n int
	if err := e.target.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestRollbackAndForwardFollowLinearOrder(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	a := env.draft(t, "a", "CREATE TABLE a (id INT)", "DROP TABLE a")
	b := env.draft(t, "b", "ALTER TABLE a ADD COLUMN b INT", "ALTER TABLE a DROP COLUMN b")
	c := env.draft(t, "c", "CREATE TABLE c (id INT)", "DROP TABLE c")
	env.run(t, a, KindUp)
	env.run(t, b, KindUp)
	upC := env.run(t, c, KindUp)

	// A → B → C: rollback C dulu sebelum B.
	if _, err := env.store.ExecuteWith(ctx, env.user, env.dbID, b.ID, ExecOptions{Kind: KindRollback}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("rollback B sebelum C harus ditolak, dapat %v", err)
	}
	rbC := env.run(t, c, KindRollback)
	if rbC.Status != StatusSuccess || rbC.Kind != KindRollback || rbC.RevertsRunID == nil || *rbC.RevertsRunID != upC.ID {
		t.Fatalf("rollback C harus merujuk eksekusi up C: %+v", rbC)
	}
	if rbC.SQL != "DROP TABLE c" {
		t.Fatalf("rollback harus menjalankan SQL down asli, dapat %q", rbC.SQL)
	}
	if env.tableExists(t, "c") || env.status(t, c.ID).Status != StatusRolledBack {
		t.Fatal("rollback C harus menghapus tabel c dan status menjadi rolled_back")
	}
	env.run(t, b, KindRollback)
	if env.columnExists(t, "a", "b") {
		t.Fatal("rollback B harus menghapus kolom b")
	}

	// Forward mengikuti urutan linear dan tidak menjalankan migration lain.
	if _, err := env.store.ExecuteWith(ctx, env.user, env.dbID, c.ID, ExecOptions{Kind: KindForward}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("forward C sebelum B harus ditolak, dapat %v", err)
	}
	if _, err := env.store.ExecuteWith(ctx, env.user, env.dbID, c.ID, ExecOptions{Kind: KindUp}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("up untuk migration yang di-rollback harus ditolak (pakai forward), dapat %v", err)
	}
	fwB := env.run(t, b, KindForward)
	if fwB.Kind != KindForward || !env.columnExists(t, "a", "b") || env.status(t, b.ID).Status != StatusSuccess {
		t.Fatalf("forward B harus menerapkan SQL up lagi: %+v", fwB)
	}
	if env.status(t, c.ID).Status != StatusRolledBack || env.tableExists(t, "c") {
		t.Fatal("forward B tidak boleh ikut menjalankan C")
	}
	env.run(t, c, KindForward)
	if !env.tableExists(t, "c") {
		t.Fatal("forward C harus membuat tabel c lagi")
	}

	// Rollback berikutnya merujuk eksekusi forward terbaru, bukan up lama.
	rbC2 := env.run(t, c, KindRollback)
	runs, _ := env.store.Runs(ctx, env.dbID, c.ID)
	if rbC2.RevertsRunID == nil || *rbC2.RevertsRunID != runs[1].ID || runs[1].Kind != KindForward {
		t.Fatalf("rollback kedua harus merujuk forward terakhir: %+v, runs %+v", rbC2, runs)
	}
}

// Contoh dari spesifikasi: A[E1] B[E2] B(rollback E2)[E3] B(forward)[E4] C[E5].
// Hide rollback menyisakan A[E1] B[E4] C[E5].
func TestHistoryHideRollbackSpecExample(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	a := env.draft(t, "A", "CREATE TABLE ha (id INT)", "DROP TABLE ha")
	b := env.draft(t, "B", "CREATE TABLE hb (id INT)", "DROP TABLE hb")
	c := env.draft(t, "C", "CREATE TABLE hc (id INT)", "DROP TABLE hc")
	e1 := env.run(t, a, KindUp)
	e2 := env.run(t, b, KindUp)
	e3 := env.run(t, b, KindRollback)
	e4 := env.run(t, b, KindForward)
	e5 := env.run(t, c, KindUp)

	full, err := env.store.History(ctx, env.dbID, false)
	if err != nil {
		t.Fatal(err)
	}
	if ids := executionIDs(full); !equalIDs(ids, []int64{e1.ID, e2.ID, e3.ID, e4.ID, e5.ID}) {
		t.Fatalf("riwayat lengkap = %v", ids)
	}
	if full[2].Kind != KindRollback || full[2].RevertsExecutionID == nil || *full[2].RevertsExecutionID != e2.ID {
		t.Fatalf("E3 harus rollback yang merujuk E2: %+v", full[2])
	}
	if full[0].SQL != "CREATE TABLE ha (id INT)" || full[0].ExecutedBy != "eksekutor" || full[0].Revision != 1 {
		t.Fatalf("riwayat harus menyimpan salinan SQL, pengguna, dan revisi: %+v", full[0])
	}
	hidden, err := env.store.History(ctx, env.dbID, true)
	if err != nil {
		t.Fatal(err)
	}
	if ids := executionIDs(hidden); !equalIDs(ids, []int64{e1.ID, e4.ID, e5.ID}) {
		t.Fatalf("hide rollback = %v, seharusnya E1 E4 E5", ids)
	}
}

func TestRollbackFailureKeepsActivePosition(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	a := env.draft(t, "a", "CREATE TABLE fa (id INT)", "DROP TABLE tidak_ada")
	up := env.run(t, a, KindUp)
	rb := env.run(t, a, KindRollback)
	if rb.Status != StatusFailed || !strings.Contains(rb.ErrorMessage, "tidak_ada") {
		t.Fatalf("rollback dengan SQL down yang gagal harus failed: %+v", rb)
	}
	if env.status(t, a.ID).Status != StatusSuccess || !env.tableExists(t, "fa") {
		t.Fatal("rollback gagal tidak boleh mengubah posisi aktif")
	}

	full, _ := env.store.History(ctx, env.dbID, false)
	if len(full) != 2 || full[0].ExecutionID != up.ID || full[1].ExecutionID != rb.ID || full[1].Status != StatusFailed {
		t.Fatalf("kegagalan tetap terlihat di riwayat: %+v", full)
	}
	var committed int
	if err := env.store.DB.QueryRow(`SELECT count(*) FROM migration_history WHERE execution_id = $1`, rb.ID).Scan(&committed); err != nil || committed != 0 {
		t.Fatalf("attempt gagal tidak boleh masuk migration_history: %d err=%v", committed, err)
	}
	hidden, _ := env.store.History(ctx, env.dbID, true)
	if len(hidden) != 2 {
		t.Fatalf("hide rollback tidak menyembunyikan kegagalan: %+v", hidden)
	}
}

func TestRollbackIrreversibleIsRejected(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	m, err := env.store.CreateDraft(ctx, env.user, env.dbID, Draft{
		Title: "hapus", SQLUp: "CREATE TABLE ir (id INT)", Irreversible: true, IrreversibleReason: "data lama dihapus permanen",
	})
	if err != nil {
		t.Fatal(err)
	}
	env.run(t, m, KindUp)
	_, err = env.store.ExecuteWith(ctx, env.user, env.dbID, m.ID, ExecOptions{Kind: KindRollback})
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "data lama dihapus permanen") {
		t.Fatalf("rollback migration irreversible harus ditolak dengan alasannya, dapat %v", err)
	}
	if !env.tableExists(t, "ir") {
		t.Fatal("tidak boleh ada perubahan")
	}
}

// Rollback ganda ditolak lewat bukti di target walaupun platform tidak tahu
// (misalnya versi sudah dibatalkan manual di luar platform).
func TestRollbackRequiresVersionActiveInTarget(t *testing.T) {
	env := newExecuteEnv(t)
	a := env.draft(t, "a", "CREATE TABLE ra (id INT)", "DROP TABLE ra")
	env.run(t, a, KindUp)
	if _, err := env.target.Exec(`DELETE FROM _dbplatform_history`); err != nil {
		t.Fatal(err)
	}
	rb := env.run(t, a, KindRollback)
	if rb.Status != StatusFailed || !strings.Contains(rb.ErrorMessage, "tidak tercatat aktif") {
		t.Fatalf("rollback tanpa bukti versi aktif harus failed: %+v", rb)
	}
	if !env.tableExists(t, "ra") || env.status(t, a.ID).Status != StatusSuccess {
		t.Fatal("SQL down tidak boleh dijalankan")
	}
}

func TestMigrationHistoryIsImmutable(t *testing.T) {
	env := newExecuteEnv(t)
	a := env.draft(t, "a", "CREATE TABLE im (id INT)", "DROP TABLE im")
	up := env.run(t, a, KindUp)
	for _, q := range []string{
		`UPDATE migration_history SET sql_text = 'x' WHERE execution_id = $1`,
		`DELETE FROM migration_history WHERE execution_id = $1`,
	} {
		if _, err := env.store.DB.Exec(q, up.ID); err == nil || !strings.Contains(err.Error(), "tidak boleh diubah") {
			t.Fatalf("%s harus ditolak, dapat %v", q, err)
		}
	}
	if _, err := env.store.DB.Exec(`TRUNCATE migration_history`); err == nil {
		t.Fatal("TRUNCATE migration_history harus ditolak")
	}
}

func TestDraftIsImmutableAfterExecution(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	m := env.draft(t, "a", "CREATE TABLE dr (id INT)", "DROP TABLE dr")
	if m.Revision != 1 || m.SQLDown != "DROP TABLE dr" || m.Irreversible {
		t.Fatalf("draft baru: %+v", m)
	}
	err := env.store.UpdateDraft(ctx, env.user, env.dbID, m.ID, Draft{Title: "a2", SQLUp: "CREATE TABLE dr (id INT, x INT)", SQLDown: "DROP TABLE dr"})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.status(t, m.ID); got.Revision != 2 || got.Title != "a2" {
		t.Fatalf("edit harus menaikkan revisi: %+v", got)
	}
	run := env.run(t, m, KindUp)
	if run.Revision != 2 || run.SQL != "CREATE TABLE dr (id INT, x INT)" {
		t.Fatalf("attempt harus mencatat revisi dan salinan SQL: %+v", run)
	}
	err = env.store.UpdateDraft(ctx, env.user, env.dbID, m.ID, Draft{Title: "a3", SQLUp: "SELECT 1", SQLDown: "SELECT 1"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SQL migration yang sudah dieksekusi tidak boleh diubah, dapat %v", err)
	}
	if err := env.store.Discard(ctx, env.user, env.dbID, m.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("migration aktif tidak boleh di-discard, dapat %v", err)
	}
	env.run(t, m, KindRollback)
	if err := env.store.Discard(ctx, env.user, env.dbID, m.ID); err != nil {
		t.Fatalf("migration yang sudah di-rollback boleh di-discard: %v", err)
	}
	if h, _ := env.store.History(ctx, env.dbID, false); len(h) != 2 {
		t.Fatalf("discard tidak menghapus riwayat: %+v", h)
	}
}

func TestLegacyCreateIsIrreversible(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "lama", "CREATE TABLE lama (id INT)")
	if !m.Irreversible || m.IrreversibleReason == "" || m.SQLDown != "" {
		t.Fatalf("migration dari halaman lama harus irreversible dengan alasan: %+v", m)
	}
}

// Pemulihan mengerti rollback: buktinya kebalikan dari up.
func TestRecoverInterruptedRollback(t *testing.T) {
	ctx := context.Background()
	for _, committed := range []bool{false, true} {
		env := newExecuteEnv(t)
		a := env.draft(t, "a", "CREATE TABLE rc (id INT)", "DROP TABLE rc")
		up := env.run(t, a, KindUp)
		// Server mati di tengah rollback.
		if _, err := env.store.DB.Exec(`UPDATE migrations SET status='running' WHERE id=$1`, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := env.store.DB.Exec(`INSERT INTO migration_runs (migration_id, kind, reverts_run_id, sql_text, executed_by, status)
			VALUES ($1, 'rollback', $2, 'DROP TABLE rc', $3, 'running')`, a.ID, up.ID, env.user.ID); err != nil {
			t.Fatal(err)
		}
		if committed {
			if _, err := env.target.Exec(`DELETE FROM _dbplatform_history WHERE version = 1; DROP TABLE rc`); err != nil {
				t.Fatal(err)
			}
		}
		if err := env.store.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		want := StatusSuccess // rollback tidak committed: posisi aktif tetap
		if committed {
			want = StatusRolledBack
		}
		if got := env.status(t, a.ID).Status; got != want {
			t.Fatalf("committed=%v: status %s, seharusnya %s", committed, got, want)
		}
		h, _ := env.store.History(ctx, env.dbID, false)
		if committed && (len(h) != 2 || h[1].Kind != KindRollback || h[1].Status != StatusSuccess) {
			t.Fatalf("rollback yang committed harus masuk riwayat: %+v", h)
		}
	}
}

func executionIDs(entries []HistoryEntry) []int64 {
	out := make([]int64, len(entries))
	for i, e := range entries {
		out[i] = e.ExecutionID
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
