// Package migration mengelola pembuatan, eksekusi, dan riwayat migration
// tiap database target.
package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// Nilainya harus sama dengan CHECK constraint migrations.status.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSuccess   Status = "success" // tidak bisa diubah lagi
	StatusFailed    Status = "failed"  // sudah di-rollback, boleh diedit lalu dijalankan ulang
	StatusDiscarded Status = "discarded"
)

func (s Status) Editable() bool    { return s == StatusPending || s == StatusFailed }
func (s Status) Executable() bool  { return s == StatusPending || s == StatusFailed }
func (s Status) Discardable() bool { return s == StatusPending || s == StatusFailed }

type Migration struct {
	ID           int64
	DBID         int64
	Version      int
	Title        string
	SQL          string
	Status       Status
	CreatedBy    string // username
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExecutedBy   string
	ExecutedAt   *time.Time
	FinishedAt   *time.Time
	ErrorMessage string
}

// Run adalah satu percobaan eksekusi.
type Run struct {
	ID           int64
	MigrationID  int64
	SQL          string
	ExecutedBy   string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Status       Status
	ErrorMessage string
}

// NextExecutable mengembalikan versi terkecil yang pending atau failed,
// supaya riwayat tetap linear. Kalau ada yang sedang running, tidak ada
// yang boleh jalan. list tidak harus terurut.
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

// CheckExecutable sebaiknya dipanggil di dalam transaction setelah
// SELECT ... FOR UPDATE, supaya hasilnya tidak basi.
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

// Setiap query harus memakai WHERE id = $1 AND db_id = $2. Kalau tidak,
// user dengan akses edit di satu DB bisa menjalankan migration DB lain
// cukup dengan mengganti ID di URL.
//
// Execute sebaiknya memanggil authz.Authorize sekali lagi, karena akses
// bisa dicabut selagi user masih di halaman review.
type Service interface {
	List(ctx context.Context, dbID int64) ([]Migration, error)
	Get(ctx context.Context, dbID, id int64) (*Migration, error)
	Runs(ctx context.Context, dbID, id int64) ([]Run, error)

	// Create menghitung versi baru (max+1) di dalam transaction yang
	// mengunci baris db_connections.
	Create(ctx context.Context, u *domain.User, dbID int64, title, sql string) (*Migration, error)

	// Update memakai WHERE status IN ('pending','failed') dan mengecek
	// RowsAffected supaya tidak balapan dengan Execute.
	Update(ctx context.Context, u *domain.User, dbID, id int64, title, sql string) error

	Discard(ctx context.Context, u *domain.User, dbID, id int64) error

	// Execute:
	//  1. Di DB platform: kunci migration dbID, CheckExecutable, set
	//     status running, tambah baris migration_runs, commit.
	//  2. Di DB target: BEGIN, set lock_timeout dan statement_timeout,
	//     pg_advisory_xact_lock(dbID), Exec(sql) tanpa argumen, lalu
	//     COMMIT atau ROLLBACK.
	//  3. Simpan hasil (success/failed, finished_at, error_message).
	// Saat start, migration yang masih running karena server crash diubah
	// jadi failed.
	Execute(ctx context.Context, u *domain.User, dbID, id int64) (*Run, error)

	// Explain menampilkan execution plan SQL migration tanpa mengubah
	// database target. Dengan analyze, statement benar-benar dijalankan
	// lalu di-rollback.
	Explain(ctx context.Context, u *domain.User, dbID, id int64, analyze bool) (*Analysis, error)
}
