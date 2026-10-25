package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

// diffEnv menyiapkan database target berisi berbagai jenis objek.
func diffEnv(t *testing.T) (*executeEnv, string) {
	t.Helper()
	env := newExecuteEnv(t)
	if _, err := env.target.Exec(`
		CREATE TABLE customers (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			note TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE orders (
			id SERIAL PRIMARY KEY,
			customer_id BIGINT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'paid')),
			qty INTEGER NOT NULL,
			price NUMERIC(10,2) NOT NULL,
			total NUMERIC(12,2) GENERATED ALWAYS AS (qty * price) STORED
		);
		CREATE INDEX orders_customer_idx ON orders (customer_id);
		CREATE INDEX orders_paid_idx ON orders (id) WHERE status = 'paid';
		CREATE TABLE "Audit Log" ("When" TIMESTAMPTZ, msg TEXT);
		CREATE TABLE lama (id INT);
		INSERT INTO customers (email) VALUES ('a@x'), ('b@x');`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := env.target.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return env, schema
}

func TestCurrentSchemaRoundTrip(t *testing.T) {
	ctx := context.Background()
	env, schema := diffEnv(t)

	ddl, err := env.store.CurrentSchema(ctx, env.user, env.dbID, schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CREATE TABLE customers`, `GENERATED ALWAYS AS IDENTITY`, `id serial`, `DEFAULT now()`,
		`GENERATED ALWAYS AS (((qty)::numeric * price)) STORED`, `CHECK`, `FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE CASCADE`,
		`WHERE (status = 'paid'::text)`, `"Audit Log"`, `"When"`,
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL schema saat ini tidak memuat %q:\n%s", want, ddl)
		}
	}
	if strings.Contains(ddl, schema+".") {
		t.Errorf("DDL memuat nama schema:\n%s", ddl)
	}

	// DDL hasil pembacaan, kalau dibandingkan lagi, tidak boleh punya beda.
	d, err := env.store.Diff(ctx, env.user, env.dbID, schema, ddl)
	if err != nil {
		t.Fatalf("diff round-trip: %v\n%s", err, ddl)
	}
	if len(d.Changes) != 0 || d.SQL != "" {
		t.Fatalf("round-trip seharusnya tanpa perbedaan, dapat %+v\n%s", d.Changes, d.SQL)
	}
}

func TestDiffGeneratesWorkingSQL(t *testing.T) {
	ctx := context.Background()
	env, schema := diffEnv(t)
	ddl, err := env.store.CurrentSchema(ctx, env.user, env.dbID, schema)
	if err != nil {
		t.Fatal(err)
	}

	// Schema yang diinginkan: tabel lama dihapus, kolom note dihapus,
	// kolom phone ditambah, tipe msg berubah, CHECK status diubah,
	// tabel payments baru dengan FK, index paid diganti index status.
	desired := strings.NewReplacer(
		"CREATE TABLE lama (\n    id integer\n);\n\n", "",
		"    note text,\n", "    phone text DEFAULT 'none'::text,\n",
		"    msg text\n", "    msg character varying(200)\n",
		"'new'::text, 'paid'::text", "'new'::text, 'paid'::text, 'shipped'::text",
		"CREATE INDEX orders_paid_idx ON orders USING btree (id) WHERE (status = 'paid'::text)", "CREATE INDEX orders_status_idx ON orders USING btree (status)",
	).Replace(ddl) + `
CREATE TABLE payments (id INT PRIMARY KEY, order_id INT NOT NULL REFERENCES orders(id), amount NUMERIC(12,2) NOT NULL);`
	if desired == ddl {
		t.Fatal("penggantian DDL tidak terjadi, test perlu disesuaikan")
	}

	d, err := env.store.Diff(ctx, env.user, env.dbID, schema, desired)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	got := map[string]bool{}
	destructive := 0
	for _, c := range d.Changes {
		got[c.Action+" "+c.Object] = true
		if c.Destructive {
			destructive++
		}
	}
	for _, want := range []string{
		"Hapus tabel lama", "Hapus kolom customers.note", "Tambah kolom customers.phone",
		"Ubah kolom Audit Log.msg", "Tambah tabel payments", "Tambah constraint payments.payments_order_id_fkey",
		"Hapus index orders_paid_idx", "Tambah index orders_status_idx",
		"Hapus constraint orders.orders_status_check", "Tambah constraint orders.orders_status_check",
	} {
		if !got[want] {
			t.Errorf("perubahan %q tidak terdeteksi; dapat %v", want, got)
		}
	}
	if destructive != 2 {
		t.Errorf("seharusnya 2 perubahan yang menghapus data (tabel lama dan kolom note), dapat %d", destructive)
	}
	if strings.Count(d.SQL, "-- Hati-hati") != 2 {
		t.Errorf("peringatan hapus data tidak ada di SQL:\n%s", d.SQL)
	}

	// Diff tidak mengubah apa pun di database target.
	if env.tableExists(t, "payments") || !env.tableExists(t, "lama") {
		t.Fatal("diff mengubah database target")
	}

	// SQL hasil diff harus benar-benar bisa dijalankan, dan setelah itu
	// schema-nya sama dengan yang diinginkan.
	if _, err := env.target.Exec(d.SQL); err != nil {
		t.Fatalf("SQL hasil diff gagal dijalankan: %v\n%s", err, d.SQL)
	}
	again, err := env.store.Diff(ctx, env.user, env.dbID, schema, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Fatalf("setelah SQL dijalankan masih ada perbedaan: %+v\n%s", again.Changes, again.SQL)
	}
	var n int
	if err := env.target.QueryRow(`SELECT count(*) FROM customers`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("data customers hilang: n=%d err=%v", n, err)
	}
}

func TestDiffRejectsUnsafeInput(t *testing.T) {
	ctx := context.Background()
	env, schema := diffEnv(t)

	for name, desired := range map[string]string{
		"nama schema asli":   "CREATE TABLE " + schema + ".x (id int)",
		"BEGIN eksplisit":    "BEGIN; CREATE TABLE x (id int); COMMIT;",
		"COMMIT tersembunyi": `SELECT 'C:\'; COMMIT;`,
		"DDL salah":          "CREATE TABLE x (id tipe_tidak_ada)",
	} {
		if _, err := env.store.Diff(ctx, env.user, env.dbID, schema, desired); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: err=%v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := env.store.Diff(ctx, env.user, env.dbID, "schema_tidak_ada", "CREATE TABLE x (id int)"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("schema tidak ada: err=%v, want ErrNotFound", err)
	}
	if !env.tableExists(t, "lama") || env.tableExists(t, "x") {
		t.Fatal("database target berubah")
	}

	env.az.Levels[authz.Key{UserID: env.user.ID, DBID: env.dbID}] = domain.AccessView
	if _, err := env.store.Diff(ctx, env.user, env.dbID, schema, "CREATE TABLE x (id int)"); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("View Only: err=%v, want ErrForbidden", err)
	}
	if _, err := env.store.CurrentSchema(ctx, env.user, env.dbID, schema); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("View Only membaca DDL: err=%v, want ErrForbidden", err)
	}
}

