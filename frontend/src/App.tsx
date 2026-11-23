import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider } from './auth/AuthContext';
import { Layout } from './components/Layout';
import { RequireAuth } from './components/RequireAuth';
import { LoginPage } from './pages/LoginPage';
import { DatabaseListPage } from './pages/databases/DatabaseListPage';
import { DatabaseShowPage } from './pages/databases/DatabaseShowPage';
import { TableDetailPage } from './pages/databases/TableDetailPage';
import { TableDataPage } from './pages/databases/TableDataPage';
import { ChangesPage } from './pages/changes/ChangesPage';
import { ChangeDetailPage } from './pages/changes/ChangeDetailPage';
import { MigrationsPage } from './pages/migrations/MigrationsPage';
import { MigrationDetailPage } from './pages/migrations/MigrationDetailPage';
import { HistoryPage } from './pages/migrations/HistoryPage';
import { AdminLayout } from './pages/admin/AdminLayout';
import { UsersPage } from './pages/admin/UsersPage';
import { UserDetailPage } from './pages/admin/UserDetailPage';
import { GroupsPage } from './pages/admin/GroupsPage';
import { GroupDetailPage } from './pages/admin/GroupDetailPage';
import { ConnectionsPage } from './pages/admin/ConnectionsPage';
import { ConnectionFormPage } from './pages/admin/ConnectionFormPage';

const basename = import.meta.env.BASE_URL.replace(/\/$/, '');

export const App = () => (
  <BrowserRouter basename={basename}>
    <AuthProvider>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<RequireAuth />}>
          <Route element={<Layout />}>
            <Route index element={<Navigate to="/databases" replace />} />
            <Route path="databases" element={<DatabaseListPage />} />
            <Route path="databases/:dbID" element={<DatabaseShowPage />} />
            <Route path="databases/:dbID/tables/:schema/:table" element={<TableDetailPage />} />
            <Route path="databases/:dbID/tables/:schema/:table/data" element={<TableDataPage />} />
            <Route path="databases/:dbID/changes" element={<ChangesPage />} />
            <Route path="databases/:dbID/changes/:requestID" element={<ChangeDetailPage />} />
            <Route path="databases/:dbID/migrations" element={<MigrationsPage />} />
            <Route path="databases/:dbID/migrations/:migrationID" element={<MigrationDetailPage />} />
            <Route path="databases/:dbID/history" element={<HistoryPage />} />
            <Route element={<RequireAuth admin />}>
              <Route path="admin" element={<AdminLayout />}>
                <Route index element={<Navigate to="users" replace />} />
                <Route path="users" element={<UsersPage />} />
                <Route path="users/:userID" element={<UserDetailPage />} />
                <Route path="groups" element={<GroupsPage />} />
                <Route path="groups/:groupID" element={<GroupDetailPage />} />
                <Route path="connections" element={<ConnectionsPage />} />
                <Route path="connections/new" element={<ConnectionFormPage />} />
                <Route path="connections/:connID" element={<ConnectionFormPage />} />
              </Route>
            </Route>
            <Route path="*" element={<div className="error-banner">Halaman tidak ditemukan.</div>} />
          </Route>
        </Route>
      </Routes>
    </AuthProvider>
  </BrowserRouter>
);