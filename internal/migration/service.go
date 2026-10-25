package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"strings"
	"time"
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
		if _, e = s.Get(ctx, dbID, id); e != nil {
			return e
		}
		return domain.ErrConflict
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
		if _, e = s.Get(ctx, dbID, id); e != nil {
			return e
		}
		return domain.ErrConflict
	}
	return nil
}

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
	work, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	target, e := s.Targets.Conn(work, dbID)
	if e != nil {
		return nil, e
	}
	platformTx, e := s.DB.BeginTx(work, nil)
	if e != nil {
		return nil, e
	}
	defer platformTx.Rollback()
	var locked int64
	e = platformTx.QueryRowContext(work, `SELECT id FROM db_connections WHERE id=$1 AND is_active=true FOR UPDATE`, dbID).Scan(&locked)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	rows, e := platformTx.QueryContext(work, `SELECT id,version,status FROM migrations WHERE db_id=$1 ORDER BY version FOR UPDATE`, dbID)
	if e != nil {
		return nil, e
	}
	list := []Migration{}
	for rows.Next() {
		var m Migration
		if e = rows.Scan(&m.ID, &m.Version, &m.Status); e != nil {
			rows.Close()
			return nil, e
		}
		list = append(list, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if e = CheckExecutable(list, id); e != nil {
		return nil, e
	}
	var sqlText string
	var version int
	e = platformTx.QueryRowContext(work, `SELECT sql_text,version FROM migrations WHERE id=$1 AND db_id=$2`, id, dbID).Scan(&sqlText, &version)
	if e != nil {
		return nil, e
	}
	_, e = platformTx.ExecContext(work, `UPDATE migrations SET status='running',executed_by=$1,executed_at=now(),finished_at=NULL,error_message=NULL WHERE id=$2 AND db_id=$3`, u.ID, id, dbID)
	if e != nil {
		return nil, e
	}
	var runID int64
	e = platformTx.QueryRowContext(work, `INSERT INTO migration_runs(migration_id,sql_text,executed_by,status) VALUES($1,$2,$3,'running') RETURNING id`, id, sqlText, u.ID).Scan(&runID)
	if e != nil {
		return nil, e
	}
	if e = platformTx.Commit(); e != nil {
		return nil, e
	}
	resultErr := func() error {
		tx, e := target.BeginTx(work, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(work, `SET LOCAL lock_timeout='5s'`); e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, `SET LOCAL statement_timeout='60s'`); e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, `SELECT pg_advisory_xact_lock($1::integer,$2::integer)`, 3110, dbID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, `CREATE TABLE IF NOT EXISTS _dbplatform_history(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, sqlText); e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, `INSERT INTO _dbplatform_history(version) VALUES($1)`, version); e != nil {
			return e
		}
		return tx.Commit()
	}()
	status := StatusSuccess
	message := ""
	if resultErr != nil {
		status = StatusFailed
		message = resultErr.Error()
	}
	finishTx, e := s.DB.BeginTx(work, nil)
	if e != nil {
		return nil, e
	}
	defer finishTx.Rollback()
	_, e = finishTx.ExecContext(work, `UPDATE migrations SET status=$1,finished_at=now(),error_message=$2 WHERE id=$3 AND db_id=$4`, status, message, id, dbID)
	if e != nil {
		return nil, e
	}
	_, e = finishTx.ExecContext(work, `UPDATE migration_runs SET status=$1,finished_at=now(),error_message=$2 WHERE id=$3 AND migration_id=$4`, status, message, runID, id)
	if e != nil {
		return nil, e
	}
	if e = finishTx.Commit(); e != nil {
		return nil, e
	}
	runs, e := s.Runs(work, dbID, id)
	if e != nil {
		return nil, e
	}
	if len(runs) == 0 {
		return nil, domain.ErrNotFound
	}
	return &runs[0], nil
}
