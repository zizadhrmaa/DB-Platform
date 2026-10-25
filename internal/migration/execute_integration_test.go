package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

type executeEnv struct {
	store  *Store
	az     *authz.Static
	target *sql.DB
	dbID   int64
	user   *domain.User
}

func newExecuteEnv(t *testing.T) *executeEnv {
	t.Helper()
	platformDB := testutil.PlatformDB(t)
	targetDB, _ := testutil.TargetDB(t)
	user := testutil.MustUser(t, platformDB, "eksekutor", false)
	dbID := testutil.MustDBConn(t, platformDB, "target")
	az := &authz.Static{Levels: map[authz.Key]domain.AccessLevel{
		{UserID: user.ID, DBID: dbID}: domain.AccessEdit,
	}}
	store := &Store{
		DB:      platformDB,
		Authz:   az,
		Targets: dbmanager.NewStatic(map[int64]*sql.DB{dbID: targetDB}),
	}
	return &executeEnv{store: store, az: az, target: targetDB, dbID: dbID, user: user}
}

func (e *executeEnv) create(t *testing.T, title, sqlText string) *Migration {
	t.Helper()
	m, err := e.store.Create(context.Background(), e.user, e.dbID, title, sqlText)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *executeEnv) tableExists(t *testing.T, name string) bool {
	t.Helper()
	var ok bool
	if err := e.target.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func (e *executeEnv) status(t *testing.T, id int64) *Migration {
	t.Helper()
	m, err := e.store.Get(context.Background(), e.dbID, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExecuteCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)

	// TEST 1: SQL berhasil di-commit.
	ok := env.create(t, "buat kota", `CREATE TABLE kota (id INT PRIMARY KEY); INSERT INTO kota VALUES (1), (2);`)
	run, err := env.store.Execute(ctx, env.user, env.dbID, ok.ID)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi pertama: %+v err=%v", run, err)
	}
	var n int
	if err := env.target.QueryRow(`SELECT count(*) FROM kota`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("data tidak ter-commit: n=%d err=%v", n, err)
	}
	if m := env.status(t, ok.ID); m.ExecutedBy != "eksekutor" || m.ExecutedAt == nil || m.FinishedAt == nil {
		t.Fatalf("pelaksana dan waktu tidak tercatat: %+v", m)
	}

	// TEST 2: SQL gagal, semua statement di-rollback.
	bad := env.create(t, "gagal", `CREATE TABLE sebagian (id INT); INSERT INTO tidak_ada VALUES (1);`)
	run, err = env.store.Execute(ctx, env.user, env.dbID, bad.ID)
	if err != nil || run.Status != StatusFailed || !strings.Contains(run.ErrorMessage, "tidak_ada") {
		t.Fatalf("eksekusi gagal: %+v err=%v", run, err)
	}
	if env.tableExists(t, "sebagian") {
		t.Fatal("statement pertama tidak di-rollback")
	}

	// TEST 3: migration gagal boleh diperbaiki lalu dijalankan ulang.
	if err := env.store.Update(ctx, env.user, env.dbID, bad.ID, "diperbaiki", `CREATE TABLE sebagian (id INT);`); err != nil {
		t.Fatal(err)
	}
	if run, err := env.store.Execute(ctx, env.user, env.dbID, bad.ID); err != nil || run.Status != StatusSuccess {
		t.Fatalf("jalankan ulang: %+v err=%v", run, err)
	}
	if runs, _ := env.store.Runs(ctx, env.dbID, bad.ID); len(runs) != 2 {
		t.Fatalf("riwayat percobaan harus 2, dapat %d", len(runs))
	}

	// TEST 4: migration sukses tidak bisa diubah atau dijalankan lagi.
	if err := env.store.Update(ctx, env.user, env.dbID, ok.ID, "x", "SELECT 1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("edit migration sukses harus ErrConflict, dapat %v", err)
	}
	if _, err := env.store.Execute(ctx, env.user, env.dbID, ok.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("jalankan ulang migration sukses harus ErrConflict, dapat %v", err)
	}
}

func TestExecuteFollowsVersionOrder(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	v1 := env.create(t, "v1", "CREATE TABLE satu (id INT)")
	v2 := env.create(t, "v2", "CREATE TABLE dua (id INT)")

	_, err := env.store.Execute(ctx, env.user, env.dbID, v2.ID)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "versi 1") {
		t.Fatalf("v2 sebelum v1 harus ErrConflict, dapat %v", err)
	}
	if env.tableExists(t, "dua") {
		t.Fatal("v2 terlanjur dijalankan")
	}
	if err := env.store.Discard(ctx, env.user, env.dbID, v1.ID); err != nil {
		t.Fatal(err)
	}
	if run, err := env.store.Execute(ctx, env.user, env.dbID, v2.ID); err != nil || run.Status != StatusSuccess {
		t.Fatalf("v2 setelah v1 dibatalkan: %+v err=%v", run, err)
	}
}

