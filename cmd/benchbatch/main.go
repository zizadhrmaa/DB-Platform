// Command benchbatch menyiapkan dataset uji dan mengukur UPDATE seluruh row
// tanpa batch dibanding per batch pada dataset yang sama.
//
// Mode batch memakai pola yang sama dengan runner batch platform
// (internal/migration/batch.go): batas atas setiap batch dicari dengan
// cursor (WHERE id > last ORDER BY id LIMIT size), lalu setiap batch satu
// transaction berisi UPDATE rentang (lower, upper] dan checkpoint.
//
//	BENCH_DATABASE_URL=postgres://... go run ./cmd/benchbatch -rows 3000000 -sizes 10000,50000,200000
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	rows := flag.Int("rows", 3_000_000, "jumlah row dataset uji")
	repeat := flag.Int("repeat", 3, "jumlah pengulangan UPDATE penuh (diambil median)")
	sizesFlag := flag.String("sizes", "10000,50000,200000", "ukuran batch yang diuji, dipisah koma")
	keep := flag.Bool("keep", false, "jangan buat ulang dataset kalau tabel sudah ada")
	flag.Parse()

	dsn := os.Getenv("BENCH_DATABASE_URL")
	if dsn == "" {
		log.Fatal("BENCH_DATABASE_URL wajib diisi")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	var version string
	must(conn.QueryRowContext(ctx, `SHOW server_version`).Scan(&version))
	fmt.Printf("PostgreSQL %s, klien %s/%s, %d CPU logis\n", version, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	if !*keep || !tableExists(ctx, conn) {
		start := time.Now()
		must2(conn.ExecContext(ctx, `DROP TABLE IF EXISTS bench_big`))
		must2(conn.ExecContext(ctx, `CREATE TABLE bench_big (id BIGINT PRIMARY KEY, harga BIGINT NOT NULL, catatan TEXT NOT NULL)`))
		must2(conn.ExecContext(ctx, `INSERT INTO bench_big SELECT g, g % 100000, md5(g::text) FROM generate_series(1, $1::bigint) g`, *rows))
		must2(conn.ExecContext(ctx, `VACUUM ANALYZE bench_big`))
		fmt.Printf("dataset: %d row dibuat dalam %v\n", *rows, time.Since(start).Round(time.Millisecond))
	}
	var n int64
	must(conn.QueryRowContext(ctx, `SELECT count(*) FROM bench_big`).Scan(&n))
	fmt.Printf("jumlah row: %d\n\n", n)

	// UPDATE seluruh row tanpa batch.
	var full []time.Duration
	for i := 0; i < *repeat; i++ {
		start := time.Now()
		must2(conn.ExecContext(ctx, `UPDATE bench_big SET harga = harga + 1`))
		d := time.Since(start)
		full = append(full, d)
		fmt.Printf("UPDATE penuh #%d: %v\n", i+1, d.Round(time.Millisecond))
	}
	fmt.Printf("UPDATE penuh median: %v\n\n", median(full).Round(time.Millisecond))

	must2(conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bench_checkpoint (run TEXT, batch_no INT, lower_bound BIGINT, upper_bound BIGINT, rows BIGINT, committed_at TIMESTAMPTZ DEFAULT now())`))
	fmt.Println("| Ukuran batch | Jumlah batch | Durasi total | Rata-rata per batch | Transaction terlama |")
	fmt.Println("|---|---|---|---|---|")
	for _, s := range strings.Split(*sizesFlag, ",") {
		size, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || size < 1 {
			log.Fatalf("ukuran batch tidak valid: %q", s)
		}
		batches, total, longest := runBatches(ctx, conn, size)
		fmt.Printf("| %d | %d | %v | %v | %v |\n", size, batches, total.Round(time.Millisecond),
			(total / time.Duration(max(batches, 1))).Round(time.Microsecond), longest.Round(time.Millisecond))
	}
}

func runBatches(ctx context.Context, conn *sql.Conn, size int) (int, time.Duration, time.Duration) {
	run := fmt.Sprintf("size-%d-%d", size, time.Now().UnixNano())
	var lower, upper sql.NullInt64
	must(conn.QueryRowContext(ctx, `SELECT min(id) - 1, max(id) FROM bench_big`).Scan(&lower, &upper))
	cursor := lower.Int64
	start := time.Now()
	var longest time.Duration
	batches := 0
	for {
		var hi sql.NullInt64
		must(conn.QueryRowContext(ctx, `SELECT max(c) FROM (SELECT id AS c FROM bench_big WHERE id > $1 AND id <= $2 ORDER BY id LIMIT $3) s`,
			cursor, upper.Int64, size).Scan(&hi))
		if !hi.Valid {
			break
		}
		t0 := time.Now()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			log.Fatal(err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE bench_big SET harga = harga + 1 WHERE id > $1 AND id <= $2`, cursor, hi.Int64)
		if err != nil {
			log.Fatal(err)
		}
		affected, _ := res.RowsAffected()
		batches++
		if _, err := tx.ExecContext(ctx, `INSERT INTO bench_checkpoint (run, batch_no, lower_bound, upper_bound, rows) VALUES ($1,$2,$3,$4,$5)`,
			run, batches, cursor, hi.Int64, affected); err != nil {
			log.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			log.Fatal(err)
		}
		if d := time.Since(t0); d > longest {
			longest = d
		}
		cursor = hi.Int64
	}
	return batches, time.Since(start), longest
}

func tableExists(ctx context.Context, conn *sql.Conn) bool {
	var ok bool
	_ = conn.QueryRowContext(ctx, `SELECT to_regclass('bench_big') IS NOT NULL`).Scan(&ok)
	return ok
}

func median(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func must2(_ sql.Result, err error) { must(err) }
