import React from 'react';
import { Link, Outlet } from 'react-router-dom';

export const Layout: React.FC = () => {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', minHeight: '100vh' }}>
      <header className="header">
        <Link to="/databases" className="header-brand">
          <span>🗄️</span>
          <span>DB Platform</span>
        </Link>
        <nav style={{ display: 'flex', gap: '1rem', alignItems: 'center' }}>
          <Link to="/databases" style={{ color: 'var(--text-secondary)' }}>
            Database
          </Link>
          <a href="/login" style={{ color: 'var(--text-muted)', fontSize: '0.875rem' }}>
            Logout
          </a>
        </nav>
      </header>
      <main className="container" style={{ flex: 1, paddingTop: '1.5rem', paddingBottom: '2rem' }}>
        <Outlet />
      </main>
      <footer style={{ borderTop: '1px solid var(--border-color)', padding: '1rem', textAlign: 'center', color: 'var(--text-muted)', fontSize: '0.8rem' }}>
        DB Platform — Milestone 2 SPA
      </footer>
    </div>
  );
};
