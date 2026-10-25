package migration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type SchemaDiff struct {
	Schema  string
	Changes []SchemaChange
	SQL     string
}

type SchemaChange struct {
	Action      string // Tambah, Hapus, atau Ubah
	Object      string // misalnya "kolom items.price"
	Detail      string
	Destructive bool // menghapus data
}

// View, function, trigger, tipe custom, dan tabel partisi tidak ikut.
type snapshot struct {
	tables map[string]*tableDef
	names  []string
	// FK ke schema lain. Tabel temporary tidak bisa punya FK ke tabel biasa,
	// jadi FK seperti ini tidak dibandingkan dan tidak pernah diubah.
	external []string
}

type tableDef struct {
	name, quoted string
	columns      []columnDef
	constraints  []constraintDef
	indexes      []indexDef
}

type columnDef struct {
	name, quoted, typ string
	notNull           bool
	def               string // ekspresi default, atau ekspresi generated column
	identity          string // "a" (ALWAYS), "d" (BY DEFAULT), atau kosong
	generated         bool
	serial            bool
}

type constraintDef struct {
	name, quoted, kind, def string // kind: p, u, c, f, x
}

type indexDef struct {
	name, quoted, def string
}

func (s *Store) Schemas(ctx context.Context, dbID int64) ([]string, error) {
	target, e := s.Targets.Conn(ctx, dbID)
	if e != nil {
		return nil, e
	}
	rows, e := target.QueryContext(ctx, `
		SELECT nspname FROM pg_namespace
		WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg\_%'
		ORDER BY nspname`)
	if e != nil {
		return nil, dbmanager.SanitizeError(e)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CurrentSchema(ctx context.Context, u *domain.User, dbID int64, schema string) (string, error) {
	var ddl string
	e := s.inTarget(ctx, u, dbID, func(work context.Context, tx *sql.Tx) error {
		cur, e := loadSnapshot(work, tx, schema)
		if e != nil {
			return e
		}
		ddl = renderSchema(cur)
		return nil
	})
	return ddl, e
}

// Diff menjalankan DDL desired di pg_temp dalam transaction yang selalu
// di-rollback, lalu membandingkan hasilnya dengan schema saat ini.
func (s *Store) Diff(ctx context.Context, u *domain.User, dbID int64, schema, desired string) (*SchemaDiff, error) {
	if e := ValidateSQL(desired); e != nil {
		return nil, e
	}
	if e := checkUnqualified(desired, schema); e != nil {
		return nil, e
	}
	var d *SchemaDiff
	e := s.inTarget(ctx, u, dbID, func(work context.Context, tx *sql.Tx) error {
		cur, e := loadSnapshot(work, tx, schema)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(work, `SELECT set_config('search_path', 'pg_temp, pg_catalog', true)`); e != nil {
			return e
		}
		for i, stmt := range splitStatements(desired) {
			if e = runStatement(work, tx, stmt); e != nil {
				msg := errorMessage(e)
				if strings.Contains(msg, "may reference only temporary tables") {
					msg = "foreign key ke tabel di schema lain belum bisa dibandingkan. Hapus constraint itu dari DDL; constraint yang sudah ada di database tidak akan diubah"
				}
				return fmt.Errorf("%w: statement ke-%d gagal: %s", domain.ErrInvalidInput, i+1, msg)
			}
		}
		var temp sql.NullString
		if e = tx.QueryRowContext(work, `SELECT nspname FROM pg_namespace WHERE oid = pg_my_temp_schema()`).Scan(&temp); e != nil && e != sql.ErrNoRows {
			return e
		}
		want := &snapshot{tables: map[string]*tableDef{}}
		if temp.Valid {
			if want, e = loadSnapshot(work, tx, temp.String); e != nil {
				return e
			}
		}
		d = diffSnapshots(cur, want)
		d.Schema = schema
		return nil
	})
	return d, e
}

// inTarget menjalankan fn di transaction database target yang selalu
// di-rollback, setelah memastikan user boleh membuat migration.
func (s *Store) inTarget(ctx context.Context, u *domain.User, dbID int64, fn func(context.Context, *sql.Tx) error) error {
	if u == nil {
		return domain.ErrUnauthenticated
	}
	if s.Authz == nil || s.Targets == nil {
		return fmt.Errorf("schema diff dependencies not initialized")
	}
	if e := s.Authz.Authorize(ctx, u, dbID, authz.ActionCreateMigration); e != nil {
		return e
	}
	target, e := s.Targets.Conn(ctx, dbID)
	if e != nil {
		return e
	}
	work, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, e := target.BeginTx(work, nil)
	if e != nil {
		return dbmanager.SanitizeError(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(work, `SET LOCAL lock_timeout='5s'`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(work, `SET LOCAL statement_timeout='30s'`); e != nil {
		return e
	}
	return fn(work, tx)
}

// checkUnqualified menolak DDL yang menyebut schema target secara
// langsung, karena objek itu akan dibuat atau diubah di schema asli,
// bukan di schema sementara.
func checkUnqualified(desired, schema string) error {
	masked := strings.ToLower(stripCommentsAndStrings(desired))
	lower := strings.ToLower(desired)
	name := strings.ToLower(schema)
	if strings.Contains(masked, name+".") || strings.Contains(lower, `"`+name+`".`) {
		return fmt.Errorf("%w: tulis nama tabel tanpa nama schema (misalnya items, bukan %s.items)", domain.ErrInvalidInput, schema)
	}
	return nil
}

func loadSnapshot(ctx context.Context, tx *sql.Tx, schema string) (*snapshot, error) {
	var exists bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		return nil, fmt.Errorf("%w: schema %s", domain.ErrNotFound, schema)
	}
	// Dengan search_path diarahkan ke schema ini, definisi dari pg_get_*
	// tidak memuat nama schema, jadi bisa dibandingkan antar schema.
	if _, e := tx.ExecContext(ctx, `SELECT set_config('search_path', quote_ident($1) || ', pg_catalog', true)`, schema); e != nil {
		return nil, e
	}
	var prefix string
	if e := tx.QueryRowContext(ctx, `SELECT quote_ident($1) || '.'`, schema).Scan(&prefix); e != nil {
		return nil, e
	}
	unqualify := func(s string) string { return strings.ReplaceAll(s, prefix, "") }
	if strings.HasPrefix(schema, "pg_temp_") {
		// Untuk tabel temporary, PostgreSQL menulis nama aliasnya (pg_temp.),
		// bukan pg_temp_N.
		unqualify = func(s string) string {
			return strings.ReplaceAll(strings.ReplaceAll(s, prefix, ""), "pg_temp.", "")
		}
	}

	snap := &snapshot{tables: map[string]*tableDef{}}
	// _dbplatform_history milik platform (pelacak eksekusi migration),
	// jadi tidak boleh ikut dibandingkan apalagi dihapus.
	rows, e := tx.QueryContext(ctx, `
		SELECT c.relname, quote_ident(c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' AND NOT c.relispartition AND c.relname <> '_dbplatform_history'
		ORDER BY c.relname`, schema)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		t := &tableDef{}
		if e = rows.Scan(&t.name, &t.quoted); e != nil {
			rows.Close()
			return nil, e
		}
		snap.tables[t.name] = t
		snap.names = append(snap.names, t.name)
	}
	if e = closeRows(rows); e != nil {
		return nil, e
	}

	rows, e = tx.QueryContext(ctx, `
		SELECT c.relname, a.attname, quote_ident(a.attname), format_type(a.atttypid, a.atttypmod), a.attnotnull,
		       coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity::text, a.attgenerated::text,
		       EXISTS (SELECT 1 FROM pg_depend dep JOIN pg_class seq ON seq.oid = dep.objid
		               WHERE dep.classid = 'pg_class'::regclass AND dep.refobjid = c.oid
		                 AND dep.refobjsubid = a.attnum AND dep.deptype = 'a' AND seq.relkind = 'S')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relkind = 'r' AND NOT c.relispartition AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`, schema)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var table, generated string
		var ownedSeq bool
		var c columnDef
		if e = rows.Scan(&table, &c.name, &c.quoted, &c.typ, &c.notNull, &c.def, &c.identity, &generated, &ownedSeq); e != nil {
			rows.Close()
			return nil, e
		}
		c.def, c.generated = unqualify(c.def), generated == "s"
		// Kolom serial adalah integer dengan default nextval dari sequence
		// miliknya sendiri. Nama sequence-nya berbeda antar schema, jadi
		// dibandingkan sebagai "serial" saja.
		if ownedSeq && c.identity == "" && strings.HasPrefix(c.def, "nextval(") {
			c.serial, c.def = true, ""
		}
		if t := snap.tables[table]; t != nil {
			t.columns = append(t.columns, c)
		}
	}
	if e = closeRows(rows); e != nil {
		return nil, e
	}

	rows, e = tx.QueryContext(ctx, `
		SELECT c.relname, con.conname, quote_ident(con.conname), con.contype::text, pg_get_constraintdef(con.oid),
		       con.contype = 'f' AND (SELECT r.relnamespace FROM pg_class r WHERE r.oid = con.confrelid) <> c.relnamespace
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' AND NOT c.relispartition AND con.contype IN ('p', 'u', 'c', 'f', 'x')
		ORDER BY c.relname, con.conname`, schema)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var table string
		var con constraintDef
		var external bool
		if e = rows.Scan(&table, &con.name, &con.quoted, &con.kind, &con.def, &external); e != nil {
			rows.Close()
			return nil, e
		}
		con.def = unqualify(con.def)
		t := snap.tables[table]
		switch {
		case t == nil:
		case external:
			snap.external = append(snap.external, addConstraintSQL(t, con))
		default:
			t.constraints = append(t.constraints, con)
		}
	}
	if e = closeRows(rows); e != nil {
		return nil, e
	}

	// Index yang dibuat otomatis oleh PRIMARY KEY, UNIQUE, atau EXCLUDE
	// sudah terwakili oleh constraint-nya.
	rows, e = tx.QueryContext(ctx, `
		SELECT c.relname, i.relname, quote_ident(i.relname), pg_get_indexdef(ix.indexrelid)
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class c ON c.oid = ix.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' AND NOT c.relispartition
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint con
		                  WHERE con.conindid = ix.indexrelid AND con.conrelid = ix.indrelid AND con.contype IN ('p', 'u', 'x'))
		ORDER BY c.relname, i.relname`, schema)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var table string
		var ix indexDef
		if e = rows.Scan(&table, &ix.name, &ix.quoted, &ix.def); e != nil {
			rows.Close()
			return nil, e
		}
		ix.def = unqualify(ix.def)
		if t := snap.tables[table]; t != nil {
			t.indexes = append(t.indexes, ix)
		}
	}
	return snap, closeRows(rows)
}

