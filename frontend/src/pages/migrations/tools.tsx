import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiFetch } from '../../api/client';
import type { Migration, MigrationDraft } from '../../api/types';
import { ErrorBanner, Loading } from '../../components/ui';
import { useAction, useLoad } from '../../hooks';
import { DraftForm, emptyDraft, sqlBox, sqlInput, useCanEdit } from './shared';

// Fitur bonus Milestone 1 yang dipindahkan ke SPA: analisis query (EXPLAIN)
// dan pembuatan migration dari schema diff.

interface PlanNode {
  depth: number;
  operation: string;
  target: string;
  condition: string;
  startup_cost: number;
  total_cost: number;
  plan_rows: number;
  has_actual: boolean;
  actual_ms: number;
  actual_rows: number;
  loops: number;
  rows_removed: number;
  uses_index: boolean;
  is_seq_scan: boolean;
  join_strategy: string;
}

interface StatementPlan {
  sql: string;
  explainable: boolean;
  error: string;
  planning_ms: number;
  execution_ms: number;
  nodes: PlanNode[];
}

interface Analysis {
  analyze: boolean;
  statements: StatementPlan[];
}

const num = (n: number) => n.toLocaleString('id-ID', { maximumFractionDigits: 2 });

export const ExplainPanel = ({ base }: { base: string }) => {
  const [analyze, setAnalyze] = useState(false);
  const [result, setResult] = useState<Analysis | null>(null);
  const action = useAction(() => {});

  const run = () =>
    action.run(async () => {
      setResult(await apiFetch<Analysis>(`${base}/explain`, { method: 'POST', json: { analyze } }));
    });

  return (
    <div className="card">
      <h2 className="h2">Analisis query</h2>
      <p className="muted">
        Menampilkan execution plan setiap statement sebelum migration dijalankan. Semua statement dijalankan di
        transaction yang selalu di-rollback, jadi isi database tidak berubah.
      </p>
      <div className="row">
        <label>
          <input type="checkbox" checked={analyze} onChange={(e) => setAnalyze(e.target.checked)} /> EXPLAIN ANALYZE
          (statement benar-benar dijalankan, lalu di-rollback)
        </label>
        <button className="btn btn-secondary" type="button" disabled={action.busy} onClick={run}>
          {action.busy ? 'Menganalisis...' : 'Analisis query'}
        </button>
      </div>
      <ErrorBanner message={action.error} />
      {result?.statements.map((s, i) => (
        <div key={i} style={{ marginTop: '1rem' }}>
          <pre style={sqlBox}>{s.sql}</pre>
          {s.error ? (
            <ErrorBanner message={s.error} />
          ) : !s.explainable ? (
            <p className="muted">Perintah DDL tidak punya execution plan; dijalankan di simulasi saja.</p>
          ) : (
            <>
              <p className="muted">
                Planning {num(s.planning_ms)} ms
                {result.analyze && <> &bull; Eksekusi {num(s.execution_ms)} ms</>}
              </p>
              <div className="table-responsive-wrapper">
                <table>
                  <thead>
                    <tr>
                      <th>Operasi</th>
                      <th>Objek</th>
                      <th>Kondisi</th>
                      <th>Cost</th>
                      <th>Estimasi row</th>
                      {result.analyze && (
                        <>
                          <th>Waktu aktual</th>
                          <th>Row aktual</th>
                          <th>Loop</th>
                        </>
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {s.nodes.map((n, j) => (
                      <tr key={j}>
                        <td style={{ paddingLeft: `${0.5 + n.depth * 1.25}rem`, whiteSpace: 'nowrap' }}>
                          {n.depth > 0 && '-> '}
                          {n.operation}
                          {n.uses_index && <span className="badge"> index</span>}
                          {n.is_seq_scan && <span className="badge"> seq scan</span>}
                          {n.join_strategy && <span className="badge"> {n.join_strategy}</span>}
                        </td>
                        <td>{n.target}</td>
                        <td>{n.condition}</td>
                        <td>
                          {num(n.startup_cost)}..{num(n.total_cost)}
                        </td>
                        <td>{num(n.plan_rows)}</td>
                        {result.analyze && (
                          <>
                            <td>{n.has_actual ? `${num(n.actual_ms)} ms` : '-'}</td>
                            <td>
                              {n.has_actual ? num(n.actual_rows) : '-'}
                              {n.rows_removed > 0 && ` (dibuang ${num(n.rows_removed)})`}
                            </td>
                            <td>{n.has_actual ? num(n.loops) : '-'}</td>
                          </>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </div>
      ))}
    </div>
  );
};

interface CurrentSchema {
  schemas: string[];
  schema: string;
  ddl: string;
}

interface SchemaDiff {
  schema: string;
  sql: string;
  changes: { action: string; object: string; detail: string; destructive: boolean }[];
}

export const SchemaDiffPage = () => {
  const { dbID } = useParams();
  const navigate = useNavigate();
  const { canEdit, dbName } = useCanEdit(dbID);
  const [schema, setSchema] = useState('');
  const path = `/api/databases/${dbID}/schema-diff`;
  const { data, error, loading } = useLoad(async () => {
    const cur = await apiFetch<CurrentSchema>(schema ? `${path}?schema=${encodeURIComponent(schema)}` : path);
    setDesired(cur.ddl);
    setResult(null);
    return cur;
  }, [path, schema]);
  const [desired, setDesired] = useState('');
  const [result, setResult] = useState<SchemaDiff | null>(null);
  const action = useAction(() => {});

  const compare = () =>
    action.run(async () => {
      setResult(await apiFetch<SchemaDiff>(path, { method: 'POST', json: { schema: data?.schema, desired } }));
    });

  const create = (draft: MigrationDraft) =>
    action.run(async () => {
      const m = await apiFetch<Migration>(`/api/databases/${dbID}/migrations`, { method: 'POST', json: draft });
      navigate(`/databases/${dbID}/migrations/${m.id}`);
    });

  if (loading && !data) return <Loading />;

  return (
    <div>
      <div className="breadcrumbs">
        <Link to="/databases">Database</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}`}>{dbName || 'Schema'}</Link>
        <span>/</span>
        <Link to={`/databases/${dbID}/migrations`}>Migration</Link>
        <span>/</span>
        <span>Schema diff</span>
      </div>
      <ErrorBanner message={error} />
      {!canEdit && <ErrorBanner message="Schema diff butuh akses View + Edit." />}

      <div className="card">
        <h2 className="h2">Buat migration dari schema diff</h2>
        <p className="muted">
          Ubah DDL di bawah menjadi schema yang diinginkan, lalu tekan Bandingkan. DDL dijalankan sebagai tabel
          temporary di transaction yang selalu di-rollback. Yang dibandingkan hanya tabel, kolom, constraint, dan
          index.
        </p>
        {data && (
          <label className="field">
            Schema{' '}
            <select className="input" style={{ width: 'auto' }} value={data.schema} onChange={(e) => setSchema(e.target.value)}>
              {data.schemas.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
        )}
        <textarea
          value={desired}
          onChange={(e) => setDesired(e.target.value)}
          rows={16}
          spellCheck={false}
          className="input"
          style={sqlInput}
          aria-label="Schema yang diinginkan"
        />
        <div className="row" style={{ marginTop: '0.5rem' }}>
          <button className="btn btn-primary" type="button" disabled={action.busy || !canEdit} onClick={compare}>
            Bandingkan
          </button>
        </div>
        <ErrorBanner message={action.error} />
      </div>

      {result && (
        <div className="card">
          <h2 className="h2">Perbedaan</h2>
          {result.changes.length === 0 ? (
            <p className="muted">Tidak ada perbedaan.</p>
          ) : (
            <div className="table-responsive-wrapper">
              <table>
                <thead>
                  <tr>
                    <th>Aksi</th>
                    <th>Objek</th>
                    <th>Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {result.changes.map((c, i) => (
                    <tr key={i}>
                      <td>
                        {c.action}
                        {c.destructive && <strong> (menghapus data)</strong>}
                      </td>
                      <td>{c.object}</td>
                      <td>{c.detail}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {result.sql && (
            <>
              <h2 className="h2" style={{ marginTop: '1rem' }}>
                Simpan sebagai migration
              </h2>
              <p className="muted">
                Kandidat SQL masih bisa diedit. Migration baru tetap melewati review, approval, dan antrean.
              </p>
              <DraftForm
                key={result.sql}
                initial={{ ...emptyDraft, title: `Schema diff ${result.schema}`, sql_up: result.sql }}
                submitLabel="Simpan migration"
                busy={action.busy}
                onSubmit={create}
                onCancel={() => setResult(null)}
              />
            </>
          )}
        </div>
      )}
    </div>
  );
};
