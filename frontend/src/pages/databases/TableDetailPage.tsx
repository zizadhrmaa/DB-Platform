import React, { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch, TableDetailResponse } from '../../api/client';

export const TableDetailPage: React.FC = () => {
  const { dbID, schema, table } = useParams<{ dbID: string; schema: string; table: string }>();
  const [data, setData] = useState<TableDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!dbID || !schema || !table) return;
    setLoading(true);
    apiFetch<TableDetailResponse>(`/api/databases/${dbID}/tables/${encodeURIComponent(schema)}/${encodeURIComponent(table)}`)
      .then((res) => {
        setData(res);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message || 'Gagal memuat struktur tabel');
        setLoading(false);
      });
  }, [dbID, schema, table]);

  if (loading) {
    return <div className="loading">Memuat struktur tabel...</div>;
  }

  if (error || !data) {
    return (
      <div>
        <div className="breadcrumbs">
          <Link to={`/databases/${dbID}`}>← Kembali ke Database</Link>
        </div>
        <div className="error-banner">
          <strong>Error:</strong> {error || 'Tabel tidak ditemukan'}
        </div>
      </div>
    );
  }

  const { db, table: t } = data;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${db.id}`}>{db.name}</Link>
        <span>/</span>
        <span>{t.schema}.{t.name}</span>
      </div>

      <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.5rem', fontWeight: 700 }}>
            {t.schema}.{t.name}
          </h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem' }}>
            Tipe: {t.kind} &bull; Akses: <span className={`badge ${db.level === 'edit' ? 'badge-edit' : 'badge-view'}`}>{db.level}</span>
          </p>
        </div>
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(t.schema)}/${encodeURIComponent(t.name)}/data`}
          className="btn btn-primary"
        >
          Buka Data Tabel &rarr;
        </Link>
      </div>

      <div className="tabs">
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(t.schema)}/${encodeURIComponent(t.name)}`}
          className="tab-link active"
        >
          Struktur (Kolom &amp; Indeks)
        </Link>
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(t.schema)}/${encodeURIComponent(t.name)}/data`}
          className="tab-link"
        >
          Data
        </Link>
      </div>

      {/* Kolom */}
      <div className="card">
        <h2 style={{ fontSize: '1.15rem', fontWeight: 600, marginBottom: '0.75rem' }}>Kolom ({t.columns.length})</h2>
        <div className="table-responsive-wrapper">
          <table>
            <thead>
              <tr>
                <th>Nama Kolom</th>
                <th>Tipe Data</th>
                <th>Nullable</th>
                <th>Nilai Default</th>
              </tr>
            </thead>
            <tbody>
              {t.columns.map((c) => (
                <tr key={c.name}>
                  <td style={{ fontWeight: 600 }}>{c.name}</td>
                  <td><code>{c.type}</code></td>
                  <td>{c.nullable ? 'YES' : 'NO'}</td>
                  <td>{c.default ? <code>{c.default}</code> : <span className="cell-null">null</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Constraints */}
      <div className="card">
        <h2 style={{ fontSize: '1.15rem', fontWeight: 600, marginBottom: '0.75rem' }}>Constraints ({t.constraints.length})</h2>
        {t.constraints.length === 0 ? (
          <p style={{ color: 'var(--text-muted)' }}>Tidak ada constraint.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Nama</th>
                  <th>Tipe</th>
                  <th>Definisi</th>
                  <th>Referensi</th>
                </tr>
              </thead>
              <tbody>
                {t.constraints.map((cn) => (
                  <tr key={cn.name}>
                    <td style={{ fontWeight: 600 }}>{cn.name}</td>
                    <td><span className="badge badge-view">{cn.type}</span></td>
                    <td><code>{cn.definition}</code></td>
                    <td>{cn.references ? <code>{cn.references}</code> : '-'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Indexes */}
      <div className="card">
        <h2 style={{ fontSize: '1.15rem', fontWeight: 600, marginBottom: '0.75rem' }}>Indexes ({t.indexes.length})</h2>
        {t.indexes.length === 0 ? (
          <p style={{ color: 'var(--text-muted)' }}>Tidak ada index tambahan.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Nama Index</th>
                  <th>Unique</th>
                  <th>Primary</th>
                  <th>Definisi</th>
                </tr>
              </thead>
              <tbody>
                {t.indexes.map((idx) => (
                  <tr key={idx.name}>
                    <td style={{ fontWeight: 600 }}>{idx.name}</td>
                    <td>{idx.unique ? 'YES' : 'NO'}</td>
                    <td>{idx.primary ? 'YES' : 'NO'}</td>
                    <td><code>{idx.definition}</code></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
};