func closeRows(rows *sql.Rows) error {
	e := rows.Err()
	if ce := rows.Close(); e == nil {
		e = ce
	}
	return e
}

func serialType(typ string) string {
	switch typ {
	case "bigint":
		return "bigserial"
	case "smallint":
		return "smallserial"
	}
	return "serial"
}

func columnSQL(c columnDef) string {
	typ := c.typ
	if c.serial {
		typ = serialType(c.typ)
	}
	s := c.quoted + " " + typ
	switch {
	case c.generated:
		s += " GENERATED ALWAYS AS (" + c.def + ") STORED"
	case c.identity != "":
		s += " " + identitySQL(c.identity)
	case c.def != "":
		s += " DEFAULT " + c.def
	}
	if c.notNull && !c.serial && c.identity == "" {
		s += " NOT NULL"
	}
	return s
}

func identitySQL(identity string) string {
	if identity == "a" {
		return "GENERATED ALWAYS AS IDENTITY"
	}
	return "GENERATED BY DEFAULT AS IDENTITY"
}

func createTableSQL(t *tableDef) string {
	var parts []string
	for _, c := range t.columns {
		parts = append(parts, "    "+columnSQL(c))
	}
	for _, con := range t.constraints {
		if con.kind != "f" {
			parts = append(parts, "    CONSTRAINT "+con.quoted+" "+con.def)
		}
	}
	return "CREATE TABLE " + t.quoted + " (\n" + strings.Join(parts, ",\n") + "\n)"
}

