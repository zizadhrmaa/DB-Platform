package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

const uniquePassword = "rahasia-unik-123"

func connectionForm(name, password string, srv testutil.PGParams) url.Values {
	return url.Values{
		"name":      {name},
		"host":      {srv.Host},
		"port":      {fmt.Sprint(srv.Port)},
		"database":  {srv.Database},
		"username":  {srv.User},
		"password":  {password},
		"sslmode":   {"disable"},
		"is_active": {"on"},
	}
}

var passwordWithValue = regexp.MustCompile(`name="password"[^>]*value=`)

func TestAdminManagesConnections(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	srv := testutil.ServerParams(t)
	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)

	if rr := root.do("GET", "/admin/databases/new", nil); rr.Code != http.StatusOK {
		t.Fatalf("form koneksi baru: %d", rr.Code)
	}
	rr := root.post("/admin/databases", connectionForm("toko", srv.Password, srv))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat koneksi: status %d", rr.Code)
	}
	good := "/admin/databases/" + locationID(t, rr)

	rr = root.post("/admin/databases", connectionForm("salah", uniquePassword, srv))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat koneksi kedua: status %d", rr.Code)
	}
	bad := "/admin/databases/" + locationID(t, rr)

	dup := root.post("/admin/databases", connectionForm("TOKO", "x", srv))
	if dup.Code != http.StatusUnprocessableEntity {
		t.Errorf("nama dobel: status %d, want 422", dup.Code)
	}
	if strings.Contains(dup.Body.String(), `value="x"`) || passwordWithValue.MatchString(dup.Body.String()) {
		t.Error("password diisi ulang setelah validasi gagal")
	}

	for _, path := range []string{"/admin/databases", bad} {
		page := root.do("GET", path, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, page.Code)
		}
		if strings.Contains(page.Body.String(), uniquePassword) || passwordWithValue.MatchString(page.Body.String()) {
			t.Errorf("GET %s: password muncul di halaman", path)
		}
	}

	if rr := root.post(good+"/test", nil); !strings.HasSuffix(rr.Header().Get("Location"), "ok=conn-ok") {
		t.Errorf("uji koneksi benar: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	fail := root.post(bad+"/test", nil)
	if fail.Code != http.StatusOK || !strings.Contains(fail.Body.String(), "Koneksi gagal") {
		t.Errorf("uji koneksi salah: %d", fail.Code)
	}
	if strings.Contains(fail.Body.String(), uniquePassword) {
		t.Error("password muncul di pesan gagal")
	}

	// Edit tanpa password tetap memakai password lama.
	edit := connectionForm("toko", "", srv)
	if rr := root.post(good, edit); rr.Code != http.StatusSeeOther {
		t.Fatalf("edit koneksi: status %d", rr.Code)
	}
	if rr := root.post(good+"/test", nil); !strings.HasSuffix(rr.Header().Get("Location"), "ok=conn-ok") {
		t.Error("password lama hilang setelah edit tanpa password")
	}

	if rr := root.post(bad+"/delete", nil); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("hapus tanpa konfirmasi: %d, want 422", rr.Code)
	}
	if rr := root.post(bad+"/delete", url.Values{"confirm": {"on"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("hapus koneksi: %d", rr.Code)
	}
	if rr := root.do("GET", bad, nil); rr.Code != http.StatusNotFound {
		t.Errorf("koneksi terhapus: %d, want 404", rr.Code)
	}
}

func TestDatabaseViewer(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	bobUser := testutil.MustUser(t, db, "bob", false)
	testutil.MustUser(t, db, "carol", false)
	srv := testutil.ServerParams(t)

	target, schema := testutil.TargetDB(t)
	if _, err := target.Exec(`
		CREATE TABLE items (id INT PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE "Audit Log" (msg TEXT);
		INSERT INTO items VALUES (1, '<script>alert(1)</script>'), (2, 'biasa');
		INSERT INTO "Audit Log" VALUES ('login');`); err != nil {
		t.Fatal(err)
	}

	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)
	rr := root.post("/admin/databases", connectionForm("toko", srv.Password, srv))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat koneksi: %d", rr.Code)
	}
	id := locationID(t, rr)
	rr = root.post("/admin/databases", connectionForm("mati", uniquePassword, srv))
	downID := locationID(t, rr)

	gid := testutil.MustGroup(t, db, "viewers")
	testutil.MustAddMember(t, db, gid, bobUser.ID)
	for _, dbID := range []string{id, downID} {
		n, _ := strconv.ParseInt(dbID, 10, 64)
		testutil.MustGrant(t, db, gid, n, "view")
	}

	bob := loginAs(t, h, "bob", testutil.TestPassword)
	carol := loginAs(t, h, "carol", testutil.TestPassword)
	base := "/databases/" + id
	tablePath := base + "/tables/" + url.PathEscape(schema) + "/items"

	show := bob.do("GET", base, nil)
	if show.Code != http.StatusOK {
		t.Fatalf("daftar tabel: %d", show.Code)
	}
	body := show.Body.String()
	if !strings.Contains(body, "Schema "+schema) || !strings.Contains(body, ">items<") || !strings.Contains(body, "Audit%20Log") {
		t.Error("daftar tabel tidak menampilkan tabel di schema test")
	}

	if page := bob.do("GET", tablePath, nil); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "PRIMARY KEY") {
		t.Errorf("struktur tabel: %d", page.Code)
	}

	data := bob.do("GET", tablePath+"/data?page=1&size=10", nil)
	if data.Code != http.StatusOK {
		t.Fatalf("data tabel: %d", data.Code)
	}
	if strings.Contains(data.Body.String(), "<script>alert") || !strings.Contains(data.Body.String(), "&lt;script&gt;") {
		t.Error("isi tabel tidak di-escape")
	}

	audit := base + "/tables/" + url.PathEscape(schema) + "/" + url.PathEscape("Audit Log") + "/data"
	if page := bob.do("GET", audit, nil); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "login") {
		t.Errorf("tabel bernama aneh: %d", page.Code)
	}

	for _, path := range []string{
		base + "/tables/" + url.PathEscape(schema) + "/tidak_ada",
		base + "/tables/" + url.PathEscape(schema) + "/" + url.PathEscape(`items"; DROP TABLE items; --`),
	} {
		if rr := bob.do("GET", path, nil); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, rr.Code)
		}
	}
	if _, err := target.Exec(`SELECT 1 FROM items`); err != nil {
		t.Fatalf("tabel items hilang: %v", err)
	}

	for _, path := range []string{base, tablePath, tablePath + "/data"} {
		if rr := carol.do("GET", path, nil); rr.Code != http.StatusNotFound {
			t.Errorf("user tanpa akses GET %s: %d, want 404", path, rr.Code)
		}
	}

	down := bob.do("GET", "/databases/"+downID, nil)
	if down.Code != http.StatusServiceUnavailable {
		t.Errorf("koneksi gagal: %d, want 503", down.Code)
	}
	if strings.Contains(down.Body.String(), uniquePassword) || strings.Contains(down.Body.String(), srv.Host+":") {
		t.Error("detail koneksi muncul di halaman error")
	}

	// Koneksi dinonaktifkan admin langsung tertutup untuk semua user.
	off := connectionForm("toko", "", srv)
	off.Del("is_active")
	if rr := root.post("/admin/databases/"+id, off); rr.Code != http.StatusSeeOther {
		t.Fatalf("nonaktifkan koneksi: %d", rr.Code)
	}
	if rr := bob.do("GET", base, nil); rr.Code != http.StatusNotFound {
		t.Errorf("koneksi nonaktif masih bisa dibuka: %d", rr.Code)
	}
}
