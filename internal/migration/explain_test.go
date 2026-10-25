package migration

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"dua statement", "CREATE TABLE a (id int); INSERT INTO a VALUES (1);", []string{"CREATE TABLE a (id int)", "INSERT INTO a VALUES (1)"}},
		{"tanpa titik koma di akhir", "SELECT 1", []string{"SELECT 1"}},
		{"titik koma di dalam string", "INSERT INTO log VALUES ('a;b'); SELECT 2", []string{"INSERT INTO log VALUES ('a;b')", "SELECT 2"}},
		{"titik koma di dalam komentar", "SELECT 1 -- satu; dua\n; SELECT 2", []string{"SELECT 1 -- satu; dua", "SELECT 2"}},
		{"body fungsi", "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql; SELECT f()",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql", "SELECT f()"}},
		{"statement kosong dan komentar saja", ";; -- catatan\n; SELECT 1;", []string{"SELECT 1"}},
	}
	for _, c := range cases {
		if got := splitStatements(c.sql); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBackslashInPlainStringIsNotEscape(t *testing.T) {
	// Di string biasa backslash bukan escape, jadi PostgreSQL membaca ini
	// sebagai dua statement dan COMMIT-nya harus ditolak.
	for _, sql := range []string{
		`SELECT 'C:\'; COMMIT;`,
		`INSERT INTO t VALUES ('a\'); COMMIT; --')`,
	} {
		if err := ValidateSQL(sql); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("COMMIT tersembunyi tidak ditolak: %q (err=%v)", sql, err)
		}
	}
	// Di string E'...' backslash memang escape.
	if err := ValidateSQL(`INSERT INTO t VALUES (E'it\'s; COMMIT')`); err != nil {
		t.Errorf("string E'...' salah dibaca: %v", err)
	}
	if got := splitStatements(`SELECT 'C:\'; SELECT 2`); len(got) != 2 {
		t.Errorf("seharusnya dua statement: %q", got)
	}
}

func TestIsExplainable(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1":                             true,
		"  select * from a":                    true,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
		"INSERT INTO a VALUES (1)":             true,
		"UPDATE a SET b = 1":                   true,
		"DELETE FROM a":                        true,
		"(SELECT 1)":                           true,
		"-- komentar\nSELECT 1":                true,
		"CREATE TABLE a (id int)":              false,
		"ALTER TABLE a ADD COLUMN b int":       false,
		"CREATE INDEX i ON a (b)":              false,
		"SET search_path TO x":                 false,
	} {
		if got := isExplainable(sql); got != want {
			t.Errorf("isExplainable(%q) = %v, want %v", sql, got, want)
		}
	}
}

func TestParsePlan(t *testing.T) {
	raw := []byte(`[{"Plan": {
		"Node Type": "Hash Join", "Join Type": "Inner", "Startup Cost": 1.5, "Total Cost": 30.25, "Plan Rows": 10,
		"Hash Cond": "(o.customer_id = c.id)", "Actual Total Time": 0.42, "Actual Rows": 8, "Actual Loops": 1,
		"Plans": [
			{"Node Type": "Seq Scan", "Relation Name": "orders", "Alias": "o", "Startup Cost": 0, "Total Cost": 20,
			 "Plan Rows": 100, "Filter": "(total > 5)", "Rows Removed by Filter": 92,
			 "Actual Total Time": 0.2, "Actual Rows": 8, "Actual Loops": 1},
			{"Node Type": "Hash", "Startup Cost": 1, "Total Cost": 1, "Plan Rows": 5, "Actual Total Time": 0.01, "Actual Rows": 5, "Actual Loops": 1,
			 "Plans": [{"Node Type": "Index Scan", "Relation Name": "customers", "Alias": "c", "Index Name": "customers_pkey",
			  "Index Cond": "(id < 6)", "Startup Cost": 0.1, "Total Cost": 1, "Plan Rows": 5,
			  "Actual Total Time": 0.01, "Actual Rows": 5, "Actual Loops": 1}]}
		]},
		"Planning Time": 0.3, "Execution Time": 0.6}]`)

	var p StatementPlan
	if err := p.parse(raw); err != nil {
		t.Fatal(err)
	}
	if p.PlanningMs != 0.3 || p.ExecutionMs != 0.6 || len(p.Nodes) != 4 {
		t.Fatalf("ringkasan salah: %+v", p)
	}
	join, seq, idx := p.Nodes[0], p.Nodes[1], p.Nodes[3]
	if join.Operation != "Hash Join" || join.JoinStrategy != "Hash Join" || join.Condition != "(o.customer_id = c.id)" || join.Depth != 0 {
		t.Errorf("node join: %+v", join)
	}
	if !seq.IsSeqScan || seq.Target != "orders o" || seq.RowsRemoved != 92 || seq.Condition != "(total > 5)" || seq.Depth != 1 {
		t.Errorf("node seq scan: %+v", seq)
	}
	if !idx.UsesIndex || idx.Target != "customers c memakai index customers_pkey" || idx.Depth != 2 || !idx.HasActual || idx.ActualRows != 5 {
		t.Errorf("node index scan: %+v", idx)
	}

	modify := StatementPlan{}
	if err := modify.parse([]byte(`[{"Plan": {"Node Type": "ModifyTable", "Operation": "Update", "Relation Name": "a", "Alias": "a",
		"Plans": [{"Node Type": "Seq Scan", "Relation Name": "a"}]}, "Planning Time": 0.1}]`)); err != nil {
		t.Fatal(err)
	}
	if modify.Nodes[0].Operation != "Update" || modify.Nodes[0].HasActual {
		t.Errorf("node modify tanpa ANALYZE: %+v", modify.Nodes[0])
	}

	if err := (&StatementPlan{}).parse([]byte(`bukan json`)); err == nil {
		t.Error("JSON rusak seharusnya error")
	}
}
