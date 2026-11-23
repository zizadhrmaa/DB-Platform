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

## Testing

```bash
make test-unit          # unit test (integration otomatis di-skip)
make test-integration   # unit + integration terhadap container test-db
```

Integration test memakai schema PostgreSQL acak per test (`internal/testutil`),
sehingga terisolasi dan aman dijalankan paralel. Test API mencakup alur
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

> TODO (Aziza): jelaskan queue dan worker, lock lintas worker dan
> pemulihan lock saat worker mati, retry dan idempotensi, penanganan hasil
> commit yang belum pasti (`uncertain`), mode batch (checkpoint, resume,
> kompensasi per batch, status `partial` dan `partially_reverted`), serta
> dataset uji (jumlah row dan durasi `UPDATE` penuh dibanding mode batch).

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
`migration_history` hanya berisi eksekusi yang berhasil di-commit, ditulis dalam
transaction yang sama dengan status attempt, dan dijaga trigger supaya tidak bisa
di-UPDATE, DELETE, atau TRUNCATE. Tabel ini sengaja tanpa foreign key supaya
riwayat tetap ada walaupun koneksi atau user dihapus.

`GET /api/databases/{dbID}/history` mengembalikan eksekusi committed dan attempt
yang gagal (supaya kegagalan tetap terlihat). Dengan `?hide_rollback=true`
(checkbox **Hide rollback** di SPA), pasangan eksekusi yang dibatalkan dan
rollback sukses yang membatalkannya disembunyikan berdasarkan execution ID:

| Riwayat lengkap | Hide rollback |
|---|---|
| A [E1] | A [E1] |
| B [E2] | |
| B rollback E2 [E3] | |
| B forward [E4] | B [E4] |
| C [E5] | C [E5] |

Filter ini tidak menghapus data riwayat maupun log. Contoh ini diuji di
`TestHistoryHideRollbackSpecExample`.

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

#### Contoh Output Log HTTP:
```json
{
  "time": "2026-11-20T11:15:30.123456Z",
  "level": "INFO",
  "msg": "http",
  "event": "http_request",
  "actor": "bob",
  "database": "1",
  "status": 200,
  "correlation_id": "a1f8b29c4d7e",
  "method": "GET",
  "path": "/api/databases/1/tables/public/items/data",
  "dur_ms": 12,
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

### 2. Wrapper SQL Ber-log (`internal/dbmanager/logged.go`)

Seluruh query database (termasuk query read-only viewer di `viewer.go`, `service.go`, `explain.go`, `diff.go`, dan worker) dibungkus oleh `LoggedDB` / `LoggedTx`.
Mulai eksekusi, hasil eksekusi, serta commit/rollback dicatat secara terpisah:
- **`sql_start`**: mencatat SQL query dan parameter yang diredaksi sebelum query dijalankan.
- **`sql_result`**: mencatat durasi (`dur_ms`), jumlah row yang terpengaruh/dikembalikan, dan kode `sqlstate` jika terjadi error.
- **`sql_commit`** / **`sql_rollback`**: mencatat hasil penyelesaian transaksi beserta durasinya.
- Jika terdapat metadata job/migration di context (`obs.WithJobContext`), log otomatis menambahkan `job_id`, `execution_id`, `migration_id`, `attempt`, dan `batch`.

#### Contoh Output Log SQL:
```json
{
  "time": "2026-11-20T11:15:30.130100Z",
  "level": "INFO",
  "msg": "sql_start",
  "event": "sql_start",
  "query": "SELECT * FROM items WHERE category = $1",
  "params": ["buku"],
  "database": "toko",
  "actor": "bob",
  "request_id": "a1f8b29c4d7e",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "5a3c9b21f0e47d12"
}
```

```json
{
  "time": "2026-11-20T11:15:30.134500Z",
  "level": "INFO",
  "msg": "sql_result",
  "event": "sql_result",
  "query": "SELECT * FROM items WHERE category = $1",
  "params": ["buku"],
  "dur_ms": 4,
  "rows_affected": 0,
  "status": "success",
  "database": "toko",
  "actor": "bob",
  "request_id": "a1f8b29c4d7e",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "5a3c9b21f0e47d12"
}
```

### 3. Tracing OpenTelemetry (`internal/obs/trace.go`)

Tracing OpenTelemetry menghubungkan alur API &rarr; Queue &rarr; Worker &rarr; Lock &rarr; Database query.
- **Trace Context Propagation**: fungsi `obs.InjectTraceParent(ctx)` mengekspor context ke format standar W3C `traceparent` (contoh: `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`) yang disimpan di kolom `jobs.trace_context` saat enqueue. Worker kemudian memanggil `obs.ExtractTraceParent(ctx, traceparent)` sehingga span di dalam worker menjadi anak dari trace yang diawali oleh request API.
- **Span Exporter**: default menggunakan console/stdout (atau OTLP Collector saat stack observability dijalankan).

#### Contoh Struktur Span:
```
Span: GET /api/databases/1/tables/public/items/data
 ├─ Span: db.query (statement: "SELECT count(*) FROM items")
 └─ Span: db.query (statement: "SELECT id, name FROM items ORDER BY id LIMIT 50")
