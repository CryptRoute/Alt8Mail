import fs from 'node:fs';
import { launchBrowser } from './cdp.mjs';

const BASE = 'https://130.94.46.22:8808';
const EMAIL = 'sysadmin@alt8mail.local';
const PASSWORD = fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, 'utf8').trim();
const b = await launchBrowser({ headless: true, width: 1440, height: 1100, ignoreCertificateErrors: true });
let p;
try {
  p = await b.newPage('about:blank');
  console.log('1 goto');
  await p.goto(`${BASE}/`);
  console.log('2 wait form');
  await p.waitFor(() => document.querySelector('#login-form'), 15000);
  console.log('3 set email');
  await p.setValue('#login-email', EMAIL);
  console.log('4 set password');
  await p.setValue('#login-password', PASSWORD);
  console.log('5 screenshot');
  await p.screenshot('/tmp/alt8mail-login-debug.png', { fullPage: true });
  console.log('6 click');
  await p.click('#login-submit');
  console.log('7 wait shell');
  await p.waitFor(() => document.querySelector('.shell') || document.querySelector('.login-error.is-visible'), 25000);
  console.log('8 status');
  console.log(await p.eval(() => ({ shell: Boolean(document.querySelector('.shell')), loginError: document.querySelector('.login-error')?.innerText || '', body: document.body.innerText.slice(0, 300) })));
} catch (error) {
  console.error('FAILED', error.message);
  console.error('errors', p?.errors);
  try { console.log('html', await p.eval('document.body.innerText.slice(0,1000)')); } catch {}
} finally {
  await b.close();
}
