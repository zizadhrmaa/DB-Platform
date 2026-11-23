package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

type Store struct {
	DB      *sql.DB
	Targets dbmanager.Manager
	Authz   authz.Authorizer

	// commit menggantikan tx.Commit di test untuk meniru COMMIT tanpa jawaban.
	commit func(*sql.Tx) error
}

var _ Service = (*Store)(nil)

// beginUserTx membuka transaction di koneksi khusus untuk SQL dari user.
// SQL user bisa meninggalkan state session, misalnya SET search_path atau
// pg_advisory_lock, yang tidak ikut hilang saat commit atau rollback. Jadi
// koneksinya ditutup lewat release, bukan dikembalikan ke pool.
func beginUserTx(ctx context.Context, target *sql.DB) (*sql.Tx, func(), error) {
	conn, e := target.Conn(ctx)
	if e != nil {
		return nil, nil, dbmanager.SanitizeError(e)
	}
	release := func() {
		conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
	}
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		release()
		return nil, nil, dbmanager.SanitizeError(e)
	}
	return tx, release, nil
}

const columns = `m.id, m.db_id, m.version, m.revision, m.title, m.sql_text, COALESCE(m.sql_down,''), m.irreversible, COALESCE(m.irreversible_reason,''), m.exec_mode, COALESCE(m.batch_table,''), COALESCE(m.batch_column,''), COALESCE(m.batch_size,0), m.status, COALESCE(creator.username,''),m.created_at,m.updated_at,COALESCE(executor.username,''),m.executed_at,m.finished_at,COALESCE(m.error_message,'')`
const joins = ` FROM migrations m LEFT JOIN users creator ON creator.id=m.created_by LEFT JOIN users executor ON executor.id=m.executed_by `

