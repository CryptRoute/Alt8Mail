import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const base = process.env.MERIDIAN_UI_URL || "https://130-94-46-22.sslip.io/";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);
const getPreferences = () => page.eval("fetch('/api/preferences').then(r=>r.json()).then(d=>d.preferences)");
const assertPref = async (key, expected, message) => {
  const prefs = await getPreferences();
  if (Array.isArray(expected) || (expected && typeof expected === "object")) {
    assert.deepEqual(prefs[key], expected, `${message} (${key}=${JSON.stringify(prefs[key])})`);
  } else {
    assert.equal(prefs[key], expected, `${message} (${key}=${JSON.stringify(prefs[key])})`);
  }
};
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(600);
  await page.click("#loginSubmit");
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  while (await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').length")) {
    await page.eval("document.querySelector('.compose [data-compose-action=\"close\"]').click()");
    await sleep(300);
  }
  await page.click(".rail-btn[data-task='settings']");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='about']");
  await page.click("#resetDemo");
  await sleep(700);

  // Appearance
  await page.click("[data-settings-tab='appearance']");
  await page.click("[data-set-theme='dark']");
  await sleep(350);
  await page.click("[data-set-accent='teal']");
  await sleep(350);
  await page.click("[data-set-density='compact']");
  await sleep(350);
  await assertPref("theme", "dark", "theme saved in DB");
  await assertPref("accent", "teal", "accent saved in DB");
  await assertPref("density", "compact", "density saved in DB");

  // Mail & reading
  await page.click("[data-settings-tab='mail']");
  await page.click("[data-pref='markReadOnOpen']");
  await sleep(300);
  await page.click("[data-server-pref='autoAdvance']");
  await sleep(300);
  await page.setValue("#defaultPrioritySelect", "high");
  await sleep(300);
  await page.click("[data-pref='defaultReceipt']");
  await sleep(300);
  await page.setValue("#undoSendSelect", "10");
  await sleep(300);
  await page.click("[data-server-pref='signatureEnabled']");
  await sleep(300);
  await page.setValue("#signatureInput", "Avery Chen\nMeridian staging\nReal signature");
  await page.click("#saveSignatureBtn");
  await sleep(500);
  await assertPref("markReadOnOpen", false, "read-on-open saved in DB");
  await assertPref("autoAdvance", true, "auto-advance saved in DB");
  await assertPref("defaultPriority", "high", "default priority saved in DB");
  await assertPref("defaultReceipt", true, "default receipt saved in DB");
  await assertPref("undoSendSeconds", 10, "undo window saved in DB");
  await assertPref("signatureEnabled", false, "signature toggle saved in DB");
  await assertPref("signature", "Avery Chen\nMeridian staging\nReal signature", "signature saved in DB");

  // Notifications (desktop permission prompts are intentionally avoided in headless)
  await page.click("[data-settings-tab='notifications']");
  await page.click("[data-pref='notifySound']");
  await sleep(350);
  await page.click("[data-pref='notifyCalendar']");
  await sleep(350);
  await assertPref("notifySound", true, "sound preference saved in DB");
  await assertPref("notifyCalendar", false, "calendar reminder preference saved in DB");

  // Language & time
  await page.click("[data-settings-tab='regional']");
  await page.setValue("#languageSelect", "en-GB");
  await sleep(300);
  await page.click("[data-time-format='24h']");
  await sleep(300);
  await page.setValue("#dateFormatSelect", "dmy");
  await sleep(300);
  await page.setValue("#timeZoneSelect", "Europe/London");
  await sleep(300);
  await page.setValue("#weekStartSelect", "sunday");
  await sleep(300);
  await page.setValue("#workStartInput", "08:30");
  await sleep(300);
  await page.setValue("#workEndInput", "18:30");
  await sleep(300);
  await page.click("[data-work-day='6']");
  await sleep(400);
  await assertPref("language", "en-GB", "language saved in DB");
  await assertPref("timeFormat", "24h", "time format saved in DB");
  await assertPref("dateFormat", "dmy", "date format saved in DB");
  await assertPref("timeZone", "Europe/London", "time zone saved in DB");
  await assertPref("weekStart", "sunday", "week start saved in DB");
  await assertPref("workStart", "08:30", "work start saved in DB");
  await assertPref("workEnd", "18:30", "work end saved in DB");
  await assertPref("workDays", [1, 2, 3, 4, 5, 6], "work days saved in DB");

  // Rules and automatic replies render and save through the Go API.
  await page.click("[data-settings-tab='rules']");
  await page.waitFor(() => document.getElementById("sieveScript").textContent.includes("Generated by Meridian"), 8000);
  assert.ok(await page.eval("document.getElementById('sieveScript').textContent.includes('Generated by Meridian')"), "Sieve script renders from the server");
  await page.click("[data-settings-tab='autoreply']");
  await page.click("#autoReplyToggle");
  await page.setValue("#autoReplyInternalMessage", "I am away from the office.");
  await sleep(200);
  await page.click("#saveAutoReplyBtn");
  await sleep(500);
  assert.equal((await page.eval("fetch('/api/sieve').then(r=>r.json()).then(d=>d.vacation.enabled)")), true, "automatic replies persisted in DB");
  await page.click("#autoReplyToggle");
  await sleep(200);
  await page.click("#saveAutoReplyBtn");
  await sleep(450);

  // Audit
  await page.click("[data-settings-tab='audit']");
  await sleep(600);
  assert.ok(await page.eval("document.querySelectorAll('#auditTableBody tr').length >= 1"), "audit log renders real entries");

  // Security: external warnings and 2FA controls.
  await page.click("[data-settings-tab='security']");
  await sleep(500);
  assert.equal(await page.eval("!document.getElementById('setupTotpBtn').hidden"), true, "authenticator setup is available");
  assert.equal(await page.eval("!document.getElementById('addPasskeyBtn').hidden"), true, "passkey setup is available");
  assert.equal(await page.eval("!document.getElementById('enrollmentPolicyToggle').closest('.settings-block').hidden"), true, "enrollment policy control is available");
  const beforeWarning = await page.eval("document.getElementById('externalWarningToggle').getAttribute('aria-checked')");
  await page.click("#externalWarningToggle");
  await sleep(400);
  await assertPref("externalWarnings", beforeWarning !== "true", "external sender warning saved in DB");
  await page.click("#externalWarningToggle");
  await sleep(300);

  // Account: real identity, aliases and sessions.
  await page.click("[data-settings-tab='account']");
  await page.waitFor(() => document.querySelectorAll("#accountSessions .session-row").length >= 1, 5000);
  assert.equal(await page.eval("document.getElementById('accountEmail').textContent"), "avery.chen@meridian.test", "account email is real");
  assert.ok(await page.eval("document.querySelectorAll('#accountSessions .session-row').length >= 1"), "real sign-in sessions render");
  assert.ok(await page.eval("document.querySelectorAll('#aliasList .alias-row').length >= 1"), "database aliases render");
  await page.screenshot(path.join(artifacts, "go-settings-account.png"));

  // Reset preferences through the UI (server endpoint).
  await page.click("[data-settings-tab='about']");
  await page.click("#resetDemo");
  await sleep(700);
  const reset = await getPreferences();
  assert.equal(reset.theme, "system", "reset restores theme in DB");
  assert.equal(reset.accent, "blue", "reset restores accent in DB");
  assert.equal(reset.language, "en-US", "reset restores language in DB");
  assert.equal(reset.notifySound, false, "reset restores notification defaults in DB");

  await page.click("[data-settings-tab='security']");
  await page.screenshot(path.join(artifacts, "go-settings-security.png"));

  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Go settings smoke passed: appearance, mail, notifications, regional, rules, automatic replies, audit, security and account are server-backed.");
} catch (error) {
  console.error("GO SETTINGS SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "go-settings-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
