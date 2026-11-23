package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestRevalidateDetectsChanges(t *testing.T) {
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
		Authz: &authz.Service{DB: db},
	}

	d, err := s.Create(ctx, alice, dbID, migID, queue.KindUp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, dbID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, alice, dbID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, alice, dbID, d.ID); err != nil {
		t.Fatal(err)
	}

	wantConflict := func(name string) {
		t.Helper()
		if err := s.Revalidate(ctx, d.ID, alice.ID, false); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("%s: err=%v, want ErrConflict", name, err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Revalidate(ctx, d.ID, alice.ID, false); err != nil {
		t.Fatalf("kondisi awal: %v", err)
	}

	exec(`UPDATE migrations SET revision = revision + 1 WHERE id = $1`, migID)
	wantConflict("revisi berubah")
	exec(`UPDATE migrations SET revision = revision - 1 WHERE id = $1`, migID)
	if err := s.Revalidate(ctx, d.ID, alice.ID, false); err != nil {
		t.Fatalf("revisi dikembalikan: %v", err)
	}

	exec(`UPDATE groups SET leader_user_id = NULL WHERE id = $1`, g)
	wantConflict("leader dihapus")
	exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, g, alice.ID)
	if _, err := s.Approve(ctx, alice, dbID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Revalidate(ctx, d.ID, alice.ID, false); err != nil {
		t.Fatalf("leader dikembalikan: %v", err)
	}

	testutil.MustGrant(t, db, g, dbID, "view")
	wantConflict("pengaju turun ke View Only")
	testutil.MustGrant(t, db, g, dbID, "edit")

	exec(`UPDATE users SET is_active = false WHERE id = $1`, alice.ID)
	wantConflict("pengaju nonaktif")
	exec(`UPDATE users SET is_active = true WHERE id = $1`, alice.ID)

	exec(`DELETE FROM group_db_access WHERE group_id = $1`, g)
	wantConflict("tidak ada group dengan akses")
}

func TestApprovalResetWhenLeaderDeleted(t *testing.T) {
	ctx := context.Background()
	db := testutil.PlatformDB(t)
	alice := testutil.MustUser(t, db, "alice", false)
	bob := testutil.MustUser(t, db, "bob", false)
	dbID := testutil.MustDBConn(t, db, "toko")
	g := testutil.MustGroup(t, db, "tim")
	testutil.MustAddMember(t, db, g, alice.ID)
	testutil.MustAddMember(t, db, g, bob.ID)
	testutil.MustGrant(t, db, g, dbID, "edit")
	if _, err := db.Exec(`UPDATE groups SET leader_user_id = $2 WHERE id = $1`, g, bob.ID); err != nil {
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
	d, err = s.Approve(ctx, bob, dbID, d.ID)
	if err != nil || d.Status != StatusApproved {
		t.Fatalf("setelah approve: %+v err=%v", d, err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, bob.ID); err != nil {
		t.Fatal(err)
	}
	d, err = s.Get(ctx, dbID, d.ID)
	if err != nil || d.Status != StatusPendingReview {
		t.Fatalf("setelah leader dihapus: %+v err=%v", d, err)
	}
	if _, err := s.Enqueue(ctx, alice, dbID, d.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("enqueue tanpa leader: err=%v, want ErrConflict", err)
	}
}
