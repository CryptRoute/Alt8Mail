import assert from "node:assert/strict";
import { launchBrowser } from "./cdp.mjs";

const base = process.env.ALT8MAIL_UI_URL || "https://130-94-46-22.sslip.io/";
const email = process.env.ALT8MAIL_UI_DEMO_EMAIL || "avery.chen@a8m.example";
const password = process.env.ALT8MAIL_UI_DEMO_PASSWORD || "";
const label = `Demo alias password verification ${Date.now()}`;
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
if (!password) throw new Error("ALT8MAIL_UI_DEMO_PASSWORD is required");

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
const page = await browser.newPage(base);
let csrf = "";
let createdId = "";

const revokeCreated = async () => {
  if (!createdId || !csrf) return;
  try {
    await page.eval(`fetch('/api/security/app-passwords/${encodeURIComponent(createdId)}',{method:'DELETE',credentials:'same-origin',headers:{'X-CSRF-Token':${JSON.stringify(csrf)}}})`);
    createdId = "";
  } catch {}
};

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 15000);
  await page.setValue("#loginEmail", email);
  await page.setValue("#loginPassword", password);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(700);
  csrf = decodeURIComponent(await page.eval("document.cookie.match(/(?:^|; )a8m_system_csrf=([^;]+)/)?.[1] || ''"));

  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click('[data-settings-tab="security"]');
  await sleep(500);
  await page.setValue("#appPasswordLabel", label);
  await page.setValue("#appPasswordCurrent", password);
  await page.click("#createAppPasswordBtn");
  await page.waitFor(() => !document.getElementById("appPasswordToken").hidden, 10000);
  assert.equal(await page.eval("!document.getElementById('appPasswordToken').hidden"), true, "current demo password is accepted for app-password creation");

  const list = await page.eval("fetch('/api/security/app-passwords',{credentials:'same-origin'}).then(r=>r.json())");
  const created = (list.appPasswords || []).find((entry) => entry.label === label);
  assert.ok(created, "created app password is visible in the credential list");
  createdId = created.id;
  await revokeCreated();
  const after = await page.eval("fetch('/api/security/app-passwords',{credentials:'same-origin'}).then(r=>r.json())");
  assert.equal((after.appPasswords || []).some((entry) => entry.label === label && entry.status === "active"), false, "test credential is revoked");
  console.log("Demo-alias app password smoke passed: mapped-account sign-in accepted the demo password for app-password creation; token issued and test credential revoked.");
} catch (error) {
  console.error("DEMO-ALIAS APP PASSWORD SMOKE FAILED:", error.message);
  await revokeCreated();
  throw error;
} finally {
  await revokeCreated();
  await browser.close();
}
