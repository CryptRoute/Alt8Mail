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
const app = await createApp({ dataFile, holdMs: 4000, undoTtlMs: 15000, allowDevOutbox: true, aiEnabled: true, aiDelayMs: 650 });
app.db.office = {
  enabled: true, ready: true, publicUrl: "https://office.a8m.example",
  status: "running", version: "9.4.0.1", secretConfigured: true
};
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
const DEMO_EMAIL = "avery.chen@a8m.example";
const DEMO_PASSWORD = "a8m-demo";
const DEMO_THREAD_ID = "N2Acmhe6TzJMY7c6L5yRfg";

async function fillDemoLogin() {
  await page.setValue("#loginEmail", DEMO_EMAIL);
  await page.setValue("#loginPassword", DEMO_PASSWORD);
}

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

async function addComposeRecipient(composeSel, field, email) {
  const inputSel = `${composeSel} [data-recipient-input="${field}"]`;
  await page.click(inputSel);
  await page.setValue(inputSel, email);
  await page.pressKey("Enter");
  await sleep(80);
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
  equal(await page.eval("document.title"), "Alt8Mail: Secure Email with AI built in - Sign-in", "login tab title matches the sign-in format");
  await fillDemoLogin();
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
  check(await page.eval("document.title.startsWith('(') && document.title.endsWith('Inbox — avery.chen@a8m.example - Alt8Mail')"), "inbox tab title shows folder, unread count and account");
  check(await page.eval(`(() => {
    const rows = [...document.querySelectorAll('.msg')];
    const unread = rows.find((row) => row.classList.contains('is-unread'));
    const read = rows.find((row) => !row.classList.contains('is-unread'));
    if (!unread || !read) return false;
    const weight = (row, selector) => getComputedStyle(row.querySelector(selector)).fontWeight;
    return weight(unread, '.msg-from') === '700'
      && weight(unread, '.msg-subject') === '650'
      && weight(read, '.msg-from') === '400'
      && weight(read, '.msg-subject') === '400';
  })()`), "read rows use normal weight while unread rows stay bold");
  equal(await page.eval("document.querySelectorAll('.msg').length"), 12, "12 inbox conversations are listed");
  equal(await page.eval("[...document.querySelectorAll('.msg-thread-count')].map(e=>e.textContent).join(',')"), "3,3,3,3,2", "threaded conversations show message counts");
  check(await page.eval("document.querySelectorAll('.compose-tab').length === 1"), "the server-restored seed draft is in the compose dock");
  check(await page.eval("document.querySelector('.compose-tab')?.textContent.includes('Offsite logistics')"), "restored draft keeps its subject");
  await page.waitFor(() => Boolean(document.querySelector(".catchup-card")), 5000);
  check(await page.eval("document.querySelector('.catchup-card')?.textContent.includes('While you were away')"), "inbox shows a dismissible catch-up card after time away");
  check(await page.eval("document.querySelector('.catchup-card')?.textContent.includes('conversations changed')"), "catch-up card reports changed conversations");
  check(await page.eval("Boolean(document.getElementById('catchUpBtn'))"), "inbox exposes a manual Catch me up action");
  await page.screenshot(path.join(artifacts, "01-inbox.png"));

  console.log("\n[1b] Connected account entry points");
  check(await page.eval("!document.getElementById('accountMenu').hidden"), "folder pane shows the mailbox switcher");
  check(await page.eval("document.getElementById('accountTriggerName')?.textContent === 'Avery Chen' && document.getElementById('accountTriggerEmail')?.textContent.includes('avery.chen@a8m.example')"), "mailbox switcher shows the active account name and address");
  check(await page.eval("!document.getElementById('accountTriggerUnread').hidden && Number(document.getElementById('accountTriggerUnreadCount').textContent) > 0"), "mailbox switcher shows an unread envelope counter");
  await page.click("#accountMenuBtn");
  await page.waitFor(() => !document.getElementById("accountPopover").hidden, 5000);
  check(await page.eval("document.getElementById('accountMenuBtn').getAttribute('aria-expanded') === 'true'"), "mailbox menu opens from the account card");
  equal(await page.eval("document.querySelectorAll('#accountMenuList [data-account-menu-id]').length"), 1, "mailbox menu lists the native mailbox");
  check(await page.eval("document.querySelector('#accountMenuList .account-row.is-selected .account-row-badge')?.textContent === 'Primary'"), "the active mailbox is marked as primary");
  check(await page.eval("document.querySelector('#accountMenuList .account-row-unread')?.textContent.trim() === document.getElementById('accountTriggerUnreadCount').textContent.trim()"), "mailbox rows show each account's unread envelope count");
  check(await page.eval("!document.getElementById('accountPopAddBtn').hidden"), "mailbox menu offers Add mail account");
  await page.screenshot(path.join(artifacts, "01b-mailbox-switcher.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  await page.click("#folderToggle");
  await page.waitFor(() => document.body.classList.contains("drawer-open"), 5000);
  if (await page.eval("document.getElementById('accountPopover').hidden")) {
    await page.click("#accountMenuBtn");
    await page.waitFor(() => !document.getElementById("accountPopover").hidden, 5000);
  }
  await page.screenshot(path.join(artifacts, "01b-mailbox-switcher-mobile.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  if (await page.eval("document.getElementById('accountPopover').hidden")) {
    await page.click("#accountMenuBtn");
    await page.waitFor(() => !document.getElementById("accountPopover").hidden, 5000);
  }
  await page.click("#accountPopAddBtn");
  await page.waitFor(() => Boolean(document.getElementById("accountDialogOverlay")), 5000);
  check(await page.eval("document.querySelector('#accountDialogTitle')?.textContent === 'Add mail account'"), "mailbox menu opens the add-account wizard");
  equal(await page.eval("document.querySelectorAll('[data-provider-tile]').length"), 12, "add-account wizard offers eleven branded providers plus other IMAP");
  equal(await page.eval("document.querySelectorAll('.mail-provider-tile img[src^=\"/icons/providers/\"]').length"), 12, "every provider tile renders its own logo");
  check(await page.eval("['Gmail','Outlook','Hotmail','Yahoo Mail','iCloud Mail','Other IMAP / SMTP'].every(name => [...document.querySelectorAll('.mail-provider-copy strong')].some(node => node.textContent === name))"), "provider gallery names Gmail, Outlook, Hotmail, Yahoo, iCloud and other IMAP");
  await page.screenshot(path.join(artifacts, "01b-add-account-providers.png"));
  await page.click("[data-provider-tile='gmail']");
  await page.waitFor(() => Boolean(document.querySelector("[data-gmail-password]")), 5000);
  check(await page.eval("document.querySelector('.mailbox-oauth-card')?.textContent.includes('Continue with Gmail')"), "Gmail opens the Google sign-in step");
  await page.screenshot(path.join(artifacts, "01b-add-account-oauth.png"));
  await page.click("[data-gmail-password]");
  await page.waitFor(() => Boolean(document.querySelector("#accountDialogForm")), 5000);
  check(await page.eval("(() => { const button = document.querySelector('[data-account-action=\"connect\"]'); return Boolean(button && button.disabled && button.querySelector('svg')); })()"), "Connect account starts disabled and carries an icon");
  check(await page.eval("Boolean(document.querySelector('[data-account-action=\"test\"] svg') && document.querySelector('[data-account-dialog-close] svg'))"), "Test connection and Cancel actions carry icons");
  await page.setValue("#accountEmailInput", "person@example.com");
  await page.setValue("#accountPassword", "app-password");
  await page.screenshot(path.join(artifacts, "01b-add-account-credentials.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  await page.screenshot(path.join(artifacts, "01b-add-account-credentials-mobile.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  check(await page.eval("document.querySelector('[data-account-action=\"connect\"]').disabled"), "Connect account stays disabled before a successful test");
  check(await page.eval("!document.querySelector('[data-account-action=\"test\"]').disabled"), "Test connection enables once the credentials are filled");
  await page.click("[data-account-action='test']");
  await page.waitFor(() => {
    const button = document.querySelector('[data-account-action="connect"]');
    return Boolean(button && !button.disabled);
  }, 5000);
  check(await page.eval("document.querySelector('.mailbox-success-stage')?.textContent.includes('Connection verified') && document.querySelector('.mailbox-success-stage')?.textContent.includes('person@example.com')"), "successful test unlocks Connect account and shows the verified mailbox");
  await page.screenshot(path.join(artifacts, "01b-add-account-wizard.png"));
  await page.click("[data-account-dialog-close]");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='accounts']");
  await page.waitFor(() => !document.querySelector("[data-settings-panel='accounts']").hidden, 5000);
  check(await page.eval("!document.getElementById('connectedAccountAdd').hidden && document.getElementById('connectedAccountAdd').textContent.includes('Add mail account')"), "Settings has a Connected accounts tab with an add-account action");
  check(await page.eval("document.getElementById('connectedAccountsList').textContent.includes('No connected accounts yet')"), "connected-account settings explains the empty state");
  await page.click("#connectedAccountAdd");
  await page.waitFor(() => Boolean(document.getElementById("accountDialogOverlay")), 5000);
  check(await page.eval("document.querySelector('#accountDialogTitle')?.textContent === 'Add mail account'"), "Settings action opens the add-account wizard");
  await page.click("[data-account-dialog-close]");
  await page.click("#settingsDone");
  await page.waitFor(() => document.getElementById("settingsView").hidden, 5000);
  await page.screenshot(path.join(artifacts, "01b-connected-accounts.png"));

  console.log("\n[2] Threaded reader collapse/expand");
  await page.click(".msg[data-thread-id]");
  await sleep(350);
  equal(await page.eval("document.querySelectorAll('.thread-msg').length"), 3, "three messages render in the thread");
  check(await page.eval("document.title.includes('Inbox · Q4 roadmap review — Thursday 10:00') && document.title.endsWith('— Alt8Mail')"), "open thread title shows the folder and subject");
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 1, "latest message is expanded by default");
  check(await page.eval("document.querySelector('.reader-subject h2')?.textContent.includes('Q4 roadmap')"), "thread subject is shown without Re: noise");
  check(await page.eval("Boolean(document.querySelector('[data-ai-commitments-chip]'))"), "reader exposes a visible What did I commit to? chip");
  await page.click("[data-ai-commitments-chip]");
  await page.waitFor(() => Boolean(document.querySelector("#readerAiCard .ai-commitment-item")), 5000);
  check(await page.eval("document.querySelector('#readerAiCard')?.textContent.includes('What you committed to')"), "AI commitments card summarises the owner's promises");
  check(await page.eval("document.querySelectorAll('#readerAiCard .ai-commitment-item').length === 1"), "commitments are linked to their source message");
  await page.click("#readerAiCard .ai-commitment-item");
  await sleep(700);
  check(await page.eval("Boolean(document.querySelector('.thread-msg.is-ai-commitment-target'))"), "clicking a commitment jumps to the source message");
  await page.click("[data-ai-reader-close]");
  check(await page.eval(`(() => {
    const body = document.querySelector('.thread-msg.is-expanded .email-body');
    const message = document.querySelector('.thread-msg.is-expanded');
    const doc = document.querySelector('.reader-doc');
    if (!body || !message || !doc) return false;
    return body.getBoundingClientRect().width >= message.getBoundingClientRect().width - 40
      && getComputedStyle(doc).maxWidth === '2000px';
  })()`), "message body fills the reading column up to the 2000px widescreen cap");
  await page.click("#readerCollapseAll");
  await sleep(200);
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 0, "Collapse all collapses every message");
  await page.click(".thread-msg-toggle");
  await sleep(200);
  equal(await page.eval("document.querySelectorAll('.thread-msg.is-expanded').length"), 1, "a collapsed message can be expanded in place");
  await page.screenshot(path.join(artifacts, "02-thread.png"));

  console.log("\n[2b] Inline selection translation");
  await page.eval(`(() => {
    const host = document.querySelector('.thread-msg .email-body') || document.getElementById('readerContent');
    const fixture = document.createElement('p');
    fixture.id = 'translation-fixture';
    fixture.textContent = 'Hola, gracias por su ayuda con el proyecto.';
    host.appendChild(fixture);
    const range = document.createRange();
    range.selectNodeContents(fixture);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    const rect = fixture.getBoundingClientRect();
    fixture.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: rect.left + 12, clientY: rect.top + 8 }));
  })()`);
  await page.waitFor(() => Boolean(document.querySelector('[data-message-command="translateSelection"]')), 3000);
  check(true, "a non-English selection offers Translate in the context menu");
  await page.click('[data-message-command="translateSelection"]');
  await page.waitFor(() => {
    const card = document.getElementById("selectionTranslation");
    return Boolean(card && !card.hidden && card.textContent.includes("Translated selection"));
  }, 5000);
  check(await page.eval("document.getElementById('selectionTranslationLanguage').textContent.includes('English')"), "selection translation is rendered in the user's configured language");
  await page.screenshot(path.join(artifacts, "02c-selection-translation.png"));
  await page.click("#selectionTranslationClose");
  check(await page.eval("document.getElementById('selectionTranslation').hidden"), "selection translation closes from its close control");
  await page.eval("document.getElementById('translation-fixture')?.remove(); window.getSelection()?.removeAllRanges()");

  console.log("\n[2c] mailto links open compose");
  await page.eval(`(() => {
    const host = document.querySelector('.thread-msg .email-body') || document.getElementById('readerContent');
    const link = document.createElement('a');
    link.id = 'mailto-fixture';
    link.href = 'mailto:recipient@example.com?subject=Hello%20from%20mailto&body=Line%20one';
    link.textContent = 'email us';
    host.appendChild(link);
  })()`);
  await page.click("#mailto-fixture");
  await page.waitFor(() => [...document.querySelectorAll('.compose')].some((section) => section.textContent.includes('recipient@example.com')), 4000);
  const mailtoCompose = await page.eval(`(() => {
    const section = [...document.querySelectorAll('.compose')].find((item) => item.textContent.includes('recipient@example.com'));
    if (!section) return null;
    return {
      id: section.getAttribute('data-compose-id'),
      to: section.querySelector('[data-recipient-chips="to"]')?.textContent || '',
      subject: section.querySelector('[data-compose-field="subject"]')?.value || '',
      body: section.querySelector('[data-compose-editor]')?.innerText || section.querySelector('[data-compose-field="body"]')?.value || ''
    };
  })()`);
  check(mailtoCompose && mailtoCompose.to.includes('recipient@example.com'), "mailto link pre-fills the To field");
  check(mailtoCompose && mailtoCompose.subject === 'Hello from mailto', "mailto link pre-fills the subject");
  check(mailtoCompose && mailtoCompose.body.includes('Line one'), "mailto link pre-fills the message body");
  await page.click(`.compose[data-compose-id="${mailtoCompose.id}"] [data-compose-action="discard"]`);
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 3000);
  await page.click("#confirmAccept");
  await page.waitFor(`!document.querySelector(${JSON.stringify(`.compose[data-compose-id="${mailtoCompose.id}"]`)})`, 4000);
  await page.eval("document.getElementById('mailto-fixture')?.remove()");

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
  check(await page.eval("Boolean(document.querySelector('.compose .compose-quote .email-quote'))"), "reply keeps the original HTML in a quoted message block");
  check(await page.eval("document.querySelector('.compose .compose-quote-lead')?.textContent.startsWith('On ')"), "reply shows the compact On … wrote: quote lead");
  check(await page.eval("Boolean(document.querySelector('.compose [data-compose-action=\"toggle-quote\"]'))"), "reply exposes a quote show/hide control");
  await page.waitFor(() => Boolean(document.querySelector('.compose .compose-duplicate-warning')), 4000);
  check(await page.eval("document.querySelector('.compose .compose-duplicate-warning')?.textContent.includes('exact subject')"), "reply warns when the sender already used this subject recently");
  check(await page.eval("document.querySelector('.compose .compose-duplicate-warning')?.textContent.includes('Tomás Ribeiro')"), "the duplicate-subject reminder names the sender");
  await page.screenshot(path.join(artifacts, "05-duplicate-subject-reply.png"));
  await page.click(".compose .compose-duplicate-warning-close");
  check(await page.eval("Boolean(document.querySelector('.compose .compose-duplicate-warning')) === false"), "the duplicate-subject reminder can be dismissed");
  await page.eval("window.__a8mAutoConfirm = true");
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

  console.log("\n[6b] Recipient chips, autocomplete and auto-saved contacts");
  const restoredChipText = await page.eval(`(() => {
    const compose = [...document.querySelectorAll('.compose')].find(e=>e.querySelector('[data-compose-field="subject"]')?.value==='E2E draft');
    return compose ? [...compose.querySelectorAll('.recipient-chip-name')].map(e=>e.textContent).join(' ') : '';
  })()`);
  check(restoredChipText.includes("Elena Vasquez"), "restored draft renders its recipient as a chip");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click("#cmdCompose");
  await sleep(350);
  const chipComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const chipComposeSel = `[data-compose-id="${chipComposeId}"]`;
  await page.click(`${chipComposeSel} [data-recipient-input="to"]`);
  await page.typeText("pri");
  await sleep(450);
  check(await page.eval(`document.querySelector('${chipComposeSel} [data-recipient-suggest="to"]')?.textContent.includes('Priya Shah')`), "typing in To opens address-book autocomplete");
  await page.click(`${chipComposeSel} [data-recipient-suggest-email="priya.shah@a8m.example"]`);
  await sleep(350);
  check(await page.eval(`[...document.querySelectorAll('${chipComposeSel} .recipient-chip-name')].some(e=>e.textContent.includes('Priya Shah'))`), "selecting a directory result creates a recipient chip");
  await page.click(`${chipComposeSel} [data-recipient-input="to"]`);
  await page.typeText("external.person@example.com");
  await page.pressKey(",");
  await sleep(750);
  check(await page.eval(`Boolean(document.querySelector('${chipComposeSel} .recipient-chip[title="external.person@example.com"]'))`), "comma commits a typed external recipient as a chip");
  const savedContact = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.people.find(p=>p.email==='external.person@example.com'))");
  check(savedContact && savedContact.source === "auto", "composed external recipient is auto-saved to contacts");
  await page.waitFor(() => {
    const chip = document.querySelector('.compose [data-compose-tone-chip]');
    return Boolean(chip && !chip.hidden);
  }, 5000);
  check(await page.eval(`document.querySelector('${chipComposeSel} [data-compose-tone-chip]')?.textContent.includes('Formal')`), "compose shows a relationship-aware tone suggestion chip");
  await page.click('button[data-task="people"]');
  await sleep(450);
  check(await page.eval("document.getElementById('peopleList').textContent.includes('External Person')"), "People page lists the auto-saved external contact");
  check(await page.eval("document.getElementById('peopleList').textContent.includes('Auto-saved')"), "People page marks auto-saved contacts");
  await page.setValue("#peopleSearch", "External");
  await sleep(250);
  check(await page.eval("document.getElementById('peopleList').textContent.includes('External Person') && !document.getElementById('peopleList').textContent.includes('Elena Vasquez')"), "People search filters names, email and company");
  await page.click("[data-people-filter='contacts']");
  await sleep(250);
  check(await page.eval("document.getElementById('peopleList').textContent.includes('External Person') && !document.getElementById('peopleList').textContent.includes('Internal directory')"), "My contacts filter shows personal contacts only");
  await page.screenshot(path.join(artifacts, "05c-people-contacts.png"));
  await page.click('button[data-task="mail"]');
  await sleep(250);
  await page.click(`${chipComposeSel} [data-compose-action="discard"]`);
  await sleep(350);

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
  check(await page.eval("document.getElementById('statusPopBody').textContent.includes('Messaging policy service')"), "outbound popover names the Messaging policy enforcement");
  app.db.user.outbound.limit = 0;
  await page.click("[data-status-refresh='outbound']");
  await sleep(500);
  check(await page.eval("document.getElementById('statusPopBody').textContent.includes('no daily cap') && document.getElementById('statusPopBody').textContent.includes('Unlimited')"), "zero outbound cap renders as unlimited");
  app.db.user.outbound.limit = 200;
  await page.click("#statusPopClose");

  console.log("\n[7a] Outbound usage counts every recipient");
  const accountingRecipients = app.db.people
    .map((person) => person.email)
    .filter((email) => email && email !== app.db.user.email)
    .slice(0, 6);
  equal(accountingRecipients.length, 6, "directory provides six recipients for the outbound accounting test");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click("#cmdCompose");
  await sleep(350);
  const recipientsComposeId = await page.eval("[...document.querySelectorAll('.compose:not([hidden])')].at(-1).getAttribute('data-compose-id')");
  const recipientsComposeSel = `[data-compose-id="${recipientsComposeId}"]`;
  await addComposeRecipient(recipientsComposeSel, "to", accountingRecipients[0]);
  await addComposeRecipient(recipientsComposeSel, "to", accountingRecipients[1]);
  await page.click(`${recipientsComposeSel} [data-compose-action="cc"]`);
  await sleep(150);
  await addComposeRecipient(recipientsComposeSel, "cc", accountingRecipients[2]);
  await addComposeRecipient(recipientsComposeSel, "cc", accountingRecipients[3]);
  await addComposeRecipient(recipientsComposeSel, "cc", accountingRecipients[4]);
  await addComposeRecipient(recipientsComposeSel, "bcc", accountingRecipients[5]);
  await page.setValue(`${recipientsComposeSel} [data-compose-field="subject"]`, "Recipient accounting");
  await setComposeBody(recipientsComposeSel, "Checking recipient usage.");
  equal(await page.eval(`document.querySelectorAll(${JSON.stringify(recipientsComposeSel + ' [data-recipient-chips="to"] .recipient-chip')}).length`), 2, "compose keeps two To recipients");
  equal(await page.eval(`document.querySelectorAll(${JSON.stringify(recipientsComposeSel + ' [data-recipient-chips="cc"] .recipient-chip')}).length`), 3, "compose keeps three Cc recipients");
  equal(await page.eval(`document.querySelectorAll(${JSON.stringify(recipientsComposeSel + ' [data-recipient-chips="bcc"] .recipient-chip')}).length`), 1, "compose keeps one Bcc recipient");
  const usageBeforeSend = await page.eval("fetch('/api/outbound').then((response) => response.json()).then((data) => data.usedWithQueued)");
  await page.click(`${recipientsComposeSel} [data-compose-action="send"]`);
  await page.waitFor(() => !document.getElementById("pendingChip").hidden, 5000);
  const usageAfterSend = await page.eval("fetch('/api/outbound').then((response) => response.json()).then((data) => data.usedWithQueued)");
  equal(usageAfterSend - usageBeforeSend, 6, "outbound usage increases by all six recipients");
  await page.click(".toast-action");
  await page.waitFor(() => document.getElementById("pendingChip").hidden, 5000);
  await sleep(400);
  const restoredRecipientsId = await openComposeBySubject("Recipient accounting");
  await page.click(`[data-compose-id="${restoredRecipientsId}"] [data-compose-action="discard"]`);
  await sleep(600);

  await page.click("[data-folder='sent']");
  await sleep(600);
  check(await page.eval("[...document.querySelectorAll('.msg-subject')].some(e=>e.textContent.includes('E2E draft'))"), "sent message appears in the Sent folder after commit");
  await page.click("[data-folder='inbox']");
  await sleep(500);

  console.log("\n[7b] Send-time intent check");
  await page.eval("window.__a8mAutoConfirm = false");
  await page.click("#cmdCompose");
  await sleep(350);
  const intentComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const intentComposeSel = `[data-compose-id="${intentComposeId}"]`;
  await page.setValue(`${intentComposeSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${intentComposeSel} [data-compose-field="subject"]`, "Intent check");
  await setComposeBody(intentComposeSel, "I have attached the final report. I will call you tomorrow.");
  check(await page.eval(`Boolean(document.querySelector('${intentComposeSel} [data-compose-intent-check]'))`), "compose footer exposes the Send check action");
  await page.click(`${intentComposeSel} [data-compose-action="send"]`);
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 5000);
  check(await page.eval("document.getElementById('confirmTitle').textContent.includes('Check before sending')"), "send-time intent check opens before sending");
  check(await page.eval("document.getElementById('confirmMessage').textContent.includes('no file is attached')"), "intent check catches an attachment promise with no file");
  await page.click("#confirmCancel");
  await sleep(200);
  check(await page.eval("[...document.querySelectorAll('.compose [data-compose-field=\"subject\"]')].some((input) => input.value === 'Intent check')"), "reviewing the warning keeps the compose window open");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click(`${intentComposeSel} [data-compose-action="discard"]`);
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
  await page.setValue("#languageSelect", "fr-FR");
  await sleep(550);
  check(await page.eval("document.querySelector('.rail-label[data-i18n=\"nav.mail\"]')?.textContent === 'Courrier'"), "French catalog translates the workspace navigation");
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

  console.log("\n[8b] Authenticator and passkey setup");
  check(await page.eval("Boolean(window.QRCode)"), "self-hosted QR renderer is loaded");
  // The Node reference server reports demo mailbox mode, so production-only
  // security controls start hidden. Reveal them to exercise the real dialogs
  // and their API routes against the reference server.
  await page.eval("document.getElementById('disableTotpBtn').hidden = false");
  await page.click("#disableTotpBtn");
  await page.waitFor(() => !document.getElementById("totpDisableOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('totpDisableTitle').textContent.includes('Remove authenticator')"), "authenticator removal uses an accessible password dialog");
  await page.setValue("#totpDisablePassword", "not-the-password");
  await page.click("#totpDisableConfirm");
  await page.waitFor(() => !document.getElementById("totpDisableError").hidden, 4000);
  check(await page.eval("document.getElementById('totpDisableError').textContent.length > 0"), "wrong password shows an inline removal error");
  await page.setValue("#totpDisablePassword", DEMO_PASSWORD);
  await page.click("#totpDisableConfirm");
  await page.waitFor(() => document.getElementById("totpDisableOverlay").hidden, 5000);
  await page.waitFor(() => document.getElementById("totpMethodStatus").textContent.includes("Not set up"), 4000);
  check(await page.eval("document.getElementById('totpMethodStatus').textContent.includes('Not set up')"), "the authenticator can be removed after password confirmation");
  check(await page.eval("document.getElementById('twoFactorStatus').textContent.includes('on')"), "registered passkeys keep two-factor protection active after removing the authenticator");

  await page.eval("document.getElementById('setupTotpBtn').hidden = false");
  await page.click("#setupTotpBtn");
  await page.waitFor(() => !document.getElementById("totpSetupOverlay").hidden, 5000);
  await sleep(450);
  check(await page.eval("Boolean(document.querySelector('#totpQrHost canvas, #totpQrHost img'))"), "authenticator setup renders a scannable QR code");
  equal(await page.eval("document.querySelectorAll('#totpProviderList .totp-provider').length"), 3, "Google, Microsoft and Authy authenticator options are shown");
  check(await page.eval("document.querySelector('.totp-affiliation').textContent.includes('not affiliated')"), "the modal discloses that Alt8Mail is not affiliated with the app vendors");
  check(await page.eval("document.getElementById('totpProviderDownloads').querySelectorAll('a').length >= 2"), "authenticator download links are provided");
  equal(await page.eval("document.querySelectorAll('#totpSetupCodeInputs input').length"), 6, "authenticator setup uses six code boxes");
  await page.click("[data-totp-provider='microsoft']");
  await sleep(120);
  check(await page.eval("document.getElementById('totpProviderTip').textContent.includes('Microsoft Authenticator')"), "choosing a provider shows its scan instructions");
  check(await page.eval("document.getElementById('totpProviderDownloads').textContent.includes('Microsoft')"), "choosing a provider updates its download links");
  await page.click("#totpKeyToggle");
  await sleep(150);
  check(await page.eval("!document.getElementById('totpKeyPanel').hidden"), "manual setup key expands on demand");
  check(await page.eval("document.getElementById('totpSetupSecret').textContent.includes('•')"), "the setup key is masked until revealed");
  await page.click("#totpRevealSecret");
  check(await page.eval("!document.getElementById('totpSetupSecret').textContent.includes('•')"), "the setup key can be revealed for manual entry");
  await page.screenshot(path.join(artifacts, "09c-authenticator-setup.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await sleep(350);
  await page.screenshot(path.join(artifacts, "09c2-authenticator-setup-mobile.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  const setupCode = "123456";
  for (let index = 0; index < setupCode.length; index += 1) {
    await page.setValue(`#totpSetupOtp${index}`, setupCode[index]);
  }
  await page.waitFor(() => !document.getElementById("recoveryOverlay").hidden, 8000);
  check(await page.eval("!document.getElementById('totpSetupOverlay').hidden === false"), "successful verification closes the authenticator setup dialog");
  equal(await page.eval("document.querySelectorAll('#recoveryCodeList code').length"), 4, "enabling an authenticator generates fresh recovery codes");
  await page.click("#recoveryClose");
  await sleep(250);
  check(await page.eval("document.getElementById('twoFactorStatus').textContent.includes('on')"), "the authenticator is enabled again after verification");
  check(await page.eval("document.getElementById('totpMethodStatus').textContent.includes('Enabled')"), "the authenticator method card reflects the enabled state");

  await page.eval("document.getElementById('addPasskeyBtn').hidden = false");
  await page.click("#addPasskeyBtn");
  await page.waitFor(() => !document.getElementById("passkeySetupOverlay").hidden, 4000);
  await page.waitFor(() => document.getElementById("passkeySupport").dataset.state !== "checking", 6000);
  check(await page.eval("['ready', 'partial', 'blocked'].includes(document.getElementById('passkeySupport').dataset.state)"), "passkey setup detects this browser and device");
  check(await page.eval("document.getElementById('passkeySupportTitle').textContent.length > 0"), "passkey setup explains the detected device capability");
  equal(await page.eval("document.querySelectorAll('#passkeyMethodGrid .passkey-method').length"), 2, "passkey setup offers this device and a security key");
  await page.screenshot(path.join(artifacts, "09d-passkey-setup.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await sleep(350);
  await page.screenshot(path.join(artifacts, "09d2-passkey-setup-mobile.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await sleep(250);
  await page.click("#passkeySetupCancel");
  await page.waitFor(() => document.getElementById("passkeySetupOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('passkeySetupOverlay').hidden"), "passkey setup can be cancelled safely");

  equal(await page.eval("document.getElementById('outboundUsedValue').textContent"), "38", "security panel shows live outbound usage");

  console.log("\n[9] Sender trust panel");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(400);
  await page.click("[data-folder='spam']");
  await sleep(350);
  check(await page.eval("document.title.endsWith('Spam — avery.chen@a8m.example - Alt8Mail')"), "switching folders updates the tab title");
  await page.click(".msg:nth-of-type(1)");
  await sleep(400);
  check(await page.eval("!!document.querySelector('.external-banner.is-danger')"), "failed external authentication raises a red banner");
  check(await page.eval("document.querySelectorAll('.auth-chip.fail').length >= 2"), "SPF/DKIM/DMARC failures are visible");
  check(await page.eval(`Boolean(document.querySelector('.thread-msg .avatar img[data-avatar-domain="a8m-security.example"]'))`), "external senders request a BIMI or domain icon avatar");
  await page.waitFor(() => Boolean(document.querySelector('.thread-msg .avatar.has-photo img[data-avatar-domain="a8m-security.example"]')), 3000);
  check(await page.eval(`Boolean(document.querySelector('.thread-msg .avatar.has-photo img[data-avatar-domain="a8m-security.example"]'))`), "the resolved sender icon replaces the initials fallback");
  await page.screenshot(path.join(artifacts, "10-trust.png"));
  await page.click("[data-trust-sender]");
  await sleep(900);
  check(await page.eval("!document.querySelector('.external-banner')"), "marking the sender trusted removes the warning");
  const trusted = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.user.trustedSenders.includes('account-verify@a8m-security.example'))");
  check(trusted, "sender trust is persisted by the server");

  console.log("\n[9b] Calendar and directory");
  await page.click(".rail-btn[data-task='calendar']");
  await sleep(450);
  equal(await page.eval("document.title"), "Alt8Mail: avery.chen@a8m.example - Smarter conversations, AI-powered Email", "non-mail workspace tab title uses the account tagline");
  equal(await page.eval("document.querySelectorAll('.cal-month-cell').length"), 42, "calendar month renders a full six-week grid");
  check(await page.eval("document.querySelectorAll('.agenda-event, .cal-empty').length > 0"), "agenda renders selected-day events");
  await page.screenshot(path.join(artifacts, "15-calendar.png"));
  await page.eval("(() => { const cell = document.querySelector('.cal-month-cell[data-cal-date]'); cell.dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); return true; })()");
  await page.waitFor(() => !document.getElementById("eventOverlay").hidden, 3000);
  check(await page.eval("document.getElementById('eventDate').value !== ''"), "double-clicking a month day opens New event");
  check(await page.eval("parseFloat(getComputedStyle(document.querySelector('.cal-cell-add')).width) >= 22"), "month cells expose a larger New event plus control");
  check(await page.eval("Number(getComputedStyle(document.querySelector('.cal-cell-add')).opacity) >= 0.5"), "month cell plus control stays visible without hover");
  await page.click("#eventCancel");
  await sleep(200);
  await page.click("#calNew");
  await sleep(250);
  await page.setValue("#eventName", "Calendar invitation check");
  await page.click("#eventAttendeeInput");
  await page.setValue("#eventAttendeeInput", "elena.vasquez@a8m.example");
  await page.pressKey("Enter");
  await sleep(150);
  equal(await page.eval("document.querySelectorAll('#eventAttendeeChips .event-attendee-chip').length"), 1, "event dialog shows the invited guest");
  await page.click("#eventSave");
  await page.waitFor(() => Boolean(document.querySelector(".toast-action")), 5000);
  equal(await page.eval("document.querySelector('.toast-action')?.textContent"), "Undo", "invitations expose the undo-send action");
  await page.click(".toast-action");
  await page.waitFor(() => (document.querySelector(".toast-text")?.textContent || "").includes("Invitations cancelled"), 5000);
  check(await page.eval("(document.querySelector('.toast-text')?.textContent || '').includes('Invitations cancelled')"), "undo cancels queued calendar invitations");
  await clickByText(".agenda-event", "Calendar invitation check");
  await sleep(300);
  check(await page.eval("!document.getElementById('eventGuestPanel').hidden"), "organizer sees the guest response panel");
  equal(await page.eval("document.querySelectorAll('#eventGuestPanel .event-guest-row').length"), 1, "guest panel lists each invitee");
  check(await page.eval("(document.getElementById('eventGuestPanel')?.textContent || '').includes('No response')"), "guest panel shows the response state");
  check(await page.eval("(document.getElementById('eventGuestPanel')?.textContent || '').includes('sequence 0')"), "guest panel shows the invitation sequence");
  await page.screenshot(path.join(artifacts, "15b-calendar-guests.png"));
  await page.click("#eventClose");
  await page.click("#calNew");
  await sleep(250);
  await page.setValue("#eventName", "Weekly planning check");
  await page.setValue("#eventRecurrence", "weekly");
  await page.click("#eventAttendeeInput");
  await page.setValue("#eventAttendeeInput", "elena.vasquez@a8m.example");
  await page.pressKey("Enter");
  await page.click("#eventResourceAdd");
  await page.waitFor(() => !document.getElementById("calendarResourceOverlay").hidden, 3000);
  await page.setValue("#calendarResourceName", "Conference Room A");
  await page.click("#calendarResourceSave");
  await page.waitFor(() => document.getElementById("calendarResourceOverlay").hidden, 5000);
  check(await page.eval("(document.getElementById('eventResourceChips')?.textContent || '').includes('Conference Room A')"), "a user-created room is added to the event");
  await page.click("#eventSave");
  await page.waitFor(() => document.getElementById("eventOverlay").hidden, 6000);
  await sleep(350);
  await clickByText(".agenda-event", "Weekly planning check");
  await sleep(300);
  check(await page.eval("!document.getElementById('eventScopeWrap').hidden"), "recurring events expose an occurrence scope");
  equal(await page.eval("document.getElementById('eventScope').value"), "occurrence", "an occurrence opens in this-event scope");
  await page.setValue("#eventName", "Weekly planning check — moved");
  await page.setValue("#eventStart", "15:00");
  await page.setValue("#eventEnd", "16:00");
  await page.click("#eventSave");
  await page.waitFor(() => document.getElementById("eventOverlay").hidden, 6000);
  await sleep(350);
  check(await page.eval("[...document.querySelectorAll('.agenda-event')].some((node) => node.textContent.includes('Weekly planning check — moved'))"), "the edited occurrence replaces only its own slot");
  await clickByText(".agenda-event", "Weekly planning check — moved");
  await sleep(300);
  equal(await page.eval("document.getElementById('eventName').value"), "Weekly planning check — moved", "the recurrence exception reopens with its overridden title");
  check(await page.eval("(document.getElementById('eventGuestPanel')?.textContent || '').includes('Conference Room A')"), "the booked room is visible to the organizer");
  await page.click("#eventSave");
  await page.waitFor(() => document.getElementById("eventOverlay").hidden, 6000);
  check(await page.eval("document.getElementById('eventOverlay').hidden"), "saving an existing event closes the dialog without a calendar move error");
  await page.screenshot(path.join(artifacts, "15c-calendar-occurrence.png"));
  await page.click("#calPollsBtn");
  await page.waitFor(() => !document.getElementById("calendarPollsOverlay").hidden, 5000);
  await page.click("[data-poll-new]");
  await sleep(200);
  await page.setValue("#pollTitle", "Partner availability");
  await page.click("#pollInvitees");
  await page.setValue("#pollInvitees", "elena.vasquez@a8m.example");
  await page.click("#pollCreate");
  await page.waitFor(() => document.getElementById("calendarPollOverlay").hidden, 6000);
  await page.waitFor(() => (document.querySelector(".toast-text")?.textContent || "").includes("Meeting poll created"), 5000);
  await page.click("#calPollsBtn");
  await page.waitFor(() => Boolean(document.querySelector(".calendar-poll-card")), 5000);
  check(await page.eval("document.querySelectorAll('.calendar-poll-option').length") === 2, "meeting poll shows both time options");
  check(await page.eval("(document.querySelector('.calendar-poll-card')?.textContent || '').includes('1 invitee')"), "meeting poll shows the invitee count");
  await page.screenshot(path.join(artifacts, "15d-calendar-poll.png"));
  await page.click("[data-poll-schedule]");
  await page.waitFor(() => !document.getElementById("eventOverlay").hidden, 7000);
  equal(await page.eval("document.getElementById('eventName').value"), "Partner availability", "scheduling a poll opens the created event");
  await page.click("#eventClose");
  await page.click(".rail-btn[data-task='people']");
  await sleep(400);
  equal(await page.eval("document.querySelectorAll('.people-section[aria-label=\"Internal directory\"] .contact').length"), 8, "directory lists the eight seeded contacts");
  equal(await page.eval("document.querySelectorAll('.people-section[aria-label=\"Your contacts\"] .contact').length"), 1, "personal contacts group holds the auto-saved recipient");
  await page.click(".contact");
  await sleep(250);
  check(await page.eval("document.querySelector('.person-hero h2')?.textContent.length > 0"), "person detail renders");
  await page.screenshot(path.join(artifacts, "16-people.png"));

  console.log("\n[9b2] Delegated mailbox switcher and separate window");
  await page.waitFor(() => {
    const switcher = document.getElementById("delegatedSwitcher");
    return Boolean(switcher && !switcher.hidden);
  }, 8000);
  check(await page.eval("!document.getElementById('delegatedSwitcher').hidden"), "delegated mailbox button appears beside notifications");
  check(await page.eval("document.getElementById('delegatedSwitchBtn').classList.contains('delegated-switch-btn')"), "delegated switcher uses the primary New-mail button treatment");
  await page.click("#delegatedSwitchBtn");
  await page.waitFor(() => !document.getElementById("delegatedMailboxPop").hidden, 4000);
  check(await page.eval("!document.getElementById('delegatedMailboxPop').hidden"), "delegated mailbox dropdown opens");
  check(await page.eval("Boolean(document.getElementById('delegatedMailboxSearch'))"), "delegated mailbox dropdown is searchable");
  equal(await page.eval("document.querySelectorAll('#delegatedMailboxList [data-delegated-mailbox]').length"), 2, "delegated mailbox dropdown lists every mailbox");
  await page.setValue("#delegatedMailboxSearch", "marcus");
  await sleep(150);
  equal(await page.eval("document.querySelectorAll('#delegatedMailboxList [data-delegated-mailbox]').length"), 1, "search narrows the delegated mailbox list");
  check(await page.eval("document.getElementById('delegatedMailboxList').textContent.includes('marcus.webb@a8m.example')"), "delegated mailbox row names the mailbox address");
  await page.screenshot(path.join(artifacts, "16a-delegated-switcher.png"));
  await page.eval(`(() => {
    window.__a8mDelegatedUrl = "";
    window.__a8mRealOpen = window.open;
    window.open = function (url) {
      if (String(url).startsWith("about:blank")) {
        return {
          document: { write() {}, close() {} },
          location: { replace(value) { window.__a8mDelegatedUrl = String(value); } },
          close() {}, opener: null
        };
      }
      return window.__a8mRealOpen.call(window, url, "_blank");
    };
  })()`);
  await page.click("#delegatedMailboxList [data-delegated-mailbox]");
  await page.waitFor(() => Boolean(window.__a8mDelegatedUrl), 6000);
  const delegatedUrl = await page.eval("window.__a8mDelegatedUrl");
  check(delegatedUrl.includes("delegate="), "choosing a mailbox requests a one-time delegated window link");
  const delegatedPage = await browser.newPage(delegatedUrl);
  try {
    await delegatedPage.waitFor(() => document.readyState === "complete", 12000);
    await delegatedPage.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 15000);
    equal(await delegatedPage.eval("document.getElementById('accountTriggerEmail')?.textContent"), "marcus.webb@a8m.example", "delegated window opens the owner mailbox");
    check(await delegatedPage.eval("document.body.innerText.includes('Delegated inbox: quarterly forecast sign-off')"), "delegated inbox loads its own conversations");
    check(await delegatedPage.eval("Boolean(document.getElementById('delegatedWindowChip'))"), "delegated window names its mailbox");
    check(await delegatedPage.eval("document.getElementById('delegatedSwitcher')?.hidden !== false"), "delegated window does not offer a second delegation switcher");
    equal(await page.eval("document.getElementById('accountTriggerEmail')?.textContent"), DEMO_EMAIL, "primary window keeps the original mailbox session");
    await delegatedPage.screenshot(path.join(artifacts, "16b-delegated-window.png"));
    await delegatedPage.click("#delegatedWindowExit");
    await sleep(700);
    const delegatedClosed = delegatedPage.closed || await delegatedPage.eval("Boolean(document.querySelector('.delegated-closed-card'))").catch(() => true);
    check(delegatedClosed, "closing the delegated window signs out only that window");
    const mainStatus = await page.eval("fetch('/api/bootstrap', { credentials: 'same-origin' }).then((response) => response.status)");
    equal(mainStatus, 200, "primary mailbox stays signed in after the delegated window closes");
  } finally {
    try { await delegatedPage.close(); } catch {}
    await page.eval("if (window.__a8mRealOpen) window.open = window.__a8mRealOpen");
  }

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
  check(await page.eval("!document.getElementById('previewNav').hidden"), "multi-attachment preview offers previous and next controls");
  equal(await page.eval("document.getElementById('previewCounter').textContent"), "1 of 2", "preview counter starts at the first attachment");
  check(await page.eval("document.getElementById('previewPrev').disabled"), "the first attachment disables Previous");
  await page.screenshot(path.join(artifacts, "17-attachment-preview.png"));
  await page.click("#previewNext");
  await sleep(250);
  equal(await page.eval("document.getElementById('previewTitle').textContent"), "Launch-scope.xlsx", "Next moves the preview to the second attachment");
  equal(await page.eval("document.getElementById('previewCounter').textContent"), "2 of 2", "preview counter updates with paging");
  check(await page.eval("document.getElementById('previewNext').disabled && !document.getElementById('previewPrev').disabled"), "navigation endpoint states update on the last attachment");
  await page.click("#previewPrev");
  await sleep(250);
  equal(await page.eval("document.getElementById('previewTitle').textContent"), "Q4-roadmap-v3.pdf", "Previous returns to the first attachment");
  await page.click("#previewClose");
  await sleep(250);
  check(await page.eval("document.getElementById('previewOverlay').hidden"), "preview dialog closes cleanly");

  console.log("\n[9d] AI reply, attachment context and signature");
  await page.click("#cmdAiReply");
  await page.waitFor(() => !document.getElementById("aiAttachmentOverlay").hidden, 5000);
  check(await page.eval("document.querySelectorAll('#aiAttachmentList [data-ai-context-row]').length === 2"), "AI reply offers the email's PDF and spreadsheet as context");
  check(await page.eval("document.getElementById('aiAttachmentConfirm').disabled"), "AI attachment confirmation starts disabled");
  await page.click("#aiAttachSelectAll");
  await sleep(120);
  check(await page.eval("document.querySelectorAll('#aiAttachmentList .ai-context-file.is-selected').length === 2"), "Select all chooses every supported attachment");
  check(await page.eval("document.getElementById('aiAttachmentBudgetLabel').textContent.includes('of 100 MB')"), "AI attachment budget shows the 100 MB limit");
  await page.screenshot(path.join(artifacts, "17c-ai-attachment-context.png"));
  await page.click("#aiAttachmentConfirm");
  await page.waitFor(() => Boolean(document.querySelector(".compose:not([hidden]) [data-compose-ai-thinking]:not([hidden])")), 2000);
  check(await page.eval("Boolean(document.querySelector('.compose:not([hidden]) [data-compose-ai-thinking]:not([hidden]) .ai-writing-animation'))"), "an animated writing indicator appears while AI drafts");
  await page.screenshot(path.join(artifacts, "17d-ai-writing.png"));
  await page.waitFor(() => [...document.querySelectorAll(".compose:not([hidden]) [data-compose-editor]")].some((editor) => editor.textContent.includes("Thank you for the update")), 10000);
  check(await page.eval("!document.querySelector('.compose:not([hidden]) [data-compose-ai-thinking]:not([hidden])')"), "the AI writing indicator disappears when the reply arrives");
  const aiReplyBody = await page.eval("[...document.querySelectorAll('.compose:not([hidden]) [data-compose-editor]')].map((editor) => editor.innerText).join('\\n')");
  const generatedIndex = aiReplyBody.indexOf("Thank you for the update");
  const signatureIndex = aiReplyBody.indexOf("VP Product · Alt8Mail");
  const quoteIndex = aiReplyBody.indexOf("One more thing for the agenda");
  check(generatedIndex >= 0 && signatureIndex > generatedIndex, "AI reply keeps the automatic signature below the generated text");
  equal((aiReplyBody.match(/VP Product · Alt8Mail/g) || []).length, 1, "AI reply does not duplicate the automatic signature");
  check(quoteIndex > signatureIndex, "the quoted conversation stays below the signature");

  await page.click(".compose:not([hidden]) [data-compose-action=\"ai\"]");
  await sleep(200);
  check(await page.eval("document.querySelectorAll('.compose:not([hidden]) [data-ai-compose-attachment]').length === 2"), "the AI writing assistant offers this email's attachments before Generate");
  await page.eval("document.querySelector('.compose:not([hidden]) [data-ai-compose-attachment]').click()");
  await sleep(120);
  check(await page.eval("document.querySelector('.compose:not([hidden]) [data-ai-attachment-budget]').textContent.includes('selected')"), "AI writing assistant tracks selected attachment context");
  await page.click(".compose:not([hidden]) [data-ai-compose-generate]");
  await page.waitFor(() => document.querySelector(".compose:not([hidden]) [data-compose-ai-thinking]:not([hidden])"), 2000);
  check(await page.eval("Boolean(document.querySelector('.compose:not([hidden]) [data-compose-ai-thinking]:not([hidden])'))"), "manual AI generation also shows the writing indicator");
  await page.waitFor(() => !document.querySelector(".compose:not([hidden]) [data-compose-ai-thinking]:not([hidden])") && !document.querySelector(".compose:not([hidden]) [data-ai-result]")?.hidden, 5000);
  check(await page.eval("document.querySelector('.compose:not([hidden]) [data-ai-result]')?.textContent.includes('Thank you for your message')"), "manual AI generation renders its draft result");

  const undoCheck = await page.eval(`(() => {
    const editor = document.querySelector('.compose:not([hidden]) [data-compose-editor]');
    editor.focus();
    document.execCommand('insertText', false, ' UNDO-CHECK');
    const inserted = editor.innerText.includes('UNDO-CHECK');
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true, cancelable: true }));
    const undone = !editor.innerText.includes('UNDO-CHECK');
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'y', ctrlKey: true, bubbles: true, cancelable: true }));
    const redone = editor.innerText.includes('UNDO-CHECK');
    return { inserted, undone, redone };
  })()`);
  check(undoCheck.inserted && undoCheck.undone && undoCheck.redone, "Ctrl+Z and Ctrl+Y undo and redo compose edits");

  await page.eval("window.__a8mAutoConfirm = false");
  await page.click(".compose:not([hidden]) [data-compose-action=\"discard\"]");
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 5000);
  await page.click("#confirmAccept");
  await page.eval("window.__a8mAutoConfirm = true");
  await sleep(500);

  await page.eval(`(() => {
    window.__a8mOriginalOpen = window.open;
    window.__a8mOpened = null;
    window.open = (url, name, features) => { window.__a8mOpened = { url, name, features }; return null; };
    const row = [...document.querySelectorAll('.msg')].find((element) => element.textContent.includes('Q4 roadmap'));
    row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, cancelable: true }));
  })()`);
  await sleep(250);
  check(await page.eval(`/^#[/]Inbox[/][A-Za-z0-9_-]{22}$/.test(location.hash)`), "conversation URL uses an opaque 22-character thread ID");
  check(await page.eval(`window.__a8mOpened && window.__a8mOpened.url.includes(${JSON.stringify("thread=" + DEMO_THREAD_ID)}) && window.__a8mOpened.url.includes('popout=1')`), "double-click opens the thread in a new window URL");
  const popout = await browser.newPage(base + "/?thread=" + DEMO_THREAD_ID + "&popout=1");
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

  console.log("\n[9f-office] Cabinet Office documents");
  check(await page.eval("!document.getElementById('newOfficeDocBtn').hidden"), "New document is available when Office is ready");
  await page.click("#newOfficeDocBtn");
  await sleep(250);
  check(await page.eval("!document.getElementById('officeNewOverlay').hidden"), "new Office document dialog opens");
  await page.screenshot(path.join(artifacts, "21a-office-new-dialog.png"));
  await page.setValue("#officeNewName", "E2E launch notes");
  await page.click('[data-office-type-option="docx"]');
  await page.click("#officeNewCreate");
  await sleep(900);
  check(await page.eval("!document.getElementById('officeEditorOverlay').hidden"), "Office editor overlay opens after creation");
  check(await page.eval("document.querySelector('#officeEditorFrames iframe:not([hidden])')?.getAttribute('src').startsWith('/office-editor?file=')"), "editor host loads the isolated Office page");
  await page.waitFor(() => document.querySelector('#officeEditorFrames iframe:not([hidden])')?.contentDocument?.readyState === "complete", 5000);
  check(await page.eval("document.querySelector('#officeEditorFrames iframe:not([hidden])')?.contentDocument?.body.textContent.includes('Office editor preview')"), "Office editor host renders inside the Cabinet");
  await page.click("#officeEditorMinimize");
  await sleep(250);
  check(await page.eval("document.getElementById('officeEditorOverlay').hidden === true"), "Office editor minimizes out of the workspace");
  check(await page.eval("!document.getElementById('composeDock').hidden"), "minimized Office document appears in the dock");
  check(await page.eval("document.querySelector('#composeDockTabs .office-tab')?.textContent.includes('E2E launch notes')"), "dock tab names the minimized document");
  await page.send("Page.reload");
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll("#composeDockTabs .office-tab").length >= 1, 12000);
  await sleep(700);
  check(await page.eval("document.querySelector('#composeDockTabs .office-tab')?.textContent.includes('E2E launch notes')"), "minimized Office document survives a page reload");
  await page.click("#composeDockTabs [data-office-dock-tab]");
  await sleep(500);
  check(await page.eval("document.getElementById('officeEditorOverlay').hidden === false"), "dock tab restores the Office editor after reload");
  await page.click("#officeEditorMaximize");
  await sleep(150);
  check(await page.eval("document.getElementById('officeEditorOverlay').classList.contains('is-maximized')"), "Office editor maximizes to the viewport");
  await page.click("#officeEditorMaximize");
  await sleep(150);
  check(await page.eval("document.getElementById('officeEditorOverlay').classList.contains('is-maximized') === false"), "Office editor restores its padded size");
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  await sleep(300);
  check(await page.eval("getComputedStyle(document.getElementById('officeEditorOverlay')).position === 'fixed'"), "Office editor uses a fixed overlay on mobile");
  check(await page.eval("document.getElementById('officeEditorOverlay').getBoundingClientRect().width >= window.innerWidth - 2"), "Office editor overlay fills the mobile viewport");
  await page.screenshot(path.join(artifacts, "21b-office-editor-mobile.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await sleep(200);
  await page.click("#officeEditorVersions");
  await sleep(450);
  check(await page.eval("!document.getElementById('officeVersionsOverlay').hidden"), "version history opens from the editor");
  check(await page.eval("document.querySelectorAll('#officeVersionList .office-version-row').length === 1"), "new documents start with version 1");
  await page.screenshot(path.join(artifacts, "21b-office-editor.png"));
  await page.click("#officeVersionsClose");
  await sleep(200);
  await page.click("#officeEditorMinimize");
  await sleep(250);
  await clickByText(".file-row", "Product-offsite-plan.docx");
  await sleep(300);
  await page.click("#fileDetail [data-office-edit]");
  await sleep(700);
  check(await page.eval("document.querySelectorAll('#composeDockTabs .office-tab').length === 2"), "Office dock holds multiple documents");
  check(await page.eval("document.querySelector('#officeEditorTitle').textContent.includes('Product-offsite-plan')"), "opening another document makes it the active editor");
  await page.click("#officeEditorClose");
  await sleep(700);
  check(await page.eval("document.querySelectorAll('#composeDockTabs .office-tab').length === 1"), "closing the active document keeps the other dock tab");
  check(await page.eval("!document.getElementById('officeEditorOverlay').hidden && document.getElementById('officeEditorTitle').textContent.includes('E2E launch notes')"), "the remaining document reopens in the shared editor");
  await page.click("#officeEditorClose");
  await sleep(1300);
  check(await page.eval("document.getElementById('officeEditorOverlay').hidden === true"), "Office editor closes cleanly");
  check(await page.eval("!document.querySelector('#composeDockTabs .office-tab')"), "closing the editor removes its dock tab");
  check(await page.eval("[...document.querySelectorAll('.file-row')].some((row) => row.textContent.includes('E2E launch notes.docx'))"), "new Office document appears in the Cabinet");
  await page.click('[data-office-hub="word"]');
  await sleep(350);
  check(await page.eval("!document.getElementById('officeHubOverlay').hidden"), "Office hub opens from the document rail icon");
  check(await page.eval("document.getElementById('officeHubBody').textContent.includes('E2E launch notes')"), "Office hub lists recent documents of that type");
  check(await page.eval("document.querySelector('[data-office-hub-create=\"docx\"]') !== null"), "Office hub offers a blank document action");
  await page.screenshot(path.join(artifacts, "21c-office-hub.png"));
  await page.click("#officeHubClose");
  await sleep(200);
  await clickByText(".file-row", "E2E launch notes.docx");
  await sleep(300);
  check(await page.eval("!!document.querySelector('#fileDetail [data-office-edit]')"), "Office document detail exposes Edit in Office");
  check(await page.eval("!!document.querySelector('#fileDetail [data-office-versions]')"), "Office document detail exposes version history");
  await page.click("#fileDetail [data-file-share]");
  await sleep(300);
  check(await page.eval("!document.getElementById('shareOverlay').hidden"), "share dialog opens for an Office document");
  check(await page.eval("document.getElementById('shareInternalSection').hidden === false"), "internal sharing section is visible to the owner");
  await page.setValue("#shareInternalTarget", "nora.patel@a8m.example");
  await page.click("#shareInternalAdd");
  await page.waitFor(() => document.querySelectorAll("#shareInternalList .share-internal-row").length === 1, 4000);
  check(await page.eval("document.querySelector('#shareInternalList .share-internal-row')?.textContent.includes('Nora Patel')"), "internal access lists the granted colleague");
  await page.click("#shareInternalList [data-share-internal-remove]");
  await page.waitFor(() => document.querySelectorAll("#shareInternalList .share-internal-row").length === 0, 4000);
  check(await page.eval("document.getElementById('shareInternalList').textContent.includes('No colleagues')"), "internal access can be revoked immediately");
  await page.click("#shareClose");

  console.log("\n[9f-office-compose] New Office document from compose");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(350);
  await page.click("#cmdNewMenuBtn");
  await sleep(150);
  check(await page.eval("!!document.querySelector('#newMenu [data-new=\"office-docx\"]')"), "New mail menu offers a Word document");
  check(await page.eval("!!document.querySelector('#newMenu [data-new=\"office-xlsx\"]')"), "New mail menu offers an Excel spreadsheet");
  check(await page.eval("!!document.querySelector('#newMenu [data-new=\"office-pptx\"]')"), "New mail menu offers a PowerPoint presentation");
  await page.click("#cmdNewMenuBtn");
  await sleep(150);
  await page.click("#cmdCompose");
  await sleep(500);
  const officeComposeId = await page.eval("[...document.querySelectorAll('.compose:not([hidden])')].at(-1).getAttribute('data-compose-id')");
  const officeComposeSel = `[data-compose-id="${officeComposeId}"]`;
  check(await page.eval(`document.querySelector('${officeComposeSel}')?.hidden === false`), "compose window is visible");
  await page.click(`${officeComposeSel} [data-compose-action="attach"]`);
  await sleep(400);
  check(await page.eval("!document.getElementById('composeAttachOverlay').hidden"), "compose attachment picker opens");
  check(await page.eval("!document.getElementById('composeNewOfficeDoc').hidden"), "compose offers a new Office document");
  await page.click("#composeNewOfficeDoc");
  await sleep(300);
  check(await page.eval("!document.getElementById('officeNewOverlay').hidden"), "new Office document dialog opens from compose");
  await page.setValue("#officeNewName", "Compose brief");
  await page.click("#officeNewCreate");
  await page.waitFor(() => !document.getElementById("officeEditorOverlay").hidden || !document.getElementById("officeNewError").hidden, 6000);
  const composeOfficeError = await page.eval("document.getElementById('officeNewError').hidden ? '' : document.getElementById('officeNewError').textContent");
  if (composeOfficeError) console.log("compose new Office error:", composeOfficeError);
  check(await page.eval("!document.getElementById('officeEditorOverlay').hidden"), "new compose Office document opens the editor");
  await page.click("#officeEditorClose");
  await sleep(1400);
  check(await page.eval(`!!document.querySelector('${officeComposeSel} [data-compose-office]')`), "compose attachment card offers Edit in Office");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click(`${officeComposeSel} [data-compose-action="discard"]`);
  await sleep(700);

  console.log("\n[9g-compose] Compose upload progress, compact attachments and preview");
  await page.click("#cmdCompose");
  await sleep(350);
  const attachComposeId = await page.eval("[...document.querySelectorAll('.compose:not([hidden])')].at(-1).getAttribute('data-compose-id')");
  const attachComposeSel = `[data-compose-id="${attachComposeId}"]`;
  await page.click(`${attachComposeSel} [data-compose-action="attach"]`);
  await sleep(300);
  check(await page.eval("document.getElementById('composeUploadProgress').hidden"), "attachment upload progress is idle before a file is chosen");
  check(await page.eval("document.getElementById('composeUploadProgress').nextElementSibling?.classList.contains('compose-attach-foot')"), "upload progress sits immediately above the attachment footer");
  await page.eval(`(() => {
    const transfer = new DataTransfer();
    transfer.items.add(new File([new Uint8Array(1200)], "preview-one.pdf", { type: "application/pdf" }));
    transfer.items.add(new File([new Uint8Array(1200000)], "preview-two.pdf", { type: "application/pdf" }));
    const input = document.getElementById("attachInput");
    input.files = transfer.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
  })()`);
  await page.waitFor(() => !document.getElementById("composeUploadProgress").hidden, 5000);
  check(await page.eval("document.getElementById('composeUploadLabel').textContent.length > 0 && !document.getElementById('composeUploadTrack').hidden"), "large uploads expose a progress label and track");
  await page.screenshot(path.join(artifacts, "17d-compose-upload-progress.png"));
  await page.waitFor(`document.querySelectorAll(${JSON.stringify(attachComposeSel + " [data-compose-files] .compose-file-card")}).length === 2`, 12000);
  await page.waitFor(() => document.getElementById("composeUploadProgress").hidden, 5000);
  const attachmentLayout = await page.eval(`(() => {
    const grid = document.querySelector(${JSON.stringify(attachComposeSel + " [data-compose-files]")});
    const cards = [...grid.querySelectorAll(".compose-file-card")];
    return {
      direction: getComputedStyle(grid).flexDirection,
      wrap: getComputedStyle(grid).flexWrap,
      overflow: getComputedStyle(grid).overflowX,
      widths: cards.map((card) => Math.round(card.getBoundingClientRect().width))
    };
  })()`);
  check(attachmentLayout.direction === "row" && attachmentLayout.wrap === "nowrap" && attachmentLayout.overflow === "auto", "multiple attachments use one horizontal scrolling row");
  check(attachmentLayout.widths.every((width) => width > 0 && width < 360), "attachment cards stay compact instead of stretching the compose window");
  await page.click("#composeAttachDone");
  await sleep(250);
  await page.click(`${attachComposeSel} [data-compose-files] .compose-file-card`);
  await page.waitFor(() => !document.getElementById("previewOverlay").hidden, 4000);
  check(await page.eval("document.querySelector('.preview-pdf')?.getAttribute('src').includes('/api/attachments/')"), "clicking an uploaded PDF opens the attachment preview");
  equal(await page.eval("document.getElementById('previewCounter').textContent"), "1 of 2", "compose attachment preview starts on the selected PDF");
  await page.click("#previewNext");
  await sleep(300);
  check(await page.eval("document.getElementById('previewTitle').textContent === 'preview-two.pdf'"), "compose attachment preview navigates to the next PDF");
  await page.click("#previewClose");
  check(await page.eval("document.getElementById('previewOverlay').hidden"), "compose attachment preview closes cleanly");
  await page.click(`${attachComposeSel} [data-compose-files] .compose-file-card .file-remove`);
  await sleep(200);
  check(await page.eval(`document.querySelectorAll(${JSON.stringify(attachComposeSel + " [data-compose-files] .compose-file-card")}).length === 1`), "the attachment remove control still works without opening preview");
  await page.click(`${attachComposeSel} [data-compose-action="discard"]`);
  await sleep(700);

  await page.click(".rail-btn[data-task='files']");
  await sleep(350);

  check(await page.eval("document.querySelector('#filesView h1')?.textContent === 'Cabinet'"), "Files module is labelled Cabinet");
  check(await page.eval("document.querySelector('.rail-btn[data-task=\"files\"] .rail-label')?.textContent === 'Cabinet'"), "Cabinet navigation label is updated");
  await page.setValue("#defaultStorageSelect", "onedrive");
  await sleep(450);
  const defaultStorage = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.defaultStorage)");
  equal(defaultStorage, "onedrive", "default storage is persisted");
  await page.click("#manageStorageCredentialsBtn");
  await sleep(450);
  check(await page.eval("!document.getElementById('storageCredentialsOverlay').hidden"), "storage credential modal opens");
  equal(await page.eval("document.querySelectorAll('#storageCredentialsTabs [data-credential-provider]').length"), 4, "credential modal lists every external connector");
  await page.click("#storageCredentialsTabs [data-credential-provider='gdrive']");
  await sleep(200);
  check(await page.eval("document.getElementById('storageCredentialFields').textContent.includes('OAuth redirect URI')"), "OAuth credential form shows the redirect URI");
  await page.setValue("[data-credential-field='clientId']", "demo-google-client.apps.googleusercontent.com");
  await page.setValue("[data-credential-field='clientSecret']", "demo-secret");
  await page.click("#storageCredentialsSave");
  await sleep(650);
  check(await page.eval("document.getElementById('storageCredentialStatus').textContent.includes('Configured')"), "OAuth credentials save for the tenant");
  await page.click("#storageCredentialsClose");
  await sleep(250);
  await page.setValue("#defaultStorageSelect", "a8m");
  await sleep(350);
  await page.screenshot(path.join(artifacts, "21b-cabinet.png"));

  console.log("\n[8z] Cabinet multi-source upload manager");
  await page.click("#uploadFileBtn");
  await page.waitFor(() => !document.getElementById("uploadManager").hidden, 4000);
  check(await page.eval("document.querySelectorAll('[data-upload-tab]').length === 3"), "upload manager offers computer, URL and mobile sources");
  await page.eval(`(() => {
    const file = new File(["Alt8Mail upload test"], "e2e-upload.txt", { type: "text/plain" });
    const transfer = new DataTransfer();
    transfer.items.add(file);
    const drop = document.querySelector("[data-upload-drop]");
    drop.dispatchEvent(new DragEvent("dragover", { bubbles: true, cancelable: true, dataTransfer: transfer }));
    drop.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: transfer }));
  })()`);
  await page.waitFor(() => Boolean(document.querySelector(".upload-item.is-done")), 8000);
  check(await page.eval("document.querySelector('.upload-item.is-done').textContent.includes('e2e-upload.txt')"), "a computer upload completes with a visible progress row");
  await page.click('[data-upload-tab="url"]');
  await page.setValue("#uploadUrlInput", "https://example.com/brief.txt");
  await page.click("[data-upload-url-add]");
  await page.waitFor(() => [...document.querySelectorAll(".upload-item.is-done")].some((node) => node.textContent.includes("brief.txt")), 8000);
  check(await page.eval("[...document.querySelectorAll('.upload-item.is-done')].some((node) => node.textContent.includes('brief.txt'))"), "URL imports finish in the same upload queue");
  await page.click('[data-upload-tab="mobile"]');
  await page.waitFor(() => Boolean(document.querySelector("#uploadMobileQr canvas") || document.querySelector("#uploadMobileQr img") || document.querySelector(".upload-qr-fallback")), 7000);
  check(await page.eval("Boolean(document.querySelector('[data-upload-extend=\"1\"]'))"), "mobile uploads show a QR code and an extension control");
  await page.click('[data-upload-extend="1"]');
  await page.waitFor(() => document.getElementById("toast")?.textContent.includes("extended"), 5000);
  await page.click(".rail-btn[data-task='mail']");
  await sleep(350);
  check(await page.eval("!document.getElementById('uploadManager').hidden"), "the upload manager stays sticky while navigating to Mail");
  await page.click(".rail-btn[data-task='files']");
  await sleep(300);
  await page.click("[data-upload-minimize]");
  check(await page.eval("document.getElementById('uploadManager').classList.contains('is-minimized')"), "the upload manager can minimize to a sticky dock");
  await page.click("[data-upload-minimize]");
  await page.click("[data-upload-close]");
  await sleep(200);
  check(await page.eval("document.getElementById('uploadManager').hidden"), "the upload manager closes when the queue is finished");

  await page.eval("window.__a8mAutoConfirm = false");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(250);
  await page.click("#cmdCompose");
  await sleep(300);
  const modalComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  check(await page.eval(`document.querySelector('[data-compose-id="${modalComposeId}"] [data-editor-font]').value.includes('Arial')`), "new messages default to Arial");
  check(await page.eval(`document.querySelector('[data-compose-id="${modalComposeId}"] [data-editor-size]').value === '12'`), "new messages default to 12 pt");
  await page.click(`[data-compose-id="${modalComposeId}"] [data-compose-action="discard"]`);
  await sleep(250);
  check(await page.eval("!document.getElementById('confirmOverlay').hidden"), "destructive actions use an in-app confirmation modal");
  check(await page.eval("document.getElementById('confirmTitle').textContent.includes('Discard this draft')"), "discard modal explains the draft action");
  await page.click("#confirmCancel");
  await sleep(200);
  check(await page.eval("document.getElementById('confirmOverlay').hidden"), "confirmation modal can be cancelled");
  await page.click(`[data-compose-id="${modalComposeId}"] [data-compose-action="discard"]`);
  await sleep(200);
  await page.click("#confirmAccept");
  await sleep(300);
  const modalSelector = "[data-compose-id=\"" + modalComposeId + "\"]";
  check(await page.eval(`!document.querySelector(${JSON.stringify(modalSelector)})`), "confirmation modal completes the discard action");
  await page.eval("window.__a8mAutoConfirm = true");

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
  await page.eval("window.__a8mAutoConfirm = true");
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
  check(await page.eval("document.getElementById('scheduledEditBody').isContentEditable"), "scheduled edit uses a rich message editor");
  await page.setValue("#scheduledEditSubject", "Managed scheduled note — edited");
  await page.eval(`(() => {
    const el = document.getElementById('scheduledEditBody');
    el.innerHTML = '<p>Updated body, ready to send from the schedule center.</p>';
    el.dispatchEvent(new InputEvent('input', { bubbles: true }));
  })()`);
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
  const edited = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>{const s=d.pendingSends.find(x=>x.subject==='Managed scheduled note — edited'); return s ? {body:s.bodyText, html:s.bodyHtml, sendAt:s.sendAt} : null;})");
  check(edited && edited.body.includes("Updated body"), "edited body is persisted on the server");
  check(edited && edited.html.includes("<p>Updated body"), "scheduled edit preserves rich HTML on the server");

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

  console.log("\n[9i] Collaboration: handling, team notes and close");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(300);
  await page.click("[data-folder='inbox']");
  await sleep(450);
  await clickByText(".msg", "Q4 roadmap");
  await sleep(500);
  check(await page.eval("Boolean(document.querySelector('.thread-collab-bar'))"), "reader exposes the conversation handling bar");
  equal(await page.eval("document.querySelector('[data-thread-status]')?.value"), "in_progress", "seeded handling status is shown");
  check(await page.eval("[...document.querySelectorAll('[data-thread-assignee] option')].some((option) => option.value === 'elena.vasquez@a8m.example' && option.selected)"), "seeded assignee is selected");
  check(await page.eval("[...document.querySelectorAll('.msg')].some((row) => row.textContent.includes('Q4 roadmap') && row.querySelector('.msg-collab-status.is-progress'))"), "list rows show the in-progress chip");

  await page.setValue("[data-thread-status]", "done");
  await sleep(750);
  const doneMeta = await page.eval(`fetch('/api/thread-meta?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json()).then(d=>d.meta)`);
  equal(doneMeta.status, "done", "handling status persists on the server");
  check(await page.eval("[...document.querySelectorAll('.msg')].some((row) => row.textContent.includes('Q4 roadmap') && row.querySelector('.msg-collab-status.is-done'))"), "list row switches to the done chip");

  await page.setValue("[data-thread-assignee]", DEMO_EMAIL);
  await sleep(750);
  const assignedMeta = await page.eval(`fetch('/api/thread-meta?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json()).then(d=>d.meta)`);
  equal(assignedMeta.assignedTo?.email, DEMO_EMAIL, "assignment persists on the server");
  check(await page.eval("[...document.querySelectorAll('.msg')].some((row) => row.textContent.includes('Q4 roadmap') && row.querySelector('.msg-collab-assignee')?.textContent === 'AC')"), "list row shows the assignee initials");

  await page.click("[data-thread-notes-toggle]");
  await page.waitFor(() => {
    const panel = document.querySelector("[data-thread-notes-panel]");
    return Boolean(panel && !panel.hidden && panel.querySelector("[data-thread-note-form]"));
  }, 5000);
  check(await page.eval("!document.querySelector('[data-thread-notes-panel]').hidden"), "Team notes opens for the conversation");
  await page.setValue("[data-thread-note-input]", "Legal review complete. Keep the shared-inbox beta in Q4.");
  await page.click("[data-thread-note-form] button[type=submit]");
  await page.waitFor(() => document.querySelector("[data-thread-notes-panel]").textContent.includes("Legal review complete"), 5000);
  check(await page.eval("document.querySelector('[data-thread-notes-panel]').textContent.includes('Legal review complete')"), "team note renders after saving");
  const notesPayload = await page.eval(`fetch('/api/thread-notes?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json())`);
  equal(notesPayload.notes.length, 1, "team note is stored on the server");
  await page.screenshot(path.join(artifacts, "24e-collaboration-notes.png"));
  const noteId = await page.eval("document.querySelector('[data-thread-note-delete]')?.getAttribute('data-thread-note-delete')");
  check(Boolean(noteId), "note author can remove the note");
  await page.click(`[data-thread-note-delete="${noteId}"]`);
  await page.waitFor(() => !document.querySelector("[data-thread-notes-panel]").textContent.includes("Legal review complete"), 5000);
  const notesAfterDelete = await page.eval(`fetch('/api/thread-notes?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json())`);
  equal(notesAfterDelete.notes.length, 0, "deleted team note is removed from the server");

  await page.click("[data-thread-close]");
  await sleep(800);
  check(await page.eval("document.querySelector('[data-thread-close]')?.textContent.includes('Reopen conversation')"), "closing the conversation offers reopen");
  check(await page.eval("[...document.querySelectorAll('.msg')].every((row) => !row.textContent.includes('Q4 roadmap'))"), "closed conversation leaves the active Inbox list");
  const closedMeta = await page.eval(`fetch('/api/thread-meta?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json()).then(d=>d.meta)`);
  equal(closedMeta.closed, true, "closed state persists on the server");

  await page.click("#filterBtn");
  await sleep(200);
  await page.click("[data-list-filter='closed']");
  await sleep(700);
  check(await page.eval("[...document.querySelectorAll('.msg')].some((row) => row.textContent.includes('Q4 roadmap'))"), "Closed conversations view lists the closed thread");
  await clickByText(".msg", "Q4 roadmap");
  await sleep(450);
  await page.click("[data-thread-close]");
  await sleep(800);
  const reopenedMeta = await page.eval(`fetch('/api/thread-meta?threadId=${encodeURIComponent(DEMO_THREAD_ID)}').then(r=>r.json()).then(d=>d.meta)`);
  equal(reopenedMeta.closed, false, "reopening clears the closed state");
  check(await page.eval("[...document.querySelectorAll('.msg')].every((row) => !row.textContent.includes('Q4 roadmap'))"), "reopened conversation leaves the Closed view");
  await page.click("[data-folder='inbox']");
  await sleep(450);

  console.log("\n[9j] Coworker mentions in compose");
  await page.click("#cmdCompose");
  await sleep(350);
  const mentionComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const mentionComposeSel = `[data-compose-id="${mentionComposeId}"]`;
  await page.click(`${mentionComposeSel} [data-compose-action="mode"][data-mode="plain"]`);
  await sleep(200);
  await page.click(`${mentionComposeSel} [data-compose-field="body"]`);
  await page.pressKey("Enter");
  await page.typeText("@");
  await sleep(450);
  check(await page.eval("Boolean(document.querySelector('.compose-mention-menu'))"), "typing @ opens the coworker picker");
  check(await page.eval("document.querySelector('.compose-mention-menu')?.textContent.includes('Priya Shah')"), "mention picker lists directory coworkers");
  await page.click(".compose-mention-menu [data-mention-email='priya.shah@a8m.example']");
  await sleep(350);
  check((await composeBodyText(mentionComposeSel)).includes("@Priya Shah"), "choosing a coworker inserts the mention");
  await page.setValue(`${mentionComposeSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${mentionComposeSel} [data-compose-field="subject"]`, "Mention test");
  await sleep(1600);
  const draftMentions = await page.eval("fetch('/api/drafts').then(r=>r.json()).then(d=>{const draft=d.drafts.find((item)=>item.subject==='Mention test'); return draft ? (draft.mentions||[]).map((item)=>item.email) : null;})");
  check(Array.isArray(draftMentions) && draftMentions.includes("priya.shah@a8m.example"), "draft keeps the mention recipient for the eventual send");
  await page.screenshot(path.join(artifacts, "24f-compose-mention.png"));
  await page.click(`${mentionComposeSel} [data-compose-action="discard"]`);
  await sleep(550);

  console.log("\n[9k] Mention notifications");
  await page.click("#notifyBtn");
  await sleep(350);
  check(await page.eval("!document.getElementById('notifyPop').hidden"), "notification center opens");
  check(await page.eval("document.getElementById('notifyPop').textContent.includes('mentioned you')"), "seeded mention notification is listed");
  const mentionNotice = await page.eval("[...document.querySelectorAll('.notify-item')].find((item)=>item.textContent.includes('mentioned you'))?.getAttribute('data-notify-id')");
  check(Boolean(mentionNotice), "mention notification is actionable");
  await page.click(`[data-notify-id="${mentionNotice}"]`);
  await sleep(800);
  check(await page.eval("document.querySelector('.reader-subject h2')?.textContent.includes('Q4 roadmap')"), "opening a mention notification jumps to the conversation");

  console.log("\n[9l] Reading time, reactions and undo");
  check(await page.eval("document.querySelector('.reader-subject-meta')?.textContent.includes('min read')"), "reader shows the conversation reading time");
  check(await page.eval("document.querySelector('.thread-msg[data-message-id=\"m3\"] .thread-msg-meta')?.textContent.includes('min read')"), "each message shows its reading time");
  await page.waitFor(() => Boolean(document.querySelector('.thread-msg[data-message-id="m3"] .reaction-chip')), 5000);
  check(await page.eval("document.querySelector('.thread-msg[data-message-id=\"m3\"] .reaction-chip')?.textContent.includes('👍')"), "sent reactions appear on the message");
  await page.click('.thread-msg[data-message-id="m3"] [data-reaction-open]');
  await page.waitFor(() => Boolean(document.querySelector('.reaction-menu')), 3000);
  await page.waitFor(() => document.querySelector('.reaction-menu')?.textContent.includes('Semantic suggestions') || document.querySelector('.reaction-menu')?.textContent.includes('Suggested by AI') || document.querySelector('.reaction-menu')?.textContent.includes('Quick reactions'), 4000);
  check(await page.eval("document.querySelectorAll('.reaction-menu .reaction-quick button').length >= 6"), "reaction picker offers quick reactions");
  check(await page.eval("document.querySelector('.reaction-menu [data-reaction-suggestions]')?.textContent.includes('Acknowledge')"), "reaction picker offers semantic suggestions");
  await page.screenshot(path.join(artifacts, "25-react-menu.png"));
  await page.click('.reaction-menu .reaction-quick [data-reaction-emoji="👍"]');
  await page.waitFor(() => document.getElementById('toast').textContent.includes('Private reaction added'), 4000);
  await page.waitFor(() => Boolean(document.querySelector('.thread-msg[data-message-id="m3"] .reaction-chip.is-own')), 4000);
  const privateReaction = await page.eval("fetch('/api/reactions?threadId=N2Acmhe6TzJMY7c6L5yRfg').then(r=>r.json()).then(d=>d.reactions.find(r=>r.own))");
  equal(privateReaction?.visibility, "private", "private reaction is stored for the reader");
  await page.click('.thread-msg[data-message-id="m3"] .reaction-chip.is-own [data-reaction-remove]');
  await page.waitFor(() => document.getElementById('toast').textContent.includes('Reaction removed'), 4000);
  check(await page.eval("!document.querySelector('.thread-msg[data-message-id=\"m3\"] .reaction-chip.is-own')"), "undo removes the private reaction");

  await page.click('.thread-msg[data-message-id="m3"] [data-reaction-open]');
  await page.waitFor(() => Boolean(document.querySelector('.reaction-menu [data-reaction-mode="sent"]')), 3000);
  check(await page.eval("document.querySelector('.reaction-menu').textContent.includes('Sending emails')"), "reaction picker explains that sent reactions email the sender");
  await page.click('.reaction-menu [data-reaction-mode="sent"]');
  await page.click('.reaction-menu .reaction-quick [data-reaction-emoji="✅"]');
  await page.waitFor(() => Boolean(document.querySelector('#reactionConfirmOverlay [data-reaction-confirm-send]')), 3000);
  check(await page.eval("document.getElementById('reactionConfirmOverlay').textContent.includes('Elena Vasquez')"), "send confirmation names the recipient before delivery");
  check(await page.eval("document.getElementById('reactionConfirmOverlay').textContent.includes('reacted via Alt8Mail')"), "send confirmation previews the Alt8Mail attribution");
  await page.click('#reactionConfirmOverlay [data-reaction-confirm-send]');
  await page.waitFor(() => document.getElementById('toast').textContent.includes('sent to the sender'), 4000);
  await page.waitFor(() => Boolean(document.querySelector('.thread-msg[data-message-id="m3"] .reaction-chip.is-own')), 4000);
  const sentReaction = await page.eval("fetch('/api/reactions?threadId=N2Acmhe6TzJMY7c6L5yRfg').then(r=>r.json()).then(d=>d.reactions.find(r=>r.own))");
  equal(sentReaction?.visibility, "sent", "sent reaction is stored with its delivery mode");
  check(await page.eval("document.querySelector('.thread-msg[data-message-id=\"m3\"] .reaction-chip.is-own')?.textContent.includes('✅')"), "sent reaction appears on the message");
  await page.click('.thread-msg[data-message-id="m3"] .reaction-chip.is-own [data-reaction-remove]');
  await page.waitFor(() => document.getElementById('toast').textContent.includes('already received'), 4000);
  check(await page.eval("!document.querySelector('.thread-msg[data-message-id=\"m3\"] .reaction-chip.is-own')"), "sent reaction can be removed locally after delivery");

  console.log("\n[9m] Subscriptions inbox and unsubscribe");
  await page.click("[data-folder='subscriptions']");
  await sleep(500);
  check(await page.eval("document.querySelectorAll('.msg').length >= 2"), "Subscriptions lists newsletter conversations");
  await clickByText(".msg", "Exclusive offer");
  await sleep(500);
  check(await page.eval("Boolean(document.querySelector('.subscription-line'))"), "newsletter message exposes an unsubscribe control");
  check(await page.eval("document.querySelector('.subscription-line button')?.textContent.includes('Unsubscribe')"), "unsubscribe action is labelled");
  await page.click(".subscription-line button");
  await page.waitFor(() => document.getElementById('toast').textContent.includes('Unsubscribed from'), 4000);
  check(await page.eval("document.querySelector('.subscription-line button')?.disabled === true"), "unsubscribe action is disabled after success");
  const unsubscribeActions = await page.eval("fetch('/api/unsubscribe-actions').then(r=>r.json()).then(d=>d.actions.map(a=>({sender:a.senderEmail,status:a.status})))");
  check(unsubscribeActions.some((item) => item.status === "sent"), "unsubscribe action is recorded on the server");
  await page.screenshot(path.join(artifacts, "25b-subscriptions.png"));
  await clickByText(".msg", "Daily deals for you");
  await sleep(450);
  await page.click(".subscription-line button");
  await page.waitFor(() => (document.querySelector(".toast-text")?.textContent || "").includes("permanently deleted"), 5000);
  check(await page.eval("fetch('/api/messages?folder=subscriptions').then(r=>r.json()).then(d=>!d.messages.some(m=>m.id==='sub3'))"), "a failed unsubscribe permanently deletes the message");
  check(await page.eval("fetch('/api/messages?folder=trash').then(r=>r.json()).then(d=>!d.messages.some(m=>m.id==='sub3'))"), "a failed unsubscribe never creates a Trash copy");

  console.log("\n[9n] Recipient-time send-later suggestions");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(250);
  await page.click("[data-folder='inbox']");
  await sleep(350);
  await page.click("#cmdCompose");
  await sleep(350);
  const suggestComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const suggestSel = `[data-compose-id="${suggestComposeId}"]`;
  await page.setValue(`${suggestSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${suggestSel} [data-compose-field="subject"]`, "Recipient time test");
  await setComposeBody(suggestSel, "Checking the recipient-aware schedule suggestions.");
  await page.click(`${suggestSel} [data-compose-action="schedule"]`);
  await page.waitFor(() => Boolean(document.querySelector('#scheduleRecipientSuggestions .schedule-suggestion')), 6000);
  check(await page.eval("document.querySelectorAll('#scheduleRecipientSuggestions .schedule-suggestion').length >= 1"), "schedule dialog suggests a recipient-aware time");
  check(await page.eval("document.getElementById('scheduleRecipientSuggestions').textContent.includes('their time')"), "suggestion labels the recipient local time");
  const beforeSuggestion = await page.eval("document.getElementById('scheduleDate').value + 'T' + document.getElementById('scheduleTime').value");
  await page.click("#scheduleRecipientSuggestions .schedule-suggestion");
  await sleep(250);
  const afterSuggestion = await page.eval("document.getElementById('scheduleDate').value + 'T' + document.getElementById('scheduleTime').value");
  check(beforeSuggestion !== afterSuggestion, "choosing a suggestion updates the schedule fields");
  await page.screenshot(path.join(artifacts, "25c-recipient-time.png"));
  await page.click("#scheduleCancel");
  await sleep(250);
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click(`${suggestSel} [data-compose-action="discard"]`);
  await sleep(500);

  console.log("\n[9o] Year in review and reaction rules");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='account']");
  await sleep(300);
  check(await page.eval("Boolean(document.getElementById('yearReviewBlock'))"), "account settings expose year in review");
  await page.click("#openYearReview");
  await page.waitFor(() => !document.getElementById("yearReviewOverlay").hidden && Boolean(document.querySelector('#yearReviewBody .year-stat')), 5000);
  check(await page.eval("document.querySelectorAll('#yearReviewBody .year-month').length === 12"), "year in review renders a twelve-month chart");
  check(await page.eval("document.querySelectorAll('#yearReviewBody .year-stat').length === 4"), "year in review renders headline statistics");
  check(await page.eval("document.getElementById('yearReviewBody').textContent.includes('People who shaped your year')"), "year in review names key contacts");
  await page.screenshot(path.join(artifacts, "25d-year-review.png"));
  await page.click("[data-year-review-close]");
  await page.click("[data-settings-tab='rules']");
  await sleep(300);
  check(await page.eval("Boolean(document.getElementById('reactionRulesBlock'))"), "rules settings expose reaction rules");
  await page.click("#addReactionRuleBtn");
  await page.waitFor(() => !document.getElementById("reactionRuleOverlay").hidden, 3000);
  await page.setValue("#reactionRuleName", "Thank the customer team");
  await page.setValue("#reactionRuleSender", "tomas.ribeiro@a8m.example");
  await page.setValue("#reactionRuleEmoji", "🙏");
  await page.click("#reactionRuleSave");
  await page.waitFor(() => document.getElementById("reactionRuleList").textContent.includes("Thank the customer team"), 5000);
  check(await page.eval("document.querySelectorAll('.reaction-rule-row').length === 1"), "reaction rule appears in the settings list");
  const rules = await page.eval("fetch('/api/reaction-rules').then(r=>r.json()).then(d=>d.rules.map(r=>({name:r.name,emoji:r.emoji,enabled:r.enabled})))");
  equal(rules[0]?.emoji, "🙏", "reaction rule persists its emoji");
  equal(rules[0]?.enabled, true, "reaction rule is enabled by default");
  await page.screenshot(path.join(artifacts, "25e-reaction-rules.png"));
  await page.click("#settingsDone");
  await sleep(300);

  console.log("\n[9p] Tenant branding and outbound DLP classification");
  const brandPayload = await page.eval("fetch('/api/branding').then(r=>r.json())");
  equal(brandPayload.productName, "Alt8Mail", "public branding endpoint identifies the tenant product");
  equal(await page.eval("document.querySelector('.brand-name')?.textContent"), brandPayload.productName, "the app shell adopts the tenant product name");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(250);
  await page.click("[data-folder='inbox']");
  await sleep(350);
  await page.click("#cmdCompose");
  await sleep(350);
  const dlpComposeId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  const dlpSel = `[data-compose-id="${dlpComposeId}"]`;
  await page.setValue(`${dlpSel} [data-compose-field="to"]`, "elena.vasquez@a8m.example");
  await page.setValue(`${dlpSel} [data-compose-field="subject"]`, "Renewal");
  await setComposeBody(dlpSel, "Please confirm the account number on the renewal.");
  equal(await page.eval(`document.querySelector('${dlpSel} [data-compose-field="classification"]')?.value`), "Internal", "compose defaults to the tenant classification");
  await page.eval("window.__a8mAutoConfirm = false");
  await page.click(`${dlpSel} [data-compose-action="send"]`);
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 5000);
  check(await page.eval("document.getElementById('confirmMessage').textContent.includes('Customer identifiers')"), "DLP warns before sending matching content");
  equal(await page.eval(`document.querySelector('${dlpSel} [data-compose-field="classification"]')?.value`), "Confidential", "a DLP match raises the message classification");
  await page.screenshot(path.join(artifacts, "25f-dlp-warning.png"));
  await page.click("#confirmCancel");
  await sleep(250);
  await page.setValue(`${dlpSel} [data-compose-field="subject"]`, "Top secret launch");
  await setComposeBody(dlpSel, "This material must not leave the organization.");
  await page.click(`${dlpSel} [data-compose-action="send"]`);
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 5000);
  check(await page.eval("document.getElementById('confirmMessage').textContent.includes('Blocked by')"), "a blocking DLP rule stops the send and explains why");
  equal(await page.eval(`document.querySelector('${dlpSel} [data-compose-field="classification"]')?.value`), "Restricted", "blocked content is classified as Restricted");
  await page.screenshot(path.join(artifacts, "25g-dlp-blocked.png"));
  await page.click("#confirmAccept");
  await page.eval("window.__a8mAutoConfirm = true");
  await page.click(`${dlpSel} [data-compose-action="discard"]`);
  await sleep(500);

  console.log("\n[9q] Mail Home, Pulse, search reports, sticky notes and the React action");
  await page.eval("document.querySelector('[data-close-reader]')?.click()");
  await sleep(350);
  check(await page.eval("Boolean(document.querySelector('[data-home-tab=\"home\"].is-active'))"), "Mail Home is the default tab when no conversation is selected");
  check(await page.eval("Boolean(document.querySelector('.home-mail-svg')) && !document.querySelector('.empty-monogram')"), "Mail Home uses a mail illustration instead of a text monogram");
  check(await page.eval("!document.getElementById('cmdReact') && Boolean(document.getElementById('readerReactBtn'))"), "the command bar no longer duplicates the React action");
  check(await page.eval("Boolean(document.querySelector('[data-home-compose]'))"), "Mail Home offers a direct compose action");
  await page.click("#dateJumpBtn");
  await page.waitFor(() => !document.getElementById("dateJumpMenu").hidden, 3000);
  check(await page.eval("document.querySelectorAll('[data-date-jump]').length === 5 && Boolean(document.querySelector('[data-date-jump-go]'))"), "date jump offers Outlook-style periods and a custom date");
  await page.click('[data-date-jump="today"]');
  await page.waitFor(() => document.getElementById("dateJumpMenu").hidden, 3000);
  check(await page.eval("document.getElementById('dateJumpMenu').hidden"), "jumping to a period closes the date menu");
  await page.click('[data-home-tab="pulse"]');
  await page.waitFor(() => Boolean(document.querySelector('.pulse-stat')) && Boolean(document.querySelector('[data-pulse-bars]')), 6000);
  check(await page.eval("document.querySelectorAll('.pulse-stat').length === 4"), "Pulse renders four headline metrics");
  check(await page.eval("document.querySelectorAll('[data-pulse-bar]').length >= 1"), "Pulse renders an interactive mail-flow chart");
  await page.click('[data-pulse-bar]:not(.is-empty)');
  await page.waitFor(() => Boolean(document.querySelector('.pulse-selection strong')), 3000);
  check(await page.eval("document.querySelector('.pulse-selection').textContent.includes('messages')"), "clicking a chart bar reveals its period detail");
  await page.screenshot(path.join(artifacts, "25h-pulse-home.png"));
  await page.setValue('[data-pulse-search-input]', "invoice");
  await page.eval("document.querySelector('[data-pulse-search-form]').requestSubmit()");
  await page.waitFor(() => Boolean(document.querySelector('[data-home-tab="search"].is-active')) && Boolean(document.querySelector('.search-report-row')), 6000);
  check(await page.eval("document.querySelector('.search-report-head').textContent.includes('invoice')"), "Pulse search opens a search report tab");
  check(await page.eval("JSON.parse(sessionStorage.getItem('a8m.home.tabs.v1')).tab === 'search'"), "search report tab is stored in session state");
  await page.screenshot(path.join(artifacts, "25i-search-report.png"));
  await page.send("Page.reload");
  await page.waitFor(() => document.readyState === "complete" && Boolean(document.querySelector('[data-home-tab="search"].is-active')), 15000);
  await page.waitFor(() => Boolean(document.querySelector('.search-report-row')), 8000);
  check(await page.eval("document.querySelector('.search-report-head').textContent.includes('invoice')"), "search report survives a browser reload in the same tab");
  await page.click("[data-home-search-close]");
  await page.waitFor(() => Boolean(document.querySelector('[data-home-tab="pulse"].is-active')), 3000);

  await page.click('[data-home-tab="home"]');
  await sleep(300);
  await page.eval(`(() => {
    const csrf = decodeURIComponent((document.cookie.match(/(?:^|; )a8m_csrf=([^;]*)/) || [])[1] || "");
    return fetch('/api/dev/notes-team', {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: JSON.stringify({ team: true })
    });
  })()`);
  await page.click("#cmdNewMenuBtn");
  await page.waitFor(() => !document.getElementById("newMenu").hidden, 3000);
  await page.click('[data-new="sticky"]');
  await page.waitFor(() => !document.getElementById("stickyNoteEditorOverlay").hidden, 4000);
  equal(await page.eval("document.getElementById('stickyEditorTitle').textContent"), "New note", "the sticky-note action starts a new note");
  equal(await page.eval("document.getElementById('stickyEditorPin').checked"), true, "the first note is pinned to Inbox by default");
  await page.setValue("#stickyNoteEditorText", "Dentist Friday 3pm\n- [ ] Send the Q4 deck");
  await page.click('[data-sticky-editor-color="pink"]');
  await page.click('[data-sticky-attach-open="event"]');
  await page.waitFor(() => document.querySelectorAll('#stickyAttachResults .sticky-attach-item').length > 0, 5000);
  await page.click('#stickyAttachResults .sticky-attach-item');
  await page.click('[data-sticky-attach-open="file"]');
  await page.waitFor(() => document.querySelectorAll('#stickyAttachResults .sticky-attach-item').length > 0, 5000);
  await page.click('#stickyAttachResults .sticky-attach-item');
  await page.waitFor(() => document.querySelectorAll('#stickyEditorLinks .sticky-editor-link').length === 2, 3000);
  check(await page.eval("document.querySelectorAll('#stickyEditorLinks .sticky-editor-link').length === 2"), "events and files attach from the note editor");
  await page.click("[data-sticky-editor-save]");
  await page.waitFor(() => document.getElementById("stickyNoteEditorOverlay").hidden && Boolean(document.querySelector('.sticky-note.sticky-home.is-pink')), 5000);
  check(await page.eval("document.querySelector('.sticky-note.sticky-home').textContent.includes('Dentist Friday')"), "a note created from New sticky note appears on Mail Home");
  check(await page.eval("document.querySelector('.sticky-note.sticky-home').textContent.includes('Send the Q4 deck')"), "sticky note checkbox lines render in the note");
  check(await page.eval("document.querySelectorAll('.sticky-note.sticky-home .sticky-link').length >= 2"), "attached events and files render on the note");
  check(await page.eval("Boolean(document.querySelector('.sticky-note.sticky-home .sticky-team'))"), "a shared-mailbox note carries a team indicator");
  await page.screenshot(path.join(artifacts, "25j-sticky-home.png"));
  await page.click("[data-folder='inbox']");
  await sleep(450);
  check(await page.eval("Boolean(document.getElementById('notesDeskBtn'))"), "a persistent Notes launcher is available in the mail toolbar");
  check(await page.eval("Boolean(document.getElementById('stickyListHost')) && Boolean(document.querySelector('#stickyListHost [data-sticky-note]'))"), "the first note is pinned above the Inbox list");
  await page.click("#stickyListHost [data-sticky-collapse]");
  await page.waitFor(() => document.querySelector('#stickyListHost [data-sticky-note]')?.classList.contains('is-collapsed'), 3000);
  check(await page.eval("document.querySelector('#stickyListHost .sticky-collapsed-line')?.textContent.includes('Dentist Friday')"), "the pinned note collapses to one line");
  await page.click("#stickyListHost [data-sticky-collapse]");
  await sleep(300);
  await page.click("#notesDeskBtn");
  await page.waitFor(() => !document.getElementById("notesDeskOverlay").hidden, 4000);
  equal(await page.eval("document.querySelectorAll('.notes-desk-card').length"), 1, "the Notes desk lists the first note");
  check(await page.eval("!document.getElementById('notesDeskTeamBadge').hidden"), "the Notes desk shows a team badge for a shared mailbox");
  check(await page.eval("document.getElementById('notesDeskSubtitle').textContent.includes('Team desk')"), "the Notes desk names the shared mailbox team context");
  check(await page.eval("Boolean(document.querySelector('.notes-desk-card .notes-desk-card-team'))"), "team notes carry an owner-visible team marker");
  await page.click("#notesDeskNew");
  await page.waitFor(() => !document.getElementById("stickyNoteEditorOverlay").hidden, 4000);
  equal(await page.eval("document.getElementById('stickyEditorPin').checked"), false, "additional notes do not steal the Inbox pin");
  await page.setValue("#stickyNoteEditorText", "Q4 launch checklist\n- [ ] Legal review");
  await page.click('[data-sticky-editor-color="blue"]');
  await page.click("[data-sticky-editor-save]");
  await page.waitFor(() => document.getElementById("stickyNoteEditorOverlay").hidden && document.querySelectorAll('.notes-desk-card').length === 2, 5000);
  check(await page.eval("document.querySelectorAll('.notes-desk-card').length === 2"), "the Notes desk holds more than one note");
  await page.click("#notesDeskClose");
  await sleep(250);
  check(await page.eval("Boolean(document.querySelector('#stickyListHost .sticky-more-chip'))"), "the pinned note shows a +N chip for the rest of the desk");
  await page.click("#stickyListHost .sticky-more-chip");
  await page.waitFor(() => !document.getElementById("notesDeskOverlay").hidden, 4000);
  await page.eval(`(() => {
    const card = [...document.querySelectorAll('.notes-desk-card')].find((node) => node.textContent.includes('Q4 launch checklist'));
    card?.querySelector('[data-notes-desk-pin]')?.click();
  })()`);
  await page.waitFor(() => Boolean(document.querySelector('.toast-action')), 5000);
  const pinnedBeforeUndo = await page.eval("fetch('/api/notes?archived=1').then(r=>r.json()).then(d=>d.pinned?.title)");
  equal(pinnedBeforeUndo, "Q4 launch checklist", "pinning another note takes the Inbox slot");
  await page.click(".toast-action");
  await sleep(700);
  check(await page.eval("fetch('/api/notes?archived=1').then(r=>r.json()).then(d=>d.pinned?.title === 'Dentist Friday 3pm')"), "undo restores the previous Inbox pin");
  await page.eval(`(() => {
    const card = [...document.querySelectorAll('.notes-desk-card')].find((node) => node.textContent.includes('Q4 launch checklist'));
    card?.querySelector('[data-notes-desk-archive]')?.click();
  })()`);
  await page.waitFor(() => document.getElementById("notesDeskArchiveCount").textContent === "1", 4000);
  await page.click('[data-notes-desk-mode="archive"]');
  await page.waitFor(() => Boolean(document.querySelector('.notes-desk-card.is-archived')), 3000);
  await page.setValue("#notesDeskSearch", "Q4");
  await sleep(700);
  await page.waitFor(() => Boolean(document.querySelector('.notes-desk-card.is-archived')?.textContent.includes('Q4 launch checklist')), 5000);
  check(await page.eval("document.querySelector('.notes-desk-card.is-archived').textContent.includes('Q4 launch checklist')"), "archive search finds matching archived notes");
  await page.eval(`(() => {
    const card = [...document.querySelectorAll('.notes-desk-card.is-archived')].find((node) => node.textContent.includes('Q4 launch checklist'));
    card?.querySelector('[data-notes-desk-restore]')?.click();
  })()`);
  await page.waitFor(() => document.getElementById("notesDeskArchiveCount").textContent === "0", 4000);
  check(await page.eval("document.getElementById('notesDeskArchiveCount').textContent === '0'"), "archived notes can be restored from the Notes desk");
  await page.click("#notesDeskClose");
  await sleep(250);
  const draggedSubject = await page.eval("document.querySelector('.msg[data-thread-id] .msg-subject')?.textContent || document.querySelector('.msg[data-thread-id]')?.textContent || ''");
  await page.eval(`(() => {
    const row = document.querySelector('.msg[data-thread-id]');
    const note = document.querySelector('#stickyListHost [data-sticky-note]');
    if (!row || !note) return false;
    const transfer = new DataTransfer();
    transfer.setData('application/x-a8m-threads', JSON.stringify([row.getAttribute('data-thread-id')]));
    note.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: transfer }));
    note.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }));
    return true;
  })()`);
  await page.waitFor(() => document.querySelector('#stickyListHost .sticky-link'), 5000);
  check(await page.eval("document.querySelectorAll('#stickyListHost .sticky-link').length >= 1"), "dragging a conversation onto a note attaches a click-through link");
  const noteServer = await page.eval("fetch('/api/notes?archived=1').then(r=>r.json()).then(d=>d.pinned)");
  check(noteServer.links.some((link) => link.type === "thread") && noteServer.links.some((link) => link.type === "event") && noteServer.links.some((link) => link.type === "file"), "threads, events and files persist on the note");
  equal(noteServer.color, "pink", "note color persists");
  await page.click("#stickyListHost [data-sticky-dismiss-toggle]");
  await page.click("#stickyListHost [data-sticky-unpin]");
  await page.waitFor(() => !document.querySelector('#stickyListHost [data-sticky-note]'), 4000);
  check(await page.eval("Boolean(document.querySelector('#stickyListHost .sticky-desk-launcher'))"), "removing the Inbox pin leaves every note in the Notes desk");
  check(await page.eval("Boolean(document.getElementById('notesDeskBtn'))"), "the persistent Notes launcher survives an unpinned Inbox");
  await page.click("#notesDeskBtn");
  await page.waitFor(() => !document.getElementById("notesDeskOverlay").hidden, 4000);
  await page.eval(`(() => {
    const card = [...document.querySelectorAll('.notes-desk-card')].find((node) => node.textContent.includes('Dentist Friday'));
    card?.querySelector('[data-notes-desk-pin]')?.click();
  })()`);
  await page.waitFor(() => Boolean(document.querySelector('#stickyListHost [data-sticky-note]')), 5000);
  await page.click("#notesDeskClose");
  await page.screenshot(path.join(artifacts, "25k-sticky-inbox.png"));
  await page.setValue("#searchInput", "launch");
  await page.waitFor(() => Boolean(document.querySelector('.search-notes-strip [data-search-note]')), 7000);
  check(await page.eval("document.querySelector('.search-notes-strip').textContent.includes('Q4 launch checklist')"), "global mail search surfaces matching notes");
  await page.click(".search-notes-strip [data-search-note]");
  await page.waitFor(() => !document.getElementById("stickyNoteEditorOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('stickyNoteEditorText').value.includes('Q4 launch checklist')"), "a global note result opens the note directly");
  await page.click("[data-sticky-editor-close]");
  await page.click("#searchClear");
  await sleep(350);

  await page.click(".msg[data-thread-id]");
  await page.waitFor(() => !document.getElementById("reader").classList.contains("is-empty"), 4000);
  check(await page.eval("Boolean(document.getElementById('readerReactBtn')) && getComputedStyle(document.getElementById('readerReactBtn')).display !== 'none'"), "React sits in the reader toolbar");
  check(await page.eval("document.getElementById('readerReactBtn').getAttribute('aria-disabled') !== 'true'"), "React is enabled for a normal inbox message");
  await page.click("#readerReactBtn");
  await page.waitFor(() => Boolean(document.querySelector('.reaction-menu [data-reaction-catalog] .reaction-catalog-group')), 5000);
  check(await page.eval("document.querySelectorAll('.reaction-menu .reaction-catalog-group').length >= 4"), "the reaction picker exposes the full emoji catalog");
  await page.setValue(".reaction-menu [data-reaction-search]", "heart");
  await page.waitFor(() => Boolean(document.querySelector('.reaction-menu [data-reaction-catalog] button[title="Red heart"]')), 3000);
  check(await page.eval("Boolean(document.querySelector('.reaction-menu [data-reaction-catalog] button[title=\"Red heart\"]'))"), "emoji search filters the catalog");
  await page.click('.reaction-menu [data-reaction-mode="sent"]');
  await page.click('.reaction-menu .reaction-quick [data-reaction-emoji="🎉"]');
  await page.waitFor(() => Boolean(document.querySelector('#reactionConfirmOverlay [data-reaction-confirm-send]')), 3000);
  check(await page.eval("document.getElementById('reactionConfirmOverlay').textContent.includes('reacted via Alt8Mail')"), "sending a reaction asks for confirmation with Alt8Mail attribution");
  await page.screenshot(path.join(artifacts, "25l-reaction-confirm.png"));
  await page.click("#reactionConfirmOverlay [data-reaction-confirm-cancel]");
  check(await page.eval("!document.getElementById('reactionConfirmOverlay')"), "cancelling the confirmation sends nothing");
  await page.click("[data-folder='subscriptions']");
  await sleep(450);
  await page.click(".msg[data-thread-id]");
  await sleep(450);
  check(await page.eval("document.getElementById('readerReactBtn').getAttribute('aria-disabled') === 'true'"), "React is disabled for newsletter and List-Unsubscribe mail");
  await page.eval("document.querySelector('.folder-row [data-folder-menu=\"subscriptions\"]')?.click()");
  await sleep(250);
  check(await page.eval("!document.getElementById('folderMenu').hidden"), "Subscriptions opens its own folder menu");
  check(await page.eval("document.querySelector('#folderMenu [data-folder-action=\"rename\"]')?.disabled === true"), "Subscriptions rename is disabled");
  check(await page.eval("document.querySelector('#folderMenu [data-folder-action=\"delete\"]')?.disabled === true"), "Subscriptions delete is disabled");
  check(await page.eval("document.querySelector('#folderMenu [data-folder-action=\"move\"]')?.disabled === true"), "Subscriptions move is disabled");
  check(await page.eval("Boolean(document.querySelector('#folderMenu [data-folder-action=\"unsubscribe-all\"]'))"), "Subscriptions offers Unsubscribe from all");
  await page.click('#folderMenu [data-folder-action="unsubscribe-all"]');
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 3000);
  check(await page.eval("document.getElementById('confirmMessage').textContent.includes('permanently deleted')"), "unsubscribe from all explains the automatic deletion");
  await page.eval("document.getElementById('confirmAccept').click()");
  await page.waitFor(() => Boolean(document.getElementById("subscriptionCleanupOverlay")), 8000);
  check(await page.eval("document.getElementById('subscriptionCleanupOverlay').textContent.includes('messages removed')"), "cleanup report shows the automatically freed messages");
  check(await page.eval("document.getElementById('subscriptionCleanupOverlay').textContent.includes('Failed')"), "cleanup report lists failed senders");
  await page.eval("document.querySelector('#subscriptionCleanupOverlay [data-sub-report-close]')?.click()");
  await sleep(500);
  check(await page.eval("fetch('/api/messages?folder=subscriptions').then(r=>r.json()).then(d=>d.messages.length===0)"), "unsubscribe from all removes attempted subscription messages");
  await page.click("[data-folder='sent']");
  await sleep(450);
  await page.click(".msg[data-thread-id]");
  await sleep(450);
  check(await page.eval("document.getElementById('readerReactBtn').getAttribute('aria-disabled') === 'true'"), "React is disabled in Sent");
  await page.eval("document.querySelector('[data-close-reader]')?.click()");
  await sleep(250);

  console.log("\n[9r] Curated add-ons directory and reader actions");
  check(await page.eval("document.getElementById('railAddons')?.offsetParent !== null"), "workspace rail exposes the Add-on shortcut");
  await page.click("#railAddons");
  await page.waitFor(() => !document.getElementById("settingsView").hidden && !document.getElementById("addonsSettings").hidden, 4000);
  await page.waitFor(() => document.querySelectorAll("#addonsGrid .addon-card").length === 20, 8000);
  check(await page.eval("Boolean(document.getElementById('addonSettingsNav'))"), "Add-ons appears in Settings");
  check(await page.eval("document.querySelector('.addons-hero')?.textContent.includes('Do more from your inbox')"), "add-ons page uses a curated directory hero");
  equal(await page.eval("document.querySelectorAll('#addonsGrid .addon-card').length"), 20, "add-ons page lists the first-party catalog");
  check(await page.eval("document.querySelector('[data-addon-card=\"deepl\"]')?.textContent.includes('DeepL')"), "add-on cards name the provider");
  check(await page.eval("document.querySelectorAll('#addonsGrid .addon-logo-tile img').length === 20"), "every add-on card shows its provider logo");
  check(await page.eval("Array.from(document.querySelectorAll('#addonsGrid .addon-logo-tile img')).every((img) => img.complete && img.naturalWidth > 0)"), "every provider logo file loads");
  check(await page.eval("document.querySelectorAll('#addonsGrid .addon-support-link').length === 20"), "every add-on card links to provider support");
  await page.screenshot(path.join(artifacts, "26-addons-catalog.png"));
  await page.eval("document.getElementById('settingsScroll').scrollTop = document.getElementById('settingsScroll').scrollHeight");
  await sleep(250);
  await page.screenshot(path.join(artifacts, "26c-addons-wave3.png"));
  await page.eval("document.getElementById('settingsScroll').scrollTop = 0");
  await page.click('[data-addon-card="hubspot"] [data-addon-details="hubspot"]');
  await page.waitFor(() => !document.getElementById("addonDetailsOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('addonDetailsTitle').textContent.includes('HubSpot')"), "add-on details identify the provider");
  check(await page.eval("Boolean(document.getElementById('addonDetailsBody').querySelector('a[href*=\"help.hubspot.com\"]'))"), "add-on details link to provider support");
  check(await page.eval("Boolean(document.getElementById('addonDetailsBody').querySelector('a[href*=\"developers.hubspot.com\"]'))"), "add-on details link to provider documentation");
  check(await page.eval("Boolean(document.querySelector('#addonDetailsIcon img'))"), "add-on details show the provider logo");
  await page.screenshot(path.join(artifacts, "26a-addon-details.png"));
  await page.click('#addonDetailsOverlay [data-addon-details-close]');
  await sleep(200);
  await page.setValue("#addonsSearch", "deepl");
  await sleep(180);
  equal(await page.eval("document.querySelectorAll('#addonsGrid .addon-card').length"), 1, "add-on search filters the catalog");

  await page.click('[data-addon-primary="deepl"]');
  await page.waitFor(() => !document.getElementById("addonSetupOverlay").hidden, 4000);
  check(await page.eval("document.getElementById('addonSetupTitle').textContent.includes('DeepL')"), "personal add-on setup opens a permissions dialog");
  check(await page.eval("document.getElementById('addonSetupPermissions').textContent.includes('Send that text to DeepL')"), "add-on setup discloses provider permissions");
  await page.setValue('#addonSetupFields [data-addon-field="apiKey"]', "test-deepl-key:fx");
  await page.setValue('#addonSetupFields [data-addon-field="targetLanguage"]', "FR");
  await page.click("#addonSetupSave");
  await page.waitFor(() => document.getElementById("addonSetupOverlay").hidden, 5000);
  await sleep(500);
  check(await page.eval("document.querySelector('[data-addon-card=\"deepl\"]')?.textContent.includes('Installed')"), "connecting a personal add-on installs it");

  await page.setValue("#addonsSearch", "slack");
  await sleep(180);
  await page.click('[data-addon-card="slack"] [data-addon-primary="slack"]');
  await page.waitFor(() => !document.getElementById("addonSetupOverlay").hidden, 4000);
  check(await page.eval("Boolean(document.querySelector('#addonSetupFields [data-addon-oauth-start=\"managed\"]'))"), "Slack setup offers managed OAuth");
  check(await page.eval("document.querySelector('.addon-oauth-byo summary')?.textContent.includes('Use your own Slack app')"), "Slack setup offers a bring-your-own-app path");
  check(await page.eval("Boolean(document.querySelector('#addonSetupLinks a[href*=\"slack.com/help\"]'))"), "Slack setup links to provider support");
  check(await page.eval("Boolean(document.querySelector('#addonSetupIcon img'))"), "Slack setup shows the provider logo");
  await page.setValue('#addonSetupFields [data-addon-field="webhookUrl"]', "https://hooks.slack.com/services/T000/B000/XXXX");
  await page.click("#addonSetupSave");
  await page.waitFor(() => document.getElementById("addonSetupOverlay").hidden, 5000);
  await sleep(500);
  check(await page.eval("document.querySelector('[data-addon-card=\"slack\"]')?.textContent.includes('Ready to install')"), "workspace admin setup makes an add-on ready for users");
  await page.click('[data-addon-card="slack"] [data-addon-primary="slack"]');
  await page.waitFor(() => document.querySelector('[data-addon-card="slack"]')?.textContent.includes('Installed'), 5000);
  check(await page.eval("document.querySelector('[data-addon-card=\"slack\"]')?.textContent.includes('Installed')"), "workspace add-on installs after setup");
  equal(await page.eval("document.querySelector('[data-addon-card=\"slack\"] [data-addon-install-policy]')?.value"), "self_service", "add-ons default to self-service installation");
  await page.setValue('[data-addon-card="slack"] [data-addon-install-policy]', "approval_required");
  await page.waitFor(() => document.querySelector('[data-addon-card="slack"] [data-addon-install-policy]')?.value === "approval_required", 5000);
  await sleep(500);
  check(await page.eval("document.querySelector('[data-addon-card=\"slack\"] [data-addon-install-policy]')?.value === 'approval_required'"), "workspace admin can require approval for future installs");

  await page.click(".rail-btn[data-task='mail']");
  await sleep(350);
  await page.click("[data-folder='inbox']");
  await sleep(350);
  await page.click(".msg[data-thread-id]");
  await page.waitFor(() => !document.getElementById("reader").classList.contains("is-empty"), 5000);
  await page.waitFor(() => document.getElementById("readerAddonsWrap") && !document.getElementById("readerAddonsWrap").hidden, 5000);
  check(await page.eval("!document.getElementById('readerAddonsWrap').hidden"), "reader exposes installed add-on actions");
  await page.click("#readerAddonsBtn");
  await page.waitFor(() => !document.getElementById("readerAddonsMenu").hidden, 3000);
  check(await page.eval("document.querySelectorAll('#readerAddonsMenu [data-addon-reader-action]').length === 2"), "reader add-on menu lists each installed action");
  await page.click('#readerAddonsMenu [data-addon-id="deepl"]');
  await page.waitFor(() => !document.getElementById("addonResultOverlay").hidden, 5000);
  check(await page.eval("document.getElementById('addonResultText').textContent.includes('Translated:')"), "DeepL action returns a translation");
  await page.screenshot(path.join(artifacts, "26b-addon-translation.png"));
  await page.click("#addonResultOverlay [data-addon-result-close]");
  await page.click("#readerAddonsBtn");
  await page.click('#readerAddonsMenu [data-addon-id="slack"]');
  await page.waitFor(() => (document.querySelector(".toast-text")?.textContent || "").includes("Sent to Slack"), 5000);
  check(await page.eval("(document.querySelector('.toast-text')?.textContent || '').includes('Sent to Slack')"), "Slack action posts through the workspace connection");
  await page.eval("document.querySelector('[data-close-reader]')?.click()");
  await sleep(300);

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
  await page.eval(`(() => {
    const csrf = decodeURIComponent((document.cookie.match(/(?:^|; )a8m_csrf=([^;]*)/) || [])[1] || "");
    return fetch('/api/dev/inject', {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: JSON.stringify({
        subject: 'Dark theme readability fixture',
        bodyText: 'Dark theme readable text. Font attribute text. HSL text. White card text.',
        bodyHtml: '<p style="color:#111111">Dark theme readable text.</p><p><font color="#111111">Font attribute text.</font></p><p style="color:hsl(0, 0%, 7%)">HSL text.</p><div style="background:#ffffff;padding:8px"><p style="color:#111111">White card text.</p></div>'
      })
    });
  })()`);
  // Force a fresh bootstrap so a fixture injected while Inbox is already
  // active is fetched into the local index.
  await page.eval("location.reload()");
  await page.waitFor(() => document.readyState === "complete", 12000);
  await page.waitFor(() => document.getElementById("app") && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 15000);
  await page.waitFor(() => document.getElementById("bootSplash")?.hidden !== false, 10000);
  await page.waitFor(() => document.documentElement.getAttribute("data-theme") === "dark", 5000);
  await page.click("[data-folder='inbox']");
  await sleep(500);
  await page.eval(`(() => {
    const row = [...document.querySelectorAll('.msg')].find((item) => item.textContent.includes('Dark theme readability fixture'));
    if (row) row.click();
  })()`);
  await sleep(450);
  equal(await page.eval("getComputedStyle(document.querySelector('.email-body p')).color"), "rgb(232, 238, 247)", "dark mode lifts dark inline email text to white");
  equal(await page.eval("getComputedStyle([...document.querySelectorAll('.email-body font')].find((node) => node.textContent.includes('Font attribute text'))).color"), "rgb(232, 238, 247)", "dark mode lifts dark <font color> text to white");
  equal(await page.eval("getComputedStyle([...document.querySelectorAll('.email-body p')].find((node) => node.textContent.includes('HSL text'))).color"), "rgb(232, 238, 247)", "dark mode lifts dark HSL text to white");
  equal(await page.eval("getComputedStyle(document.querySelector('.email-body div p')).color"), "rgb(17, 17, 17)", "dark mode keeps dark text on a white email card");
  await page.screenshot(path.join(artifacts, "06-dark-email.png"));
  await page.screenshot(path.join(artifacts, "11-dark.png"));
  await page.click("[data-folder='inbox']");
  await sleep(350);
  await page.eval("document.querySelector('[data-close-reader]')?.click()");
  await sleep(200);
  const offlineTitle = await page.eval("window.dispatchEvent(new Event('offline')); document.title");
  equal(offlineTitle, "Offline · Inbox — Alt8Mail", "offline tab title replaces the folder title");
  await page.eval("window.dispatchEvent(new Event('online'))");
  await sleep(700);
  check(await page.eval("document.title.endsWith('Inbox — avery.chen@a8m.example - Alt8Mail')"), "online tab title returns to the folder format");

  console.log("\n[10b] Individual-view thread URLs");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='mail']");
  await sleep(350);
  await page.setValue("#inboxViewSelect", "individual");
  await sleep(700);
  await page.click(".rail-btn[data-task='mail']");
  await sleep(500);
  await page.click(".msg[data-thread-id]");
  await sleep(400);
  const individualHash = await page.eval("location.hash");
  check(/^#[/]Inbox[/][A-Za-z0-9_-]{22}$/.test(individualHash), "individual view writes the opaque thread ID to the URL");
  check(!individualHash.includes("msg_imap_"), "individual view does not expose message IDs in the URL");
  const beforeIndividualDelete = await page.eval("document.querySelectorAll('.msg').length");
  await page.pressKey("#");
  await sleep(120);
  equal(await page.eval("document.querySelectorAll('.msg').length"), beforeIndividualDelete - 1, "deleting an individual-view message removes it immediately");
  await page.waitFor(() => document.querySelector(".toast-action")?.textContent.includes("Undo"), 5000);
  await page.click(".toast-action");
  await sleep(900);
  await page.click("[data-folder='spam']");
  await sleep(400);
  check(await page.eval("document.querySelectorAll('.msg').length > 0"), "spam has messages before it is emptied");
  await page.eval("window.__a8mAutoConfirm = false");
  await page.eval("document.querySelector('.folder-row [data-folder-menu=\"spam\"]')?.click()");
  await sleep(250);
  await page.eval("document.querySelector('#folderMenu [data-folder-action=\"empty\"]')?.click()");
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 3000);
  await page.eval("document.getElementById('confirmAccept').click()");
  await page.eval("window.__a8mAutoConfirm = true");
  await sleep(30);
  check(await page.eval("document.querySelectorAll('.msg.is-removing').length > 0"), "emptying a folder fades its rows out");
  await sleep(250);
  equal(await page.eval("document.querySelectorAll('.msg').length"), 0, "emptying a folder clears its rows without a refresh");
  check(await page.eval("document.querySelector('.list-empty')?.textContent.includes('folder is empty')"), "emptied folder shows its empty state immediately");
  await sleep(700);
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='mail']");
  await sleep(350);
  await page.setValue("#inboxViewSelect", "grouped");
  await sleep(700);
  await page.click(".rail-btn[data-task='mail']");
  await sleep(350);

  console.log("\n[11] Mobile layout");
  const mobile = await browser.newPage("about:blank");
  await mobile.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  await mobile.goto(base + "/");
  await mobile.waitFor(() => document.readyState === "complete", 10000);
  if (await mobile.eval("!document.getElementById('loginScreen').hidden")) {
    await mobile.setValue("#loginEmail", DEMO_EMAIL);
    await mobile.setValue("#loginPassword", DEMO_PASSWORD);
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

  console.log("\n[11a] HTML reply quote with images");
  await page.eval(`(() => {
    const csrf = decodeURIComponent((document.cookie.match(/(?:^|; )a8m_csrf=([^;]*)/) || [])[1] || "");
    return fetch('/api/dev/inject', {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: JSON.stringify({
        subject: 'HTML reply fixture',
        bodyText: 'The original message keeps its layout and image.',
        bodyHtml: '<div style="background:#f4f7fb;padding:18px"><p style="font-size:18px;font-weight:700">The original message keeps its layout and image.</p><img src="https://images.example.test/logo.png" alt="Brand logo" width="240" style="width:100%;height:auto"><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=" alt="Sized logo" width="240" style="width:100%;height:auto"></div>'
      })
    });
  })()`);
  await page.click('[data-folder="inbox"]');
  await sleep(500);
  await page.eval(`(() => {
    const row = [...document.querySelectorAll('.msg')].find((item) => item.textContent.includes('HTML reply fixture'));
    if (row) row.click();
  })()`);
  await sleep(400);
  await page.waitFor(() => Boolean(document.querySelector(".reader [data-trust-explain]")), 5000);
  check(await page.eval("document.querySelector('.reader [data-trust-explain]')?.textContent.includes('Why is this')"), "reader shows a visible safety-explanation action");
  await page.click(".reader [data-trust-explain]");
  await page.waitFor(() => !document.getElementById("detailsOverlay").hidden, 5000);
  await page.waitFor(() => Boolean(document.querySelector("[data-trust-explanation] .trust-explanation-head")), 5000);
  check(await page.eval("document.querySelector('[data-trust-explanation]')?.textContent.includes('Why this is safe')"), "security details include a plain-English safety explanation");
  await page.click('[data-details-tab="source"]');
  await sleep(450);
  check(await page.eval("document.querySelector('[data-source-view]').textContent.includes('Subject: HTML reply fixture')"), "raw source shows the complete header block");
  check(await page.eval("document.querySelector('[data-source-view]').textContent.includes('Received: from mx')"), "raw source includes the receiving hop");
  check(await page.eval("document.querySelector('[data-source-meta]').textContent.includes('headers')"), "raw source reports the header count and size");
  await page.screenshot(path.join(artifacts, "11a-raw-source.png"));
  await page.click("#detailsClose");
  await sleep(200);
  await page.click('.reader-toolbar [data-action="reply"]');
  await sleep(500);
  const htmlReplyId = await page.eval("[...document.querySelectorAll('.compose')].at(-1).getAttribute('data-compose-id')");
  check(await page.eval(`Boolean(document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-quote .email-quote img[src^="/api/mail/image"]'))`), "reply renders the original image through the privacy proxy");
  check(await page.eval(`document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-quote img')?.getAttribute('data-email-src') === 'https://images.example.test/logo.png'`), "the quoted image keeps its original URL for sending");
  check(await page.eval(`Boolean(document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-quote .email-quote p'))`), "reply preserves the original HTML layout inside the quote");
  check(await page.eval(`Math.abs(document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-quote img[alt="Sized logo"]').getBoundingClientRect().width - 240) < 2`), "quoted image keeps its designed width instead of filling the composer");
  check(await page.eval(`!document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-editor]').classList.contains('is-quote-collapsed')`), "reply shows the original quote automatically");
  check(await page.eval(`(() => { const editor = document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-editor]'); const inner = editor.querySelector('.compose-quote-inner'); return Math.abs(inner.getBoundingClientRect().left - editor.getBoundingClientRect().left - 14) < 12; })()`), "the quoted message stays aligned with the left writing edge");
  check(await page.eval(`(() => { const editor = document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-editor]'); const quote = editor.querySelector('.compose-quote'); const before = quote?.previousElementSibling; return before?.tagName === 'P' && !before.textContent.trim(); })()`), "compose has a blank writing line immediately above the quote");
  check(await page.eval(`document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-priority="normal"]')?.classList.contains('is-active')`), "normal priority is selected by default");
  await page.click(`[data-compose-id="${htmlReplyId}"] [data-compose-action="options"]`);
  await sleep(200);
  check(await page.eval(`getComputedStyle(document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-priority="normal"]')).backgroundColor !== 'rgba(0, 0, 0, 0)'`), "selected priority has a distinct color treatment");
  await page.screenshot(path.join(artifacts, "11c-compose-priority.png"));
  await page.click(`[data-compose-id="${htmlReplyId}"] [data-compose-action="options"]`);
  await sleep(150);
  const replyHasAi = await page.eval(`Boolean(document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-ai-btn'))`);
  if (replyHasAi) {
    check(await page.eval(`(() => { const ai = document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-ai-btn'); const quote = document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-action="toggle-quote"]'); return Boolean(ai) && quote?.nextElementSibling === ai; })()`), "AI assistant sits immediately after the Quote control");
    check(await page.eval(`getComputedStyle(document.querySelector('[data-compose-id="${htmlReplyId}"] .compose-ai-btn')).animationName.includes('compose-ai-pulse')`), "AI assistant button pulses for visibility");
  } else {
    console.log("  - SKIP: the demo workspace has AI disabled; the Go backend enables it per organization");
  }
  await page.screenshot(path.join(artifacts, "11b-html-reply.png"));
  await page.click(`[data-compose-id="${htmlReplyId}"] [data-compose-action="toggle-quote"]`);
  await sleep(200);
  check(await page.eval(`document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-editor]').classList.contains('is-quote-collapsed')`), "the quote control can collapse the original HTML");
  const alignmentSetup = await page.eval(`(() => {
    const editor = document.querySelector('[data-compose-id="${htmlReplyId}"] [data-compose-editor]');
    const block = document.createElement('p');
    block.id = 'align-shortcut-probe';
    block.textContent = 'Alignment shortcut check';
    editor.prepend(block);
    editor.focus();
    const range = document.createRange();
    range.selectNodeContents(block);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    const buttons = [...document.querySelectorAll('[data-compose-id="${htmlReplyId}"] [data-editor-cmd^="justify"]')];
    return { count: buttons.length, titles: buttons.map((button) => button.title) };
  })()`);
  equal(alignmentSetup.count, 4, "compose toolbar offers Left, Center, Right and Justify alignment");
  check(alignmentSetup.titles.some((title) => title.includes("Ctrl+L")) && alignmentSetup.titles.some((title) => title.includes("Ctrl+J")), "alignment controls advertise their keyboard shortcuts");
  const alignmentStates = {};
  for (const [key, vk, label] of [["r", 82, "right"], ["l", 76, "left"], ["e", 69, "center"], ["r", 82, "right"], ["j", 74, "justify"]]) {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", modifiers: 2, key, code: `Key${key.toUpperCase()}`, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", modifiers: 2, key, code: `Key${key.toUpperCase()}`, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
    await sleep(100);
    alignmentStates[label] = await page.eval(`getComputedStyle(document.querySelector('[data-compose-id="${htmlReplyId}"] #align-shortcut-probe')).textAlign`);
  }
  equal(alignmentStates.left, "left", "Ctrl+L aligns the current paragraph left");
  equal(alignmentStates.center, "center", "Ctrl+E centers the current paragraph");
  equal(alignmentStates.right, "right", "Ctrl+R aligns the current paragraph right");
  equal(alignmentStates.justify, "justify", "Ctrl+J justifies the current paragraph");
  check(await page.eval(`document.querySelector('[data-compose-id="${htmlReplyId}"] [data-editor-cmd="justifyFull"]')?.classList.contains('is-active')`), "the Justify toolbar state follows the keyboard command");
  await page.eval("window.__a8mAutoConfirm = false");
  await page.click(`[data-compose-id="${htmlReplyId}"] [data-compose-action="discard"]`);
  await page.waitFor(() => !document.getElementById("confirmOverlay").hidden, 5000);
  check(await page.eval("getComputedStyle(document.getElementById('confirmAccept')).color === 'rgb(255, 255, 255)'"), "danger confirmation uses a readable solid red action");
  await page.click("#confirmAccept");
  await page.eval("window.__a8mAutoConfirm = true");
  await sleep(500);

  console.log("\n[11aa] Signature editor undo and redo");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='mail']");
  await sleep(350);
  await page.click("#newSignatureBtn");
  await page.waitFor(() => !document.getElementById("signatureEditorOverlay").hidden, 3000);
  await sleep(150);
  const signatureRichUndo = await page.eval(`(() => {
    const editor = document.getElementById('signatureEditorBody');
    editor.focus();
    document.execCommand('insertText', false, 'RichUndoCheck');
    const inserted = editor.innerText.includes('RichUndoCheck');
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true, cancelable: true }));
    const undone = !editor.innerText.includes('RichUndoCheck');
    editor.dispatchEvent(new KeyboardEvent('keydown', { key: 'y', ctrlKey: true, bubbles: true, cancelable: true }));
    return { inserted, undone, redone: editor.innerText.includes('RichUndoCheck') };
  })()`);
  check(signatureRichUndo.inserted && signatureRichUndo.undone && signatureRichUndo.redone, "Ctrl+Z and Ctrl+Y work in the rich signature editor");
  await page.click("[data-signature-mode='plain']");
  await sleep(120);
  await page.eval("document.getElementById('signatureEditorPlain').focus()");
  await page.send("Input.insertText", { text: "PlainUndoCheck" });
  await sleep(80);
  const plainInserted = await page.eval("document.getElementById('signatureEditorPlain').value.includes('PlainUndoCheck')");
  await page.eval("document.getElementById('signatureEditorPlain').dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true, cancelable: true }))");
  const plainUndone = await page.eval("!document.getElementById('signatureEditorPlain').value.includes('PlainUndoCheck')");
  await page.eval("document.getElementById('signatureEditorPlain').dispatchEvent(new KeyboardEvent('keydown', { key: 'y', ctrlKey: true, bubbles: true, cancelable: true }))");
  const plainRedone = await page.eval("document.getElementById('signatureEditorPlain').value.includes('PlainUndoCheck')");
  check(plainInserted && plainUndone && plainRedone, "Ctrl+Z and Ctrl+Y work in the plain signature editor");
  await page.click("#signatureEditorCancel");
  await sleep(200);
  await page.click("#settingsDone");
  await sleep(300);

  console.log("\n[11a] Unread mailbox notifications");
  app.db.externalMailAccounts = [{
    id: "acct_gmail_demo", provider: "gmail", email: "team@gmail.example",
    displayName: "Team Gmail", color: "#c5221f", oauth: true, status: "active",
    unreadCount: 2,
    latestUnread: {
      id: "gmail_demo_1", fromName: "Jordan Blake", fromEmail: "jordan@example.com",
      subject: "Q4 supplier update", date: new Date().toISOString()
    }
  }];
  await page.eval("location.reload()");
  await waitForApp();
  check(await page.eval("!document.getElementById('accountMenu').hidden"), "account switcher loads with connected mailboxes");
  equal(await page.eval("document.getElementById('accountMenuCount').textContent"), "2", "mailbox count includes the connected Gmail account");
  check(await page.eval("!document.getElementById('accountTriggerUnread').hidden && Number(document.getElementById('accountTriggerUnreadCount').textContent) > 0"), "folder switcher counts unread mail across mailboxes");
  await page.click("#accountMenuBtn");
  await page.waitFor(() => !document.getElementById("accountPopover").hidden, 5000);
  check(await page.eval("document.querySelector('#accountMenuList [data-account-menu-id=\"all\"] .account-row-unread')?.textContent.trim() === document.getElementById('accountTriggerUnreadCount').textContent.trim()"), "unified mailbox row mirrors the total unread envelope count");
  check(await page.eval("[...document.querySelectorAll('#accountMenuList .account-row-copy strong')].some(node => node.textContent === 'Team Gmail')"), "account menu lists the connected Gmail mailbox");
  check(await page.eval("document.querySelector('#accountMenuList [data-account-menu-id=\"acct_gmail_demo\"] .account-row-unread')?.textContent.trim() === '2'"), "connected mailbox shows its unread envelope count");
  await page.click("#accountMenuBtn");
  app.db.externalMailAccounts[0].unreadCount = 5;
  app.db.externalMailAccounts[0].latestUnread = {
    id: "gmail_demo_2", fromName: "Jordan Blake", fromEmail: "jordan@example.com",
    subject: "New supplier contract", date: new Date().toISOString()
  };
  await page.eval("document.dispatchEvent(new Event('visibilitychange'))");
  await page.waitFor(() => {
    const ping = document.getElementById("accountPing");
    return Boolean(ping && !ping.hidden);
  }, 5000);
  check(await page.eval("document.getElementById('accountPing')?.textContent.includes('Team Gmail') && document.getElementById('accountPing')?.textContent.includes('New supplier contract')"), "new mail in another mailbox pops a notification from the account selector");
  check(await page.eval("Number(document.getElementById('accountPingCount').textContent.trim()) === 5"), "notification shows the connected mailbox's unread total");
  check(await page.eval("document.getElementById('accountMenu').classList.contains('has-news')"), "account selector pulses while the new-mail alert is visible");
  await page.screenshot(path.join(artifacts, "01c-account-new-mail-ping.png"));
  await page.click("#accountPingClose");
  check(await page.eval("document.getElementById('accountPing').hidden"), "new-mail alert can be dismissed");
  app.db.externalMailAccounts = [];

  console.log("\n[11b] Paged delete auto-loads remaining mail");
  const pageSeed = app.db.messages.find((message) => message.folder === "inbox" && !message.draft);
  for (let index = 0; index < 70; index += 1) {
    app.db.messages.push({
      ...pageSeed,
      id: `bulk_page_${index}`,
      threadId: `bulk_thread_${index}`,
      messageId: `<bulk-page-${index}@example.test>`,
      subject: `Bulk paging ${index}`,
      snippet: `Bulk paging ${index}`,
      date: new Date(Date.now() - index * 60000).toISOString(),
      unread: false,
      starred: false,
      flagged: false,
      attachments: []
    });
  }
  await page.eval("location.reload()");
  await waitForApp();
  check(await page.eval("document.querySelectorAll('.msg').length >= 40"), "large inbox loads its first page");
  await page.click("#selectAll");
  await page.waitFor(() => !document.getElementById("bulkBar").hidden, 3000);
  await page.click("[data-bulk-action='delete']");
  await page.waitFor(() => document.querySelectorAll(".msg").length > 0, 6000);
  check(await page.eval("document.querySelectorAll('.msg').length > 0 && !document.querySelector('#messageList .list-empty')"), "deleting every loaded row auto-loads the next page");
  await page.screenshot(path.join(artifacts, "25b-paged-delete-auto-load.png"));

  console.log("\n[11c] Passkey sign-in");
  await page.eval("localStorage.setItem('a8m.accent', 'rose'); document.documentElement.style.setProperty('--accent', '#b8336a')");
  const logoutStartedAt = Date.now();
  await page.click("#railLogout");
  await page.waitFor(() => !document.getElementById("bootSplash").hidden && document.getElementById("bootSplash").classList.contains("is-logout"), 3000);
  check(await page.eval("document.querySelector('#bootSplash h1')?.textContent === 'Closing your workspace.'"), "logout shows the closing splash heading");
  check(await page.eval("document.querySelector('#bootSplash p:not(.eyebrow)')?.textContent.includes('Signing out of your inbox')"), "logout shows the closing session copy");
  check(await page.eval("Boolean(document.querySelector('#bootSplash .boot-lock svg'))"), "logout splash includes the lock icon");
  await sleep(2200);
  check(await page.eval("Number(getComputedStyle(document.querySelector('#bootSplash .boot-lock')).opacity) > 0.5"), "the envelope fades into a visible lock during sign-out");
  check(await page.eval("getComputedStyle(document.querySelector('#bootSplash .boot-progress span')).animationName.includes('boot-progress-drain')"), "the sign-out progress bar drains instead of loading");
  check(await page.eval("getComputedStyle(document.querySelector('#bootSplash .boot-progress span')).backgroundColor === 'rgb(149, 162, 181)'"), "the sign-out progress bar uses a neutral grey");
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 8000);
  equal(await page.eval("document.title"), "Alt8Mail: Secure Email with AI built in - Sign-in", "the tab returns to the sign-in title after logout");
  check(Date.now() - logoutStartedAt >= 5800, "login appears only after the six-second closing sequence");
  check(await page.eval("localStorage.getItem('a8m.accent') === null"), "logout clears the cached accent");
  check(await page.eval("getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() === '#0f6cbd'"), "the login screen returns to the default system accent");
  await fillDemoLogin();
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  const passkeyEndpoint = await page.eval(`fetch('/api/session/2fa/webauthn/begin', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}'
  }).then((response) => response.status)`);
  if (passkeyEndpoint === 401 || passkeyEndpoint === 404) {
    console.log("  - SKIP: the Node reference server does not implement WebAuthn; the Go backend owns the real passkey flow");
  } else {
    await page.click("#usePasskeyBtn");
    await page.waitFor(() => !document.getElementById("passkeyOverlay").hidden, 4000);
    check(await page.eval("document.getElementById('passkeyPromptStatus').textContent.includes('Waiting')"), "passkey prompt shows the device-waiting state");
    await page.screenshot(path.join(artifacts, "23-passkey-prompt.png"));
    await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);
    check(await page.eval("document.getElementById('loginScreen').hidden"), "passkey verification signs in and restores the workspace");
  }

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
