package approval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/migration"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
)

type Status string

const (
	StatusDraft         Status = "draft"
	StatusPendingReview Status = "pending_review"
	StatusApproved      Status = "approved"
	StatusQueued        Status = "queued"
	StatusRejected      Status = "rejected"
	StatusCancelled     Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusPendingReview, StatusApproved, StatusQueued, StatusRejected, StatusCancelled:
		return true
	}
	return false
}

type Stage string

const (
	StageCreate  Stage = "create"
	StageSubmit  Stage = "submit"
	StageEnqueue Stage = "enqueue"
	StageExecute Stage = "execute"
)

type Enqueuer interface {
	Enqueue(ctx context.Context, p queue.EnqueueParams) (*queue.Job, bool, error)
}

type Guard func(ctx context.Context, stage Stage, dbID, migrationID int64, kind queue.Kind, targetRunID *int64) error

type Store struct {
	DB    *sql.DB
	Queue Enqueuer
	Guard Guard
	Authz authz.Authorizer
}

type Request struct {
	ID                int64      `json:"id"`
	DBID              int64      `json:"database_id"`
	MigrationID       int64      `json:"migration_id"`
	Version           int        `json:"version"`
	Title             string     `json:"title"`
	Revision          int        `json:"revision"`
	Stale             bool       `json:"stale"`
	Kind              queue.Kind `json:"kind"`
	TargetExecutionID *int64     `json:"target_execution_id"`
	Status            Status     `json:"status"`
	RequestedBy       string     `json:"requested_by"`
	RejectReason      string     `json:"reject_reason"`
	JobID             *int64     `json:"job_id"`
	JobStatus         string     `json:"job_status"`
	CreatedAt         time.Time  `json:"created_at"`
	SubmittedAt       *time.Time `json:"submitted_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Approval struct {
	GroupID   int64      `json:"group_id"`
	GroupName string     `json:"group_name"`
	LeaderID  *int64     `json:"leader_id"`
	Leader    string     `json:"leader"`
	Status    string     `json:"status"`
	DecidedBy string     `json:"decided_by"`
	DecidedAt *time.Time `json:"decided_at"`
}

type Detail struct {
	Request
	SQL       string     `json:"sql"`
	TargetSQL string     `json:"target_sql"`
	Approvals []Approval `json:"approvals"`
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type lockedRow struct {
	migrationID int64
	revision    int
	kind        queue.Kind
	target      *int64
	status      Status
	requestedBy *int64
}

const requestSelect = `
	SELECT r.id, r.db_id, r.migration_id, m.version, m.title, r.revision,
	       (r.status IN ('draft','pending_review','approved','queued') AND r.revision <> m.revision),
	       r.kind, r.target_run_id, r.status,
	       COALESCE(u.username, ''), COALESCE(r.reject_reason, ''),
	       COALESCE((SELECT j.id FROM jobs j WHERE j.request_id = r.id ORDER BY j.id DESC LIMIT 1), r.revert_job_id),
	       COALESCE((SELECT j.status FROM jobs j WHERE j.request_id = r.id ORDER BY j.id DESC LIMIT 1),
	                (SELECT v.status FROM jobs v WHERE v.id = r.revert_job_id), ''),
	       r.created_at, r.submitted_at, r.updated_at
	FROM change_requests r
	JOIN migrations m ON m.id = r.migration_id
	LEFT JOIN users u ON u.id = r.requested_by `

func scanRequest(s interface{ Scan(...any) error }) (Request, error) {
	var r Request
	err := s.Scan(&r.ID, &r.DBID, &r.MigrationID, &r.Version, &r.Title, &r.Revision, &r.Stale,
		&r.Kind, &r.TargetExecutionID, &r.Status, &r.RequestedBy, &r.RejectReason, &r.JobID, &r.JobStatus,
		&r.CreatedAt, &r.SubmittedAt, &r.UpdatedAt)
	return r, err
}

func reason(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrConflict):
		return httpx.Conflict(strings.TrimPrefix(err.Error(), domain.ErrConflict.Error()+": "))
	case errors.Is(err, domain.ErrNotFound):
		return httpx.NotFound("Migration tidak ditemukan di database ini.")
	}
	return err
}

// DefaultGuard memeriksa aturan migration untuk setiap jenis perubahan.
// Saat request dibuat atau diajukan, hanya status migration yang diperiksa
// (request versi berikutnya boleh disiapkan lebih dulu). Saat enqueue dan
// eksekusi, urutan linear ikut diperiksa: up dan forward harus giliran
// berikutnya, rollback hanya untuk migration aktif terakhir. Eksekusi target
// rollback, kalau disebut, harus eksekusi up/forward aktif terakhir.
func DefaultGuard(m migration.Service) Guard {
	return func(ctx context.Context, stage Stage, dbID, migrationID int64, kind queue.Kind, target *int64) error {
		list, err := m.List(ctx, dbID)
		if err != nil {
			return err
		}
		mk := migration.Kind(kind)
		// Untuk migration batch, request rollback adalah jalur revert
		// (kompensasi per batch lewat POST /jobs/{id}/revert). Syaratnya
		// diperiksa ConsumeRevert (job harus partial/partially_reverted) dan
		// worker; rollback biasa tetap ditolak CheckRunnable saat eksekusi.
		if mk == migration.KindRollback {
			for _, it := range list {
				if it.ID == migrationID && it.ExecMode == migration.ExecBatch {
					return nil
				}
			}
		}
		if stage == StageCreate || stage == StageSubmit {
			found := false
			for _, it := range list {
				if it.ID == migrationID {
					found = true
					if err := reason(migration.CheckRequestable(it, mk)); err != nil {
						return err
					}
				}
			}
			if !found {
				return httpx.NotFound("Migration tidak ditemukan di database ini.")
			}
		} else if err := reason(migration.CheckRunnable(list, migrationID, mk)); err != nil {
			return err
		}
		if mk != migration.KindRollback || target == nil {
			return nil
		}
		runs, err := m.Runs(ctx, dbID, migrationID)
		if err != nil {
			return reason(err)
		}
		if last, ok := migration.LatestApplied(runs); !ok || last.ID != *target {
			return httpx.Conflict("Eksekusi target bukan eksekusi aktif terakhir migration ini.")
		}
		return nil
	}
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (s *Store) guard(ctx context.Context, stage Stage, dbID int64, r lockedRow) error {
	if s.Guard == nil {
		return errors.New("approval: guard belum diatur")
	}
	return s.Guard(ctx, stage, dbID, r.migrationID, r.kind, r.target)
}

func (s *Store) requirePostgres(ctx context.Context, dbID int64) error {
	var dbType string
	err := s.DB.QueryRowContext(ctx, `SELECT db_type FROM db_connections WHERE id = $1`, dbID).Scan(&dbType)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound("Database tidak ditemukan.")
	}
	if err != nil {
		return err
	}
	if dbType != "postgres" {
		return httpx.Invalid("Request perubahan hanya didukung untuk database PostgreSQL.")
	}
	return nil
}

func lockRow(ctx context.Context, tx *sql.Tx, dbID, id int64) (lockedRow, error) {
	var r lockedRow
	err := tx.QueryRowContext(ctx, `
		SELECT migration_id, revision, kind, target_run_id, status, requested_by
		FROM change_requests
		WHERE id = $1 AND db_id = $2 FOR NO KEY UPDATE`, id, dbID).
		Scan(&r.migrationID, &r.revision, &r.kind, &r.target, &r.status, &r.requestedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return r, httpx.NotFound("Request tidak ditemukan.")
	}
	return r, err
}

func peekRow(ctx context.Context, q queryer, dbID, id int64) (lockedRow, error) {
	var r lockedRow
	err := q.QueryRowContext(ctx, `
		SELECT migration_id, revision, kind, target_run_id, status, requested_by
		FROM change_requests WHERE id = $1 AND db_id = $2`, id, dbID).
		Scan(&r.migrationID, &r.revision, &r.kind, &r.target, &r.status, &r.requestedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return r, httpx.NotFound("Request tidak ditemukan.")
	}
	return r, err
}

func checkRevision(ctx context.Context, tx *sql.Tx, r lockedRow) error {
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM migrations WHERE id = $1`, r.migrationID).Scan(&current); err != nil {
		return err
	}
	if current != r.revision {
		return httpx.Conflict("SQL migration sudah berubah sejak request dibuat. Buat request baru.")
	}
	return nil
}

