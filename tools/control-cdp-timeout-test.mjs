import { launchBrowser } from './cdp.mjs';
const b=await launchBrowser({headless:true,width:800,height:600,ignoreCertificateErrors:true});
try { const p=await b.newPage('about:blank'); for (const t of [15000,20000,25000,30000]) { try { console.log(t, await p.eval('1+1',{timeout:t})); } catch(e){ console.log(t,'FAIL',e.message); } } } finally { await b.close(); }
