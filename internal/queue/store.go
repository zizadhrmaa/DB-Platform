package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Store struct {
	DB     *sql.DB
	Policy RetryPolicy
}

// Execer dipenuhi *sql.DB dan *sql.Tx, supaya SetRun bisa ikut transaction
// yang mencatat migration_runs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const jobColumns = `id, db_id, migration_id, request_id, kind, mode, status, idempotency_key,
	requested_by, attempt, max_attempts, next_run_at, COALESCE(lease_owner,''), lease_until, run_id,
	COALESCE(trace_context,''), batches_committed, rows_committed, COALESCE(last_cursor,''),
	COALESCE(error_message,''), created_at, started_at, finished_at, updated_at`

func scanJob(s interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	err := s.Scan(&j.ID, &j.DBID, &j.MigrationID, &j.RequestID, &j.Kind, &j.Mode, &j.Status, &j.IdempotencyKey,
		&j.RequestedBy, &j.Attempt, &j.MaxAttempts, &j.NextRunAt, &j.LeaseOwner, &j.LeaseUntil, &j.RunID,
		&j.TraceContext, &j.BatchesCommitted, &j.RowsCommitted, &j.LastCursor,
		&j.ErrorMessage, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func millis(d time.Duration) int64 { return d.Milliseconds() }

type EnqueueParams struct {
	DBID           int64
	MigrationID    int64
	Kind           Kind   // kosong = up
	Mode           Mode   // kosong = normal
	IdempotencyKey string // wajib; kiriman ulang dengan kunci sama mengembalikan job yang sama
	RequestedBy    int64  // 0 = tidak dicatat
	RequestID      *int64
	MaxAttempts    int // 0 = Policy.MaxAttempts
	TraceContext   string
}

// Enqueue memasukkan job baru. Hasil created=false berarti job yang sudah
// ada dikembalikan: kunci idempotensi yang sama, atau migration ini masih
// punya job terbuka. Kunci yang sudah dipakai untuk migration lain
// menghasilkan ErrConflict.
func (s *Store) Enqueue(ctx context.Context, p EnqueueParams) (*Job, bool, error) {
	p.IdempotencyKey = strings.TrimSpace(p.IdempotencyKey)
	if p.Kind == "" {
		p.Kind = KindUp
	}
	if p.Mode == "" {
		p.Mode = ModeNormal
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = s.policy().MaxAttempts
	}
	switch {
	case p.DBID <= 0 || p.MigrationID <= 0:
		return nil, false, fmt.Errorf("%w: database dan migration wajib diisi", domain.ErrInvalidInput)
	case p.IdempotencyKey == "" || len(p.IdempotencyKey) > 200:
		return nil, false, fmt.Errorf("%w: kunci idempotensi wajib diisi (maksimal 200 karakter)", domain.ErrInvalidInput)
	case !p.Kind.Valid() || !p.Mode.Valid():
		return nil, false, fmt.Errorf("%w: jenis atau mode eksekusi tidak dikenal", domain.ErrInvalidInput)
	case p.MaxAttempts < 1:
		return nil, false, fmt.Errorf("%w: jumlah attempt minimal 1", domain.ErrInvalidInput)
	}
	var requestedBy *int64
	if p.RequestedBy > 0 {
		requestedBy = &p.RequestedBy
	}

	// Job yang bentrok bisa saja selesai di antara INSERT dan SELECT, jadi
	// dicoba beberapa kali.
	for range 3 {
		// ON CONFLICT tanpa target mencakup semua index unik: kunci
		// idempotensi dan satu job terbuka per migration.
		j, err := scanJob(s.DB.QueryRowContext(ctx, `
			INSERT INTO jobs (db_id, migration_id, request_id, kind, mode, idempotency_key,
			                  requested_by, max_attempts, trace_context)
			SELECT m.db_id, m.id, $3, $4, $5, $6, $7, $8, NULLIF($9,'')
			FROM migrations m WHERE m.id = $2 AND m.db_id = $1
			ON CONFLICT DO NOTHING
			RETURNING `+jobColumns,
			p.DBID, p.MigrationID, p.RequestID, p.Kind, p.Mode, p.IdempotencyKey,
			requestedBy, p.MaxAttempts, p.TraceContext))
		if err == nil {
			return j, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}

		same, err := scanJob(s.DB.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE idempotency_key = $1`, p.IdempotencyKey))
		switch {
		case err == nil:
			if same.DBID != p.DBID || same.MigrationID != p.MigrationID || same.Kind != p.Kind || same.Mode != p.Mode {
				return nil, false, fmt.Errorf("%w: kunci idempotensi sudah dipakai untuk eksekusi lain", domain.ErrConflict)
			}
			return same, false, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, false, err
		}

		var exists bool
		if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migrations WHERE id = $1 AND db_id = $2)`, p.MigrationID, p.DBID).Scan(&exists); err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, fmt.Errorf("%w: migration tidak ada di database ini", domain.ErrNotFound)
		}

		open, err := scanJob(s.DB.QueryRowContext(ctx, `
			SELECT `+jobColumns+` FROM jobs
			WHERE migration_id = $1 AND status IN `+openStatuses, p.MigrationID))
		if err == nil {
			// Jangan memberikan ID job up kepada permintaan rollback (atau
			// sebaliknya) seolah-olah itu eksekusi yang diminta.
			if open.Kind != p.Kind || open.Mode != p.Mode {
				return nil, false, fmt.Errorf("%w: migration ini masih punya eksekusi %s (%s) yang belum selesai, job #%d",
					domain.ErrConflict, open.Kind, open.Mode, open.ID)
			}
			return open, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	return nil, false, fmt.Errorf("%w: antrean untuk migration ini sedang berubah, coba lagi", domain.ErrConflict)
}

// Claim mengambil satu job queued yang sudah waktunya lalu menjadikannya
// running milik owner. Hasil nil tanpa error berarti tidak ada job yang
// bisa diambil.
//
// Per database, job dijalankan sesuai urutan masuk antrean: hanya job
// queued tertua (id terkecil) yang menjadi kandidat, termasuk ketika job itu
// sedang menunggu jeda retry. Jadi migration berikutnya tidak menyalip
// migration sebelumnya. Database yang punya job aktif dilewati.
func (s *Store) Claim(ctx context.Context, owner string, lease time.Duration) (*Job, error) {
	if owner == "" || lease <= 0 {
		return nil, fmt.Errorf("%w: owner dan lease wajib diisi", domain.ErrInvalidInput)
	}
	for range 3 {
		j, err := scanJob(s.DB.QueryRowContext(ctx, `
			WITH next AS (
				SELECT j.id AS next_id FROM jobs j
				WHERE j.status = 'queued' AND j.next_run_at <= now()
				  AND NOT EXISTS (SELECT 1 FROM jobs a WHERE a.db_id = j.db_id AND a.status IN `+holdingStatuses+`)
				  AND NOT EXISTS (SELECT 1 FROM jobs q WHERE q.db_id = j.db_id AND q.status = 'queued' AND q.id < j.id)
				ORDER BY j.next_run_at, j.id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			UPDATE jobs SET status = 'running', attempt = attempt + 1, run_id = NULL, error_message = NULL,
			       lease_owner = $1, lease_until = now() + $2 * interval '1 millisecond',
			       started_at = COALESCE(started_at, now()), updated_at = now()
			FROM next WHERE jobs.id = next.next_id
			RETURNING `+jobColumns, owner, millis(lease)))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		// Ada job yang aktif di database yang sama setelah snapshot query ini
		// diambil, sehingga index unik menolak. Ulangi dengan snapshot baru:
		// database itu kini terlihat sibuk dan database lain bisa diambil.
		if isUniqueViolation(err) {
			continue
		}
		return j, err
	}
	return nil, nil
}

// Heartbeat memperpanjang lease. ErrLeaseLost berarti worker harus berhenti.
func (s *Store) Heartbeat(ctx context.Context, id int64, owner string, lease time.Duration) error {
	return expectOne(s.DB.ExecContext(ctx, `
		UPDATE jobs SET lease_until = now() + $3 * interval '1 millisecond', updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, id, owner, millis(lease)))
}

