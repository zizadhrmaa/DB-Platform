package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

// Migration per batch.
//
// Satu job batch memegang satu koneksi khusus ke database target selama
// seluruh eksekusi, dan di koneksi itu mengambil pg_advisory_lock level
// session dengan kunci yang sama seperti eksekusi biasa. Lock session tidak
// lepas saat COMMIT, jadi tetap melindungi database di antara commit batch.
// Kalau proses worker mati, koneksinya putus dan PostgreSQL melepas lock
// itu sendiri.
//
// Setiap batch adalah satu transaction: SQL user untuk rentang (lower,
// upper] dan checkpoint di _dbplatform_batches ditulis bersama lalu
// di-commit. Batch yang gagal hanya me-rollback transaction-nya sendiri.
// Cakupan (batas atas kolom cursor) dicatat sekali di awal job, sehingga row
// yang masuk belakangan tidak ikut diproses. Platform hanya mengambil satu
// nilai batas atas per batch, tidak pernah memuat isi tabel ke memory.

const (
	batchTableSQL = `CREATE TABLE IF NOT EXISTS _dbplatform_batches (
		job_id       BIGINT      NOT NULL,
		batch_no     INTEGER     NOT NULL,
		lower_bound  BIGINT      NOT NULL,
		upper_bound  BIGINT      NOT NULL,
		rows         BIGINT      NOT NULL,
		committed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		reverted_at  TIMESTAMPTZ,
		PRIMARY KEY (job_id, batch_no))`
	batchScopeSQL = `CREATE TABLE IF NOT EXISTS _dbplatform_batch_scope (
		job_id      BIGINT PRIMARY KEY,
		lower_bound BIGINT,
		upper_bound BIGINT)`
)

// BatchAction adalah pekerjaan job batch.
type BatchAction string

const (
	BatchRun    BatchAction = "run"    // jalankan batch berikutnya dari checkpoint
	BatchRevert BatchAction = "revert" // batalkan batch committed, urutan terbalik
)

// BatchStatus adalah hasil akhir ExecuteBatch, sama dengan status job.
type BatchStatus string

const (
	BatchSucceeded         BatchStatus = "succeeded"
	BatchPartial           BatchStatus = "partial"
	BatchFailed            BatchStatus = "failed"
	BatchReverted          BatchStatus = "reverted"
	BatchPartiallyReverted BatchStatus = "partially_reverted"
)

type BatchProgress struct {
	Batches int   // jumlah batch yang committed dan belum dibatalkan
	Rows    int64 // jumlah row dari batch tersebut
	Cursor  int64 // batas atas batch committed terakhir
}

type BatchOptions struct {
	JobID   int64
	Action  BatchAction
	Timeout time.Duration // statement_timeout per batch; 0 = 60 detik
	// OnStart: lihat ExecOptions.OnStart. Hanya dipanggil untuk BatchRun.
	OnStart func(ctx context.Context, tx *sql.Tx, runID int64) error
	// OnBatch dipanggil setelah setiap batch committed atau dibatalkan.
	// Error menghentikan job (misalnya lease hilang); batch berikutnya tidak
	// dijalankan.
	OnBatch func(ctx context.Context, p BatchProgress) error
}

type BatchResult struct {
	Status   BatchStatus
	Progress BatchProgress
	RunID    int64
	Message  string
}

