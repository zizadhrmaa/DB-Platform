# Kontrak Tim Milestone 1

Isi dokumen ini adalah hal-hal yang sudah kita sepakati bersama dan sudah ada di skeleton. Kalau ada yang mau diubah, bahas dulu di grup, lalu ubah kode dan dokumen ini di PR yang sama.

## Pembagian tugas

|  | Bagian | Folder utama |
| --- | --- | --- |
| Orang 1 | Login, session, CSRF | `internal/auth` |
| Orang 2 | Hak akses, halaman admin (user, group, grant) | `internal/authz`, `internal/admin` |
| Orang 3 | Koneksi DB target, viewer, infra & test DB | `internal/dbmanager`, `internal/testutil`, `deploy/`, Docker, CI |
| Orang 4 | Migration | `internal/migration` |

Sisanya (`domain`, `httpx`, `view`, `config`, `platformdb`, `cmd/server`, CSS) dipegang bersama. Template halaman dibuat oleh yang mengerjakan fiturnya.

## Keputusan teknis

- Router: `net/http` ServeMux bawaan Go 1.22 (`"GET /login"`, `{dbID}`).
- Database: `database/sql` dengan driver `pgx/v5/stdlib`.
- Tampilan: `html/template`, layout `base` dan blok `content` per halaman.
- Password: Argon2id, disimpan dalam format PHC.
- Session ID: 32 byte acak. Di tabel `sessions` yang disimpan hanya sha256-nya.
- Cookie `dbp_session`: HttpOnly, SameSite=Lax, Path=/, Secure di production.
- Idle timeout 30 menit, absolute timeout 8 jam sejak login (tidak ikut diperpanjang saat rotasi).
- Rotasi ID tiap 15 menit, ID lama masih diterima 30 detik.
- CSRF: satu token per session, field `csrf_token`.
- Akses efektif: level tertinggi dari semua group user. Tidak ada grant "deny".
- Platform Admin bukan level akses database. Admin tanpa grant tidak bisa melihat isi database.
- Tidak punya akses ke database: 404, supaya keberadaan DB tidak bocor. Punya View tapi mencoba aksi Edit: 403.
- Belum login: GET diarahkan ke `/login`, method lain dapat 401.
- Password DB target dienkripsi AES-256-GCM dengan `APP_SECRET_KEY`.
- Nomor versi migration diberikan saat dibuat (max + 1 per database), dan eksekusinya harus urut.
- Integration test memakai schema acak per test.

## Schema database internal

Lengkapnya ada di `internal/platformdb/schema/0001_init.sql`. Ringkasnya:

- `users`: username unik tanpa membedakan huruf besar/kecil, formatnya `^[A-Za-z0-9_.-]{3,64}$`.
- `sessions`: satu baris per session ID. Hasil rotasi punya `family_id` yang sama dengan session asalnya.
- `groups` dan `group_members`: satu user bisa masuk banyak group.
- `db_connections`: data koneksi DB target. Kalau `is_active` false, tidak ada yang bisa mengaksesnya.
- `group_db_access`: satu level (`view` atau `edit`) per group per database.
- `migrations`: `UNIQUE(db_id, version)`.
- `migration_runs`: catatan setiap percobaan eksekusi, termasuk SQL yang dijalankan saat itu.

Relasi kepemilikan memakai `ON DELETE CASCADE`. Kolom seperti `created_by`dan `executed_by` memakai `ON DELETE SET NULL`, jadi menghapus user tidak ikut menghapus riwayat migration.

File schema yang sudah masuk `main` jangan diedit. Perubahan baru masuk ke file `0002_...sql` dan seterusnya.

## Cara modul saling memakai

### User yang sedang login

```go
u, ok := auth.CurrentUser(r.Context())
token := auth.CSRFToken(r.Context()) // sudah otomatis masuk PageData
```

User dibaca ulang dari database di setiap request, jadi kalau admin menonaktifkan user atau mencabut status admin, efeknya langsung terasa. Di test cukup pakai `auth.WithUser(ctx, u)`.

Login sudah jadi. Kalau ingin mencoba halaman sebagai user tertentu tanpa login, isi `DEV_AUTH_USER_ID` di `.env`. Di production ini ditolak. Detail perilaku session ada di interface `SessionManager` (`internal/auth/session.go`).

### Hak akses

