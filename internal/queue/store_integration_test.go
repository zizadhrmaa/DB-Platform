package queue_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

type fixture struct {
	db    *sql.DB
	store *queue.Store
	user  *domain.User
}

func setup(t *testing.T) fixture {
	t.Helper()
	db := testutil.PlatformDB(t)
	return fixture{
		db:    db,
		store: &queue.Store{DB: db, Policy: queue.DefaultRetryPolicy()},
		user:  testutil.MustUser(t, db, "alice", false),
	}
}

// mustMigration membuat migration pending dengan versi berikutnya.
func mustMigration(t *testing.T, db *sql.DB, dbID int64) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`
		INSERT INTO migrations (db_id, version, title, sql_text)
		SELECT $1, COALESCE(MAX(version),0)+1, 'm', 'SELECT 1' FROM migrations WHERE db_id = $1
		RETURNING id`, dbID).Scan(&id)
	if err != nil {
		t.Fatalf("mustMigration: %v", err)
	}
	return id
}

func (f fixture) enqueue(t *testing.T, dbID, migID int64, key string) *queue.Job {
	t.Helper()
	j, created, err := f.store.Enqueue(context.Background(), queue.EnqueueParams{
		DBID: dbID, MigrationID: migID, IdempotencyKey: key, RequestedBy: f.user.ID,
	})
	if err != nil || !created {
		t.Fatalf("enqueue %s: created=%v err=%v", key, created, err)
	}
	return j
}

func (f fixture) claim(t *testing.T, owner string) *queue.Job {
	t.Helper()
	j, err := f.store.Claim(context.Background(), owner, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return j
}

func (f fixture) status(t *testing.T, id int64) queue.Status {
	t.Helper()
	j, err := f.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return j.Status
}

func TestEnqueueIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	m1, m2 := mustMigration(t, f.db, dbID), mustMigration(t, f.db, dbID)

	j1 := f.enqueue(t, dbID, m1, "klik-1")
	if j1.Status != queue.StatusQueued || j1.Attempt != 0 || j1.Kind != queue.KindUp || j1.Mode != queue.ModeNormal {
		t.Fatalf("job baru tidak sesuai: %+v", j1)
	}
	if j1.MaxAttempts != queue.DefaultRetryPolicy().MaxAttempts {
		t.Errorf("max_attempts = %d, seharusnya default policy", j1.MaxAttempts)
	}

	again, created, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m1, IdempotencyKey: "klik-1", RequestedBy: f.user.ID})
	if err != nil || created || again.ID != j1.ID {
		t.Fatalf("kunci sama harus mengembalikan job yang sama: id=%v created=%v err=%v", again, created, err)
	}

	open, created, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m1, IdempotencyKey: "klik-2", RequestedBy: f.user.ID})
	if err != nil || created || open.ID != j1.ID {
		t.Fatalf("migration yang masih punya job terbuka harus mengembalikan job lama: %v created=%v err=%v", open, created, err)
	}

	_, _, err = f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m2, IdempotencyKey: "klik-1", RequestedBy: f.user.ID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("kunci yang dipakai migration lain harus ErrConflict, dapat %v", err)
	}

	other := testutil.MustDBConn(t, f.db, "kampus")
	_, _, err = f.store.Enqueue(ctx, queue.EnqueueParams{DBID: other, MigrationID: m2, IdempotencyKey: "klik-3", RequestedBy: f.user.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("migration dari database lain harus ErrNotFound, dapat %v", err)
	}

	_, _, err = f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m2, IdempotencyKey: "  ", RequestedBy: f.user.ID})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("kunci kosong harus ErrInvalidInput, dapat %v", err)
	}

	// Setelah job lama selesai, migration yang sama boleh diantrekan lagi.
	c := f.claim(t, "w1")
	if err := f.store.Finish(ctx, c.ID, "w1", queue.StatusFailed, "gagal"); err != nil {
		t.Fatal(err)
	}
	fresh, created, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m1, IdempotencyKey: "klik-4", RequestedBy: f.user.ID})
	if err != nil || !created || fresh.ID == j1.ID {
		t.Fatalf("job baru setelah job lama selesai: %v created=%v err=%v", fresh, created, err)
	}
}

