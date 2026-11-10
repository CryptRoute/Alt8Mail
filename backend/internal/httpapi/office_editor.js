/* Isolated Alt8Mail Office editor host. It is framed by the webmail Cabinet. */
(function () {
  "use strict";

  var params = new URLSearchParams(window.location.search);
  var fileId = params.get("file") || "";
  var shareToken = params.get("share") || "";
  var theme = params.get("theme") === "dark" ? "theme-dark" : "theme-light";
  var loadedTheme = theme;
  var editor = null;
  var documentDirty = false;
  var pendingThemeReload = false;
  function applyHostTheme() {
    document.documentElement.setAttribute("data-theme", theme === "theme-dark" ? "dark" : "light");
  }
  function reloadWithTheme() {
    pendingThemeReload = false;
    var next = new URL(window.location.href);
    next.searchParams.set("theme", theme === "theme-dark" ? "dark" : "light");
    window.location.replace(next.toString());
  }
  function applyEditorTheme() {
    if (!editor) return;
    if (typeof editor.setTheme === "function") {
      try { editor.setTheme(theme); return; } catch (_error) {}
    }
    // The Docs API does not expose a reliable runtime theme switch. Reload the
    // editor host only after the current document reports a clean saved state.
    if (documentDirty) pendingThemeReload = true;
    else reloadWithTheme();
  }
  applyHostTheme();
  window.addEventListener("message", function (event) {
    if (event.origin !== window.location.origin) return;
    var data = event.data || {};
    if (data.type !== "a8m-office-theme") return;
    var nextTheme = data.theme === "dark" ? "theme-dark" : "theme-light";
    if (nextTheme === theme) return;
    theme = nextTheme;
    applyHostTheme();
    applyEditorTheme();
  });
  var state = document.getElementById("state");
  var spinner = document.getElementById("spinner");
  var stateMark = document.getElementById("stateMark");
  var stateTitle = document.getElementById("stateTitle");
  var stateMessage = document.getElementById("stateMessage");

  function csrfToken() {
    var prefix = "a8m_system_csrf=";
    var parts = document.cookie ? document.cookie.split("; ") : [];
    for (var index = 0; index < parts.length; index += 1) {
      if (parts[index].indexOf(prefix) === 0) {
        try { return decodeURIComponent(parts[index].slice(prefix.length)); } catch (_error) { return ""; }
      }
    }
    return "";
  }

  function notifyParent(type, detail) {
    try { window.parent.postMessage({ type: type, detail: detail || "" }, window.location.origin); } catch (_error) {}
  }

  function fail(message) {
    if (message && typeof message === "object") message = message.message || message.code || "";
    spinner.hidden = true;
    stateMark.hidden = false;
    stateTitle.textContent = "Office could not open";
    stateMessage.textContent = message || "Try closing this editor and opening the document again.";
    stateMessage.className = "error";
    notifyParent("a8m-office-error", message || "");
  }

  function loadScript(source) {
    return new Promise(function (resolve, reject) {
      var script = document.createElement("script");
      script.src = source;
      script.async = true;
      script.dataset.officeApi = "1";
      var timer = window.setTimeout(function () {
        script.remove();
        reject(new Error("The Office editor script timed out."));
      }, 20000);
      script.addEventListener("load", function () {
        window.clearTimeout(timer);
        resolve();
      });
      script.addEventListener("error", function () {
        window.clearTimeout(timer);
        reject(new Error("The Office editor script could not be loaded."));
      });
      document.head.appendChild(script);
    });
  }

  async function boot() {
    if (!fileId && !shareToken) {
      fail("No document was selected.");
      return;
    }
    var endpoint = shareToken
      ? "/api/public/shares/" + encodeURIComponent(shareToken) + "/office"
      : "/api/office/documents/" + encodeURIComponent(fileId) + "/open";
    var headers = { "Content-Type": "application/json" };
    if (!shareToken) headers["X-CSRF-Token"] = csrfToken();
    loadedTheme = theme;
    var response;
    try {
      response = await fetch(endpoint, {
        method: "POST",
        credentials: "same-origin",
        headers: headers,
        body: JSON.stringify({ theme: theme })
      });
    } catch (_error) {
      fail("The Alt8Mail API could not be reached.");
      return;
    }
    var data = {};
    try { data = await response.json(); } catch (_error) {}
    if (!response.ok) {
      fail(data.error || "Office is not available for this document.");
      return;
    }
    if (!data.documentServerUrl || !data.config) {
      fail("The Office editor configuration is incomplete.");
      return;
    }
    if (data.file && data.file.name) document.title = data.file.name + " · Alt8Mail Office";
    try {
      await loadScript(String(data.documentServerUrl).replace(/\/$/, "") + "/web-apps/apps/api/documents/api.js");
    } catch (error) {
      fail(error.message);
      return;
    }
    if (!window.DocsAPI || typeof window.DocsAPI.DocEditor !== "function") {
      fail("The Office editor did not initialize.");
      return;
    }
    state.hidden = true;
    try {
      data.config.events = Object.assign({}, data.config.events || {}, {
        onDocumentStateChange: function (event) {
          if (event && event.data) {
            documentDirty = true;
            return;
          }
          if (documentDirty) {
            documentDirty = false;
            notifyParent("a8m-office-saved", fileId || shareToken);
            if (pendingThemeReload) reloadWithTheme();
          }
        }
      });
      editor = new window.DocsAPI.DocEditor("editor", data.config);
      if (theme !== loadedTheme) applyEditorTheme();
      notifyParent("a8m-office-ready", fileId || shareToken);
      if (!shareToken && data.aiEnabled && typeof window.Alt8OfficeAI === "function") {
        window.Alt8OfficeAI(editor, data.document && data.document.id);
      }
    } catch (_error) {
      fail("The Office editor rejected the document configuration.");
    }
  }


  window.Alt8OfficeAI = function (docEditor, documentId) {
    var fab = document.getElementById("aiFab");
    var panel = document.getElementById("aiPanel");
    var close = document.getElementById("aiClose");
    var actions = document.querySelectorAll("[data-ai-action]");
    var languageWrap = document.getElementById("aiLanguageWrap");
    var language = document.getElementById("aiLanguage");
    var prompt = document.getElementById("aiPrompt");
    var generate = document.getElementById("aiGenerate");
    var statusEl = document.getElementById("aiStatus");
    var resultWrap = document.getElementById("aiResultWrap");
    var resultEl = document.getElementById("aiResult");
    var insert = document.getElementById("aiInsert");
    var copy = document.getElementById("aiCopy");
    if (!fab || !panel || !generate) return;
    var activeAction = "rewrite";
    var lastResult = "";
    var busy = false;
    var connector = null;
    var connectorPromise = null;

    function setStatus(message, isError) {
      statusEl.textContent = message || "";
      statusEl.className = "ai-status" + (isError ? " is-error" : "");
    }

    function setActive(action) {
      activeAction = action;
      actions.forEach(function (button) {
        button.classList.toggle("is-active", button.getAttribute("data-ai-action") === action);
      });
      languageWrap.hidden = action !== "translate";
    }

    function ensureConnector() {
      if (connector) return Promise.resolve(connector);
      if (connectorPromise) return connectorPromise;
      connectorPromise = new Promise(function (resolve) {
        var attempts = 0;
        var timer = setInterval(function () {
          attempts += 1;
          try {
            var candidate = docEditor.createConnector();
            if (candidate) {
              connector = candidate;
              clearInterval(timer);
              resolve(candidate);
              return;
            }
          } catch (_error) {}
          if (attempts >= 30) {
            clearInterval(timer);
            resolve(null);
          }
        }, 500);
      });
      return connectorPromise;
    }

    function selectedText() {
      return ensureConnector().then(function (ready) {
        if (!ready) return "";
        return new Promise(function (resolve) {
          try {
            ready.executeMethod("GetSelectedText", [{ Numbering: false, Math: false }], function (result) {
              if (typeof result === "string") resolve(result);
              else if (result && typeof result.text === "string") resolve(result.text);
              else resolve("");
            });
          } catch (_error) {
            resolve("");
          }
        });
      });
    }

    function copyResult() {
      if (!lastResult) return Promise.resolve(false);
      if (navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(lastResult).then(function () { return true; }, function () { return false; });
      }
      return Promise.resolve(false);
    }

    function requestAI(action, text) {
      var headers = { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() };
      return fetch("/api/office/ai", {
        method: "POST",
        credentials: "same-origin",
        headers: headers,
        body: JSON.stringify({
          action: action,
          documentId: documentId || "",
          text: text,
          prompt: prompt.value.trim(),
          language: language.value
        })
      }).then(function (response) {
        return response.json().catch(function () { return {}; }).then(function (data) {
          if (!response.ok) throw new Error((data.error && data.error.message) || "The AI assistant could not complete the request.");
          if (!data.text) throw new Error("The AI assistant returned an empty response.");
          return data;
        });
      });
    }

    function runGeneration() {
      if (busy) return;
      var action = activeAction;
      var promptValue = prompt.value.trim();
      var documentAction = ["ask", "action_items", "draft_reply"].indexOf(action) >= 0;
      if (action === "custom" && !promptValue) {
        setStatus("Describe what the assistant should write.", true);
        prompt.focus();
        return;
      }
      if (action === "ask" && !promptValue) {
        setStatus("Type a question about this document.", true);
        prompt.focus();
        return;
      }
      busy = true;
      generate.disabled = true;
      resultWrap.hidden = true;
      var prepare = documentAction ? Promise.resolve("") : selectedText().then(function (text) {
        text = String(text || "").trim().slice(0, 16000);
        if (!text && action !== "custom" && action !== "continue") {
          throw new Error("Select text in the document first.");
        }
        if (!text && action === "continue" && !promptValue) {
          throw new Error("Select text or describe what to continue.");
        }
        return text;
      });
      setStatus(documentAction ? "Reading the document…" : "Reading the selected text…", false);
      prepare.then(function (text) {
        setStatus("Writing…", false);
        return requestAI(action, text);
      }).then(function (data) {
        var display = data.text;
        if (data.items && data.items.length) {
          display += "\n\n" + data.items.map(function (item) {
            var suffix = item.owner ? " — " + item.owner : "";
            if (item.due) suffix += " (due " + item.due + ")";
            return "• " + item.task + suffix;
          }).join("\n");
        }
        if (data.citations && data.citations.length && !(data.items && data.items.length)) {
          display += "\n\nSources:\n" + data.citations.map(function (citation) {
            return "“" + citation.quote + "”";
          }).join("\n");
        }
        lastResult = data.text;
        resultEl.textContent = display;
        resultWrap.hidden = false;
        if (data.dlp && data.dlp.warnings && data.dlp.warnings.length) {
          setStatus(data.dlp.warnings.join(" "), false);
        } else {
          setStatus("Review the result, then insert it into the document.", false);
        }
      }).catch(function (error) {
        setStatus(error.message || "The AI assistant could not complete the request.", true);
      }).then(function () {
        busy = false;
        generate.disabled = false;
      });
    }

    fab.addEventListener("click", function () {
      panel.hidden = false;
      fab.hidden = true;
      setStatus("", false);
      setTimeout(function () { prompt.focus(); }, 40);
    });
    close.addEventListener("click", function () {
      panel.hidden = true;
      fab.hidden = false;
    });
    actions.forEach(function (button) {
      button.addEventListener("click", function () {
        setActive(button.getAttribute("data-ai-action"));
        setStatus("", false);
      });
    });
    generate.addEventListener("click", runGeneration);
    prompt.addEventListener("keydown", function (event) {
      if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
        event.preventDefault();
        runGeneration();
      }
    });
    insert.addEventListener("click", function () {
      if (!lastResult) return;
      ensureConnector().then(function (ready) {
        if (!ready) {
          copyResult().then(function (copied) {
            setStatus(copied ? "Copied. Paste it into the document." : "Insert is unavailable. Select and copy the result manually.", !copied);
          });
          return;
        }
        try {
          try { docEditor.focus(); } catch (_error) {}
          ready.executeMethod("PasteText", [lastResult], function () {
            setStatus("Inserted into the document.", false);
          });
        } catch (_error) {
          copyResult().then(function (copied) {
            setStatus(copied ? "Copied. Paste it into the document." : "Insert is unavailable. Select and copy the result manually.", !copied);
          });
        }
      });
    });
    copy.addEventListener("click", function () {
      copyResult().then(function (copied) {
        setStatus(copied ? "Copied to the clipboard." : "Clipboard access is unavailable.", !copied);
      });
    });
    setActive("rewrite");
    fab.hidden = false;
  };

  window.addEventListener("pagehide", function () {
    try { if (editor && typeof editor.destroyEditor === "function") editor.destroyEditor(); } catch (_error) {}
  });

  boot();
})();
