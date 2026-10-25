package dbmanager

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	MaxPage         = 10_000_000 // supaya perhitungan OFFSET tidak overflow
	maxCellRunes    = 200
	maxBytesShown   = 32

	// Di atas jumlah ini total baris diambil dari statistik (perkiraan),
	// supaya count(*) tidak membuat halaman lambat.
	exactCountLimit = 100_000
)

// Relasi yang ditampilkan: tabel, tabel partisi, view, materialized view,
// dan foreign table.
const relkinds = `('r','p','v','m','f')`

const hiddenSchemas = `n.nspname NOT IN ('pg_catalog', 'information_schema')
	AND n.nspname NOT LIKE 'pg\_toast%'
	AND n.nspname NOT LIKE 'pg\_temp\_%'`

type Table struct {
	Schema string
	Name   string
	Kind   string
}

type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
}

type Constraint struct {
	Name       string
	Type       string // PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK, EXCLUDE
	Definition string
	References string // tabel rujukan untuk FOREIGN KEY
}

type Index struct {
	Name       string
	Unique     bool
	Primary    bool
	Definition string
}

type TableDetail struct {
	Table
	Columns     []Column
	Constraints []Constraint
	Indexes     []Index
}

type Cell struct {
	Text      string
	Null      bool
	Truncated bool
}

type DataPage struct {
	Table
	Columns       []string
	Rows          [][]Cell
	Page          int
	Size          int
	Total         int64
	TotalEstimate bool
	OrderedBy     string // kosong kalau urutan baris tidak dijamin (view)
}

func (p *DataPage) HasPrev() bool { return p.Page > 1 }
func (p *DataPage) HasNext() bool { return int64(p.Page)*int64(p.Size) < p.Total }

// PageCount mengembalikan jumlah halaman, minimal 1.
func (p *DataPage) PageCount() int64 {
	if p.Total == 0 {
		return 1
	}
	return (p.Total + int64(p.Size) - 1) / int64(p.Size)
}

// readOnly menjalankan fn di transaction read-only dengan batas waktu,
// lalu selalu di-rollback.
func readOnly(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SanitizeError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '10s'`); err != nil {
		return SanitizeError(err)
	}
	return viewerError(fn(tx))
}

// viewerError mengubah error dari database target menjadi error domain.
// Pesan asli dari PostgreSQL tidak diteruskan ke user.
func viewerError(err error) error {
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42501":
			return fmt.Errorf("%w: role database tidak punya hak SELECT", domain.ErrForbidden)
		case "57014":
			return fmt.Errorf("%w: query terlalu lama", domain.ErrUnavailable)
		}
		return err
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1142, 1143:
			return fmt.Errorf("%w: role database tidak punya hak SELECT", domain.ErrForbidden)
		case 1045:
			return ErrConnUnavailable
		case 3024, 1317:
			return fmt.Errorf("%w: query terlalu lama", domain.ErrUnavailable)
		}
		return err
	}
	return SanitizeError(err)
}

func ListTables(ctx context.Context, db *sql.DB) ([]Table, error) {
	if IsMySQL(db) {
		return listTablesMySQL(ctx, db)
	}
	list := []Table{}
	err := readOnly(ctx, db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT n.nspname, c.relname, c.relkind
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN `+relkinds+` AND `+hiddenSchemas+`
			ORDER BY n.nspname, c.relname`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Table
			var kind string
			if err := rows.Scan(&t.Schema, &t.Name, &kind); err != nil {
				return err
			}
			t.Kind = kindLabel(kind)
			list = append(list, t)
		}
		return rows.Err()
	})
	return list, err
}

func kindLabel(relkind string) string {
	switch relkind {
	case "v":
		return "view"
	case "m":
		return "materialized view"
	case "f":
		return "foreign table"
	case "p":
		return "tabel partisi"
	default:
		return "tabel"
	}
}

// findTable mencari OID tabel dari nama di URL. Nama hanya dipakai sebagai
// parameter query, tidak pernah disambung ke SQL.
func findTable(ctx context.Context, tx *sql.Tx, schema, name string) (uint32, Table, error) {
	var oid uint32
	var kind string
	err := tx.QueryRowContext(ctx, `
		SELECT c.oid, c.relkind
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
		  AND c.relkind IN `+relkinds+` AND `+hiddenSchemas,
		schema, name).Scan(&oid, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Table{}, fmt.Errorf("tabel %s.%s: %w", schema, name, domain.ErrNotFound)
	}
	return oid, Table{Schema: schema, Name: name, Kind: kindLabel(kind)}, err
}

