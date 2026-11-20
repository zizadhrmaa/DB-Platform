import React from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { DatabaseListPage } from './pages/databases/DatabaseListPage';
import { DatabaseShowPage } from './pages/databases/DatabaseShowPage';
import { TableDetailPage } from './pages/databases/TableDetailPage';
import { TableDataPage } from './pages/databases/TableDataPage';

export const App: React.FC = () => {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Layout />}>
          <Route index element={<Navigate to="/databases" replace />} />
          <Route path="databases" element={<DatabaseListPage />} />
          <Route path="databases/:dbID" element={<DatabaseShowPage />} />
          <Route path="databases/:dbID/tables/:schema/:table" element={<TableDetailPage />} />
          <Route path="databases/:dbID/tables/:schema/:table/data" element={<TableDataPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
};
