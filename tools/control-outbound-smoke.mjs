import fs from 'node:fs';
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const suffix = Date.now().toString(36);
const transport = `smtp-ctl${suffix}`.slice(0, 24);
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
let ipId = '';
let routeId = '';
try {
  const ip = await main.req('/control/api/delivery/outbound-ips', {
    method: 'POST', expected: 201,
    body: { address: '130.94.46.22', label: `Control test ${suffix}`, mode: 'backup', priority: 50, interface: 'eth0', heloName: 'mail.a8m.test', active: true, notes: 'Automated test' },
  });
  ipId = ip.id;
  const route = await main.req('/control/api/delivery/sender-routes', {
    method: 'POST', expected: 201,
    body: { senderDomain: 'a8m.test', sourceIpId: ipId, heloName: 'mail.a8m.test', transportName: transport, active: true },
  });
  routeId = route.id;
  const config = await main.req('/control/api/services/config?file=master.cf');
  const hasTransport = String(config.content || '').includes(transport);
  const hasHelo = String(config.content || '').includes('smtp_helo_name=mail.a8m.test');
  console.log('outbound route applied', JSON.stringify({ transport, hasTransport, hasHelo }));
  if (!hasTransport || !hasHelo) throw new Error('sender transport was not rendered into master.cf');
  await main.req(`/control/api/delivery/sender-routes/${routeId}`, { method: 'DELETE' });
  routeId = '';
  await main.req(`/control/api/delivery/outbound-ips/${ipId}`, { method: 'DELETE' });
  ipId = '';
  const after = await main.req('/control/api/services/config?file=master.cf');
  const removed = !String(after.content || '').includes(transport);
  console.log('outbound route removed', JSON.stringify({ removed }));
  if (!removed) throw new Error('transport block remained after route deletion');
  console.log('outbound routing smoke passed');
} finally {
  if (routeId) await main.req(`/control/api/delivery/sender-routes/${routeId}`, { method: 'DELETE' }).catch(() => {});
  if (ipId) await main.req(`/control/api/delivery/outbound-ips/${ipId}`, { method: 'DELETE' }).catch(() => {});
}
