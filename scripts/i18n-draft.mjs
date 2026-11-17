#!/usr/bin/env node
/*
 * Generate machine-translation drafts for locale catalogs.
 *
 * Drafts live in locales/drafts/<catalog>.json and never reach the runtime.
 * A locale is promoted only after review with scripts/i18n-promote.mjs.
 *
 * The provider is a local Ollama server. No source strings leave the machine.
 *
 * Usage:
 *   node scripts/i18n-draft.mjs --locale de
 *   node scripts/i18n-draft.mjs --locale de,fr,ja --batch 30
 *   node scripts/i18n-draft.mjs --wave 1,2
 *   node scripts/i18n-draft.mjs --locale all --force
 *   node scripts/i18n-draft.mjs --locale de --limit 20
 *
 * Options:
 *   --locale <list>   Comma-separated locale codes/catalogs, or "all".
 *   --wave <list>     Manifest rollout wave numbers, e.g. 1 or 1,2.
 *   --model <name>    Ollama model (default qwen3.6:latest fallback gemma4).
 *   --endpoint <url>  Ollama base URL (default http://127.0.0.1:11434).
 *   --batch <n>       Keys per request (default 30).
 *   --limit <n>       Translate at most n missing keys per locale.
 *   --force           Re-translate machine draft keys (reviewed live keys stay).
 *   --repair          Re-translate invalid or plural-incomplete draft keys.
 *   --strict-plurals  Reject plural values missing a required UI category.
 *   --strict          Exit non-zero when any key fails validation.
 */
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { requiredPluralForms } from "./i18n-plural.mjs";

const root = path.resolve(import.meta.dirname, "..");
const localeRoot = path.join(root, "locales");
const draftRoot = path.join(localeRoot, "drafts");
const englishFile = path.join(localeRoot, "en.json");
const placeholderPattern = /\{([A-Za-z0-9_]+)\}/g;

function arg(name, fallback = "") {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] || fallback : fallback;
}
const strict = process.argv.includes("--strict");
const force = process.argv.includes("--force");
const repair = process.argv.includes("--repair");
const strictPlurals = process.argv.includes("--strict-plurals");
const debug = process.argv.includes("--debug");
const limit = Number(arg("--limit", "0")) || 0;
const batchSize = Math.max(1, Number(arg("--batch", "30")) || 30);
const endpoint = String(arg("--endpoint", "http://127.0.0.1:11434")).replace(/\/+$/, "");
let model = String(arg("--model", "qwen3.6:latest"));

const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
const english = JSON.parse(fs.readFileSync(englishFile, "utf8"));
const sourceHash = crypto.createHash("sha256").update(JSON.stringify(english)).digest("hex").slice(0, 16);

