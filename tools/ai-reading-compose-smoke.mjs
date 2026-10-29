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
const stamp = Date.now();
const fontSubject = `Postcard font smoke ${stamp}`;
const aiReplyStub = "Thank you for the update. I reviewed the conversation and will send the confirmed next steps by Friday.";
const summaryStub = "The conversation confirms the IDLE delivery test succeeded and the team is reviewing the next steps.";
const translationStub = "La conversación confirma que la prueba de entrega IDLE fue exitosa.";
if (!password) throw new Error("ALT8MAIL_UI_PASSWORD is required");

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);
let beforeDraftIds = new Set();
let draftsBaselineReady = false;

const csrf = () => page.eval("document.cookie.match(/(?:^|; )a8m_system_csrf=([^;]+)/)?.[1] || ''");
const deleteDraft = async (id) => {
  const token = decodeURIComponent(await csrf());
  await page.eval(`fetch('/api/drafts/${encodeURIComponent(id)}',{method:'DELETE',credentials:'same-origin',headers:{'X-CSRF-Token':${JSON.stringify(token)}}})`);
};
const deleteNewTestDrafts = async () => {
  if (!draftsBaselineReady) return;
  try {
    const data = await page.eval("fetch('/api/drafts',{credentials:'same-origin'}).then(r=>r.json())");
    for (const draft of data.drafts || []) {
      if (!beforeDraftIds.has(draft.id)) await deleteDraft(draft.id);
    }
  } catch {}
};

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 15000);
  await page.setValue("#loginEmail", email);
  await page.setValue("#loginPassword", password);
  const loginStarted = Date.now();
  await page.click("#loginSubmit");
  await page.waitFor(() => document.getElementById("bootSplash").hidden === false, 8000);
  await sleep(3000);
  assert.equal(await page.eval("document.getElementById('bootSplash').hidden"), false, "workspace splash stays visible during the five-second opening sequence");
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 12000);
  const splashElapsed = Date.now() - loginStarted;
  assert.equal(splashElapsed >= 4800 && splashElapsed <= 12000, true, `splash timing is deliberate (${splashElapsed}ms)`);
  await page.waitFor(() => document.querySelectorAll(".msg").length > 0, 12000);
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(900);
  const draftsBefore = await page.eval("fetch('/api/drafts',{credentials:'same-origin'}).then(r=>r.json())");
  beforeDraftIds = new Set((draftsBefore.drafts || []).map((draft) => draft.id));
  draftsBaselineReady = true;

  await page.eval(`(() => {
    window.__a8mRealFetch = window.fetch;
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : input.url;
      if (url.includes('/api/ai/')) {
        const payload = url.endsWith('/translate')
          ? { translation: ${JSON.stringify(translationStub)}, language: "en-US" }
          : url.endsWith('/reply')
            ? { reply: ${JSON.stringify(aiReplyStub)}, language: "en-US" }
            : { summary: ${JSON.stringify(summaryStub)}, language: "en-US" };
        return Promise.resolve(new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } }));
      }
      return window.__a8mRealFetch(input, init);
    };
  })()`);

  // Open a real conversation, avoiding threads whose newest row is a draft.
  let selectedSubject = "";
  const rowCount = await page.eval("document.querySelectorAll('.msg').length");
  for (let index = 0; index < rowCount; index += 1) {
    await page.eval(`document.querySelectorAll('.msg')[${index}].click()`);
    await sleep(450);
    selectedSubject = await page.eval("document.querySelector('#reader .reader-subject h2')?.textContent || ''");
    if (selectedSubject) break;
    if (await page.eval("document.querySelectorAll('.compose').length")) {
      await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
      await sleep(650);
    }
  }
  assert.ok(selectedSubject, "a non-draft conversation opens in the reader");

  await sleep(1200);
  assert.equal(await page.eval("!!document.getElementById('readerAiCard')"), false, "opening a message does not trigger an automatic AI summary");
  assert.equal(await page.eval("document.getElementById('cmdAiWrap').hidden"), false, "the AI split button appears after the user opens an email");
  assert.equal(await page.eval("document.getElementById('cmdAiReply').hidden"), false, "Reply with AI appears when the tenant assistant is enabled");
  assert.equal(await page.eval("document.querySelector('.commandbar [data-action=\"reply-all\"]').hidden"), true, "Reply all is hidden when the replied message has no additional recipients");

  await page.click("#cmdAiBtn");
  assert.equal(await page.eval("document.getElementById('cmdAiMenu').hidden"), false, "the AI split button opens its dropdown");
  assert.deepEqual(
    await page.eval("[...document.querySelectorAll('#cmdAiMenu strong')].map((node) => node.textContent)"),
    ["Summarise this email", "Translate Email"],
    "AI menu has the requested summary and translation actions"
  );
  await page.click('#cmdAiMenu [data-ai-action="summary"]');
  await sleep(250);
  assert.equal(await page.eval("document.querySelector('#readerAiCard .ai-summary-body')?.textContent"), summaryStub, "manual summary renders its result");
  await page.click("#cmdAiChevron");
  await page.click('#cmdAiMenu [data-ai-action="translate"]');
  await sleep(250);
  assert.equal(await page.eval("document.querySelector('#readerAiCard .ai-summary-body')?.textContent"), translationStub, "manual translation renders in the requested language");
  assert.equal(await page.eval("document.querySelector('#readerAiCard strong')?.textContent"), "AI translation", "translation result is labelled correctly");
  await page.click("#readerAiBtn");
  assert.deepEqual(
    await page.eval(`(() => {
      const menu = document.getElementById('readerAiMenu');
      const rect = menu.getBoundingClientRect();
      return {
        open: !menu.hidden && menu.classList.contains('is-portal') && menu.parentElement === document.body,
        withinViewport: rect.left >= 0 && rect.top >= 0 && rect.right <= window.innerWidth && rect.bottom <= window.innerHeight,
        onTop: document.elementFromPoint(rect.left + 20, rect.top + 20)?.closest('#readerAiMenu')?.id || ''
      };
    })()`),
    { open: true, withinViewport: true, onTop: "readerAiMenu" },
    "reader-toolbar AI menu escapes the scroll container and stays on screen"
  );
  await page.click("#readerAiBtn");

  await page.click("#cmdAiReply");
  await page.waitFor(() => document.querySelectorAll(".compose").length === 1, 4000);
  await sleep(350);
  const aiReply = await page.eval(`(() => {
    const compose = document.querySelector('.compose');
    return {
      subject: compose.querySelector('[data-compose-field="subject"]')?.value || '',
      to: compose.querySelector('[data-compose-field="to"]')?.value || '',
      body: compose.querySelector('[data-compose-editor]')?.textContent || ''
    };
  })()`);
  assert.equal(aiReply.subject, selectedSubject.startsWith("Re:") ? selectedSubject : `Re: ${selectedSubject}`, "AI reply opens a prefilled reply compose");
  assert.ok(aiReply.to.includes("@"), "AI reply has a recipient");
  assert.equal(aiReply.body.includes(aiReplyStub), true, "AI reply text is inserted into the composer");
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(900);

  // Font controls must survive the client editor and the server sanitizer.
  await page.click("#cmdCompose");
  await page.waitFor(() => document.querySelectorAll(".compose").length === 1, 4000);
  const styles = await page.eval(`(() => {
    const compose = document.querySelector('.compose');
    const editor = compose.querySelector('[data-compose-editor]');
    editor.innerHTML = '<p>Enterprise typography check</p>';
    editor.dispatchEvent(new InputEvent('input', { bubbles: true }));
    const selectContents = () => {
      const range = document.createRange();
      range.selectNodeContents(editor);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    };
    selectContents();
    const font = compose.querySelector('[data-editor-font]');
    font.value = "Georgia, 'Times New Roman', serif";
    font.dispatchEvent(new Event('change', { bubbles: true }));
    selectContents();
    const size = compose.querySelector('[data-editor-size]');
    size.value = "24";
    size.dispatchEvent(new Event('change', { bubbles: true }));
    return editor.innerHTML;
  })()`);
  assert.ok(styles.includes("font-family") && styles.includes("font-size"), "font family and size controls format the editor");
  assert.deepEqual(
    await page.eval(`(() => {
      const compose = document.querySelector('.compose');
      return [compose.querySelector('[data-editor-font]').value, compose.querySelector('[data-editor-size]').value];
    })()`),
    ["Georgia, 'Times New Roman', serif", "24"],
    "font controls keep the selected font and size visible"
  );
  await page.setValue('.compose [data-compose-field="subject"]', fontSubject);
  await sleep(1300);
  const savedDrafts = await page.eval("fetch('/api/drafts',{credentials:'same-origin'}).then(r=>r.json())");
  const savedDraft = (savedDrafts.drafts || []).find((draft) => draft.subject === fontSubject);
  assert.ok(savedDraft, "font-formatted draft saves");
  assert.ok(String(savedDraft.bodyHtml || "").includes("font-family") && String(savedDraft.bodyHtml || "").includes("font-size"), "server keeps safe font formatting in the saved draft");

  // A minimized composer becomes a clearly visible Outlook-blue dock tab.
  await page.eval("document.querySelector('.compose [data-compose-action=\"minimize\"]').click()");
  await sleep(200);
  const minimized = await page.eval(`(() => {
    const tab = document.querySelector('.compose-tab.is-minimized');
    const style = tab ? getComputedStyle(tab) : null;
    return {
      exists: Boolean(tab),
      flashing: Boolean(tab && tab.classList.contains('is-just-minimized')),
      background: style ? style.backgroundImage : '',
      color: style ? style.color : '',
      toast: document.getElementById('toast')?.textContent || ''
    };
  })()`);
  assert.equal(minimized.exists, true, "minimized compose has a dock tab");
  assert.equal(minimized.flashing, true, "minimized tab animates into view");
  assert.equal(minimized.background.includes("linear-gradient"), true, "minimized tab uses a strong blue gradient");
  assert.equal(minimized.color, "rgb(255, 255, 255)", "minimized tab has high-contrast white text");
  assert.equal(minimized.toast.includes("Draft minimized"), true, "minimize action reports where the draft went");
  await page.screenshot(path.join(artifacts, "ai-reading-compose-0.8.png"));

  await page.eval("document.querySelector('.compose-tab.is-minimized').click()");
  await sleep(400);
  assert.equal(await page.eval("document.querySelectorAll('.compose:not([hidden])').length"), 1, "the minimized draft reopens from its dock tab");
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(900);

  // Grouped vs individual list view is server-backed and changes the rows.
  const groupedRows = await page.eval("document.querySelectorAll('.msg').length");
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click('[data-settings-tab="mail"]');
  await page.setValue("#inboxViewSelect", "individual");
  await sleep(850);
  assert.equal(await page.eval("fetch('/api/preferences').then(r=>r.json()).then(d=>d.preferences.inboxView)"), "individual", "individual view persists in PostgreSQL");
  await page.click("#settingsDone");
  await sleep(500);
  const individualRows = await page.eval("document.querySelectorAll('.msg').length");
  const inboxMessageCount = await page.eval("fetch('/api/messages?folder=inbox').then(r=>r.json()).then(d=>(d.messages||[]).filter(m=>m.folder==='inbox').length)");
  assert.equal(individualRows, inboxMessageCount, "individual view renders one row per inbox message");
  assert.equal(individualRows >= groupedRows, true, `individual view renders message rows (${groupedRows} -> ${individualRows})`);
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click('[data-settings-tab="mail"]');
  await page.setValue("#inboxViewSelect", "grouped");
  await sleep(750);
  assert.equal(await page.eval("fetch('/api/preferences').then(r=>r.json()).then(d=>d.preferences.inboxView)"), "grouped", "grouped view is restored");
  await page.click("#settingsDone");

  await deleteNewTestDrafts();
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log(`AI/reading/compose smoke passed: 5s splash (${splashElapsed}ms), manual AI summary/translation, Reply with AI, font controls, blue minimized tab, and grouped/individual inbox view.`);
} catch (error) {
  console.error("AI/READING/COMPOSE SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try {
    await page.screenshot(path.join(artifacts, "ai-reading-compose-0.8-failure.png"));
    await deleteNewTestDrafts();
  } catch {}
  throw error;
} finally {
  await deleteNewTestDrafts();
  await browser.close();
}
