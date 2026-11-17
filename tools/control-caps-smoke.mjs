import fs from 'node:fs';
import crypto from 'node:crypto';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 4)}`.toLowerCase();
const domain = `cap-${suffix}.a8m.test`;
let domainId = '';
let mailboxId = '';
let mailboxEmail = '';
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
try {
  const tenants = await main.req('/control/api/tenants');
  const tenantId = tenants.tenants?.[0]?.id;
  const created = await main.req('/control/api/domains', {
    method: 'POST', expected: 201,
    body: { domain, tenantId, isPrimary: false, selector: `cap${suffix}`, createPostmaster: false },
  });
  domainId = created.id;
  await main.req(`/control/api/domains/${domainId}`, {
    method: 'PATCH', body: { capsEnabled: true, capHour: 1, capDay: 0, capMonth: 0, burst: 0 },
  });
  const mailbox = await main.req('/control/api/mailboxes', {
    method: 'POST', expected: 201,
    body: { name: 'Cap Test Sender', localPart: 'sender', domainId, quotaBytes: 1073741824, password: crypto.randomBytes(18).toString('hex'), sendTo: '' },
  });
  mailboxId = mailbox.id;
  mailboxEmail = mailbox.email;
  const first = await main.req('/control/api/delivery/smtp-tests', {
    method: 'POST',
    body: { from: mailboxEmail, recipient: 'postmaster@a8m.test', subject: `Cap test first ${suffix}`, body: 'First message inside the rolling allowance.' },
  });
  if (!first.ok) throw new Error(`first SMTP test failed: ${first.error || first.status}`);
  const second = await main.req('/control/api/delivery/smtp-tests', {
    method: 'POST',
    body: { from: mailboxEmail, recipient: 'postmaster@a8m.test', subject: `Cap test second ${suffix}`, body: 'Second message should be deferred by the rolling cap.' },
  });
  console.log('cap first', JSON.stringify({ ok: first.ok, status: first.status }));
  console.log('cap second', JSON.stringify({ ok: second.ok, status: second.status, error: second.error || '' }));
  if (second.ok) throw new Error('second message was not deferred by the hourly cap');
  if (!String(second.error || '').toLowerCase().includes('hourly cap')) throw new Error(`unexpected cap error: ${second.error}`);
  console.log('rolling cap enforced');
} finally {
  try {
    if (mailboxId) {
      await main.req(`/control/api/mailboxes/${mailboxId}`, { method: 'DELETE', body: { confirmEmail: mailboxEmail } });
      mailboxId = '';
    }
  } catch (error) { console.log('cleanup mailbox', error.message); }
  try {
    if (domainId) {
      const preview = await main.req(`/control/api/domains/${domainId}/delete-preview`);
      await main.req(`/control/api/domains/${domainId}`, { method: 'DELETE', body: { confirmDomain: preview.domain, confirmToken: preview.confirmToken } });
      domainId = '';
    }
  } catch (error) { console.log('cleanup domain', error.message); }
}
