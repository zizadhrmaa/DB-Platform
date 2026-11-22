package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// Pemulihan yang membaca keadaan attempt lama tidak boleh menimpa attempt
// berikutnya, walaupun status dan pemilik lease-nya kebetulan sama (ABA).
func TestResolveRejectsStaleAttempt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	j := f.enqueue(t, dbID, mustMigration(t, f.db, dbID), "aba")

	// Attempt 1 berakhir uncertain. Pemulihan R2 mencatat keadaan ini.
	f.claim(t, "w1")
	if err := f.store.Finish(ctx, j.ID, "w1", queue.StatusUncertain, ""); err != nil {
		t.Fatal(err)
	}
	stale := queue.Expect{Status: queue.StatusUncertain, Attempt: 1}

	// Pemulihan R1 lebih cepat: requeue, lalu attempt 2 juga berakhir uncertain.
	if ok, err := f.store.Resolve(ctx, j.ID, stale, queue.StatusQueued, ""); err != nil || !ok {
		t.Fatalf("requeue R1: ok=%v err=%v", ok, err)
	}
	f.exec(t, `UPDATE jobs SET next_run_at = now() WHERE id = $1`, j.ID)
	if c := f.claim(t, "w1"); c == nil || c.Attempt != 2 {
		t.Fatalf("attempt 2: %+v", c)
	}
	if err := f.store.Finish(ctx, j.ID, "w1", queue.StatusUncertain, ""); err != nil {
		t.Fatal(err)
	}

	// R2 memakai bukti attempt 1: harus ditolak.
	if ok, err := f.store.Resolve(ctx, j.ID, stale, queue.StatusQueued, ""); err != nil || ok {
		t.Fatalf("resolve dengan attempt basi tidak boleh berhasil: ok=%v err=%v", ok, err)
	}
	if got, _ := f.store.Get(ctx, j.ID); got.Status != queue.StatusUncertain || got.Attempt != 2 {
		t.Fatalf("job harus tetap uncertain attempt 2: %+v", got)
	}

	// Varian running dengan pemilik sama.
	if ok, _ := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusUncertain, Attempt: 2}, queue.StatusQueued, ""); !ok {
		t.Fatal("requeue attempt 2 harus berhasil")
	}
	f.exec(t, `UPDATE jobs SET next_run_at = now() WHERE id = $1`, j.ID)
	f.claim(t, "w1") // attempt 3, pemilik sama
	f.exec(t, `UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, j.ID)
	if ok, _ := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w1", Attempt: 2}, queue.StatusQueued, ""); ok {
		t.Fatal("ekspektasi attempt 2 tidak boleh berlaku untuk attempt 3")
	}
	if ok, _ := f.store.Resolve(ctx, j.ID, queue.Expect{Status: queue.StatusRunning, LeaseOwner: "w1", Attempt: 3}, queue.StatusUncertain, ""); !ok {
		t.Fatal("ekspektasi attempt yang benar harus berhasil")
	}
}

// Selama job tertua sebuah database menunggu jeda retry, job berikutnya di
// database yang sama tidak boleh menyalip. Database lain tetap jalan.
func TestClaimKeepsOrderWithinDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbA := testutil.MustDBConn(t, f.db, "toko")
	dbB := testutil.MustDBConn(t, f.db, "kampus")
	v1 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "v1")
	f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "v2")

	f.claim(t, "w1")
	got, err := f.store.Retry(ctx, v1.ID, "w1", "target sibuk")
	if err != nil || got.Status != queue.StatusQueued {
		t.Fatalf("retry v1: %+v err=%v", got, err)
	}
	if c := f.claim(t, "w2"); c != nil {
		t.Fatalf("v2 tidak boleh menyalip v1 yang sedang menunggu jeda, dapat job %d", c.ID)
	}

	b1 := f.enqueue(t, dbB, mustMigration(t, f.db, dbB), "b1")
	if c := f.claim(t, "w2"); c == nil || c.ID != b1.ID {
		t.Fatalf("database lain tetap harus bisa di-claim, dapat %+v", c)
	}

	f.exec(t, `UPDATE jobs SET next_run_at = now() WHERE id = $1`, v1.ID)
	c := f.claim(t, "w3")
	if c == nil || c.ID != v1.ID {
		t.Fatalf("setelah jeda lewat, v1 yang harus jalan, dapat %+v", c)
	}
	if c.ErrorMessage != "" {
		t.Fatalf("pesan error attempt sebelumnya harus dikosongkan saat claim, dapat %q", c.ErrorMessage)
	}
}

// Claim lain yang belum commit tidak boleh membuat claim ini kosong
// padahal database lain punya job siap.
func TestClaimWhileAnotherClaimIsUncommitted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbA := testutil.MustDBConn(t, f.db, "toko")
	dbB := testutil.MustDBConn(t, f.db, "kampus")
	a1 := f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a1")
	f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a2")
	b1 := f.enqueue(t, dbB, mustMigration(t, f.db, dbB), "b1")

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE jobs SET status = 'running', attempt = 1, lease_owner = 'lain', lease_until = now() + interval '1 minute'
		WHERE id = $1`, a1.ID); err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := f.store.Claim(cctx, "w2", time.Minute)
	if err != nil {
		t.Fatalf("claim tidak boleh error atau menunggu: %v", err)
	}
	if c == nil || c.ID != b1.ID {
		t.Fatalf("claim harus mengambil job DB B, bukan kosong atau a2; dapat %+v", c)
	}
}

