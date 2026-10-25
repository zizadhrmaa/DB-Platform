package dbmanager_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
	"github.com/zizadhrmaa/DB-Platform/internal/testutil"
)

// sampleDB membuat database target kecil yang memuat berbagai jenis objek.
func sampleDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, schema := testutil.TargetDB(t)
	_, err := db.Exec(`
		CREATE TABLE customers (
			id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			email      TEXT NOT NULL UNIQUE,
			note       TEXT,
			photo      BYTEA,
			profile    JSONB,
			score      INTEGER CHECK (score >= 0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE orders (
			id          BIGINT PRIMARY KEY,
			customer_id BIGINT REFERENCES customers(id),
			total       NUMERIC(10,2) DEFAULT 0
		);
		CREATE INDEX orders_customer_idx ON orders (customer_id);
		CREATE VIEW top_customers AS SELECT id, email FROM customers WHERE score > 10;
		CREATE TABLE "Audit Log" ("When" TIMESTAMPTZ, msg TEXT);
		CREATE TABLE victim (x INT);

		INSERT INTO customers (email, note, score)
		SELECT 'user' || g || '@example.com', 'catatan ' || g, g FROM generate_series(1, 120) g;
		UPDATE customers SET note = NULL WHERE id = 1;
		UPDATE customers SET note = '' WHERE id = 2;
		UPDATE customers SET note = repeat('panjang ', 100), photo = decode(repeat('ab', 100), 'hex') WHERE id = 3;
		UPDATE customers SET note = '<script>alert(1)</script>', profile = '{"a": 1}' WHERE id = 4;
		INSERT INTO "Audit Log" VALUES (now(), 'login');`)
	if err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func TestListTables(t *testing.T) {
	db, schema := sampleDB(t)
	tables, err := dbmanager.ListTables(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tb := range tables {
		if tb.Schema == schema {
			got[tb.Name] = tb.Kind
		}
		if tb.Schema == "pg_catalog" || tb.Schema == "information_schema" {
			t.Errorf("schema sistem ikut tampil: %s.%s", tb.Schema, tb.Name)
		}
	}
	want := map[string]string{"customers": "tabel", "orders": "tabel", "top_customers": "view", "Audit Log": "tabel", "victim": "tabel"}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: jenis %q, want %q", name, got[name], kind)
		}
	}
}

func TestDescribeTable(t *testing.T) {
	db, schema := sampleDB(t)
	ctx := context.Background()

	c, err := dbmanager.DescribeTable(ctx, db, schema, "customers")
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]dbmanager.Column{}
	for _, col := range c.Columns {
		cols[col.Name] = col
	}
	if len(c.Columns) != 7 || c.Columns[0].Name != "id" {
		t.Errorf("kolom tidak lengkap atau tidak urut: %+v", c.Columns)
	}
	if col := cols["id"]; col.Type != "bigint" || col.Nullable || col.Default != "GENERATED ALWAYS AS IDENTITY" {
		t.Errorf("kolom id: %+v", col)
	}
	if col := cols["note"]; col.Type != "text" || !col.Nullable {
		t.Errorf("kolom note: %+v", col)
	}
	if col := cols["created_at"]; col.Type != "timestamp with time zone" || col.Default != "now()" {
		t.Errorf("kolom created_at: %+v", col)
	}

	types := map[string]string{}
	for _, con := range c.Constraints {
		types[con.Type] = con.Definition
	}
	if !strings.HasPrefix(types["PRIMARY KEY"], "PRIMARY KEY (id)") || !strings.Contains(types["UNIQUE"], "email") ||
		!strings.Contains(types["CHECK"], "score >= 0") {
		t.Errorf("constraint customers: %+v", c.Constraints)
	}
	if len(c.Indexes) < 2 || !c.Indexes[0].Primary {
		t.Errorf("index customers: %+v", c.Indexes)
	}

	o, err := dbmanager.DescribeTable(ctx, db, schema, "orders")
	if err != nil {
		t.Fatal(err)
	}
	var fk *dbmanager.Constraint
	for i := range o.Constraints {
		if o.Constraints[i].Type == "FOREIGN KEY" {
			fk = &o.Constraints[i]
		}
	}
	if fk == nil || !strings.Contains(fk.References, "customers") || !strings.Contains(fk.Definition, "REFERENCES") {
		t.Errorf("foreign key orders: %+v", o.Constraints)
	}
	found := false
	for _, ix := range o.Indexes {
		found = found || (ix.Name == "orders_customer_idx" && strings.Contains(ix.Definition, "(customer_id)"))
	}
	if !found {
		t.Errorf("index orders_customer_idx tidak ditemukan: %+v", o.Indexes)
	}

	if v, err := dbmanager.DescribeTable(ctx, db, schema, "top_customers"); err != nil || v.Kind != "view" || len(v.Columns) != 2 {
		t.Errorf("view: %+v err=%v", v, err)
	}
	if a, err := dbmanager.DescribeTable(ctx, db, schema, "Audit Log"); err != nil || len(a.Columns) != 2 {
		t.Errorf("tabel bernama aneh: %+v err=%v", a, err)
	}
}

func TestUnknownOrInjectedNamesAreNotFound(t *testing.T) {
	db, schema := sampleDB(t)
	ctx := context.Background()
	for _, c := range []struct{ schema, name string }{
		{schema, "tidak_ada"},
		{"schema_tidak_ada", "customers"},
		{schema, `victim"; DROP TABLE victim; --`},
		{schema + `"; DROP TABLE victim; --`, "victim"},
		{"pg_catalog", "pg_class"},
	} {
		if _, err := dbmanager.DescribeTable(ctx, db, c.schema, c.name); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("DescribeTable(%q, %q): err=%v, want ErrNotFound", c.schema, c.name, err)
		}
		if _, err := dbmanager.TableData(ctx, db, c.schema, c.name, 1, 10); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("TableData(%q, %q): err=%v, want ErrNotFound", c.schema, c.name, err)
		}
	}
	if _, err := db.Exec(`SELECT * FROM victim`); err != nil {
		t.Fatalf("tabel victim hilang: %v", err)
	}
}

