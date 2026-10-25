package admin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/admin"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func formMsg(t *testing.T, err error) string {
	t.Helper()
	var fe *admin.FormError
	if !errors.As(err, &fe) {
		t.Fatalf("err=%v, want FormError", err)
	}
	return fe.Msg
}

func TestCreateUser(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "carol", "rahasia123", false)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := auth.Authenticate(ctx, db, "carol", "rahasia123"); err != nil || u.ID != id {
		t.Fatalf("user baru tidak bisa login: u=%v err=%v", u, err)
	}

	for _, c := range []struct{ name, username, password, msg string }{
		{"username dobel beda huruf", "CAROL", "rahasia123", "sudah dipakai"},
		{"username ada spasi", "a b", "rahasia123", "Username harus"},
		{"username terlalu pendek", "ab", "rahasia123", "Username harus"},
		{"password terlalu pendek", "dave", "1234567", "minimal 8"},
		{"password terlalu panjang", "dave", strings.Repeat("x", auth.MaxPasswordBytes+1), "maksimal"},
	} {
		_, err := s.CreateUser(ctx, c.username, c.password, false)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: err=%v, want ErrInvalidInput", c.name, err)
			continue
		}
		if msg := formMsg(t, err); !strings.Contains(msg, c.msg) {
			t.Errorf("%s: pesan %q tidak memuat %q", c.name, msg, c.msg)
		}
	}
}

func TestLastAdminCannotBeRemoved(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()
	root := testutil.MustUser(t, db, "root", true)
	const system = 0 // aktor yang bukan target, supaya yang dites aturan admin terakhir

	if err := s.UpdateUser(ctx, system, root.ID, false, true); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("nonaktifkan admin terakhir: err=%v, want ErrConflict", err)
	}
	if err := s.UpdateUser(ctx, system, root.ID, true, false); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("turunkan admin terakhir: err=%v, want ErrConflict", err)
	}
	if err := s.DeleteUser(ctx, system, root.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("hapus admin terakhir: err=%v, want ErrConflict", err)
	}

	second := testutil.MustUser(t, db, "second", true)
	err := s.UpdateUser(ctx, root.ID, root.ID, false, true)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(formMsg(t, err), "sendiri") {
		t.Errorf("admin menonaktifkan diri sendiri: err=%v, want ErrConflict", err)
	}
	if err := s.UpdateUser(ctx, second.ID, root.ID, true, false); err != nil {
		t.Fatalf("turunkan admin saat masih ada admin lain: %v", err)
	}
	if err := s.DeleteUser(ctx, root.ID, second.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second sekarang admin terakhir: err=%v, want ErrConflict", err)
	}

	// Admin yang sudah nonaktif tidak dihitung, jadi boleh dihapus.
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, is_platform_admin, is_active) VALUES ('old', 'x', true, false)`); err != nil {
		t.Fatal(err)
	}
	var oldID int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username = 'old'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, second.ID, oldID); err != nil {
		t.Errorf("hapus admin nonaktif: %v", err)
	}

	if err := s.UpdateUser(ctx, second.ID, 99999, true, false); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("user tidak ada: err=%v, want ErrNotFound", err)
	}
}

func TestConcurrentDemotionKeepsOneAdmin(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()
	a := testutil.MustUser(t, db, "alpha", true)
	b := testutil.MustUser(t, db, "bravo", true)

	// alpha menurunkan bravo dan bravo menurunkan alpha di saat yang sama.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pair := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.UpdateUser(ctx, pair[0], pair[1], true, false)
		}()
	}
	wg.Wait()

	var admins int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE is_platform_admin AND is_active`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("tersisa %d admin aktif, want 1 (errs: %v)", admins, errs)
	}
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("seharusnya tepat satu yang berhasil: %v", errs)
	}
}

