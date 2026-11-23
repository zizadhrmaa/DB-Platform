// Package migration mengelola pembuatan, eksekusi, dan riwayat migration
// tiap database target.
package migration

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// Nilainya harus sama dengan CHECK constraint migrations.status.
type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusSuccess    Status = "success"
	StatusFailed     Status = "failed" // sudah di-rollback, boleh diedit lalu dijalankan ulang
	StatusDiscarded  Status = "discarded"
	StatusRolledBack Status = "rolled_back" // perubahan dibatalkan rollback; bisa di-forward
)

func (s Status) Editable() bool   { return s == StatusPending || s == StatusFailed }
func (s Status) Executable() bool { return s == StatusPending || s == StatusFailed }

// Discardable: migration yang sudah di-rollback juga boleh dibatalkan supaya
// migration berikutnya bisa jalan. Riwayat eksekusinya tetap tersimpan.
func (s Status) Discardable() bool {
	return s == StatusPending || s == StatusFailed || s == StatusRolledBack
}

// Kind adalah jenis eksekusi. Nilainya sama dengan queue.Kind dan CHECK
// constraint migration_runs.kind.
type Kind string

const (
	KindUp       Kind = "up"       // menjalankan SQL up pertama kali
	KindRollback Kind = "rollback" // menjalankan SQL down untuk membatalkan eksekusi yang committed
	KindForward  Kind = "forward"  // menjalankan SQL up lagi setelah rollback
)

func (k Kind) Valid() bool { return k == KindUp || k == KindRollback || k == KindForward }

// migrationStatusAfter menentukan status migration setelah attempt selesai.
// Attempt yang gagal tidak mengubah posisi migration aktif: rollback gagal
// tetap success, forward gagal tetap rolled_back.
func migrationStatusAfter(kind Kind, result Status) Status {
	switch {
	case kind == KindRollback && result == StatusSuccess:
		return StatusRolledBack
	case kind == KindRollback:
		return StatusSuccess
	case kind == KindForward && result != StatusSuccess:
		return StatusRolledBack
	}
	return result
}

type Migration struct {
	ID                 int64
	DBID               int64
	Version            int
	Revision           int
	Title              string
	SQL                string // SQL up
	SQLDown            string
	Irreversible       bool
	IrreversibleReason string
	ExecMode           ExecMode
	BatchTable         string
	BatchColumn        string
	BatchSize          int
	Status             Status
	CreatedBy          string // username
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ExecutedBy         string
	ExecutedAt         *time.Time
	FinishedAt         *time.Time
	ErrorMessage       string
}

// Run adalah satu attempt eksekusi. ID-nya adalah execution ID.
type Run struct {
	ID           int64
	MigrationID  int64
	Kind         Kind
	Revision     int
	RevertsRunID *int64 // rollback: eksekusi yang dibatalkan
	SQL          string // salinan SQL yang benar-benar dijalankan
	ExecutedBy   string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Status       Status
	ErrorMessage string
}

// ExecMode adalah cara migration dijalankan worker.
type ExecMode string

const (
	ExecNormal ExecMode = "normal" // seluruh SQL dalam satu transaction
	ExecBatch  ExecMode = "batch"  // per batch berdasarkan kolom cursor
)

// Draft adalah isi migration yang ditulis user.
type Draft struct {
	Title              string
	SQLUp              string
	SQLDown            string
	Irreversible       bool
	IrreversibleReason string
	// Mode batch: BatchTable ("tabel" atau "schema.tabel"), BatchColumn
	// (kolom integer unik yang tidak berubah), dan BatchSize. SQL up dan
	// down masing-masing satu statement yang memakai $1 (batas bawah,
	// eksklusif) dan $2 (batas atas, inklusif).
	ExecMode    ExecMode
	BatchTable  string
	BatchColumn string
	BatchSize   int
}

