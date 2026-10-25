CREATE TABLE IF NOT EXISTS mahasiswa (
    nim CHAR(8) PRIMARY KEY,
    nama VARCHAR(255) NOT NULL,
    angkatan SMALLINT NOT NULL,
    ipk DECIMAL(3,2)
);

CREATE TABLE IF NOT EXISTS mata_kuliah (
    kode CHAR(6) PRIMARY KEY,
    nama VARCHAR(255) NOT NULL,
    sks SMALLINT NOT NULL
);

CREATE TABLE IF NOT EXISTS krs (
    nim CHAR(8) NOT NULL,
    kode_mk CHAR(6) NOT NULL,
    semester VARCHAR(50) NOT NULL,
    nilai CHAR(2),
    PRIMARY KEY (nim, kode_mk, semester),
    CONSTRAINT fk_krs_mahasiswa FOREIGN KEY (nim) REFERENCES mahasiswa(nim) ON DELETE CASCADE,
    CONSTRAINT fk_krs_mk FOREIGN KEY (kode_mk) REFERENCES mata_kuliah(kode)
);

CREATE INDEX krs_kode_idx ON krs (kode_mk);

INSERT IGNORE INTO mata_kuliah VALUES
  ('IF3110', 'Pengembangan Aplikasi Web', 3),
  ('IF3140', 'Manajemen Basis Data', 3),
  ('IF3130', 'Jaringan Komputer', 3);

INSERT IGNORE INTO mahasiswa (nim, nama, angkatan, ipk) VALUES
  ('13524001', 'Mahasiswa 1', 2023, 3.50),
  ('13524002', 'Mahasiswa 2', 2023, 3.75),
  ('13524003', 'Mahasiswa 3', 2023, 3.20);

INSERT IGNORE INTO krs (nim, kode_mk, semester, nilai) VALUES
  ('13524001', 'IF3110', '2026-1', 'A'),
  ('13524002', 'IF3110', '2026-1', 'B'),
  ('13524003', 'IF3140', '2026-1', 'A');
