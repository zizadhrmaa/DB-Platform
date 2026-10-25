package migration

import (
	"context"
	"database/sql"
	"fmt"
)

// Recover menyelaraskan migration berstatus running
// dengan bukti COMMIT pada database target.
//
// Jika bukti COMMIT ditemukan, status dipulihkan menjadi success.
// Jika tidak ditemukan, status running dibiarkan untuk pemeriksaan
// lebih lanjut agar tidak keliru menyatakan eksekusi aktif sebagai gagal.
func (s *Store) Recover(ctx context.Context) error {
	if s.DB == nil || s.Targets == nil {
		return fmt.Errorf("recovery dependencies not initialized")
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, db_id, version
		FROM migrations
		WHERE status = 'running'
		ORDER BY db_id, version
	`)
	if err != nil {
		return err
	}

	type pendingRecovery struct {
		id      int64
		dbID    int64
		version int
	}

	var items []pendingRecovery

	for rows.Next() {
		var item pendingRecovery
		if err := rows.Scan(
			&item.id,
			&item.dbID,
			&item.version,
		); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}

	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	for _, item := range items {
		target, err := s.Targets.Conn(ctx, item.dbID)
		if err != nil {
			return fmt.Errorf(
				"recovery db %d: %w",
				item.dbID, err,
			)
		}

		var historyExists bool
		err = target.QueryRowContext(ctx, `
			SELECT to_regclass('public._dbplatform_history')
				IS NOT NULL
		`).Scan(&historyExists)
		if err != nil {
			return err
		}

		if !historyExists {
			continue
		}

		var applied bool
		err = target.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM public._dbplatform_history
				WHERE version = $1
			)
		`, item.version).Scan(&applied)

		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}

		if !applied {
			continue
		}

		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE migrations
			SET status = 'success',
				finished_at = now(),
				error_message = NULL
			WHERE id = $1
			  AND db_id = $2
			  AND status = 'running'
		`, item.id, item.dbID)

		if err != nil {
			tx.Rollback()
			return err
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE migration_runs
			SET status = 'success',
				finished_at = now(),
				error_message = NULL
			WHERE migration_id = $1
			  AND status = 'running'
		`, item.id)

		if err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}
