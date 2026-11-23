import { FormEvent, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Connection } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { useAction } from '../../hooks';

const empty = {
  name: '',
  db_type: 'postgres',
  host: '',
  port: '5432',
  database: '',
  username: '',
  password: '',
  sslmode: 'disable',
  is_active: true,
};

export const ConnectionFormPage = () => {
  const { connID } = useParams();
  const navigate = useNavigate();
  const editing = connID !== undefined;
  const [form, setForm] = useState(empty);
  const [loading, setLoading] = useState(editing);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const action = useAction(() => undefined);

  useEffect(() => {
    if (!editing) return;
    apiFetch<Connection>(`/api/admin/databases/${connID}`)
      .then((c) => {
        setForm({ ...empty, ...c, port: String(c.port), password: '' });
        setLoading(false);
      })
      .catch((err: Error) => {
        setLoadError(err.message);
        setLoading(false);
      });
  }, [connID, editing]);

  if (loading) return <Loading />;
  if (loadError) return <ErrorBanner message={loadError} />;

  const set = (key: keyof typeof empty, value: string | boolean) => setForm({ ...form, [key]: value });

  const payload = () => {
    const body: Record<string, unknown> = {
      name: form.name,
      db_type: form.db_type,
      host: form.host,
      port: Number(form.port),
      database: form.database,
      username: form.username,
      sslmode: form.sslmode,
      is_active: form.is_active,
    };
    if (form.password !== '') body.password = form.password;
    return body;
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setNotice(null);
    if (editing) {
      action.run(
        () => apiFetch(`/api/admin/databases/${connID}`, { method: 'PATCH', json: payload() }),
        () => {
          setForm({ ...form, password: '' });
          setNotice('Perubahan koneksi disimpan.');
        },
      );
      return;
    }
    action.run(async () => {
      const created = await apiFetch<{ id: number }>('/api/admin/databases', { method: 'POST', json: payload() });
      navigate(`/admin/connections/${created.id}`, { replace: true });
    });
  };

  const onTest = () => {
    setNotice(null);
    apiFetch<{ ok: boolean; message: string }>(`/api/admin/databases/${connID}/test`, { method: 'POST' })
      .then((res) => setNotice(res.message))
      .catch((err: Error) => setNotice(err.message));
  };

  const onDelete = () => {
    if (!window.confirm(`Hapus koneksi ${form.name}? Grant ke group ikut terhapus.`)) return;
    action.run(
      () => apiFetch(`/api/admin/databases/${connID}`, { method: 'DELETE' }),
      () => navigate('/admin/connections', { replace: true }),
    );
  };

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/admin/connections">Koneksi database</Link>
        <span>/</span>
        <span>{editing ? form.name : 'Baru'}</span>
      </div>
      <form className="card" onSubmit={onSubmit}>
        <h2 className="h2">{editing ? 'Data koneksi' : 'Tambah koneksi'}</h2>
        <ErrorBanner message={action.error} />
        {notice && <div className="ok-banner">{notice}</div>}
        <div className="form-grid">
          <div className="field">
            <label htmlFor="c-name">Nama tampilan</label>
            <input id="c-name" className="input" required value={form.name} onChange={(e) => set('name', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-type">DBMS</label>
            <select id="c-type" className="input" value={form.db_type} onChange={(e) => set('db_type', e.target.value)}>
              <option value="postgres">postgres</option>
              <option value="mysql">mysql</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="c-host">Host</label>
            <input id="c-host" className="input" required autoComplete="off" value={form.host} onChange={(e) => set('host', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-port">Port</label>
            <input id="c-port" className="input" type="number" min={1} max={65535} value={form.port} onChange={(e) => set('port', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-db">Nama database</label>
            <input id="c-db" className="input" required autoComplete="off" value={form.database} onChange={(e) => set('database', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-user">Username</label>
            <input id="c-user" className="input" required autoComplete="off" value={form.username} onChange={(e) => set('username', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-pass">Password{editing ? ' (kosongkan jika tidak diganti)' : ''}</label>
            <input
              id="c-pass"
              className="input"
              type="password"
              autoComplete="new-password"
              required={!editing}
              value={form.password}
              onChange={(e) => set('password', e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="c-ssl">Mode SSL</label>
            <select id="c-ssl" className="input" value={form.sslmode} onChange={(e) => set('sslmode', e.target.value)}>
              <option value="disable">disable</option>
              <option value="require">require</option>
              <option value="verify-ca">verify-ca</option>
              <option value="verify-full">verify-full</option>
            </select>
          </div>
        </div>
        <label className="row" style={{ margin: '0.75rem 0' }}>
          <input type="checkbox" checked={form.is_active} onChange={(e) => set('is_active', e.target.checked)} />
          Aktif
        </label>
        <div className="row">
          <button className="btn btn-primary" type="submit" disabled={action.busy}>
            Simpan
          </button>
          {editing && (
            <>
              <button className="btn btn-secondary" type="button" disabled={action.busy} onClick={onTest}>
                Uji koneksi
              </button>
              <button className="btn btn-danger" type="button" disabled={action.busy} onClick={onDelete}>
                Hapus
              </button>
            </>
          )}
        </div>
      </form>
    </div>
  );
};