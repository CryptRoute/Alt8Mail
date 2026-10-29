import fs from 'node:fs';
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const MODE = process.argv[2] || 'create';
const NAME = 'selfsigned-invcheck-a8m-test';
class Session {
  constructor() { this.jar = new Map(); this.csrf = ''; }
  store(response) { for (const cookie of response.headers.getSetCookie ? response.headers.getSetCookie() : []) { const [pair] = cookie.split(';'); const i = pair.indexOf('='); if (i < 0) continue; const k = pair.slice(0, i).trim(); const v = pair.slice(i + 1); if (/max-age=0/i.test(cookie) || /expires=thu, 01 jan 1970/i.test(cookie)) this.jar.delete(k); else this.jar.set(k, v); } }
  async req(path, { method = 'GET', body, expected = 200 } = {}) { const headers = { Accept: 'application/json' }; if (body !== undefined) headers['Content-Type'] = 'application/json'; if (this.csrf) headers['X-Alt8Mail-CSRF'] = this.csrf; if (this.jar.size) headers.Cookie = Array.from(this.jar.entries()).map(([k, v]) => `${k}=${v}`).join('; '); const response = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' }); this.store(response); const data = await response.json().catch(() => ({})); if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status} ${data?.error?.message || ''}`); return data; }
  async login() { const s = await this.req('/control/api/session'); this.csrf = s.csrfToken; const l = await this.req('/control/api/login', { method: 'POST', body: { email: 'sysadmin@alt8mail.local', password: PASSWORD } }); this.csrf = l.csrfToken || this.csrf; }
}
const session = new Session();
await session.login();
if (MODE === 'create') {
  const created = await session.req('/control/api/tls/self-signed', { method: 'POST', expected: 201, body: { commonName: 'invcheck.a8m.test', sans: 'invcheck.a8m.test', force: true } });
  const listed = await session.req('/control/api/tls/certificates');
  const found = listed.certificates.find((entry) => entry.name === NAME);
  if (!found) throw new Error('new self-signed certificate missing from inventory');
  console.log(JSON.stringify({ mode: 'create', listed: Boolean(found), name: found.name, selfSigned: found.selfSigned, certPath: found.certPath, keyPath: found.keyPath, sans: found.sans, daysLeft: found.daysLeft }));
} else {
  const listed = await session.req('/control/api/tls/certificates');
  const found = listed.certificates.find((entry) => entry.name === NAME);
  if (found) throw new Error('removed self-signed certificate still present in inventory');
  console.log(JSON.stringify({ mode: 'verify-pruned', removedFromInventory: true, certificates: listed.certificates.map((entry) => entry.name) }));
}
