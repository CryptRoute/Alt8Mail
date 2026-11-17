import fs from 'node:fs';
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
class Session {
  constructor() { this.jar = new Map(); this.csrf = ''; }
  store(response) { for (const cookie of response.headers.getSetCookie ? response.headers.getSetCookie() : []) { const [pair] = cookie.split(';'); const i = pair.indexOf('='); if (i < 0) continue; const k = pair.slice(0, i).trim(); const v = pair.slice(i + 1); if (/max-age=0/i.test(cookie) || /expires=thu, 01 jan 1970/i.test(cookie)) this.jar.delete(k); else this.jar.set(k, v); } }
  async req(path, { method = 'GET', body, expected = 200 } = {}) { const headers = { Accept: 'application/json' }; if (body !== undefined) headers['Content-Type'] = 'application/json'; if (this.csrf) headers['X-Alt8Mail-CSRF'] = this.csrf; if (this.jar.size) headers.Cookie = Array.from(this.jar.entries()).map(([k, v]) => `${k}=${v}`).join('; '); const response = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual' }); this.store(response); const data = await response.json().catch(() => ({})); if (response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status} ${data?.error?.message || ''}`); return data; }
  async login() { const s = await this.req('/control/api/session'); this.csrf = s.csrfToken; const l = await this.req('/control/api/login', { method: 'POST', body: { email: 'sysadmin@alt8mail.local', password: PASSWORD } }); this.csrf = l.csrfToken || this.csrf; }
}
const main = new Session();
await main.login();
const server = await main.req('/control/api/server/settings', { method: 'PUT', body: { hostname: 'mail.a8m.test', mailHostname: 'mail.a8m.test', webmailUrl: 'https://130-94-46-22.sslip.io', dnsResolver: '1.1.1.1:53', publicIpv4: '130.94.46.22', publicIpv6: '', mailFrom: 'postmaster@a8m.test', applyHostname: false } });
console.log('server settings', JSON.stringify(server));
const tls = await main.req('/control/api/tls/settings', { method: 'PUT', body: { adminEmail: 'postmaster@a8m.test', warnDays: 14 } });
console.log('tls settings', JSON.stringify(tls));
const wrapper = await main.req('/control/api/tls/renewal-wrapper', { method: 'POST' });
console.log('renewal wrapper', JSON.stringify(wrapper));
console.log('settings verified', JSON.stringify({ server: (await main.req('/control/api/server/settings')).settings, tls: await main.req('/control/api/tls/settings'), wrapper: await main.req('/control/api/tls/renewal-wrapper') }));
