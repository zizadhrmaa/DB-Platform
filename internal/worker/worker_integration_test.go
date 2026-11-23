package worker_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
	"github.com/zizadhrmaa/DB-Platform/internal/worker"
)

type env struct {
	platform *sql.DB
	targets  map[int64]*sql.DB
	az       *authz.Static
	migs     *migration.Store
	queue    *queue.Store
	user     *domain.User
	dbA, dbB int64
	keys     int
}

func setup(t *testing.T) *env {
	t.Helper()
	platform := testutil.PlatformDB(t)
	targetA, _ := testutil.TargetDB(t)
	targetB, _ := testutil.TargetDB(t)
	user := testutil.MustUser(t, platform, "eksekutor", false)
	dbA := testutil.MustDBConn(t, platform, "toko")
	dbB := testutil.MustDBConn(t, platform, "kampus")
	az := &authz.Static{Levels: map[authz.Key]domain.AccessLevel{
		{UserID: user.ID, DBID: dbA}: domain.AccessEdit,
		{UserID: user.ID, DBID: dbB}: domain.AccessEdit,
	}}
	targets := map[int64]*sql.DB{dbA: targetA, dbB: targetB}
	return &env{
		platform: platform,
		targets:  targets,
		az:       az,
		migs:     &migration.Store{DB: platform, Authz: az, Targets: dbmanager.NewStatic(targets)},
		queue:    &queue.Store{DB: platform, Policy: queue.RetryPolicy{MaxAttempts: 5, BaseDelay: 200 * time.Millisecond, MaxDelay: time.Second}},
		user:     user,
		dbA:      dbA,
		dbB:      dbB,
	}
}

func (e *env) worker(cfg worker.Config) *worker.Worker {
	if cfg.ID == "" {
		cfg.ID = "w-test"
	}
	if cfg.Lease == 0 {
		cfg.Lease = 5 * time.Second
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 20 * time.Millisecond
	}
	return &worker.Worker{Queue: e.queue, Users: e.platform, Exec: e.migs, Config: cfg}
}

// submit membuat migration lalu memasukkannya ke antrean, seperti endpoint
// enqueue setelah approval lengkap.
func (e *env) submit(t *testing.T, dbID int64, sqlText string) *queue.Job {
	t.Helper()
	m, err := e.migs.Create(context.Background(), e.user, dbID, "m", sqlText)
	if err != nil {
		t.Fatal(err)
	}
	e.keys++
	j, created, err := e.queue.Enqueue(context.Background(), queue.EnqueueParams{
		DBID: dbID, MigrationID: m.ID, RequestedBy: e.user.ID, IdempotencyKey: "k" + string(rune('a'+e.keys)),
	})
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	return j
}