function readJson(file, fallback = null) {
  try { return JSON.parse(fs.readFileSync(file, "utf8")); } catch { return fallback; }
}
function writeJson(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const temp = `${file}.tmp`;
  fs.writeFileSync(temp, JSON.stringify(value, null, 2) + "\n");
  fs.renameSync(temp, file);
}
function placeholders(value) {
  return [...String(value).matchAll(placeholderPattern)].map((match) => match[1]).sort();
}
function markupSignature(value) {
  const text = String(value);
  return {
    tags: [...text.matchAll(/<[^>]+>/g)].map((match) => match[0]).sort().join("|"),
    urls: [...text.matchAll(/https?:\/\/[^\s<>"']+/g)].map((match) => match[0]).sort().join("|")
  };
}
function formPlaceholders(value, form = "") {
  const omitCount = form === "one" || form === "two";
  return placeholders(value).filter((name) => !(omitCount && name === "count"));
}
function compareText(source, candidate, form = "") {
  if (formPlaceholders(source, form).join(",") !== formPlaceholders(candidate, form).join(",")) return "placeholder mismatch";
  const expected = markupSignature(source);
  const actual = markupSignature(candidate);
  if (expected.tags !== actual.tags) return "HTML tag mismatch";
  if (expected.urls !== actual.urls) return "URL mismatch";
  return "";
}
function isPlural(value) {
  return value && typeof value === "object" && !Array.isArray(value);
}
function validValue(source, candidate, requiredForms = []) {
  if (typeof source === "string") {
    if (typeof candidate !== "string" || !candidate.trim()) return "not a non-empty string";
    const issue = compareText(source, candidate);
    if (issue) return issue;
    if (/\{[^}]*$/.test(candidate)) return "unterminated placeholder";
    return "";
  }
  if (!isPlural(candidate)) return "expected plural object";
  if (!Object.prototype.hasOwnProperty.call(candidate, "other")) return "missing other form";
  for (const form of Object.keys(candidate)) {
    if (typeof candidate[form] !== "string" || !candidate[form].trim()) return `empty ${form} form`;
    const sourceForm = form in source ? source[form] : source.other;
    const issue = compareText(sourceForm, candidate[form], form);
    if (issue) return `${form} ${issue.toLowerCase()}`;
  }
  const missing = requiredForms.filter((form) => !(form in candidate));
  if (missing.length) return `missing plural categories ${missing.join(", ")}`;
  return "";
}
function stripJson(text) {
  const value = String(text || "").trim().replace(/^```(?:json)?\s*/i, "").replace(/\s*```$/, "");
  try { return JSON.parse(value); } catch {}
  const start = value.indexOf("{");
  const end = value.lastIndexOf("}");
  if (start >= 0 && end > start) {
    try { return JSON.parse(value.slice(start, end + 1)); } catch {}
  }
  return null;
}
function localeList() {
  const requested = arg("--locale", "");
  const waveArg = arg("--wave", "");
  if (!requested && !waveArg) {
    console.error("Pass --locale <code|list|all> or --wave <list>");
    process.exit(2);
  }
  const entries = manifest.locales.filter((meta) => meta && meta.status !== "source" && meta.status !== "pseudo");
  if (requested === "all") return entries;
  const selected = new Map();
  if (requested) {
    const wanted = new Set(requested.split(",").map((value) => value.trim()).filter(Boolean));
    for (const meta of entries) {
      if (wanted.has(meta.code) || wanted.has(meta.catalog)) selected.set(meta.code, meta);
    }
    const missing = [...wanted].filter((value) => ![...selected.values()].some((meta) => meta.code === value || meta.catalog === value));
    if (missing.length) {
      console.error(`Unknown or non-translatable locale(s): ${missing.join(", ")}`);
      process.exit(2);
    }
  }
  if (waveArg) {
    const waves = new Set(waveArg.split(",").map((value) => value.trim()).filter(Boolean));
    for (const meta of entries) {
      if (waves.has(String(meta.wave || ""))) selected.set(meta.code, meta);
    }
    const found = new Set([...selected.values()].map((meta) => String(meta.wave || "")));
    const missing = [...waves].filter((wave) => !found.has(wave));
    if (missing.length) {
      console.error(`No locales configured for wave(s): ${missing.join(", ")}`);
      process.exit(2);
    }
  }
  return [...selected.values()];
}

async function ollamaChat(messages, timeoutMs = 180000) {
  const response = await fetch(`${endpoint}/api/chat`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model,
      stream: false,
      think: false,
      format: "json",
      options: { temperature: 0.1, top_p: 0.9 },
      messages
    }),
    signal: AbortSignal.timeout(timeoutMs)
  });
  if (!response.ok) throw new Error(`Ollama HTTP ${response.status}: ${(await response.text()).slice(0, 200)}`);
  const data = await response.json();
  const output = stripJson(data?.message?.content);
  if (!output) throw new Error("Ollama returned non-JSON content");
  return output;
}

function systemPrompt(meta) {
  const forms = requiredPluralForms(meta.code);
  const extraForms = forms.filter((form) => form !== "one" && form !== "other");
  const example = Object.fromEntries(forms.map((form) => [form, "{count} …"]));
  const pluralRule = extraForms.length
    ? `${meta.label} plural objects MUST contain all ${forms.length} forms: ${forms.join(", ")}. Never omit ${extraForms.join(", ")}; returning only one and other is invalid.`
    : `When a value is a plural object, output exactly these forms: ${forms.join(", ")}.`;
  return [
    "You are a professional software localization engine.",
    `Translate the values of the JSON object from English into ${meta.label} (${meta.code}).`,
    "Return ONLY a valid JSON object with exactly the same keys and value shapes as the input.",
    `${pluralRule} Required shape example: ${JSON.stringify({ key: example })}. Translate the text, keep the shape and keep {count} in every form.`,
    "Never translate or alter: {placeholder} tokens, HTML tags, URLs, email addresses, product names (Alt8Mail, Gmail, Microsoft 365, Outlook, Google, Apple) or technical acronyms (SPF, DKIM, DMARC, TLS, SSL, IMAP, SMTP, OAuth, SAML, SSO, API, CSV, JSON, MIME, RFC, VIP).",
    "Preserve leading and trailing punctuation and spacing.",
    "Use concise, natural interface language. Do not add explanations, comments or markdown fences."
  ].join(" ");
}

async function translateBatch(meta, entries) {
  const payload = Object.fromEntries(entries);
  const output = await ollamaChat([
    { role: "system", content: systemPrompt(meta) },
    { role: "user", content: JSON.stringify(payload) }
  ]);
  if (!output || typeof output !== "object" || Array.isArray(output)) throw new Error("model output is not an object");
  return output;
}

async function translateOne(meta, key, value) {
  const output = await translateBatch(meta, [[key, value]]);
  return output[key];
}

