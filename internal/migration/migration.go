// Package migration: pembuatan, review, eksekusi, dan riwayat migration
// per database target (Owner: Orang 4).
//
// File ini berisi KONTRAK + aturan murni (tanpa I/O) yang sudah di-test.
// Implementasi Service (query ke tabel migrations/migration_runs dan
// eksekusi ke database target) dikerjakan Orang 4.
package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// Status sesuai CHECK constraint kolom migrations.status.
type Status string

const (
	StatusPending   Status = "pending"   // draft: boleh diedit, belum pernah jalan
	StatusRunning   Status = "running"   // sedang dieksekusi
	StatusSuccess   Status = "success"   // sudah commit -> IMMUTABLE
	StatusFailed    Status = "failed"    // gagal & di-rollback -> boleh diedit & dijalankan ulang
	StatusDiscarded Status = "discarded" // dibatalkan, dilewati dalam urutan, tetap tercatat
)

// Editable: SQL/judul boleh diubah.
func (s Status) Editable() bool { return s == StatusPending || s == StatusFailed }

// Executable: boleh dieksekusi (selama juga merupakan giliran berikutnya).
func (s Status) Executable() bool { return s == StatusPending || s == StatusFailed }

// Discardable: boleh dibatalkan.
func (s Status) Discardable() bool { return s == StatusPending || s == StatusFailed }

// Migration adalah satu baris tabel migrations.
type Migration struct {
	ID           int64
	DBID         int64
	Version      int
	Title        string
	SQL          string
	Status       Status
	CreatedBy    string // username (hasil JOIN), untuk tampilan
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExecutedBy   string     // pelaksana terakhir
	ExecutedAt   *time.Time // waktu eksekusi terakhir
	FinishedAt   *time.Time
	ErrorMessage string
}

// Run adalah satu percobaan eksekusi (tabel migration_runs).
type Run struct {
	ID           int64
	MigrationID  int64
	SQL          string
	ExecutedBy   string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Status       Status // running / success / failed
	ErrorMessage string
}

// NextExecutable menentukan migration mana yang BOLEH dieksekusi sekarang
// agar riwayat tetap LINEAR: versi terkecil yang berstatus pending/failed.
// success & discarded dilewati. Jika ada yang running -> tidak ada.
// list boleh tidak terurut.
func NextExecutable(list []Migration) (Migration, bool) {
	var next *Migration
	for i := range list {
		m := &list[i]
		if m.Status == StatusRunning {
			return Migration{}, false
		}
		if m.Status.Executable() && (next == nil || m.Version < next.Version) {
			next = m
		}
	}
	if next == nil {
		return Migration{}, false
	}
	return *next, true
}

// CheckExecutable mengembalikan nil jika migration id boleh dieksekusi
// sekarang, atau ErrConflict / ErrNotFound beserta alasannya.
// Panggil di DALAM transaction setelah mengunci baris (FOR UPDATE) agar
// keputusan tidak basi.
func CheckExecutable(list []Migration, id int64) error {
	var target *Migration
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
		}
	}
	if target == nil {
		return fmt.Errorf("%w: migration tidak ada di database ini", domain.ErrNotFound)
	}
	if !target.Status.Executable() {
		return fmt.Errorf("%w: migration berstatus %s tidak bisa dieksekusi", domain.ErrConflict, target.Status)
	}
	next, ok := NextExecutable(list)
	if !ok {
		return fmt.Errorf("%w: ada migration lain yang sedang berjalan", domain.ErrConflict)
	}
	if next.ID != id {
		return fmt.Errorf("%w: jalankan dulu versi %d sebelum versi %d", domain.ErrConflict, next.Version, target.Version)
	}
	return nil
}

// Service adalah kontrak yang diimplementasikan Orang 4.
//
// Semua method menerima dbID dari path DAN memastikan migration memang
// milik dbID tersebut (WHERE id=$1 AND db_id=$2). Tanpa ini, user dengan
// akses Edit di DB A bisa mengeksekusi migration milik DB B hanya dengan
// mengganti ID di URL (BOLA, materi REST API Design).
//
// Hak akses (View vs Edit) sudah dicek authz.RequireDB di router, tetapi
// Execute sebaiknya memanggil authz.Authorize sekali lagi tepat sebelum
// eksekusi (akses bisa dicabut saat user sedang di halaman review).
type Service interface {
	List(ctx context.Context, dbID int64) ([]Migration, error)
	Get(ctx context.Context, dbID, id int64) (*Migration, error)
	Runs(ctx context.Context, dbID, id int64) ([]Run, error)

	// Create: versi = max(version)+1 untuk dbID, dihitung di dalam tx
	// yang mengunci baris db_connections (SELECT ... FOR UPDATE).
	Create(ctx context.Context, u *domain.User, dbID int64, title, sql string) (*Migration, error)

	// Update: hanya jika Status.Editable(). Gunakan
	// "UPDATE ... WHERE id=$ AND db_id=$ AND status IN ('pending','failed')"
	// lalu cek RowsAffected untuk mencegah race dengan Execute.
	Update(ctx context.Context, u *domain.User, dbID, id int64, title, sql string) error

	Discard(ctx context.Context, u *domain.User, dbID, id int64) error

	// Execute — alur yang disarankan:
	//  1. Platform DB tx: SELECT ... FOR UPDATE semua migration dbID,
	//     CheckExecutable, set status='running', executed_by/at, insert
	//     migration_runs(status='running'), COMMIT.
	//  2. Target DB: BEGIN; SET LOCAL lock_timeout='5s';
	//     SET LOCAL statement_timeout='60s';
	//     SELECT pg_advisory_xact_lock(<konstanta>, dbID)  -- cegah eksekusi ganda
	//     Exec(sql) TANPA argumen (simple protocol, multi-statement OK);
	//     COMMIT jika sukses, ROLLBACK jika error.
	//  3. Platform DB: set status success/failed + finished_at +
	//     error_message (pesan PG, tanpa kredensial) di migrations & runs.
	//  Startup: migration yang tertinggal 'running' (server crash) ditandai
	//  'failed' dengan pesan "eksekusi terputus" (tx target otomatis
	//  rollback saat koneksi putus).
	Execute(ctx context.Context, u *domain.User, dbID, id int64) (*Run, error)
}
