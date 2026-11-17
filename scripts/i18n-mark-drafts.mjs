#!/usr/bin/env node
/*
 * Mark planned locales whose machine draft is structurally complete as
 * status "draft" in the manifest. This never enables a locale and never
 * changes locales that are already reviewed or enabled.
 *
 * Usage:
 *   node scripts/i18n-mark-drafts.mjs
 *   node scripts/i18n-mark-drafts.mjs --check
 */
import fs from "node:fs";
import path from "node:path";
import { requiredPluralForms } from "./i18n-plural.mjs";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const english = JSON.parse(fs.readFileSync(path.join(localeRoot, "en.json"), "utf8"));
const manifestFile = path.join(localeRoot, "manifest.json");
const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
const check = process.argv.includes("--check");
const keys = Object.keys(english);
let changed = 0;

for (const meta of manifest.locales) {
  if (!meta || meta.status === "source" || meta.status === "pseudo" || meta.enabled && meta.status !== "reviewed") continue;
  const catalog = meta.catalog || meta.code;
  const draftFile = path.join(localeRoot, "drafts", `${catalog}.json`);
  if (!fs.existsSync(draftFile)) continue;
  let draft;
  try { draft = JSON.parse(fs.readFileSync(draftFile, "utf8")); } catch { continue; }
  if (keys.some((key) => !(key in draft))) continue;
  if (meta.status === "planned") meta.status = "draft";
  const required = requiredPluralForms(meta.code || catalog);
  const pluralComplete = keys.every((key) => {
    const source = english[key];
    const value = draft[key];
    if (!source || typeof source !== "object" || Array.isArray(source)) return true;
    return required.every((form) => value && typeof value === "object" && typeof value[form] === "string");
  });
  if (meta.draftReady !== pluralComplete) {
    meta.draftReady = pluralComplete;
    changed += 1;
  }
  console.log(`${catalog}: ${pluralComplete ? "draft ready" : "draft seeded, plural review pending"} (${meta.enabled ? "enabled" : "disabled"})`);
}
if (changed && !check) {
  fs.writeFileSync(manifestFile, JSON.stringify(manifest, null, 2) + "\n");
  console.log(`\nUpdated ${changed} manifest entr${changed === 1 ? "y" : "ies"}. Run node scripts/i18n-sync.mjs.`);
} else if (!changed) {
  console.log("All eligible locales already have complete drafts.");
}
