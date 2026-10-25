package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

type migrationEnv struct {
	h      http.Handler
	db     *sql.DB
	target *sql.DB
	base   string // /databases/{dbID}/migrations
	other  string // migration database lain
	group  int64
}

// newMigrationEnv menyiapkan satu database target dengan bob (View + Edit),
// carol (View Only), dan dave (tanpa akses).
func newMigrationEnv(t *testing.T) *migrationEnv {
	t.Helper()
	db := testutil.PlatformDB(t)
	target, _ := testutil.TargetDB(t)
	otherTarget, _ := testutil.TargetDB(t)
	dbID := testutil.MustDBConn(t, db, "toko")
	otherID := testutil.MustDBConn(t, db, "kampus")

	bob := testutil.MustUser(t, db, "bob", false)
	carol := testutil.MustUser(t, db, "carol", false)
	testutil.MustUser(t, db, "dave", false)
	editors := testutil.MustGroup(t, db, "editors")
	viewers := testutil.MustGroup(t, db, "viewers")
	testutil.MustAddMember(t, db, editors, bob.ID)
	testutil.MustAddMember(t, db, viewers, carol.ID)
	testutil.MustGrant(t, db, editors, dbID, "edit")
	testutil.MustGrant(t, db, editors, otherID, "edit")
	testutil.MustGrant(t, db, viewers, dbID, "view")

	pools := dbmanager.NewStatic(map[int64]*sql.DB{dbID: target, otherID: otherTarget})
	return &migrationEnv{
		h:      newHandlerWith(t, db, pools),
		db:     db,
		target: target,
		base:   fmt.Sprintf("/databases/%d/migrations", dbID),
		other:  fmt.Sprintf("/databases/%d/migrations", otherID),
		group:  editors,
	}
}

// createMigration membuat migration lewat form dan mengembalikan path detailnya.
func createMigration(t *testing.T, b *browser, base, title, sqlText string) string {
	t.Helper()
	rr := b.post(base, url.Values{"title": {title}, "sql": {sqlText}})
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, base+"/") {
		t.Fatalf("buat migration %q: status %d location %q", title, rr.Code, loc)
	}
	return loc
}

func (e *migrationEnv) columnExists(t *testing.T, table, column string) bool {
	t.Helper()
	var n int
	if err := e.target.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestMigrationFlow(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)

	if rr := bob.do("GET", e.base, nil); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Belum ada migration") {
		t.Fatalf("riwayat kosong: %d", rr.Code)
	}

	bad := bob.post(e.base, url.Values{"title": {"salah"}, "sql": {"BEGIN; CREATE TABLE x (id int); COMMIT;"}})
	if bad.Code != http.StatusUnprocessableEntity || !strings.Contains(bad.Body.String(), "BEGIN") {
		t.Errorf("SQL dengan BEGIN: status %d, want 422", bad.Code)
	}

	v1 := createMigration(t, bob, e.base, "buat city", `CREATE TABLE city (id INT PRIMARY KEY, name TEXT);
INSERT INTO city VALUES (1, '<b>Bandung</b>');`)
	review := bob.do("GET", v1, nil).Body.String()
	if !strings.Contains(review, "&lt;b&gt;Bandung") || strings.Contains(review, "<b>Bandung") {
		t.Error("SQL di halaman review tidak di-escape")
	}
	if !strings.Contains(review, "Jalankan migration") {
		t.Error("tombol jalankan tidak muncul untuk View + Edit")
	}

	if rr := bob.post(v1+"/execute", nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("eksekusi v1: %d", rr.Code)
	}
	if !e.columnExists(t, "city", "name") {
		t.Fatal("v1 tidak ter-commit")
	}

	v2 := createMigration(t, bob, e.base, "tambah kolom", `ALTER TABLE city ADD COLUMN pop INT;
INSERT INTO tidak_ada VALUES (1);`)
	v3 := createMigration(t, bob, e.base, "index", `CREATE INDEX city_name_idx ON city (name);`)

	if rr := bob.post(v2+"/execute", nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("eksekusi v2: %d", rr.Code)
	}
	if detail := bob.do("GET", v2, nil).Body.String(); !strings.Contains(detail, "tidak_ada") || !strings.Contains(detail, "failed") {
		t.Error("pesan error v2 tidak tampil")
	}
	if e.columnExists(t, "city", "pop") {
		t.Fatal("kolom pop tidak di-rollback")
	}

	// v3 tidak boleh melompati v2 yang masih gagal, dan alasannya ditampilkan.
	if rr := bob.post(v3+"/execute", nil); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "versi 2") {
		t.Errorf("v3 sebelum v2: status %d, want 409 dengan alasan", rr.Code)
	}

	if rr := bob.post(v2, url.Values{"title": {"tambah kolom"}, "sql": {"ALTER TABLE city ADD COLUMN pop INT;"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("simpan v2: %d", rr.Code)
	}
	for _, v := range []string{v2, v3} {
		if rr := bob.post(v+"/execute", nil); rr.Code != http.StatusSeeOther {
			t.Fatalf("eksekusi %s: %d", v, rr.Code)
		}
	}
	if !e.columnExists(t, "city", "pop") {
		t.Fatal("v2 tidak ter-commit setelah diperbaiki")
	}

	// Migration yang sudah sukses tidak bisa diubah lagi.
	if rr := bob.do("GET", v1+"/edit", nil); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "success") {
		t.Errorf("edit migration sukses: %d, want 409", rr.Code)
	}
	if rr := bob.post(v1+"/execute", nil); rr.Code != http.StatusConflict {
		t.Errorf("jalankan ulang migration sukses: %d, want 409", rr.Code)
	}

	history := bob.do("GET", e.base, nil).Body.String()
	for _, want := range []string{"buat city", "tambah kolom", "index", "success", "Pelaksana", "bob"} {
		if !strings.Contains(history, want) {
			t.Errorf("riwayat tidak memuat %q", want)
		}
	}
}