func addConstraintSQL(t *tableDef, con constraintDef) string {
	return "ALTER TABLE " + t.quoted + " ADD CONSTRAINT " + con.quoted + " " + con.def
}

// renderSchema menulis DDL lengkap sebuah schema. Foreign key ditulis
// setelah semua tabel dibuat supaya urutan tabel tidak jadi masalah.
func renderSchema(snap *snapshot) string {
	var stmts []string
	for _, name := range snap.names {
		stmts = append(stmts, createTableSQL(snap.tables[name]))
	}
	for _, name := range snap.names {
		t := snap.tables[name]
		for _, con := range t.constraints {
			if con.kind == "f" {
				stmts = append(stmts, addConstraintSQL(t, con))
			}
		}
	}
	for _, name := range snap.names {
		for _, ix := range snap.tables[name].indexes {
			stmts = append(stmts, ix.def)
		}
	}
	if len(snap.external) > 0 {
		lines := []string{"-- Foreign key berikut menunjuk ke tabel di schema lain. Constraint ini tidak ikut",
			"-- dibandingkan dan tidak akan diubah oleh schema diff:"}
		for _, fk := range snap.external {
			lines = append(lines, "--   "+fk)
		}
		stmts = append(stmts, strings.Join(lines, "\n"))
	}
	if len(stmts) == 0 {
		return ""
	}
	return strings.Join(stmts, ";\n\n") + ";\n"
}

