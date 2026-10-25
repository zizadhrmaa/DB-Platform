# DB Platform IF3110 Milestone 1

Fondasi Database Management Platform: access control (login, session,
group-based authorization), database viewer untuk PostgreSQL dan MySQL, dan migration
yang terkontrol dengan riwayat linear.

## Identitas kelompok

Kelompok 05, Kelas 02

| NIM | Nama |
|---|---|
| 13524017 | Aziza Dharma Putri |
| 13524071 | Kalyca Nathania B Manullang |
| 13524114 | Mirza Tsabita Wafa'ana |
| 13524137 | Reysha Syafitri Mulya Ramadhan |

## Pembagian tugas

Disusun dari riwayat commit di branch `main`.

| Anggota | Bagian | Folder utama |
|---|---|---|
| Aziza Dharma Putri | Kerangka proyek (domain, config, httpx, view, routes, schema platform), login, session, CSRF, pembatasan login, Docker/CI, perbaikan integrasi migration, fitur bonus analisis query dan schema diff | `internal/auth`, `internal/platformdb`, `internal/httpx`, `cmd/server`, `internal/migration/explain.go`, `internal/migration/diff.go` |
| Kalyca Nathania B Manullang | Kelola user, group, anggota, grant akses, dan halaman admin | `internal/admin`, `cmd/server/admin.go`, `web/templates/pages/admin` |
| Mirza Tsabita Wafa'ana | Service migration (create, edit, discard, execute, riwayat), handler, dan halaman migration | `internal/migration`, `web/templates/pages/migration` |
| Reysha Syafitri Mulya Ramadhan | Koneksi database target, connection pool, viewer schema dan data | `internal/dbmanager`, `cmd/server/databases.go`, `cmd/server/connections.go` |

## Menjalankan

Prasyarat: Go 1.24+, Docker.

```bash
go mod tidy                 # sekali saja, mengunduh dependency & melengkapi go.sum
cp .env.example .env        # lalu isi APP_SECRET_KEY (make keygen) dan ADMIN_PASSWORD
make up                     # platform-db, target-toko, target-kampus, test-db
make run                    # http://localhost:8080
```

Login pertama memakai `ADMIN_USERNAME` / `ADMIN_PASSWORD` dari `.env`
(admin hanya dibuat jika belum ada admin sama sekali).

Database target contoh untuk didaftarkan lewat halaman admin:

| Nama | DBMS | Host (dari laptop) | Port | Database | User | Password |
|---|---|---|---|---|---|---|
| Toko | postgres | localhost | 5433 | toko | platform_app | platform_app_pass |
| Kampus (Postgres) | postgres | localhost | 5434 | kampus | platform_app | platform_app_pass |
| Kampus (MySQL) | mysql | localhost | 3306 | kampus | platform_app | platform_app_pass |

Jika aplikasi dijalankan di Docker (`docker compose --profile app up`), gunakan host
`target-toko` / `target-kampus` / `target-mysql`.

## Session

| Parameter | Nilai |
|---|---|
| Idle timeout | 30 menit tanpa aktivitas |
| Absolute expiration | 8 jam sejak login, tidak diperpanjang oleh rotasi |
| Rotasi session ID | saat login, saat user dijadikan admin, dan setiap 15 menit |
| Grace period ID lama | 30 detik setelah dirotasi |
| Cookie | `dbp_session`, HttpOnly, SameSite=Lax, Secure di production |

Session ID dibuat acak (32 byte) dan yang disimpan di database hanya hash
SHA-256-nya. Logout mencabut session di backend, dan user yang dinonaktifkan
atau password-nya direset langsung kehilangan semua session-nya. Setiap
request yang mengubah data wajib membawa token CSRF. Nilainya diatur di
`internal/auth/config.go`.

Percobaan login dibatasi 5 kali per pasangan username dan IP, dan 100 kali
per IP, dalam 15 menit. Batasnya sengaja tidak per username saja, supaya
orang lain tidak bisa mengunci akun kita hanya dengan salah password lima
kali. Login yang berhasil tidak ikut dihitung. Hashing password (Argon2id,
64 MiB per hash) paling banyak berjalan 4 sekaligus; kalau server sedang
sibuk, login dibalas 503 dan bisa dicoba lagi beberapa detik kemudian.

## Akses efektif

User hanya mendapat akses database lewat group. Kalau user berada di
beberapa group yang punya akses ke database yang sama, **akses efektifnya
adalah level tertinggi** di antara group tersebut. Contoh: group A View Only
dan group B View + Edit, maka user di kedua group mendapat View + Edit.
Tidak ada grant yang mengurangi akses, jadi menambahkan user ke group tidak
pernah membuat aksesnya berkurang.

Platform Admin bukan level akses database. Admin tetap harus masuk ke group
yang punya grant untuk melihat isi database. Perubahan akses langsung berlaku
di request berikutnya. Aturannya ada di `internal/authz/policy.go`.

## Route

Form HTML cuma bisa GET dan POST, jadi ubah dan hapus memakai POST ke
sub-path. User tanpa akses ke database mendapat 404 supaya keberadaan
database tidak bocor, sedangkan user View Only yang mencoba aksi Edit
mendapat 403.