func TestExecuteChecksAccessAgain(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	m := env.create(t, "t", "CREATE TABLE t (id INT)")

	// Akses diturunkan setelah user membuka halaman review.
	env.az.Levels[authz.Key{UserID: env.user.ID, DBID: env.dbID}] = domain.AccessView
	if _, err := env.store.Execute(ctx, env.user, env.dbID, m.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("harus ErrForbidden, dapat %v", err)
	}
	if env.tableExists(t, "t") {
		t.Fatal("migration tetap jalan padahal akses sudah dicabut")
	}
}

// markRunning meniru server yang mati di tengah eksekusi.
func markRunning(t *testing.T, env *executeEnv, id int64) {
	t.Helper()
	if _, err := env.store.DB.Exec(`UPDATE migrations SET status='running', executed_by=$2, executed_at=now() WHERE id=$1`, id, env.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB.Exec(`INSERT INTO migration_runs (migration_id, sql_text, executed_by, status) SELECT id, sql_text, executed_by, 'running' FROM migrations WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverInterruptedMigration(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	lost := env.create(t, "terputus", "CREATE TABLE terputus (id INT)")
	applied := env.create(t, "sudah commit", "CREATE TABLE sudah (id INT)")
	markRunning(t, env, lost.ID)
	markRunning(t, env, applied.ID)

	// Versi 2 sempat commit di target, tapi statusnya belum tersimpan.
	if _, err := env.target.Exec(`CREATE TABLE _dbplatform_history (version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.target.Exec(`INSERT INTO _dbplatform_history (version) VALUES ($1)`, applied.Version); err != nil {
		t.Fatal(err)
	}

	// TEST 1: selama kunci eksekusi dipegang, status tidak disentuh.
	hold, err := env.target.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(`SELECT pg_advisory_xact_lock(3110, $1::integer)`, env.dbID); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if m := env.status(t, lost.ID); m.Status != StatusRunning {
		t.Fatalf("migration yang mungkin masih berjalan ikut diubah: %s", m.Status)
	}
	hold.Rollback()

	// TEST 2: tanpa bukti commit jadi failed, dengan bukti jadi success.
	if err := env.store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if m := env.status(t, lost.ID); m.Status != StatusFailed || !strings.Contains(m.ErrorMessage, "terputus") {
		t.Fatalf("migration terputus: %+v", m)
	}
	if m := env.status(t, applied.ID); m.Status != StatusSuccess {
		t.Fatalf("migration yang sudah commit: %s", m.Status)
	}
	var running int
	if err := env.store.DB.QueryRow(`SELECT count(*) FROM migration_runs WHERE status='running'`).Scan(&running); err != nil || running != 0 {
		t.Fatalf("masih ada %d run berstatus running", running)
	}

	// TEST 3: migration terputus bisa langsung dijalankan ulang.
	if run, err := env.store.Execute(ctx, env.user, env.dbID, lost.ID); err != nil || run.Status != StatusSuccess {
		t.Fatalf("jalankan ulang: %+v err=%v", run, err)
	}
}

func TestExecuteRecoversStuckMigrationFirst(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "macet", "CREATE TABLE macet (id INT)")
	markRunning(t, env, m.ID)

	// Tanpa pemulihan, status running akan memblokir migration ini selamanya.
	run, err := env.store.Execute(context.Background(), env.user, env.dbID, m.ID)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi setelah server mati: %+v err=%v", run, err)
	}
}

func TestExecuteDoesNotLeakSessionState(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	// Satu koneksi saja, supaya kalau koneksi dipakai ulang, state-nya pasti terbawa.
	env.target.SetMaxOpenConns(1)

	set := env.create(t, "ubah search_path", "SET search_path TO pg_temp; SELECT pg_advisory_lock(424242, 1);")
	if run, err := env.store.Execute(ctx, env.user, env.dbID, set.ID); err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi pertama: %+v err=%v", run, err)
	}
	next := env.create(t, "buat tabel", "CREATE TABLE sesudah_set (id INT)")
	if run, err := env.store.Execute(ctx, env.user, env.dbID, next.ID); err != nil || run.Status != StatusSuccess {
		t.Fatalf("eksekusi kedua: %+v err=%v", run, err)
	}
	var schema string
	if err := env.target.QueryRow(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = 'sesudah_set'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(schema, "pg_temp") {
		t.Fatalf("tabel dibuat di %s; search_path dari migration sebelumnya terbawa", schema)
	}
	waitNoAdvisoryLock(t, env.target, 424242)
}

func TestExplainDoesNotLeakAdvisoryLock(t *testing.T) {
	env := newExecuteEnv(t)
	m := env.create(t, "kunci", "SELECT pg_advisory_lock(424243, 1);")
	if _, err := env.store.Explain(context.Background(), env.user, env.dbID, m.ID, true); err != nil {
		t.Fatal(err)
	}
	waitNoAdvisoryLock(t, env.target, 424243)
}

// Koneksi yang ditutup baru melepas kuncinya setelah backend-nya selesai,
// jadi dicek beberapa kali.
func waitNoAdvisoryLock(t *testing.T, db *sql.DB, classID int) {
	t.Helper()
	var n int
	for i := 0; i < 40; i++ {
		if err := db.QueryRow(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = $1`, classID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("advisory lock dari SQL migration masih dipegang (%d)", n)
}
