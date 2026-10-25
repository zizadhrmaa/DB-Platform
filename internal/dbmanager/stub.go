package dbmanager

import (
	"context"
	"database/sql"
	"sync"
)

// Static adalah Manager untuk test yang memakai *sql.DB yang sudah dibuka.
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

// Close tidak menutup apa pun; *sql.DB ditutup oleh test yang membukanya.
func (s *Static) Close() error { return nil }
