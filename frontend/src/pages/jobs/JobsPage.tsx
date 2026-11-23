import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Job, JobStatus } from '../../api/types';
import { terminalJobStatuses } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useLoad } from '../../hooks';

const statuses: JobStatus[] = ['queued', 'running', 'uncertain', 'partial', 'partially_reverted', 'succeeded', 'failed', 'reverted'];

// usePolling memanggil reload setiap 2 detik selama active dan tab terlihat.
export function usePolling(active: boolean, reload: () => void) {
  useEffect(() => {
    if (!active) return;
    const tick = () => {
      if (document.visibilityState === 'visible') reload();
    };
    const id = window.setInterval(tick, 2000);
    return () => window.clearInterval(id);
  }, [active, reload]);
}

export const JobsPage = () => {
  const { dbID } = useParams();
  const [status, setStatus] = useState('');
  const { data, error, loading, reload } = useLoad(
    () => apiFetch<Job[]>(`/api/databases/${dbID}/jobs${status ? `?status=${status}` : ''}`),
    [dbID, status],
  );
  usePolling(!!data && data.some((j) => !terminalJobStatuses.includes(j.status)), reload);

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}`}>Schema</Link>
        <span>/</span>
        <span>Job</span>
      </div>
      <ErrorBanner message={error} />
      <div className="card">
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 className="h2" style={{ margin: 0 }}>
            Job eksekusi migration
          </h2>
          <select className="input" style={{ maxWidth: '14rem' }} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">Semua status</option>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
        {loading && !data ? (
          <Loading />
        ) : data && data.length === 0 ? (
          <p className="muted">Belum ada job.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Job</th>
                  <th>Migration</th>
                  <th>Jenis</th>
                  <th>Status</th>
                  <th>Attempt</th>
                  <th>Progres</th>
                  <th>Dibuat</th>
                </tr>
              </thead>
              <tbody>
                {(data || []).map((j) => (
                  <tr key={j.id}>
                    <td>
                      <Link to={`/databases/${dbID}/jobs/${j.id}`}>#{j.id}</Link>
                    </td>
                    <td>
                      <Link to={`/databases/${dbID}/migrations/${j.migration_id}`}>#{j.migration_id}</Link>
                    </td>
                    <td>
                      {j.kind}
                      {j.mode === 'batch' ? ` (batch${j.action === 'revert' ? ', revert' : ''})` : ''}
                    </td>
                    <td>{j.status}</td>
                    <td>
                      {j.attempt}/{j.max_attempts}
                    </td>
                    <td>{j.mode === 'batch' ? `${j.batches_committed} batch / ${j.rows_committed} row` : '-'}</td>
                    <td>{fmtTime(j.created_at)}</td>
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
