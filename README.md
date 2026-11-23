# DB Platform IF3110 Milestone 1 dan 2

Database Management Platform: access control (login, session,
group-based authorization), database viewer untuk PostgreSQL dan MySQL,
migration yang terkontrol dengan riwayat linear, lalu pada Milestone 2
ditambah SPA, workflow approval, antrean dan worker, rollback dan forward,
migration per batch, serta logging dan tracing terstruktur.

## Identitas kelompok

Kelompok 05, Kelas 02

| NIM | Nama |
|---|---|
| 13524017 | Aziza Dharma Putri |
| 13524071 | Kalyca Nathania B Manullang |
| 13524114 | Mirza Tsabita Wafa'ana |
| 13524137 | Reysha Syafitri Mulya Ramadhan |

## Pembagian tugas

### Milestone 1

Disusun dari riwayat commit di branch `main`.

| Anggota | Bagian | Folder utama |
|---|---|---|
| Aziza Dharma Putri | Kerangka proyek (domain, config, httpx, view, routes, schema platform), login, session, CSRF, pembatasan login, Docker/CI, perbaikan integrasi migration, fitur bonus analisis query dan schema diff | `internal/auth`, `internal/platformdb`, `internal/httpx`, `cmd/server`, `internal/migration/explain.go`, `internal/migration/diff.go` |
| Kalyca Nathania B Manullang | Kelola user, group, anggota, grant akses, dan halaman admin | `internal/admin`, `cmd/server/admin.go`, `web/templates/pages/admin` |
| Mirza Tsabita Wafa'ana | Service migration (create, edit, discard, execute, riwayat), handler, dan halaman migration | `internal/migration`, `web/templates/pages/migration` |
| Reysha Syafitri Mulya Ramadhan | Koneksi database target, connection pool, viewer schema dan data | `internal/dbmanager`, `cmd/server/databases.go`, `cmd/server/connections.go` |

### Milestone 2

| Anggota | Spek wajib | Bonus |
|---|---|---|
| Aziza | Queue, worker, lock, retry, recovery, migration per batch, dataset uji, SPA monitoring job | Stress testing k6 |
| Mirza | Migration v2 (up/down, irreversible, revisi immutable, rollback/forward, riwayat, Hide rollback), JSON API dan SPA migration | Security SAST CodeQL |
| Kalyca | Fondasi SPA, auth JSON, admin (user, group, grant, koneksi), leader group, approval workflow, authz | Pengumpulan super cepat |
| Reysha | JSON API dan SPA viewer, structured logging, wrapper SQL ber-log, tracing OpenTelemetry | Observability |

Kontrak antarbagian ada di [`docs/m2-contracts.md`](docs/m2-contracts.md).

## Menjalankan

Prasyarat: Go 1.24+, Node 22+, Docker. Database platform harus PostgreSQL
15 atau lebih baru (skema memakai `ON DELETE SET NULL (kolom)`).

```bash
go mod tidy                 # sekali saja, mengunduh dependency & melengkapi go.sum
cp .env.example .env        # lalu isi APP_SECRET_KEY (make keygen) dan ADMIN_PASSWORD
make up                     # platform-db, target-toko, target-kampus, test-db
make fe-build               # build SPA ke web/dist
make run                    # http://localhost:8080 (SPA di /app/)
```

Untuk pengembangan frontend: `make fe-dev` (Vite di `http://localhost:5173`,
`/api` diteruskan ke backend). Login pertama memakai `ADMIN_USERNAME` /
`ADMIN_PASSWORD` dari `.env` (admin hanya dibuat jika belum ada admin sama
sekali).

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
request yang mengubah data wajib membawa token CSRF (header `X-CSRF-Token`
untuk API, field form untuk halaman lama). Nilainya diatur di
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

## Workflow approval (Milestone 2)

Setiap perubahan database (up, rollback, forward) melewati satu request
dengan alur `draft → pending_review → approved → queued`. Request yang
ditolak berstatus `rejected`, dan yang dibatalkan pengaju `cancelled`.
Status eksekusi (`queued`, `running`, `succeeded`, `failed`, `partial`, dan
seterusnya) ada di job, bukan di request.

Aturan yang dipakai:

- **Approver** adalah leader dari **setiap group yang punya akses** ke
  database tersebut, baik View Only maupun View + Edit. Kalau hanya satu
  group yang punya akses, cukup leader group itu.
- Setiap group punya paling banyak satu leader, dan leader harus anggota
  group itu (dijaga foreign key). Leader dipasang Platform Admin. User yang
  keluar dari group atau dinonaktifkan kehilangan jabatan leader otomatis.
- **Self-approval boleh**: pengaju yang juga leader boleh menyetujui
  requestnya sendiri. Satu user yang menjadi leader di beberapa group cukup
  satu kali approve untuk semua group itu.
- **Group tanpa leader** membuat request tetap `pending_review` (tidak
  ditolak) sampai leader ditetapkan.
- **Approval dicabut otomatis** selama job belum berjalan: kalau group baru
  diberi akses, akses group dicabut, atau leader berganti, approval yang
  tidak sah lagi dikembalikan ke pending dan request kembali ke
  `pending_review`. Job yang masih antre untuk request itu digagalkan.
  Pencabutan dijalankan langsung setelah perubahan admin dan sekali lagi
  saat request dibaca, di-enqueue, atau divalidasi worker. Setiap pencabutan
  tercatat di log (`event=approval_revoked`).
- Request menyimpan **revisi SQL** migration saat dibuat. Kalau SQL berubah
  sesudahnya, request ditandai stale dan tidak bisa di-approve atau
  dijalankan. Migration yang punya request terbuka tidak bisa diedit.
### Revert untuk job partial

Migration yang berhenti di tengah mode batch (`partial` atau `partially_reverted`) tidak bisa di-enqueue ulang lewat request biasa, karena database tetap ditahan job tersebut. Pembatalan batch yang sudah committed memakai request `rollback` yang sudah disetujui semua leader group. Request itu dipasangkan ke job lewat `POST /api/databases/{dbID}/jobs/{jobID}/revert` dengan body `{"request_id": N}`. Sistem memeriksa jenis request, status approval, revisi SQL, kesamaan migration, dan status job. Kalau lolos, request berstatus `queued` dan menyimpan `revert_job_id`. Pemanggilan ulang dengan pasangan yang sama aman dan dipakai untuk melanjutkan revert yang terhenti.

