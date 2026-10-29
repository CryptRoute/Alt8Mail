#!/usr/bin/env node
// Validate the Alt8Mail locale catalogs before a release.
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "public", "locales");
const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const defaultBase = manifest.default || "en";
const errors = [];

if (!Array.isArray(manifest.locales) || manifest.locales.length === 0) {
  errors.push("manifest.locales is empty");
}
const values = new Set();
for (const locale of manifest.locales) {
  if (!locale.code || !locale.value || !locale.label) {
    errors.push("locale entry is missing code, value or label");
    continue;
  }
  if (values.has(locale.value)) errors.push(`duplicate locale value: ${locale.value}`);
  values.add(locale.value);
  if (!["ltr", "rtl"].includes(locale.dir || "ltr")) {
    errors.push(`invalid direction for ${locale.value}: ${locale.dir}`);
  }
}

const canonical = JSON.parse(
  fs.readFileSync(path.join(localeRoot, defaultBase, "common.json"), "utf8")
);
const canonicalKeys = Object.keys(canonical);
const bases = [...new Set(manifest.locales.map((locale) => locale.code))];

const usedKeys = new Set();
for (const file of ["public/index.html", "public/app.js"]) {
  const source = fs.readFileSync(path.join(root, file), "utf8");
  for (const match of source.matchAll(/data-i18n(?:-placeholder|-aria-label|-title)?="([^"]+)"/g)) {
    usedKeys.add(match[1]);
  }
  for (const match of source.matchAll(/\bt[n]?\(\s*["']([^"']+)["']/g)) {
    usedKeys.add(match[1]);
  }
}
for (const key of usedKeys) {
  if (!(key in canonical)) errors.push(`UI references missing translation key: ${key}`);
}

for (const base of bases) {
  const file = path.join(localeRoot, base, "common.json");
  if (!fs.existsSync(file)) {
    errors.push(`missing catalog: ${base}/common.json`);
    continue;
  }
  let catalog;
  try {
    catalog = JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (error) {
    errors.push(`${base}/common.json is not valid JSON: ${error.message}`);
    continue;
  }
  for (const key of canonicalKeys) {
    if (!(key in catalog)) errors.push(`${base}/common.json is missing ${key}`);
  }
  for (const [key, value] of Object.entries(catalog)) {
    if (typeof value !== "string" || value.trim() === "") {
      errors.push(`${base}/common.json has an empty value for ${key}`);
    }
    if (!(key in canonical)) errors.push(`${base}/common.json has an unknown key ${key}`);
  }
}

if (errors.length) {
  console.error(`Locale validation failed:\n- ${errors.join("\n- ")}`);
  process.exit(1);
}
console.log(`Locale catalogs OK: ${bases.length} base languages, ${canonicalKeys.length} keys each.`);
