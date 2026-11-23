import { FormEvent, useEffect, useState } from 'react';
import { apiFetch } from '../../api/client';
import type { DatabaseShowResponse } from '../../api/client';
import type { MigrationDraft } from '../../api/types';

export const sqlBox = {
  overflow: 'auto',
  maxHeight: '20rem',
  whiteSpace: 'pre',
  padding: '0.75rem',
  borderRadius: '6px',
  border: '1px solid var(--border-color)',
  backgroundColor: 'var(--bg-primary)',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: '0.8rem',
  margin: 0,
} as const;

export const sqlInput = {
  width: '100%',
  minHeight: '8rem',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: '0.85rem',
  resize: 'vertical',
} as const;

export const statusLabel: Record<string, string> = {
  pending: 'pending',
  running: 'running',
  success: 'aktif',
  failed: 'gagal',
  discarded: 'dibatalkan',
  rolled_back: 'di-rollback',
};

// useCanEdit mengambil level akses user untuk menampilkan tombol yang
// relevan. Backend tetap memeriksa hak akses di setiap aksi.
export function useCanEdit(dbID: string | undefined) {
  const [canEdit, setCanEdit] = useState(false);
  const [dbName, setDbName] = useState('');
  useEffect(() => {
    if (!dbID) return;
    apiFetch<DatabaseShowResponse>(`/api/databases/${dbID}`)
      .then((res) => {
        setCanEdit(res.db.level === 'edit');
        setDbName(res.db.name);
      })
      .catch(() => setCanEdit(false));
  }, [dbID]);
  return { canEdit, dbName };
}

export const emptyDraft: MigrationDraft = {
  title: '',
  sql_up: '',
  sql_down: '',
  irreversible: false,
  irreversible_reason: '',
  exec_mode: 'normal',
  batch_table: '',
  batch_column: 'id',
  batch_size: 1000,
};

interface DraftFormProps {
  initial: MigrationDraft;
  submitLabel: string;
  busy: boolean;
  onSubmit: (draft: MigrationDraft) => void;
  onCancel?: () => void;
}

// DraftForm dipakai untuk membuat dan mengedit migration: SQL up wajib,
// lalu SQL down atau tanda irreversible beserta alasannya.
export const DraftForm = ({ initial, submitLabel, busy, onSubmit, onCancel }: DraftFormProps) => {
  const [draft, setDraft] = useState<MigrationDraft>(initial);
  const set = <K extends keyof MigrationDraft>(key: K, value: MigrationDraft[K]) =>
    setDraft((d) => ({ ...d, [key]: value }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(
      draft.irreversible
        ? { ...draft, sql_down: '' }
        : { ...draft, irreversible_reason: '' },
    );
  };

  return (
    <form onSubmit={submit}>
      <div className="field">
        <label htmlFor="mig-title">Judul</label>
        <input
          id="mig-title"
          className="input"
          required
          value={draft.title}
          onChange={(e) => set('title', e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="mig-mode">Mode eksekusi</label>
        <select
          id="mig-mode"
          className="input"
          value={draft.exec_mode}
          onChange={(e) => set('exec_mode', e.target.value as MigrationDraft['exec_mode'])}
        >
          <option value="normal">Biasa (satu transaction)</option>
          <option value="batch">Per batch</option>
        </select>
      </div>
      {draft.exec_mode === 'batch' && (
        <>
          <p className="muted">
            SQL up dan down harus satu statement yang memakai <code>$1</code> (batas bawah, eksklusif) dan{' '}
            <code>$2</code> (batas atas, inklusif), misalnya{' '}
            <code>UPDATE produk SET harga = harga * 2 WHERE id &gt; $1 AND id &lt;= $2</code>.
          </p>
          <div className="form-grid">
            <div className="field">
              <label htmlFor="mig-table">Tabel</label>
              <input
                id="mig-table"
                className="input"
                required
                placeholder="produk atau public.produk"
                value={draft.batch_table}
                onChange={(e) => set('batch_table', e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="mig-column">Kolom batch (integer, unik)</label>
              <input
                id="mig-column"
                className="input"
                required
                value={draft.batch_column}
                onChange={(e) => set('batch_column', e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="mig-size">Ukuran batch</label>
              <input
                id="mig-size"
                className="input"
                type="number"
                min={1}
                required
                value={draft.batch_size}
                onChange={(e) => set('batch_size', Number(e.target.value))}
              />
            </div>
          </div>
        </>
      )}
      <div className="field">
        <label htmlFor="mig-up">SQL up</label>
        <textarea
          id="mig-up"
          className="input"
          style={sqlInput}
          required
          spellCheck={false}
          value={draft.sql_up}
          onChange={(e) => set('sql_up', e.target.value)}
        />
      </div>
      <div className="field">
        <label style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <input
            type="checkbox"
            checked={draft.irreversible}
            onChange={(e) => set('irreversible', e.target.checked)}
          />
          Irreversible (tidak bisa di-rollback)
        </label>
      </div>
      {draft.irreversible ? (
        <div className="field">
          <label htmlFor="mig-reason">Alasan irreversible</label>
          <textarea
            id="mig-reason"
            className="input"
            required
            rows={3}
            value={draft.irreversible_reason}
            onChange={(e) => set('irreversible_reason', e.target.value)}
          />
        </div>
      ) : (
        <div className="field">
          <label htmlFor="mig-down">SQL down (membatalkan SQL up)</label>
          <textarea
            id="mig-down"
            className="input"
            style={sqlInput}
            required
            spellCheck={false}
            value={draft.sql_down}
            onChange={(e) => set('sql_down', e.target.value)}
          />
        </div>
      )}
      <div className="row">
        <button className="btn btn-primary" type="submit" disabled={busy}>
          {submitLabel}
        </button>
        {onCancel && (
          <button className="btn btn-secondary" type="button" onClick={onCancel} disabled={busy}>
            Batal
          </button>
        )}
      </div>
    </form>
  );
};
