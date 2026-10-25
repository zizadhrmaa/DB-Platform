-- Menandai apakah user sudah admin saat ID session ini dibuat. Kalau user
-- kemudian dijadikan admin, middleware session melihat perbedaannya dan
-- langsung merotasi ID.
ALTER TABLE sessions ADD COLUMN privileged BOOLEAN NOT NULL DEFAULT FALSE;
