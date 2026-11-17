#!/usr/bin/env node
/*
 * Build a translator review package from locales/drafts.
 *
 * Produces, under artifacts/i18n-review/:
 *   <catalog>.csv   spreadsheet-ready key/English/draft review sheet
 *   <catalog>.md    reviewer summary with coverage and flagged values
 *   <catalog>.json  machine-readable package (coverage, entries, flags)
 *
 * Usage:
 *   node scripts/i18n-review.mjs --locale de
 *   node scripts/i18n-review.mjs --locale all
 */
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { requiredPluralForms } from "./i18n-plural.mjs";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const draftRoot = path.join(localeRoot, "drafts");
const outputRoot = path.join(root, "artifacts", "i18n-review");
const english = JSON.parse(fs.readFileSync(path.join(localeRoot, "en.json"), "utf8"));
const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const sourceHash = crypto.createHash("sha256").update(JSON.stringify(english)).digest("hex").slice(0, 16);
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] || fallback : fallback;
}
const requested = arg("--locale", "");
if (!requested) {
  console.error("Pass --locale <code|list|all>");
  process.exit(2);
}
const placeholder = (value, form = "") => [...String(value).matchAll(placeholderPattern)]
  .map((match) => match[1])
  .filter((name) => !((form === "one" || form === "two") && name === "count"))
  .sort().join(",");
