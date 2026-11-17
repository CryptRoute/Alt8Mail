/*
 * Alt8Mail System Control localization runtime.
 *
 * The control console has no build step. This module loads the phrase catalog
 * for the active language and translates rendered English text in place, so
 * server error messages, toast text and templates added after boot are covered
 * without marking every string individually.
 */
const STORAGE_KEY = "alt8mail.control.language";
const LOCALES_URL = "/control/api/locales";
const CATALOG_BASE = "/console-locales";
const DEFAULT_VALUE = "en-US";
const DEFAULT_CATALOG = "en";

let manifest = null;
let entries = [];
let defaultCode = DEFAULT_VALUE;
let userLanguage = "";
let currentCode = DEFAULT_VALUE;
let currentCatalog = DEFAULT_CATALOG;
let catalog = Object.create(null);
let patterns = [];
let initialized = false;
let initPromise = null;
let observer = null;
const listeners = new Set();

function normalize(value) {
  return String(value || "").trim().replace(/_/g, "-");
}

function primary(value) {
  return normalize(value).split("-")[0].toLowerCase();
}

function normalizePhrase(value) {
  return String(value || "").replace(/’/g, "'").replace(/\s+/g, " ").trim();
}

function escapeHtml(value) {
  return String(value == null ? "" : value)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

function interpolate(template, vars) {
  return String(template).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    vars && Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match
  ));
}

function resolveEntry(code) {
  const requested = normalize(code);
  if (!entries.length) return { code: defaultCode || DEFAULT_VALUE, catalog: DEFAULT_CATALOG, dir: "ltr", label: "English (United States)" };
  if (!requested) return entryFor(defaultCode) || entries[0];
  const exact = entries.find((entry) => normalize(entry.code).toLowerCase() === requested.toLowerCase());
  if (exact) return exact;
  const parts = requested.split("-");
  for (let length = parts.length - 1; length >= 1; length -= 1) {
    const prefix = parts.slice(0, length).join("-").toLowerCase();
    const match = entries.find((entry) => normalize(entry.code).toLowerCase() === prefix);
    if (match) return match;
  }
  const baseMatch = entries.find((entry) => primary(entry.code) === primary(requested));
  if (baseMatch) return baseMatch;
  return entryFor(defaultCode) || entries[0];
}

function entryFor(code) {
  return entries.find((entry) => normalize(entry.code).toLowerCase() === normalize(code).toLowerCase()) || null;
}

function buildPatterns(map) {
  const result = [];
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
      result.push({ regex: new RegExp(`^${regexSource}$`, "i"), names, translation });
    } catch { /* ignore malformed phrase */ }
  }
  return result;
}

function translateValue(value, vars = {}, depth = 0) {
  const source = normalizePhrase(value);
  if (!source) return value;
  if (Object.prototype.hasOwnProperty.call(catalog, source)) {
    return interpolate(catalog[source], vars);
  }
  if (depth > 2) return value;
  for (const pattern of patterns) {
    const match = source.match(pattern.regex);
    if (!match) continue;
    const values = {};
    pattern.names.forEach((name, index) => {
      const raw = match[index + 1];
      values[name] = depth < 2 ? translateValue(raw, {}, depth + 1) : raw;
    });
    // "one|other" selects the singular form for a numeric count of exactly 1.
    let template = pattern.translation;
    if (template.includes("|")) {
      const countName = pattern.names.find((name) => name === "count" || /count$/i.test(name));
      const countValue = countName ? String(values[countName] || "").replace(/[^\d]/g, "") : "";
      const options = template.split("|");
      template = countValue === "1" ? options[0] : options[1] || options[0];
    }
    return interpolate(template, { ...values, ...vars });
  }
  return value;
}

/** Translate dynamic server text. */
export function t(value, vars = {}) {
  return translateValue(value, vars);
}

export const tp = t;

function translateTextNode(node) {
  const raw = node.nodeValue;
  if (!raw || !/[A-Za-z]/.test(raw)) return;
  const leading = raw.match(/^\s*/)[0];
  const trailing = raw.match(/\s*$/)[0];
  const core = raw.trim();
  const translated = translateValue(core);
  if (translated !== core) node.nodeValue = leading + translated + trailing;
}

function closestSkip(node) {
  const element = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
  return element && element.closest
    ? element.closest("[data-i18n-ignore],script,style,code,pre,textarea,svg,.mono,.code,.code-block,.terminal,.xterm,.log,.log-line,.detail-row b,.kv-grid dd,[contenteditable='true']")
    : null;
}

const ATTRIBUTES = ["placeholder", "title", "aria-label", "alt"];

function translateElementAttributes(element) {
  for (const name of ATTRIBUTES) {
    const value = element.getAttribute(name);
    if (!value || !/[A-Za-z]/.test(value)) continue;
    const translated = translateValue(value);
    if (translated !== value) element.setAttribute(name, translated);
  }
}

export function translateTree(root = document.body) {
  if (!root || !catalog) return;
  if (root.nodeType === Node.TEXT_NODE) {
    if (!closestSkip(root)) translateTextNode(root);
    return;
  }
  if (root.nodeType !== Node.ELEMENT_NODE && root.nodeType !== Node.DOCUMENT_NODE) return;
  translatedElements(root);
}

