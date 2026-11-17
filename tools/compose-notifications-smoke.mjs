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
const subject = `Compose close smoke ${Date.now()}`;

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden || !document.getElementById("app").hidden, 15000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 5000);
  assert.equal(await page.eval("document.getElementById('bootSplash').hidden === true"), true, "boot splash clears after session restore");
  if (await page.eval("!document.getElementById('loginScreen').hidden")) {
    await page.setValue("#loginEmail", email);
    await page.setValue("#loginPassword", password);
    await page.click("#loginSubmit");
  }
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(700);

  // Real notification center, not the former demo rows.
  assert.equal(await page.eval("document.getElementById('notifyPop').textContent.includes('Roadmap prep starts')"), false, "demo notification rows are absent");
  await page.click("#notifyBtn");
  await page.waitFor(() => !document.getElementById("notifyPop").hidden, 4000);
  const notificationRows = await page.eval("document.querySelectorAll('#notifyList .notify-item, #notifyList .notify-empty').length");
  assert.equal(notificationRows >= 1, true, "notification popover renders real or empty state");
  await page.click("#notifyViewAll");
  await page.waitFor(() => !document.getElementById("noticeLayer").hidden, 4000);
  assert.equal(await page.eval("document.querySelectorAll('#noticeBody .notice-center-item, #noticeBody .notice-empty').length >= 1"), true, "notification center renders a real list");
  assert.equal(await page.eval("document.querySelectorAll('[data-notice-filter]').length"), 4, "notification center filters are present");
  await page.click("#noticeClose");
  await page.waitFor(() => document.getElementById("noticeLayer").hidden, 3000);

  // Compose editor and attachment picker.
  await page.click("#cmdCompose");
  await page.waitFor(() => !document.querySelector(".compose")?.hidden, 5000);
  await sleep(350);
  assert.equal(await page.eval("document.querySelectorAll('.compose [data-compose-editor]').length"), 1, "rich HTML editor is present");
  assert.equal(await page.eval("document.querySelectorAll('.compose [data-compose-priority]').length"), 3, "priority is a three-way radio group");
  assert.equal(await page.eval("document.querySelector('.compose [data-compose-field=\"fromEmail\"]').value"), "avery.chen@a8m.test", "compose From is the real signed-in address");
  assert.equal(await page.eval("document.querySelectorAll('.compose .compose-editor-toolbar button').length >= 10"), true, "WYSIWYG toolbar is present");
  const aiEnabled = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>Boolean(d.ai && d.ai.enabled))");
  assert.equal(await page.eval("document.querySelectorAll('.compose [data-compose-action=\"ai\"]').length"), aiEnabled ? 1 : 0, "AI compose button follows the tenant assistant setting");
  await page.click('.compose [data-compose-action="attach"]');
  await page.waitFor(() => !document.getElementById("composeAttachOverlay").hidden, 3000);
  assert.equal(await page.eval("!!document.getElementById('composeAttachDrop')"), true, "attachment drag-and-drop surface is present");
  assert.equal(await page.eval("!!document.getElementById('composeAttachFileList')"), true, "Files picker surface is present");
  await page.click("#composeAttachClose");

  await page.setValue('.compose [data-compose-field="to"]', "jordan.lee@a8m.test");
  await page.setValue('.compose [data-compose-field="subject"]', subject);
  await page.eval(`(() => {
    const editor = document.querySelector('.compose [data-compose-editor]');
    editor.innerHTML = '<p><strong>Hello</strong> Team</p>';
    editor.dispatchEvent(new InputEvent('input', { bubbles: true }));
  })()`);
  await sleep(900);
  assert.equal(await page.eval("document.querySelector('.compose [data-compose-ai-panel]').hidden"), true, "AI panel stays closed until requested");
  await page.click('.compose [data-compose-action="close"]');
  await page.waitFor(() => document.querySelectorAll(".compose").length === 0, 10000);
  await sleep(900);

  // A deliberately closed draft must not reopen after a reload.
  await page.eval("setTimeout(() => location.reload(), 50)");
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 8000);
  await sleep(500);
  assert.equal(await page.eval(`Array.from(document.querySelectorAll('.compose [data-compose-title]')).some((node) => node.textContent === ${JSON.stringify(subject)})`), false, "closed draft does not reopen after reload");
  assert.equal(await page.eval("document.querySelectorAll('.compose').length"), 0, "no compose window is restored after deliberate close");

  // Settings surfaces.
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click('[data-settings-tab="compose"]');
  await sleep(250);
  assert.equal(await page.eval("document.getElementById('composeModeSelect').value"), "html", "HTML compose is the default");
  assert.equal(await page.eval("document.getElementById('composeWidthSelect').value"), "wide", "wide compose is the default");
  assert.equal(await page.eval("document.getElementById('composePositionSelect').value"), "center", "centered compose is the default");
  await page.click('[data-settings-tab="identities"]');
  await page.waitFor(() => document.querySelectorAll("#identityList .identity-card").length >= 3, 6000);
  assert.equal(await page.eval("document.querySelectorAll('#identityList .identity-card').length >= 3"), true, "real sending identities render");
  assert.equal(await page.eval("document.getElementById('identityList').textContent.includes('avery.chen@a8m.test')"), true, "primary identity is real");
  await page.click('[data-settings-tab="security"]');
  await sleep(300);
  assert.equal(await page.eval("!!document.getElementById('appPasswordExpiry')"), true, "app-password expiry control is present");
  assert.equal(await page.eval("!!document.getElementById('appPasswordForm')"), true, "app-password form is present");
  await page.click("#settingsDone");

  await page.screenshot(path.join(artifacts, "compose-notifications-smoke.png"));
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Compose/notifications smoke passed: live notification center, HTML editor, priority, attachments, identities, app-password controls and closed-draft persistence.");
} catch (error) {
  console.error("COMPOSE/NOTIFICATIONS SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "compose-notifications-smoke-failure.png")); } catch {}
  throw error;
} finally {
  try {
    const csrf = await page.eval("fetch('/api/bootstrap',{credentials:'same-origin'}).then(r=>r.json()).then(d=>d.csrf || '')");
    const drafts = await page.eval("fetch('/api/drafts',{credentials:'same-origin'}).then(r=>r.json()).catch(()=>({drafts:[]}))");
    const draft = (drafts.drafts || []).find((entry) => entry.subject === subject);
    if (draft && csrf) {
      await page.eval(`fetch('/api/drafts/${draft.id}',{method:'DELETE',credentials:'same-origin',headers:{'X-CSRF-Token':${JSON.stringify(csrf)}}})`);
    }
  } catch {}
  await browser.close();
}