func TestDiffColumnAttributes(t *testing.T) {
	ctx := context.Background()
	env := newExecuteEnv(t)
	if _, err := env.target.Exec(`
		CREATE TABLE akun (
			kode INTEGER,
			nama TEXT NOT NULL,
			saldo NUMERIC DEFAULT 0,
			nomor INTEGER GENERATED BY DEFAULT AS IDENTITY,
			catatan TEXT
		);
		INSERT INTO akun (kode, nama) VALUES (1, 'a'), (2, 'b');`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := env.target.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}

	// kode jadi identity (perlu NOT NULL dulu), nama boleh NULL, default
	// saldo berubah, nomor bukan identity lagi dan boleh NULL, catatan
	// mendapat default.
	desired := `CREATE TABLE akun (
		kode INTEGER GENERATED ALWAYS AS IDENTITY,
		nama TEXT,
		saldo NUMERIC DEFAULT 100,
		nomor INTEGER,
		catatan TEXT DEFAULT '-'
	);`
	d, err := env.store.Diff(ctx, env.user, env.dbID, schema, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) == 0 {
		t.Fatal("perubahan atribut kolom tidak terdeteksi")
	}
	if _, err := env.target.Exec(d.SQL); err != nil {
		t.Fatalf("SQL hasil diff gagal: %v\n%s", err, d.SQL)
	}
	again, err := env.store.Diff(ctx, env.user, env.dbID, schema, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Fatalf("masih ada perbedaan setelah SQL dijalankan: %+v\n%s", again.Changes, again.SQL)
	}
}

func TestDiffIgnoresPlatformHistoryTable(t *testing.T) {
	ctx := context.Background()
	env, schema := diffEnv(t)
	// Eksekusi migration membuat _dbplatform_history di schema target.
	m := env.create(t, "kosong", "SELECT 1")
	if _, err := env.store.Execute(ctx, env.user, env.dbID, m.ID); err != nil {
		t.Fatal(err)
	}
	if !env.tableExists(t, "_dbplatform_history") {
		t.Fatal("tabel riwayat tidak terbentuk, test perlu disesuaikan")
	}
	ddl, err := env.store.CurrentSchema(ctx, env.user, env.dbID, schema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ddl, "_dbplatform_history") {
		t.Error("DDL schema saat ini memuat tabel internal platform")
	}
	d, err := env.store.Diff(ctx, env.user, env.dbID, schema, ddl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.SQL, "_dbplatform_history") || len(d.Changes) != 0 {
		t.Fatalf("tabel internal platform ikut dibandingkan: %+v\n%s", d.Changes, d.SQL)
	}
}

func TestDiffCrossSchemaForeignKey(t *testing.T) {
	ctx := context.Background()
	env, schema := diffEnv(t)
	other := schema + "_lain"
	if _, err := env.target.Exec(`CREATE SCHEMA ` + other + `; CREATE TABLE ` + other + `.gudang (id INT PRIMARY KEY);
		CREATE TABLE stok (id INT PRIMARY KEY, gudang_id INT REFERENCES ` + other + `.gudang(id));`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { env.target.Exec(`DROP SCHEMA ` + other + ` CASCADE`) })

	ddl, err := env.store.CurrentSchema(ctx, env.user, env.dbID, schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "--   ALTER TABLE stok ADD CONSTRAINT stok_gudang_id_fkey") {
		t.Errorf("FK lintas schema seharusnya ditulis sebagai komentar:\n%s", ddl)
	}
	d, err := env.store.Diff(ctx, env.user, env.dbID, schema, ddl)
	if err != nil {
		t.Fatalf("diff dengan FK lintas schema: %v", err)
	}
	if len(d.Changes) != 0 {
		t.Fatalf("FK lintas schema tidak boleh dianggap berubah: %+v\n%s", d.Changes, d.SQL)
	}

	_, err = env.store.Diff(ctx, env.user, env.dbID, schema, ddl+"\nCREATE TABLE baru (id INT REFERENCES "+other+".gudang(id));")
	if !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "schema lain") {
		t.Fatalf("FK baru ke schema lain: err=%v, want pesan yang jelas", err)
	}
}