func TestClaimConcurrentOnlyOneRunningPerDatabase(t *testing.T) {
	f := setup(t)
	dbID := testutil.MustDBConn(t, f.db, "toko")
	for i := 0; i < 5; i++ {
		f.enqueue(t, dbID, mustMigration(t, f.db, dbID), fmt.Sprintf("k%d", i))
	}

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		mu      sync.Mutex
		claimed []int64
		errs    []error
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			j, err := f.store.Claim(context.Background(), fmt.Sprintf("w%d", i), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else if j != nil {
				claimed = append(claimed, j.ID)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("claim bersamaan tidak boleh error: %v", errs)
	}
	if len(claimed) != 1 {
		t.Fatalf("tepat satu claim yang berhasil, dapat %d: %v", len(claimed), claimed)
	}
	var running int
	f.db.QueryRow(`SELECT count(*) FROM jobs WHERE db_id = $1 AND status = 'running'`, dbID).Scan(&running)
	if running != 1 {
		t.Fatalf("job running di database = %d, seharusnya 1", running)
	}
}

func TestClaimSkipsDatabaseWithActiveJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbA := testutil.MustDBConn(t, f.db, "toko")
	dbB := testutil.MustDBConn(t, f.db, "kampus")
	a1 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a1")
	a2 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a2")
	b1 := f.enqueue(t, dbB, mustMigration(t, f.db, dbB), "b1")

	if got := f.claim(t, "w1"); got == nil || got.ID != a1.ID {
		t.Fatalf("claim pertama harus a1, dapat %+v", got)
	}
	got := f.claim(t, "w2")
	if got == nil || got.ID != b1.ID {
		t.Fatalf("claim kedua harus job DB B, bukan job kedua DB A; dapat %+v", got)
	}
	if got := f.claim(t, "w3"); got != nil {
		t.Fatalf("tidak boleh ada job lain yang bisa di-claim, dapat %+v", got)
	}

	// uncertain tetap menahan database A.
	if err := f.store.Finish(ctx, a1.ID, "w1", queue.StatusUncertain, "hasil belum pasti"); err != nil {
		t.Fatal(err)
	}
	if got := f.claim(t, "w3"); got != nil {
		t.Fatalf("job uncertain harus menahan database, tetapi %d di-claim", got.ID)
	}
	if f.status(t, a2.ID) != queue.StatusQueued {
		t.Fatal("a2 harus tetap queued")
	}
}

func TestIndexRejectsSecondActiveJob(t *testing.T) {
	f := setup(t)
	dbID := testutil.MustDBConn(t, f.db, "toko")
	m1, m2 := mustMigration(t, f.db, dbID), mustMigration(t, f.db, dbID)

	insert := func(migID int64, key, status string) error {
		_, err := f.db.Exec(`
			INSERT INTO jobs (db_id, migration_id, idempotency_key, status, lease_owner, lease_until)
			VALUES ($1, $2, $3, $4,
			        CASE WHEN $4 = 'running' THEN 'manual' END,
			        CASE WHEN $4 = 'running' THEN now() + interval '1 minute' END)`,
			dbID, migID, key, status)
		return err
	}
	isUnique := func(err error) bool {
		var pe *pgconn.PgError
		return errors.As(err, &pe) && pe.Code == "23505"
	}

	if err := insert(m1, "x1", "running"); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"running", "uncertain", "partial", "partially_reverted"} {
		if err := insert(m2, "x-"+st, st); !isUnique(err) {
			t.Errorf("job %s kedua di DB yang sama harus ditolak index unik, dapat %v", st, err)
		}
	}
	if err := insert(m1, "x2", "queued"); !isUnique(err) {
		t.Errorf("job terbuka kedua untuk migration yang sama harus ditolak, dapat %v", err)
	}
	if err := insert(m2, "x3", "queued"); err != nil {
		t.Errorf("job queued untuk migration lain boleh: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs (db_id, migration_id, idempotency_key, status) VALUES ($1,$2,'x4','running')`, dbID, m2); err == nil {
		t.Error("job running tanpa lease harus ditolak constraint")
	}
}