func TestResetPassword(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()
	u := testutil.MustUser(t, db, "carol", false)

	if err := s.ResetPassword(ctx, u.ID, "pendek"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("password pendek: err=%v", err)
	}
	if err := s.ResetPassword(ctx, u.ID, "passwordbaru"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, db, "carol", testutil.TestPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Error("password lama masih berlaku")
	}
	if _, err := auth.Authenticate(ctx, db, "carol", "passwordbaru"); err != nil {
		t.Errorf("password baru ditolak: %v", err)
	}
	if err := s.ResetPassword(ctx, 99999, "passwordbaru"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("user tidak ada: err=%v", err)
	}
}

func TestGroupMembersAndAccess(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()
	root := testutil.MustUser(t, db, "root", true)
	bob := testutil.MustUser(t, db, "bob", false)
	off := testutil.MustUser(t, db, "off", false)
	if _, err := db.Exec(`UPDATE users SET is_active = false WHERE id = $1`, off.ID); err != nil {
		t.Fatal(err)
	}
	toko := testutil.MustDBConn(t, db, "toko")

	gid, err := s.CreateGroup(ctx, "  viewers  ", "  tim viewer ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(ctx, "VIEWERS", ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("nama group dobel: err=%v", err)
	}
	if _, err := s.CreateGroup(ctx, "   ", ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("nama kosong: err=%v", err)
	}

	added, err := s.AddMember(ctx, gid, bob.ID)
	if err != nil || !added {
		t.Fatalf("tambah anggota: added=%v err=%v", added, err)
	}
	if added, err := s.AddMember(ctx, gid, bob.ID); err != nil || added {
		t.Errorf("tambah anggota dua kali: added=%v err=%v", added, err)
	}
	if _, err := s.AddMember(ctx, gid, 99999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("user tidak ada: err=%v", err)
	}

	if err := s.Grant(ctx, gid, toko, domain.AccessView, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, gid, toko, domain.AccessEdit, root.ID); err != nil {
		t.Fatalf("ubah level: %v", err)
	}
	if err := s.Grant(ctx, gid, toko, domain.AccessNone, root.ID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("level none: err=%v", err)
	}
	if err := s.Grant(ctx, gid, 99999, domain.AccessView, root.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("database tidak ada: err=%v", err)
	}

	g, err := s.GetGroup(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "viewers" || g.Description != "tim viewer" {
		t.Errorf("nama/deskripsi tidak di-trim: %q %q", g.Name, g.Description)
	}
	if len(g.Access) != 1 || g.Access[0].Level != domain.AccessEdit {
		t.Errorf("akses = %+v, want satu baris edit", g.Access)
	}
	if len(g.Members) != 1 || g.Members[0].Username != "bob" {
		t.Errorf("anggota = %+v", g.Members)
	}
	for _, c := range g.Candidates {
		if c.Username == "bob" || c.Username == "off" {
			t.Errorf("kandidat anggota tidak boleh berisi %s", c.Username)
		}
	}

	if err := s.Revoke(ctx, gid, toko); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, gid, toko); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("cabut dua kali: err=%v", err)
	}
	if err := s.RemoveMember(ctx, gid, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, gid, bob.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("keluarkan dua kali: err=%v", err)
	}

	if err := s.DeleteGroup(ctx, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGroup(ctx, gid); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("group terhapus masih ada: err=%v", err)
	}
}

func TestDeleteGroupRemovesAccess(t *testing.T) {
	db := testutil.PlatformDB(t)
	s := &admin.Store{DB: db}
	ctx := context.Background()
	bob := testutil.MustUser(t, db, "bob", false)
	toko := testutil.MustDBConn(t, db, "toko")
	gid := testutil.MustGroup(t, db, "viewers")
	testutil.MustAddMember(t, db, gid, bob.ID)
	testutil.MustGrant(t, db, gid, toko, "view")

	if err := s.DeleteGroup(ctx, gid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM group_db_access WHERE group_id = $1`, gid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("grant group yang dihapus masih tersisa")
	}
}
