package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// Analisis query (bonus M1) lewat API SPA: berhasil untuk View + Edit,
// ditolak untuk View Only, tanpa akses, dan belum login.
func TestAPIExplainMigration(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dave := loginAs(t, e.h, "dave", testutil.TestPassword)
	anon := &browser{t: t, h: e.h}
	dbID := strings.Split(e.base, "/")[2]

	loc := createMigration(t, bob, e.base, "isi", "CREATE TABLE x (id INT); INSERT INTO x VALUES (1); SELECT * FROM x WHERE id = 1")
	migID := loc[strings.LastIndex(loc, "/")+1:]
	path := "/api/databases/" + dbID + "/migrations/" + migID + "/explain"

	rr := bob.api(http.MethodPost, path, map[string]any{"analyze": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("explain: status %d (%s)", rr.Code, rr.Body.String())
	}
	got := decodeBody[struct {
		Analyze    bool `json:"analyze"`
		Statements []struct {
			SQL         string `json:"sql"`
			Explainable bool   `json:"explainable"`
			Nodes       []struct {
				Operation  string  `json:"operation"`
				HasActual  bool    `json:"has_actual"`
				ActualRows float64 `json:"actual_rows"`
			} `json:"nodes"`
		} `json:"statements"`
	}](t, rr)
	if !got.Analyze || len(got.Statements) != 3 || got.Statements[0].Explainable || !got.Statements[2].Explainable {
		t.Fatalf("hasil explain salah: %+v", got)
	}
	if n := got.Statements[2].Nodes; len(n) == 0 || !n[0].HasActual {
		t.Fatalf("ANALYZE harus berisi angka aktual: %+v", n)
	}
	// Simulasi tidak mengubah database target.
	if rr := bob.api(http.MethodGet, "/api/databases/"+dbID+"/tables/public/x", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("tabel simulasi tidak boleh tersisa: status %d", rr.Code)
	}

	if rr := carol.api(http.MethodPost, path, map[string]any{}); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only: status %d, want 403", rr.Code)
	}
	if rr := dave.api(http.MethodPost, path, map[string]any{}); rr.Code != http.StatusNotFound {
		t.Fatalf("tanpa akses: status %d, want 404", rr.Code)
	}
	if rr := anon.api(http.MethodPost, path, map[string]any{}); rr.Code != http.StatusUnauthorized {
		t.Fatalf("belum login: status %d, want 401", rr.Code)
	}
}

// Schema diff (bonus M1) lewat API SPA.
func TestAPISchemaDiff(t *testing.T) {
	e := newMigrationEnv(t)
	bob := loginAs(t, e.h, "bob", testutil.TestPassword)
	carol := loginAs(t, e.h, "carol", testutil.TestPassword)
	dbID := strings.Split(e.base, "/")[2]
	path := "/api/databases/" + dbID + "/schema-diff"

	rr := bob.api(http.MethodGet, path, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("schema saat ini: status %d (%s)", rr.Code, rr.Body.String())
	}
	cur := decodeBody[struct {
		Schemas []string `json:"schemas"`
		Schema  string   `json:"schema"`
		DDL     string   `json:"ddl"`
	}](t, rr)
	if cur.Schema == "" || len(cur.Schemas) == 0 {
		t.Fatalf("schema awal kosong: %+v", cur)
	}

	rr = bob.api(http.MethodPost, path, map[string]any{
		"schema":  cur.Schema,
		"desired": cur.DDL + "\nCREATE TABLE baru (id INT PRIMARY KEY, nama TEXT);",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("diff: status %d (%s)", rr.Code, rr.Body.String())
	}
	d := decodeBody[struct {
		Changes []struct {
			Action string `json:"action"`
			Object string `json:"object"`
		} `json:"changes"`
		SQL string `json:"sql"`
	}](t, rr)
	if len(d.Changes) == 0 || !strings.Contains(d.SQL, "CREATE TABLE") || !strings.Contains(d.SQL, "baru") {
		t.Fatalf("diff harus mengusulkan tabel baru: %+v", d)
	}

	if rr := bob.api(http.MethodPost, path, map[string]any{"schema": cur.Schema, "desired": "CREATE TABLE ("}); rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("DDL tidak valid: status %d", rr.Code)
	}
	if rr := carol.api(http.MethodGet, path, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("View Only: status %d, want 403", rr.Code)
	}
}
