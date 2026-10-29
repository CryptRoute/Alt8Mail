import fs from 'node:fs';
import { launchBrowser } from './cdp.mjs';

const BASE = 'https://130.94.46.22:8808';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const OUT = 'artifacts/control-ui';
const browser = await launchBrowser({ headless: true, width: 1440, height: 1100, ignoreCertificateErrors: true });
let page;
try {
  page = await browser.newPage('about:blank');
  await page.goto(`${BASE}/`);
  await page.waitFor(() => document.querySelector('#login-form'), 15000);
  await page.setValue('#login-email', 'sysadmin@alt8mail.local');
  await page.setValue('#login-password', PASSWORD);
  await page.click('#login-submit');
  await page.waitFor(() => document.querySelector('.shell'), 25000);
  await page.eval("location.hash = '#/delivery'");
  await page.waitFor(() => document.querySelector('#smtp-test-new'), 25000);
  await new Promise((r) => setTimeout(r, 700));
  await page.click('#smtp-test-new');
  await page.waitFor(() => document.querySelector('#smtp-test-form'), 8000);
  await page.setValue('#smtp-test-form [name="recipient"]', 'postmaster@a8m.test');
  await page.setValue('#smtp-test-form [name="subject"]', `System Control delivery path test ${new Date().toISOString()}`);
  await page.setValue('#smtp-test-form [name="body"]', 'This is an automated delivery-path test from Alt8Mail System Control. Policy service and local delivery are being verified.');
  await page.screenshot(`${OUT}/12-smtp-test-form.png`);
  await page.eval("document.querySelector('#smtp-test-form').requestSubmit()");
  await page.waitFor(() => document.querySelector('.modal-overlay .notice.tone-good, .modal-overlay .notice.tone-critical, .modal-overlay .inline-notice'), 45000);
  await new Promise((r) => setTimeout(r, 800));
  const result = await page.eval(() => ({
    text: document.querySelector('.modal-overlay .modal-body')?.innerText?.slice(0, 2500) || '',
    status: document.querySelector('.modal-overlay h2')?.textContent || '',
    transcript: document.querySelector('.modal-overlay pre')?.innerText || '',
    errors: Array.from(document.querySelectorAll('.toast')).map((el) => el.innerText),
  }));
  console.log('smtpResult', JSON.stringify(result, null, 2));
  await page.screenshot(`${OUT}/13-smtp-test-result.png`, { fullPage: false });
  await page.pressKey('Escape');
  await new Promise((r) => setTimeout(r, 1000));
  await page.eval("location.hash = '#/logs?q=postmaster%40a8m.test'");
  await page.waitFor(() => !document.querySelector('#logs-table .loading-state'), 25000);
  await new Promise((r) => setTimeout(r, 1500));
  const logs = await page.eval(() => ({
    summary: document.querySelector('#logs-summary')?.innerText || '',
    rows: Array.from(document.querySelectorAll('#logs-table tbody tr')).slice(0, 8).map((tr) => tr.innerText.replace(/\n/g, ' | ').slice(0, 300)),
    critical: Array.from(document.querySelectorAll('#view .inline-notice.tone-critical')).map((el) => el.innerText),
  }));
  console.log('logs', JSON.stringify(logs, null, 2));
  await page.screenshot(`${OUT}/14-logs-after-test.png`, { fullPage: true });
} finally {
  if (page) console.log('pageErrors', JSON.stringify(page.errors));
  await browser.close();
}