```go
// di routes.go
db(authz.ActionViewSchema, handler) // RequireLogin + RequireDB

// di handler
d, _ := authz.FromContext(r.Context()) // d.DBID, d.Level

// cek ulang, misalnya tepat sebelum execute
if err := az.Authorize(ctx, u, dbID, authz.ActionExecuteMigration); err != nil {
	httpx.Error(w, r, err)
	return
}
```

Level minimum tiap aksi hanya didefinisikan di `internal/authz/policy.go`:

- View Only: `ActionViewSchema`, `ActionViewData`, `ActionViewMigrations`, `ActionRunReadQuery`.
- View + Edit: `ActionCreateMigration`, `ActionEditMigration` (termasuk discard), `ActionExecuteMigration`.
- Aksi yang tidak terdaftar selalu ditolak.

`authz.Service` sudah jadi dan sudah dites. Untuk test tanpa database ada `authz.Static`.

### Koneksi ke DB target

```go
target, err := mgr.Conn(ctx, dbID) // pool bersama, jangan di-Close
if err != nil {
	httpx.Error(w, r, err) // otomatis 404 atau 503
	return
}
```

`Conn` tidak mengecek hak akses karena itu sudah dilakukan `RequireDB`. Setiap kali admin mengubah, menonaktifkan, atau menghapus koneksi, panggil `mgr.Invalidate(dbID)`. Untuk test ada `dbmanager.NewStatic`. Helper yang sudah tersedia: `BuildDSN`, `SanitizeError`, `SecretBox`.

### Migration

Interface `Service` ada di `internal/migration/migration.go`. Fungsi yang sudah jadi dan dites: `Status.Editable/Executable/Discardable`, `NextExecutable`, `CheckExecutable`, `ValidateSQL`.

## Daftar route

Form HTML cuma bisa GET dan POST, jadi ubah dan hapus memakai POST ke sub-path. Saat mengerjakan fitur, ganti `app.todo(...)` di `cmd/server/routes.go` dengan handler asli, tapi path-nya jangan diubah.

| Route | Butuh |
| --- | --- |
| `GET /login`, `POST /login` | publik |
| `POST /logout` | login |
| `GET /databases` | login |
| `GET /databases/{dbID}` | View |
| `GET /databases/{dbID}/tables/{schema}/{table}` | View |
| `GET /databases/{dbID}/tables/{schema}/{table}/data?page=&size=` | View |
| `GET/POST /databases/{dbID}/query` (opsional) | View |
| `GET /databases/{dbID}/migrations` | View |
| `GET .../migrations/new`, `POST .../migrations` | Edit |
| `GET .../migrations/{migrationID}` | View |
| `GET .../{migrationID}/edit`, `POST .../{migrationID}` | Edit |
| `POST .../{migrationID}/execute` | Edit |
| `POST .../{migrationID}/discard` | Edit |
| `POST .../{migrationID}/explain` (bonus) | Edit |
| `/admin`, `/admin/users...`, `/admin/groups...`, `/admin/databases...` | Admin |

POST yang berhasil diakhiri redirect 303, supaya refresh tidak mengirim form lagi. Kalau validasi gagal, render ulang form dengan status 422 dan pesan di `PageData.Error`.

## Error dan status code

Bungkus error dengan salah satu `domain.ErrX`, misalnya `fmt.Errorf("migration %d: %w", id, domain.ErrNotFound)`, lalu panggil `httpx.Error(w, r, err)`.

| Error | Status |
| --- | --- |
| `ErrUnauthenticated` | 401 (GET diarahkan ke `/login`) |
| `ErrForbidden` | 403 |
| `ErrNotFound` | 404 |
| `ErrInvalidInput` | 400, atau 422 untuk form |
| `ErrConflict` | 409 |
| `ErrUnavailable` | 503 |
| lainnya | 500 dengan pesan umum, detailnya hanya di log |

Pesan error PostgreSQL dari migration yang gagal boleh ditampilkan ke user Edit, karena mereka perlu itu untuk membetulkan SQL-nya. Pesan error koneksi jangan ditampilkan karena bisa memuat host dan username.

## Session

Implementasinya ada di `internal/auth/manager.go` (`DBSessionManager`), CSRF di `internal/auth/csrf.go`, dan pengecekan password di `internal/auth/login.go`.

Urutan pengecekan di setiap request:

