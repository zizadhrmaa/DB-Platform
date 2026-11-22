ALTER TABLE jobs ADD CONSTRAINT jobs_request_fk
    FOREIGN KEY (request_id) REFERENCES change_requests(id) ON DELETE SET NULL;