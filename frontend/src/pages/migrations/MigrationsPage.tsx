import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Migration, MigrationDraft } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';
import { DraftForm, emptyDraft, statusLabel, useCanEdit } from './shared';

export const MigrationsPage = () => {
  const { dbID } = useParams();
  const navigate = useNavigate();
  const { canEdit, dbName } = useCanEdit(dbID);
  const { data, error, loading, reload } = useLoad(
    () => apiFetch<Migration[]>(`/api/databases/${dbID}/migrations`),
    [dbID],
  );
  const [creating, setCreating] = useState(false);
  const action = useAction(reload);

  const create = (draft: MigrationDraft) =>
    action.run(async () => {
      const m = await apiFetch<Migration>(`/api/databases/${dbID}/migrations`, { method: 'POST', json: draft });
      navigate(`/databases/${dbID}/migrations/${m.id}`);
    });

  if (loading && !data) return <Loading />;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}`}>{dbName || 'Schema'}</Link>
        <span>/</span>
        <span>Migration</span>
      </div>
      <ErrorBanner message={error} />

      <div className="row" style={{ marginBottom: '1rem' }}>
        <Link className="btn btn-secondary" to={`/databases/${dbID}/history`}>
          Riwayat eksekusi
        </Link>
        <Link className="btn btn-secondary" to={`/databases/${dbID}/changes`}>
          Request perubahan
        </Link>
        {canEdit && !creating && (
          <button className="btn btn-primary" type="button" onClick={() => setCreating(true)}>
            Buat migration
          </button>
        )}
        {canEdit && (
          <Link className="btn btn-secondary" to={`/databases/${dbID}/migrations/diff`}>
            Buat dari schema diff
          </Link>
        )}
      </div>

      {creating && (
        <div className="card">
          <h2 className="h2">Migration baru</h2>
          <ErrorBanner message={action.error} />
          <DraftForm
            initial={emptyDraft}
            submitLabel="Simpan"
            busy={action.busy}
            onSubmit={create}
            onCancel={() => setCreating(false)}
          />
        </div>
      )}

      <div className="card">
        <h2 className="h2">Migration</h2>
        {data && data.length === 0 ? (
          <p className="muted">Belum ada migration.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Versi</th>
                  <th>Judul</th>
                  <th>Status</th>
                  <th>Rollback</th>
                  <th>Dibuat oleh</th>
                  <th>Eksekusi terakhir</th>
                </tr>
              </thead>
              <tbody>
                {(data || []).map((m) => (
                  <tr key={m.id}>
                    <td>v{m.version}</td>
                    <td>
                      <Link to={`/databases/${dbID}/migrations/${m.id}`}>{m.title}</Link>
                    </td>
                    <td>{statusLabel[m.status] ?? m.status}</td>
                    <td>{m.irreversible ? 'irreversible' : 'reversible'}</td>
                    <td>{m.created_by}</td>
                    <td>{fmtTime(m.executed_at)}</td>
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