// SetRun mencatat migration_runs milik attempt ini. ex boleh berupa
// transaction yang juga meng-insert migration_runs supaya keduanya atomik.
func (s *Store) SetRun(ctx context.Context, ex Execer, id int64, owner string, runID int64) error {
	return expectOne(ex.ExecContext(ctx, `
		UPDATE jobs SET run_id = $3, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, id, owner, runID))
}

// finishable adalah status akhir attempt yang boleh ditulis worker.
func finishable(st Status) bool {
	switch st {
	case StatusSucceeded, StatusFailed, StatusUncertain, StatusPartial, StatusPartiallyReverted:
		return true
	}
	return false
}

// Finish menulis hasil attempt dan melepas lease. finished_at hanya diisi
// untuk status terminal.
func (s *Store) Finish(ctx context.Context, id int64, owner string, st Status, message string) error {
	if !finishable(st) {
		return fmt.Errorf("%w: status akhir job tidak valid: %s", domain.ErrInvalidInput, st)
	}
	return expectOne(s.DB.ExecContext(ctx, `
		UPDATE jobs SET status = $3, error_message = NULLIF($4,''),
		       lease_owner = NULL, lease_until = NULL, updated_at = now(),
		       finished_at = CASE WHEN $3 IN ('succeeded','failed') THEN now() END
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, id, owner, st, message))
}

