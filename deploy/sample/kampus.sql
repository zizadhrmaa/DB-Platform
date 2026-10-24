-- Database target contoh "kampus".
CREATE TABLE mahasiswa (
    nim       CHAR(8) PRIMARY KEY CHECK (nim ~ '^[0-9]{8}$'),
    nama      TEXT NOT NULL,
    angkatan  SMALLINT NOT NULL CHECK (angkatan BETWEEN 2000 AND 2100),
    ipk       NUMERIC(3,2) CHECK (ipk BETWEEN 0 AND 4)
);

CREATE TABLE mata_kuliah (
    kode  CHAR(6) PRIMARY KEY,
    nama  TEXT NOT NULL,
    sks   SMALLINT NOT NULL CHECK (sks BETWEEN 1 AND 6)
);

CREATE TABLE krs (
    nim       CHAR(8) REFERENCES mahasiswa(nim) ON DELETE CASCADE,
    kode_mk   CHAR(6) REFERENCES mata_kuliah(kode),
    semester  TEXT NOT NULL,
    nilai     CHAR(2),
    PRIMARY KEY (nim, kode_mk, semester)
);
CREATE INDEX krs_kode_idx ON krs (kode_mk);

INSERT INTO mata_kuliah VALUES
  ('IF3110', 'Pengembangan Aplikasi Web', 3),
  ('IF3140', 'Manajemen Basis Data', 3),
  ('IF3130', 'Jaringan Komputer', 3);

INSERT INTO mahasiswa (nim, nama, angkatan, ipk)
SELECT '1352' || lpad(g::text, 4, '0'), 'Mahasiswa ' || g, 2023, round((2 + (g % 200) / 100.0)::numeric, 2)
FROM generate_series(1, 150) g;

INSERT INTO krs (nim, kode_mk, semester, nilai)
SELECT nim, 'IF3110', '2026-1', NULL FROM mahasiswa;

CREATE ROLE platform_app LOGIN PASSWORD 'platform_app_pass';
ALTER TABLE mahasiswa OWNER TO platform_app;
ALTER TABLE mata_kuliah OWNER TO platform_app;
ALTER TABLE krs OWNER TO platform_app;
GRANT USAGE, CREATE ON SCHEMA public TO platform_app;