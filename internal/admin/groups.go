package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

const (
	maxGroupName        = 100
	maxGroupDescription = 500
)

type Group struct {
	ID          int64
	Name        string
	Description string
	MemberCount int
	AccessCount int
}

type Member struct {
	UserID   int64
	Username string
	IsActive bool
}

type Access struct {
	DBID     int64
	DBName   string
	DBActive bool
	Level    domain.AccessLevel
}

type DBOption struct {
	ID       int64
	Name     string
	IsActive bool
}

type GroupDetail struct {
	Group
	Members    []Member
	Access     []Access
	Candidates []Member   // user aktif yang belum menjadi anggota
	Databases  []DBOption // semua koneksi, untuk form grant
}

// ValidateGroup mengembalikan nama dan deskripsi yang sudah di-trim.
func ValidateGroup(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return name, description, invalid("Nama group wajib diisi.")
	}
	if utf8.RuneCountInString(name) > maxGroupName {
		return name, description, invalid(fmt.Sprintf("Nama group maksimal %d karakter.", maxGroupName))
	}
	if utf8.RuneCountInString(description) > maxGroupDescription {
		return name, description, invalid(fmt.Sprintf("Deskripsi maksimal %d karakter.", maxGroupDescription))
	}
	return name, description, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT g.id, g.name, g.description,
		       (SELECT count(*) FROM group_members m WHERE m.group_id = g.id),
		       (SELECT count(*) FROM group_db_access a WHERE a.group_id = g.id)
		FROM groups g
		ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.MemberCount, &g.AccessCount); err != nil {
			return nil, err
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

func (s *Store) GetGroup(ctx context.Context, id int64) (*GroupDetail, error) {
	g := &GroupDetail{Group: Group{ID: id}}
	err := s.DB.QueryRowContext(ctx,
		`SELECT name, description FROM groups WHERE id = $1`, id,
	).Scan(&g.Name, &g.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("group %d: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	if g.Members, err = s.members(ctx, `
		SELECT u.id, u.username, u.is_active FROM users u
		JOIN group_members m ON m.user_id = u.id
		WHERE m.group_id = $1
		ORDER BY lower(u.username)`, id); err != nil {
		return nil, err
	}
	if g.Candidates, err = s.members(ctx, `
		SELECT u.id, u.username, u.is_active FROM users u
		WHERE u.is_active
		  AND NOT EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = $1 AND m.user_id = u.id)
		ORDER BY lower(u.username)`, id); err != nil {
		return nil, err
	}
	if g.Access, err = s.access(ctx, id); err != nil {
		return nil, err
	}
	if g.Databases, err = s.databases(ctx); err != nil {
		return nil, err
	}
	g.MemberCount, g.AccessCount = len(g.Members), len(g.Access)
	return g, nil
}

func (s *Store) members(ctx context.Context, query string, groupID int64) ([]Member, error) {
	rows, err := s.DB.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Username, &m.IsActive); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func (s *Store) access(ctx context.Context, groupID int64) ([]Access, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT d.id, d.name, d.is_active, a.level
		FROM group_db_access a
		JOIN db_connections d ON d.id = a.db_id
		WHERE a.group_id = $1
		ORDER BY lower(d.name)`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Access{}
	for rows.Next() {
		var a Access
		var level string
		if err := rows.Scan(&a.DBID, &a.DBName, &a.DBActive, &level); err != nil {
			return nil, err
		}
		if a.Level, err = domain.ParseAccessLevel(level); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

func (s *Store) databases(ctx context.Context) ([]DBOption, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, is_active FROM db_connections ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []DBOption{}
	for rows.Next() {
		var d DBOption
		if err := rows.Scan(&d.ID, &d.Name, &d.IsActive); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

func (s *Store) CreateGroup(ctx context.Context, name, description string) (int64, error) {
	name, description, err := ValidateGroup(name, description)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx,
		`INSERT INTO groups (name, description) VALUES ($1, $2) RETURNING id`, name, description,
	).Scan(&id)
	if pgCode(err) == pgUniqueViolation {
		return 0, invalid("Nama group sudah dipakai.")
	}
	return id, err
}

func (s *Store) UpdateGroup(ctx context.Context, id int64, name, description string) error {
	name, description, err := ValidateGroup(name, description)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE groups SET name = $2, description = $3 WHERE id = $1`, id, name, description)
	if pgCode(err) == pgUniqueViolation {
		return invalid("Nama group sudah dipakai.")
	}
	return notFoundIfNone(res, err, "group", id)
}

// DeleteGroup ikut menghapus keanggotaan dan grant group tersebut, jadi
// akses anggotanya langsung hilang.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, id)
	return notFoundIfNone(res, err, "group", id)
}

// AddMember mengembalikan false kalau user sudah menjadi anggota.
func (s *Store) AddMember(ctx context.Context, groupID, userID int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, groupID, userID)
	if pgCode(err) == pgForeignKeyViolation {
		return false, fmt.Errorf("group %d atau user %d: %w", groupID, userID, domain.ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) RemoveMember(ctx context.Context, groupID, userID int64) error {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	return notFoundIfNone(res, err, "anggota", userID)
}

// Grant memberi atau mengubah level akses group ke satu database.
func (s *Store) Grant(ctx context.Context, groupID, dbID int64, level domain.AccessLevel, grantedBy int64) error {
	if level != domain.AccessView && level != domain.AccessEdit {
		return invalid("Pilih level akses View Only atau View + Edit.")
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO group_db_access (group_id, db_id, level, granted_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (group_id, db_id)
		DO UPDATE SET level = EXCLUDED.level, granted_by = EXCLUDED.granted_by, granted_at = now()`,
		groupID, dbID, level.String(), grantedBy)
	if pgCode(err) == pgForeignKeyViolation {
		return fmt.Errorf("group %d atau database %d: %w", groupID, dbID, domain.ErrNotFound)
	}
	return err
}

func (s *Store) Revoke(ctx context.Context, groupID, dbID int64) error {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM group_db_access WHERE group_id = $1 AND db_id = $2`, groupID, dbID)
	return notFoundIfNone(res, err, "akses database", dbID)
}

func notFoundIfNone(res sql.Result, err error, what string, id int64) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s %d: %w", what, id, domain.ErrNotFound)
	}
	return nil
}
