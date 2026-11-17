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
  await page.waitFor(() => !document.querySelector('#view .loading-state'), 20000);
  await new Promise((r) => setTimeout(r, 1000));

  const before = await page.eval(() => ({
    chip: document.querySelector('#sync-chip')?.innerText || '',
    banner: document.querySelector('#config-sync-banner')?.innerText || '',
  }));
  console.log('before', JSON.stringify(before));
  const runButton = await page.eval(() => Boolean(document.querySelector('#dashboard-sync')));
  if (!runButton) throw new Error('Run sync control missing');
  await page.click('#dashboard-sync');
  const started = Date.now();
  let done = false;
  while (Date.now() - started < 180000) {
    const state = await page.eval(() => ({
      chip: document.querySelector('#sync-chip')?.innerText || '',
      banner: document.querySelector('#config-sync-banner')?.innerText || '',
      toast: Array.from(document.querySelectorAll('.toast')).map((el) => el.innerText).join(' | '),
    }));
    if (!state.chip.includes('Never synced') && !state.chip.includes('drift') || state.toast.includes('Configuration sync completed')) {
      done = true;
      console.log('after', JSON.stringify(state));
      break;
    }
    if (state.toast.includes('failed')) { console.log('failure', JSON.stringify(state)); break; }
    await new Promise((r) => setTimeout(r, 1500));
  }
  if (!done) console.log('sync still running after 180s');
  await new Promise((r) => setTimeout(r, 2000));
  await page.screenshot(`${OUT}/10-dashboard-after-sync.png`, { fullPage: true });
  await page.eval("location.hash = '#/sync'");
  await page.waitFor(() => document.querySelector('[data-sync-run], #sync-run, .page-head'), 20000).catch(() => {});
  await new Promise((r) => setTimeout(r, 2500));
  const syncPage = await page.eval(() => ({
    text: document.querySelector('#view')?.innerText?.slice(0, 2500) || '',
    error: document.querySelector('#view .error-state')?.innerText || '',
    cards: document.querySelectorAll('#view .card').length,
  }));
  console.log('syncPage', JSON.stringify(syncPage, null, 2));
  await page.screenshot(`${OUT}/11-sync-page.png`, { fullPage: true });
} finally {
  if (page) console.log('pageErrors', JSON.stringify(page.errors));
  await browser.close();
}
