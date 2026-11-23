import { FormEvent, useState } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { ErrorBanner } from '../components/ui';

const safeRedirect = (path: string | undefined) =>
  path && path.startsWith('/') && !path.startsWith('//') ? path : '/databases';

export const LoginPage = () => {
  const { user, loading, login } = useAuth();
  const location = useLocation();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const from = safeRedirect((location.state as { from?: string } | null)?.from);

  if (!loading && user) return <Navigate to={from} replace />;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(username, password);
    } catch (err) {
      setError((err as Error).message);
      setPassword('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <header className="header">
        <span className="header-brand">
          <span className="brand-icon" aria-hidden="true">
            ▤
          </span>
          <span>DB Platform</span>
        </span>
      </header>
    <div className="container narrow">
      <form className="card" onSubmit={onSubmit}>
        <h1 style={{ fontSize: '1.5rem', marginBottom: '1rem' }}>Masuk</h1>
        <ErrorBanner message={error} />
        <div className="field">
          <label htmlFor="username">Username</label>
          <input
            id="username"
            className="input"
            autoComplete="username"
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>
        <div className="field">
          <label htmlFor="password">Password</label>
          <input
            id="password"
            className="input"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
        <button className="btn btn-primary" type="submit" disabled={busy}>
          {busy ? 'Memproses...' : 'Masuk'}
        </button>
      </form>
    </div>
    </>
  );
};