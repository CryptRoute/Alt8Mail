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
const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-i18n-matrix-"));
const app = await createApp({ dataFile: path.join(tempDir, "db.json"), holdMs: 0, undoTtlMs: 1000 });
const address = await app.listen(0);
const base = `http://127.0.0.1:${address.port}`;
const manifest = JSON.parse(await fs.readFile(path.join(root, "public", "locales", "manifest.json"), "utf8"));
const locales = [];
const seen = new Set();
for (const meta of manifest.locales) {
  if (!meta || !meta.enabled || meta.status === "source" || meta.status === "pseudo" || meta.hidden) continue;
  const catalog = meta.catalog || meta.code;
  if (seen.has(catalog)) continue;
  seen.add(catalog);
  locales.push(meta);
}
let browser;
let page;
let failures = 0;

function check(condition, message) {
  try {
    assert.ok(condition, message);
    console.log(`  ✓ ${message}`);
  } catch (error) {
    failures += 1;
    console.error(`  ✗ ${message}`);
    throw error;
  }
}

try {
  browser = await launchBrowser({ headless: true, width: 1440, height: 1000 });
  page = await browser.newPage(base + "/");
  await page.waitFor(() => document.readyState === "complete", 10000);
  await page.waitFor(() => Boolean(document.getElementById("loginScreen")) && !document.getElementById("loginScreen").hidden, 10000);
  await page.setValue("#loginEmail", "avery.chen@a8m.example");
  await page.setValue("#loginPassword", "a8m-demo");
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("twoFactorForm").hidden, 8000);
  await page.click("#fillDemoCode");
  await page.click("#twoFactorSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden && document.querySelectorAll(".msg").length > 0, 12000);

  for (const meta of locales) {
    const code = meta.code;
    console.log(`\n[matrix] ${code} (${meta.catalog})`);
    await page.eval(`(() => {
      const select = document.getElementById("languageSelect");
      if (!select) return;
      select.value = ${JSON.stringify(code)};
      select.dispatchEvent(new Event("change", { bubbles: true }));
    })()`);
    await page.waitFor(`document.readyState === "complete" && document.documentElement.lang === ${JSON.stringify(code)}`, 12000);
    await page.waitFor(() => !document.getElementById("app")?.hidden && document.querySelectorAll(".msg").length > 0, 12000);
    check(await page.eval(`document.documentElement.lang === ${JSON.stringify(code)}`), `${code}: document language applied`);
    check(await page.eval(`document.documentElement.dir === ${JSON.stringify(meta.dir || "ltr")}`), `${code}: text direction ${meta.dir || "ltr"}`);
    const overflow = await page.eval("Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth");
    check(overflow <= 1, `${code}: no horizontal overflow at 1440px (${overflow}px)`);
    await page.screenshot(path.join(artifacts, `i18n-matrix-${meta.catalog}-wide.png`));
    await page.send("Emulation.setDeviceMetricsOverride", { width: 360, height: 800, deviceScaleFactor: 1, mobile: false });
    await new Promise((resolve) => setTimeout(resolve, 250));
    const narrowOverflow = await page.eval("Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth");
    check(narrowOverflow <= 1, `${code}: no horizontal overflow at 360px (${narrowOverflow}px)`);
    await page.screenshot(path.join(artifacts, `i18n-matrix-${meta.catalog}-360.png`));
    await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await new Promise((resolve) => setTimeout(resolve, 150));
  }

  await page.eval('localStorage.setItem("a8m.language", "en-US"); location.reload()').catch(() => {});
  await page.waitFor(() => document.readyState === "complete" && document.documentElement.lang === "en-US", 10000);
  check(await page.eval("document.documentElement.lang === 'en-US'"), "English restores after the matrix");
  console.log(`\nLocale matrix complete: ${locales.length} enabled locales, ${failures} failures`);
} catch (error) {
  failures += 1;
  console.error("\nI18N MATRIX FAILED:", error.message);
  if (page) console.error("Page errors:", page.errors);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  await app.close();
  await fs.rm(tempDir, { recursive: true, force: true });
}
