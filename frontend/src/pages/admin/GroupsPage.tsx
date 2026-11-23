import { FormEvent, useState } from 'react';
import { Link } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { AdminGroup } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { useAction, useLoad } from '../../hooks';

export const GroupsPage = () => {
  const { data, error, loading, reload } = useLoad(() => apiFetch<AdminGroup[]>('/api/admin/groups'), []);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const action = useAction(() => {
    setName('');
    setDescription('');
    reload();
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    action.run(() => apiFetch('/api/admin/groups', { method: 'POST', json: { name, description } }));
  };

  if (loading) return <Loading />;

  return (
    <div>
      <ErrorBanner message={error} />
      <div className="card">
        <h2 className="h2">Daftar group</h2>
        <div className="table-responsive-wrapper">
          <table>
            <thead>
              <tr>
                <th>Nama</th>
                <th>Deskripsi</th>
                <th>Anggota</th>
                <th>Akses database</th>
                <th>Leader</th>
              </tr>
            </thead>
            <tbody>
              {(data || []).map((g) => (
                <tr key={g.id}>
                  <td>
                    <Link to={`/admin/groups/${g.id}`}>{g.name}</Link>
                  </td>
                  <td>{g.description}</td>
                  <td>{g.member_count}</td>
                  <td>{g.access_count}</td>
                  <td>{g.leader_user_id ? 'Ada' : <span className="muted">Belum ada</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <form className="card" onSubmit={onSubmit}>
        <h2 className="h2">Buat group</h2>
        <ErrorBanner message={action.error} />
        <div className="form-grid">
          <div className="field">
            <label htmlFor="group-name">Nama</label>
            <input
              id="group-name"
              className="input"
              required
              maxLength={100}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="group-description">Deskripsi (opsional)</label>
            <input
              id="group-description"
              className="input"
              maxLength={500}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
        <button className="btn btn-primary" type="submit" disabled={action.busy}>
          Buat group
        </button>
      </form>
    </div>
  );
};