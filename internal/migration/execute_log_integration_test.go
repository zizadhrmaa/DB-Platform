package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

// captureLogs memasang handler JSON platform ke buffer selama test.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	prev := slog.Default()
	slog.SetDefault(slog.New(obs.NewJSONHandler(&lockedWriter{mu: &mu, w: &buf}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("log bukan JSON: %q", line)
			}
			out = append(out, m)
		}
		return out
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func findEvent(logs []map[string]any, event string, match func(map[string]any) bool) map[string]any {
	for _, m := range logs {
		if m["event"] == event && (match == nil || match(m)) {
			return m
		}
	}
	return nil
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// Spesifikasi: setiap attempt dicatat dengan migration ID, execution ID,
// versi, pengguna, status, dan salinan SQL; setiap eksekusi SQL dicatat
// dengan query, durasi, SQLSTATE, dan ID terkait; mulai, hasil, dan
// commit/rollback dicatat terpisah.
func TestExecuteWithLogsAttemptAndSQL(t *testing.T) {
	logs := captureLogs(t)
	env := newExecuteEnv(t)
	ok := env.create(t, "kota", "CREATE TABLE kota (id INT)")
	ctx := obs.WithJobContext(context.Background(), obs.JobContext{JobID: 77, Attempt: 2})
	run, err := env.store.ExecuteWith(ctx, env.user, env.dbID, ok.ID, ExecOptions{})
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi: %+v err=%v", run, err)
	}
	all := logs()

	start := findEvent(all, "migration.attempt_start", nil)
	if start == nil {
		t.Fatalf("log mulai attempt tidak ada: %v", all)
	}
	if num(start["migration_id"]) != ok.ID || num(start["execution_id"]) != run.ID || num(start["version"]) != 1 ||
		start["user"] != "eksekutor" || start["sql"] != "CREATE TABLE kota (id INT)" || num(start["job_id"]) != 77 || num(start["attempt"]) != 2 {
		t.Fatalf("atribut mulai attempt tidak lengkap: %v", start)
	}

	userSQL := findEvent(all, "sql_result", func(m map[string]any) bool { return m["query"] == "CREATE TABLE kota (id INT)" })
	if userSQL == nil || userSQL["status"] != "success" || num(userSQL["execution_id"]) != run.ID || num(userSQL["job_id"]) != 77 {
		t.Fatalf("hasil SQL migration harus dicatat dengan execution_id dan job_id: %v", userSQL)
	}
	if _, ok := userSQL["dur_ms"]; !ok {
		t.Fatalf("durasi SQL tidak dicatat: %v", userSQL)
	}
	if findEvent(all, "sql_start", func(m map[string]any) bool { return m["query"] == "CREATE TABLE kota (id INT)" }) == nil {
		t.Fatal("mulai eksekusi SQL harus dicatat terpisah")
	}
	if c := findEvent(all, "sql_commit", nil); c == nil || c["status"] != "success" || num(c["execution_id"]) != run.ID {
		t.Fatalf("commit harus dicatat dengan execution_id: %v", c)
	}
	if lock := findEvent(all, "migration.lock", nil); lock == nil || lock["status"] != "acquired" {
		t.Fatalf("pengambilan lock target harus dicatat: %v", lock)
	}
	if res := findEvent(all, "migration.attempt_result", nil); res == nil || res["status"] != "success" || num(res["execution_id"]) != run.ID {
		t.Fatalf("hasil attempt harus dicatat: %v", res)
	}
}

func TestExecuteWithLogsFailedSQLAndRollback(t *testing.T) {
	logs := captureLogs(t)
	env := newExecuteEnv(t)
	bad := env.create(t, "gagal", "INSERT INTO tidak_ada VALUES (1)")
	run, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, bad.ID, ExecOptions{})
	if err != nil || run.Status != StatusFailed {
		t.Fatalf("eksekusi: %+v err=%v", run, err)
	}
	all := logs()

	res := findEvent(all, "sql_result", func(m map[string]any) bool { return m["query"] == "INSERT INTO tidak_ada VALUES (1)" })
	if res == nil || res["status"] != "failed" || res["sqlstate"] != "42P01" || num(res["execution_id"]) != run.ID {
		t.Fatalf("SQL gagal harus dicatat dengan SQLSTATE: %v", res)
	}
	if rb := findEvent(all, "sql_rollback", nil); rb == nil || num(rb["execution_id"]) != run.ID {
		t.Fatalf("rollback harus dicatat terpisah dengan execution_id: %v", rb)
	}
	if findEvent(all, "sql_commit", nil) != nil {
		t.Fatal("tidak boleh ada commit untuk SQL yang gagal")
	}
	r := findEvent(all, "migration.attempt_result", nil)
	if r == nil || r["status"] != "failed" || !strings.Contains(r["error"].(string), "tidak_ada") {
		t.Fatalf("hasil attempt gagal harus dicatat dengan pesan error: %v", r)
	}
}

func TestExecuteWithLogsBusyLock(t *testing.T) {
	logs := captureLogs(t)
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
	if _, err := env.store.ExecuteWith(context.Background(), env.user, env.dbID, m.ID, ExecOptions{}); err == nil {
		t.Fatal("harus ErrTargetBusy")
	}
	if lock := findEvent(logs(), "migration.lock", nil); lock == nil || lock["status"] != "busy" {
		t.Fatalf("lock target yang sibuk harus dicatat: %v", lock)
	}
}
