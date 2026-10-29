import fs from 'node:fs';
import crypto from 'node:crypto';

process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const BASE = process.env.CONTROL_BASE || 'https://130.94.46.22:8808';
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const EMAIL = 'sysadmin@alt8mail.local';
const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 5)}`.toLowerCase();
const RUN = `control-test-${suffix}`;

const results = [];
const resources = {
  groupId: '', userId: '', aliasId: '', suppressionId: '', cronId: '', backupId: '',
  webhookId: '', handleId: '', apiKeyId: '', domainId: '', domainName: '', mailboxId: '', mailboxEmail: '',
  firewallPort: 65530,
};
let main;

function record(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}

function cleanError(error) {
  const message = String(error?.message || error || 'unknown error');
  return message
    .replace(/a8m_[A-Za-z0-9_-]+/g, 'a8m_[redacted]')
    .replace(/password[^,; ]*/ig, 'password=[redacted]')
    .slice(0, 500);
}

async function step(name, fn) {
  try {
    const detail = await fn();
    record(name, true, typeof detail === 'string' ? detail : '');
    return detail;
  } catch (error) {
    record(name, false, cleanError(error));
    return null;
  }
}

class Session {
  constructor(csrf = '') {
    this.jar = new Map();
    this.csrf = csrf;
  }
  store(response) {
    const cookies = response.headers.getSetCookie ? response.headers.getSetCookie() : [response.headers.get('set-cookie')].filter(Boolean);
    for (const cookie of cookies) {
      const [pair] = cookie.split(';');
      const index = pair.indexOf('=');
      if (index < 0) continue;
      const name = pair.slice(0, index).trim();
      const value = pair.slice(index + 1);
      if (/max-age=0/i.test(cookie) || /expires=thu, 01 jan 1970/i.test(cookie)) this.jar.delete(name);
      else this.jar.set(name, value);
    }
  }
  cookieHeader() {
    return Array.from(this.jar.entries()).map(([k, v]) => `${k}=${v}`).join('; ');
  }
  async req(path, { method = 'GET', body, csrf = this.csrf, headers = {}, raw = false, expected = 200 } = {}) {
    const requestHeaders = { Accept: 'application/json', ...headers };
    if (body !== undefined) requestHeaders['Content-Type'] = 'application/json';
    if (csrf) requestHeaders['X-Alt8Mail-CSRF'] = csrf;
    if (this.jar.size) requestHeaders.Cookie = this.cookieHeader();
    const response = await fetch(`${BASE}${path}`, {
      method,
      headers: requestHeaders,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: 'manual',
    });
    this.store(response);
    if (raw) {
      const data = Buffer.from(await response.arrayBuffer());
      if (expected !== null && response.status !== expected) throw new Error(`${method} ${path}: HTTP ${response.status}`);
      return { status: response.status, data, headers: response.headers };
    }
    const text = await response.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = text; }
    if (expected !== null && response.status !== expected) {
      const message = data?.error?.message || (typeof data === 'string' ? data.slice(0, 200) : '');
      throw new Error(`${method} ${path}: HTTP ${response.status}${message ? ` · ${message}` : ''}`);
    }
    return { status: response.status, data, headers: response.headers };
  }
  async init() {
    const response = await this.req('/control/api/session', { csrf: '', expected: 200 });
    this.csrf = response.data.csrfToken || '';
    return response.data;
  }
  async login(email, password) {
    await this.init();
    const response = await this.req('/control/api/login', { method: 'POST', body: { email, password } });
    this.csrf = response.data.csrfToken || this.csrf;
    return response.data;
  }
  async me() {
    return (await this.req('/control/api/me')).data;
  }
  async logout() {
    await this.req('/control/api/logout', { method: 'POST', expected: 200 });
    this.csrf = '';
  }
}

function base32Decode(value) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  const clean = value.replace(/=+$/, '').toUpperCase();
  let bits = '';
  for (const ch of clean) {
    const index = alphabet.indexOf(ch);
    if (index < 0) continue;
    bits += index.toString(2).padStart(5, '0');
  }
  const bytes = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(bytes);
}

function totp(secret) {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = crypto.createHmac('sha1', base32Decode(secret)).update(counter).digest();
  const offset = digest[digest.length - 1] & 0x0f;
  const code = ((digest[offset] & 0x7f) << 24) | (digest[offset + 1] << 16) | (digest[offset + 2] << 8) | digest[offset + 3];
  return String(code % 1000000).padStart(6, '0');
}

async function cleanup() {
  const safe = async (name, fn) => {
    try { await fn(); } catch (error) { console.log(`cleanup ${name}: ${cleanError(error)}`); }
  };
  if (!main) return;
  await safe('mailbox', async () => {
    if (!resources.mailboxId) return;
    await main.req(`/control/api/mailboxes/${resources.mailboxId}`, { method: 'DELETE', body: { confirmEmail: resources.mailboxEmail }, expected: 200 });
    resources.mailboxId = '';
  });
  await safe('domain', async () => {
    if (!resources.domainId) return;
    const preview = await main.req(`/control/api/domains/${resources.domainId}/delete-preview`);
    await main.req(`/control/api/domains/${resources.domainId}`, {
      method: 'DELETE', body: { confirmDomain: preview.data.domain, confirmToken: preview.data.confirmToken }, expected: 200,
    });
    resources.domainId = '';
  });
  await safe('api key', async () => {
    if (!resources.apiKeyId) return;
    await main.req(`/control/api/access/api-keys/${resources.apiKeyId}`, { method: 'DELETE', expected: 200 });
    resources.apiKeyId = '';
  });
  await safe('webhook', async () => {
    if (!resources.webhookId) return;
    await main.req(`/control/api/webhooks/${resources.webhookId}`, { method: 'DELETE', expected: 200 });
    resources.webhookId = '';
  });
  await safe('notification handle', async () => {
    if (!resources.handleId) return;
    await main.req(`/control/api/notifications/handles/${resources.handleId}`, { method: 'DELETE', expected: 200 });
    resources.handleId = '';
  });
  await safe('cron', async () => {
    if (!resources.cronId) return;
    await main.req(`/control/api/cron/${resources.cronId}`, { method: 'DELETE', expected: 200 });
    resources.cronId = '';
  });
  await safe('backup', async () => {
    if (!resources.backupId) return;
    await main.req(`/control/api/backups/${resources.backupId}`, { method: 'DELETE', expected: 200 });
    resources.backupId = '';
  });
  await safe('alias', async () => {
    if (!resources.aliasId) return;
    await main.req(`/control/api/aliases/${resources.aliasId}`, { method: 'DELETE', expected: 200 });
    resources.aliasId = '';
  });
  await safe('suppression', async () => {
    if (!resources.suppressionId) return;
    await main.req(`/control/api/delivery/suppressions/${resources.suppressionId}`, { method: 'DELETE', expected: 200 });
    resources.suppressionId = '';
  });
  await safe('firewall rule', async () => {
    if (resources.firewallNumber) {
      await main.req(`/control/api/server/firewall/${resources.firewallNumber}`, { method: 'DELETE', expected: 200 });
      resources.firewallNumber = '';
    }
  });
  await safe('temp user', async () => {
    if (!resources.userId) return;
    await main.req(`/control/api/access/users/${resources.userId}`, { method: 'DELETE', expected: 200 });
    resources.userId = '';
  });
  await safe('temp group', async () => {
    if (!resources.groupId) return;
    await main.req(`/control/api/access/groups/${resources.groupId}`, { method: 'DELETE', expected: 200 });
    resources.groupId = '';
  });
}

try {
  main = new Session();
  await step('unauthenticated_health', async () => {
    const health = await main.req('/healthz', { csrf: '', expected: 200 });
    if (!health.data?.ok) throw new Error('healthz did not return ok');
    const metrics = await main.req('/metrics', { csrf: '', expected: 200 });
    if (!String(metrics.data).includes('admin_info')) throw new Error('metrics missing admin_info');
    await main.req('/healthz/stacks', { csrf: '', expected: 401 });
    return 'healthz, metrics and auth gate verified';
  });

  await step('login_main', async () => {
    const payload = await main.login(EMAIL, PASSWORD);
    if (!payload.authenticated) throw new Error('login did not authenticate');
    const me = await main.me();
    if (me.user.email !== EMAIL) throw new Error('unexpected session user');
    return `role=${me.user.role}`;
  });

  await step('csrf_rejected', async () => {
    const response = await main.req('/control/api/config-sync/run', { method: 'POST', csrf: '', expected: 403 });
    if (response.status !== 403) throw new Error('CSRF bypass');
    return 'unsigned mutation rejected';
  });

  const existing = { domainId: '', mailboxId: '' };
  await step('read_only_endpoint_sweep', async () => {
    const domains = await main.req('/control/api/domains');
    existing.domainId = domains.data.domains?.[0]?.id || '';
    const boxes = await main.req('/control/api/mailboxes?includeDisabled=1');
    existing.mailboxId = boxes.data.mailboxes?.[0]?.id || '';
    const paths = [
      '/control/api/dashboard', '/control/api/diagnostics', '/control/api/config-sync',
      '/control/api/config-sync/history', '/control/api/tenants', '/control/api/aliases',
      '/control/api/tls/certificates', '/control/api/tls/settings', '/control/api/tls/renewal-wrapper',
      '/control/api/delivery/queue', '/control/api/delivery/deliverability', '/control/api/delivery/relay',
      '/control/api/delivery/outbound-ips', '/control/api/delivery/sender-routes',
      '/control/api/delivery/suppressions', '/control/api/delivery/blocklists',
      '/control/api/delivery/smtp-tests', '/control/api/services', '/control/api/services/ports',
      '/control/api/services/stack-ports', '/control/api/services/config?file=main.cf',
      '/control/api/logs', '/control/api/logs/settings', '/control/api/imports',
      '/control/api/backups', '/control/api/cron', '/control/api/server/settings',
      '/control/api/server/network', '/control/api/server/time', '/control/api/server/firewall',
      '/control/api/database', '/control/api/database/migrate', '/control/api/access/meta',
      '/control/api/access/users', '/control/api/access/groups', '/control/api/access/sessions',
      '/control/api/audit', '/control/api/access/api-keys', '/control/api/webhooks',
      '/control/api/webhooks/deliveries', '/control/api/notifications/handles',
      '/control/api/notifications/deliveries', '/control/api/notifications/events',
      '/control/api/me', '/control/api/me/sessions', '/control/api/access/api-keys',
    ];
    if (existing.domainId) paths.push(`/control/api/domains/${existing.domainId}`, `/control/api/domains/${existing.domainId}/dns`, `/control/api/domains/${existing.domainId}/delete-preview`);
    if (existing.mailboxId) paths.push(`/control/api/mailboxes/${existing.mailboxId}`, `/control/api/mailboxes/${existing.mailboxId}/history`, `/control/api/mailboxes/${existing.mailboxId}/sieve`);
    const failures = [];
    for (const path of paths) {
      try {
        const response = await main.req(path);
        if (response.status !== 200 || (response.data && response.data.error)) failures.push(`${path}:${response.status}`);
      } catch (error) {
        failures.push(cleanError(error));
      }
    }
    if (failures.length) throw new Error(failures.slice(0, 6).join(' | '));
    const stacks = await main.req('/healthz/stacks', { expected: 200 });
    if (!String(stacks.data).includes('goroutine')) throw new Error('stack endpoint did not return goroutines');
    return `${paths.length} endpoints + authenticated stacks`;
  });

  const tempEmail = `control-test-${suffix}@alt8mail.local`;
  const tempPassword = crypto.randomBytes(18).toString('hex');
  let tempSecret = '';
  await step('access_group_user_totp', async () => {
    const group = await main.req('/control/api/access/groups', {
      method: 'POST', expected: 201,
      body: { name: `Control test ${suffix}`, description: 'Automated integration test group', permissions: ['dashboard.read', 'logs.read'] },
    });
    resources.groupId = group.data.id;
    const user = await main.req('/control/api/access/users', {
      method: 'POST', expected: 201,
      body: { email: tempEmail, name: 'Control Test User', role: 'readonly', password: tempPassword, groupIds: [resources.groupId], sessionTtlSeconds: 3600 },
    });
    resources.userId = user.data.id;
    const temp = new Session();
    const login = await temp.login(tempEmail, tempPassword);
    if (!login.authenticated) throw new Error('temporary panel user could not sign in');
    await temp.req('/control/api/dashboard');
    const denied = await temp.req('/control/api/domains', {
      method: 'POST', expected: 403,
      body: { domain: `denied-${suffix}.example`, tenantId: '', selector: 'a8m' },
    });
    if (denied.status !== 403) throw new Error('readonly user mutated domains');
    const begin = await temp.req('/control/api/me/totp/begin', { method: 'POST' });
    tempSecret = begin.data.secret;
    if (!tempSecret) throw new Error('TOTP begin returned no secret');
    const finish = await temp.req('/control/api/me/totp/finish', { method: 'POST', body: { code: totp(tempSecret) } });
    if (!finish.data.totpEnabled) throw new Error('TOTP was not enabled');
    await temp.logout();
    const second = await temp.login(tempEmail, tempPassword);
    if (!second.totpRequired || !second.challengeId) throw new Error('TOTP challenge was not issued');
    const verified = await temp.req('/control/api/login/totp', { method: 'POST', body: { challengeId: second.challengeId, code: totp(tempSecret) } });
    if (!verified.data.authenticated) throw new Error('TOTP challenge did not authenticate');
    temp.csrf = verified.data.csrfToken || temp.csrf;
    const disabled = await temp.req('/control/api/me/totp/disable', { method: 'POST', body: { password: tempPassword, code: totp(tempSecret) } });
    if (disabled.data.totpEnabled) throw new Error('TOTP disable did not take effect');
    await temp.me();
    const revoked = await main.req(`/control/api/access/sessions/revoke-user/${resources.userId}`, { method: 'POST' });
    if (revoked.status !== 200) throw new Error('session revoke failed');
    const expired = await temp.req('/control/api/me', { expected: 401 });
    if (expired.status !== 401) throw new Error('revoked session still valid');
    await main.req(`/control/api/access/users/${resources.userId}`, { method: 'DELETE' });
    resources.userId = '';
    await main.req(`/control/api/access/groups/${resources.groupId}`, { method: 'DELETE' });
    resources.groupId = '';
    return 'role denial, group assignment, TOTP enable/login/disable, session revocation';
  });

  await step('alias_lifecycle', async () => {
    const address = `ct${suffix}@a8m.test`;
    const created = await main.req('/control/api/aliases', {
      method: 'POST', expected: 201,
      body: { address, kind: 'forward', targets: ['avery.chen@a8m.test'], target: 'avery.chen@a8m.test', description: 'Automated test forwarder', active: true },
    });
    resources.aliasId = created.data.id;
    const preview = await main.req('/control/api/aliases/preview', {
      method: 'POST', body: { action: 'update', id: resources.aliasId, input: { address, kind: 'forward', target: 'jordan.lee@a8m.test', active: true } },
    });
    if (!Array.isArray(preview.data.diff) || !preview.data.postfixQuery) throw new Error('alias preview missing diff');
    await main.req(`/control/api/aliases/${resources.aliasId}`, { method: 'PATCH', body: { address, kind: 'forward', target: 'jordan.lee@a8m.test', active: false } });
    await main.req(`/control/api/aliases/${resources.aliasId}`, { method: 'PATCH', body: { address, kind: 'forward', target: 'jordan.lee@a8m.test', active: true } });
    await main.req(`/control/api/aliases/${resources.aliasId}`, { method: 'DELETE' });
    resources.aliasId = '';
    return `${preview.data.diff.length} preview lines`;
  });

  await step('suppression_lifecycle', async () => {
    const pattern = `ct${suffix}@example.invalid`;
    const created = await main.req('/control/api/delivery/suppressions', {
      method: 'POST', expected: 201,
      body: { direction: 'outbound', pattern, kind: 'address', reason: 'Automated integration test', expiresAt: '' },
    });
    resources.suppressionId = created.data.id;
    await main.req(`/control/api/delivery/suppressions/${resources.suppressionId}`, { method: 'PATCH', body: { reason: 'Updated automated test suppression' } });
    await main.req(`/control/api/delivery/suppressions/${resources.suppressionId}`, { method: 'DELETE' });
    resources.suppressionId = '';
    return 'create, update, map regeneration and delete';
  });

  await step('cron_lifecycle', async () => {
    const created = await main.req('/control/api/cron', {
      method: 'POST', expected: 201,
      body: { name: `Control test ${suffix}`, schedule: '*/5 * * * *', command: 'printf control-cron-ok', runAs: 'root', stdin: '', startDate: '', endDate: '', enabled: true },
    });
    resources.cronId = created.data.id;
    await main.req(`/control/api/cron/${resources.cronId}/run`, { method: 'POST', expected: 202 });
    let run = null;
    for (let i = 0; i < 20; i += 1) {
      await new Promise((r) => setTimeout(r, 500));
      const runs = await main.req(`/control/api/cron/${resources.cronId}/runs`);
      run = runs.data.runs?.find((entry) => entry.status === 'success' || entry.status === 'failed');
      if (run) break;
    }
    if (!run) throw new Error('cron run did not complete');
    if (run.status !== 'success' || !String(run.output || '').includes('control-cron-ok')) throw new Error(`cron output mismatch: ${run.status}`);
    await main.req(`/control/api/cron/${resources.cronId}`, { method: 'PATCH', body: { enabled: false } });
    await main.req(`/control/api/cron/${resources.cronId}`, { method: 'DELETE' });
    resources.cronId = '';
    return `run ${run.durationMs}ms`;
  });

  await step('backup_lifecycle', async () => {
    const created = await main.req('/control/api/backups', {
      method: 'POST', expected: 202,
      body: { name: `control-test-${suffix}`, contents: ['panel_database', 'postfix', 'dovecot', 'dkim'] },
    });
    resources.backupId = created.data.id;
    let item = null;
    for (let i = 0; i < 60; i += 1) {
      await new Promise((r) => setTimeout(r, 2000));
      const list = await main.req('/control/api/backups');
      item = list.data.backups?.find((entry) => entry.id === resources.backupId);
      if (item && item.status !== 'running') break;
    }
    if (!item) throw new Error('backup disappeared');
    if (item.status !== 'completed') throw new Error(`backup ended ${item.status}${item.detail ? `: ${item.detail}` : ''}`);
    const download = await main.req(`/control/api/backups/${resources.backupId}/download`, { raw: true });
    if (download.data[0] !== 0x1f || download.data[1] !== 0x8b) throw new Error('download is not gzip');
    await main.req(`/control/api/backups/${resources.backupId}`, { method: 'DELETE' });
    resources.backupId = '';
    return `${item.sizeBytes} bytes, gzip verified`;
  });

  await step('webhook_lifecycle', async () => {
    const created = await main.req('/control/api/webhooks', {
      method: 'POST', expected: 201,
      body: { name: `Control test ${suffix}`, url: 'http://127.0.0.1:9/control-test', events: 'service.down', secret: '' },
    });
    resources.webhookId = created.data.id;
    if (!created.data.secret) throw new Error('generated webhook secret missing');
    await main.req(`/control/api/webhooks/${resources.webhookId}/test`, { method: 'POST' });
    let delivery = null;
    for (let i = 0; i < 10; i += 1) {
      await new Promise((r) => setTimeout(r, 400));
      const deliveries = await main.req('/control/api/webhooks/deliveries');
      delivery = deliveries.data.deliveries?.find((entry) => entry.webhookId === resources.webhookId);
      if (delivery) break;
    }
    if (!delivery) throw new Error('webhook delivery was not logged');
    await main.req(`/control/api/webhooks/${resources.webhookId}`, { method: 'DELETE' });
    resources.webhookId = '';
    return `delivery recorded (${delivery.status})`;
  });

  await step('notification_handle_lifecycle', async () => {
    const created = await main.req('/control/api/notifications/handles', {
      method: 'POST', expected: 201,
      body: { name: `Control test ${suffix}`, kind: 'webhook', events: 'service.down', webhookUrl: 'http://127.0.0.1:9/control-test', email: '', botToken: '', chatId: '' },
    });
    resources.handleId = created.data.id;
    const test = await main.req(`/control/api/notifications/handles/${resources.handleId}/test`, { method: 'POST', expected: 502 });
    if (test.status !== 502) throw new Error('unreachable notification handle unexpectedly succeeded');
    const deliveries = await main.req('/control/api/notifications/deliveries');
    const delivery = deliveries.data.deliveries?.find((entry) => entry.handleId === resources.handleId);
    if (!delivery) throw new Error('notification delivery was not logged');
    await main.req(`/control/api/notifications/handles/${resources.handleId}`, { method: 'DELETE' });
    resources.handleId = '';
    return `delivery failure recorded`;
  });

  let apiKey = '';
  await step('api_key_and_public_api', async () => {
    const created = await main.req('/control/api/access/api-keys', {
      method: 'POST', expected: 201,
      body: { name: `Control test ${suffix}`, scopes: ['admin'], expiresInDays: 1 },
    });
    resources.apiKeyId = created.data.id;
    apiKey = created.data.key;
    const root = await fetch(`${BASE}/api/v1`, { headers: { Authorization: `Bearer ${apiKey}` } });
    if (root.status !== 200) throw new Error(`API root HTTP ${root.status}`);
    const unauthorized = await fetch(`${BASE}/api/v1/suppressions`);
    if (unauthorized.status !== 401) throw new Error('API accepted a request without a key');
    const slug = `control-test-${suffix}`;
    const template = await fetch(`${BASE}/api/v1/templates`, {
      method: 'POST', headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'Control test template', slug, subject: 'Control test {{name}}', text: 'Hello {{name}}', html: '<p>Hello {{name}}</p>' }),
    });
    if (template.status !== 201) throw new Error(`template create HTTP ${template.status}`);
    const templateData = await template.json();
    const send = await fetch(`${BASE}/api/v1/messages`, {
      method: 'POST', headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ from: 'postmaster@a8m.test', to: ['postmaster@a8m.test'], subject: `Control API test ${suffix}`, text: 'Public API integration test.', variables: {} }),
    });
    if (send.status !== 202) throw new Error(`API send HTTP ${send.status}`);
    const sendData = await send.json();
    if (!sendData.id || !sendData.messageId) throw new Error('API send response incomplete');
    const history = await fetch(`${BASE}/api/v1/messages`, { headers: { Authorization: `Bearer ${apiKey}` } });
    if (history.status !== 200) throw new Error(`history HTTP ${history.status}`);
    const patch = await fetch(`${BASE}/api/v1/templates/${templateData.id}`, {
      method: 'PATCH', headers: { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ subject: 'Control test updated' }),
    });
    if (patch.status !== 200) throw new Error(`template patch HTTP ${patch.status}`);
    const del = await fetch(`${BASE}/api/v1/templates/${templateData.id}`, { method: 'DELETE', headers: { Authorization: `Bearer ${apiKey}` } });
    if (del.status !== 200) throw new Error(`template delete HTTP ${del.status}`);
    await main.req(`/control/api/access/api-keys/${resources.apiKeyId}`, { method: 'DELETE' });
    resources.apiKeyId = '';
    const revoked = await fetch(`${BASE}/api/v1`, { headers: { Authorization: `Bearer ${apiKey}` } });
    if (revoked.status !== 401) throw new Error('revoked API key still accepted');
    return `template + send + history + revocation (${sendData.status})`;
  });

  await step('tls_test_email', async () => {
    const result = await main.req('/control/api/tls/test-email', { method: 'POST', body: { recipient: 'postmaster@a8m.test' } });
    if (!result.data.ok || !String(result.data.transcript || '').includes('250')) throw new Error('SSL test email was not accepted');
    return 'SMTP transcript accepted';
  });

  await step('service_reload', async () => {
    const result = await main.req('/control/api/services/nginx.service/reload', { method: 'POST' });
    if (!result.data.ok) throw new Error('nginx reload returned not ok');
    return 'nginx.service reloaded';
  });

  await step('ports_preview', async () => {
    const current = await main.req('/control/api/services/ports');
    const preview = await main.req('/control/api/services/ports/preview', { method: 'POST', body: { listeners: current.data.listeners } });
    if (!preview.data.master || !preview.data.dovecot) throw new Error('preview data missing');
    return `${current.data.listeners.length} listeners, ${preview.data.master.diff.length + preview.data.dovecot.diff.length} diff lines`;
  });

  await step('postfix_config_roundtrip', async () => {
    const config = await main.req('/control/api/services/config?file=main.cf');
    const saved = await main.req('/control/api/services/config', {
      method: 'PUT', body: { file: 'main.cf', content: config.data.content, expectedHash: config.data.sha256 },
    });
    if (!saved.data.ok) throw new Error('postfix save returned not ok');
    await main.req('/control/api/services/config/reload', { method: 'POST' });
    return `sha256 ${String(saved.data.sha256 || '').slice(0, 12)}…`;
  });

  await step('firewall_rule_lifecycle', async () => {
    const before = await main.req('/control/api/server/firewall');
    if (before.data.backend !== 'ufw') throw new Error(`firewall backend is ${before.data.backend}`);
    await main.req('/control/api/server/firewall', {
      method: 'POST', body: { port: resources.firewallPort, proto: 'tcp', from: '127.0.0.1', deny: false },
    });
    const after = await main.req('/control/api/server/firewall');
    const match = String(after.data.raw || '').match(/\[\s*(\d+)\]\s+[^\n]*65530[^\n]*ALLOW/i);
    if (!match) throw new Error('added firewall rule not visible');
    resources.firewallNumber = Number(match[1]);
    await main.req(`/control/api/server/firewall/${resources.firewallNumber}`, { method: 'DELETE' });
    resources.firewallNumber = '';
    return 'rule 65530 allowed from loopback and removed';
  });

  await step('database_connection_test', async () => {
    const result = await main.req('/control/api/database/test', {
      method: 'POST',
      body: { host: '127.0.0.1', port: 5432, database: 'a8m', user: `alt8mail_missing_${suffix}`, password: 'not-a-real-password', sslmode: 'disable' },
    });
    if (result.data.ok !== false || !result.data.error) throw new Error('invalid database credentials unexpectedly succeeded');
    return 'invalid credentials rejected cleanly';
  });

  await step('domain_mailbox_sieve_lifecycle', async () => {
    const tenants = await main.req('/control/api/tenants');
    const tenantId = tenants.data.tenants?.[0]?.id;
    if (!tenantId) throw new Error('no active tenant');
    const oldDomain = `ct-${suffix}.a8m.test`;
    const newDomain = `ct-${suffix}b.a8m.test`;
    const created = await main.req('/control/api/domains', {
      method: 'POST', expected: 201,
      body: { domain: oldDomain, tenantId, isPrimary: false, selector: `ct${suffix}`, createPostmaster: false },
    });
    resources.domainId = created.data.id;
    resources.domainName = oldDomain;
    const renamed = await main.req(`/control/api/domains/${resources.domainId}`, { method: 'PATCH', body: { domain: newDomain } });
    if (renamed.data.domain !== newDomain) throw new Error('domain rename did not take effect');
    resources.domainName = newDomain;
    const detail = await main.req(`/control/api/domains/${resources.domainId}`);
    if (detail.data.domain !== newDomain) throw new Error('renamed domain lookup failed');
    const mailboxPassword = crypto.randomBytes(18).toString('hex');
    const mailbox = await main.req('/control/api/mailboxes', {
      method: 'POST', expected: 201,
      body: { name: 'Control Test Mailbox', localPart: 'probe', domainId: resources.domainId, quotaBytes: 1073741824, password: mailboxPassword, sendTo: '' },
    });
    resources.mailboxId = mailbox.data.id;
    resources.mailboxEmail = mailbox.data.email;
    const mailboxDetail = await main.req(`/control/api/mailboxes/${resources.mailboxId}`);
    if (mailboxDetail.data.mailbox.provisionStatus !== 'ready') throw new Error(`mailbox provisioned as ${mailboxDetail.data.mailbox.provisionStatus}`);
    const resetPassword = crypto.randomBytes(18).toString('hex');
    const reset = await main.req(`/control/api/mailboxes/${resources.mailboxId}/password`, { method: 'POST', body: { password: resetPassword, sendTo: '' } });
    if (!String(reset.data.probe || '').toLowerCase().includes('accepted')) throw new Error('Dovecot password probe did not accept the hash');
    const rules = [{ id: 'rule-control-test', name: 'Automated control test', enabled: true, match: 'all', conditions: [{ field: 'subject', operator: 'contains', value: 'control-test' }], action: { type: 'move', value: 'Control' } }];
    const sieve = await main.req(`/control/api/mailboxes/${resources.mailboxId}/sieve`, { method: 'PUT', body: { rules } });
    if (!sieve.data.managed || !String(sieve.data.script || '').includes('Automated control test')) throw new Error('Sieve deployment failed');
    const preview = await main.req(`/control/api/mailboxes/${resources.mailboxId}/sieve/preview`, { method: 'POST', body: { rules } });
    if (preview.data.ruleCount !== 1) throw new Error('Sieve preview count mismatch');
    await main.req(`/control/api/mailboxes/${resources.mailboxId}`, { method: 'PATCH', body: { name: 'Control Test Updated', quotaBytes: 2147483648, status: 'active' } });
    const history = await main.req(`/control/api/mailboxes/${resources.mailboxId}/history`);
    if (!Array.isArray(history.data.history)) throw new Error('mailbox history malformed');
    await main.req(`/control/api/mailboxes/${resources.mailboxId}`, { method: 'DELETE', body: { confirmEmail: resources.mailboxEmail } });
    resources.mailboxId = '';
    const deletePreview = await main.req(`/control/api/domains/${resources.domainId}/delete-preview`);
    if (!deletePreview.data.confirmToken) throw new Error('domain delete preview missing token');
    await main.req(`/control/api/domains/${resources.domainId}`, {
      method: 'DELETE', body: { confirmDomain: newDomain, confirmToken: deletePreview.data.confirmToken },
    });
    resources.domainId = '';
    resources.domainName = '';
    const domains = await main.req('/control/api/domains');
    if (domains.data.domains?.some((domain) => domain.domain === newDomain)) throw new Error('deleted domain still listed');
    return 'create, rename, mailbox provision, password probe, Sieve, delete, domain delete';
  });

  await step('mail_event_ingestion', async () => {
    let found = [];
    for (let i = 0; i < 12; i += 1) {
      await new Promise((r) => setTimeout(r, 2000));
      const logs = await main.req('/control/api/logs?q=postmaster%40a8m.test&limit=50');
      found = logs.data.events || [];
      if (found.length) break;
    }
    if (!found.length) throw new Error('no ingested mail events found for postmaster');
    const internal = found.find((event) => event.direction === 'internal' && event.status === 'sent');
    return `${found.length} events${internal ? ', internal direction verified' : ''}`;
  });

  await step('diagnostics_final', async () => {
    const diagnostics = await main.req('/control/api/diagnostics');
    if (diagnostics.data.failures > 0) throw new Error(`${diagnostics.data.failures} diagnostic failures`);
    if (diagnostics.data.warnings > 0) return `${diagnostics.data.warnings} warnings, 0 failures`;
    return '0 failures, 0 warnings';
  });
} finally {
  await cleanup();
  const failures = results.filter((entry) => !entry.ok);
  const summary = {
    base: BASE,
    checks: results.length,
    failures: failures.length,
    failed: failures,
    resources: Object.fromEntries(Object.entries(resources).filter(([, value]) => value)),
  };
  fs.mkdirSync('artifacts/control-ui', { recursive: true });
  fs.writeFileSync('artifacts/control-ui/api-smoke.json', JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ checks: results.length, failures: failures.length, failed: failures }, null, 2));
  if (failures.length) process.exitCode = 1;
}
