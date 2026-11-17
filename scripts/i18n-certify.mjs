#!/usr/bin/env node
/*
 * Locale release certification.
 *
 * A locale is certified only when its runtime catalog has every English key,
 * passes structural checks, is marked reviewed and is enabled. Draft-ready
 * locales are reported separately; partial enabled locales are release
 * blockers under --strict.
 *
 * Usage:
 *   node scripts/i18n-certify.mjs
 *   node scripts/i18n-certify.mjs --strict
 *   node scripts/i18n-certify.mjs --require ar,de,fr --strict
 *   node scripts/i18n-certify.mjs --json
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;
const english = JSON.parse(fs.readFileSync(path.join(localeRoot, "en.json"), "utf8"));
const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const total = Object.keys(english).length;
const strict = process.argv.includes("--strict");
const json = process.argv.includes("--json");
const requireIndex = process.argv.indexOf("--require");
const required = new Set(requireIndex >= 0 ? String(process.argv[requireIndex + 1] || "").split(",").map((value) => value.trim()).filter(Boolean) : []);

const placeholderNames = (value) => [...String(value).matchAll(placeholderPattern)].map((match) => match[1]).sort();
const placeholders = (value) => placeholderNames(value).join(",");
const isPlural = (value) => value && typeof value === "object" && !Array.isArray(value);

function valueProblem(source, value) {
  if (typeof source === "string") {
    if (typeof value !== "string" || !value.trim()) return "empty or non-string";
    if (placeholders(source) !== placeholders(value)) return "placeholder mismatch";
    return "";
  }
  if (!isPlural(value)) return "expected plural object";
  if (!Object.prototype.hasOwnProperty.call(value, "other")) return "missing other form";
  for (const [form, text] of Object.entries(value)) {
    if (typeof text !== "string" || !text.trim()) return `${form} is empty`;
    const sourceForm = form in source ? source[form] : source.other;
    const omitCount = form === "zero" || form === "one" || form === "two";
    const filtered = (value) => (omitCount ? placeholderNames(value).filter((name) => name !== "count") : placeholderNames(value)).join(",");
    if (filtered(sourceForm) !== filtered(text)) return `${form} placeholder mismatch`;
  }
  return "";
}

function readCatalog(catalog) {
  const file = path.join(localeRoot, `${catalog}.json`);
  if (!fs.existsSync(file)) return null;
  try { return JSON.parse(fs.readFileSync(file, "utf8")); } catch { return null; }
}

const rows = [];
for (const meta of manifest.locales) {
  if (!meta || meta.status === "source" || meta.status === "pseudo") continue;
  const catalog = meta.catalog || meta.code;
  const live = readCatalog(catalog);
  const liveKeys = live ? Object.keys(live).filter((key) => key in english) : [];
  const problems = [];
  if (live) {
    for (const key of Object.keys(live)) {
      if (!(key in english)) problems.push(`${key}: stale key`);
    }
    for (const key of liveKeys) {
      const issue = valueProblem(english[key], live[key]);
      if (issue) problems.push(`${key}: ${issue}`);
    }
  }
  const draftFile = path.join(localeRoot, "drafts", `${catalog}.json`);
  let draftKeys = 0;
  const draftProblems = [];
  if (fs.existsSync(draftFile)) {
    try {
      const draft = JSON.parse(fs.readFileSync(draftFile, "utf8"));
      draftKeys = Object.keys(draft).filter((key) => key in english).length;
      for (const key of Object.keys(draft)) {
        if (!(key in english)) { draftProblems.push(`${key}: stale key`); continue; }
        const issue = valueProblem(english[key], draft[key]);
        if (issue) draftProblems.push(`${key}: ${issue}`);
      }
    } catch {}
  }
  let state = "planned";
  if (meta.enabled && liveKeys.length === total && problems.length === 0 && meta.status === "reviewed") state = "certified";
  else if (meta.enabled) state = "partial-enabled";
  else if (meta.draftReady && draftKeys === total && draftProblems.length === 0) state = "ready-for-review";
  else if (draftKeys > 0) state = "draft";
  rows.push({
    code: meta.code, catalog, status: meta.status, enabled: Boolean(meta.enabled),
    dir: meta.dir || "ltr", wave: meta.wave || "", live: liveKeys.length, draft: draftKeys, state,
    required: required.has(meta.code) || required.has(catalog), problems, draftProblems
  });
}

const failures = [];
if (strict) {
  for (const row of rows) {
    if (row.state === "partial-enabled") failures.push(`${row.code}: enabled with incomplete catalog (${row.live}/${total})`);
    if (row.enabled && row.problems.length) failures.push(`${row.code}: enabled catalog has ${row.problems.length} structural problem(s)`);
  }
}
for (const code of required) {
  const row = rows.find((item) => item.code === code || item.catalog === code);
  if (!row || row.state !== "certified") failures.push(`${code}: required locale is not certified`);
}

if (json) {
  console.log(JSON.stringify({ total, rows, failures }, null, 2));
} else {
  const header = ["locale", "state", "live", "draft", "review", "enabled", "dir"];
  const cells = rows.map((row) => [
    row.code, row.state, `${row.live}/${total}`, `${row.draft}/${total}`,
    row.status, row.enabled ? "yes" : "no", row.dir
  ]);
  const widths = header.map((title, index) => Math.max(title.length, ...cells.map((row) => row[index].length)));
  const format = (row) => row.map((value, index) => String(value).padEnd(widths[index])).join("  ").trimEnd();
  console.log(format(header));
  for (const cell of cells) console.log(format(cell));
  console.log(`\nCertification: ${rows.filter((row) => row.state === "certified").length} certified · ${rows.filter((row) => row.state === "ready-for-review").length} ready for review · ${rows.filter((row) => row.state === "partial-enabled").length} partial enabled.`);
  if (failures.length) {
    console.error(`\nCertification failures (${failures.length}):`);
    for (const failure of failures) console.error(`- ${failure}`);
  }
}
if (failures.length) process.exit(1);
