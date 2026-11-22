package approval

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestApprovalWorkflow(t *testing.T) {
	ctx := context.Background()
	db := testutil.PlatformDB(t)
	alice := testutil.MustUser(t, db, "alice", false)
	bob := testutil.MustUser(t, db, "bob", false)
	carol := testutil.MustUser(t, db, "carol", false)
	dave := testutil.MustUser(t, db, "dave", false)
	dbID := testutil.MustDBConn(t, db, "toko")
	ga := testutil.MustGroup(t, db, "tim-a")
	gb := testutil.MustGroup(t, db, "tim-b")
	gc := testutil.MustGroup(t, db, "tim-c")
	testutil.MustAddMember(t, db, ga, alice.ID)
	testutil.MustAddMember(t, db, ga, bob.ID)
	testutil.MustAddMember(t, db, gb, bob.ID)
	testutil.MustAddMember(t, db, gc, carol.ID)
	testutil.MustGrant(t, db, ga, dbID, "edit")
	testutil.MustGrant(t, db, gb, dbID, "view")

	var migID int64
	if err := db.QueryRow(`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 'm', 'SELECT 1') RETURNING id`, dbID).Scan(&migID); err != nil {
		t.Fatal(err)
	}

	s := &Store{
		DB:    db,
		Queue: &queue.Store{DB: db, Policy: queue.DefaultRetryPolicy()},
		Guard: func(context.Context, Stage, int64, int64, queue.Kind, *int64) error { return nil },
	}
	setLeader := func(gid, uid int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, gid, uid); err != nil {
			t.Fatal(err)
		}
	}
	want := func(d *Detail, err error, status Status) *Detail {
		t.Helper()
		if err != nil || d.Status != status {
			t.Fatalf("status = %v err = %v, want %s", d, err, status)
		}
		return d
	}

	if _, err := db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, ga, dave.ID); err == nil {
		t.Fatal("non-anggota tidak boleh menjadi leader")
	}

	d, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil)
	want(d, err, StatusDraft)
	id := d.ID
	if _, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("request kedua untuk migration yang sama: %v", err)
	}
	if _, err := s.Enqueue(ctx, alice, dbID, id); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue draft: %v", err)
	}

	d, err = s.Submit(ctx, dbID, id)
	want(d, err, StatusPendingReview)
	if len(d.Approvals) != 2 {
		t.Fatalf("approval = %d, want 2", len(d.Approvals))
	}

	if _, err := s.Approve(ctx, dave, dbID, id); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-leader approve: %v", err)
	}
	if _, err := s.Approve(ctx, bob, dbID, id); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("group tanpa leader tidak boleh diapprove: %v", err)
	}

	setLeader(ga, bob.ID)
	d, err = s.Approve(ctx, bob, dbID, id)
	want(d, err, StatusPendingReview)
	if _, err := s.Enqueue(ctx, alice, dbID, id); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue sebelum semua approve: %v", err)
	}

	setLeader(gb, bob.ID)
	d, err = s.Approve(ctx, bob, dbID, id)
	want(d, err, StatusApproved)

	testutil.MustGrant(t, db, gc, dbID, "view")
	d, err = s.Get(ctx, dbID, id)
	want(d, err, StatusPendingReview)
	setLeader(gc, carol.ID)
	d, err = s.Approve(ctx, carol, dbID, id)
	want(d, err, StatusApproved)

	setLeader(ga, alice.ID)
	d, err = s.Get(ctx, dbID, id)
	want(d, err, StatusPendingReview)
	d, err = s.Approve(ctx, alice, dbID, id)
	want(d, err, StatusApproved)

	job, err := s.Enqueue(ctx, alice, dbID, id)
	if err != nil || job.RequestID == nil || *job.RequestID != id || job.Kind != queue.KindUp {
		t.Fatalf("enqueue: %+v err=%v", job, err)
	}
	again, err := s.Enqueue(ctx, alice, dbID, id)
	if err != nil || again.ID != job.ID {
		t.Fatalf("enqueue ulang harus idempoten: %+v err=%v", again, err)
	}
	d, err = s.Get(ctx, dbID, id)
	want(d, err, StatusQueued)
	if d.JobID == nil || *d.JobID != job.ID {
		t.Fatalf("job_id = %v", d.JobID)
	}

	if _, err := db.Exec(`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, gb, bob.ID); err != nil {
		t.Fatal(err)
	}
	var leader sql.NullInt64
	if err := db.QueryRow(`SELECT leader_user_id FROM groups WHERE id = $1`, gb).Scan(&leader); err != nil || leader.Valid {
		t.Fatalf("leader harus dikosongkan saat keluar dari group: %v err=%v", leader, err)
	}
}

func TestRejectStopsRequest(t *testing.T) {
	ctx := context.Background()
	db := testutil.PlatformDB(t)
	alice := testutil.MustUser(t, db, "alice", false)
	dbID := testutil.MustDBConn(t, db, "toko")
	g := testutil.MustGroup(t, db, "tim")
	testutil.MustAddMember(t, db, g, alice.ID)
	testutil.MustGrant(t, db, g, dbID, "edit")
	if _, err := db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, g, alice.ID); err != nil {
		t.Fatal(err)
	}
	var migID int64
	if err := db.QueryRow(`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 'm', 'SELECT 1') RETURNING id`, dbID).Scan(&migID); err != nil {
		t.Fatal(err)
	}
	s := &Store{
		DB:    db,
		Queue: &queue.Store{DB: db, Policy: queue.DefaultRetryPolicy()},
		Guard: func(context.Context, Stage, int64, int64, queue.Kind, *int64) error { return nil },
	}
	d, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, dbID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reject(ctx, alice, dbID, d.ID, "  "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("alasan kosong: %v", err)
	}
	d, err = s.Reject(ctx, alice, dbID, d.ID, "SQL belum benar")
	if err != nil || d.Status != StatusRejected || d.RejectReason != "SQL belum benar" {
		t.Fatalf("reject: %+v err=%v", d, err)
	}
	if _, err := s.Approve(ctx, alice, dbID, d.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("approve setelah reject: %v", err)
	}
	if _, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil); err != nil {
		t.Fatalf("request baru setelah reject harus boleh: %v", err)
	}
}

type approvedFixture struct {
	db    *sql.DB
	s     *Store
	qs    *queue.Store
	alice *domain.User
	dbID  int64
	group int64
	reqID int64
}

func newApprovedFixture(t *testing.T) approvedFixture {
	t.Helper()
	ctx := context.Background()
	db := testutil.PlatformDB(t)
	alice := testutil.MustUser(t, db, "alice", false)
	dbID := testutil.MustDBConn(t, db, "toko")
	g := testutil.MustGroup(t, db, "tim")
	testutil.MustAddMember(t, db, g, alice.ID)
	testutil.MustGrant(t, db, g, dbID, "edit")
	if _, err := db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, g, alice.ID); err != nil {
		t.Fatal(err)
	}
	var migID int64
	if err := db.QueryRow(`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 'm', 'SELECT 1') RETURNING id`, dbID).Scan(&migID); err != nil {
		t.Fatal(err)
	}
	qs := &queue.Store{DB: db, Policy: queue.DefaultRetryPolicy()}
	s := &Store{
		DB:    db,
		Queue: qs,
		Guard: func(context.Context, Stage, int64, int64, queue.Kind, *int64) error { return nil },
	}
	d, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, dbID, d.ID); err != nil {
		t.Fatal(err)
	}
	d, err = s.Approve(ctx, alice, dbID, d.ID)
	if err != nil || d.Status != StatusApproved {
		t.Fatalf("setelah approve: %+v err=%v", d, err)
	}
	return approvedFixture{db: db, s: s, qs: qs, alice: alice, dbID: dbID, group: g, reqID: d.ID}
}

func (f approvedFixture) requestStatus(t *testing.T) string {
	t.Helper()
	var st string
	if err := f.db.QueryRow(`SELECT status FROM change_requests WHERE id = $1`, f.reqID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (f approvedFixture) jobCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM jobs WHERE request_id = $1`, f.reqID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f approvedFixture) migrationID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT migration_id FROM change_requests WHERE id = $1`, f.reqID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnqueueRejectedAfterSuccessAllowedAfterFailure(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)

	first, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil || c == nil || c.ID != first.ID {
		t.Fatalf("claim job pertama: %+v err=%v", c, err)
	}
	if err := f.qs.Finish(ctx, first.ID, "w1", queue.StatusFailed, "SQL gagal"); err != nil {
		t.Fatal(err)
	}

	second, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil || second.ID == first.ID {
		t.Fatalf("enqueue setelah gagal: %+v err=%v", second, err)
	}
	if c, err := f.qs.Claim(ctx, "w2", time.Minute); err != nil || c == nil || c.ID != second.ID {
		t.Fatalf("claim job kedua: %+v err=%v", c, err)
	}
	if err := f.qs.Finish(ctx, second.ID, "w2", queue.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue setelah sukses: err=%v, want ErrConflict", err)
	}
	if n := f.jobCount(t); n != 2 {
		t.Fatalf("jumlah job = %d, want 2", n)
	}
}

func TestEnqueueDetectsEditedSQLAfterFailure(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.qs.Finish(ctx, job.ID, "w1", queue.StatusFailed, "SQL gagal"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE migrations SET revision = revision + 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue setelah SQL berubah: err=%v, want ErrConflict", err)
	}
	if n := f.jobCount(t); n != 1 {
		t.Fatalf("jumlah job = %d, want 1", n)
	}
}

func TestSyncOpenRevokesApprovalWithoutReadingRequest(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)

	if _, err := f.db.Exec(`UPDATE groups SET leader_user_id = NULL WHERE id = $1`, f.group); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if st := f.requestStatus(t); st != string(StatusPendingReview) {
		t.Fatalf("status setelah SyncOpen = %s, want pending_review", st)
	}
}

func TestSyncOpenFailsQueuedJobWhenApprovalRevoked(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE groups SET leader_user_id = NULL WHERE id = $1`, f.group); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if st := f.requestStatus(t); st != string(StatusPendingReview) {
		t.Fatalf("status request = %s, want pending_review", st)
	}
	var jobStatus string
	if err := f.db.QueryRow(`SELECT status FROM jobs WHERE id = $1`, job.ID).Scan(&jobStatus); err != nil || jobStatus != string(queue.StatusFailed) {
		t.Fatalf("status job = %s err=%v, want failed", jobStatus, err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue sebelum approval ulang: err=%v, want ErrConflict", err)
	}
}

