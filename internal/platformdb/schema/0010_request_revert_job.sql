-- Request rollback yang dipakai untuk membatalkan batch committed milik job
-- partial atau partially_reverted. Request itu tidak membuat job baru;
-- ia dipasangkan ke job yang sudah ada lewat kolom ini
-- (POST /api/databases/{dbID}/jobs/{jobID}/revert).
ALTER TABLE change_requests
    ADD COLUMN revert_job_id BIGINT REFERENCES jobs(id) ON DELETE SET NULL;

CREATE INDEX change_requests_revert_job_idx ON change_requests (revert_job_id)
    WHERE revert_job_id IS NOT NULL;