func syncApprovals(ctx context.Context, tx *sql.Tx, id, dbID int64, status Status) (Status, error) {
	if status != StatusPendingReview && status != StatusApproved && status != StatusQueued {
		return status, nil
	}
	if status == StatusQueued {
		var waiting bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE request_id = $1 AND status = 'queued')`, id).Scan(&waiting); err != nil {
			return status, err
		}
		if !waiting {
			return status, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM change_request_approvals a
		WHERE a.request_id = $1
		  AND NOT EXISTS (SELECT 1 FROM group_db_access g WHERE g.group_id = a.group_id AND g.db_id = $2)`, id, dbID); err != nil {
		return status, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO change_request_approvals (request_id, group_id)
		SELECT $1::bigint, g.group_id FROM group_db_access g WHERE g.db_id = $2
		ON CONFLICT DO NOTHING`, id, dbID); err != nil {
		return status, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE change_request_approvals a
		SET status = 'pending', decided_by = NULL, decided_at = NULL
		FROM groups gr
		WHERE a.request_id = $1 AND gr.id = a.group_id AND a.status = 'approved'
		  AND (gr.leader_user_id IS NULL OR a.decided_by IS NULL OR gr.leader_user_id <> a.decided_by)`, id); err != nil {
		return status, err
	}
	var total, approved int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'approved')
		FROM change_request_approvals WHERE request_id = $1`, id).Scan(&total, &approved); err != nil {
		return status, err
	}
	complete := total > 0 && total == approved
	if status == StatusQueued {
		if complete {
			return status, nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = 'failed', error_message = 'Approval dicabut sebelum job berjalan',
			       finished_at = now(), updated_at = now()
			WHERE request_id = $1 AND status = 'queued'`, id); err != nil {
			return status, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status = 'pending_review', updated_at = now() WHERE id = $1`, id); err != nil {
			return status, err
		}
		logRevoked(ctx, id, dbID, status, StatusPendingReview)
		return StatusPendingReview, nil
	}
	next := StatusPendingReview
	if complete {
		next = StatusApproved
	}
	if next != status {
		if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status = $2, updated_at = now() WHERE id = $1`, id, next); err != nil {
			return status, err
		}
		if status == StatusApproved && next == StatusPendingReview {
			logRevoked(ctx, id, dbID, status, next)
		}
	}
	return next, nil
}

