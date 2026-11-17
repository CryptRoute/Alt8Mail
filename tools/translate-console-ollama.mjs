#!/usr/bin/env node
/*
 * Fill console phrase catalogs with a local Ollama model when the public
 * Google endpoint is rate-limited. The source is public/console-locales/en.json.
 *
 * Usage:
 *   node tools/translate-console-ollama.mjs <catalog> [--limit N] [--batch N] [--only file.json]
 *
 * --only reads a JSON array of English phrases to translate, which keeps bulk
 * work focused on strings the admin and control-plane shells actually render.
 */
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const consoleDir = path.join(root, "public", "console-locales");
const source = JSON.parse(fs.readFileSync(path.join(consoleDir, "en.json"), "utf8"));

const args = process.argv.slice(2);
const catalog = args.find((value) => !value.startsWith("--"));
const option = (name, fallback) => {
  const index = args.indexOf(`--${name}`);
  return index >= 0 && args[index + 1] ? args[index + 1] : fallback;
};
if (!catalog) {
  console.error("usage: node tools/translate-console-ollama.mjs <catalog> [--limit N] [--batch N] [--only file.json]");
  process.exit(2);
}
const limit = Number(option("limit", "0")) || 0;
const batchSize = Math.max(1, Number(option("batch", "24")) || 24);
const onlyFile = option("only", "");
const model = option("model", "gemma4:latest");
const ollamaUrl = option("url", "http://127.0.0.1:11434/api/chat");

const targetFile = path.join(consoleDir, `${catalog}.json`);
const target = fs.existsSync(targetFile) ? JSON.parse(fs.readFileSync(targetFile, "utf8")) : {};
const sourcePhrases = onlyFile
  ? JSON.parse(fs.readFileSync(path.resolve(root, onlyFile), "utf8")).filter((phrase) => typeof phrase === "string" && source[phrase])
  : Object.keys(source);

function normalize(value) {
  return String(value || "").replace(/’/g, "'").replace(/\s+/g, " ").trim();
}

const missing = sourcePhrases.filter((phrase) => {
  const current = target[phrase];
  return !current || normalize(current) === normalize(source[phrase]);
});
const todo = limit ? missing.slice(0, limit) : missing;
if (!todo.length) {
  console.log(`${catalog}: nothing to translate`);
  process.exit(0);
}

function protect(text) {
  const values = [];
  const protectedText = String(text).replace(/\{[A-Za-z0-9_]+\}/g, (match) => {
    values.push(match);
    return `XPHX${values.length - 1}X`;
  });
  return { protectedText, values };
}
function restore(text, values) {
  return String(text).replace(/XPHX(\d+)X/g, (_match, index) => values[Number(index)] ?? "");
}
function chunks(list, size) {
  const output = [];
  for (let index = 0; index < list.length; index += size) output.push(list.slice(index, index + size));
  return output;
}
function parseObject(raw) {
  const cleaned = String(raw || "").replace(/^```(?:json)?\s*/i, "").replace(/```\s*$/i, "").trim();
  const start = cleaned.indexOf("{");
  const end = cleaned.lastIndexOf("}");
  if (start < 0 || end <= start) throw new Error("response contained no JSON object");
  return JSON.parse(cleaned.slice(start, end + 1));
}

async function translateBatch(batch) {
  const protectedBatch = batch.map((phrase) => protect(phrase).protectedText);
  const body = {
    model,
    stream: false,
    think: false,
    options: { temperature: 0.1, num_predict: 4096 },
    messages: [
      {
        role: "system",
        content: `You translate software interface strings into ${catalog}. Return only a JSON object with the exact English keys from the input mapped to natural ${catalog} translations. Keep XPHX0X-style placeholder tokens exactly as they appear. Keep product names such as Alt8Mail, Postfix, Dovecot, DKIM, SPF and DMARC unchanged. Match the concise tone of a professional administration console. Do not add explanations.`,
      },
      { role: "user", content: JSON.stringify(Object.fromEntries(batch.map((phrase, index) => [phrase, protectedBatch[index]]))) },
    ],
  };
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      const response = await fetch(ollamaUrl, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(600000),
      });
      if (!response.ok) throw new Error(`Ollama HTTP ${response.status}`);
      const payload = await response.json();
      const parsed = parseObject(payload.message?.content || "");
      return batch.map((phrase) => {
        const translated = parsed[phrase] ?? parsed[protect(phrase).protectedText];
        return restore(typeof translated === "string" && translated.trim() ? translated.trim() : phrase, protect(phrase).values);
      });
    } catch (error) {
      if (attempt === 2) throw error;
      await new Promise((resolve) => setTimeout(resolve, 2500 * (attempt + 1)));
    }
  }
  return batch;
}

let translated = 0;
for (const batch of chunks(todo, batchSize)) {
  const values = await translateBatch(batch);
  batch.forEach((phrase, index) => { target[phrase] = values[index]; });
  translated += batch.length;
  fs.writeFileSync(targetFile, JSON.stringify(target, null, 2) + "\n");
  console.log(`${catalog}: ${translated}/${todo.length}`);
}
console.log(`${catalog}: wrote ${Object.keys(target).length} phrases`);
