#!/usr/bin/env node
/* Fill newly-added canonical keys in every live locale with Google Translate.
 * Existing translations are preserved; only missing keys are translated. */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const source = JSON.parse(fs.readFileSync(path.join(root, "locales", "en.json"), "utf8"));
const manifest = JSON.parse(fs.readFileSync(path.join(root, "locales", "manifest.json"), "utf8"));
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
async function translateBatch(locale, texts) {
  const params = new URLSearchParams({ client: "gtx", sl: "en", tl: locale });
  for (const text of texts) params.append("q", protect(text).protectedText);
  const url = `https://translate.googleapis.com/translate_a/t?${params.toString()}`;
  for (let attempt = 0; attempt < 6; attempt += 1) {
    let response;
    try { response = await fetch(url, { signal: AbortSignal.timeout(60000) }); }
    catch { await new Promise((r) => setTimeout(r, 1200 * (attempt + 1))); continue; }
    if (response.status === 429 || response.status >= 500) {
      await new Promise((r) => setTimeout(r, 2500 * (attempt + 1))); continue;
    }
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const payload = await response.json();
    if (!Array.isArray(payload) || payload.length !== texts.length) throw new Error("shape mismatch");
    return payload.map((value, index) => restore(value, protect(texts[index]).values));
  }
  throw new Error("retries exhausted");
}

for (const meta of manifest.locales) {
  if (!meta.enabled || meta.status === "pseudo" || meta.catalog === "en") continue;
  const targetLocale = GOOGLE_LOCALE[meta.catalog];
  if (!targetLocale) { console.log(`skip ${meta.catalog}`); continue; }
  const file = path.join(root, "locales", `${meta.catalog}.json`);
  const target = JSON.parse(fs.readFileSync(file, "utf8"));
  const missing = Object.keys(source).filter((key) => !(key in target));
  if (!missing.length) { console.log(`${meta.catalog}: complete`); continue; }
  const stringKeys = missing.filter((key) => typeof source[key] === "string");
  for (let offset = 0; offset < stringKeys.length; offset += 35) {
    const batch = stringKeys.slice(offset, offset + 35);
    const values = await translateBatch(targetLocale, batch.map((key) => source[key]));
    batch.forEach((key, index) => { target[key] = values[index]; });
    await new Promise((r) => setTimeout(r, 120));
  }
  for (const key of missing) {
    if (typeof source[key] !== "object") continue;
    const forms = {};
    for (const [form, value] of Object.entries(source[key])) {
      const [translated] = await translateBatch(targetLocale, [value]);
      forms[form] = translated;
      await new Promise((r) => setTimeout(r, 100));
    }
    target[key] = forms;
  }
  fs.writeFileSync(file, JSON.stringify(target, null, 2) + "\n");
  console.log(`${meta.catalog}: filled ${missing.length} key(s)`);
}
