#!/usr/bin/env node
/*
 * Validate canonical locale catalogs and the generated runtime copies.
 *
 * Rules:
 *   - en.json is the superset. A key that exists only in a target catalog fails.
 *   - Missing target keys are reported as coverage, not a build failure.
 *   - Placeholder names and value shapes must match en.json.
 *   - Plural objects must include an "other" form. Missing CLDR categories are
 *     reported for incremental translation work.
 *   - public/locales must be exactly what scripts/i18n-sync.mjs generates.
 *
 * Usage:
 *   node scripts/i18n-check.mjs
 *   node scripts/i18n-check.mjs --strict   # missing keys fail
 */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { requiredPluralForms } from "./i18n-plural.mjs";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const strict = process.argv.includes("--strict");
const errors = [];
const warnings = [];
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function placeholders(value) {
  return [...String(value).matchAll(placeholderPattern)].map((match) => match[1]).sort();
}
function formPlaceholders(value, form) {
  const omitCount = form === "one" || form === "two";
  return placeholders(value).filter((name) => !(omitCount && name === "count"));
}
function markup(value) {
  const text = String(value);
  return {
    tags: [...text.matchAll(/<[^>]+>/g)].map((match) => match[0]).sort().join("|"),
    urls: [...text.matchAll(/https?:\/\/[^\s<>"']+/g)].map((match) => match[0]).sort().join("|")
  };
}
function compareMarkup(locale, key, source, translated) {
  if (markup(source).tags !== markup(translated).tags) errors.push(`${locale}: ${key} HTML tags do not match en.json`);
  if (markup(source).urls !== markup(translated).urls) errors.push(`${locale}: ${key} URLs do not match en.json`);
}

function isPlural(value) {
  return value && typeof value === "object" && !Array.isArray(value);
}

function compareShape(key, english, translated, locale) {
  if (isPlural(english) !== isPlural(translated)) {
    errors.push(`${locale}: ${key} has ${isPlural(translated) ? "plural object" : "string"} value, expected ${isPlural(english) ? "plural object" : "string"}`);
    return;
  }
  if (isPlural(english)) {
    if (!Object.prototype.hasOwnProperty.call(translated, "other")) {
      errors.push(`${locale}: ${key} plural object is missing the "other" form`);
    }
    const forms = Object.keys(translated);
    for (const form of forms) {
      const sourceForm = Object.prototype.hasOwnProperty.call(english, form) ? english[form] : english.other;
      const expected = formPlaceholders(sourceForm, form).join(",");
      const actual = formPlaceholders(translated[form], form).join(",");
      if (expected !== actual) {
        errors.push(`${locale}: ${key}.${form} placeholders are {${actual}}, expected {${expected}}`);
      }
      compareMarkup(locale, `${key}.${form}`, sourceForm, translated[form]);
    }
    try {
      const localeValue = locale === "en-XA" ? "en" : locale;
      const required = requiredPluralForms(localeValue);
      const missing = required.filter((form) => !forms.includes(form));
      if (missing.length) warnings.push(`${locale}: ${key} is missing plural categories: ${missing.join(", ")}`);
    } catch {
      // Locale validity is checked by the manifest pass.
    }
    return;
  }
  const expected = placeholders(english).join(",");
  const actual = placeholders(translated).join(",");
  if (expected !== actual) {
    errors.push(`${locale}: ${key} placeholders are {${actual}}, expected {${expected}}`);
  }
  compareMarkup(locale, key, english, translated);
}

function validateCatalogValue(locale, key, value) {
  if (typeof value === "string") {
    if (!value.trim()) errors.push(`${locale}: ${key} is empty`);
    return;
  }
  if (isPlural(value)) {
    if (!Object.keys(value).length) errors.push(`${locale}: ${key} has an empty plural object`);
    for (const [form, text] of Object.entries(value)) {
      if (typeof text !== "string" || !text.trim()) {
        errors.push(`${locale}: ${key}.${form} must be a non-empty string`);
      }
    }
    return;
  }
  errors.push(`${locale}: ${key} must be a string or plural object`);
}

const manifestFile = path.join(localeRoot, "manifest.json");
if (!fs.existsSync(manifestFile)) {
  console.error("Missing locales/manifest.json");
  process.exit(1);
}
const manifest = readJson(manifestFile);
if (!Array.isArray(manifest.locales) || !manifest.locales.length) {
  errors.push("manifest.locales is empty");
}
if (!manifest.default) errors.push("manifest.default is missing");

const codes = new Set();
for (const meta of manifest.locales) {
  if (!meta || typeof meta !== "object") {
    errors.push("manifest contains a non-object locale entry");
    continue;
  }
  const label = meta.code || "(missing code)";
  if (!meta.code || !meta.catalog || !meta.label) {
    errors.push(`${label}: manifest entry needs code, catalog and label`);
    continue;
  }
  if (codes.has(meta.code)) errors.push(`${label}: duplicate locale code`);
  codes.add(meta.code);
  if (!["ltr", "rtl"].includes(meta.dir || "ltr")) errors.push(`${label}: invalid dir ${meta.dir}`);
  if (!["source", "reviewed", "draft", "planned", "pseudo"].includes(meta.status || "")) {
    errors.push(`${label}: invalid status ${meta.status}`);
  }
  if (meta.status !== "pseudo") {
    try {
      Intl.getCanonicalLocales(meta.code);
    } catch {
      errors.push(`${label}: not a valid BCP 47 locale`);
    }
  }
  if (meta.enabled && !fs.existsSync(path.join(localeRoot, `${meta.catalog}.json`)) && meta.status !== "pseudo") {
    errors.push(`${label}: enabled locale has no locales/${meta.catalog}.json`);
  }
}
if (manifest.default && !codes.has(manifest.default)) {
  errors.push(`manifest.default ${manifest.default} is not in manifest.locales`);
}

const englishFile = path.join(localeRoot, "en.json");
if (!fs.existsSync(englishFile)) {
  console.error("Missing locales/en.json");
  process.exit(1);
}
const english = readJson(englishFile);
const englishKeys = Object.keys(english);
if (!englishKeys.length) errors.push("locales/en.json is empty");
for (const [key, value] of Object.entries(english)) {
  if (!/^[a-z0-9]+(?:[._-][a-zA-Z0-9]+)+$/.test(key)) {
    warnings.push(`en: key "${key}" does not use the surface.component.element convention`);
  }
  validateCatalogValue("en", key, value);
  if (isPlural(value) && !Object.prototype.hasOwnProperty.call(value, "other")) {
    errors.push(`en: ${key} plural object is missing the "other" form`);
  }
}

const usedKeys = new Set();
const scanFiles = [];
function collectSourceFiles(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    if (entry.name === "locales" || entry.name === "vendor" || entry.name.startsWith(".")) continue;
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) collectSourceFiles(file);
    else if (/\.(?:js|mjs|html)$/.test(entry.name)) scanFiles.push(file);
  }
}
collectSourceFiles(path.join(root, "public"));
for (const file of scanFiles) {
  const source = fs.readFileSync(file, "utf8");
  for (const match of source.matchAll(/data-i18n(?:-placeholder|-aria-label|-title)?="([^"]+)"/g)) {
    usedKeys.add(match[1]);
  }
  for (const match of source.matchAll(/\bt[n]?\(\s*["']([^"']+)["']/g)) {
    usedKeys.add(match[1]);
  }
}
for (const key of usedKeys) {
  if (!(key in english)) errors.push(`UI references missing translation key: ${key}`);
}

const coverage = [];
const seenCatalogs = new Set();
for (const meta of manifest.locales) {
  if (!meta || !meta.catalog || meta.status === "pseudo") continue;
  const file = path.join(localeRoot, `${meta.catalog}.json`);
  if (!fs.existsSync(file)) {
    if (meta.enabled) errors.push(`${meta.code}: missing locales/${meta.catalog}.json`);
    continue;
  }
  if (seenCatalogs.has(meta.catalog)) continue;
  seenCatalogs.add(meta.catalog);
  let catalog;
  try {
    catalog = readJson(file);
  } catch (error) {
    errors.push(`${meta.catalog}: invalid JSON (${error.message})`);
    continue;
  }
  if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) {
    errors.push(`${meta.catalog}: catalog must be a JSON object`);
    continue;
  }
  const translated = 0;
  let present = 0;
  for (const [key, value] of Object.entries(catalog)) {
    if (!(key in english)) {
      errors.push(`${meta.catalog}: stale key ${key} is not in en.json`);
      continue;
    }
    present += 1;
    validateCatalogValue(meta.catalog, key, value);
    compareShape(key, english[key], value, meta.catalog);
  }
  const missing = englishKeys.filter((key) => !(key in catalog));
  coverage.push({ catalog: meta.catalog, present, missing: missing.length, total: englishKeys.length });
  if (missing.length && strict) {
    errors.push(`${meta.catalog}: ${missing.length} missing key(s) (strict mode)`);
  }
  void translated;
}

