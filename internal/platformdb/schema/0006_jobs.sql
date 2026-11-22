-- ---------------------------------------------------------------------
-- JOBS
-- Antrean eksekusi migration. Setiap eksekusi (up, rollback, forward)
-- menjadi satu baris di sini lalu diambil worker dengan
-- SELECT ... FOR UPDATE SKIP LOCKED, jadi job tetap ada setelah restart.
--
-- Status:
--   queued             -> menunggu worker (atau menunggu jeda retry)
--   running            -> dipegang worker selama lease_until belum lewat
--   succeeded          -> selesai dan sudah committed
--   failed             -> gagal permanen (SQL gagal, attempt habis, ditolak revalidasi)
--   uncertain          -> hasil commit belum bisa dipastikan, menunggu pemeriksaan bukti
--   partial            -> mode batch: sebagian batch committed, sisanya belum
--   partially_reverted -> mode batch: pembatalan batch berhenti di tengah
--
-- running, uncertain, partial, dan partially_reverted menahan database:
-- selama ada job dengan status itu, job lain di database yang sama tidak
-- boleh di-claim. Aturan ini dijamin index unik parsial di bawah, bukan
-- hanya oleh kode, supaya tetap benar lintas worker dan lintas proses.
--
-- Lease: worker yang memegang job memperpanjang lease_until lewat
-- heartbeat. Lease yang lewat berarti worker dianggap mati dan job masuk
-- proses pemulihan. Semua perubahan status memakai compare-and-set
-- terhadap lease_owner supaya worker lama tidak menimpa hasil pemulihan.
-- ---------------------------------------------------------------------
CREATE TABLE jobs (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    db_id             BIGINT      NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    migration_id      BIGINT      NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    -- FK ke change_requests ditambahkan di file terpisah setelah skema approval masuk.
    request_id        BIGINT,
    kind              TEXT        NOT NULL DEFAULT 'up'
                      CHECK (kind IN ('up','rollback','forward')),
    mode              TEXT        NOT NULL DEFAULT 'normal'
                      CHECK (mode IN ('normal','batch')),
    status            TEXT        NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued','running','succeeded','failed','uncertain','partial','partially_reverted')),
    -- Kunci dari client (misalnya per klik tombol) supaya kiriman ulang
    -- tidak membuat job kedua.
    idempotency_key   TEXT        NOT NULL UNIQUE
                      CHECK (btrim(idempotency_key) <> '' AND length(idempotency_key) <= 200),
    requested_by      BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    attempt           INTEGER     NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts      INTEGER     NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    next_run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner       TEXT,
    lease_until       TIMESTAMPTZ,
    -- migration_runs untuk attempt yang sedang/terakhir berjalan. Dikosongkan
    -- setiap claim, jadi selalu merujuk attempt saat ini.
    run_id            BIGINT      REFERENCES migration_runs(id) ON DELETE SET NULL,
    -- Header W3C traceparent dari request API, diteruskan ke worker.
    trace_context     TEXT,
    batches_committed INTEGER     NOT NULL DEFAULT 0 CHECK (batches_committed >= 0),
    rows_committed    BIGINT      NOT NULL DEFAULT 0 CHECK (rows_committed >= 0),
    last_cursor       TEXT,
    error_message     TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Hanya job running yang punya pemilik lease.
    CONSTRAINT jobs_lease_consistency CHECK (
        (status = 'running') = (lease_owner IS NOT NULL AND lease_until IS NOT NULL)
    )
);

-- Satu job aktif per database.
CREATE UNIQUE INDEX jobs_one_active_per_db_uq ON jobs (db_id)
    WHERE status IN ('running','uncertain','partial','partially_reverted');

-- Satu job terbuka per migration, supaya migration yang sama tidak antre dua kali.
CREATE UNIQUE INDEX jobs_one_open_per_migration_uq ON jobs (migration_id)
    WHERE status IN ('queued','running','uncertain','partial','partially_reverted');

CREATE INDEX jobs_claim_idx    ON jobs (next_run_at, id) WHERE status = 'queued';
-- Claim hanya mengambil job queued tertua per database.
CREATE INDEX jobs_queued_per_db_idx ON jobs (db_id, id) WHERE status = 'queued';
CREATE INDEX jobs_recovery_idx ON jobs (lease_until) WHERE status IN ('running','uncertain');
CREATE INDEX jobs_db_idx       ON jobs (db_id, created_at DESC, id DESC);
