import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch, DatabaseShowResponse } from '../../api/client';

export const DatabaseShowPage: React.FC = () => {
  const { dbID } = useParams<{ dbID: string }>();
  const [data, setData] = useState<DatabaseShowResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');

  useEffect(() => {
    if (!dbID) return;
    setLoading(true);
    apiFetch<DatabaseShowResponse>(`/api/databases/${dbID}`)
      .then((res) => {
        setData(res);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message || 'Gagal memuat detail database');
        setLoading(false);
      });
  }, [dbID]);

  if (loading) {
    return <div className="loading">Memuat ringkasan schema...</div>;
  }

  if (error || !data) {
    return (
      <div>
        <div className="breadcrumbs">
          <Link to="/databases">← Kembali ke Daftar Database</Link>
        </div>
        <div className="error-banner">
          <strong>Error:</strong> {error || 'Data database tidak ditemukan'}
        </div>
      </div>
    );
  }

  const { db, schemas } = data;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <span>{db.name}</span>
      </div>

      <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.5rem', fontWeight: 700 }}>{db.name}</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem' }}>
            ID: {db.id} &bull; Akses: <span className={`badge ${db.level === 'edit' ? 'badge-edit' : 'badge-view'}`}>{db.level === 'edit' ? 'View + Edit' : 'View Only'}</span>
          </p>
        </div>
        <div>
          <input
            type="text"
            placeholder="Cari tabel..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
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

      {schemas.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', color: 'var(--text-muted)' }}>
          Tidak ada tabel yang ditemukan di database ini.
        </div>
      ) : (
        schemas.map((sg) => {
          const matchingTables = sg.tables.filter((t) =>
            t.name.toLowerCase().includes(search.toLowerCase())
          );
          if (matchingTables.length === 0 && search !== '') return null;

          return (
            <div key={sg.schema} className="card">
              <h2 style={{ fontSize: '1.15rem', fontWeight: 600, marginBottom: '0.75rem', color: 'var(--accent-blue)' }}>
                Schema: {sg.schema}
              </h2>
              <div className="table-responsive-wrapper">
                <table>
                  <thead>
                    <tr>
                      <th>Nama Tabel / View</th>
                      <th>Tipe Relasi</th>
                      <th style={{ textAlign: 'right' }}>Aksi</th>
                    </tr>
                  </thead>
                  <tbody>
                    {matchingTables.map((t) => (
                      <tr key={t.name}>
                        <td style={{ fontWeight: 500 }}>
                          <Link to={`/databases/${db.id}/tables/${encodeURIComponent(sg.schema)}/${encodeURIComponent(t.name)}`}>
                            {t.name}
                          </Link>
                        </td>
                        <td>
                          <span className="badge" style={{ backgroundColor: 'var(--border-color)', color: 'var(--text-secondary)' }}>
                            {t.kind}
                          </span>
                        </td>
                        <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                          <Link
                            to={`/databases/${db.id}/tables/${encodeURIComponent(sg.schema)}/${encodeURIComponent(t.name)}`}
                            className="btn btn-secondary"
                            style={{ padding: '0.25rem 0.5rem', fontSize: '0.75rem', marginRight: '0.5rem' }}
                          >
                            Struktur
                          </Link>
                          <Link
                            to={`/databases/${db.id}/tables/${encodeURIComponent(sg.schema)}/${encodeURIComponent(t.name)}/data`}
                            className="btn btn-primary"
                            style={{ padding: '0.25rem 0.5rem', fontSize: '0.75rem' }}
                          >
                            Lihat Data
                          </Link>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          );
        })
      )}
    </div>
  );
};
