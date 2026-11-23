package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"

	"github.com/zizadhrmaa/DB-Platform/internal/obs"
)

// DBTX adalah antarmuka umum untuk database/sql yang didukung oleh *sql.DB,
// *sql.Tx, *LoggedDB, dan *LoggedTx.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LoggedDB membungkus *sql.DB dengan pencatatan structured log dan tracing OpenTelemetry
// untuk setiap operasi SQL, transaksi, dan koneksi.
type LoggedDB struct {
	db     *sql.DB
	dbID   int64
	dbName string
}

// NewLoggedDB membuat instance LoggedDB baru.
func NewLoggedDB(db *sql.DB, dbID int64, dbName string) *LoggedDB {
	return &LoggedDB{db: db, dbID: dbID, dbName: dbName}
}

// WrapDB membungkus *sql.DB dengan LoggedDB tanpa ID database eksplisit.
func WrapDB(db *sql.DB) *LoggedDB {
	return NewLoggedDB(db, 0, "")
}

func (l *LoggedDB) Raw() *sql.DB {
	return l.db
}

func (l *LoggedDB) PingContext(ctx context.Context) error {
	return l.db.PingContext(ctx)
}

func (l *LoggedDB) Close() error {
	return l.db.Close()
}

func (l *LoggedDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*LoggedTx, error) {
	start := time.Now()
	ctx, span := obs.StartSpan(ctx, "db.begin_tx")
	defer span.End()

	tx, err := l.db.BeginTx(ctx, opts)
	dur := time.Since(start)
	if err != nil {
		obs.RecordError(span, err)
		logAttrs := l.baseAttrs(ctx, "sql_begin", "")
		logAttrs = append(logAttrs, "status", "failed", "dur_ms", dur.Milliseconds(), "error", err.Error())
		slog.ErrorContext(ctx, "sql_begin failed", logAttrs...)
		return nil, err
	}

	logAttrs := l.baseAttrs(ctx, "sql_begin", "")
	logAttrs = append(logAttrs, "status", "success", "dur_ms", dur.Milliseconds())
	slog.InfoContext(ctx, "sql_begin", logAttrs...)

	return &LoggedTx{tx: tx, dbID: l.dbID, dbName: l.dbName}, nil
}

func (l *LoggedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	// Catat mulai eksekusi secara terpisah
	startAttrs := l.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.exec")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	res, err := l.db.ExecContext(ctx, query, args...)
	dur := time.Since(start)

	var rowsAffected int64
	if res != nil {
		rowsAffected, _ = res.RowsAffected()
	}

	sqlstate := extractSQLState(err)
	resultAttrs := l.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
		"rows_affected", rowsAffected,
	)
	if sqlstate != "" {
		resultAttrs = append(resultAttrs, "sqlstate", sqlstate)
	}

	if err != nil {
		obs.RecordError(span, err)
		resultAttrs = append(resultAttrs, "status", "failed", "error", err.Error())
		slog.ErrorContext(ctx, "sql_result", resultAttrs...)
		return res, err
	}

	resultAttrs = append(resultAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return res, nil
}

func (l *LoggedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	// Catat mulai eksekusi secara terpisah
	startAttrs := l.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.query")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	rows, err := l.db.QueryContext(ctx, query, args...)
	dur := time.Since(start)
	sqlstate := extractSQLState(err)

	resultAttrs := l.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
	)
	if sqlstate != "" {
		resultAttrs = append(resultAttrs, "sqlstate", sqlstate)
	}

	if err != nil {
		obs.RecordError(span, err)
		resultAttrs = append(resultAttrs, "status", "failed", "error", err.Error())
		slog.ErrorContext(ctx, "sql_result", resultAttrs...)
		return rows, err
	}

	resultAttrs = append(resultAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return rows, nil
}

func (l *LoggedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	startAttrs := l.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.query_row")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	row := l.db.QueryRowContext(ctx, query, args...)
	dur := time.Since(start)

	resultAttrs := l.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
		"status", "success",
	)
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return row
}

// LoggedTx membungkus *sql.Tx dengan pencatatan log dan tracing untuk setiap statement,
// commit, dan rollback secara terpisah.
type LoggedTx struct {
	tx     *sql.Tx
	dbID   int64
	dbName string
}

// WrapTx membungkus transaction yang sudah dibuka di tempat lain, misalnya
// di koneksi khusus yang dibuang setelah dipakai (eksekusi migration).
func WrapTx(tx *sql.Tx, dbID int64, dbName string) *LoggedTx {
	return &LoggedTx{tx: tx, dbID: dbID, dbName: dbName}
}

func (t *LoggedTx) Raw() *sql.Tx {
	return t.tx
}

func (t *LoggedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	startAttrs := t.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.tx.exec")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	res, err := t.tx.ExecContext(ctx, query, args...)
	dur := time.Since(start)

	var rowsAffected int64
	if res != nil {
		rowsAffected, _ = res.RowsAffected()
	}

	sqlstate := extractSQLState(err)
	resultAttrs := t.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
		"rows_affected", rowsAffected,
	)
	if sqlstate != "" {
		resultAttrs = append(resultAttrs, "sqlstate", sqlstate)
	}

	if err != nil {
		obs.RecordError(span, err)
		resultAttrs = append(resultAttrs, "status", "failed", "error", err.Error())
		slog.ErrorContext(ctx, "sql_result", resultAttrs...)
		return res, err
	}

	resultAttrs = append(resultAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return res, nil
}

