package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// loginAs membuka browser baru yang sudah login.
func loginAs(t *testing.T, h http.Handler, username, password string) *browser {
	t.Helper()
	b := &browser{t: t, h: h}
	rr := b.do("POST", "/login", url.Values{"username": {username}, "password": {password}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("login %s: status %d", username, rr.Code)
	}
	return b
}

// post mengirim form beserta token CSRF yang valid, supaya penolakan yang
// terjadi memang berasal dari aturan akses, bukan dari CSRF.
func (b *browser) post(path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	page := b.do("GET", "/databases", nil)
	m := csrfInput.FindStringSubmatch(page.Body.String())
	if m == nil {
		b.t.Fatalf("token CSRF tidak ditemukan (status %d)", page.Code)
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", m[1])
	return b.do("POST", path, form)
}

var idInPath = regexp.MustCompile(`/(\d+)\?`)

func locationID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	m := idInPath.FindStringSubmatch(rr.Header().Get("Location"))
	if m == nil {
		t.Fatalf("Location %q tidak memuat ID", rr.Header().Get("Location"))
	}
	return m[1]
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminRoutesRejectNonAdmin(t *testing.T) {
	db := testutil.PlatformDB(t)
	root := testutil.MustUser(t, db, "root", true)
	bob := testutil.MustUser(t, db, "bob", false)
	gid := testutil.MustGroup(t, db, "viewers")
	toko := testutil.MustDBConn(t, db, "toko")
	h := newHandler(t, db)
	b := loginAs(t, h, "bob", testutil.TestPassword)

	type route struct {
		method, path string
		form         url.Values
	}
	routes := []route{
		{"GET", "/admin", nil},
		{"GET", "/admin/users", nil},
		{"GET", "/admin/users/new", nil},
		{"POST", "/admin/users", url.Values{"username": {"evil"}, "password": {"password123"}, "is_admin": {"on"}}},
		{"GET", fmt.Sprintf("/admin/users/%d", root.ID), nil},
		{"POST", fmt.Sprintf("/admin/users/%d", bob.ID), url.Values{"aksi": {"status"}, "is_active": {"on"}, "is_admin": {"on"}}},
		{"POST", fmt.Sprintf("/admin/users/%d", root.ID), url.Values{"aksi": {"password"}, "password": {"diambilalih"}}},
		{"POST", fmt.Sprintf("/admin/users/%d/delete", root.ID), url.Values{"confirm": {"on"}}},
		{"GET", "/admin/groups", nil},
		{"POST", "/admin/groups", url.Values{"name": {"evil"}}},
		{"GET", fmt.Sprintf("/admin/groups/%d", gid), nil},
		{"POST", fmt.Sprintf("/admin/groups/%d", gid), url.Values{"name": {"diganti"}}},
		{"POST", fmt.Sprintf("/admin/groups/%d/delete", gid), url.Values{"confirm": {"on"}}},
		{"POST", fmt.Sprintf("/admin/groups/%d/members", gid), url.Values{"user_id": {fmt.Sprint(bob.ID)}}},
		{"POST", fmt.Sprintf("/admin/groups/%d/members/%d/delete", gid, bob.ID), nil},
		{"POST", fmt.Sprintf("/admin/groups/%d/access", gid), url.Values{"db_id": {fmt.Sprint(toko)}, "level": {"edit"}}},
		{"POST", fmt.Sprintf("/admin/groups/%d/access/%d/delete", gid, toko), nil},
		{"GET", "/admin/databases", nil},
		{"GET", "/admin/databases/new", nil},
		{"POST", "/admin/databases", nil},
		{"GET", fmt.Sprintf("/admin/databases/%d", toko), nil},
		{"POST", fmt.Sprintf("/admin/databases/%d", toko), nil},
		{"POST", fmt.Sprintf("/admin/databases/%d/test", toko), nil},
		{"POST", fmt.Sprintf("/admin/databases/%d/delete", toko), nil},
	}
	for _, rt := range routes {
		var rr *httptest.ResponseRecorder
		if rt.method == "POST" {
			rr = b.post(rt.path, rt.form)
		} else {
			rr = b.do("GET", rt.path, nil)
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", rt.method, rt.path, rr.Code)
		}
	}

	// Tidak ada satu pun yang berubah.
	if n := count(t, db, `SELECT count(*) FROM users`); n != 2 {
		t.Errorf("jumlah user %d, want 2", n)
	}
	if n := count(t, db, `SELECT count(*) FROM users WHERE is_platform_admin`); n != 1 {
		t.Errorf("jumlah admin %d, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM groups WHERE name = 'viewers'`); n != 1 {
		t.Error("group berubah atau terhapus")
	}
	if n := count(t, db, `SELECT count(*) FROM group_members`) + count(t, db, `SELECT count(*) FROM group_db_access`); n != 0 {
		t.Error("anggota atau grant bertambah")
	}
}

func TestAdminUserLifecycle(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)

	if rr := root.do("GET", "/admin", nil); rr.Code != http.StatusOK {
		t.Fatalf("GET /admin: status %d", rr.Code)
	}

	rr := root.post("/admin/users", url.Values{"username": {"carol"}, "password": {"rahasia123"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat user: status %d", rr.Code)
	}
	id := locationID(t, rr)
	if page := root.do("GET", "/admin/users/"+id+"?ok=user-created", nil); !strings.Contains(page.Body.String(), "User berhasil dibuat.") {
		t.Error("pesan sukses tidak tampil")
	}
	if page := root.do("GET", "/admin/users?ok=<script>", nil); strings.Contains(page.Body.String(), "<script>") {
		t.Error("isi query string ikut dirender")
	}

	for name, form := range map[string]url.Values{
		"username dobel":  {"username": {"CAROL"}, "password": {"rahasia123"}},
		"username salah":  {"username": {"a b"}, "password": {"rahasia123"}},
		"password pendek": {"username": {"dave"}, "password": {"123"}},
	} {
		rr := root.post("/admin/users", form)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "rahasia123") {
			t.Errorf("%s: password diisi ulang di form", name)
		}
	}

	carol := loginAs(t, h, "carol", "rahasia123")
	if rr := carol.do("GET", "/databases", nil); rr.Code != http.StatusOK {
		t.Fatalf("carol belum bisa masuk: %d", rr.Code)
	}

	// Menonaktifkan user langsung mengeluarkannya.
	if rr := root.post("/admin/users/"+id, url.Values{"aksi": {"status"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("nonaktifkan: status %d", rr.Code)
	}
	if rr := carol.do("GET", "/databases", nil); rr.Code != http.StatusSeeOther {
		t.Errorf("user nonaktif masih bisa masuk: %d", rr.Code)
	}
	if rr := (&browser{t: t, h: h}).do("POST", "/login", url.Values{"username": {"carol"}, "password": {"rahasia123"}}); rr.Code != http.StatusUnauthorized {
		t.Errorf("user nonaktif bisa login: %d", rr.Code)
	}

	if rr := root.post("/admin/users/"+id, url.Values{"aksi": {"status"}, "is_active": {"on"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("aktifkan lagi: status %d", rr.Code)
	}
	carol = loginAs(t, h, "carol", "rahasia123")

	// Reset password mencabut session yang sedang berjalan.
	if rr := root.post("/admin/users/"+id, url.Values{"aksi": {"password"}, "password": {"passwordbaru"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("reset password: status %d", rr.Code)
	}
	if rr := carol.do("GET", "/databases", nil); rr.Code != http.StatusSeeOther {
		t.Errorf("session lama masih berlaku setelah reset password: %d", rr.Code)
	}
	loginAs(t, h, "carol", "passwordbaru")

	if rr := root.post("/admin/users/"+id+"/delete", nil); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("hapus tanpa konfirmasi: status %d, want 422", rr.Code)
	}
	if rr := root.post("/admin/users/"+id+"/delete", url.Values{"confirm": {"on"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("hapus user: status %d", rr.Code)
	}
	if rr := root.do("GET", "/admin/users/"+id, nil); rr.Code != http.StatusNotFound {
		t.Errorf("user terhapus: status %d, want 404", rr.Code)
	}
	if rr := root.do("GET", "/admin/users/abc", nil); rr.Code != http.StatusNotFound {
		t.Errorf("ID tidak valid: status %d, want 404", rr.Code)
	}
}

func TestAdminCannotLockThemselvesOut(t *testing.T) {
	db := testutil.PlatformDB(t)
	me := testutil.MustUser(t, db, "root", true)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)
	path := fmt.Sprintf("/admin/users/%d", me.ID)

	for name, rr := range map[string]*httptest.ResponseRecorder{
		"nonaktifkan diri": root.post(path, url.Values{"aksi": {"status"}, "is_admin": {"on"}}),
		"turunkan diri":    root.post(path, url.Values{"aksi": {"status"}, "is_active": {"on"}}),
		"hapus diri":       root.post(path+"/delete", url.Values{"confirm": {"on"}}),
	} {
		if rr.Code != http.StatusConflict {
			t.Errorf("%s: status %d, want 409", name, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "akun Anda sendiri") {
			t.Errorf("%s: pesan penjelasan tidak tampil", name)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM users WHERE is_platform_admin AND is_active`); n != 1 {
		t.Fatalf("admin aktif tersisa %d", n)
	}
}

func TestAdminGroupAccessTakesEffectImmediately(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	bobUser := testutil.MustUser(t, db, "bob", false)
	toko := testutil.MustDBConn(t, db, "toko")
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)
	bob := loginAs(t, h, "bob", testutil.TestPassword)

	schema := fmt.Sprintf("/databases/%d", toko)
	newMigration := schema + "/migrations/new"
	allowed := func(path string) bool {
		rr := bob.do("GET", path, nil)
		return rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound
	}

	rr := root.post("/admin/groups", url.Values{"name": {"viewers"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat group: status %d", rr.Code)
	}
	gid := locationID(t, rr)
	group := "/admin/groups/" + gid
	if rr := root.post("/admin/groups", url.Values{"name": {"VIEWERS"}}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("nama group dobel: status %d, want 422", rr.Code)
	}

	member := url.Values{"user_id": {fmt.Sprint(bobUser.ID)}}
	if rr := root.post(group+"/members", member); !strings.HasSuffix(rr.Header().Get("Location"), "ok=member-added") {
		t.Fatalf("tambah anggota: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if rr := root.post(group+"/members", member); !strings.HasSuffix(rr.Header().Get("Location"), "ok=member-exists") {
		t.Errorf("tambah anggota dua kali: %s", rr.Header().Get("Location"))
	}

	if bob.do("GET", schema, nil).Code != http.StatusNotFound {
		t.Error("bob bisa membuka database sebelum diberi akses")
	}

	grant := func(level string) *httptest.ResponseRecorder {
		return root.post(group+"/access", url.Values{"db_id": {fmt.Sprint(toko)}, "level": {level}})
	}
	if rr := grant("view"); rr.Code != http.StatusSeeOther {
		t.Fatalf("grant view: status %d", rr.Code)
	}
	if !allowed(schema) {
		t.Error("View Only tidak bisa melihat schema")
	}
	if bob.do("GET", newMigration, nil).Code != http.StatusForbidden {
		t.Error("View Only bisa membuka form migration (aksi Edit)")
	}
	if page := bob.do("GET", "/databases", nil).Body.String(); !strings.Contains(page, "toko") || !strings.Contains(page, "View Only") {
		t.Error("daftar database bob tidak menampilkan toko dengan level View Only")
	}

	if rr := grant("edit"); rr.Code != http.StatusSeeOther {
		t.Fatalf("grant edit: status %d", rr.Code)
	}
	if !allowed(newMigration) {
		t.Error("level edit tidak langsung berlaku")
	}

	for name, form := range map[string]url.Values{
		"level tidak dikenal": {"db_id": {fmt.Sprint(toko)}, "level": {"admin"}},
		"db_id bukan angka":   {"db_id": {"abc"}, "level": {"view"}},
	} {
		if rr := root.post(group+"/access", form); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, rr.Code)
		}
	}
	if rr := root.post(group+"/access", url.Values{"db_id": {"99999"}, "level": {"view"}}); rr.Code != http.StatusNotFound {
		t.Errorf("database tidak ada: status %d, want 404", rr.Code)
	}

	if page := root.do("GET", group, nil); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "bob") {
		t.Errorf("halaman group: status %d", page.Code)
	}

	// Mencabut akses langsung berlaku tanpa bob perlu login ulang.
	if rr := root.post(fmt.Sprintf("%s/access/%d/delete", group, toko), nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("cabut akses: status %d", rr.Code)
	}
	if bob.do("GET", schema, nil).Code != http.StatusNotFound {
		t.Error("akses bob masih ada setelah dicabut")
	}

	grant("view")
	if rr := root.post(fmt.Sprintf("%s/members/%d/delete", group, bobUser.ID), nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("keluarkan anggota: status %d", rr.Code)
	}
	if bob.do("GET", schema, nil).Code != http.StatusNotFound {
		t.Error("akses bob masih ada setelah keluar dari group")
	}

	root.post(group+"/members", member)
	if !allowed(schema) {
		t.Fatal("bob seharusnya punya akses lagi")
	}
	if rr := root.post(group+"/delete", url.Values{"confirm": {"on"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("hapus group: status %d", rr.Code)
	}
	if bob.do("GET", schema, nil).Code != http.StatusNotFound {
		t.Error("akses bob masih ada setelah group dihapus")
	}
}