func (d *Draft) normalize() {
	d.Title = strings.TrimSpace(d.Title)
	d.IrreversibleReason = strings.TrimSpace(d.IrreversibleReason)
	if strings.TrimSpace(d.SQLDown) == "" {
		d.SQLDown = ""
	}
	if !d.Irreversible {
		d.IrreversibleReason = ""
	}
	if d.ExecMode == "" {
		d.ExecMode = ExecNormal
	}
	d.BatchTable = strings.TrimSpace(d.BatchTable)
	d.BatchColumn = strings.TrimSpace(d.BatchColumn)
	if d.ExecMode == ExecNormal {
		d.BatchTable, d.BatchColumn, d.BatchSize = "", "", 0
	}
}

func (d Draft) validate() error {
	d.normalize()
	if d.Title == "" {
		return fmt.Errorf("%w: judul wajib diisi", domain.ErrInvalidInput)
	}
	if err := ValidateSQL(d.SQLUp); err != nil {
		return fmt.Errorf("SQL up: %w", err)
	}
	if d.Irreversible {
		if err := d.validateBatch(); err != nil {
			return err
		}
		if d.SQLDown != "" {
			return fmt.Errorf("%w: migration irreversible tidak boleh punya SQL down", domain.ErrInvalidInput)
		}
		if d.IrreversibleReason == "" || len(d.IrreversibleReason) > 1000 {
			return fmt.Errorf("%w: alasan irreversible wajib diisi (maksimal 1000 karakter)", domain.ErrInvalidInput)
		}
		return nil
	}
	if d.SQLDown == "" {
		return fmt.Errorf("%w: SQL down wajib diisi, atau tandai migration sebagai irreversible beserta alasannya", domain.ErrInvalidInput)
	}
	if err := ValidateSQL(d.SQLDown); err != nil {
		return fmt.Errorf("SQL down: %w", err)
	}
	return d.validateBatch()
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (d Draft) validateBatch() error {
	switch d.ExecMode {
	case ExecNormal:
		return nil
	case ExecBatch:
	default:
		return fmt.Errorf("%w: mode eksekusi harus normal atau batch", domain.ErrInvalidInput)
	}
	parts := strings.Split(d.BatchTable, ".")
	if len(parts) > 2 {
		return fmt.Errorf("%w: tabel batch harus berbentuk tabel atau schema.tabel", domain.ErrInvalidInput)
	}
	for _, p := range parts {
		if !identRe.MatchString(p) {
			return fmt.Errorf("%w: nama tabel batch tidak valid", domain.ErrInvalidInput)
		}
	}
	if !identRe.MatchString(d.BatchColumn) {
		return fmt.Errorf("%w: nama kolom batch tidak valid", domain.ErrInvalidInput)
	}
	if d.BatchSize < 1 || d.BatchSize > 1_000_000 {
		return fmt.Errorf("%w: ukuran batch harus 1 sampai 1.000.000", domain.ErrInvalidInput)
	}
	for name, q := range map[string]string{"SQL up": d.SQLUp, "SQL down": d.SQLDown} {
		if q == "" {
			continue // irreversible tidak punya SQL down
		}
		if len(splitStatements(q)) != 1 {
			return fmt.Errorf("%w: %s mode batch harus satu statement", domain.ErrInvalidInput, name)
		}
		if !strings.Contains(q, "$1") || !strings.Contains(q, "$2") {
			return fmt.Errorf("%w: %s mode batch harus memakai $1 (batas bawah) dan $2 (batas atas) rentang batch", domain.ErrInvalidInput, name)
		}
	}
	return nil
}

// NextExecutable mengembalikan versi terkecil yang belum aktif (pending,
// failed, atau rolled_back), supaya riwayat tetap linear. Kalau ada yang
// sedang running, tidak ada yang boleh jalan. list tidak harus terurut.
func NextExecutable(list []Migration) (Migration, bool) {
	var next *Migration
	for i := range list {
		m := &list[i]
		if m.Status == StatusRunning {
			return Migration{}, false
		}
		if (m.Status.Executable() || m.Status == StatusRolledBack) && (next == nil || m.Version < next.Version) {
			next = m
		}
	}
	if next == nil {
		return Migration{}, false
	}
	return *next, true
}

// CheckExecutable sama dengan CheckRunnable untuk eksekusi up.
func CheckExecutable(list []Migration, id int64) error {
	return CheckRunnable(list, id, KindUp)
}

// CheckRunnable memeriksa apakah migration id boleh dieksekusi dengan jenis
// kind. Sebaiknya dipanggil di dalam transaction setelah SELECT ... FOR
// UPDATE, supaya hasilnya tidak basi.
//
//   - up: migration pending/failed yang merupakan versi berikutnya.
//   - forward: migration rolled_back yang merupakan versi berikutnya. Forward
//     tidak menjalankan migration lain.
//   - rollback: migration aktif terakhir (versi tertinggi berstatus success)
//     yang tidak irreversible. Untuk A → B → C, C harus di-rollback dulu
//     sebelum B.
func CheckRunnable(list []Migration, id int64, kind Kind) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: jenis eksekusi tidak dikenal %q", domain.ErrInvalidInput, kind)
	}
	var target, lastActive *Migration
	for i := range list {
		m := &list[i]
		if m.ID == id {
			target = m
		}
		if m.Status == StatusRunning {
			return fmt.Errorf("%w: ada migration lain yang sedang berjalan", domain.ErrConflict)
		}
		if m.Status == StatusSuccess && (lastActive == nil || m.Version > lastActive.Version) {
			lastActive = m
		}
	}
	if target == nil {
		return fmt.Errorf("%w: migration tidak ada di database ini", domain.ErrNotFound)
	}

	if kind != KindUp && target.ExecMode == ExecBatch {
		return errBatchKind
	}
	if kind == KindRollback {
		if target.Status != StatusSuccess {
			return fmt.Errorf("%w: rollback hanya untuk migration yang sudah berhasil diterapkan (status sekarang %s)", domain.ErrConflict, target.Status)
		}
		if target.Irreversible {
			return fmt.Errorf("%w: migration irreversible tidak bisa di-rollback: %s", domain.ErrConflict, target.IrreversibleReason)
		}
		if lastActive.ID != id {
			return fmt.Errorf("%w: rollback hanya untuk migration aktif terakhir; rollback dulu versi %d", domain.ErrConflict, lastActive.Version)
		}
		return nil
	}

	switch {
	case kind == KindUp && target.Status == StatusRolledBack:
		return fmt.Errorf("%w: migration ini sudah di-rollback; gunakan forward untuk menerapkannya lagi", domain.ErrConflict)
	case kind == KindUp && !target.Status.Executable():
		return fmt.Errorf("%w: migration berstatus %s tidak bisa dieksekusi", domain.ErrConflict, target.Status)
	case kind == KindForward && target.Status != StatusRolledBack:
		return fmt.Errorf("%w: forward hanya untuk migration yang sudah di-rollback (status sekarang %s)", domain.ErrConflict, target.Status)
	}
	next, _ := NextExecutable(list)
	if next.ID != id {
		return fmt.Errorf("%w: jalankan dulu versi %d sebelum versi %d", domain.ErrConflict, next.Version, target.Version)
	}
	return nil
}

