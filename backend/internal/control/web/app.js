/* Alt8Mail System Control — application shell, authentication and core views. */
import * as ui from './ui.js';
import {
  renderDomains, renderDomainDetail, renderMailboxes, renderMailboxDetail,
  renderAliases, renderImports,
} from './views-mail.js';
import {
  renderDelivery, renderQueue, renderRelay, renderSuppressions,
} from './views-delivery.js';
import {
  renderTLS, renderAccess, renderAPIKeys, renderWebhooks, renderNotifications,
} from './views-security.js';
import { renderSecurityReports } from './views-security-reports.js';
import { renderSystem } from './views-system.js';
import {
  renderServices, renderPorts, renderPostfix, renderLogs, renderBackups,
  renderCron, renderServer, renderFirewall, renderDatabase, renderUpdates, renderTerminal,
} from './views-infra.js';

const APP_VERSION = '0.3.15';

const state = {
  csrf: '',
  user: null,
  permissions: new Set(),
  version: APP_VERSION,
  env: 'staging',
  service: 'alt8mail-control',
  session: null,
  serverOffset: 0,
  configSync: null,
  configSyncLoaded: false,
  route: { name: 'dashboard', params: {}, query: {} },
  routeSeq: 0,
  cleanups: [],
  abort: null,
  sse: null,
  sseOnline: false,
  recentEvents: [],
  eventCount: 0,
  dashboardPeriod: 30,
  clockTimer: null,
  syncRequested: false,
  loginChallenge: '',
  loginEmail: '',
};

const NAV_GROUPS = [
  { label: 'Overview', items: [
    { key: 'dashboard', label: 'Dashboard', icon: 'dashboard', href: '#/dashboard', perm: 'dashboard.read', desc: 'Traffic, host health and live service state' },
    { key: 'diagnostics', label: 'Diagnostics', icon: 'activity', href: '#/diagnostics', perm: 'dashboard.read', desc: 'Run the doctor and inspect stack health' },
  ] },
  { label: 'Mail', items: [
    { key: 'domains', label: 'Domains & DNS', icon: 'globe', href: '#/domains', perm: 'domains.read', desc: 'Domains, DKIM, DNS plans and sending caps' },
    { key: 'mailboxes', label: 'Mailboxes', icon: 'mail', href: '#/mailboxes', perm: 'mailboxes.read', desc: 'Accounts, quotas, passwords and Sieve' },
    { key: 'aliases', label: 'Aliases & forwarding', icon: 'forward', href: '#/aliases', perm: 'aliases.read', desc: 'Forwarders, catch-alls and distribution lists' },
    { key: 'imports', label: 'Mail import', icon: 'upload', href: '#/imports', perm: 'imports.read', desc: 'Bring in IMAP, mbox or Maildir mailboxes' },
  ] },
  { label: 'Deliverability', items: [
    { key: 'delivery', label: 'Deliverability', icon: 'send', href: '#/delivery', perm: 'delivery.read', desc: 'Volume, bounce rate, tests and DNSBL checks' },
    { key: 'queue', label: 'Mail queue', icon: 'queue', href: '#/queue', perm: 'delivery.read', desc: 'Inspect, flush or remove Postfix queue entries' },
    { key: 'relay', label: 'Relay & outbound', icon: 'relay', href: '#/relay', perm: 'delivery.read', desc: 'Smarthost, source addresses and sender routes' },
    { key: 'suppressions', label: 'Suppressions', icon: 'shield-off', href: '#/suppressions', perm: 'delivery.read', desc: 'Inbound and outbound deny maps' },
  ] },
  { label: 'Security', items: [
    { key: 'security-reports', label: 'Security reports', icon: 'alert', href: '#/security-reports', perm: 'security.read', desc: 'Reported phishing, spam and malware across tenants' },
    { key: 'tls', label: 'TLS certificates', icon: 'lock', href: '#/tls', perm: 'tls.read', desc: 'Issue, renew and inspect certificates' },
    { key: 'access', label: 'Access & security', icon: 'users', href: '#/access', perm: 'access.read', desc: 'Panel roles, groups, sessions and audit' },
    { key: 'api', label: 'API keys', icon: 'key', href: '#/api', perm: 'api.read', desc: 'Scoped keys for the public REST API' },
    { key: 'webhooks', label: 'Webhooks', icon: 'webhook', href: '#/webhooks', perm: 'notifications.read', desc: 'Signed outbound event delivery' },
    { key: 'notifications', label: 'Notifications', icon: 'bell', href: '#/notifications', perm: 'notifications.read', desc: 'Slack, Discord, Telegram and email alerts' },
  ] },
  { label: 'Infrastructure', items: [
    { key: 'system', label: 'System', icon: 'power', href: '#/system', perm: 'server.read', desc: 'Release updates, version history and host reboot' },
    { key: 'services', label: 'Services', icon: 'server', href: '#/services', perm: 'services.read', desc: 'systemd health and lifecycle actions' },
    { key: 'updates', label: 'Software updates', icon: 'download', href: '#/updates', perm: 'server.read', desc: 'OS package updates with live command output' },
    { key: 'terminal', label: 'Terminal', icon: 'terminal', href: '#/terminal', perm: 'server.manage', desc: 'Authenticated SSH shell on this host' },
    { key: 'ports', label: 'Mail listeners', icon: 'plug', href: '#/ports', perm: 'services.read', desc: 'SMTP, IMAP and POP3 listener ports' },
    { key: 'postfix', label: 'Postfix config', icon: 'file-code', href: '#/postfix', perm: 'services.read', desc: 'Review and edit main.cf and master.cf' },
    { key: 'logs', label: 'Mail logs', icon: 'list', href: '#/logs', perm: 'logs.read', desc: 'Searchable event log and live tail' },
    { key: 'backups', label: 'Backups', icon: 'archive', href: '#/backups', perm: 'backups.read', desc: 'Archives, download and restore' },
    { key: 'cron', label: 'Scheduled tasks', icon: 'calendar', href: '#/cron', perm: 'cron.read', desc: 'Cron jobs, run-now and execution logs' },
    { key: 'server', label: 'Server & network', icon: 'network', href: '#/server', perm: 'server.read', desc: 'Identity, DNS resolver, interfaces and time' },
    { key: 'firewall', label: 'Firewall', icon: 'flame', href: '#/firewall', perm: 'server.read', desc: 'UFW rules and firewall state' },
    { key: 'database', label: 'Database', icon: 'database', href: '#/database', perm: 'database.read', desc: 'PostgreSQL health, provisioning and copies' },
    { key: 'sync', label: 'Config sync', icon: 'layers', href: '#/sync', perm: 'dashboard.read', desc: 'Reconcile mail resources and inspect drift' },
  ] },
];

const ROUTE_DEFS = {
  dashboard: { title: 'System overview', crumb: 'Overview', nav: 'dashboard', perm: 'dashboard.read', render: renderDashboard },
  diagnostics: { title: 'Diagnostics', crumb: 'Overview', nav: 'diagnostics', perm: 'dashboard.read', render: renderDiagnostics },
  domains: { title: 'Domains & DNS', crumb: 'Mail', nav: 'domains', perm: 'domains.read', render: renderDomains },
  'domain-detail': { title: 'Domain details', crumb: 'Domains & DNS', nav: 'domains', perm: 'domains.read', render: renderDomainDetail },
  mailboxes: { title: 'Mailboxes', crumb: 'Mail', nav: 'mailboxes', perm: 'mailboxes.read', render: renderMailboxes },
  'mailbox-detail': { title: 'Mailbox details', crumb: 'Mailboxes', nav: 'mailboxes', perm: 'mailboxes.read', render: renderMailboxDetail },
  aliases: { title: 'Aliases & forwarding', crumb: 'Mail', nav: 'aliases', perm: 'aliases.read', render: renderAliases },
  imports: { title: 'Mail import', crumb: 'Mail', nav: 'imports', perm: 'imports.read', render: renderImports },
  delivery: { title: 'Deliverability', crumb: 'Deliverability', nav: 'delivery', perm: 'delivery.read', render: renderDelivery },
  queue: { title: 'Mail queue', crumb: 'Deliverability', nav: 'queue', perm: 'delivery.read', render: renderQueue },
  relay: { title: 'Relay & outbound', crumb: 'Deliverability', nav: 'relay', perm: 'delivery.read', render: renderRelay },
  suppressions: { title: 'Suppressions', crumb: 'Deliverability', nav: 'suppressions', perm: 'delivery.read', render: renderSuppressions },
  'security-reports': { title: 'Security reports', crumb: 'Security', nav: 'security-reports', perm: 'security.read', render: renderSecurityReports },
  tls: { title: 'TLS certificates', crumb: 'Security', nav: 'tls', perm: 'tls.read', render: renderTLS },
  access: { title: 'Access & security', crumb: 'Security', nav: 'access', perm: 'access.read', render: renderAccess },
  api: { title: 'API keys', crumb: 'Security', nav: 'api', perm: 'api.read', render: renderAPIKeys },
  webhooks: { title: 'Webhooks', crumb: 'Security', nav: 'webhooks', perm: 'notifications.read', render: renderWebhooks },
  notifications: { title: 'Notifications', crumb: 'Security', nav: 'notifications', perm: 'notifications.read', render: renderNotifications },
  services: { title: 'Services', crumb: 'Infrastructure', nav: 'services', perm: 'services.read', render: renderServices },
  system: { title: 'System', crumb: 'Infrastructure', nav: 'system', perm: 'server.read', render: renderSystem },
  updates: { title: 'Software updates', crumb: 'Infrastructure', nav: 'updates', perm: 'server.read', render: renderUpdates },
  terminal: { title: 'Terminal', crumb: 'Infrastructure', nav: 'terminal', perm: 'server.manage', render: renderTerminal },
  ports: { title: 'Mail listeners', crumb: 'Infrastructure', nav: 'ports', perm: 'services.read', render: renderPorts },
  postfix: { title: 'Postfix configuration', crumb: 'Infrastructure', nav: 'postfix', perm: 'services.read', render: renderPostfix },
  logs: { title: 'Mail logs', crumb: 'Infrastructure', nav: 'logs', perm: 'logs.read', render: renderLogs },
  backups: { title: 'Backups', crumb: 'Infrastructure', nav: 'backups', perm: 'backups.read', render: renderBackups },
  cron: { title: 'Scheduled tasks', crumb: 'Infrastructure', nav: 'cron', perm: 'cron.read', render: renderCron },
  server: { title: 'Server & network', crumb: 'Infrastructure', nav: 'server', perm: 'server.read', render: renderServer },
  firewall: { title: 'Firewall', crumb: 'Infrastructure', nav: 'firewall', perm: 'server.read', render: renderFirewall },
  database: { title: 'Database', crumb: 'Infrastructure', nav: 'database', perm: 'database.read', render: renderDatabase },
  sync: { title: 'Config sync', crumb: 'Infrastructure', nav: 'sync', perm: 'dashboard.read', render: renderSync },
  account: { title: 'My security', crumb: 'Account', nav: '', perm: '', render: renderAccount },
};

/* --------------------------------------------------------------- API + errors */

class ApiError extends Error {
  constructor(message, { status = 0, code = '', details = null } = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

function errorDetail(error) {
  if (!(error instanceof ApiError)) return String(error?.message || error || 'Unknown error');
  if (error.details === null || error.details === undefined || error.details === '') return error.message;
  const detail = typeof error.details === 'string' ? error.details : JSON.stringify(error.details);
  return detail ? `${error.message} — ${detail}` : error.message;
}

async function api(path, { method = 'GET', body, signal, headers = {}, text = false } = {}) {
  const requestHeaders = { Accept: text ? 'text/plain, text/html' : 'application/json', ...headers };
  if (body !== undefined) requestHeaders['Content-Type'] = 'application/json';
  if (state.csrf) requestHeaders['X-Alt8Mail-CSRF'] = state.csrf;
  let response;
  try {
    response = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: requestHeaders,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    });
  } catch (error) {
    if (error.name === 'AbortError') throw error;
    throw new ApiError('The control service could not be reached. Check the network and service status.', { code: 'NETWORK' });
  }
  const contentType = response.headers.get('content-type') || '';
  if (text) {
    const value = await response.text();
    if (!response.ok) throw new ApiError(value || `Request failed with HTTP ${response.status}`, { status: response.status });
    return value;
  }
  let payload = null;
  if (contentType.includes('application/json')) {
    try { payload = await response.json(); } catch { payload = null; }
  } else {
    const raw = await response.text();
    if (!response.ok) throw new ApiError(raw || `Request failed with HTTP ${response.status}`, { status: response.status });
    return raw;
  }
  if (!response.ok) {
    const info = payload?.error || {};
    // A 401 from an upstream credential (for example a failed SSH login) must
    // not destroy the operator's panel session. Only the control plane's own
    // UNAUTHENTICATED response means the panel session has expired.
    if (response.status === 401 && path !== '/control/api/session' && info.code === 'UNAUTHENTICATED') {
      if (state.user) expireSession();
      throw new ApiError('Your panel session has expired. Sign in again.', { status: 401, code: 'UNAUTHENTICATED' });
    }
    throw new ApiError(info.message || `Request failed with HTTP ${response.status}`, {
      status: response.status, code: info.code || '', details: info.details ?? null,
    });
  }
  return payload;
}