func logRevoked(ctx context.Context, id, dbID int64, from, to Status) {
	slog.InfoContext(ctx, "approval",
		"event", "approval_revoked",
		"status", "success",
		"change_request_id", id,
		"db_id", dbID,
		"from_status", string(from),
		"to_status", string(to))
}

func approvalsComplete(ctx context.Context, tx *sql.Tx, id, dbID int64) (bool, error) {
	var total, missing int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE a.group_id IS NULL OR gr.leader_user_id IS NULL
		                        OR a.decided_by IS NULL OR gr.leader_user_id <> a.decided_by)
		FROM group_db_access g
		JOIN groups gr ON gr.id = g.group_id
		LEFT JOIN change_request_approvals a
		  ON a.request_id = $1 AND a.group_id = g.group_id AND a.status = 'approved'
		WHERE g.db_id = $2`, id, dbID).Scan(&total, &missing)
	if err != nil {
		return false, err
	}
	return total > 0 && missing == 0, nil
}

func loadDetail(ctx context.Context, q queryer, dbID, id int64) (*Detail, error) {
	req, err := scanRequest(q.QueryRowContext(ctx, requestSelect+`WHERE r.id = $1 AND r.db_id = $2`, id, dbID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NotFound("Request tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	d := &Detail{Request: req, Approvals: []Approval{}}
	err = q.QueryRowContext(ctx, `
		SELECT CASE WHEN $3 = 'rollback' THEN COALESCE(m.sql_down, '') ELSE m.sql_text END,
		       COALESCE((SELECT t.sql_text FROM migration_runs t WHERE t.id = $2), '')
		FROM migrations m WHERE m.id = $1`, req.MigrationID, req.TargetExecutionID, string(req.Kind)).Scan(&d.SQL, &d.TargetSQL)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT a.group_id, g.name, g.leader_user_id, COALESCE(l.username, ''), a.status,
		       COALESCE(d.username, ''), a.decided_at
		FROM change_request_approvals a
		JOIN groups g ON g.id = a.group_id
		LEFT JOIN users l ON l.id = g.leader_user_id
		LEFT JOIN users d ON d.id = a.decided_by
		WHERE a.request_id = $1
		ORDER BY lower(g.name)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.GroupID, &a.GroupName, &a.LeaderID, &a.Leader, &a.Status, &a.DecidedBy, &a.DecidedAt); err != nil {
			return nil, err
		}
		d.Approvals = append(d.Approvals, a)
	}
	return d, rows.Err()
}

func (s *Store) validateTarget(ctx context.Context, dbID, migrationID int64, kind queue.Kind, target *int64) error {
	if kind == queue.KindUp {
		if target != nil {
			return httpx.Invalid("Request up tidak memakai eksekusi target.")
		}
		return nil
	}
	if target == nil {
		return nil
	}
	var ok bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM migration_runs r JOIN migrations m ON m.id = r.migration_id
			WHERE r.id = $1 AND m.id = $2 AND m.db_id = $3)`, *target, migrationID, dbID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Invalid("Eksekusi target tidak ditemukan untuk migration ini.")
	}
	return nil
}