1. Tidak ada cookie: lanjut tanpa user.
2. Hash cookie tidak ada di tabel: hapus cookie, lanjut tanpa user.
3. Tolak kalau `revoked_at` terisi, sudah lewat `absolute_expires_at`, atau sudah idle lebih dari 30 menit.
4. Tolak kalau ID ini sudah dirotasi dan grace period-nya habis.
5. Muat user aktif dengan `auth.LoadActiveUser`. User nonaktif ditolak.
6. Kalau ID ini yang terbaru dan umurnya sudah 15 menit, atau user baru saja dijadikan admin, rotasi.
7. Selain itu, perbarui `last_seen_at` paling sering sekali per menit.

Perilaku yang perlu diketahui:

- Request paralel saat rotasi (beberapa tab sekaligus): satu ID hanya dirotasi sekali karena `UPDATE ... WHERE replaced_at IS NULL`. Request lain yang membawa ID lama diterima selama grace period tanpa dirotasi lagi.
- Kalau response yang membawa cookie baru hilang, browser masih memegang ID lama. ID itu hanya sah 30 detik, setelah itu user harus login lagi. Server tidak bisa mengirim ulang ID baru karena yang disimpan hanya hash-nya.
- Saat rotasi, `absolute_expires_at` dan token CSRF disalin apa adanya.
- Login selalu membuat ID baru dan mencabut session lama di browser itu, untuk mencegah session fixation.
- Logout mencabut seluruh family session dan menghapus cookie. Logout hanya lewat POST dengan CSRF, karena GET bisa dipicu dari `<img src=/logout>`.
- Login gagal: pesannya sama ("Username atau password salah.") untuk username tidak ada, password salah, dan user nonaktif, dengan status 401. Kalau username tidak ada, `auth.VerifyAgainstDummy` tetap dijalankan supaya waktu responsnya sama. Username diisi ulang di form, password tidak. Field kosong dibalas 422.
- CSRF: request yang sudah login harus membawa `csrf_token` di form (atau header `X-CSRF-Token`). `POST /login` belum punya session, jadi yang dicek header `Origin` atau `Referer` harus sama dengan host. Request tanpa keduanya ditolak, jadi kalau mencoba lewat curl tambahkan `-H "Origin: http://localhost:8080"`.
- Kenaikan privilege dideteksi lewat kolom `sessions.privileged` (schema `0002`). Kalau user dijadikan admin, request berikutnya otomatis mendapat ID baru. `RotateOnPrivilegeChange` tetap tersedia untuk rotasi manual.
- Password dan session ID tidak pernah muncul di URL, body response, maupun log.
- User dinonaktifkan atau password-nya direset admin: panggil `RevokeAllForUser`. User nonaktif juga otomatis ditolak di request berikutnya.
- Session yang sudah tidak berlaku lebih dari sehari dihapus tiap jam oleh `RunCleanup`.
- `DEV_AUTH_USER_ID` masih bisa dipakai untuk mencoba halaman sebagai user tertentu tanpa login.
- Percobaan login dibatasi (`internal/auth/ratelimit.go`): 5 kali gagal per username atau 20 kali gagal per IP dalam 15 menit. Setelah itu login dibalas 429 dengan header `Retry-After` tanpa mengecek password, termasuk untuk username yang tidak ada. Login berhasil mengosongkan hitungan untuk username itu. Datanya di memori, jadi hilang saat server restart. IP diambil dari koneksi langsung, bukan `X-Forwarded-For`.

## Hak akses dan halaman admin

- Contoh akses efektif: group A punya `view` dan group B punya `edit` di database yang sama, maka user di kedua group dapat `edit`.
- `authz.Service` tidak memakai cache. Keluar dari group, grant dicabut, atau koneksi dinonaktifkan langsung berlaku di request berikutnya.
- Kalau admin perlu melihat data, masukkan dia ke group yang punya grant.

Halaman admin ada di `cmd/server/admin.go`, sedangkan aturan dan query-nya di `internal/admin`. Perilakunya:

- Admin aktif terakhir tidak bisa dinonaktifkan, diturunkan, atau dihapus. Admin juga tidak bisa melakukan ketiganya ke akunnya sendiri. Pengecekannya di dalam transaction yang mengunci semua baris admin aktif (`SELECT ... FOR UPDATE`), jadi dua admin yang saling menurunkan di waktu bersamaan tidak bisa membuat platform kehilangan admin. Pelanggaran dibalas 409 dengan pesan yang menjelaskan alasannya.
- User dinonaktifkan atau password-nya direset: semua session-nya langsung dicabut (`RevokeAllForUser`). User yang dihapus ikut kehilangan session karena `ON DELETE CASCADE`. Riwayat migration buatannya tetap ada.
- Username mengikuti format `^[A-Za-z0-9_.-]{3,64}$` dan password 8 sampai 256 karakter. Username dan nama group yang dobel (tanpa membedakan huruf besar/kecil) dibalas 422 dengan pesan yang jelas.
- Grant yang sudah ada diperbarui levelnya (`ON CONFLICT ... DO UPDATE`). Mencabut akses menghapus barisnya.
- Menambah anggota yang sudah ada tidak dianggap error, cukup tampil info.
- ID dari form tetap divalidasi di server. ID yang bukan angka dibalas 422, dan ID yang tidak ada dibalas 404.
- Hapus user dan hapus group harus mencentang kotak konfirmasi, karena halaman tidak boleh memakai JavaScript inline.
- Pesan sukses setelah redirect dikirim lewat `?ok=<kode>`. Yang ditampilkan hanya teks yang sudah ditentukan di kode, jadi isi query string tidak pernah dirender.
- Menyembunyikan menu saja tidak cukup. Semua `/admin/*` dibungkus `RequireAdmin`, dan ada test yang memastikan setiap route admin menolak non-admin walaupun token CSRF-nya valid.

## Koneksi dan viewer database

Pool ada di `internal/dbmanager/pool.go` (`PoolManager`), query viewer di `internal/dbmanager/viewer.go`, CRUD koneksi di `internal/admin/connections.go`, dan handler-nya di `cmd/server/databases.go` serta `cmd/server/connections.go`.

- Pool per database disimpan di `map[int64]*sql.DB` dengan mutex. Batasnya kecil (`MaxOpenConns` 5, idle 5 menit) supaya banyak DB target tidak menghabiskan koneksi.
- Koneksi gagal (host mati, password salah, timeout 5 detik) dibalas 503 lewat `SanitizeError`. Pool yang gagal `Ping` jangan disimpan.
- Edit koneksi tanpa mengisi password berarti password lama dipertahankan. Field password di form edit selalu kosong.
- Tombol "Uji koneksi" cukup menampilkan berhasil atau gagal, tanpa detail.
- Query viewer dijalankan di `BEGIN READ ONLY` dengan `SET LOCAL statement_timeout = '10s'`.
- Nama schema dan tabel dari URL dicek dulu keberadaannya di `pg_class`/`pg_namespace` lewat parameter query, lalu di-quote dengan `pgx.Identifier{schema, table}.Sanitize()`. Nama dari URL tidak pernah disambung langsung ke SQL. Nama yang tidak ada, termasuk percobaan injeksi, dibalas 404.
- Metadata yang ditampilkan: tabel (termasuk view), kolom (tipe, nullable, default), constraint (PK, FK beserta tabel rujukannya, UNIQUE, CHECK lewat `pg_get_constraintdef`), dan index (`pg_get_indexdef`). Schema sistem seperti `pg_catalog`, `information_schema`, dan `pg_toast` disembunyikan.
- Pagination: default 50 baris, maksimal 200. `page` yang tidak valid dianggap 1. Urutkan berdasarkan PK supaya hasilnya stabil, atau `ctid`kalau tidak ada PK. Total baris pakai `count(*)`, atau `reltuples` untuk tabel besar.
- Tampilan sel: NULL dibedakan dari string kosong, `bytea` ditampilkan hex dan dipotong, teks panjang dipotong sekitar 200 karakter. Data contoh sengaja berisi `<script>` untuk memastikan escaping jalan.
- Nama tabel yang aneh seperti `"Audit Log"` harus tetap bisa dibuka, jadi segmen path-nya di-escape.
- Tabel kosong, schema tanpa tabel, atau tidak punya hak SELECT: tampilkan pesan, jangan 500. Role tanpa hak SELECT dibalas 403, query yang melewati batas waktu dibalas 503.
- Hapus koneksi ikut menghapus grant dan riwayat migration database itu. Untuk menutup akses sementara, cukup nonaktifkan koneksinya. Setiap edit, nonaktifkan, atau hapus langsung membuang pool lama (`Invalidate`).
- Password koneksi tidak pernah dikirim ke halaman, termasuk saat form gagal divalidasi.

## Query console read-only (opsional)