### Aturan tambahan

- **Enqueue manual:** setelah semua approval lengkap, pengaju menekan Jalankan. Request yang job-nya `succeeded` tidak bisa dijalankan lagi. Setelah job gagal terminal, tombol Jalankan ulang muncul di SPA dan membuat job baru tanpa approval ulang selama revisi SQL dan approval masih sah.
- **Cancel:** boleh dilakukan siapa pun yang punya akses Edit ke database itu, bukan hanya pengaju. Request yang sudah `queued` tidak bisa dibatalkan.
- **Hanya PostgreSQL:** pembuatan, pengajuan, enqueue, dan revert request ditolak (400) untuk database MySQL. Viewer MySQL dan bonus M1 tetap berjalan.
- **Log login** mencatat hash username, bukan username mentah, supaya password yang salah diketik di kolom username tidak ikut tercatat.

Kebijakan akses tiap aksi (`db.change.view`, `db.change.request`,
`db.change.approve`, `db.job.*`, `db.history.view`) ada di
`internal/authz/policy.go`. Pemeriksaan "apakah user ini leader" dilakukan di
`internal/approval`, bukan di handler.


## Route (Milestone 1, halaman lama)

**Milestone 2:** seluruh tampilan sudah dipindahkan ke SPA (`/app/`). Secara
default halaman template Go di bawah tidak dilayani lagi: GET ke URL lama
dialihkan (302) ke halaman SPA yang setara (misalnya `/login` ke `/app/login`,
`/admin/databases/4` ke `/app/admin/connections/4`), dan form POST lama tidak
tersedia karena SPA memakai `/api`. Handler lama hanya didaftarkan di test
(`App.LegacyHTML`) supaya perilaku Milestone 1 tetap teruji. Tabel berikut
mendokumentasikan route Milestone 1.

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
| `POST .../{migrationID}/execute` | Edit (dinonaktifkan di M2, dibalas 410) |
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

## Fitur bonus Milestone 1: analisis query

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

Di Milestone 2 fitur ini ada di SPA: panel **Analisis query** di halaman
detail migration (`POST /api/databases/{dbID}/migrations/{migrationID}/explain`
dengan body `{"analyze": bool}`).

## Fitur bonus Milestone 1: schema diff

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

Di Milestone 2 fitur ini ada di SPA: tombol **Buat dari schema diff** di
halaman Migration (`GET /api/databases/{dbID}/schema-diff?schema=` untuk DDL
saat ini, `POST` dengan body `{"schema", "desired"}` untuk perbandingan).
Kandidat SQL disimpan sebagai migration baru yang tetap melewati approval dan
antrean.

## Testing

```bash
make test-unit          # unit test (integration otomatis di-skip)
make test-integration   # unit + integration terhadap container test-db
```

Integration test memakai schema PostgreSQL acak per test (`internal/testutil`),
sehingga data antar-test terisolasi. Paket test dijalankan berurutan
(`go test -p 1`) karena advisory lock eksekusi migration berlaku per database
PostgreSQL, bukan per schema, sehingga paket yang berjalan paralel bisa
berebut lock untuk `db_id` fixture yang sama. Test API mencakup alur
berhasil, gagal, dan penolakan akses (non-admin, View Only, tanpa akses,
bukan leader), termasuk workflow approval dan pencabutan approval.

## Struktur

```
cmd/server/          entry point & daftar route
internal/auth/       authentication & session
internal/authz/      authorization (kebijakan terpusat)
internal/admin/      kelola user, group, leader, grant
internal/approval/   workflow request perubahan dan approval
internal/queue/      antrean job eksekusi migration
internal/dbmanager/  koneksi DB target, viewer & SQL logging wrapper
internal/migration/  migration & riwayat
internal/obs/        observability (structured JSON log, OpenTelemetry tracing, Prometheus metrics)
internal/platformdb/ schema database internal
internal/testutil/   database khusus testing + fixture
frontend/            SPA viewer & manajemen (Vite + React + TypeScript)
deploy/              sample database & konfigurasi observability
docs/                kontrak antarbagian Milestone 2
web/                 template Go, fallback embed & aset statis
```

---

## Milestone 2: antrean, worker, rollback, dan batch

### Antrean dan worker

Setiap eksekusi migration (up, rollback, forward, resume, dan revert batch)
menjadi baris di tabel `jobs` dan dijalankan worker secara asynchronous. Endpoint
enqueue mengembalikan job ID tanpa menunggu SQL; status dipantau lewat
`GET /api/databases/{dbID}/jobs` dan halaman **Job** di SPA (polling setiap 2
detik selama job belum selesai).

- **Antrean di PostgreSQL platform.** Worker mengambil job dengan
  `SELECT ... FOR UPDATE SKIP LOCKED`, jadi job tetap ada setelah restart tanpa
  broker tambahan. Per database hanya job antre tertua yang diambil, sehingga
  urutan migration tidak tersalip.
- **Satu database, satu migration.** Index unik parsial
  `jobs_one_active_per_db_uq` menahan database selama ada job `running`,
  `uncertain`, `partial`, atau `partially_reverted`, lintas worker maupun proses.
  Di database target, eksekusi mengambil advisory lock yang sama. Database
  berbeda diproses paralel (`WORKER_CONCURRENCY`).
- **Lease dan heartbeat.** Worker memperpanjang lease setiap sepertiga durasinya.
  Semua perubahan status memakai compare-and-set terhadap pemilik lease, nomor
  attempt, dan execution ID.
- **Revalidasi.** Sebelum SQL jalan, worker memuat ulang pengaju, memeriksa
  approval (`approval.Store.Revalidate`), akses, dan urutan migration.
- **Retry dan idempotensi.** Kegagalan sementara (lock target dipegang, target
  tidak terjangkau, koneksi putus sebelum COMMIT, worker mati) diulang dengan
  jeda `min(2s × 2^(attempt−1), 60s)`, maksimal 5 attempt. SQL yang ditolak
  server tidak diulang. Kiriman ulang dengan kunci idempotensi yang sama
  mengembalikan job yang sama, dan bukti di target mencegah double commit.
- **Hasil commit belum pasti.** Kalau COMMIT tidak dijawab, bukti di database
  target diperiksa dulu; kalau belum bisa diperiksa, job menjadi `uncertain` dan
  database ditahan sampai pemulihan memastikan hasilnya.

