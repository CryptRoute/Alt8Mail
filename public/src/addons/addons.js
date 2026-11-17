/* Add-ons directory, setup and reader actions. */
import { api } from "../core/api.js";
import { appConfirm } from "../core/dom.js";
import { avatar, esc } from "../core/helpers.js";
import { state } from "../core/state.js";
import { showError, showToast } from "../core/toast.js";
import { t, tn, tp } from "../../i18n.mjs";

let hooks = { currentThread: () => null, ensureAISettingsUi: () => {} };
export function configureAddons(next = {}) { hooks = { ...hooks, ...next }; }

export function ensureAddonsSettingsUi() {
  const railAddons = document.getElementById("railAddons");
  if (railAddons) railAddons.hidden = Boolean(state.delegatedMode);
  if (state.delegatedMode) {
    document.getElementById("addonSettingsNav")?.remove();
    document.querySelector('[data-settings-panel="addons"]')?.remove();
    return;
  }
  if (document.getElementById("addonSettingsNav")) return;
  const nav = document.querySelector(".settings-nav-list");
  const about = nav?.querySelector('[data-settings-tab="about"]');
  if (nav) {
    const button = document.createElement("button");
    button.className = "settings-nav-item";
    button.id = "addonSettingsNav";
    button.type = "button";
    button.setAttribute("data-settings-tab", "addons");
    button.innerHTML = `<svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg><span>${t("addons.tool-label")}</span><span class="addon-nav-badge" id="addonNavBadge" hidden>0</span><svg class="icon nav-chevron" aria-hidden="true"><use href="#i-chevron-right"/></svg>`;
    nav.insertBefore(button, about || null);
  }
  const panelHost = document.querySelector(".settings-panels");
  if (!panelHost) return;
  const panel = document.createElement("section");
  panel.className = "settings-panel";
  panel.setAttribute("data-settings-panel", "addons");
  panel.hidden = true;
  panel.innerHTML = `
    <div class="addons-settings" id="addonsSettings">
      <section class="addons-hero">
        <div class="addons-hero-copy">
          <span class="addons-hero-mark"><svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg></span>
          <div><p class="eyebrow">${t("addons.curated-by")}</p><h2>${t("addons.hero-title")}</h2><p>${t("addons.hero-body")}</p></div>
        </div>
        <div class="addons-hero-actions"><span class="addons-reviewed-pill"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>${t("addons.verified")}</span><button class="btn-secondary small" id="addonsRefreshBtn" type="button"><svg class="icon" aria-hidden="true"><use href="#i-refresh"/></svg>${t("addons.refresh")}</button></div>
      </section>
      <div id="addonsAdminRequests" class="addons-requests-host"></div>
      <div class="addons-toolbar">
        <label class="addons-search"><svg class="icon" aria-hidden="true"><use href="#i-search"/></svg><input id="addonsSearch" type="search" placeholder="${esc(t("addons.search-placeholder"))}" autocomplete="off" spellcheck="false" aria-label="${esc(t("addons.search-placeholder"))}"></label>
        <div class="addons-filters" role="group" aria-label="${esc(t("addons.filter-aria"))}">
          <button class="addons-filter is-active" data-addons-filter="all" type="button">${t("addons.filter.all")}</button>
          <button class="addons-filter" data-addons-filter="installed" type="button">${t("addons.filter.installed")}</button>
          <button class="addons-filter" data-addons-filter="available" type="button">${t("addons.filter.available")}</button>
          <button class="addons-filter" data-addons-filter="setup" type="button">${t("addons.filter.setup")}</button>
        </div>
      </div>
      <div class="addons-summary" id="addonsSummary"></div>
      <div class="addons-grid" id="addonsGrid"></div>
      <div class="addons-empty" id="addonsEmpty" hidden></div>
      <p class="addons-footnote"><svg class="icon" aria-hidden="true"><use href="#i-lock"/></svg>${t("addons.footnote")}</p>
    </div>`;
  panelHost.appendChild(panel);
  const search = panel.querySelector("#addonsSearch");
  search?.addEventListener("input", () => {
    state.addonsQuery = search.value || "";
    renderAddonsGrid();
  });
  panel.querySelector("#addonsRefreshBtn")?.addEventListener("click", () => loadAddons({ silent: false }));
  panel.addEventListener("click", (event) => {
    const filter = event.target.closest("[data-addons-filter]");
    if (filter) {
      state.addonsFilter = filter.getAttribute("data-addons-filter") || "all";
      renderAddonsGrid();
      return;
    }
    const primary = event.target.closest("[data-addon-primary]");
    if (primary) { handleAddonPrimary(primary.getAttribute("data-addon-primary")); return; }
    const details = event.target.closest("[data-addon-details]");
    if (details) { openAddonDetails(details.getAttribute("data-addon-details")); return; }
    const setup = event.target.closest("[data-addon-setup]");
    if (setup) { openAddonSetup(setup.getAttribute("data-addon-setup")); return; }
    const remove = event.target.closest("[data-addon-remove]");
    if (remove) { removeAddon(remove.getAttribute("data-addon-remove")); return; }
    const request = event.target.closest("[data-addon-request]");
    if (request) { requestAddonSetup(request.getAttribute("data-addon-request")); return; }
    const resolve = event.target.closest("[data-addon-request-resolve]");
    if (resolve) { resolveAddonRequest(resolve.getAttribute("data-addon-request-resolve"), resolve.getAttribute("data-addon-request-status")); return; }
  });
  panel.addEventListener("change", (event) => {
    const toggle = event.target.closest("[data-addon-tenant-toggle]");
    if (toggle) {
      setAddonWorkspaceEnabled(toggle.getAttribute("data-addon-tenant-toggle"), toggle.checked, toggle);
      return;
    }
    const policy = event.target.closest("[data-addon-install-policy]");
    if (policy) setAddonInstallPolicy(policy.getAttribute("data-addon-install-policy"), policy.value, policy);
  });
  ensureAddonOverlays();
}

