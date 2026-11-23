// Package queue menyimpan antrean eksekusi migration di database platform.
//
// Job diambil worker dengan SELECT ... FOR UPDATE SKIP LOCKED. Aturan satu
// job aktif per database dijamin index unik parsial di tabel jobs, jadi
// tetap benar walaupun ada banyak worker atau banyak proses. Pemegang job
// dibuktikan lewat lease (lease_owner, lease_until) dan semua perubahan
// status memakai compare-and-set terhadap pemilik lease.
package queue

import (
	"errors"
	"time"
)

// Nilainya harus sama dengan CHECK constraint jobs.status.
type Status string

const (
	StatusQueued            Status = "queued"
	StatusRunning           Status = "running"
	StatusSucceeded         Status = "succeeded"
	StatusFailed            Status = "failed"
	StatusUncertain         Status = "uncertain"
	StatusPartial           Status = "partial"
	StatusPartiallyReverted Status = "partially_reverted"
	StatusReverted          Status = "reverted" // mode batch: semua batch committed sudah dibatalkan
)

// holdingStatuses harus sama dengan predikat index jobs_one_active_per_db_uq.
const holdingStatuses = `('running','uncertain','partial','partially_reverted')`

// openStatuses harus sama dengan predikat index jobs_one_open_per_migration_uq.
const openStatuses = `('queued','running','uncertain','partial','partially_reverted')`

func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed,
		StatusUncertain, StatusPartial, StatusPartiallyReverted, StatusReverted:
		return true
	}
	return false
}

// Terminal berarti job tidak akan diproses lagi.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusReverted
}

// HoldsDatabase berarti job lain di database yang sama tidak boleh jalan.
// Spesifikasi: tahan migration berikutnya selama hasil belum pasti atau
// migration masih partial.
func (s Status) HoldsDatabase() bool {
	switch s {
	case StatusRunning, StatusUncertain, StatusPartial, StatusPartiallyReverted:
		return true
	}
	return false
}

type Kind string

const (
	KindUp       Kind = "up"
	KindRollback Kind = "rollback"
	KindForward  Kind = "forward"
)

func (k Kind) Valid() bool { return k == KindUp || k == KindRollback || k == KindForward }

type Mode string

const (
	ModeNormal Mode = "normal"
	ModeBatch  Mode = "batch"
)

func (m Mode) Valid() bool { return m == ModeNormal || m == ModeBatch }

// Action menentukan apa yang dikerjakan job batch: menjalankan batch dari
// checkpoint, atau membatalkan batch yang sudah committed.
type Action string

const (
	ActionRun    Action = "run"
	ActionRevert Action = "revert"
)

// Job dikirim ke SPA apa adanya. Field yang hanya berguna di dalam sistem
// (pemilik lease berisi hostname worker, kunci idempotensi, trace context)
// sengaja tidak ikut di JSON.
type Job struct {
	ID               int64      `json:"id"`
	DBID             int64      `json:"db_id"`
	MigrationID      int64      `json:"migration_id"`
	RequestID        *int64     `json:"request_id"`
	Kind             Kind       `json:"kind"`
	Mode             Mode       `json:"mode"`
	Action           Action     `json:"action"`
	Status           Status     `json:"status"`
	IdempotencyKey   string     `json:"-"`
	RequestedBy      *int64     `json:"requested_by"`
	Attempt          int        `json:"attempt"`
	MaxAttempts      int        `json:"max_attempts"`
	NextRunAt        time.Time  `json:"next_run_at"`
	LeaseOwner       string     `json:"-"`
	LeaseUntil       *time.Time `json:"-"`
	RunID            *int64     `json:"execution_id"` // migration_runs.id attempt terakhir
	TraceContext     string     `json:"-"`
	BatchesCommitted int        `json:"batches_committed"`
	RowsCommitted    int64      `json:"rows_committed"`
	LastCursor       string     `json:"last_cursor"`
	ErrorMessage     string     `json:"error_message"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// RetryPolicy mengatur retry untuk kegagalan sementara (target tidak
// terjangkau, lock target dipegang, worker mati).
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy: maksimal 5 attempt, jeda min(2s × 2^(attempt−1), 60s).
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 60 * time.Second}
}

// Delay mengembalikan jeda sebelum attempt berikutnya, setelah attempt
// ke-attempt gagal.
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	d := p.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if p.MaxDelay > 0 && d >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		return p.MaxDelay
	}
	return d
}

// ErrLeaseLost berarti job tidak lagi dipegang worker ini: lease sudah
// lewat dan diambil alih pemulihan, atau status job sudah berubah. Worker
// harus berhenti dan tidak boleh menulis hasil apa pun.
var ErrLeaseLost = errors.New("queue: job tidak lagi dipegang worker ini")
