package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Store struct {
	DB      *sql.DB
	Targets dbmanager.Manager
	Authz   authz.Authorizer
}

var _ Service = (*Store)(nil)

const columns = `m.id, m.db_id, m.version, m.title, m.sql_text, m.status, COALESCE(creator.username,''),m.created_at,m.updated_at,COALESCE(executor.username,''),m.executed_at,m.finished_at,COALESCE(m.error_message,'')`
const joins = ` FROM migrations m LEFT JOIN users creator ON creator.id=m.created_by LEFT JOIN users executor ON executor.id=m.executed_by `

func scan(s interface{ Scan(...any) error }) (*Migration, error) {
	var m Migration
	err := s.Scan(&m.ID, &m.DBID, &m.Version, &m.Title, &m.SQL, &m.Status, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.ExecutedBy, &m.ExecutedAt, &m.FinishedAt, &m.ErrorMessage)
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
	rows, e := s.DB.QueryContext(ctx, `SELECT r.id,r.migration_id,r.sql_text,COALESCE(u.username,''),r.started_at,r.finished_at,r.status,COALESCE(r.error_message,'') FROM migration_runs r JOIN migrations m ON m.id=r.migration_id LEFT JOIN users u ON u.id=r.executed_by WHERE m.db_id=$1 AND r.migration_id=$2 ORDER BY r.started_at DESC,r.id DESC`, dbID, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if e = rows.Scan(&r.ID, &r.MigrationID, &r.SQL, &r.ExecutedBy, &r.StartedAt, &r.FinishedAt, &r.Status, &r.ErrorMessage); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Create(ctx context.Context, u *domain.User, dbID int64, title, sqlText string) (*Migration, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	title = strings.TrimSpace(title)
	if dbID <= 0 || title == "" {
		return nil, domain.ErrInvalidInput
	}
	if e := ValidateSQL(sqlText); e != nil {
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
	e = tx.QueryRowContext(ctx, `INSERT INTO migrations(db_id,version,title,sql_text,status,created_by) VALUES($1,$2,$3,$4,'pending',$5) RETURNING id`, dbID, v, title, sqlText, u.ID).Scan(&id)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return s.Get(ctx, dbID, id)
}
func (s *Store) Update(ctx context.Context, u *domain.User, dbID, id int64, title, sqlText string) error {
	if u == nil {
		return domain.ErrUnauthenticated
	}
	title = strings.TrimSpace(title)
	if title == "" || id <= 0 || dbID <= 0 {
		return domain.ErrInvalidInput
	}
	if e := ValidateSQL(sqlText); e != nil {
		return e
	}
	r, e := s.DB.ExecContext(ctx, `UPDATE migrations SET title=$1,sql_text=$2,updated_by=$3,updated_at=now() WHERE id=$4 AND db_id=$5 AND status IN('pending','failed')`, title, sqlText, u.ID, id, dbID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		m, e := s.Get(ctx, dbID, id)
		if e != nil {
			return e
		}
		return fmt.Errorf("%w: migration berstatus %s tidak bisa diubah", domain.ErrConflict, m.Status)
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
	r, e := s.DB.ExecContext(ctx, `UPDATE migrations SET status='discarded',updated_by=$1,updated_at=now() WHERE id=$2 AND db_id=$3 AND status IN('pending','failed')`, u.ID, id, dbID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		m, e := s.Get(ctx, dbID, id)
		if e != nil {
			return e
		}
		return fmt.Errorf("%w: migration berstatus %s tidak bisa dibatalkan", domain.ErrConflict, m.Status)
	}
	return nil
}

// Execute menjalankan SQL migration di database target.
//
// Urutannya: kunci advisory di target diambil dulu, baru status diubah ke
// running di database platform. Jadi status running hanya ada selama kunci
// itu dipegang, dan Recover bisa membedakan eksekusi yang masih berjalan
// dengan eksekusi yang terputus.
func (s *Store) Execute(ctx context.Context, u *domain.User, dbID, id int64) (*Run, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if s.Authz == nil || s.Targets == nil {
		return nil, fmt.Errorf("execute dependencies not initialized")
	}
	if e := s.Authz.Authorize(ctx, u, dbID, authz.ActionExecuteMigration); e != nil {
		return nil, e
	}
	// Context terpisah supaya pencatatan tetap selesai walaupun user menutup tab.
	work, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	target, e := s.Targets.Conn(work, dbID)
	if e != nil {
		return nil, e
	}

	tx, e := target.BeginTx(work, nil)
	if e != nil {
		return nil, dbmanager.SanitizeError(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(work, `SET LOCAL lock_timeout='5s'`); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(work, `SET LOCAL statement_timeout='60s'`); e != nil {
		return nil, e
	}
	locked, e := tryLockTarget(work, tx, dbID)
	if e != nil {
		return nil, e
	}
	if !locked {
		return nil, fmt.Errorf("%w: ada migration lain yang sedang dijalankan di database ini", domain.ErrConflict)
	}
	// Selama kunci dipegang, migration berstatus running pasti sisa eksekusi
	// yang terputus, jadi dibereskan dulu sebelum mengecek giliran.
	if e = s.reconcile(work, tx, dbID); e != nil {
		return nil, e
	}

	sqlText, version, runID, e := s.start(work, u, dbID, id)
	if e != nil {
		return nil, e
	}

	resultErr := func() error {
		// Penanda ini ikut commit bersama SQL migration, dan ditulis sebelum
		// SQL user supaya SET search_path di dalamnya tidak memindahkan lokasinya.
		if _, e := tx.ExecContext(work, `CREATE TABLE IF NOT EXISTS _dbplatform_history(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); e != nil {
			return e
		}
		if _, e := tx.ExecContext(work, `INSERT INTO _dbplatform_history(version) VALUES($1)`, version); e != nil {
			return e
		}
		// Tanpa argumen, SQL migration boleh berisi banyak statement.
		if _, e := tx.ExecContext(work, sqlText); e != nil {
			return e
		}
		return tx.Commit()
	}()
	status := StatusSuccess
	message := ""
	if resultErr != nil {
		status = StatusFailed
		message = errorMessage(resultErr)
	}
	if e = s.finish(work, id, dbID, runID, status, message); e != nil {
		return nil, e
	}
	runs, e := s.Runs(work, dbID, id)
	if e != nil {
		return nil, e
	}
	if len(runs) == 0 {
		return nil, domain.ErrNotFound
	}
	// Kegagalan SQL dicatat sebagai run failed; handler menampilkannya di halaman review.
	return &runs[0], nil
}

// start mengunci migration database ini, memastikan id adalah giliran
// berikutnya, lalu menandainya running.
func (s *Store) start(work context.Context, u *domain.User, dbID, id int64) (string, int, int64, error) {
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
	rows, e := platformTx.QueryContext(work, `SELECT id,version,status FROM migrations WHERE db_id=$1 ORDER BY version FOR UPDATE`, dbID)
	if e != nil {
		return "", 0, 0, e
	}
	list := []Migration{}
	for rows.Next() {
		var m Migration
		if e = rows.Scan(&m.ID, &m.Version, &m.Status); e != nil {
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
	if e = CheckExecutable(list, id); e != nil {
		return "", 0, 0, e
	}
	var sqlText string
	var version int
	e = platformTx.QueryRowContext(work, `SELECT sql_text,version FROM migrations WHERE id=$1 AND db_id=$2`, id, dbID).Scan(&sqlText, &version)
	if e != nil {
		return "", 0, 0, e
	}
	_, e = platformTx.ExecContext(work, `UPDATE migrations SET status='running',executed_by=$1,executed_at=now(),finished_at=NULL,error_message=NULL WHERE id=$2 AND db_id=$3`, u.ID, id, dbID)
	if e != nil {
		return "", 0, 0, e
	}
	var runID int64
	e = platformTx.QueryRowContext(work, `INSERT INTO migration_runs(migration_id,sql_text,executed_by,status) VALUES($1,$2,$3,'running') RETURNING id`, id, sqlText, u.ID).Scan(&runID)
	if e != nil {
		return "", 0, 0, e
	}
	return sqlText, version, runID, platformTx.Commit()
}

func (s *Store) finish(work context.Context, id, dbID, runID int64, status Status, message string) error {
	finishTx, e := s.DB.BeginTx(work, nil)
	if e != nil {
		return e
	}
	defer finishTx.Rollback()
	_, e = finishTx.ExecContext(work, `UPDATE migrations SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE id=$3 AND db_id=$4`, status, message, id, dbID)
	if e != nil {
		return e
	}
	_, e = finishTx.ExecContext(work, `UPDATE migration_runs SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE id=$3 AND migration_id=$4`, status, message, runID, id)
	if e != nil {
		return e
	}
	return finishTx.Commit()
}

// errorMessage mengambil pesan error PostgreSQL. Error lain, misalnya
// koneksi putus, diganti pesan umum supaya detail koneksi tidak tersimpan.
func errorMessage(e error) string {
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
