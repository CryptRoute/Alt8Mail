#!/usr/bin/env node
/*
 * Fail when component CSS reintroduces physical left/right layout properties.
 * The RTL override file is intentionally exempt because it expresses mirrored
 * affordances that have no logical equivalent (icon transforms, select arrows).
 *
 * Usage:
 *   node tools/rtl-audit.mjs
 *   node tools/rtl-audit.mjs --fail
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const cssRoot = path.join(root, "public");
const files = fs.readdirSync(cssRoot)
  .filter((name) => name.endsWith(".css") && name !== "rtl.css")
  .sort();
const findings = [];
const patterns = [
  { re: /\b(?:margin|padding)-(?:left|right)\b/g, message: "physical margin/padding" },
  { re: /\bborder-(?:left|right)\b/g, message: "physical border" },
  { re: /\bborder-(?:top|bottom)-(?:left|right)-radius\b/g, message: "physical corner radius" },
  { re: /\btext-align:\s*(?:left|right)\b/g, message: "physical text alignment" },
  { re: /\bfloat:\s*(?:left|right)\b/g, message: "physical float" },
  { re: /(?<![-\w])(?:left|right):/g, message: "physical offset" }
];
for (const name of files) {
  const file = path.join(cssRoot, name);
  const lines = fs.readFileSync(file, "utf8").split(/\r?\n/);
  lines.forEach((line, index) => {
    // Centered decorative marks keep `left: 50%` plus a physical transform;
    // converting them would break centering in both directions.
    if (/\b(?:left|right):\s*50%/.test(line)) return;
    if (/background-position:\s*(?:left|right)/.test(line)) return;
    for (const { re, message } of patterns) {
      re.lastIndex = 0;
      if (re.test(line)) findings.push({ file: `public/${name}`, line: index + 1, message, text: line.trim().slice(0, 120) });
    }
  });
}

if (!findings.length) {
  console.log(`RTL audit clean: ${files.length} component stylesheets use logical properties.`);
  process.exit(0);
}
console.error(`RTL audit found ${findings.length} physical directional declaration(s):`);
for (const finding of findings.slice(0, 80)) {
  console.error(`  ${finding.file}:${finding.line}  ${finding.message} — ${finding.text}`);
}
if (findings.length > 80) console.error(`  … ${findings.length - 80} more`);
if (process.argv.includes("--fail")) process.exit(1);
