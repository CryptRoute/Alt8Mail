import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.ALT8MAIL_UI_URL || "https://130-94-46-22.sslip.io/";
const EMAIL = process.env.ALT8MAIL_UI_EMAIL || "avery.chen@a8m.test";
const PASSWORD = process.env.ALT8MAIL_UI_PASSWORD || fs.readFileSync(path.resolve(__dirname, "..", "staging_avery_password"), "utf8").trim();
const OUT = path.resolve(__dirname, "..", "artifacts", "webmail-0.9");
fs.mkdirSync(OUT, { recursive: true });
const checks = [];
const pageErrors = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(BASE);
page.on("Runtime.exceptionThrown", (params) => pageErrors.push(params.exceptionDetails?.exception?.description || params.exceptionDetails?.text || "exception"));

const check = (name, condition, detail = "") => {
  const ok = Boolean(condition);
  checks.push({ name, ok, detail });
  if (!ok) throw new Error(`${name}${detail ? ` — ${detail}` : ""}`);
};

const api = (method, url, body) => page.eval(`(async () => {
  const match = document.cookie.match(/(?:^|; )a8m_system_csrf=([^;]+)/);
  const headers = { "Content-Type": "application/json" };
  if (match) headers["X-CSRF-Token"] = decodeURIComponent(match[1]);
  const response = await fetch(${JSON.stringify(url)}, {
    method: ${JSON.stringify(method)}, credentials: "same-origin", headers,
    body: ${body === undefined ? "undefined" : JSON.stringify(JSON.stringify(body))}
  });
  const payload = await response.json().catch(() => null);
  return { status: response.status, body: payload };
})()`);

async function waitForWorkspace() {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => document.getElementById("bootSplash")?.hidden === true, 20000);
  await page.waitFor(() => !document.getElementById("app").hidden, 15000);
  await sleep(350);
}

async function login() {
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 12000);
  await page.setValue("#loginEmail", EMAIL);
  await page.setValue("#loginPassword", PASSWORD);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden || !document.getElementById("twoFactorForm").hidden || !document.getElementById("enrollmentForm").hidden, 20000);
  const result = await page.eval("!document.getElementById('app').hidden ? 'app' : (!document.getElementById('twoFactorForm').hidden ? 'twofactor' : 'enrollment')");
  if (result !== "app") throw new Error(`Sign-in did not reach the workspace (${result})`);
  await waitForWorkspace();
}

