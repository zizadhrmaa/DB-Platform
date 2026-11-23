import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { apiFetch } from '../api/client';
import { setCsrfToken, setUnauthorizedHandler } from '../api/session';
import type { Me } from '../api/types';

interface AuthState {
  user: Me | null;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [user, setUser] = useState<Me | null>(null);
  const [loading, setLoading] = useState(true);

  const apply = useCallback((me: Me | null) => {
    setCsrfToken(me ? me.csrf_token : '');
    setUser(me);
  }, []);

  const refresh = useCallback(async () => {
    try {
      apply(await apiFetch<Me>('/api/auth/me'));
    } catch {
      apply(null);
    } finally {
      setLoading(false);
    }
  }, [apply]);

  useEffect(() => {
    setUnauthorizedHandler(() => apply(null));
    refresh();
    return () => setUnauthorizedHandler(null);
  }, [apply, refresh]);

  const login = useCallback(
    async (username: string, password: string) => {
      await apiFetch('/api/auth/login', { method: 'POST', json: { username, password } });
      await refresh();
    },
    [refresh],
  );

  const logout = useCallback(async () => {
    try {
      await apiFetch('/api/auth/logout', { method: 'POST' });
    } finally {
      apply(null);
    }
  }, [apply]);

  const value = useMemo(() => ({ user, loading, login, logout }), [user, loading, login, logout]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export const useAuth = () => {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth harus dipakai di dalam AuthProvider');
  return ctx;
};