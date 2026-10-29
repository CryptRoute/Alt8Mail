import fs from 'node:fs';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
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
    if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status} ${data?.error?.message || ''} ${String(data?.error?.details || '').slice(0, 500)}`);
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
const before = await main.req('/control/api/tls/certificates');
const letsEncrypt = before.certificates.find((cert) => /let's encrypt|r3|r10|r11|e5|e6/i.test(cert.issuer) && !cert.selfSigned) || before.certificates.find((cert) => !cert.selfSigned);
if (!letsEncrypt) {
  console.log('No CA-issued certificate found for dry-run.');
  process.exit(0);
}
console.log('dry-run target', JSON.stringify({ name: letsEncrypt.name, notAfter: letsEncrypt.notAfter, issuer: letsEncrypt.issuer }));
const started = Date.now();
try {
  const result = await main.req('/control/api/tls/renew', { method: 'POST', body: { name: letsEncrypt.name, dryRun: true }, expected: 200 });
  console.log('dry-run ok', JSON.stringify({ elapsedMs: Date.now() - started, output: String(result.output || '').split('\n').slice(-8).join(' | ').slice(0, 1200) }));
} catch (error) {
  console.log('dry-run failed', error.message);
  process.exitCode = 1;
}
const after = await main.req('/control/api/tls/certificates');
const same = after.certificates.find((cert) => cert.name === letsEncrypt.name);
console.log('certificate unchanged', JSON.stringify({ before: letsEncrypt.notAfter, after: same?.notAfter, unchanged: same?.notAfter === letsEncrypt.notAfter }));