// Urutan fase SQL hasil diff. Constraint dan index lama dihapus dulu,
// foreign key ditambahkan paling akhir setelah semua tabel dan kolom ada.
const (
	phaseDropConstraint = iota
	phaseDropIndex
	phaseDropTable
	phaseCreateTable
	phaseColumns
	phaseAddConstraint
	phaseAddForeignKey
	phaseCreateIndex
	phaseCount
)

type diffBuilder struct {
	changes []SchemaChange
	phases  [phaseCount][]string
}

func (b *diffBuilder) add(phase int, stmt string, ch SchemaChange) {
	if ch.Destructive {
		stmt = "-- Hati-hati: statement ini menghapus data.\n" + stmt
	}
	b.phases[phase] = append(b.phases[phase], stmt)
	b.changes = append(b.changes, ch)
}

func diffSnapshots(cur, want *snapshot) *SchemaDiff {
	b := &diffBuilder{}

	for _, name := range cur.names {
		if _, ok := want.tables[name]; !ok {
			t := cur.tables[name]
			for _, con := range t.constraints {
				if con.kind == "f" {
					b.phases[phaseDropConstraint] = append(b.phases[phaseDropConstraint],
						"ALTER TABLE "+t.quoted+" DROP CONSTRAINT "+con.quoted)
				}
			}
			b.add(phaseDropTable, "DROP TABLE "+t.quoted, SchemaChange{Action: "Hapus", Object: "tabel " + name, Destructive: true})
		}
	}
	for _, name := range want.names {
		t := want.tables[name]
		old, ok := cur.tables[name]
		if !ok {
			b.add(phaseCreateTable, createTableSQL(t), SchemaChange{Action: "Tambah", Object: "tabel " + name})
			for _, con := range t.constraints {
				if con.kind == "f" {
					b.add(phaseAddForeignKey, addConstraintSQL(t, con),
						SchemaChange{Action: "Tambah", Object: "constraint " + name + "." + con.name, Detail: con.def})
				}
			}
			for _, ix := range t.indexes {
				b.add(phaseCreateIndex, ix.def, SchemaChange{Action: "Tambah", Object: "index " + ix.name, Detail: ix.def})
			}
			continue
		}
		b.diffColumns(old, t)
		b.diffConstraints(old, t)
		b.diffIndexes(old, t)
	}

	d := &SchemaDiff{Changes: b.changes}
	var stmts []string
	for _, phase := range b.phases {
		stmts = append(stmts, phase...)
	}
	if len(stmts) > 0 {
		d.SQL = strings.Join(stmts, ";\n") + ";\n"
	}
	return d
}

