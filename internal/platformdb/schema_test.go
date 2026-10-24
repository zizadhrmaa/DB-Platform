package platformdb_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/platformdb"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestMigrateIsIdempotent(t *testing.T) {
	db := testutil.PlatformDB(t)
	if err := platformdb.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate kedua kali gagal: %v", err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "alice", false)
	dbID := testutil.MustDBConn(t, db, "toko")
	g := testutil.MustGroup(t, db, "dev")

	mustFail := func(name, q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err == nil {
			t.Errorf("%s: seharusnya ditolak database", name)
		}
	}
	mustFail("username duplikat beda huruf",
		`INSERT INTO users (username, password_hash) VALUES ('ALICE', 'x')`)
	mustFail("username dengan spasi",
		`INSERT INTO users (username, password_hash) VALUES ('a b', 'x')`)
	mustFail("level akses tidak dikenal",
		`INSERT INTO group_db_access (group_id, db_id, level) VALUES ($1, $2, 'admin')`, g, dbID)
	mustFail("group nama duplikat",
		`INSERT INTO groups (name) VALUES ('DEV')`)
	mustFail("status migration tidak dikenal",
		`INSERT INTO migrations (db_id, version, title, sql_text, status) VALUES ($1, 1, 't', 'select 1', 'done')`, dbID)

	if _, err := db.Exec(`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 't', 'select 1')`, dbID); err != nil {
		t.Fatal(err)
	}
	mustFail("versi migration duplikat per DB",
		`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 't2', 'select 2')`, dbID)

	other := testutil.MustDBConn(t, db, "kampus")
	if _, err := db.Exec(`INSERT INTO migrations (db_id, version, title, sql_text) VALUES ($1, 1, 't', 'select 1')`, other); err != nil {
		t.Errorf("versi 1 di DB lain seharusnya boleh: %v", err)
	}

	testutil.MustGrant(t, db, g, dbID, "view")
	if _, err := db.Exec(`DELETE FROM db_connections WHERE id = $1`, dbID); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM group_db_access WHERE db_id = $1`, dbID).Scan(&n)
	if n != 0 {
		t.Error("grant tidak ikut terhapus")
	}
}

func TestBootstrapAdmin(t *testing.T) {
	db := testutil.PlatformDB(t)
	ctx := context.Background()
	created, err := platformdb.EnsureBootstrapAdmin(ctx, db, "admin", testutil.PasswordHash(t))
	if err != nil || !created {
		t.Fatalf("admin pertama: created=%v err=%v", created, err)
	}
	created, err = platformdb.EnsureBootstrapAdmin(ctx, db, "admin2", testutil.PasswordHash(t))
	if err != nil || created {
		t.Fatalf("admin sudah ada, tidak boleh membuat lagi: created=%v err=%v", created, err)
	}
}

func TestBootstrapAdminDoesNotPromoteExistingUser(t *testing.T) {
	db := testutil.PlatformDB(t)
	testutil.MustUser(t, db, "admin", false)
	_, err := platformdb.EnsureBootstrapAdmin(context.Background(), db, "admin", testutil.PasswordHash(t))
	if err == nil || !strings.Contains(err.Error(), "non-admin") {
		t.Fatalf("seharusnya menolak mempromosikan user biasa, err=%v", err)
	}
}