function download(path) {
  const anchor = document.createElement('a');
  anchor.href = path;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

function can(permission) {
  if (!permission) return true;
  return state.permissions.has('*') || state.permissions.has(permission);
}

function setSession(payload) {
  if (payload?.user) {
    state.user = payload.user;
    state.permissions = new Set(['*'].includes(payload.user.permissions?.[0]) ? ['*'] : payload.user.permissions || []);
  }
  if (payload?.csrfToken) state.csrf = payload.csrfToken;
  if (payload?.version) state.version = payload.version;
  if (payload?.env) state.env = payload.env;
  if (payload?.session) state.session = payload.session;
  if (payload?.serverTime) {
    const server = Date.parse(payload.serverTime);
    if (Number.isFinite(server)) state.serverOffset = server - Date.now();
  }
}

function clearSession() {
  state.user = null;
  state.permissions = new Set();
  state.session = null;
  state.csrf = '';
  state.configSync = null;
  state.configSyncLoaded = false;
  if (state.sse) { state.sse.close(); state.sse = null; }
  if (state.clockTimer) { clearInterval(state.clockTimer); state.clockTimer = null; }
}

function expireSession() {
  if (state.expiringSession) return;
  state.expiringSession = true;
  const message = 'Your panel session expired. Sign in again to continue.';
  clearSession();
  cleanupRoute();
  renderLogin({ authenticated: false, csrfToken: state.csrf }, message);
  fetch('/control/api/session', { credentials: 'same-origin', cache: 'no-store' })
    .then((response) => response.ok ? response.json() : null)
    .then((payload) => {
      if (!payload) return;
      state.csrf = payload.csrfToken || '';
      state.version = payload.version || state.version;
      state.env = payload.env || state.env;
    })
    .catch(() => {})
    .finally(() => { state.expiringSession = false; });
}

/* --------------------------------------------------------------- formatting */

const fmt = {
  num: ui.num,
  pct: ui.pct,
  bytes: ui.bytes,
  dateTime: ui.dateTime,
  dateOnly: ui.dateOnly,
  timeAgo: ui.timeAgo,
  duration: ui.duration,
  status: ui.statusPill,
  value: (value) => (value === null || value === undefined || value === '' ? '—' : String(value)),
};

function esc(value) { return ui.esc(value); }

/* --------------------------------------------------------------- theme */

function initTheme() {
  let theme = '';
  try { theme = localStorage.getItem('alt8mail.control.theme') || ''; } catch { /* storage can be blocked */ }
  if (theme !== 'light' && theme !== 'dark') {
    theme = window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  document.documentElement.dataset.theme = theme;
  return theme;
}

function currentTheme() {
  return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
}

function toggleTheme() {
  const next = currentTheme() === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem('alt8mail.control.theme', next); } catch { /* ignore */ }
  const button = document.getElementById('theme-toggle');
  if (button) {
    button.innerHTML = ui.icon(next === 'dark' ? 'sun' : 'moon', 17);
    button.setAttribute('aria-label', next === 'dark' ? 'Switch to light theme' : 'Switch to dark theme');
    button.title = next === 'dark' ? 'Light theme' : 'Dark theme';
  }
}

initTheme();

/* --------------------------------------------------------------- login */

function loginStatusMarkup() {
  return `<span class="login-status" id="login-health"><span class="pulse"></span><span>Connecting to control plane…</span></span>`;
}

function loginHTML() {
  return `<div class="login-page">
    <aside class="login-aside">
      <a class="login-brand" href="#/dashboard">
        <span class="brand-mark">${ui.icon('shield-check', 21)}</span>
        <span><strong>Alt8Mail</strong><span>System Control</span></span>
      </a>
      <div class="login-hero">
        <div class="eyebrow">Server control plane</div>
        <h1>Own the mail platform.<br><em>End to end.</em></h1>
        <p>Domain and DNS control, mailbox lifecycle, delivery policy, service health and the audit trail behind every change — in one operator console.</p>
        <div class="login-features">
          <span class="login-feature">${ui.icon('globe', 15)}Domain, DKIM and DNS plans</span>
          <span class="login-feature">${ui.icon('mail', 15)}Mailboxes, aliases and Sieve</span>
          <span class="login-feature">${ui.icon('send', 15)}Queue, relay and deliverability</span>
          <span class="login-feature">${ui.icon('server', 15)}Services, ports and config sync</span>
          <span class="login-feature">${ui.icon('lock', 15)}TLS issuance and renewal</span>
          <span class="login-feature">${ui.icon('shield', 15)}Roles, TOTP and full audit</span>
        </div>
      </div>
      <div class="login-foot">
        ${loginStatusMarkup()}
        <span>Operator access only · TLS enforced</span>
      </div>
    </aside>
    <main class="login-main">
      <div class="login-main-inner">
        <div class="login-mobile-brand">
          <span class="brand-mark">${ui.icon('shield-check', 21)}</span>
          <span class="brand-copy"><strong>Alt8Mail</strong><span>System Control</span></span>
          <span class="env-chip" style="margin-left:auto"><span class="dot"></span>${esc(state.env || 'staging')}</span>
        </div>
        <div class="login-card" id="login-card"></div>
      </div>
      <div class="login-footer">Alt8Mail System Control · ${esc(state.version || APP_VERSION)}</div>
    </main>
  </div>`;
}

function loginFormHTML(message = '') {
  return `<div class="login-step">${ui.icon('lock', 12)} Secure operator sign-in</div>
    <h2>Sign in to System Control</h2>
    <p>Use your panel account. The webmail password is separate.</p>
    ${message ? `<div class="notice tone-warning login-error is-visible" role="alert">${ui.icon('alert-circle', 17)}<div><strong>Sign-in could not complete</strong><p>${esc(message)}</p></div></div>` : ''}
    <form class="login-form" id="login-form" novalidate>
      <div class="field"><label for="login-email">Panel email</label><input class="input" id="login-email" name="email" type="email" autocomplete="username" inputmode="email" placeholder="admin@example.com" required autofocus value="${ui.esc(state.loginEmail)}"></div>
      <div class="field"><label for="login-password">Password</label><div class="password-wrap"><input class="input" id="login-password" name="password" type="password" autocomplete="current-password" placeholder="Your panel password" required><button class="icon-btn password-toggle" type="button" id="password-toggle" aria-label="Show password">${ui.icon('eye', 16)}</button></div></div>
      <button class="btn btn-primary btn-block login-submit" type="submit" id="login-submit">Sign in to Control</button>
    </form>
    <div class="login-help">Authorised administrators only. Sign-in attempts, lockouts and every change are written to the panel audit log. Two-factor authentication is enforced when enabled on your account.</div>`;
}

function totpFormHTML(email) {
  return `<button type="button" class="login-back" id="totp-back">${ui.icon('chevron-left', 14)} Back to sign-in</button>
    <div class="login-step">${ui.icon('shield-check', 12)} Second factor required</div>
    <h2>Authenticator code</h2>
    <p>Enter the six-digit code for <strong>${esc(email)}</strong> from your authenticator app.</p>
    <div id="totp-error"></div>
    <form class="login-form" id="totp-form" novalidate>
      <div class="field"><label for="totp-code">Six-digit code</label><input class="input totp-input" id="totp-code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" maxlength="6" placeholder="000000" autofocus required></div>
      <button class="btn btn-primary btn-block login-submit" type="submit" id="totp-submit">Verify and open Control</button>
    </form>
    <div class="login-help">The challenge expires after five minutes. Five incorrect codes close the challenge; you will need to enter your password again.</div>`;
}

function renderLogin(session = null, message = '') {
  if (session?.csrfToken) state.csrf = session.csrfToken;
  if (session?.version) state.version = session.version;
  if (session?.env) state.env = session.env;
  const app = document.getElementById('app');
  state.loginChallenge = '';
  app.innerHTML = loginHTML();
  const card = document.getElementById('login-card');
  card.innerHTML = loginFormHTML(message);
  bindLoginForm();
  checkLoginHealth();
}

function checkLoginHealth() {
  const el = document.getElementById('login-health');
  if (!el) return;
  fetch('/healthz', { credentials: 'same-origin', cache: 'no-store' })
    .then((response) => response.ok ? response.json() : Promise.reject(new Error('offline')))
    .then((payload) => {
      if (!payload) return;
      if (payload.version && !state.version) state.version = payload.version;
      el.innerHTML = `<span class="pulse"></span><span>Control plane online · ${esc(payload.version || state.version || '')}</span>`;
    })
    .catch(() => {
      el.classList.add('is-off');
      el.innerHTML = `<span class="pulse"></span><span>Control plane unreachable</span>`;
    });
}

function bindLoginForm() {
  const form = document.getElementById('login-form');
  if (!form) return;
  const password = document.getElementById('login-password');
  const toggle = document.getElementById('password-toggle');
  toggle?.addEventListener('click', () => {
    const visible = password.type === 'text';
    password.type = visible ? 'password' : 'text';
    toggle.innerHTML = ui.icon(visible ? 'eye' : 'x-circle', 16);
    toggle.setAttribute('aria-label', visible ? 'Show password' : 'Hide password');
  });
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const submit = document.getElementById('login-submit');
    const email = form.querySelector('[name="email"]').value.trim();
    const pass = form.querySelector('[name="password"]').value;
    if (!email || !pass) {
      showLoginError('Enter both your panel email and password.');
      return;
    }
    state.loginEmail = email;
    submit.classList.add('is-loading');
    submit.disabled = true;
    try {
      const payload = await api('/control/api/login', { method: 'POST', body: { email, password: pass } });
      if (payload.totpRequired) {
        state.loginChallenge = payload.challengeId;
        showTotpStep(email);
        return;
      }
      await finishAuthentication(payload);
    } catch (error) {
      if (error instanceof ApiError && error.code === 'CSRF') {
        // The CSRF cookie can outlive a stale tab. Refresh the signed cookie and
        // let the operator retry without losing the form.
        try {
          const fresh = await api('/control/api/session');
          if (fresh.csrfToken) state.csrf = fresh.csrfToken;
        } catch { /* keep original error */ }
      }
      showLoginError(errorDetail(error));
    } finally {
      submit.classList.remove('is-loading');
      submit.disabled = false;
    }
  });
}

function showLoginError(message) {
  let notice = document.querySelector('.login-card .login-error');
  if (!notice) {
    const form = document.getElementById('login-form');
    notice = document.createElement('div');
    notice.className = 'notice tone-warning login-error is-visible';
    notice.setAttribute('role', 'alert');
    form?.parentNode?.insertBefore(notice, form);
  }
  notice.innerHTML = `${ui.icon('alert-circle', 17)}<div><strong>Sign-in could not complete</strong><p>${esc(message)}</p></div>`;
}

function showTotpStep(email) {
  const card = document.getElementById('login-card');
  card.innerHTML = totpFormHTML(email);
  document.getElementById('totp-back')?.addEventListener('click', () => {
    state.loginChallenge = '';
    card.innerHTML = loginFormHTML('');
    bindLoginForm();
  });
  const form = document.getElementById('totp-form');
  const code = document.getElementById('totp-code');
  code?.addEventListener('input', () => {
    code.value = code.value.replace(/\D/g, '').slice(0, 6);
  });
  form?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const submit = document.getElementById('totp-submit');
    const value = code.value.trim();
    if (!/^\d{6}$/.test(value)) {
      showTotpError('Enter the six-digit code from your authenticator app.');
      code.focus();
      return;
    }
    submit.classList.add('is-loading');
    submit.disabled = true;
    try {
      const payload = await api('/control/api/login/totp', {
        method: 'POST', body: { challengeId: state.loginChallenge, code: value },
      });
      await finishAuthentication(payload);
    } catch (error) {
      showTotpError(errorDetail(error));
      submit.classList.remove('is-loading');
      submit.disabled = false;
      code.select();
    }
  });
}

function showTotpError(message) {
  const host = document.getElementById('totp-error');
  if (host) host.innerHTML = `<div class="notice tone-warning login-error is-visible" role="alert">${ui.icon('alert-circle', 17)}<div><strong>Verification failed</strong><p>${esc(message)}</p></div></div>`;
}

