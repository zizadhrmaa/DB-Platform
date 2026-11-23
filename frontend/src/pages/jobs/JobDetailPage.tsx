import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { ChangeRequest, Job, Migration } from '../../api/types';
import { terminalJobStatuses } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';
import { useCanEdit } from '../migrations/shared';
import { usePolling } from './JobsPage';

const statusHelp: Record<string, string> = {
  queued: 'Menunggu worker.',
  running: 'Sedang dijalankan worker.',
  succeeded: 'Selesai dan sudah committed.',
  failed: 'Gagal; perubahan tidak masuk.',
  uncertain: 'Hasil commit belum pasti; database ditahan sampai bukti diperiksa.',
  partial: 'Sebagian batch sudah committed; database ditahan. Lanjutkan (resume) atau batalkan (revert).',
  partially_reverted: 'Pembatalan batch berhenti di tengah; database ditahan. Lanjutkan pembatalan.',
  reverted: 'Semua batch committed sudah dibatalkan.',
};

export const JobDetailPage = () => {
  const { dbID, jobID } = useParams();
  const base = `/api/databases/${dbID}/jobs/${jobID}`;
  const { canEdit } = useCanEdit(dbID);
  const { data: job, error, loading, reload } = useLoad(() => apiFetch<Job>(base), [base]);
  const action = useAction(reload);
  const [requestId, setRequestId] = useState('');
  usePolling(!!job && !terminalJobStatuses.includes(job.status), reload);

  const migration = useLoad(
    () => (job ? apiFetch<{ migration: Migration }>(`/api/databases/${dbID}/migrations/${job.migration_id}`) : Promise.resolve(null)),
    [dbID, job?.migration_id],
  );
  const canRevert = !!job && job.mode === 'batch' && (job.status === 'partial' || job.status === 'partially_reverted');
  const rollbacks = useLoad(
    () => (canRevert ? apiFetch<ChangeRequest[]>(`/api/databases/${dbID}/requests?status=approved`) : Promise.resolve([])),
    [dbID, canRevert],
  );

  if (loading && !job) return <Loading />;
  if (!job) return <ErrorBanner message={error} />;

  const m = migration.data?.migration;
  const approvedRollbacks = (rollbacks.data || []).filter((r) => r.kind === 'rollback' && r.migration_id === job.migration_id);
  const canResume = job.mode === 'batch' && (job.status === 'partial' || job.status === 'partially_reverted');

  const post = (suffix: string, json?: unknown) => action.run(() => apiFetch(`${base}/${suffix}`, { method: 'POST', json }));

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}/jobs`}>Job</Link>
        <span>/</span>
        <span>#{job.id}</span>
      </div>
      <ErrorBanner message={action.error} />
      {error && <ErrorBanner message={error} />}

      <div className="card">
        <h2 className="h2">
          Job #{job.id}: {job.kind} {m ? `v${m.version} ${m.title}` : `migration #${job.migration_id}`}
        </h2>
        <p>
          Status: <strong>{job.status}</strong> &mdash; {statusHelp[job.status]}
        </p>
        <p className="muted">
          Mode {job.mode}
          {job.mode === 'batch' ? ` (${job.action})` : ''} &bull; Attempt {job.attempt}/{job.max_attempts}
          {job.execution_id ? ` • Eksekusi E${job.execution_id}` : ''}
          {job.request_id ? (
            <>
              {' '}
              &bull; <Link to={`/databases/${dbID}/changes/${job.request_id}`}>Request #{job.request_id}</Link>
            </>
          ) : null}
        </p>
        {job.error_message && <ErrorBanner message={job.error_message} />}
        <div className="table-responsive-wrapper">
          <table>
            <tbody>
              <tr>
                <th>Dibuat</th>
                <td>{fmtTime(job.created_at)}</td>
              </tr>
              <tr>
                <th>Mulai</th>
                <td>{fmtTime(job.started_at)}</td>
              </tr>
              <tr>
                <th>Selesai</th>
                <td>{fmtTime(job.finished_at)}</td>
              </tr>
              {job.status === 'queued' && (
                <tr>
                  <th>Dijadwalkan</th>
                  <td>{fmtTime(job.next_run_at)}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {job.mode === 'batch' && (
        <div className="card">
          <h2 className="h2">Progres batch</h2>
          <p>
            <strong>{job.batches_committed}</strong> batch committed, <strong>{job.rows_committed}</strong> row diproses
            {job.last_cursor ? ` (cursor ${m?.batch_column ?? ''} = ${job.last_cursor})` : ''}.
          </p>
          {m && m.exec_mode === 'batch' && (
            <p className="muted">
              Tabel {m.batch_table}, {m.batch_size} row per batch.
            </p>
          )}
          {canEdit && canResume && (
            <div className="row" style={{ marginTop: '0.75rem' }}>
              <button className="btn btn-primary" type="button" disabled={action.busy} onClick={() => post('resume')}>
                {job.status === 'partial' ? 'Lanjutkan dari checkpoint' : 'Lanjutkan pembatalan'}
              </button>
            </div>
          )}
          {canEdit && job.status === 'partial' && (
            <div style={{ marginTop: '1rem' }}>
              <h2 className="h2">Batalkan batch committed</h2>
              <p className="muted">
                Butuh request rollback untuk migration ini yang sudah disetujui semua leader. SQL down dijalankan per batch
                dalam urutan terbalik.
              </p>
              {approvedRollbacks.length === 0 ? (
                <p className="muted">
                  Belum ada request rollback yang disetujui.{' '}
                  <Link to={`/databases/${dbID}/migrations/${job.migration_id}`}>Ajukan dari halaman migration</Link>.
                </p>
              ) : (
                <div className="row">
                  <select className="input" style={{ maxWidth: '16rem' }} value={requestId} onChange={(e) => setRequestId(e.target.value)}>
                    <option value="">Pilih request rollback</option>
                    {approvedRollbacks.map((r) => (
                      <option key={r.id} value={r.id}>
                        Request #{r.id}
                      </option>
                    ))}
                  </select>
                  <button
                    className="btn btn-danger"
                    type="button"
                    disabled={action.busy || !requestId}
                    onClick={() => post('revert', { request_id: Number(requestId) })}
                  >
                    Revert batch
                  </button>
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
};
