/*
 * Alt8Mail UI localization.
 *
 * English is the canonical catalog. Catalogs are versioned JSON files served
 * from /locales/<base-language>/<namespace>.json. A missing key falls back to
 * English, then to the key itself so a broken catalog never blanks the UI.
 */

const DEFAULT_BASE = "en";
const DEFAULT_VALUE = "en-US";
const CATALOG_ROOT = "/locales";
const NAMESPACES = ["common"];

let manifest = null;
let currentBase = DEFAULT_BASE;
let currentValue = DEFAULT_VALUE;
let currentDir = "ltr";
let catalogs = new Map();
let loading = new Map();
let initialized = false;
let initPromise = null;

function normalizeBase(value) {
  return String(value || DEFAULT_VALUE).split("-")[0].toLowerCase();
}

function metaFor(value) {
  if (!manifest) return null;
  const exact = manifest.locales.find((item) => item.value === value);
  if (exact) return exact;
  const base = normalizeBase(value);
  return manifest.locales.find((item) => item.code === base) || manifest.locales[0];
}

async function fetchJson(url) {
  const response = await fetch(url, { credentials: "same-origin" });
  if (!response.ok) throw new Error(`i18n catalog ${url}: HTTP ${response.status}`);
  return response.json();
}

async function ensureNamespace(base, namespace) {
  const key = `${base}/${namespace}`;
  if (catalogs.has(key)) return catalogs.get(key);
  if (!loading.has(key)) {
    loading.set(key, fetchJson(`${CATALOG_ROOT}/${base}/${namespace}.json`)
      .catch(() => null)
      .finally(() => loading.delete(key)));
  }
  const catalog = (await loading.get(key)) || {};
  catalogs.set(key, catalog);
  return catalog;
}

function readCatalog(key) {
  for (const namespace of NAMESPACES) {
    const catalog = catalogs.get(`${currentBase}/${namespace}`) || {};
    if (Object.prototype.hasOwnProperty.call(catalog, key)) return catalog[key];
  }
  for (const namespace of NAMESPACES) {
    const catalog = catalogs.get(`${DEFAULT_BASE}/${namespace}`) || {};
    if (Object.prototype.hasOwnProperty.call(catalog, key)) return catalog[key];
  }
  return null;
}

function interpolate(template, vars) {
  return String(template).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match
  ));
}

/** Translate a stable key, with optional {placeholder} interpolation. */
export function t(key, vars = {}) {
  const value = readCatalog(key);
  if (value == null) return key;
  if (typeof value === "string") return interpolate(value, vars);
  return value;
}

/** Translate a pluralized key using Intl.PluralRules and catalog forms. */
export function tn(key, count, vars = {}) {
  const category = new Intl.PluralRules(currentValue).select(Number(count));
  const forms = readCatalog(key);
  if (forms && typeof forms === "object") {
    const template = forms[category] || forms.other || forms[Object.keys(forms)[0]];
    if (template != null) return interpolate(template, { ...vars, count });
  }
  return t(key, { ...vars, count });
}

export function currentLocale() {
  return currentValue;
}

export function currentDirection() {
  return currentDir;
}

export function supportedLocales() {
  return manifest ? manifest.locales.slice() : [{ code: "en", value: DEFAULT_VALUE, label: "English (United States)", dir: "ltr" }];
}

export function applyStaticTranslations(root = document) {
  root.querySelectorAll("[data-i18n]").forEach((element) => {
    const value = t(element.getAttribute("data-i18n"));
    if (value !== element.getAttribute("data-i18n")) element.textContent = value;
  });
  root.querySelectorAll("[data-i18n-placeholder]").forEach((element) => {
    const value = t(element.getAttribute("data-i18n-placeholder"));
    if (value !== element.getAttribute("data-i18n-placeholder")) element.setAttribute("placeholder", value);
  });
  root.querySelectorAll("[data-i18n-aria-label]").forEach((element) => {
    const value = t(element.getAttribute("data-i18n-aria-label"));
    if (value !== element.getAttribute("data-i18n-aria-label")) element.setAttribute("aria-label", value);
  });
  root.querySelectorAll("[data-i18n-title]").forEach((element) => {
    const value = t(element.getAttribute("data-i18n-title"));
    if (value !== element.getAttribute("data-i18n-title")) element.setAttribute("title", value);
  });
}

export function populateLanguageSelect(select) {
  if (!select || !manifest) return;
  const selected = currentValue;
  select.innerHTML = manifest.locales.map((item) => (
    `<option value="${item.value}"${item.value === selected ? " selected" : ""}>${item.label}</option>`
  )).join("");
}

export async function setLocale(preferred) {
  if (!manifest) await initI18n(preferred);
  const meta = metaFor(preferred);
  currentValue = meta ? meta.value : DEFAULT_VALUE;
  currentBase = meta ? meta.code : DEFAULT_BASE;
  currentDir = meta ? meta.dir || "ltr" : "ltr";
  await Promise.all(NAMESPACES.map((namespace) => ensureNamespace(currentBase, namespace)));
  if (currentBase !== DEFAULT_BASE) {
    await Promise.all(NAMESPACES.map((namespace) => ensureNamespace(DEFAULT_BASE, namespace)));
  }
  document.documentElement.lang = currentValue;
  document.documentElement.dir = currentDir;
  applyStaticTranslations(document);
  populateLanguageSelect(document.getElementById("languageSelect"));
  document.dispatchEvent(new CustomEvent("a8m:i18n", { detail: { locale: currentValue, base: currentBase, dir: currentDir } }));
  return currentValue;
}

export async function initI18n(preferred) {
  if (initPromise) return initPromise;
  initPromise = (async () => {
    try {
      manifest = await fetchJson(`${CATALOG_ROOT}/manifest.json`);
    } catch {
      manifest = { default: "en", locales: [{ code: "en", value: DEFAULT_VALUE, label: "English (United States)", dir: "ltr" }] };
    }
    await setLocale(preferred || navigator.language || DEFAULT_VALUE);
    initialized = true;
    return currentValue;
  })();
  return initPromise;
}

export function isInitialized() {
  return initialized;
}
