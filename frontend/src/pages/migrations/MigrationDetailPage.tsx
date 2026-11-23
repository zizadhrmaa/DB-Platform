import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { ChangeDetail, ExecutionKind, MigrationDetail, MigrationDraft } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';
import { DraftForm, sqlBox, statusLabel, useCanEdit } from './shared';
import { ExplainPanel } from './tools';

export const MigrationDetailPage = () => {
  const { dbID, migrationID } = useParams();
  const navigate = useNavigate();
  const base = `/api/databases/${dbID}/migrations/${migrationID}`;
  const { canEdit } = useCanEdit(dbID);
  const { data, error, loading, reload } = useLoad(() => apiFetch<MigrationDetail>(base), [base]);
  const [editing, setEditing] = useState(false);
  const action = useAction(reload);

  if (loading && !data) return <Loading />;
  if (!data) return <ErrorBanner message={error} />;
  const m = data.migration;

  const editable = m.status === 'pending' || m.status === 'failed';
  const discardable = editable || m.status === 'rolled_back';
  const requestKinds: ExecutionKind[] = [];
  if (editable) requestKinds.push('up');
  if (m.exec_mode === 'normal' && m.status === 'success' && !m.irreversible) requestKinds.push('rollback');
  if (m.exec_mode === 'normal' && m.status === 'rolled_back') requestKinds.push('forward');
  // Batch committed dibatalkan lewat aksi revert pada job: butuh request rollback yang disetujui.
  if (m.exec_mode === 'batch' && m.status === 'failed' && !m.irreversible) requestKinds.push('rollback');

  const save = (draft: MigrationDraft) =>
    action.run(
      () => apiFetch(base, { method: 'PATCH', json: draft }),
      () => {
        setEditing(false);
        reload();
      },
    );

  const discard = () => {
    if (!window.confirm('Batalkan migration ini? Riwayat eksekusinya tetap tersimpan.')) return;
    action.run(() => apiFetch(`${base}/discard`, { method: 'POST' }));
  };

  const request = (kind: ExecutionKind) =>
    action.run(async () => {
      const json: Record<string, unknown> = { kind };
      if (kind === 'rollback' && data.latest_applied_execution_id) {
        json.target_execution_id = data.latest_applied_execution_id;
      }
      const created = await apiFetch<ChangeDetail>(`${base}/requests`, { method: 'POST', json });
      navigate(`/databases/${dbID}/changes/${created.id}`);
    });

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}/migrations`}>Migration</Link>
        <span>/</span>
        <span>v{m.version}</span>
      </div>
      <ErrorBanner message={action.error} />
      {error && <ErrorBanner message={error} />}

      <div className="card">
        <h2 className="h2">
          v{m.version} {m.title}
        </h2>
        <p className="muted">
          Status: <strong>{statusLabel[m.status] ?? m.status}</strong> &bull; Revisi {m.revision} &bull; Dibuat oleh{' '}
          {m.created_by} ({fmtTime(m.created_at)})
        </p>
        {m.exec_mode === 'batch' && (
          <p>
            <strong>Mode batch:</strong> tabel {m.batch_table}, kolom {m.batch_column}, {m.batch_size} row per batch.
          </p>
        )}
        {m.irreversible && (
          <p>
            <strong>Irreversible:</strong> {m.irreversible_reason}
          </p>
        )}
        {m.error_message && <ErrorBanner message={`Error terakhir: ${m.error_message}`} />}
        {canEdit && (
          <div className="row" style={{ marginTop: '0.75rem' }}>
            {requestKinds.map((k) => (
              <button key={k} className="btn btn-primary" type="button" disabled={action.busy} onClick={() => request(k)}>
                Ajukan {k}
              </button>
            ))}
            {editable && !editing && (
              <button className="btn btn-secondary" type="button" onClick={() => setEditing(true)}>
                Edit
              </button>
            )}
            {discardable && (
              <button className="btn btn-danger" type="button" disabled={action.busy} onClick={discard}>
                Discard
              </button>
            )}
          </div>
        )}
      </div>

      {editing ? (
        <div className="card">
          <h2 className="h2">Edit migration</h2>
          <DraftForm
            initial={{
              title: m.title,
              sql_up: m.sql,
              sql_down: m.sql_down,
              irreversible: m.irreversible,
              irreversible_reason: m.irreversible_reason,
              exec_mode: m.exec_mode,
              batch_table: m.batch_table,
              batch_column: m.batch_column || 'id',
              batch_size: m.batch_size || 1000,
            }}
            submitLabel="Simpan revisi"
            busy={action.busy}
            onSubmit={save}
            onCancel={() => setEditing(false)}
          />
        </div>
      ) : (
        <div className="card">
          <h2 className="h2">SQL up</h2>
          <pre style={sqlBox}>{m.sql}</pre>
          {!m.irreversible && (
            <>
              <h2 className="h2" style={{ marginTop: '1rem' }}>
                SQL down
              </h2>
              <pre style={sqlBox}>{m.sql_down}</pre>
            </>
          )}
        </div>
      )}

      {canEdit && m.exec_mode === 'normal' && editable && <ExplainPanel base={base} />}

      <div className="card">
        <h2 className="h2">Eksekusi</h2>
        {data.executions.length === 0 ? (
          <p className="muted">Belum pernah dieksekusi.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Execution ID</th>
                  <th>Jenis</th>
                  <th>Status</th>
                  <th>Revisi</th>
                  <th>Oleh</th>
                  <th>Mulai</th>
                  <th>Selesai</th>
                  <th>Keterangan</th>
                </tr>
              </thead>
              <tbody>
                {data.executions.map((x) => (
                  <tr key={x.execution_id}>
                    <td>E{x.execution_id}</td>
                    <td>{x.kind}</td>
                    <td>{x.status}</td>
                    <td>{x.revision}</td>
                    <td>{x.executed_by}</td>
                    <td>{fmtTime(x.started_at)}</td>
                    <td>{fmtTime(x.finished_at)}</td>
                    <td>
                      {x.reverts_execution_id ? `membatalkan E${x.reverts_execution_id}` : ''}
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