export function ensureAddonOverlays() {
  if (document.getElementById("addonSetupOverlay")) return;
  const host = document.createElement("div");
  host.innerHTML = `
    <div class="overlay addon-overlay" id="addonSetupOverlay" hidden>
      <section class="addon-setup-dialog" role="dialog" aria-modal="true" aria-labelledby="addonSetupTitle">
        <header class="addon-setup-head">
          <div class="addon-setup-brand"><span class="addon-setup-icon" id="addonSetupIcon"><svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg></span><div><p class="eyebrow" id="addonSetupEyebrow">${t("addons.setup.eyebrow")}</p><h2 id="addonSetupTitle">${t("addons.setup.connect-title")}</h2><p id="addonSetupSubtitle"></p></div></div>
          <button class="icon-btn" type="button" data-addon-setup-close aria-label="${esc(t("addons.setup.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
        </header>
        <div class="addon-setup-body">
          <div class="addon-setup-fields" id="addonSetupFields"></div>
          <div class="addon-permissions">
            <div class="addon-permissions-head"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg><strong>${t("addons.setup.can-do-title")}</strong></div>
            <ul id="addonSetupPermissions"></ul>
            <p>${t("addons.setup.data-note")}</p>
          </div>
          <div id="addonSetupLinks"></div>
          <p class="addon-setup-error" id="addonSetupError" hidden></p>
        </div>
        <footer class="addon-setup-foot">
          <button class="link-btn addon-disable-btn" id="addonSetupDisable" type="button" hidden>${t("addons.setup.disable-workspace-short")}</button>
          <span class="tool-spacer"></span>
          <button class="btn-secondary" type="button" data-addon-setup-close>${t("common.cancel")}</button>
          <button class="btn-primary" id="addonSetupSave" type="button">${t("common.save")}</button>
        </footer>
      </section>
    </div>
    <div class="overlay addon-overlay" id="addonResultOverlay" hidden>
      <section class="addon-result-dialog" role="dialog" aria-modal="true" aria-labelledby="addonResultTitle">
        <header class="addon-setup-head">
          <div class="addon-setup-brand"><span class="addon-setup-icon"><svg class="icon" aria-hidden="true"><use href="#i-translate"/></svg></span><div><p class="eyebrow" id="addonResultEyebrow">${t("addons.result.eyebrow")}</p><h2 id="addonResultTitle">${t("addons.result.title")}</h2><p id="addonResultMeta"></p></div></div>
          <button class="icon-btn" type="button" data-addon-result-close aria-label="${esc(t("addons.result.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
        </header>
        <div class="addon-result-body"><pre id="addonResultText"></pre></div>
        <footer class="addon-setup-foot"><span class="tool-spacer"></span><button class="btn-secondary" id="addonResultCopy" type="button"><svg class="icon" aria-hidden="true"><use href="#i-copy"/></svg>${t("addons.result.copy")}</button><button class="btn-primary" type="button" data-addon-result-close>${t("common.done")}</button></footer>
      </section>
    </div>
    <div class="overlay addon-overlay" id="addonDetailsOverlay" hidden>
      <section class="addon-details-dialog" role="dialog" aria-modal="true" aria-labelledby="addonDetailsTitle">
        <header class="addon-setup-head">
          <div class="addon-setup-brand"><span class="addon-setup-icon" id="addonDetailsIcon"><svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg></span><div><p class="eyebrow" id="addonDetailsEyebrow">${t("addons.details.eyebrow")}</p><h2 id="addonDetailsTitle">${t("addons.addon")}</h2><p id="addonDetailsSubtitle"></p></div></div>
          <button class="icon-btn" type="button" data-addon-details-close aria-label="${esc(t("addons.details.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
        </header>
        <div class="addon-details-body" id="addonDetailsBody"></div>
        <footer class="addon-setup-foot"><span class="tool-spacer"></span><button class="btn-primary" type="button" data-addon-details-close>${t("common.done")}</button></footer>
      </section>
    </div>`;
  document.body.appendChild(host);
  const setup = document.getElementById("addonSetupOverlay");
  const result = document.getElementById("addonResultOverlay");
  setup.querySelectorAll("[data-addon-setup-close]").forEach((button) => button.addEventListener("click", closeAddonSetup));
  setup.addEventListener("click", (event) => { if (event.target === setup) closeAddonSetup(); });
  setup.addEventListener("click", (event) => {
    const oauth = event.target.closest("[data-addon-oauth-start]");
    if (oauth) startAddonOAuth(oauth.getAttribute("data-addon-oauth-start") || "", oauth);
  });
  document.getElementById("addonSetupSave")?.addEventListener("click", submitAddonSetup);
  document.getElementById("addonSetupDisable")?.addEventListener("click", disableAddonFromSetup);
  result.querySelectorAll("[data-addon-result-close]").forEach((button) => button.addEventListener("click", closeAddonResult));
  result.addEventListener("click", (event) => { if (event.target === result) closeAddonResult(); });
  const details = document.getElementById("addonDetailsOverlay");
  details?.querySelectorAll("[data-addon-details-close]").forEach((button) => button.addEventListener("click", closeAddonDetails));
  details?.addEventListener("click", (event) => { if (event.target === details) closeAddonDetails(); });
  document.getElementById("addonResultCopy")?.addEventListener("click", async () => {
    const text = document.getElementById("addonResultText")?.textContent || "";
    try { await navigator.clipboard.writeText(text); showToast(t("addons.copied")); }
    catch { showToast(t("addons.copy-failed")); }
  });
}

