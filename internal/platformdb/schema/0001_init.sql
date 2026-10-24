-- =====================================================================
-- Schema database internal platform (bukan database target).
-- Dijalankan otomatis saat server start (platformdb.Migrate) dan oleh
-- testutil untuk setiap integration test.
--
-- File yang sudah masuk main jangan diubah. Untuk perubahan, buat file
-- baru 0002_xxx.sql, 0003_xxx.sql, dan seterusnya.
-- =====================================================================

-- ---------------------------------------------------------------------
-- USERS
-- ---------------------------------------------------------------------
CREATE TABLE users (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username          TEXT        NOT NULL,
    -- Format PHC: $argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>
    password_hash     TEXT        NOT NULL,
    is_platform_admin BOOLEAN     NOT NULL DEFAULT FALSE,
    is_active         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_username_format CHECK (username ~ '^[A-Za-z0-9_.-]{3,64}$')
);
-- Username unik tanpa membedakan huruf besar/kecil ("Admin" == "admin").
CREATE UNIQUE INDEX users_username_lower_uq ON users (lower(username));

-- ---------------------------------------------------------------------
-- SESSIONS
-- Yang disimpan hanya SHA-256 dari session ID, jadi kalau tabel ini bocor
-- isinya tidak bisa dipakai untuk membajak session.
--
-- Rotasi: buat baris baru dengan family_id & absolute_expires_at yang
-- sama, lalu baris lama diisi replaced_at + grace_until.
-- Logout: set revoked_at untuk semua baris dengan family_id tersebut.
-- ---------------------------------------------------------------------
CREATE TABLE sessions (
    id_hash             BYTEA       PRIMARY KEY,          -- sha256(session_id)
    family_id           UUID        NOT NULL,             -- sama untuk semua hasil rotasi
    user_id             BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token          TEXT        NOT NULL,             -- synchronizer token, ikut disalin saat rotasi
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(), -- untuk idle timeout
    absolute_expires_at TIMESTAMPTZ NOT NULL,             -- tetap saat rotasi
    replaced_at         TIMESTAMPTZ,                      -- diisi saat ID ini dirotasi
    grace_until         TIMESTAMPTZ,                      -- ID lama masih sah sampai waktu ini
    revoked_at          TIMESTAMPTZ,                      -- logout / pencabutan
    CONSTRAINT sessions_grace_consistency CHECK ((replaced_at IS NULL) = (grace_until IS NULL))
);
CREATE INDEX sessions_user_idx   ON sessions (user_id);
CREATE INDEX sessions_family_idx ON sessions (family_id);

-- ---------------------------------------------------------------------
-- GROUPS & MEMBERSHIP
-- ---------------------------------------------------------------------
CREATE TABLE groups (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT groups_name_not_blank CHECK (btrim(name) <> '')
);
CREATE UNIQUE INDEX groups_name_lower_uq ON groups (lower(name));

CREATE TABLE group_members (
    group_id   BIGINT      NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_idx ON group_members (user_id);

-- ---------------------------------------------------------------------
-- DATABASE CONNECTIONS
-- Password database target disimpan terenkripsi (AES-256-GCM, key dari
-- env APP_SECRET_KEY) lewat dbmanager.SecretBox. Tidak pernah dikirim
-- ke client dan tidak pernah ditulis ke log.
-- ---------------------------------------------------------------------
CREATE TABLE db_connections (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                TEXT        NOT NULL,             -- nama tampilan, mis. "Toko (staging)"
    host                TEXT        NOT NULL,
    port                INTEGER     NOT NULL DEFAULT 5432 CHECK (port BETWEEN 1 AND 65535),
    database_name       TEXT        NOT NULL,
    username            TEXT        NOT NULL,
    password_ciphertext BYTEA       NOT NULL,
    sslmode             TEXT        NOT NULL DEFAULT 'disable'
                        CHECK (sslmode IN ('disable','require','verify-ca','verify-full')),
    is_active           BOOLEAN     NOT NULL DEFAULT TRUE, -- nonaktif = tidak bisa diakses siapa pun
    created_by          BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX db_connections_name_lower_uq ON db_connections (lower(name));

-- ---------------------------------------------------------------------
-- GROUP -> DATABASE ACCESS
-- Akses hanya lewat group. Satu baris per (group, database).
-- Level: 'view' (View Only) atau 'edit' (View + Edit).
-- ---------------------------------------------------------------------
CREATE TABLE group_db_access (
    group_id   BIGINT      NOT NULL REFERENCES groups(id)         ON DELETE CASCADE,
    db_id      BIGINT      NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    level      TEXT        NOT NULL CHECK (level IN ('view','edit')),
    granted_by BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, db_id)
);
CREATE INDEX group_db_access_db_idx ON group_db_access (db_id);

-- ---------------------------------------------------------------------
-- MIGRATIONS
-- Versi diberikan saat migration dibuat: max(version)+1 per database,
-- di dalam transaction yang mengunci baris db_connections terkait
-- (SELECT ... FOR UPDATE) supaya dua pembuatan bersamaan tidak bentrok.
--
-- Status:
--   pending   -> draft, boleh diedit, belum pernah dijalankan
--   running   -> sedang dieksekusi (dikunci)
--   success   -> sudah commit, tidak bisa diubah
--   failed    -> gagal & sudah rollback, boleh diedit lalu dijalankan ulang
--   discarded -> dibatalkan, dilewati dalam urutan, tetap tercatat
-- Urutan linear: yang boleh dieksekusi hanya versi terkecil yang
-- statusnya pending/failed (lihat migration.NextExecutable).
-- ---------------------------------------------------------------------
CREATE TABLE migrations (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    db_id        BIGINT      NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    version      INTEGER     NOT NULL CHECK (version > 0),
    title        TEXT        NOT NULL CHECK (btrim(title) <> ''),
    sql_text     TEXT        NOT NULL CHECK (btrim(sql_text) <> ''),
    status       TEXT        NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','running','success','failed','discarded')),
    created_by   BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by   BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_by  BIGINT      REFERENCES users(id) ON DELETE SET NULL, -- pelaksana terakhir
    executed_at  TIMESTAMPTZ,                                         -- waktu eksekusi terakhir
    finished_at  TIMESTAMPTZ,
    error_message TEXT,                                               -- pesan error PG (tanpa kredensial)
    UNIQUE (db_id, version)
);
CREATE INDEX migrations_db_status_idx ON migrations (db_id, status, version);

-- Setiap percobaan eksekusi dicatat (audit). Migration yang failed lalu
-- diperbaiki dan dijalankan ulang akan punya beberapa baris di sini.
CREATE TABLE migration_runs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    migration_id  BIGINT      NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    sql_text      TEXT        NOT NULL,            -- snapshot SQL yang benar-benar dijalankan
    executed_by   BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ,
    status        TEXT        NOT NULL CHECK (status IN ('running','success','failed')),
    error_message TEXT
);
CREATE INDEX migration_runs_migration_idx ON migration_runs (migration_id, started_at DESC);