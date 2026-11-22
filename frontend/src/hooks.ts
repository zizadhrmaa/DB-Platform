import { useEffect, useState } from 'react';

export function useLoad<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    fn()
      .then((value) => {
        if (!alive) return;
        setData(value);
        setError(null);
        setLoading(false);
      })
      .catch((err: Error) => {
        if (!alive) return;
        setError(err.message);
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [...deps, tick]);

  return { data, error, loading, reload: () => setTick((t) => t + 1) };
}

export function useAction(onDone: () => void) {
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<unknown>, after: () => void = onDone) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      after();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return { error, busy, run };
}

export const fmtTime = (value: string | null) => (value ? new Date(value).toLocaleString('id-ID') : '-');