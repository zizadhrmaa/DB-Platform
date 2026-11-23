import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { Loading } from './ui';

export const RequireAuth = ({ admin = false }: { admin?: boolean }) => {
  const { user, loading } = useAuth();
  const location = useLocation();

  if (loading) return <Loading />;
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }
  if (admin && !user.is_platform_admin) {
    return <div className="error-banner">Anda tidak memiliki izin untuk membuka halaman ini.</div>;
  }
  return <Outlet />;
};