function translatedElements(root) {
  if (root.nodeType === Node.ELEMENT_NODE) {
    if (closestSkip(root)) return;
    translateElementAttributes(root);
  }
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
  let node = walker.nextNode();
  while (node) {
    if (node.nodeType === Node.TEXT_NODE) {
      if (!closestSkip(node)) translateTextNode(node);
    } else {
      translateElementAttributes(node);
    }
    node = walker.nextNode();
  }
}

async function fetchJSON(url) {
  const response = await fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" }, signal: AbortSignal.timeout(10000) });
  if (!response.ok) throw new Error(`${url}: HTTP ${response.status}`);
  return response.json();
}

async function loadCatalog(name) {
  if (!name || name === DEFAULT_CATALOG) {
    catalog = Object.create(null);
    patterns = [];
    return;
  }
  try {
    const raw = await fetchJSON(`${CATALOG_BASE}/${encodeURIComponent(name)}.json`);
    const map = Object.create(null);
    if (raw && typeof raw === "object" && !Array.isArray(raw)) {
      for (const [key, value] of Object.entries(raw)) {
        if (typeof value === "string") map[normalizePhrase(key)] = value;
      }
    }
    catalog = map;
    patterns = buildPatterns(map);
  } catch {
    catalog = Object.create(null);
    patterns = [];
  }
}

function applyDocument() {
  if (!catalog) return;
  document.documentElement.lang = currentCode;
  document.documentElement.dir = resolveEntry(currentCode).dir || "ltr";
  translateTree(document.body);
}

function startObserver() {
  if (observer || !document.body) return;
  observer = new MutationObserver((records) => {
    for (const record of records) {
      if (record.type === "characterData") {
        if (!closestSkip(record.target)) translateTextNode(record.target);
      } else if (record.type === "attributes") {
        if (!closestSkip(record.target)) translateElementAttributes(record.target);
      } else {
        for (const node of record.addedNodes) translateTree(node);
      }
    }
  });
  observer.observe(document.body, {
    childList: true,
    subtree: true,
    characterData: true,
    attributes: true,
    attributeFilter: ATTRIBUTES,
  });
}

export async function setLocale(code, { persist = true } = {}) {
  const entry = resolveEntry(code);
  const nextCode = entry.code || DEFAULT_VALUE;
  const nextCatalog = entry.catalog || DEFAULT_CATALOG;
  if (nextCatalog !== currentCatalog || nextCode !== currentCode) {
    await loadCatalog(nextCatalog);
  }
  currentCode = nextCode;
  currentCatalog = nextCatalog;
  if (persist) {
    try {
      if (userLanguage) localStorage.setItem(STORAGE_KEY, userLanguage);
      else localStorage.removeItem(STORAGE_KEY);
    } catch { /* ignore */ }
  }
  applyDocument();
  document.dispatchEvent(new CustomEvent("a8m:i18n", {
    detail: { locale: currentCode, catalog: currentCatalog, dir: entry.dir || "ltr" },
  }));
  for (const listener of listeners) listener(currentCode);
  return currentCode;
}

export function currentLocale() {
  return currentCode;
}

export function currentLanguagePreference() {
  return userLanguage;
}

export function populateLanguageSelect(select) {
  if (!select) return;
  const systemLabel = t("System default");
  const options = [`<option value="">${escapeHtml(`${systemLabel} (${entryFor(defaultCode)?.label || defaultCode})`)}</option>`]
    .concat(entries.map((entry) => `<option value="${escapeHtml(entry.code)}">${escapeHtml(entry.label || entry.code)}</option>`));
  select.innerHTML = options.join("");
  select.value = userLanguage || "";
}

/** Apply the language stored on the signed-in operator account. */
export async function setUserLanguage(language, { persist = false } = {}) {
  userLanguage = normalize(language);
  if (userLanguage && !entryFor(userLanguage)) userLanguage = "";
  const next = userLanguage || defaultCode;
  await setLocale(next, { persist });
  populateLanguageSelect(document.getElementById("languageSelect"));
  populateLanguageSelect(document.getElementById("profileLanguageSelect"));
}

export async function setDefaultLanguage(language) {
  const next = normalize(language);
  if (next) defaultCode = next;
  if (!userLanguage) await setLocale(defaultCode, { persist: false });
}

export function onLocaleChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export async function initI18n() {
  if (initPromise) return initPromise;
  initPromise = (async () => {
    try {
      const payload = await fetchJSON(LOCALES_URL);
      manifest = payload;
      entries = Array.isArray(payload.locales)
        ? payload.locales.filter((entry) => entry.enabled !== false && entry.status !== "pseudo" && !entry.hidden)
        : [];
      defaultCode = normalize(payload.default) || DEFAULT_VALUE;
    } catch {
      entries = [];
    }
    if (!entries.length) {
      try {
        const fallback = await fetchJSON("/locales/manifest.json");
        entries = (fallback.locales || []).filter((entry) => entry.enabled !== false && entry.status !== "pseudo" && !entry.hidden);
        defaultCode = normalize(fallback.default) || DEFAULT_VALUE;
      } catch {
        entries = [{ code: DEFAULT_VALUE, catalog: DEFAULT_CATALOG, label: "English (United States)", dir: "ltr" }];
      }
    }
    try { userLanguage = normalize(localStorage.getItem(STORAGE_KEY)); } catch { userLanguage = ""; }
    if (userLanguage && !entryFor(userLanguage)) userLanguage = "";
    await setLocale(userLanguage || defaultCode, { persist: false });
    initialized = true;
    startObserver();
    return currentCode;
  })();
  return initPromise;
}

export function isInitialized() {
  return initialized;
}

export { escapeHtml };
