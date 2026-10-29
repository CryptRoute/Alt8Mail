import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import https from "node:https";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.CONTROL_BASE || "https://130.94.46.22:8808";
const EMAIL = process.env.CONTROL_EMAIL || "sysadmin@alt8mail.local";
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, "utf8").trim();
const SSH_KEY = process.env.CONTROL_SSH_KEY || path.join(os.homedir(), ".ssh", "meridian_deploy_ed25519");
const OUT = path.resolve(__dirname, "..", "artifacts", "control-ui");
fs.mkdirSync(OUT, { recursive: true });
const checks = [];
const errors = [];
const check = (name, condition, detail = "") => {
  checks.push({ name, ok: Boolean(condition), detail });
  if (!condition) throw new Error(`${name}${detail ? ` — ${detail}` : ""}`);
};
const ssh = (script) => execFileSync("ssh", ["-i", SSH_KEY, "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "root@130.94.46.22", script], { encoding: "utf8" }).trim();
const requestHealth = (url, rejectUnauthorized) => new Promise((resolve) => {
  const request = https.get(url, { rejectUnauthorized, timeout: 15000 }, (response) => {
    response.resume();
    resolve(String(response.statusCode || 0));
  });
  request.on("timeout", () => { request.destroy(); resolve("0"); });
  request.on("error", () => resolve("0"));
});
const curlK = (url) => requestHealth(url, false);
const probeHealth = async (url, { insecure = false } = {}) => {
  let status = "0";
  for (let attempt = 0; attempt < 5; attempt += 1) {
    status = await requestHealth(url, !insecure);
    if (status === "200") return status;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  return status;
};

const browser = await launchBrowser({ headless: true, width: 1280, height: 900, ignoreCertificateErrors: true });
const page = await browser.newPage(BASE);
let importedName = "";
let originalWeb = null;
let originalPostfix = null;
let webTarget = null;
let postfixTarget = null;
let webSwapped = false;
let postfixSwapped = false;
const api = (method, url, body) => page.eval(`(async () => {
  const headers = { "Content-Type": "application/json" };
  const token = document.cookie.match(/(?:^|; )alt8mail_control_csrf=([^;]+)/)?.[1];
  if (token) headers["X-Alt8Mail-CSRF"] = decodeURIComponent(token);
  const response = await fetch(${JSON.stringify(url)}, { method: ${JSON.stringify(method)}, credentials: "same-origin", headers, body: ${body === undefined ? "undefined" : JSON.stringify(JSON.stringify(body))} });
  const payload = await response.json().catch(() => null);
  return { status: response.status, body: payload };
})()`);

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => Boolean(document.querySelector('#login-form input[name=email]')), 15000);
  await page.eval(`(() => {
    const email = document.querySelector('#login-form input[name=email]');
    const password = document.querySelector('#login-form input[name=password]');
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    for (const [el, value] of [[email, ${JSON.stringify(EMAIL)}], [password, ${JSON.stringify(PASSWORD)}]]) {
      setter.call(el, value); el.dispatchEvent(new Event('input', { bubbles: true }));
    }
    email.form.requestSubmit();
  })()`);
  await page.waitFor(() => Boolean(document.getElementById("sidebar")), 15000);

  const mappings = (await api("GET", "/control/api/tls/assignments")).body;
  webTarget = mappings.targets.find((target) => target.key === "web");
  postfixTarget = mappings.targets.find((target) => target.key === "postfix");
  const certs = (await api("GET", "/control/api/tls/certificates")).body.certificates;
  originalWeb = certs.find((cert) => cert.certPath === webTarget?.certPath && cert.hasKey);
  originalPostfix = certs.find((cert) => cert.certPath === postfixTarget?.certPath && cert.hasKey);
  check("original web certificate is in the inventory", Boolean(originalWeb), JSON.stringify(webTarget));
  check("original Postfix certificate is in the inventory", Boolean(originalPostfix), JSON.stringify(postfixTarget));

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "alt8mail-live-tls-"));
  const certFile = path.join(tmp, "cert.pem");
  const keyFile = path.join(tmp, "key.pem");
  execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
    "-subj", "/CN=live-swap.alt8mail.local",
    "-addext", "subjectAltName=DNS:live-swap.alt8mail.local",
    "-keyout", keyFile, "-out", certFile], { stdio: "ignore" });
  const imported = await api("POST", "/control/api/tls/import", {
    name: `swap-${Date.now()}`, certificate: fs.readFileSync(certFile, "utf8"),
    privateKey: fs.readFileSync(keyFile, "utf8"), passphrase: "", overwrite: false,
  });
  fs.rmSync(tmp, { recursive: true, force: true });
  check("temporary certificate is imported", imported.status === 201, JSON.stringify(imported.body?.error));
  importedName = imported.body.certificate.name;

  // Swap nginx to the imported certificate, prove it serves TLS, then restore.
  const webAssign = await api("POST", "/control/api/tls/assign", { certificate: importedName, targets: ["web"] });
  check("nginx accepts the imported certificate", webAssign.status === 200, JSON.stringify(webAssign.body?.error));
  webSwapped = true;
  check("nginx health responds with the imported certificate", (await probeHealth("https://130-94-46-22.sslip.io/api/health", { insecure: true })) === "200");
  const webConfig = ssh("readlink -f /etc/nginx/sites-enabled/meridian && grep -n 'ssl_certificate ' /etc/nginx/sites-available/meridian | head -n 1");
  check("nginx configuration points at the imported certificate", webConfig.includes(`/etc/ssl/alt8mail/imported/${importedName}.crt`), webConfig);
  check("nginx site remains a symlink after the rewrite", ssh("test -L /etc/nginx/sites-enabled/meridian && echo symlink").includes("symlink"));
  const webRestore = await api("POST", "/control/api/tls/assign", { certificate: originalWeb.name, targets: ["web"] });
  check("nginx certificate is restored", webRestore.status === 200, JSON.stringify(webRestore.body?.error));
  webSwapped = false;
  check("webmail responds with its original certificate after restore", (await probeHealth("https://130-94-46-22.sslip.io/api/health")) === "200");

  // Swap Postfix to the imported certificate, prove the unprivileged daemon can
  // read the ACL-protected key, then restore the original mail certificate.
  const postfixAssign = await api("POST", "/control/api/tls/assign", { certificate: importedName, targets: ["postfix"] });
  check("Postfix accepts the imported certificate", postfixAssign.status === 200, JSON.stringify(postfixAssign.body?.error));
  postfixSwapped = true;
  const postfixPaths = ssh("postconf -h smtpd_tls_cert_file smtpd_tls_key_file");
  check("Postfix configuration points at the imported certificate", postfixPaths.includes(`/etc/ssl/alt8mail/imported/${importedName}.crt`), postfixPaths);
  const serviceStatus = (await api("GET", "/control/api/services")).body.services;
  check("Postfix remains active with the imported key", serviceStatus.some((service) => service.unit === "postfix@-.service" && service.active));
  const postfixRestore = await api("POST", "/control/api/tls/assign", { certificate: originalPostfix.name, targets: ["postfix"] });
  check("Postfix certificate is restored", postfixRestore.status === 200, JSON.stringify(postfixRestore.body?.error));
  postfixSwapped = false;
  check("Postfix configuration returns to its original path", ssh("postconf -h smtpd_tls_cert_file").includes(postfixTarget.certPath));

  const summary = { checks: checks.length, failures: 0, errors, checks };
  fs.writeFileSync(path.join(OUT, "tls-live-swap-summary.json"), JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ checks: checks.length, failures: 0, importedName }, null, 2));
} catch (error) {
  const summary = { checks, errors, importedName, error: error.message };
  fs.writeFileSync(path.join(OUT, "tls-live-swap-summary.json"), JSON.stringify(summary, null, 2));
  console.error(JSON.stringify({ error: error.message, checks, errors }, null, 2));
  process.exitCode = 1;
} finally {
  // Always put the live stack back, even if the test aborted between swap and restore.
  try {
    if (webSwapped && originalWeb) {
      const restored = await api("POST", "/control/api/tls/assign", { certificate: originalWeb.name, targets: ["web"] });
      if (restored.status === 200) webSwapped = false;
    }
    if (postfixSwapped && originalPostfix) {
      const restored = await api("POST", "/control/api/tls/assign", { certificate: originalPostfix.name, targets: ["postfix"] });
      if (restored.status === 200) postfixSwapped = false;
    }
  } catch { /* forced restore below */ }
  if (webSwapped) {
    const cert = originalWeb?.certPath || webTarget?.certPath || "/etc/letsencrypt/live/130-94-46-22.sslip.io/fullchain.pem";
    const key = originalWeb?.keyPath || webTarget?.keyPath || "/etc/letsencrypt/live/130-94-46-22.sslip.io/privkey.pem";
    ssh(`sed -i "s#^\\([[:space:]]*ssl_certificate[[:space:]]\\).*#\\1${cert};#" /etc/nginx/sites-available/meridian; sed -i "s#^\\([[:space:]]*ssl_certificate_key[[:space:]]\\).*#\\1${key};#" /etc/nginx/sites-available/meridian; nginx -t && systemctl reload nginx`);
    webSwapped = false;
  }
  if (postfixSwapped) {
    const cert = originalPostfix?.certPath || postfixTarget?.certPath || "/etc/ssl/meridian/mail.crt";
    const key = originalPostfix?.keyPath || postfixTarget?.keyPath || "/etc/ssl/meridian/mail.key";
    ssh(`postconf -e smtpd_tls_cert_file=${cert} smtpd_tls_key_file=${key} && postfix check && postfix reload`);
    postfixSwapped = false;
  }
  if (importedName) {
    try {
      const referenced = ssh(`grep -R ${importedName} /etc/nginx/sites-available /etc/postfix/main.cf /etc/dovecot/conf.d 2>/dev/null | wc -l`);
      if (referenced.trim() !== "0") {
        console.error("Imported test certificate is still referenced by a live service; leaving it on disk for manual recovery.");
      } else {
        ssh(`rm -f /etc/ssl/alt8mail/imported/${importedName}.crt /etc/ssl/alt8mail/imported/${importedName}.key; runuser -u postgres -- psql -d meridian -q -c "DELETE FROM control.ssl_certificates WHERE name='${importedName}'; DELETE FROM control.tls_assignments WHERE certificate_name='${importedName}'"`);
      }
    } catch (error) {
      console.error(`Could not clean imported swap certificate: ${error.message}`);
    }
  }
  await browser.close();
}
