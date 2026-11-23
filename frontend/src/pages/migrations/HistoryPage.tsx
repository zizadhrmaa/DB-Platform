import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Execution } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useLoad } from '../../hooks';
import { sqlBox, useCanEdit } from './shared';

// HistoryPage menampilkan riwayat eksekusi migration. Hide rollback hanya
// menyembunyikan pasangan eksekusi yang dibatalkan dan rollback suksesnya
// (berdasarkan execution ID); data riwayat tidak berubah, dan kegagalan
// tetap terlihat.
export const HistoryPage = () => {
  const { dbID } = useParams();
  const { dbName } = useCanEdit(dbID);
  const [hide, setHide] = useState(false);
  const { data, error, loading } = useLoad(
    () => apiFetch<Execution[]>(`/api/databases/${dbID}/history?hide_rollback=${hide}`),
    [dbID, hide],
  );

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}`}>{dbName || 'Schema'}</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}/migrations`}>Migration</Link>
        <span>/</span>
        <span>Riwayat</span>
      </div>
      <ErrorBanner message={error} />

      <div className="card">
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 className="h2" style={{ margin: 0 }}>
            Riwayat eksekusi
          </h2>
          <label style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
            <input type="checkbox" checked={hide} onChange={(e) => setHide(e.target.checked)} />
            Hide rollback
          </label>
        </div>
        {loading && !data ? (
          <Loading />
        ) : data && data.length === 0 ? (
          <p className="muted">Belum ada eksekusi.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Execution ID</th>
                  <th>Migration</th>
                  <th>Jenis</th>
                  <th>Status</th>
                  <th>Revisi</th>
                  <th>Oleh</th>
                  <th>Waktu</th>
                  <th>Keterangan</th>
                </tr>
              </thead>
              <tbody>
                {(data || []).map((x) => (
                  <tr key={x.execution_id}>
                    <td>E{x.execution_id}</td>
                    <td>
                      <Link to={`/databases/${dbID}/migrations/${x.migration_id}`}>
                        v{x.version} {x.title}
                      </Link>
                    </td>
                    <td>{x.kind === 'rollback' && x.reverts_execution_id ? `rollback E${x.reverts_execution_id}` : x.kind}</td>
                    <td>{x.status === 'success' ? 'committed' : x.status}</td>
                    <td>{x.revision}</td>
                    <td>{x.executed_by}</td>
                    <td>{fmtTime(x.started_at)}</td>
                    <td>
                      {x.error_message}
                      <details>
                        <summary>SQL</summary>
                        <pre style={sqlBox}>{x.sql}</pre>
                      </details>
                    </td>
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
