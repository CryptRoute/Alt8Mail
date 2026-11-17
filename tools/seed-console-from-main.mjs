#!/usr/bin/env node
/*
 * Seed the admin/control phrase catalogs from the main webmail catalogs.
 *
 * Every console phrase whose English text also appears as a value in
 * locales/en.json can reuse the already-reviewed translation for the same
 * catalog. This fills common labels without another machine-translation pass;
 * unmatched phrases keep their existing value and still fall back to English.
 *
 * Usage: node tools/seed-console-from-main.mjs [catalog...]
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const mainDir = path.join(root, "locales");
const consoleDir = path.join(root, "public", "console-locales");

function normalize(value) {
  return String(value || "").replace(/’/g, "'").replace(/\s+/g, " ").trim();
}

function placeholderSignature(value) {
  return [...String(value || "").matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((match) => match[1]).sort().join(",");
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
  const normalized = normalize(value);
  if (!normalized) continue;
  if (!reverse.has(normalized)) reverse.set(normalized, []);
  reverse.get(normalized).push({ key, signature: placeholderSignature(value) });
}

const consoleSource = JSON.parse(fs.readFileSync(path.join(consoleDir, "en.json"), "utf8"));
const requested = process.argv.slice(2).filter(Boolean);
const catalogs = requested.length
  ? requested
  : fs.readdirSync(consoleDir).filter((name) => name.endsWith(".json") && name !== "en.json").map((name) => name.slice(0, -5));

for (const catalog of catalogs) {
  const targetFile = path.join(consoleDir, `${catalog}.json`);
  if (!fs.existsSync(targetFile)) continue;
  let target;
  try {
    target = JSON.parse(fs.readFileSync(targetFile, "utf8"));
  } catch {
    console.error(`${catalog}: unreadable catalog, skipped`);
    continue;
  }
  const mainFile = path.join(mainDir, `${catalog}.json`);
  if (!fs.existsSync(mainFile)) continue;
  const main = JSON.parse(fs.readFileSync(mainFile, "utf8"));
  let filled = 0;
  for (const [phrase, englishValue] of Object.entries(consoleSource)) {
    const current = target[phrase];
    if (typeof current === "string" && current && normalize(current) !== normalize(englishValue)) continue;
    const signature = placeholderSignature(phrase);
    const candidates = reverse.get(normalize(englishValue)) || [];
    const match = candidates.find((candidate) => candidate.signature === signature);
    if (!match) continue;
    const translated = valueFor(main[match.key]);
    if (!translated || normalize(translated) === normalize(englishValue)) continue;
    target[phrase] = translated;
    filled += 1;
  }
  fs.writeFileSync(targetFile, JSON.stringify(target, null, 2) + "\n");
  console.log(`${catalog}: seeded ${filled} phrases`);
}
