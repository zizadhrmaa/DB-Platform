# DB Platform IF3110 Milestone 1

Fondasi Database Management Platform: access control (login, session,
group-based authorization), database viewer untuk PostgreSQL, dan migration
yang terkontrol dengan riwayat linear.

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

| Nama | Host (dari laptop) | Port | Database | User | Password |
|---|---|---|---|---|---|
| Toko | localhost | 5433 | toko | platform_app | platform_app_pass |
| Kampus | localhost | 5434 | kampus | platform_app | platform_app_pass |

Jika aplikasi dijalankan di Docker (`docker compose --profile app up`), gunakan host
`target-toko` / `target-kampus` port `5432`.

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