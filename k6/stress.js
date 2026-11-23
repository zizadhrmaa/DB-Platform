// Stress test k6 untuk DB Platform (bonus Milestone 2).
//
// Dua skenario berjalan bersamaan dengan beban yang dinaikkan bertahap di
// atas beban normal lalu diturunkan kembali:
//   api  : user membaca API (daftar database, schema, data tabel, migration,
//          job, riwayat).
//   jobs : user mengirim job migration. Setiap iterasi membuat migration,
//          request, submit, approve (sampai semua leader setuju), enqueue,
//          lalu memantau job sampai selesai. Database dipilih bergiliran
//          sehingga ada job bersamaan di database yang sama maupun berbeda.
//
// Respons enqueue (202) TIDAK dihitung sebagai keberhasilan migration; job
// baru dihitung berhasil setelah statusnya succeeded.
//
// Jalankan lewat k6/run.sh (menyiapkan platform dan database target khusus).

import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8081';
const ADMIN_USER = __ENV.ADMIN_USERNAME || 'admin';
const ADMIN_PASS = __ENV.ADMIN_PASSWORD;
const TARGET_HOST = __ENV.TARGET_HOST || 'localhost';
const TARGET_PORT = parseInt(__ENV.TARGET_PORT || '55432', 10);
const TARGET_DBS = (__ENV.TARGET_DBS || 'k6_a,k6_b,k6_c').split(',');
const TARGET_USER = __ENV.TARGET_USER || 'postgres';
const TARGET_PASS = __ENV.TARGET_PASS || 'postgres';
const SCALE = parseFloat(__ENV.SCALE || '1');
const STAGE_SECONDS = parseInt(__ENV.STAGE_SECONDS || '60', 10);

// Beban normal yang ditetapkan tim: 10 VU pembaca API + 2 VU pengirim job.
// Tahap: normal -> 2x -> 4x -> kembali normal (pemulihan) -> 0.
const STAGES = [
  { name: 'normal', duration: STAGE_SECONDS, api: 10, jobs: 2 },
  { name: 'tinggi_2x', duration: STAGE_SECONDS, api: 20, jobs: 4 },
  { name: 'puncak_4x', duration: STAGE_SECONDS, api: 40, jobs: 8 },
  { name: 'pemulihan', duration: STAGE_SECONDS, api: 10, jobs: 2 },
];
const RAMP = 15; // detik transisi antar tahap

function rampingStages(key) {
  const out = [];
  for (const s of STAGES) {
    out.push({ duration: `${RAMP}s`, target: Math.max(1, Math.round(s[key] * SCALE)) });
    out.push({ duration: `${s.duration}s`, target: Math.max(1, Math.round(s[key] * SCALE)) });
  }
  out.push({ duration: `${RAMP}s`, target: 0 });
  return out;
}

// Tahap aktif dihitung dari waktu sejak skenario mulai, untuk tag per tahap.
function stageNow() {
  let start;
  try {
    start = exec.scenario.startTime;
  } catch (_) {
    return 'setup'; // setup/teardown berjalan di luar skenario
  }
  let t = (Date.now() - start) / 1000;
  for (const s of STAGES) {
    if (t < RAMP) return `naik_${s.name}`;
    t -= RAMP;
    if (t < s.duration) return s.name;
    t -= s.duration;
  }
  return 'turun';
}

const stageThresholds = {};
for (const s of STAGES) {
  // Threshold per tahap juga membuat sub-metrik muncul di ringkasan.
  stageThresholds[`http_req_duration{scenario:api,stage:${s.name}}`] = ['p(95)<1000'];
  stageThresholds[`http_req_failed{stage:${s.name}}`] = ['rate<0.05'];
  stageThresholds[`http_reqs{stage:${s.name}}`] = ['count>0'];
  stageThresholds[`job_queue_wait_ms{stage:${s.name}}`] = ['p(95)>=0'];
  stageThresholds[`job_duration_ms{stage:${s.name}}`] = ['p(95)>=0'];
  stageThresholds[`job_e2e_ms{stage:${s.name}}`] = ['p(95)>=0'];
  stageThresholds[`jobs_succeeded{stage:${s.name}}`] = ['count>=0'];
  stageThresholds[`job_success{stage:${s.name}}`] = ['rate>=0'];
}

