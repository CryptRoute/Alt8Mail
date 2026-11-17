import assert from "node:assert/strict";
import fs from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createApp } from "../server/app.mjs";
import { launchBrowser } from "../tools/cdp.mjs";
import {
  hasBlockingFindings, printSummary, runAudit, summarize, writeIndex
} from "../tools/a11y-audit.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(__dirname, "..");
const reportOnly = process.argv.includes("--report-only");
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-a11y-"));
const app = await createApp({
  dataFile: path.join(tempDir, "db.json"),
  holdMs: 500,
  undoTtlMs: 5000,
  allowDevOutbox: true
});
const address = await app.listen(0);
const base = `http://127.0.0.1:${address.port}`;
const adminRoot = path.join(root, "admin");
const adminServer = http.createServer(async (request, response) => {
  const pathname = decodeURIComponent(new URL(request.url, "http://127.0.0.1").pathname);
  if (pathname.startsWith("/api/")) {
    response.writeHead(404, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: "Not available in the static accessibility harness" } }));
    return;
  }
  const relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
  const filePath = path.resolve(adminRoot, relative);
  if (!filePath.startsWith(adminRoot + path.sep) || relative.includes("..")) {
    response.writeHead(403);
    response.end("Forbidden");
    return;
  }
  const types = {
    ".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
    ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
    ".svg": "image/svg+xml", ".png": "image/png", ".json": "application/json"
  };
  try {
    const body = await fs.readFile(filePath);
    response.writeHead(200, { "content-type": types[path.extname(filePath)] || "application/octet-stream" });
    response.end(body);
  } catch {
    response.writeHead(404);
    response.end("Not found");
  }
});
const adminAddress = await new Promise((resolve) => adminServer.listen(0, "127.0.0.1", () => resolve(adminServer.address())));
const adminBase = `http://127.0.0.1:${adminAddress.port}`;
let browser;
let failures = 0;

async function auditSurface(page, surface, options) {
  const result = await runAudit(page, surface, options);
  const summary = summarize(result);
  printSummary(summary);
  failures += summary.counts.Critical;
  return result;
}

try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
  const page = await browser.newPage(base + "/");
  await page.waitFor(() => document.readyState === "complete", 12000);
  await page.waitFor(() => Boolean(document.getElementById("loginScreen")), 10000);
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 10000);
  await sleep(200);

  const results = [];
  results.push(await auditSurface(page, "login"));

  await page.setValue("#loginEmail", "avery.chen@a8m.example");
  await page.setValue("#loginPassword", "a8m-demo");
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  results.push(await auditSurface(page, "two-factor"));

  await page.click("#fillDemoCode");
  await page.click("#twoFactorSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 15000);
  await page.waitFor(() => document.getElementById("bootSplash")?.hidden !== false, 12000);
  await sleep(500);
  results.push(await auditSurface(page, "inbox"));

  await page.click(".msg");
  await page.waitFor(() => document.body.classList.contains("reader-open") || !document.getElementById("reader").classList.contains("is-empty"), 8000);
  await sleep(300);
  results.push(await auditSurface(page, "reader"));

  const composeCount = await page.eval("document.querySelectorAll('.compose[data-compose-id]').length");
  await page.pressKey("c");
  await page.waitFor(`document.querySelectorAll('.compose[data-compose-id]').length > ${composeCount}`, 8000);
  await sleep(250);
  results.push(await auditSurface(page, "compose"));

  await page.pressKey("Escape");
  await sleep(300);
  await page.click("#settingsBtn");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 8000);
  await sleep(300);
  results.push(await auditSurface(page, "settings"));

  await page.eval(`(() => {
    document.documentElement.lang = "ar";
    document.documentElement.dir = "rtl";
    return true;
  })()`);
  await page.click("#settingsDone");
  await page.waitFor(() => document.getElementById("settingsView").hidden, 8000);
  await sleep(250);
  results.push(await auditSurface(page, "rtl-360", { width: 360, height: 800 }));

  const controlPage = await browser.newPage(adminBase + "/");
  await controlPage.waitFor(() => document.readyState === "complete", 12000);
  await controlPage.waitFor(() => document.getElementById("loginView") && !document.getElementById("loginView").hidden, 10000);
  await sleep(250);
  results.push(await auditSurface(controlPage, "control-login"));
  await controlPage.eval(`(() => {
    document.getElementById("loginView").hidden = true;
    document.getElementById("adminApp").hidden = false;
    document.getElementById("viewRoot").innerHTML = "<h1>Overview</h1>";
    return true;
  })()`);
  await sleep(250);
  results.push(await auditSurface(controlPage, "control-shell"));
  await controlPage.close();

  const indexPath = await writeIndex(results);
  console.log(`Accessibility report index: ${indexPath}`);
  assert.ok(results.length >= 8, "every baseline surface was audited");
  if (hasBlockingFindings(results) && !reportOnly) {
    console.error("Accessibility baseline has Critical findings.");
    process.exitCode = 1;
  } else if (hasBlockingFindings(results)) {
    console.log("Report-only run: Critical findings recorded without failing.");
  } else {
    console.log("Accessibility baseline: no Critical findings.");
  }
} catch (error) {
  console.error(error.stack || error.message || String(error));
  process.exitCode = 1;
} finally {
  try { await browser?.close(); } catch {}
  try { await app.close(); } catch {}
  await new Promise((resolve) => adminServer.close(resolve));
  await fs.rm(tempDir, { recursive: true, force: true });
}
