import React, { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { apiFetch, DatabaseSummary } from '../../api/client';

export const DatabaseListPage: React.FC = () => {
  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    apiFetch<DatabaseSummary[]>('/api/databases')
      .then((data) => {
        setDatabases(data || []);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message || 'Gagal memuat daftar database');
        setLoading(false);
      });
  }, []);

  const filtered = databases.filter((db) =>
    db.name.toLowerCase().includes(filter.toLowerCase()) ||
    db.db_type.toLowerCase().includes(filter.toLowerCase())
  );

  if (loading) {
    return <div className="loading">Memuat database...</div>;
  }

  if (error) {
    return (
      <div className="error-banner">
        <strong>Error:</strong> {error}
      </div>
    );
  }

  // Tampilan mengikuti halaman daftar database Milestone 1
  // (web/templates/pages/databases/list.html).
  return (
    <section className="peach-workspace">
      <div className="peach-hero">
        <div>
          <p className="peach-eyebrow">✿ PENGELOLAAN DATABASE</p>
          <h1>Selamat Datang di DB Platform</h1>
          <p className="peach-description">
            Kelola database, lihat struktur tabel, dan jalankan migrasi melalui satu platform yang terintegrasi.
          </p>
        </div>
        <div className="peach-hero-icon" aria-hidden="true">
          ✿
        </div>
      </div>

      {databases.length === 0 ? (
        <div className="peach-empty-state">
          <div aria-hidden="true">✿</div>
          <h2>Belum Ada Database</h2>
          <p>
            Anda belum memiliki akses ke database. Silakan hubungi Platform Admin untuk mendapatkan hak akses yang
            sesuai.
          </p>
        </div>
      ) : (
        <>
          <div className="peach-section-heading">
            <div>
              <h2>Daftar Database</h2>
              <p>Database yang dapat Anda akses sesuai hak akses yang diberikan.</p>
            </div>
            <input
              className="input"
              type="search"
              placeholder="Cari database..."
              aria-label="Cari database"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </div>

          {filtered.length === 0 ? (
            <div className="peach-empty-state">
              <p>Tidak ada database yang cocok dengan pencarian.</p>
            </div>
          ) : (
            <div className="peach-db-grid">
              {filtered.map((db) => (
                <article key={db.id} className="peach-db-card">
                  <div className="peach-card-top">
                    <div className="peach-db-icon" aria-hidden="true">
                      ▤
                    </div>
                    <div className="peach-card-badges">
                      <span className={`badge ${db.level === 'edit' ? 'badge-edit' : 'badge-view'}`}>
                        {db.level === 'edit' ? 'View + Edit' : 'View Only'}
                      </span>
                      <span className={`badge badge-${db.db_type}`}>
                        {db.db_type === 'mysql' ? 'MySQL' : 'PostgreSQL'}
                      </span>
                    </div>
                  </div>
                  <div className="peach-card-info">
                    <h3>{db.name}</h3>
                    <p>Database {db.db_type === 'mysql' ? 'MySQL' : 'PostgreSQL'}</p>
                  </div>
                  <Link className="peach-open-button" to={`/databases/${db.id}`}>
                    Buka Database <span aria-hidden="true">→</span>
                  </Link>
                </article>
              ))}
              <aside className="peach-decoration-card">
                <div className="peach-decoration-icon" aria-hidden="true">
                  ✿
                </div>
                <h3>Pengelolaan Terintegrasi</h3>
                <p>Akses informasi tabel, kelola hak akses, dan tinjau riwayat migrasi database.</p>
              </aside>
            </div>
          )}
        </>
      )}
    </section>
  );
};
