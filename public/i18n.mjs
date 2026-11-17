/*
 * Alt8Mail UI localization.
 *
 * English is the canonical catalog. Runtime catalogs are generated from the
 * repository-level `locales/` tree into /locales/<catalog>.json. A missing key
 * falls back to English, then to the key itself so a broken catalog never
 * blanks the UI.
 */

const DEFAULT_VALUE = "en-US";
const DEFAULT_CATALOG = "en";
const CATALOG_ROOT = "/locales";
const PHRASE_ROOT = "/console-locales";

let manifest = null;
let currentMeta = null;
let currentValue = DEFAULT_VALUE;
let currentCatalog = DEFAULT_CATALOG;
let currentDir = "ltr";
let currentPluralLocale = DEFAULT_VALUE;
const catalogs = new Map();
const loading = new Map();
const phraseCatalogs = new Map();
const phraseLoading = new Map();
let phraseCatalog = null;
let phrasePatterns = null;
const missingKeys = new Set();
let initialized = false;
let initPromise = null;
let debugMissing = false;
let defaultValue = DEFAULT_VALUE;

try {
  debugMissing = new URLSearchParams(location.search).has("i18n-debug") ||
    ["localhost", "127.0.0.1", "::1"].includes(location.hostname);
} catch {
  debugMissing = false;
}

function normalizeValue(value) {
  return String(value || "").trim().replace(/_/g, "-");
}

function primaryLanguage(value) {
  return normalizeValue(value).split("-")[0].toLowerCase();
}

function fetchJson(url) {
  return fetch(url, { credentials: "same-origin" }).then((response) => {
    if (!response.ok) throw new Error(`i18n catalog ${url}: HTTP ${response.status}`);
    return response.json();
  });
}

function defaultMeta() {
  return {
    code: defaultValue,
    catalog: DEFAULT_CATALOG,
    label: defaultValue === DEFAULT_VALUE ? "English (United States)" : defaultValue,
    dir: "ltr",
    status: "source",
    enabled: true
  };
}

function escapeOption(value) {
  return String(value == null ? "" : value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function localeEntries() {
  return manifest && Array.isArray(manifest.locales) && manifest.locales.length
    ? manifest.locales
    : [defaultMeta()];
}

/** Resolve a BCP 47 value or catalog name to a manifest entry. */
export function resolveLocaleMeta(preferred) {
  const requested = normalizeValue(preferred);
  const allEntries = localeEntries();
  const entries = allEntries.filter((item) => item.enabled !== false || item.status === "pseudo");
  if (!requested) return entries[0] || defaultMeta();
  const exact = entries.find((item) => normalizeValue(item.code).toLowerCase() === requested.toLowerCase()) ||
    entries.find((item) => normalizeValue(item.catalog).toLowerCase() === requested.toLowerCase());
  if (exact) return exact;

  // BCP 47 progressive fallback: zh-Hans-CN -> zh-Hans -> zh.
  const parts = requested.split("-");
  for (let length = parts.length - 1; length >= 1; length -= 1) {
    const prefix = parts.slice(0, length).join("-").toLowerCase();
    const match = entries.find((item) => normalizeValue(item.code).toLowerCase() === prefix);
    if (match) return match;
  }

  // A base language such as "de" or "en" should find its regional catalog.
  const primary = primaryLanguage(requested);
  const baseMatch = entries.find((item) => primaryLanguage(item.code) === primary);
  if (baseMatch) return baseMatch;

  const configured = entries.find((item) => normalizeValue(item.code).toLowerCase() === normalizeValue(defaultValue).toLowerCase());
  if (configured) return configured;
  return entries.find((item) => normalizeValue(item.code) === DEFAULT_VALUE) ||
    allEntries.find((item) => normalizeValue(item.code) === DEFAULT_VALUE) ||
    entries[0] || allEntries[0] || defaultMeta();
}

function catalogPath(catalog) {
  return `${CATALOG_ROOT}/${encodeURIComponent(catalog)}.json`;
}

async function ensureCatalog(catalog) {
  if (!catalog) return {};
  if (catalogs.has(catalog)) return catalogs.get(catalog);
  if (!loading.has(catalog)) {
    loading.set(catalog, fetchJson(catalogPath(catalog))
      .catch((error) => {
        if (debugMissing) console.warn(`[i18n] could not load catalog ${catalog}: ${error.message}`);
        return null;
      })
      .finally(() => loading.delete(catalog)));
  }
  const loaded = await loading.get(catalog);
  const catalogValue = loaded && typeof loaded === "object" && !Array.isArray(loaded) ? loaded : {};
  catalogs.set(catalog, catalogValue);
  return catalogValue;
}

function readCatalog(key) {
  const current = catalogs.get(currentCatalog) || {};
  if (Object.prototype.hasOwnProperty.call(current, key)) return current[key];
  const fallback = catalogs.get(DEFAULT_CATALOG) || {};
  if (Object.prototype.hasOwnProperty.call(fallback, key)) return fallback[key];
  return null;
}

function interpolate(template, vars) {
  return String(template).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match
  ));
}

