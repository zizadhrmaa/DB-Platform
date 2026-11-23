-- =====================================================================
-- Migration per batch.
--
-- Migration batch memproses tabel batch_table secara bertahap berdasarkan
-- kolom cursor batch_column (integer, unik, tidak berubah), batch_size row
-- per batch. SQL up dan SQL down masing-masing satu statement yang memakai
-- $1 (batas bawah, eksklusif) dan $2 (batas atas, inklusif), misalnya
--   UPDATE big SET harga = harga * 2 WHERE id > $1 AND id <= $2
-- Checkpoint setiap batch ditulis di database target dalam transaction yang
-- sama dengan perubahan batch itu (tabel _dbplatform_batches).
-- =====================================================================
ALTER TABLE migrations
    ADD COLUMN exec_mode    TEXT    NOT NULL DEFAULT 'normal' CHECK (exec_mode IN ('normal','batch')),
    ADD COLUMN batch_table  TEXT,
    ADD COLUMN batch_column TEXT,
    ADD COLUMN batch_size   INTEGER CHECK (batch_size > 0);

ALTER TABLE migrations ADD CONSTRAINT migrations_batch_config CHECK (
    (exec_mode = 'normal' AND batch_table IS NULL AND batch_column IS NULL AND batch_size IS NULL)
    OR (exec_mode = 'batch' AND btrim(COALESCE(batch_table, '')) <> ''
        AND btrim(COALESCE(batch_column, '')) <> '' AND batch_size IS NOT NULL)
);

-- action: run = menjalankan/melanjutkan batch dari checkpoint,
--         revert = membatalkan batch yang sudah committed (urutan terbalik).
ALTER TABLE jobs ADD COLUMN action TEXT NOT NULL DEFAULT 'run' CHECK (action IN ('run','revert'));

-- reverted: semua batch committed sudah dibatalkan.
ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('queued','running','succeeded','failed','uncertain','partial','partially_reverted','reverted'));
