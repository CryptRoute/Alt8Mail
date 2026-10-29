import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const base = process.env.MERIDIAN_UI_URL || "https://130-94-46-22.sslip.io/";
const email = process.env.MERIDIAN_UI_EMAIL || "avery.chen@meridian.test";
const password = process.env.MERIDIAN_UI_PASSWORD || "";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const stamp = Date.now();

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
const page = await browser.newPage(base);
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(600);
  await page.setValue("#loginEmail", email);
  await page.setValue("#loginPassword", password);
  await page.click("#loginSubmit");
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await sleep(300);
  // Close drafts restored from the server so they do not cover settings controls.
  while (await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').length")) {
    await page.eval("document.querySelector('.compose [data-compose-action=\"close\"]').click()");
    await sleep(150);
  }

  // Editable calendar event: create, edit and remove.
  await page.click(".rail-btn[data-task='calendar']");
  await page.waitFor(() => !document.getElementById("calendarView").hidden, 5000);
  await page.click("#calNew");
  await page.setValue("#eventName", `UI smoke event ${stamp}`);
  await page.setValue("#eventWhere", "QA room");
  await page.click("#eventSave");
  await page.waitFor(`[...document.querySelectorAll('[data-edit-event]')].some((node) => node.textContent.includes('UI smoke event ${stamp}'))`, 8000);
  const eventId = await page.eval(`(() => { const node = [...document.querySelectorAll('[data-edit-event]')].find((item) => item.textContent.includes('UI smoke event ${stamp}')); return node ? node.getAttribute('data-edit-event') : ''; })()`);
  assert.ok(eventId, "created event is rendered as editable");
  await page.click(`[data-edit-event="${eventId}"]`);
  assert.equal(await page.eval("document.getElementById('eventDialogTitle').textContent"), "Edit event", "event dialog switches to edit mode");
  await page.setValue("#eventName", `UI smoke event edited ${stamp}`);
  await page.click("#eventSave");
  await page.waitFor(`[...document.querySelectorAll('[data-edit-event]')].some((node) => node.textContent.includes('UI smoke event edited ${stamp}'))`, 8000);
  await page.eval(`(() => { const node = [...document.querySelectorAll('[data-edit-event]')].find((item) => item.textContent.includes('UI smoke event edited ${stamp}')); node.click(); })()`);
  await sleep(250);
  await page.eval("window.confirm = () => true");
  await page.click("#eventDelete");
  await page.waitFor(`![...document.querySelectorAll('[data-edit-event]')].some((node) => node.textContent.includes('UI smoke event edited ${stamp}'))`, 8000);

  // Personal contact create, edit and delete from People.
  await page.click(".rail-btn[data-task='people']");
  await page.waitFor(() => !document.getElementById("peopleView").hidden, 5000);
  const before = await page.eval("document.querySelectorAll('.contact').length");
  await page.click("#newContactBtn");
  await page.setValue("#contactName", `UI Contact ${stamp}`);
  await page.setValue("#contactEmailField", `ui-contact-${stamp}@example.test`);
  await page.setValue("#contactCompany", "Smoke Co");
  await page.setValue("#contactTitle", "QA");
  await page.click("#contactSave");
  await page.waitFor(`document.querySelectorAll('.contact').length === ${before + 1}`, 8000);
  const contactEmail = `ui-contact-${stamp}@example.test`;
  await page.click(`[data-edit-contact="${contactEmail}"]`);
  assert.equal(await page.eval("document.getElementById('contactDialogTitle').textContent"), "Edit contact", "contact dialog opens in edit mode");
  await page.setValue("#contactTitle", "Senior QA");
  await page.click("#contactSave");
  await page.waitFor(() => document.getElementById("contactOverlay").hidden, 8000);
  await page.click(`[data-delete-contact="${contactEmail}"]`);
  await page.waitFor(`document.querySelectorAll('.contact').length === ${before}`, 8000);

  // Profile editing and app-password controls.
  await page.click(".rail-btn[data-task='settings']");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='account']");
  await page.waitFor(() => Boolean(document.getElementById("profileName") && document.getElementById("profileName").value), 8000);
  const originalTitle = await page.eval("document.getElementById('profileTitle').value");
  await page.setValue("#profileTitle", "UI smoke title");
  await page.click("#saveProfileBtn");
  await sleep(900);
  assert.equal(await page.eval("document.getElementById('profileTitle').value"), "UI smoke title", "profile title was saved");
  await page.setValue("#profileTitle", originalTitle);
  await page.click("#saveProfileBtn");
  await sleep(900);

  await page.click("[data-settings-tab='security']");
  await page.waitFor(() => Boolean(document.getElementById("createAppPasswordBtn")), 5000);
  await page.setValue("#appPasswordLabel", `UI smoke ${stamp}`);
  await page.setValue("#appPasswordCurrent", password);
  await page.click("#createAppPasswordBtn");
  await page.waitFor(() => !document.getElementById("appPasswordToken").hidden, 8000);
  assert.equal(await page.eval("document.getElementById('appPasswordToken').hidden"), false, "app password token is shown once");
  assert.ok(await page.eval("document.querySelectorAll('[data-remove-app-password]').length >= 1"), "app password can be revoked");
  await page.eval("document.querySelector('[data-remove-app-password]').click()");
  await sleep(900);

  await page.screenshot(path.join(artifacts, "workspace-ui-smoke.png"));
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Workspace UI smoke passed: calendar create/edit/delete, contact create/edit/delete, profile edit, app password create/revoke.");
} catch (error) {
  console.error("WORKSPACE UI SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "workspace-ui-smoke-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
