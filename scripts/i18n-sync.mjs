#!/usr/bin/env node
/*
 * Build the runtime locale catalogs from the canonical `locales/` tree.
 *
 * Canonical files are edited by hand and by the translation pipeline:
 *   locales/manifest.json
 *   locales/en.json
 *   locales/<catalog>.json
 *
 * Generated runtime copies (never edit):
 *   public/locales/manifest.json
 *   public/locales/<catalog>.json
 *
 * Usage:
 *   node scripts/i18n-sync.mjs
 *   node scripts/i18n-sync.mjs --check
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const sourceRoot = path.join(root, "locales");
const targetRoot = path.join(root, "public", "locales");
const check = process.argv.includes("--check");
const errors = [];

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

const manifest = readJson(path.join(sourceRoot, "manifest.json"));
if (!Array.isArray(manifest.locales) || !manifest.locales.length) {
  console.error("locales/manifest.json has no locales");
  process.exit(1);
}

const sourceCatalog = (catalog) => path.join(sourceRoot, `${catalog}.json`);
const generated = new Map();

function accent(value) {
  const map = {
    a: "á", b: "ƀ", c: "ç", d: "ð", e: "é", f: "ƒ", g: "ĝ", h: "ĥ", i: "í",
    j: "ĵ", k: "ķ", l: "ļ", m: "ɱ", n: "ñ", o: "ó", p: "þ", q: "ɋ", r: "ŕ",
    s: "š", t: "ţ", u: "ú", v: "ṽ", w: "ŵ", x: "ẋ", y: "ý", z: "ž",
    A: "Á", B: "Ɓ", C: "Ç", D: "Ð", E: "É", F: "Ƒ", G: "Ĝ", H: "Ĥ", I: "Í",
    J: "Ĵ", K: "Ķ", L: "Ļ", M: "Ṁ", N: "Ñ", O: "Ó", P: "Þ", Q: "Q", R: "Ŕ",
    S: "Š", T: "Ţ", U: "Ú", V: "Ṽ", W: "Ŵ", X: "Ẋ", Y: "Ý", Z: "Ž"
  };
  return String(value).split("").map((char) => map[char] || char).join("");
}

function pseudoValue(value, key) {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return Object.fromEntries(Object.entries(value).map(([form, text]) => [form, pseudoValue(text, `${key}.${form}`)]));
  }
  if (typeof value !== "string") return value;
  // Keep interpolation tokens intact so placeholder checks stay meaningful.
  const parts = value.split(/(\{[A-Za-z0-9_]+\})/g);
  const accented = parts.map((part) => (part.startsWith("{") && part.endsWith("}") ? part : accent(part))).join("");
  const targetLength = Math.max(accented.length + 6, Math.ceil(accented.length * 1.35));
  const padding = " ·".repeat(Math.max(1, Math.ceil((targetLength - accented.length) / 2)));
  return `[!! ${accented}${padding} !!]`;
}

function catalogFor(meta) {
  if (meta.catalog === "en-XA") {
    const english = readJson(sourceCatalog("en"));
    return Object.fromEntries(Object.entries(english).map(([key, value]) => [key, pseudoValue(value, key)]));
  }
  const file = sourceCatalog(meta.catalog);
  if (!fs.existsSync(file)) return null;
  return readJson(file);
}

for (const meta of manifest.locales) {
  if (!meta || !meta.code || !meta.catalog) {
    errors.push("manifest entry is missing code or catalog");
    continue;
  }
  if (!["ltr", "rtl"].includes(meta.dir || "ltr")) {
    errors.push(`${meta.code}: invalid direction ${meta.dir}`);
  }
  const catalog = catalogFor(meta);
  if (!catalog) {
    if (meta.enabled) errors.push(`${meta.code}: enabled locale has no locales/${meta.catalog}.json`);
    continue;
  }
  generated.set(`${meta.catalog}.json`, stableJson(catalog));
}

generated.set("manifest.json", stableJson({
  ...manifest,
  generatedBy: "scripts/i18n-sync.mjs"
}));

if (errors.length) {
  console.error(`i18n sync failed:\n- ${errors.join("\n- ")}`);
  process.exit(1);
}

if (check) {
  const problems = [];
  for (const [name, content] of generated.entries()) {
    const file = path.join(targetRoot, name);
    if (!fs.existsSync(file)) {
      problems.push(`missing ${path.relative(root, file)}`);
      continue;
    }
    if (fs.readFileSync(file, "utf8") !== content) {
      problems.push(`out of date: ${path.relative(root, file)} (run npm run i18n:sync)`);
    }
  }
  if (fs.existsSync(targetRoot)) {
    for (const name of fs.readdirSync(targetRoot)) {
      if (name.startsWith(".")) continue;
      if (!generated.has(name)) problems.push(`stale generated file: public/locales/${name}`);
    }
  }
  if (problems.length) {
    console.error(`i18n catalogs are not in sync:\n- ${problems.join("\n- ")}`);
    process.exit(1);
  }
  console.log(`i18n catalogs in sync: ${generated.size - 1} runtime files`);
  process.exit(0);
}

fs.rmSync(targetRoot, { recursive: true, force: true });
fs.mkdirSync(targetRoot, { recursive: true });
for (const [name, content] of generated.entries()) {
  fs.writeFileSync(path.join(targetRoot, name), content);
}
console.log(`Wrote ${generated.size} locale files to public/locales`);
