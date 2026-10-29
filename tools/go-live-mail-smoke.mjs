import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const base = process.env.ALT8MAIL_UI_URL || "https://130-94-46-22.sslip.io/";
const email = process.env.ALT8MAIL_UI_EMAIL || "avery.chen@a8m.test";
const password = process.env.ALT8MAIL_UI_PASSWORD || "";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
if (!password) throw new Error("ALT8MAIL_UI_PASSWORD is required");

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);

const apiFetch = (method, url, body) => page.eval(`(async () => {
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

try {
  await page.send("Browser.setPermission", {
    permission: { name: "notifications" },
    setting: "denied",
    origin: new URL(base).origin
  });
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(500);
  await page.setValue("#loginEmail", email);
  await page.setValue("#loginPassword", password);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await sleep(300);

  console.log("\n[1] Open webmail stays live while a message is delivered");
  const marker = await page.eval("window.__liveMarker = Math.random()");
  const baseline = await page.eval("document.querySelectorAll('.msg').length");
  await page.eval(`window.__mailToast = false; window.__toastObserver = new MutationObserver(() => {
    const toast = document.getElementById("toast");
    if (toast && toast.textContent.includes("New mail")) window.__mailToast = true;
  }); window.__toastObserver.observe(document.getElementById("toast"), { childList: true, subtree: true, characterData: true });`);

  const subject = `Live inbox delivery ${Date.now()}`;
  const queued = await apiFetch("POST", "/api/send", {
    fromEmail: email,
    to: email,
    cc: "",
    bcc: "",
    subject,
    bodyText: "This message verifies live INBOX delivery while the webmail stays open.",
    attachments: [],
    priority: "normal",
    receipt: false,
    delivery: false,
    saveCopy: true,
    holdSeconds: 5
  });
  assert.equal(queued.status, 200, `send queued: ${JSON.stringify(queued.body)}`);
  console.log("  Queued self-delivery:", subject);

  await page.waitFor(`(() => {
    const rows = [...document.querySelectorAll('.msg')];
    return rows.some((row) => row.textContent.includes(${JSON.stringify(subject)}));
  })()`, 90000, 500);
  await sleep(500);
  const after = await page.eval("document.querySelectorAll('.msg').length");
  assert.equal(after > baseline, true, "new INBOX conversation appears without a page reload");
  assert.equal(await page.eval("window.__liveMarker === " + JSON.stringify(marker)), true, "the page was not reloaded");
  assert.equal(await page.eval("window.__mailToast === true"), true, "an in-app new-mail notification was shown");
  assert.equal(await page.eval("!document.getElementById('app').hidden"), true, "the webmail stayed signed in");
  await page.screenshot(path.join(artifacts, "go-live-mail.png"));
  await page.eval("window.__toastObserver.disconnect()");

  console.log("  New mail appeared in the open inbox and a notification was shown.");
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("\nGo live mail smoke passed: self-delivery reaches INBOX, UI updates in place and notifies.");
} catch (error) {
  console.error("GO LIVE MAIL SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "go-live-mail-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
