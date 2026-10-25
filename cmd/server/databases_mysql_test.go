package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func mysqlForm(name, password, host string, port int, dbName, user string) url.Values {
	return url.Values{
		"name":      {name},
		"db_type":   {"mysql"},
		"host":      {host},
		"port":      {fmt.Sprint(port)},
		"database":  {dbName},
		"username":  {user},
		"password":  {password},
		"sslmode":   {"disable"},
		"is_active": {"on"},
	}
}

func TestAdminManagesMySQLConnection(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "root", true)
	bobUser := testutil.MustUser(t, db, "bob", false)
	testutil.MustUser(t, db, "carol", false)

	h := newHandler(t, db)
	root := loginAs(t, h, "root", testutil.TestPassword)

	form := mysqlForm("kampus-mysql", "rahasia-mysql", "127.0.0.1", 3306, "kampus", "platform_app")
	rr := root.post("/admin/databases", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat koneksi mysql: status %d", rr.Code)
	}
	id := locationID(t, rr)
	connID, _ := strconv.ParseInt(id, 10, 64)

	listPage := root.do("GET", "/admin/databases", nil)
	if listPage.Code != http.StatusOK {
		t.Fatalf("GET /admin/databases: %d", listPage.Code)
	}
	if !strings.Contains(listPage.Body.String(), "kampus-mysql") || !strings.Contains(listPage.Body.String(), "mysql") {
		t.Errorf("daftar koneksi tidak memuat kampus-mysql atau tipe mysql")
	}

	detailPath := "/admin/databases/" + id
	detailPage := root.do("GET", detailPath, nil)
	if detailPage.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", detailPath, detailPage.Code)
	}
	if !strings.Contains(detailPage.Body.String(), "mysql") || strings.Contains(detailPage.Body.String(), "rahasia-mysql") {
		t.Error("halaman edit tidak memuat db_type atau membocorkan password")
	}

	editForm := mysqlForm("kampus-mysql-edited", "", "127.0.0.1", 3306, "kampus", "platform_app")
	if rr := root.post(detailPath, editForm); rr.Code != http.StatusSeeOther {
		t.Fatalf("edit koneksi mysql: status %d", rr.Code)
	}

	gid := testutil.MustGroup(t, db, "mysql-viewers")
	testutil.MustAddMember(t, db, gid, bobUser.ID)
	testutil.MustGrant(t, db, gid, connID, "view")

	bob := loginAs(t, h, "bob", testutil.TestPassword)
	carol := loginAs(t, h, "carol", testutil.TestPassword)

	bobList := bob.do("GET", "/databases", nil)
	if bobList.Code != http.StatusOK || !strings.Contains(bobList.Body.String(), "kampus-mysql-edited") {
		t.Errorf("bob tidak melihat kampus-mysql-edited di daftar db: status %d", bobList.Code)
	}

	if rr := carol.do("GET", "/databases/"+id, nil); rr.Code != http.StatusNotFound {
		t.Errorf("carol tanpa akses GET /databases/%s: status %d, want 404", id, rr.Code)
	}

	off := mysqlForm("kampus-mysql-edited", "", "127.0.0.1", 3306, "kampus", "platform_app")
	off.Del("is_active")
	if rr := root.post(detailPath, off); rr.Code != http.StatusSeeOther {
		t.Fatalf("nonaktifkan koneksi mysql: status %d", rr.Code)
	}
	if rr := bob.do("GET", "/databases/"+id, nil); rr.Code != http.StatusNotFound {
		t.Errorf("koneksi nonaktif masih bisa dibuka: status %d, want 404", rr.Code)
	}

	if rr := root.post(detailPath+"/delete", url.Values{"confirm": {"on"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("hapus koneksi mysql: status %d", rr.Code)
	}
	if rr := root.do("GET", detailPath, nil); rr.Code != http.StatusNotFound {
		t.Errorf("koneksi terhapus masih ditemukan: status %d, want 404", rr.Code)
	}
}

func TestLiveMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DATABASE_URL kosong, lewati live integration test")
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse mysql dsn: %v", err)
	}

	targetDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer targetDB.Close()

	if err := targetDB.Ping(); err != nil {
		t.Fatalf("ping mysql: %v", err)
	}

	initSQL := `
		CREATE TABLE IF NOT EXISTS test_mahasiswa (
			nim CHAR(8) PRIMARY KEY,
			nama VARCHAR(255) NOT NULL,
			angkatan SMALLINT NOT NULL
		);
		INSERT IGNORE INTO test_mahasiswa VALUES ('13524001', 'Test Mahasiswa', 2023);
	`
	if _, err := targetDB.ExecContext(context.Background(), initSQL); err != nil {
		t.Fatalf("init test tables: %v", err)
	}

	platformDB := testutil.PlatformDB(t)
	testutil.MustUser(t, platformDB, "root", true)
	bobUser := testutil.MustUser(t, platformDB, "bob", false)

	h := newHandler(t, platformDB)
	root := loginAs(t, h, "root", testutil.TestPassword)

	port := 3306
	host := cfg.Addr
	if strings.Contains(cfg.Addr, ":") {
		parts := strings.Split(cfg.Addr, ":")
		host = parts[0]
		port, _ = strconv.Atoi(parts[1])
	}

	form := mysqlForm("kampus-live-mysql", cfg.Passwd, host, port, cfg.DBName, cfg.User)
	rr := root.post("/admin/databases", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("buat koneksi: status %d", rr.Code)
	}
	dbIDStr := locationID(t, rr)
	dbID, _ := strconv.ParseInt(dbIDStr, 10, 64)

	gid := testutil.MustGroup(t, platformDB, "editors")
	testutil.MustAddMember(t, platformDB, gid, bobUser.ID)
	testutil.MustGrant(t, platformDB, gid, dbID, "edit")

	bob := loginAs(t, h, "bob", testutil.TestPassword)

	// Viewer show tables
	showPage := bob.do("GET", "/databases/"+dbIDStr, nil)
	if showPage.Code != http.StatusOK {
		t.Fatalf("buka viewer mysql: status %d", showPage.Code)
	}
	if !strings.Contains(showPage.Body.String(), "test_mahasiswa") {
		t.Errorf("tabel test_mahasiswa tidak ditemukan di viewer")
	}

	// Viewer table data
	dataPath := fmt.Sprintf("/databases/%s/tables/%s/test_mahasiswa/data", dbIDStr, cfg.DBName)
	dataPage := bob.do("GET", dataPath, nil)
	if dataPage.Code != http.StatusOK {
		t.Fatalf("buka data tabel: status %d", dataPage.Code)
	}
	if !strings.Contains(dataPage.Body.String(), "13524001") {
		t.Errorf("data baris tidak ditemukan pada response: %s", dataPage.Body.String())
	}

	// Migration: create and execute migration on MySQL
	migForm := url.Values{
		"title":    {"Tambah tabel nilai"},
		"sql_text": {"CREATE TABLE IF NOT EXISTS test_nilai (id INT PRIMARY KEY, score INT);"},
	}
	migCreate := bob.post(fmt.Sprintf("/databases/%s/migrations", dbIDStr), migForm)
	if migCreate.Code != http.StatusSeeOther {
		t.Fatalf("buat migration: status %d", migCreate.Code)
	}
	migID := locationID(t, migCreate)

	execPath := fmt.Sprintf("/databases/%s/migrations/%s/execute", dbIDStr, migID)
	execRes := bob.post(execPath, nil)
	if execRes.Code != http.StatusSeeOther {
		t.Fatalf("eksekusi migration mysql: status %d", execRes.Code)
	}
}