// errBatchKind: batch committed dibatalkan lewat aksi revert pada job-nya
// (kompensasi per batch), bukan rollback biasa.
var errBatchKind = fmt.Errorf("%w: migration batch tidak memakai rollback atau forward biasa; batalkan batch lewat aksi revert pada job-nya", domain.ErrConflict)

// CheckRequestable memeriksa status satu migration saat request perubahan
// dibuat atau diajukan. Urutan belum diperiksa di tahap ini (request untuk
// versi berikutnya boleh disiapkan lebih dulu); urutan diperiksa lagi saat
// enqueue dan saat eksekusi dengan CheckRunnable.
func CheckRequestable(m Migration, kind Kind) error {
	if kind != KindUp && kind.Valid() && m.ExecMode == ExecBatch {
		return errBatchKind
	}
	switch kind {
	case KindUp:
		if !m.Status.Executable() {
			return fmt.Errorf("%w: migration berstatus %s tidak bisa diajukan untuk up", domain.ErrConflict, m.Status)
		}
	case KindForward:
		if m.Status != StatusRolledBack {
			return fmt.Errorf("%w: forward hanya untuk migration yang sudah di-rollback (status sekarang %s)", domain.ErrConflict, m.Status)
		}
	case KindRollback:
		if m.Status != StatusSuccess {
			return fmt.Errorf("%w: rollback hanya untuk migration yang sudah berhasil diterapkan (status sekarang %s)", domain.ErrConflict, m.Status)
		}
		if m.Irreversible {
			return fmt.Errorf("%w: migration irreversible tidak bisa di-rollback: %s", domain.ErrConflict, m.IrreversibleReason)
		}
	default:
		return fmt.Errorf("%w: jenis eksekusi tidak dikenal %q", domain.ErrInvalidInput, kind)
	}
	return nil
}

