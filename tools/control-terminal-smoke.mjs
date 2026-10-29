import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.CONTROL_BASE || "https://130.94.46.22:8808";
const EMAIL = process.env.CONTROL_EMAIL || "sysadmin@alt8mail.local";
const PASSWORD = process.env.CONTROL_PASSWORD || fs.readFileSync(process.env.CONTROL_PASSWORD_FILE, "utf8").trim();
const KEY_PATH = process.env.TERMINAL_KEY_PATH || path.join(os.homedir(), ".ssh", "meridian_deploy_ed25519");
const OUT = path.resolve(__dirname, "..", "artifacts", "control-ui");
fs.mkdirSync(OUT, { recursive: true });
const checks = [];
const pageErrors = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const check = (name, condition, detail = "") => {
  checks.push({ name, ok: Boolean(condition), detail });
  if (!condition) throw new Error(`${name}${detail ? ` — ${detail}` : ""}`);
};
const ssh = (command, input) => execFileSync("ssh", ["-i", KEY_PATH, "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "root@130.94.46.22", command], { input, encoding: "utf8" }).trim();
let tempUser = "";
let tempUserCreated = false;

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

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => Boolean(document.querySelector('#login-form input[name=email]')), 15000);
  // The control login form uses stable names; fill whichever is present.
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
  await page.waitFor(() => !document.getElementById("app")?.querySelector(".login-page"), 15000);
  await sleep(800);
  check("control panel signs in", await page.eval("Boolean(document.querySelector('.sidebar'))"));

  const info = await api("GET", "/control/api/infra/terminal/info");
  check("terminal info endpoint succeeds", info.status === 200, JSON.stringify(info.body));
  check("SSH port is detected from sshd", Number(info.body?.port) > 0 && String(info.body?.portSource || "").includes("sshd"), JSON.stringify(info.body));
  check("terminal reports loopback-only access", info.body?.host === "127.0.0.1" && info.body?.loopbackOnly === true);
  check("terminal info reports key authentication", info.body?.keyAuthentication === true);
  check("terminal info reports loopback password authentication", info.body?.passwordAuthentication === true, JSON.stringify(info.body));

  // Reproduce the actual UI path: a wrong system password must leave the panel
  // signed in and show the terminal error in place.
  await page.eval("location.hash = '#/terminal'");
  await page.waitFor(() => Boolean(document.querySelector('.terminal-canvas .xterm')), 15000);
  await page.setValue('#terminal-form [name="authType"]', "password");
  await page.setValue('#terminal-form [name="username"]', "root");
  await page.setValue('#terminal-form [name="password"]', `wrong-${crypto.randomBytes(12).toString("hex")}`);
  await page.click("#terminal-connect");
  await page.waitFor(() => document.getElementById("terminal-state").textContent === "Failed", 20000);
  check("wrong terminal password keeps the panel signed in", await page.eval("Boolean(document.querySelector('.sidebar')) && !document.querySelector('.login-page')"), "login page or missing sidebar");
  check("wrong terminal password shows an in-place terminal failure", await page.eval("document.getElementById('terminal-state').textContent === 'Failed' && Boolean(document.querySelector('.sidebar'))"));

  // A failed SSH password must return a terminal-specific error and must not
  // expire the operator's System Control session. This is the regression that
  // previously threw the whole panel back to the login screen.
  const badPassword = await api("POST", "/control/api/infra/terminal/sessions", {
    username: "root", authType: "password", password: `wrong-${crypto.randomBytes(12).toString("hex")}`,
    rows: 32, cols: 100,
  });
  check("failed SSH password returns 422, not a panel 401", badPassword.status === 422 && badPassword.body?.error?.code === "SSH_AUTH", JSON.stringify({ status: badPassword.status, code: badPassword.body?.error?.code }));
  const panelAfterFailure = await api("GET", "/control/api/me");
  check("panel session survives a failed terminal password", panelAfterFailure.status === 200 && await page.eval("Boolean(document.querySelector('.sidebar'))"), String(panelAfterFailure.status));

  // Create a short-lived local account to prove the password path end to end.
  const suffix = crypto.randomBytes(6).toString("hex");
  tempUser = `a8mterm${suffix}`;
  const tempPassword = `T!${crypto.randomBytes(18).toString("base64url")}`;
  ssh(`useradd -m -s /bin/bash ${tempUser} && chpasswd`, `${tempUser}:${tempPassword}
`);
  tempUserCreated = true;
  const passwordSession = await api("POST", "/control/api/infra/terminal/sessions", {
    username: tempUser, authType: "password", password: tempPassword, rows: 32, cols: 100,
  });
  check("SSH terminal session is created with a system password", passwordSession.status === 201 && Boolean(passwordSession.body?.ticket), JSON.stringify({ status: passwordSession.status, code: passwordSession.body?.error?.code, message: passwordSession.body?.error?.message }));
  const passwordOutput = await page.eval(`(async () => {
    return await new Promise((resolve, reject) => {
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(scheme + '://' + location.host + '/control/api/infra/terminal/ws?ticket=' + encodeURIComponent(${JSON.stringify(passwordSession.body.ticket)}));
      ws.binaryType = 'arraybuffer';
      let text = '';
      const timer = setTimeout(() => { try { ws.close(); } catch {} reject(new Error('timed out; output=' + text)); }, 15000);
      ws.onopen = () => { ws.send(JSON.stringify({ type: 'input', data: "printf 'ALT8MAIL_PASSWORD_TERMINAL_OK\\\\n'; exit\\n" })); };
      ws.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) text += new TextDecoder().decode(new Uint8Array(event.data));
        else text += String(event.data);
        if (text.includes('ALT8MAIL_PASSWORD_TERMINAL_OK')) { clearTimeout(timer); try { ws.close(); } catch {} resolve(text); }
      };
      ws.onerror = () => { clearTimeout(timer); reject(new Error('websocket error; output=' + text)); };
      ws.onclose = () => { if (!text.includes('ALT8MAIL_PASSWORD_TERMINAL_OK')) { clearTimeout(timer); reject(new Error('closed; output=' + text)); } };
    });
  })()`);
  check("password SSH shell returns command output over WebSocket", passwordOutput.includes("ALT8MAIL_PASSWORD_TERMINAL_OK"));
  await sleep(300);
  await page.setValue('#terminal-form [name="authType"]', "password");
  await page.setValue('#terminal-form [name="username"]', tempUser);
  await page.setValue('#terminal-form [name="password"]', tempPassword);
  await page.click("#terminal-connect");
  await page.waitFor(() => document.getElementById("terminal-state").textContent === "Connected", 20000);
  check("terminal page connects with a system password without signing out", await page.eval("document.getElementById('terminal-state').textContent === 'Connected' && Boolean(document.querySelector('.sidebar')) && !document.querySelector('.login-page')"));
  await page.eval("document.getElementById('terminal-disconnect').click()");
  await sleep(500);
  ssh(`pkill -KILL -u ${tempUser} 2>/dev/null || true; sleep 0.2; userdel -r ${tempUser} 2>/dev/null || true`);
  tempUserCreated = false;

  const privateKey = fs.readFileSync(KEY_PATH, "utf8");
  const created = await api("POST", "/control/api/infra/terminal/sessions", {
    username: "root",
    authType: "key",
    privateKey,
    passphrase: "",
    rows: 32,
    cols: 100,
  });
  check("SSH terminal session is created with an SSH key", created.status === 201 && Boolean(created.body?.ticket), JSON.stringify({ status: created.status, code: created.body?.error?.code, message: created.body?.error?.message }));
  check("session reports the detected port", Number(created.body?.port) === Number(info.body?.port));

  const output = await page.eval(`(async () => {
    return await new Promise((resolve, reject) => {
      const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(scheme + '://' + location.host + '/control/api/infra/terminal/ws?ticket=' + encodeURIComponent(${JSON.stringify(created.body.ticket)}));
      ws.binaryType = 'arraybuffer';
      let text = '';
      const timer = setTimeout(() => { try { ws.close(); } catch {} reject(new Error('timed out; output=' + text)); }, 15000);
      ws.onopen = () => {
        ws.send(JSON.stringify({ type: 'input', data: "printf 'ALT8MAIL_TERMINAL_OK\\\\n'; exit\\n" }));
      };
      ws.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) text += new TextDecoder().decode(new Uint8Array(event.data));
        else text += String(event.data);
        if (text.includes('ALT8MAIL_TERMINAL_OK')) { clearTimeout(timer); try { ws.close(); } catch {} resolve(text); }
      };
      ws.onerror = () => { clearTimeout(timer); reject(new Error('websocket error; output=' + text)); };
      ws.onclose = () => { if (!text.includes('ALT8MAIL_TERMINAL_OK')) { clearTimeout(timer); reject(new Error('closed; output=' + text)); } };
    });
  })()`);
  check("SSH shell returns command output over WebSocket", output.includes("ALT8MAIL_TERMINAL_OK"));

  // The interactive UI should mount xterm and show the detected port.
  await page.eval("location.hash = '#/terminal'");
  await page.waitFor(() => location.hash === "#/terminal", 5000);
  await page.waitFor(() => Boolean(document.querySelector('.terminal-canvas .xterm')), 15000);
  check("terminal page mounts the xterm renderer", await page.eval("Boolean(document.querySelector('.terminal-canvas .xterm'))"));
  check("terminal page shows the detected port", await page.eval("document.querySelector('.terminal-port-chip')?.textContent.includes(String(" + JSON.stringify(String(info.body.port)) + "))"));
  await page.screenshot(path.join(OUT, "terminal.png"));

  const summary = { checks: checks.length, passed: checks.filter((c) => c.ok).length, pageErrors, checks };
  fs.writeFileSync(path.join(OUT, "terminal-summary.json"), JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ checks: checks.length, failures: 0, pageErrors: pageErrors.length, port: info.body.port, portSource: info.body.portSource }, null, 2));
} catch (error) {
  const summary = { checks, pageErrors, error: error.message };
  fs.writeFileSync(path.join(OUT, "terminal-summary.json"), JSON.stringify(summary, null, 2));
  try { await page.screenshot(path.join(OUT, "terminal-failure.png")); } catch {}
  console.error(JSON.stringify({ error: error.message, checks, pageErrors }, null, 2));
  process.exitCode = 1;
} finally {
  if (tempUserCreated && tempUser) {
    try { ssh(`pkill -KILL -u ${tempUser} 2>/dev/null || true; sleep 0.2; userdel -r ${tempUser} 2>/dev/null || true`); } catch {}
  }
  await browser.close();
}
