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

const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-e2e-"));
const dataFile = path.join(tempDir, "db.json");
const app = await createApp({ dataFile, holdMs: 4000, undoTtlMs: 15000 });
const address = await app.listen(0);
const base = `http://127.0.0.1:${address.port}`;
let browser;
let page;
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
const equal = (actual, expected, message) => {
  try {
    assert.equal(actual, expected, message);
    console.log(`  ✓ ${message}`);
  } catch (error) {
    failures += 1;
    console.error(`  ✗ ${message} (expected ${expected}, got ${actual})`);
    throw error;
  }
};
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitForApp() {
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);
  // The product deliberately holds the branded splash for five seconds after
  // an interactive sign-in. Wait for it to finish before driving the mailbox.
  await page.waitFor(() => document.getElementById("bootSplash")?.hidden !== false, 9000);
  await sleep(450);
}

async function openComposeBySubject(subject) {
  const id = await page.eval(`(() => {
    const sections = [...document.querySelectorAll('.compose')];
    const match = sections.find((section) => section.querySelector('[data-compose-field="subject"]')?.value === ${JSON.stringify(subject)});
    return match ? match.getAttribute('data-compose-id') : null;
  })()`);
  assert.ok(id, `Compose window with subject ${subject} should exist`);
  return id;
}

async function setComposeBody(composeSel, text) {
  const plain = await page.eval(`Boolean(document.querySelector(${JSON.stringify(composeSel + ' [data-compose-action="mode"][data-mode="plain"]')}))`);
  if (plain) {
    await page.click(`${composeSel} [data-compose-action="mode"][data-mode="plain"]`);
    await sleep(150);
  }
  await page.setValue(`${composeSel} [data-compose-field="body"]`, text);
}

async function composeBodyText(composeSel) {
  return page.eval(`(() => {
    const section = document.querySelector(${JSON.stringify(composeSel)});
    const textarea = section?.querySelector('[data-compose-field="body"]');
    if (textarea) return textarea.value;
    return section?.querySelector('[data-compose-editor]')?.innerText || "";
  })()`);
}

async function clickByText(selector, text) {
  await page.eval(`(() => {
    const node = [...document.querySelectorAll(${JSON.stringify(selector)})].find((element) => element.textContent.includes(${JSON.stringify(text)}));
    if (!node) throw new Error(${JSON.stringify("No " + selector + " containing " + text)});
    node.click();
  })()`);
}