Penjelasan lengkap, termasuk **pemulihan lock ketika worker gagal**, ada di
[`docs/antrean-dan-pemulihan.md`](docs/antrean-dan-pemulihan.md).

Konfigurasi: `WORKER_ENABLED` (default `true`), `WORKER_CONCURRENCY` (default
`2`), `JOB_TIMEOUT` (default `30m`).

### Migration per batch

Migration bisa dibuat dengan mode batch: tabel, kolom cursor (integer, unik,
tidak berubah), dan ukuran batch. SQL up dan SQL down masing-masing satu
statement yang memakai `$1` (batas bawah, eksklusif) dan `$2` (batas atas,
inklusif):

```sql
UPDATE produk SET harga = harga * 2 WHERE id > $1 AND id <= $2   -- up
UPDATE produk SET harga = harga / 2 WHERE id > $1 AND id <= $2   -- down
```

- Satu job memegang `pg_advisory_lock` level session di satu koneksi selama
  seluruh job, jadi lock tetap melindungi database **di antara** commit batch.
  Kalau worker mati, koneksi putus dan PostgreSQL melepas lock itu.
- Cakupan ditentukan sekali di awal job (`max` kolom cursor), lalu batas atas
  setiap batch dicari dengan cursor
  (`WHERE id > last ORDER BY id LIMIT size`). Platform hanya membaca satu nilai
  per batch dan tidak pernah memuat isi tabel ke memory.
- Setiap batch satu transaction: SQL user untuk rentang `(lower, upper]` dan
  checkpoint (job, nomor batch, rentang, jumlah row) di tabel target
  `_dbplatform_batches` ditulis bersama lalu di-commit. Batch yang gagal hanya
  me-rollback transaction-nya sendiri; job menjadi `partial` dan database tetap
  ditahan. Jumlah batch/row committed tampil di halaman Job.
- **Resume** (`POST /jobs/{id}/resume`) melanjutkan dari checkpoint di target;
  batch committed tidak diulang. Job dari worker yang mati dipulihkan dan
  dilanjutkan dari checkpoint yang sama.
- **Revert** (`POST /jobs/{id}/revert` dengan request rollback yang disetujui
  semua leader) menjalankan SQL down per batch dalam urutan terbalik memakai
  rentang dari checkpoint. Setiap pembatalan ditandai `reverted_at` dalam
  transaction yang sama; kalau gagal di tengah, job menjadi
  `partially_reverted` dan bisa dilanjutkan. Migration irreversible tidak bisa
  di-revert.

### Dataset uji

Dataset dan pengukuran bisa diulang dengan satu perintah:

```
make bench-batch            # default 3.000.000 row; ubah dengan BENCH_ROWS=...
```

Perintah ini membuat database `bench` di container `test-db`, mengisi tabel
`bench_big` dengan `generate_series`, mengukur `UPDATE bench_big SET harga =
harga + 1` tanpa batch sebanyak 3 kali (median), lalu menjalankan update yang
sama per batch dengan pola statement yang sama seperti runner batch platform
(`cmd/benchbatch`).

Lingkungan uji: Intel Core i7-13620H (10 core / 16 thread), RAM 16 GB, SSD,
Windows 11; PostgreSQL 16.15 di Docker Desktop (container `test-db`).

| Dataset | UPDATE penuh tanpa batch (3 kali) | Median |
|---|---|---|
| 3.000.000 row | 11,17 s; 17,01 s; 14,97 s | **14,97 s** |

| Ukuran batch | Jumlah batch | Durasi total | Rata-rata per batch | Transaction terlama |
|---|---|---|---|---|
| 10.000 | 300 | 16,86 s | 56 ms | 89 ms |
| 50.000 | 60 | 14,77 s | 246 ms | 244 ms |
| 200.000 | 15 | 13,27 s | 884 ms | 801 ms |

**Analisis.** `UPDATE` penuh mengunci dan menulis 3 juta row dalam satu
transaction selama hampir 15 detik; selama itu row yang ter-update terkunci
untuk transaction lain, dan kalau gagal di tengah seluruh pekerjaan hilang. Mode
batch menyelesaikan pekerjaan yang sama dalam waktu total yang sebanding
(13–17 s), tetapi setiap transaction hanya puluhan sampai ratusan milidetik,
sehingga lock row dilepas cepat, kegagalan hanya membatalkan satu batch, dan
pekerjaan bisa dilanjutkan dari checkpoint. Batch kecil (10.000) memberi
transaction terpendek dengan sedikit overhead (pencarian batas atas dan
checkpoint per batch); batch besar (200.000) lebih cepat secara total tetapi
setiap transaction mendekati 1 detik. Ukuran 10.000–50.000 adalah kompromi yang
baik untuk dataset ini.

### Migration v2: up/down, rollback, forward, dan riwayat

**SQL up dan down.** Setiap migration punya SQL up dan SQL down, atau ditandai
*irreversible* beserta alasannya (`POST /api/databases/{dbID}/migrations` dengan
`sql_up`, `sql_down`, `irreversible`, `irreversible_reason`). Aturan ini dijaga
CHECK constraint `migrations_down_or_irreversible`. Migration dari Milestone 1
dan yang dibuat lewat halaman lama tidak punya SQL down, jadi otomatis
irreversible.

**Revisi immutable.** Migration hanya bisa diedit selama `pending` atau
`failed` dan belum masuk request atau antrean. Setiap edit menaikkan
`revision`. Setelah pernah berhasil dieksekusi, SQL up dan down tidak bisa
diubah lagi, termasuk saat mengajukan rollback atau forward; perbaikan dilakukan
dengan migration baru. Setiap attempt menyimpan revisi dan salinan SQL yang
benar-benar dijalankan di `migration_runs`.

**Status migration.** `pending → success` (up), `success → rolled_back`
(rollback), `rolled_back → success` (forward). Attempt yang gagal tidak mengubah
posisi aktif: up gagal menjadi `failed`, rollback gagal tetap `success`, forward
gagal tetap `rolled_back`. Migration `rolled_back` boleh di-*discard* supaya
versi berikutnya bisa jalan; riwayatnya tetap ada.

**Aturan rollback dan forward** (`migration.CheckRunnable`):

- Rollback hanya untuk migration aktif terakhir, yaitu versi tertinggi yang
  berstatus `success`, dan tidak boleh irreversible. Untuk A → B → C, C harus
  di-rollback sebelum B.