| Route | Butuh |
|---|---|
| `GET /login`, `POST /login` | publik |
| `POST /logout` | login |
| `GET /databases` | login |
| `GET /databases/{dbID}` | View |
| `GET /databases/{dbID}/tables/{schema}/{table}` | View |
| `GET /databases/{dbID}/tables/{schema}/{table}/data?page=&size=` | View |
| `GET /databases/{dbID}/migrations` | View |
| `GET .../migrations/new`, `POST .../migrations` | Edit |
| `GET .../migrations/{migrationID}` | View |
| `GET .../{migrationID}/edit`, `POST .../{migrationID}` | Edit |
| `POST .../{migrationID}/execute` | Edit |
| `POST .../{migrationID}/discard` | Edit |
| `POST .../{migrationID}/explain` (bonus) | Edit |
| `GET/POST /databases/{dbID}/migrations/diff` (bonus) | Edit |
| `/admin`, `/admin/users...`, `/admin/groups...`, `/admin/databases...` | Admin |

POST yang berhasil diakhiri redirect 303. Kalau validasi gagal, form
dirender ulang dengan status 422.

| Error | Status |
|---|---|
| Belum login | 401 (GET diarahkan ke `/login`) |
| Akses kurang | 403 |
| Tidak ditemukan atau tidak punya akses | 404 |
| Input tidak valid | 400, atau 422 untuk form |
| Konflik (misalnya urutan migration) | 409 |
| Database target tidak bisa dihubungi | 503 |
| Lainnya | 500 dengan pesan umum, detailnya hanya di log |

## Keamanan viewer

- Query viewer dijalankan di transaction `READ ONLY` dengan
  `statement_timeout` 10 detik, lalu selalu di-rollback.
- Nama schema dan tabel dari URL dicek dulu keberadaannya di `pg_class` dan
  `pg_namespace` lewat parameter query, lalu di-quote dengan
  `pgx.Identifier`. Nama dari URL tidak pernah disambung langsung ke SQL,
  dan nama yang tidak ada (termasuk percobaan injeksi) dibalas 404.
- Pesan error koneksi tidak ditampilkan karena bisa memuat host dan
  username. Koneksi gagal dibalas 503, role tanpa hak SELECT dibalas 403.
- Password koneksi disimpan terenkripsi AES-256-GCM dengan `APP_SECRET_KEY`
  dan tidak pernah dikirim ke halaman.
- Isi sel di-escape oleh `html/template`. NULL dibedakan dari string kosong,
  `bytea` ditampilkan hex, dan teks panjang dipotong.
- Pagination default 50 baris, maksimal 200, diurutkan berdasarkan primary
  key supaya stabil.

## Fitur bonus: analisis query

Di halaman review migration, user dengan akses View + Edit bisa menekan
**Analisis query** untuk melihat execution plan setiap statement sebelum
migration dijalankan: jenis scan, index yang dipakai, strategi join,
estimasi cost dan jumlah row. Dengan pilihan **EXPLAIN ANALYZE**, statement
benar-benar dijalankan sehingga waktu dan jumlah row sebenarnya ikut tampil.

Semua statement dijalankan berurutan di satu transaction yang selalu
di-rollback, jadi isi database target tidak berubah. Perintah DDL seperti
`CREATE TABLE` tidak punya execution plan, tapi tetap dijalankan di simulasi
supaya statement berikutnya bisa memakai hasilnya. Satu-satunya efek yang
tidak ikut kembali adalah nilai sequence yang sudah terpakai saat ANALYZE.
Implementasinya ada di `internal/migration/explain.go`.

## Fitur bonus: schema diff

Dari halaman riwayat migration, user dengan akses View + Edit bisa membuka
**Buat dari schema diff**. Halaman ini berisi DDL schema saat ini yang dibuat
otomatis dari database target. User mengubahnya menjadi schema yang
diinginkan, lalu menekan **Bandingkan**. Platform menampilkan daftar
perbedaan (tabel, kolom, constraint, index) dan kandidat SQL migration yang
masih bisa diedit, lalu disimpan lewat alur migration biasa, termasuk review
sebelum dijalankan.

DDL yang diinginkan dijalankan sebagai tabel temporary di dalam transaction
yang selalu di-rollback, jadi database target tidak berubah dan role target
tidak butuh hak CREATE di level database. Perubahan yang menghapus data
(DROP TABLE, DROP COLUMN) diberi tanda dan komentar peringatan di SQL-nya.

Batasannya:

- Yang dibandingkan hanya tabel biasa beserta kolom, constraint, dan index.
  View, function, trigger, tipe custom (enum), dan tabel partisi tidak ikut.
- Rename terbaca sebagai hapus lalu tambah. Ganti SQL-nya dengan
  `ALTER TABLE ... RENAME` kalau maksudnya rename.
- Foreign key ke tabel di schema lain tidak dibandingkan dan tidak pernah
  diubah (ditulis sebagai komentar di DDL).
- Perubahan kolom serial atau generated column ditulis sebagai komentar
  supaya dikerjakan manual.

Implementasinya ada di `internal/migration/diff.go`.

## Testing

```bash
make test-unit          # unit test (integration otomatis di-skip)
make test-integration   # unit + integration terhadap container test-db
```

Integration test memakai schema PostgreSQL acak per test (`internal/testutil`),
sehingga terisolasi dan aman dijalankan paralel.

## Struktur

```
cmd/server/          entry point & daftar route
internal/auth/       authentication & session
internal/authz/      authorization (kebijakan terpusat)
internal/admin/      kelola user, group, grant
internal/dbmanager/  koneksi DB target & viewer
internal/migration/  migration & riwayat
internal/platformdb/ schema database internal
internal/testutil/   database khusus testing + fixture
web/                 template Go & aset statis
```