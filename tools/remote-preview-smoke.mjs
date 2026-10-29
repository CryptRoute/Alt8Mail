import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage("https://130-94-46-22.sslip.io/");
try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(700);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  await page.click("#fillDemoCode");
  await page.click("#twoFactorSubmit");
  await page.waitFor(() => document.getElementById("loginScreen").hidden && !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 15000);
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
  await sleep(300);
  const messages = await page.eval("document.querySelectorAll('.msg').length");
  assert.equal(messages, 12, "12 inbox conversations");
  assert.equal(await page.eval("document.querySelector('.account-id span').textContent"), "avery.chen@meridian.example", "demo identity");
  await page.click(".rail-btn[data-task='tasks']");
  await page.waitFor(() => !document.getElementById("tasksView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.task-row').length"), 8, "tasks workspace");
  await page.click(".rail-btn[data-task='files']");
  await page.waitFor(() => !document.getElementById("filesView").hidden, 5000);
  assert.equal(await page.eval("document.querySelectorAll('.storage-card').length"), 5, "files workspace connectors");
  assert.equal(await page.eval("document.querySelectorAll('.file-row').length"), 12, "files workspace rows");
  await page.click(".rail-btn[data-task='mail']");
  await sleep(400);
  await page.screenshot(path.join(artifacts, "server-preview.png"));
  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Preview browser smoke passed: mail, Tasks and Files are live.");
} catch (error) {
  console.error("PREVIEW SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "server-preview-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
