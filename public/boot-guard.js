/*
 * Alt8Mail boot watchdog.
 *
 * If the module graph fails to parse, an import 404s, or a boot step hangs,
 * the static splash would otherwise spin forever with no actionable error.
 * This file is a classic script so it runs even when app.js cannot.
 *
 * The copy uses keys that already exist in every webmail catalog; if i18n
 * did load, the normal static-translation pass replaces the English fallback.
 */
(function () {
  "use strict";

  var WATCHDOG_MS = 20000;
  var timer = null;
  var finished = false;

  function splash() {
    return document.getElementById("bootSplash");
  }

  function booted() {
    var node = splash();
    if (!node) return true;
    if (node.hidden || node.classList.contains("is-hiding") || node.classList.contains("is-logout")) return true;
    var login = document.getElementById("loginScreen");
    if (login && !login.hidden) return true;
    var app = document.getElementById("app");
    return Boolean(app && !app.hidden);
  }

  function finish() {
    if (finished) return;
    finished = true;
    if (timer) clearInterval(timer);
  }

  function stall() {
    if (finished || booted()) {
      if (booted()) finish();
      return;
    }
    var node = splash();
    if (!node || node.querySelector("[data-boot-stalled]")) return;
    finished = true;
    if (timer) clearInterval(timer);
    node.classList.add("is-stalled");
    node.setAttribute("aria-busy", "false");
    var panel = document.createElement("div");
    panel.setAttribute("data-boot-stalled", "");
    panel.setAttribute("role", "alert");
    panel.style.cssText = "margin-top:20px;max-width:360px;padding:14px 16px;border:1px solid rgba(255,255,255,.18);border-radius:12px;background:rgba(9,50,86,.55);color:#f4f8ff;font:13px/1.5 system-ui,sans-serif;text-align:left";
    panel.innerHTML = '<p style="margin:0 0 4px;font-weight:700" data-i18n="common.error">Something went wrong</p>' +
      '<p style="margin:0 0 12px;opacity:.82" data-i18n="ui.auth-boot.something-went-wrong-please-try-again">Something went wrong. Please try again.</p>' +
      '<button type="button" data-boot-retry style="appearance:none;border:0;border-radius:8px;padding:8px 13px;background:#fff;color:#093256;font:600 13px system-ui,sans-serif;cursor:pointer" data-i18n="common.retry">Try again</button>';
    panel.querySelector("[data-boot-retry]").addEventListener("click", function () {
      window.location.reload();
    });
    var inner = node.querySelector(".boot-splash-inner") || node;
    inner.appendChild(panel);
    if (inner.querySelector(".boot-progress")) inner.querySelector(".boot-progress").hidden = true;
  }

  window.addEventListener("a8m:booted", finish, { once: true });
  window.addEventListener("error", function (event) {
    // Resource 404s (a missing icon, for example) target an element and are
    // not fatal. Only surface real script/exception failures.
    if (event && event.target && event.target !== window) return;
    setTimeout(stall, 4000);
  }, true);
  window.addEventListener("unhandledrejection", function () {
    setTimeout(stall, 4000);
  });
  timer = setInterval(function () {
    if (booted()) finish();
  }, 750);
  setTimeout(stall, WATCHDOG_MS);

  // The watchdog timer must not keep an old browser tab alive on its own.
  if (timer && typeof timer.unref === "function") timer.unref();
})();