- Rollback menjalankan SQL down asli migration itu dan merujuk execution ID
  up/forward yang dibatalkannya (`reverts_execution_id`).
- Forward menjalankan SQL up lagi untuk migration `rolled_back` yang merupakan
  giliran berikutnya, dan tidak otomatis menjalankan migration lain.
- Rollback dan forward adalah eksekusi baru dengan execution ID sendiri, melewati
  request, approval semua leader, antrean, dan worker seperti up.

**Bukti di database target.** `_dbplatform_history` berisi versi yang sedang
aktif dan ditulis dalam transaction yang sama dengan SQL migration: up/forward
menambah versi, rollback menghapusnya (harus tepat satu baris, sehingga rollback
ganda ditolak). Pemulihan memakai tabel ini untuk memastikan hasil attempt yang
terputus, termasuk rollback.

**Riwayat.** Semua attempt (sukses, gagal, rollback, forward, retry) dicatat di
log aplikasi JSON (`migration.attempt_start`, `sql_start`, `sql_result`,
`sql_commit`/`sql_rollback`, `migration.attempt_result`) dengan migration ID,
execution ID, versi, revisi, pengguna, waktu, status, dan salinan SQL. Tabel
`migration_history` menyimpan **semua attempt, berhasil maupun gagal** (spek
revisi 4), dengan `status` dan `error_message`. Baris ditulis dalam transaction
yang sama dengan perubahan status attempt, dan dijaga trigger supaya tidak bisa
di-UPDATE, DELETE, atau TRUNCATE. Tabel ini sengaja tanpa foreign key supaya
riwayat tetap ada walaupun koneksi atau user dihapus. Pembatalan batch (revert)
juga dicatat sebagai attempt `rollback` yang merujuk execution ID run batch
yang dibatalkannya.

`GET /api/databases/{dbID}/history` mengembalikan seluruh isi migration history,
termasuk attempt yang gagal. Dengan `?hide_rollback=true`
(checkbox **Hide rollback** di SPA), pasangan eksekusi yang dibatalkan dan
rollback sukses yang membatalkannya disembunyikan berdasarkan execution ID:

| Riwayat lengkap | Hide rollback |
|---|---|
| A [E1] | A [E1] |
| B [E2] | |
| B rollback E2 [E3] | |
| B forward [E4] | B [E4] |
| C [E5] | C [E5] |

Filter ini tidak menghapus data riwayat maupun log, dan attempt yang gagal
selalu tetap terlihat (rollback hanya dipasangkan dengan eksekusi yang sukses).
Contoh ini diuji di `TestHistoryHideRollbackSpecExample`.

**SPA.** Halaman `Migration` (daftar dan pembuatan dengan SQL up/down atau
irreversible), detail migration (SQL, revisi, eksekusi, edit, discard, dan tombol
ajukan up/rollback/forward), serta `Riwayat eksekusi` dengan checkbox Hide
rollback.

## Keputusan desain Milestone 2

| Topik | Keputusan |
|---|---|
| Message broker | Antrean berbasis tabel `jobs` di database platform dengan `FOR UPDATE SKIP LOCKED` dan lease, tanpa broker eksternal (diperbolehkan asisten selama mekanisme queue-worker asynchronous ada). |
| Satu job aktif per database | Dijamin index unik parsial `jobs_one_active_per_db_uq`. Job `running`, `uncertain`, `partial`, dan `partially_reverted` menahan database. |
| Riwayat dan attempt | Semua attempt dicatat, termasuk yang gagal dan ter-rollback. Log aplikasi ke stdout tidak disimpan di database platform. |
| Execution ID | `migration_runs.id`. |
| Self-approval | Diperbolehkan. |
| Approval setelah akses atau leader berubah | Dicabut otomatis selama job belum berjalan, request kembali ke `pending_review`. |
| Group tanpa leader | Request tetap pending, tidak ditolak. |
| Enqueue | Manual, setelah semua approval lengkap. |
| Retry, resume, pembatalan batch | Tidak memerlukan approval ulang. |
| Data pemulihan batch | Cukup SQL down yang reversible untuk rentang batch yang sudah committed. |
| Audit log | Structured log ke stdout, tidak di database platform. |
| Validasi sebelum review | Sintaks dan izin. |
| MySQL | Bonus M1, tidak dipakai di alur M2. Job, batch, rollback, dan forward hanya untuk PostgreSQL. |

---

## Milestone 2: Logging, Tracing, dan Observability

### 1. Structured Logging JSON (`internal/obs`)

Seluruh log aplikasi menggunakan structured log berformat JSON via `log/slog` dengan handler pembungkus di `internal/obs/handler.go`. Log dicetak ke stdout/stderr dan tidak disimpan di database platform.

#### Atribut Wajib
Setiap log HTTP dan aktivitas memuat atribut wajib berikut:
- `time`: Stempel waktu ISO 8601 UTC.
- `level`: Level log (`INFO`, `WARN`, `ERROR`, `DEBUG`).
- `event`: Nama kejadian/aktivitas (misal: `http_request`, `sql_start`, `sql_result`, `sql_commit`, `sql_rollback`).
- `actor`: Username pengguna yang terautentikasi (atau `anonymous`).
- `database`: ID atau nama database target yang sedang diakses.
- `status`: Kode status HTTP atau status eksekusi (`success`, `failed`).
- `correlation_id` / `request_id`: ID korelasi pelacakan request lintas komponen (dari header `X-Request-ID` atau digenerate otomatis).
- `trace_id` & `span_id`: ID trace dan span OpenTelemetry yang aktif pada context saat log dipanggil.

#### Redaksi Kunci & Nilai Sensitif
Handler secara otomatis mendeteksi dan menyamarkan informasi sensitif:
- Seluruh key atribut yang mengandung kata kunci seperti `password`, `passwd`, `secret`, `token`, `session`, `cookie`, `csrf`, `dsn`, `authorization`, `ciphertext`, diganti nilainya menjadi `[REDACTED]`.
- String koneksi (DSN PostgreSQL atau MySQL) yang berada di dalam pesan atau argumen diredaksi bagian password-nya (misal: `postgres://user:[REDACTED]@host:5432/db`).
- Parameter query SQL diredaksi menggunakan `obs.RedactArgs`.

#### Contoh Output Log HTTP

