#!/usr/bin/env node
/*
 * Minimal hardcoded-UI-string inventory.
 *
 * This is intentionally heuristic. It reports strings in the highest-value
 * UI sinks so extraction work can be measured; it is not a perfect parser.
 * Once a surface is fully wrapped, `--fail` can be enabled for that surface.
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const fail = process.argv.includes("--fail");
const files = ["public/app.js", "public/index.html"];
const allow = [
  /^https?:/i, /^\/api\//, /^[a-z0-9_.-]+\/[a-z0-9_.-]+$/i,
  /^(GET|POST|PUT|PATCH|DELETE)$/, /^[A-Z0-9_]+$/, /^[\d\s.,:%+–—-]+$/,
  /^(text|application|image|audio|video)\//i, /^Alt8Mail$/,
  /^#[0-9a-f]{3,8}$/i, /^[a-z-]+$/
];
const findings = [];

function looksLikeCopy(value) {
  const text = String(value || "").trim();
  if (text.length < 3 || !/[A-Za-z]{2,}/.test(text)) return false;
  if (allow.some((pattern) => pattern.test(text))) return false;
  if (/^[a-z0-9]+(?:[._-][a-zA-Z0-9]+)+$/.test(text)) return false;
  return true;
}

function scan(file) {
  const source = fs.readFileSync(path.join(root, file), "utf8");
  const patterns = [
    /\bshowToast\(\s*(?:"([^"]+)"|'([^']+)')/g,
    /\bappConfirm\(\s*\{[^}]*\b(?:title|message|confirmLabel)\s*:\s*(?:"([^"]+)"|'([^']+)')/gs,
    /\.textContent\s*=\s*(?:"([^"]+)"|'([^']+)')/g,
    /\bsetAttribute\(\s*["'](?:aria-label|title|placeholder)["']\s*,\s*(?:"([^"]+)"|'([^']+)')/g,
    /\.textContent\s*=\s*[^;\n]*?\?\s*(?:"([^"]+)"|'([^']+)')\s*:\s*(?:"([^"]+)"|'([^']+)')/g,
    /\.textContent\s*=\s*[^;\n]*?\|\|\s*(?:"([^"]+)"|'([^']+)')/g,
    /\bshowToast\(\s*[^;\n]*?\?\s*(?:"([^"]+)"|'([^']+)')\s*:\s*(?:"([^"]+)"|'([^']+)')/g,
    /\bnew Error\(\s*(?:"([^"]+)"|'([^']+)')/g
  ];
  for (const pattern of patterns) {
    for (const match of source.matchAll(pattern)) {
      const values = match.slice(1).filter(Boolean);
      const index = match.index || 0;
      const line = source.slice(0, index).split(/\r?\n/).length;
      for (const value of values) {
        if (!looksLikeCopy(value)) continue;
        findings.push({ file, line, value: value.replace(/\s+/g, " ").slice(0, 120) });
      }
    }
  }
}

for (const file of files) {
  if (fs.existsSync(path.join(root, file))) scan(file);
}

const unique = [...new Map(findings.map((item) => [`${item.file}:${item.line}:${item.value}`, item])).values()];
const byFile = new Map();
for (const item of unique) byFile.set(item.file, (byFile.get(item.file) || 0) + 1);
console.log("Hardcoded UI string inventory:");
for (const [file, count] of [...byFile.entries()].sort()) console.log(`  ${file}: ${count}`);
console.log(`  total candidates: ${unique.length}`);
for (const item of unique.slice(0, 40)) console.log(`  ${item.file}:${item.line}  ${item.value}`);
if (unique.length > 40) console.log(`  … ${unique.length - 40} more`);

if (fail && unique.length > 0) {
  console.error("\ni18n lint failed: hardcoded UI strings remain");
  process.exit(1);
}