// Retry mengembalikan job ke antrean dengan jeda sesuai Policy, atau
// menjadikannya failed kalau attempt sudah habis.
func (s *Store) Retry(ctx context.Context, id int64, owner, message string) (*Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var attempt, maxAttempts int
	err = tx.QueryRowContext(ctx, `
		SELECT attempt, max_attempts FROM jobs
		WHERE id = $1 AND lease_owner = $2 AND status = 'running' FOR UPDATE`, id, owner).Scan(&attempt, &maxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLeaseLost
	}
	if err != nil {
		return nil, err
	}
	j, err := s.requeue(ctx, tx, id, attempt, maxAttempts, message)
	if err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

func (s *Store) requeue(ctx context.Context, tx *sql.Tx, id int64, attempt, maxAttempts int, message string) (*Job, error) {
	if attempt >= maxAttempts {
		return scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs SET status = 'failed', error_message = NULLIF($2,''),
			       lease_owner = NULL, lease_until = NULL, finished_at = now(), updated_at = now()
			WHERE id = $1 RETURNING `+jobColumns, id, message))
	}
	return scanJob(tx.QueryRowContext(ctx, `
		UPDATE jobs SET status = 'queued', error_message = NULLIF($2,''),
		       next_run_at = now() + $3 * interval '1 millisecond',
		       lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 RETURNING `+jobColumns, id, message, millis(s.policy().Delay(attempt))))
}

// policy mengembalikan Policy, atau DefaultRetryPolicy kalau Store dibuat
// tanpa Policy, supaya Store{DB: db} tetap punya batas attempt dan jeda.
func (s *Store) policy() RetryPolicy {
	if s.Policy.MaxAttempts == 0 && s.Policy.BaseDelay == 0 && s.Policy.MaxDelay == 0 {
		return DefaultRetryPolicy()
	}
	return s.Policy
}

// Recoverable mengembalikan job yang perlu diperiksa pemulihan: running
// dengan lease yang sudah lewat, dan uncertain. Running didahulukan supaya
// job uncertain yang buktinya belum bisa diperiksa (misalnya target mati)
// tidak menutupi worker yang baru saja mati.
func (s *Store) Recoverable(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.query(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE (status = 'running' AND lease_until < now()) OR status = 'uncertain'
		ORDER BY (status = 'running') DESC, updated_at, id LIMIT $1`, limit)
}

// Expect adalah keadaan job yang dilihat pemulihan sebelum memutuskan.
// LeaseOwner kosong berarti job tidak punya pemilik (misalnya uncertain).
// Attempt wajib diisi: status dan pemilik bisa sama persis di attempt
// berikutnya (satu proses worker memakai owner yang sama untuk semua
// claim), jadi hanya nomor attempt yang membedakan bukti lama dari baru.
type Expect struct {
	Status     Status
	LeaseOwner string
	Attempt    int
}

// Resolve mengubah status job hasil pemulihan dengan compare-and-set: hanya
// berhasil kalau job masih dalam keadaan from (status, pemilik, dan attempt
// yang sama) dan, untuk job running, lease-nya sudah lewat. ok=false berarti
// keadaan sudah berubah (worker ternyata masih hidup, atau pemulihan lain
// sudah menang) dan tidak ada yang diubah. to=queued memakai aturan retry
// yang sama dengan Retry.
func (s *Store) Resolve(ctx context.Context, id int64, from Expect, to Status, message string) (bool, error) {
	if from.Status != StatusRunning && from.Status != StatusUncertain {
		return false, fmt.Errorf("%w: pemulihan hanya untuk job running atau uncertain", domain.ErrInvalidInput)
	}
	if from.Attempt < 1 {
		return false, fmt.Errorf("%w: pemulihan wajib menyebut attempt yang diperiksa", domain.ErrInvalidInput)
	}
	if to != StatusQueued && !finishable(to) {
		return false, fmt.Errorf("%w: status hasil pemulihan tidak valid: %s", domain.ErrInvalidInput, to)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var attempt, maxAttempts int
	err = tx.QueryRowContext(ctx, `
		SELECT attempt, max_attempts FROM jobs
		WHERE id = $1 AND status = $2 AND lease_owner IS NOT DISTINCT FROM NULLIF($3,'')
		  AND attempt = $4
		  AND (status <> 'running' OR lease_until < now())
		FOR UPDATE`, id, from.Status, from.LeaseOwner, from.Attempt).Scan(&attempt, &maxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if to == StatusQueued {
		_, err = s.requeue(ctx, tx, id, attempt, maxAttempts, message)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE jobs SET status = $2, error_message = NULLIF($3,''),
			       lease_owner = NULL, lease_until = NULL, updated_at = now(),
			       finished_at = CASE WHEN $2 IN ('succeeded','failed') THEN now() END
			WHERE id = $1`, id, to, message)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) Get(ctx context.Context, id int64) (*Job, error) {
	return s.one(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id)
}

// GetInDB memastikan job milik database dbID, supaya user tidak bisa
// membaca job database lain dengan mengganti ID di URL.
func (s *Store) GetInDB(ctx context.Context, dbID, id int64) (*Job, error) {
	return s.one(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1 AND db_id = $2`, id, dbID)
}

type ListFilter struct {
	Status Status // kosong = semua
	Limit  int    // 0 = 50, maksimal 200
}

// List mengembalikan job sebuah database, terbaru dulu.
func (s *Store) List(ctx context.Context, dbID int64, f ListFilter) ([]Job, error) {
	if f.Status != "" && !f.Status.Valid() {
		return nil, fmt.Errorf("%w: status job tidak dikenal %q", domain.ErrInvalidInput, f.Status)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	return s.query(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE db_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, dbID, string(f.Status), f.Limit)
}

func (s *Store) one(ctx context.Context, q string, args ...any) (*Job, error) {
	j, err := scanJob(s.DB.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: job tidak ditemukan", domain.ErrNotFound)
	}
	return j, err
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func expectOne(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
