import { FormEvent, useState } from 'react';
import { Link } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { AdminUser } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { fmtTime, useAction, useLoad } from '../../hooks';

const emptyForm = { username: '', password: '', is_admin: false };

export const UsersPage = () => {
  const { data, error, loading, reload } = useLoad(() => apiFetch<AdminUser[]>('/api/admin/users'), []);
  const [form, setForm] = useState(emptyForm);
  const action = useAction(() => {
    setForm(emptyForm);
    reload();
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    action.run(() => apiFetch('/api/admin/users', { method: 'POST', json: form }));
  };

  if (loading) return <Loading />;

  return (
    <div>
      <ErrorBanner message={error} />
      <div className="card">
        <h2 className="h2">Daftar user</h2>
        <div className="table-responsive-wrapper">
          <table>
            <thead>
              <tr>
                <th>Username</th>
                <th>Peran</th>
                <th>Status</th>
                <th>Group</th>
                <th>Dibuat</th>
              </tr>
            </thead>
            <tbody>
              {(data || []).map((u) => (
                <tr key={u.id}>
                  <td>
                    <Link to={`/admin/users/${u.id}`}>{u.username}</Link>
                  </td>
                  <td>{u.is_platform_admin ? 'Platform Admin' : 'User'}</td>
                  <td>{u.is_active ? 'Aktif' : 'Nonaktif'}</td>
                  <td>{u.group_count}</td>
                  <td>{fmtTime(u.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <form className="card" onSubmit={onSubmit}>
        <h2 className="h2">Tambah user</h2>
        <ErrorBanner message={action.error} />
        <div className="form-grid">
          <div className="field">
            <label htmlFor="new-username">Username</label>
            <input
              id="new-username"
              className="input"
              required
              autoComplete="off"
              value={form.username}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
            />
          </div>
          <div className="field">
            <label htmlFor="new-password">Password</label>
            <input
              id="new-password"
              className="input"
              type="password"
              required
              autoComplete="new-password"
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
            />
          </div>
          <label className="row">
            <input
              type="checkbox"
              checked={form.is_admin}
              onChange={(e) => setForm({ ...form, is_admin: e.target.checked })}
            />
            Platform Admin
          </label>
        </div>
        <button className="btn btn-primary" type="submit" disabled={action.busy}>
          Simpan
        </button>
      </form>
    </div>
  );
};