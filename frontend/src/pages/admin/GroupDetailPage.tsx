import { FormEvent, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { GroupDetail } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { useAction, useLoad } from '../../hooks';

export const GroupDetailPage = () => {
  const { groupID } = useParams();
  const navigate = useNavigate();
  const { data, error, loading, reload } = useLoad(
    () => apiFetch<GroupDetail>(`/api/admin/groups/${groupID}`),
    [groupID],
  );
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [memberId, setMemberId] = useState('');
  const [grantDb, setGrantDb] = useState('');
  const [grantLevel, setGrantLevel] = useState('view');
  const action = useAction(reload);
  const base = `/api/admin/groups/${groupID}`;

  useEffect(() => {
    if (!data) return;
    setName(data.name);
    setDescription(data.description);
  }, [data]);

  if (loading && !data) return <Loading />;
  if (!data) return <ErrorBanner message={error} />;

  const onEdit = (e: FormEvent) => {
    e.preventDefault();
    action.run(() => apiFetch(base, { method: 'PATCH', json: { name, description } }));
  };

  const onAddMember = (e: FormEvent) => {
    e.preventDefault();
    action.run(
      () => apiFetch(`${base}/members`, { method: 'POST', json: { user_id: Number(memberId) } }),
      () => {
        setMemberId('');
        reload();
      },
    );
  };

  const onGrant = (e: FormEvent) => {
    e.preventDefault();
    action.run(() => apiFetch(`${base}/access/${grantDb}`, { method: 'PUT', json: { level: grantLevel } }));
  };

  const onLeader = (value: string) =>
    action.run(() =>
      apiFetch(`${base}/leader`, { method: 'PUT', json: { user_id: value === '' ? null : Number(value) } }),
    );

  const onDelete = () => {
    if (!window.confirm(`Hapus group ${data.name}?`)) return;
    action.run(
      () => apiFetch(base, { method: 'DELETE' }),
      () => navigate('/admin/groups'),
    );
  };

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/admin/groups">Group</Link>
        <span>/</span>
        <span>{data.name}</span>
      </div>
      <ErrorBanner message={action.error} />

      <div className="card">
        <h2 className="h2">Leader</h2>
        <p className="muted">Leader memberi approval atas perubahan database yang diakses group ini.</p>
        <select
          className="input"
          aria-label="Leader group"
          value={data.leader_user_id ?? ''}
          disabled={action.busy}
          onChange={(e) => onLeader(e.target.value)}
        >
          <option value="">Belum ada leader</option>
          {data.members
            .filter((m) => m.is_active)
            .map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.username}
              </option>
            ))}
        </select>
      </div>

      <div className="card">
        <h2 className="h2">Akses database</h2>
        {data.access.length === 0 ? (
          <p className="muted">Group ini belum punya akses ke database mana pun.</p>
        ) : (
          <div className="table-responsive-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Database</th>
                  <th>Level</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {data.access.map((a) => (
                  <tr key={a.database_id}>
                    <td>
                      {a.database_name}
                      {!a.database_active && <span className="muted"> (nonaktif)</span>}
                    </td>
                    <td>
                      <select
                        className="input"
                        aria-label={`Level akses ${a.database_name}`}
                        value={a.level}
                        disabled={action.busy}
                        onChange={(e) =>
                          action.run(() =>
                            apiFetch(`${base}/access/${a.database_id}`, {
                              method: 'PUT',
                              json: { level: e.target.value },
                            }),
                          )
                        }
                      >
                        <option value="view">View Only</option>
                        <option value="edit">View + Edit</option>
                      </select>
                    </td>
                    <td>
                      <button
                        className="btn btn-danger"
                        disabled={action.busy}
                        onClick={() =>
                          action.run(() => apiFetch(`${base}/access/${a.database_id}`, { method: 'DELETE' }))
                        }
                      >
                        Cabut
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {data.databases.length > 0 && (
          <form className="row" style={{ marginTop: '1rem' }} onSubmit={onGrant}>
            <select
              className="input"
              style={{ width: 'auto' }}
              aria-label="Database"
              required
              value={grantDb}
              onChange={(e) => setGrantDb(e.target.value)}
            >
              <option value="">Pilih database</option>
              {data.databases.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                  {!d.is_active ? ' (nonaktif)' : ''}
                </option>
              ))}
            </select>
            <select
              className="input"
              style={{ width: 'auto' }}
              aria-label="Level"
              value={grantLevel}
              onChange={(e) => setGrantLevel(e.target.value)}
            >
              <option value="view">View Only</option>
              <option value="edit">View + Edit</option>
            </select>
            <button className="btn btn-primary" type="submit" disabled={action.busy}>
              Simpan akses
            </button>
          </form>
        )}
      </div>

      <div className="card">
        <h2 className="h2">Anggota</h2>
        {data.members.length === 0 ? (
          <p className="muted">Group ini belum punya anggota.</p>
        ) : (
          <ul className="plain-list">
            {data.members.map((m) => (
              <li key={m.user_id} className="row">
                <Link to={`/admin/users/${m.user_id}`}>{m.username}</Link>
                {!m.is_active && <span className="muted">(nonaktif)</span>}
                {data.leader_user_id === m.user_id && <span className="badge badge-edit">Leader</span>}
                <button
                  className="btn btn-secondary"
                  disabled={action.busy}
                  onClick={() => action.run(() => apiFetch(`${base}/members/${m.user_id}`, { method: 'DELETE' }))}
                >
                  Keluarkan
                </button>
              </li>
            ))}
          </ul>
        )}
        {data.candidates.length > 0 && (
          <form className="row" style={{ marginTop: '1rem' }} onSubmit={onAddMember}>
            <select
              className="input"
              style={{ width: 'auto' }}
              aria-label="Tambah anggota"
              required
              value={memberId}
              onChange={(e) => setMemberId(e.target.value)}
            >
              <option value="">Pilih user</option>
              {data.candidates.map((c) => (
                <option key={c.user_id} value={c.user_id}>
                  {c.username}
                </option>
              ))}
            </select>
            <button className="btn btn-primary" type="submit" disabled={action.busy}>
              Tambah
            </button>
          </form>
        )}
      </div>

      <form className="card" onSubmit={onEdit}>
        <h2 className="h2">Ubah group</h2>
        <div className="form-grid">
          <div className="field">
            <label htmlFor="edit-name">Nama</label>
            <input
              id="edit-name"
              className="input"
              required
              maxLength={100}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="edit-description">Deskripsi</label>
            <input
              id="edit-description"
              className="input"
              maxLength={500}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
        <button className="btn btn-primary" type="submit" disabled={action.busy}>
          Simpan
        </button>
      </form>

      <div className="card">
        <h2 className="h2">Hapus group</h2>
        <p className="muted">Semua anggota langsung kehilangan akses yang didapat dari group ini.</p>
        <button className="btn btn-danger" disabled={action.busy} onClick={onDelete}>
          Hapus group
        </button>
      </div>
    </div>
  );
};