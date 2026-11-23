import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { ChangeDetail } from '../../api/types';
import { useAuth } from '../../auth/AuthContext';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';

const sqlBox = {
  overflow: 'auto',
  maxHeight: '24rem',
  whiteSpace: 'pre',
  padding: '0.75rem',
  borderRadius: '6px',
  border: '1px solid var(--border-color)',
  backgroundColor: 'var(--bg-primary)',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: '0.8rem',
} as const;

export const ChangeDetailPage = () => {
  const { dbID, requestID } = useParams();
  const { user } = useAuth();
  const base = `/api/databases/${dbID}/requests/${requestID}`;
  const { data, error, loading, reload } = useLoad(() => apiFetch<ChangeDetail>(base), [base]);
  const [reason, setReason] = useState('');
  const [rejecting, setRejecting] = useState(false);
  const action = useAction(reload);

  if (loading && !data) return <Loading />;
  if (!data) return <ErrorBanner message={error} />;

  const reviewing = data.status === 'pending_review' || data.status === 'approved';
  const mine = data.approvals.filter((a) => a.leader_id === user?.id);
  const canApprove = data.status === 'pending_review' && !data.stale && mine.some((a) => a.status === 'pending');
  const canReject = reviewing && mine.some((a) => a.status !== 'rejected');
  const canCancel = data.status === 'draft' || reviewing;
  const canRun = data.status === 'approved' && !data.stale;
  const canRerun = data.status === 'queued' && data.job_status === 'failed' && data.kind !== 'rollback';

  const post = (suffix: string, json?: unknown, after: () => void = reload) =>
    action.run(() => apiFetch(`${base}/${suffix}`, { method: 'POST', json }), after);

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}/changes`}>Perubahan</Link>
        <span>/</span>
        <span>#{data.id}</span>
      </div>
      <ErrorBanner message={action.error} />
      {error && <ErrorBanner message={error} />}

      <div className="card">
        <h2 className="h2">
          Request #{data.id}: {data.kind} migration v{data.version}
        </h2>
        <p style={{ fontWeight: 600 }}>{data.title}</p>
        <p className="muted">
          Status: <strong>{data.status}</strong> &bull; Pengaju: {data.requested_by} &bull; Dibuat{' '}
          {fmtTime(data.created_at)}
        </p>
        {data.stale && (
          <div className="error-banner">
            SQL migration sudah berubah sejak request ini dibuat. Buat request baru.
          </div>
        )}
        {data.reject_reason && <div className="error-banner">Ditolak: {data.reject_reason}</div>}
        {data.job_id && (
          <p>
            <Link to={`/databases/${dbID}/jobs/${data.job_id}`}>Job #{data.job_id}</Link>: <strong>{data.job_status}</strong>
          </p>
        )}
        {canRerun && (
          <div className="error-banner">
            Eksekusi terakhir gagal. Approval masih berlaku, jadi request ini bisa dijalankan ulang tanpa approval baru
            selama SQL migration tidak berubah.
          </div>
        )}
        <div className="row" style={{ marginTop: '1rem' }}>
          {data.status === 'draft' && (
            <button className="btn btn-primary" disabled={action.busy} onClick={() => post('submit')}>
              Ajukan untuk review
            </button>
          )}
          {canApprove && (
            <button className="btn btn-primary" disabled={action.busy} onClick={() => post('approve')}>
              Approve
            </button>
          )}
          {canReject && (
            <button className="btn btn-danger" disabled={action.busy} onClick={() => setRejecting(!rejecting)}>
              Tolak
            </button>
          )}
          {canCancel && (
            <button className="btn btn-secondary" disabled={action.busy} onClick={() => post('cancel')}>
              Batalkan request
            </button>
          )}
          {canRun && (
            <button className="btn btn-primary" disabled={action.busy} onClick={() => post('enqueue')}>
              Jalankan
            </button>
          )}
          {canRerun && (
            <button className="btn btn-primary" disabled={action.busy} onClick={() => post('enqueue')}>
              Jalankan ulang
            </button>
          )}
        </div>
        {rejecting && canReject && (
          <div className="row" style={{ marginTop: '1rem' }}>
            <input
              className="input"
              style={{ flex: 1 }}
              aria-label="Alasan penolakan"
              maxLength={500}
              placeholder="Alasan penolakan"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
            <button
              className="btn btn-danger"
              disabled={action.busy || reason.trim() === ''}
              onClick={() =>
                post('reject', { reason }, () => {
                  setReason('');
                  setRejecting(false);
                  reload();
                })
              }
            >
              Kirim penolakan
            </button>
          </div>
        )}
      </div>

      <div className="card">
        <h2 className="h2">SQL yang akan dijalankan</h2>
        <pre style={sqlBox}>{data.sql}</pre>
        {data.target_sql && (
          <>
            <h2 className="h2" style={{ marginTop: '1rem' }}>
              SQL eksekusi target #{data.target_execution_id}
            </h2>
            <pre style={sqlBox}>{data.target_sql}</pre>
          </>
        )}
      </div>

      <div className="card">
        <h2 className="h2">Approval per group</h2>
        {data.approvals.length === 0 ? (
          <p className="muted">Belum ada approval. Ajukan request terlebih dahulu.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Group</th>
                  <th>Leader</th>
                  <th>Status</th>
                  <th>Diputuskan oleh</th>
                  <th>Waktu</th>
                </tr>
              </thead>
              <tbody>
                {data.approvals.map((a) => (
                  <tr key={a.group_id}>
                    <td>{a.group_name}</td>
                    <td>{a.leader || <span className="muted">Belum ada leader</span>}</td>
                    <td>{a.status}</td>
                    <td>{a.decided_by || '-'}</td>
                    <td>{fmtTime(a.decided_at)}</td>
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