async function finishAuthentication(payload) {
  setSession(payload);
  state.loginChallenge = '';
  if (!location.hash || location.hash === '#') location.hash = '#/dashboard';
  renderShell();
  connectSSE();
  await refreshConfigSync(true);
  await route({ replace: true });
}

/* --------------------------------------------------------------- shell */

function navMarkup() {
  return NAV_GROUPS.map((group) => {
    const items = group.items.filter((item) => can(item.perm));
    if (!items.length) return '';
    return `<div class="nav-group"><div class="nav-group-title">${esc(group.label)}</div>${items.map((item) => `<a class="nav-item" href="${item.href}" data-nav="${item.key}" title="${esc(item.desc || item.label)}">${ui.icon(item.icon, 17)}<span class="nav-label">${esc(item.label)}</span></a>`).join('')}</div>`;
  }).join('');
}

function initials(name, email) {
  const value = String(name || email || '?').trim();
  const parts = value.split(/\s+/);
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  return value.slice(0, 2).toUpperCase();
}

function renderShell() {
  const app = document.getElementById('app');
  const theme = currentTheme();
  app.innerHTML = `<div class="shell">
    <aside class="sidebar" id="sidebar">
      <a class="brand" href="#/dashboard" aria-label="Alt8Mail System Control dashboard">
        <span class="brand-mark">${ui.icon('shield-check', 20)}</span>
        <span class="brand-copy"><strong>Alt8Mail</strong><span>System Control</span></span>
      </a>
      <nav class="sidebar-nav" id="sidebar-nav" aria-label="System Control sections">${navMarkup()}</nav>
      <div class="sidebar-foot">
        <div class="sidebar-env"><span class="env-chip"><span class="dot"></span>${esc(state.env || 'staging')}</span><span>v${esc(state.version || APP_VERSION)}</span></div>
        <button class="sidebar-collapse" type="button" id="sidebar-collapse">${ui.icon('panel-left', 14)} Collapse sidebar</button>
      </div>
    </aside>
    <div class="workspace">
      <header class="topbar">
        <div class="topbar-start">
          <button class="icon-btn mobile-menu-btn" type="button" id="mobile-menu" aria-label="Open navigation">${ui.icon('menu', 19)}</button>
          <div class="topbar-title"><span class="crumb" id="topbar-crumb">Overview</span><span class="title" id="topbar-title">System overview</span></div>
        </div>
        <div class="topbar-end">
          <button class="sync-chip" type="button" id="sync-chip" title="Configuration sync status">${ui.icon('layers', 14)}<span>Checking sync…</span></button>
          <span class="live-chip is-off" id="live-chip" title="Live event stream"><span class="pulse"></span><span>Offline</span></span>
          <span class="clock" id="server-clock" title="Server time (UTC)">--:--:-- UTC</span>
          <span class="topbar-divider"></span>
          <button class="icon-btn" type="button" id="palette-open" aria-label="Search and jump (Ctrl+K)" title="Search and jump · Ctrl K">${ui.icon('search', 17)}</button>
          <button class="icon-btn" type="button" id="event-bell" aria-label="Live events" title="Live events">${ui.icon('bell', 17)}<span class="bell-dot" id="bell-dot" hidden></span></button>
          <button class="icon-btn" type="button" id="theme-toggle" aria-label="${theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}" title="${theme === 'dark' ? 'Light theme' : 'Dark theme'}">${ui.icon(theme === 'dark' ? 'sun' : 'moon', 17)}</button>
          <div class="profile-wrap">
            <button class="profile-btn" type="button" id="profile-toggle" aria-haspopup="true" aria-expanded="false">
              <span class="avatar">${esc(initials(state.user?.name, state.user?.email))}</span>
              <span class="profile-copy"><strong>${esc(state.user?.name || 'Administrator')}</strong><span>${esc(state.user?.role || '')}</span></span>
              ${ui.icon('chevron-down', 13)}
            </button>
            <div class="profile-menu" id="profile-menu" hidden>
              <div class="profile-menu-head"><strong>${esc(state.user?.name || 'Administrator')}</strong><span>${esc(state.user?.email || '')}</span></div>
              <a class="menu-item" href="#/account">${ui.icon('user', 15)} My security</a>
              <button class="menu-item" type="button" id="menu-palette">${ui.icon('search', 15)} Search Control <span class="palette-kbd">${ui.kbd('Ctrl K')}</span></button>
              <div class="menu-separator"></div>
              <button class="menu-item danger" type="button" id="logout-btn">${ui.icon('logout', 15)} Sign out</button>
              <div class="menu-note">${esc(state.user?.email || '')}<br>Session expires ${esc(fmt.dateTime(state.session?.expiresAt))}</div>
            </div>
          </div>
        </div>
      </header>
      <main class="content">
        <div id="config-sync-banner"></div>
        <div id="view">${ui.loadingState('Loading view…', 5)}</div>
      </main>
    </div>
  </div>`;
  bindShell();
  applySidebarState();
  updateClock();
  state.clockTimer = setInterval(updateClock, 1000);
  renderConfigSyncBanner();
  refreshConfigSync();
}

function bindShell() {
  document.getElementById('mobile-menu')?.addEventListener('click', () => {
    document.body.classList.toggle('is-mobile-nav-open');
  });
  document.getElementById('sidebar-nav')?.addEventListener('click', (event) => {
    if (event.target.closest('.nav-item')) document.body.classList.remove('is-mobile-nav-open');
  });
  document.getElementById('sidebar-collapse')?.addEventListener('click', () => {
    document.body.classList.toggle('is-sidebar-collapsed');
    try { localStorage.setItem('alt8mail.control.sidebar', document.body.classList.contains('is-sidebar-collapsed') ? 'collapsed' : 'expanded'); } catch { /* ignore */ }
  });
  document.getElementById('theme-toggle')?.addEventListener('click', toggleTheme);
  document.getElementById('palette-open')?.addEventListener('click', openCommandPalette);
  document.getElementById('menu-palette')?.addEventListener('click', () => { closeProfileMenu(); openCommandPalette(); });
  document.getElementById('event-bell')?.addEventListener('click', openEventLog);
  document.getElementById('sync-chip')?.addEventListener('click', () => navigate('sync'));
  document.getElementById('logout-btn')?.addEventListener('click', logout);
  bindProfileMenu();
  const clock = () => updateClock();
  window.addEventListener('resize', clock, { passive: true });
}

function applySidebarState() {
  let stored = '';
  try { stored = localStorage.getItem('alt8mail.control.sidebar') || ''; } catch { /* ignore */ }
  if (stored === 'collapsed') document.body.classList.add('is-sidebar-collapsed');
}

function updateClock() {
  const el = document.getElementById('server-clock');
  if (!el) return;
  const now = new Date(Date.now() + state.serverOffset);
  el.textContent = `${now.toISOString().slice(11, 19)} UTC`;
}

function bindProfileMenu() {
  const toggle = document.getElementById('profile-toggle');
  const menu = document.getElementById('profile-menu');
  if (!toggle || !menu) return;
  toggle.addEventListener('click', (event) => {
    event.stopPropagation();
    const open = !menu.hidden;
    menu.hidden = open;
    toggle.setAttribute('aria-expanded', String(!open));
  });
  document.addEventListener('click', (event) => {
    if (!menu.hidden && !event.target.closest('.profile-wrap')) closeProfileMenu();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') closeProfileMenu();
  });
}

function closeProfileMenu() {
  const menu = document.getElementById('profile-menu');
  const toggle = document.getElementById('profile-toggle');
  if (menu) menu.hidden = true;
  if (toggle) toggle.setAttribute('aria-expanded', 'false');
}

async function logout() {
  closeProfileMenu();
  try {
    await api('/control/api/logout', { method: 'POST' });
  } catch { /* the cookie is cleared locally regardless */ }
  clearSession();
  state.csrf = '';
  try {
    const fresh = await api('/control/api/session');
    state.csrf = fresh.csrfToken || '';
    state.version = fresh.version || state.version;
    state.env = fresh.env || state.env;
  } catch { /* login will retry session */ }
  renderLogin({ csrfToken: state.csrf, version: state.version }, 'You have signed out of System Control.');
}

/* --------------------------------------------------------------- SSE */

function connectSSE() {
  if (state.sse) return;
  const source = new EventSource('/events');
  state.sse = source;
  source.addEventListener('open', () => setLive(true));
  source.addEventListener('update', (event) => {
    setLive(true);
    let payload;
    try { payload = JSON.parse(event.data); } catch { return; }
    handleLiveEvent(payload);
  });
  source.addEventListener('error', () => {
    setLive(false);
    if (source.readyState === EventSource.CLOSED) {
      state.sse = null;
      setTimeout(() => { if (state.user) connectSSE(); }, 5000);
    }
  });
}

function setLive(online) {
  if (state.sseOnline === online) return;
  state.sseOnline = online;
  const chip = document.getElementById('live-chip');
  if (!chip) return;
  chip.classList.toggle('is-off', !online);
  chip.innerHTML = `<span class="pulse"></span><span>${online ? 'Live' : 'Reconnecting'}</span>`;
}

function handleLiveEvent(payload) {
  const event = payload?.event || 'event';
  state.recentEvents.unshift(payload);
  state.recentEvents = state.recentEvents.slice(0, 40);
  state.eventCount += 1;
  const dot = document.getElementById('bell-dot');
  if (dot) dot.hidden = false;
  const significant = {
    'service.down': ['A monitored service went down', 'critical'],
    'service.up': ['A monitored service recovered', 'good'],
    'ssl.failed': ['Certificate operation failed', 'critical'],
    'ssl.expiring': ['A certificate is approaching expiry', 'warning'],
    'backup.failed': ['A backup failed', 'critical'],
    'blocklist.listed': ['An outbound IP is listed', 'warning'],
    'config.sync': ['Configuration sync completed', 'info'],
    'outbound.ip_discovered': ['New outbound IP discovered', 'info'],
    'domain.created': ['A mail domain was created', 'info'],
    'domain.deleted': ['A mail domain was deleted', 'warning'],
    'user.created': ['A mailbox account was created', 'info'],
    'security.report': ['A user reported a suspicious email', 'critical'],
    'security.report_reviewed': ['A security report was reviewed', 'info'],
  }[event];
  if (significant) {
    const detail = payload?.data?.unit || payload?.data?.domain || payload?.data?.message || payload?.data?.certificate || '';
    ui.toast(detail ? `${significant[0]}: ${detail}` : significant[0], significant[1], { title: ui.humanTitle(event.replace('.', ' ')) });
  }
  window.dispatchEvent(new CustomEvent('control:event', { detail: payload }));
  if (!document.getElementById('event-log-body')) return;
  // An open event viewer refreshes in place.
  refreshEventLog();
}

function openEventLog() {
  const dot = document.getElementById('bell-dot');
  if (dot) dot.hidden = true;
  state.eventCount = 0;
  const modal = ui.openModal({
    title: 'Live event stream', subtitle: 'Server-sent events from the control plane. This list is held in memory for this session.',
    size: 'medium',
    body: `<div id="event-log-body">${eventLogMarkup()}</div>`,
    footer: `<button type="button" class="btn btn-ghost" id="event-clear">Clear view</button><button type="button" class="btn btn-secondary" data-modal-close>Close</button>`,
  });
  modal.root.querySelector('#event-clear')?.addEventListener('click', () => {
    state.recentEvents = [];
    refreshEventLog();
  });
}

function eventLogMarkup() {
  if (!state.recentEvents.length) return ui.emptyState('No live events yet', 'Service changes, mail events, certificate warnings and configuration syncs will appear here.', '', 'activity');
  return `<div class="list audit-list">${state.recentEvents.map((entry) => {
    const event = entry.event || 'event';
    const data = entry.data || {};
    const detail = data.unit || data.domain || data.email || data.certificate || data.message || data.queueId || JSON.stringify(data).slice(0, 120);
    return `<div class="list-item"><span class="list-icon">${ui.icon(event.startsWith('ssl') ? 'lock' : event.startsWith('service') ? 'server' : event.startsWith('mail') ? 'mail' : 'activity', 14)}</span><div class="list-copy"><strong>${esc(event)}</strong><span>${esc(detail)}</span></div><span class="list-time">${esc(fmt.timeAgo(entry.timestamp))}</span></div>`;
  }).join('')}</div>`;
}

function refreshEventLog() {
  const body = document.getElementById('event-log-body');
  if (body) body.innerHTML = eventLogMarkup();
}

/* --------------------------------------------------------------- config sync */