func TestTableDataPagination(t *testing.T) {
	db, schema := sampleDB(t)
	ctx := context.Background()

	p, err := dbmanager.TableData(ctx, db, schema, "customers", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 120 || p.TotalEstimate || len(p.Rows) != 50 || p.OrderedBy != "id" {
		t.Fatalf("halaman 1: total=%d estimate=%v rows=%d order=%q", p.Total, p.TotalEstimate, len(p.Rows), p.OrderedBy)
	}
	if p.HasPrev() || !p.HasNext() || p.PageCount() != 3 {
		t.Errorf("navigasi halaman 1 salah: prev=%v next=%v count=%d", p.HasPrev(), p.HasNext(), p.PageCount())
	}
	if p.Columns[0] != "id" || p.Rows[0][0].Text != "1" {
		t.Errorf("baris pertama bukan id 1: %v %+v", p.Columns, p.Rows[0][0])
	}

	p2, err := dbmanager.TableData(ctx, db, schema, "customers", 2, 50)
	if err != nil || p2.Rows[0][0].Text != "51" {
		t.Fatalf("halaman 2 tidak dimulai dari id 51: %+v err=%v", p2.Rows[0][0], err)
	}
	p3, err := dbmanager.TableData(ctx, db, schema, "customers", 3, 50)
	if err != nil || len(p3.Rows) != 20 || p3.HasNext() {
		t.Fatalf("halaman 3: rows=%d next=%v err=%v", len(p3.Rows), p3.HasNext(), err)
	}
	p9, err := dbmanager.TableData(ctx, db, schema, "customers", 9, 50)
	if err != nil || len(p9.Rows) != 0 {
		t.Fatalf("halaman di luar jangkauan: rows=%d err=%v", len(p9.Rows), err)
	}

	empty, err := dbmanager.TableData(ctx, db, schema, "orders", 1, 50)
	if err != nil || empty.Total != 0 || len(empty.Rows) != 0 || empty.PageCount() != 1 {
		t.Fatalf("tabel kosong: %+v err=%v", empty, err)
	}

	audit, err := dbmanager.TableData(ctx, db, schema, "Audit Log", 1, 50)
	if err != nil || audit.OrderedBy != "ctid" || len(audit.Rows) != 1 {
		t.Fatalf("tabel tanpa PK: %+v err=%v", audit, err)
	}
	view, err := dbmanager.TableData(ctx, db, schema, "top_customers", 1, 50)
	if err != nil || view.OrderedBy != "" || view.Total != 110 {
		t.Fatalf("view: order=%q total=%d err=%v", view.OrderedBy, view.Total, err)
	}
}

func TestClampPage(t *testing.T) {
	for _, c := range []struct {
		page, size         string
		wantPage, wantSize int
	}{
		{"", "", 1, dbmanager.DefaultPageSize},
		{"3", "20", 3, 20},
		{"0", "9999", 1, dbmanager.MaxPageSize},
		{"-5", "-1", 1, dbmanager.DefaultPageSize},
		{"abc", "x", 1, dbmanager.DefaultPageSize},
	} {
		if p, s := dbmanager.ClampPage(c.page, c.size); p != c.wantPage || s != c.wantSize {
			t.Errorf("ClampPage(%q, %q) = %d, %d; want %d, %d", c.page, c.size, p, s, c.wantPage, c.wantSize)
		}
	}
}

func TestCellFormatting(t *testing.T) {
	db, schema := sampleDB(t)
	p, err := dbmanager.TableData(context.Background(), db, schema, "customers", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, name := range p.Columns {
		col[name] = i
	}
	note, photo, profile := col["note"], col["photo"], col["profile"]

	if c := p.Rows[0][note]; !c.Null {
		t.Errorf("NULL tidak ditandai: %+v", c)
	}
	if c := p.Rows[1][note]; c.Null || c.Text != "" {
		t.Errorf("string kosong dianggap NULL: %+v", c)
	}
	if c := p.Rows[2][note]; !c.Truncated || len([]rune(c.Text)) != 200 {
		t.Errorf("teks panjang tidak dipotong: %d karakter, truncated=%v", len([]rune(c.Text)), c.Truncated)
	}
	if c := p.Rows[2][photo]; !strings.HasPrefix(c.Text, `\xabab`) || !c.Truncated {
		t.Errorf("bytea: %+v", c)
	}
	if c := p.Rows[3][profile]; c.Text != `{"a": 1}` {
		t.Errorf("jsonb seharusnya tampil sebagai teks: %+v", c)
	}
	if c := p.Rows[3][note]; c.Text != "<script>alert(1)</script>" {
		t.Errorf("teks asli berubah: %+v", c)
	}
}

func TestNoSelectPrivilegeIsForbidden(t *testing.T) {
	_, schema := sampleDB(t)
	admin, err := sql.Open("pgx", testutil.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	role := "viewer_" + strings.TrimPrefix(schema, "t_")
	if _, err := admin.Exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'rahasia'`, role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP ROLE IF EXISTS ` + role) })

	cfg, err := pgx.ParseConfig(testutil.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = role, "rahasia"
	limited := stdlib.OpenDB(*cfg)
	defer limited.Close()

	_, err = dbmanager.TableData(context.Background(), limited, schema, "customers", 1, 10)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("tanpa hak SELECT: err=%v, want ErrForbidden", err)
	}
}
