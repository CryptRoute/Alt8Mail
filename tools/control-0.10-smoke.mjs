import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.CONTROL_BASE || "https://130.94.46.22:8808";
const EMAIL = process.env.CONTROL_EMAIL || "sysadmin@alt8mail.local";
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, "utf8").trim();
const SSH_KEY = process.env.CONTROL_SSH_KEY || path.join(os.homedir(), ".ssh", "a8m_deploy_ed25519");
const OUT = path.resolve(__dirname, "..", "artifacts", "control-ui");
fs.mkdirSync(OUT, { recursive: true });
const checks = [];
const pageErrors = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const check = (name, condition, detail = "") => {
  checks.push({ name, ok: Boolean(condition), detail });
  if (!condition) throw new Error(`${name}${detail ? ` — ${detail}` : ""}`);
};

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: true });
const page = await browser.newPage(BASE);
page.on("Runtime.exceptionThrown", (params) => pageErrors.push(params.exceptionDetails?.exception?.description || params.exceptionDetails?.text || "exception"));

const api = (method, url, body) => page.eval(`(async () => {
  const headers = { "Content-Type": "application/json" };
  const token = document.cookie.match(/(?:^|; )alt8mail_control_csrf=([^;]+)/)?.[1];
  if (token) headers["X-Alt8Mail-CSRF"] = decodeURIComponent(token);
  const response = await fetch(${JSON.stringify(url)}, {
    method: ${JSON.stringify(method)}, credentials: "same-origin", headers,
    body: ${body === undefined ? "undefined" : JSON.stringify(JSON.stringify(body))}
  });
  const payload = await response.json().catch(() => null);
  return { status: response.status, body: payload };
})()`);