try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
  page = await browser.newPage(base + "/");
  await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => Boolean(document.getElementById("loginScreen")), 10000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 10000);
  await sleep(150);

  console.log("\n[1] Two-factor sign-in and initial mailbox");
  check(await page.eval("!document.getElementById('loginScreen').hidden"), "login screen is shown");
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  check(await page.eval("!document.getElementById('twoFactorForm').hidden"), "password sign-in opens the two-factor challenge");
  check(await page.eval("!!document.querySelector('#passkeyPanel .passkey-art')"), "passkey sign-in option is offered");
  check(await page.eval("document.querySelectorAll('#otpInputs input').length === 6"), "authenticator code uses six OTP boxes");
  check(await page.eval("!document.getElementById('twoFactorDemo').hidden && document.getElementById('twoFactorDemoCode').textContent === '123456'"), "demo verification code is available");
  await page.screenshot(path.join(artifacts, "00-two-factor.png"));
  await page.click("#fillDemoCode");
  equal(await page.eval("[...document.querySelectorAll('#otpInputs input')].map(input => input.value).join('')"), "123456", "demo code fills every OTP box");
  await page.click("#twoFactorSubmit");
  await waitForApp();
  equal(await page.eval("document.querySelectorAll('.msg').length"), 12, "12 inbox conversations are listed");
  equal(await page.eval("[...document.querySelectorAll('.msg-thread-count')].map(e=>e.textContent).join(',')"), "3,3,3,3,2", "threaded conversations show message counts");
  check(await page.eval("document.querySelectorAll('.compose-tab').length === 1"), "the server-restored seed draft is in the compose dock");
  check(await page.eval("document.querySelector('.compose-tab')?.textContent.includes('Offsite logistics')"), "restored draft keeps its subject");
  await page.screenshot(path.join(artifacts, "01-inbox.png"));

  console.log("\n[2] Threaded reader collapse/expand");
  await page.click(".msg[data-thread-id]");
  await sleep(350);
  equal(await page.eval("document.querySelectorAll('.thread-msg').length"), 3, "three messages render in the thread");
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 1, "latest message is expanded by default");
  check(await page.eval("document.querySelector('.reader-subject h2')?.textContent.includes('Q4 roadmap')"), "thread subject is shown without Re: noise");
  await page.click("#readerCollapseAll");
  await sleep(200);
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 0, "Collapse all collapses every message");
  await page.click(".thread-msg-toggle");
  await sleep(200);
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 1, "a collapsed message can be expanded in place");
  await page.screenshot(path.join(artifacts, "02-thread.png"));

  console.log("\n[3] Instant search with operators");
  await page.click("#searchInput");
  await page.setValue("#searchInput", "from:elena has:attachment");
  await sleep(350);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 1, "from: + has:attachment returns one conversation");
  check(await page.eval("document.getElementById('searchSummary').textContent.includes('All mail')"), "summary reports all-mail search scope");
  await page.screenshot(path.join(artifacts, "03-search.png"));
  await page.setValue("#searchInput", "in:archive has:attachment");
  await sleep(300);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 2, "in:archive has:attachment returns two conversations");
  await page.setValue("#searchInput", "filename:invoice");
  await sleep(300);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 1, "filename: searches attachment names");
  await page.setValue("#searchInput", "before:2000-01-01");
  await sleep(300);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 0, "before: filters out every modern message");
  await page.click("#searchClearInline");
  await sleep(350);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 12, "clearing search restores the inbox");

  console.log("\n[4] Multi-select and bulk archive/undo");
  await page.click(".msg:nth-of-type(1) .row-check");
  await page.click(".msg:nth-of-type(2) .row-check");
  await sleep(250);
  equal(await page.eval("document.querySelectorAll('[data-thread-check]:checked').length"), 2, "two conversations are checked");
  check(await page.eval("!document.getElementById('bulkBar').hidden"), "bulk action bar appears");
  check(await page.eval("document.getElementById('bulkCount').textContent.includes('2 conversations')"), "bulk count is announced");
  await page.screenshot(path.join(artifacts, "04-bulk.png"));
  await page.click("[data-bulk-action='archive']");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 10, "archive removes the selected conversations from Inbox");
  check(await page.eval("document.getElementById('toast').textContent.includes('Undo')"), "undo action is offered");
  await page.click(".toast-action");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 12, "undo restores the archived conversations");

  console.log("\n[4b] Bulk mark and move");
  await page.click(".msg:nth-of-type(1) .row-check");
  await page.click(".msg:nth-of-type(2) .row-check");
  const unreadThreadsBefore = await page.eval("document.querySelectorAll('.msg.is-unread').length");
  await page.click("[data-bulk-action='markRead']");
  await sleep(800);
  check(await page.eval("document.querySelectorAll('.msg.is-unread').length") < unreadThreadsBefore, "bulk mark read clears unread conversations");
  await page.click(".msg:nth-of-type(1) .row-check");
  await page.click(".msg:nth-of-type(2) .row-check");
  await page.click("[data-bulk-move='open']");
  await sleep(200);
  check(await page.eval("!document.getElementById('bulkMoveMenu').hidden"), "bulk move menu opens");
  await page.click("[data-bulk-move-folder='archive']");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 10, "bulk move removes conversations from Inbox");
  check(await page.eval("document.getElementById('toast').textContent.includes('Moved to Archive')"), "bulk move reports the destination");
  await page.click(".toast-action");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 12, "undo restores bulk-moved conversations");

  console.log("\n[5] Keyboard-first navigation");
  await page.eval("document.activeElement.blur()");
  await page.pressKey("j");
  await sleep(300);
  check(await page.eval("!document.getElementById('reader').classList.contains('is-empty')"), "J opens the next conversation");
  const unreadBefore = await page.eval("document.querySelectorAll('.msg.is-unread').length");
  await page.pressKey("u");
  await sleep(550);
  equal(await page.eval("document.querySelectorAll('.msg.is-unread').length"), unreadBefore + 1, "U marks the conversation unread");
  await page.pressKey("e");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 11, "E archives the open conversation");
  await page.click(".toast-action");
  await sleep(900);
  await page.eval("document.activeElement.blur()");
  await page.pressKey("j");
  await sleep(250);
  await page.pressKey("x");
  await sleep(250);
  check(await page.eval("!document.getElementById('bulkBar').hidden"), "X selects the open conversation");
  await page.click("[data-bulk-action='clear']");
  await sleep(200);
  await page.pressKey("r");
  await sleep(400);
  check(await page.eval("[...document.querySelectorAll('.compose [data-compose-field=\"subject\"]')].at(-1)?.value.startsWith('Re:')"), "R opens a reply with a Re: subject");
  await page.eval("window.confirm = () => true");
  const replyId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  await page.click(`[data-compose-id="${replyId}"] [data-compose-action="discard"]`);
  await sleep(600);
  await page.pressKey("?");
  await sleep(250);
  check(await page.eval("!document.getElementById('helpOverlay').hidden"), "? opens the shortcut reference");
  await page.screenshot(path.join(artifacts, "04b-shortcuts.png"));
  await page.pressKey("Escape");
  await sleep(200);
  await page.pressKey("#");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 11, "# deletes the open conversation");
  check(await page.eval("document.getElementById('toast').textContent.includes('Undo')"), "delete can be undone");
  await page.click(".toast-action");
  await sleep(900);

  console.log("\n[6] Compose autosave survives reload");
  await page.click("#cmdCompose");
  await sleep(300);
  const composeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const composeSel = `[data-compose-id="${composeId}"]`;
  await page.setValue(`${composeSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${composeSel} [data-compose-field="subject"]`, "E2E draft");
  await setComposeBody(composeSel, "This draft must survive a page reload.");
  await page.click(`${composeSel} [data-compose-action="options"]`);
  await sleep(250);
  await page.setValue(`${composeSel} [data-compose-field="fromEmail"]`, "product@a8m.example");
  await sleep(1400);
  check(await page.eval(`document.querySelector('${composeSel} [data-compose-field="fromEmail"]').value === 'product@a8m.example'`), "From selector is wired to a sendable alias");
  check(await page.eval(`document.querySelector('${composeSel} [data-compose-identity]').textContent.includes('Product Team')`), "compose header shows the selected identity");
  check(await page.eval(`document.querySelector('${composeSel} [data-compose-status]').textContent.includes('server')`), "compose window reports a server save");
  await page.screenshot(path.join(artifacts, "05-compose.png"));
  await page.send("Page.reload");
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".compose").length >= 2, 12000);
  await sleep(700);
  const restoredSubject = await page.eval("[...document.querySelectorAll('.compose [data-compose-field=\"subject\"]')].map(e=>e.value)");
  check(restoredSubject.includes("E2E draft"), "compose session and content are restored after reload");
  const restoredComposeId = await page.eval("[...document.querySelectorAll('.compose')].find(e=>e.querySelector('[data-compose-field=\"subject\"]')?.value==='E2E draft')?.getAttribute('data-compose-id')");
  const restoredBody = await composeBodyText(`[data-compose-id="${restoredComposeId}"]`);
  equal(restoredBody, "This draft must survive a page reload.", "restored draft keeps its body");
  const restoredFrom = await page.eval("[...document.querySelectorAll('.compose')].find(e=>e.querySelector('[data-compose-field=\"subject\"]')?.value==='E2E draft')?.querySelector('[data-compose-field=\"fromEmail\"]')?.value");
  equal(restoredFrom, "product@a8m.example", "restored draft keeps its From identity");
  const restoredBadge = await page.eval("[...document.querySelectorAll('.compose')].find(e=>e.querySelector('[data-compose-field=\"subject\"]')?.value==='E2E draft')?.querySelector('[data-compose-identity]')?.textContent");
  check((restoredBadge || "").includes("Product Team"), "restored compose shows the alias badge");

  console.log("\n[7] Undo send and committed delivery");
  const restoredId = await openComposeBySubject("E2E draft");
  await page.click(`[data-compose-id="${restoredId}"] [data-compose-action="send"]`);
  await page.waitFor(() => !document.getElementById("pendingChip").hidden, 4000);
  check(await page.eval("document.getElementById('toast').textContent.includes('Undo')"), "send offers an undo window");
  check(await page.eval("document.getElementById('pendingText').textContent.includes('Sending')"), "status bar shows the queued send");
  await page.screenshot(path.join(artifacts, "06-undo-send.png"));
  await page.click("#pendingChip");
  await sleep(250);
  check(await page.eval("!document.getElementById('statusPop').hidden"), "pending chip opens the send queue");
  check(await page.eval("document.getElementById('statusPopBody').textContent.includes('Undo')"), "queue panel offers undo");
  await page.screenshot(path.join(artifacts, "06b-queue.png"));
  await page.click("#statusPopClose");
  await sleep(150);
  await page.click(".toast-action");
  await page.waitFor(() => document.getElementById("pendingChip").hidden, 5000);
  await sleep(700);
  check(await page.eval("!!document.querySelector('.compose') && [...document.querySelectorAll('.compose')].some(e=>e.querySelector('[data-compose-field=\"subject\"]')?.value==='E2E draft')"), "undo send restores the draft into compose");
  const restoredAfterUndo = await openComposeBySubject("E2E draft");
  await page.click(`[data-compose-id="${restoredAfterUndo}"] [data-compose-action="send"]`);
  await sleep(5500);
  const delivered = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>({sent:d.messages.filter(m=>m.folder==='sent'&&m.subject==='E2E draft').length,pending:d.pendingSends.length,used:d.outbound.used,fromEmail:d.messages.find(m=>m.folder==='sent'&&m.subject==='E2E draft')?.from.email}))");
  equal(delivered.sent, 1, "message is committed to Sent after the hold");
  equal(delivered.pending, 0, "pending queue is empty after commit");
  equal(delivered.used, 38, "outbound usage increments after delivery");
  equal(delivered.fromEmail, "product@a8m.example", "the chosen From alias is recorded on the sent message");
  await page.screenshot(path.join(artifacts, "07-sent.png"));
  await page.click("#quotaButtonStatus");
  await sleep(250);
  check(await page.eval("document.getElementById('statusPopBody').textContent.includes('Alt8Mail storage')"), "quota popover names the live storage source");
  check(await page.eval("!document.getElementById('statusQuotaLabel').textContent.includes('15.0')"), "quota label uses human-readable units");
  await page.click("#statusPopClose");
  await page.click("#outboundChip");
  await sleep(250);
  check(await page.eval("document.getElementById('statusPopBody').textContent.includes('postfwd')"), "outbound popover names the postfwd enforcement");
  await page.click("#statusPopClose");
  await page.click("[data-folder='sent']");
  await sleep(600);
  check(await page.eval("[...document.querySelectorAll('.msg-subject')].some(e=>e.textContent.includes('E2E draft'))"), "sent message appears in the Sent folder after commit");
  await page.click("[data-folder='inbox']");
  await sleep(500);

  console.log("\n[8] Sieve rules, vacation and audit log");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='rules']");
  await sleep(400);
  equal(await page.eval("document.querySelectorAll('.rule-card').length"), 3, "three seeded Sieve rules are listed");
  await page.click("#addRuleBtn");
  await page.setValue("#ruleName", "VIP customer mail");
  await page.setValue("#ruleConditions [data-condition-value]", "jordan.blake");
  await page.eval("(() => { const s=document.getElementById('ruleAction'); s.value='star'; s.dispatchEvent(new Event('change',{bubbles:true})); })()");
  await page.click("#saveRuleBtn");
  await sleep(900);
  equal(await page.eval("document.querySelectorAll('.rule-card').length"), 4, "new Sieve rule appears in the list");
  const sieve = await page.eval("fetch('/api/sieve').then(r=>r.json())");
  check((sieve.script || "").includes("VIP customer mail"), "generated Sieve script contains the new rule");
  await page.screenshot(path.join(artifacts, "08-rules.png"));

  console.log("\n[8b] Automatic replies");
  await page.click("[data-settings-tab='autoreply']");
  await sleep(400);
  check(await page.eval("document.getElementById('autoReplyStatus').textContent.includes('off')"), "automatic replies start switched off");
  await page.click("#autoReplyToggle");
  await sleep(200);
  equal(await page.eval("document.getElementById('autoReplyToggle').getAttribute('aria-checked')"), "true", "automatic replies switch turns on");
  await page.setValue("#autoReplyInternalMessage", "I am away until Monday; for urgent product questions contact Elena.");
  await page.setValue("#autoReplyExternalMessage", "Thanks for your message. I am away until Monday and will reply on my return.");
  check(await page.eval("document.getElementById('autoReplyPreview').textContent.includes('Inside Alt8Mail')"), "auto-reply preview updates while typing");
  await page.eval("document.getElementById('saveAutoReplyBtn').scrollIntoView({block:'center'})");
  await sleep(150);
  await page.click("#saveAutoReplyBtn");
  await sleep(900);
  check(await page.eval("document.getElementById('toast').textContent.includes('Automatic replies saved')"), "automatic replies save through Sieve");
  const vacation = await page.eval("fetch('/api/sieve').then(r=>r.json()).then(d=>d.vacation)");
  equal(vacation.enabled, true, "server has automatic replies enabled");
  check(vacation.internal.message.includes("away until Monday"), "server stores the inside-organization message");
  await page.screenshot(path.join(artifacts, "08b-automatic-replies.png"));

  console.log("\n[8c] Language, time format and time zone");
  await page.click("[data-settings-tab='regional']");
  await sleep(400);
  await page.click("[data-time-format='24h']");
  await sleep(450);
  await page.setValue("#timeZoneSelect", "Europe/Berlin");
  await sleep(450);
  await page.setValue("#languageSelect", "en-GB");
  await sleep(450);
  await page.setValue("#dateFormatSelect", "dmy");
  await sleep(500);
  check(await page.eval("/Europe\\/Berlin/i.test(document.getElementById('regionalPreview').textContent)"), "regional preview renders the chosen time zone");
  check(await page.eval("document.getElementById('regionalPreview').textContent.includes('Work hours')"), "regional preview shows working hours");
  const prefs = await page.eval("fetch('/api/preferences').then(r=>r.json()).then(d=>d.preferences)");
  equal(prefs.timeZone, "Europe/Berlin", "time zone is persisted to the server");
  equal(prefs.timeFormat, "24h", "time format is persisted to the server");
  equal(prefs.language, "en-GB", "language is persisted to the server");
  await page.screenshot(path.join(artifacts, "08c-regional.png"));

  await page.click("[data-settings-tab='audit']");
  await sleep(1000);
  check(await page.eval("document.querySelectorAll('#auditTableBody tr').length > 0"), "audit log lists entries");
  check(await page.eval("document.getElementById('auditTableBody').textContent.includes('Rules updated')"), "rule changes are audited");
  check(await page.eval("document.getElementById('auditTableBody').textContent.includes('preferences.updated')"), "regional preference changes are audited");
  await page.screenshot(path.join(artifacts, "09-audit.png"));
  await page.click("[data-settings-tab='account']");
  await sleep(350);
  equal(await page.eval("document.querySelectorAll('.alias-row').length"), 4, "account panel lists all send-as identities");
  check(await page.eval("document.querySelector('.alias-row.is-disabled')?.textContent.includes('support@a8m.example')"), "non-delegated shared mailbox is read-only");
  await page.click("[data-settings-tab='security']");
  await sleep(350);
  check(await page.eval("document.getElementById('twoFactorStatus').textContent.includes('on')"), "security panel shows two-factor as enabled");
  equal(await page.eval("document.querySelectorAll('#twoFactorMethods .security-method').length"), 3, "passkey, authenticator and recovery methods are listed");
  await page.click("#viewRecoveryCodes");
  await sleep(400);
  check(await page.eval("!document.getElementById('recoveryOverlay').hidden"), "recovery-code dialog opens");
  equal(await page.eval("document.querySelectorAll('#recoveryCodeList code').length"), 4, "four recovery codes are shown");
  await page.screenshot(path.join(artifacts, "09b-recovery-codes.png"));
  await page.click("#recoveryClose");
  await sleep(200);
  equal(await page.eval("document.getElementById('outboundUsedValue').textContent"), "38", "security panel shows live outbound usage");

  console.log("\n[9] Sender trust panel");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(400);
  await page.click("[data-folder='spam']");
  await sleep(350);
  await page.click(".msg:nth-of-type(1)");
  await sleep(400);
  check(await page.eval("!!document.querySelector('.external-banner.is-danger')"), "failed external authentication raises a red banner");
  check(await page.eval("document.querySelectorAll('.auth-chip.fail').length >= 2"), "SPF/DKIM/DMARC failures are visible");
  await page.screenshot(path.join(artifacts, "10-trust.png"));
  await page.click("[data-trust-sender]");
  await sleep(900);
  check(await page.eval("!document.querySelector('.external-banner')"), "marking the sender trusted removes the warning");
  const trusted = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.user.trustedSenders.includes('account-verify@a8m-security.example'))");
  check(trusted, "sender trust is persisted by the server");

  console.log("\n[9b] Calendar and directory");
  await page.click(".rail-btn[data-task='calendar']");
  await sleep(450);
  equal(await page.eval("document.querySelectorAll('.cal-cell').length"), 42, "calendar month renders a full six-week grid");
  check(await page.eval("document.querySelectorAll('.agenda-event, .cal-empty').length > 0"), "agenda renders selected-day events");
  await page.screenshot(path.join(artifacts, "15-calendar.png"));
  await page.click(".rail-btn[data-task='people']");
  await sleep(400);
  equal(await page.eval("document.querySelectorAll('.contact').length"), 8, "directory lists the eight seeded contacts");
  await page.click(".contact");
  await sleep(250);
  check(await page.eval("document.querySelector('.person-hero h2')?.textContent.length > 0"), "person detail renders");
  await page.screenshot(path.join(artifacts, "16-people.png"));

  console.log("\n[9c] Attachment preview and Outlook-style pop-out");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(300);
  await page.click("[data-folder='inbox']");
  await sleep(350);
  await clickByText(".msg", "Q4 roadmap");
  await sleep(450);
  await page.click("#threadExpandAll");
  await sleep(250);
  check(await page.eval("!!document.querySelector('[data-attachment=\"Q4-roadmap-v3.pdf\"]')"), "PDF attachment is listed with preview metadata");
  await page.click('[data-attachment="Q4-roadmap-v3.pdf"]');
  await sleep(400);
  check(await page.eval("!document.getElementById('previewOverlay').hidden"), "clicking an attachment opens the preview dialog");
  check(await page.eval("document.querySelector('.preview-pdf')?.getAttribute('src').includes('Q4-roadmap-v3.pdf')"), "PDF preview renders in the secure viewer iframe");
  await page.screenshot(path.join(artifacts, "17-attachment-preview.png"));
  await page.click("#previewClose");
  await sleep(250);
  check(await page.eval("document.getElementById('previewOverlay').hidden"), "preview dialog closes cleanly");

  await page.eval(`(() => {
    window.__a8mOriginalOpen = window.open;
    window.__a8mOpened = null;
    window.open = (url, name, features) => { window.__a8mOpened = { url, name, features }; return null; };
    const row = [...document.querySelectorAll('.msg')].find((element) => element.textContent.includes('Q4 roadmap'));
    row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, cancelable: true }));
  })()`);
  await sleep(250);
  check(await page.eval("window.__a8mOpened && window.__a8mOpened.url.includes('thread=t1') && window.__a8mOpened.url.includes('popout=1')"), "double-click opens the thread in a new window URL");
  const popout = await browser.newPage(base + "/?thread=t1&popout=1");
  await popout.waitFor(() => document.readyState === "complete", 10000);
  await popout.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".thread-msg").length > 0, 12000);
  await sleep(400);
  check(await popout.eval("document.body.classList.contains('popout')"), "pop-out window uses the focused reader layout");
  check(await popout.eval("document.querySelector('.reader-subject h2')?.textContent.includes('Q4 roadmap')"), "pop-out window opens the requested conversation");
  await popout.screenshot(path.join(artifacts, "17b-popout.png"));
  await popout.close();
  await page.eval("window.open = window.__a8mOriginalOpen");

  console.log("\n[9d] Reading-pane flag, spam and report controls");
  await clickByText(".msg", "expense report");
  await sleep(450);
  check(await page.eval("document.getElementById('readerFlagBtn').getAttribute('aria-pressed') === 'false'"), "follow-up flag starts clear");
  await page.click("#readerFlagBtn");
  await sleep(800);
  equal(await page.eval("document.getElementById('readerFlagBtn').getAttribute('aria-pressed')"), "true", "reader flag button marks the conversation");
  check(await page.eval("document.getElementById('toast').textContent.includes('Flagged for follow-up')"), "flagging reports a follow-up toast");
  check(await page.eval("!!document.querySelector('.msg-flag')"), "flagged conversation shows a flag in the list");
  await page.click("#readerSpamBtn");
  await sleep(950);
  check(await page.eval("document.getElementById('toast').textContent.includes('Marked as spam')"), "Mark as spam moves the thread out of Inbox");
  const spamMove = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.messages.find(m=>m.subject.includes('expense report')).folder)");
  equal(spamMove, "spam", "server files the message in Junk");
  await page.click("[data-folder='spam']");
  await sleep(450);
  await clickByText(".msg", "expense report");
  await sleep(450);
  equal(await page.eval("document.getElementById('readerSpamLabel').textContent"), "Not junk", "spam messages offer a Not junk control");
  await page.screenshot(path.join(artifacts, "18-spam-controls.png"));
  await page.click("#readerSpamBtn");
  await sleep(950);
  const restoredFolder = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.messages.find(m=>m.subject.includes('expense report')).folder)");
  equal(restoredFolder, "inbox", "Not junk returns the conversation to Inbox");
  await page.click("[data-folder='inbox']");
  await sleep(400);
  await clickByText(".msg", "expense report");
  await sleep(400);
  await page.click("#readerReportBtn");
  await sleep(350);
  check(await page.eval("!document.getElementById('reportOverlay').hidden"), "Report opens the security reporting dialog");
  equal(await page.eval("document.querySelectorAll('#reportOverlay .report-reason').length"), 4, "report dialog offers four reasons");
  await page.eval("document.querySelector('input[name=\"reportReason\"][value=\"phishing\"]').click()");
  await page.setValue("#reportNote", "Lookalike domain asking for payroll credentials.");
  await page.screenshot(path.join(artifacts, "18b-report-dialog.png"));
  await page.click("#reportSubmit");
  await sleep(1100);
  check(await page.eval("document.getElementById('toast').textContent.includes('Reported to Alt8Mail Security')"), "report confirmation is announced");
  const reportState = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>{const m=d.messages.find(m=>m.subject.includes('expense report')); return {folder:m.folder, reason:m.spamReport?.reason, flagged:m.flagged};})");
  equal(reportState.folder, "spam", "reported message is quarantined in Junk");
  equal(reportState.reason, "phishing", "report reason is stored with the message");
  equal(reportState.flagged, true, "reported message is flagged for follow-up");
  await page.click("[data-folder='inbox']");
  await sleep(400);

  console.log("\n[9e] Tasks workspace");
  await page.click(".rail-btn[data-task='tasks']");
  await page.waitFor(() => !document.getElementById("tasksView").hidden, 5000);
  await sleep(450);
  equal(await page.eval("document.querySelectorAll('.task-row').length"), 8, "seed tasks are listed in the workspace");
  equal(await page.eval("document.querySelectorAll('.task-stat').length"), 4, "task dashboard shows four summary tiles");
  check(await page.eval("document.getElementById('taskDetail').textContent.includes('Prepare') || document.getElementById('taskDetail').textContent.includes('Task')"), "task detail pane renders the selected task");
  await page.screenshot(path.join(artifacts, "19-tasks.png"));
  await page.click("#newTaskBtn");
  await sleep(300);
  await page.setValue("#taskTitleInput", "Confirm E2E launch plan");
  await page.setValue("#taskNotesInput", "Verify the enterprise workspace before the pilot.");
  const dueDate = await page.eval("new Date(Date.now() + 3*86400000).toISOString().slice(0,10)");
  await page.setValue("#taskDueInput", dueDate);
  await page.setValue("#taskDueTimeInput", "16:00");
  await page.eval("(() => { const s=document.getElementById('taskPriorityInput'); s.value='urgent'; s.dispatchEvent(new Event('change',{bubbles:true})); })()");
  await page.setValue("#taskTagsInput", "Launch, Enterprise");
  await page.click("#saveTaskBtn");
  await sleep(800);
  equal(await page.eval("document.querySelectorAll('.task-row').length"), 9, "new task appears in the task list");
  check(await page.eval("document.getElementById('toast').textContent.includes('Task created')"), "task creation is confirmed");
  check(await page.eval("document.getElementById('taskDetail').textContent.includes('Confirm E2E launch plan')"), "new task is selected in the detail pane");
  await page.click(".task-row .task-check");
  await sleep(700);
  check(await page.eval("document.getElementById('toast').textContent.includes('Task completed') || document.getElementById('toast').textContent.includes('Task reopened')"), "task checkbox completes the task");
  await page.screenshot(path.join(artifacts, "19b-task-detail.png"));

  console.log("\n[9f] Files, storage connectors and preview");
  await page.click(".rail-btn[data-task='files']");
  await page.waitFor(() => !document.getElementById("filesView").hidden, 5000);
  await sleep(500);
  equal(await page.eval("document.querySelectorAll('.storage-card').length"), 5, "all five enterprise storage providers are listed");
  equal(await page.eval("document.querySelectorAll('.file-row').length"), 12, "seeded files are indexed across connected drives");
  await clickByText(".file-row", "Q4-roadmap-v3.pdf");
  await sleep(350);
  check(await page.eval("document.getElementById('fileDetail').textContent.includes('Q4-roadmap-v3.pdf')"), "file detail opens for the selected document");
  await page.click("#fileDetail [data-file-preview]");
  await sleep(350);
  check(await page.eval("document.querySelector('.preview-pdf')?.getAttribute('src').includes('Q4-roadmap-v3.pdf')"), "file PDF preview opens in the viewer");
  await page.click("#previewClose");
  await sleep(250);
  await page.click("#fileDetail [data-file-share]");
  await sleep(300);
  check(await page.eval("!document.getElementById('shareOverlay').hidden"), "file share dialog opens");
  await page.setValue("#shareAccess", "edit");
  await page.eval("(() => { const s=document.getElementById('shareExpiry'); s.value='14'; s.dispatchEvent(new Event('change',{bubbles:true})); })()");
  await page.click("#createShare");
  await sleep(700);
  check(await page.eval("document.getElementById('shareUrl').value.startsWith('https://files.a8m.example/s/')"), "share link is generated");
  await page.screenshot(path.join(artifacts, "20-file-share.png"));
  await page.click("#shareCancel");
  await sleep(250);

  await clickByText(".file-row", "offsite-team.svg");
  await sleep(350);
  await page.click("#fileDetail [data-file-preview]");
  await sleep(450);
  check(await page.eval("!!document.querySelector('.preview-image')"), "image files render an inline preview");
  check(await page.eval("document.getElementById('previewToolbar').hidden === false"), "image preview exposes zoom controls");
  await page.screenshot(path.join(artifacts, "20b-image-preview.png"));
  await page.click("#previewClose");
  await sleep(250);

  await page.click("#connectStorageBtn");
  await sleep(300);
  equal(await page.eval("document.querySelectorAll('#storageConnectList .storage-connect-row').length"), 5, "storage dialog lists every connector");
  await page.click("#storageConnectList [data-storage-connect='dropbox']");
  await sleep(1500);
  check(await page.eval("document.querySelector('#storageConnectList [data-provider=\"dropbox\"]')?.textContent.includes('Connected')"), "Dropbox connector reports connected");
  await page.click("#storageClose");
  await sleep(400);
  equal(await page.eval("document.querySelectorAll('.file-row').length"), 14, "connecting Dropbox adds its files to the workspace");
  check(await page.eval("document.querySelector('.storage-card')?.textContent.length > 0"), "storage cards show live usage");
  await page.screenshot(path.join(artifacts, "21-files.png"));

  console.log("\n[9g] Send later scheduling flow");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(350);
  await page.click("#cmdCompose");
  await sleep(350);
  const scheduleComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const scheduleSel = `[data-compose-id="${scheduleComposeId}"]`;
  await page.setValue(`${scheduleSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${scheduleSel} [data-compose-field="subject"]`, "Scheduled E2E note");
  await setComposeBody(scheduleSel, "This message should wait for the chosen time.");
  await page.click(`${scheduleSel} [data-compose-action="schedule"]`);
  await sleep(500);
  check(await page.eval("!document.getElementById('scheduleOverlay').hidden"), "Send later opens the scheduling workspace");
  check(await page.eval("document.querySelectorAll('#schedulePresets [data-schedule-preset]').length >= 4"), "scheduling offers Outlook-style presets");
  check(await page.eval("document.getElementById('scheduleInsight').textContent.includes('Leaves the outbox')"), "schedule preview explains the send time");
  await page.eval("document.querySelector('#schedulePresets [data-schedule-preset=\"custom\"]').click()");
  await sleep(250);
  const laterDate = await page.eval("new Date(Date.now() + 2*86400000).toISOString().slice(0,10)");
  await page.setValue("#scheduleDate", laterDate);
  await page.setValue("#scheduleTime", "09:30");
  await sleep(350);
  check(await page.eval("document.getElementById('scheduleInsight').textContent.includes('Central European') || document.getElementById('scheduleInsight').textContent.includes('Europe') || document.getElementById('scheduleZoneInfo').textContent.includes('Europe')"), "schedule preview uses the selected time zone");
  await page.screenshot(path.join(artifacts, "22-schedule-send.png"));
  check(await page.eval("document.getElementById('scheduleConfirm').disabled === false"), "future custom time enables Schedule send");
  await page.click("#scheduleConfirm");
  await sleep(1000);
  check(await page.eval("!document.getElementById('scheduledChip').hidden"), "scheduled status chip appears after scheduling");
  const queued = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.pendingSends.filter(s=>s.scheduled).map(s=>({subject:s.subject, scheduled:s.scheduled, canCancel:s.canCancel})))");
  equal(queued.length, 1, "server queue holds the scheduled message");
  equal(queued[0].subject, "Scheduled E2E note", "scheduled queue carries the compose subject");
  await page.click("#scheduledChip");
  await sleep(300);
  check(await page.eval("!document.getElementById('statusPop').hidden && document.querySelectorAll('.pending-item.scheduled').length === 1"), "scheduled popover lists the pending message");
  await page.screenshot(path.join(artifacts, "22b-scheduled-queue.png"));
  await page.click("[data-cancel-send]");
  await sleep(1000);
  check(await page.eval("document.getElementById('scheduledChip').hidden"), "canceling a scheduled send clears the status chip");
  check(await page.eval("document.getElementById('toast').textContent.includes('Scheduled send cancelled')"), "cancel confirmation restores the message to drafts");
  if (await page.eval("!document.getElementById('statusPop').hidden")) { await page.click("#statusPopClose"); }
  const restoredScheduleId = await openComposeBySubject("Scheduled E2E note");
  await page.eval("window.confirm = () => true");
  await page.click(`[data-compose-id="${restoredScheduleId}"] [data-compose-action="discard"]`);
  await sleep(600);

  console.log("\n[9h] Scheduled center: edit, send now, cancel");
  await page.click("#cmdCompose");
  await sleep(350);
  const manageComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const manageSel = `[data-compose-id="${manageComposeId}"]`;
  await page.setValue(`${manageSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${manageSel} [data-compose-field="subject"]`, "Managed scheduled note");
  await setComposeBody(manageSel, "This message will be managed from the scheduled center.");
  await page.click(`${manageSel} [data-compose-action="schedule"]`);
  await sleep(450);
  await page.eval("document.querySelector('#schedulePresets [data-schedule-preset=\"custom\"]').click()");
  await sleep(200);
  const manageDate = await page.eval("new Date(Date.now() + 3*86400000).toISOString().slice(0,10)");
  await page.setValue("#scheduleDate", manageDate);
  await page.setValue("#scheduleTime", "14:15");
  await sleep(300);
  await page.click("#scheduleConfirm");
  await sleep(1000);
  check(await page.eval("!document.getElementById('scheduledChip').hidden"), "second scheduled message is queued");
  check(await page.eval("!!document.querySelector('.folder-item[data-open-scheduled]')"), "Scheduled is available from the folder pane");
  await page.click("#scheduledChip");
  await sleep(300);
  check(await page.eval("document.querySelectorAll('#statusPopBody [data-scheduled-send-now]').length === 1"), "scheduled popover offers Send now");
  check(await page.eval("document.querySelectorAll('#statusPopBody [data-scheduled-edit]').length === 1"), "scheduled popover offers Edit");
  check(await page.eval("document.querySelectorAll('#statusPopBody [data-cancel-send]').length === 1"), "scheduled popover offers Cancel");
  await page.click("#statusPopBody [data-open-scheduled]");
  await page.waitFor(() => !document.getElementById("scheduledView").hidden && document.querySelectorAll(".scheduled-card").length === 1, 6000);
  check(await page.eval("document.getElementById('scheduledMeta').textContent.includes('waiting')"), "scheduled center shows the waiting queue");
  equal(await page.eval("document.querySelectorAll('#scheduledSummary .scheduled-stat').length"), 4, "scheduled center shows four summary tiles");
  check(await page.eval("document.getElementById('scheduledList').textContent.includes('Managed scheduled note')"), "scheduled center lists the scheduled message");
  await page.screenshot(path.join(artifacts, "24-scheduled-center.png"));

  await page.click(".scheduled-card [data-scheduled-edit]");
  await page.waitFor(() => !document.getElementById("scheduledEditOverlay").hidden, 4000);
  equal(await page.eval("document.getElementById('scheduledEditTo').value"), "elena.vasquez@a8m.example", "edit dialog loads the recipients");
  check(await page.eval("document.getElementById('scheduledEditOriginal').textContent.includes('Currently scheduled')"), "edit dialog shows the original send time");
  await page.setValue("#scheduledEditSubject", "Managed scheduled note — edited");
  await page.setValue("#scheduledEditBody", "Updated body, ready to send from the schedule center.");
  const adjustedDate = await page.eval("new Date(Date.now() + 4*86400000).toISOString().slice(0,10)");
  await page.setValue("#scheduledEditDate", adjustedDate);
  await page.setValue("#scheduledEditTime", "10:45");
  await sleep(300);
  check(await page.eval("document.getElementById('scheduledEditInsight').textContent.includes('Leaves the outbox')"), "edit dialog previews the new send time");
  await page.screenshot(path.join(artifacts, "24b-scheduled-edit.png"));
  await page.click("#scheduledEditSave");
  await page.waitFor(() => document.getElementById("scheduledEditOverlay").hidden, 5000);
  await sleep(500);
  check(await page.eval("document.getElementById('toast').textContent.includes('Scheduled message updated')"), "edited schedule is saved");
  check(await page.eval("document.getElementById('scheduledList').textContent.includes('Managed scheduled note — edited')"), "scheduled center reflects the edited subject");
  const edited = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>{const s=d.pendingSends.find(x=>x.subject==='Managed scheduled note — edited'); return s ? {body:s.bodyText, sendAt:s.sendAt} : null;})");
  check(edited && edited.body.includes("Updated body"), "edited body is persisted on the server");

  await page.click(".scheduled-card [data-scheduled-send-now]");
  await page.waitFor(() => !document.getElementById("sendNowOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('sendNowSummary').textContent.includes('Managed scheduled note — edited')"), "send-now confirmation shows the message");
  await page.screenshot(path.join(artifacts, "24c-send-now.png"));
  await page.click("#sendNowConfirm");
  await page.waitFor(() => document.getElementById("toast").textContent.includes('sent now'), 8000);
  await sleep(700);
  check(await page.eval("document.getElementById('scheduledChip').hidden"), "sending now clears the scheduled status chip");
  const afterSend = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>({scheduled:d.pendingSends.filter(s=>s.scheduled).length, sent:d.messages.some(m=>m.folder==='sent'&&m.subject==='Managed scheduled note — edited')}))");
  equal(afterSend.scheduled, 0, "server schedule queue is empty after Send now");
  equal(afterSend.sent, true, "Send now delivers the edited message to Sent");
  equal(await page.eval("document.querySelectorAll('.scheduled-card').length"), 0, "scheduled center shows its empty state");
  await page.screenshot(path.join(artifacts, "24d-scheduled-empty.png"));
  if (await page.eval("!document.getElementById('toast').hidden && document.querySelector('.toast-action')?.textContent === 'View sent'")) {
    await page.click(".toast-action");
    await page.waitFor(() => document.getElementById('listTitle').textContent === "Sent", 5000);
    check(await page.eval("[...document.querySelectorAll('.msg-subject')].some((element) => element.textContent.includes('Managed scheduled note — edited'))"), "View sent opens the delivered message in Sent");
  } else {
    check(false, "Send now offers a View sent action");
  }

  console.log("\n[10] PWA and theming");
  const swReady = await page.eval("navigator.serviceWorker.ready.then(r=>({scope:r.scope,state:r.active?.state}))", { timeout: 15000 });
  equal(swReady.state, "activated", "service worker is activated");
  const manifest = await page.send("Page.getAppManifest");
  check(manifest.errors.length === 0, "manifest parses without errors");
  const installability = await page.send("Page.getInstallabilityErrors");
  check(Array.isArray(installability.installabilityErrors) && installability.installabilityErrors.length === 0, "PWA installability checks pass");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(250);
  await page.click("#themeBtn");
  await sleep(450);
  check(await page.eval("document.documentElement.getAttribute('data-theme') === 'dark'"), "theme toggle applies dark mode");
  await page.screenshot(path.join(artifacts, "11-dark.png"));

  console.log("\n[11] Mobile layout");
  const mobile = await browser.newPage("about:blank");
  await mobile.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  await mobile.goto(base + "/");
  await mobile.waitFor(() => document.readyState === "complete", 10000);
  if (await mobile.eval("!document.getElementById('loginScreen').hidden")) {
    await mobile.click("#loginSubmit");
    await mobile.waitFor(() => !document.getElementById("twoFactorForm").hidden || !document.getElementById("app").hidden, 10000);
    if (await mobile.eval("!document.getElementById('twoFactorForm').hidden")) {
      await mobile.click("#usePasskeyBtn");
      await mobile.waitFor(() => !document.getElementById("passkeyOverlay").hidden, 4000);
      check(await mobile.eval("document.getElementById('passkeyPromptStatus').textContent.includes('Waiting')"), "mobile passkey prompt waits for device verification");
    }
  }
  await mobile.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);
  await sleep(700);
  check(await mobile.eval("getComputedStyle(document.querySelector('.mobile-tabs')).display !== 'none'"), "mobile tab bar is visible");
  check(await mobile.eval("getComputedStyle(document.getElementById('composeFab')).display !== 'none'"), "compose FAB remains available above the draft dock");
  await mobile.screenshot(path.join(artifacts, "12-mobile.png"));
  await mobile.click("#composeFab");
  await sleep(400);
  check(await mobile.eval("document.body.classList.contains('compose-visible')"), "mobile compose opens full-screen");
  await mobile.screenshot(path.join(artifacts, "12b-mobile-compose.png"));
  const mobileComposeId = await mobile.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  await mobile.click(`[data-compose-id="${mobileComposeId}"] [data-compose-action='close']`);
  await sleep(600);
  await mobile.click(".msg[data-thread-id]");
  await sleep(400);
  check(await mobile.eval("document.body.classList.contains('reader-open')"), "opening a conversation switches to the mobile reader");
  await mobile.screenshot(path.join(artifacts, "13-mobile-reader.png"));
  await mobile.click("#readerBack");
  await sleep(250);
  check(await mobile.eval("!document.body.classList.contains('reader-open')"), "mobile back returns to the list");
  await mobile.click("#folderToggle");
  await sleep(250);
  check(await mobile.eval("document.body.classList.contains('drawer-open')"), "mobile folder drawer opens");
  check(await mobile.eval("!!document.querySelector('.folder-item[data-open-scheduled]')"), "mobile folder drawer exposes Scheduled");
  await mobile.screenshot(path.join(artifacts, "14-mobile-drawer.png"));
  await mobile.close();

  console.log("\n[11b] Passkey sign-in");
  await page.click("#railLogout");
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 5000);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  await page.click("#usePasskeyBtn");
  await page.waitFor(() => !document.getElementById("passkeyOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('passkeyPromptStatus').textContent.includes('Waiting')"), "passkey prompt shows the device-waiting state");
  await page.screenshot(path.join(artifacts, "23-passkey-prompt.png"));
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);
  check(await page.eval("document.getElementById('loginScreen').hidden"), "passkey verification signs in and restores the workspace");

  await sleep(400);
  const runtimeErrors = page.errors.filter((error) =>
    !/401/.test(error) && !/Failed to load resource/.test(error) && !/ERR_INTERNET_DISCONNECTED/.test(error)
  );
  equal(runtimeErrors.length, 0, "no unexpected browser runtime errors");

  console.log(`\nE2E complete: ${failures} failure${failures === 1 ? "" : "s"}`);
  console.log(`Screenshots: ${artifacts}`);
} catch (error) {
  failures += 1;
  console.error("\nE2E FAILED:", error.message);
  if (page) {
    try {
      console.error("Page errors:", page.errors.filter((item) => !item.includes("401")));
      await page.screenshot(path.join(artifacts, "failure.png"));
    } catch {}
  }
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  await app.close();
  await fs.rm(tempDir, { recursive: true, force: true });
}
