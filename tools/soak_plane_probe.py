#!/usr/bin/env python3
"""Low-concurrency admin/control/webmail probes during the 1,000-user soak."""
from __future__ import annotations
import json, os, pathlib, time, urllib3, requests
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
WEB='https://130-94-46-22.sslip.io'; CONTROL='https://130.94.46.22:8808'
OUT=pathlib.Path(os.environ.get('SOAK_OUT','artifacts/soak')); OUT.mkdir(parents=True, exist_ok=True)
STOP=pathlib.Path(os.environ.get('SOAK_PROBE_STOP','artifacts/soak/plane-probe.stop'))
WEB_PASSWORD=os.environ.get('SOAK_PROBE_WEB_PASS',''); ADMIN_PASSWORD=os.environ.get('SOAK_PROBE_ADMIN_PASS', WEB_PASSWORD)
CONTROL_PASSWORD=os.environ.get('SOAK_PROBE_CONTROL_PASS','')
def req(s, method, url, **kw):
    start=time.time()
    try:
        r=s.request(method,url,timeout=20,**kw)
        return {'url':url,'status':r.status_code,'ms':round((time.time()-start)*1000,1)}
    except Exception as exc:
        return {'url':url,'status':None,'ms':round((time.time()-start)*1000,1),'error':type(exc).__name__}
def session():
    s=requests.Session(); s.trust_env=False; s.verify=False; s.headers.update({'User-Agent':'Alt8Mail-soak-plane/1.0'}); return s
web=session(); admin=session(); control=session()
r=web.get(WEB+'/api/health',timeout=20); web_ok=r.status_code==200
admin_login=admin.post(WEB+'/admin/api/session',json={'email':os.environ.get('SOAK_PROBE_WEB_EMAIL','avery.chen@meridian.test'),'password':ADMIN_PASSWORD},timeout=30)
admin_ok=admin_login.status_code==200
admin_csrf=admin_login.json().get('csrf','') if admin_ok else ''
c=control.get(CONTROL+'/control/api/session',timeout=20); ccsrf=c.json().get('csrfToken','') if c.ok else ''
control_login=control.post(CONTROL+'/control/api/login',headers={'X-Alt8Mail-CSRF':ccsrf},json={'email':os.environ.get('SOAK_PROBE_CONTROL_EMAIL','sysadmin@alt8mail.local'),'password':CONTROL_PASSWORD},timeout=30)
control_ok=control_login.status_code==200
print(json.dumps({'web_health':web_ok,'admin_login':admin_login.status_code,'control_login':control_login.status_code}),flush=True)
samples=[]; start=time.time()
while not STOP.exists():
    row={'at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())}
    row['web_health']=req(web,'GET',WEB+'/api/health')
    if admin_ok:
        row['admin_bootstrap']=req(admin,'GET',WEB+'/admin/api/bootstrap',headers={'X-CSRF-Token':admin_csrf})
    if control_ok:
        row['control_dashboard']=req(control,'GET',CONTROL+'/control/api/dashboard')
        row['control_health']=req(control,'GET',CONTROL+'/healthz')
    samples.append(row)
    time.sleep(10)
def summ(key):
    vals=[r[key] for r in samples if isinstance(r.get(key),dict)]
    statuses={}
    for v in vals: statuses[str(v.get('status'))]=statuses.get(str(v.get('status')),0)+1
    times=sorted(v['ms'] for v in vals)
    def p(q):
        return times[min(len(times)-1,int((len(times)-1)*q))] if times else 0
    return {'count':len(vals),'statuses':statuses,'p50ms':p(.5),'p95ms':p(.95),'maxMs':max(times) if times else 0}
summary={'elapsedSeconds':round(time.time()-start,1),'webHealth':summ('web_health'),'adminBootstrap':summ('admin_bootstrap'),'controlDashboard':summ('control_dashboard'),'controlHealth':summ('control_health'),'raw':samples}
(OUT/'plane-probe.json').write_text(json.dumps(summary,indent=2))
print(json.dumps({k:v for k,v in summary.items() if k!='raw'},indent=2))