// ExecuteBatch menjalankan atau membatalkan migration batch untuk job
// opt.JobID. Error berarti tidak ada batch yang dijalankan pada pemanggilan
// ini (akses, urutan, target sibuk atau tidak terjangkau); kegagalan di
// tengah dilaporkan lewat BatchResult.
func (s *Store) ExecuteBatch(ctx context.Context, u *domain.User, dbID, id int64, opt BatchOptions) (*BatchResult, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if opt.JobID <= 0 || (opt.Action != BatchRun && opt.Action != BatchRevert) {
		return nil, fmt.Errorf("%w: job dan action batch wajib diisi", domain.ErrInvalidInput)
	}
	if e := s.Authz.Authorize(ctx, u, dbID, authz.ActionExecuteMigration); e != nil {
		return nil, e
	}
	m, e := s.Get(ctx, dbID, id)
	if e != nil {
		return nil, e
	}
	if m.ExecMode != ExecBatch {
		return nil, fmt.Errorf("%w: migration ini bukan mode batch", domain.ErrInvalidInput)
	}
	if opt.Action == BatchRevert && m.Irreversible {
		return nil, fmt.Errorf("%w: migration irreversible tidak bisa dibatalkan: %s", domain.ErrConflict, m.IrreversibleReason)
	}
	stmtTimeout := opt.Timeout
	if stmtTimeout <= 0 {
		stmtTimeout = 60 * time.Second
	}

	base := obs.WithJobContext(context.WithoutCancel(ctx), obs.JobContext{JobID: opt.JobID, MigrationID: id})
	target, e := s.Targets.Conn(base, dbID)
	if e != nil {
		return nil, e
	}
	if dbmanager.IsMySQL(target) {
		return nil, fmt.Errorf("%w: mode batch hanya untuk PostgreSQL", domain.ErrInvalidInput)
	}
	conn, e := target.Conn(base)
	if e != nil {
		return nil, dbmanager.SanitizeError(e)
	}
	// Koneksi dibuang setelah dipakai (state session dari SQL user tidak
	// boleh kembali ke pool). Lock dilepas eksplisit dulu.
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1::integer, $2::integer)`, 3110, dbID)
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
	}()

	var locked bool
	if e := conn.QueryRowContext(base, `SELECT pg_try_advisory_lock($1::integer, $2::integer)`, 3110, dbID).Scan(&locked); e != nil {
		return nil, dbmanager.SanitizeError(e)
	}
	if !locked {
		slog.WarnContext(base, "lock database target dipegang migration lain", "event", "migration.lock", "status", "busy", "db_id", dbID, "mode", "batch")
		return nil, ErrTargetBusy
	}
	slog.InfoContext(base, "lock session database target diambil untuk seluruh job batch", "event", "migration.lock", "status", "acquired", "db_id", dbID, "mode", "batch")
	for _, q := range []string{batchTableSQL, batchScopeSQL} {
		if _, e := conn.ExecContext(base, q); e != nil {
			return nil, dbmanager.SanitizeError(e)
		}
	}

	b := &batchRun{s: s, conn: conn, dbID: dbID, m: m, opt: opt, timeout: stmtTimeout}
	b.table, e = quoteTable(m.BatchTable)
	if e != nil {
		return nil, e
	}
	b.column = pgx.Identifier{m.BatchColumn}.Sanitize()
	if opt.Action == BatchRevert {
		return b.revert(base)
	}
	return b.run(base, u)
}

type batchRun struct {
	s       *Store
	conn    *sql.Conn
	dbID    int64
	m       *Migration
	opt     BatchOptions
	timeout time.Duration
	table   string
	column  string
}

func quoteTable(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) == 0 || len(parts) > 2 {
		return "", fmt.Errorf("%w: nama tabel batch tidak valid", domain.ErrInvalidInput)
	}
	return pgx.Identifier(parts).Sanitize(), nil
}

// progress membaca checkpoint job ini dari database target.
func (b *batchRun) progress(ctx context.Context, fallbackCursor int64) (BatchProgress, error) {
	var p BatchProgress
	var cursor sql.NullInt64
	err := b.conn.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(rows),0), max(upper_bound)
		FROM _dbplatform_batches WHERE job_id = $1 AND reverted_at IS NULL`, b.opt.JobID).Scan(&p.Batches, &p.Rows, &cursor)
	p.Cursor = fallbackCursor
	if cursor.Valid {
		p.Cursor = cursor.Int64
	}
	return p, err
}

func (b *batchRun) report(ctx context.Context, p BatchProgress) error {
	if b.opt.OnBatch == nil {
		return nil
	}
	return b.opt.OnBatch(ctx, p)
}

