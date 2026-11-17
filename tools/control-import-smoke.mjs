import fs from 'node:fs';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const MBOX = process.env.IMPORT_MBOX;
class Session {
  constructor() { this.jar = new Map(); this.csrf = ''; }
  store(response) {
    for (const cookie of response.headers.getSetCookie ? response.headers.getSetCookie() : []) {
      const [pair] = cookie.split(';'); const index = pair.indexOf('=');
      if (index < 0) continue;
      const name = pair.slice(0, index).trim(); const value = pair.slice(index + 1);
      if (/max-age=0/i.test(cookie) || /expires=thu, 01 jan 1970/i.test(cookie)) this.jar.delete(name); else this.jar.set(name, value);
    }
  }
  async req(path, { method = 'GET', body, expected = 200 } = {}) {
    const headers = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.csrf) headers['X-Alt8Mail-CSRF'] = this.csrf;
    if (this.jar.size) headers.Cookie = Array.from(this.jar.entries()).map(([k, v]) => `${k}=${v}`).join('; ');
    const response = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' });
    this.store(response);
    const data = await response.json().catch(() => ({}));
    if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status} ${data?.error?.message || ''}`);
    return data;
  }
  async login() {
    const initial = await this.req('/control/api/session');
    this.csrf = initial.csrfToken;
    const login = await this.req('/control/api/login', { method: 'POST', body: { email: 'sysadmin@alt8mail.local', password: PASSWORD } });
    this.csrf = login.csrfToken || this.csrf;
  }
}
const main = new Session();
await main.login();
let jobId = '';
try {
  const created = await main.req('/control/api/imports', {
    method: 'POST', expected: 201,
    body: { kind: 'mbox', targetMailbox: 'postmaster@a8m.test', path: MBOX },
  });
  jobId = created.id;
  let job = null;
  for (let i = 0; i < 90; i += 1) {
    await new Promise((r) => setTimeout(r, 1000));
    const payload = await main.req(`/control/api/imports/${jobId}`);
    job = payload.job;
    if (job.status === 'completed' || job.status === 'failed' || job.status === 'stopped') break;
  }
  console.log('import job', JSON.stringify({ status: job?.status, progress: job?.progress, messagesImported: job?.messagesImported, error: job?.error || '' }));
  if (!job || job.status !== 'completed') throw new Error(`import did not complete: ${job?.status} ${job?.error || ''}`);
  if (job.messagesImported < 1) throw new Error('import reported zero messages');
  if (!/import complete|imported/i.test(String(job.log || ''))) throw new Error('import log does not show completion');
  await main.req(`/control/api/imports/${jobId}`, { method: 'DELETE' });
  jobId = '';
  console.log('import smoke passed');
} finally {
  if (jobId) await main.req(`/control/api/imports/${jobId}`, { method: 'DELETE' }).catch(() => {});
}
