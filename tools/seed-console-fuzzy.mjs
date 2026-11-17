#!/usr/bin/env node
/*
 * Second-pass seeding for the administration phrase catalogs.
 *
 * The exact seeder in seed-console-from-main.mjs only matches identical text
 * and placeholder names. This pass matches text with the same shape, ignoring
 * placeholder names, case and a trailing period, then renames the translated
 * placeholders positionally. It fills common labels across every locale from
 * the already-reviewed webmail catalogs without another machine-translation
 * run.
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const mainDir = path.join(root, "locales");
const consoleDir = path.join(root, "public", "console-locales");

function shape(value) {
  return String(value || "")
    .replace(/’/g, "'")
    .replace(/\{([A-Za-z0-9_]+)\}/g, "\u0000")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/[.!?。！？]+$/, "")
    .toLowerCase();
}

function placeholders(value) {
  return [...String(value || "").matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((match) => match[1]);
}

function valueFor(entry) {
  if (typeof entry === "string") return entry;
  if (entry && typeof entry === "object") return entry.other || entry.one || "";
  return "";
}

const english = JSON.parse(fs.readFileSync(path.join(mainDir, "en.json"), "utf8"));
const reverse = new Map();
for (const [key, entry] of Object.entries(english)) {
  const value = valueFor(entry);
  if (!value) continue;
  const keyShape = shape(value);
  if (!keyShape) continue;
  if (!reverse.has(keyShape)) reverse.set(keyShape, []);
  reverse.get(keyShape).push({ key, names: placeholders(value) });
}

const consoleSource = JSON.parse(fs.readFileSync(path.join(consoleDir, "en.json"), "utf8"));
let totalFilled = 0;
const targets = process.argv.slice(2).filter(Boolean);
const catalogs = targets.length
  ? targets
  : fs.readdirSync(consoleDir).filter((name) => name.endsWith(".json") && name !== "en.json").map((name) => name.slice(0, -5));

for (const catalog of catalogs) {
  const targetFile = path.join(consoleDir, `${catalog}.json`);
  const mainFile = path.join(mainDir, `${catalog}.json`);
  if (!fs.existsSync(targetFile) || !fs.existsSync(mainFile)) continue;
  const target = JSON.parse(fs.readFileSync(targetFile, "utf8"));
  const main = JSON.parse(fs.readFileSync(mainFile, "utf8"));
  let filled = 0;
  for (const [phrase, englishValue] of Object.entries(consoleSource)) {
    const current = target[phrase];
    if (typeof current === "string" && current && current !== phrase) continue;
    const names = placeholders(phrase);
    const candidates = reverse.get(shape(englishValue)) || [];
    const match = candidates.find((candidate) => candidate.names.length === names.length);
    if (!match) continue;
    const raw = valueFor(main[match.key]);
    if (!raw || shape(raw) === shape(englishValue)) continue;
    const translatedNames = placeholders(raw);
    if (translatedNames.length !== names.length) continue;
    let index = 0;
    const translated = raw.replace(/\{([A-Za-z0-9_]+)\}/g, () => `{${names[index++]}}`);
    if (shape(translated) === shape(englishValue)) continue;
    target[phrase] = translated;
    filled += 1;
  }
  fs.writeFileSync(targetFile, JSON.stringify(target, null, 2) + "\n");
  totalFilled += filled;
  console.log(`${catalog}: fuzzy-seeded ${filled}`);
}
console.log(`total ${totalFilled}`);
