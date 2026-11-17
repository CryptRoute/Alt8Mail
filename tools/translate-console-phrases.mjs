#!/usr/bin/env node
/*
 * Translate the console phrase catalog (admin, control, add-ons, audit,
 * server errors) with the public Google Translate endpoint.
 *
 * The source is public/console-locales/en.json, a flat English -> English map.
 * Each target file keeps the English phrase as the key so the admin/control
 * DOM translator and the webmail tp() helper can look up rendered text.
 *
 * Usage: node tools/translate-console-phrases.mjs <catalog> [<catalog>...]
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const sourceFile = path.join(root, "public", "console-locales", "en.json");
const catalogDir = path.join(root, "public", "console-locales");
const source = JSON.parse(fs.readFileSync(sourceFile, "utf8"));
const phrases = Object.keys(source);
const target = process.argv.slice(2).filter(Boolean);
if (!target.length) {
  console.error("usage: node tools/translate-console-phrases.mjs <catalog> [...]");
  process.exit(2);
}

const GOOGLE_LOCALE = {
  ar: "ar", bg: "bg", cs: "cs", da: "da", de: "de", el: "el", es: "es", et: "et",
  fi: "fi", fr: "fr", he: "he", hi: "hi", hr: "hr", hu: "hu", id: "id", it: "it",
  ja: "ja", ko: "ko", lt: "lt", lv: "lv", ms: "ms", nb: "no", nl: "nl", pl: "pl",
  pt: "pt", ro: "ro", ru: "ru", sk: "sk", sl: "sl", sv: "sv", tr: "tr", uk: "uk",
  "zh-Hans": "zh-CN",
};

const placeholderPattern = /\{[A-Za-z0-9_]+\}/g;
function protect(text) {
  const values = [];
  const protectedText = String(text).replace(placeholderPattern, (match) => {
    const index = values.push(match) - 1;
    return `XPHX${index}X`;
  });
  return { protectedText, values };
}
function restore(text, values) {
  return String(text).replace(/XPHX(\d+)X/g, (_match, index) => values[Number(index)] ?? "");
}

async function requestBatch(targetLocale, batch) {
  const protectedBatch = batch.map((text) => protect(text).protectedText);
  const body = new URLSearchParams({
    client: "gtx", sl: "en", tl: targetLocale, dt: "t", q: protectedBatch.join("\n"),
  });
  for (let attempt = 0; attempt < 6; attempt += 1) {
    let response;
    try {
      response = await fetch("https://translate.google.com/translate_a/single", {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body,
        signal: AbortSignal.timeout(90000),
      });
    } catch (error) {
      if (attempt === 5) throw error;
      await new Promise((resolve) => setTimeout(resolve, 2500 * (attempt + 1)));
      continue;
    }
    if (response.status === 429 || response.status >= 500) {
      await new Promise((resolve) => setTimeout(resolve, 8000 * (attempt + 1)));
      continue;
    }
    if (!response.ok) throw new Error(`Google Translate HTTP ${response.status}`);
    const payload = await response.json();
    const joined = Array.isArray(payload?.[0]) ? payload[0].map((part) => part[0] || "").join("") : "";
    const lines = joined.split("\n");
    if (lines.length !== batch.length) {
      throw new Error(`batch shape mismatch (${lines.length}/${batch.length})`);
    }
    return lines.map((value, index) => restore(value, protect(batch[index]).values));
  }
  throw new Error("Google Translate retries exhausted");
}

async function translateBatchSafe(locale, batch) {
  try {
    return await requestBatch(locale, batch);
  } catch (error) {
    if (batch.length <= 1) {
      await new Promise((resolve) => setTimeout(resolve, 15000));
      try { return await requestBatch(locale, batch); }
      catch { return [batch[0]]; }
    }
    const middle = Math.ceil(batch.length / 2);
    const first = await translateBatchSafe(locale, batch.slice(0, middle));
    await new Promise((resolve) => setTimeout(resolve, 1200));
    const second = await translateBatchSafe(locale, batch.slice(middle));
    return first.concat(second);
  }
}

function batches() {
  const result = [];
  let current = [];
  let chars = 0;
  for (const phrase of phrases) {
    if (current.length >= 150 || chars + phrase.length > 6000) {
      result.push(current);
      current = [];
      chars = 0;
    }
    current.push(phrase);
    chars += phrase.length + 8;
  }
  if (current.length) result.push(current);
  return result;
}

const chunks = batches();
const delayMs = Math.max(0, Number(process.env.TRANSLATE_DELAY_MS || 80));
for (const catalog of target) {
  const targetLocale = GOOGLE_LOCALE[catalog];
  if (!targetLocale) {
    console.error(`skip ${catalog}: no Google locale mapping`);
    continue;
  }
  const outputFile = path.join(catalogDir, `${catalog}.json`);
  let existing = {};
  if (fs.existsSync(outputFile)) {
    try { existing = JSON.parse(fs.readFileSync(outputFile, "utf8")); } catch { existing = {}; }
  }
  const output = {};
  let translated = 0;
  for (const chunk of chunks) {
    const todo = chunk.filter((phrase) => !existing[phrase]);
    if (!todo.length) {
      for (const phrase of chunk) output[phrase] = existing[phrase];
      continue;
    }
    const values = await translateBatchSafe(targetLocale, todo);
    todo.forEach((phrase, index) => { output[phrase] = values[index]; });
    for (const phrase of chunk) if (!(phrase in output) && existing[phrase]) output[phrase] = existing[phrase];
    translated += todo.length;
    fs.writeFileSync(outputFile, JSON.stringify(output, null, 2) + "\n");
    process.stdout.write(`\r${catalog}: ${translated}/${phrases.length}`);
    await new Promise((resolve) => setTimeout(resolve, delayMs));
  }
  fs.writeFileSync(outputFile, JSON.stringify(output, null, 2) + "\n");
  console.log(`\n${catalog}: wrote ${Object.keys(output).length} phrases`);
}