Dikerjakan hanya kalau viewer tabel sudah selesai. Kalau dikerjakan, penjagaannya harus lengkap.

- Aksi `authz.ActionRunReadQuery` (cukup View Only), route `GET/POST /databases/{dbID}/query`.
- Penjaga utamanya PostgreSQL, bukan parsing teks: `BEGIN TRANSACTION READ ONLY`, `SET LOCAL statement_timeout = '5s'`, `SET LOCAL idle_in_transaction_session_timeout = '10s'`, jalankan query, lalu selalu `ROLLBACK`, bahkan kalau berhasil.
- Hanya satu statement, dijalankan lewat extended protocol (misalnya `pgx.QueryExecModeDescribeExec`) yang menolak multi-statement. Jangan pakai simple protocol di sini, karena `COMMIT; DELETE FROM x` akan menutup transaksi read-only lalu menjalankan DELETE.
- Perintah seperti `BEGIN`, `COMMIT`, `SET`, `RESET`, `DISCARD`, `LISTEN`, dan `COPY ... TO PROGRAM` ditolak lebih awal dengan pesan yang jelas (bisa pakai `stripCommentsAndStrings` dari `internal/migration`). Ini lapisan tambahan saja.
- Fungsi yang tetap berefek walau read-only (`pg_terminate_backend`, `pg_sleep`, `dblink`, `lo_export`) dibatasi lewat role DB target yang tidak punya hak itu (lihat `platform_app` di `deploy/sample`) dan statement timeout.
- Hasil dibatasi 500 baris, sel panjang dipotong, dan tampilkan keterangan kalau ada yang dipotong.
- Error PostgreSQL ditampilkan sebagai pesan form dengan status 422.
- Body form dibatasi `httpx.MaxSQLFormBytes`, harus POST dengan CSRF, dan isi query jangan masuk log.
- Test yang perlu ada: SELECT berhasil; DELETE/UPDATE/CREATE TABLE ditolak dan data tidak berubah; `COMMIT; DELETE ...` ditolak; `SET TRANSACTION READ WRITE; DELETE ...` ditolak; `SELECT pg_sleep(10)`kena timeout; hasil di atas 500 baris dipotong; user tanpa akses dapat 404.

## Migration

Alur status: `pending` ke `running`, lalu ke `success` atau `failed`. Migration `failed` bisa diedit dan dijalankan lagi. `pending` dan `failed`bisa di-discard. `success` tidak bisa diubah lagi.

Yang boleh dieksekusi hanya versi terkecil yang masih `pending` atau `failed` (`migration.CheckExecutable`). Versi yang di-discard dilewati tapi tetap muncul di riwayat.

Hal yang perlu diperhatikan:

- Dua user membuat migration bersamaan bisa dapat nomor versi yang sama. Hitung `max(version) + 1` di transaction yang mengunci baris `db_connections` dengan `FOR UPDATE`. `UNIQUE(db_id, version)` jadi pengaman terakhir.
- Eksekusi bersamaan (klik dua kali, atau dua user): status diubah ke `running` di transaction platform dengan baris terkunci, dan di DB target ada `pg_advisory_xact_lock`. Yang kalah dapat 409.
- Edit saat sedang dieksekusi: `UPDATE ... WHERE status IN ('pending','failed')`lalu cek `RowsAffected`, balas 409 kalau nol.
- SQL gagal: rollback, status jadi `failed`, simpan pesan error PostgreSQL (`Message`, `Detail`, `Position` dari `pgconn.PgError`).
- DB platform dan DB target tidak bisa berada dalam satu transaction. Kalau target sudah commit tapi update status di platform gagal, riwayatnya jadi salah. Solusinya, catat juga di tabel `_dbplatform_history(version, applied_at)`di DB target, di transaction target yang sama, lalu cocokkan saat riwayat dibuka.
- Server crash saat `running`: transaction target otomatis rollback. Saat start, ubah `running` jadi `failed` ("eksekusi terputus"), kecuali tabel pelacak di target menunjukkan versi itu sudah masuk, maka jadi `success`.
- SQL yang tidak bisa jalan di dalam transaction (`CREATE INDEX CONCURRENTLY`, `VACUUM`, `CREATE DATABASE`, `ALTER SYSTEM`) dan BEGIN/COMMIT eksplisit ditolak saat disimpan oleh `migration.ValidateSQL`, dengan status 422.
- Multi-statement: `tx.ExecContext(ctx, sql)` tanpa argumen membuat pgx memakai simple protocol, jadi banyak statement sekaligus bisa jalan.
- Pasang `SET LOCAL lock_timeout = '5s'` dan `statement_timeout = '60s'`. `WriteTimeout` server sudah 120 detik.
- Jangan pakai `r.Context()` untuk transaction target, karena akan dibatalkan kalau user menutup tab. Pakai `context.WithTimeout(context.Background(), ...)` supaya statusnya tetap tercatat.
- Panggil `Authorize` lagi tepat sebelum eksekusi, karena akses bisa dicabut selagi user masih di halaman review.
- Selalu `WHERE id = $1 AND db_id = $2`. Migration milik database lain dibalas 404.
- SQL kosong, isinya cuma komentar, atau lebih dari 512 KiB: 422.
- Halaman detail menampilkan SQL di `<pre>` dan tombol execute (POST dengan CSRF). Tombolnya hanya muncul untuk level Edit, tapi backend tetap mengecek.
- Riwayat menampilkan versi, judul, status, pembuat, pelaksana terakhir, dan waktu. Halaman detail menampilkan semua `migration_runs`.

