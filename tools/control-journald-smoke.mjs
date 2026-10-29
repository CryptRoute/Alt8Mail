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
const original = await main.req('/control/api/logs/settings');
console.log('original', JSON.stringify(original));
try {
  const invalid = await main.req('/control/api/logs/settings', { method: 'PUT', body: { source: 'not-a-source', retentionDays: original.retentionDays }, expected: 400 });
  console.log('invalid source rejected');
  await main.req('/control/api/logs/settings', { method: 'PUT', body: { source: 'journald', retentionDays: original.retentionDays } });
  console.log('switched to journald');
  await new Promise((r) => setTimeout(r, 2500));
  const send = await main.req('/control/api/delivery/smtp-tests', {
    method: 'POST',
    body: { from: 'postmaster@meridian.test', recipient: 'postmaster@meridian.test', subject: `Journald ingestion test ${suffix}`, body: 'Testing journald ingestion source.' },
  });
  const match = String(send.transcript || '').match(/queued as ([A-Fa-f0-9]+)/);
  const queueId = match ? match[1] : '';
  console.log('smtp', JSON.stringify({ ok: send.ok, queueId }));
  let event = null;
  for (let i = 0; i < 20; i += 1) {
    await new Promise((r) => setTimeout(r, 1500));
    const logs = await main.req(`/control/api/logs?q=${encodeURIComponent(queueId || 'postmaster@meridian.test')}&limit=50`);
    event = logs.events?.find((entry) => entry.source === 'journald' && (!queueId || entry.queueId === queueId));
    if (event) break;
  }
  if (!event) throw new Error('no journald-sourced event ingested');
  console.log('journald event', JSON.stringify({ source: event.source, queueId: event.queueId, status: event.status, direction: event.direction }));
} finally {
  await main.req('/control/api/logs/settings', { method: 'PUT', body: { source: original.source, retentionDays: original.retentionDays } }).catch((error) => console.log('restore failed', error.message));
  console.log('restored source', original.source);
}