func (e *env) job(t *testing.T, id int64) *queue.Job {
	t.Helper()
	j, err := e.queue.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e *env) migrationStatus(t *testing.T, j *queue.Job) migration.Status {
	t.Helper()
	m, err := e.migs.Get(context.Background(), j.DBID, j.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	return m.Status
}

func (e *env) tableExists(t *testing.T, dbID int64, name string) bool {
	t.Helper()
	var ok bool
	if err := e.targets[dbID].QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func (e *env) runCount(t *testing.T, migID int64) int {
	t.Helper()
	var n int
	if err := e.platform.QueryRow(`SELECT count(*) FROM migration_runs WHERE migration_id = $1`, migID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func processNext(t *testing.T, w *worker.Worker) bool {
	t.Helper()
	ok, err := w.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("ProcessNext: %v", err)
	}
	return ok
}

func TestWorkerRunsQueuedJob(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE kota (id INT)")
	if j.Status != queue.StatusQueued || e.tableExists(t, e.dbA, "kota") {
		t.Fatal("submit hanya memasukkan job ke antrean tanpa menjalankan SQL")
	}

	if !processNext(t, e.worker(worker.Config{})) {
		t.Fatal("worker harus mengambil job")
	}
	got := e.job(t, j.ID)
	if got.Status != queue.StatusSucceeded || got.Attempt != 1 || got.FinishedAt == nil || got.RunID == nil {
		t.Fatalf("job harus succeeded dengan run_id: %+v", got)
	}
	runs, _ := e.migs.Runs(context.Background(), j.DBID, j.MigrationID)
	if len(runs) != 1 || runs[0].ID != *got.RunID {
		t.Fatalf("run_id job harus menunjuk attempt yang dijalankan: %+v vs %d", runs, *got.RunID)
	}
	if e.migrationStatus(t, j) != migration.StatusSuccess || !e.tableExists(t, e.dbA, "kota") {
		t.Fatal("migration harus success dan ter-commit")
	}
	if processNext(t, e.worker(worker.Config{})) {
		t.Fatal("tidak boleh ada job lain")
	}
}

func TestWorkerSQLFailureIsNotRetried(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE sebagian (id INT); INSERT INTO tidak_ada VALUES (1)")
	w := e.worker(worker.Config{})
	processNext(t, w)

	got := e.job(t, j.ID)
	if got.Status != queue.StatusFailed || got.Attempt != 1 || !strings.Contains(got.ErrorMessage, "tidak_ada") {
		t.Fatalf("SQL gagal harus failed di attempt 1 dengan pesan error: %+v", got)
	}
	if e.tableExists(t, e.dbA, "sebagian") {
		t.Fatal("transaction harus rollback")
	}
	if processNext(t, w) {
		t.Fatal("SQL yang gagal tidak boleh di-retry")
	}
}

func TestWorkerCommitRejectedIsPlainFailure(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, `
		CREATE TABLE induk (id INT PRIMARY KEY);
		CREATE TABLE anak (induk_id INT REFERENCES induk(id) DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO anak VALUES (1);`)
	processNext(t, e.worker(worker.Config{}))
	if got := e.job(t, j.ID); got.Status != queue.StatusFailed {
		t.Fatalf("COMMIT ditolak server harus failed biasa, bukan %s", got.Status)
	}
}

func TestWorkerRetriesWhenTargetBusy(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
	holder, err := e.targets[e.dbA].Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Lock advisory berlaku untuk seluruh database Postgres test, jadi harus
	// dilepas walaupun test gagal di tengah supaya tidak bocor ke test lain.
	defer holder.Rollback()
	if _, err := holder.Exec(`SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, 3110, e.dbA); err != nil {
		t.Fatal(err)
	}

	w := e.worker(worker.Config{})
	processNext(t, w)
	got := e.job(t, j.ID)
	if got.Status != queue.StatusQueued || got.Attempt != 1 || !got.NextRunAt.After(time.Now()) {
		t.Fatalf("target sibuk harus kembali ke antrean dengan jeda: %+v", got)
	}
	if processNext(t, w) {
		t.Fatal("job tidak boleh diambil sebelum jeda lewat")
	}

	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(got.NextRunAt) + 50*time.Millisecond)
	if !processNext(t, w) {
		t.Fatal("setelah jeda lewat job harus diambil lagi")
	}
	got = e.job(t, j.ID)
	if got.Status != queue.StatusSucceeded || got.Attempt != 2 {
		t.Fatalf("harus succeeded di attempt 2: %+v", got)
	}
	if n := e.runCount(t, j.MigrationID); n != 1 {
		t.Fatalf("attempt yang gagal karena target sibuk tidak menjalankan SQL, run = %d", n)
	}
}

func TestWorkerRevalidatesBeforeExecuting(t *testing.T) {
	t.Run("akses diturunkan ke view", func(t *testing.T) {
		e := setup(t)
		j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
		e.az.Levels[authz.Key{UserID: e.user.ID, DBID: e.dbA}] = domain.AccessView
		processNext(t, e.worker(worker.Config{}))
		if got := e.job(t, j.ID); got.Status != queue.StatusFailed || got.ErrorMessage == "" {
			t.Fatalf("job harus failed dengan alasan: %+v", got)
		}
		if e.tableExists(t, e.dbA, "t") || e.runCount(t, j.MigrationID) != 0 {
			t.Fatal("SQL tidak boleh dijalankan")
		}
	})

	t.Run("user dinonaktifkan", func(t *testing.T) {
		e := setup(t)
		j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
		if _, err := e.platform.Exec(`UPDATE users SET is_active = false WHERE id = $1`, e.user.ID); err != nil {
			t.Fatal(err)
		}
		processNext(t, e.worker(worker.Config{}))
		if got := e.job(t, j.ID); got.Status != queue.StatusFailed {
			t.Fatalf("job harus failed: %+v", got)
		}
		if e.tableExists(t, e.dbA, "t") {
			t.Fatal("SQL tidak boleh dijalankan")
		}
	})

	t.Run("hook approval menolak", func(t *testing.T) {
		e := setup(t)
		j := e.submit(t, e.dbA, "CREATE TABLE t (id INT)")
		w := e.worker(worker.Config{})
		var gotJob int64
		var gotUser int64
		w.Revalidate = func(_ context.Context, job *queue.Job, u *domain.User) error {
			gotJob, gotUser = job.ID, u.ID
			return errors.New("approval dicabut")
		}
		processNext(t, w)
		got := e.job(t, j.ID)
		if got.Status != queue.StatusFailed || !strings.Contains(got.ErrorMessage, "approval dicabut") {
			t.Fatalf("job harus failed dengan alasan dari hook: %+v", got)
		}
		if gotJob != j.ID || gotUser != e.user.ID {
			t.Fatalf("hook harus menerima job dan user pengaju: job=%d user=%d", gotJob, gotUser)
		}
		if e.tableExists(t, e.dbA, "t") {
			t.Fatal("SQL tidak boleh dijalankan")
		}
	})
}

func TestWorkerRunsDatabasesInParallel(t *testing.T) {
	e := setup(t)
	a := e.submit(t, e.dbA, "SELECT pg_sleep(1)")
	b := e.submit(t, e.dbB, "SELECT pg_sleep(1)")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := e.worker(worker.Config{Concurrency: 2})
	start := time.Now()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	for {
		ja, jb := e.job(t, a.ID), e.job(t, b.ID)
		if ja.Status == queue.StatusSucceeded && jb.Status == queue.StatusSucceeded {
			break
		}
		if ja.Status.Terminal() && jb.Status.Terminal() {
			t.Fatalf("kedua job harus succeeded: %+v %+v", ja, jb)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("job tidak selesai")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("dua database berbeda harus diproses paralel (< 2s), selesai dalam %v", d)
	}
	cancel()
	<-done
}

// Shutdown berhenti mengambil job baru tetapi menunggu job yang sedang
// berjalan selesai; eksekusinya tidak ikut dibatalkan.
func TestWorkerShutdownWaitsForRunningJob(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "CREATE TABLE lambat (id INT); SELECT pg_sleep(1)")
	next := e.submit(t, e.dbB, "CREATE TABLE berikutnya (id INT)")
	if _, err := e.platform.Exec(`UPDATE jobs SET next_run_at = now() + interval '1 hour' WHERE id = $1`, next.ID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := e.worker(worker.Config{Concurrency: 1})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for e.job(t, j.ID).Status != queue.StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("job tidak pernah running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run tidak berhenti")
	}
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded {
		t.Fatalf("job yang sedang berjalan harus selesai sebelum Run kembali: %+v", got)
	}
	if !e.tableExists(t, e.dbA, "lambat") {
		t.Fatal("eksekusi tidak boleh dibatalkan saat shutdown")
	}
	if got := e.job(t, next.ID); got.Status != queue.StatusQueued {
		t.Fatalf("setelah shutdown tidak boleh mengambil job baru: %+v", got)
	}
}

// Heartbeat memperpanjang lease selama eksekusi lebih lama dari lease.
func TestWorkerHeartbeatKeepsLease(t *testing.T) {
	e := setup(t)
	j := e.submit(t, e.dbA, "SELECT pg_sleep(1.5)")
	w := e.worker(worker.Config{Lease: 600 * time.Millisecond})

	var (
		mu        sync.Mutex
		recovered []queue.Job
	)
	stop := make(chan struct{})
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			rec, _ := e.queue.Recoverable(context.Background(), 10)
			mu.Lock()
			recovered = append(recovered, rec...)
			mu.Unlock()
		}
	}()
	processNext(t, w)
	close(stop)
	<-watcher

	if len(recovered) != 0 {
		t.Fatalf("lease tidak boleh lewat selama worker hidup: %+v", recovered)
	}
	if got := e.job(t, j.ID); got.Status != queue.StatusSucceeded {
		t.Fatalf("job harus succeeded: %+v", got)
	}
}
