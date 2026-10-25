package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
)

const interruptedMessage = "Eksekusi terputus sebelum selesai. Perubahan di database target sudah di-rollback."

// tryLockTarget mengambil kunci eksekusi migration di database target.
// Kunci lepas otomatis saat transaction selesai, termasuk kalau server mati.
func tryLockTarget(ctx context.Context, tx *sql.Tx, dbID int64, isMySQL bool) (bool, error) {
	if isMySQL {
		var res sql.NullInt64
		lockName := fmt.Sprintf("dbplatform_migration_%d", dbID)
		if e := tx.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, lockName).Scan(&res); e != nil {
			return false, dbmanager.SanitizeError(e)
		}
		return res.Valid && res.Int64 == 1, nil
	}
	var locked bool
	if e := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1::integer,$2::integer)`, 3110, dbID).Scan(&locked); e != nil {
		return false, dbmanager.SanitizeError(e)
	}
	return locked, nil
}

func unlockTarget(ctx context.Context, tx *sql.Tx, dbID int64, isMySQL bool) {
	if isMySQL {
		lockName := fmt.Sprintf("dbplatform_migration_%d", dbID)
		_, _ = tx.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, lockName)
	}
}

func (s *Store) Recover(ctx context.Context) error {
	if s.DB == nil || s.Targets == nil {
		return fmt.Errorf("recovery dependencies not initialized")
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT DISTINCT db_id FROM migrations WHERE status='running' ORDER BY db_id`)
	if e != nil {
		return e
	}
	var dbIDs []int64
	for rows.Next() {
		var dbID int64
		if e = rows.Scan(&dbID); e != nil {
			rows.Close()
			return e
		}
		dbIDs = append(dbIDs, dbID)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}

	var errs []error
	for _, dbID := range dbIDs {
		if e := s.recoverDB(ctx, dbID); e != nil {
			errs = append(errs, fmt.Errorf("recovery db %d: %w", dbID, e))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) recoverDB(ctx context.Context, dbID int64) error {
	target, e := s.Targets.Conn(ctx, dbID)
	if e != nil {
		return e
	}
	isMy := dbmanager.IsMySQL(target)
	tx, e := target.BeginTx(ctx, nil)
	if e != nil {
		return dbmanager.SanitizeError(e)
	}
	defer tx.Rollback()
	locked, e := tryLockTarget(ctx, tx, dbID, isMy)
	if e != nil || !locked {
		return e
	}
	if isMy {
		defer unlockTarget(ctx, tx, dbID, isMy)
	}
	return s.reconcile(ctx, tx, dbID, isMy)
}

func (s *Store) reconcile(ctx context.Context, tx *sql.Tx, dbID int64, isMySQL bool) error {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,version FROM migrations WHERE db_id=$1 AND status='running'`, dbID)
	if e != nil {
		return e
	}
	type stuck struct {
		id      int64
		version int
	}
	var items []stuck
	for rows.Next() {
		var it stuck
		if e = rows.Scan(&it.id, &it.version); e != nil {
			rows.Close()
			return e
		}
		items = append(items, it)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(items) == 0 {
		return e
	}

	var hasHistory bool
	if isMySQL {
		var cnt int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '_dbplatform_history'`).Scan(&cnt); e != nil {
			return dbmanager.SanitizeError(e)
		}
		hasHistory = cnt > 0
	} else {
		if e = tx.QueryRowContext(ctx, `SELECT to_regclass('_dbplatform_history') IS NOT NULL`).Scan(&hasHistory); e != nil {
			return dbmanager.SanitizeError(e)
		}
	}

	for _, it := range items {
		applied := false
		if hasHistory {
			checkSQL := `SELECT EXISTS (SELECT 1 FROM _dbplatform_history WHERE version=$1)`
			if isMySQL {
				checkSQL = `SELECT EXISTS (SELECT 1 FROM _dbplatform_history WHERE version=?)`
			}
			if e = tx.QueryRowContext(ctx, checkSQL, it.version).Scan(&applied); e != nil {
				return dbmanager.SanitizeError(e)
			}
		}
		status, message := StatusFailed, interruptedMessage
		if applied {
			status, message = StatusSuccess, ""
		}
		if e = s.finishInterrupted(ctx, it.id, dbID, status, message); e != nil {
			return e
		}
		slog.Warn("migration yang terputus dibereskan", "migration_id", it.id, "status", status)
	}
	return nil
}

func (s *Store) finishInterrupted(ctx context.Context, id, dbID int64, status Status, message string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `UPDATE migrations SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE id=$3 AND db_id=$4 AND status='running'`, status, message, id, dbID)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE migration_runs SET status=$1,finished_at=now(),error_message=NULLIF($2,'') WHERE migration_id=$3 AND status='running'`, status, message, id)
	if e != nil {
		return e
	}
	return tx.Commit()
}