func (b *diffBuilder) diffColumns(old, t *tableDef) {
	oldCols := map[string]columnDef{}
	for _, c := range old.columns {
		oldCols[c.name] = c
	}
	newCols := map[string]bool{}
	alter := "ALTER TABLE " + t.quoted + " "
	for _, c := range t.columns {
		newCols[c.name] = true
		obj := "kolom " + t.name + "." + c.name
		prev, ok := oldCols[c.name]
		if !ok {
			b.add(phaseColumns, alter+"ADD COLUMN "+columnSQL(c), SchemaChange{Action: "Tambah", Object: obj, Detail: columnSQL(c)})
			continue
		}
		col := alter + "ALTER COLUMN " + c.quoted + " "
		if prev.serial != c.serial || prev.generated != c.generated || (prev.generated && prev.def != c.def) {
			// Tidak ada ALTER yang aman untuk perubahan ini, jadi dibiarkan
			// sebagai komentar supaya user menulisnya sendiri.
			b.add(phaseColumns, "-- Ubah manual: kolom "+t.name+"."+c.name+" berubah dari "+columnSQL(prev)+" menjadi "+columnSQL(c),
				SchemaChange{Action: "Ubah", Object: obj, Detail: "perubahan serial atau generated column perlu ditulis manual"})
			continue
		}
		if prev.typ != c.typ {
			b.add(phaseColumns, col+"TYPE "+c.typ, SchemaChange{Action: "Ubah", Object: obj, Detail: "tipe " + prev.typ + " menjadi " + c.typ})
		}
		// Identity hanya bisa ditambahkan ke kolom yang sudah NOT NULL.
		if c.notNull && !prev.notNull {
			b.add(phaseColumns, col+"SET NOT NULL", SchemaChange{Action: "Ubah", Object: obj, Detail: "menjadi NOT NULL"})
		}
		switch {
		case prev.identity == c.identity:
		case prev.identity == "":
			b.add(phaseColumns, col+"ADD "+identitySQL(c.identity),
				SchemaChange{Action: "Ubah", Object: obj, Detail: "menjadi identity"})
		case c.identity == "":
			b.add(phaseColumns, col+"DROP IDENTITY", SchemaChange{Action: "Ubah", Object: obj, Detail: "bukan identity lagi"})
		case c.identity == "a":
			b.add(phaseColumns, col+"SET GENERATED ALWAYS", SchemaChange{Action: "Ubah", Object: obj, Detail: "identity menjadi ALWAYS"})
		default:
			b.add(phaseColumns, col+"SET GENERATED BY DEFAULT", SchemaChange{Action: "Ubah", Object: obj, Detail: "identity menjadi BY DEFAULT"})
		}
		if prev.def != c.def && !c.generated {
			if c.def == "" {
				b.add(phaseColumns, col+"DROP DEFAULT", SchemaChange{Action: "Ubah", Object: obj, Detail: "default dihapus"})
			} else {
				b.add(phaseColumns, col+"SET DEFAULT "+c.def, SchemaChange{Action: "Ubah", Object: obj, Detail: "default menjadi " + c.def})
			}
		}
		// DROP IDENTITY tidak ikut membuang NOT NULL.
		if !c.notNull && prev.notNull {
			b.add(phaseColumns, col+"DROP NOT NULL", SchemaChange{Action: "Ubah", Object: obj, Detail: "boleh NULL"})
		}
	}
	for _, c := range old.columns {
		if !newCols[c.name] {
			b.add(phaseColumns, alter+"DROP COLUMN "+c.quoted,
				SchemaChange{Action: "Hapus", Object: "kolom " + t.name + "." + c.name, Destructive: true})
		}
	}
}

func (b *diffBuilder) diffConstraints(old, t *tableDef) {
	oldCons := map[string]constraintDef{}
	for _, con := range old.constraints {
		oldCons[con.name] = con
	}
	newCons := map[string]constraintDef{}
	for _, con := range t.constraints {
		newCons[con.name] = con
	}
	// Foreign key dihapus lebih dulu supaya constraint yang dirujuknya
	// bisa ikut dihapus.
	sorted := append([]constraintDef(nil), old.constraints...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].kind == "f" && sorted[j].kind != "f" })
	for _, con := range sorted {
		if n, ok := newCons[con.name]; ok && n.def == con.def {
			continue
		}
		b.add(phaseDropConstraint, "ALTER TABLE "+t.quoted+" DROP CONSTRAINT "+con.quoted,
			SchemaChange{Action: "Hapus", Object: "constraint " + t.name + "." + con.name, Detail: con.def})
	}
	for _, con := range t.constraints {
		if o, ok := oldCons[con.name]; ok && o.def == con.def {
			continue
		}
		phase := phaseAddConstraint
		if con.kind == "f" {
			phase = phaseAddForeignKey
		}
		b.add(phase, addConstraintSQL(t, con), SchemaChange{Action: "Tambah", Object: "constraint " + t.name + "." + con.name, Detail: con.def})
	}
}

func (b *diffBuilder) diffIndexes(old, t *tableDef) {
	oldIdx := map[string]indexDef{}
	for _, ix := range old.indexes {
		oldIdx[ix.name] = ix
	}
	newIdx := map[string]indexDef{}
	for _, ix := range t.indexes {
		newIdx[ix.name] = ix
	}
	for _, ix := range old.indexes {
		if n, ok := newIdx[ix.name]; ok && n.def == ix.def {
			continue
		}
		b.add(phaseDropIndex, "DROP INDEX "+ix.quoted, SchemaChange{Action: "Hapus", Object: "index " + ix.name, Detail: ix.def})
	}
	for _, ix := range t.indexes {
		if o, ok := oldIdx[ix.name]; ok && o.def == ix.def {
			continue
		}
		b.add(phaseCreateIndex, ix.def, SchemaChange{Action: "Tambah", Object: "index " + ix.name, Detail: ix.def})
	}
}