export async function loadAddons({ silent = false } = {}) {
  if (state.delegatedMode || state.addonsLoading) return;
  state.addonsLoading = true;
  if (!silent && !state.addonsLoaded) renderAddonsSettings();
  try {
    const data = await api("/api/addons");
    state.addons = Array.isArray(data.addons) ? data.addons : [];
    state.addonsAdmin = Boolean(data.isAdmin);
    state.addonsRequests = Array.isArray(data.requests) ? data.requests : [];
    state.addonsLoaded = true;
  } catch (error) {
    if (!silent) showError(error);
  } finally {
    state.addonsLoading = false;
    renderAddonNavBadge();
    renderAddonsSettings();
    renderReaderAddons();
  }
}

export function renderAddonNavBadge() {
  const badge = document.getElementById("addonNavBadge");
  if (!badge) return;
  const count = (state.addonsRequests || []).length;
  badge.hidden = count === 0;
  badge.textContent = String(count);
}

export function renderAddonsSettings() {
  ensureAddonsSettingsUi();
  hooks.ensureAISettingsUi?.();
  const grid = document.getElementById("addonsGrid");
  if (!grid) return;
  const search = document.getElementById("addonsSearch");
  if (search && search.value !== state.addonsQuery) search.value = state.addonsQuery;
  ensureAddonOverlays();
  document.querySelectorAll("[data-addons-filter]").forEach((button) => {
    const active = button.getAttribute("data-addons-filter") === state.addonsFilter;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  renderAddonNavBadge();
  renderAddonRequests();
  renderAddonsGrid();
  const summary = document.getElementById("addonsSummary");
  if (summary) {
    const installed = (state.addons || []).filter((addon) => addon.installed).length;
    const available = (state.addons || []).filter((addon) => ["ready", "needs_connection"].includes(addon.status)).length;
    const total = (state.addons || []).length;
    summary.textContent = state.addonsLoading && !state.addonsLoaded
      ? t("addons.loading")
      : tn("addons.summary", total, { count: total, ready: available, installed });
  }
}

export function addonMatchesFilter(addon) {
  const query = state.addonsQuery.trim().toLowerCase();
  if (query) {
    const haystack = `${addon.name || ""} ${addon.vendor || ""} ${addon.category || ""} ${addon.description || ""}`.toLowerCase();
    if (!haystack.includes(query)) return false;
  }
  switch (state.addonsFilter) {
    case "installed": return addon.installed;
    case "available": return ["ready", "needs_connection"].includes(addon.status);
    case "setup": return ["needs_setup", "requested", "blocked"].includes(addon.status);
    default: return true;
  }
}

export function addonStatusMeta(addon) {
  switch (addon.status) {
    case "installed": return { label: t("addons.status.installed"), tone: "installed" };
    case "ready": return { label: t("addons.status.ready"), tone: "ready" };
    case "needs_connection": return { label: t("addons.status.needs-connection"), tone: "attention" };
    case "needs_setup": return { label: addon.canManage ? t("addons.status.workspace-setup-needed") : t("addons.status.awaiting-workspace-setup"), tone: "attention" };
    case "approval_required": return { label: t("addons.status.admin-approval"), tone: "attention" };
    case "requested": return { label: addon.requestType === "install" ? t("addons.status.install-approval-pending") : t("addons.status.request-sent"), tone: "neutral" };
    default: return { label: addon.tenantPolicySet ? t("addons.status.disabled-workspace") : t("addons.status.unavailable"), tone: "neutral" };
  }
}

export function addonLogoMarkup(addon, className = "addon-logo") {
  if (addon?.logo) {
    return `<img class="${esc(className)}" src="${esc(addon.logo)}" alt="${esc(t("addons.logo-alt", { name: addon.name || t("addons.addon") }))}" decoding="async">`;
  }
  return `<svg class="icon" aria-hidden="true"><use href="#${esc(addon?.icon || "i-grid")}"/></svg>`;
}

export function addonAuthLabel(addon) {
  if (addon?.id === "slack") return t("addons.auth.slack");
  if (addon?.id === "salesforce" || addon?.id === "zoho") return t("addons.auth.refresh-token");
  if (addon?.id === "ticktick") return t("addons.auth.access-token");
  if (addon?.id === "zoom") return t("addons.auth.server-to-server");
  if (addon?.authType === "email") return t("addons.auth.email");
  if (addon?.authType === "webhook") return t("addons.auth.webhook");
  if (addon?.authType === "oauth2") return t("addons.auth.oauth2");
  return t("addons.auth.api-key");
}

export function addonProviderLinksMarkup(addon) {
  const links = [
    [t("addons.links.website"), addon.websiteUrl],
    [t("addons.links.support"), addon.supportUrl],
    [t("addons.links.docs"), addon.docsUrl],
    [t("addons.links.privacy"), addon.privacyUrl]
  ].filter(([, url]) => url);
  if (!links.length) return "";
  return `<div class="addon-provider-links">${links.map(([label, url]) =>
    `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(label)}<svg class="icon" aria-hidden="true"><use href="#i-external"/></svg></a>`
  ).join("")}</div>`;
}

export function addonPrimaryMarkup(addon) {
  if (addon.installed) return `<button class="addon-btn is-installed" type="button" disabled>${t("addons.action.installed")}</button>`;
  if (addon.status === "ready") return `<button class="addon-btn" type="button" data-addon-primary="${esc(addon.id)}">${t("addons.action.install")}</button>`;
  if (addon.status === "needs_connection") return `<button class="addon-btn" type="button" data-addon-primary="${esc(addon.id)}">${t("addons.action.connect")}</button>`;
  if (addon.status === "approval_required") return `<button class="addon-btn" type="button" data-addon-request="${esc(addon.id)}">${t("addons.action.request-install")}</button>`;
  if (addon.status === "requested") return `<button class="addon-btn is-installed" type="button" disabled>${addon.requestType === "install" ? t("addons.status.approval-pending") : t("addons.status.request-sent")}</button>`;
  if (addon.status === "needs_setup") {
    return addon.canManage
      ? `<button class="addon-btn" type="button" data-addon-primary="${esc(addon.id)}">${t("addons.action.set-up")}</button>`
      : `<button class="addon-btn" type="button" data-addon-request="${esc(addon.id)}">${t("addons.action.request-setup")}</button>`;
  }
  if (addon.canRequest) return `<button class="addon-btn" type="button" data-addon-request="${esc(addon.id)}">${t("addons.action.request-access")}</button>`;
  return `<button class="addon-btn is-installed" type="button" disabled>${t("addons.status.unavailable")}</button>`;
}

function addonCategoryLabel(category) {
  const value = String(category || "").trim();
  if (!value) return t("addons.addon");
  const key = `addons.category.${value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")}`;
  const translated = t(key);
  return translated === key ? tp(value) : translated;
}

export function addonCardMarkup(addon) {
  const status = addonStatusMeta(addon);
  const scopeLabel = addon.connectionScope === "tenant" ? t("addons.scope.workspace") : t("addons.scope.personal");
  const permissions = Array.isArray(addon.permissions) ? addon.permissions.length : 0;
  const secondary = `<span class="addon-card-links">${addon.installed ? `<button class="link-btn" type="button" data-addon-remove="${esc(addon.id)}">${t("addons.action.remove")}</button>` : ""}${addon.canManage && addon.configured && addon.status !== "blocked" ? `<button class="link-btn" type="button" data-addon-setup="${esc(addon.id)}">${addon.installed ? t("addons.action.reconnect") : t("addons.action.update-setup")}</button>` : ""}</span>`;
  const toggle = addon.canManage && (addon.tenantPolicySet || addon.connectionScope === "user")
    ? `<label class="addon-workspace-toggle" title="${esc(t("addons.workspace-toggle-title"))}"><input type="checkbox" data-addon-tenant-toggle="${esc(addon.id)}" ${addon.tenantEnabled ? "checked" : ""}><span>${t("addons.workspace")}</span></label>`
    : "";
  const policy = addon.canManage
    ? `<select class="addon-policy-select" data-addon-install-policy="${esc(addon.id)}" title="${esc(t("addons.install-policy-title"))}" aria-label="${esc(t("addons.install-policy-aria", { name: addon.name }))}"><option value="self_service"${addon.installPolicy !== "approval_required" ? " selected" : ""}>${t("addons.policy.self-service")}</option><option value="approval_required"${addon.installPolicy === "approval_required" ? " selected" : ""}>${t("addons.policy.approval-required")}</option></select>`
    : "";
  return `<article class="addon-card" data-addon-card="${esc(addon.id)}">
    <div class="addon-card-art" style="--addon-color:${esc(addon.color || "#34557a")}">
      <span class="addon-logo-tile">${addonLogoMarkup(addon)}</span>
      <span class="addon-verified" title="${esc(t("addons.verified"))}"><svg class="icon" aria-hidden="true"><use href="#i-check-circle"/></svg></span>
    </div>
    <div class="addon-card-copy">
      <div class="addon-card-heading"><div><h3>${esc(addon.name)}</h3><span class="addon-vendor">${esc(addon.vendor || "Alt8Mail")}</span></div><span class="addon-category">${esc(addonCategoryLabel(addon.category))}</span></div>
      <p>${esc(tp(addon.description || ""))}</p>
      <div class="addon-card-meta"><span>${esc(scopeLabel)}</span>${addon.configured && addon.connectionLabel ? `<span>${esc(tp(addon.connectionLabel))}</span>` : ""}<span>${esc(tn("addons.permission-count", permissions, { count: permissions }))}</span>${addon.supportUrl ? `<a class="addon-support-link" href="${esc(addon.supportUrl)}" target="_blank" rel="noopener noreferrer" title="${esc(t("addons.open-support", { name: addon.name }))}" aria-label="${esc(t("addons.open-support", { name: addon.name }))}"><svg class="icon" aria-hidden="true"><use href="#i-external"/></svg></a>` : ""}<button class="link-btn addon-details-link" type="button" data-addon-details="${esc(addon.id)}">${t("addons.details-button")}</button></div>
    </div>
    <div class="addon-card-foot">
      <span class="addon-status is-${esc(status.tone)}"><span class="addon-status-dot"></span>${esc(status.label)}</span>
      <div class="addon-card-actions">${policy}${toggle}${secondary}${addonPrimaryMarkup(addon)}</div>
    </div>
  </article>`;
}

export function renderAddonsGrid() {
  const grid = document.getElementById("addonsGrid");
  const empty = document.getElementById("addonsEmpty");
  if (!grid || !empty) return;
  const addons = (state.addons || []).filter(addonMatchesFilter);
  grid.innerHTML = addons.map(addonCardMarkup).join("");
  const loading = state.addonsLoading && !state.addonsLoaded;
  empty.hidden = Boolean(addons.length) || loading;
  if (!addons.length && !loading) {
    empty.innerHTML = `<svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg><strong>${t("addons.empty-title")}</strong><span>${t("addons.empty-hint")}</span>`;
  }
}

export function renderAddonRequests() {
  const host = document.getElementById("addonsAdminRequests");
  if (!host) return;
  const requests = state.addonsRequests || [];
  if (!state.addonsAdmin || !requests.length) {
    host.innerHTML = "";
    return;
  }
  host.innerHTML = `<section class="addon-request-panel">
    <div class="addon-request-head"><div><p class="eyebrow">${t("addons.admin-queue")}</p><h3>${t("addons.requests-title")}</h3><p>${esc(tn("addons.requests-waiting", requests.length, { count: requests.length }))}</p></div><span class="addon-request-count">${requests.length}</span></div>
    <div class="addon-request-list">${requests.map((request) => `<div class="addon-request-row"><span class="avatar avatar-sm" style="--av:#34557a"><span class="avatar-initials">${esc(initialsFrom(request.userName || request.userEmail || "?"))}</span></span><div><strong>${request.addonLogo ? `<img class="addon-request-logo" src="${esc(request.addonLogo)}" alt="" loading="lazy" decoding="async">` : ""}${esc(request.addonName)} <span class="addon-request-type">${request.requestType === "install" ? t("addons.request.install-approval") : t("addons.request.setup-request")}</span></strong><span>${esc(request.userName || request.userEmail)}${request.note ? ` · ${esc(request.note)}` : ""}</span></div><div class="addon-request-actions"><button class="btn-secondary small" type="button" data-addon-request-resolve="${esc(request.id)}" data-addon-request-status="approved">${t("addons.action.approve")}</button><button class="link-btn" type="button" data-addon-request-resolve="${esc(request.id)}" data-addon-request-status="denied">${t("addons.action.deny")}</button></div></div>`).join("")}</div>
  </section>`;
}

export function addonByID(id) {
  return (state.addons || []).find((addon) => addon.id === id) || null;
}

export function handleAddonPrimary(id) {
  const addon = addonByID(id);
  if (!addon) return;
  if (addon.installed) return;
  if (addon.status === "ready") {
    installAddon(id);
    return;
  }
  if (addon.status === "needs_connection" || addon.status === "needs_setup") {
    if (addon.status === "needs_connection" || addon.canManage) openAddonSetup(id);
    else requestAddonSetup(id);
    return;
  }
  if (addon.status === "approval_required" || (addon.status === "blocked" && addon.canRequest)) requestAddonSetup(id);
}

export async function installAddon(id) {
  try {
    await api(`/api/addons/${encodeURIComponent(id)}/install`, { method: "POST", body: {} });
    showToast(t("addons.toast.installed"));
    await loadAddons({ silent: true });
  } catch (error) { showError(error); }
}

export async function removeAddon(id) {
  const addon = addonByID(id);
  if (!addon) return;
  const confirmed = await appConfirm({
    title: t("addons.remove-title", { name: addon.name }),
    message: addon.connectionScope === "user"
      ? t("addons.remove-personal-message")
      : t("addons.remove-workspace-message"),
    confirmLabel: t("addons.action.remove-addon"),
    tone: "danger",
    icon: "i-grid"
  });
  if (!confirmed) return;
  try {
    await api(`/api/addons/${encodeURIComponent(id)}/install`, { method: "DELETE" });
    showToast(t("addons.removed", { name: addon.name }));
    await loadAddons({ silent: true });
  } catch (error) { showError(error); }
}

export async function requestAddonSetup(id) {
  const addon = addonByID(id);
  if (!addon) return;
  try {
    await api(`/api/addons/${encodeURIComponent(id)}/request`, { method: "POST", body: { note: "" } });
    showToast(addon.status === "approval_required" ? t("addons.toast.install-request-sent", { name: addon.name }) : t("addons.toast.request-sent", { name: addon.name }));
    await loadAddons({ silent: true });
  } catch (error) { showError(error); }
}

export async function resolveAddonRequest(id, status) {
  try {
    await api(`/api/addon-requests/${encodeURIComponent(id)}/resolve`, { method: "POST", body: { status } });
    showToast(status === "approved" ? t("addons.toast.request-approved") : t("addons.toast.request-denied"));
    await loadAddons({ silent: true });
  } catch (error) { showError(error); }
}

export async function setAddonWorkspaceEnabled(id, enabled, toggle) {
  const addon = addonByID(id);
  if (!addon) return false;
  if (!enabled) {
    const confirmed = await appConfirm({
      title: t("addons.disable-title", { name: addon.name }),
      message: t("addons.disable-message"),
      confirmLabel: t("addons.action.disable-addon"),
      tone: "danger",
      icon: "i-grid"
    });
    if (!confirmed) {
      if (toggle) toggle.checked = true;
      return false;
    }
  }
  try {
    await api(`/api/addons/${encodeURIComponent(id)}/tenant`, { method: "PUT", body: { enabled } });
    showToast(enabled ? t("addons.toast.enabled-workspace", { name: addon.name }) : t("addons.toast.disabled-workspace", { name: addon.name }));
    await loadAddons({ silent: true });
    return true;
  } catch (error) {
    if (toggle) toggle.checked = !enabled;
    showError(error);
    return false;
  }
}

export async function setAddonInstallPolicy(id, policy, select) {
  const addon = addonByID(id);
  if (!addon) return false;
  const previous = addon.installPolicy || "self_service";
  if (policy === previous) return true;
  try {
    await api(`/api/addons/${encodeURIComponent(id)}/tenant`, { method: "PUT", body: { installPolicy: policy } });
    showToast(t("addons.toast.policy-updated", { name: addon.name }));
    await loadAddons({ silent: true });
    return true;
  } catch (error) {
    if (select) select.value = previous;
    showError(error);
    return false;
  }
}

export function addonOAuthMarkup(addon) {
  if (!addon.oauthAvailable || !addon.oauthMode || addon.oauthMode === "disabled") return "";
  const managed = addon.oauthMode === "managed";
  const appFields = `
    <div class="addon-oauth-app-fields">
      <label class="form-field addon-field"><span>${t("addons.oauth.client-id")}</span><input data-addon-oauth-field="clientId" type="text" autocomplete="off" spellcheck="false" placeholder="1234567890.1234567890"></label>
      <label class="form-field addon-field"><span>${t("addons.oauth.client-secret")}</span><input data-addon-oauth-field="clientSecret" type="password" autocomplete="off" placeholder="${esc(t("addons.oauth.client-secret-placeholder"))}"></label>
      <label class="form-field addon-field"><span>${t("addons.oauth.signing-secret")}</span><input data-addon-oauth-field="signingSecret" type="password" autocomplete="off" placeholder="${esc(t("addons.oauth.signing-secret-placeholder"))}"><small>${t("addons.oauth.signing-secret-help")}</small></label>
    </div>`;
  const byoOption = managed
    ? `<details class="addon-oauth-byo"><summary>${t("addons.oauth.use-own-app")}</summary>${appFields}<button class="btn-secondary small addon-oauth-btn" type="button" data-addon-oauth-start="byo"><svg class="icon" aria-hidden="true"><use href="#i-link"/></svg>${t("addons.oauth.connect-own-app")}</button></details>`
    : appFields;
  return `<div class="addon-oauth-panel">
    <div class="addon-oauth-head">
      <span class="addon-oauth-mark"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg></span>
      <div><strong>${t("addons.oauth.connect-title")}</strong><small>${managed ? t("addons.oauth.managed-description") : t("addons.oauth.byo-description")}</small></div>
    </div>
    ${byoOption}
    <button class="btn-primary addon-oauth-btn" type="button" data-addon-oauth-start="${managed ? "managed" : "byo"}"><svg class="icon" aria-hidden="true"><use href="#i-link"/></svg>${t("addons.oauth.connect-button")}</button>
  </div>
  <div class="addon-field-divider"><span>${t("addons.oauth.webhook-divider")}</span></div>`;
}

export function openAddonSetup(id) {
  const addon = addonByID(id);
  if (!addon) return;
  ensureAddonOverlays();
  state.addonModalId = id;
  const overlay = document.getElementById("addonSetupOverlay");
  document.getElementById("addonSetupTitle").textContent = addon.installed ? t("addons.setup.reconnect-title", { name: addon.name }) : t("addons.setup.setup-title", { name: addon.name });
  document.getElementById("addonSetupEyebrow").textContent = addon.connectionScope === "tenant" ? t("addons.setup.workspace-addon") : t("addons.setup.personal-addon");
  document.getElementById("addonSetupSubtitle").textContent = tp(addon.description || "");
  const icon = document.getElementById("addonSetupIcon");
  if (icon) {
    icon.style.setProperty("--addon-color", addon.color || "#34557a");
    icon.innerHTML = addonLogoMarkup(addon, "addon-setup-logo");
  }
  const error = document.getElementById("addonSetupError");
  if (error) { error.hidden = true; error.textContent = ""; }
  const links = document.getElementById("addonSetupLinks");
  if (links) links.innerHTML = addonProviderLinksMarkup(addon);
  const fields = document.getElementById("addonSetupFields");
  fields.innerHTML = addonOAuthMarkup(addon) + (addon.fields || []).map((field) => {
    const value = field.default || addon.connectionConfig?.[field.key] || "";
    if (field.type === "select") {
      return `<label class="form-field addon-field"><span>${esc(tp(field.label))}${field.required ? " *" : ""}</span><select data-addon-field="${esc(field.key)}">${(field.options || []).map((option) => `<option value="${esc(option.value)}" ${String(value) === String(option.value) ? "selected" : ""}>${esc(tp(option.label))}</option>`).join("")}</select>${field.help ? `<small>${esc(tp(field.help))}</small>` : ""}</label>`;
    }
    return `<label class="form-field addon-field"><span>${esc(tp(field.label))}${field.required ? " *" : ""}</span><input data-addon-field="${esc(field.key)}" type="${esc(field.type === "password" ? "password" : field.type === "url" ? "url" : "text")}" value="${field.secret ? "" : esc(value)}" placeholder="${esc(tp(field.placeholder || ""))}" autocomplete="off" spellcheck="false">${field.help ? `<small>${esc(tp(field.help))}</small>` : ""}</label>`;
  }).join("");
  const permissions = document.getElementById("addonSetupPermissions");
  if (permissions) permissions.innerHTML = (addon.permissions || []).map((permission) => `<li>${esc(tp(permission))}</li>`).join("");
  const save = document.getElementById("addonSetupSave");
  if (save) {
    save.textContent = addon.connectionScope === "user" ? t("addons.setup.save-install") : t("addons.setup.save-workspace");
    save.disabled = false;
  }
  const disable = document.getElementById("addonSetupDisable");
  if (disable) {
    disable.hidden = !(addon.canManage && addon.tenantPolicySet && addon.tenantEnabled);
    disable.textContent = t("addons.setup.disable-workspace", { name: addon.name });
  }
  overlay.hidden = false;
  setTimeout(() => (fields.querySelector(".addon-oauth-btn") || fields.querySelector("input, select"))?.focus(), 40);
}

export async function startAddonOAuth(mode = "", trigger = null) {
  const addon = addonByID(state.addonModalId);
  if (!addon) return;
  const button = trigger || document.querySelector("#addonSetupFields [data-addon-oauth-start]");
  const errorBox = document.getElementById("addonSetupError");
  if (errorBox) { errorBox.hidden = true; errorBox.textContent = ""; }
  const payload = {};
  if (mode) payload.mode = mode;
  if (mode === "byo" || addon.oauthMode === "byo") {
    for (const key of ["clientId", "clientSecret", "signingSecret"]) {
      const input = document.querySelector(`#addonSetupFields [data-addon-oauth-field="${key}"]`);
      payload[key] = input ? String(input.value || "").trim() : "";
    }
  }
  const label = button?.innerHTML;
  if (button) { button.disabled = true; button.textContent = t("addons.oauth.opening-slack"); }
  try {
    const data = await api(`/api/addons/${encodeURIComponent(addon.id)}/oauth/start`, { method: "POST", body: payload });
    if (!data?.oauthUrl) throw new Error(t("addons.oauth.could-not-start"));
    window.location.href = data.oauthUrl;
  } catch (error) {
    if (errorBox) { errorBox.textContent = error.message || t("addons.oauth.start-failed"); errorBox.hidden = false; }
    if (button) { button.disabled = false; button.innerHTML = label || t("addons.oauth.connect-button"); }
  }
}

export function closeAddonSetup() {
  const overlay = document.getElementById("addonSetupOverlay");
  if (overlay) overlay.hidden = true;
  state.addonModalId = "";
  state.addonSaving = false;
}

export async function submitAddonSetup() {
  if (state.addonSaving) return;
  const addon = addonByID(state.addonModalId);
  if (!addon) return;
  const fields = {};
  const errorBox = document.getElementById("addonSetupError");
  for (const field of addon.fields || []) {
    const input = document.querySelector(`#addonSetupFields [data-addon-field="${CSS.escape(field.key)}"]`);
    const value = input ? String(input.value || "").trim() : "";
    if (field.required && !value) {
      if (errorBox) { errorBox.textContent = t("addons.setup.enter-field", { field: tp(field.label) }); errorBox.hidden = false; }
      input?.focus();
      return;
    }
    fields[field.key] = value;
  }
  const save = document.getElementById("addonSetupSave");
  state.addonSaving = true;
  if (save) { save.disabled = true; save.textContent = t("addons.setup.saving"); }
  try {
    await api(`/api/addons/${encodeURIComponent(addon.id)}/connect`, { method: "POST", body: fields });
    if (addon.connectionScope === "user" && !addon.installed) {
      await api(`/api/addons/${encodeURIComponent(addon.id)}/install`, { method: "POST", body: {} });
    }
    closeAddonSetup();
    showToast(addon.connectionScope === "tenant" ? t("addons.toast.workspace-ready", { name: addon.name }) : t("addons.toast.connected-installed", { name: addon.name }));
    await loadAddons({ silent: true });
  } catch (error) {
    if (errorBox) { errorBox.textContent = error.message || t("addons.setup.save-failed"); errorBox.hidden = false; }
    if (save) { save.disabled = false; save.textContent = addon.connectionScope === "user" ? t("addons.setup.save-install") : t("addons.setup.save-workspace"); }
  } finally {
    state.addonSaving = false;
  }
}

export async function disableAddonFromSetup() {
  const addon = addonByID(state.addonModalId);
  if (!addon) return;
  if (await setAddonWorkspaceEnabled(addon.id, false)) closeAddonSetup();
}

export function ensureReaderAddonsButton() {
  if (document.getElementById("readerAddonsWrap")) return;
  const toolbar = document.getElementById("readerToolbar");
  if (!toolbar) return;
  const wrap = document.createElement("div");
  wrap.className = "reader-addons-wrap";
  wrap.id = "readerAddonsWrap";
  wrap.hidden = true;
  wrap.innerHTML = `<button class="tool-btn reader-addons-btn" id="readerAddonsBtn" type="button" aria-label="${esc(t("addons.actions"))}" title="${esc(t("addons.actions"))}" aria-haspopup="menu" aria-expanded="false"><svg class="icon" aria-hidden="true"><use href="#i-grid"/></svg><span class="tool-label">${t("addons.tool-label")}</span><svg class="icon addon-menu-chevron" aria-hidden="true"><use href="#i-chevron-down"/></svg></button><div class="reader-addons-menu" id="readerAddonsMenu" role="menu" hidden></div>`;
  const spacer = toolbar.querySelector(".tool-spacer");
  toolbar.insertBefore(wrap, spacer || null);
  document.getElementById("readerAddonsBtn")?.addEventListener("click", (event) => {
    event.stopPropagation();
    toggleReaderAddonMenu();
  });
  document.getElementById("readerAddonsMenu")?.addEventListener("click", (event) => {
    const action = event.target.closest("[data-addon-reader-action]");
    if (!action) return;
    runReaderAddonAction(action.getAttribute("data-addon-id"), action.getAttribute("data-addon-reader-action"));
  });
  document.addEventListener("click", (event) => {
    if (!event.target.closest("#readerAddonsWrap")) closeReaderAddonMenu();
  });
}

export function readerAddonActions() {
  return (state.addons || []).flatMap((addon) =>
    addon.installed && Array.isArray(addon.availableActions)
      ? addon.availableActions.filter((action) => action.surface === "reader").map((action) => ({ addon, action }))
      : []
  );
}

export function renderReaderAddons() {
  ensureReaderAddonsButton();
  const wrap = document.getElementById("readerAddonsWrap");
  const menu = document.getElementById("readerAddonsMenu");
  if (!wrap || !menu) return;
  const thread = hooks.currentThread?.();
  const actions = readerAddonActions();
  const visible = Boolean(thread && actions.length && !state.delegatedMode);
  wrap.hidden = !visible;
  if (!visible) {
    closeReaderAddonMenu();
    return;
  }
  menu.innerHTML = `<div class="reader-addons-head"><span>${t("addons.reader-menu-title")}</span><span>${actions.length}</span></div>${actions.map(({ addon, action }) => `<button type="button" role="menuitem" class="reader-addon-item" data-addon-reader-action="${esc(action.id)}" data-addon-id="${esc(addon.id)}"><span class="reader-addon-icon" style="--addon-color:${esc(addon.color || "#34557a")}">${addonLogoMarkup(addon, "reader-addon-logo")}</span><span><strong>${esc(tp(action.label))}</strong><small>${esc(addon.name)} · ${esc(tp((addon.permissions || [])[0] || t("addons.first-party")))}</small></span><svg class="icon reader-addon-chevron" aria-hidden="true"><use href="#i-chevron-right"/></svg></button>`).join("")}`;
}

export function toggleReaderAddonMenu(force) {
  const menu = document.getElementById("readerAddonsMenu");
  const button = document.getElementById("readerAddonsBtn");
  if (!menu) return;
  const open = typeof force === "boolean" ? force : menu.hidden;
  menu.hidden = !open;
  state.addonReaderMenuOpen = open;
  button?.setAttribute("aria-expanded", open ? "true" : "false");
  document.getElementById("readerAddonsWrap")?.classList.toggle("is-open", open);
}

export function closeReaderAddonMenu() {
  toggleReaderAddonMenu(false);
}

export async function runReaderAddonAction(addonId, actionId) {
  const addon = addonByID(addonId);
  const thread = hooks.currentThread?.();
  const message = thread?.latest;
  if (!addon || !message?.id) return;
  closeReaderAddonMenu();
  try {
    const data = await api(`/api/addons/${encodeURIComponent(addonId)}/actions/${encodeURIComponent(actionId)}`, {
      method: "POST",
      body: { messageId: message.id }
    });
    if (data?.result?.translation?.text) {
      showAddonResult(t("addons.result.translation-title", { name: addon.name }), data.result.translation.text, `${data.result.translation.sourceLanguage || t("addons.result.source")} → ${data.result.translation.targetLanguage || t("addons.result.target")}`);
    } else if (data?.result?.meetingUrl) {
      showAddonResult(t("addons.result.meeting-created", { name: addon.name }), data.result.meetingUrl, `${data.result.startTime || t("addons.result.starts-5-min")} · ${data.result.duration || 30} ${t("addons.result.minutes")}`);
    } else if (data?.result?.taskUrl) {
      showAddonResult(t("addons.result.task-created", { name: addon.name }), data.result.taskUrl, t("addons.result.task-link-note"));
    } else if (data?.result?.contactName) {
      showToast(t("addons.toast.email-logged", { name: addon.name, contact: data.result.contactName }));
    } else if (data?.result?.dedupKey) {
      showToast(t("addons.toast.incident", { name: addon.name }));
    } else if (data?.result?.taskId) {
      showToast(t("addons.toast.task-created", { name: addon.name }));
    } else if (data?.result?.posted) {
      showToast(t("addons.toast.sent-to", { name: addon.name }));
    } else if (data?.result?.saved) {
      showToast(t("addons.toast.saved-to", { name: addon.name }));
    } else if (data?.result?.queued) {
      showToast(t("addons.toast.added-to", { name: addon.name }));
    } else {
      showToast(t("addons.toast.action-completed", { name: addon.name }));
    }
  } catch (error) { showError(error); }
}

export function showAddonResult(title, text, meta = "") {
  ensureAddonOverlays();
  document.getElementById("addonResultTitle").textContent = title;
  document.getElementById("addonResultMeta").textContent = meta;
  document.getElementById("addonResultText").textContent = String(text || "");
  document.getElementById("addonResultOverlay").hidden = false;
}

export function closeAddonResult() {
  const overlay = document.getElementById("addonResultOverlay");
  if (overlay) overlay.hidden = true;
}

export function openAddonDetails(id) {
  const addon = addonByID(id);
  if (!addon) return;
  ensureAddonOverlays();
  const status = addonStatusMeta(addon);
  const icon = document.getElementById("addonDetailsIcon");
  if (icon) {
    icon.style.setProperty("--addon-color", addon.color || "#34557a");
    icon.innerHTML = addonLogoMarkup(addon, "addon-setup-logo");
  }
  document.getElementById("addonDetailsTitle").textContent = addon.name;
  document.getElementById("addonDetailsSubtitle").textContent = `${addon.vendor || "Alt8Mail"} · ${addonCategoryLabel(addon.category)}`;
  const actions = Array.isArray(addon.actions) ? addon.actions : [];
  const permissions = Array.isArray(addon.permissions) ? addon.permissions : [];
  document.getElementById("addonDetailsBody").innerHTML = `
    <p class="addon-details-tagline">${esc(tp(addon.tagline || addon.description || ""))}</p>
    ${addonProviderLinksMarkup(addon)}
    <dl class="addon-details-grid">
      <div><dt>${t("addons.details.vendor")}</dt><dd>${esc(addon.vendor || "Alt8Mail")}</dd></div>
      <div><dt>${t("addons.details.category")}</dt><dd>${esc(addonCategoryLabel(addon.category))}</dd></div>
      <div><dt>${t("addons.details.connection")}</dt><dd>${esc(addon.connectionScope === "tenant" ? t("addons.scope.workspace") : t("addons.scope.personal"))}</dd></div>
      <div><dt>${t("addons.details.authentication")}</dt><dd>${esc(addonAuthLabel(addon))}</dd></div>
      <div><dt>${t("addons.details.connector-version")}</dt><dd>${esc(addon.connectorVersion || "1.0")}</dd></div>
      <div><dt>${t("addons.details.status")}</dt><dd>${esc(status.label)}</dd></div>
    </dl>
    ${actions.length ? `<section class="addon-details-section"><h3>${t("addons.details.reader-actions")}</h3><ul>${actions.map((action) => `<li><strong>${esc(tp(action.label))}</strong>${action.description ? `<span>${esc(tp(action.description))}</span>` : ""}</li>`).join("")}</ul></section>` : ""}
    ${permissions.length ? `<section class="addon-details-section"><h3>${t("addons.details.permissions-requested")}</h3><ul>${permissions.map((permission) => `<li>${esc(tp(permission))}</li>`).join("")}</ul></section>` : ""}
    <p class="addon-details-note">${esc(t("addons.details.trademark", { name: addon.name, vendor: addon.vendor || t("addons.details.the-provider") }))}</p>`;
  document.getElementById("addonDetailsOverlay").hidden = false;
}

export function closeAddonDetails() {
  const overlay = document.getElementById("addonDetailsOverlay");
  if (overlay) overlay.hidden = true;
}
