package migration

import (
	"errors"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func TestNextExecutableIsLowestPendingOrFailed(t *testing.T) {
	list := []Migration{
		{ID: 3, Version: 3, Status: StatusPending},
		{ID: 1, Version: 1, Status: StatusSuccess},
		{ID: 2, Version: 2, Status: StatusDiscarded},
		{ID: 4, Version: 4, Status: StatusFailed},
	}
	next, ok := NextExecutable(list)
	if !ok || next.ID != 3 {
		t.Fatalf("next = %+v ok=%v, want ID 3", next, ok)
	}
}

func TestNextExecutableBlockedWhileRunning(t *testing.T) {
	list := []Migration{{ID: 1, Version: 1, Status: StatusRunning}, {ID: 2, Version: 2, Status: StatusPending}}
	if _, ok := NextExecutable(list); ok {
		t.Fatal("tidak boleh ada yang executable saat ada running")
	}
}

func TestCheckExecutable(t *testing.T) {
	list := []Migration{
		{ID: 1, Version: 1, Status: StatusSuccess},
		{ID: 2, Version: 2, Status: StatusFailed},
		{ID: 3, Version: 3, Status: StatusPending},
	}
	if err := CheckExecutable(list, 2); err != nil {
		t.Errorf("v2 (failed, terkecil) harus boleh: %v", err)
	}
	if err := CheckExecutable(list, 3); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("v3 melompati v2: err=%v, want Conflict", err)
	}
	if err := CheckExecutable(list, 1); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("v1 success dijalankan ulang: err=%v, want Conflict", err)
	}
	if err := CheckExecutable(list, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("id milik DB lain: err=%v, want NotFound", err)
	}
}

func TestStatusRules(t *testing.T) {
	if StatusSuccess.Editable() || StatusRunning.Editable() || StatusDiscarded.Editable() {
		t.Error("success/running/discarded tidak boleh diedit")
	}
	if !StatusPending.Editable() || !StatusFailed.Editable() {
		t.Error("pending/failed harus bisa diedit")
	}
}

func TestValidateSQL(t *testing.T) {
	ok := []string{
		"CREATE TABLE a (id int);",
		"ALTER TABLE a ADD COLUMN b text; CREATE INDEX a_b_idx ON a (b);",
		"INSERT INTO log(msg) VALUES ('BEGIN; COMMIT;');",                                         // keyword di string
		"-- BEGIN\nCREATE TABLE b (id int);",                                                      // keyword di komentar
		"/* VACUUM /* nested */ */ UPDATE a SET b = 'x';",                                         // komentar bersarang
		"CREATE FUNCTION f() RETURNS void AS $$ BEGIN RAISE NOTICE 'x'; END $$ LANGUAGE plpgsql;", // BEGIN di body fungsi
		`INSERT INTO "BEGIN" VALUES (1);`,
		"create table x (y int)", // tanpa titik koma
	}
	for _, s := range ok {
		if err := ValidateSQL(s); err != nil {
			t.Errorf("SQL valid ditolak: %q -> %v", s, err)
		}
	}
	bad := []string{
		"",
		"   ;  ",
		"-- hanya komentar",
		"BEGIN; CREATE TABLE a (id int); COMMIT;",
		"create table a(id int); commit",
		"START TRANSACTION;",
		"CREATE INDEX CONCURRENTLY i ON a (b);",
		"create unique index concurrently i on a (b);",
		"VACUUM a;",
		"DROP DATABASE prod;",
		"ALTER SYSTEM SET work_mem = '1GB';",
		strings.Repeat("x", MaxSQLBytes+1),
	}
	for _, s := range bad {
		if err := ValidateSQL(s); !errors.Is(err, domain.ErrInvalidInput) {
			short := s
			if len(short) > 40 {
				short = short[:40] + "..."
			}
			t.Errorf("SQL bermasalah tidak ditolak: %q -> %v", short, err)
		}
	}
}
