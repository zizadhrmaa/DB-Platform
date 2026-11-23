# Antrean, worker, dan pemulihan lock

Dokumen ini menjelaskan cara eksekusi migration diproses secara asynchronous dan apa yang terjadi ketika worker gagal di tengah jalan. Kode terkait ada di `internal/queue`, `internal/worker`, dan `internal/migration` (`ExecuteWith`, `VerifyRun`).

## Alur

1. Setelah semua approval lengkap, request dimasukkan ke antrean menjadi satu baris di tabel `jobs` berstatus `queued`. API langsung mengembalikan job ID tanpa menunggu SQL.
2. Worker mengambil job dengan `SELECT ... FOR UPDATE SKIP LOCKED`, lalu mengubahnya menjadi `running` dan mencatat dirinya sebagai pemegang lease (`lease_owner`, `lease_until`).
3. Sebelum SQL dijalankan, worker memeriksa ulang (revalidasi): pengaju masih ada dan aktif, approval masih berlaku, akses masih View + Edit, dan migration masih giliran berikutnya.
4. Worker mengambil lock eksekusi di database target, mencatat attempt di `migration_runs`, lalu menjalankan SQL migration dan bukti eksekusi (`_dbplatform_history`) dalam satu transaction di target.
5. Hasil attempt ditulis ke job: `succeeded`, `failed`, dijadwalkan ulang (`queued`), atau `uncertain`.

Antrean disimpan di database platform, jadi job dan statusnya tetap ada setelah server restart.

## Satu database, satu migration

Ada dua lapis pengaman:

- **Di database platform**: index unik parsial `jobs_one_active_per_db_uq` hanya mengizinkan satu job berstatus `running`, `uncertain`, `partial`, atau `partially_reverted` per database. Karena dijamin index, aturan ini tetap benar walaupun ada banyak worker atau banyak proses server. Claim juga hanya mengambil job `queued` tertua per database, supaya migration berikutnya tidak menyalip migration sebelumnya yang sedang menunggu jeda retry.
- **Di database target**: lock advisory `pg_try_advisory_xact_lock(3110, db_id)` diambil di transaction yang menjalankan SQL migration. Lock ini level transaction, jadi lepas otomatis saat transaction commit atau rollback.

Database berbeda diproses paralel oleh goroutine worker yang berbeda.

## Lease dan heartbeat

Worker memperpanjang `lease_until` setiap sepertiga durasi lease. Kalau worker mati (proses dihentikan, server mati, atau jaringan putus), heartbeat berhenti dan lease lewat. Job dengan lease yang lewat dianggap milik worker yang mati dan diambil alih loop pemulihan.

Semua perubahan status memakai compare-and-set: worker hanya bisa menulis hasil kalau `lease_owner` masih dirinya dan status masih `running`. Pemulihan hanya bisa mengubah job kalau status, pemilik lease, nomor attempt, dan `run_id` masih sama dengan yang ia periksa. Jadi worker lama yang ternyata masih hidup tidak bisa menimpa hasil pemulihan, dan pemulihan tidak bisa menimpa attempt yang baru saja tercatat.

## Pemulihan lock ketika worker gagal

Lock eksekusi di target tidak pernah perlu dibuka paksa. Saat proses worker mati, koneksinya ke database target putus. PostgreSQL kemudian me-rollback transaction yang belum commit dan melepas lock advisory transaction itu. Lock di platform (lease) lepas sendiri karena waktunya habis.

Loop pemulihan berjalan berkala di setiap worker dan memeriksa job `running` yang lease-nya lewat serta job `uncertain`:

| Keadaan job | Tindakan |
|---|---|
| Belum ada attempt tercatat (`run_id` kosong) | SQL belum pernah jalan. Job dijadwalkan ulang. |
| Ada attempt | Pemulihan mencoba mengambil lock target yang sama (tanpa menunggu), lalu membaca bukti. |
| Lock target masih dipegang | Transaction lama mungkin masih berjalan dan bisa saja commit. Job menjadi `uncertain`, migration tidak disentuh, dan database tetap ditahan. Diperiksa lagi di putaran berikutnya. |
| Lock didapat, bukti ada di `_dbplatform_history` | Perubahan sudah committed. Job `succeeded` tanpa menjalankan SQL lagi. |
| Lock didapat, bukti tidak ada | Transaction lama sudah di-rollback. Attempt dicatat terputus dan job dijadwalkan ulang. |
| Attempt gagal karena SQL ditolak server | Job `failed`; mengulang tidak akan membantu. |

Bukti eksekusi disimpan di database target, ditulis dalam transaction yang sama dengan SQL migration. Database platform dan target adalah server berbeda, jadi hanya di target pencatatan bisa atomik dengan perubahan migration. Begitu lock target berhasil diambil, isi `_dbplatform_history` adalah bukti final: transaction lama sudah pasti selesai.

## Hasil commit yang belum pasti

Kalau server target tidak menjawab COMMIT (misalnya koneksi putus tepat saat COMMIT), worker tidak langsung mengulang. Ia memeriksa bukti dengan langkah yang sama seperti pemulihan:

- bukti ada: attempt `success`, job `succeeded`;
- bukti tidak ada: attempt terputus, job dijadwalkan ulang;
- bukti belum bisa diperiksa: job `uncertain`, migration tetap `running`.

Selama job `uncertain`, index unik menahan database itu, sehingga job lain di database yang sama tidak diambil. Loop pemulihan terus memeriksa sampai hasilnya pasti. Karena bukti memakai versi migration sebagai primary key di `_dbplatform_history`, eksekusi ulang yang tidak sengaja juga akan ditolak database target, sehingga tidak ada double commit.

## Retry dan idempotensi

- Kegagalan sementara (lock target dipegang, target tidak terjangkau, koneksi putus sebelum commit, worker mati) dijadwalkan ulang dengan jeda `min(2s × 2^(attempt−1), 60s)` dan maksimal 5 attempt. Setelah itu job `failed`.
- SQL yang ditolak server (termasuk COMMIT yang ditolak, misalnya constraint deferred) dan eksekusi yang melewati batas waktu langsung `failed`.
- Kiriman ulang dengan kunci idempotensi yang sama mengembalikan job yang sama, dan satu migration hanya boleh punya satu job terbuka (index unik `jobs_one_open_per_migration_uq`).

## Shutdown

Saat server dihentikan, worker berhenti mengambil job baru tetapi menyelesaikan job yang sedang berjalan. Eksekusinya tidak ikut dibatalkan. Kalau proses tetap dimatikan paksa, transaction di target di-rollback oleh PostgreSQL dan job dipulihkan seperti di atas.
