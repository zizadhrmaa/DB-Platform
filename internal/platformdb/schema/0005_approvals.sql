ALTER TABLE groups ADD COLUMN leader_user_id BIGINT;

ALTER TABLE groups ADD CONSTRAINT groups_leader_is_member_fk
    FOREIGN KEY (id, leader_user_id) REFERENCES group_members (group_id, user_id)
    ON DELETE SET NULL (leader_user_id);

CREATE TABLE change_requests (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    db_id         BIGINT      NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    migration_id  BIGINT      NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    revision      INTEGER     NOT NULL DEFAULT 1 CHECK (revision > 0),
    kind          TEXT        NOT NULL CHECK (kind IN ('up','rollback','forward')),
    target_run_id BIGINT      REFERENCES migration_runs(id) ON DELETE SET NULL,
    status        TEXT        NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft','pending_review','approved','queued','rejected')),
    requested_by  BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    reject_reason TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at  TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX change_requests_one_open_uq ON change_requests (migration_id)
    WHERE status IN ('draft','pending_review','approved');
CREATE INDEX change_requests_db_idx ON change_requests (db_id, created_at DESC, id DESC);

CREATE TABLE change_request_approvals (
    request_id BIGINT      NOT NULL REFERENCES change_requests(id) ON DELETE CASCADE,
    group_id   BIGINT      NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    status     TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    decided_by BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    PRIMARY KEY (request_id, group_id)
);