// LatestApplied mengembalikan eksekusi up/forward terakhir yang sukses dari
// runs, yaitu eksekusi yang akan dibatalkan rollback berikutnya.
func LatestApplied(runs []Run) (Run, bool) {
	var best *Run
	for i := range runs {
		r := &runs[i]
		if r.Status == StatusSuccess && r.Kind != KindRollback && (best == nil || r.ID > best.ID) {
			best = r
		}
	}
	if best == nil {
		return Run{}, false
	}
	return *best, true
}

// HistoryEntry adalah satu baris riwayat eksekusi dari migration_history
// (tidak bisa diubah), berisi attempt yang berhasil maupun gagal.
type HistoryEntry struct {
	ExecutionID        int64
	MigrationID        int64
	Version            int
	Revision           int
	Title              string
	Kind               Kind
	Status             Status // success (committed) atau failed
	SQL                string
	ExecutedBy         string
	StartedAt          time.Time
	FinishedAt         *time.Time
	RevertsExecutionID *int64
	ErrorMessage       string
}

// HideRolledBack membuang pasangan eksekusi yang dibatalkan dan rollback
// sukses yang membatalkannya. Pasangan ditentukan dari execution ID, bukan
// nama migration. Attempt yang gagal tetap ditampilkan.
func HideRolledBack(entries []HistoryEntry) []HistoryEntry {
	// Hanya eksekusi sukses yang bisa "dibatalkan". Revert batch merujuk
	// attempt batch yang gagal (partial); kegagalan itu dan pembatalannya
	// tetap ditampilkan.
	succeeded := map[int64]bool{}
	for _, e := range entries {
		if e.Status == StatusSuccess {
			succeeded[e.ExecutionID] = true
		}
	}
	hidden := map[int64]bool{}
	for _, e := range entries {
		if e.Kind == KindRollback && e.Status == StatusSuccess && e.RevertsExecutionID != nil && succeeded[*e.RevertsExecutionID] {
			hidden[e.ExecutionID] = true
			hidden[*e.RevertsExecutionID] = true
		}
	}
	out := make([]HistoryEntry, 0, len(entries))
	for _, e := range entries {
		if e.Status == StatusSuccess && hidden[e.ExecutionID] {
			continue
		}
		out = append(out, e)
	}
	return out
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

	// CreateDraft dan UpdateDraft menyimpan SQL up, SQL down, atau tanda
	// irreversible beserta alasannya.
	CreateDraft(ctx context.Context, u *domain.User, dbID int64, d Draft) (*Migration, error)
	UpdateDraft(ctx context.Context, u *domain.User, dbID, id int64, d Draft) error

	// History mengembalikan riwayat eksekusi database, urut waktu mulai.
	History(ctx context.Context, dbID int64, hideRollback bool) ([]HistoryEntry, error)

	Execute(ctx context.Context, u *domain.User, dbID, id int64) (*Run, error)

	// Dengan analyze, statement benar-benar dijalankan lalu di-rollback.
	Explain(ctx context.Context, u *domain.User, dbID, id int64, analyze bool) (*Analysis, error)

	Schemas(ctx context.Context, dbID int64) ([]string, error)
	CurrentSchema(ctx context.Context, u *domain.User, dbID int64, schema string) (string, error)
	Diff(ctx context.Context, u *domain.User, dbID int64, schema, desired string) (*SchemaDiff, error)
}