async function runLocale(meta) {
  const catalog = meta.catalog || meta.code;
  const draftFile = path.join(draftRoot, `${catalog}.json`);
  const metaFile = path.join(draftRoot, `${catalog}.meta.json`);
  const liveFile = path.join(localeRoot, `${catalog}.json`);
  const live = readJson(liveFile, {}) || {};
  const draft = force ? {} : (readJson(draftFile, {}) || {});
  const previousMeta = readJson(metaFile, {}) || {};
  const preserved = {};
  for (const [key, value] of Object.entries(live)) {
    if (key in english && !validValue(english[key], value)) preserved[key] = value;
  }
  const result = { ...preserved, ...draft };
  const requiredForms = requiredPluralForms(meta.code);
  const needsRepair = (key) => {
    if (!repair || !(key in result)) return false;
    if (validValue(english[key], result[key])) return true;
    if (!isPlural(english[key])) return false;
    const value = result[key];
    return requiredForms.some((form) => !(form in value));
  };
  const pending = Object.keys(english).filter((key) => !(key in result) || needsRepair(key));
  const queue = limit ? pending.slice(0, limit) : pending;
  const failures = new Map();
  const metaState = {
    locale: meta.code,
    catalog,
    label: meta.label,
    model,
    sourceHash,
    startedAt: previousMeta.sourceHash === sourceHash && previousMeta.startedAt ? previousMeta.startedAt : new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    preservedKeys: Object.keys(preserved).length,
    machineKeys: previousMeta.machineKeys || 0,
    failedKeys: []
  };
  if (!queue.length) {
    console.log(`  ${catalog}: nothing to translate (${Object.keys(result).length}/${Object.keys(english).length})`);
    return { catalog, translated: 0, remaining: 0, failures: [] };
  }
  console.log(`  ${catalog} (${meta.label}): ${queue.length} key(s) to translate with ${model}`);
  let translated = 0;
  for (let offset = 0; offset < queue.length; offset += batchSize) {
    const batch = queue.slice(offset, offset + batchSize);
    let output = null;
    let lastError = "";
    for (let attempt = 1; attempt <= 2 && !output; attempt += 1) {
      try {
        output = await translateBatch(meta, batch.map((key) => [key, english[key]]));
      } catch (error) {
        lastError = error.message;
        if (attempt === 2) break;
        await new Promise((resolve) => setTimeout(resolve, 1200));
      }
    }
    if (!output) {
      for (const key of batch) failures.set(key, lastError || "request failed");
      console.warn(`    batch ${Math.floor(offset / batchSize) + 1}: failed (${lastError || "unknown"})`);
    } else {
      for (const key of batch) {
        if (!(key in output)) { failures.set(key, "missing from model output"); continue; }
        const problem = validValue(english[key], output[key], strictPlurals ? requiredForms : []);
        if (problem) { failures.set(key, problem); if (debug) console.warn(`      ${key}: ${problem} :: ${JSON.stringify(output[key])}`); continue; }
        result[key] = output[key];
        translated += 1;
        failures.delete(key);
      }
    }
    // Retry invalid individual keys once; batches can make one key drift.
    const retry = batch.filter((key) => failures.has(key));
    for (const key of retry) {
      try {
        const value = await translateOne(meta, key, english[key]);
        const problem = validValue(english[key], value, strictPlurals ? requiredForms : []);
        if (!problem) {
          result[key] = value;
          translated += 1;
          failures.delete(key);
        }
      } catch {}
    }
    metaState.updatedAt = new Date().toISOString();
    metaState.machineKeys = Object.keys(result).filter((key) => !(key in preserved) && key in english).length;
    metaState.failedKeys = [...failures.keys()];
    writeJson(draftFile, Object.fromEntries(Object.keys(english).filter((key) => key in result).map((key) => [key, result[key]])));
    writeJson(metaFile, metaState);
    const done = Math.min(offset + batch.length, queue.length);
    console.log(`    ${done}/${queue.length} · ${Object.keys(result).length}/${Object.keys(english).length} present${failures.size ? ` · ${failures.size} pending` : ""}`);
  }
  console.log(`  ${catalog}: draft ${Object.keys(result).length}/${Object.keys(english).length}, ${translated} translated, ${failures.size} failed`);
  return { catalog, translated, remaining: failures.size, failures: [...failures.entries()] };
}

if (!fs.existsSync(path.join(localeRoot, "manifest.json"))) {
  console.error("Missing locales/manifest.json");
  process.exit(2);
}
try {
  const health = await fetch(`${endpoint}/api/tags`, { signal: AbortSignal.timeout(5000) });
  if (!health.ok) throw new Error(`HTTP ${health.status}`);
  const tags = await health.json();
  const names = (tags.models || []).map((entry) => entry.name);
  if (!names.includes(model)) {
    if (names.includes("gemma4:latest")) {
      model = "gemma4:latest";
      console.warn(`Model not installed; falling back to ${model}`);
    } else {
      console.error(`Ollama model ${model} is not installed. Available: ${names.join(", ") || "none"}`);
      process.exit(2);
    }
  }
} catch (error) {
  console.error(`Cannot reach Ollama at ${endpoint}: ${error.message}`);
  process.exit(2);
}

const locales = localeList();
const summary = [];
for (const meta of locales) {
  summary.push(await runLocale(meta));
}
const failed = summary.reduce((total, row) => total + row.remaining, 0);
console.log(`\nDraft run complete: ${summary.length} locale(s), ${summary.reduce((total, row) => total + row.translated, 0)} translated, ${failed} failed.`);
console.log("Drafts are in locales/drafts/ and are not loaded by the webmail.");
if (failed && strict) process.exit(1);
