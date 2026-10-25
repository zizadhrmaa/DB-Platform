package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

func TestStoreReadMigration(t *testing.T) {
	// Database khusus test, terisolasi dari database utama.
	db := testutil.PlatformDB(t)
	ctx := context.Background()

	// Siapkan user dan database target.
	user := testutil.MustUser(t, db, "tester", false)
	dbID := testutil.MustDBConn(t, db, "database_toko")

	// Masukkan contoh migration.
	var migrationID int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO migrations
			(db_id, version, title, sql_text, created_by)
		VALUES ($1, 1, 'Tambah kolom umur',
			'ALTER TABLE users ADD COLUMN umur INT', $2)
		RETURNING id
	`, dbID, user.ID).Scan(&migrationID)
	if err != nil {
		t.Fatal(err)
	}

	store := &Store{DB: db}

	// TEST 1: List
	list, err := store.List(ctx, dbID)
	if err != nil {
		t.Fatalf("List gagal: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Harusnya 1 migration, dapat %d", len(list))
	}
	if list[0].Title != "Tambah kolom umur" {
		t.Fatalf("Judul salah: %s", list[0].Title)
	}

	// TEST 2: Get
	m, err := store.Get(ctx, dbID, migrationID)
	if err != nil {
		t.Fatalf("Get gagal: %v", err)
	}
	if m.Version != 1 || m.Status != StatusPending {
		t.Fatalf("Data migration tidak sesuai: %+v", m)
	}

	// TEST 3: Runs kosong
	runs, err := store.Runs(ctx, dbID, migrationID)
	if err != nil {
		t.Fatalf("Runs gagal: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("Harusnya belum ada eksekusi")
	}

	// Tambahkan contoh riwayat eksekusi.
	_, err = db.ExecContext(ctx, `
		INSERT INTO migration_runs
			(migration_id, sql_text, executed_by, status)
		VALUES ($1, 'SELECT 1', $2, 'failed')
	`, migrationID, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// TEST 4: Runs berisi satu percobaan.
	runs, err = store.Runs(ctx, dbID, migrationID)
	if err != nil {
		t.Fatalf("Runs gagal: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != StatusFailed {
		t.Fatalf("Riwayat eksekusi salah: %+v", runs)
	}

	// TEST 5: Migration milik database lain
	// tidak boleh ditemukan.
	otherDBID := testutil.MustDBConn(t, db, "database_lain")
	_, err = store.Get(ctx, otherDBID, migrationID)
	if err == nil {
		t.Fatal("Migration database lain harus ditolak")
	}
}

func TestStoreCreateMigration(t *testing.T) {
	db := testutil.PlatformDB(t)
	ctx := context.Background()

	user := testutil.MustUser(t, db, "creator", false)
	dbID := testutil.MustDBConn(t, db, "db_create_test")

	store := &Store{DB: db}

	// TEST 1: Create migration pertama.
	m1, err := store.Create(
		ctx, user, dbID,
		"Tambah kolom kota",
		"ALTER TABLE customers ADD COLUMN city TEXT",
	)
	if err != nil {
		t.Fatalf("Create pertama gagal: %v", err)
	}
	if m1.Version != 1 || m1.Status != StatusPending {
		t.Fatalf("Migration pertama salah: %+v", m1)
	}

	// TEST 2: Versi otomatis bertambah.
	m2, err := store.Create(
		ctx, user, dbID,
		"Tambah kolom telepon",
		"ALTER TABLE customers ADD COLUMN phone TEXT",
	)
	if err != nil {
		t.Fatalf("Create kedua gagal: %v", err)
	}
	if m2.Version != 2 {
		t.Fatalf("Harusnya versi 2, dapat %d", m2.Version)
	}

	// TEST 3: Database lain mulai dari versi 1.
	otherDB := testutil.MustDBConn(t, db, "db_lain_create")
	m3, err := store.Create(
		ctx, user, otherDB,
		"Buat tabel produk",
		"CREATE TABLE products (id INT)",
	)
	if err != nil {
		t.Fatalf("Create database lain gagal: %v", err)
	}
	if m3.Version != 1 {
		t.Fatalf("Database lain harus mulai versi 1")
	}

	// TEST 4: SQL kosong harus ditolak.
	_, err = store.Create(ctx, user, dbID, "SQL kosong", "")
	if err == nil {
		t.Fatal("SQL kosong seharusnya ditolak")
	}

	// TEST 5: Judul kosong harus ditolak.
	_, err = store.Create(ctx, user, dbID, "", "SELECT 1")
	if err == nil {
		t.Fatal("Judul kosong seharusnya ditolak")
	}

	// TEST 6: Database tidak ada harus ditolak.
	_, err = store.Create(
		ctx, user, 999999,
		"Database tidak ada", "SELECT 1",
	)
	if err == nil {
		t.Fatal("Database tidak ditemukan harus ditolak")
	}

	// TEST 7: Jumlah migration tidak berubah
	// setelah percobaan Create yang gagal.
	list, err := store.List(ctx, dbID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("Harusnya tetap 2 migration, dapat %d", len(list))
	}
}

func TestStoreUpdateAndDiscard(t *testing.T) {
	db := testutil.PlatformDB(t)
	ctx := context.Background()

	user := testutil.MustUser(t, db, "editor", false)
	dbID := testutil.MustDBConn(t, db, "db_update_test")
	store := &Store{DB: db}

	// Buat migration.
	m, err := store.Create(
		ctx, user, dbID,
		"Judul lama",
		"CREATE TABLE demo (id INT)",
	)
	if err != nil {
		t.Fatal(err)
	}

	// TEST 1: Update berhasil.
	err = store.Update(
		ctx, user, dbID, m.ID,
		"Judul baru",
		"CREATE TABLE demo (id BIGINT)",
	)
	if err != nil {
		t.Fatalf("Update gagal: %v", err)
	}

	updated, err := store.Get(ctx, dbID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Judul baru" {
		t.Fatalf("Judul tidak berubah: %s", updated.Title)
	}
	if updated.SQL != "CREATE TABLE demo (id BIGINT)" {
		t.Fatal("SQL tidak berubah")
	}

	// TEST 2: Discard berhasil.
	err = store.Discard(ctx, user, dbID, m.ID)
	if err != nil {
		t.Fatalf("Discard gagal: %v", err)
	}

	discarded, err := store.Get(ctx, dbID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Status != StatusDiscarded {
		t.Fatalf("Status salah: %s", discarded.Status)
	}

	// TEST 3: Migration discarded tidak bisa diedit.
	err = store.Update(
		ctx, user, dbID, m.ID,
		"Edit lagi",
		"SELECT 1",
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Seharusnya ErrConflict, dapat %v", err)
	}

	// TEST 4: Tidak bisa discard dua kali.
	err = store.Discard(ctx, user, dbID, m.ID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Seharusnya ErrConflict, dapat %v", err)
	}

	// TEST 5: Migration database lain tidak bisa diedit.
	otherDB := testutil.MustDBConn(t, db, "db_other_update")
	err = store.Update(
		ctx, user, otherDB, m.ID,
		"Edit ilegal",
		"SELECT 1",
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Seharusnya ErrNotFound, dapat %v", err)
	}
}