// Jalur pengaman terakhir: aktivasi di database yang sama yang belum
// terlihat oleh snapshot claim ditolak index unik (23505). Claim harus
// mencoba lagi dan mengambil job database lain, bukan error atau kosong.
func TestClaimRetriesAfterUniqueViolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbA := testutil.MustDBConn(t, f.db, "toko")
	dbB := testutil.MustDBConn(t, f.db, "kampus")
	f.enqueue(t, dbA, mustMigration(t, f.db, dbA), "a1")
	other := mustMigration(t, f.db, dbA)
	b1 := f.enqueue(t, dbB, mustMigration(t, f.db, dbB), "b1")

	// Transaction lain mengaktifkan job di DB A tetapi belum commit.
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO jobs (db_id, migration_id, idempotency_key, status, attempt, lease_owner, lease_until)
		VALUES ($1, $2, 'manual', 'running', 1, 'lain', now() + interval '1 minute')`, dbA, other); err != nil {
		t.Fatal(err)
	}

	type result struct {
		job *queue.Job
		err error
	}
	done := make(chan result, 1)
	go func() {
		j, err := f.store.Claim(ctx, "w2", time.Minute)
		done <- result{j, err}
	}()

	// Tunggu sampai claim benar-benar tertahan pemeriksaan index unik.
	// Dibaca lewat f.db, bukan tx: di dalam satu transaction PostgreSQL
	// menyimpan snapshot pg_stat_activity, jadi polling dari tx selalu
	// melihat keadaan pertama.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := f.db.QueryRow(`
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%next_id%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("claim selesai sebelum tertahan index, skenario tidak teruji: %+v err=%v", r.job, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("claim tidak pernah tertahan index unik")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("unique violation tidak boleh diteruskan sebagai error: %v", r.err)
		}
		if r.job == nil || r.job.ID != b1.ID {
			t.Fatalf("claim harus mencoba lagi dan mengambil job DB B, dapat %+v", r.job)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claim tidak selesai setelah transaction lain commit")
	}
}

func TestEnqueueRejectsDifferentKindOrMode(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dbID := testutil.MustDBConn(t, f.db, "toko")
	m := mustMigration(t, f.db, dbID)
	f.enqueue(t, dbID, m, "up-1")

	_, _, err := f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m, IdempotencyKey: "rb-1", Kind: queue.KindRollback})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("rollback saat job up masih terbuka harus ErrConflict, dapat %v", err)
	}
	_, _, err = f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m, IdempotencyKey: "up-1", Mode: queue.ModeBatch})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("kunci sama dengan mode berbeda harus ErrConflict, dapat %v", err)
	}
	_, _, err = f.store.Enqueue(ctx, queue.EnqueueParams{DBID: dbID, MigrationID: m, IdempotencyKey: "up-2", Mode: queue.ModeBatch})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mode berbeda saat job masih terbuka harus ErrConflict, dapat %v", err)
	}
}

// Job uncertain lama tidak boleh menutupi job running yang lease-nya lewat.
func TestRecoverablePrefersExpiredRunning(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, name := range []string{"u1", "u2", "u3"} {
		db := testutil.MustDBConn(t, f.db, name)
		j := f.enqueue(t, db, mustMigration(t, f.db, db), name)
		if c := f.claim(t, "w1"); c == nil || c.ID != j.ID {
			t.Fatalf("claim %s: %+v", name, c)
		}
		if err := f.store.Finish(ctx, j.ID, "w1", queue.StatusUncertain, ""); err != nil {
			t.Fatal(err)
		}
	}
	db := testutil.MustDBConn(t, f.db, "toko")
	r := f.enqueue(t, db, mustMigration(t, f.db, db), "r")
	f.claim(t, "w2")
	f.exec(t, `UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, r.ID)

	rec, err := f.store.Recoverable(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 3 || rec[0].ID != r.ID {
		t.Fatalf("job running dengan lease lewat harus didahulukan: %+v", rec)
	}
}

// Status partial dan partially_reverted yang ditulis lewat Store menahan database.
func TestPartialStatusesHoldDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, st := range []queue.Status{queue.StatusPartial, queue.StatusPartiallyReverted} {
		db := testutil.MustDBConn(t, f.db, "db-"+string(st))
		j := f.enqueue(t, db, mustMigration(t, f.db, db), "a-"+string(st))
		f.enqueue(t, db, mustMigration(t, f.db, db), "b-"+string(st))
		f.claim(t, "w1")
		if err := f.store.Finish(ctx, j.ID, "w1", st, ""); err != nil {
			t.Fatal(err)
		}
		if c := f.claim(t, "w2"); c != nil {
			t.Fatalf("job %s harus menahan database, tetapi job %d di-claim", st, c.ID)
		}
		got, _ := f.store.Get(ctx, j.ID)
		if got.FinishedAt != nil {
			t.Fatalf("%s bukan status terminal, finished_at harus kosong", st)
		}
	}
}

func TestStoreWithoutPolicyUsesDefault(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &queue.Store{DB: db}
	dbID := testutil.MustDBConn(t, db, "toko")
	j, created, err := s.Enqueue(context.Background(), queue.EnqueueParams{DBID: dbID, MigrationID: mustMigration(t, db, dbID), IdempotencyKey: "k"})
	if err != nil || !created || j.MaxAttempts != queue.DefaultRetryPolicy().MaxAttempts {
		t.Fatalf("Store tanpa Policy harus memakai default: %+v err=%v", j, err)
	}
	if _, err := s.Claim(context.Background(), "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.Retry(context.Background(), j.ID, "w1", "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt.Sub(time.Now()) < time.Second {
		t.Fatalf("retry tanpa Policy harus tetap memakai jeda default, next_run_at=%v", got.NextRunAt)
	}
}