Output asli (23 November 2026) dari request enqueue job batch yang dipakai
di [`docs/observability.md`](docs/observability.md):

```json
{
  "time": "2026-11-23T12:25:39.7804845+07:00",
  "level": "INFO",
  "msg": "http",
  "event": "http_request",
  "actor": "admin",
  "status": 202,
  "correlation_id": "demo-enqueue-batch-1",
  "method": "POST",
  "path": "/api/databases/1/requests/1/enqueue",
  "dur_ms": 25,
  "database": "1",
  "trace_id": "8253f687e03dd309a0e02880ae8f3b06",
  "span_id": "bbbd02d2f3c15c86",
  "request_id": "demo-enqueue-batch-1"
}
```

### 2. Wrapper SQL Ber-log (`internal/dbmanager/logged.go`)

Seluruh query database (termasuk query read-only viewer di `viewer.go`, `service.go`, `explain.go`, `diff.go`, dan worker) dibungkus oleh `LoggedDB` / `LoggedTx`.
Mulai eksekusi, hasil eksekusi, serta commit/rollback dicatat secara terpisah:
- **`sql_start`**: mencatat SQL query dan parameter yang diredaksi sebelum query dijalankan.
- **`sql_result`**: mencatat durasi (`dur_ms`), jumlah row yang terpengaruh/dikembalikan, dan kode `sqlstate` jika terjadi error.
- **`sql_commit`** / **`sql_rollback`**: mencatat hasil penyelesaian transaksi beserta durasinya.
- Jika terdapat metadata job/migration di context (`obs.WithJobContext`), log otomatis menambahkan `job_id`, `execution_id`, `migration_id`, `attempt`, dan `batch`.

#### Contoh Output Log SQL

Output asli batch 1 dari job yang sama di worker: hasil eksekusi dan commit
dicatat terpisah, lengkap dengan `job_id`, `execution_id`, `attempt`, dan
`batch`. `trace_id` sama dengan request API di atas.

```json
{
  "time": "2026-11-23T12:25:40.497228+07:00",
  "level": "INFO",
  "msg": "sql_result",
  "event": "sql_result",
  "query": "UPDATE produk SET harga = harga * 2 WHERE id > $1 AND id <= $2",
  "database": "1",
  "params": [0, 10000],
  "dur_ms": 46,
  "rows_affected": 10000,
  "status": "success",
  "trace_id": "8253f687e03dd309a0e02880ae8f3b06",
  "span_id": "7b716fd0502d30d3",
  "job_id": 1,
  "execution_id": 1,
  "migration_id": 1,
  "attempt": 1,
  "batch": 1
}
```

```json
{
  "time": "2026-11-23T12:25:40.5134333+07:00",
  "level": "INFO",
  "msg": "sql_commit",
  "event": "sql_commit",
  "database": "1",
  "dur_ms": 8,
  "status": "success",
  "trace_id": "8253f687e03dd309a0e02880ae8f3b06",
  "span_id": "859804a77baf34d6",
  "job_id": 1,
  "execution_id": 1,
  "migration_id": 1,
  "attempt": 1,
  "batch": 1
}
```

Urutan lengkap log job ini (API, worker, lock, batch, hasil) ada di
[`docs/observability.md`](docs/observability.md).

### 3. Tracing OpenTelemetry (`internal/obs/trace.go`)

Tracing OpenTelemetry menghubungkan alur API &rarr; Queue &rarr; Worker &rarr; Lock &rarr; Batch &rarr; Database query dalam satu trace.

| Span | Tempat | Atribut penting |
|---|---|---|
| `METHOD /path` | middleware HTTP | |
| `queue.enqueue` | `approval.Store.Enqueue` | `db.id`, `migration.id`, `job.id`, `job.kind`, `job.mode` |
| `job.process` | worker | `job.id`, `job.attempt`, `job.result` |
| `migration.lock` | eksekusi biasa dan batch | `db.id`, `lock.scope` (`session` untuk batch), `lock.acquired` |
| `migration.batch` | setiap batch (run dan revert) | `batch.no`, `batch.lower`, `batch.upper`, `batch.rows`, `batch.action` |
| `db.*` | `LoggedDB`/`LoggedTx` | `db.statement` |

- **Propagasi lewat queue**: span `queue.enqueue` menyimpan `traceparent` W3C
  ke kolom `jobs.trace_context`. Worker memanggil `obs.ExtractTraceParent`, jadi
  `job.process` menjadi anak `queue.enqueue` walaupun dijalankan di goroutine
  atau proses lain.
- **Durasi dan error**: setiap span mencatat durasinya; span yang gagal
  berstatus `ERROR` dan mencatat exception (`obs.RecordError`).
- **Exporter** (`OTEL_TRACES_EXPORTER`): `otlp` mengirim ke OpenTelemetry
  Collector lewat OTLP/HTTP (`OTEL_EXPORTER_OTLP_ENDPOINT`, default
  `http://localhost:4318`), `none` mematikan ekspor, selain itu console (stdout).
- Diuji di `TestEnqueueSpanCarriesTraceContextToJob` dan
  `TestBatchTracingLockAndPerBatchSpans`.

#### Contoh span (output asli dari Jaeger)

Trace job batch yang sama dengan contoh log di atas (32 span; `db.*` hanya
ditampilkan untuk batch 1):

```
POST /api/databases/1/requests/1/enqueue  [25.9 ms]
  queue.enqueue  [3.3 ms]  db.id=1 job.mode=batch job.id=1
    job.process  [485.0 ms]  job.id=1 job.attempt=1 job.result=succeeded
      migration.lock  [4.7 ms]  lock.scope=session lock.acquired=True
      migration.batch  [65.8 ms]  batch.no=1 batch.lower=0 batch.upper=10000 batch.rows=10000
        db.tx.exec  [46.3 ms]   (UPDATE produk ...)
        db.tx.commit  [13.6 ms]  span_id=859804a77baf34d6  <- sama dengan log sql_commit di atas
      migration.batch  [65.0 ms]  batch.no=2 ...
      migration.batch  [62.9 ms]  batch.no=3 ...
      migration.batch  [55.4 ms]  batch.no=4 ...
      migration.batch  [54.3 ms]  batch.no=5 ...
```

![Trace job batch di Jaeger](docs/img/jaeger-trace-batch.png)

### 4. Observability Stack (Collector, Prometheus, Grafana)