## Testing

- Unit test untuk fungsi murni dan handler, dengan `authz.Static`, `dbmanager.NewStatic`, dan `auth.WithUser`.
- Integration test pakai `testutil.PlatformDB(t)` atau `testutil.TargetDB(t)`, dengan fixture `MustUser`, `MustGroup`, `MustAddMember`, `MustDBConn`, dan `MustGrant`. Handler dipanggil lewat `httptest`, tidak perlu menyalakan server.
- Setiap fitur punya test untuk kasus berhasil, gagal, dan akses ditolak.
- `make test-integration` menyalakan `test-db` lalu menjalankan `go test ./...`. Tanpa `TEST_DATABASE_URL`, integration test di-skip.
- CI menjalankan `gofmt`, `go vet`, dan `go test -race` dengan Postgres.

Test minimal per bagian:

- Session: login berhasil/gagal dengan pesan yang sama, flag cookie, idle dan absolute timeout, rotasi dan grace period (ID lama diterima sebelum 30 detik, ditolak sesudahnya), rotasi tidak memperpanjang absolute timeout, logout mencabut family, CSRF salah atau hilang dapat 403.
- Admin: akses efektif multi-group (sudah ada), non-admin dapat 403 di semua route admin, View Only ditolak untuk aksi Edit, admin terakhir tidak bisa dinonaktifkan, pencabutan grant langsung berlaku.
- Viewer: metadata dari `TargetDB`, pagination dan batas size, tabel tidak ada atau nama berisi injeksi (`x"; DROP...`) dapat 404, koneksi gagal dapat 503, kredensial tidak muncul di response, user tanpa akses dapat 404.
- Migration: berhasil lalu commit, SQL error lalu rollback (tabel tidak terbentuk), v3 sebelum v2 dapat 409, eksekusi bersamaan hanya jalan satu, View Only dapat 403 untuk create/edit/execute, migration DB lain dapat 404.

## Git

- Satu branch per fitur: `feat/auth-session`, `feat/admin`, `feat/dbviewer`, `feat/migration`.
- PR kecil, minimal satu reviewer, CI hijau sebelum merge ke `main`.
- Kabari grup dulu sebelum mengubah `internal/domain`, `internal/httpx`, `cmd/server/routes.go`, file schema, atau dokumen ini.

## Skenario demo

1. Login sebagai admin. Buat user `ani` dan `budi`, group `viewer` dan `editor`, dan koneksi `toko`.
2. Beri `viewer` akses `view` ke toko dan `editor` akses `edit`. Masukkan ani ke `viewer` dan budi ke `editor`.
3. Login sebagai ani, buka toko, lihat schema, constraint, index, dan data dengan pagination. Coba buka form migration, hasilnya 403.
4. Login sebagai budi, buat migration `ALTER TABLE customers ADD COLUMN city text;`, review, lalu execute sampai success.
5. Buat migration dengan SQL yang salah, execute sampai failed, tunjukkan tabel tidak berubah, lalu edit dan jalankan lagi.
6. Tunjukkan riwayat yang linear: versi, SQL, pelaksana, waktu, status.
7. Admin mencabut akses ani. Saat ani refresh, langsung 404.
8. Tunjukkan cookie (HttpOnly, SameSite), rotasi ID, dan logout yang mencabut session.