func DescribeTable(ctx context.Context, db *sql.DB, schema, name string) (*TableDetail, error) {
	if IsMySQL(db) {
		return describeTableMySQL(ctx, db, schema, name)
	}
	d := &TableDetail{}
	err := readOnly(ctx, db, func(tx *sql.Tx) error {
		oid, t, err := findTable(ctx, tx, schema, name)
		if err != nil {
			return err
		}
		d.Table = t
		if d.Columns, err = columns(ctx, tx, oid); err != nil {
			return err
		}
		if d.Constraints, err = constraints(ctx, tx, oid); err != nil {
			return err
		}
		d.Indexes, err = indexes(ctx, tx, oid)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func columns(ctx context.Context, tx *sql.Tx, oid uint32) ([]Column, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.attname,
		       format_type(a.atttypid, a.atttypmod),
		       NOT a.attnotnull,
		       CASE
		         WHEN a.attidentity = 'a' THEN 'GENERATED ALWAYS AS IDENTITY'
		         WHEN a.attidentity = 'd' THEN 'GENERATED BY DEFAULT AS IDENTITY'
		         WHEN a.attgenerated = 's' THEN 'GENERATED ALWAYS AS (' || pg_get_expr(d.adbin, d.adrelid) || ') STORED'
		         ELSE coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		       END
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Column{}
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func constraints(ctx context.Context, tx *sql.Tx, oid uint32) ([]Constraint, error) {
	// NOT NULL sudah terlihat di daftar kolom, jadi tidak ikut ditampilkan.
	rows, err := tx.QueryContext(ctx, `
		SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid),
		       CASE WHEN con.contype = 'f' THEN con.confrelid::regclass::text ELSE '' END
		FROM pg_constraint con
		WHERE con.conrelid = $1 AND con.contype IN ('p', 'f', 'u', 'c', 'x')
		ORDER BY array_position(ARRAY['p', 'u', 'f', 'c', 'x'], con.contype::text), con.conname`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Constraint{}
	for rows.Next() {
		var c Constraint
		var kind string
		if err := rows.Scan(&c.Name, &kind, &c.Definition, &c.References); err != nil {
			return nil, err
		}
		c.Type = constraintLabel(kind)
		list = append(list, c)
	}
	return list, rows.Err()
}

func constraintLabel(contype string) string {
	switch contype {
	case "p":
		return "PRIMARY KEY"
	case "f":
		return "FOREIGN KEY"
	case "u":
		return "UNIQUE"
	case "c":
		return "CHECK"
	default:
		return "EXCLUDE"
	}
}

func indexes(ctx context.Context, tx *sql.Tx, oid uint32) ([]Index, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT i.relname, ix.indisunique, ix.indisprimary, pg_get_indexdef(ix.indexrelid)
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1
		ORDER BY ix.indisprimary DESC, i.relname`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Index{}
	for rows.Next() {
		var ix Index
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Definition); err != nil {
			return nil, err
		}
		list = append(list, ix)
	}
	return list, rows.Err()
}

// ClampPage merapikan nilai page dan size dari query string.
func ClampPage(pageStr, sizeStr string) (int, int) {
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(sizeStr)
	if err != nil || size < 1 {
		size = DefaultPageSize
	}
	return min(page, MaxPage), min(size, MaxPageSize)
}

func TableData(ctx context.Context, db *sql.DB, schema, name string, page, size int) (*DataPage, error) {
	if IsMySQL(db) {
		return tableDataMySQL(ctx, db, schema, name, page, size)
	}
	p := &DataPage{Page: page, Size: size}
	err := readOnly(ctx, db, func(tx *sql.Tx) error {
		oid, t, err := findTable(ctx, tx, schema, name)
		if err != nil {
			return err
		}
		p.Table = t
		ident := pgx.Identifier{schema, name}.Sanitize()

		order, err := orderBy(ctx, tx, oid, t.Kind)
		if err != nil {
			return err
		}
		p.OrderedBy = order.label

		if p.Total, p.TotalEstimate, err = rowCount(ctx, tx, oid, ident); err != nil {
			return err
		}

		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s%s LIMIT $1 OFFSET $2`, ident, order.sql),
			size, int64(page-1)*int64(size))
		if err != nil {
			return err
		}
		defer rows.Close()

		if p.Columns, err = rows.Columns(); err != nil {
			return err
		}
		types, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		bytea := make([]bool, len(types))
		for i, ct := range types {
			bytea[i] = ct.DatabaseTypeName() == "BYTEA"
		}
		p.Rows = [][]Cell{}
		values := make([]any, len(p.Columns))
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			row := make([]Cell, len(values))
			for i, v := range values {
				row[i] = formatCell(v, bytea[i])
			}
			p.Rows = append(p.Rows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

type ordering struct {
	sql   string
	label string
}

// orderBy memakai primary key supaya urutan halaman stabil. Tanpa primary
// key, tabel biasa diurutkan dengan ctid, sedangkan view tidak bisa
// diurutkan dengan pasti.
func orderBy(ctx context.Context, tx *sql.Tx, oid uint32, kind string) (ordering, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_index ix
		CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, pos)
		JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
		WHERE ix.indrelid = $1 AND ix.indisprimary
		ORDER BY k.pos`, oid)
	if err != nil {
		return ordering{}, err
	}
	defer rows.Close()
	var cols, quoted []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return ordering{}, err
		}
		cols = append(cols, c)
		quoted = append(quoted, pgx.Identifier{c}.Sanitize())
	}
	if err := rows.Err(); err != nil {
		return ordering{}, err
	}
	switch {
	case len(cols) > 0:
		return ordering{sql: " ORDER BY " + strings.Join(quoted, ", "), label: strings.Join(cols, ", ")}, nil
	case kind == "tabel" || kind == "materialized view":
		return ordering{sql: " ORDER BY ctid", label: "ctid"}, nil
	default:
		return ordering{}, nil
	}
}

func rowCount(ctx context.Context, tx *sql.Tx, oid uint32, ident string) (int64, bool, error) {
	var estimate float64
	if err := tx.QueryRowContext(ctx, `SELECT reltuples FROM pg_class WHERE oid = $1`, oid).Scan(&estimate); err != nil {
		return 0, false, err
	}
	if estimate >= exactCountLimit {
		return int64(estimate), true, nil
	}
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+ident).Scan(&n)
	return n, false, err
}

// formatCell mengubah nilai menjadi teks. Driver juga memakai []byte untuk
// beberapa tipe selain bytea (misalnya json), jadi hex hanya dipakai kalau
// kolomnya memang bytea.
func formatCell(v any, bytea bool) Cell {
	switch x := v.(type) {
	case nil:
		return Cell{Null: true}
	case []byte:
		if !bytea {
			return truncate(string(x))
		}
		shown, cut := x, false
		if len(shown) > maxBytesShown {
			shown, cut = shown[:maxBytesShown], true
		}
		return Cell{Text: `\x` + hex.EncodeToString(shown), Truncated: cut}
	case time.Time:
		return Cell{Text: x.Format("2006-01-02 15:04:05.999999 -07:00")}
	case string:
		return truncate(x)
	default:
		return truncate(fmt.Sprint(x))
	}
}

func truncate(s string) Cell {
	if utf8.RuneCountInString(s) <= maxCellRunes {
		return Cell{Text: s}
	}
	r := []rune(s)
	return Cell{Text: string(r[:maxCellRunes]), Truncated: true}
}

func readOnlyMySQL(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SanitizeError(err)
	}
	defer tx.Rollback()
	return viewerError(fn(tx))
}

func listTablesMySQL(ctx context.Context, db *sql.DB) ([]Table, error) {
	list := []Table{}
	err := readOnlyMySQL(ctx, db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE()
			ORDER BY TABLE_SCHEMA, TABLE_NAME`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Table
			var rawKind string
			if err := rows.Scan(&t.Schema, &t.Name, &rawKind); err != nil {
				return err
			}
			if strings.EqualFold(rawKind, "VIEW") {
				t.Kind = "view"
			} else {
				t.Kind = "tabel"
			}
			list = append(list, t)
		}
		return rows.Err()
	})
	return list, err
}

func describeTableMySQL(ctx context.Context, db *sql.DB, schema, name string) (*TableDetail, error) {
	d := &TableDetail{}
	err := readOnlyMySQL(ctx, db, func(tx *sql.Tx) error {
		var rawKind string
		err := tx.QueryRowContext(ctx, `
			SELECT TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, name,
		).Scan(&rawKind)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tabel %s.%s: %w", schema, name, domain.ErrNotFound)
		}
		if err != nil {
			return err
		}
		kind := "tabel"
		if strings.EqualFold(rawKind, "VIEW") {
			kind = "view"
		}
		d.Table = Table{Schema: schema, Name: name, Kind: kind}

		colRows, err := tx.QueryContext(ctx, `
			SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, ''), EXTRA
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION`, schema, name)
		if err != nil {
			return err
		}
		defer colRows.Close()
		d.Columns = []Column{}
		for colRows.Next() {
			var c Column
			var nullable string
			var extra string
			if err := colRows.Scan(&c.Name, &c.Type, &nullable, &c.Default, &extra); err != nil {
				return err
			}
			c.Nullable = strings.EqualFold(nullable, "YES")
			if extra != "" && c.Default == "" {
				c.Default = extra
			}
			d.Columns = append(d.Columns, c)
		}
		if err := colRows.Err(); err != nil {
			return err
		}

		conRows, err := tx.QueryContext(ctx, `
			SELECT tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE,
			       COALESCE(kcu.REFERENCED_TABLE_NAME, '')
			FROM information_schema.TABLE_CONSTRAINTS tc
			LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu
			  ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
			 AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
			 AND tc.TABLE_NAME = kcu.TABLE_NAME
			WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ?
			GROUP BY tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE, kcu.REFERENCED_TABLE_NAME
			ORDER BY tc.CONSTRAINT_NAME`, schema, name)
		if err != nil {
			return err
		}
		defer conRows.Close()
		d.Constraints = []Constraint{}
		for conRows.Next() {
			var con Constraint
			var conType string
			if err := conRows.Scan(&con.Name, &conType, &con.References); err != nil {
				return err
			}
			con.Type = conType
			d.Constraints = append(d.Constraints, con)
		}
		if err := conRows.Err(); err != nil {
			return err
		}

		idxRows, err := tx.QueryContext(ctx, `
			SELECT INDEX_NAME, NON_UNIQUE
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			GROUP BY INDEX_NAME, NON_UNIQUE
			ORDER BY (INDEX_NAME = 'PRIMARY') DESC, INDEX_NAME`, schema, name)
		if err != nil {
			return err
		}
		defer idxRows.Close()
		d.Indexes = []Index{}
		for idxRows.Next() {
			var ix Index
			var nonUnique int
			if err := idxRows.Scan(&ix.Name, &nonUnique); err != nil {
				return err
			}
			ix.Unique = (nonUnique == 0)
			ix.Primary = (ix.Name == "PRIMARY")
			ix.Definition = "INDEX " + ix.Name
			if ix.Primary {
				ix.Definition = "PRIMARY KEY"
			}
			d.Indexes = append(d.Indexes, ix)
		}
		return idxRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func tableDataMySQL(ctx context.Context, db *sql.DB, schema, name string, page, size int) (*DataPage, error) {
	p := &DataPage{Page: page, Size: size}
	err := readOnlyMySQL(ctx, db, func(tx *sql.Tx) error {
		var rawKind string
		err := tx.QueryRowContext(ctx, `
			SELECT TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, name,
		).Scan(&rawKind)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tabel %s.%s: %w", schema, name, domain.ErrNotFound)
		}
		if err != nil {
			return err
		}
		kind := "tabel"
		if strings.EqualFold(rawKind, "VIEW") {
			kind = "view"
		}
		p.Table = Table{Schema: schema, Name: name, Kind: kind}

		pkRows, err := tx.QueryContext(ctx, `
			SELECT COLUMN_NAME
			FROM information_schema.KEY_COLUMN_USAGE
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
			ORDER BY ORDINAL_POSITION`, schema, name)
		if err != nil {
			return err
		}
		defer pkRows.Close()
		var pkCols, quotedPKCols []string
		for pkRows.Next() {
			var col string
			if err := pkRows.Scan(&col); err != nil {
				return err
			}
			pkCols = append(pkCols, col)
			quotedPKCols = append(quotedPKCols, mysqlQuoteIdent(col))
		}
		if err := pkRows.Err(); err != nil {
			return err
		}

		orderSQL := ""
		if len(pkCols) > 0 {
			orderSQL = " ORDER BY " + strings.Join(quotedPKCols, ", ")
			p.OrderedBy = strings.Join(pkCols, ", ")
		}

		quotedTable := mysqlQuoteIdent(schema) + "." + mysqlQuoteIdent(name)
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quotedTable).Scan(&p.Total); err != nil {
			return err
		}

		offset := int64(page-1) * int64(size)
		dataQuery := fmt.Sprintf("SELECT * FROM %s%s LIMIT ? OFFSET ?", quotedTable, orderSQL)
		rows, err := tx.QueryContext(ctx, dataQuery, size, offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		if p.Columns, err = rows.Columns(); err != nil {
			return err
		}
		types, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		isBinary := make([]bool, len(types))
		for i, ct := range types {
			tName := strings.ToUpper(ct.DatabaseTypeName())
			isBinary[i] = strings.Contains(tName, "BLOB") || strings.Contains(tName, "BINARY")
		}

		p.Rows = [][]Cell{}
		values := make([]any, len(p.Columns))
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			row := make([]Cell, len(values))
			for i, v := range values {
				row[i] = formatCell(v, isBinary[i])
			}
			p.Rows = append(p.Rows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func mysqlQuoteIdent(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
