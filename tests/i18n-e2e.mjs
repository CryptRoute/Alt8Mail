import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createApp } from "../server/app.mjs";
import { launchBrowser } from "../tools/cdp.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const artifacts = path.join(root, "artifacts");
await fs.mkdir(artifacts, { recursive: true });
const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-i18n-e2e-"));
const dataFile = path.join(tempDir, "db.json");
const app = await createApp({ dataFile, holdMs: 0, undoTtlMs: 1000 });
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

async function waitForLogin() {
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => Boolean(document.getElementById("loginScreen")) && !document.getElementById("loginScreen").hidden, 10000);
  await page.waitFor(() => document.documentElement.lang.length > 0, 5000);
}

try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
  page = await browser.newPage(base + "/");
  await waitForLogin();

  console.log("\n[i18n] Control Plane language availability");
  const initialOptions = await page.eval("document.querySelectorAll('#languageSelect option').length");
  check(initialOptions >= 35, `all enabled catalogs appear in the picker (${initialOptions} options)`);
  const policyPayload = await page.eval("fetch('/api/locales').then((response) => response.json())");
  check(policyPayload.locales.length >= 36, "locale API returns the full shipped manifest");
  app.db.webmailLocales.disabled = ["es-ES"];
  await page.eval("localStorage.setItem('a8m.language', 'es-ES'); location.reload()").catch(() => {});
  await waitForLogin();
  check(await page.eval("document.documentElement.lang === 'en-US'"), "a disabled saved language falls back to the system default");
  check(await page.eval("!Array.from(document.querySelectorAll('#languageSelect option')).some((option) => option.value === 'es-ES')"), "a disabled language is removed from the picker");
  check(await page.eval("document.getElementById('languageSelect')?.value === 'en-US'"), "the picker selects the effective fallback");
  app.db.webmailLocales.disabled = [];
  await page.eval("localStorage.removeItem('a8m.language'); location.reload()").catch(() => {});
  await waitForLogin();

  console.log("\n[i18n] RTL and pseudo-locale login rendering");
  await page.eval("localStorage.setItem('a8m.language', 'ar'); location.reload()").catch(() => {});
  await waitForLogin();
  check(await page.eval("document.documentElement.lang === 'ar'"), "Arabic locale sets html lang=ar");
  check(await page.eval("document.documentElement.dir === 'rtl'"), "Arabic locale sets right-to-left direction");
  check(await page.eval("document.querySelector('.login-heading h2')?.textContent === 'تسجيل الدخول'"), "login heading is translated from the Arabic catalog");
  await page.screenshot(path.join(artifacts, "i18n-login-ar.png"));

  await page.eval("localStorage.setItem('a8m.language', 'en-XA'); location.reload()").catch(() => {});
  await waitForLogin();
  check(await page.eval("document.documentElement.lang === 'en-XA'"), "pseudo locale sets html lang=en-XA");
  check(await page.eval("document.querySelector('.login-heading h2')?.textContent.startsWith('[!! ')"), "pseudo locale renders login text through the catalog");
  check(await page.eval("!Array.from(document.querySelectorAll('#languageSelect option')).some((option) => option.value === 'en-XA')"), "pseudo locale stays out of the language selector");
  await page.screenshot(path.join(artifacts, "i18n-login-pseudo.png"));

  await page.setValue("#loginEmail", "avery.chen@a8m.example");
  await page.setValue("#loginPassword", "a8m-demo");
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  await page.click("#fillDemoCode");
  await page.click("#twoFactorSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);
  check(await page.eval("document.querySelector('.rail-label[data-i18n=\"nav.mail\"]')?.textContent.startsWith('[!! ')"), "pseudo locale renders navigation after sign-in");
  check(await page.eval("document.documentElement.lang === 'en-XA' && document.documentElement.dir === 'ltr'"), "pseudo locale keeps the signed-in shell LTR");
  await page.send("Emulation.setDeviceMetricsOverride", { width: 360, height: 800, deviceScaleFactor: 1, mobile: false });
  await new Promise((resolve) => setTimeout(resolve, 300));
  const pseudoOverflow = await page.eval("Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth");
  check(pseudoOverflow <= 1, `pseudo-locale expansion has no horizontal overflow at 360px (${pseudoOverflow}px)`);
  await page.screenshot(path.join(artifacts, "i18n-pseudo-360.png"));
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await new Promise((resolve) => setTimeout(resolve, 150));
  check(await page.eval("[...document.querySelectorAll('.folder-section')].every((node) => node.textContent.startsWith('[!! '))"), "pseudo locale renders the folder section labels through the catalog");
  check(await page.eval("document.getElementById('listCount')?.textContent.includes('[!! ')"), "pseudo locale renders the mail list count through the catalog");
  await page.eval("document.getElementById('searchInput').focus()");
  await page.waitFor(() => !document.getElementById("searchHints").hidden, 3000);
  check(await page.eval("document.getElementById('searchScope')?.textContent.startsWith('[!! ')"), "pseudo locale renders the search scope hint through the catalog");
  await page.eval("document.getElementById('searchInput').blur()");
  await page.click(".msg");
  await page.waitFor(() => Boolean(document.querySelector(".reader [data-trust-explain]")), 8000);
  check(await page.eval("document.querySelector('.reader [data-trust-explain]')?.textContent.startsWith('[!! ')"), "pseudo locale renders the reader trust action through the catalog");
  check(await page.eval("[...document.querySelectorAll('.reader [data-reply-message]')].every((node) => node.textContent.startsWith('[!! '))"), "pseudo locale renders the reader message actions through the catalog");
  await page.click(".rail-btn[data-task='tasks']");
  await page.waitFor(() => !document.getElementById("tasksView").hidden, 5000);
  check(await page.eval("document.querySelector('#taskFilters .chip')?.textContent.startsWith('[!! ')"), "pseudo locale renders the Tasks filters through the catalog");
  check(await page.eval("document.querySelector('#taskStats .task-stat-label')?.textContent.startsWith('[!! ')"), "pseudo locale renders the Tasks statistics through the catalog");
  await page.click("#newTaskBtn");
  await page.waitFor(() => Boolean(document.getElementById("taskTitleInput")), 4000);
  check(await page.eval("[...document.querySelectorAll('#taskDetail .form-field > span')].some((node) => node.textContent.startsWith('[!! '))"), "pseudo locale renders the task editor labels through the catalog");
  await page.click("[data-task-editor-close]");
  await page.screenshot(path.join(artifacts, "i18n-tasks-pseudo.png"));

  await page.eval("localStorage.setItem('a8m.language', 'en-US'); location.reload()").catch(() => {});
  await page.waitFor(() => document.readyState === "complete" && document.documentElement.lang === "en-US", 10000);
  check(await page.eval("document.documentElement.lang === 'en-US'"), "English locale restores html lang=en-US");
  check(await page.eval("document.documentElement.dir === 'ltr'"), "English locale restores left-to-right direction");

  const scriptFonts = await page.eval(`(() => {
    const root = document.documentElement;
    const needles = { ja: "Yu Gothic", "zh-Hans": "Microsoft YaHei", ko: "Malgun Gothic", hi: "Nirmala UI", he: "Noto Sans Hebrew" };
    const result = {};
    for (const [lang, needle] of Object.entries(needles)) {
      root.lang = lang;
      result[lang] = getComputedStyle(document.body).fontFamily.includes(needle);
    }
    root.lang = "en-US";
    return result;
  })()`);
  for (const [lang, ok] of Object.entries(scriptFonts)) check(ok, `script font stack applies for ${lang}`);

  console.log(`\nI18N e2e complete: ${failures} failures`);
} catch (error) {
  failures += 1;
  console.error("\nI18N E2E FAILED:", error.message);
  if (page) console.error("Page errors:", page.errors);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  await app.close();
  await fs.rm(tempDir, { recursive: true, force: true });
}
