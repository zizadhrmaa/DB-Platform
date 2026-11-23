import { Link, Outlet, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';

export const Layout = () => {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  const onLogout = async () => {
    await logout();
    navigate('/login', { replace: true });
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', minHeight: '100vh' }}>
      <header className="header">
        <Link to="/databases" className="header-brand">
          <span className="brand-icon" aria-hidden="true">
            ▤
          </span>
          <span>DB Platform</span>
        </Link>
        <nav className="row">
          <Link to="/databases">Database</Link>
          {user?.is_platform_admin && <Link to="/admin">Admin</Link>}
          <span className="nav-user">{user?.username}</span>
          <button className="btn btn-secondary" onClick={onLogout}>
            Keluar
          </button>
        </nav>
      </header>
      <main className="container" style={{ flex: 1, paddingTop: '1.5rem', paddingBottom: '2rem' }}>
        <Outlet />
      </main>
      <footer className="footer">IF3110 · Milestone 2</footer>
    </div>
  );
};