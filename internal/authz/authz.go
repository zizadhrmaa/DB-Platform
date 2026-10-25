package authz

import (
	"context"
	"database/sql"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Authorizer interface {
	// Can hanya mengembalikan error kalau ada kegagalan sistem.
	Can(ctx context.Context, u *domain.User, dbID int64, action Action) (bool, error)

	// Authorize mengembalikan ErrUnauthenticated, ErrNotFound, atau
	// ErrForbidden yang bisa langsung dioper ke httpx.Error.
	Authorize(ctx context.Context, u *domain.User, dbID int64, action Action) error

	// Level dipakai UI untuk menampilkan atau menyembunyikan tombol. Backend
	// tetap harus memanggil Authorize.
	Level(ctx context.Context, u *domain.User, dbID int64) (domain.AccessLevel, error)

	AccessibleDatabases(ctx context.Context, u *domain.User) ([]DBAccess, error)
}

type DBAccess struct {
	DBID   int64
	Name   string
	DBType string
	Level  domain.AccessLevel
}

// Service sengaja tidak memakai cache supaya pencabutan akses langsung berlaku di request berikutnya.
type Service struct {
	DB *sql.DB
}

var _ Authorizer = (*Service)(nil)

// Akses efektif adalah level tertinggi dari semua group user. User dan
// koneksi yang nonaktif tidak dihitung.
const levelSQL = `
SELECT COALESCE(MAX(CASE a.level WHEN 'edit' THEN 2 WHEN 'view' THEN 1 END), 0)
FROM group_db_access a
JOIN group_members  m ON m.group_id = a.group_id
JOIN users          u ON u.id = m.user_id AND u.is_active
JOIN db_connections d ON d.id = a.db_id   AND d.is_active
WHERE m.user_id = $1 AND a.db_id = $2`

func (s *Service) Level(ctx context.Context, u *domain.User, dbID int64) (domain.AccessLevel, error) {
	if u == nil {
		return domain.AccessNone, nil
	}
	var lvl int
	if err := s.DB.QueryRowContext(ctx, levelSQL, u.ID, dbID).Scan(&lvl); err != nil {
		return domain.AccessNone, err
	}
	return domain.AccessLevel(lvl), nil
}

func (s *Service) Authorize(ctx context.Context, u *domain.User, dbID int64, action Action) error {
	if u == nil || !u.IsActive {
		return domain.ErrUnauthenticated
	}
	lvl, err := s.Level(ctx, u, dbID)
	if err != nil {
		return err
	}
	return Decide(u, lvl, action)
}

func (s *Service) Can(ctx context.Context, u *domain.User, dbID int64, action Action) (bool, error) {
	err := s.Authorize(ctx, u, dbID, action)
	if err == nil {
		return true, nil
	}
	if isDecisionError(err) {
		return false, nil
	}
	return false, err
}

func (s *Service) AccessibleDatabases(ctx context.Context, u *domain.User) ([]DBAccess, error) {
	if u == nil || !u.IsActive {
		return nil, domain.ErrUnauthenticated
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT d.id, d.name, d.db_type,
		       MAX(CASE a.level WHEN 'edit' THEN 2 WHEN 'view' THEN 1 END)
		FROM group_db_access a
		JOIN group_members  m ON m.group_id = a.group_id
		JOIN db_connections d ON d.id = a.db_id AND d.is_active
		WHERE m.user_id = $1
		GROUP BY d.id, d.name, d.db_type
		ORDER BY lower(d.name), d.id`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBAccess{}
	for rows.Next() {
		var a DBAccess
		var lvl int
		if err := rows.Scan(&a.DBID, &a.Name, &a.DBType, &lvl); err != nil {
			return nil, err
		}
		a.Level = domain.AccessLevel(lvl)
		out = append(out, a)
	}
	return out, rows.Err()
}