func (t *LoggedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	startAttrs := t.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.tx.query")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	rows, err := t.tx.QueryContext(ctx, query, args...)
	dur := time.Since(start)
	sqlstate := extractSQLState(err)

	resultAttrs := t.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
	)
	if sqlstate != "" {
		resultAttrs = append(resultAttrs, "sqlstate", sqlstate)
	}

	if err != nil {
		obs.RecordError(span, err)
		resultAttrs = append(resultAttrs, "status", "failed", "error", err.Error())
		slog.ErrorContext(ctx, "sql_result", resultAttrs...)
		return rows, err
	}

	resultAttrs = append(resultAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return rows, nil
}

func (t *LoggedTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	redactedArgs := obs.RedactArgs(args)

	startAttrs := t.baseAttrs(ctx, "sql_start", query)
	startAttrs = append(startAttrs, "params", redactedArgs)
	slog.InfoContext(ctx, "sql_start", startAttrs...)

	ctx, span := obs.StartSpan(ctx, "db.tx.query_row")
	span.SetAttributes(attribute.String("db.statement", query))
	defer span.End()

	row := t.tx.QueryRowContext(ctx, query, args...)
	dur := time.Since(start)

	resultAttrs := t.baseAttrs(ctx, "sql_result", query)
	resultAttrs = append(resultAttrs,
		"params", redactedArgs,
		"dur_ms", dur.Milliseconds(),
		"status", "success",
	)
	slog.InfoContext(ctx, "sql_result", resultAttrs...)
	return row
}

// Commit sama dengan CommitContext tanpa context pemanggil.
func (t *LoggedTx) Commit() error { return t.CommitContext(context.Background()) }

// CommitContext mencatat commit dengan trace dan metadata job dari ctx,
// supaya log commit bisa dikaitkan dengan eksekusinya.
func (t *LoggedTx) CommitContext(ctx context.Context) error {
	start := time.Now()
	ctx, span := obs.StartSpan(ctx, "db.tx.commit")
	defer span.End()

	err := t.tx.Commit()
	dur := time.Since(start)

	commitAttrs := t.baseAttrs(ctx, "sql_commit", "")
	commitAttrs = append(commitAttrs, "dur_ms", dur.Milliseconds())

	if err != nil {
		obs.RecordError(span, err)
		commitAttrs = append(commitAttrs, "status", "failed", "error", err.Error())
		slog.ErrorContext(ctx, "sql_commit", commitAttrs...)
		return err
	}

	commitAttrs = append(commitAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_commit", commitAttrs...)
	return nil
}

// Rollback sama dengan RollbackContext tanpa context pemanggil.
func (t *LoggedTx) Rollback() error { return t.RollbackContext(context.Background()) }

// RollbackContext mencatat rollback dengan trace dan metadata job dari ctx.
func (t *LoggedTx) RollbackContext(ctx context.Context) error {
	start := time.Now()
	ctx, span := obs.StartSpan(ctx, "db.tx.rollback")
	defer span.End()

	err := t.tx.Rollback()
	dur := time.Since(start)

	rollbackAttrs := t.baseAttrs(ctx, "sql_rollback", "")
	rollbackAttrs = append(rollbackAttrs, "dur_ms", dur.Milliseconds())

	// Rollback pada tx yang sudah selesai menghasilkan ErrTxDone, ini wajar untuk defer rollback
	if err != nil && !errors.Is(err, sql.ErrTxDone) {
		obs.RecordError(span, err)
		rollbackAttrs = append(rollbackAttrs, "status", "failed", "error", err.Error())
		slog.WarnContext(ctx, "sql_rollback", rollbackAttrs...)
		return err
	}

	rollbackAttrs = append(rollbackAttrs, "status", "success")
	slog.InfoContext(ctx, "sql_rollback", rollbackAttrs...)
	return nil
}

func (l *LoggedDB) baseAttrs(ctx context.Context, event, query string) []any {
	attrs := []any{"event", event}
	if query != "" {
		attrs = append(attrs, "query", query)
	}
	if l.dbName != "" {
		attrs = append(attrs, "database", l.dbName)
	} else if l.dbID != 0 {
		attrs = append(attrs, "database", fmt.Sprint(l.dbID))
	}
	return attrs
}

func (t *LoggedTx) baseAttrs(ctx context.Context, event, query string) []any {
	attrs := []any{"event", event}
	if query != "" {
		attrs = append(attrs, "query", query)
	}
	if t.dbName != "" {
		attrs = append(attrs, "database", t.dbName)
	} else if t.dbID != 0 {
		attrs = append(attrs, "database", fmt.Sprint(t.dbID))
	}
	return attrs
}

func extractSQLState(err error) string {
	if err == nil {
		return ""
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return strconv.Itoa(int(me.Number))
	}
	return ""
}
