import { Link } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Connection } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { useLoad } from '../../hooks';

export const ConnectionsPage = () => {
  const { data, error, loading } = useLoad(() => apiFetch<Connection[]>('/api/admin/databases'), []);

  if (loading) return <Loading />;

  return (
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <h2 className="h2">Koneksi database</h2>
        <Link className="btn btn-primary" to="/admin/connections/new">
          Tambah koneksi
        </Link>
      </div>
      <ErrorBanner message={error} />
      <div className="table-responsive-wrapper">
        <table>
          <thead>
            <tr>
              <th>Nama</th>
              <th>DBMS</th>
              <th>Host</th>
              <th>Database</th>
              <th>Username</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {(data || []).map((c) => (
              <tr key={c.id}>
                <td>
                  <Link to={`/admin/connections/${c.id}`}>{c.name}</Link>
                </td>
                <td>{c.db_type}</td>
                <td>
                  {c.host}:{c.port}
                </td>
                <td>{c.database}</td>
                <td>{c.username}</td>
                <td>{c.is_active ? 'Aktif' : 'Nonaktif'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};