const markup = (value) => ({
  tags: [...String(value).matchAll(/<[^>]+>/g)].map((match) => match[0]).sort().join("|"),
  urls: [...String(value).matchAll(/https?:\/\/[^\s<>"']+/g)].map((match) => match[0]).sort().join("|")
});
const isPlural = (value) => value && typeof value === "object" && !Array.isArray(value);
const hasTranslatableText = (value) => {
  const texts = isPlural(value) ? Object.values(value) : [value];
  return texts.some((text) => String(text || "")
    .replace(/\{[^}]+\}/g, "")
    .replace(/<[^>]+>/g, "")
    .replace(/https?:\/\/\S+/g, "")
    .replace(/[^\p{L}]+/gu, "").length > 0);
};
const csvCell = (value) => `"${String(value == null ? "" : value).replace(/"/g, '""')}"`;

function compareText(source, value, form = "") {
  if (placeholder(source, form) !== placeholder(value, form)) return "placeholder mismatch";
  if (markup(source).tags !== markup(value).tags) return "HTML tag mismatch";
  if (markup(source).urls !== markup(value).urls) return "URL mismatch";
  return "";
}

function validate(key, source, value) {
  if (typeof source === "string") {
    if (typeof value !== "string" || !value.trim()) return "not a non-empty string";
    return compareText(source, value);
  }
  if (!isPlural(value)) return "expected plural object";
  if (!Object.prototype.hasOwnProperty.call(value, "other")) return "missing other form";
  for (const form of Object.keys(value)) {
    if (typeof value[form] !== "string" || !value[form].trim()) return `empty ${form} form`;
    const sourceForm = form in source ? source[form] : source.other;
    const issue = compareText(sourceForm, value[form], form);
    if (issue) return `${form} ${issue.toLowerCase()}`;
  }
  return "";
}

const wanted = requested === "all"
  ? manifest.locales.filter((meta) => meta && meta.status !== "source" && meta.status !== "pseudo").map((meta) => meta.catalog || meta.code)
  : requested.split(",").map((value) => value.trim()).filter(Boolean);
fs.mkdirSync(outputRoot, { recursive: true });

for (const target of wanted) {
  const meta = manifest.locales.find((entry) => entry && (entry.code === target || entry.catalog === target));
  if (!meta || meta.status === "source" || meta.status === "pseudo") {
    console.error(`${target}: unknown or non-translatable locale`);
    process.exitCode = 1;
    continue;
  }
  const catalog = meta.catalog || meta.code;
  const draftFile = path.join(draftRoot, `${catalog}.json`);
  if (!fs.existsSync(draftFile)) {
    console.error(`${catalog}: no draft yet`);
    continue;
  }
  const draft = JSON.parse(fs.readFileSync(draftFile, "utf8"));
  const draftMeta = fs.existsSync(path.join(draftRoot, `${catalog}.meta.json`))
    ? JSON.parse(fs.readFileSync(path.join(draftRoot, `${catalog}.meta.json`), "utf8")) : {};
  const liveFile = path.join(localeRoot, `${catalog}.json`);
  const live = fs.existsSync(liveFile) ? JSON.parse(fs.readFileSync(liveFile, "utf8")) : {};
  const entries = [];
  const groups = new Map();
  const flags = [];
  const requiredForms = requiredPluralForms(meta.code);
  for (const [key, source] of Object.entries(english)) {
    const value = draft[key];
    const problem = key in draft ? validate(key, source, value) : "missing";
    const origin = key in live ? "reviewed" : "machine";
    const identical = key in draft && hasTranslatableText(source) && JSON.stringify(source) === JSON.stringify(value);
    const missingForms = isPlural(source) && isPlural(value)
      ? requiredForms.filter((form) => !(form in value))
      : [];
    if (problem) flags.push(`${key}: ${problem}`);
    if (identical) flags.push(`${key}: identical to English`);
    if (missingForms.length) flags.push(`${key}: missing plural forms ${missingForms.join(", ")}`);
    const entry = { key, source, draft: value ?? null, origin, problem, identical, missingForms };
    entries.push(entry);
    const group = key.split(".").slice(0, 2).join(".");
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(entry);
  }
  const present = entries.filter((entry) => entry.problem === "").length;
  const coverage = ((present / entries.length) * 100).toFixed(1);
  const stale = Object.keys(draft).filter((key) => !(key in english));
  const staleNote = draftMeta.sourceHash && draftMeta.sourceHash !== sourceHash
    ? `Draft source hash ${draftMeta.sourceHash} does not match en.json ${sourceHash}; regenerate before review.`
    : "Draft matches the current en.json.";
  const header = ["key", "origin", "status", "english", "draft", "note"];
  const lines = [header.map(csvCell).join(",")];
  for (const entry of entries) {
    const status = entry.problem ? "invalid" : "ok";
    const notes = [
      entry.problem,
      entry.identical ? "identical to English" : "",
      entry.missingForms.length ? `missing plural forms: ${entry.missingForms.join(", ")}` : ""
    ].filter(Boolean).join("; ");
    lines.push([entry.key, entry.origin, status, JSON.stringify(entry.source), JSON.stringify(entry.draft), notes].map(csvCell).join(","));
  }
  fs.writeFileSync(path.join(outputRoot, `${catalog}.csv`), "﻿" + lines.join("\r\n") + "\r\n");
  fs.writeFileSync(path.join(outputRoot, `${catalog}.json`), JSON.stringify({
    locale: meta.code, catalog, label: meta.label, dir: meta.dir, status: meta.status,
    model: draftMeta.model || "", sourceHash, draftSourceHash: draftMeta.sourceHash || "",
    coverage: Number(coverage), present, total: entries.length, stale, flags, entries
  }, null, 2) + "\n");
  const markdown = [
    `# ${meta.label} (${meta.code}) — translation review`,
    "",
    `- Catalog: \`locales/${catalog}.json\``,
    `- Coverage: **${present}/${entries.length} (${coverage}%)**`,
    `- Draft model: ${draftMeta.model || "unknown"}`,
    `- Source: en.json \`${sourceHash}\``,
    `- ${staleNote}`,
    `- Review status: ${meta.status}${meta.enabled ? " · enabled" : " · disabled"}`,
    "",
    "## Reviewer checklist",
    "",
    "- [ ] Navigation, authentication and security strings read naturally.",
    "- [ ] Placeholders (`{name}`), plural forms and HTML survive unchanged.",
    "- [ ] Brand names and technical acronyms are intentionally not translated.",
    "- [ ] Dates, times, numbers and currency examples fit the locale.",
    "- [ ] Long labels do not clip in the webmail, compose and settings screens.",
    "",
    `## Flags (${flags.length})`,
    "",
    ...(flags.length ? flags.slice(0, 60).map((flag) => `- ${flag}`) : ["- None."]),
    ...(flags.length > 60 ? [`- … ${flags.length - 60} more; see the CSV.`] : []),
    "",
    "## Keys by surface",
    ""
  ];
  for (const [group, items] of [...groups.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    markdown.push(`### ${group} (${items.length})`, "");
    for (const item of items.slice(0, 40)) {
      const notes = [
        item.problem ? `⚠ ${item.problem}` : "",
        item.identical ? "⚠ identical to English" : "",
        item.missingForms.length ? `⚠ missing plural forms ${item.missingForms.join(", ")}` : ""
      ].filter(Boolean).join(" · ");
      markdown.push(`- **${item.key}** — ${notes || "ok"}`);
    }
    if (items.length > 40) markdown.push(`- … ${items.length - 40} more`);
    markdown.push("");
  }
  fs.writeFileSync(path.join(outputRoot, `${catalog}.md`), markdown.join("\n"));
  const escapeHtml = (value) => String(value == null ? "" : value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char]));
  const htmlRows = [];
  for (const [group, items] of [...groups.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    htmlRows.push(`<tr class="group"><th colspan="3">${escapeHtml(group)} <span>${items.length}</span></th></tr>`);
    for (const item of items) {
      htmlRows.push(`<tr class="${item.problem ? "is-bad" : item.identical ? "is-same" : ""}">
        <td class="key"><code>${escapeHtml(item.key)}</code></td>
        <td class="source" dir="auto">${escapeHtml(typeof item.source === "string" ? item.source : JSON.stringify(item.source))}</td>
        <td class="draft" dir="auto">${item.draft == null ? '<em>missing</em>' : escapeHtml(typeof item.draft === "string" ? item.draft : JSON.stringify(item.draft))}${item.problem ? `<small>${escapeHtml(item.problem)}</small>` : item.identical ? "<small>identical to English</small>" : ""}${item.missingForms.length ? `<small>missing plural forms: ${escapeHtml(item.missingForms.join(", "))}</small>` : ""}</td>
      </tr>`);
    }
  }
  const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>${escapeHtml(meta.label)} translation review</title>
<style>
:root{color-scheme:light dark;--bg:#f7f7f5;--panel:#fff;--ink:#1c1c1a;--muted:#6b6b66;--line:#deded8;--accent:#0f6cbd;--bad:#b42318;--same:#8a6d1f}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--panel:#20201e;--ink:#f0f0ec;--muted:#aaa9a2;--line:#34342f;--accent:#6cb6ff;--bad:#ff8a80;--same:#e4c46b}}
*{box-sizing:border-box}body{margin:0;padding:28px 20px 60px;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,sans-serif}
main{max-width:1100px;margin:0 auto}h1{font-size:24px;margin:0 0 4px}p.meta{color:var(--muted);margin:0 0 18px}
.stats{display:flex;gap:10px;flex-wrap:wrap;margin:0 0 20px}.stats span{background:var(--panel);border:1px solid var(--line);border-radius:999px;padding:5px 12px}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line)}th,td{padding:8px 10px;border-bottom:1px solid var(--line);vertical-align:top;text-align:start}
tr.group th{background:color-mix(in srgb,var(--accent) 8%,var(--panel));font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted)}
tr.group span{float:inline-end}td.key{width:26%;font-size:12px;word-break:break-word}td.source,td.draft{width:37%}
tr.is-bad td.draft{background:color-mix(in srgb,var(--bad) 8%,transparent)}tr.is-same td.draft{background:color-mix(in srgb,var(--same) 9%,transparent)}
small{display:block;color:var(--muted);margin-top:4px}tr.is-bad small{color:var(--bad)}tr.is-same small{color:var(--same)}
</style></head><body><main>
<h1>${escapeHtml(meta.label)} <span style="color:var(--muted);font-weight:400">${escapeHtml(meta.code)}</span></h1>
<p class="meta">Draft machine translation review · source en.json ${escapeHtml(sourceHash)} · model ${escapeHtml(draftMeta.model || "unknown")}</p>
<div class="stats"><span>${present}/${entries.length} (${coverage}%)</span><span>${flags.length} flags</span><span>${stale.length} stale</span></div>
<table><thead><tr><th>Key</th><th>English</th><th>${escapeHtml(meta.label)} draft</th></tr></thead><tbody>${htmlRows.join("")}</tbody></table>
</main></body></html>`;
  fs.writeFileSync(path.join(outputRoot, `${catalog}.html`), html);
  console.log(`${catalog}: ${coverage}% · ${flags.length} flag(s) · artifacts/i18n-review/${catalog}.{csv,md,html,json}`);
  if (stale.length) console.log(`  ${stale.length} stale draft key(s): ${stale.slice(0, 5).join(", ")}${stale.length > 5 ? "…" : ""}`);
}
