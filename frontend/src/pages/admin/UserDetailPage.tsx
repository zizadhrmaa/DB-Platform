import { FormEvent, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { AdminUser } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';

export const UserDetailPage = () => {
  const { userID } = useParams();
  const navigate = useNavigate();
  const { data, error, loading, reload } = useLoad(() => apiFetch<AdminUser>(`/api/admin/users/${userID}`), [userID]);
  const [password, setPassword] = useState('');
  const action = useAction(reload);
  const path = `/api/admin/users/${userID}`;

  if (loading) return <Loading />;
  if (!data) return <ErrorBanner message={error} />;

  const patch = (json: Record<string, unknown>) =>
    action.run(() => apiFetch(path, { method: 'PATCH', json }));

  const onReset = (e: FormEvent) => {
    e.preventDefault();
    action.run(() => apiFetch(path, { method: 'PATCH', json: { password } }), () => {
      setPassword('');
      reload();
    });
  };

  const onDelete = () => {
    if (!window.confirm(`Hapus user ${data.username}?`)) return;
    action.run(
      () => apiFetch(path, { method: 'DELETE' }),
      () => navigate('/admin/users'),
    );
  };

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/admin/users">User</Link>
        <span>/</span>
        <span>{data.username}</span>
      </div>
      <ErrorBanner message={action.error} />
      <div className="card">
        <h2 className="h2">{data.username}</h2>
        <p className="muted">
          Dibuat {fmtTime(data.created_at)} &bull; Group:{' '}
          {data.groups.length === 0
            ? 'belum masuk group mana pun'
            : data.groups.map((g, i) => (
                <span key={g.id}>
                  {i > 0 && ', '}
                  <Link to={`/admin/groups/${g.id}`}>{g.name}</Link>
                </span>
              ))}
        </p>
        <div className="row" style={{ marginTop: '1rem' }}>
          <button
            className="btn btn-secondary"
            disabled={action.busy}
            onClick={() => patch({ is_active: !data.is_active })}
          >
            {data.is_active ? 'Nonaktifkan' : 'Aktifkan'}
          </button>
          <button
            className="btn btn-secondary"
            disabled={action.busy}
            onClick={() => patch({ is_admin: !data.is_platform_admin })}
          >
            {data.is_platform_admin ? 'Turunkan dari Platform Admin' : 'Jadikan Platform Admin'}
          </button>
        </div>
      </div>

      <form className="card" onSubmit={onReset}>
        <h2 className="h2">Ganti password</h2>
        <div className="field">
          <label htmlFor="reset-password">Password baru (minimal 8 karakter)</label>
          <input
            id="reset-password"
            className="input"
            type="password"
            required
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
        <button className="btn btn-primary" type="submit" disabled={action.busy}>
          Ganti password
        </button>
      </form>

      <div className="card">
        <h2 className="h2">Hapus user</h2>
        <button className="btn btn-danger" disabled={action.busy} onClick={onDelete}>
          Hapus user
        </button>
      </div>
    </div>
  );
};