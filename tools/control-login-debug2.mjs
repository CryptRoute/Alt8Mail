import fs from 'node:fs';
import { launchBrowser } from './cdp.mjs';
const BASE='https://130.94.46.22:8808';
const EMAIL='sysadmin@alt8mail.local';
const PASSWORD=fs.readFileSync(process.env.CONTROL_PASSWORD_FILE,'utf8').trim();
const b=await launchBrowser({headless:true,width:1440,height:1100,ignoreCertificateErrors:true});
let p;
try {
  p=await b.newPage('about:blank');
  await p.goto(BASE+'/');
  await p.waitFor(() => document.querySelector('#login-form'), 15000);
  await p.setValue('#login-email',EMAIL);
  await p.setValue('#login-password',PASSWORD);
  await p.click('#login-submit');
  await new Promise(r=>setTimeout(r,2500));
  console.log('shell?', await p.eval("document.querySelector('.shell') ? 'yes' : 'no'"));
  console.log('loginerror?', await p.eval("document.querySelector('.login-error.is-visible') ? 'yes' : 'no'"));
  console.log('expr1', await p.eval("Boolean((() => document.querySelector('.shell') || document.querySelector('.login-error.is-visible'))())"));
  console.log('view', await p.eval("document.querySelector('#view')?.innerText?.slice(0,300)"));
  console.log('errors', p.errors);
  console.log('console', p.console.slice(-20));
} catch(e) { console.error('FAIL',e.message, p?.errors); }
finally { await b.close(); }