Disediakan stack observability lengkap di `docker-compose.yml` di bawah profile `obs`:
- **OpenTelemetry Collector**: menerima trace dan log OTLP (gRPC `4317`, HTTP `4318`), meneruskan trace ke Jaeger dan log ke Loki (`deploy/observability/otel-collector-config.yml`).
- **Jaeger**: backend trace di `http://localhost:16686`.
- **Loki**: backend log terpusat di `http://localhost:3100`. Atribut log (`trace_id`, `span_id`, `job_id`, `actor`, `event`, ...) disimpan sebagai structured metadata.
- **Prometheus**: mengikis `/metrics` aplikasi setiap 5 detik di `http://localhost:9090`.
- **Grafana**: `http://localhost:3000` (lihat tanpa login; edit dengan `admin`/`admin`). Datasource Prometheus, Jaeger, dan Loki diprovisioning, dengan korelasi dua arah: `trace_id` di log Loki menjadi tautan ke trace Jaeger, dan dari span Jaeger bisa membuka log Loki dengan `trace_id` yang sama.

Log tetap selalu ditulis ke stdout. Dengan `OTEL_LOGS_EXPORTER=otlp`, record
yang sama (sudah diperkaya `trace_id` dan sudah diredaksi) juga dikirim ke
Collector lewat bridge `otelslog` (`internal/obs/logs.go`).

#### Menjalankan Stack Observability:
```bash
make obs-up          # otel-collector, loki, jaeger, prometheus, grafana
OTEL_TRACES_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 make run
make obs-down        # menghentikan stack observability
```

**Contoh hasil korelasi log, trace, dan metrik** untuk satu job batch (output
asli, termasuk screenshot Jaeger dan dashboard Grafana) ada di
[`docs/observability.md`](docs/observability.md).

#### Endpoint Metrik Prometheus (`GET /metrics`)
Aplikasi mengekspos metrik berformat Prometheus di `/metrics`:
- **Queue/Job**:
  - `dbp_jobs{status="..."}` (gauge): jumlah job **saat ini** per status (`queued` menunggu, `running` berjalan, `succeeded`, `failed`, `uncertain`, `partial`, `partially_reverted`, `reverted`), dihitung dari tabel `jobs` setiap scrape sehingga tetap benar setelah restart.
  - `dbp_jobs_total{db="...", status="..."}`: jumlah hasil job per status yang diproses worker sejak server menyala.
  - `dbp_job_retries_total{db="..."}`: Jumlah percobaan ulang job.
  - `dbp_batches_processed_total{db="..."}` & `dbp_rows_processed_total{db="..."}`: Jumlah batch dan row yang telah di-commit.
  - `dbp_queue_wait_seconds`: Waktu tunggu antrean job.
  - `dbp_lock_wait_seconds`: Waktu tunggu perolehan lock database.
  - `dbp_migration_duration_seconds`: Durasi total eksekusi migration.
- **Connection Pool** (dari `PoolManager.Stats()`):
  - `dbp_pool_open_connections{db_id="..."}`: Jumlah koneksi terbuka ke target.
  - `dbp_pool_in_use_connections{db_id="..."}`: Jumlah koneksi yang sedang aktif melayani query.
  - `dbp_pool_idle_connections{db_id="..."}`: Jumlah koneksi menganggur di pool.
  - `dbp_pool_max_open_connections{db_id="..."}`: Batas maksimal koneksi pool.
  - `dbp_pool_wait_count_total{db_id="..."}` & `dbp_pool_wait_duration_seconds_total{db_id="..."}`: Statistik antrean menunggu koneksi.

Label dibatasi pada nilai yang jumlahnya terbatas (`status`, `db`/`db_id`);
ID user, ID job, dan SQL tidak pernah menjadi label.

Dashboard Grafana diprovisioning otomatis dari
`deploy/observability/grafana/dashboards/dbplatform.json`: job per status,
retry, waktu tunggu antrean dan lock, durasi migration, progres batch/row,
koneksi pool open/in-use/idle, waktu tunggu koneksi, dan panel log Loki.

![Dashboard Grafana](docs/img/grafana-dashboard.png)

### 5. Frontend Single-Page Application (SPA)

Antarmuka dipindahkan ke SPA berbasis React + TypeScript di folder `frontend/`:
- **Login dan admin**: user, group (anggota, grant, leader), koneksi database dengan tes koneksi.
- **Request perubahan**: daftar, detail (SQL, status approval per group, job terkait), ajukan, approve, tolak, batalkan, dan jalankan.
- **Daftar Database**: Menampilkan badge DBMS (PostgreSQL / MySQL), badge hak akses (View Only / View + Edit), serta pencarian database.
- **Ringkasan Schema**: Mengelompokkan relasi berdasarkan schema dengan badge tipe (`tabel`, `view`, `materialized view`, `foreign table`, dsb.).
- **Detail Struktur Tabel**: Kolom (tipe, nullable, default), Constraint (PK, FK, Unique, Check), dan Index.
- **Data Viewer Berpagination**: Pagination default 50 baris (dapat dipilih 10, 25, 50, 100, 200), indikator total baris dan estimasi, serta penanganan sel `NULL` dan teks panjang.
- **Tampilan**: tema "Peach Pudding" yang sama dengan halaman Milestone 1 (warna, header, kartu, tabel, dan halaman daftar database dengan hero dan kartu database), sekarang sebagai SPA.
- **Responsivitas**: Komponen tabel lebar memiliki pembungkus scroll horizontal internal (`.table-responsive-wrapper`), SQL di `<pre>` dan editor punya scroll sendiri, sehingga tidak merusak lebar layout halaman baik pada mobile (360px) maupun desktop (1280px).
- **Hasil uji responsif** (23 November 2026, Chrome headless lewat DevTools Protocol): 20 halaman SPA dengan tema akhir (login, daftar database, schema, struktur dan data tabel, migration, detail migration, schema diff, riwayat, job, detail job batch, request, detail request, admin user/group/koneksi) diukur pada lebar 360 px dan 1280 px. Di semua halaman lebar dokumen tidak melebihi lebar layar (tidak ada scroll horizontal di level halaman) dan tidak ada tombol, input, atau select yang berada di luar layar. Contoh tampilan 360 px:

  ![SPA di layar 360 px](docs/img/spa-mobile-360.png)

Untuk menjalankan frontend dev server:
```bash
make fe-dev
```
Untuk build production:
```bash
make fe-build
```