async function refreshConfigSync(force = false) {
  if (!can('dashboard.read')) { updateSyncChip(); return; }
  if (!force && state.configSyncLoaded) { updateSyncChip(); renderConfigSyncBanner(); return; }
  try {
    const payload = await api('/control/api/config-sync');
    state.configSync = payload;
    state.configSyncLoaded = true;
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) return;
    state.configSyncLoaded = true;
    state.configSync = { error: errorDetail(error) };
  }
  updateSyncChip();
  renderConfigSyncBanner();
}

function syncHealth() {
  if (!state.configSync) return { tone: 'neutral', label: 'Checking sync', detail: '' };
  if (state.configSync.error) return { tone: 'critical', label: 'Sync unavailable', detail: state.configSync.error };
  const latest = state.configSync.latest || {};
  const drift = Array.isArray(latest.drift) ? latest.drift.filter((line) => line && line !== 'No drift detected') : [];
  if (latest.status === 'never') return { tone: 'warning', label: 'Never synced', detail: 'The mail configuration has not been reconciled with the control database.' };
  if (latest.status === 'error') return { tone: 'critical', label: 'Sync failed', detail: latest.error || 'The last configuration sync failed.' };
  if (latest.status === 'running') return { tone: 'info', label: 'Sync running', detail: 'A configuration reconcile is in progress.' };
  if (drift.length || (Array.isArray(latest.warnings) && latest.warnings.length)) {
    return { tone: latest.warnings?.length ? 'warning' : 'warning', label: `${drift.length || latest.warnings?.length} drift item${(drift.length || latest.warnings?.length) === 1 ? '' : 's'}`, detail: drift[0] || latest.warnings?.[0] || '' };
  }
  return { tone: 'good', label: 'Config in sync', detail: `Last run ${fmt.timeAgo(latest.startedAt)}` };
}

function updateSyncChip() {
  const chip = document.getElementById('sync-chip');
  if (!chip) return;
  const health = syncHealth();
  chip.className = `sync-chip is-${health.tone === 'neutral' ? 'good' : health.tone}`;
  chip.innerHTML = `${ui.icon(health.tone === 'good' ? 'check-circle' : health.tone === 'critical' ? 'alert-circle' : 'layers', 14)}<span>${esc(health.label)}</span>`;
  chip.title = health.detail || health.label;
}

function renderConfigSyncBanner() {
  const host = document.getElementById('config-sync-banner');
  if (!host) return;
  if (!state.configSync || !state.configSyncLoaded) { host.innerHTML = ''; return; }
  if (state.configSync.error) {
    host.innerHTML = `<div class="config-banner is-critical">${ui.icon('alert-circle', 18)}<div class="config-banner-copy"><strong>Configuration status is unavailable</strong><span>${esc(state.configSync.error)}</span></div><div class="config-banner-actions"><button class="btn btn-secondary btn-sm" type="button" data-sync-retry>${ui.icon('refresh', 13)} Retry</button></div></div>`;
    host.querySelector('[data-sync-retry]')?.addEventListener('click', () => refreshConfigSync(true));
    return;
  }
  const latest = state.configSync.latest || {};
  const health = syncHealth();
  if (health.tone === 'good' || health.tone === 'info') { host.innerHTML = ''; return; }
  const drift = Array.isArray(latest.drift) ? latest.drift.filter((line) => line && line !== 'No drift detected') : [];
  const warnings = Array.isArray(latest.warnings) ? latest.warnings : [];
  const detail = health.detail || (drift[0] || warnings[0] || '');
  host.innerHTML = `<div class="config-banner ${health.tone === 'critical' ? 'is-critical' : ''}">${ui.icon(health.tone === 'critical' ? 'alert-circle' : 'alert', 18)}<div class="config-banner-copy"><strong>${esc(health.label)}</strong><span>${esc(detail)}${latest.startedAt ? ` · Last checked ${esc(fmt.timeAgo(latest.startedAt))}` : ''}</span></div><div class="config-banner-actions"><button class="btn btn-secondary btn-sm" type="button" data-sync-review>Review</button>${can('settings.write') ? `<button class="btn btn-primary btn-sm" type="button" data-sync-run>${ui.icon('refresh', 13)} Run sync</button>` : ''}</div></div>`;
  host.querySelector('[data-sync-review]')?.addEventListener('click', () => navigate('sync'));
  const run = host.querySelector('[data-sync-run]');
  if (run) run.addEventListener('click', async () => {
    if (state.syncRequested) return;
    state.syncRequested = true;
    run.classList.add('is-loading');
    run.disabled = true;
    try {
      await api('/control/api/config-sync/run', { method: 'POST' });
      ui.toast('Configuration sync completed.', 'good');
      await refreshConfigSync(true);
      if (state.route.name === 'sync' && state.user) route();
    } catch (error) {
      ui.toast(errorDetail(error), 'critical', { title: 'Configuration sync failed' });
    } finally {
      state.syncRequested = false;
      run.classList.remove('is-loading');
      run.disabled = false;
    }
  });
}

/* --------------------------------------------------------------- routing */

function navigate(path) {
  const target = `#/${String(path).replace(/^#?\/?/, '')}`;
  if (location.hash === target) { route(); return; }
  location.hash = target;
}

function parseRoute() {
  const raw = location.hash.replace(/^#\/?/, '');
  const [pathPart, queryPart = ''] = raw.split('?');
  const parts = pathPart.split('/').filter(Boolean);
  let name = parts[0] || 'dashboard';
  const params = {};
  if (name === 'domains' && parts[1]) { name = 'domain-detail'; params.id = parts[1]; }
  else if (name === 'mailboxes' && parts[1]) { name = 'mailbox-detail'; params.id = parts[1]; }
  else if (!ROUTE_DEFS[name]) name = 'dashboard';
  const query = {};
  new URLSearchParams(queryPart).forEach((value, key) => { query[key] = value; });
  return { name, params, query };
}

function cleanupRoute() {
  state.abort?.abort();
  state.abort = null;
  for (const fn of state.cleanups.splice(0)) {
    try { fn(); } catch { /* cleanup is best-effort */ }
  }
}

function makeContext(seq, query) {
  const controller = new AbortController();
  state.abort = controller;
  const ctx = {
    state,
    api: (path, options = {}) => api(path, { ...options, signal: options.signal || controller.signal }),
    can,
    navigate,
    download,
    esc,
    fmt,
    ui,
    modal: ui.openModal,
    confirm: ui.confirmDialog,
    toast: ui.toast,
    root: null,
    params: state.route.params,
    query,
    signal: controller.signal,
    isCurrent: () => seq === state.routeSeq,
    onCleanup: (fn) => { state.cleanups.push(fn); },
    interval: (fn, ms) => {
      const id = setInterval(fn, ms);
      state.cleanups.push(() => clearInterval(id));
      return id;
    },
    timeout: (fn, ms) => {
      const id = setTimeout(fn, ms);
      state.cleanups.push(() => clearTimeout(id));
      return id;
    },
    listen: (target, event, fn, options) => {
      target.addEventListener(event, fn, options);
      state.cleanups.push(() => target.removeEventListener(event, fn, options));
    },
    setTitle: (title, crumb = '') => {
      const el = document.getElementById('topbar-title');
      const crumbEl = document.getElementById('topbar-crumb');
      if (el) el.textContent = title;
      if (crumbEl && crumb) crumbEl.textContent = crumb;
      document.title = `${title} · Alt8Mail System Control`;
    },
    refreshConfigSync,
    errorDetail,
    handleError: (error, title = 'Action failed') => {
      if (error?.name === 'AbortError') return;
      ui.toast(errorDetail(error), 'critical', { title });
    },
    renderBanner: renderConfigSyncBanner,
  };
  return ctx;
}

async function route({ replace = false, silent = false } = {}) {
  cleanupRoute();
  closeProfileMenu();
  document.body.classList.remove('is-mobile-nav-open');
  const parsed = parseRoute();
  state.route = parsed;
  const def = ROUTE_DEFS[parsed.name] || ROUTE_DEFS.dashboard;
  const view = document.getElementById('view');
  if (!view) return;
  updateChrome(parsed, def);
  document.title = `${def.title} · Alt8Mail System Control`;
  if (state.user && def.perm && !can(def.perm)) {
    view.innerHTML = forbiddenMarkup();
    document.getElementById('retry-forbidden')?.addEventListener('click', () => navigate('dashboard'));
    return;
  }
  const seq = ++state.routeSeq;
  const ctx = makeContext(seq, parsed.query);
  ctx.root = view;
  if (!replace) window.scrollTo({ top: 0, behavior: 'auto' });
  view.innerHTML = ui.loadingState(`Loading ${def.title.toLowerCase()}…`, 5);
  try {
    await def.render(ctx, parsed.params, parsed.query);
    if (seq !== state.routeSeq) return;
    ui.bindLineChart(view);
    const retry = view.querySelector('[data-retry]');
    if (retry) retry.addEventListener('click', () => route());
  } catch (error) {
    if (error?.name === 'AbortError' || seq !== state.routeSeq) return;
    view.innerHTML = `<div class="error-state" role="alert"><div class="error-icon">${ui.icon('alert', 22)}</div><div><h3>Could not load ${esc(def.title.toLowerCase())}</h3><p>${esc(errorDetail(error))}</p></div><button class="btn btn-secondary" type="button" id="route-retry">${ui.icon('refresh', 15)} Try again</button></div>`;
    view.querySelector('#route-retry')?.addEventListener('click', () => route());
  }
}

function updateChrome(parsed, def) {
  document.querySelectorAll('.nav-item').forEach((item) => {
    item.classList.toggle('is-active', item.dataset.nav === def.nav);
    if (item.dataset.nav === def.nav) item.setAttribute('aria-current', 'page');
    else item.removeAttribute('aria-current');
  });
  const title = document.getElementById('topbar-title');
  const crumb = document.getElementById('topbar-crumb');
  if (title) title.textContent = def.title;
  if (crumb) crumb.textContent = def.crumb;
}

function forbiddenMarkup() {
  return `<div class="empty-state" style="padding-top:72px">
    <div class="empty-icon">${ui.icon('lock', 23)}</div>
    <h3>This section is outside your role</h3>
    <p>Your panel role does not include permission to view this area. Ask a superadmin if you need access.</p>
    <button class="btn btn-secondary" type="button" id="retry-forbidden">Return to dashboard</button>
  </div>`;
}

/* --------------------------------------------------------------- command palette */

function openCommandPalette() {
  closeProfileMenu();
  const items = [];
  NAV_GROUPS.forEach((group) => {
    group.items.filter((item) => can(item.perm)).forEach((item) => {
      items.push({ label: item.label, group: group.label, icon: item.icon, href: item.href, desc: item.desc });
    });
  });
  items.push({ label: 'My security', group: 'Account', icon: 'user', href: '#/account', desc: 'Password, TOTP and sessions' });
  items.push({ label: 'Toggle theme', group: 'Actions', icon: 'moon', action: 'theme', desc: 'Switch light or dark appearance' });
  items.push({ label: 'Refresh this view', group: 'Actions', icon: 'refresh', action: 'refresh', desc: 'Reload the current section' });
  const modal = ui.openModal({
    title: 'Search System Control', size: 'medium',
    body: `<div class="palette-search">${ui.icon('search', 17)}<input class="input" id="palette-input" placeholder="Jump to a section or action…" autocomplete="off" aria-label="Search sections"></div><div class="palette-list" id="palette-list"></div>`,
  });
  const input = modal.root.querySelector('#palette-input');
  const list = modal.root.querySelector('#palette-list');
  let focused = 0;
  let filtered = items.slice();
  const paint = () => {
    if (!filtered.length) { list.innerHTML = `<div class="palette-empty">No sections match that search.</div>`; return; }
    let lastGroup = '';
    list.innerHTML = filtered.map((item, index) => {
      const group = item.group !== lastGroup ? `<div class="section-label">${esc(item.group)}</div>` : '';
      lastGroup = item.group;
      return `${group}<button type="button" class="palette-item${index === focused ? ' is-focused' : ''}" data-index="${index}">${ui.icon(item.icon, 16)}<span>${esc(item.label)}</span><small>${esc(item.desc || '')}</small></button>`;
    }).join('');
    list.querySelectorAll('.palette-item').forEach((button) => {
      button.addEventListener('click', () => choose(filtered[Number(button.dataset.index)]));
      button.addEventListener('mouseenter', () => { focused = Number(button.dataset.index); });
    });
  };
  const choose = (item) => {
    modal.close();
    if (item.action === 'theme') toggleTheme();
    if (item.action === 'refresh') route();
    if (item.href) navigate(item.href.replace(/^#\//, ''));
  };
  const filter = () => {
    const query = input.value.trim().toLowerCase();
    filtered = items.filter((item) => `${item.label} ${item.group} ${item.desc || ''}`.toLowerCase().includes(query));
    focused = 0;
    paint();
  };
  input.addEventListener('input', filter);
  input.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); focused = Math.min(filtered.length - 1, focused + 1); paint(); }
    if (event.key === 'ArrowUp') { event.preventDefault(); focused = Math.max(0, focused - 1); paint(); }
    if (event.key === 'Enter' && filtered[focused]) { event.preventDefault(); choose(filtered[focused]); }
  });
  paint();
}