func scan(s interface{ Scan(...any) error }) (*Migration, error) {
	var m Migration
	err := s.Scan(&m.ID, &m.DBID, &m.Version, &m.Revision, &m.Title, &m.SQL, &m.SQLDown, &m.Irreversible, &m.IrreversibleReason, &m.ExecMode, &m.BatchTable, &m.BatchColumn, &m.BatchSize, &m.Status, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.ExecutedBy, &m.ExecutedAt, &m.FinishedAt, &m.ErrorMessage)
	return &m, err
}
func (s *Store) List(ctx context.Context, dbID int64) ([]Migration, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT `+columns+joins+`WHERE m.db_id=$1 ORDER BY m.version`, dbID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Migration{}
	for rows.Next() {
		m, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, dbID, id int64) (*Migration, error) {
	m, e := scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+joins+`WHERE m.db_id=$1 AND m.id=$2`, dbID, id))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return m, e
}
func (s *Store) Runs(ctx context.Context, dbID, id int64) ([]Run, error) {
	if _, e := s.Get(ctx, dbID, id); e != nil {
		return nil, e
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM migration_runs r JOIN migrations m ON m.id=r.migration_id LEFT JOIN users u ON u.id=r.executed_by WHERE m.db_id=$1 AND r.migration_id=$2 ORDER BY r.started_at DESC,r.id DESC`, dbID, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// legacyIrreversibleReason dipakai migration yang dibuat lewat halaman lama,
// yang tidak punya kolom SQL down.
const legacyIrreversibleReason = "Dibuat lewat halaman lama tanpa SQL down."

// Create dipakai halaman lama. Karena tidak ada SQL down, migration
// ditandai irreversible.
func (s *Store) Create(ctx context.Context, u *domain.User, dbID int64, title, sqlText string) (*Migration, error) {
	return s.CreateDraft(ctx, u, dbID, Draft{Title: title, SQLUp: sqlText, Irreversible: true, IrreversibleReason: legacyIrreversibleReason})
}

// CreateDraft menghitung versi baru (max+1) di dalam transaction yang
// mengunci baris db_connections.
func (s *Store) CreateDraft(ctx context.Context, u *domain.User, dbID int64, d Draft) (*Migration, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	d.normalize()
	if dbID <= 0 {
		return nil, domain.ErrInvalidInput
	}
	if e := d.validate(); e != nil {
		return nil, e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var lockID int64
	e = tx.QueryRowContext(ctx, `SELECT id FROM db_connections WHERE id=$1 AND is_active=true FOR UPDATE`, dbID).Scan(&lockID)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	var v int
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM migrations WHERE db_id=$1`, dbID).Scan(&v)
	if e != nil {
		return nil, e
	}
	var id int64
	e = tx.QueryRowContext(ctx, `
		INSERT INTO migrations(db_id,version,title,sql_text,sql_down,irreversible,irreversible_reason,status,created_by,
		                       exec_mode,batch_table,batch_column,batch_size)
		VALUES($1,$2,$3,$4,NULLIF($5,''),$6,NULLIF($7,''),'pending',$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,0)) RETURNING id`,
		dbID, v, d.Title, d.SQLUp, d.SQLDown, d.Irreversible, d.IrreversibleReason, u.ID,
		d.ExecMode, d.BatchTable, d.BatchColumn, d.BatchSize).Scan(&id)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return s.Get(ctx, dbID, id)
}

// Update dipakai halaman lama: hanya judul dan SQL up yang diubah, SQL down
// dan tanda irreversible tetap seperti sebelumnya.
func (s *Store) Update(ctx context.Context, u *domain.User, dbID, id int64, title, sqlText string) error {
	if u == nil {
		return domain.ErrUnauthenticated
	}
	if strings.TrimSpace(title) == "" || id <= 0 || dbID <= 0 {
		return domain.ErrInvalidInput
	}
	m, e := s.Get(ctx, dbID, id)
	if e != nil {
		return e
	}
	return s.UpdateDraft(ctx, u, dbID, id, Draft{
		Title: title, SQLUp: sqlText, SQLDown: m.SQLDown,
		Irreversible: m.Irreversible, IrreversibleReason: m.IrreversibleReason,
		ExecMode: m.ExecMode, BatchTable: m.BatchTable, BatchColumn: m.BatchColumn, BatchSize: m.BatchSize,
	})
}

// UpdateDraft hanya untuk migration pending atau failed yang belum masuk
// request perubahan atau antrean. SQL yang pernah dieksekusi sampai sukses
// tidak pernah bisa diubah; perbaikannya lewat migration baru. Setiap
// perubahan menaikkan revisi, dan setiap attempt mencatat revisi serta
// salinan SQL yang dijalankan.
func (s *Store) UpdateDraft(ctx context.Context, u *domain.User, dbID, id int64, d Draft) error {
	if u == nil {
		return domain.ErrUnauthenticated
	}
	d.normalize()
	if id <= 0 || dbID <= 0 {
		return domain.ErrInvalidInput
	}
	if e := d.validate(); e != nil {
		return e
	}
	r, e := s.DB.ExecContext(ctx, `
		UPDATE migrations SET title=$1, sql_text=$2, sql_down=NULLIF($3,''), irreversible=$4,
		       irreversible_reason=NULLIF($5,''), updated_by=$6, updated_at=now(), revision=revision+1,
		       exec_mode=$9, batch_table=NULLIF($10,''), batch_column=NULLIF($11,''), batch_size=NULLIF($12,0)
		WHERE id=$7 AND db_id=$8 AND status IN ('pending','failed')
		  AND NOT EXISTS (
			SELECT 1 FROM change_requests c
			WHERE c.migration_id = migrations.id AND c.status IN ('draft','pending_review','approved'))
		  AND NOT EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.migration_id = migrations.id
			  AND j.status IN ('queued','running','uncertain','partial','partially_reverted'))`,
		d.Title, d.SQLUp, d.SQLDown, d.Irreversible, d.IrreversibleReason, u.ID, id, dbID,
		d.ExecMode, d.BatchTable, d.BatchColumn, d.BatchSize)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		m, e := s.Get(ctx, dbID, id)
		if e != nil {
			return e
		}
		if !m.Status.Editable() {
			return fmt.Errorf("%w: migration berstatus %s tidak bisa diubah", domain.ErrConflict, m.Status)
		}
		return fmt.Errorf("%w: migration sedang dalam request perubahan atau antrean, jadi tidak bisa diubah", domain.ErrConflict)
	}
	return nil
}
func (s *Store) Discard(ctx context.Context, u *domain.User, dbID, id int64) error {
	if u == nil {
		return domain.ErrUnauthenticated
	}
	if dbID <= 0 || id <= 0 {
		return domain.ErrInvalidInput
	}
	r, e := s.DB.ExecContext(ctx, `
		UPDATE migrations SET status='discarded', updated_by=$1, updated_at=now()
		WHERE id=$2 AND db_id=$3 AND status IN ('pending','failed','rolled_back')
		  AND NOT EXISTS (
			SELECT 1 FROM change_requests c
			WHERE c.migration_id = migrations.id AND c.status IN ('draft','pending_review','approved','queued'))
		  AND NOT EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.migration_id = migrations.id
			  AND j.status IN ('queued','running','uncertain','partial','partially_reverted'))`, u.ID, id, dbID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		m, e := s.Get(ctx, dbID, id)
		if e != nil {
			return e
		}
		if !m.Status.Discardable() {
			return fmt.Errorf("%w: migration berstatus %s tidak bisa dibatalkan", domain.ErrConflict, m.Status)
		}
		return fmt.Errorf("%w: migration sedang dalam request perubahan atau antrean, jadi tidak bisa dibatalkan", domain.ErrConflict)
	}
	return nil
}

// Error hasil eksekusi yang perlu dibedakan worker. Masing-masing
// membungkus error domain supaya httpx.StatusFor tetap bisa memetakannya.
// errNotApplied: rollback ditolak karena versi tidak tercatat aktif di
// database target, misalnya sudah di-rollback di luar platform.
var errNotApplied = errors.New("versi migration tidak tercatat aktif di database target, rollback dibatalkan")

var (
	// ErrTargetBusy: lock eksekusi di database target sedang dipegang.
	// Belum ada attempt yang tercatat, jadi aman dicoba lagi.
	ErrTargetBusy = fmt.Errorf("%w: ada migration lain yang sedang dijalankan di database ini", domain.ErrConflict)
	// ErrInterrupted: koneksi ke target putus sebelum COMMIT, atau setelah
	// COMMIT tanpa jawaban tetapi bukti menunjukkan tidak committed. Attempt
	// sudah dicatat failed dan perubahan pasti tidak masuk, jadi aman dicoba lagi.
	ErrInterrupted = fmt.Errorf("%w: koneksi ke database target terputus sebelum perubahan di-commit", domain.ErrUnavailable)
	// ErrUncertain: COMMIT tidak dijawab dan bukti di target belum bisa
	// diperiksa. Migration dibiarkan running supaya versi berikutnya
	// tertahan sampai pemulihan memastikan hasilnya.
	ErrUncertain = fmt.Errorf("%w: hasil commit migration belum pasti dan sedang menunggu pemeriksaan", domain.ErrUnavailable)
)

// ExecOptions mengatur eksekusi yang dijalankan worker.
type ExecOptions struct {
	// Kind adalah jenis eksekusi; kosong berarti up.
	Kind Kind
	// Timeout seluruh eksekusi, juga dipakai sebagai statement_timeout.
	// 0 berarti 90 detik dengan statement_timeout 60 detik (perilaku lama).
	Timeout time.Duration
	// OnStart dipanggil di dalam transaction platform yang mencatat
	// migration_runs, sebelum transaction itu di-commit dan sebelum SQL
	// dijalankan di target. Error dari OnStart membatalkan pencatatan dan
	// SQL tidak dijalankan. Worker memakainya untuk menulis jobs.run_id
	// secara atomik dengan attempt.
	OnStart func(ctx context.Context, tx *sql.Tx, runID int64) error
}

// Execute menjalankan SQL migration di database target secara sinkron.
// Perilakunya sama seperti sebelum ada worker: koneksi yang putus sebelum
// COMMIT dicatat sebagai run failed tanpa error.
func (s *Store) Execute(ctx context.Context, u *domain.User, dbID, id int64) (*Run, error) {
	run, err := s.ExecuteWith(ctx, u, dbID, id, ExecOptions{})
	if errors.Is(err, ErrInterrupted) && run != nil {
		return run, nil
	}
	return run, err
}

// ExecuteWith menjalankan SQL migration di database target.
//
// Urutannya: kunci advisory di target diambil dulu, baru status diubah ke
// running di database platform. Jadi status running hanya ada selama kunci
// itu dipegang, dan Recover bisa membedakan eksekusi yang masih berjalan
// dengan eksekusi yang terputus.
//
// Hasil:
//   - run success atau failed tanpa error: SQL selesai (failed berarti SQL
//     ditolak server, termasuk COMMIT yang ditolak, atau melewati batas
//     waktu). Tidak perlu dicoba lagi.
//   - ErrTargetBusy, ErrInterrupted, atau error domain.ErrUnavailable lain:
//     kegagalan sementara, perubahan pasti tidak masuk.
//   - ErrUncertain: COMMIT tidak dijawab dan bukti belum bisa diperiksa.
//   - error domain lain (urutan, akses, tidak ditemukan): ditolak.
func (s *Store) ExecuteWith(ctx context.Context, u *domain.User, dbID, id int64, opt ExecOptions) (*Run, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if s.Authz == nil || s.Targets == nil {
		return nil, fmt.Errorf("execute dependencies not initialized")
	}
	if e := s.Authz.Authorize(ctx, u, dbID, authz.ActionExecuteMigration); e != nil {
		return nil, e
	}
	timeout, stmtTimeout := opt.Timeout, opt.Timeout
	if timeout <= 0 {
		timeout, stmtTimeout = 90*time.Second, 60*time.Second
	}
	// Context terpisah dari pembatalan pemanggil supaya pencatatan tetap
	// selesai walaupun user menutup tab atau server mulai shutdown.
	base := context.WithoutCancel(ctx)
	work, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	target, e := s.Targets.Conn(work, dbID)
	if e != nil {
		return nil, e
	}

	tx, release, e := beginUserTx(work, target)
	if e != nil {
		return nil, e
	}
	defer release()
	defer tx.Rollback()
	isMy := dbmanager.IsMySQL(target)
	if !isMy {
		if _, e = tx.ExecContext(work, `SET LOCAL lock_timeout='5s'`); e != nil {
			return nil, dbmanager.SanitizeError(e)
		}
		if _, e = tx.ExecContext(work, fmt.Sprintf(`SET LOCAL statement_timeout='%dms'`, stmtTimeout.Milliseconds())); e != nil {
			return nil, dbmanager.SanitizeError(e)
		}
	}
	lockStart := time.Now()
	lockCtx, lockSpan := obs.StartSpan(work, "migration.lock", trace.WithAttributes(attribute.Int64("db.id", dbID)))
	locked, e := tryLockTarget(lockCtx, tx, dbID, isMy)
	obs.RecordLockWait(time.Since(lockStart))
	obs.RecordError(lockSpan, e)
	lockSpan.End()
	if e != nil {
		slog.WarnContext(work, "lock database target gagal diambil", "event", "migration.lock", "status", "error", "db_id", dbID)
		return nil, e
	}
	if !locked {
		slog.WarnContext(work, "lock database target dipegang migration lain", "event", "migration.lock", "status", "busy", "db_id", dbID)
		return nil, ErrTargetBusy
	}
	slog.InfoContext(work, "lock database target diambil", "event", "migration.lock", "status", "acquired", "db_id", dbID)
	if isMy {
		defer unlockTarget(work, tx, dbID, isMy)
	}
	if e = s.reconcile(work, tx, dbID, isMy); e != nil {
		return nil, e
	}

	kind := opt.Kind
	if kind == "" {
		kind = KindUp
	}
	sqlText, version, runID, e := s.start(work, u, dbID, id, kind, opt.OnStart)
	if e != nil {
		return nil, e
	}
	// Semua log setelah ini membawa execution ID attempt ini.
	jc := obs.JobContext{ExecutionID: runID, MigrationID: id}
	base, work = obs.WithJobContext(base, jc), obs.WithJobContext(work, jc)
	slog.InfoContext(work, "attempt migration dimulai", "event", "migration.attempt_start",
		"db_id", dbID, "version", version, "kind", kind, "user", u.Username, "sql", sqlText)

	// Pencatatan hasil memakai context sendiri: work bisa saja sudah habis
	// karena timeout eksekusi.
	book, cancelBook := context.WithTimeout(base, 15*time.Second)
	defer cancelBook()

	ltx := dbmanager.WrapTx(tx, dbID, "")

	execErr := func() error {
		historyTableSQL := `CREATE TABLE IF NOT EXISTS _dbplatform_history(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`
		insertSQL := `INSERT INTO _dbplatform_history(version) VALUES($1)`
		if isMy {
			historyTableSQL = `CREATE TABLE IF NOT EXISTS _dbplatform_history(version INT PRIMARY KEY,applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`
			insertSQL = `INSERT INTO _dbplatform_history(version) VALUES(?)`
		}
		// _dbplatform_history berisi versi yang sedang aktif di target dan
		// ditulis dalam transaction yang sama dengan SQL migration, jadi
		// menjadi bukti commit. up/forward menambah versi (primary key
		// mencegah penerapan ganda); rollback menghapusnya dan harus tepat
		// satu baris (mencegah rollback ganda).
		if kind == KindRollback {
			deleteSQL := `DELETE FROM _dbplatform_history WHERE version=$1`
			if isMy {
				deleteSQL = `DELETE FROM _dbplatform_history WHERE version=?`
			}
			res, e := ltx.ExecContext(work, deleteSQL, version)
			if e != nil {
				return e
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return errNotApplied
			}
		} else {
			if _, e := ltx.ExecContext(work, historyTableSQL); e != nil {
				return e
			}
			if _, e := ltx.ExecContext(work, insertSQL, version); e != nil {
				return e
			}
		}
		_, e := ltx.ExecContext(work, sqlText)
		return e
	}()
	var commitErr error
	if execErr == nil {
		commitErr = s.commitTx(book, tx, ltx)
	} else {
		_ = ltx.RollbackContext(book)
	}

	switch {
	case execErr == nil && commitErr == nil:
		return s.finishRun(book, id, dbID, runID, StatusSuccess, "", nil)
	case execErr != nil:
		if serverRejected(execErr) || errors.Is(execErr, context.DeadlineExceeded) || errors.Is(execErr, errNotApplied) {
			return s.finishRun(book, id, dbID, runID, StatusFailed, errorMessage(execErr), nil)
		}
		// Koneksi putus sebelum COMMIT: transaction pasti batal.
		return s.finishRun(book, id, dbID, runID, StatusFailed, interruptedMessage, ErrInterrupted)
	case serverRejected(commitErr):
		// Server menjawab COMMIT dengan error (misalnya constraint deferred):
		// transaction batal, ini kegagalan biasa.
		return s.finishRun(book, id, dbID, runID, StatusFailed, errorMessage(commitErr), nil)
	}

	// COMMIT tanpa jawaban. Periksa bukti di target sebelum memutuskan.
	if !s.verifyAfterCommit(book, dbID, isMy) {
		slog.WarnContext(book, "hasil attempt migration belum pasti", "event", "migration.attempt_result",
			"status", "uncertain", "db_id", dbID, "error", "COMMIT tidak dijawab dan bukti belum bisa diperiksa")
		run, _ := s.runByID(book, id, runID)
		return run, ErrUncertain
	}
	run, e := s.runByID(book, id, runID)
	if e != nil {
		return nil, e
	}
	if run.Status == StatusSuccess {
		return run, nil
	}
	return run, ErrInterrupted
}

// commitTx bisa diganti test untuk meniru COMMIT yang tidak dijawab.
func (s *Store) commitTx(ctx context.Context, tx *sql.Tx, ltx *dbmanager.LoggedTx) error {
	if s.commit != nil {
		return s.commit(tx)
	}
	return ltx.CommitContext(ctx)
}

// serverRejected berarti server database menjawab dengan error SQL biasa.
// Error kelas 08 (koneksi) dan 57P (server dimatikan atau koneksi diputus
// administrator) dianggap koneksi putus, bukan kesalahan SQL user.
func serverRejected(e error) bool {
	var me *mysql.MySQLError
	if errors.As(e, &me) {
		return true
	}
	var pe *pgconn.PgError
	if !errors.As(e, &pe) {
		return false
	}
	return !strings.HasPrefix(pe.Code, "08") && !strings.HasPrefix(pe.Code, "57P")
}

// verifyAfterCommit mengambil lock target lewat koneksi baru lalu
// membereskan migration running dengan bukti _dbplatform_history. Hasil
// false berarti bukti belum bisa diperiksa: lock masih dipegang (transaction
// lama mungkin masih berjalan di server) atau target tidak terjangkau.
func (s *Store) verifyAfterCommit(ctx context.Context, dbID int64, isMy bool) bool {
	target, e := s.Targets.Conn(ctx, dbID)
	if e != nil {
		return false
	}
	tx, e := target.BeginTx(ctx, nil)
	if e != nil {
		return false
	}
	defer tx.Rollback()
	locked, e := tryLockTarget(ctx, tx, dbID, isMy)
	if e != nil || !locked {
		return false
	}
	if isMy {
		defer unlockTarget(ctx, tx, dbID, isMy)
	}
	return s.reconcile(ctx, tx, dbID, isMy) == nil
}

func (s *Store) finishRun(ctx context.Context, id, dbID, runID int64, status Status, message string, result error) (*Run, error) {
	if e := s.finish(ctx, id, dbID, runID, status, message); e != nil {
		return nil, e
	}
	level := slog.LevelInfo
	if status != StatusSuccess {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "hasil attempt migration", "event", "migration.attempt_result",
		"status", status, "db_id", dbID, "error", message)
	run, e := s.runByID(ctx, id, runID)
	if e != nil {
		return nil, e
	}
	return run, result
}

const runColumns = `r.id,r.migration_id,r.kind,r.revision,r.reverts_run_id,r.sql_text,COALESCE(u.username,''),r.started_at,r.finished_at,r.status,COALESCE(r.error_message,'')`

func scanRun(s interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	if e := s.Scan(&r.ID, &r.MigrationID, &r.Kind, &r.Revision, &r.RevertsRunID, &r.SQL, &r.ExecutedBy, &r.StartedAt, &r.FinishedAt, &r.Status, &r.ErrorMessage); e != nil {
		return nil, e
	}
	return &r, nil
}

func (s *Store) runByID(ctx context.Context, migrationID, runID int64) (*Run, error) {
	r, err := scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+`
		FROM migration_runs r LEFT JOIN users u ON u.id=r.executed_by
		WHERE r.id=$1 AND r.migration_id=$2`, runID, migrationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return r, err
}

// History mengembalikan riwayat eksekusi database, urut waktu mulai.
// Semua attempt yang sudah selesai, berhasil maupun gagal, diambil dari
// migration_history (tidak bisa diubah). Dengan hideRollback, pasangan
// eksekusi yang dibatalkan dan rollback suksesnya disembunyikan; datanya
// sendiri tidak berubah.
func (s *Store) History(ctx context.Context, dbID int64, hideRollback bool) ([]HistoryEntry, error) {
	rows, e := s.DB.QueryContext(ctx, `
		SELECT h.execution_id, h.migration_id, h.version, h.revision, h.title, h.kind, h.status,
		       h.sql_text, h.executed_by_username, h.started_at, h.committed_at, h.reverts_execution_id,
		       COALESCE(h.error_message,'')
		FROM migration_history h
		WHERE h.db_id = $1
		ORDER BY h.started_at, h.execution_id`, dbID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if e := rows.Scan(&h.ExecutionID, &h.MigrationID, &h.Version, &h.Revision, &h.Title, &h.Kind, &h.Status,
			&h.SQL, &h.ExecutedBy, &h.StartedAt, &h.FinishedAt, &h.RevertsExecutionID, &h.ErrorMessage); e != nil {
			return nil, e
		}
		out = append(out, h)
	}
	if e := rows.Err(); e != nil {
		return nil, e
	}
	if hideRollback {
		out = HideRolledBack(out)
	}
	return out, nil
}

// start mengunci migration database ini, memastikan id boleh dieksekusi
// dengan jenis kind, lalu menandainya running dan mencatat attempt beserta
// salinan SQL dan revisinya. Untuk rollback, SQL yang dijalankan adalah SQL
// down migration itu (tidak bisa diubah setelah dieksekusi), dan attempt
// merujuk eksekusi up/forward yang dibatalkan.
func (s *Store) start(work context.Context, u *domain.User, dbID, id int64, kind Kind, onStart func(context.Context, *sql.Tx, int64) error) (string, int, int64, error) {
	platformTx, e := s.DB.BeginTx(work, nil)
	if e != nil {
		return "", 0, 0, e
	}
	defer platformTx.Rollback()
	var locked int64
	e = platformTx.QueryRowContext(work, `SELECT id FROM db_connections WHERE id=$1 AND is_active=true FOR UPDATE`, dbID).Scan(&locked)
	if errors.Is(e, sql.ErrNoRows) {
		return "", 0, 0, domain.ErrNotFound
	}
	if e != nil {
		return "", 0, 0, e
	}
	rows, e := platformTx.QueryContext(work, `SELECT id,version,status,irreversible,COALESCE(irreversible_reason,''),exec_mode FROM migrations WHERE db_id=$1 ORDER BY version FOR UPDATE`, dbID)
	if e != nil {
		return "", 0, 0, e
	}
	list := []Migration{}
	for rows.Next() {
		var m Migration
		if e = rows.Scan(&m.ID, &m.Version, &m.Status, &m.Irreversible, &m.IrreversibleReason, &m.ExecMode); e != nil {
			rows.Close()
			return "", 0, 0, e
		}
		list = append(list, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", 0, 0, e
	}
	if e = CheckRunnable(list, id, kind); e != nil {
		return "", 0, 0, e
	}
	var up, down string
	var version, revision int
	e = platformTx.QueryRowContext(work, `SELECT sql_text,COALESCE(sql_down,''),version,revision FROM migrations WHERE id=$1 AND db_id=$2`, id, dbID).Scan(&up, &down, &version, &revision)
	if e != nil {
		return "", 0, 0, e
	}
	sqlText := up
	var reverts *int64
	if kind == KindRollback {
		sqlText = down
		var rid int64
		e = platformTx.QueryRowContext(work, `
			SELECT id FROM migration_runs
			WHERE migration_id=$1 AND status='success' AND kind IN ('up','forward')
			ORDER BY id DESC LIMIT 1`, id).Scan(&rid)
		if errors.Is(e, sql.ErrNoRows) {
			return "", 0, 0, fmt.Errorf("%w: eksekusi yang akan di-rollback tidak ditemukan", domain.ErrConflict)
		}
		if e != nil {
			return "", 0, 0, e
		}
		reverts = &rid
	}
	_, e = platformTx.ExecContext(work, `UPDATE migrations SET status='running',executed_by=$1,executed_at=now(),finished_at=NULL,error_message=NULL WHERE id=$2 AND db_id=$3`, u.ID, id, dbID)
	if e != nil {
		return "", 0, 0, e
	}
	var runID int64
	e = platformTx.QueryRowContext(work, `
		INSERT INTO migration_runs(migration_id,kind,revision,reverts_run_id,sql_text,executed_by,status)
		VALUES($1,$2,$3,$4,$5,$6,'running') RETURNING id`, id, kind, revision, reverts, sqlText, u.ID).Scan(&runID)
	if e != nil {
		return "", 0, 0, e
	}
	if onStart != nil {
		if e = onStart(work, platformTx, runID); e != nil {
			return "", 0, 0, e
		}
	}
	return sqlText, version, runID, platformTx.Commit()
}

// finish menulis hasil attempt runID. result adalah hasil attempt (success
// atau failed); status migration diturunkan dari jenis eksekusinya.
func (s *Store) finish(work context.Context, id, dbID, runID int64, result Status, message string) error {
	tx, e := s.DB.BeginTx(work, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = settle(work, tx, id, dbID, runID, result, message); e != nil {
		return e
	}
	return tx.Commit()
}

// settle mengubah attempt yang masih running menjadi result, status
// migration sesuai jenis eksekusi, dan untuk attempt yang committed
// menambah migration_history dalam transaction yang sama. Attempt yang
// sudah tidak running (sudah dibereskan pemulihan) tidak diubah lagi.
func settle(ctx context.Context, tx *sql.Tx, id, dbID, runID int64, result Status, message string) error {
	var kind Kind
	e := tx.QueryRowContext(ctx, `SELECT kind FROM migration_runs WHERE id=$1 AND migration_id=$2 FOR UPDATE`, runID, id).Scan(&kind)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, `UPDATE migration_runs SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE id=$3 AND status='running'`, result, message, runID)
	if e != nil {
		return e
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil
	}
	_, e = tx.ExecContext(ctx, `UPDATE migrations SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE id=$3 AND db_id=$4 AND status='running'`,
		migrationStatusAfter(kind, result), message, id, dbID)
	if e != nil {
		return e
	}
	return appendHistory(ctx, tx, runID)
}

// appendHistory menyalin attempt runID yang sudah selesai (success atau
// failed) ke migration_history. Dipanggil dalam transaction yang sama
// dengan perubahan status attempt, sehingga setiap attempt yang selesai
// pasti tercatat tepat sekali.
func appendHistory(ctx context.Context, tx *sql.Tx, runID int64) error {
	_, e := tx.ExecContext(ctx, `
		INSERT INTO migration_history (execution_id, db_id, migration_id, version, revision, title, kind,
		                               sql_text, executed_by, executed_by_username, started_at, committed_at,
		                               reverts_execution_id, status, error_message)
		SELECT r.id, m.db_id, m.id, m.version, r.revision, m.title, r.kind,
		       r.sql_text, r.executed_by, COALESCE(u.username,''), r.started_at, COALESCE(r.finished_at, now()),
		       r.reverts_run_id, r.status, r.error_message
		FROM migration_runs r
		JOIN migrations m ON m.id = r.migration_id
		LEFT JOIN users u ON u.id = r.executed_by
		WHERE r.id = $1 AND r.status IN ('success','failed')`, runID)
	return e
}

// errorMessage mengambil pesan error PostgreSQL. Error lain, misalnya
// koneksi putus, diganti pesan umum supaya detail koneksi tidak tersimpan.
func errorMessage(e error) string {
	if errors.Is(e, errNotApplied) {
		return "Versi migration tidak tercatat aktif di database target, jadi rollback dibatalkan."
	}
	var me *mysql.MySQLError
	if errors.As(e, &me) {
		return fmt.Sprintf("MySQL Error %d: %s", me.Number, me.Message)
	}
	var pe *pgconn.PgError
	if errors.As(e, &pe) {
		msg := pe.Message
		if pe.Detail != "" {
			msg += ". Detail: " + pe.Detail
		}
		if pe.Position > 0 {
			msg += fmt.Sprintf(" (posisi %d)", pe.Position)
		}
		return msg
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "Eksekusi melewati batas waktu."
	}
	return "Koneksi ke database target terputus saat eksekusi."
}
