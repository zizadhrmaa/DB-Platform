ALTER TABLE change_requests DROP CONSTRAINT change_requests_status_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_status_check
    CHECK (status IN ('draft','pending_review','approved','queued','rejected','cancelled'));