/* --------------------------------------------------------------- routes: dashboard */

async function renderDashboard(ctx, params, query) {
  const data = await ctx.api('/control/api/dashboard');
  if (!ctx.isCurrent()) return;
  const host = data.host || {};
  const services = Array.isArray(data.services) ? data.services : [];
  const activeServices = services.filter((s) => s.active).length;
  const traffic = data.traffic || {};
  const counts = data.counts || {};
  const todaySent = traffic.today?.sent || 0;
  const todayReceived = traffic.today?.received || 0;
  const volume = Array.isArray(data.volume) ? data.volume : [];
  const hostHistory = Array.isArray(data.hostHistory) ? data.hostHistory : [];
  ctx.state.dashboardPeriod = Number(query.days) === 7 ? 7 : (ctx.state.dashboardPeriod || 30);
  const period = ctx.state.dashboardPeriod;
  const labels = volume.slice(-period).map((row) => row.day.slice(5));
  const series = [
    { name: 'Sent', values: volume.slice(-period).map((row) => row.sent), color: 0 },
    { name: 'Received', values: volume.slice(-period).map((row) => row.received), color: 1 },
    { name: 'Bounced', values: volume.slice(-period).map((row) => row.bounced), dashed: true },
    { name: 'Deferred', values: volume.slice(-period).map((row) => row.deferred), dashed: true },
  ];
  const certRows = Array.isArray(data.certificates) ? data.certificates : [];
  const topDomains = Array.isArray(data.topDomains) ? data.topDomains : [];
  const recentAudit = Array.isArray(data.recentAudit) ? data.recentAudit : [];
  const deliverability = data.deliverabilityHealth || {};
  const deliverabilityIps = Array.isArray(deliverability.ips) ? deliverability.ips : [];
  const securityReports = data.securityReports || {};
  const pendingReports = Number(securityReports.counts?.pending || 0);
  const latestReport = Array.isArray(securityReports.recent) ? securityReports.recent[0] : null;
  const queue = Number(data.queueMessages) || 0;
  const sync = data.configSync || {};
  const syncDrift = Array.isArray(sync.drift) ? sync.drift.filter((item) => item && item !== 'No drift detected') : [];
  const totalTop = topDomains.reduce((sum, row) => sum + Number(row.count || 0), 0);
  ctx.setTitle('System overview', 'Overview');

  ctx.root.innerHTML = `
    ${ui.pageHeader({
      eyebrow: 'Control plane',
      title: 'System overview',
      description: 'Mail flow, host pressure and service state across the Alt8Mail platform.',
      meta: `<span class="icon-text">${ui.icon('clock', 13)} Sampled ${esc(fmt.timeAgo(data.checkedAt))}</span><span class="dot-sep"></span><span class="icon-text">${ui.icon('shield', 13)} ${esc(state.env || 'staging')} environment</span><span class="dot-sep"></span><span class="mono">${esc(state.version || APP_VERSION)}</span>`,
      actions: `<button class="btn btn-secondary" type="button" id="dashboard-diagnostics">${ui.icon('activity', 15)} Diagnostics</button><button class="btn btn-secondary" type="button" id="dashboard-refresh">${ui.icon('refresh', 15)} Refresh</button>${can('settings.write') ? `<button class="btn btn-primary" type="button" id="dashboard-sync">${ui.icon('layers', 15)} Run sync</button>` : ''}`,
    })}
    <div class="stat-grid">
      ${ui.statTile({ label: 'Sent today', value: ui.num(todaySent), detail: 'Outbound, since midnight', iconName: 'send', href: '#/logs?direction=outbound', tone: 'info' })}
      ${ui.statTile({ label: 'Received today', value: ui.num(todayReceived), detail: 'Inbound, since midnight', iconName: 'inbox', href: '#/logs?direction=inbound' })}
      ${ui.statTile({ label: 'Queue', value: ui.num(queue), detail: queue ? 'Messages waiting in Postfix' : 'Queue is clear', iconName: 'queue', href: '#/queue', tone: queue ? 'warning' : 'good' })}
      ${ui.statTile({ label: 'Bounces · 30d', value: ui.num(data.bounces || 0), detail: 'Delivery failures', iconName: 'x-circle', href: '#/delivery', tone: Number(data.bounces) ? 'critical' : 'good' })}
      ${ui.statTile({ label: 'Deferrals · 30d', value: ui.num(data.deferrals || 0), detail: 'Temporary failures', iconName: 'clock', href: '#/delivery', tone: Number(data.deferrals) ? 'warning' : 'good' })}
      ${ui.statTile({ label: 'Services', value: `${activeServices}/${services.length}`, detail: activeServices === services.length ? 'All monitored units up' : 'Attention required', iconName: 'server', href: '#/services', tone: activeServices === services.length ? 'good' : 'critical' })}
    </div>
    ${pendingReports ? `<a class="notice tone-critical security-report-alert mt-3" href="#/security-reports">${ui.icon('alert-circle', 17)}<div><strong>${ui.num(pendingReports)} security report${pendingReports === 1 ? '' : 's'} awaiting review</strong><p>${latestReport ? `${esc(latestReport.senderEmail || 'Unknown sender')} · ${esc(latestReport.reason || 'reported mail')}${latestReport.subject ? ` · ${esc(latestReport.subject)}` : ''}` : 'Reported senders are blocked at the Postfix policy layer while review is pending.'}</p></div><span class="btn btn-secondary btn-sm">Review reports ${ui.icon('arrow-up-right', 13)}</span></a>` : ''}

    <div class="grid grid-main mt-4">
      <section class="card">
        <header class="card-head"><div><h3>Mail volume</h3><p>Outbound and inbound events per day · last ${period} days</p></div><div class="card-actions"><div class="segmented" id="period-toggle"><button type="button" data-days="7" class="${period === 7 ? 'is-active' : ''}">7 days</button><button type="button" data-days="30" class="${period === 30 ? 'is-active' : ''}">30 days</button></div></div></header>
        <div class="card-body">
          <div id="dashboard-chart">${ui.lineChart({ labels, series, height: 268, ariaLabel: 'Sent, received, bounced and deferred mail over the last ' + period + ' days' })}</div>
          <div class="host-summary"><span><b>${ui.num(todaySent)}</b> sent today</span><span><b>${ui.num(todayReceived)}</b> received today</span><span><b>${ui.num(data.bounces || 0)}</b> bounced (30d)</span><span><b>${ui.num(data.deferrals || 0)}</b> deferred (30d)</span></div>
        </div>
      </section>
      <section class="card">
        <header class="card-head"><div><h3>Host health</h3><p>${esc(host.updatedAt ? `Sampled ${fmt.timeAgo(host.updatedAt)}` : 'Live system pressure')}</p></div><div class="card-actions"><a class="btn btn-ghost btn-sm" href="#/services">${ui.icon('server', 13)} Services</a></div></header>
        <div class="card-body">
          <div class="host-metrics">
            <div class="host-metric">${ui.meter({ label: 'CPU', value: host.cpuPercent, detail: `${ui.num(host.cpuPercent)}% busy` })}${ui.sparkline(hostHistory.map((p) => p.cpu), { width: 220, height: 30, label: 'CPU trend' })}</div>
            <div class="host-metric">${ui.meter({ label: 'Memory', value: host.memoryPercent, detail: `${ui.bytes(host.memoryUsedBytes)} of ${ui.bytes(host.memoryTotalBytes)}` })}${ui.sparkline(hostHistory.map((p) => p.memory), { width: 220, height: 30, label: 'Memory trend' })}</div>
            <div class="host-metric">${ui.meter({ label: 'Disk', value: host.diskPercent, detail: `${ui.bytes(host.diskUsedBytes)} of ${ui.bytes(host.diskTotalBytes)}` })}${ui.sparkline(hostHistory.map((p) => p.disk), { width: 220, height: 30, label: 'Disk trend' })}</div>
          </div>
          <div class="host-summary">
            <span>Uptime <b>${esc(fmt.duration(host.uptimeSeconds))}</b></span>
            <span>Load <b>${Number(host.load1 || 0).toFixed(2)} / ${Number(host.load5 || 0).toFixed(2)} / ${Number(host.load15 || 0).toFixed(2)}</b></span>
            <span class="package-count">${ui.icon(Number(host.packageUpdates) ? 'alert-circle' : 'check-circle', 13)} <strong>${ui.num(host.packageUpdates || 0)}</strong> package update${Number(host.packageUpdates) === 1 ? '' : 's'}</span>
          </div>
          <div class="deliverability-health">
            <div class="health-subhead">
              <div><strong>Deliverability Health</strong><span>System IP${deliverabilityIps.length === 1 ? '' : 's'} used by Postfix · DNS blocklists</span></div>
              ${can('delivery.manage') ? `<button class="btn btn-ghost btn-sm" type="button" id="dashboard-blocklist-check">${ui.icon('refresh', 13)} Check now</button>` : ''}
            </div>
            ${deliverabilityIps.length ? `<div class="rep-health-list">${deliverabilityIps.map((entry) => {
              const state = entry.state || 'unchecked';
              const label = state === 'listed'
                ? `Listed on ${(entry.listedZones || []).join(', ')}`
                : state === 'clear'
                  ? `Clear on all ${ui.num(entry.zonesTotal)} blocklists`
                  : state === 'blocked'
                    ? `Resolver blocked on ${(entry.blockedZones || []).join(', ')}`
                    : state === 'partial'
                      ? `${ui.num(entry.zonesChecked)} of ${ui.num(entry.zonesTotal)} blocklists checked`
                      : 'Not checked yet';
              const stateLabel = state === 'listed' ? 'Listed' : state === 'clear' ? 'Clear' : state === 'blocked' ? 'Unverified' : state === 'partial' ? 'Partial' : 'Unchecked';
              return `<div class="rep-ip is-${esc(state)}"><span class="rep-pulse" aria-hidden="true"></span><div class="rep-ip-copy"><strong class="mono">${esc(entry.ip)}</strong><span>${esc(label)}</span></div><span class="rep-ip-state">${esc(stateLabel)}</span></div>`;
            }).join('')}</div>` : `<div class="notice tone-warning">${ui.icon('alert', 15)}<div><strong>No outbound IP registered</strong><p>Add the server's public sending IP in <a href="#/relay">Relay &amp; outbound</a> so reputation can be tracked.</p></div></div>`}
            <div class="rep-health-foot">
              <span class="${deliverability.overall === 'listed' ? 'text-critical' : deliverability.overall === 'clear' ? 'text-good' : deliverability.overall === 'blocked' ? 'text-warning' : 'muted'}">${deliverability.overall === 'listed' ? `${ui.num(deliverability.listedIps)} IP${Number(deliverability.listedIps) === 1 ? '' : 's'} currently listed` : deliverability.overall === 'clear' ? 'All monitored IPs are clear' : deliverability.overall === 'blocked' ? 'Some zones could not be verified' : 'Reputation check incomplete'}</span>
              <span>${deliverability.lastCheckedAt ? `Last checked ${esc(fmt.timeAgo(deliverability.lastCheckedAt))}` : 'Never checked'}</span>
            </div>
            ${deliverability.overall === 'listed' ? `<div class="notice tone-critical" style="margin-top:9px">${ui.icon('alert-circle', 16)}<div><strong>Immediate action required</strong><p>Request delisting from the named blocklists and review outbound authentication, relay settings and recent account activity.</p></div></div>` : ''}
            ${deliverability.overall === 'blocked' ? `<div class="notice tone-warning" style="margin-top:9px">${ui.icon('alert', 16)}<div><strong>Some blocklists declined the query</strong><p>Spamhaus and similar zones reject queries from public resolvers. Configure a resolver permitted by the zone operator, then run Check now again; unverified zones are not treated as clear.</p></div></div>` : ''}
          </div>
        </div>
      </section>
    </div>

    <div class="grid grid-main mt-4">
      <section class="card">
        <header class="card-head"><div><h3>Core services</h3><p>Live systemd health · HTMX-ready fragment</p></div><div class="card-actions"><button class="btn btn-ghost btn-sm" type="button" id="services-refresh">${ui.icon('refresh', 13)} Refresh</button><a class="btn btn-ghost btn-sm" href="#/services">Manage ${ui.icon('arrow-up-right', 12)}</a></div></header>
        <div class="card-body" id="dashboard-services">${ui.loadingState('Sampling services…', 2)}</div>
      </section>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Top outbound destinations</h3><p>Recipient domains · 30 days</p></div></header>
          <div class="card-body">${ui.barList(topDomains.map((row) => ({ label: row.domain, count: Number(row.count || 0) })), { total: totalTop, empty: 'No outbound mail recorded yet' })}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Certificates</h3><p>Expiry across managed endpoints</p></div><a class="btn btn-ghost btn-sm" href="#/tls">TLS details ${ui.icon('arrow-up-right', 12)}</a></header>
          <div class="card-body">${certRows.length ? certRows.map((cert) => `<div class="cert-row"><span class="cert-icon ${cert.daysLeft < 14 ? 'bad' : cert.daysLeft < 30 ? 'warn' : ''}">${ui.icon('lock', 14)}</span><div class="grow"><strong class="truncate">${esc(cert.name)}</strong><span class="muted" style="display:block;font-size:10px">${cert.selfSigned ? 'Self-signed' : 'Managed certificate'}</span></div><span class="cert-days ${cert.daysLeft < 14 ? 'text-critical' : cert.daysLeft < 30 ? 'text-warning' : ''}">${cert.daysLeft} days</span></div>`).join('') : ui.emptyState('No certificates discovered', 'Certificates will appear when TLS material is present.', `<a class="btn btn-secondary btn-sm" href="#/tls">Open TLS</a>`, 'lock')}</div>
        </section>
      </div>
    </div>

    <div class="grid grid-main mt-4">
      <section class="card">
        <header class="card-head"><div><h3>Recent operator activity</h3><p>Latest audited changes</p></div><a class="btn btn-ghost btn-sm" href="#/access?tab=audit">Full audit ${ui.icon('arrow-up-right', 12)}</a></header>
        <div class="card-body">${recentAudit.length ? `<div class="list audit-list">${recentAudit.map((row) => `<div class="list-item"><span class="list-icon">${ui.icon(row.action?.includes('password') ? 'key' : row.action?.includes('domain') ? 'globe' : row.action?.includes('service') ? 'server' : 'activity', 13)}</span><div class="list-copy"><strong>${esc(ui.humanTitle(row.action || 'activity'))}</strong><span>${esc(row.actor || 'system')} · ${esc(row.target || '')} ${row.detail ? `· ${esc(ui.truncate(row.detail, 64))}` : ''}</span></div><span class="list-time">${esc(fmt.timeAgo(row.createdAt))}</span></div>`).join('')}</div>` : ui.emptyState('No audit entries yet', 'Operator actions will be recorded here.', '', 'shield')}</div>
      </section>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Platform counts</h3><p>Managed resources</p></div></header>
          <div class="card-body">
            <div class="split-kv">
              <div class="kv-box"><span>Domains</span><strong>${ui.num(counts.domains)}</strong></div>
              <div class="kv-box"><span>Users</span><strong>${ui.num(counts.users)}</strong></div>
              <div class="kv-box"><span>Mailboxes</span><strong>${ui.num(counts.mailboxes)}</strong></div>
              <div class="kv-box"><span>Aliases</span><strong>${ui.num(counts.aliases)}</strong></div>
            </div>
            <div class="divider"></div>
            <div class="tile-link"><span>${ui.icon('shield-off', 15)} Suppressed addresses</span><span class="value">${ui.num(counts.suppressions)}</span></div>
            <div class="tile-link"><span>${ui.icon('mail-open', 15)} Active inboxes</span><span class="value">${ui.num(counts.mailboxes)}</span></div>
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Configuration sync</h3><p>${sync.status === 'never' ? 'Never reconciled' : `Last ${esc(sync.status || 'unknown')} · ${esc(fmt.timeAgo(sync.startedAt))}`}</p></div><a class="btn btn-ghost btn-sm" href="#/sync">Details ${ui.icon('arrow-up-right', 12)}</a></header>
          <div class="card-body">
            ${sync.status === 'never' ? `<div class="notice tone-info">${ui.icon('info', 16)}<div><strong>Never synced</strong><p>Run configuration sync to provision missing DKIM, postmaster mailboxes, Maildirs and Postfix maps.</p></div></div>` : sync.status === 'error' ? `<div class="notice tone-critical">${ui.icon('alert-circle', 16)}<div><strong>Last sync failed</strong><p>${esc(sync.error || 'Open Config sync for details.')}</p></div></div>` : syncDrift.length ? `<div class="notice tone-warning">${ui.icon('alert-circle', 16)}<div><strong>${syncDrift.length} drift item${syncDrift.length === 1 ? '' : 's'}</strong><p>${esc(syncDrift[0])}</p></div></div>` : `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>Resources reconciled</strong><p>${sync.filesChanged ? `${sync.filesChanged} managed files updated in the last run.` : 'No drift detected in the current configuration.'}</p></div></div>`}
            <div class="host-summary"><span>Files changed <b>${ui.num(sync.filesChanged || 0)}</b></span><span>Warnings <b>${ui.num(Array.isArray(sync.warnings) ? sync.warnings.length : 0)}</b></span></div>
          </div>
        </section>
      </div>
    </div>`;

  const loadServices = async (force = false) => {
    const hostEl = document.getElementById('dashboard-services');
    if (!hostEl) return;
    if (force) hostEl.innerHTML = ui.loadingState('Sampling services…', 2);
    try {
      const html = await fetch(`/control/fragments/services${force ? '?refresh=1' : ''}`, { credentials: 'same-origin', cache: 'no-store', signal: ctx.signal }).then((response) => {
        if (!response.ok) throw new Error(`Service fragment returned HTTP ${response.status}`);
        return response.text();
      });
      if (!ctx.isCurrent()) return;
      hostEl.innerHTML = html || ui.emptyState('No service state', 'The monitor has not sampled services yet.', '', 'server');
      const totalUnits = services.length;
      const shown = Math.min(8, totalUnits);
      hostEl.insertAdjacentHTML('beforeend', `<div class="service-digest"><span>Showing ${shown} of ${totalUnits} monitored units</span><a href="#/services">Open all services ${ui.icon('chevron-right', 11)}</a></div>`);
    } catch (error) {
      if (error.name === 'AbortError') return;
      hostEl.innerHTML = ui.inlineNotice('warning', 'Service status is temporarily unavailable', error.message);
    }
  };
  loadServices(false);
  document.getElementById('services-refresh')?.addEventListener('click', () => loadServices(true));

  const paintChart = (days) => {
    const slice = volume.slice(-days);
    const chartLabels = slice.map((row) => row.day.slice(5));
    const chartSeries = [
      { name: 'Sent', values: slice.map((row) => row.sent) },
      { name: 'Received', values: slice.map((row) => row.received) },
      { name: 'Bounced', values: slice.map((row) => row.bounced), dashed: true },
      { name: 'Deferred', values: slice.map((row) => row.deferred), dashed: true },
    ];
    const host = document.getElementById('dashboard-chart');
    if (host) {
      host.innerHTML = ui.lineChart({ labels: chartLabels, series: chartSeries, height: 268, ariaLabel: `Mail volume for the last ${days} days` });
      ui.bindLineChart(host);
    }
  };
  document.querySelectorAll('#period-toggle button').forEach((button) => {
    button.addEventListener('click', () => {
      const days = Number(button.dataset.days);
      state.dashboardPeriod = days;
      document.querySelectorAll('#period-toggle button').forEach((b) => b.classList.toggle('is-active', b === button));
      paintChart(days);
    });
  });

  document.getElementById('dashboard-refresh')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      await route({ silent: true, replace: true });
    } finally {
      button.classList.remove('is-loading');
      button.disabled = false;
    }
  });
  document.getElementById('dashboard-blocklist-check')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      const result = await ctx.api('/control/api/delivery/blocklists/check', { method: 'POST' });
      const listed = (result.results || []).filter((row) => row.listed).length;
      ui.toast(listed ? `${listed} blocklist listing${listed === 1 ? '' : 's'} found.` : 'All checked blocklists are clear.', listed ? 'critical' : 'good', { title: 'Deliverability health' });
      await route({ silent: true, replace: true });
    } catch (error) {
      ui.toast(errorDetail(error), 'critical', { title: 'Blocklist check failed' });
      button.classList.remove('is-loading');
      button.disabled = false;
    }
  });
  document.getElementById('dashboard-diagnostics')?.addEventListener('click', () => openDiagnostics(ctx));
  document.getElementById('dashboard-sync')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      await ctx.api('/control/api/config-sync/run', { method: 'POST' });
      ui.toast('Configuration resources reconciled.', 'good');
      await refreshConfigSync(true);
      await route({ silent: true, replace: true });
    } catch (error) { ctx.handleError(error, 'Configuration sync failed'); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });

  // Background refresh keeps the dashboard current without a full network reload.
  const refresh = () => {
    if (!ctx.isCurrent()) return;
    if (document.visibilityState !== 'visible') return;
    route({ silent: true, replace: true }).catch(() => {});
  };
  ctx.interval(refresh, 45000);
  let mailTimer = null;
  ctx.listen(window, 'control:event', (event) => {
    if (event.detail?.event !== 'mail.event') return;
    if (mailTimer) clearTimeout(mailTimer);
    mailTimer = setTimeout(() => { mailTimer = null; refresh(); }, 8000);
    ctx.onCleanup(() => { if (mailTimer) clearTimeout(mailTimer); });
  });
}

async function openDiagnostics(ctx) {
  const modal = ui.openModal({
    title: 'System diagnostics', subtitle: 'Live checks against PostgreSQL, Postfix, Dovecot, Redis, systemd, storage and DNS.',
    size: 'large', body: ui.loadingState('Running diagnostics…', 4),
    footer: `<span class="muted" style="margin-right:auto;font-size:10.5px">Doctor output is safe to copy into an incident note. Secrets are not included.</span><button class="btn btn-secondary" type="button" id="diag-links">${ui.icon('activity', 14)} Metrics</button><button class="btn btn-secondary" type="button" data-modal-close>Close</button><button class="btn btn-primary" type="button" id="diag-rerun">${ui.icon('refresh', 14)} Run again</button>`,
  });
  const paint = async () => {
    const body = modal.root.querySelector('.modal-body');
    body.innerHTML = ui.loadingState('Running diagnostics…', 4);
    try {
      const payload = await ctx.api('/control/api/diagnostics');
      if (!modal.root.isConnected) return;
      body.innerHTML = diagnosticsMarkup(payload);
    } catch (error) {
      if (modal.root.isConnected) body.innerHTML = ui.inlineNotice('critical', 'Diagnostics failed', errorDetail(error));
    }
  };
  modal.root.querySelector('#diag-rerun')?.addEventListener('click', paint);
  modal.root.querySelector('#diag-links')?.addEventListener('click', () => {
    window.open('/metrics', '_blank', 'noopener');
  });
  await paint();
}

function diagnosticsMarkup(payload) {
  const checks = Array.isArray(payload.checks) ? payload.checks : [];
  return `<div class="diagnostic-summary">${ui.statusPill(payload.ok ? 'pass' : 'fail', payload.ok ? 'Healthy' : 'Action needed')}<span class="summary-pill">${ui.icon('alert-circle', 12)} ${payload.failures} failure${payload.failures === 1 ? '' : 's'}</span><span class="summary-pill">${ui.icon('alert', 12)} ${payload.warnings} warning${payload.warnings === 1 ? '' : 's'}</span><span class="summary-pill">${ui.icon('clock', 12)} ${esc(fmt.timeAgo(payload.checkedAt))}</span></div>
    <div class="diagnostic-list">${checks.map((check) => `<div class="diagnostic-item">${ui.statusPill(check.status, ui.humanTitle(check.status))}<div class="diagnostic-copy"><strong>${esc(check.label)}</strong><p>${esc(check.detail)}</p>${check.fixHint ? `<small>${ui.icon('arrow-up-right', 11)} ${esc(check.fixHint)}</small>` : ''}</div></div>`).join('')}</div>
    <div class="divider"></div>
    <div class="note-strip">${ui.icon('info', 13)} <span>Operational endpoints: <a href="/healthz" target="_blank" rel="noopener">/healthz</a> · <a href="/metrics" target="_blank" rel="noopener">/metrics</a> · <a href="/healthz/stacks" target="_blank" rel="noopener">/healthz/stacks</a>. Stacks output is authenticated and may contain internal state.</span></div>`;
}

async function renderDiagnostics(ctx) {
  const payload = await ctx.api('/control/api/diagnostics');
  if (!ctx.isCurrent()) return;
  ctx.setTitle('Diagnostics', 'Overview');
  ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Overview', title: 'Diagnostics', description: 'The same checks used by the repair doctor, run in-process against the live stack.', actions: `<button class="btn btn-secondary" type="button" id="diag-refresh">${ui.icon('refresh', 15)} Run again</button><button class="btn btn-secondary" type="button" id="diag-metrics">${ui.icon('activity', 15)} Open metrics</button>` })}
    <div id="diagnostics-body">${diagnosticsMarkup(payload)}</div>
    <div class="grid grid-3 mt-4">
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Liveness</h3><p class="form-note">Cheap unauthenticated liveness probe for load balancers and monitoring.</p><div class="code-block" style="margin-top:8px"><pre><code>GET /healthz</code></pre></div></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Prometheus metrics</h3><p class="form-note">Text exposition at <span class="mono">/metrics</span>; no Prometheus server is required to consume it.</p><div class="code-block" style="margin-top:8px"><pre><code>GET /metrics</code></pre></div></div></div>
      <div class="card"><div class="card-body"><h3 style="font-size:12px">Goroutine stacks</h3><p class="form-note">Authenticated stack dump for incident response. Requires services.read or settings.write.</p><div class="code-block" style="margin-top:8px"><pre><code>GET /healthz/stacks</code></pre></div></div></div>
    </div>`;
  const rerun = async () => {
    const body = document.getElementById('diagnostics-body');
    if (!body) return;
    body.innerHTML = ui.loadingState('Running diagnostics…', 4);
    try {
      const next = await ctx.api('/control/api/diagnostics');
      if (ctx.isCurrent()) body.innerHTML = diagnosticsMarkup(next);
    } catch (error) { ctx.handleError(error, 'Diagnostics failed'); }
  };
  document.getElementById('diag-refresh')?.addEventListener('click', rerun);
  document.getElementById('diag-metrics')?.addEventListener('click', () => window.open('/metrics', '_blank', 'noopener'));
}