const draftRoot = path.join(localeRoot, "drafts");
if (fs.existsSync(draftRoot)) {
  for (const name of fs.readdirSync(draftRoot).filter((entry) => entry.endsWith(".json") && !entry.endsWith(".meta.json")).sort()) {
    const catalogName = name.slice(0, -5);
    const meta = manifest.locales.find((item) => item && item.catalog === catalogName && item.status !== "source" && item.status !== "pseudo");
    if (!meta) {
      errors.push(`drafts/${name}: no translatable locale uses catalog ${catalogName}`);
      continue;
    }
    let draft;
    try { draft = readJson(path.join(draftRoot, name)); } catch (error) {
      errors.push(`drafts/${name}: invalid JSON (${error.message})`);
      continue;
    }
    if (!draft || typeof draft !== "object" || Array.isArray(draft)) {
      errors.push(`drafts/${name}: draft must be a JSON object`);
      continue;
    }
    for (const [key, value] of Object.entries(draft)) {
      if (!(key in english)) { errors.push(`drafts/${name}: key ${key} is not in en.json`); continue; }
      validateCatalogValue(`drafts/${catalogName}`, key, value);
      compareShape(key, english[key], value, meta.code);
    }
  }
}

const sync = spawnSync(process.execPath, [path.join(root, "scripts", "i18n-sync.mjs"), "--check"], {
  encoding: "utf8"
});
if (sync.status !== 0) {
  const output = `${sync.stdout || ""}${sync.stderr || ""}`.trim();
  errors.push(output || "public/locales is out of sync with locales/");
}

if (warnings.length) {
  const unique = [...new Set(warnings)];
  console.warn(`i18n warnings (${unique.length}):`);
  for (const warning of unique.slice(0, 40)) console.warn(`- ${warning}`);
  if (unique.length > 40) console.warn(`- … ${unique.length - 40} more`);
}
console.log("i18n coverage:");
for (const row of coverage.sort((a, b) => b.present - a.present || a.catalog.localeCompare(b.catalog))) {
  const percent = row.total ? ((row.present / row.total) * 100).toFixed(1) : "0.0";
  console.log(`  ${row.catalog.padEnd(8)} ${String(row.present).padStart(4)}/${row.total} (${percent}%)${row.missing ? ` · ${row.missing} missing` : ""}`);
}

if (errors.length) {
  console.error(`\ni18n check failed:\n- ${errors.join("\n- ")}`);
  process.exit(1);
}
console.log(`\ni18n check passed: ${englishKeys.length} canonical keys, ${usedKeys.size} referenced UI keys`);