function warnMissing(key) {
  if (!debugMissing || missingKeys.has(key)) return;
  missingKeys.add(key);
  console.warn(`[i18n] missing key: ${key} (${currentCatalog})`);
}

/** Translate a stable key, with optional {placeholder} interpolation. */
export function t(key, vars = {}) {
  const value = readCatalog(key);
  if (value == null) {
    warnMissing(key);
    return key;
  }
  if (typeof value === "string") return interpolate(value, vars);
  // A plural object used as a plain translation falls back to "other".
  if (value && typeof value === "object" && typeof value.other === "string") {
    return interpolate(value.other, vars);
  }
  return key;
}

/** Translate a pluralized key using Intl.PluralRules and catalog forms. */
export function tn(key, count, vars = {}) {
  let category = "other";
  try {
    category = new Intl.PluralRules(currentPluralLocale).select(Number(count));
  } catch {
    category = new Intl.PluralRules(DEFAULT_VALUE).select(Number(count));
  }
  const forms = readCatalog(key);
  if (forms && typeof forms === "object" && !Array.isArray(forms)) {
    const template = forms[category] || forms.other || forms[Object.keys(forms)[0]];
    if (template != null) return interpolate(template, { ...vars, count });
  }
  return t(key, { ...vars, count });
}

/** Return the resolved BCP 47 locale, e.g. de-DE or zh-Hans-CN. */
export function currentLocale() {
  return currentValue;
}

/** Return the catalog name, e.g. de or zh-Hans. */
export function currentCatalogName() {
  return currentCatalog;
}

export function currentDirection() {
  return currentDir;
}

export function isRTL() {
  return currentDir === "rtl";
}

/** Missing keys observed in this session (development diagnostics). */
export function missingTranslationKeys() {
  return [...missingKeys];
}

export function supportedLocales() {
  const entries = localeEntries().filter((item) => item.enabled !== false && !item.hidden);
  return entries.length ? entries.slice() : [defaultMeta()];
}

export function allLocales() {
  return localeEntries().slice();
}

function normalizePhrase(value) {
  return String(value || "").replace(/’/g, "'").replace(/\s+/g, " ").trim();
}

function normalizePhraseMap(raw) {
  const result = Object.create(null);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return result;
  for (const [key, value] of Object.entries(raw)) {
    if (typeof value !== "string") continue;
    const normalized = normalizePhrase(key);
    if (normalized) result[normalized] = value;
  }
  return result;
}

function buildPhrasePatterns(map) {
  const patterns = [];
  for (const [source, translation] of Object.entries(map)) {
    if (!source.includes("{")) continue;
    const names = [];
    const escaped = source.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const regexSource = escaped.replace(/\\\{([A-Za-z0-9_]+)\\\}/g, (_match, name) => {
      names.push(name);
      // Counts and measurements only match their own shape so a generic unit
      // pattern cannot rewrite prose or a user-provided title.
      if (name === "count") return "(\\d[\\d,.]*)";
      if (name === "percent") return "(\\d+(?:[.,]\\d+)?%)";
      if (name === "used" || name === "total") return "(\\d+(?:[.,]\\d+)?\\s*[KMGT]?B)";
      return "(.+?)";
    });
    if (!names.length) continue;
    try {
      patterns.push({ regex: new RegExp(`^${regexSource}$`, "i"), names, translation });
    } catch {
      // Ignore malformed catalog entries; they fall back to the source text.
    }
  }
  return patterns;
}

function phraseText(source, vars, depth) {
  if (!source || !phraseCatalog) return source;
  if (Object.prototype.hasOwnProperty.call(phraseCatalog, source)) {
    return interpolate(phraseCatalog[source], vars);
  }
  if (depth > 2) return source;
  for (const pattern of phrasePatterns || []) {
    const match = source.match(pattern.regex);
    if (!match) continue;
    const values = {};
    pattern.names.forEach((name, index) => {
      const raw = match[index + 1];
      values[name] = depth < 2 ? phraseText(raw, {}, depth + 1) : raw;
    });
    // A "one|other" translation selects the singular form when a numeric
    // {count} placeholder is exactly 1. This keeps count details such as
    // "1 recipient" / "5 recipients" grammatical without a full ICU runtime.
    let template = pattern.translation;
    if (template.includes("|")) {
      const countName = pattern.names.find((name) => name === "count" || /count$/i.test(name));
      const countValue = countName ? String(values[countName] || "").replace(/[^\d]/g, "") : "";
      const singular = countValue === "1";
      const options = template.split("|");
      template = singular ? options[0] : options[1] || options[0];
    }
    return interpolate(template, { ...values, ...vars });
  }
  return source;
}

