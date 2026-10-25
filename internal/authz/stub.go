package authz

import (
	"context"
	"errors"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// Static adalah Authorizer tanpa database untuk test. Keputusannya tetap
// lewat Decide, jadi hasil 401/403/404-nya sama dengan Service.
type Static struct {
	Levels map[Key]domain.AccessLevel
	Names  map[int64]string
}

type Key struct{ UserID, DBID int64 }

var _ Authorizer = (*Static)(nil)

func (s *Static) Level(_ context.Context, u *domain.User, dbID int64) (domain.AccessLevel, error) {
	if u == nil {
		return domain.AccessNone, nil
	}
	return s.Levels[Key{u.ID, dbID}], nil
}

func (s *Static) Authorize(ctx context.Context, u *domain.User, dbID int64, a Action) error {
	lvl, _ := s.Level(ctx, u, dbID)
	return Decide(u, lvl, a)
}

func (s *Static) Can(ctx context.Context, u *domain.User, dbID int64, a Action) (bool, error) {
	return s.Authorize(ctx, u, dbID, a) == nil, nil
}

func (s *Static) AccessibleDatabases(_ context.Context, u *domain.User) ([]DBAccess, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	out := []DBAccess{}
	for k, l := range s.Levels {
		if k.UserID == u.ID && l > domain.AccessNone {
			out = append(out, DBAccess{DBID: k.DBID, Name: s.Names[k.DBID], Level: l})
		}
	}
	return out, nil
}

func isDecisionError(err error) bool {
	return errors.Is(err, domain.ErrForbidden) ||
		errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrUnauthenticated)
}
