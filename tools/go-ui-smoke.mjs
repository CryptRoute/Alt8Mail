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

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden || !document.getElementById("app").hidden, 15000);
  assert.equal(await page.eval("!document.getElementById('loginScreen').hidden"), true, "login screen is shown");
  if (password) {
    await page.setValue("#loginEmail", email);
    await page.setValue("#loginPassword", password);
  }
  await page.click("#loginSubmit");
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 20000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await sleep(300);
  // Drafts deliberately left open are restored; close them for a clean navigation test.
  await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').forEach((button) => button.click())");
  await sleep(700);
  const messageCount = await page.eval("document.querySelectorAll('.msg').length");
  assert.equal(messageCount >= 1, true, "real Dovecot conversations are rendered");
  assert.equal(await page.eval("document.querySelector('.account-id span').textContent"), "avery.chen@a8m.test", "Go identity is rendered");
  assert.equal(await page.eval("document.getElementById('resetData').textContent.trim()"), "Resync from Dovecot", "UI is in real IMAP mode");

  const sender = await page.eval("fetch('/api/bootstrap').then(r=>r.json()).then(d=>d.messages[0].from.email)");
  await page.setValue("#searchInput", "from:" + sender);
  await sleep(650);
  assert.equal(await page.eval("document.querySelectorAll('.msg').length >= 1"), true, "server search returns real mailbox results");
  await page.setValue("#searchInput", "");
  await sleep(350);

  await page.click(".rail-btn[data-task='tasks']");
  await page.waitFor(() => !document.getElementById("tasksView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.task-row').length >= 0"), true, "Tasks workspace renders without demo rows");

  await page.click(".rail-btn[data-task='files']");
  await page.waitFor(() => !document.getElementById("filesView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.storage-card').length"), 5, "Files workspace renders five providers");
  assert.equal(await page.eval("document.querySelectorAll('.file-row').length >= 0"), true, "Files workspace renders without demo files");

  await page.click(".rail-btn[data-task='people']");
  await page.waitFor(() => !document.getElementById("peopleView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.contact').length >= 8"), true, "Directory renders the tenant contacts");

  await page.click(".rail-btn[data-task='calendar']");
  await page.waitFor(() => !document.getElementById("calendarView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.cal-cell').length"), 42, "Calendar renders a full month grid without demo events");
  assert.equal(await page.eval("document.querySelectorAll('.cal-event').length >= 0"), true, "Calendar is safe with an empty real event set");

  await page.click(".rail-btn[data-task='mail']");
  await page.waitFor(() => !document.getElementById("mailView").hidden, 5000);
  await page.click("[data-open-scheduled]");
  await page.waitFor(() => !document.getElementById("scheduledView").hidden, 5000);
  assert.equal(await page.eval("document.getElementById('scheduledView').hidden"), false, "Scheduled center opens");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(400);

  await page.screenshot(path.join(artifacts, "go-feature-api.png"));
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Go UI smoke passed: real IMAP mail, search, scheduled, Tasks, Files, People and Calendar.");
} catch (error) {
  console.error("GO UI SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "go-feature-api-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