async function ensurePhrases(catalog = currentCatalog) {
  if (!catalog || catalog === DEFAULT_CATALOG) {
    phraseCatalog = Object.create(null);
    phrasePatterns = [];
    return phraseCatalog;
  }
  if (phraseCatalogs.has(catalog)) {
    phraseCatalog = phraseCatalogs.get(catalog);
    phrasePatterns = buildPhrasePatterns(phraseCatalog);
    return phraseCatalog;
  }
  if (!phraseLoading.has(catalog)) {
    phraseLoading.set(catalog, fetchJson(`${PHRASE_ROOT}/${encodeURIComponent(catalog)}.json`)
      .then((raw) => normalizePhraseMap(raw))
      .catch(() => Object.create(null))
      .finally(() => phraseLoading.delete(catalog)));
  }
  const loaded = await phraseLoading.get(catalog);
  phraseCatalogs.set(catalog, loaded);
  phraseCatalog = loaded;
  phrasePatterns = buildPhrasePatterns(loaded);
  return loaded;
}

/** Load the phrase catalog used for server-provided and rendered UI text. */
export async function loadPhrases() {
  if (!manifest) return {};
  return ensurePhrases(currentCatalog);
}

/** Translate an English phrase from server data or a rendered string. */
export function tp(text, vars = {}) {
  const source = normalizePhrase(text);
  if (!source) return text;
  return phraseText(source, vars, 0);
}

export function applyStaticTranslations(root = document) {
  root.querySelectorAll("[data-i18n]").forEach((element) => {
    const key = element.getAttribute("data-i18n");
    const value = t(key);
    if (value !== key) element.textContent = value;
  });
  root.querySelectorAll("[data-i18n-placeholder]").forEach((element) => {
    const key = element.getAttribute("data-i18n-placeholder");
    const value = t(key);
    if (value !== key) element.setAttribute("placeholder", value);
  });
  root.querySelectorAll("[data-i18n-aria-label]").forEach((element) => {
    const key = element.getAttribute("data-i18n-aria-label");
    const value = t(key);
    if (value !== key) element.setAttribute("aria-label", value);
  });
  root.querySelectorAll("[data-i18n-title]").forEach((element) => {
    const key = element.getAttribute("data-i18n-title");
    const value = t(key);
    if (value !== key) element.setAttribute("title", value);
  });
  root.querySelectorAll("[data-i18n-alt]").forEach((element) => {
    const key = element.getAttribute("data-i18n-alt");
    const value = t(key);
    if (value !== key) element.setAttribute("alt", value);
  });
}

export function populateLanguageSelect(select) {
  if (!select || !manifest) return;
  const locales = supportedLocales();
  if (!locales.length) return;
  const available = new Set(locales.map((item) => item.code));
  const selected = available.has(currentValue) ? currentValue : (available.has(defaultValue) ? defaultValue : locales[0].code);
  select.innerHTML = locales.map((item) => (
    `<option value="${escapeOption(item.code)}"${item.code === selected ? " selected" : ""}>${escapeOption(item.label || item.code)}</option>`
  )).join("");
  select.value = selected;
}

/** The system default chosen in the Control Plane, after policy filtering. */
export function defaultLocale() {
  return defaultValue;
}

export async function setLocale(preferred) {
  if (!manifest) await initI18n(preferred);
  const meta = resolveLocaleMeta(preferred);
  currentMeta = meta;
  currentValue = meta.code || DEFAULT_VALUE;
  currentCatalog = meta.catalog || DEFAULT_CATALOG;
  currentDir = meta.dir || "ltr";
  currentPluralLocale = meta.status === "pseudo" ? "en" : currentValue;
  try {
    new Intl.PluralRules(currentPluralLocale);
  } catch {
    currentPluralLocale = DEFAULT_VALUE;
  }

  await ensureCatalog(currentCatalog);
  if (currentCatalog !== DEFAULT_CATALOG) await ensureCatalog(DEFAULT_CATALOG);
  ensurePhrases(currentCatalog).catch(() => {});

  document.documentElement.lang = currentValue;
  document.documentElement.dir = currentDir;
  document.documentElement.dataset.locale = currentCatalog;
  applyStaticTranslations(document);
  populateLanguageSelect(document.getElementById("languageSelect"));
  document.dispatchEvent(new CustomEvent("a8m:i18n", {
    detail: { locale: currentValue, catalog: currentCatalog, dir: currentDir, meta: currentMeta }
  }));
  return currentValue;
}

export async function initI18n(preferred) {
  if (initPromise) return initPromise;
  initPromise = (async () => {
    let loaded = null;
    // The Go/Node server returns the manifest with the Control Plane's
    // enabled/disabled flags already applied. This is the authoritative list;
    // the static file below is the offline and first-deploy fallback.
    try {
      loaded = await fetchJson("/api/locales");
    } catch {
      loaded = null;
    }
    if (!loaded || !Array.isArray(loaded.locales) || !loaded.locales.length) {
      try {
        loaded = await fetchJson(`${CATALOG_ROOT}/manifest.json`);
      } catch {
        loaded = null;
      }
    }
    manifest = loaded && Array.isArray(loaded.locales) && loaded.locales.length
      ? loaded
      : { default: DEFAULT_VALUE, locales: [defaultMeta()] };
    const configuredDefault = normalizeValue(manifest.default);
    if (configuredDefault) defaultValue = configuredDefault;
    await setLocale(preferred || defaultValue);
    await ensurePhrases(currentCatalog).catch(() => {});
    initialized = true;
    return currentValue;
  })();
  return initPromise;
}

export function isInitialized() {
  return initialized;
}
