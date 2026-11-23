-- =====================================================================
-- Migration v2: SQL up/down, irreversible, rollback/forward, dan
-- migration history.
--
-- Nomor 0011 (bukan 0004) karena file ini memakai kolom migrations.revision
-- dari 0008; schema dijalankan urut nama file.
-- =====================================================================

-- ---------------------------------------------------------------------
-- SQL down dan irreversible
-- Migration reversible wajib punya SQL down. Migration irreversible tidak
-- punya SQL down dan wajib menyebut alasannya.
-- ---------------------------------------------------------------------
ALTER TABLE migrations
    ADD COLUMN sql_down            TEXT,
    ADD COLUMN irreversible        BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN irreversible_reason TEXT;

-- Migration dari Milestone 1 tidak punya SQL down.
UPDATE migrations
SET irreversible = TRUE,
    irreversible_reason = 'Dibuat sebelum Milestone 2, tidak punya SQL down.'
WHERE sql_down IS NULL;

-- Baris yang dibuat tanpa menyebut SQL down (data lama, insert langsung)
-- otomatis irreversible. Aplikasi selalu mengisi ketiga kolom secara eksplisit.
ALTER TABLE migrations
    ALTER COLUMN irreversible SET DEFAULT TRUE,
    ALTER COLUMN irreversible_reason SET DEFAULT 'Tidak punya SQL down.';

ALTER TABLE migrations ADD CONSTRAINT migrations_down_or_irreversible CHECK (
    (irreversible AND sql_down IS NULL AND btrim(COALESCE(irreversible_reason, '')) <> '')
    OR (NOT irreversible AND btrim(COALESCE(sql_down, '')) <> '' AND irreversible_reason IS NULL)
);

-- rolled_back: perubahan migration sudah dibatalkan oleh rollback yang
-- berhasil. Bisa diterapkan lagi dengan forward.
ALTER TABLE migrations DROP CONSTRAINT migrations_status_check;
ALTER TABLE migrations ADD CONSTRAINT migrations_status_check
    CHECK (status IN ('pending','running','success','failed','discarded','rolled_back'));

-- ---------------------------------------------------------------------
-- Attempt (execution). migration_runs.id adalah execution ID.
-- ---------------------------------------------------------------------
ALTER TABLE migration_runs
    ADD COLUMN kind           TEXT    NOT NULL DEFAULT 'up' CHECK (kind IN ('up','rollback','forward')),
    ADD COLUMN revision       INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    -- Untuk rollback: eksekusi up/forward yang dibatalkan.
    ADD COLUMN reverts_run_id BIGINT REFERENCES migration_runs(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------
-- MIGRATION HISTORY
-- Hanya eksekusi yang berhasil di-commit. Ditulis dalam transaction yang
-- sama dengan perubahan status attempt menjadi success. Tidak boleh diubah
-- atau dihapus (dijaga trigger), dan sengaja tanpa foreign key supaya
-- riwayat tetap ada walaupun koneksi database atau user dihapus.
-- ---------------------------------------------------------------------
CREATE TABLE migration_history (
    execution_id         BIGINT      PRIMARY KEY,           -- migration_runs.id
    db_id                BIGINT      NOT NULL,
    migration_id         BIGINT      NOT NULL,
    version              INTEGER     NOT NULL,
    revision             INTEGER     NOT NULL,
    title                TEXT        NOT NULL,
    kind                 TEXT        NOT NULL CHECK (kind IN ('up','rollback','forward')),
    sql_text             TEXT        NOT NULL,              -- salinan SQL yang benar-benar dijalankan
    executed_by          BIGINT,
    executed_by_username TEXT        NOT NULL DEFAULT '',
    started_at           TIMESTAMPTZ NOT NULL,
    committed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    reverts_execution_id BIGINT,                            -- rollback: eksekusi yang dibatalkan
    CONSTRAINT migration_history_rollback_ref CHECK ((kind = 'rollback') = (reverts_execution_id IS NOT NULL))
);
CREATE INDEX migration_history_db_idx ON migration_history (db_id, started_at, execution_id);

-- Eksekusi sukses dari Milestone 1 masuk riwayat sebagai up.
INSERT INTO migration_history (execution_id, db_id, migration_id, version, revision, title, kind,
                               sql_text, executed_by, executed_by_username, started_at, committed_at)
SELECT r.id, m.db_id, m.id, m.version, 1, m.title, 'up', r.sql_text, r.executed_by,
       COALESCE(u.username, ''), r.started_at, COALESCE(r.finished_at, r.started_at)
FROM migration_runs r
JOIN migrations m ON m.id = r.migration_id
LEFT JOIN users u ON u.id = r.executed_by
WHERE r.status = 'success';

CREATE FUNCTION migration_history_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'migration_history tidak boleh diubah atau dihapus';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER migration_history_no_update_delete
    BEFORE UPDATE OR DELETE ON migration_history
    FOR EACH ROW EXECUTE FUNCTION migration_history_immutable();
CREATE TRIGGER migration_history_no_truncate
    BEFORE TRUNCATE ON migration_history
    FOR EACH STATEMENT EXECUTE FUNCTION migration_history_immutable();
