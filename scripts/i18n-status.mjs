#!/usr/bin/env node
/*
 * Print translation coverage and rollout status per locale.
 *
 * Usage:
 *   node scripts/i18n-status.mjs
 *   node scripts/i18n-status.mjs --missing de
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const english = JSON.parse(fs.readFileSync(path.join(localeRoot, "en.json"), "utf8"));
const englishKeys = Object.keys(english);
const missingArgIndex = process.argv.indexOf("--missing");
const missingCatalog = missingArgIndex >= 0 ? process.argv[missingArgIndex + 1] : "";
const showDraft = process.argv.includes("--draft");

function readDraft(meta) {
  const draftFile = path.join(localeRoot, "drafts", `${meta.catalog || meta.code}.json`);
  if (!fs.existsSync(draftFile)) return null;
  try { return JSON.parse(fs.readFileSync(draftFile, "utf8")); } catch { return null; }
}

const rows = [];
for (const meta of manifest.locales) {
  if (!meta || !meta.code) continue;
  const file = path.join(localeRoot, `${meta.catalog || meta.code}.json`);
  const draft = readDraft(meta);
  const draftPresent = draft ? englishKeys.filter((key) => key in draft).length : null;
  if (!fs.existsSync(file)) {
    rows.push({ ...meta, present: 0, missing: englishKeys.length, missingKeys: englishKeys, absent: true, draftPresent });
    continue;
  }
  const catalog = JSON.parse(fs.readFileSync(file, "utf8"));
  const missingKeys = englishKeys.filter((key) => !(key in catalog));
  const staleKeys = Object.keys(catalog).filter((key) => !(key in english));
  rows.push({ ...meta, present: englishKeys.length - missingKeys.length, missing: missingKeys.length, stale: staleKeys.length, missingKeys, draftPresent });
}

const header = ["locale", "catalog", "dir", "status", "wave", "enabled", "coverage"];
if (showDraft) header.push("draft");
const cells = rows.map((row) => {
  const percent = englishKeys.length ? ((row.present / englishKeys.length) * 100).toFixed(1) : "0.0";
  const coverage = row.absent ? "not started" : `${row.present}/${englishKeys.length} (${percent}%)${row.missing ? ` · ${row.missing} missing` : ""}${row.stale ? ` · ${row.stale} stale` : ""}`;
  const values = [
    row.code,
    row.catalog || "",
    row.dir || "ltr",
    row.status || "",
    String(row.wave || ""),
    row.enabled ? "yes" : "no",
    coverage
  ];
  if (showDraft) {
    const draftPercent = row.draftPresent != null && englishKeys.length ? ((row.draftPresent / englishKeys.length) * 100).toFixed(1) : null;
    values.push(row.draftPresent != null ? `${row.draftPresent}/${englishKeys.length} (${draftPercent}%)` : "—");
  }
  return values;
});
const widths = header.map((title, index) => Math.max(title.length, ...cells.map((row) => String(row[index]).length)));
const formatRow = (row) => row.map((value, index) => String(value).padEnd(widths[index])).join("  ").trimEnd();
console.log(formatRow(header));
for (const row of cells) console.log(formatRow(row));

if (missingCatalog) {
  const row = rows.find((item) => item.catalog === missingCatalog || item.code === missingCatalog);
  if (!row) {
    console.error(`Unknown catalog: ${missingCatalog}`);
    process.exit(1);
  }
  console.log(`\nMissing keys for ${missingCatalog}:`);
  for (const key of row.missingKeys) console.log(`  ${key}`);
}
