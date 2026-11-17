#!/usr/bin/env node
/*
 * Locale payload and cache budget report.
 *
 * Budgets protect the mobile first load: catalogs are fetched after the shell
 * and kept in the service-worker cache, so gzip size matters more than raw
 * JSON on the wire.
 *
 * Usage:
 *   node scripts/i18n-perf.mjs
 *   node scripts/i18n-perf.mjs --fail
 */
import fs from "node:fs";
import path from "node:path";
import zlib from "node:zlib";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "public", "locales");
const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const enabledCatalogs = new Set(manifest.locales.filter((meta) => meta && meta.enabled).map((meta) => meta.catalog || meta.code));
// The full-core sweep catalogues every visible product string, so one locale
// is larger than the original shell-only catalog. The per-catalog budget still
// protects the single locale a browser downloads; the enabled total is a
// repository/release size guard, not a first-load payload.
const budget = {
  perCatalogRaw: 2 * 1024 * 1024,
  perCatalogGzip: 160 * 1024,
  enabledTotalRaw: 24 * 1024 * 1024,
  enabledTotalGzip: 4 * 1024 * 1024,
  manifestGzip: 30 * 1024
};
const fail = process.argv.includes("--fail");
const rows = [];
let enabledRaw = 0;
let enabledGzip = 0;
let totalRaw = 0;
let totalGzip = 0;

for (const name of fs.readdirSync(localeRoot).filter((entry) => entry.endsWith(".json") && entry !== "manifest.json").sort()) {
  const file = path.join(localeRoot, name);
  const buffer = fs.readFileSync(file);
  const gzip = zlib.gzipSync(buffer, { level: 9 }).length;
  const catalog = name.slice(0, -5);
  totalRaw += buffer.length;
  totalGzip += gzip;
  if (enabledCatalogs.has(catalog)) {
    enabledRaw += buffer.length;
    enabledGzip += gzip;
  }
  rows.push({ name, catalog, raw: buffer.length, gzip, enabled: enabledCatalogs.has(catalog) });
}
const manifestBuffer = fs.readFileSync(path.join(localeRoot, "manifest.json"));
const manifestGzip = zlib.gzipSync(manifestBuffer, { level: 9 }).length;

const problems = [];
for (const row of rows) {
  if (row.raw > budget.perCatalogRaw) problems.push(`${row.name}: ${row.raw} B raw exceeds ${budget.perCatalogRaw} B`);
  if (row.gzip > budget.perCatalogGzip) problems.push(`${row.name}: ${row.gzip} B gzip exceeds ${budget.perCatalogGzip} B`);
}
if (enabledRaw > budget.enabledTotalRaw) problems.push(`enabled catalogs: ${enabledRaw} B raw exceeds ${budget.enabledTotalRaw} B`);
if (enabledGzip > budget.enabledTotalGzip) problems.push(`enabled catalogs: ${enabledGzip} B gzip exceeds ${budget.enabledTotalGzip} B`);
if (manifestGzip > budget.manifestGzip) problems.push(`manifest: ${manifestGzip} B gzip exceeds ${budget.manifestGzip} B`);

const header = ["catalog", "enabled", "raw", "gzip"];
const cells = rows.map((row) => [row.name, row.enabled ? "yes" : "no", `${(row.raw / 1024).toFixed(1)} KB`, `${(row.gzip / 1024).toFixed(1)} KB`]);
const widths = header.map((title, index) => Math.max(title.length, ...cells.map((row) => row[index].length)));
const format = (row) => row.map((value, index) => String(value).padEnd(widths[index])).join("  ").trimEnd();
console.log(format(header));
for (const cell of cells) console.log(format(cell));
console.log(`\nEnabled total: ${(enabledRaw / 1024).toFixed(1)} KB raw · ${(enabledGzip / 1024).toFixed(1)} KB gzip`);
console.log(`All catalogs: ${(totalRaw / 1024).toFixed(1)} KB raw · ${(totalGzip / 1024).toFixed(1)} KB gzip`);
console.log(`Manifest: ${(manifestGzip / 1024).toFixed(1)} KB gzip`);
if (problems.length) {
  console.error(`\nLocale budget failures (${problems.length}):`);
  for (const problem of problems) console.error(`- ${problem}`);
  if (fail) process.exit(1);
}
