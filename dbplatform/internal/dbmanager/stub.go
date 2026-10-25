package dbmanager

import (
	"context"
	"database/sql"
	"sync"
)

// Static adalah STUB Manager: memetakan dbID -> *sql.DB yang sudah dibuka.
// Dipakai Orang 4 (dan test Orang 3) sebelum PoolManager asli selesai:
//
//	target, _ := testutil.TargetDB(t)
//	mgr := dbmanager.NewStatic(map[int64]*sql.DB{dbID: target})
type Static struct {
	mu  sync.RWMutex
	dbs map[int64]*sql.DB
}

var _ Manager = (*Static)(nil)

func NewStatic(dbs map[int64]*sql.DB) *Static {
	if dbs == nil {
		dbs = map[int64]*sql.DB{}
	}
	return &Static{dbs: dbs}
}

func (s *Static) Conn(_ context.Context, dbID int64) (*sql.DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	db, ok := s.dbs[dbID]
	if !ok {
		return nil, ErrConnNotFound
	}
	return db, nil
}

func (s *Static) Invalidate(dbID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dbs, dbID)
}

// Close tidak menutup *sql.DB milik pemanggil (test yang menutupnya).
func (s *Static) Close() error { return nil }
