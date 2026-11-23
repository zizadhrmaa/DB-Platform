package migration

import (
	"context"
	"strings"
	"testing"
	"time"
)

func (e *executeEnv) lastRunID(t *testing.T, migID int64) int64 {
	t.Helper()
	var id int64
	if err := e.store.DB.QueryRow(`SELECT max(id) FROM migration_runs WHERE migration_id = $1`, migID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestVerifyRun(t *testing.T) {
	ctx := context.Background()

	t.Run("sukses", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		run, err := env.store.ExecuteWith(ctx, env.user, env.dbID, m.ID, ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if v, _, err := env.store.VerifyRun(ctx, env.dbID, m.ID, run.ID); err != nil || v != VerdictCommitted {
			t.Fatalf("vonis = %v err=%v, seharusnya committed", v, err)
		}
	})

	t.Run("sql gagal", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "INSERT INTO tidak_ada VALUES (1)")
		run, err := env.store.ExecuteWith(ctx, env.user, env.dbID, m.ID, ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		v, msg, err := env.store.VerifyRun(ctx, env.dbID, m.ID, run.ID)
		if err != nil || v != VerdictFailed || !strings.Contains(msg, "tidak_ada") {
			t.Fatalf("vonis = %v msg=%q err=%v, seharusnya failed dengan pesan SQL", v, msg, err)
		}
	})

	t.Run("worker mati sebelum commit", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		markRunning(t, env, m.ID)
		v, _, err := env.store.VerifyRun(ctx, env.dbID, m.ID, env.lastRunID(t, m.ID))
		if err != nil || v != VerdictInterrupted {
			t.Fatalf("vonis = %v err=%v, seharusnya interrupted", v, err)
		}
		if got := env.status(t, m.ID).Status; got != StatusFailed {
			t.Fatalf("migration harus dibereskan menjadi failed, dapat %s", got)
		}
	})

	t.Run("worker mati setelah commit", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		markRunning(t, env, m.ID)
		// Bukti commit ada di target walaupun platform belum sempat mencatat.
		if _, err := env.target.Exec(`
			CREATE TABLE _dbplatform_history(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
			INSERT INTO _dbplatform_history(version) VALUES (1);
			CREATE TABLE t (id INT);`); err != nil {
			t.Fatal(err)
		}
		v, _, err := env.store.VerifyRun(ctx, env.dbID, m.ID, env.lastRunID(t, m.ID))
		if err != nil || v != VerdictCommitted {
			t.Fatalf("vonis = %v err=%v, seharusnya committed", v, err)
		}
		if got := env.status(t, m.ID).Status; got != StatusSuccess {
			t.Fatalf("migration harus success, dapat %s", got)
		}
	})

	t.Run("lock target masih dipegang", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "t", "CREATE TABLE t (id INT)")
		markRunning(t, env, m.ID)
		holder, err := env.target.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Rollback()
		if _, err := holder.Exec(`SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, 3110, env.dbID); err != nil {
			t.Fatal(err)
		}
		v, _, err := env.store.VerifyRun(ctx, env.dbID, m.ID, env.lastRunID(t, m.ID))
		if err != nil || v != VerdictUnknown {
			t.Fatalf("vonis = %v err=%v, seharusnya unknown", v, err)
		}
		if got := env.status(t, m.ID).Status; got != StatusRunning {
			t.Fatalf("migration tidak boleh disentuh selama lock dipegang, dapat %s", got)
		}
	})

	t.Run("koneksi diputus di tengah SQL", func(t *testing.T) {
		env := newExecuteEnv(t)
		m := env.create(t, "diputus", "CREATE TABLE diputus2 (id INT); SELECT pg_sleep(5)")
		go func() {
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				_ = env.target.QueryRow(`
					SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
					WHERE query LIKE '%CREATE TABLE diputus2%' AND pid <> pg_backend_pid()`).Scan(&n)
				if n > 0 {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
		run, err := env.store.ExecuteWith(ctx, env.user, env.dbID, m.ID, ExecOptions{})
		if run == nil || err == nil {
			t.Fatalf("eksekusi harus terputus: %+v err=%v", run, err)
		}
		if v, _, err := env.store.VerifyRun(ctx, env.dbID, m.ID, run.ID); err != nil || v != VerdictInterrupted {
			t.Fatalf("attempt yang terputus harus dikenali sebagai interrupted, dapat %v err=%v", v, err)
		}
	})
}
