#!/usr/bin/env node
/*
 * Enable or disable a locale in the runtime manifest.
 *
 * Enabling requires a complete, structurally valid, reviewed live catalog.
 * Disabling is always allowed and is the rollback path: it is a manifest change
 * plus a sync, with no code deploy.
 *
 * Usage:
 *   node scripts/i18n-release.mjs --list
 *   node scripts/i18n-release.mjs --enable ja
 *   node scripts/i18n-release.mjs --disable ja
 *   node scripts/i18n-release.mjs --disable ja --no-sync
 */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const manifestFile = path.join(localeRoot, "manifest.json");
const english = JSON.parse(fs.readFileSync(path.join(localeRoot, "en.json"), "utf8"));
const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
const total = Object.keys(english).length;
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] || fallback : fallback;
}
const enable = arg("--enable", "").trim();
const disable = arg("--disable", "").trim();
const noSync = process.argv.includes("--no-sync");
const list = process.argv.includes("--list") || (!enable && !disable);

const placeholders = (value) => [...String(value).matchAll(placeholderPattern)].map((match) => match[1]).sort().join(",");
const isPlural = (value) => value && typeof value === "object" && !Array.isArray(value);
function valueProblem(source, value) {
  if (typeof source === "string") {
    if (typeof value !== "string" || !value.trim()) return "empty or non-string";
    return placeholders(source) === placeholders(value) ? "" : "placeholder mismatch";
  }
  if (!isPlural(value)) return "expected plural object";
  if (!Object.prototype.hasOwnProperty.call(value, "other")) return "missing other form";
  for (const [form, text] of Object.entries(value)) {
    if (typeof text !== "string" || !text.trim()) return `${form} is empty`;
    const sourceForm = form in source ? source[form] : source.other;
    const omit = form === "zero" || form === "one" || form === "two";
    const names = (entry) => {
      const list = [...String(entry).matchAll(placeholderPattern)].map((match) => match[1]);
      return (omit ? list.filter((name) => name !== "count") : list).sort().join(",");
    };
    if (names(sourceForm) !== names(text)) return `${form} placeholder mismatch`;
  }
  return "";
}

function stateOf(meta) {
  const catalog = meta.catalog || meta.code;
  const file = path.join(localeRoot, `${catalog}.json`);
  if (!fs.existsSync(file)) return { state: "missing", live: 0, problems: [] };
  let live;
  try { live = JSON.parse(fs.readFileSync(file, "utf8")); } catch { return { state: "invalid", live: 0, problems: ["invalid JSON"] }; }
  const problems = [];
  for (const key of Object.keys(live)) {
    if (!(key in english)) { problems.push(`${key}: stale key`); continue; }
    const issue = valueProblem(english[key], live[key]);
    if (issue) problems.push(`${key}: ${issue}`);
  }
  const count = Object.keys(live).filter((key) => key in english).length;
  const certified = count === total && problems.length === 0 && meta.status === "reviewed";
  return { state: count === total ? "complete" : "incomplete", live: count, problems, certified };
}

function sync() {
  if (noSync) return;
  const result = spawnSync(process.execPath, [path.join(root, "scripts", "i18n-sync.mjs")], { stdio: "inherit" });
  if (result.status !== 0) process.exit(result.status || 1);
}

function save() {
  fs.writeFileSync(manifestFile, JSON.stringify(manifest, null, 2) + "\n");
  sync();
}

if (list) {
  for (const meta of manifest.locales) {
    if (!meta || meta.status === "source" || meta.status === "pseudo") continue;
    const status = stateOf(meta);
    console.log(`${(meta.code + " ").padEnd(10)} ${(meta.status + " ").padEnd(9)} ${meta.enabled ? "enabled " : "disabled"} ${status.live}/${total}${status.certified ? " certified" : ""}${status.problems.length ? ` · ${status.problems.length} problem(s)` : ""}`);
  }
  process.exit(0);
}

if (enable) {
  const meta = manifest.locales.find((entry) => entry && (entry.code === enable || entry.catalog === enable));
  if (!meta || meta.status === "source" || meta.status === "pseudo") {
    console.error(`Unknown locale: ${enable}`);
    process.exit(1);
  }
  const status = stateOf(meta);
  if (!status.certified) {
    console.error(`${meta.code}: cannot enable — ${status.state}, ${status.live}/${total} keys, status ${meta.status}${status.problems.length ? `, ${status.problems.length} problem(s)` : ""}`);
    console.error("A locale must have a complete, structurally valid, reviewed catalog before release.");
    process.exit(1);
  }
  meta.enabled = true;
  meta.releasedAt = new Date().toISOString().slice(0, 10);
  save();
  console.log(`${meta.code}: enabled. Review the manifest diff, then deploy.`);
}

if (disable) {
  const meta = manifest.locales.find((entry) => entry && (entry.code === disable || entry.catalog === disable));
  if (!meta || meta.status === "source" || meta.status === "pseudo") {
    console.error(`Unknown locale: ${disable}`);
    process.exit(1);
  }
  meta.enabled = false;
  meta.disabledAt = new Date().toISOString().slice(0, 10);
  save();
  console.log(`${meta.code}: disabled. This is the rollback path; no code deploy is required.`);
}
