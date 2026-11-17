#!/usr/bin/env node
/*
 * Promote a reviewed machine draft into a runtime locale catalog.
 *
 * A draft cannot be promoted unless every English key is present and every
 * value matches the source shape, placeholders and plural forms. Promotion
 * without --reviewed-by writes the catalog with status "draft" and keeps the
 * locale disabled; --enable is only allowed together with a reviewer.
 *
 * Usage:
 *   node scripts/i18n-promote.mjs --locale de --reviewed-by "A. Reviewer" --enable
 *   node scripts/i18n-promote.mjs --locale ja --reviewed-by "A. Reviewer"
 *   node scripts/i18n-promote.mjs --locale de --check
 */
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { requiredPluralForms } from "./i18n-plural.mjs";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const draftRoot = path.join(localeRoot, "drafts");
const englishFile = path.join(localeRoot, "en.json");
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;
const manifestFile = path.join(localeRoot, "manifest.json");

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] || fallback : fallback;
}
const requested = arg("--locale", "");
const reviewer = arg("--reviewed-by", "").trim();
const enable = process.argv.includes("--enable");
const checkOnly = process.argv.includes("--check");

if (!requested) {
  console.error("Pass --locale <code|list>");
  process.exit(2);
}
if (enable && !reviewer) {
  console.error("--enable requires --reviewed-by because unreviewed catalogs must stay disabled");
  process.exit(2);
}

const english = JSON.parse(fs.readFileSync(englishFile, "utf8"));
const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
const sourceHash = crypto.createHash("sha256").update(JSON.stringify(english)).digest("hex").slice(0, 16);
const placeholder = (value, form = "") => [...String(value).matchAll(placeholderPattern)]
  .map((match) => match[1])
  .filter((name) => !((form === "one" || form === "two") && name === "count"))
  .sort().join(",");
const markup = (value) => ({
  tags: [...String(value).matchAll(/<[^>]+>/g)].map((match) => match[0]).sort().join("|"),
  urls: [...String(value).matchAll(/https?:\/\/[^\s<>"']+/g)].map((match) => match[0]).sort().join("|")
});
const isPlural = (value) => value && typeof value === "object" && !Array.isArray(value);

function compareText(key, source, value, form = "") {
  if (placeholder(source, form) !== placeholder(value, form)) return "placeholder mismatch";
  if (markup(source).tags !== markup(value).tags) return "HTML tag mismatch";
  if (markup(source).urls !== markup(value).urls) return "URL mismatch";
  return "";
}

function validate(key, source, value) {
  if (typeof source === "string") {
    if (typeof value !== "string" || !value.trim()) return "not a non-empty string";
    return compareText(key, source, value);
  }
  if (!isPlural(value)) return "expected plural object";
  if (!Object.prototype.hasOwnProperty.call(value, "other")) return "missing other form";
  for (const form of Object.keys(value)) {
    if (typeof value[form] !== "string" || !value[form].trim()) return `empty ${form} form`;
    const sourceForm = form in source ? source[form] : source.other;
    const issue = compareText(key, sourceForm, value[form], form);
    if (issue) return `${form} ${issue.toLowerCase()}`;
  }
  return "";
}

const targets = requested.split(",").map((value) => value.trim()).filter(Boolean);
const results = [];
for (const target of targets) {
  const meta = manifest.locales.find((entry) => entry && (entry.code === target || entry.catalog === target));
  if (!meta || meta.status === "source" || meta.status === "pseudo") {
    console.error(`${target}: unknown or non-translatable locale`);
    process.exitCode = 1;
    continue;
  }
  const catalog = meta.catalog || meta.code;
  const draftFile = path.join(draftRoot, `${catalog}.json`);
  const draftMetaFile = path.join(draftRoot, `${catalog}.meta.json`);
  if (!fs.existsSync(draftFile)) {
    console.error(`${catalog}: no draft at ${path.relative(root, draftFile)}`);
    process.exitCode = 1;
    continue;
  }
  const draft = JSON.parse(fs.readFileSync(draftFile, "utf8"));
  const draftMeta = fs.existsSync(draftMetaFile) ? JSON.parse(fs.readFileSync(draftMetaFile, "utf8")) : {};
  const problems = [];
  const keys = Object.keys(english);
  const requiredForms = requiredPluralForms(meta.code);
  for (const key of keys) {
    if (!(key in draft)) { problems.push(`${key}: missing`); continue; }
    const issue = validate(key, english[key], draft[key]);
    if (issue) problems.push(`${key}: ${issue}`);
    if (isPlural(english[key]) && isPlural(draft[key])) {
      const missing = requiredForms.filter((form) => !(form in draft[key]));
      if (missing.length) problems.push(`${key}: missing plural categories ${missing.join(", ")}`);
    }
  }
  for (const key of Object.keys(draft)) {
    if (!(key in english)) problems.push(`${key}: not in en.json`);
  }
  const coverage = keys.length ? ((Object.keys(draft).filter((key) => key in english).length / keys.length) * 100).toFixed(1) : "0.0";
  if (draftMeta.sourceHash && draftMeta.sourceHash !== sourceHash) {
    problems.push(`draft was generated from an older en.json (${draftMeta.sourceHash} != ${sourceHash}); regenerate it before promoting`);
  }
  if (problems.length) {
    console.error(`${catalog}: ${problems.length} problem(s)`);
    for (const problem of problems.slice(0, 40)) console.error(`- ${problem}`);
    if (problems.length > 40) console.error(`- … ${problems.length - 40} more`);
    process.exitCode = 1;
    continue;
  }
  const identical = keys.filter((key) => JSON.stringify(english[key]) === JSON.stringify(draft[key])).length;
  results.push({ meta, catalog, draft, draftMeta, coverage, identical });
  if (checkOnly) {
    console.log(`${catalog}: ready · ${coverage}% · ${identical} value(s) identical to English${reviewer ? ` · reviewer ${reviewer}` : ""}`);
    continue;
  }
  if (meta.enabled && !reviewer) {
    console.error(`${catalog}: locale is enabled; promoting unreviewed machine text would change a released locale. Pass --reviewed-by.`);
    process.exitCode = 1;
    continue;
  }
  const liveFile = path.join(localeRoot, `${catalog}.json`);
  fs.writeFileSync(liveFile, JSON.stringify(draft, null, 2) + "\n");
  meta.status = reviewer ? "reviewed" : "draft";
  meta.translationStatus = reviewer ? "reviewed" : "machine-draft";
  meta.reviewedAt = reviewer ? new Date().toISOString().slice(0, 10) : undefined;
  meta.reviewedBy = reviewer || undefined;
  meta.sourceHash = sourceHash;
  if (enable) meta.enabled = true;
  if (!reviewer) meta.enabled = false;
  fs.writeFileSync(manifestFile, JSON.stringify(manifest, null, 2) + "\n");
  console.log(`${catalog}: promoted to locales/${catalog}.json · ${coverage}% · status ${meta.status} · ${meta.enabled ? "enabled" : "disabled"}`);
  if (identical) console.log(`  note: ${identical} value(s) are identical to English; confirm they are brands, acronyms or intentional borrowings.`);
}
if (results.length) {
  console.log("\nRun `node scripts/i18n-sync.mjs` to regenerate public/locales after promotion.");
}