## API JSON (Milestone 2)

Base path `/api`. Error berbentuk `{"error":{"code","message","fields"}}`. Semua request yang mengubah data membawa header `X-CSRF-Token` yang didapat dari `GET /api/auth/me`.

| Route | Butuh |
|---|---|
| `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/me` | publik, login |
| `GET /api/databases`, `GET /api/databases/{dbID}`, tabel dan data | View |
| `GET /api/databases/{dbID}/requests`, `GET .../requests/{requestID}` | View |
| `POST .../migrations/{migrationID}/requests`, `.../requests/{requestID}/submit`, `.../enqueue`, `.../cancel` | Edit |
| `POST .../requests/{requestID}/approve`, `.../reject` | View + leader group yang punya akses |
| `GET .../migrations`, `GET .../migrations/{migrationID}` | View |
| `POST .../migrations`, `PATCH .../migrations/{migrationID}`, `POST .../{migrationID}/discard` | Edit |
| `POST .../migrations/{migrationID}/explain`, `GET/POST .../schema-diff` (bonus M1) | Edit |
| `GET .../jobs`, `GET .../jobs/{jobID}`, `GET .../history?hide_rollback=` | View |
| `POST .../jobs/{jobID}/resume`, `.../retry`, `.../revert` | Edit |
| `/api/admin/users`, `/groups`, `/databases` | Admin |
| `PUT /api/admin/groups/{groupID}/leader` | Admin |

Alur request: `draft → pending_review → approved → queued`. Setiap leader
group yang punya akses harus approve, self-approval boleh. Jika leader atau
akses berubah sebelum job berjalan, approval dicabut dan request kembali ke
`pending_review`. Aturan lengkapnya ada di bagian *Workflow approval*.

Menjalankan SPA: `make fe-build` lalu `make run`, buka `http://localhost:8080/app/`. Untuk pengembangan: `make fe-dev`.

### Stress testing dengan k6 (bonus)

```bash
make k6                          # atau: bash k6/run.sh
STAGE_SECONDS=10 SCALE=0.3 make k6   # uji cepat dengan beban kecil
```

`k6/run.sh` menyiapkan lingkungan terpisah supaya data platform utama tidak
tersentuh: database platform `k6_platform` dan tiga database target `k6_a`,
`k6_b`, `k6_c` (masing-masing berisi tabel `k6_items` 1.000 row) di container
`test-db`, server di port 8081 dengan admin khusus k6 (`WORKER_CONCURRENCY=4`),
lalu menjalankan k6 dari image `grafana/k6:0.54.0`. Output tersimpan di
`k6/hasil/output.txt` dan `k6/hasil/summary.json`.

**Script** ([`k6/stress.js`](k6/stress.js)). `setup()` login sebagai admin,
membuat group k6 (admin sebagai anggota dan leader), mendaftarkan tiga koneksi
target, dan memberi akses View + Edit. Dua skenario berjalan bersamaan:

- `api`: user membaca API secara bergiliran (daftar database, schema, data
  tabel, migration, job, riwayat, request) dengan think time 0,5–1 detik.
- `jobs`: setiap iterasi membuat migration, request `up`, submit, approve
  oleh leader, lalu enqueue, dan memantau `GET /jobs/{id}` sampai status
  terminal. Database dipilih bergiliran, jadi ada job bersamaan di database
  yang sama maupun berbeda. Respons enqueue (202) tidak dihitung sebagai
  keberhasilan; job baru dihitung sukses setelah berstatus `succeeded`.
  Enqueue yang dibalas 409 (versi sebelumnya di database itu belum selesai,
  urutan linear dijaga server) diulang setelah 0,3 detik dan dihitung di
  metrik `enqueue_conflicts`, bukan sebagai error.

Waktu tunggu queue = `started_at − created_at` job, durasi job =
`finished_at − started_at`, end-to-end = `finished_at − created_at`, semua dari
API job.

**Beban.** Beban normal yang kami tetapkan: 10 VU pembaca API dan 2 VU
pengirim job. Beban dinaikkan bertahap ke 2x dan 4x, lalu diturunkan kembali
ke normal untuk melihat pemulihan, kemudian ke 0. Setiap tahap 60 detik dengan
transisi 15 detik.

**Threshold:** p95 response time API (skenario `api`) < 500 ms, error rate
request (`http_req_failed`) < 1%, tingkat keberhasilan job > 99%, serta p95 API
< 1000 ms di setiap tahap.

**Hasil** (laptop yang sama dengan dataset uji, server dan PostgreSQL lokal di
Docker Desktop, 23 November 2026):

| Tahap | VU api/jobs | Request | Request/s | p50 API (ms) | p95 API (ms) | Error rate | Job sukses | Job/menit | Queue wait p50/p95 (ms) | Durasi job p50/p95 (ms) | End-to-end p95 (ms) |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Normal | 10/2 | 1.878 | 31,3 | 12,6 | 48,6 | 0,00% | 100% (114) | 114 | 732/862 | 61/98 | 930 |
| Tinggi (2x) | 20/4 | 3.396 | 56,6 | 17,0 | 47,6 | 0,00% | 100% (172) | 172 | 791/890 | 56/86 | 960 |
| Puncak (4x) | 40/8 | 5.761 | 96,0 | 15,8 | 52,5 | 0,00% | 100% (182) | 182 | 788/929 | 54/101 | 988 |
| Pemulihan (normal) | 10/2 | 1.850 | 30,8 | 25,9 | 73,9 | 0,00% | 100% (115) | 115 | 719/859 | 65/86 | 944 |

Total 16.278 request (51,6/s), p95 API keseluruhan 54,4 ms, error rate 0%,
757 job sukses tanpa job gagal, 1.309 enqueue dibalas 409 lalu diulang. Semua
threshold lulus.

**Analisis.**

- **API tetap cepat sampai beban 4x.** Throughput request naik hampir linear
  (31 → 57 → 96 request/s) sementara p95 tetap sekitar 50 ms dan tidak ada
  error. Eksekusi migration berjalan di worker, jadi request API tidak ikut
  menunggu SQL; ini tujuan utama antrean asynchronous.
