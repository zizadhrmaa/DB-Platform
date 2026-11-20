import React, { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { apiFetch, TableDataResponse } from '../../api/client';

export const TableDataPage: React.FC = () => {
  const { dbID, schema, table } = useParams<{ dbID: string; schema: string; table: string }>();
  const [searchParams, setSearchParams] = useSearchParams();

  const page = parseInt(searchParams.get('page') || '1', 10);
  const size = parseInt(searchParams.get('size') || '50', 10);

  const [data, setData] = useState<TableDataResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!dbID || !schema || !table) return;
    setLoading(true);
    apiFetch<TableDataResponse>(
      `/api/databases/${dbID}/tables/${encodeURIComponent(schema)}/${encodeURIComponent(table)}/data?page=${page}&size=${size}`
    )
      .then((res) => {
        setData(res);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message || 'Gagal memuat data tabel');
        setLoading(false);
      });
  }, [dbID, schema, table, page, size]);

  const handlePageChange = (newPage: number) => {
    setSearchParams({ page: newPage.toString(), size: size.toString() });
  };

  const handleSizeChange = (newSize: number) => {
    setSearchParams({ page: '1', size: newSize.toString() });
  };

  if (loading) {
    return <div className="loading">Memuat baris data...</div>;
  }

  if (error || !data) {
    return (
      <div>
        <div className="breadcrumbs">
          <Link to={`/databases/${dbID}`}>← Kembali ke Database</Link>
        </div>
        <div className="error-banner">
          <strong>Error:</strong> {error || 'Data tabel tidak dapat diambil'}
        </div>
      </div>
    );
  }

  const { db, page: p } = data;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${db.id}`}>{db.name}</Link>
        <span>/</span>
        <span>{p.schema}.{p.name}</span>
      </div>

      <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.5rem', fontWeight: 700 }}>
            {p.schema}.{p.name}
          </h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem' }}>
            Total baris: <strong>{p.total.toLocaleString()}</strong>
            {p.total_estimate ? ' (estimasi statistik)' : ''} &bull; Urutan: {p.ordered_by || 'tanpa jaminan'}
          </p>
        </div>
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(p.schema)}/${encodeURIComponent(p.name)}`}
          className="btn btn-secondary"
        >
          Lihat Struktur Kolom
        </Link>
      </div>

      <div className="tabs">
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(p.schema)}/${encodeURIComponent(p.name)}`}
          className="tab-link"
        >
          Struktur
        </Link>
        <Link
          to={`/databases/${db.id}/tables/${encodeURIComponent(p.schema)}/${encodeURIComponent(p.name)}/data`}
          className="tab-link active"
        >
          Data ({p.total.toLocaleString()})
        </Link>
      </div>

      {/* Kontrol Pagination Atas */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', flexWrap: 'wrap', gap: '0.75rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <label htmlFor="sizeSelect" style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
            Baris per halaman:
          </label>
          <select
            id="sizeSelect"
            value={p.size}
            onChange={(e) => handleSizeChange(parseInt(e.target.value, 10))}
            style={{
              padding: '0.25rem 0.5rem',
              borderRadius: '4px',
              border: '1px solid var(--border-color)',
              backgroundColor: 'var(--bg-secondary)',
              color: 'var(--text-primary)',
              fontSize: '0.875rem',
            }}
          >
            <option value="10">10</option>
            <option value="25">25</option>
            <option value="50">50</option>
            <option value="100">100</option>
            <option value="200">200</option>
          </select>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <span style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
            Halaman {p.page} dari {p.page_count}
          </span>
          <button
            className="btn btn-secondary"
            disabled={!p.has_prev}
            onClick={() => handlePageChange(p.page - 1)}
            style={{ padding: '0.25rem 0.5rem', fontSize: '0.75rem' }}
          >
            &larr; Prev
          </button>
          <button
            className="btn btn-secondary"
            disabled={!p.has_next}
            onClick={() => handlePageChange(p.page + 1)}
            style={{ padding: '0.25rem 0.5rem', fontSize: '0.75rem' }}
          >
            Next &rarr;
          </button>
        </div>
      </div>

      {/* Tabel Data (Responsive dengan internal scroll) */}
      <div className="card" style={{ padding: '0' }}>
        <div className="table-responsive-wrapper" style={{ margin: '0', border: 'none' }}>
          <table>
            <thead>
              <tr>
                {p.columns.map((col) => (
                  <th key={col}>{col}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {p.rows.length === 0 ? (
                <tr>
                  <td colSpan={p.columns.length} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                    Tabel ini kosong.
                  </td>
                </tr>
              ) : (
                p.rows.map((row, rIdx) => (
                  <tr key={rIdx}>
                    {row.map((cell, cIdx) => (
                      <td key={cIdx}>
                        {cell.null ? (
                          <span className="cell-null">NULL</span>
                        ) : (
                          <span>
                            {cell.text}
                            {cell.truncated ? <span style={{ color: 'var(--accent-purple)' }}>&hellip;</span> : ''}
                          </span>
                        )}
                      </td>
                    ))}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Kontrol Pagination Bawah */}
      <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', marginTop: '1rem', gap: '0.5rem' }}>
        <button
          className="btn btn-secondary"
          disabled={!p.has_prev}
          onClick={() => handlePageChange(p.page - 1)}
        >
          &larr; Halaman Sebelumnya
        </button>
        <button
          className="btn btn-secondary"
          disabled={!p.has_next}
          onClick={() => handlePageChange(p.page + 1)}
        >
          Halaman Berikutnya &rarr;
        </button>
      </div>
    </div>
  );
};