func TestMigrationAccessControl(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	v1 := createMigration(t, bob, e.base, "t", "CREATE TABLE t (id INT)")

	// View Only boleh melihat riwayat dan detail, tanpa tombol aksi.
	if rr := carol.do("GET", e.base, nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "Buat migration baru") {
		t.Errorf("riwayat untuk View Only: %d", rr.Code)
	}
	if rr := carol.do("GET", v1, nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "Jalankan migration") {
		t.Errorf("detail untuk View Only: %d", rr.Code)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", e.base + "/new"},
		{"POST", e.base},
		{"GET", v1 + "/edit"},
		{"POST", v1},
		{"POST", v1 + "/execute"},
		{"POST", v1 + "/discard"},
	} {
		var rr *httptest.ResponseRecorder
		if c.method == "POST" {
			rr = carol.post(c.path, url.Values{"title": {"x"}, "sql": {"DROP TABLE t"}})
		} else {
			rr = carol.do("GET", c.path, nil)
		}
		if rr.Code != http.StatusForbidden {
			t.Errorf("View Only %s %s: %d, want 403", c.method, c.path, rr.Code)
		}
	}

	for _, path := range []string{e.base, v1} {
		if rr := dave.do("GET", path, nil); rr.Code != http.StatusNotFound {
			t.Errorf("tanpa akses GET %s: %d, want 404", path, rr.Code)
		}
	}

	// ID migration milik database lain dibalas 404 walaupun bob punya akses Edit di sana.
	foreign := e.other + v1[strings.LastIndex(v1, "/"):]
	if rr := bob.do("GET", foreign, nil); rr.Code != http.StatusNotFound {
		t.Errorf("migration database lain: %d, want 404", rr.Code)
	}
	if rr := bob.post(foreign+"/execute", nil); rr.Code != http.StatusNotFound {
		t.Errorf("eksekusi migration database lain: %d, want 404", rr.Code)
	}

	// Akses diturunkan ke View Only selagi bob masih di halaman review.
	if _, err := e.db.Exec(`UPDATE group_db_access SET level = 'view' WHERE group_id = $1`, e.group); err != nil {
		t.Fatal(err)
	}
	if rr := bob.post(v1+"/execute", nil); rr.Code != http.StatusForbidden {
		t.Errorf("eksekusi setelah akses diturunkan: %d, want 403", rr.Code)
	}
	var exists bool
	if err := e.target.QueryRow(`SELECT to_regclass('t') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("migration tetap jalan padahal akses sudah dicabut")
	}
}

func TestMigrationExplain(t *testing.T) {
	e := newMigrationEnv(t)
	if _, err := e.target.Exec(`CREATE TABLE produk (id INT PRIMARY KEY, nama TEXT);
		INSERT INTO produk SELECT g, 'p' || g FROM generate_series(1, 500) g;`); err != nil {
		t.Fatal(err)
	}
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	v1 := createMigration(t, bob, e.base, "hapus produk", `DELETE FROM produk WHERE id = 7;
UPDATE produk SET nama = '<b>x</b>' WHERE id = 8;`)

	if page := bob.do("GET", v1, nil).Body.String(); !strings.Contains(page, "Analisis query") {
		t.Error("form analisis tidak muncul untuk View + Edit")
	}
	if page := carol.do("GET", v1, nil).Body.String(); strings.Contains(page, "Analisis query") {
		t.Error("form analisis muncul untuk View Only")
	}

	rr := bob.post(v1+"/explain", url.Values{"analyze": {"on"}})
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "Hasil EXPLAIN ANALYZE") {
		t.Fatalf("explain analyze: status %d", rr.Code)
	}
	for _, want := range []string{"Delete", "Update", "produk_pkey", "Waktu aktual", "&lt;b&gt;x"} {
		if !strings.Contains(body, want) {
			t.Errorf("hasil analisis tidak memuat %q", want)
		}
	}
	if strings.Contains(body, "<b>x</b>") {
		t.Error("SQL di hasil analisis tidak di-escape")
	}
	var n int
	if err := e.target.QueryRow(`SELECT count(*) FROM produk WHERE id = 7 OR (id = 8 AND nama = 'p8')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("analisis mengubah data: n=%d err=%v", n, err)
	}

	if rr := bob.post(v1+"/explain", nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "Waktu aktual") {
		t.Errorf("explain tanpa analyze: %d", rr.Code)
	}
	if rr := carol.post(v1+"/explain", url.Values{"analyze": {"on"}}); rr.Code != http.StatusForbidden {
		t.Errorf("View Only explain: %d, want 403", rr.Code)
	}
}
