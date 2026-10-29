import fs from 'node:fs';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();

class Session {
  constructor() { this.jar = new Map(); this.csrf = ''; }
  store(response) {
    for (const cookie of response.headers.getSetCookie ? response.headers.getSetCookie() : []) {
      const [pair] = cookie.split(';');
      const index = pair.indexOf('=');
      if (index < 0) continue;
      const name = pair.slice(0, index).trim();
      const value = pair.slice(index + 1);
      if (/max-age=0/i.test(cookie) || /expires=thu, 01 jan 1970/i.test(cookie)) this.jar.delete(name);
      else this.jar.set(name, value);
    }
  }
  async req(path, { method = 'GET', body, csrf = this.csrf, expected = 200 } = {}) {
    const headers = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (csrf) headers['X-Alt8Mail-CSRF'] = csrf;
    if (this.jar.size) headers.Cookie = Array.from(this.jar.entries()).map(([k, v]) => `${k}=${v}`).join('; ');
    const response = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' });
    this.store(response);
    const data = await response.json().catch(() => ({}));
    if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status} ${data?.error?.message || ''}`);
    return data;
  }
  async login() {
    const session = await this.req('/control/api/session', { csrf: '' });
    this.csrf = session.csrfToken;
    const login = await this.req('/control/api/login', { method: 'POST', body: { email: 'sysadmin@alt8mail.local', password: PASSWORD } });
    this.csrf = login.csrfToken || this.csrf;
  }
}

const main = new Session();
await main.login();
const current = await main.req('/control/api/services/ports');
console.log('current', current.listeners.map((listener) => `${listener.protocol}:${listener.port}/${listener.tls}${listener.saslRequired ? '/sasl' : ''}`).join(' '));
const preview = await main.req('/control/api/services/ports/preview', { method: 'POST', body: { listeners: current.listeners } });
const masterAfter = preview.master.after;
const forbidden = ['143 inet', '993 inet', '110 inet', '995 inet'].filter((needle) => masterAfter.includes(needle));
if (forbidden.length) throw new Error(`Postfix renderer includes non-SMTP listeners: ${forbidden.join(', ')}`);
console.log('preview', `masterDiff=${preview.master.diff.length} dovecotDiff=${preview.dovecot.diff.length}`);
console.log('master preview snippet', masterAfter.split('\n').filter((line) => /^smtp|^submission|^smtps|^465|^587|^25\b/.test(line)).slice(0, 8).join(' | '));
const saved = await main.req('/control/api/services/ports', { method: 'PUT', body: { listeners: current.listeners } });
console.log('saved', JSON.stringify({ ok: saved.ok, listeners: saved.listeners.length }));
const after = await main.req('/control/api/services/ports');
console.log('after', after.listeners.map((listener) => `${listener.protocol}:${listener.port}/${listener.tls}`).join(' '));
const smtp = await main.req('/control/api/delivery/smtp-tests', {
  method: 'POST',
  body: { recipient: 'postmaster@meridian.test', from: 'postmaster@meridian.test', subject: `Listener save delivery test ${new Date().toISOString()}`, body: 'Listener configuration save verification.' },
});
console.log('smtp test', JSON.stringify({ ok: smtp.ok, status: smtp.status, error: smtp.error || '' }));
if (!smtp.ok) process.exitCode = 1;
