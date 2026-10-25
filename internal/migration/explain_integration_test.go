package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// explainEnv menyiapkan tabel items (dengan index) di database target.
func explainEnv(t *testing.T) *executeEnv {
	t.Helper()
	env := newExecuteEnv(t)
	if _, err := env.target.Exec(`
		CREATE TABLE items (id INT PRIMARY KEY, category INT NOT NULL, name TEXT NOT NULL);
		INSERT INTO items SELECT g, g % 10, 'item ' || g FROM generate_series(1, 2000) g;
		CREATE INDEX items_category_idx ON items (category);
		ANALYZE items;`); err != nil {
		t.Fatal(err)
	}
	return env
}

func (e *executeEnv) itemCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.target.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func findNode(nodes []PlanNode, op string) *PlanNode {
	for i := range nodes {
		if strings.Contains(nodes[i].Operation, op) {
			return &nodes[i]
		}
	}
	return nil
}

const explainSQL = `CREATE TABLE baru (id INT);
INSERT INTO baru VALUES (1);
UPDATE items SET name = 'diubah' WHERE id = 5;
DELETE FROM items WHERE category = 3;
SELECT i.name FROM items i JOIN baru b ON b.id = i.id;`

func TestExplainDoesNotChangeDatabase(t *testing.T) {
	ctx := context.Background()
	env := explainEnv(t)
	m := env.create(t, "analisis", explainSQL)

	for _, analyze := range []bool{false, true} {
		a, err := env.store.Explain(ctx, env.user, env.dbID, m.ID, analyze)
		if err != nil {
			t.Fatalf("analyze=%v: %v", analyze, err)
		}
		if a.Analyze != analyze || len(a.Statements) != 5 {
			t.Fatalf("analyze=%v: %d statement, want 5", analyze, len(a.Statements))
		}
		create, update, del, sel := a.Statements[0], a.Statements[2], a.Statements[3], a.Statements[4]
		if create.Explainable || create.Error != "" {
			t.Errorf("CREATE TABLE seharusnya dijalankan tanpa plan: %+v", create)
		}
		// Statement berikutnya bisa memakai tabel yang dibuat statement sebelumnya.
		if sel.Error != "" || findNode(sel.Nodes, "Join") == nil {
			t.Errorf("SELECT dengan join tabel baru: %+v", sel)
		}
		if n := findNode(update.Nodes, "Update"); n == nil {
			t.Errorf("UPDATE tidak punya node Update: %+v", update.Nodes)
		}
		if idx := findNode(update.Nodes, "Index Scan"); idx == nil || !idx.UsesIndex || !strings.Contains(idx.Target, "items_pkey") {
			t.Errorf("UPDATE WHERE id seharusnya memakai index items_pkey: %+v", update.Nodes)
		}
		if findNode(del.Nodes, "Delete") == nil {
			t.Errorf("DELETE tidak punya node Delete: %+v", del.Nodes)
		}
		top := update.Nodes[0]
		if top.HasActual != analyze || (analyze && update.ExecutionMs <= 0) || update.PlanningMs <= 0 {
			t.Errorf("analyze=%v: data aktual tidak sesuai: %+v planning=%v execution=%v", analyze, top, update.PlanningMs, update.ExecutionMs)
		}
		if analyze {
			if d := findNode(del.Nodes, "Delete"); d == nil || del.Nodes[len(del.Nodes)-1].ActualRows != 200 {
				t.Errorf("DELETE ANALYZE seharusnya memproses 200 row: %+v", del.Nodes)
			}
		}

		// Semua perubahan dibatalkan.
		if env.tableExists(t, "baru") {
			t.Fatalf("analyze=%v: tabel baru tertinggal", analyze)
		}
		if n := env.itemCount(t); n != 2000 {
			t.Fatalf("analyze=%v: isi items berubah menjadi %d", analyze, n)
		}
		var name string
		if err := env.target.QueryRow(`SELECT name FROM items WHERE id = 5`).Scan(&name); err != nil || name != "item 5" {
			t.Fatalf("analyze=%v: UPDATE ikut tersimpan: %q %v", analyze, name, err)
		}
	}
	if got := env.status(t, m.ID); got.Status != StatusPending || got.ExecutedAt != nil {
		t.Fatalf("analisis tidak boleh mengubah status migration: %+v", got)
	}
}

func TestExplainStopsAtFailingStatement(t *testing.T) {
	env := explainEnv(t)
	m := env.create(t, "gagal", `SELECT * FROM items WHERE id = 1; SELECT * FROM tidak_ada; SELECT 2;`)
	a, err := env.store.Explain(context.Background(), env.user, env.dbID, m.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Statements) != 2 || a.Statements[0].Error != "" || !strings.Contains(a.Statements[1].Error, "tidak_ada") {
		t.Fatalf("seharusnya berhenti di statement kedua: %+v", a.Statements)
	}
}

func TestExplainAccessAndStatus(t *testing.T) {
	ctx := context.Background()
	env := explainEnv(t)
	m := env.create(t, "t", "SELECT 1")

	// Migration yang sudah sukses tidak perlu dianalisis lagi.
	if _, err := env.store.Execute(ctx, env.user, env.dbID, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Explain(ctx, env.user, env.dbID, m.ID, false); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("migration sukses: err=%v, want ErrConflict", err)
	}

	pending := env.create(t, "u", "SELECT 2")
	env.az.Levels[authz.Key{UserID: env.user.ID, DBID: env.dbID}] = domain.AccessView
	if _, err := env.store.Explain(ctx, env.user, env.dbID, pending.ID, true); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("View Only: err=%v, want ErrForbidden", err)
	}
}

func TestExplainWaitsForRunningExecution(t *testing.T) {
	env := explainEnv(t)
	m := env.create(t, "t", "SELECT 1")
	hold, err := env.target.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if _, err := hold.Exec(`SELECT pg_advisory_xact_lock(3110, $1::integer)`, env.dbID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Explain(context.Background(), env.user, env.dbID, m.ID, false); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("saat ada eksekusi berjalan: err=%v, want ErrConflict", err)
	}
}
