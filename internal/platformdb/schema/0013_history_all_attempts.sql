-- =====================================================================
-- Migration history mencatat SEMUA attempt (spek M2 revisi 4).
--
-- Sebelumnya migration_history hanya berisi eksekusi yang berhasil
-- di-commit. Sekarang attempt yang gagal (SQL ditolak, transaction
-- di-rollback, atau terputus) juga ditulis, dalam transaction yang sama
-- dengan perubahan status attempt di migration_runs. Baris tetap tidak
-- boleh diubah atau dihapus (trigger dari 0011).
--
-- committed_at sekarang berarti waktu attempt selesai (berhasil maupun
-- gagal); nama kolom dipertahankan supaya data lama tetap utuh.
-- =====================================================================
ALTER TABLE migration_history
    ADD COLUMN status        TEXT NOT NULL DEFAULT 'success' CHECK (status IN ('success','failed')),
    ADD COLUMN error_message TEXT;

COMMENT ON COLUMN migration_history.committed_at IS 'Waktu attempt selesai (berhasil atau gagal).';

-- Attempt gagal yang sudah ada sebelum file ini.
INSERT INTO migration_history (execution_id, db_id, migration_id, version, revision, title, kind,
                               sql_text, executed_by, executed_by_username, started_at, committed_at,
                               reverts_execution_id, status, error_message)
SELECT r.id, m.db_id, m.id, m.version, r.revision, m.title, r.kind,
       r.sql_text, r.executed_by, COALESCE(u.username, ''), r.started_at,
       COALESCE(r.finished_at, r.started_at), r.reverts_run_id, 'failed', r.error_message
FROM migration_runs r
JOIN migrations m ON m.id = r.migration_id
LEFT JOIN users u ON u.id = r.executed_by
WHERE r.status = 'failed'
  AND NOT EXISTS (SELECT 1 FROM migration_history h WHERE h.execution_id = r.id);
