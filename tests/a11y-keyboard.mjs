import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createApp } from "../server/app.mjs";
import { launchBrowser } from "../tools/cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(__dirname, "..");
const artifacts = path.join(root, "artifacts");
await fs.mkdir(artifacts, { recursive: true });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-a11y-keyboard-"));
const app = await createApp({ dataFile: path.join(tempDir, "db.json"), holdMs: 500, undoTtlMs: 8000, allowDevOutbox: true });
const address = await app.listen(0);
const base = `http://127.0.0.1:${address.port}`;
let browser;
let failures = 0;

const check = (condition, message) => {
  try {
    assert.ok(condition, message);
    console.log(`  ✓ ${message}`);
  } catch (error) {
    failures += 1;
    console.error(`  ✗ ${message}`);
    throw error;
  }
};

async function activeInfo(page) {
  return page.eval(`(() => {
    const el = document.activeElement;
    if (!el) return { tag: "", id: "", label: "", field: "", recipient: "", editable: false };
    return {
      tag: el.tagName || "",
      id: el.id || "",
      label: el.getAttribute("aria-label") || "",
      field: el.getAttribute("data-compose-field") || "",
      recipient: el.getAttribute("data-recipient-input") || "",
      editable: el.getAttribute("contenteditable") === "true",
      textbox: el.getAttribute("role") === "textbox"
    };
  })()`);
}

async function tabTo(page, condition, description, max = 90) {
  for (let index = 0; index < max; index += 1) {
    await page.pressKey("Tab");
    if (await page.eval(`Boolean(${condition})`)) return activeInfo(page);
  }
  throw new Error(`Tab order did not reach ${description}`);
}

async function focusRing(page) {
  return page.eval(`(() => {
    const el = document.activeElement;
    if (!el) return { visible: false, outline: "none", width: "0px", shadow: "none" };
    const style = getComputedStyle(el);
    const visible = (style.outlineStyle !== "none" && parseFloat(style.outlineWidth) >= 2)
      || (style.boxShadow && style.boxShadow !== "none");
    return { visible, outline: style.outlineStyle, width: style.outlineWidth, shadow: style.boxShadow };
  })()`);
}

async function waitForComposeCount(page, count) {
  await page.waitFor(`document.querySelectorAll('.compose[data-compose-id]').length > ${count}`, 8000);
  await sleep(250);
}

async function typeRecipient(page, email) {
  await page.typeText(email);
  await page.pressKey("Enter");
  await sleep(120);
}

async function fillAndSendWithKeyboard(page, { recipient, subject, body }) {
  const before = await page.eval("document.querySelectorAll('.compose[data-compose-id]').length");
  await page.pressKey("c");
  await waitForComposeCount(page, before);
  const to = await activeInfo(page);
  if (!(to.recipient === "to")) {
    await tabTo(page, `document.activeElement?.getAttribute('data-recipient-input') === 'to'`, "compose To field");
  }
  await typeRecipient(page, recipient);
  await tabTo(page, `document.activeElement?.getAttribute('data-compose-field') === 'subject'`, "compose subject");
  await page.typeText(subject);
  await tabTo(page, `document.activeElement?.matches('[data-compose-editor], [data-compose-field="body"]')`, "compose body");
  const ring = await focusRing(page);
  check(ring.visible, "compose body shows a visible keyboard focus indicator");
  await page.typeText(body);
  await sendWithKeyboard(page);
}

async function sendWithKeyboard(page) {
  await page.pressKey("Enter", { ctrl: true });
  await sleep(650);
  if (!(await page.eval("document.getElementById('spellcheckOverlay')?.hidden !== false"))) {
    await tabTo(page, `document.activeElement?.id === 'spellcheckSendAnyway'`, "spell-check Send anyway", 40);
    await page.pressKey("Enter");
    await sleep(350);
  }
  if (!(await page.eval("document.getElementById('confirmOverlay')?.hidden !== false"))) {
    await page.pressKey("Enter");
    await sleep(250);
  }
}

async function subjectSent(page, subject) {
  return page.eval(`fetch('/api/bootstrap').then((response) => response.json()).then((data) => ({
    pending: data.pendingSends.some((item) => item.subject === ${JSON.stringify(subject)}),
    sent: data.messages.some((message) => message.folder === 'sent' && message.subject === ${JSON.stringify(subject)})
  }))`);
}

async function waitForSubject(page, subject, timeout = 12000) {
  const started = Date.now();
  let last = { pending: false, sent: false };
  while (Date.now() - started < timeout) {
    last = await subjectSent(page, subject);
    if (last.pending || last.sent) return last;
    await sleep(250);
  }
  return last;
}

