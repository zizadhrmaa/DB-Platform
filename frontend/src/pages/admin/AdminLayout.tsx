import { NavLink, Outlet } from 'react-router-dom';

const tab = ({ isActive }: { isActive: boolean }) => `tab-link${isActive ? ' active' : ''}`;

export const AdminLayout = () => (
  <div>
    <h1 style={{ fontSize: '1.5rem', marginBottom: '1rem' }}>Platform Admin</h1>
    <nav className="tabs">
      <NavLink to="/admin/users" className={tab}>
        User
      </NavLink>
      <NavLink to="/admin/groups" className={tab}>
        Group
      </NavLink>
      <NavLink to="/admin/connections" className={tab}>
        Koneksi database
      </NavLink>
    </nav>
    <Outlet />
  </div>
);