/* --------------------------------------------------------------- routes: sync */

async function renderSync(ctx) {
  const [current, history] = await Promise.all([
    ctx.api('/control/api/config-sync'),
    ctx.api('/control/api/config-sync/history'),
  ]);
  if (!ctx.isCurrent()) return;
  state.configSync = current;
  state.configSyncLoaded = true;
  updateSyncChip();
  renderConfigSyncBanner();
  const latest = current.latest || {};
  const drift = Array.isArray(latest.drift) ? latest.drift : [];
  const warnings = Array.isArray(latest.warnings) ? latest.warnings : [];
  const runs = Array.isArray(history.runs) ? history.runs : [];
  ctx.setTitle('Config sync', 'Infrastructure');
  ctx.root.innerHTML = `${ui.pageHeader({
    eyebrow: 'Infrastructure', title: 'Configuration sync',
    description: 'Reconcile mail domains, DKIM, Maildirs, alias maps, suppression maps, relay transport and Postfix policy configuration from the control database.',
    meta: latest.startedAt ? `<span>Last run ${esc(fmt.timeAgo(latest.startedAt))} · ${esc(latest.status)}</span>` : '<span>No sync has run yet</span>',
    actions: `${can('settings.write') ? `<button class="btn btn-primary" type="button" id="sync-run">${ui.icon('refresh', 15)} Run configuration sync</button>` : ''}<button class="btn btn-secondary" type="button" id="sync-import">${ui.icon('upload', 15)} Inspect server Maildirs</button>`,
  })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Current state</h3><p>Computed live from the database and managed files</p></div>${ui.statusPill(latest.status || 'unknown')}</header>
          <div class="card-body">
            ${drift.length && drift[0] !== 'No drift detected' ? `<div class="notice tone-warning">${ui.icon('alert-circle', 16)}<div><strong>Drift detected</strong><p>${esc(drift[0])}</p></div></div>` : `<div class="notice tone-good">${ui.icon('check-circle', 16)}<div><strong>No drift detected</strong><p>Database resources and managed mail configuration agree.</p></div></div>`}
            <div class="divider"></div>
            <div class="kv-grid is-3">
              <div><dt>Last status</dt><dd>${ui.statusPill(latest.status || 'never', latest.status || 'Never')}</dd></div>
              <div><dt>Files changed</dt><dd>${ui.num(latest.filesChanged || 0)}</dd></div>
              <div><dt>Warnings</dt><dd>${ui.num(warnings.length)}</dd></div>
              <div><dt>Trigger</dt><dd>${esc(latest.trigger || '—')}</dd></div>
              <div><dt>Started</dt><dd>${esc(fmt.dateTime(latest.startedAt))}</dd></div>
              <div><dt>Finished</dt><dd>${esc(fmt.dateTime(latest.finishedAt))}</dd></div>
            </div>
            ${warnings.length ? `<div class="divider"></div><h4 class="form-section-title">Warnings from the last run</h4><div class="list">${warnings.map((warning) => `<div class="list-item"><span class="list-icon">${ui.icon('alert', 13)}</span><div class="list-copy"><strong class="wrap-anywhere">${esc(warning)}</strong></div></div>`).join('')}</div>` : ''}
            ${drift.length ? `<div class="divider"></div><h4 class="form-section-title">Drift items</h4><div class="list">${drift.map((item) => `<div class="list-item"><span class="list-icon">${ui.icon(item === 'No drift detected' ? 'check' : 'alert-circle', 13)}</span><div class="list-copy"><strong class="wrap-anywhere">${esc(item)}</strong></div></div>`).join('')}</div>` : ''}
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Run history</h3><p>Last 30 reconciliations</p></div></header>
          <div class="card-body is-flush">${runs.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Started</th><th>Trigger</th><th>Status</th><th class="num">Files</th><th>Error</th></tr></thead><tbody>${runs.map((run) => `<tr><td>${esc(fmt.dateTime(run.startedAt))}</td><td>${esc(run.trigger)}</td><td>${ui.statusPill(run.status)}</td><td class="num">${ui.num(run.filesChanged)}</td><td class="wrap-anywhere">${esc(ui.truncate(run.error, 70) || '—')}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No sync runs yet', 'Run the first sync to reconcile mail resources.', '', 'layers')}</div>
        </section>
      </div>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>What sync manages</h3><p>Idempotent resource repair</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('globe', 15)} Domains and DKIM</span><span class="value">${ui.num(latest.details?.domains || 0)}</span></div>
            <div class="tile-link"><span>${ui.icon('mail', 15)} Maildirs</span><span class="value">${ui.num(latest.details?.mailboxes || 0)}</span></div>
            <div class="tile-link"><span>${ui.icon('forward', 15)} Postfix alias map</span><span class="value">${drift.some((d) => d.includes('alias')) ? 'Drift' : 'OK'}</span></div>
            <div class="tile-link"><span>${ui.icon('shield-off', 15)} Suppression maps</span><span class="value">${drift.some((d) => d.includes('suppression')) ? 'Drift' : 'OK'}</span></div>
            <div class="tile-link"><span>${ui.icon('lock', 15)} DKIM selector map</span><span class="value">${drift.some((d) => d.includes('selector')) ? 'Drift' : 'OK'}</span></div>
            <div class="tile-link"><span>${ui.icon('relay', 15)} Relay and transport</span><span class="value">managed</span></div>
            <div class="note-strip" style="margin-top:11px">${ui.icon('info', 13)} <span>The Postfix policy daemon is configured during sync. It enforces per-domain rolling caps inside PostgreSQL.</span></div>
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Server Maildir adoption</h3><p>Preview mailboxes present on disk but absent from PostgreSQL</p></div></header>
          <div class="card-body" id="sync-preview"><p class="form-note">Run the preview to compare <span class="mono">/var/vmail</span> with managed mailboxes. No changes are made until you import.</p><button class="btn btn-secondary btn-sm" type="button" id="sync-preview-btn">${ui.icon('search', 13)} Preview Maildirs</button></div>
        </section>
      </div>
    </div>`;

  document.getElementById('sync-run')?.addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      await ctx.api('/control/api/config-sync/run', { method: 'POST' });
      ui.toast('Configuration sync completed.', 'good');
      await refreshConfigSync(true);
      await renderSync(ctx);
    } catch (error) { ctx.handleError(error, 'Configuration sync failed'); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('sync-import')?.addEventListener('click', () => document.getElementById('sync-preview-btn')?.click());
  const preview = async () => {
    const host = document.getElementById('sync-preview');
    host.innerHTML = ui.loadingState('Inspecting /var/vmail…', 3);
    try {
      const payload = await ctx.api('/control/api/config-sync/import-preview');
      if (!ctx.isCurrent()) return;
      const items = Array.isArray(payload.items) ? payload.items : [];
      const missing = items.filter((item) => !item.exists);
      host.innerHTML = items.length ? `<div class="notice tone-info">${ui.icon('info', 16)}<div><strong>${ui.num(missing.length)} unmanaged mailbox${missing.length === 1 ? '' : 'es'} found</strong><p>${ui.num(items.length)} Maildirs scanned; existing accounts are not touched.</p></div></div>
        <div class="table-wrap" style="margin-top:10px;max-height:260px"><table class="data compact"><thead><tr><th>Address</th><th class="num">Messages</th><th>State</th></tr></thead><tbody>${items.slice(0, 200).map((item) => `<tr><td class="mono">${esc(item.email)}</td><td class="num">${ui.num(item.messages)}</td><td>${item.exists ? ui.badge('Managed', 'good') : ui.badge('On disk only', 'warning')}</td></tr>`).join('')}</tbody></table></div>
        ${missing.length && can('settings.write') ? `<div class="form-actions"><span class="form-note">Importing creates webmail accounts with generated passwords and links the existing Maildir.</span><button class="btn btn-primary btn-sm" type="button" id="sync-import-all">${ui.icon('upload', 13)} Import ${missing.length} mailbox${missing.length === 1 ? '' : 'es'}</button></div>` : ''}`
        : ui.emptyState('No Maildirs found', 'The configured mail store has no domain directories.', '', 'archive');
      document.getElementById('sync-import-all')?.addEventListener('click', async (event) => {
        const ok = await ui.confirmDialog({ title: 'Adopt server Maildirs', message: `Create ${missing.length} webmail accounts for Maildirs that exist on disk? Existing accounts are not modified.`, confirmLabel: 'Import mailboxes', tone: 'primary' });
        if (!ok) return;
        const button = event.currentTarget;
        button.classList.add('is-loading');
        try {
          const result = await ctx.api('/control/api/config-sync/import', { method: 'POST', body: { emails: missing.map((item) => item.email) } });
          ui.toast(`${result.created.length} mailbox${result.created.length === 1 ? '' : 'es'} imported.`, 'good');
          if (result.warnings?.length) ui.toast(`${result.warnings.length} warning${result.warnings.length === 1 ? '' : 's'} returned; review the response.`, 'warning');
          await preview();
        } catch (error) { ctx.handleError(error, 'Maildir import failed'); }
        finally { button.classList.remove('is-loading'); }
      });
    } catch (error) {
      host.innerHTML = ui.inlineNotice('critical', 'Maildir preview failed', errorDetail(error));
    }
  };
  document.getElementById('sync-preview-btn')?.addEventListener('click', preview);
}

/* --------------------------------------------------------------- routes: account */

async function renderAccount(ctx) {
  const [me, sessions] = await Promise.all([
    ctx.api('/control/api/me'),
    ctx.api('/control/api/me/sessions'),
  ]);
  if (!ctx.isCurrent()) return;
  state.user = me.user;
  state.permissions = new Set(me.user.permissions || []);
  const user = me.user;
  ctx.setTitle('My security', 'Account');
  const sessionRows = Array.isArray(sessions.sessions) ? sessions.sessions : [];
  ctx.root.innerHTML = `${ui.pageHeader({ eyebrow: 'Account', title: 'My security', description: 'Protect your operator account and review where it is signed in.', meta: `<span>${esc(user.email)}</span><span class="dot-sep"></span><span class="role-chip">${esc(user.role)}</span><span class="dot-sep"></span>${user.totpEnabled ? ui.statusPill('active', 'TOTP enabled') : ui.statusPill('warning', 'TOTP not enabled')}` })}
    <div class="grid grid-main">
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Two-factor authentication</h3><p>Time-based one-time passwords for panel sign-in</p></div>${user.totpEnabled ? ui.badge('Enabled', 'good', 'shield-check') : ui.badge('Not enabled', 'warning', 'alert')}</header>
          <div class="card-body">
            ${user.totpEnabled ? `<p class="form-note">Your account requires a six-digit authenticator code after the password step. Store recovery access with your normal identity process; this panel does not issue bypass codes.</p><div class="form-actions"><span class="form-note">Disabling TOTP requires both your password and a current code.</span><button class="btn btn-soft-danger" type="button" id="totp-disable">${ui.icon('shield-off', 14)} Disable TOTP</button></div>` : `<p class="form-note">Add this account to an authenticator app. TOTP is checked with a one-step clock drift window, and five incorrect challenge codes invalidate the challenge.</p><div class="form-actions"><span class="form-note">The shared secret is shown once during setup.</span><button class="btn btn-primary" type="button" id="totp-begin">${ui.icon('shield-check', 14)} Set up authenticator</button></div>`}
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Change password</h3><p>Other sessions are revoked after a successful change</p></div></header>
          <div class="card-body">
            <form id="password-form" class="form-grid is-single">
              ${ui.field({ label: 'Current password', name: 'currentPassword', type: 'password', required: true, autocomplete: 'current-password' })}
              ${ui.field({ label: 'New password', name: 'newPassword', type: 'password', required: true, help: 'At least 12 characters. Use a unique passphrase.', autocomplete: 'new-password' })}
              ${ui.field({ label: 'Confirm new password', name: 'confirmPassword', type: 'password', required: true, autocomplete: 'new-password' })}
              <div class="form-actions" style="margin-top:4px"><span class="form-note">All other active sessions for this account are revoked.</span><div class="form-actions-right"><button class="btn btn-primary" type="submit">Update password</button></div></div>
            </form>
          </div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Active sessions</h3><p>${sessionRows.length} session${sessionRows.length === 1 ? '' : 's'} for this account</p></div><button class="btn btn-secondary btn-sm" type="button" id="revoke-others">${ui.icon('logout', 13)} Revoke other sessions</button></header>
          <div class="card-body is-flush">${sessionRows.length ? `<div class="table-wrap is-bare"><table class="data"><thead><tr><th>Device / IP</th><th>Last seen</th><th>Expires</th><th></th></tr></thead><tbody>${sessionRows.map((session) => `<tr><td><span class="cell-main">${session.id === me.session.id ? ui.badge('This session', 'good') : esc(ui.truncate(session.userAgent, 58) || 'Unknown client')}</span><span class="cell-sub mono">${esc(session.ip)}</span></td><td>${esc(fmt.timeAgo(session.lastSeenAt))}</td><td>${esc(fmt.dateTime(session.expiresAt))}</td><td class="actions">${session.id === me.session.id ? '' : `<button class="icon-btn is-danger" type="button" data-revoke-session="${esc(session.id)}" title="Revoke session" aria-label="Revoke session">${ui.icon('logout', 15)}</button>`}</td></tr>`).join('')}</tbody></table></div>` : ui.emptyState('No active sessions', '', '', 'shield')}</div>
        </section>
      </div>
      <div class="stack">
        <section class="card">
          <header class="card-head"><div><h3>Account</h3><p>Panel identity</p></div></header>
          <div class="card-body">${ui.kv([
            ['Name', esc(user.name || '—')],
            ['Email', `<span class="mono">${esc(user.email)}</span>`],
            ['Role', `<span class="role-chip">${esc(user.role)}</span>`],
            ['Groups', esc(user.groups?.length ? user.groups.join(', ') : 'No additional groups')],
            ['Last sign-in', esc(fmt.dateTime(user.lastLoginAt))],
            ['Session TTL', `${ui.num(Math.round((user.sessionTtlSeconds || 0) / 60))} minutes`],
          ])}</div>
        </section>
        <section class="card">
          <header class="card-head"><div><h3>Session policy</h3><p>Applies to this account</p></div></header>
          <div class="card-body">
            <div class="tile-link"><span>${ui.icon('clock', 15)} Idle check</span><span class="value">60 seconds</span></div>
            <div class="tile-link"><span>${ui.icon('lock', 15)} Cookie</span><span class="value">HttpOnly · Secure · Strict</span></div>
            <div class="tile-link"><span>${ui.icon('shield', 15)} Request protection</span><span class="value">Signed CSRF token</span></div>
            <div class="tile-link"><span>${ui.icon('key', 15)} Password hash</span><span class="value">Argon2id</span></div>
          </div>
        </section>
      </div>
    </div>`;

  document.getElementById('password-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = ui.formValues(form);
    if (values.newPassword !== values.confirmPassword) { ui.toast('The new passwords do not match.', 'critical'); return; }
    if ((values.newPassword || '').length < 12) { ui.toast('Use at least 12 characters for the new password.', 'critical'); return; }
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading');
    button.disabled = true;
    try {
      await ctx.api('/control/api/me/password', { method: 'POST', body: { currentPassword: values.currentPassword, newPassword: values.newPassword } });
      ui.toast('Password updated. Other sessions were revoked.', 'good');
      form.reset();
    } catch (error) { ctx.handleError(error, 'Password change failed'); }
    finally { button.classList.remove('is-loading'); button.disabled = false; }
  });
  document.getElementById('revoke-others')?.addEventListener('click', async () => {
    const ok = await ui.confirmDialog({ title: 'Revoke other sessions', message: 'Sign out every other browser session for your account? This session stays active.', confirmLabel: 'Revoke others', tone: 'danger' });
    if (!ok) return;
    try {
      const result = await ctx.api('/control/api/me/sessions/revoke-others', { method: 'POST' });
      ui.toast(`${result.revoked || 0} session${result.revoked === 1 ? '' : 's'} revoked.`, 'good');
      await renderAccount(ctx);
    } catch (error) { ctx.handleError(error, 'Could not revoke sessions'); }
  });
  ctx.root.querySelectorAll('[data-revoke-session]').forEach((button) => {
    button.addEventListener('click', async () => {
      const ok = await ui.confirmDialog({ title: 'Revoke session', message: 'That browser will be signed out immediately.', confirmLabel: 'Revoke session', tone: 'danger' });
      if (!ok) return;
      try {
        await ctx.api(`/control/api/me/sessions/${button.dataset.revokeSession}`, { method: 'DELETE' });
        ui.toast('Session revoked.', 'good');
        await renderAccount(ctx);
      } catch (error) { ctx.handleError(error, 'Could not revoke session'); }
    });
  });
  document.getElementById('totp-begin')?.addEventListener('click', () => beginTOTP(ctx));
  document.getElementById('totp-disable')?.addEventListener('click', () => disableTOTP(ctx));
}

async function beginTOTP(ctx) {
  let payload;
  try { payload = await ctx.api('/control/api/me/totp/begin', { method: 'POST' }); }
  catch (error) { ctx.handleError(error, 'Could not start TOTP setup'); return; }
  const modal = ui.openModal({
    title: 'Set up authenticator', subtitle: 'Scan the code or enter the secret manually, then verify one code.',
    size: 'small',
    body: `<div style="display:flex;justify-content:center;padding:7px 0 14px"><div id="totp-qr" style="background:#fff;padding:10px;border-radius:9px;border:1px solid var(--border)"></div></div>
      <div class="field"><label>Manual setup secret</label><div class="input-group"><input class="input mono" readonly value="${ui.attr(payload.secret)}"><button class="btn btn-secondary" type="button" data-copy="${ui.attr(payload.secret)}" title="Copy secret">${ui.icon('copy', 14)}</button></div><small class="field-help">Time-based, 6 digits, 30-second period. Keep this secret private.</small></div>
      <div class="divider"></div>
      <form id="totp-finish-form"><div class="field"><label for="totp-finish-code">Verify a code</label><input class="input totp-input" id="totp-finish-code" name="code" inputmode="numeric" maxlength="6" pattern="[0-9]{6}" placeholder="000000" autocomplete="one-time-code" required><small class="field-help">Enter the current six-digit code from the app to prove setup.</small></div><div class="form-actions"><span class="form-note">TOTP will be required at the next sign-in.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button class="btn btn-primary" type="submit">Enable TOTP</button></div></div></form>`,
    footer: '',
  });
  const qrHost = modal.root.querySelector('#totp-qr');
  if (window.QRCode && qrHost) {
    new window.QRCode(qrHost, { text: payload.otpauth, width: 176, height: 176, correctLevel: window.QRCode.CorrectLevel.M });
  } else if (qrHost) {
    qrHost.innerHTML = `<div class="form-note" style="max-width:210px">QR rendering unavailable. Add this URI to your authenticator:<br><span class="mono break-all">${esc(payload.otpauth)}</span></div>`;
  }
  const input = modal.root.querySelector('#totp-finish-code');
  input?.addEventListener('input', () => { input.value = input.value.replace(/\D/g, '').slice(0, 6); });
  modal.root.querySelector('#totp-finish-form')?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = event.currentTarget.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api('/control/api/me/totp/finish', { method: 'POST', body: { code: input.value.trim() } });
      modal.close();
      ui.toast('Two-factor authentication is enabled.', 'good');
      state.user.totpEnabled = true;
      await renderAccount(ctx);
    } catch (error) { ctx.handleError(error, 'TOTP verification failed'); button.classList.remove('is-loading'); button.disabled = false; }
  });
}

function disableTOTP(ctx) {
  const modal = ui.openModal({
    title: 'Disable two-factor authentication', subtitle: 'Enter your password and a current authenticator code.',
    size: 'small',
    body: `<div class="notice tone-warning">${ui.icon('alert', 16)}<div><strong>This weakens account protection</strong><p>Only continue if you are replacing the authenticator device.</p></div></div><form id="totp-disable-form" style="margin-top:13px">${ui.field({ label: 'Panel password', name: 'password', type: 'password', required: true, autocomplete: 'current-password' })}<div style="height:12px"></div>${ui.field({ label: 'Authenticator code', name: 'code', type: 'text', required: true, extra: 'inputmode="numeric" maxlength="6" autocomplete="one-time-code"' })}<div class="form-actions"><span class="form-note">This action is audited.</span><div class="form-actions-right"><button type="button" class="btn btn-ghost" data-modal-close>Cancel</button><button type="submit" class="btn btn-danger">Disable TOTP</button></div></div></form>`,
  });
  const form = modal.root.querySelector('#totp-disable-form');
  form?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const values = ui.formValues(form);
    const button = form.querySelector('button[type="submit"]');
    button.classList.add('is-loading'); button.disabled = true;
    try {
      await ctx.api('/control/api/me/totp/disable', { method: 'POST', body: { password: values.password, code: values.code } });
      modal.close();
      ui.toast('Two-factor authentication disabled.', 'warning');
      state.user.totpEnabled = false;
      await renderAccount(ctx);
    } catch (error) { ctx.handleError(error, 'Could not disable TOTP'); button.classList.remove('is-loading'); button.disabled = false; }
  });
}

/* --------------------------------------------------------------- global bindings */

function bindGlobal() {
  if (state.globalBound) return;
  state.globalBound = true;
  document.addEventListener('click', (event) => {
    const copyTarget = event.target.closest('[data-copy]');
    if (copyTarget) {
      event.preventDefault();
      ui.copy(copyTarget.dataset.copy || '');
    }
    if (event.target.closest('#theme-toggle')) return;
  });
  document.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      if (state.user) openCommandPalette();
    }
    if (event.key === 'Escape') {
      document.body.classList.remove('is-mobile-nav-open');
    }
  });
  window.addEventListener('hashchange', () => { if (state.user) route(); });
  window.addEventListener('beforeunload', () => {
    if (state.sse) state.sse.close();
    state.clockTimer && clearInterval(state.clockTimer);
  });
}

/* --------------------------------------------------------------- boot */

async function boot() {
  bindGlobal();
  try {
    const session = await api('/control/api/session');
    state.csrf = session.csrfToken || '';
    state.version = session.version || APP_VERSION;
    state.env = session.env || 'staging';
    if (session.serverTime) {
      const server = Date.parse(session.serverTime);
      if (Number.isFinite(server)) state.serverOffset = server - Date.now();
    }
    if (session.authenticated && session.user) {
      setSession(session);
      if (!location.hash || location.hash === '#') location.hash = '#/dashboard';
      renderShell();
      connectSSE();
      await refreshConfigSync(true);
      await route({ replace: true });
    } else {
      renderLogin(session);
    }
  } catch (error) {
    document.getElementById('app').innerHTML = `<div class="login-page"><div class="login-main"><div class="login-main-inner"><div class="login-mobile-brand"><span class="brand-mark">${ui.icon('shield-check', 21)}</span><span class="brand-copy"><strong>Alt8Mail</strong><span>System Control</span></span></div><div class="login-card"><h2>Control plane unavailable</h2><p>The browser could not reach the System Control service.</p><div class="notice tone-critical" style="margin-top:16px">${ui.icon('alert-circle', 17)}<div><strong>Connection failed</strong><p>${esc(errorDetail(error))}</p></div></div><button class="btn btn-primary" style="margin-top:16px" type="button" id="boot-retry">Try again</button></div></div></div></div>`;
    document.getElementById('boot-retry')?.addEventListener('click', () => boot());
  }
}

boot();
