/*
 * Alt8Mail customer admin localization runtime.
 *
 * Classic script (the admin console has no module graph). It loads the phrase
 * catalog for the active language and translates rendered English text in
 * place, including text produced by API responses and toasts.
 */
(function () {
  "use strict";

  var STORAGE_KEY = "a8m.admin.language";
  var LOCALES_URL = "/admin/api/locales";
  var CATALOG_BASE = "/admin/console-locales";
  var DEFAULT_VALUE = "en-US";
  var DEFAULT_CATALOG = "en";

  var entries = [];
  var defaultCode = DEFAULT_VALUE;
  var userLanguage = "";
  var currentCode = DEFAULT_VALUE;
  var currentCatalog = DEFAULT_CATALOG;
  var catalog = Object.create(null);
  var patterns = [];
  var initialized = false;
  var observer = null;
  var listeners = [];
  var initPromise = null;

  function normalize(value) { return String(value || "").trim().replace(/_/g, "-"); }
  function primary(value) { return normalize(value).split("-")[0].toLowerCase(); }
  function normalizePhrase(value) { return String(value || "").replace(/’/g, "'").replace(/\s+/g, " ").trim(); }

  function interpolate(template, vars) {
    return String(template).replace(/\{([A-Za-z0-9_]+)\}/g, function (match, name) {
      return vars && Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match;
    });
  }

  function entryFor(code) {
    return entries.find(function (entry) {
      return normalize(entry.code).toLowerCase() === normalize(code).toLowerCase();
    }) || null;
  }

  function resolveEntry(code) {
    var requested = normalize(code);
    if (!entries.length) return { code: defaultCode || DEFAULT_VALUE, catalog: DEFAULT_CATALOG, dir: "ltr", label: "English (United States)" };
    if (!requested) return entryFor(defaultCode) || entries[0];
    var exact = entries.find(function (entry) { return normalize(entry.code).toLowerCase() === requested.toLowerCase(); });
    if (exact) return exact;
    var parts = requested.split("-");
    for (var length = parts.length - 1; length >= 1; length -= 1) {
      var prefix = parts.slice(0, length).join("-").toLowerCase();
      var match = entries.find(function (entry) { return normalize(entry.code).toLowerCase() === prefix; });
      if (match) return match;
    }
    var baseMatch = entries.find(function (entry) { return primary(entry.code) === primary(requested); });
    if (baseMatch) return baseMatch;
    return entryFor(defaultCode) || entries[0];
  }

  function buildPatterns(map) {
    var result = [];
    Object.keys(map).forEach(function (source) {
      if (source.indexOf("{") === -1) return;
      var names = [];
      var escaped = source.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      var regexSource = escaped.replace(/\\\{([A-Za-z0-9_]+)\\\}/g, function (_match, name) {
        names.push(name);
        // Counts and measurements only match their own shape so a generic unit
        // pattern cannot rewrite prose or a user-provided title.
        if (name === "count") return "(\\d[\\d,.]*)";
        if (name === "percent") return "(\\d+(?:[.,]\\d+)?%)";
        if (name === "used" || name === "total") return "(\\d+(?:[.,]\\d+)?\\s*[KMGT]?B)";
        return "(.+?)";
      });
      if (!names.length) return;
      try {
        result.push({ regex: new RegExp("^" + regexSource + "$", "i"), names: names, translation: map[source] });
      } catch (error) { /* ignore malformed phrase */ }
    });
    return result;
  }

  function translateValue(value, vars, depth) {
    var source = normalizePhrase(value);
    if (!source) return value;
    depth = depth || 0;
    if (Object.prototype.hasOwnProperty.call(catalog, source)) return interpolate(catalog[source], vars);
    if (depth > 2) return value;
    for (var index = 0; index < patterns.length; index += 1) {
      var pattern = patterns[index];
      var match = source.match(pattern.regex);
      if (!match) continue;
      var values = {};
      pattern.names.forEach(function (name, i) {
        var raw = match[i + 1];
        values[name] = depth < 2 ? translateValue(raw, {}, depth + 1) : raw;
      });
      var template = pattern.translation;
      if (template.indexOf("|") !== -1) {
        var countName = "";
        for (var nameIndex = 0; nameIndex < pattern.names.length; nameIndex += 1) {
          if (pattern.names[nameIndex] === "count" || /count$/i.test(pattern.names[nameIndex])) { countName = pattern.names[nameIndex]; break; }
        }
        var countValue = countName ? String(values[countName] || "").replace(/[^\d]/g, "") : "";
        var options = template.split("|");
        template = countValue === "1" ? options[0] : options[1] || options[0];
      }
      return interpolate(template, Object.assign({}, values, vars || {}));
    }
    return value;
  }

  function t(value, vars) { return translateValue(value, vars); }

  var SKIP = "[data-i18n-ignore],script,style,code,pre,textarea,svg,.mono,.code,.code-block,.terminal,.xterm,.log,.log-line,.detail-row b,.kv-grid dd,[contenteditable='true']";
  var ATTRIBUTES = ["placeholder", "title", "aria-label", "alt"];

  function closestSkip(node) {
    var element = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
    return element && element.closest ? element.closest(SKIP) : null;
  }

  function translateTextNode(node) {
    var raw = node.nodeValue;
    if (!raw || !/[A-Za-z]/.test(raw)) return;
    var leading = raw.match(/^\s*/)[0];
    var trailing = raw.match(/\s*$/)[0];
    var core = raw.trim();
    var translated = translateValue(core);
    if (translated !== core) node.nodeValue = leading + translated + trailing;
  }

  function translateAttributes(element) {
    ATTRIBUTES.forEach(function (name) {
      var value = element.getAttribute(name);
      if (!value || !/[A-Za-z]/.test(value)) return;
      var translated = translateValue(value);
      if (translated !== value) element.setAttribute(name, translated);
    });
  }

  function translateTree(root) {
    root = root || document.body;
    if (!root) return;
    if (root.nodeType === Node.TEXT_NODE) {
      if (!closestSkip(root)) translateTextNode(root);
      return;
    }
    if (root.nodeType === Node.ELEMENT_NODE && !closestSkip(root)) translateAttributes(root);
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
    var node = walker.nextNode();
    while (node) {
      if (node.nodeType === Node.TEXT_NODE) {
        if (!closestSkip(node)) translateTextNode(node);
      } else if (!closestSkip(node)) {
        translateAttributes(node);
      }
      node = walker.nextNode();
    }
  }

  function fetchJSON(url) {
    return fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" }, signal: AbortSignal.timeout(10000) })
      .then(function (response) {
        if (!response.ok) throw new Error(url + ": HTTP " + response.status);
        return response.json();
      });
  }

  function loadCatalog(name) {
    if (!name || name === DEFAULT_CATALOG) {
      catalog = Object.create(null);
      patterns = [];
      return Promise.resolve();
    }
    return fetchJSON(CATALOG_BASE + "/" + encodeURIComponent(name) + ".json")
      .then(function (raw) {
        var map = Object.create(null);
        if (raw && typeof raw === "object" && !Array.isArray(raw)) {
          Object.keys(raw).forEach(function (key) {
            if (typeof raw[key] === "string") map[normalizePhrase(key)] = raw[key];
          });
        }
        catalog = map;
        patterns = buildPatterns(map);
      })
      .catch(function () {
        catalog = Object.create(null);
        patterns = [];
      });
  }

  function applyDocument() {
    document.documentElement.lang = currentCode;
    document.documentElement.dir = resolveEntry(currentCode).dir || "ltr";
    translateTree(document.body);
  }

  function startObserver() {
    if (observer || !document.body) return;
    observer = new MutationObserver(function (records) {
      records.forEach(function (record) {
        if (record.type === "characterData") {
          if (!closestSkip(record.target)) translateTextNode(record.target);
        } else if (record.type === "attributes") {
          if (!closestSkip(record.target)) translateAttributes(record.target);
        } else {
          Array.prototype.forEach.call(record.addedNodes, function (node) { translateTree(node); });
        }
      });
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ATTRIBUTES });
  }

  function setLocale(code, options) {
    options = options || {};
    var entry = resolveEntry(code);
    var nextCode = entry.code || DEFAULT_VALUE;
    var nextCatalog = entry.catalog || DEFAULT_CATALOG;
    var loading = nextCatalog !== currentCatalog || nextCode !== currentCode ? loadCatalog(nextCatalog) : Promise.resolve();
    return loading.then(function () {
      currentCode = nextCode;
      currentCatalog = nextCatalog;
      if (options.persist) {
        try {
          if (userLanguage) localStorage.setItem(STORAGE_KEY, userLanguage);
          else localStorage.removeItem(STORAGE_KEY);
        } catch (error) { /* ignore */ }
      }
      applyDocument();
      document.dispatchEvent(new CustomEvent("a8m:i18n", { detail: { locale: currentCode, catalog: currentCatalog, dir: entry.dir || "ltr" } }));
      listeners.forEach(function (listener) { listener(currentCode); });
      return currentCode;
    });
  }

  function populateLanguageSelect(select) {
    if (!select) return;
    var parts = ['<option value="">' + escapeHtml(t("System default") + " (" + ((entryFor(defaultCode) || {}).label || defaultCode) + ")") + "</option>"];
    entries.forEach(function (entry) {
      parts.push('<option value="' + escapeHtml(entry.code) + '">' + escapeHtml(entry.label || entry.code) + "</option>");
    });
    select.innerHTML = parts.join("");
    select.value = userLanguage || "";
  }

  function escapeHtml(value) {
    return String(value == null ? "" : value)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  function setUserLanguage(language, options) {
    userLanguage = normalize(language);
    if (userLanguage && !entryFor(userLanguage)) userLanguage = "";
    return setLocale(userLanguage || defaultCode, options);
  }

  function setDefaultLanguage(language) {
    var next = normalize(language);
    if (next) defaultCode = next;
    return userLanguage ? Promise.resolve(currentCode) : setLocale(defaultCode, { persist: false });
  }

  function init() {
    if (initPromise) return initPromise;
    initPromise = fetchJSON(LOCALES_URL)
      .then(function (payload) {
        entries = Array.isArray(payload.locales)
          ? payload.locales.filter(function (entry) { return entry.enabled !== false && entry.status !== "pseudo" && !entry.hidden; })
          : [];
        defaultCode = normalize(payload.default) || DEFAULT_VALUE;
      })
      .catch(function () {
        return fetchJSON("/admin/locales/manifest.json").then(function (manifest) {
          entries = (manifest.locales || []).filter(function (entry) { return entry.enabled !== false && entry.status !== "pseudo" && !entry.hidden; });
          defaultCode = normalize(manifest.default) || DEFAULT_VALUE;
        }).catch(function () {
          entries = [{ code: DEFAULT_VALUE, catalog: DEFAULT_CATALOG, label: "English (United States)", dir: "ltr" }];
        });
      })
      .then(function () {
        try { userLanguage = normalize(localStorage.getItem(STORAGE_KEY)); } catch (error) { userLanguage = ""; }
        if (userLanguage && !entryFor(userLanguage)) userLanguage = "";
        return setLocale(userLanguage || defaultCode, { persist: false });
      })
      .then(function () {
        initialized = true;
        startObserver();
        return currentCode;
      });
    return initPromise;
  }

  window.A8M_I18N = {
    ready: init(),
    t: t,
    tp: t,
    translateTree: translateTree,
    setLocale: setLocale,
    setUserLanguage: setUserLanguage,
    setDefaultLanguage: setDefaultLanguage,
    populateLanguageSelect: populateLanguageSelect,
    currentLocale: function () { return currentCode; },
    currentLanguagePreference: function () { return userLanguage; },
    onLocaleChange: function (listener) { listeners.push(listener); return function () { listeners = listeners.filter(function (item) { return item !== listener; }); }; },
    isInitialized: function () { return initialized; },
    escapeHtml: escapeHtml
  };
})();
