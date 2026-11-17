import fs from 'node:fs';
import { launchBrowser } from './cdp.mjs';

const BASE = 'https://130.94.46.22:8808';
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const OUT = 'artifacts/control-ui';
fs.mkdirSync(OUT, { recursive: true });
const checks = [];
function check(name, ok, detail = '') {
  checks.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}
const browser = await launchBrowser({ headless: true, width: 1440, height: 1100, ignoreCertificateErrors: true });
let page;
const click = async (selector) => { await page.click(selector); await new Promise((r) => setTimeout(r, 300)); };
const closeModal = async () => { await page.pressKey('Escape'); await new Promise((r) => setTimeout(r, 250)); };
const openModal = async (selector, waitSelector) => {
  await click(selector);
  await page.waitFor(() => document.querySelector('.modal-overlay'), 8000);
  if (waitSelector) await page.waitFor(`document.querySelector(${JSON.stringify(waitSelector)})`, 8000);
};
try {
  page = await browser.newPage('about:blank');
  await page.goto(`${BASE}/`);
  await page.waitFor(() => document.querySelector('#login-form'), 15000);
  await page.setValue('#login-email', 'sysadmin@alt8mail.local');
  await page.setValue('#login-password', PASSWORD);
  await page.click('#login-submit');
  await page.waitFor(() => document.querySelector('.shell'), 25000);
  await page.waitFor(() => !document.querySelector('#view .loading-state'), 20000);
  await new Promise((r) => setTimeout(r, 1200));

  const dashboard = await page.eval(() => ({
    banner: document.querySelector('#config-sync-banner')?.innerText || '',
    chip: document.querySelector('#sync-chip')?.innerText || '',
    services: document.querySelectorAll('#dashboard-services .service-card').length,
    digest: document.querySelector('#dashboard-services .service-digest')?.innerText || '',
    chart: Boolean(document.querySelector('.line-chart, .chart-empty')),
    theme: document.documentElement.dataset.theme,
  }));
  check('dashboard_after_sync', !dashboard.banner && dashboard.chip.toLowerCase().includes('in sync'), JSON.stringify(dashboard));
  await page.screenshot(`${OUT}/20-dashboard-final-dark.png`, { fullPage: true });

  // Light theme, chart and service compaction.
  await click('#theme-toggle');
  const light = await page.eval(() => ({ theme: document.documentElement.dataset.theme, chart: Boolean(document.querySelector('.line-chart, .chart-empty')), services: document.querySelectorAll('#dashboard-services .service-card').length }));
  check('light_theme', light.theme === 'light', JSON.stringify(light));
  await page.screenshot(`${OUT}/21-dashboard-final-light.png`, { fullPage: true });

  // Command palette keyboard navigation.
  await page.pressKey('k', { ctrl: true });
  await page.waitFor(() => document.querySelector('#palette-input'), 8000);
  await page.click('#palette-input');
  await page.setValue('#palette-input', 'mailboxes');
  await new Promise((r) => setTimeout(r, 250));
  const palette = await page.eval(() => ({ items: Array.from(document.querySelectorAll('#palette-list .palette-item')).map((el) => el.innerText), input: document.querySelector('#palette-input')?.value || '' }));
  await page.pressKey('Enter');
  await new Promise((r) => setTimeout(r, 900));
  const afterPalette = await page.eval(() => location.hash);
  check('command_palette', palette.items.length >= 1 && afterPalette.includes('mailboxes'), `${palette.items.length} results → ${afterPalette}`);

  // Modal inventory. Each opens, is inspected, and is dismissed without writes.
  await page.eval("location.hash = '#/domains'");
  await page.waitFor(() => document.querySelector('#domain-create'), 15000);
  await openModal('#domain-create', '#domain-create-form');
  const domainModal = await page.eval(() => ({ title: document.querySelector('.modal-overlay h2')?.textContent, fields: document.querySelectorAll('.modal-overlay input, .modal-overlay select').length }));
  check('domain_create_modal', domainModal.fields >= 3, JSON.stringify(domainModal));
  await page.screenshot(`${OUT}/22-domain-create-modal.png`);
  await closeModal();

  await page.eval("location.hash = '#/aliases'");
  await page.waitFor(() => document.querySelector('#alias-create'), 15000);
  await openModal('#alias-create', '[id^="alias-form-"]');
  await page.setValue('[id^="alias-form-"] [name="kind"]', 'catch_all');
  await new Promise((r) => setTimeout(r, 250));
  const aliasCatchAll = await page.eval(() => Boolean(document.querySelector('[id^="alias-form-"] [name="domain"]')));
  check('alias_catch_all_form', aliasCatchAll, 'domain selector rendered');
  await page.screenshot(`${OUT}/23-alias-modal.png`);
  await closeModal();

  await page.eval("location.hash = '#/imports'");
  await page.waitFor(() => document.querySelector('#import-create'), 15000);
  await openModal('#import-create', '[id^="import-form-"]');
  const imapFields = await page.eval(() => ({ host: Boolean(document.querySelector('[id^="import-form-"] [name="host"]')), target: Boolean(document.querySelector('[id^="import-form-"] [name="targetMailbox"]')) }));
  await page.eval("document.querySelector('[id^=\"import-form-\"] [data-kind=\"mbox\"]')?.click()");
  await new Promise((r) => setTimeout(r, 250));
  const mboxField = await page.eval(() => Boolean(document.querySelector('[id^="import-form-"] [name="path"]')));
  check('import_source_tabs', imapFields.host && imapFields.target && mboxField, JSON.stringify(imapFields));
  await closeModal();

  await page.eval("location.hash = '#/delivery'");
  await page.waitFor(() => document.querySelector('#smtp-test-new'), 20000);
  await openModal('#smtp-test-new', '#smtp-test-form');
  check('smtp_test_modal', await page.eval(() => Boolean(document.querySelector('#smtp-test-form [name="recipient"]'))), 'recipient form present');
  await closeModal();

  await page.eval("location.hash = '#/relay'");
  await page.waitFor(() => document.querySelector('#ip-create'), 20000);
  await openModal('#ip-create', '#ip-form');
  check('outbound_ip_modal', await page.eval(() => Boolean(document.querySelector('#ip-form [name="address"]'))), 'address form present');
  await closeModal();
  await openModal('#route-create', '#route-form');
  check('sender_route_modal', await page.eval(() => Boolean(document.querySelector('#route-form [name="senderDomain"]'))), 'route form present');
  await closeModal();

  await page.eval("location.hash = '#/suppressions'");
  await page.waitFor(() => document.querySelector('#suppression-create'), 15000);
  await openModal('#suppression-create', '#suppression-form');
  check('suppression_modal', await page.eval(() => Boolean(document.querySelector('#suppression-form [name="direction"]'))), 'direction form present');
  await closeModal();

  await page.eval("location.hash = '#/tls'");
  await page.waitFor(() => document.querySelector('#tls-issue'), 20000);
  await openModal('#tls-issue', '#tls-issue-form');
  check('tls_issue_modal', await page.eval(() => Boolean(document.querySelector('#tls-issue-form [name="domains"]'))), 'certbot form present');
  await closeModal();
  await openModal('#tls-self-signed', '#tls-self-form');
  check('tls_self_signed_modal', await page.eval(() => Boolean(document.querySelector('#tls-self-form [name="commonName"]'))), 'self-signed form present');
  await closeModal();

  await page.eval("location.hash = '#/access?tab=users'");
  await page.waitFor(() => document.querySelector('#user-create'), 15000);
  await openModal('#user-create', '#access-user-form');
  const userModal = await page.eval(() => ({ title: document.querySelector('.modal-overlay h2')?.textContent, role: Boolean(document.querySelector('#access-user-form [name="role"]')), groups: document.querySelectorAll('#access-user-form [name="groupIds"]').length }));
  check('panel_user_modal', userModal.role, JSON.stringify(userModal));
  await closeModal();
  await page.eval("location.hash = '#/access?tab=groups'");
  await page.waitFor(() => document.querySelector('#group-create'), 15000);
  await openModal('#group-create', '#access-group-form');
  const permissionCount = await page.eval(() => document.querySelectorAll('#access-group-form [name="permissions"]').length);
  check('permission_group_modal', permissionCount >= 20, `${permissionCount} permission checkboxes`);
  await page.screenshot(`${OUT}/24-group-modal.png`);
  await closeModal();

  await page.eval("location.hash = '#/api'");
  await page.waitFor(() => document.querySelector('#apikey-create'), 15000);
  await openModal('#apikey-create', '#apikey-form');
  check('api_key_modal', await page.eval(() => document.querySelectorAll('#apikey-form [name="scopes"]').length === 3), 'three scopes');
  await closeModal();

  await page.eval("location.hash = '#/webhooks'");
  await page.waitFor(() => document.querySelector('#webhook-create'), 15000);
  await openModal('#webhook-create', '#webhook-form');
  const webhookEvents = await page.eval(() => document.querySelectorAll('#webhook-form [name="webhookEvents"]').length);
  check('webhook_modal', webhookEvents >= 10, `${webhookEvents} events`);
  await closeModal();

  await page.eval("location.hash = '#/notifications'");
  await page.waitFor(() => document.querySelector('#notification-create'), 15000);
  await openModal('#notification-create', '#handle-form');
  await page.setValue('#handle-kind', 'telegram');
  await new Promise((r) => setTimeout(r, 200));
  const telegram = await page.eval(() => Boolean(document.querySelector('#handle-form [name="botToken"]')));
  check('notification_kind_form', telegram, 'telegram fields rendered');
  await closeModal();

  await page.eval("location.hash = '#/services'");
  await page.waitFor(() => document.querySelector('#services-list table'), 20000);
  check('services_table', await page.eval(() => document.querySelectorAll('#services-list tbody tr').length >= 10), 'unit rows present');

  await page.eval("location.hash = '#/ports'");
  await page.waitFor(() => document.querySelector('#ports-preview'), 20000);
  await page.click('#ports-preview');
  await page.waitFor(() => document.querySelector('.modal-overlay'), 15000);
  const portsPreview = await page.eval(() => ({ title: document.querySelector('.modal-overlay h2')?.textContent, selects: document.querySelectorAll('.modal-overlay .diff-block').length }));
  check('ports_preview_modal', Boolean(portsPreview.title), JSON.stringify(portsPreview));
  await closeModal();

  await page.eval("location.hash = '#/postfix'");
  await page.waitFor(() => document.querySelector('#postfix-editor'), 20000);
  const mainLines = await page.eval(() => document.querySelector('#postfix-editor')?.value?.split('\n').length || 0);
  await page.eval("document.querySelector('#postfix-tabs [data-file=\"master.cf\"]')?.click()");
  await page.waitFor(() => document.querySelector('#postfix-editor')?.value?.includes('smtp'), 10000).catch(() => {});
  const masterLines = await page.eval(() => document.querySelector('#postfix-editor')?.value?.split('\n').length || 0);
  check('postfix_editor_tabs', mainLines > 10 && masterLines > 5, `${mainLines} main.cf lines, ${masterLines} master.cf lines`);

  await page.eval("location.hash = '#/logs'");
  await page.waitFor(() => document.querySelector('#logs-settings'), 20000);
  await openModal('#logs-settings', '#logs-settings-form');
  check('log_ingestion_modal', await page.eval(() => Boolean(document.querySelector('#logs-settings-form [name="source"]'))), 'source and retention form');
  await closeModal();

  await page.eval("location.hash = '#/backups'");
  await page.waitFor(() => document.querySelector('#backup-create'), 20000);
  await openModal('#backup-create', '#backup-form');
  check('backup_modal', await page.eval(() => document.querySelectorAll('#backup-form [name="backupContents"]').length >= 5), 'content options present');
  await closeModal();

  await page.eval("location.hash = '#/cron'");
  await page.waitFor(() => document.querySelector('#cron-create'), 20000);
  await openModal('#cron-create', '#cron-form');
  check('cron_modal', await page.eval(() => Boolean(document.querySelector('#cron-form [name="schedule"]'))), 'schedule and command form');
  await closeModal();

  await page.eval("location.hash = '#/server'");
  await page.waitFor(() => document.querySelector('#address-add'), 20000);
  // Wait for the async network card to settle so the button has a stable hit
  // target before the mouse click.
  await page.waitFor(() => {
    const body = document.querySelector('#network-body');
    return body && !body.querySelector('.loading-state');
  }, 20000);
  await openModal('#address-add', '#address-form');
  check('network_address_modal', await page.eval(() => Boolean(document.querySelector('#address-form [name="interface"]'))), 'interface form');
  await closeModal();

  await page.eval("location.hash = '#/database'");
  await page.waitFor(() => document.querySelector('#db-provision'), 20000);
  await openModal('#db-provision', '#db-provision-form');
  check('database_provision_modal', await page.eval(() => Boolean(document.querySelector('#db-provision-form [name="role"]'))), 'role form');
  await closeModal();

  await page.eval("location.hash = '#/account'");
  await page.waitFor(() => document.querySelector('#totp-begin'), 20000);
  await page.click('#totp-begin');
  await page.waitFor(() => document.querySelector('.modal-overlay'), 10000);
  await page.waitFor(() => document.querySelector('#totp-qr canvas, #totp-qr img, #totp-qr table, #totp-qr .form-note'), 10000);
  const qr = await page.eval(() => ({ canvas: Boolean(document.querySelector('#totp-qr canvas')), table: Boolean(document.querySelector('#totp-qr table')), fallback: Boolean(document.querySelector('#totp-qr .form-note')), code: Boolean(document.querySelector('#totp-finish-code')) }));
  check('totp_qr_modal', qr.canvas || qr.table || qr.fallback, JSON.stringify(qr));
  await page.screenshot(`${OUT}/25-totp-qr.png`);
  await closeModal();

  // Mobile layout.
  await page.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true, screenWidth: 390, screenHeight: 844 });
  await page.eval("location.hash = '#/dashboard'");
  await new Promise((r) => setTimeout(r, 1000));
  const mobile = await page.eval(() => ({
    width: window.innerWidth,
    sidebarHidden: getComputedStyle(document.querySelector('#sidebar')).transform !== 'none',
    menuVisible: getComputedStyle(document.querySelector('#mobile-menu')).display !== 'none',
    scrollWidth: document.documentElement.scrollWidth,
  }));
  check('mobile_layout', mobile.width === 390 && mobile.menuVisible, JSON.stringify(mobile));
  await page.screenshot(`${OUT}/26-mobile-dashboard.png`, { fullPage: false });
  await page.click('#mobile-menu');
  await new Promise((r) => setTimeout(r, 350));
  const navOpen = await page.eval(() => document.body.classList.contains('is-mobile-nav-open'));
  check('mobile_navigation', navOpen, 'off-canvas navigation opened');
  await page.screenshot(`${OUT}/27-mobile-navigation.png`, { fullPage: false });
  await page.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1100, deviceScaleFactor: 1, mobile: false });

  const failures = checks.filter((entry) => !entry.ok);
  fs.writeFileSync(`${OUT}/ui-interactions.json`, JSON.stringify({ checks, failures, errors: page.errors }, null, 2));
  console.log(JSON.stringify({ checks: checks.length, failures: failures.length, pageErrors: page.errors }, null, 2));
  if (failures.length) process.exitCode = 1;
} finally {
  if (page) console.log('pageErrors', JSON.stringify(page.errors));
  await browser.close();
}
