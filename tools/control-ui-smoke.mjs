import fs from 'node:fs';
import path from 'node:path';
import { launchBrowser } from './cdp.mjs';

const BASE = process.env.CONTROL_BASE || 'https://130.94.46.22:8808';
const EMAIL = process.env.CONTROL_EMAIL || 'sysadmin@alt8mail.local';
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const OUT = process.env.CONTROL_OUT || path.resolve('artifacts/control-ui');
fs.mkdirSync(OUT, { recursive: true });

const routes = [
  ['dashboard', 'System overview'],
  ['diagnostics', 'Diagnostics'],
  ['domains', 'Domains & DNS'],
  ['mailboxes', 'Mailboxes'],
  ['aliases', 'Aliases & forwarding'],
  ['imports', 'Mail import'],
  ['delivery', 'Deliverability'],
  ['queue', 'Mail queue'],
  ['relay', 'Relay & outbound'],
  ['suppressions', 'Suppressions'],
  ['tls', 'TLS certificates'],
  ['access?tab=users', 'Access & security'],
  ['access?tab=groups', 'Access & security'],
  ['access?tab=sessions', 'Access & security'],
  ['access?tab=audit', 'Access & security'],
  ['access?tab=roles', 'Access & security'],
  ['api', 'API keys'],
  ['webhooks', 'Webhooks'],
  ['notifications', 'Notifications'],
  ['services', 'Services'],
  ['updates', 'Software updates'],
  ['terminal', 'Terminal'],
  ['ports', 'Mail listeners'],
  ['postfix', 'Postfix configuration'],
  ['logs', 'Mail logs'],
  ['backups', 'Backups'],
  ['cron', 'Scheduled tasks'],
  ['server', 'Server & network'],
  ['firewall', 'Firewall'],
  ['database', 'Database'],
  ['sync', 'Config sync'],
  ['account', 'My security'],
];

