package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

type migrationJSON struct {
	ID           int64      `json:"id"`
	Version      int        `json:"version"`
	Title        string     `json:"title"`
	SQL          string     `json:"sql"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	ExecutedAt   *time.Time `json:"executed_at"`
	ErrorMessage string     `json:"error_message"`
}

// Daftar migration dipakai halaman request di SPA untuk memilih migration.
func TestAPIListMigrations(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	anon := &browser{t: t, h: e.h}
	dbID := strings.Split(e.base, "/")[2]
	otherID := strings.Split(e.other, "/")[2]

	createMigration(t, bob, e.base, "satu", "CREATE TABLE satu (id INT)")
	loc := createMigration(t, bob, e.base, "dua", "CREATE TABLE dua (id INT)")
	duaID, _ := strconv.ParseInt(loc[strings.LastIndex(loc, "/")+1:], 10, 64)
	createMigration(t, bob, e.other, "lain", "CREATE TABLE lain (id INT)")

	path := "/api/databases/" + dbID + "/migrations"
	rr := bob.api(http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("daftar migration: status %d (%s)", rr.Code, rr.Body.String())
	}
	list := decodeBody[[]migrationJSON](t, rr)
	if len(list) != 2 {
		t.Fatalf("hanya migration database ini yang dikembalikan, dapat %+v", list)
	}
	if list[0].Version != 1 || list[0].Title != "satu" || list[1].ID != duaID || list[1].Version != 2 {
		t.Fatalf("urutan versi atau isi salah: %+v", list)
	}
	if list[1].Status != "pending" || list[1].SQL != "CREATE TABLE dua (id INT)" || list[1].CreatedBy != "bob" || list[1].CreatedAt.IsZero() {
		t.Fatalf("field migration tidak lengkap: %+v", list[1])
	}

	if rr := carol.api(http.MethodGet, path, nil); rr.Code != http.StatusOK {
		t.Fatalf("View Only boleh melihat daftar migration: status %d", rr.Code)
	}
	if rr := dave.api(http.MethodGet, path, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("tanpa akses: status %d, want 404", rr.Code)
	}
	if rr := anon.api(http.MethodGet, path, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("belum login: status %d, want 401", rr.Code)
	}
	if got := decodeBody[[]migrationJSON](t, bob.api(http.MethodGet, "/api/databases/"+otherID+"/migrations", nil)); len(got) != 1 || got[0].Title != "lain" {
		t.Fatalf("database lain: %+v", got)
	}
}
