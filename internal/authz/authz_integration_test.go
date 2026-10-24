package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestServiceEffectiveAccess(t *testing.T) {
	db := testutil.PlatformDB(t)
	ctx := context.Background()
	az := &authz.Service{DB: db}

	alice := testutil.MustUser(t, db, "alice", false)
	bob := testutil.MustUser(t, db, "bob", false)
	admin := testutil.MustUser(t, db, "root", true)

	toko := testutil.MustDBConn(t, db, "toko")
	kampus := testutil.MustDBConn(t, db, "kampus")

	viewers := testutil.MustGroup(t, db, "viewers")
	editors := testutil.MustGroup(t, db, "editors")
	testutil.MustAddMember(t, db, viewers, alice.ID)
	testutil.MustAddMember(t, db, editors, alice.ID)
	testutil.MustAddMember(t, db, viewers, bob.ID)

	testutil.MustGrant(t, db, viewers, toko, "view")
	testutil.MustGrant(t, db, editors, toko, "edit")
	testutil.MustGrant(t, db, viewers, kampus, "view")

	level := func(u *domain.User, dbID int64) domain.AccessLevel {
		l, err := az.Level(ctx, u, dbID)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	if l := level(alice, toko); l != domain.AccessEdit {
		t.Errorf("alice@toko (view+edit dari 2 group) = %v, want edit", l)
	}
	if l := level(bob, toko); l != domain.AccessView {
		t.Errorf("bob@toko = %v, want view", l)
	}
	if l := level(admin, toko); l != domain.AccessNone {
		t.Errorf("admin tanpa grant = %v, want none (admin bukan level akses DB)", l)
	}

	if err := az.Authorize(ctx, bob, toko, authz.ActionExecuteMigration); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("bob execute: %v, want ErrForbidden", err)
	}
	if err := az.Authorize(ctx, admin, toko, authz.ActionViewSchema); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("admin view tanpa grant: %v, want ErrNotFound", err)
	}

	list, err := az.AccessibleDatabases(ctx, alice)
	if err != nil || len(list) != 2 {
		t.Fatalf("AccessibleDatabases(alice) = %v, %v; want 2 db", list, err)
	}

	if _, err := db.Exec(`DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, editors, alice.ID); err != nil {
		t.Fatal(err)
	}
	if l := level(alice, toko); l != domain.AccessView {
		t.Errorf("setelah keluar dari editors: %v, want view", l)
	}

	if _, err := db.Exec(`UPDATE db_connections SET is_active=false WHERE id=$1`, toko); err != nil {
		t.Fatal(err)
	}
	if l := level(alice, toko); l != domain.AccessNone {
		t.Errorf("db nonaktif: %v, want none", l)
	}

	if _, err := db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, bob.ID); err != nil {
		t.Fatal(err)
	}
	if l := level(bob, kampus); l != domain.AccessNone {
		t.Errorf("user nonaktif: %v, want none", l)
	}
}
