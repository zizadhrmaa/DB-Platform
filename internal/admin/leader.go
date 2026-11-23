package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

func (e *FormError) UserMessage() string { return e.Msg }

func (s *Store) Leaders(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, leader_user_id FROM groups WHERE leader_user_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var gid, uid int64
		if err := rows.Scan(&gid, &uid); err != nil {
			return nil, err
		}
		out[gid] = uid
	}
	return out, rows.Err()
}

func (s *Store) Leader(ctx context.Context, groupID int64) (*int64, error) {
	var leader sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT leader_user_id FROM groups WHERE id = $1`, groupID).Scan(&leader)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("group %d: %w", groupID, domain.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if !leader.Valid {
		return nil, nil
	}
	v := leader.Int64
	return &v, nil
}

func (s *Store) SetLeader(ctx context.Context, groupID, userID int64) error {
	var active bool
	err := s.DB.QueryRowContext(ctx, `SELECT is_active FROM users WHERE id = $1`, userID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user %d: %w", userID, domain.ErrNotFound)
	}
	if err != nil {
		return err
	}
	if !active {
		return invalid("Leader harus user yang aktif.")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE groups SET leader_user_id = $2 WHERE id = $1`, groupID, userID)
	if pgCode(err) == pgForeignKeyViolation {
		return invalid("Leader harus anggota group ini.")
	}
	return notFoundIfNone(res, err, "group", groupID)
}

func (s *Store) ClearLeader(ctx context.Context, groupID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE groups SET leader_user_id = NULL WHERE id = $1`, groupID)
	return notFoundIfNone(res, err, "group", groupID)
}
