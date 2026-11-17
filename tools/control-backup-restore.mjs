import fs from 'node:fs';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const suffix = Date.now().toString(36);
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
let backupId = '';
try {
  const created = await main.req('/control/api/backups', { method: 'POST', expected: 202, body: { name: `control-restore-${suffix}`, contents: ['postfix'] } });
  backupId = created.id;
  let item;
  for (let i = 0; i < 40; i += 1) {
    await new Promise((r) => setTimeout(r, 1000));
    const list = await main.req('/control/api/backups');
    item = list.backups.find((entry) => entry.id === backupId);
    if (item && item.status !== 'running') break;
  }
  console.log('backup', JSON.stringify({ status: item?.status, size: item?.sizeBytes, contents: item?.contents }));
  if (item?.status !== 'completed') throw new Error('backup did not complete');
  const restored = await main.req(`/control/api/backups/${backupId}/restore`, { method: 'POST', body: { confirm: 'RESTORE', contents: ['postfix'] } });
  console.log('restore', JSON.stringify({ ok: restored.ok, restored: restored.restored, counts: restored.counts }));
  if (!restored.ok) throw new Error('restore failed');
  await main.req(`/control/api/backups/${backupId}`, { method: 'DELETE' });
  backupId = '';
  console.log('backup restore smoke passed');
} finally {
  if (backupId) await main.req(`/control/api/backups/${backupId}`, { method: 'DELETE' }).catch(() => {});
}