try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
  const page = await browser.newPage(base + "/");
  await page.waitFor(() => document.readyState === "complete", 12000);
  await page.waitFor(() => Boolean(document.getElementById("loginScreen")) && !document.getElementById("loginScreen").hidden, 10000);
  await sleep(200);

  console.log("\n[1] Keyboard-only sign-in");
  await tabTo(page, `document.activeElement?.id === 'loginEmail'`, "login email");
  const emailRing = await focusRing(page);
  check(emailRing.visible, "login email shows a visible keyboard focus indicator");
  await page.typeText("avery.chen@a8m.example");
  await tabTo(page, `document.activeElement?.id === 'loginPassword'`, "login password");
  await page.typeText("a8m-demo");
  await tabTo(page, `document.activeElement?.id === 'loginSubmit'`, "login submit");
  await page.pressKey("Enter");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  const otpFocus = await activeInfo(page);
  check(otpFocus.id === "otp0", "two-factor challenge focuses the first code box");
  for (const digit of "123456") await page.pressKey(digit);
  check(await page.eval("[...document.querySelectorAll('#otpInputs input')].map((input) => input.value).join('') === '123456'"), "keyboard enters the six-digit code");
  await tabTo(page, `document.activeElement?.id === 'twoFactorSubmit'`, "two-factor submit");
  const otpRing = await focusRing(page);
  check(otpRing.visible, "two-factor submit shows a visible keyboard focus indicator");
  await page.pressKey("Enter");
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 18000);
  await page.waitFor(() => document.getElementById("bootSplash")?.hidden !== false, 12000);
  await sleep(500);
  check(await page.eval("!document.getElementById('app').hidden"), "keyboard sign-in reaches the mailbox");

  console.log("\n[2] Keyboard-only compose and send");
  const subject = `Keyboard gate ${Date.now()}`;
  await page.eval("document.activeElement?.blur?.()");
  await fillAndSendWithKeyboard(page, {
    recipient: "priya.shah@a8m.example",
    subject,
    body: "Keyboard-only send verification completed."
  });
  const delivered = await waitForSubject(page, subject);
  if (!(delivered.pending || delivered.sent)) {
    console.error(await page.eval(`(() => ({
      composes: document.querySelectorAll('.compose[data-compose-id]').length,
      status: [...document.querySelectorAll('[data-compose-status]')].map((node) => node.textContent),
      toast: document.getElementById('toast')?.textContent || '',
      spellcheck: document.getElementById('spellcheckOverlay')?.hidden === false,
      dialogs: [...document.querySelectorAll('[role="dialog"]')].filter((node) => node.getClientRects().length).map((node) => node.getAttribute('aria-label') || node.textContent.slice(0, 80))
    }))()`));
  }
  check(delivered.pending || delivered.sent, "Ctrl+Enter sends the keyboard-composed message to the outbox/Sent");
  check(await page.eval("document.getElementById('toast').textContent.length > 0"), "send reports a visible status message");

  console.log("\n[3] Settings navigation with the keyboard");
  await page.eval("document.activeElement?.blur?.()");
  await tabTo(page, `document.activeElement?.id === 'settingsBtn'`, "settings button", 120);
  const settingsRing = await focusRing(page);
  check(settingsRing.visible, "settings button shows a visible keyboard focus indicator");
  await page.pressKey("Enter");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 8000);
  await sleep(250);
  check(await page.eval("document.activeElement?.closest('#settingsView') !== null"), "opening settings moves focus into the panel");
  await tabTo(page, `document.activeElement?.id === 'settingsDone'`, "settings Done button", 120);
  await page.pressKey("Enter");
  await page.waitFor(() => document.getElementById("settingsView").hidden, 8000);
  check(await page.eval("document.activeElement?.closest('#settingsView') === null"), "closing settings returns focus outside the hidden panel");

  console.log("\n[4] RTL at 360px");
  await page.eval(`import('/i18n.mjs').then((module) => module.setLocale('ar'))`);
  await page.waitFor(() => document.documentElement.dir === "rtl" && document.documentElement.lang === "ar", 8000);
  await page.send("Emulation.setDeviceMetricsOverride", { width: 360, height: 800, deviceScaleFactor: 1, mobile: false });
  await sleep(400);
  check(await page.eval("document.documentElement.dir === 'rtl'"), "Arabic locale sets document direction to RTL");
  const overflow = await page.eval("Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth");
  check(overflow <= 1, `RTL layout has no horizontal overflow at 360px (${overflow}px)`);
  const rtlSubject = `RTL keyboard gate ${Date.now()}`;
  await page.eval("document.activeElement?.blur?.()");
  const composeBefore = await page.eval("document.querySelectorAll('.compose[data-compose-id]').length");
  await page.pressKey("c");
  await waitForComposeCount(page, composeBefore);
  const toVisible = await page.eval(`(() => {
    const field = document.querySelector('.compose[data-compose-id]:not([hidden]) [data-recipient-input="to"]');
    if (!field) return false;
    const rect = field.getBoundingClientRect();
    return rect.width > 20 && rect.left >= -1 && rect.right <= innerWidth + 1;
  })()`);
  check(toVisible, "RTL compose keeps the To field inside the 360px viewport");
  await typeRecipient(page, "priya.shah@a8m.example");
  await tabTo(page, `document.activeElement?.getAttribute('data-compose-field') === 'subject'`, "RTL compose subject");
  await page.typeText(rtlSubject);
  await tabTo(page, `document.activeElement?.matches('[data-compose-editor], [data-compose-field="body"]')`, "RTL compose body");
  await page.typeText("RTL keyboard send verification completed.");
  await page.screenshot(path.join(artifacts, "a11y-rtl-compose.png"));
  await sendWithKeyboard(page);
  const rtlDelivered = await waitForSubject(page, rtlSubject);
  check(rtlDelivered.pending || rtlDelivered.sent, "RTL Ctrl+Enter sends from the 360px layout");

  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await page.screenshot(path.join(artifacts, "a11y-keyboard-complete.png"));
  console.log("\nKeyboard accessibility gate complete: 0 failures");
} catch (error) {
  console.error(error.stack || error.message || String(error));
  process.exitCode = 1;
} finally {
  try { await browser?.close(); } catch {}
  try { await app.close(); } catch {}
  await fs.rm(tempDir, { recursive: true, force: true });
  if (failures > 0) process.exitCode = 1;
}