async function reloadAtCurrentUrl() {
  const url = await page.eval("location.href");
  await page.goto(url, { waitForLoad: true });
  await waitForWorkspace();
}

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await login();

  // Normalize test state: close in-page sessions before deleting drafts so the
  // unload keepalive cannot recreate a draft that was just removed.
  await page.eval("window.confirm = () => true");
  await page.eval(`document.querySelectorAll('.compose[data-compose-id]').forEach((el) => { const value = el.querySelector('[data-compose-field=subject]')?.value || ''; if (value.startsWith('Minimized persistence')) el.querySelector('[data-compose-action=discard]')?.click(); })`);
  await sleep(900);
  const staleDrafts = await api("GET", "/api/drafts");
  let removedStale = false;
  for (const stale of (staleDrafts.body?.drafts || [])) {
    if (String(stale.subject || "").startsWith("Minimized persistence")) {
      await api("DELETE", `/api/drafts/${encodeURIComponent(stale.id)}`);
      removedStale = true;
    }
  }
  if (removedStale) await reloadAtCurrentUrl();
  const bootstrap = await api("GET", "/api/bootstrap");
  const pinnedIds = (bootstrap.body?.messages || []).filter((message) => message.pinned).map((message) => message.id);
  if (pinnedIds.length) {
    const unpin = await api("POST", "/api/messages/actions", { ids: pinnedIds, action: "unpin" });
    assert.equal(unpin.status, 200);
    await reloadAtCurrentUrl();
  }
  await page.eval("(() => { const el = document.getElementById('inboxViewSelect'); if (el) { el.value = 'grouped'; el.dispatchEvent(new Event('change', { bubbles: true })); } })()");
  await sleep(500);
  const originalView = "grouped";
  check("workspace loads", await page.eval("!document.getElementById('app').hidden"));

  // Folder hash survives reload.
  await page.eval("location.hash = '#/Sent'");
  await page.waitFor(() => document.getElementById("listTitle").textContent === "Sent", 8000);
  await reloadAtCurrentUrl();
  check("folder hash restores the Sent folder", await page.eval("document.getElementById('listTitle').textContent === 'Sent' && document.querySelector('[data-folder=sent]').classList.contains('is-active')"));
  check("folder hash is preserved", await page.eval("location.hash === '#/Sent'"));

  // Settings without a submenu keeps its canonical anchor.
  await page.eval("location.hash = '#/Settings'");
  await page.waitFor(() => !document.getElementById("settingsView").hidden && document.getElementById("settingsTitle").textContent === "Appearance", 8000);
  await reloadAtCurrentUrl();
  check("Settings anchor restores the settings page", await page.eval("!document.getElementById('settingsView').hidden && document.getElementById('settingsTitle').textContent === 'Appearance'"));
  check("Settings anchor is canonical", await page.eval("location.hash === '#/Settings'"));

  // Hash routing: settings submenu survives reload.
  await page.eval("document.getElementById('settingsBtn').click()");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.waitFor(() => location.hash.startsWith("#/Settings"), 5000);
  await page.eval("document.querySelector('[data-settings-tab=\"rules\"]').click()");
  await page.waitFor(() => document.getElementById("settingsTitle").textContent === "Rules", 5000);
  const settingsHash = await page.eval("location.hash");
  check("settings hash names the rules tab", settingsHash === "#/Settings/rules", settingsHash);
  await reloadAtCurrentUrl();
  check("settings page restored after reload", await page.eval("!document.getElementById('settingsView').hidden && document.getElementById('settingsTitle').textContent === 'Rules'"));
  check("settings hash preserved after reload", await page.eval("location.hash === '#/Settings/rules'"));

  // Branding and About content.
  check("quota source is Alt8Mail", await page.eval("document.getElementById('quotaSource').textContent === 'Alt8Mail live'"));
  check("settings footer is Alt8Mail", await page.eval("document.querySelector('.settings-nav-foot').textContent.includes('Alt8Mail') && !document.querySelector('.settings-nav-foot').textContent.includes('Alt8Mail IT')"));
  await page.eval("document.querySelector('[data-settings-tab=\"about\"]').click()");
  await page.waitFor(() => document.getElementById("settingsTitle").textContent === "About", 5000);
  const about = await page.eval("document.querySelector('[data-settings-panel=\"about\"]').innerText");
  check("About no longer uses the concept heading", !about.includes("About this concept"));
  check("About uses Alt8Mail Email System", about.includes("Alt8Mail Email System") && !about.includes("Aurora"));
  check("About describes AI and advanced security", about.toLowerCase().includes("ai assistance") && about.toLowerCase().includes("advanced security"));
  const mailClient = await page.eval("document.getElementById('mailClientDetails').innerText");
  check("About lists IMAP, POP3 and SMTP hosts", mailClient.includes("IMAP") && mailClient.includes("POP3") && mailClient.includes("SMTP") && mailClient.includes("993") && mailClient.includes("995") && mailClient.includes("587"), mailClient);
  check("About includes client setup instructions", about.includes("Connect another email client") && about.includes("app password"));

  // Individual reading view hides conversation-only controls.
  await page.eval("document.querySelector('[data-settings-tab=\"mail\"]').click()");
  await page.waitFor(() => document.getElementById("inboxViewSelect") !== null, 5000);
  await page.eval("(() => { const el = document.getElementById('inboxViewSelect'); el.value = 'individual'; el.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(700);
  await page.eval("document.querySelector('[data-task=\"mail\"]').click()");
  await page.waitFor(() => !document.getElementById("mailView").hidden, 5000);
  await page.eval("document.querySelector('[data-folder=\"inbox\"]').click()");
  await sleep(500);
  if (!(await page.eval("document.querySelectorAll('.msg[data-thread-id]').length"))) {
    await page.eval("document.querySelector('[data-folder=\"sent\"]').click()");
    await sleep(500);
  }
  check("a mailbox row is available for reading", await page.eval("document.querySelectorAll('.msg[data-thread-id]').length > 0"));
  await page.eval("document.querySelector('.msg[data-thread-id]').click()");
  await page.waitFor(() => !document.getElementById("reader").classList.contains("is-empty"), 5000);
  await sleep(500);
  check("individual view hides conversation reply", await page.eval("!document.querySelector('[data-reply-thread]')"));
  check("individual view hides conversation forward", await page.eval("!document.querySelector('[data-forward-thread]')"));
  check("individual view hides expand all", await page.eval("document.getElementById('threadExpandAll').hidden === true && !document.getElementById('readerExpandAll')"));
  check("individual view hides collapse all", await page.eval("document.getElementById('threadCollapseAll').hidden === true && !document.getElementById('readerCollapseAll')"));
  const messageHash = await page.eval("location.hash");
  check("message hash includes an item id", /^#\/[^/]+\/[^/]+/.test(messageHash), messageHash);

  // Pin and unpin, persisted through reload.
  const pinDebug = await page.eval("(async () => { const data = await fetch('/api/bootstrap', { credentials: 'same-origin' }).then((r) => r.json()); const key = location.hash.split('/').pop(); const msg = (data.messages || []).find((item) => item.id === key); return JSON.stringify({ aria: document.getElementById('readerPinBtn').getAttribute('aria-pressed'), messagePinned: msg?.pinned, hash: location.hash, listPins: document.querySelectorAll('.msg .pin-btn.is-pinned').length }); })()");
  check("pin control starts unpinned", await page.eval("document.getElementById('readerPinBtn').getAttribute('aria-pressed') === 'false'"), pinDebug);
  await page.click("#readerPinBtn");
  await page.waitFor(() => document.getElementById("readerPinBtn").getAttribute("aria-pressed") === "true", 6000);
  check("pinned message is marked in the list", await page.eval("Boolean(document.querySelector('.msg .pin-btn.is-pinned'))"));
  const draftsBeforeReload = await api("GET", "/api/drafts");
  check("draft API remains available", draftsBeforeReload.status === 200);
  await reloadAtCurrentUrl();
  check("selected message restored from hash", await page.eval("!document.getElementById('reader').classList.contains('is-empty')"));
  check("pin state persists after reload", await page.eval("document.getElementById('readerPinBtn').getAttribute('aria-pressed') === 'true'"));
  check("individual controls stay hidden after reload", await page.eval("document.getElementById('threadExpandAll').hidden === true && !document.querySelector('[data-reply-thread]')"));
  await page.screenshot(path.join(OUT, "individual-pinned.png"));

  // Grouped view restores conversation controls.
  await page.eval("document.querySelector('[data-task=\"settings\"]').click()");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.eval("document.querySelector('[data-settings-tab=\"mail\"]').click()");
  await sleep(300);
  await page.eval("(() => { const el = document.getElementById('inboxViewSelect'); el.value = 'grouped'; el.dispatchEvent(new Event('change', { bubbles: true })); })()");
  await sleep(600);
  await page.goto(BASE + messageHash);
  await waitForWorkspace();
  check("grouped view shows conversation reply and forward", await page.eval("Boolean(document.querySelector('[data-reply-thread]') && document.querySelector('[data-forward-thread]'))"));
  check("grouped view shows expand and collapse", await page.eval("!document.getElementById('threadExpandAll').hidden && Boolean(document.getElementById('readerExpandAll'))"));
  await page.eval("document.getElementById('readerPinBtn').click()");
  await sleep(700);

  // Compose minimization survives reload, logout and a fresh sign-in.
  await page.eval("document.getElementById('cmdCompose').click()");
  await page.waitFor(() => document.querySelectorAll('.compose[data-compose-id]').length > 0, 5000);
  const subject = `Minimized persistence ${Date.now()}`;
  await page.setValue('.compose[data-compose-id] [data-compose-field="subject"]', subject);
  await sleep(1100);
  await page.eval(`(() => { const el = Array.from(document.querySelectorAll('.compose[data-compose-id]')).find((node) => node.querySelector('[data-compose-field="subject"]')?.value === ${JSON.stringify(subject)}); if (!el) throw new Error('test compose not found'); el.querySelector('[data-compose-action="minimize"]').click(); })()`);
  await page.waitFor(() => document.querySelectorAll('#composeDockTabs [data-compose-tab]').length > 0, 5000);
  await sleep(900);
  let drafts = await api("GET", "/api/drafts");
  let draft = (drafts.body?.drafts || []).find((item) => item.subject === subject);
  check("compose is saved as minimized on the server", Boolean(draft) && draft.windowMode === "minimized", draft ? draft.windowMode : "missing");
  const ourComposeHidden = () => page.eval(`(() => { const el = Array.from(document.querySelectorAll('.compose[data-compose-id]')).find((node) => node.querySelector('[data-compose-field="subject"]')?.value === ${JSON.stringify(subject)}); return Boolean(el) && el.hidden && getComputedStyle(el).display === 'none'; })()`);
  check("compose window is hidden after minimize", await ourComposeHidden());
  await reloadAtCurrentUrl();
  check("minimized compose restored after reload", await ourComposeHidden() && await page.eval(`Array.from(document.querySelectorAll('#composeDockTabs [data-compose-tab]')).some((tab) => tab.textContent.includes(${JSON.stringify(subject)}))`));
  drafts = await api("GET", "/api/drafts");
  draft = (drafts.body?.drafts || []).find((item) => item.subject === subject);
  check("server still reports minimized after reload", Boolean(draft) && draft.windowMode === "minimized");

  await page.eval("document.getElementById('railLogout').click()");
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 10000);
  await login();
  check("minimized compose survives logout and sign-in", await ourComposeHidden() && await page.eval(`Array.from(document.querySelectorAll('#composeDockTabs [data-compose-tab]')).some((tab) => tab.textContent.includes(${JSON.stringify(subject)}))`));
  drafts = await api("GET", "/api/drafts");
  draft = (drafts.body?.drafts || []).find((item) => item.subject === subject);
  check("server keeps minimized state across sign-in", Boolean(draft) && draft.windowMode === "minimized");
  check("minimized state is server-side, not browser storage", await page.eval("!Object.keys(localStorage).some((key) => key.includes('compose') && localStorage.getItem(key)?.includes('minimized'))"));
  await page.screenshot(path.join(OUT, "minimized-after-login.png"));

  // A second, clean browser profile represents signing in from another PC.
  const browser2 = await launchBrowser({ headless: true, width: 1280, height: 900, port: 9334, ignoreCertificateErrors: false });
  try {
    const page2 = await browser2.newPage(BASE);
    await page2.waitFor(() => document.readyState === "complete", 15000);
    await page2.waitFor(() => !document.getElementById("loginScreen").hidden, 12000);
    await page2.setValue("#loginEmail", EMAIL);
    await page2.setValue("#loginPassword", PASSWORD);
    await page2.click("#loginSubmit");
    await page2.waitFor(() => !document.getElementById("app").hidden, 20000);
    await page2.waitFor(() => document.getElementById("bootSplash")?.hidden === true, 20000);
    await page2.waitFor(`Array.from(document.querySelectorAll('#composeDockTabs [data-compose-tab]')).some((tab) => tab.textContent.includes(${JSON.stringify(subject)}))`, 12000);
    check("a second browser restores the draft minimized from server state", await page2.eval(`(() => { const el = Array.from(document.querySelectorAll('.compose[data-compose-id]')).find((node) => node.querySelector('[data-compose-field="subject"]')?.value === ${JSON.stringify(subject)}); return Boolean(el) && el.hidden && Array.from(document.querySelectorAll('#composeDockTabs [data-compose-tab]')).some((tab) => tab.textContent.includes(${JSON.stringify(subject)})); })()`));
    await page2.screenshot(path.join(OUT, "minimized-second-browser.png"));
  } finally {
    await browser2.close();
  }

  // Reopen the draft, verify it becomes floating, then discard it.
  await page.eval(`Array.from(document.querySelectorAll('#composeDockTabs [data-compose-tab]')).find((tab) => tab.textContent.includes(${JSON.stringify(subject)}))?.click()`);
  await page.waitFor(() => document.querySelectorAll('.compose[data-compose-id]:not([hidden])').length > 0, 5000);
  await sleep(700);
  drafts = await api("GET", "/api/drafts");
  draft = (drafts.body?.drafts || []).find((item) => item.subject === subject);
  check("opening a minimized draft restores floating mode", Boolean(draft) && draft.windowMode === "floating", draft ? draft.windowMode : "missing");
  await page.eval("window.confirm = () => true");
  await page.eval(`(() => { const el = Array.from(document.querySelectorAll('.compose[data-compose-id]')).find((node) => node.querySelector('[data-compose-field="subject"]')?.value === ${JSON.stringify(subject)}); el?.querySelector('[data-compose-action=discard]')?.click(); })()`);
  await sleep(800);
  const afterDiscard = await api("GET", "/api/drafts");
  check("test draft is discarded and does not recreate on exit", !(afterDiscard.body?.drafts || []).some((item) => item.subject === subject));
  await page.eval("location.hash = '#/Inbox'");
  await sleep(300);

  // Restore the original inbox preference.
  if (true) {
    await page.eval("document.querySelector('[data-task=\"settings\"]').click()");
    await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
    await page.eval("document.querySelector('[data-settings-tab=\"mail\"]').click()");
    await sleep(250);
    await page.eval("(() => { const el = document.getElementById('inboxViewSelect'); el.value = 'grouped'; el.dispatchEvent(new Event('change', { bubbles: true })); })()");
    await sleep(500);
  }

  const summary = { checks: checks.length, passed: checks.filter((item) => item.ok).length, pageErrors, checks };
  fs.writeFileSync(path.join(OUT, "summary.json"), JSON.stringify(summary, null, 2));
  console.log(JSON.stringify({ summary: { checks: checks.length, failures: 0, pageErrors: pageErrors.length }, pageErrors }, null, 2));
} catch (error) {
  const summary = { checks, pageErrors, error: error.message };
  fs.writeFileSync(path.join(OUT, "summary.json"), JSON.stringify(summary, null, 2));
  try { await page.screenshot(path.join(OUT, "failure.png")); } catch {}
  console.error(JSON.stringify({ error: error.message, checks, pageErrors, pageConsole: page.console.slice(-20) }, null, 2));
  process.exitCode = 1;
} finally {
  await browser.close();
}