func TestSyncOpenIgnoresFinishedQueuedRequests(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.qs.Finish(ctx, job.ID, "w1", queue.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE groups SET leader_user_id = NULL WHERE id = $1`, f.group); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SyncOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if st := f.requestStatus(t); st != string(StatusQueued) {
		t.Fatalf("request yang job-nya selesai tidak boleh berubah: %s", st)
	}
}

func TestRejectsNonPostgresDatabase(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	migID := f.migrationID(t)
	if _, err := f.db.Exec(`UPDATE db_connections SET db_type = 'mysql' WHERE id = $1`, f.dbID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("enqueue mysql: err=%v, want ErrInvalidInput", err)
	}
	if _, err := f.s.Submit(ctx, f.dbID, f.reqID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("submit mysql: err=%v, want ErrInvalidInput", err)
	}
	if _, err := f.s.Create(ctx, f.alice, f.dbID, migID, queue.KindRollback, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("create mysql: err=%v, want ErrInvalidInput", err)
	}
	if n := f.jobCount(t); n != 0 {
		t.Fatalf("job terbentuk untuk database mysql: %d", n)
	}
}

func TestCancelRequest(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	d, err := f.s.Cancel(ctx, f.alice, f.dbID, f.reqID)
	if err != nil || d.Status != StatusCancelled {
		t.Fatalf("cancel: %+v err=%v", d, err)
	}
	if _, err := f.s.Cancel(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancel dua kali: err=%v, want ErrConflict", err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue setelah cancel: err=%v, want ErrConflict", err)
	}
	if _, err := f.s.Cancel(ctx, f.alice, f.dbID, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancel request tidak ada: err=%v, want ErrNotFound", err)
	}
}

func TestCancelRejectedOnceQueued(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Cancel(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancel setelah masuk antrean: err=%v, want ErrConflict", err)
	}
}

func TestConcurrentEnqueueCreatesSingleJob(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[int64]bool{}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
			if err != nil {
				t.Errorf("enqueue paralel: %v", err)
				return
			}
			mu.Lock()
			ids[job.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(ids) != 1 {
		t.Fatalf("job berbeda yang terbentuk = %d, want 1", len(ids))
	}
	if n := f.jobCount(t); n != 1 {
		t.Fatalf("jumlah job = %d, want 1", n)
	}
}

func TestConsumeRevertForPartialJob(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	migID := f.migrationID(t)

	up, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil || c == nil || c.ID != up.ID {
		t.Fatalf("claim: %+v err=%v", c, err)
	}
	if err := f.qs.Finish(ctx, up.ID, "w1", queue.StatusPartial, "batch 3 gagal"); err != nil {
		t.Fatal(err)
	}

	rb, err := f.s.Create(ctx, f.alice, f.dbID, migID, queue.KindRollback, nil)
	if err != nil {
		t.Fatalf("buat request rollback: %v", err)
	}
	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revert dengan request draft: err=%v, want ErrConflict", err)
	}
	if _, err := f.s.Submit(ctx, f.dbID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revert sebelum approved: err=%v, want ErrConflict", err)
	}
	d, err := f.s.Approve(ctx, f.alice, f.dbID, rb.ID)
	if err != nil || d.Status != StatusApproved {
		t.Fatalf("approve rollback: %+v err=%v", d, err)
	}

	_, err = f.s.Enqueue(ctx, f.alice, f.dbID, rb.ID)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "revert") {
		t.Fatalf("enqueue rollback saat job partial: err=%v, want konflik yang menyebut revert", err)
	}

	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, f.reqID+9999, rb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("job tidak ada: err=%v, want ErrNotFound", err)
	}
	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("request up dipakai untuk revert: err=%v, want ErrConflict", err)
	}

	got, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID)
	if err != nil || got.Status != StatusQueued || got.JobID == nil || *got.JobID != up.ID {
		t.Fatalf("revert: %+v err=%v", got, err)
	}
	again, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID)
	if err != nil || again.Status != StatusQueued {
		t.Fatalf("revert ulang harus idempoten: %+v err=%v", again, err)
	}
}

func TestConsumeRevertRejectsNonPartialJob(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	migID := f.migrationID(t)

	up, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.qs.Finish(ctx, up.ID, "w1", queue.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}

	rb, err := f.s.Create(ctx, f.alice, f.dbID, migID, queue.KindRollback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Submit(ctx, f.dbID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(ctx, f.alice, f.dbID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revert job succeeded: err=%v, want ErrConflict", err)
	}
	if st := f.requestStatus(t); st != string(StatusQueued) {
		t.Fatalf("request up berubah: %s", st)
	}
}

func TestEnqueueAfterFailureRechecksApprovals(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	job, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil || c == nil || c.ID != job.ID {
		t.Fatalf("claim: %+v err=%v", c, err)
	}
	if err := f.qs.Finish(ctx, job.ID, "w1", queue.StatusFailed, "SQL gagal"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE groups SET leader_user_id = NULL WHERE id = $1`, f.group); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue setelah leader dicabut: err=%v, want ErrConflict", err)
	}
	if st := f.requestStatus(t); st != string(StatusPendingReview) {
		t.Fatalf("status request = %s, want pending_review", st)
	}
	if n := f.jobCount(t); n != 1 {
		t.Fatalf("jumlah job = %d, want 1", n)
	}
}

func TestConsumedRevertRequestCannotBeEnqueued(t *testing.T) {
	ctx := context.Background()
	f := newApprovedFixture(t)
	migID := f.migrationID(t)
	up, err := f.s.Enqueue(ctx, f.alice, f.dbID, f.reqID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.qs.Claim(ctx, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.qs.Finish(ctx, up.ID, "w1", queue.StatusPartial, "batch 3 gagal"); err != nil {
		t.Fatal(err)
	}
	rb, err := f.s.Create(ctx, f.alice, f.dbID, migID, queue.KindRollback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Submit(ctx, f.dbID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(ctx, f.alice, f.dbID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ConsumeRevert(ctx, f.alice, f.dbID, up.ID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Enqueue(ctx, f.alice, f.dbID, rb.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue request yang sudah dipakai revert: err=%v, want ErrConflict", err)
	}
}