func (b *batchRun) run(ctx context.Context, u *domain.User) (*BatchResult, error) {
	_, version, runID, e := b.s.start(ctx, u, b.dbID, b.m.ID, KindUp, b.opt.OnStart)
	if e != nil {
		return nil, e
	}
	ctx = obs.WithJobContext(ctx, obs.JobContext{ExecutionID: runID})
	res := &BatchResult{RunID: runID}
	finish := func(st BatchStatus, msg string) (*BatchResult, error) {
		res.Status, res.Message = st, msg
		result := StatusFailed
		if st == BatchSucceeded {
			result = StatusSuccess
		}
		book, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, e := b.s.finishRun(book, b.m.ID, b.dbID, runID, result, msg, nil); e != nil {
			return nil, e
		}
		return res, nil
	}

	// Cakupan target ditentukan sekali di awal job.
	var lower0, upper sql.NullInt64
	err := b.conn.QueryRowContext(ctx, `SELECT lower_bound, upper_bound FROM _dbplatform_batch_scope WHERE job_id = $1`, b.opt.JobID).Scan(&lower0, &upper)
	if errors.Is(err, sql.ErrNoRows) {
		err = b.conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(%s)::bigint - 1, max(%s)::bigint FROM %s`, b.column, b.column, b.table)).Scan(&lower0, &upper)
		if err == nil {
			_, err = b.conn.ExecContext(ctx, `INSERT INTO _dbplatform_batch_scope (job_id, lower_bound, upper_bound) VALUES ($1,$2,$3)`, b.opt.JobID, lower0, upper)
		}
	}
	if err != nil {
		return finish(BatchFailed, "Gagal menentukan cakupan batch: "+errorMessage(err))
	}
	res.Progress, err = b.progress(ctx, lower0.Int64)
	if err != nil {
		return finish(BatchFailed, "Gagal membaca checkpoint batch: "+errorMessage(err))
	}
	if err := b.report(ctx, res.Progress); err != nil {
		return finish(BatchPartial, "Job dihentikan: "+err.Error())
	}
	slog.InfoContext(ctx, "migration batch dimulai", "event", "migration.batch_start", "db_id", b.dbID,
		"version", version, "user", u.Username, "sql", b.m.SQL, "batch_size", b.m.BatchSize,
		"resume_from_batch", res.Progress.Batches+1, "cursor", res.Progress.Cursor)

	stopped := func(msg string) (*BatchResult, error) {
		if res.Progress.Batches > 0 {
			return finish(BatchPartial, msg)
		}
		return finish(BatchFailed, msg)
	}
	if upper.Valid {
		nextUpper := fmt.Sprintf(`SELECT max(c)::bigint FROM (SELECT %s AS c FROM %s WHERE %s > $1 AND %s <= $2 ORDER BY %s LIMIT $3) s`,
			b.column, b.table, b.column, b.column, b.column)
		for {
			var hi sql.NullInt64
			if err := b.conn.QueryRowContext(ctx, nextUpper, res.Progress.Cursor, upper.Int64, b.m.BatchSize).Scan(&hi); err != nil {
				return stopped(fmt.Sprintf("Batch %d gagal menentukan rentang: %s", res.Progress.Batches+1, errorMessage(err)))
			}
			if !hi.Valid {
				break
			}
			no := res.Progress.Batches + 1
			n, err := b.apply(obs.WithJobContext(ctx, obs.JobContext{Batch: no}), b.m.SQL, res.Progress.Cursor, hi.Int64, func(ctx context.Context, tx execer, rows int64) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO _dbplatform_batches (job_id, batch_no, lower_bound, upper_bound, rows) VALUES ($1,$2,$3,$4,$5)`,
					b.opt.JobID, no, res.Progress.Cursor, hi.Int64, rows)
				return err
			})
			if err != nil {
				// Hasil COMMIT bisa saja tidak pasti; checkpoint di target tetap
				// menjadi sumber kebenaran saat resume.
				if p, perr := b.progress(ctx, lower0.Int64); perr == nil {
					res.Progress = p
				}
				return stopped(fmt.Sprintf("Batch %d (%s %d..%d) gagal: %s", no, b.m.BatchColumn, res.Progress.Cursor+1, hi.Int64, errorMessage(err)))
			}
			res.Progress = BatchProgress{Batches: no, Rows: res.Progress.Rows + n, Cursor: hi.Int64}
			obs.RecordBatchProcessed(strconv.FormatInt(b.dbID, 10), int(n))
			if err := b.report(ctx, res.Progress); err != nil {
				return stopped("Job dihentikan: " + err.Error())
			}
		}
	}

	// Semua batch selesai: catat versi sebagai aktif di target.
	tx, err := b.conn.BeginTx(ctx, nil)
	if err == nil {
		ltx := dbmanager.WrapTx(tx, b.dbID, "")
		if _, err = ltx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _dbplatform_history(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err == nil {
			_, err = ltx.ExecContext(ctx, `INSERT INTO _dbplatform_history(version) VALUES($1)`, version)
		}
		if err == nil {
			err = ltx.CommitContext(ctx)
		} else {
			_ = ltx.RollbackContext(ctx)
		}
	}
	if err != nil {
		return stopped("Semua batch committed, tetapi versi gagal dicatat aktif: " + errorMessage(err))
	}
	slog.InfoContext(ctx, "migration batch selesai", "event", "migration.batch_done", "db_id", b.dbID,
		"batches", res.Progress.Batches, "rows", res.Progress.Rows)
	return finish(BatchSucceeded, "")
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// apply menjalankan satu batch di transaction sendiri: SQL user untuk
// rentang (lower, upper], lalu checkpoint, lalu COMMIT.
func (b *batchRun) apply(ctx context.Context, sqlText string, lower, upper int64, checkpoint func(context.Context, execer, int64) error) (int64, error) {
	tx, err := b.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	ltx := dbmanager.WrapTx(tx, b.dbID, "")
	if _, err = ltx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL statement_timeout='%dms'`, b.timeout.Milliseconds())); err != nil {
		_ = ltx.RollbackContext(ctx)
		return 0, err
	}
	r, err := ltx.ExecContext(ctx, sqlText, lower, upper)
	if err != nil {
		_ = ltx.RollbackContext(ctx)
		return 0, err
	}
	n, _ := r.RowsAffected()
	if err = checkpoint(ctx, ltx, n); err != nil {
		_ = ltx.RollbackContext(ctx)
		return 0, err
	}
	return n, ltx.CommitContext(ctx)
}

// revert membatalkan batch committed dengan SQL down dalam urutan batch
// terbalik. Setiap pembatalan satu transaction bersama penanda reverted_at
// (checkpoint pembatalan), jadi revert yang terhenti bisa dilanjutkan.
func (b *batchRun) revert(ctx context.Context) (*BatchResult, error) {
	res := &BatchResult{}
	rows, err := b.conn.QueryContext(ctx, `
		SELECT batch_no, lower_bound, upper_bound FROM _dbplatform_batches
		WHERE job_id = $1 AND reverted_at IS NULL ORDER BY batch_no DESC`, b.opt.JobID)
	if err != nil {
		return nil, dbmanager.SanitizeError(err)
	}
	type span struct {
		no           int
		lower, upper int64
	}
	var todo []span // jumlah batch, bukan jumlah row
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.no, &sp.lower, &sp.upper); err != nil {
			rows.Close()
			return nil, err
		}
		todo = append(todo, sp)
	}
	rows.Close()
	var reverted int
	if err := b.conn.QueryRowContext(ctx, `SELECT count(*) FROM _dbplatform_batches WHERE job_id = $1 AND reverted_at IS NOT NULL`, b.opt.JobID).Scan(&reverted); err != nil {
		return nil, err
	}
	res.Progress, _ = b.progress(ctx, 0)
	slog.InfoContext(ctx, "pembatalan migration batch dimulai", "event", "migration.batch_revert_start",
		"db_id", b.dbID, "batches", len(todo), "sql", b.m.SQLDown)

	partial := func(msg string) (*BatchResult, error) {
		res.Status, res.Message = BatchPartial, msg
		if reverted > 0 {
			res.Status = BatchPartiallyReverted
		}
		return res, nil
	}
	for _, sp := range todo {
		_, err := b.apply(obs.WithJobContext(ctx, obs.JobContext{Batch: sp.no}), b.m.SQLDown, sp.lower, sp.upper, func(ctx context.Context, tx execer, _ int64) error {
			_, err := tx.ExecContext(ctx, `UPDATE _dbplatform_batches SET reverted_at = now() WHERE job_id = $1 AND batch_no = $2`, b.opt.JobID, sp.no)
			return err
		})
		if p, perr := b.progress(ctx, 0); perr == nil {
			res.Progress = p
		}
		if err != nil {
			return partial(fmt.Sprintf("Pembatalan batch %d gagal: %s", sp.no, errorMessage(err)))
		}
		reverted++
		if err := b.report(ctx, res.Progress); err != nil {
			return partial("Job dihentikan: " + err.Error())
		}
	}
	res.Status = BatchReverted
	slog.InfoContext(ctx, "semua batch dibatalkan", "event", "migration.batch_reverted", "db_id", b.dbID, "batches", reverted)
	return res, nil
}
