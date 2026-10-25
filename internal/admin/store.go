package admin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Store struct {
	DB *sql.DB
}

// FormError berisi pesan yang aman ditampilkan langsung di form admin.
type FormError struct {
	Msg string
	Err error // domain.ErrInvalidInput atau domain.ErrConflict
}

func (e *FormError) Error() string { return e.Msg }
func (e *FormError) Unwrap() error { return e.Err }

func invalid(msg string) error  { return &FormError{Msg: msg, Err: domain.ErrInvalidInput} }
func conflict(msg string) error { return &FormError{Msg: msg, Err: domain.ErrConflict} }

const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

type Counts struct {
	Users     int
	Groups    int
	Databases int
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.DB.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM users),
		       (SELECT count(*) FROM groups),
		       (SELECT count(*) FROM db_connections)`,
	).Scan(&c.Users, &c.Groups, &c.Databases)
	return c, err
}
