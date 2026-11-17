import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";
import { requiredPluralForms } from "../scripts/i18n-plural.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const localeRoot = path.join(root, "public", "locales");

globalThis.location = { search: "?i18n-debug=1", hostname: "localhost" };
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
  }
};
globalThis.document = {
  documentElement: { lang: "", dir: "", dataset: {} },
  querySelectorAll: () => [],
  getElementById: () => null,
  dispatchEvent: () => {}
};
globalThis.fetch = async (url) => {
  const pathname = new URL(String(url), "https://alt8mail.test").pathname;
  const consoleRequest = pathname.startsWith("/console-locales/");
  const base = consoleRequest ? path.join(root, "public", "console-locales") : localeRoot;
  const relative = consoleRequest ? pathname.replace(/^\/console-locales\//, "") : pathname.replace(/^\/locales\//, "");
  const file = path.join(base, relative);
  if (!file.startsWith(base) || !fs.existsSync(file)) {
    return { ok: false, status: 404, json: async () => ({}) };
  }
  const body = JSON.parse(fs.readFileSync(file, "utf8"));
  return { ok: true, status: 200, json: async () => body };
};

const i18n = await import("../public/i18n.mjs");

test("resolves regional locales and base-language fallbacks", async () => {
  assert.equal(await i18n.initI18n("de-DE"), "de-DE");
  assert.equal(i18n.currentLocale(), "de-DE");
  assert.equal(i18n.currentDirection(), "ltr");
  assert.equal(i18n.t("login.signIn"), "Anmelden");

  assert.equal(await i18n.setLocale("ar-EG"), "ar");
  assert.equal(i18n.currentDirection(), "rtl");
  assert.equal(document.documentElement.dir, "rtl");

  assert.equal(await i18n.setLocale("en-GB"), "en-GB");
  assert.equal(i18n.currentDirection(), "ltr");
});

test("returns the key when no catalog entry exists", async () => {
  await i18n.setLocale("de-DE");
  assert.equal(i18n.t("missing.catalog.key"), "missing.catalog.key");
});

test("plural helpers use the active locale rules", async () => {
  await i18n.setLocale("en-US");
  assert.equal(i18n.tn("mail.unit.message", 1), "message");
  assert.equal(i18n.tn("mail.unit.message", 2), "messages");
  assert.equal(i18n.tn("shell.eventsThisMonth", 1, { count: 1 }), "1 event this month");
  assert.equal(i18n.tn("shell.eventsThisMonth", 3, { count: 3 }), "3 events this month");
});

test("phrase translation covers nested audit values and counted units", async () => {
  await i18n.setLocale("es-ES");
  await i18n.loadPhrases();
  assert.equal(i18n.tp("Single sign-on · Chrome on Windows"), "Inicio de sesión único · Chrome en Windows");
  assert.equal(i18n.tp("Conversations archived from Inbox"), "Conversaciones archivadas de Bandeja de entrada");
  assert.equal(i18n.tp("1 recipient"), "1 destinatario");
  assert.equal(i18n.tp("3 conversations"), "3 conversaciones");
  assert.equal(i18n.tp("Ruleset saved · revision 4 · vacation responder enabled"), "Conjunto de reglas guardado · revisión 4 · respuesta automática habilitada");
});

test("pseudo locale is available for visual extraction tests", async () => {
  assert.equal(await i18n.setLocale("en-XA"), "en-XA");
  assert.equal(i18n.currentLocale(), "en-XA");
  const value = i18n.t("login.signIn");
  assert.match(value, /^\[!! /);
  assert.match(value, /!!\]$/);
});

test("released locales are offered while the hidden pseudo locale is not", async () => {
  const locales = i18n.supportedLocales().map((item) => item.code);
  assert.ok(locales.includes("en-US"));
  assert.ok(locales.includes("ar"));
  assert.ok(locales.includes("ja"));
  assert.ok(!locales.includes("en-XA"));
});

test("UI plural requirements cover the categories small counts select", () => {
  const russian = requiredPluralForms("ru");
  assert.ok(russian.includes("one"));
  assert.ok(russian.includes("few"));
  assert.ok(russian.includes("many"));
  const arabic = requiredPluralForms("ar");
  for (const form of ["zero", "one", "two", "few", "many", "other"]) assert.ok(arabic.includes(form), form);
  assert.deepEqual(requiredPluralForms("fr").sort(), ["one", "other"]);
  assert.deepEqual(requiredPluralForms("ja"), ["other"]);
  assert.deepEqual(requiredPluralForms("en_US"), ["other"]);
});

test("certification report exposes every target locale", () => {
  const output = execFileSync(process.execPath, ["scripts/i18n-certify.mjs", "--json"], { cwd: root, encoding: "utf8" });
  const report = JSON.parse(output);
  assert.equal(report.failures.length, 0);
  assert.ok(report.rows.length >= 33);
  for (const row of report.rows) {
    assert.ok(["certified", "partial-enabled", "ready-for-review", "draft", "planned"].includes(row.state), row.state);
    assert.ok(row.live <= report.total);
    assert.ok(row.draft <= report.total);
  }
});

test("every enabled locale ships an administration phrase catalog", () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(localeRoot, "manifest.json"), "utf8"));
  const consoleRoot = path.join(root, "public", "console-locales");
  for (const meta of manifest.locales) {
    if (meta.enabled === false || meta.status === "pseudo" || meta.hidden) continue;
    const catalogName = meta.catalog || meta.code;
    if (catalogName === "en") continue;
    const catalogFile = path.join(consoleRoot, `${catalogName}.json`);
    const catalog = JSON.parse(fs.readFileSync(catalogFile, "utf8"));
    assert.ok(Object.keys(catalog).length > 300, `${meta.code}: phrase catalog is too small`);
  }
});

test("administration phrase catalogs preserve interpolation placeholders", () => {
  const consoleRoot = path.join(root, "public", "console-locales");
  const english = JSON.parse(fs.readFileSync(path.join(consoleRoot, "en.json"), "utf8"));
  const signature = (value) => [...new Set([...String(value).matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((match) => match[1]))].sort().join(",");
  for (const name of fs.readdirSync(consoleRoot)) {
    if (!name.endsWith(".json") || name === "en.json") continue;
    const catalog = JSON.parse(fs.readFileSync(path.join(consoleRoot, name), "utf8"));
    for (const [phrase, translated] of Object.entries(catalog)) {
      if (typeof translated !== "string" || !translated || translated === phrase) continue;
      if (!(phrase in english)) continue;
      assert.equal(signature(translated), signature(phrase), `${name}: placeholders changed for "${phrase}"`);
      assert.doesNotMatch(translated, /XPHX|XPX/, `${name}: unprotected placeholder token in "${phrase}"`);
    }
  }
});
