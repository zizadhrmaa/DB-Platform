// Package migration: pembuatan, review, eksekusi, dan riwayat migration
// per database target (Owner: Orang 4).

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
	StatusPending   Status = "pending"   
	StatusRunning   Status = "running"   // sedang dieksekusi
	StatusSuccess   Status = "success"   // sudah commit -> IMMUTABLE
	StatusFailed    Status = "failed"    // gagal & di-rollback -> boleh diedit & dijalankan ulang
	StatusDiscarded Status = "discarded" // dibatalkan, dilewati dalam urutan, tetap tercatat
)

// SQL/judul boleh diubah.
func (s Status) Editable() bool { return s == StatusPending || s == StatusFailed }

// boleh dieksekusi (selama juga merupakan giliran berikutnya).
func (s Status) Executable() bool { return s == StatusPending || s == StatusFailed }

// boleh dibatalkan.
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

// mengembalikan nil jika migration id boleh dieksekusi
// sekarang, atau ErrConflict / ErrNotFound beserta alasannya.
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


type Service interface {
	List(ctx context.Context, dbID int64) ([]Migration, error)
	Get(ctx context.Context, dbID, id int64) (*Migration, error)
	Runs(ctx context.Context, dbID, id int64) ([]Run, error)

	Create(ctx context.Context, u *domain.User, dbID int64, title, sql string) (*Migration, error)

	
	Update(ctx context.Context, u *domain.User, dbID, id int64, title, sql string) error

	Discard(ctx context.Context, u *domain.User, dbID, id int64) error

	// Execute:
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