let importedName = "";
let dovecotTouched = false;
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => Boolean(document.querySelector('#login-form input[name=email]')), 15000);
  await sleep(150);
  await page.eval(`(() => {
    const email = document.querySelector('#login-form input[name=email], input[type=email]');
    const password = document.querySelector('#login-form input[name=password], input[type=password]');
    if (!email || !password) throw new Error('login form not found');
    for (const [el, value] of [[email, ${JSON.stringify(EMAIL)}], [password, ${JSON.stringify(PASSWORD)}]]) {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, value);
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }
    email.form.requestSubmit();
  })()`);
  await page.waitFor(() => Boolean(document.getElementById("sidebar")), 15000);
  check("control panel signs in", await page.eval("Boolean(document.getElementById('sidebar'))"));

  // ---- Mailboxes: page size, sorting, status filter and real pagination.
  const boxes = await api("GET", "/control/api/mailboxes?status=all&page=1&pageSize=100&sort=quota&dir=desc");
  check("mailboxes endpoint returns pagination metadata", boxes.status === 200 && boxes.body?.pageSize === 100 && Number(boxes.body?.total) >= 1 && Number(boxes.body?.pages) >= 1, JSON.stringify(boxes.body && { status: boxes.status, total: boxes.body.total, pages: boxes.body.pages, pageSize: boxes.body.pageSize }));
  check("mailbox quota sort is accepted", boxes.body?.sort === "quota" && boxes.body?.dir === "desc", JSON.stringify({ sort: boxes.body?.sort, dir: boxes.body?.dir }));
  const pageTwo = await api("GET", "/control/api/mailboxes?status=all&page=2&pageSize=1&sort=status&dir=asc");
  check("mailbox page two is sliceable", pageTwo.status === 200 && pageTwo.body?.page === 2 && pageTwo.body?.pageSize === 1, JSON.stringify(pageTwo.body && { page: pageTwo.body.page, pages: pageTwo.body.pages, count: pageTwo.body.count }));
  const badSort = await api("GET", "/control/api/mailboxes?sort=not_a_sort");
  check("invalid mailbox sort is rejected", badSort.status === 400, String(badSort.status));

  await page.eval("location.hash = '#/mailboxes'");
  await page.waitFor(() => document.getElementById("mailbox-page-size") !== null, 10000);
  const pageSizes = await page.eval("Array.from(document.querySelectorAll('#mailbox-page-size option')).map((option) => option.value).join(',')");
  check("mailbox UI offers 100/150/250/500 page sizes", pageSizes === "100,150,250,500", pageSizes);
  await page.eval("(() => { const el = document.getElementById('mailbox-sort'); el.value = 'quota'; el.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(600);
  check("mailbox UI sorting updates direction", await page.eval("document.getElementById('mailbox-dir-label').textContent === 'Descending'"));
  check("mailbox UI pagination footer is present", await page.eval("document.getElementById('mailbox-pagination').textContent.includes('Showing')"));
  await page.screenshot(path.join(OUT, "mailboxes-pagination.png"));

  // ---- TLS inventory detail route and imported certificate lifecycle.
  const certList = await api("GET", "/control/api/tls/certificates");
  check("TLS certificate inventory loads", certList.status === 200 && Array.isArray(certList.body?.certificates) && certList.body.certificates.length > 0);
  let detailOK = true;
  for (const cert of certList.body.certificates) {
    const detail = await api("GET", `/control/api/tls/certificates/${encodeURIComponent(cert.name)}`);
    if (detail.status !== 200 || detail.body?.name !== cert.name) { detailOK = false; break; }
  }
  check("every TLS certificate opens its detail record", detailOK);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "alt8mail-tls-smoke-"));
  const certPath = path.join(tmp, "cert.pem");
  const keyPath = path.join(tmp, "key.pem");
  execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
    "-subj", `/CN=smoke-${Date.now()}.alt8mail.local`,
    "-addext", "subjectAltName=DNS:smoke.alt8mail.local",
    "-keyout", keyPath, "-out", certPath], { stdio: "ignore" });
  const certPEM = fs.readFileSync(certPath, "utf8");
  const keyPEM = fs.readFileSync(keyPath, "utf8");
  const importName = `smoke-${Date.now()}`;
  const imported = await api("POST", "/control/api/tls/import", { name: importName, certificate: certPEM, privateKey: keyPEM, passphrase: "", overwrite: false });
  check("a purchased-style certificate can be imported", imported.status === 201 && Boolean(imported.body?.certificate?.name), JSON.stringify({ status: imported.status, error: imported.body?.error }));
  importedName = imported.body?.certificate?.name || "";
  const importedDetail = await api("GET", `/control/api/tls/certificates/${encodeURIComponent(importedName)}`);
  check("imported certificate detail resolves", importedDetail.status === 200 && importedDetail.body?.name === importedName, String(importedDetail.status));
  const duplicate = await api("POST", "/control/api/tls/import", { name: importName, certificate: certPEM, privateKey: keyPEM, passphrase: "", overwrite: false });
  check("duplicate import requires explicit overwrite", duplicate.status === 409, String(duplicate.status));
  fs.rmSync(tmp, { recursive: true, force: true });

  const mappings = await api("GET", "/control/api/tls/assignments");
  check("TLS service mapping endpoint loads", mappings.status === 200 && Array.isArray(mappings.body?.targets));
  const webTarget = mappings.body.targets.find((target) => target.key === "web");
  check("nginx TLS mapping is detected", webTarget?.available === true, JSON.stringify(webTarget));
  const webCert = certList.body.certificates.find((cert) => cert.certPath === webTarget?.certPath && cert.hasKey);
  check("current nginx certificate is available for a no-op assignment", Boolean(webCert));
  const emptyAssign = await api("POST", "/control/api/tls/assign", { certificate: webCert.name, targets: [] });
  check("empty TLS target list is rejected", emptyAssign.status === 400, String(emptyAssign.status));
  const assigned = await api("POST", "/control/api/tls/assign", { certificate: webCert.name, targets: ["web"] });
  check("a saved certificate can be mapped to nginx", assigned.status === 200 && (assigned.body?.targets || []).includes("web"), JSON.stringify({ status: assigned.status, body: assigned.body?.error || assigned.body?.targets }));
  const afterAssign = await api("GET", "/control/api/tls/assignments");
  const webAfter = afterAssign.body.targets.find((target) => target.key === "web");
  check("nginx still points at the assigned certificate path", webAfter?.certPath === webCert.certPath, JSON.stringify({ before: webCert.certPath, after: webAfter?.certPath }));
  const webProbe = await fetch("https://130-94-46-22.sslip.io/api/health").then((response) => response.status).catch(() => 0);
  check("webmail HTTPS stays healthy after the TLS reload", webProbe === 200, String(webProbe));
  const postfixTarget = mappings.body.targets.find((target) => target.key === "postfix");
  const postfixCert = certList.body.certificates.find((cert) => cert.certPath === postfixTarget?.certPath && cert.hasKey);
  check("current Postfix certificate is available for a no-op assignment", Boolean(postfixCert), JSON.stringify(postfixTarget));
  const assignedPostfix = await api("POST", "/control/api/tls/assign", { certificate: postfixCert.name, targets: ["postfix"] });
  check("a saved certificate can be mapped to Postfix", assignedPostfix.status === 200 && (assignedPostfix.body?.targets || []).includes("postfix"), JSON.stringify({ status: assignedPostfix.status, body: assignedPostfix.body?.error || assignedPostfix.body?.targets }));
  const afterPostfix = await api("GET", "/control/api/tls/assignments");
  const postfixAfter = afterPostfix.body.targets.find((target) => target.key === "postfix");
  check("Postfix still points at the assigned certificate path", postfixAfter?.certPath === postfixTarget?.certPath, JSON.stringify({ before: postfixTarget?.certPath, after: postfixAfter?.certPath }));
  const serviceStatus = await api("GET", "/control/api/services?refresh=1");
  check("Postfix is active after its TLS reload", (serviceStatus.body?.services || []).some((service) => service.unit === "postfix@-.service" && service.active), JSON.stringify((serviceStatus.body?.services || []).filter((service) => service.unit.includes("postfix"))));
  const dovecotTarget = mappings.body.targets.find((target) => target.key === "dovecot");
  const dovecotCert = certList.body.certificates.find((cert) => cert.certPath === dovecotTarget?.certPath && cert.hasKey);
  check("current Dovecot certificate is available for a no-op assignment", Boolean(dovecotCert), JSON.stringify(dovecotTarget));
  const assignedDovecot = await api("POST", "/control/api/tls/assign", { certificate: dovecotCert.name, targets: ["dovecot"] });
  check("a saved certificate can be mapped to Dovecot", assignedDovecot.status === 200 && (assignedDovecot.body?.targets || []).includes("dovecot"), JSON.stringify({ status: assignedDovecot.status, body: assignedDovecot.body?.error || assignedDovecot.body?.targets }));
  const afterDovecot = await api("GET", "/control/api/tls/assignments");
  const dovecotAfter = afterDovecot.body.targets.find((target) => target.key === "dovecot");
  check("Dovecot still points at the assigned certificate path", dovecotAfter?.certPath === dovecotTarget?.certPath, JSON.stringify({ before: dovecotTarget?.certPath, after: dovecotAfter?.certPath }));
  const serviceStatus2 = await api("GET", "/control/api/services?refresh=1");
  check("Dovecot is active after its TLS reload", (serviceStatus2.body?.services || []).some((service) => service.unit === "dovecot.service" && service.active), JSON.stringify((serviceStatus2.body?.services || []).filter((service) => service.unit.includes("dovecot"))));
  dovecotTouched = true;
  await page.eval("location.hash = '#/tls'");
  await page.waitFor(() => document.querySelector('#tls-list .table-wrap') !== null, 15000);
  await page.eval("document.getElementById('tls-import').click()");
  await page.waitFor(() => Boolean(document.getElementById('tls-import-form')), 5000);
  check("TLS import dialog is available in the UI", await page.eval("Boolean(document.getElementById('tls-import-form'))"));
  await page.eval("document.querySelector('#tls-import-form [data-modal-close]')?.click()");
  await page.eval("document.getElementById('tls-assign').click()");
  await page.waitFor(() => Boolean(document.getElementById('tls-assign-form')), 8000);
  check("TLS assignment dialog lists service targets", await page.eval("document.querySelectorAll('#tls-assign-form input[name=assign-target]').length >= 3"));
  await page.eval("document.querySelector('#tls-assign-form [data-modal-close]')?.click()");
  await page.screenshot(path.join(OUT, "tls-import-assign.png"));

  // ---- Software update listing and safe check.
  const updates = await api("GET", "/control/api/infra/updates");
  check("software update list loads", updates.status === 200 && Array.isArray(updates.body?.packages), String(updates.status));
  const updateCheck = await api("POST", "/control/api/infra/updates/check");
  check("package index refresh succeeds", updateCheck.status === 200 && Number(updateCheck.body?.count) >= 0, JSON.stringify({ status: updateCheck.status, code: updateCheck.body?.error?.code }));
  const missingJob = await api("GET", "/control/api/infra/updates/jobs/does-not-exist");
  check("unknown update job returns not found", missingJob.status === 404, String(missingJob.status));
  const badPackage = await api("POST", "/control/api/infra/updates/upgrade", { packages: ["not-a-real-package"], all: false });
  check("unavailable package updates are rejected", badPackage.status === 400, String(badPackage.status));
  if (Number(updateCheck.body?.count) === 0) {
    // Safe end-to-end exercise: the host has no candidate packages, so the
    // transient systemd unit runs apt-get upgrade as a no-op and still proves
    // job creation, streaming output, exit handling and cleanup.
    const noopJob = await api("POST", "/control/api/infra/updates/upgrade", { packages: [], all: true });
    check("a no-op bulk update job is accepted", noopJob.status === 202 && Boolean(noopJob.body?.jobId), JSON.stringify({ status: noopJob.status, error: noopJob.body?.error }));
    let finished = null;
    for (let attempt = 0; attempt < 90; attempt += 1) {
      await sleep(500);
      const job = await api("GET", `/control/api/infra/updates/jobs/${encodeURIComponent(noopJob.body.jobId)}`);
      if (job.status === 200 && ["completed", "failed"].includes(job.body?.status)) { finished = job.body; break; }
    }
    check("bulk update job completes through the transient systemd unit", finished?.status === "completed" && Number(finished?.exitCode) === 0, JSON.stringify({ status: finished?.status, exitCode: finished?.exitCode, error: finished?.error }));
    check("bulk update job streams apt output", String(finished?.output || "").includes("apt-get"));
  }
  await page.eval("location.hash = '#/updates'");
  await page.waitFor(() => document.querySelector('#updates-body .empty-state, #updates-body .table-wrap') !== null, 20000);
  check("software updates page is interactive", await page.eval("Boolean(document.querySelector('#updates-kpis .stat-grid, #updates-body'))"));
  await page.screenshot(path.join(OUT, "software-updates.png"));

  // ---- Deliverability health on the dashboard.
  const dashboard = await api("GET", "/control/api/dashboard");
  const health = dashboard.body?.deliverabilityHealth;
  check("dashboard exposes deliverability health", dashboard.status === 200 && health && Array.isArray(health.ips), JSON.stringify({ status: dashboard.status, health }));
  check("deliverability health identifies monitored IPs", health.ips.length === 0 || health.ips.every((entry) => entry.ip && entry.state), JSON.stringify(health.ips));
  const blocklistCheck = await api("POST", "/control/api/delivery/blocklists/check");
  check("blocklist check runs for dashboard reputation", blocklistCheck.status === 200 || blocklistCheck.status === 409, JSON.stringify({ status: blocklistCheck.status, code: blocklistCheck.body?.error?.code }));
  await page.eval("location.hash = '#/dashboard'");
  await page.waitFor(() => document.querySelector(".deliverability-health") !== null, 10000);
  check("dashboard renders Deliverability Health", await page.eval("Boolean(document.querySelector('.deliverability-health'))"));
  check("reputation rows include a pulsing state", await page.eval("document.querySelectorAll('.rep-ip').length === 0 || Array.from(document.querySelectorAll('.rep-ip')).every((row) => ['is-clear','is-listed','is-blocked','is-partial','is-unchecked'].some((name) => row.classList.contains(name)))"));
  await page.screenshot(path.join(OUT, "dashboard-deliverability.png"));

  const summary = { checks: checks.length, passed: checks.filter((item) => item.ok).length, pageErrors, importedName, checks };
  fs.writeFileSync(path.join(OUT, "control-0.10-summary.json"), JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ checks: checks.length, failures: 0, pageErrors: pageErrors.length, importedName }, null, 2));
} catch (error) {
  const summary = { checks, pageErrors, importedName, error: error.message };
  fs.writeFileSync(path.join(OUT, "control-0.10-summary.json"), JSON.stringify(summary, null, 2));
  try { await page.screenshot(path.join(OUT, "control-0.10-failure.png")); } catch {}
  console.error(JSON.stringify({ error: error.message, checks, pageErrors }, null, 2));
  process.exitCode = 1;
} finally {
  await browser.close();
  if (importedName) {
    try {
      execFileSync("ssh", ["-i", SSH_KEY, "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "root@130.94.46.22",
        `rm -f /etc/ssl/alt8mail/imported/${importedName}.crt /etc/ssl/alt8mail/imported/${importedName}.key; runuser -u postgres -- psql -d a8m -q -c "DELETE FROM control.ssl_certificates WHERE name='${importedName}'"`], { stdio: "ignore" });
    } catch (error) {
      console.error(`Could not clean imported test certificate: ${error.message}`);
    }
  }
  if (dovecotTouched) {
    try {
      execFileSync("ssh", ["-i", SSH_KEY, "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "root@130.94.46.22",
        "rm -f /etc/dovecot/conf.d/99-alt8mail-tls.conf; systemctl reload dovecot; runuser -u postgres -- psql -d a8m -q -c \"DELETE FROM control.tls_assignments WHERE target='dovecot'\""], { stdio: "ignore" });
    } catch (error) {
      console.error(`Could not clean Dovecot test assignment: ${error.message}`);
    }
  }
}