const results = [];
const pageErrors = [];
let browser;
try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1100, ignoreCertificateErrors: true });
  const page = await browser.newPage('about:blank');
  page.on('Runtime.exceptionThrown', (params) => pageErrors.push(params.exceptionDetails?.exception?.description || params.exceptionDetails?.text || 'exception'));
  await page.goto(`${BASE}/`);
  await page.waitFor(() => document.querySelector('#login-form'), 15000);
  await page.setValue('#login-email', EMAIL);
  await page.setValue('#login-password', PASSWORD);
  await page.screenshot(path.join(OUT, '00-login.png'), { fullPage: true });
  await page.click('#login-submit');
  await page.waitFor(() => document.querySelector('.shell') || document.querySelector('.login-error.is-visible'), 25000);
  const loginFailed = await page.eval(() => Boolean(document.querySelector('.login-error.is-visible')));
  if (loginFailed) {
    const text = await page.eval(() => document.querySelector('.login-error')?.innerText || 'login failed');
    throw new Error(text);
  }
  await page.waitFor(() => document.querySelector('#view .stat-grid, #view .page-head'), 15000);
  await new Promise((r) => setTimeout(r, 900));
  await page.screenshot(path.join(OUT, '01-dashboard.png'), { fullPage: true });

  for (const [route, expected] of routes) {
    const started = Date.now();
    page.clearErrors();
    await page.eval(`location.hash = ${JSON.stringify(`#/${route}`)}`);
    await page.waitFor(() => {
      const view = document.querySelector('#view');
      return view && !view.querySelector('.loading-state') && view.innerText.trim().length > 10;
    }, 30000).catch(() => {});
    await new Promise((r) => setTimeout(r, route.startsWith('tls') || route.startsWith('server') || route.startsWith('database') ? 1400 : 650));
    const state = await page.eval(() => {
      const view = document.querySelector('#view');
      const title = document.querySelector('#topbar-title')?.textContent || '';
      const error = view?.querySelector('.error-state')?.innerText || '';
      const critical = Array.from(view?.querySelectorAll('.inline-notice.tone-critical') || []).map((el) => el.innerText).join(' | ');
      const text = view?.innerText || '';
      return {
        title, error, critical, textLen: text.length, sample: text.slice(0, 220),
        cards: view?.querySelectorAll('.card').length || 0,
        tables: view?.querySelectorAll('table').length || 0,
        modals: document.querySelectorAll('.modal-overlay').length,
      };
    });
    const errors = page.errors.slice();
    results.push({ route, expected, ms: Date.now() - started, ...state, errors });
    const name = route.replace(/[^a-z0-9]+/gi, '-');
    await page.screenshot(path.join(OUT, `route-${name}.png`), { fullPage: false });
    console.log(JSON.stringify({ route, title: state.title, ms: Date.now() - started, error: state.error, critical: state.critical, cards: state.cards, tables: state.tables, errors }));
  }

  await page.eval(`location.hash = '#/dashboard'`);
  await new Promise((r) => setTimeout(r, 1200));
  await page.click('#dashboard-diagnostics');
  await page.waitFor(() => document.querySelector('.modal-overlay .diagnostic-item, .modal-overlay .inline-notice'), 25000);
  await page.screenshot(path.join(OUT, '02-diagnostics-modal.png'), { fullPage: false });
  const diagnosticsState = await page.eval(() => ({
    items: document.querySelectorAll('.modal-overlay .diagnostic-item').length,
    title: document.querySelector('.modal-overlay h2')?.textContent || '',
    text: document.querySelector('.modal-overlay .modal-body')?.innerText?.slice(0, 500) || '',
  }));
  console.log(JSON.stringify({ diagnostics: diagnosticsState }));
  await page.pressKey('Escape');

  await page.eval(`location.hash = '#/domains'`);
  await new Promise((r) => setTimeout(r, 1200));
  const domainLink = await page.eval(() => document.querySelector('tr[data-href]')?.getAttribute('data-href') || document.querySelector('a[href^="#/domains/"]')?.getAttribute('href') || '');
  if (domainLink) {
    await page.eval(`location.hash = ${JSON.stringify(domainLink)}`);
    await page.waitFor(() => document.querySelector('[data-domain-panel="overview"]'), 20000);
    await new Promise((r) => setTimeout(r, 700));
    await page.screenshot(path.join(OUT, '03-domain-detail.png'), { fullPage: true });
    await page.eval(`document.querySelector('[data-domain-tab="dns"]')?.click()`);
    await new Promise((r) => setTimeout(r, 400));
    const dnsButton = await page.eval(() => Boolean(document.querySelector('#dns-check, #dns-recheck')));
    if (dnsButton) {
      await page.click('#dns-check, #dns-recheck').catch(() => {});
      await page.waitFor(() => document.querySelector('#dns-body table, #dns-body .inline-notice'), 30000);
      await new Promise((r) => setTimeout(r, 500));
      const dns = await page.eval(() => ({
        rows: document.querySelectorAll('#dns-body tbody tr').length,
        notice: document.querySelector('#dns-body .inline-notice')?.innerText || '',
        statuses: Array.from(document.querySelectorAll('#dns-body .status-pill')).map((el) => el.textContent.trim()).slice(0, 12),
      }));
      console.log(JSON.stringify({ dns }));
      await page.screenshot(path.join(OUT, '04-domain-dns.png'), { fullPage: true });
    }
  }

  await page.eval(`location.hash = '#/mailboxes'`);
  await new Promise((r) => setTimeout(r, 1200));
  const mailboxLink = await page.eval(() => document.querySelector('tr[data-href^="#/mailboxes/"]')?.getAttribute('data-href') || '');
  if (mailboxLink) {
    await page.eval(`location.hash = ${JSON.stringify(mailboxLink)}`);
    await page.waitFor(() => document.querySelector('[data-mailbox-panel="overview"]'), 20000);
    await new Promise((r) => setTimeout(r, 700));
    await page.screenshot(path.join(OUT, '05-mailbox-detail.png'), { fullPage: true });
    await page.eval(`document.querySelector('[data-mailbox-tab="history"]')?.click()`);
    await page.waitFor(() => !document.querySelector('#history-body .loading-state'), 20000);
    await page.eval(`document.querySelector('[data-mailbox-tab="sieve"]')?.click()`);
    await page.waitFor(() => !document.querySelector('#sieve-body .loading-state'), 20000);
    await new Promise((r) => setTimeout(r, 500));
    const mailboxState = await page.eval(() => ({
      historyRows: document.querySelectorAll('#history-body tbody tr').length,
      sieveRules: document.querySelectorAll('#sieve-editor article.card').length,
      sieveText: document.querySelector('#sieve-body')?.innerText?.slice(0, 300) || '',
      critical: Array.from(document.querySelectorAll('#view .inline-notice.tone-critical')).map((el) => el.innerText),
    }));
    console.log(JSON.stringify({ mailboxState }));
    await page.screenshot(path.join(OUT, '06-mailbox-sieve.png'), { fullPage: true });
  }

  await page.eval(`location.hash = '#/account'`);
  await page.waitFor(() => document.querySelector('#password-form'), 20000);
  await new Promise((r) => setTimeout(r, 500));
  await page.screenshot(path.join(OUT, '07-account.png'), { fullPage: true });

  const failures = results.filter((r) => r.error || r.critical || r.textLen < 20 || r.modals > 0);
  const summary = {
    base: BASE,
    routes: results.length,
    failures,
    pageErrors,
    screenshots: fs.readdirSync(OUT).sort(),
  };
  fs.writeFileSync(path.join(OUT, 'summary.json'), JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ summary: { routes: results.length, failures: failures.length, pageErrors: pageErrors.length }, failures, pageErrors }, null, 2));
} finally {
  if (browser) await browser.close();
}
