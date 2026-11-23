import { FormEvent, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { DatabaseShowResponse } from '../../api/client';
import type { ChangeDetail, ChangeRequest } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';

interface MigrationOption {
  id: number;
  version: number;
  title: string;
  status: string;
}

export const ChangesPage = () => {
  const { dbID } = useParams();
  const navigate = useNavigate();
  const { data, error, loading, reload } = useLoad(
    () => apiFetch<ChangeRequest[]>(`/api/databases/${dbID}/requests`),
    [dbID],
  );
  const [canEdit, setCanEdit] = useState(false);
  const [options, setOptions] = useState<MigrationOption[] | null>(null);
  const [migrationId, setMigrationId] = useState('');
  const [kind, setKind] = useState('up');
  const action = useAction(reload);

  useEffect(() => {
    apiFetch<DatabaseShowResponse>(`/api/databases/${dbID}`)
      .then((res) => setCanEdit(res.db.level === 'edit'))
      .catch(() => setCanEdit(false));
    apiFetch<MigrationOption[]>(`/api/databases/${dbID}/migrations`)
      .then((res) => setOptions(Array.isArray(res) ? res : null))
      .catch(() => setOptions(null));
  }, [dbID]);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    action.run(async () => {
      const created = await apiFetch<ChangeDetail>(`/api/databases/${dbID}/migrations/${migrationId}/requests`, {
        method: 'POST',
        json: { kind },
      });
      navigate(`/databases/${dbID}/changes/${created.id}`);
    });
  };

  if (loading) return <Loading />;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}`}>Schema</Link>
        <span>/</span>
        <span>Perubahan</span>
      </div>
      <ErrorBanner message={error} />
      <div className="card">
        <h2 className="h2">Request perubahan</h2>
        <div className="table-responsive-wrapper">
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>Migration</th>
                <th>Jenis</th>
                <th>Status</th>
                <th>Pengaju</th>
                <th>Dibuat</th>
              </tr>
            </thead>
            <tbody>
              {(data || []).map((r) => (
                <tr key={r.id}>
                  <td>
                    <Link to={`/databases/${dbID}/changes/${r.id}`}>{r.id}</Link>
                  </td>
                  <td>
                    v{r.version} {r.title}
                  </td>
                  <td>{r.kind}</td>
                  <td>
                    {r.stale ? `${r.status} (SQL berubah)` : r.status}
                    {r.job_status ? ` / job ${r.job_status}` : ''}
                  </td>
                  <td>{r.requested_by}</td>
                  <td>{fmtTime(r.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {canEdit && (
        <form className="card" onSubmit={onSubmit}>
          <h2 className="h2">Buat request</h2>
          <ErrorBanner message={action.error} />
          <div className="form-grid">
            <div className="field">
              <label htmlFor="migration-id">Migration</label>
              {options ? (
                <select
                  id="migration-id"
                  className="input"
                  required
                  value={migrationId}
                  onChange={(e) => setMigrationId(e.target.value)}
                >
                  <option value="">Pilih migration</option>
                  {options.map((m) => (
                    <option key={m.id} value={m.id}>
                      v{m.version} {m.title} ({m.status})
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id="migration-id"
                  className="input"
                  type="number"
                  min={1}
                  required
                  value={migrationId}
                  onChange={(e) => setMigrationId(e.target.value)}
                />
              )}
            </div>
            <div className="field">
              <label htmlFor="kind">Jenis</label>
              <select id="kind" className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
                <option value="up">up</option>
                <option value="rollback">rollback</option>
                <option value="forward">forward</option>
              </select>
            </div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={action.busy}>
            Buat request
          </button>
        </form>
      )}
    </div>
  );
};