```

### 4. Observability Stack (Collector, Prometheus, Grafana)

Disediakan stack observability lengkap di `docker-compose.yml` di bawah profile `obs`:
- **OpenTelemetry Collector**: menerima traces & logs OTLP gRPC/HTTP di port `4317` & `4318`.
- **Jaeger**: visualisasi distributed trace di `http://localhost:16686`.
- **Prometheus**: mengikis metrik aplikasi setiap 5 detik di `http://localhost:9090`.
- **Grafana**: dashboard analitik terpusat di `http://localhost:3000` (user: `admin`, pass: `admin`).

#### Menjalankan Stack Observability:
```bash
make obs-up          # Menjalankan otel-collector, jaeger, prometheus, dan grafana
make obs-down        # Menghentikan stack observability
```

#### Endpoint Metrik Prometheus (`GET /metrics`)
Aplikasi mengekspos metrik berformat Prometheus di `/metrics`:
- **Queue/Job**:
  - `dbp_jobs_total{db="...", status="..."}`: Jumlah total job per status (`queued`, `running`, `succeeded`, `failed`, `partial`, `uncertain`).
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

Dashboard Grafana telah diprovisioning otomatis dari file `deploy/observability/grafana/dashboards/dbplatform.json`.

### 5. Frontend Single-Page Application (SPA)

Antarmuka dipindahkan ke SPA berbasis React + TypeScript di folder `frontend/`:
- **Login dan admin**: user, group (anggota, grant, leader), koneksi database dengan tes koneksi.
- **Request perubahan**: daftar, detail (SQL, status approval per group, job terkait), ajukan, approve, tolak, batalkan, dan jalankan.
- **Daftar Database**: Menampilkan badge DBMS (PostgreSQL / MySQL), badge hak akses (View Only / View + Edit), serta pencarian database.
- **Ringkasan Schema**: Mengelompokkan relasi berdasarkan schema dengan badge tipe (`tabel`, `view`, `materialized view`, `foreign table`, dsb.).
- **Detail Struktur Tabel**: Kolom (tipe, nullable, default), Constraint (PK, FK, Unique, Check), dan Index.
- **Data Viewer Berpagination**: Pagination default 50 baris (dapat dipilih 10, 25, 50, 100, 200), indikator total baris dan estimasi, serta penanganan sel `NULL` dan teks panjang.
- **Responsivitas**: Komponen tabel lebar memiliki pembungkus scroll horizontal internal (`.table-responsive-wrapper`) sehingga tidak merusak lebar layout halaman baik pada mobile (360px) maupun desktop (1280px).

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
| `GET .../jobs`, `GET .../jobs/{jobID}`, `GET .../history` | View (belum diimplementasikan) |
| `POST .../jobs/{jobID}/resume`, `.../retry`, `.../revert` | Edit (belum diimplementasikan) |
| `/api/admin/users`, `/groups`, `/databases` | Admin |
| `PUT /api/admin/groups/{groupID}/leader` | Admin |

Alur request: `draft → pending_review → approved → queued`. Setiap leader
group yang punya akses harus approve, self-approval boleh. Jika leader atau
akses berubah sebelum job berjalan, approval dicabut dan request kembali ke
`pending_review`. Aturan lengkapnya ada di bagian *Workflow approval*.

Menjalankan SPA: `make fe-build` lalu `make run`, buka `http://localhost:8080/app/`. Untuk pengembangan: `make fe-dev`.

> TODO (Aziza): hasil stress test k6 (script, threshold, hasil per tingkat beban).

### Security: SAST dengan CodeQL (bonus)

Workflow `.github/workflows/codeql.yml` menjalankan CodeQL (query
`security-extended`) untuk Go dan TypeScript pada push dan pull request ke
`main`, setiap Senin, dan manual lewat *Run workflow*. Hasilnya ada di tab
**Security → Code scanning**.

> TODO (Mirza): setelah scan pertama selesai, isi tabel temuan di bawah: rule,
> severity, lokasi, dampak, keputusan (diperbaiki / false positive / risiko
> diterima) beserta alasan, dan commit perbaikan + hasil scan ulang. Jika tidak
> ada temuan, tulis cakupan kode yang dianalisis dan lampirkan hasil scan.

| Rule | Severity | Lokasi | Dampak | Keputusan | Alasan / commit |
|---|---|---|---|---|---|
| | | | | | |