- **Throughput job jenuh di sekitar 180 job/menit.** Dari normal ke 2x job
  per menit naik (114 → 172), tetapi dari 2x ke 4x hampir tidak bertambah
  (182) walaupun pengirim job dua kali lipat. Penyebabnya disengaja: satu
  database hanya menjalankan satu migration dan migration harus berurutan,
  sehingga dengan tiga database paling banyak sekitar tiga job berjalan
  bersamaan. Kelebihan pengirim terlihat sebagai enqueue 409 yang diulang,
  bukan sebagai job gagal atau double commit.
- **Waktu tunggu queue sekitar 0,7–0,9 detik di semua tahap** dan tidak
  menumpuk. Angka ini didominasi interval polling worker saat antrean kosong
  (1 detik), bukan antrean yang panjang: begitu job di-enqueue, worker
  mengambilnya pada polling berikutnya. Durasi eksekusi job sendiri hanya
  sekitar 60 ms (p95 ±100 ms) dan tidak memburuk saat beban naik, karena lock
  per database mencegah dua migration saling berebut di target yang sama.
- **Pemulihan.** Setelah beban turun ke normal, error tetap 0 dan throughput
  job kembali ke tingkat normal (115 job/menit). p50/p95 API sedikit lebih
  tinggi daripada tahap normal pertama (25,9/73,9 ms vs 12,6/48,6 ms). Dugaan
  kami (belum diukur per endpoint): pada titik ini setiap database sudah berisi
  ratusan migration dan entri riwayat,
  sedangkan `GET /migrations` dan `GET /history` mengembalikan seluruh daftar
  tanpa pagination, jadi respons membesar seiring data. Pagination pada kedua
  endpoint itu adalah perbaikan berikutnya jika riwayat tumbuh jauh lebih
  besar.
- Interval polling worker bisa diperkecil untuk menurunkan waktu tunggu queue,
  dengan biaya query `SKIP LOCKED` yang lebih sering ke database platform.

### Security: SAST dengan CodeQL (bonus)

Workflow `.github/workflows/codeql.yml` menjalankan CodeQL (query
`security-extended`) untuk Go dan TypeScript pada push dan pull request ke
`main`, setiap Senin, dan manual lewat *Run workflow*. Hasilnya ada di tab
**Security → Code scanning**.

Setiap run juga menulis ringkasan temuan (rule, severity, lokasi, dan sumber
data) ke job summary dan anotasi run, serta menyimpan file SARIF sebagai
artifact `codeql-sarif-go` / `codeql-sarif-javascript-typescript`.

**Cakupan.** Seluruh kode Go (`cmd/`, `internal/`, `web/`: backend dan worker)
dan seluruh kode TypeScript/React di `frontend/`, dengan suite
`security-extended`.

**Hasil scan** pada commit `d89686e` (merge PR #16, push ke `main`,
23 November 2026; run `codeql` #45115528553): TypeScript **0 temuan**, Go
**10 temuan** dari 3 rule. Hasilnya sama dengan scan sebelumnya pada
`f0a24d9`. Severity adalah `security-severity` CodeQL, sumber adalah lokasi
nilai dari user yang dirujuk pesan CodeQL. Scan pada pull request memakai
analisis *diff-informed* (hanya baris yang berubah), jadi bukti lengkapnya
adalah run push ke `main`.

| Rule | Severity | Lokasi | Sumber data | Dampak | Keputusan | Alasan |
|---|---|---|---|---|---|---|
| `go/sql-injection` | 8.8 (high) | `internal/migration/explain.go:156` (`runStatement`) | Body JSON request (`internal/httpx/jsonbody.go:69-70`) dan form schema diff (`internal/migration/handler.go:291`) | User bisa menjalankan SQL sembarang di database target. | Risiko diterima (perilaku yang memang dirancang) | `runStatement` dipakai analisis query dan schema diff; menjalankan SQL buatan user adalah fungsi inti platform. Keduanya hanya untuk user dengan akses View + Edit (dicek `authz` di route dan lagi di service), berjalan di transaction yang **selalu di-rollback**, dengan extended protocol sehingga lebih dari satu statement atau `COMMIT` tersembunyi ditolak driver. Schema diff juga memaksa `search_path` ke `pg_temp` (DDL hanya membuat tabel temporary), menolak DDL yang menyebut schema target (`checkUnqualified`), dan memakai `statement_timeout` 30 detik serta `lock_timeout` 5 detik. Eksekusi sungguhan selalu lewat approval semua leader. |
| `go/log-injection` (5 temuan) | 6.1 (medium) | `internal/dbmanager/logged.go:130, 152, 157, 167, 182` | Body JSON request (`internal/httpx/jsonbody.go:69-70`) | Penyerang memalsukan baris log dengan menyisipkan newline. | False positive | Log SQL (query dan parameter) wajib dicatat menurut spek. Semua log ditulis oleh `slog` JSON handler (`cmd/server/main.go:30`), yang meng-escape newline dan tanda kutip di dalam string JSON, jadi input user tidak bisa membuat record log baru. Sudah diuji: query berisi `\n{"level":"ERROR",...}` tetap tercatat sebagai satu nilai string. Parameter SQL juga disamarkan `obs.RedactArgs`. |
| `go/log-injection` | 6.1 (medium) | `internal/approval/store.go:340` | Body JSON request (`internal/httpx/jsonbody.go:69-70`) | Sama seperti di atas. | False positive | Atribut yang dicatat hanya ID numerik (`int64`) dan status dari konstanta internal; tidak ada teks bebas dari user, dan log tetap melalui JSON handler yang sama. |
| `go/clear-text-logging` (3 temuan) | 7.5 (high) | `internal/httpx/middleware.go:158, 160, 162` | Header request (`internal/httpx/middleware.go:114`, `X-Request-ID`/`X-Correlation-ID`) | Data sensitif dari header HTTP tercatat di log. | False positive | Nilai dari header itu adalah ID korelasi, bukan credential, dan hanya diterima jika berisi huruf, angka, `-`, `_`, `.` dengan panjang maksimal 64 karakter (`validRequestID`); selain itu diganti ID acak. Cookie session, token CSRF, dan password tidak pernah dicatat; handler log juga menyamarkan key seperti `session`, `token`, `csrf`, `password`, `cookie`, dan `authorization`. |

Tidak ada temuan yang perlu diperbaiki di kode. Hasil bersih tidak berarti
aplikasi pasti aman: CodeQL tidak menguji logika otorisasi, yang dicakup oleh
integration test akses (alur berhasil, gagal, dan penolakan akses).