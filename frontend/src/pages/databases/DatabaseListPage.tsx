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

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem', flexWrap: 'wrap', gap: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.75rem', fontWeight: 700 }}>Daftar Database</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
            Pilih database untuk melihat ringkasan schema, struktur tabel, dan isi data.
          </p>
        </div>
        <div>
          <input
            type="text"
            placeholder="Cari database..."
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{
              padding: '0.5rem 0.75rem',
              borderRadius: '6px',
              border: '1px solid var(--border-color)',
              backgroundColor: 'var(--bg-secondary)',
              color: 'var(--text-primary)',
              fontSize: '0.875rem',
            }}
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', color: 'var(--text-muted)' }}>
          {databases.length === 0 ? 'Belum ada database yang dapat diakses.' : 'Tidak ada database yang cocok.'}
        </div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' }}>
          {filtered.map((db) => (
            <div key={db.id} className="card" style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
              <div>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '0.75rem' }}>
                  <h2 style={{ fontSize: '1.2rem', fontWeight: 600 }}>{db.name}</h2>
                  <span className={`badge badge-${db.db_type}`}>{db.db_type}</span>
                </div>
                <div style={{ marginBottom: '1rem' }}>
                  <span className={`badge ${db.level === 'edit' ? 'badge-edit' : 'badge-view'}`}>
                    {db.level === 'edit' ? 'View + Edit' : 'View Only'}
                  </span>
                </div>
              </div>
              <Link to={`/databases/${db.id}`} className="btn btn-primary" style={{ width: '100%' }}>
                Buka Schema Viewer
              </Link>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};
