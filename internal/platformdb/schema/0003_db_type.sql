ALTER TABLE db_connections ADD COLUMN db_type TEXT NOT NULL DEFAULT 'postgres' CHECK (db_type IN ('postgres', 'mysql'));