func TestRetryBackoffAndExhaustion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	j := f.enqueue(t, dbID, mustMigration(t, f.db, dbID), "r1")

	delayOf := func(id int64) time.Duration {
		t.Helper()
		var ms float64
		if err := f.db.QueryRow(`SELECT EXTRACT(EPOCH FROM (next_run_at - now())) * 1000 FROM jobs WHERE id = $1`, id).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		return time.Duration(ms) * time.Millisecond
	}

	f.claim(t, "w1")
	got, err := f.store.Retry(ctx, j.ID, "w1", "target sibuk")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != queue.StatusQueued || got.Attempt != 1 || got.ErrorMessage != "target sibuk" || got.LeaseOwner != "" {
		t.Fatalf("setelah retry: %+v", got)
	}
	if d := delayOf(j.ID); d < 1500*time.Millisecond || d > 2500*time.Millisecond {
		t.Fatalf("jeda retry pertama sekitar 2s, dapat %v", d)
	}
	if c := f.claim(t, "w2"); c != nil {
		t.Fatalf("job tidak boleh di-claim sebelum jeda lewat, dapat %d", c.ID)
	}

	// Majukan waktu: attempt kedua, jeda berikutnya sekitar 4s.
	f.db.Exec(`UPDATE jobs SET next_run_at = now() WHERE id = $1`, j.ID)
	if c := f.claim(t, "w2"); c == nil || c.Attempt != 2 {
		t.Fatalf("claim setelah jeda lewat harus attempt 2, dapat %+v", c)
	}
	if _, err := f.store.Retry(ctx, j.ID, "w2", "target sibuk"); err != nil {
		t.Fatal(err)
	}
	if d := delayOf(j.ID); d < 3500*time.Millisecond || d > 4500*time.Millisecond {
		t.Fatalf("jeda retry kedua sekitar 4s, dapat %v", d)
	}

	// Attempt habis.
	db2 := testutil.MustDBConn(t, f.db, "gudang")
	one, _, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: db2, MigrationID: mustMigration(t, f.db, db2), IdempotencyKey: "r2", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if c := f.claim(t, "w3"); c == nil || c.ID != one.ID {
		t.Fatalf("claim job satu-attempt: %+v", c)
	}
	done, err := f.store.Retry(ctx, one.ID, "w3", "target tidak terjangkau")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != queue.StatusFailed || done.FinishedAt == nil || done.ErrorMessage != "target tidak terjangkau" {
		t.Fatalf("attempt habis harus failed dengan finished_at: %+v", done)
	}
}

func TestLeaseOwnershipAndRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	migID := mustMigration(t, f.db, dbID)
	j := f.enqueue(t, dbID, migID, "l1")
	f.claim(t, "w1")

	var runID int64
	if err := f.db.QueryRow(`INSERT INTO migration_runs (migration_id, sql_text, status) VALUES ($1,'SELECT 1','running') RETURNING id`, migID).Scan(&runID); err != nil {
		t.Fatal(err)
	}

	if err := f.store.Heartbeat(ctx, j.ID, "w2", time.Minute); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("heartbeat worker lain: %v", err)
	}
	if err := f.store.SetRun(ctx, f.db, j.ID, "w2", runID); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("set run worker lain: %v", err)
	}
	if err := f.store.Finish(ctx, j.ID, "w2", queue.StatusSucceeded, ""); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("finish worker lain: %v", err)
	}
	if _, err := f.store.Retry(ctx, j.ID, "w2", "x"); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("retry worker lain: %v", err)
	}
	if err := f.store.Finish(ctx, j.ID, "w1", queue.StatusQueued, ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("finish ke status queued harus ditolak, dapat %v", err)
	}

	if err := f.store.Heartbeat(ctx, j.ID, "w1", time.Minute); err != nil {
		t.Fatalf("heartbeat pemilik: %v", err)
	}
	if err := f.store.SetRun(ctx, f.db, j.ID, "w1", runID); err != nil {
		t.Fatalf("set run pemilik: %v", err)
	}

	// Lease belum lewat: pemulihan tidak boleh mengubah apa pun.
	if rec, _ := f.store.Recoverable(ctx, 10); len(rec) != 0 {
		t.Fatalf("job dengan lease aktif tidak boleh masuk daftar pemulihan: %+v", rec)
	}
	ok, err := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w1"}, queue.StatusQueued, "worker mati")
	if err != nil || ok {
		t.Fatalf("resolve sebelum lease lewat: ok=%v err=%v", ok, err)
	}
	if got, _ := f.store.Get(ctx, j.ID); got.Status != queue.StatusRunning || got.LeaseOwner != "w1" || got.RunID == nil || *got.RunID != runID {
		t.Fatalf("job harus tetap running milik w1 dengan run_id: %+v", got)
	}

	// Lease lewat.
	f.db.Exec(`UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, j.ID)
	rec, err := f.store.Recoverable(ctx, 10)
	if err != nil || len(rec) != 1 || rec[0].ID != j.ID {
		t.Fatalf("job dengan lease lewat harus masuk daftar pemulihan: %+v err=%v", rec, err)
	}
	// Ekspektasi pemilik yang salah tidak boleh menang.
	if ok, _ := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w9"}, queue.StatusQueued, ""); ok {
		t.Fatal("resolve dengan pemilik yang salah tidak boleh berhasil")
	}
	ok, err = f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w1"}, queue.StatusUncertain, "hasil belum pasti")
	if err != nil || !ok {
		t.Fatalf("resolve setelah lease lewat: ok=%v err=%v", ok, err)
	}
	// Worker lama yang ternyata hidup tidak bisa menimpa hasil pemulihan.
	if err := f.store.Finish(ctx, j.ID, "w1", queue.StatusSucceeded, ""); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("worker lama tidak boleh menimpa hasil pemulihan: %v", err)
	}
	if f.status(t, j.ID) != queue.StatusUncertain {
		t.Fatal("status harus uncertain")
	}
	// uncertain juga masuk daftar pemulihan, lalu bisa diselesaikan.
	if rec, _ := f.store.Recoverable(ctx, 10); len(rec) != 1 {
		t.Fatalf("job uncertain harus masuk daftar pemulihan: %+v", rec)
	}
	ok, err = f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusUncertain}, queue.StatusSucceeded, "")
	if err != nil || !ok {
		t.Fatalf("resolve uncertain: ok=%v err=%v", ok, err)
	}
	got, _ := f.store.Get(ctx, j.ID)
	if got.Status != queue.StatusSucceeded || got.FinishedAt == nil || got.ErrorMessage != "" {
		t.Fatalf("job harus succeeded dengan finished_at: %+v", got)
	}
}

func TestResolveRequeueRespectsAttempts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	j, _, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: mustMigration(t, f.db, dbID), IdempotencyKey: "q1", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	expire := func() {
		f.db.Exec(`UPDATE jobs SET lease_until = now() - interval '1 second', next_run_at = now() WHERE id = $1`, j.ID)
	}

	f.claim(t, "w1")
	expire()
	if ok, err := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w1"}, queue.StatusQueued, "worker mati"); err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}
	f.db.Exec(`UPDATE jobs SET next_run_at = now() WHERE id = $1`, j.ID)
	c := f.claim(t, "w2")
	if c == nil || c.Attempt != 2 || c.RunID != nil {
		t.Fatalf("claim ulang harus attempt 2 dengan run_id kosong: %+v", c)
	}
	expire()
	if ok, _ := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w2"}, queue.StatusQueued, "worker mati"); !ok {
		t.Fatal("requeue kedua harus berhasil di-CAS")
	}
	got, _ := f.store.Get(ctx, j.ID)
	if got.Status != queue.StatusFailed || got.FinishedAt == nil {
		t.Fatalf("requeue saat attempt habis harus failed: %+v", got)
	}
}

func TestGetAndListAreScopedToDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbA := testutil.MustDBConn(t, f.db, "toko")
	dbB := testutil.MustDBConn(t, f.db, "kampus")
	a1 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a1")
	a2 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a2")
	f.enqueue(t, dbB, mustMigration(t, f.db, dbB), "b1")

	if _, err := f.store.GetInDB(ctx, dbB, a1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("job DB lain harus ErrNotFound, dapat %v", err)
	}
	if _, err := f.store.Get(ctx, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("job tidak ada harus ErrNotFound, dapat %v", err)
	}
	got, err := f.store.GetInDB(ctx, dbA, a1.ID)
	if err != nil || got.ID != a1.ID || got.RequestedBy == nil || *got.RequestedBy != f.user.ID {
		t.Fatalf("GetInDB: %+v err=%v", got, err)
	}

	f.claim(t, "w1") // a1 running

	all, err := f.store.List(ctx, dbA, queue.ListFilter{})
	if err != nil || len(all) != 2 || all[0].ID != a2.ID {
		t.Fatalf("list DB A harus 2 job, terbaru dulu: %+v err=%v", all, err)
	}
	running, err := f.store.List(ctx, dbA, queue.ListFilter{Status: queue.StatusRunning})
	if err != nil || len(running) != 1 || running[0].ID != a1.ID {
		t.Fatalf("filter status running: %+v err=%v", running, err)
	}
	if _, err := f.store.List(ctx, dbA, queue.ListFilter{Status: "aneh"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("status filter tidak dikenal harus ErrInvalidInput, dapat %v", err)
	}
	limited, _ := f.store.List(ctx, dbA, queue.ListFilter{Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("limit 1: %d job", len(limited))
	}
}
