package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func (e *executeEnv) runCount(t *testing.T, id int64) int {
	t.Helper()
	var n int
	if err := e.store.DB.QueryRow(`SELECT count(*) FROM migration_runs WHERE migration_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExecuteWithOnStartRunsInsideStartTransaction(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "t", "CREATE TABLE t (id INT)")

	var seen int64
	run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{
		OnStart: func(ctx context.Context, tx *sql.Tx, runID int64) error {
			// Baris migration_runs harus terlihat dari transaction yang sama.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_runs WHERE id = $1 AND status = 'running'`, runID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return errors.New("migration_runs belum ada di transaction OnStart")
			}
			seen = runID
			return nil
		},
	})
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi: %+v err=%v", run, err)
	}
	if seen == 0 || seen != run.ID {
		t.Fatalf("OnStart harus menerima run ID yang sama: %d vs %d", seen, run.ID)
	}
}

func TestExecuteWithOnStartErrorStopsBeforeSQL(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "t", "CREATE TABLE t (id INT)")
	stop := errors.New("lease hilang")

	_, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{
		OnStart: func(context.Context, *sql.Tx, int64) error { return stop },
	})
	if !errors.Is(err, stop) {
		t.Fatalf("error OnStart harus diteruskan, dapat %v", err)
	}
	if env.tableExists(t, "t") {
		t.Fatal("SQL tidak boleh dijalankan kalau OnStart gagal")
	}
	if got := env.status(t, m.ID); got.Status != StatusPending {
		t.Fatalf("migration harus tetap pending, dapat %s", got.Status)
	}
	if n := env.runCount(t, m.ID); n != 0 {
		t.Fatalf("attempt tidak boleh tercatat, dapat %d run", n)
	}
}

func TestExecuteWithTargetBusy(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "t", "CREATE TABLE t (id INT)")

	holder, err := env.target.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, 3110, env.dbID); err != nil {
		t.Fatal(err)
	}

	_, err = env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
	if !errors.Is(err, ErrTargetBusy) || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("lock target dipegang harus ErrTargetBusy (dan ErrConflict), dapat %v", err)
	}
	if n := env.runCount(t, m.ID); n != 0 || env.status(t, m.ID).Status != StatusPending {
		t.Fatalf("target sibuk tidak boleh mencatat attempt: runs=%d", n)
	}
}

// COMMIT yang dijawab error oleh server adalah kegagalan biasa, bukan hasil
// yang belum pasti.
func TestExecuteWithCommitRejectedByServer(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "fk tertunda", `
		CREATE TABLE induk (id INT PRIMARY KEY);
		CREATE TABLE anak (induk_id INT REFERENCES induk(id) DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO anak VALUES (99);`)

	run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
	if err != nil {
		t.Fatalf("COMMIT ditolak server harus menjadi run failed tanpa error, dapat %v", err)
	}
	if run.Status != StatusFailed || !strings.Contains(run.ErrorMessage, "foreign key") {
		t.Fatalf("run harus failed dengan pesan FK: %+v", run)
	}
	if env.tableExists(t, "induk") {
		t.Fatal("transaction harus rollback")
	}
}

func TestExecuteWithTimeoutIsPermanentFailure(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "lambat", "SELECT pg_sleep(3)")

	start := time.Now()
	run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout eksekusi harus run failed tanpa error (tidak di-retry), dapat %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("run harus failed: %+v", run)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("Timeout tidak dipatuhi, eksekusi %v", d)
	}
}

// Koneksi target diputus server di tengah SQL: transaction pasti batal, jadi
// boleh dicoba lagi.
func TestExecuteWithConnectionKilledIsInterrupted(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "diputus", "CREATE TABLE diputus (id INT); SELECT pg_sleep(5)")

	go func() {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			_ = env.target.QueryRow(`
				SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
				WHERE query LIKE '%CREATE TABLE diputus%' AND pid <> pg_backend_pid()`).Scan(&n)
			if n > 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
	if !errors.Is(err, ErrInterrupted) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("koneksi diputus harus ErrInterrupted, dapat %v (run %+v)", err, run)
	}
	if run == nil || run.Status != StatusFailed {
		t.Fatalf("attempt harus tercatat failed: %+v", run)
	}
	if env.tableExists(t, "diputus") {
		t.Fatal("transaction yang terputus tidak boleh ter-commit")
	}
	if got := env.status(t, m.ID); got.Status != StatusFailed {
		t.Fatalf("migration harus failed (boleh dijalankan ulang), dapat %s", got.Status)
	}
}

// COMMIT tanpa jawaban: bukti di target diperiksa sebelum memutuskan.
func TestExecuteWithCommitWithoutAnswer(t *testing.T) {
	lost := errors.New("unexpected EOF")

	t.Run("sudah committed", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		env.store.commit = func(tx *sql.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			return lost
		}
		run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
		if err != nil || run.Status != StatusSuccess {
			t.Fatalf("bukti commit ada, harus success: %+v err=%v", run, err)
		}
		if env.status(t, m.ID).Status != StatusSuccess || env.runCount(t, m.ID) != 1 {
			t.Fatal("migration harus success dengan satu attempt")
		}
	})

	t.Run("tidak committed", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		env.store.commit = func(tx *sql.Tx) error {
			if err := tx.Rollback(); err != nil {
				return err
			}
			return lost
		}
		run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("bukti tidak ada, harus ErrInterrupted (boleh retry), dapat %v", err)
		}
		if run == nil || run.Status != StatusFailed || env.tableExists(t, "t") {
			t.Fatalf("attempt harus failed tanpa perubahan: %+v", run)
		}
	})

	t.Run("belum bisa diperiksa", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		// Transaction lama dibiarkan terbuka sehingga lock target masih
		// dipegang: bukti belum bisa diperiksa.
		env.store.commit = func(*sql.Tx) error { return lost }
		_, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{})
		if !errors.Is(err, ErrUncertain) {
			t.Fatalf("bukti belum bisa diperiksa harus ErrUncertain, dapat %v", err)
		}
		// Migration tetap running supaya versi berikutnya tertahan.
		if got := env.status(t, m.ID).Status; got != StatusRunning {
			t.Fatalf("migration harus tetap running selama hasil belum pasti, dapat %s", got)
		}
		// Setelah transaction lama selesai (di sini: rollback), pemulihan
		// bisa memastikan hasilnya.
		env.store.commit = nil
		if err := env.store.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := env.status(t, m.ID).Status; got != StatusFailed {
			t.Fatalf("setelah diperiksa, migration yang tidak committed harus failed, dapat %s", got)
		}
	})
}