func (s *Store) Create(ctx context.Context, u *domain.User, dbID, migrationID int64, kind queue.Kind, targetRunID *int64) (*Detail, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if !kind.Valid() {
		return nil, httpx.Invalid("Jenis perubahan harus up, rollback, atau forward.")
	}
	if err := s.requirePostgres(ctx, dbID); err != nil {
		return nil, err
	}
	var revision int
	err := s.DB.QueryRowContext(ctx, `SELECT revision FROM migrations WHERE id = $1 AND db_id = $2`, migrationID, dbID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NotFound("Migration tidak ditemukan di database ini.")
	}
	if err != nil {
		return nil, err
	}
	if err := s.validateTarget(ctx, dbID, migrationID, kind, targetRunID); err != nil {
		return nil, err
	}
	if s.Guard == nil {
		return nil, errors.New("approval: guard belum diatur")
	}
	if err := s.Guard(ctx, StageCreate, dbID, migrationID, kind, targetRunID); err != nil {
		return nil, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO change_requests (db_id, migration_id, revision, kind, target_run_id, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		dbID, migrationID, revision, kind, targetRunID, u.ID).Scan(&id)
	if isUnique(err) {
		return nil, httpx.Conflict("Migration ini sudah punya request yang belum selesai.")
	}
	if err != nil {
		return nil, err
	}
	return loadDetail(ctx, s.DB, dbID, id)
}

func (s *Store) Get(ctx context.Context, dbID, id int64) (*Detail, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	if _, err := syncApprovals(ctx, tx, id, dbID, r.status); err != nil {
		return nil, err
	}
	d, err := loadDetail(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	return d, tx.Commit()
}

func (s *Store) List(ctx context.Context, dbID int64, status Status) ([]Request, error) {
	if status != "" && !status.Valid() {
		return nil, httpx.Invalid("Status request tidak dikenal.")
	}
	rows, err := s.DB.QueryContext(ctx, requestSelect+`
		WHERE r.db_id = $1 AND ($2::text = '' OR r.status = $2::text)
		ORDER BY r.created_at DESC, r.id DESC LIMIT 200`, dbID, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SyncOpen(ctx context.Context) error {
	type ref struct{ id, dbID int64 }
	rows, err := s.DB.QueryContext(ctx, `
		SELECT r.id, r.db_id FROM change_requests r
		WHERE r.status IN ('pending_review','approved')
		   OR (r.status = 'queued'
		       AND EXISTS (SELECT 1 FROM jobs j WHERE j.request_id = r.id AND j.status = 'queued'))
		ORDER BY r.id`)
	if err != nil {
		return err
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.dbID); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range refs {
		if err := s.syncOne(ctx, r.dbID, r.id); err != nil {
			errs = append(errs, fmt.Errorf("request %d: %w", r.id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) syncOne(ctx context.Context, dbID, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := syncApprovals(ctx, tx, id, dbID, r.status); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Submit(ctx context.Context, dbID, id int64) (*Detail, error) {
	if err := s.requirePostgres(ctx, dbID); err != nil {
		return nil, err
	}
	peek, err := peekRow(ctx, s.DB, dbID, id)
	if err != nil {
		return nil, err
	}
	if peek.status != StatusDraft {
		return nil, httpx.Conflict("Request ini sudah diajukan atau sudah selesai.")
	}
	if err := s.guard(ctx, StageSubmit, dbID, peek); err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	if r.status != StatusDraft {
		return nil, httpx.Conflict("Request ini sudah diajukan atau sudah selesai.")
	}
	if err := checkRevision(ctx, tx, r); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE change_requests SET status = 'pending_review', submitted_at = now(), updated_at = now()
		WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := syncApprovals(ctx, tx, id, dbID, StatusPendingReview); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dbID, id)
}

func (s *Store) Approve(ctx context.Context, u *domain.User, dbID, id int64) (*Detail, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	status, err := syncApprovals(ctx, tx, id, dbID, r.status)
	if err != nil {
		return nil, err
	}
	if status != StatusPendingReview {
		return nil, httpx.Conflict("Request tidak sedang menunggu review.")
	}
	if err := checkRevision(ctx, tx, r); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE change_request_approvals a
		SET status = 'approved', decided_by = $2, decided_at = now()
		FROM groups g
		WHERE a.request_id = $1 AND a.group_id = g.id AND a.status = 'pending' AND g.leader_user_id = $2`, id, u.ID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, httpx.Forbidden("Anda bukan leader dari group yang masih perlu memberi approval.")
	}
	if _, err := syncApprovals(ctx, tx, id, dbID, StatusPendingReview); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dbID, id)
}

func (s *Store) Reject(ctx context.Context, u *domain.User, dbID, id int64, text string) (*Detail, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.Invalid("Alasan penolakan wajib diisi.")
	}
	if utf8.RuneCountInString(text) > 500 {
		return nil, httpx.Invalid("Alasan penolakan maksimal 500 karakter.")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	status, err := syncApprovals(ctx, tx, id, dbID, r.status)
	if err != nil {
		return nil, err
	}
	if status != StatusPendingReview && status != StatusApproved {
		return nil, httpx.Conflict("Request tidak sedang dalam tahap review.")
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE change_request_approvals a
		SET status = 'rejected', decided_by = $2, decided_at = now()
		FROM groups g
		WHERE a.request_id = $1 AND a.group_id = g.id AND a.status IN ('pending', 'approved') AND g.leader_user_id = $2`, id, u.ID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, httpx.Forbidden("Anda bukan leader dari group yang terkait request ini.")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE change_requests SET status = 'rejected', reject_reason = $2, updated_at = now() WHERE id = $1`, id, text); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dbID, id)
}

func (s *Store) readyToEnqueue(ctx context.Context, dbID, id int64) (lockedRow, Status, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return lockedRow{}, "", err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return r, "", err
	}
	status, err := syncApprovals(ctx, tx, id, dbID, r.status)
	if err != nil {
		return r, status, err
	}
	if status == StatusQueued {
		var last sql.NullString
		lastErr := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE request_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&last)
		if lastErr != nil && !errors.Is(lastErr, sql.ErrNoRows) {
			return r, status, lastErr
		}
		if last.Valid && last.String == string(queue.StatusFailed) {
			complete, err := approvalsComplete(ctx, tx, id, dbID)
			if err != nil {
				return r, status, err
			}
			if !complete {
				if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status = 'pending_review', updated_at = now() WHERE id = $1`, id); err != nil {
					return r, status, err
				}
				if _, err := syncApprovals(ctx, tx, id, dbID, StatusPendingReview); err != nil {
					return r, status, err
				}
				if err := tx.Commit(); err != nil {
					return r, status, err
				}
				logRevoked(ctx, id, dbID, StatusQueued, StatusPendingReview)
				return r, StatusPendingReview, httpx.Conflict("Approval dicabut karena akses atau leader group berubah. Minta approval ulang.")
			}
		}
	}
	if status == StatusApproved || status == StatusQueued {
		if err := checkRevision(ctx, tx, r); err != nil {
			return r, status, err
		}
	}
	if err := tx.Commit(); err != nil {
		return r, status, err
	}
	if status != StatusApproved && status != StatusQueued {
		return r, status, httpx.Conflict("Request belum disetujui oleh semua leader group.")
	}
	return r, status, nil
}

func (s *Store) latestJobStatus(ctx context.Context, requestID int64) (queue.Status, error) {
	var st string
	err := s.DB.QueryRowContext(ctx, `
		SELECT status FROM jobs WHERE request_id = $1 ORDER BY id DESC LIMIT 1`, requestID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return queue.Status(st), nil
}

func (s *Store) Enqueue(ctx context.Context, u *domain.User, dbID, id int64) (*queue.Job, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if err := s.requirePostgres(ctx, dbID); err != nil {
		return nil, err
	}
	var consumed sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT revert_job_id FROM change_requests WHERE id = $1 AND db_id = $2`, id, dbID).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NotFound("Request tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	if consumed.Valid {
		return nil, httpx.Conflict(fmt.Sprintf("Request rollback ini sudah dipakai untuk revert job #%d dan tidak bisa dijalankan ulang.", consumed.Int64))
	}
	r, status, err := s.readyToEnqueue(ctx, dbID, id)
	if err != nil {
		return nil, err
	}
	last, err := s.latestJobStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	if last == queue.StatusSucceeded {
		return nil, httpx.Conflict("Request ini sudah berhasil dijalankan dan tidak bisa dijalankan lagi. Buat request baru jika perlu.")
	}
	if status == StatusApproved {
		if err := s.guard(ctx, StageEnqueue, dbID, r); err != nil {
			return nil, err
		}
	}
	var attempts int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE request_id = $1`, id).Scan(&attempts); err != nil {
		return nil, err
	}
	mode := queue.ModeNormal
	var execMode string
	if err := s.DB.QueryRowContext(ctx, `SELECT exec_mode FROM migrations WHERE id = $1`, r.migrationID).Scan(&execMode); err != nil {
		return nil, err
	}
	if execMode == string(queue.ModeBatch) && r.kind == queue.KindUp {
		mode = queue.ModeBatch
	}
	// Span queue.enqueue: traceparent diambil dari span ini, jadi span
	// job.process di worker menjadi anaknya (API -> queue -> worker).
	qctx, span := obs.StartSpan(ctx, "queue.enqueue", trace.WithAttributes(
		attribute.Int64("db.id", dbID),
		attribute.Int64("migration.id", r.migrationID),
		attribute.Int64("change_request.id", id),
		attribute.String("job.kind", string(r.kind)),
		attribute.String("job.mode", string(mode)),
	))
	job, _, err := s.Queue.Enqueue(qctx, queue.EnqueueParams{
		DBID:           dbID,
		MigrationID:    r.migrationID,
		Kind:           r.kind,
		Mode:           mode,
		IdempotencyKey: fmt.Sprintf("request-%d-%d", id, attempts),
		RequestedBy:    u.ID,
		RequestID:      &id,
		TraceContext:   obs.InjectTraceParent(qctx),
	})
	if job != nil {
		span.SetAttributes(attribute.Int64("job.id", job.ID))
	}
	obs.RecordError(span, err)
	span.End()
	if err != nil {
		// queue menolak job dengan jenis berbeda (misalnya rollback saat job
		// up masih terbuka) supaya ID job lain tidak dikembalikan sebagai
		// eksekusi yang diminta. Beri pesan yang menjelaskan job yang menahan.
		if errors.Is(err, domain.ErrConflict) {
			open, oerr := s.openJob(ctx, r.migrationID)
			if oerr != nil {
				return nil, oerr
			}
			if open != nil {
				return nil, openJobConflict(open)
			}
		}
		return nil, err
	}
	if job.RequestID == nil || *job.RequestID != id {
		return nil, openJobConflict(job)
	}
	if status == StatusApproved {
		if _, err := s.DB.ExecContext(ctx, `
			UPDATE change_requests SET status = 'queued', updated_at = now()
			WHERE id = $1 AND status = 'approved'`, id); err != nil {
			return nil, err
		}
	}
	return job, nil
}

// openJob mengembalikan job terbuka milik migration, atau nil.
func (s *Store) openJob(ctx context.Context, migrationID int64) (*queue.Job, error) {
	j := &queue.Job{}
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, status FROM jobs
		WHERE migration_id = $1 AND status IN ('queued','running','uncertain','partial','partially_reverted')`,
		migrationID).Scan(&j.ID, &j.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

func openJobConflict(j *queue.Job) error {
	if j.Status == queue.StatusPartial || j.Status == queue.StatusPartiallyReverted {
		return httpx.Conflict(fmt.Sprintf("Migration ini masih partial pada job #%d. Gunakan aksi revert pada job tersebut dengan request rollback ini.", j.ID))
	}
	return httpx.Conflict("Migration ini masih punya job lain yang belum selesai.")
}

func (s *Store) ConsumeRevert(ctx context.Context, u *domain.User, dbID, jobID, requestID int64) (*Detail, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if err := s.requirePostgres(ctx, dbID); err != nil {
		return nil, err
	}
	peek, err := peekRow(ctx, s.DB, dbID, requestID)
	if err != nil {
		return nil, err
	}
	if peek.kind != queue.KindRollback {
		return nil, httpx.Conflict("Revert hanya bisa memakai request berjenis rollback.")
	}
	if peek.status == StatusApproved {
		if err := s.guard(ctx, StageEnqueue, dbID, peek); err != nil {
			return nil, err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, requestID)
	if err != nil {
		return nil, err
	}
	status, err := syncApprovals(ctx, tx, requestID, dbID, r.status)
	if err != nil {
		return nil, err
	}
	var jobMigration int64
	var jobStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT migration_id, status FROM jobs WHERE id = $1 AND db_id = $2 FOR UPDATE`, jobID, dbID).
		Scan(&jobMigration, &jobStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NotFound("Job tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	if jobMigration != r.migrationID {
		return nil, httpx.Conflict("Request rollback ini bukan untuk migration milik job tersebut.")
	}
	var linked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revert_job_id FROM change_requests WHERE id = $1`, requestID).Scan(&linked); err != nil {
		return nil, err
	}
	if status == StatusQueued && linked.Valid && linked.Int64 == jobID {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.Get(ctx, dbID, requestID)
	}
	if status != StatusApproved {
		return nil, httpx.Conflict("Request rollback belum disetujui oleh semua leader group.")
	}
	if jobStatus != string(queue.StatusPartial) && jobStatus != string(queue.StatusPartiallyReverted) {
		return nil, httpx.Conflict("Revert hanya bisa dilakukan pada job berstatus partial atau partially_reverted.")
	}
	if err := checkRevision(ctx, tx, r); err != nil {
		return nil, err
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM change_requests WHERE revert_job_id = $1 AND status = 'queued' AND id <> $2)`,
		jobID, requestID).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, httpx.Conflict("Job ini sudah punya request rollback lain yang sedang dipakai.")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE change_requests SET status = 'queued', revert_job_id = $2, updated_at = now() WHERE id = $1`,
		requestID, jobID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dbID, requestID)
}

func (s *Store) Cancel(ctx context.Context, u *domain.User, dbID, id int64) (*Detail, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return nil, err
	}
	switch r.status {
	case StatusDraft, StatusPendingReview, StatusApproved:
	default:
		return nil, httpx.Conflict("Request ini sudah dibatalkan, ditolak, atau sudah masuk antrean.")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status = 'cancelled', updated_at = now() WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, dbID, id)
}

func (s *Store) Revalidate(ctx context.Context, id, executorID int64, resume bool) error {
	var dbID int64
	err := s.DB.QueryRowContext(ctx, `SELECT db_id FROM change_requests WHERE id = $1`, id).Scan(&dbID)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound("Request tidak ditemukan.")
	}
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := lockRow(ctx, tx, dbID, id)
	if err != nil {
		return err
	}
	if r.status != StatusQueued && r.status != StatusApproved {
		return httpx.Conflict("Request tidak berstatus siap dijalankan.")
	}
	if !resume {
		if err := checkRevision(ctx, tx, r); err != nil {
			return err
		}
		complete, err := approvalsComplete(ctx, tx, id, dbID)
		if err != nil {
			return err
		}
		if !complete {
			if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status = 'pending_review', updated_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
			if _, err := syncApprovals(ctx, tx, id, dbID, StatusPendingReview); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			logRevoked(ctx, id, dbID, r.status, StatusPendingReview)
			return httpx.Conflict("Approval dicabut karena akses atau leader group berubah.")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.checkUser(ctx, dbID, executorID); err != nil {
		return err
	}
	if resume {
		return nil
	}
	return s.guard(ctx, StageExecute, dbID, r)
}

func (s *Store) checkUser(ctx context.Context, dbID, userID int64) error {
	u := &domain.User{}
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, username, is_platform_admin, is_active FROM users WHERE id = $1`, userID).
		Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !u.IsActive) {
		return httpx.Conflict("User yang menjalankan sudah tidak aktif.")
	}
	if err != nil {
		return err
	}
	if s.Authz == nil {
		return nil
	}
	err = s.Authz.Authorize(ctx, u, dbID, authz.ActionRequestChange)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrUnauthenticated):
		return httpx.Conflict("User yang menjalankan tidak lagi punya akses Edit ke database ini.")
	default:
		return err
	}
}