export const options = {
  setupTimeout: '120s',
  teardownTimeout: '120s',
  scenarios: {
    api: { executor: 'ramping-vus', exec: 'apiUser', startVUs: 0, stages: rampingStages('api'), gracefulRampDown: '10s' },
    jobs: { executor: 'ramping-vus', exec: 'jobUser', startVUs: 0, stages: rampingStages('jobs'), gracefulRampDown: '120s' },
  },
  thresholds: {
    // Threshold utama tim.
    'http_req_duration{scenario:api}': ['p(95)<500'],
    http_req_failed: ['rate<0.01'],
    job_success: ['rate>0.99'],
    ...stageThresholds,
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

const queueWait = new Trend('job_queue_wait_ms', true);
const jobDuration = new Trend('job_duration_ms', true);
const jobE2E = new Trend('job_e2e_ms', true);
const jobsSucceeded = new Counter('jobs_succeeded');
const jobsFailed = new Counter('jobs_failed');
const jobSuccess = new Rate('job_success');
const enqueueConflicts = new Counter('enqueue_conflicts');

const json = (body) => JSON.stringify(body);
const expect409 = http.expectedStatuses({ min: 200, max: 299 }, 409);

function client(session) {
  const headers = {
    'Content-Type': 'application/json',
    Cookie: `dbp_session=${session.cookie}`,
    'X-CSRF-Token': session.csrf,
  };
  const tags = (name, extra) => ({ name, stage: stageNow(), ...(extra || {}) });
  return {
    get: (path, name) => http.get(`${BASE}${path}`, { headers, tags: tags(name) }),
    post: (path, body, name, params) =>
      http.post(`${BASE}${path}`, json(body || {}), { headers, tags: tags(name), ...(params || {}) }),
    put: (path, body, name) => http.put(`${BASE}${path}`, json(body || {}), { headers, tags: tags(name) }),
  };
}

function must(res, _status, what) {
  if (res.status < 200 || res.status > 299) {
    throw new Error(`${what}: status ${res.status} ${res.body}`);
  }
  return res.body ? res.json() : null;
}

// setup: login admin, siapkan group khusus k6 (admin sebagai anggota dan
// leader) dengan akses View + Edit ke database target k6. Hanya group ini
// yang punya akses, jadi approval leader-nya cukup.
export function setup() {
  if (!ADMIN_PASS) throw new Error('ADMIN_PASSWORD wajib diisi');
  const login = http.post(`${BASE}/api/auth/login`, json({ username: ADMIN_USER, password: ADMIN_PASS }), {
    // Sebelum ada session, server memeriksa Origin (proteksi CSRF login),
    // seperti yang dikirim browser.
    headers: { 'Content-Type': 'application/json', Origin: BASE },
  });
  if (login.status !== 200) throw new Error(`login gagal: ${login.status} ${login.body}`);
  const cookie = login.cookies.dbp_session[0].value;
  const me = http.get(`${BASE}/api/auth/me`, { headers: { Cookie: `dbp_session=${cookie}` } }).json();
  const session = { cookie, csrf: me.csrf_token };
  const c = client(session);

  const group = must(c.post('/api/admin/groups', { name: `k6-${Date.now()}`, description: 'stress test k6' }, 'setup'), 201, 'buat group');
  must(c.post(`/api/admin/groups/${group.id}/members`, { user_id: me.id }, 'setup'), 204, 'tambah anggota');
  must(c.put(`/api/admin/groups/${group.id}/leader`, { user_id: me.id }, 'setup'), 204, 'set leader');

  const dbs = [];
  for (const name of TARGET_DBS) {
    const conn = must(
      c.post('/api/admin/databases', {
        name: `${name}-${Date.now()}`, db_type: 'postgres', host: TARGET_HOST, port: TARGET_PORT,
        database: name, username: TARGET_USER, password: TARGET_PASS, sslmode: 'disable', is_active: true,
      }, 'setup'),
      201, 'buat koneksi',
    );
    must(c.put(`/api/admin/groups/${group.id}/access/${conn.id}`, { level: 'edit' }, 'setup'), 204, 'grant akses');
    dbs.push(conn.id);
  }
  // Tabel kecil untuk dibaca skenario api.
  return { session, dbs };
}

const READS = [
  (c, db) => c.get('/api/databases', 'GET /databases'),
  (c, db) => c.get(`/api/databases/${db}`, 'GET /databases/{id}'),
  (c, db) => c.get(`/api/databases/${db}/migrations`, 'GET /migrations'),
  (c, db) => c.get(`/api/databases/${db}/jobs`, 'GET /jobs'),
  (c, db) => c.get(`/api/databases/${db}/history?hide_rollback=true`, 'GET /history'),
  (c, db) => c.get(`/api/databases/${db}/requests`, 'GET /requests'),
  (c, db) => c.get(`/api/databases/${db}/tables/public/k6_items/data?page=1&size=50`, 'GET /tables/.../data'),
];

export function apiUser(data) {
  const c = client(data.session);
  const db = data.dbs[(exec.vu.idInTest + exec.vu.iterationInScenario) % data.dbs.length];
  const read = READS[exec.vu.iterationInScenario % READS.length];
  const res = read(c, db);
  check(res, { 'api 200': (r) => r.status === 200 });
  sleep(0.5 + Math.random() * 0.5); // think time
}

const terminal = ['succeeded', 'failed', 'reverted'];

export function jobUser(data) {
  const c = client(data.session);
  const db = data.dbs[(exec.vu.idInTest + exec.vu.iterationInScenario) % data.dbs.length];
  const tag = `k6_${exec.vu.idInTest}_${exec.vu.iterationInScenario}_${Date.now()}`;
  const base = `/api/databases/${db}`;

  // 1. Migration dengan SQL up/down yang tidak bisa gagal.
  const mres = c.post(`${base}/migrations`, {
    title: `k6 ${tag}`,
    sql_up: `CREATE TABLE IF NOT EXISTS ${tag} (id INT PRIMARY KEY); INSERT INTO k6_items (note) VALUES ('${tag}')`,
    sql_down: `DROP TABLE IF EXISTS ${tag}`,
    exec_mode: 'normal',
  }, 'POST /migrations');
  if (!check(mres, { 'migration dibuat': (r) => r.status === 201 })) return;
  const mig = mres.json();

  // 2-4. Request, submit, approve oleh leader.
  const rres = c.post(`${base}/migrations/${mig.id}/requests`, { kind: 'up' }, 'POST /requests');
  if (!check(rres, { 'request dibuat': (r) => r.status === 201 })) return;
  const req = rres.json();
  const okSubmit = check(c.post(`${base}/requests/${req.id}/submit`, {}, 'POST /submit'), { 'submit 200': (r) => r.status === 200 });
  const okApprove = check(c.post(`${base}/requests/${req.id}/approve`, {}, 'POST /approve'), { 'approve 200': (r) => r.status === 200 });
  if (!okSubmit || !okApprove) return giveUp(c, base, req.id, mig.id);

  // 5. Enqueue. 409 berarti migration versi sebelumnya di database ini
  //    belum selesai (urutan linear dijaga server); coba lagi.
  let job = null;
  const deadline = Date.now() + 90000;
  while (Date.now() < deadline) {
    const e = c.post(`${base}/requests/${req.id}/enqueue`, {}, 'POST /enqueue', { responseCallback: expect409 });
    if (e.status === 202) {
      job = e.json();
      break;
    }
    if (e.status !== 409) break;
    enqueueConflicts.add(1, { stage: stageNow() });
    sleep(0.3);
  }
  if (!check(job, { 'job di-enqueue': (j) => j !== null })) return giveUp(c, base, req.id, mig.id);

  // 6. Pantau job sampai status terminal.
  const pollDeadline = Date.now() + 180000;
  let j = null;
  while (Date.now() < pollDeadline) {
    const r = c.get(`${base}/jobs/${job.job_id}`, 'GET /jobs/{id} (poll)');
    if (r.status === 200) {
      j = r.json();
      if (terminal.includes(j.status)) break;
    }
    sleep(0.25);
  }
  const stage = stageNow();
  const ok = j !== null && j.status === 'succeeded';
  jobSuccess.add(ok, { stage });
  if (!ok) {
    jobsFailed.add(1, { stage });
    console.warn(`job ${job.job_id} tidak sukses: ${j ? j.status + ' ' + j.error_message : 'timeout'}`);
    return;
  }
  jobsSucceeded.add(1, { stage });
  const created = Date.parse(j.created_at);
  const started = Date.parse(j.started_at);
  const finished = Date.parse(j.finished_at);
  queueWait.add(started - created, { stage });
  jobDuration.add(finished - started, { stage });
  jobE2E.add(finished - created, { stage });
}

// giveUp membatalkan request dan migration iterasi ini supaya tidak
// menahan urutan migration berikutnya di database yang sama.
function giveUp(c, base, reqID, migID) {
  c.post(`${base}/requests/${reqID}/cancel`, {}, 'cleanup');
  c.post(`${base}/migrations/${migID}/discard`, {}, 'cleanup');
}

export function handleSummary(data) {
  return {
    stdout: textSummary(data),
    'k6/hasil/summary.json': JSON.stringify(data, null, 2),
  };
}

// Ringkasan per tahap dalam bentuk tabel markdown.
function textSummary(data) {
  const m = data.metrics;
  const v = (name, stat) => (m[name] && m[name].values[stat] !== undefined ? m[name].values[stat] : NaN);
  const f = (x, d = 1) => (Number.isFinite(x) ? x.toFixed(d) : '-');
  const lines = [];
  lines.push('| Tahap | VU api/jobs | Req | Req/s | p50 API (ms) | p95 API (ms) | Error rate | Job sukses | Job/menit | Queue wait p50/p95 (ms) | Durasi job p50/p95 (ms) | End-to-end p95 (ms) |');
  lines.push('|---|---|---|---|---|---|---|---|---|---|---|---|');
  for (const s of STAGES) {
    const k = (name) => `${name}{stage:${s.name}}`;
    const reqs = v(k('http_reqs'), 'count');
    const dur = `http_req_duration{scenario:api,stage:${s.name}}`;
    const jobs = v(k('jobs_succeeded'), 'count');
    lines.push(`| ${s.name} | ${Math.round(s.api * SCALE)}/${Math.round(s.jobs * SCALE)} | ${f(reqs, 0)} | ${f(reqs / s.duration)} | ${f(v(dur, 'med'))} | ${f(v(dur, 'p(95)'))} | ${f(v(k('http_req_failed'), 'rate') * 100, 2)}% | ${f(v(k('job_success'), 'rate') * 100, 1)}% (${f(jobs, 0)}) | ${f((jobs / s.duration) * 60)} | ${f(v(k('job_queue_wait_ms'), 'med'), 0)}/${f(v(k('job_queue_wait_ms'), 'p(95)'), 0)} | ${f(v(k('job_duration_ms'), 'med'), 0)}/${f(v(k('job_duration_ms'), 'p(95)'), 0)} | ${f(v(k('job_e2e_ms'), 'p(95)'), 0)} |`);
  }
  lines.push('');
  lines.push(`Total: ${f(v('http_reqs', 'count'), 0)} request (${f(v('http_reqs', 'rate'))}/s), p95 API ${f(v('http_req_duration{scenario:api}', 'p(95)'))} ms, error rate ${f(v('http_req_failed', 'rate') * 100, 2)}%, job sukses ${f(v('jobs_succeeded', 'count'), 0)}, gagal ${f(v('jobs_failed', 'count'), 0)}, konflik enqueue (409, urutan dijaga) ${f(v('enqueue_conflicts', 'count'), 0)}.`);
  lines.push('');
  lines.push('Threshold:');
  for (const [name, mm] of Object.entries(m)) {
    if (!mm.thresholds || name.includes('{stage:')) continue;
    for (const [t, r] of Object.entries(mm.thresholds)) lines.push(`- ${name} ${t}: ${r.ok ? 'LULUS' : 'GAGAL'}`);
  }
  return lines.join('\n') + '\n';
}
