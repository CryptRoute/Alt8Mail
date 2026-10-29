import fs from 'node:fs';
import { launchBrowser } from './cdp.mjs';

const BASE = 'https://130.94.46.22:8808';
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const suffix = Date.now().toString(36);
const aliasAddress = `uitest${suffix}@a8m.test`;
const cronName = `UI test ${suffix}`;
const keyName = `UI test key ${suffix}`;
const checks = [];
const check = (name, ok, detail = '') => {
  checks.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
};
const browser = await launchBrowser({ headless: true, width: 1440, height: 1100, ignoreCertificateErrors: true });
let page;
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const confirmButton = async (text) => {
  await page.waitFor(() => document.querySelector('.modal-overlay .modal-footer'), 8000);
  const clicked = await page.eval(`(() => {
    const buttons = Array.from(document.querySelectorAll('.modal-overlay .modal-footer .btn'));
    const target = buttons.find((button) => button.textContent.trim().includes(${JSON.stringify(text)}));
    if (!target) return false;
    target.click();
    return true;
  })()`);
  await wait(700);
  return clicked;
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

  await page.eval("location.hash = '#/aliases'");
  await page.waitFor(() => document.querySelector('#alias-create'), 15000);
  await page.click('#alias-create');
  await page.waitFor(() => document.querySelector('[id^="alias-form-"]'), 10000);
  await page.setValue('[id^="alias-form-"] [name="address"]', aliasAddress);
  await page.setValue('[id^="alias-form-"] [name="targets"]', 'avery.chen@a8m.test');
  await page.setValue('[id^="alias-form-"] [name="description"]', 'UI write test');
  await page.eval("document.querySelector('[id^=\"alias-form-\"]').requestSubmit()");
  await page.waitFor(`Array.from(document.querySelectorAll('#aliases-body tbody tr')).some((row) => row.innerText.includes(${JSON.stringify(aliasAddress)}))`, 15000);
  check('ui_alias_create', true, aliasAddress);
  await page.eval(`(() => { const row = Array.from(document.querySelectorAll('#aliases-body tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(aliasAddress)})); row?.querySelector('[data-alias-delete]')?.click(); })()`);
  await page.waitFor(() => document.querySelector('#alias-confirm-delete'), 8000);
  await page.click('#alias-confirm-delete');
  await page.waitFor(`!Array.from(document.querySelectorAll('#aliases-body tbody tr')).some((row) => row.innerText.includes(${JSON.stringify(aliasAddress)}))`, 15000);
  check('ui_alias_delete', true, 'preview and delete');

  await page.eval("location.hash = '#/cron'");
  await page.waitFor(() => document.querySelector('#cron-create'), 15000);
  await page.click('#cron-create');
  await page.waitFor(() => document.querySelector('#cron-form'), 8000);
  await page.setValue('#cron-form [name="name"]', cronName);
  await page.setValue('#cron-form [name="command"]', 'printf ui-cron-ok');
  await page.eval("document.querySelector('#cron-form').requestSubmit()");
  await page.waitFor(`Array.from(document.querySelectorAll('#cron-list tbody tr')).some((row) => row.innerText.includes(${JSON.stringify(cronName)}))`, 15000);
  check('ui_cron_create', true, cronName);
  await page.eval(`(() => { const row = Array.from(document.querySelectorAll('#cron-list tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(cronName)})); row?.querySelector('[data-cron-run]')?.click(); })()`);
  await confirmButton('Run now');
  await wait(1500);
  await page.eval(`(() => { const row = Array.from(document.querySelectorAll('#cron-list tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(cronName)})); row?.querySelector('[data-cron-runs]')?.click(); })()`);
  await page.waitFor(() => document.querySelector('.modal-overlay .code-block') || document.querySelector('.modal-overlay .table-wrap'), 15000);
  const runText = await page.eval(() => document.querySelector('.modal-overlay .modal-body')?.innerText || '');
  check('ui_cron_run', /success/i.test(runText), runText.slice(0, 120).replace(/\n/g, ' | '));
  await page.pressKey('Escape');
  await wait(300);
  await page.eval(`(() => { const row = Array.from(document.querySelectorAll('#cron-list tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(cronName)})); row?.querySelector('[data-cron-delete]')?.click(); })()`);
  await confirmButton('Delete task');
  await page.waitFor(`!Array.from(document.querySelectorAll('#cron-list tbody tr')).some((row) => row.innerText.includes(${JSON.stringify(cronName)}))`, 15000);
  check('ui_cron_delete', true, 'deleted');

  await page.eval("location.hash = '#/api'");
  await page.waitFor(() => document.querySelector('#apikey-create'), 15000);
  await page.click('#apikey-create');
  await page.waitFor(() => document.querySelector('#apikey-form'), 8000);
  await page.setValue('#apikey-form [name="name"]', keyName);
  await page.eval("document.querySelector('#apikey-form').requestSubmit()");
  await page.waitFor(() => document.querySelector('#secret-value'), 10000);
  const keyVisible = await page.eval(() => (document.querySelector('#secret-value')?.value || '').startsWith('a8m_'));
  check('ui_api_key_create', keyVisible, 'secret shown once');
  await page.eval("document.querySelector('.modal-overlay .modal-footer .btn-primary')?.click()");
  await wait(800);
  await page.waitFor(`Array.from(document.querySelectorAll('#apikey-list tbody tr')).some((row) => row.innerText.includes(${JSON.stringify(keyName)}))`, 15000);
  await page.eval(`(() => { const row = Array.from(document.querySelectorAll('#apikey-list tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(keyName)})); row?.querySelector('[data-key-revoke]')?.click(); })()`);
  await confirmButton('Revoke key');
  await page.waitFor(`(() => { const row = Array.from(document.querySelectorAll('#apikey-list tbody tr')).find((tr) => tr.innerText.includes(${JSON.stringify(keyName)})); return !row || /revoked/i.test(row.innerText) || !row.querySelector('[data-key-revoke]'); })()`, 15000);
  check('ui_api_key_revoke', true, 'revoked');

  const failures = checks.filter((entry) => !entry.ok);
  fs.writeFileSync('artifacts/control-ui/ui-writes.json', JSON.stringify({ checks, failures, errors: page.errors }, null, 2));
  console.log(JSON.stringify({ checks: checks.length, failures: failures.length, pageErrors: page.errors }, null, 2));
  if (failures.length) process.exitCode = 1;
} finally {
  if (page) console.log('pageErrors', JSON.stringify(page.errors));
  await browser.close();
}
