"use strict";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
const esc = (value) => String(value == null ? "" : value)
  .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
  .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
const icon = (name, cls = "") => `<svg class="icon ${cls}" aria-hidden="true"><use href="#i-${name}"/></svg>`;
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

class ApiError extends Error {
  constructor(message, status, code, details) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

const state = {
  admin: null,
  tenants: [],
  activeTenant: null,
  stats: null,
  policy: null,
  catalog: [],
  permissions: [],
  view: "overview",
  routeParam: "",
  csrf: "",
  selectedUsers: new Set(),
  userFilter: { q: "", status: "", role: "", page: 1, limit: 25, admin: false, deleted: false },
  users: { items: [], total: 0 },
  domains: [],
  drawerUser: null,
  auditSource: "admin",
  commandOpen: false,
  scimURL: "",
  theme: localStorage.getItem("a8m_admin_theme") || "light"
};

/* ------------------------------------------------------------
   HTTP
------------------------------------------------------------ */
function cookieValue(name) {
  const match = document.cookie.match(new RegExp("(?:^|; )" + name.replace(/[.$?*|{}()[\]\\/+^]/g, "\\$&") + "=([^;]*)"));
  return match ? decodeURIComponent(match[1]) : "";
}

async function api(path, { method = "GET", body } = {}) {
  const headers = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const csrf = state.csrf || cookieValue("a8m_admin_csrf");
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  const response = await fetch("./api/" + path, {
    method,
    credentials: "same-origin",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const text = await response.text();
  let payload = null;
  try { payload = text ? JSON.parse(text) : null; } catch { payload = { raw: text }; }
  if (!response.ok) {
    if (response.status === 401 && !path.startsWith("session")) showLogin();
    const error = payload && payload.error ? payload.error : {};
    throw new ApiError(error.message || "The request could not be completed", response.status, error.code || "", error.details);
  }
  return payload;
}

function toast(message, type = "success") {
  const el = document.createElement("div");
  el.className = "toast" + (type === "error" ? " error" : "");
  el.innerHTML = `${icon(type === "error" ? "alert" : "check")}<span>${esc(message)}</span>`;
  $("#toastRoot").appendChild(el);
  setTimeout(() => { el.style.opacity = "0"; el.style.transition = "opacity .2s"; setTimeout(() => el.remove(), 220); }, 3600);
}

function showError(error) {
  toast(error && error.message ? error.message : "Something went wrong", "error");
}

/* ------------------------------------------------------------
   Theme + chrome
------------------------------------------------------------ */
function applyTheme(theme) {
  state.theme = theme === "dark" ? "dark" : "light";
  document.documentElement.setAttribute("data-theme", state.theme);
  localStorage.setItem("a8m_admin_theme", state.theme);
}
applyTheme(state.theme);

function setBusy(button, busy, label = "Working…") {
  if (!button) return;
  if (busy) {
    button.dataset.original = button.innerHTML;
    button.disabled = true;
    button.innerHTML = `${icon("refresh")}<span>${esc(label)}</span>`;
  } else {
    button.disabled = false;
    if (button.dataset.original) button.innerHTML = button.dataset.original;
  }
}

function initials(name) {
  return String(name || "?").split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0].toUpperCase()).join("") || "?";
}

function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
  return `${(value / Math.pow(1024, index)).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatDate(value, { time = false } = {}) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const opts = { month: "short", day: "numeric", year: "numeric" };
  if (time) { opts.hour = "numeric"; opts.minute = "2-digit"; }
  return date.toLocaleString("en-US", opts);
}

function relativeTime(value) {
  if (!value) return "—";
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "—";
  const delta = Date.now() - then;
  if (delta < 60000) return "just now";
  if (delta < 3600000) return `${Math.floor(delta / 60000)}m ago`;
  if (delta < 86400000) return `${Math.floor(delta / 3600000)}h ago`;
  if (delta < 604800000) return `${Math.floor(delta / 86400000)}d ago`;
  return formatDate(value);
}

function badge(status) {
  const labels = { active: "Active", suspended: "Suspended", invited: "Invited", archived: "Archived", deleted: "Closed", verified: "Verified", pending: "Pending", failed: "Needs attention", manual: "Manual" };
  return `<span class="badge ${esc(status || "neutral")}">${esc(labels[status] || status || "—")}</span>`;
}

function roleLabel(role) {
  return { owner: "Owner", admin: "Admin", member: "Member", readonly: "Read-only" }[role] || role;
}

function can(permission) {
  return Array.isArray(state.permissions) && (state.permissions.includes(permission) || state.permissions.includes("*") || state.admin?.isSystemAdmin);
}

function updateChrome() {
  const admin = state.admin || {};
  const tenant = state.activeTenant || {};
  $("#adminName").textContent = admin.name || "Administrator";
  $("#adminAccountEmail").textContent = admin.email || "";
  $("#adminAvatar").textContent = admin.initials || initials(admin.name);
  $("#tenantNameMini").textContent = tenant.name || "Organization";
  $("#topTenantName").textContent = tenant.name || "Organization";
  $("#tenantAvatar").textContent = initials(tenant.name);
  $("#tenantStatusDot").classList.toggle("suspended", tenant.status === "suspended");
  $("#navUserCount").textContent = state.stats?.users?.active ?? "";
  const pending = state.stats?.domains?.pending ?? 0;
  $("#navDomainState").textContent = pending > 0 ? String(pending) : "";
  $$("[data-permission]").forEach((element) => { element.hidden = !can(element.dataset.permission); });
  $("#systemNav").hidden = !can("system.tenants.manage");
  const activeTenant = state.activeTenant?.status;
  $("#tenantPicker").title = activeTenant === "suspended" ? "This organization is suspended" : "Switch organization";
}

/* ------------------------------------------------------------
   Login / session
------------------------------------------------------------ */
function showLogin() {
  $("#adminApp").hidden = true;
  $("#loginView").hidden = false;
  $("#loginError").hidden = true;
  $("#mfaError").hidden = true;
  $("#loginStep").hidden = false;
  $("#mfaStep").hidden = true;
  setTimeout(() => $("#adminEmail")?.focus(), 80);
}

function showMfa(data) {
  state.challengeId = data.challengeId;
  $("#loginStep").hidden = true;
  $("#mfaStep").hidden = false;
  $("#mfaCopy").textContent = data.methods?.recovery
    ? "Enter the six-digit code from your authenticator app, or use a recovery code."
    : "Enter the six-digit code from your authenticator app.";
  $("#mfaError").hidden = true;
  setTimeout(() => $("#mfaCode")?.focus(), 80);
}

async function finishLogin() {
  const data = await api("session");
  state.csrf = data.csrf;
  await loadBootstrap();
  $("#loginView").hidden = true;
  $("#adminApp").hidden = false;
  route();
}

async function loadBootstrap() {
  const data = await api("bootstrap");
  state.admin = data.admin;
  state.tenants = data.tenants || [];
  state.activeTenant = data.activeTenant;
  state.stats = data.stats;
  state.policy = data.policy;
  state.catalog = data.catalog || [];
  state.permissions = data.permissions || [];
  state.scimURL = String(data.server?.scimURL || "");
  applyAdminBranding(data.policy && data.policy.branding);
  updateChrome();
}

function applyAdminBranding(branding) {
  if (!branding) return;
  const name = String(branding.productName || "").trim().slice(0, 60) || "Alt8Mail";
  $$(".brand-lockup > span:not(.brand-mark)").forEach((node) => {
    node.innerHTML = `${esc(name)} <b>Admin</b>`;
  });
  if (document.title.endsWith("Admin") || document.title.startsWith("Alt8Mail")) {
    document.title = `${name} Admin`;
  }
}

$("#adminLoginForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = $("#loginSubmit");
  $("#loginError").hidden = true;
  setBusy(button, true, "Signing in…");
  try {
    const data = await api("session", { method: "POST", body: { email: $("#adminEmail").value.trim(), password: $("#adminPassword").value } });
    if (data.twoFactorRequired) {
      showMfa(data);
      return;
    }
    state.csrf = data.csrf || "";
    await finishLogin();
  } catch (error) {
    $("#loginError").textContent = error.message;
    $("#loginError").hidden = false;
  } finally {
    setBusy(button, false);
  }
});

$("#mfaForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = $("#mfaSubmit");
  $("#mfaError").hidden = true;
  setBusy(button, true, "Verifying…");
  try {
    const method = $("#mfaCode").value.replace(/[^A-Za-z0-9-]/g, "").length > 8 ? "recovery" : "totp";
    await api("session/2fa", { method: "POST", body: { challengeId: state.challengeId, code: $("#mfaCode").value.trim(), method } });
    await finishLogin();
  } catch (error) {
    $("#mfaError").textContent = error.message;
    $("#mfaError").hidden = false;
  } finally {
    setBusy(button, false);
  }
});

$("#mfaBack").addEventListener("click", showLogin);
$("#useRecoveryBtn").addEventListener("click", () => {
  $("#mfaCode").value = "";
  $("#mfaCode").placeholder = "Recovery code";
  $("#mfaCode").removeAttribute("pattern");
  $("#mfaCode").setAttribute("maxlength", "20");
  $("#mfaCopy").textContent = "Enter one of the single-use recovery codes generated when you enabled your authenticator.";
  $("#mfaCode").focus();
});

async function logout() {
  try { await api("session", { method: "DELETE" }); } catch {}
  state.admin = null;
  state.csrf = "";
  try { sessionStorage.clear(); } catch {}
  closeModal();
  // A full reload guarantees no in-memory console state survives sign-out.
  const base = location.href.split("#")[0].split("?")[0];
  location.replace(base + "?signedout=1");
}

/* ------------------------------------------------------------
   Navigation / route
------------------------------------------------------------ */
const views = new Set(["overview", "users", "user-detail", "import-users", "import-mailboxes", "groups", "team-inboxes", "org-units", "domains", "roles", "provisioning", "policies", "addons", "storage", "ai", "quarantine", "audit", "compliance", "integrations", "tenants", "settings"]);

function route() {
  if (!state.admin) return;
  const raw = (location.hash || "#/overview").replace(/^#\//, "").split("?")[0];
  const parts = raw.split("/").filter(Boolean);
  let view = parts[0] || "overview";
  let routeParam = "";
  if (view === "users" && parts[1] === "import") view = "import-users";
  else if (view === "users" && parts[1]) { view = "user-detail"; routeParam = parts[1]; }
  else if (view === "mailboxes" && parts[1] === "import") view = "import-mailboxes";
  state.routeParam = routeParam;
  state.view = views.has(view) ? view : "overview";
  const required = { overview: "tenant.read", users: "users.read", "user-detail": "users.read", "import-users": "users.create", "import-mailboxes": "users.create", groups: "groups.read", "team-inboxes": "groups.read", "org-units": "orgunits.read", domains: "domains.read", roles: "roles.read", provisioning: "roles.manage", policies: "policies.read", addons: "policies.read", storage: "policies.read", ai: "policies.read", quarantine: "users.security", audit: "audit.read", compliance: "legal.read", integrations: "siem.read", tenants: "system.tenants.manage", settings: "tenant.read" }[state.view];
  if (required && !can(required)) { state.view = "overview"; location.hash = "#/overview"; return; }
  const navView = ["user-detail", "import-users", "import-mailboxes"].includes(state.view) ? "users" : state.view;
  $$(".nav-link[data-view]").forEach((link) => link.classList.toggle("active", link.dataset.view === navView));
  $("#sidebar").classList.remove("open");
  switch (state.view) {
    case "overview": renderOverview(); break;
    case "users": renderUsers(); break;
    case "user-detail": renderUserPage(state.routeParam); break;
    case "import-users": renderImportUsers(); break;
    case "import-mailboxes": renderImportMailboxes(); break;
    case "groups": renderGroups(); break;
    case "team-inboxes": renderTeamInboxes(); break;
    case "org-units": renderOrgUnits(); break;
    case "domains": renderDomains(); break;
    case "roles": renderRoles(); break;
    case "provisioning": renderProvisioning(); break;
    case "policies": renderPolicies(); break;
    case "addons": renderAddons(); break;
    case "storage": renderStoragePolicy(); break;
    case "ai": renderAI(); break;
    case "quarantine": renderQuarantine(); break;
    case "audit": renderAudit(); break;
    case "compliance": renderCompliance(); break;
    case "integrations": renderIntegrations(); break;
    case "tenants": renderTenants(); break;
    case "settings": renderSettings(); break;
  }
}

window.addEventListener("hashchange", route);

function loading(title) {
  $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>${esc(title)}</h1><p>Loading administration data…</p></div></div><div class="card card-pad"><div class="empty">${icon("refresh")}<h3>Loading</h3><p>Reading from the Alt8Mail control plane.</p></div></div>`;
}

function viewError(error) {
  $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Something went wrong</h1><p>${esc(error.message)}</p></div></div><div class="card card-pad"><button class="btn primary" data-action="retry-view">Try again</button></div>`;
}

/* ------------------------------------------------------------
   Overview
------------------------------------------------------------ */
async function renderOverview() {
  loading("Overview");
  try {
    await loadBootstrap();
    const stats = state.stats || {};
    const users = stats.users || {};
    const domains = stats.domains || {};
    const posture = stats.posture || {};
    const storage = stats.storage || {};
    const mailImports = stats.mailImports || {};
    const blocklist = stats.blocklist || {};
    const usedPercent = storage.quotaBytes > 0 ? Math.round((storage.usedBytes / storage.quotaBytes) * 100) : 0;
    $("#viewRoot").innerHTML = `
      <div class="view-head">
        <div><h1>Good ${greeting()}, ${esc((state.admin.name || "admin").split(" ")[0])}</h1><p>Here is the security and administration posture for ${esc(state.activeTenant.name)}.</p></div>
        <div class="view-actions">${can("policies.manage") ? `<button class="btn secondary" data-action="go-policies">${icon("sliders")}Review policies</button>` : ""}${can("users.create") ? `<button class="btn primary" data-action="new-user">${icon("plus")}Add user</button>` : ""}</div>
      </div>
      <div class="stat-grid">
        <div class="stat-card"><span class="stat-label">${icon("users")}Active users</span><span class="stat-value">${users.active ?? 0}</span><span class="stat-meta">${users.total ?? 0} total · ${users.suspended ?? 0} suspended</span></div>
        <div class="stat-card"><span class="stat-label">${icon("group")}Groups</span><span class="stat-value">${stats.groups ?? 0}</span><span class="stat-meta">${stats.orgUnits ?? 0} org units configured</span></div>
        <div class="stat-card"><span class="stat-label">${icon("globe")}Domains</span><span class="stat-value">${domains.total ?? 0}</span><span class="stat-meta">${domains.pending ? `${domains.pending} awaiting DNS` : "All domains verified"}</span></div>
        <div class="stat-card"><span class="stat-label">${icon("grid")}Storage used</span><span class="stat-value">${esc(formatBytes(storage.usedBytes))}</span><span class="stat-meta">of ${esc(formatBytes(storage.quotaBytes))} · ${usedPercent}%</span></div>
      </div>
      ${(mailImports.active || mailImports.awaitingMapping) ? `<a class="import-alert" href="#/mailboxes/import"><span class="import-alert-icon">${icon("refresh")}</span><div><b>Mailbox migration in progress</b><small>${mailImports.active || 0} running · ${mailImports.awaitingMapping || 0} awaiting mapping · ${mailImports.importedMessages || 0} messages copied so far</small></div><span class="import-alert-link">Open migration ${icon("chevron-right")}</span></a>` : ""}
      <div class="dashboard-grid">
        <div class="card">
          <div class="card-head"><div><h2>Security posture</h2><p>Controls that protect this organization.</p></div><span class="badge ${mfaScore(posture)}">${(posture.mfaCoverage || 0) >= 90 ? "Strong" : (posture.mfaCoverage || 0) >= 50 ? "Improving" : "Needs attention"}</span></div>
          <div class="policy-ring">
            <div class="ring" style="--value:${posture.mfaCoverage || 0}"><strong>${posture.mfaCoverage || 0}%</strong><small>MFA</small></div>
            <div class="ring-copy"><h3>Multi-factor coverage</h3><p>${posture.mfaUsers || 0} of ${posture.mfaTarget || 0} active users have an authenticator. Enforcement is controlled by policy.</p>
              <div class="check-list">
                <div class="check-item"><span class="check-dot ${(posture.mfaCoverage || 0) >= 90 ? "good" : "warn"}">${icon((posture.mfaCoverage || 0) >= 90 ? "check" : "alert")}</span>Authenticator coverage ${posture.mfaCoverage || 0}%</div>
                <div class="check-item"><span class="check-dot ${(posture.domainHealth || 0) >= 100 ? "good" : "warn"}">${icon((posture.domainHealth || 0) >= 100 ? "check" : "alert")}</span>Domain DNS health ${posture.domainHealth || 0}%</div>
                <div class="check-item"><span class="check-dot good">${icon("check")}</span>${posture.adminAccounts || 0} delegated admin account${posture.adminAccounts === 1 ? "" : "s"}</div>
              </div>
            </div>
          </div>
          <div class="audit-mini" style="border-top:1px solid var(--line);margin-top:4px">
            ${(stats.recentAudit || []).slice(0, 4).map((entry) => auditMini(entry)).join("") || `<div class="empty-mini" style="padding:18px 19px">No administrative activity yet.</div>`}
          </div>
        </div>
        <div class="card">
          <div class="card-head"><div><h2>Quick actions</h2><p>Common administration tasks.</p></div></div>
          <div class="quick-grid">
            ${can("users.create") ? quick("users", "plus", "Add a user", "Create a mailbox and identity", "new-user") : ""}
            ${can("users.create") ? quick("list", "arrow-up", "Import users", "CSV or Excel account creation", "import-users") : ""}
            ${can("users.create") ? quick("mail", "arrow-up", "Import mailboxes", "Bring mail from another provider", "import-mailboxes") : ""}
            ${can("groups.manage") ? quick("group", "plus", "Create a group", "Distribution and security groups", "new-group") : ""}
            ${can("domains.manage") ? quick("globe", "plus", "Add a domain", "Start DNS verification", "new-domain") : ""}
            ${can("policies.manage") ? quick("shield", "sliders", "Review policies", "MFA, mail and sharing controls", "go-policies") : ""}
            ${can("audit.read") ? quick("list", "list", "Open audit log", "Inspect administrative changes", "go-audit") : ""}
          </div>
          <div class="card-pad" style="border-top:1px solid var(--line);padding-top:15px">
            <div class="stat-label" style="margin-bottom:8px">${icon("clock")}Last 24 hours</div>
            <div style="display:flex;justify-content:space-between;align-items:baseline"><span class="muted" style="font-size:11px">Sign-ins</span><b style="font-size:18px">${stats.logins24h || 0}</b></div>
          </div>
        </div>
      </div>
      <div class="card" style="margin-top:14px">
        <div class="card-head"><div><h2>Blocklist health</h2><p>Confirmed threat intelligence and the blocks it produced for ${esc(state.activeTenant.name)}.</p></div>${Number(blocklist.confirmed || 0) ? '<span class="badge pending">Protection active</span>' : '<span class="badge active">Clear</span>'}</div>
        <div class="card-pad">
          <div class="stat-grid" style="margin:0">
            <div class="stat-card"><span class="stat-label">${icon("shield")}Confirmed threats</span><span class="stat-value">${blocklist.confirmed || 0}</span><span class="stat-meta">${blocklist.pending || 0} awaiting review</span></div>
            <div class="stat-card"><span class="stat-label">${icon("alert")}Blocks fired · 7 days</span><span class="stat-value">${blocklist.blocksWeek || 0}</span><span class="stat-meta">Rejected at SMTP policy</span></div>
            <div class="stat-card"><span class="stat-label">${icon("check")}Dismissed reports</span><span class="stat-value">${blocklist.dismissed || 0}</span><span class="stat-meta">Reviewed and unblocked</span></div>
          </div>
        </div>
      </div>`;
  } catch (error) { viewError(error); }
}

function greeting() {
  const hour = new Date().getHours();
  return hour < 12 ? "morning" : hour < 18 ? "afternoon" : "evening";
}
function mfaScore(posture) {
  const value = posture.mfaCoverage || 0;
  return value >= 90 ? "active" : value >= 50 ? "pending" : "suspended";
}
function quick(iconName, actionIcon, title, description, action) {
  return `<button class="quick-action" data-action="${esc(action)}">${icon(iconName)}<b>${esc(title)}</b><small>${esc(description)}</small></button>`;
}
function auditMini(entry) {
  return `<div class="audit-mini-item"><span class="audit-mini-icon">${icon("list")}</span><div class="audit-mini-copy"><b>${esc(entry.action)}</b><small>${esc(entry.actor || "System")} · ${esc(entry.target || "—")}</small></div><span class="audit-mini-time">${relativeTime(entry.createdAt)}</span></div>`;
}

/* ------------------------------------------------------------
   Users
------------------------------------------------------------ */
async function renderUsers() {
  loading("Users");
  try {
    await loadUsers();
  } catch (error) { viewError(error); }
}

async function loadUsers() {
  const filter = state.userFilter;
  const params = new URLSearchParams({ page: String(filter.page), limit: String(filter.limit) });
  if (filter.q) params.set("q", filter.q);
  if (filter.status) params.set("status", filter.status);
  if (filter.role) params.set("role", filter.role);
  if (filter.admin) params.set("admin", "true");
  if (filter.deleted) params.set("deleted", "true");
  const data = await api("users?" + params.toString());
  state.users = { items: data.users || [], total: data.total || 0 };
  state.selectedUsers.clear();
  renderUsersView();
}

function renderUsersView() {
  const items = state.users.items;
  const total = state.users.total;
  const page = state.userFilter.page;
  const pages = Math.max(1, Math.ceil(total / state.userFilter.limit));
  $("#viewRoot").innerHTML = `
    <div class="view-head">
      <div><h1>Users</h1><p>Manage identities, access, quotas and mailbox configuration across ${esc(state.activeTenant.name)}.</p></div>
      <div class="view-actions">${can("users.create") ? `<button class="btn secondary" data-action="import-users">${icon("list")}Import users</button><button class="btn secondary" data-action="import-mailboxes">${icon("refresh")}Import mailboxes</button><button class="btn primary" data-action="new-user">${icon("plus")}Add user</button>` : ""}</div>
    </div>
    <div class="toolbar">
      <div class="search-box">${icon("search")}<input id="userSearch" type="search" placeholder="Search name, email, title…" value="${esc(state.userFilter.q)}"></div>
      <select id="userStatusFilter" aria-label="Filter status">
        ${["", "active", "suspended", "invited", "deleted"].map((value) => `<option value="${value}" ${state.userFilter.status === value ? "selected" : ""}>${value === "" ? "All statuses" : value[0].toUpperCase() + value.slice(1)}</option>`).join("")}
      </select>
      <select id="userRoleFilter" aria-label="Filter role">
        ${["", "owner", "admin", "member", "readonly"].map((value) => `<option value="${value}" ${state.userFilter.role === value ? "selected" : ""}>${value === "" ? "All roles" : roleLabel(value)}</option>`).join("")}
      </select>
      <label class="btn ghost small" style="display:flex;align-items:center;gap:6px"><input type="checkbox" id="userAdminFilter" ${state.userFilter.admin ? "checked" : ""}>Admins only</label>
      <span style="flex:1"></span>
      <span class="muted" style="font-size:11px">${total} user${total === 1 ? "" : "s"}</span>
    </div>
    <div class="bulk-bar" id="userBulkBar" ${state.selectedUsers.size ? "" : "hidden"}><b id="bulkCount">${state.selectedUsers.size}</b><span>selected</span><span class="spacer"></span><button class="btn small secondary" data-action="bulk-activate">Activate</button><button class="btn small secondary" data-action="bulk-suspend">Suspend</button><button class="btn small danger" data-action="bulk-delete">Close accounts</button></div>
    <div class="table-wrap">
      <div class="table-scroll">
        <table class="data">
          <thead><tr><th class="check-col"><input type="checkbox" id="selectAllUsers" aria-label="Select all users"></th><th>User</th><th>Status</th><th>Role</th><th>Org unit</th><th>MFA</th><th>Last sign-in</th><th>Storage</th><th></th></tr></thead>
          <tbody>${items.map(userRow).join("") || `<tr><td colspan="9"><div class="empty">${icon("users")}<h3>No users found</h3><p>Adjust the filters or add a new user.</p></div></td></tr>`}</tbody>
        </table>
      </div>
      <div class="pagination"><span>Page ${page} of ${pages} · ${total} total</span><span style="display:flex;gap:5px"><button class="btn small secondary" data-action="users-prev" ${page <= 1 ? "disabled" : ""}>Previous</button><button class="btn small secondary" data-action="users-next" ${page >= pages ? "disabled" : ""}>Next</button></span></div>
    </div>`;
  bindUserList();
}

function userRow(user) {
  const selected = state.selectedUsers.has(user.id);
  const status = user.deletedAt ? "suspended" : user.status;
  const actionToggle = status === "active" ? "suspend" : "activate";
  return `<tr class="clickable" data-user-id="${esc(user.id)}">
    <td class="check-col"><input type="checkbox" data-user-select="${esc(user.id)}" ${selected ? "checked" : ""} aria-label="Select ${esc(user.name)}"></td>
    <td><div class="user-cell"><span class="avatar" style="background:${esc(user.color || "#34557a")}">${esc(user.initials || initials(user.name))}</span><div><b>${esc(user.name)}</b><small>${esc(user.email)}</small></div></div></td>
    <td>${badge(user.deletedAt ? "deleted" : user.status)}</td>
    <td>${esc(roleLabel(user.role))}${user.isSystemAdmin ? ' <span class="badge system">System</span>' : ""}</td>
    <td>${esc(user.orgUnitName || "—")}</td>
    <td>${user.mfaEnabled ? `<span class="badge active">${icon("shield")} Enabled</span>` : `<span class="badge neutral">Not enrolled</span>`}</td>
    <td><span class="cell-sub">${user.lastLoginAt ? relativeTime(user.lastLoginAt) : "Never"}</span></td>
    <td><span class="cell-sub">${esc(formatBytes(user.usedBytes))} / ${esc(formatBytes(user.quotaBytes))}</span></td>
    <td><div class="row-actions">
      <button class="icon-button" data-user-action="${actionToggle}" data-user-id="${esc(user.id)}" title="${actionToggle === "suspend" ? "Suspend" : "Activate"}" aria-label="${actionToggle} ${esc(user.name)}">${icon(actionToggle === "suspend" ? "pause" : "play")}</button>
      <button class="icon-button" data-user-action="reset" data-user-id="${esc(user.id)}" title="Reset password" aria-label="Reset password for ${esc(user.name)}">${icon("key")}</button>
      <button class="icon-button" data-user-action="open" data-user-id="${esc(user.id)}" title="Open details" aria-label="Open ${esc(user.name)}">${icon("chevron-right")}</button>
    </div></td>
  </tr>`;
}

function bindUserList() {
  const search = $("#userSearch");
  let timer = null;
  search?.addEventListener("input", () => {
    clearTimeout(timer);
    timer = setTimeout(() => { state.userFilter.q = search.value.trim(); state.userFilter.page = 1; loadUsers().catch(showError); }, 300);
  });
  $("#userStatusFilter")?.addEventListener("change", (event) => { state.userFilter.status = event.target.value; state.userFilter.page = 1; loadUsers().catch(showError); });
  $("#userRoleFilter")?.addEventListener("change", (event) => { state.userFilter.role = event.target.value; state.userFilter.page = 1; loadUsers().catch(showError); });
  $("#userAdminFilter")?.addEventListener("change", (event) => { state.userFilter.admin = event.target.checked; state.userFilter.page = 1; loadUsers().catch(showError); });
  $("#selectAllUsers")?.addEventListener("change", (event) => {
    $$("[data-user-select]").forEach((checkbox) => {
      checkbox.checked = event.target.checked;
      if (event.target.checked) state.selectedUsers.add(checkbox.dataset.userSelect); else state.selectedUsers.delete(checkbox.dataset.userSelect);
    });
    updateBulkBar();
  });
  $$("[data-user-select]").forEach((checkbox) => checkbox.addEventListener("change", () => {
    if (checkbox.checked) state.selectedUsers.add(checkbox.dataset.userSelect); else state.selectedUsers.delete(checkbox.dataset.userSelect);
    updateBulkBar();
  }));
}

function updateBulkBar() {
  const bar = $("#userBulkBar");
  if (!bar) return;
  bar.hidden = state.selectedUsers.size === 0;
  const count = $("#bulkCount");
  if (count) count.textContent = String(state.selectedUsers.size);
}

async function openUserDrawer(id) {
  try {
    const data = await api("users/" + encodeURIComponent(id));
    state.drawerUser = data.user;
    renderUserDrawer(data.user);
  } catch (error) { showError(error); }
}

function renderUserDrawer(user) {
  const security = user.security || {};
  const summary = security.summary || {};
  const methods = security.methods || [];
  const status = user.deletedAt ? "deleted" : user.status;
  $("#drawerRoot").innerHTML = `
    <div class="drawer-backdrop" data-drawer-close></div>
    <aside class="drawer" role="dialog" aria-modal="true" aria-label="User details">
      <div class="drawer-head"><div style="flex:1"><div class="avatar-block"><span class="avatar" style="background:${esc(user.color)}">${esc(user.initials)}</span><div><h2>${esc(user.name)}</h2><p>${esc(user.email)} · ${esc(user.title || roleLabel(user.role))}</p></div></div><div style="display:flex;gap:5px">${badge(status)}${user.mfaEnabled ? `<span class="badge system">MFA</span>` : ""}</div></div><button class="icon-button" data-drawer-close aria-label="Close details">${icon("x")}</button></div>
      <div class="tabs" role="tablist"><button class="active" data-user-tab="profile">Profile</button><button data-user-tab="access">Access</button><button data-user-tab="security">Security</button><button data-user-tab="aliases">Aliases</button><button data-user-tab="activity">Activity</button></div>
      <div class="drawer-body">
        <section class="tab-panel active" data-user-panel="profile">
          <div class="detail-list">
            <div class="detail-row"><span>Display name</span><b>${esc(user.name)}</b></div>
            <div class="detail-row"><span>Primary email</span><b>${esc(user.email)}</b></div>
            <div class="detail-row"><span>Title</span><b>${esc(user.title || "—")}</b></div>
            <div class="detail-row"><span>Department</span><b>${esc(user.department || "—")}</b></div>
            <div class="detail-row"><span>Employee ID</span><b>${esc(user.employeeId || "—")}</b></div>
            <div class="detail-row"><span>Manager</span><b>${esc(user.managerEmail || "—")}</b></div>
            <div class="detail-row"><span>Org unit</span><b>${esc(user.orgUnitName || "Unassigned")}</b></div>
            <div class="detail-row"><span>Storage</span><b>${esc(formatBytes(user.usedBytes))} of ${esc(formatBytes(user.quotaBytes))}</b></div>
            <div class="detail-row"><span>Mailbox</span><b>${user.mailboxStatus === "ready" ? `<span class="badge active">Provisioned</span>` : user.mailboxStatus === "error" ? `<span class="badge suspended">Provisioning error</span>` : `<span class="badge pending">Provisioning</span>`}${user.maildirPath ? `<br><small class="muted">${esc(user.maildirPath)}</small>` : ""}</b></div>
            <div class="detail-row"><span>Created</span><b>${formatDate(user.createdAt)}</b></div>
            <div class="detail-row"><span>Last sign-in</span><b>${user.lastLoginAt ? formatDate(user.lastLoginAt, { time: true }) : "Never"}</b></div>
          </div>
          <div style="display:flex;gap:7px;margin-top:16px">${can("users.update") ? `<button class="btn secondary" data-user-action="edit" data-user-id="${esc(user.id)}">${icon("sliders")}Edit profile</button>` : ""}${user.forcePasswordChange ? `<span class="badge pending">Password reset required</span>` : ""}</div>
        </section>
        <section class="tab-panel" data-user-panel="access">
          <div class="mini-section" style="margin-top:0"><h3>Organization role</h3><div class="detail-list"><div class="detail-row"><span>Base role</span><b>${esc(roleLabel(user.role))}</b></div>${user.isSystemAdmin ? `<div class="detail-row"><span>Platform access</span><b>System administrator</b></div>` : ""}</div></div>
          <div class="mini-section"><h3>Delegated admin roles</h3>${(user.adminRoles || []).length ? (user.adminRoles || []).map((role) => `<div class="detail-row"><span>${esc(role.name)}</span><b>${(role.permissions || []).length} permissions</b></div>`).join("") : `<div class="empty-mini">No custom admin roles assigned.</div>`}${can("roles.manage") ? `<button class="btn secondary small" data-user-action="assign-role" data-user-id="${esc(user.id)}" style="margin-top:10px">${icon("plus")}Assign a role</button>` : ""}</div>
          <div class="mini-section"><h3>Groups</h3>${(user.groups || []).length ? (user.groups || []).map((group) => `<div class="detail-row"><span>${esc(group.name)}</span><b>${esc(group.memberRole)}</b></div>`).join("") : `<div class="empty-mini">Not a member of any group.</div>`}</div>
        </section>
        <section class="tab-panel" data-user-panel="security">
          <div class="detail-list">
            <div class="detail-row"><span>Authenticator</span><b>${summary.twoFactor?.totpEnabled ? "Enabled" : "Not enrolled"}</b></div>
            <div class="detail-row"><span>Passkeys</span><b>${summary.twoFactor?.passkeys?.length || 0}</b></div>
            <div class="detail-row"><span>Recovery codes remaining</span><b>${summary.twoFactor?.recoveryCodesRemaining || 0}</b></div>
            <div class="detail-row"><span>Active web sessions</span><b>${security.activeSessions || 0}</b></div>
            <div class="detail-row"><span>Trusted devices</span><b>${security.trustedDevices || 0}</b></div>
          </div>
          <div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:16px">
            ${can("users.security") ? `<button class="btn secondary" data-user-action="reset-mfa" data-user-id="${esc(user.id)}">${icon("shield")}Reset MFA</button>` : ""}
            ${can("sessions.manage") ? `<button class="btn secondary" data-user-action="revoke-sessions" data-user-id="${esc(user.id)}">${icon("devices")}Revoke sessions</button>` : ""}
          </div>
        </section>
        <section class="tab-panel" data-user-panel="aliases">
          <div id="userAliases">${(user.aliases || []).map(aliasRow).join("") || `<div class="empty-mini">No aliases.</div>`}</div>
          ${can("users.aliases") ? `<form id="addAliasForm" class="inline-form" style="margin-top:14px"><label class="field"><span>New alias email</span><input id="aliasEmail" type="email" placeholder="name@${esc((user.email || "").split("@")[1] || "company.com")}" required></label><button class="btn primary" type="submit">${icon("plus")}Add</button></form>` : ""}
        </section>
        <section class="tab-panel" data-user-panel="activity">
          <div id="userSessions"><div class="empty-mini">Loading sessions…</div></div>
        </section>
      </div>
      <div class="drawer-foot">
        ${can("users.reset_password") ? `<button class="btn secondary" data-user-action="reset" data-user-id="${esc(user.id)}">${icon("key")}Reset password</button>` : ""}
        ${status === "deleted" ? `<button class="btn primary" data-user-action="restore" data-user-id="${esc(user.id)}">${icon("refresh")}Restore</button>` : can("users.delete") ? `<button class="btn danger" data-user-action="delete" data-user-id="${esc(user.id)}">${icon("trash")}Close account</button>` : ""}
        ${can("users.update") && status !== "deleted" ? `<button class="btn ${status === "active" ? "secondary" : "primary"}" data-user-action="${status === "active" ? "suspend" : "activate"}" data-user-id="${esc(user.id)}">${icon(status === "active" ? "pause" : "play")}${status === "active" ? "Suspend" : "Activate"}</button>` : ""}
      </div>
    </aside>`;
  bindUserDrawer(user);
}

function aliasRow(alias) {
  return `<div class="detail-row"><span>${esc(alias.email)} ${alias.kind === "primary" ? `<span class="badge neutral">Primary</span>` : ""}</span><b>${alias.canSend ? "Can send" : "Receive only"} ${alias.kind !== "primary" && can("users.aliases") ? `<button class="icon-button" data-alias-delete="${esc(alias.id)}" aria-label="Remove alias">${icon("trash")}</button>` : ""}</b></div>`;
}

function closeDrawer() {
  $("#drawerRoot").innerHTML = "";
  state.drawerUser = null;
}

function bindUserDrawer(user) {
  $$("[data-drawer-close]").forEach((button) => button.addEventListener("click", closeDrawer));
  $$("[data-user-tab]").forEach((button) => button.addEventListener("click", () => {
    $$("[data-user-tab]").forEach((tab) => tab.classList.toggle("active", tab === button));
    $$("[data-user-panel]").forEach((panel) => panel.classList.toggle("active", panel.dataset.userPanel === button.dataset.userTab));
    if (button.dataset.userTab === "activity") loadUserSessions(user.id);
  }));
  const addAlias = $("#addAliasForm");
  addAlias?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.target.querySelector("button");
    setBusy(button, true, "Adding…");
    try {
      await api(`users/${encodeURIComponent(user.id)}/aliases`, { method: "POST", body: { email: $("#aliasEmail").value.trim(), label: "Alias" } });
      toast("Alias added");
      await openUserDrawer(user.id);
    } catch (error) { showError(error); setBusy(button, false); }
  });
  $$("[data-alias-delete]").forEach((button) => button.addEventListener("click", async () => {
    if (!(await confirmDialog("Remove alias", "This address will stop routing to the user.", "Remove alias"))) return;
    try {
      await api(`users/${encodeURIComponent(user.id)}/aliases/${encodeURIComponent(button.dataset.aliasDelete)}`, { method: "DELETE" });
      toast("Alias removed");
      await openUserDrawer(user.id);
    } catch (error) { showError(error); }
  }));
}

async function loadUserSessions(userId) {
  if (!state.drawerUser) return;
  try {
    const data = await api(`users/${encodeURIComponent(userId)}/sessions`);
    const target = $("#userSessions");
    if (!target) return;
    target.innerHTML = (data.sessions || []).length ? (data.sessions || []).map((session) => `<div class="detail-row"><span>${esc(session.device)}<br><small class="muted">${esc(session.ip || "Unknown IP")}</small></span><b>${relativeTime(session.lastSeenAt)}</b></div>`).join("") : `<div class="empty-mini">No active web sessions.</div>`;
  } catch (error) { showError(error); }
}

/* User actions */
async function userAction(action, id, button) {
  if (action === "open") { location.hash = "#/users/" + encodeURIComponent(id); return; }
  if (action === "edit") {
    const data = await api("users/" + encodeURIComponent(id));
    state.drawerUser = data.user;
    return editUserModal(data.user);
  }
  if (action === "assign-role") {
    const data = await api("users/" + encodeURIComponent(id));
    state.assignmentUser = data.user;
    return assignRoleModal(data.user);
  }
  if (action === "reset") return resetPasswordModal(id);
  if (action === "reset-mfa") return resetMfaModal(id);
  if (action === "revoke-sessions") return revokeSessionsModal(id);
  if (action === "delete") return deleteUserModal(id);
  if (action === "restore") return restoreUserModal(id);
  if (action === "suspend" || action === "activate") return toggleUserStatus(id, action);
}

async function resetPasswordModal(id) {
  if (!(await confirmDialog("Reset password", "A new temporary web password and mailbox password will be generated. All sessions will be revoked.", "Generate new passwords"))) return;
  try {
    const data = await api(`users/${encodeURIComponent(id)}/reset-password`, { method: "POST", body: {} });
    showCredentials("Temporary credentials", data, "The user must sign in with these credentials. They will not be shown again.");
    if (state.drawerUser && state.view !== "user-detail") openUserDrawer(id);
  } catch (error) { showError(error); }
}

async function resetMfaModal(id) {
  if (!(await confirmDialog("Reset MFA", "This removes the authenticator, passkeys, recovery codes and trusted devices. The user may be prompted to enroll again by policy.", "Reset MFA"))) return;
  try {
    await api(`users/${encodeURIComponent(id)}/reset-mfa`, { method: "POST", body: {} });
    toast("Authentication factors reset");
    closeDrawer();
  } catch (error) { showError(error); }
}

async function revokeSessionsModal(id) {
  if (!(await confirmDialog("Revoke sessions", "All active sign-in sessions for this user will be invalidated.", "Revoke sessions"))) return;
  try {
    const data = await api(`users/${encodeURIComponent(id)}/revoke-sessions`, { method: "POST", body: {} });
    toast(`${data.revoked || 0} session${data.revoked === 1 ? "" : "s"} revoked`);
    closeDrawer();
  } catch (error) { showError(error); }
}

async function deleteUserModal(id) {
  if (!(await confirmDialog("Close account", "The account will be suspended and appear in the closed accounts view. Mail and audit history are retained.", "Close account", true))) return;
  try {
    await api("users/" + encodeURIComponent(id), { method: "DELETE" });
    closeDrawer();
    toast("User account closed");
    await loadUsers();
  } catch (error) { showError(error); }
}

async function restoreUserModal(id) {
  try {
    await api(`users/${encodeURIComponent(id)}/restore`, { method: "POST", body: {} });
    toast("User restored");
    closeDrawer();
    await loadUsers();
  } catch (error) { showError(error); }
}

async function toggleUserStatus(id, action) {
  try {
    await api("users/" + encodeURIComponent(id), { method: "PATCH", body: { status: action === "suspend" ? "suspended" : "active" } });
    toast(action === "suspend" ? "User suspended" : "User activated");
    if (state.drawerUser && state.view !== "user-detail") await openUserDrawer(id);
    await loadUsers();
  } catch (error) { showError(error); }
}

function showCredentials(title, data, description) {
  openModal({
    title,
    subtitle: description,
    body: `<div class="detail-list">
      ${data.temporaryPassword && !data.passwordProvided ? `<div class="mini-section" style="margin-top:0"><h3>Temporary web password</h3><div class="copy-box"><span>${esc(data.temporaryPassword)}</span><button class="icon-button" data-copy="${esc(data.temporaryPassword)}" aria-label="Copy password">${icon("list")}</button></div></div>` : ""}
      ${data.passwordProvided ? `<div class="mini-section" style="margin-top:0"><h3>Web password</h3><p class="muted" style="font-size:11px;margin:0">The password you entered is active. The user will be asked to complete guided setup at first sign-in.</p></div>` : ""}
      ${data.mailPassword ? `<div class="mini-section"><h3>Mailbox password</h3><div class="copy-box"><span>${esc(data.mailPassword)}</span><button class="icon-button" data-copy="${esc(data.mailPassword)}" aria-label="Copy mailbox password">${icon("list")}</button></div><p class="muted" style="font-size:10.5px;margin:6px 0 0">Used by IMAP/SMTP mail clients. Store it in a password manager.</p></div>` : ""}
      ${data.owner?.email ? `<div class="mini-section"><h3>Owner account</h3><div class="detail-row"><span>Email</span><b>${esc(data.owner.email)}</b></div></div>` : ""}
    </div>`,
    footer: `<button class="btn primary" data-modal-close>Done</button>`
  });
  $$("[data-copy]").forEach((button) => button.addEventListener("click", async () => {
    await navigator.clipboard.writeText(button.dataset.copy).catch(() => {});
    toast("Copied to clipboard");
  }));
}

function userFormBody(root) {
  const body = {};
  $$("[data-user-field]", root).forEach((input) => {
    const key = input.dataset.userField;
    if (input.type === "checkbox") body[key] = input.checked;
    else if (input.dataset.number === "true") body[key] = Number(input.value);
    else body[key] = input.value.trim();
  });
  return body;
}

function userFormFields(user = {}) {
  const editing = Boolean(user.id);
  const emailField = editing
    ? `<label class="field"><span>Email</span><input data-user-field="email" type="email" value="${esc(user.email || "")}" disabled></label>`
    : `<label class="field"><span>Email address</span><div class="email-builder"><input data-user-field="username" value="" placeholder="avery.chen" autocomplete="off" autocapitalize="none" spellcheck="false" required><span class="email-at">@</span><select data-user-field="domain" aria-label="Email domain"><option value="">Loading domains…</option></select></div><span class="field-hint" data-user-form-hint>Choose a verified domain for this mailbox.</span></label>`;
  const passwordFields = editing ? "" : `<div class="field-row"><label class="field"><span>Password <small class="muted">optional</small></span><input data-user-field="password" type="password" autocomplete="new-password" placeholder="Set a password"><span class="field-hint">Leave blank to generate a one-time temporary password.</span></label><label class="field"><span>Confirm password</span><input data-password-confirm type="password" autocomplete="new-password" placeholder="Repeat the password"></label></div>`;
  return `<div class="field-row"><label class="field"><span>Full name</span><input data-user-field="name" value="${esc(user.name || "")}" placeholder="Avery Chen" required></label>${emailField}</div>
    ${passwordFields}
    <div class="field-row"><label class="field"><span>Base role</span><select data-user-field="role">${(can("roles.manage") ? ["member", "admin", "owner", "readonly"] : ["member", "readonly"]).map((role) => `<option value="${role}" ${user.role === role ? "selected" : ""}>${roleLabel(role)}</option>`).join("")}</select></label><label class="field"><span>Status</span><select data-user-field="status">${["active", "invited", "suspended"].map((status) => `<option value="${status}" ${user.status === status ? "selected" : ""}>${status[0].toUpperCase() + status.slice(1)}</option>`).join("")}</select></label></div>
    <div class="field-row"><label class="field"><span>Title</span><input data-user-field="title" value="${esc(user.title || "")}" placeholder="Product lead"></label><label class="field"><span>Department</span><input data-user-field="department" value="${esc(user.department || "")}" placeholder="Product"></label></div>
    <div class="field-row"><label class="field"><span>Employee ID</span><input data-user-field="employeeId" value="${esc(user.employeeId || "")}" placeholder="EMP-1042"></label><label class="field"><span>Manager email</span><input data-user-field="managerEmail" type="email" value="${esc(user.managerEmail || "")}" placeholder="manager@company.com"></label></div>
    <div class="field-row"><label class="field"><span>Storage quota (GB)</span><input data-user-field="quotaGb" data-number="true" type="number" min="1" max="1024" value="${Math.round((user.quotaBytes || 17179869184) / 1073741824)}"></label><label class="field"><span>Org unit</span><select data-user-field="orgUnitId"><option value="">Unassigned</option>${state.orgUnitOptions || ""}</select></label></div>
    ${editing ? "" : `<label class="field"><span>Additional aliases <small class="muted">optional</small></span><input data-user-field="aliases" placeholder="sales@company.com, support@company.com"><span class="field-hint">Full addresses, separated by commas. Their domain must also be verified.</span></label>`}`;
}

function openNewUserModal() {
  openModal({
    title: "Add a user",
    subtitle: "Pick the verified domain, enter the mailbox name and optionally set the first password.",
    wide: true,
    body: `<form id="newUserForm">${userFormFields({ status: "active" })}<p class="muted" style="font-size:10.5px">The user receives a Dovecot mailbox password and completes a guided first sign-in to set their display name, time zone, language and theme.</p></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" id="newUserSubmit" data-modal-submit="newUserForm" disabled>Create user</button>`,
    onMount: () => { loadUserFormDomains(); loadOrgUnitOptions(); }
  });
}

async function loadUserFormDomains() {
  const select = $('[data-user-field="domain"]');
  const submit = $("#newUserSubmit");
  const hint = $("[data-user-form-hint]");
  if (!select) return;
  try {
    const data = await api("users/domains");
    state.domains = data.domains || [];
    const usable = state.domains.filter((domain) => domain.status === "verified" || domain.status === "manual");
    if (!usable.length) {
      select.innerHTML = `<option value="">No verified domain</option>`;
      select.disabled = true;
      if (submit) submit.disabled = true;
      if (hint) hint.innerHTML = `Add and verify a domain in <a href="#/domains" data-open-domains>Domains</a> before creating users.`;
      return;
    }
    select.innerHTML = usable.map((domain) => `<option value="${esc(domain.domain)}" ${domain.isPrimary ? "selected" : ""}>${esc(domain.domain)}${domain.isPrimary ? " · primary" : ""}${domain.mailReady ? "" : " · DNS pending"}</option>`).join("");
    select.disabled = false;
    if (submit) submit.disabled = false;
    const usernameInput = $('[data-user-field="username"]');
    const renderPreview = () => {
      if (!hint || !usernameInput) return;
      const local = usernameInput.value.trim().toLowerCase();
      hint.innerHTML = local && select.value
        ? `Mailbox address: <b style="color:var(--ink-2)">${esc(local)}@${esc(select.value)}</b>`
        : "Only verified domains can receive a mailbox.";
    };
    usernameInput?.addEventListener("input", renderPreview);
    select.addEventListener("change", renderPreview);
    renderPreview();
  } catch (error) {
    select.innerHTML = `<option value="">Domains unavailable</option>`;
    select.disabled = true;
    if (submit) submit.disabled = true;
    if (hint) hint.textContent = error.message || "Could not load verified domains.";
  }
}

async function loadOrgUnitOptions(selected = "") {
  try {
    const data = await api("org-units");
    state.orgUnitOptions = (data.orgUnits || []).map((unit) => `<option value="${esc(unit.id)}" ${selected === unit.id ? "selected" : ""}>${esc(unit.name)}</option>`).join("");
    const select = $('[data-user-field="orgUnitId"]');
    if (select) select.innerHTML = `<option value="">Unassigned</option>${state.orgUnitOptions}`;
  } catch {}
}

function activeDomain() {
  const domains = state.domains || [];
  const primary = domains.find((domain) => domain.isPrimary) || domains[0];
  return primary?.domain || "company.com";
}

function editUserModal(user) {
  openModal({
    title: "Edit user",
    subtitle: `Update profile, role, status and quota for ${user.email}.`,
    wide: true,
    body: `<form id="editUserForm">${userFormFields(user)}</form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="editUserForm">Save changes</button>`,
    onMount: () => loadOrgUnitOptions(user.orgUnitId || "")
  });
}

async function submitUserForm(formId, userId) {
  const form = $("#" + formId);
  if (!form) return;
  const body = userFormBody(form);
  const quota = body.quotaGb;
  delete body.quotaGb;
  body.quotaBytes = Number(quota) * 1024 * 1024 * 1024;
  if (userId) {
    delete body.email;
    delete body.aliases;
    delete body.username;
    delete body.domain;
    delete body.password;
    const data = await api("users/" + encodeURIComponent(userId), { method: "PATCH", body });
    toast("User updated");
    closeModal();
    if (state.view === "user-detail") {
      await renderUserPage(userId);
    } else {
      await openUserDrawer(userId);
      await loadUsers();
    }
    return;
  }
  const usernameInput = form.querySelector('[data-user-field="username"]');
  const domainSelect = form.querySelector('[data-user-field="domain"]');
  const passwordInput = form.querySelector('[data-user-field="password"]');
  const passwordConfirm = form.querySelector("[data-password-confirm]");
  const username = String(usernameInput?.value || "").trim().toLowerCase();
  const domain = String(domainSelect?.value || "").trim().toLowerCase();
  if (!domain) {
    toast("Add and verify a domain before creating users", "error");
    return;
  }
  if (!/^[a-z0-9](?:[a-z0-9._%+-]{0,62}[a-z0-9])?$/.test(username) || username.includes("..") || username.includes("@")) {
    toast("Enter only the mailbox name before @", "error");
    usernameInput?.focus();
    return;
  }
  body.email = `${username}@${domain}`;
  delete body.username;
  delete body.domain;
  const password = String(passwordInput?.value || "");
  if (password) {
    if (password.length < 8) {
      toast("Use a password of at least 8 characters", "error");
      passwordInput?.focus();
      return;
    }
    if (password !== String(passwordConfirm?.value || "")) {
      toast("The passwords do not match", "error");
      passwordConfirm?.focus();
      return;
    }
    body.password = password;
  } else {
    delete body.password;
  }
  if (typeof body.aliases === "string") body.aliases = body.aliases.split(",").map((value) => value.trim()).filter(Boolean);
  const data = await api("users", { method: "POST", body });
  closeModal();
  showCredentials("User created", data, data.passwordProvided
    ? "The web password you set is active. The separate mailbox password is shown once."
    : "Share these one-time credentials with the user. The mailbox password is separate from the web password.");
  await loadUsers();
}

async function bulkUsers(action) {
  if (!state.selectedUsers.size) return;
  const label = { activate: "activate", suspend: "suspend", delete: "close" }[action];
  if (!(await confirmDialog("Bulk " + label, `${state.selectedUsers.size} selected account${state.selectedUsers.size === 1 ? "" : "s"} will be ${label === "close" ? "closed" : label + "d"}.`, "Continue", action === "delete"))) return;
  try {
    const data = await api("users/bulk", { method: "POST", body: { action, ids: Array.from(state.selectedUsers) } });
    toast(`${data.affected} account${data.affected === 1 ? "" : "s"} updated`);
    await loadUsers();
  } catch (error) { showError(error); }
}

/* ------------------------------------------------------------
   Groups
------------------------------------------------------------ */
async function renderGroups() {
  loading("Groups");
  try {
    const data = await api("groups");
    $("#viewRoot").innerHTML = `
      <div class="view-head"><div><h1>Groups</h1><p>Distribution, security and shared-mailbox groups for ${esc(state.activeTenant.name)}.</p></div>${can("groups.manage") ? `<div class="view-actions"><button class="btn primary" data-action="new-group">${icon("plus")}Create group</button></div>` : ""}</div>
      ${(data.groups || []).length ? `<div class="entity-grid">${(data.groups || []).map((group) => `<article class="entity-card" data-group-id="${esc(group.id)}"><div class="entity-head"><span class="entity-icon">${icon("group")}</span><div class="entity-title"><b>${esc(group.name)}</b><small>${esc(group.email)}</small></div>${badge(group.status)}</div><p class="muted" style="font-size:11px;margin:0">${esc(group.description || "No description.")}</p><div class="entity-meta"><span>${esc(group.type)}</span><span>·</span><span>${group.members} member${group.members === 1 ? "" : "s"}</span><span class="spacer"></span>${icon("chevron-right")}</div></article>`).join("")}</div>` : `<div class="card card-pad"><div class="empty">${icon("group")}<h3>No groups yet</h3><p>Create a distribution list or security group to organize access.</p></div></div>`}`;
    $$("[data-group-id]").forEach((card) => card.addEventListener("click", () => openGroupDrawer(card.dataset.groupId)));
  } catch (error) { viewError(error); }
}

function openNewGroupModal() {
  openModal({
    title: "Create group",
    subtitle: "Groups have a real address in PostgreSQL. Delivery integration is separate.",
    body: `<form id="newGroupForm"><div class="field-row"><label class="field"><span>Group name</span><input data-group-field="name" placeholder="Sales team" required></label><label class="field"><span>Group email</span><input data-group-field="email" type="email" placeholder="sales@${esc(state.activeTenant.slug)}.test" required></label></div><div class="field-row"><label class="field"><span>Type</span><select data-group-field="type"><option value="distribution">Distribution</option><option value="security">Security</option><option value="shared">Shared mailbox</option></select></label><label class="field"><span>Description</span><input data-group-field="description" placeholder="Customer-facing sales team"></label></div></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="newGroupForm">Create group</button>`
  });
}

function scimRoleLabel(role) {
  return { admin: "Admin", member: "Member", readonly: "Read-only" }[role] || "None";
}

function scimRoleRank(role) {
  return { "": 0, readonly: 1, member: 2, admin: 3 }[role] ?? 0;
}

function scimRoleMappingSection(group) {
  const role = String(group.scimRole || "");
  const security = group.type === "security";
  if (!security && !role) return "";
  const canManage = can("roles.manage") && can("groups.manage");
  const heading = `<div class="mini-section"><h3>Role mapping</h3>`;
  if (!security) {
    const action = canManage
      ? `<p class="muted" style="font-size:11.5px;margin:7px 0 0">This mapping is inactive because the group is not a security group. Clearing it requires group management and role management permissions (<code>groups.manage</code> and <code>roles.manage</code>).</p><button class="btn secondary small" id="scimRoleClearBtn" type="button">Clear mapping</button>`
      : `<p class="muted" style="font-size:11.5px;margin:7px 0 0">This mapping is inactive because the group is not a security group. Clearing it requires group management and role management permissions (<code>groups.manage</code> and <code>roles.manage</code>).</p>`;
    return `${heading}<div class="detail-row"><span>Legacy mapping</span><b>${esc(scimRoleLabel(role))}</b></div>${action}</div>`;
  }
  if (canManage) {
    return `${heading}<label class="field"><span>All members receive</span><select id="scimRoleSelect" data-scim-role-select>${[["", "None"], ["admin", "Admin"], ["member", "Member"], ["readonly", "Read-only"]].map(([value, label]) => `<option value="${value}" ${role === value ? "selected" : ""}>${label}</option>`).join("")}</select><small class="muted">Changing this mapping requires <code>groups.manage</code> and <code>roles.manage</code>. The mapped role wins over manual role changes while the mapping exists. The owner role is never changed. Saving recomputes every member immediately.</small></label></div>`;
  }
  return `${heading}<div class="detail-row"><span>All members receive</span><b>${esc(scimRoleLabel(role))}</b></div><p class="muted" style="font-size:11.5px;margin:7px 0 0">Only an administrator with group management and role management permissions (<code>groups.manage</code> and <code>roles.manage</code>) can change this mapping.</p></div>`;
}

async function openGroupDrawer(id) {
  try {
    const data = await api("groups/" + encodeURIComponent(id));
    const group = data.group;
    state.group = group;
    const members = group.members || [];
    const scimRole = String(group.scimRole || "");
    const mapped = scimRole !== "";
    const roleManager = can("roles.manage");
    const canMembers = can("groups.manage") && (!mapped || roleManager);
    const canDelete = can("groups.manage") && (!mapped || roleManager);
    const mappedNotice = mapped && !roleManager
      ? `<div class="group-role-notice" data-scim-mapped-notice>${icon("lock")}<span>This group is mapped to the ${esc(scimRoleLabel(scimRole))} role. Membership, deletion and type/status changes require group management and role management permissions (<code>groups.manage</code> and <code>roles.manage</code>).</span></div>`
      : "";
    $("#drawerRoot").innerHTML = `<div class="drawer-backdrop" data-drawer-close></div><aside class="drawer" role="dialog" aria-modal="true"><div class="drawer-head"><div style="flex:1"><div class="avatar-block"><span class="avatar" style="background:var(--brand-soft);color:var(--brand)">${icon("group")}</span><div><h2>${esc(group.name)}</h2><p>${esc(group.email)} · ${esc(group.type)} group</p></div></div>${badge(group.status)}</div><button class="icon-button" data-drawer-close aria-label="Close">${icon("x")}</button></div><div class="drawer-body"><div class="detail-list"><div class="detail-row"><span>Description</span><b>${esc(group.description || "—")}</b></div><div class="detail-row"><span>Members</span><b>${members.length}</b></div><div class="detail-row"><span>Created</span><b>${formatDate(group.createdAt)}</b></div></div>${scimRoleMappingSection(group)}${mappedNotice}<div class="mini-section"><h3>Members</h3>${members.length ? members.map((member) => `<div class="detail-row"><span style="display:flex;align-items:center;gap:7px"><span class="avatar" style="width:24px;height:24px;font-size:9px;background:${esc(member.color)}">${esc(member.initials)}</span>${esc(member.name)}<small class="muted">${esc(member.memberRole)}</small></span><b>${canMembers ? `<button class="icon-button" data-remove-member="${esc(member.id)}" aria-label="Remove ${esc(member.name)}">${icon("x")}</button>` : esc(member.email)}</b></div>`).join("") : `<div class="empty-mini">No members yet.</div>`}</div>${canMembers ? `<form id="addGroupMemberForm" class="inline-form" style="margin-top:14px"><label class="field"><span>Add user by email</span><input id="groupMemberSearch" type="email" placeholder="user@company.com" required></label><button class="btn primary" type="submit">${icon("plus")}Add</button></form><p class="muted" style="font-size:10.5px;margin-top:7px">Enter the exact user email in this organization.</p>` : ""}</div><div class="drawer-foot">${can("groups.manage") ? `<button class="btn secondary" data-group-edit="${esc(group.id)}">${icon("sliders")}Edit group</button>` : ""}${canDelete ? `<button class="btn danger" data-group-delete="${esc(group.id)}">${icon("trash")}Delete group</button>` : ""}</div></aside>`;
    $$("[data-drawer-close]").forEach((button) => button.addEventListener("click", closeDrawer));
    $("#scimRoleSelect")?.addEventListener("change", async (event) => {
      const select = event.currentTarget;
      const nextRole = select.value;
      if (nextRole === "admin" && !(await confirmDialog("Map group to Admin", "Every current and future member of this group will receive the tenant Admin role. Only continue if this group is controlled by your identity provider.", "Map to Admin", true))) {
        select.value = scimRole;
        return;
      }
      if (nextRole !== "admin" && scimRoleRank(nextRole) < scimRoleRank(scimRole)
        && !(await confirmDialog("Reduce group role mapping", `“${group.name}” is mapped to ${scimRoleLabel(scimRole)}. Changing it to ${scimRoleLabel(nextRole)} immediately reduces the mapped role for every affected member.`, "Reduce mapping", true))) {
        select.value = scimRole;
        return;
      }
      select.disabled = true;
      try {
        await api(`groups/${encodeURIComponent(group.id)}`, { method: "PATCH", body: { scimRole: nextRole } });
        toast(nextRole ? `Group mapped to ${roleLabel(nextRole)}` : "Role mapping cleared");
        await openGroupDrawer(group.id);
      } catch (error) {
        showError(error);
        select.disabled = false;
        select.value = scimRole;
      }
    });
    $("#scimRoleClearBtn")?.addEventListener("click", async (event) => {
      const button = event.currentTarget;
      if (!(await confirmDialog("Clear legacy role mapping", `The inactive ${scimRoleLabel(scimRole)} mapping will be removed from this non-security group.`, "Clear mapping", true))) return;
      setBusy(button, true, "Clearing…");
      try {
        await api(`groups/${encodeURIComponent(group.id)}`, { method: "PATCH", body: { scimRole: "" } });
        toast("Role mapping cleared");
        await openGroupDrawer(group.id);
      } catch (error) {
        showError(error);
        setBusy(button, false);
      }
    });
    $("#addGroupMemberForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      try {
        const list = await api(`users?q=${encodeURIComponent($("#groupMemberSearch").value.trim())}&limit=5`);
        const match = (list.users || []).find((user) => user.email.toLowerCase() === $("#groupMemberSearch").value.trim().toLowerCase());
        if (!match) throw new Error("No user in this organization has that email");
        await api(`groups/${encodeURIComponent(group.id)}/members`, { method: "POST", body: { userId: match.id } });
        toast("Member added");
        await openGroupDrawer(group.id);
      } catch (error) { showError(error); }
    });
    $$("[data-remove-member]").forEach((button) => button.addEventListener("click", async () => {
      try {
        await api(`groups/${encodeURIComponent(group.id)}/members/${encodeURIComponent(button.dataset.removeMember)}`, { method: "DELETE" });
        toast("Member removed");
        await openGroupDrawer(group.id);
      } catch (error) { showError(error); }
    }));
    $("[data-group-edit]")?.addEventListener("click", () => editGroupModal(group));
    $("[data-group-delete]")?.addEventListener("click", async () => {
      if (!(await confirmDialog("Delete group", "The group and its membership will be removed. This cannot be undone.", "Delete group", true))) return;
      try { await api("groups/" + encodeURIComponent(group.id), { method: "DELETE" }); closeDrawer(); toast("Group deleted"); renderGroups(); } catch (error) { showError(error); }
    });
  } catch (error) { showError(error); }
}

function editGroupModal(group) {
  const scimRole = String(group.scimRole || "");
  const mapped = scimRole !== "";
  const lifecycleLocked = mapped && !can("roles.manage");
  const editNotice = !mapped ? "" : lifecycleLocked
    ? `<div class="group-role-notice" data-scim-edit-notice>${icon("lock")}<span>This group is mapped to the ${esc(scimRoleLabel(scimRole))} role. Changing its type or status requires group management and role management permissions (<code>groups.manage</code> and <code>roles.manage</code>).</span></div>`
    : `<div class="group-role-notice" data-scim-edit-notice>${icon("lock")}<span>This group is mapped to the ${esc(scimRoleLabel(scimRole))} role and must remain a security group. Clear the role mapping in the group drawer before changing its type.</span></div>`;
  openModal({
    title: "Edit group",
    subtitle: group.email,
    body: `<form id="editGroupForm"><div class="field-row"><label class="field"><span>Group name</span><input data-group-edit-field="name" value="${esc(group.name)}" required></label><label class="field"><span>Email</span><input data-group-edit-field="email" type="email" value="${esc(group.email)}" required></label></div><div class="field-row"><label class="field"><span>Type</span><select data-group-edit-field="type" ${lifecycleLocked ? "disabled" : ""}>${["distribution", "security", "shared"].map((type) => `<option value="${type}" ${group.type === type ? "selected" : ""} ${mapped && type !== "security" ? "disabled" : ""}>${type[0].toUpperCase() + type.slice(1)}</option>`).join("")}</select></label><label class="field"><span>Status</span><select data-group-edit-field="status" ${lifecycleLocked ? "disabled" : ""}>${["active", "archived"].map((status) => `<option value="${status}" ${group.status === status ? "selected" : ""}>${status[0].toUpperCase() + status.slice(1)}</option>`).join("")}</select></label></div><label class="field"><span>Description</span><input data-group-edit-field="description" value="${esc(group.description || "")}"></label>${editNotice}</form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="editGroupForm">Save group</button>`
  });
}

async function submitGroupForm(formId, groupId) {
  const form = $("#" + formId);
  const body = {};
  $$(groupId ? "[data-group-edit-field]" : "[data-group-field]", form).forEach((input) => { body[input.dataset.groupEditField || input.dataset.groupField] = input.value.trim(); });
  if (groupId) {
    await api("groups/" + encodeURIComponent(groupId), { method: "PATCH", body });
    toast("Group updated");
    closeModal();
    await openGroupDrawer(groupId);
  } else {
    await api("groups", { method: "POST", body });
    toast("Group created");
    closeModal();
    renderGroups();
  }
}

/* ------------------------------------------------------------
   Team inboxes
------------------------------------------------------------ */
async function renderTeamInboxes() {
  loading("Team inboxes");
  try {
    const data = await api("team-inboxes");
    state.teamInboxes = data.teamInboxes || [];
    const items = state.teamInboxes;
    $("#viewRoot").innerHTML = `
      <div class="view-head"><div><h1>Team inboxes</h1><p>Shared queues backed by an organization mailbox, with group membership, assignment policy and SLA targets for ${esc(state.activeTenant.name)}.</p></div>${can("groups.manage") ? `<div class="view-actions"><button class="btn primary" data-action="new-team-inbox">${icon("plus")}Create team inbox</button></div>` : ""}</div>
      ${items.length ? `<div class="entity-grid">${items.map((team) => `<article class="entity-card" data-team-inbox-id="${esc(team.id)}"><div class="entity-head"><span class="entity-icon">${icon("users")}</span><div class="entity-title"><b>${esc(team.name)}</b><small>${esc(team.address)}</small></div>${badge(team.active ? "active" : "archived")}</div><div class="entity-meta"><span>${esc(team.strategy === "round_robin" ? "Round-robin" : "Manual")}</span><span>·</span><span>${team.members} agent${team.members === 1 ? "" : "s"}</span><span class="spacer"></span>${team.slaFirstResponseMinutes ? `${team.slaFirstResponseMinutes}m SLA` : "No SLA"} ${icon("chevron-right")}</div></article>`).join("")}</div>` : `<div class="card card-pad"><div class="empty">${icon("users")}<h3>No team inboxes yet</h3><p>Create a shared inbox for a support, sales or operations group.</p></div></div>`}`;
    $$("[data-team-inbox-id]").forEach((card) => card.addEventListener("click", () => openEditTeamInboxModal(state.teamInboxes.find((team) => team.id === card.dataset.teamInboxId))));
  } catch (error) { viewError(error); }
}

async function openNewTeamInboxModal() {
  try {
    const data = await api("groups");
    const groups = (data.groups || []).filter((group) => group.status === "active");
    openModal({
      title: "Create team inbox",
      subtitle: "The owner mailbox stores the mail; the group controls who can work the queue.",
      body: `<form id="newTeamInboxForm"><div class="field-row"><label class="field"><span>Name</span><input data-team-field="name" placeholder="Customer support" required></label><label class="field"><span>Queue address</span><input data-team-field="address" type="email" placeholder="support@company.com"></label></div><div class="field-row"><label class="field"><span>Backing mailbox owner</span><input data-team-field="ownerEmail" type="email" placeholder="support-mailbox@company.com" required><small class="muted">An active user with a mailbox in this organization.</small></label><label class="field"><span>Agent group</span><select data-team-field="groupId" required><option value="">Choose a group</option>${groups.map((group) => `<option value="${esc(group.id)}">${esc(group.name)} · ${esc(group.email)}</option>`).join("")}</select></label></div><div class="field-row"><label class="field"><span>Assignment</span><select data-team-field="assignmentStrategy"><option value="manual">Manual</option><option value="round_robin">Round-robin</option></select></label><label class="field"><span>First-response SLA (minutes)</span><input data-team-field="slaFirstResponseMinutes" type="number" min="0" max="10080" value="0"></label></div><div class="field-row"><label class="field"><span>Resolution SLA (minutes)</span><input data-team-field="slaResolutionMinutes" type="number" min="0" max="10080" value="0"></label><label class="check-field"><input data-team-field="canSend" type="checkbox" checked> Agents may send from this queue</label></div></form>`,
      footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="newTeamInboxForm">Create team inbox</button>`
    });
  } catch (error) { showError(error); }
}

function openEditTeamInboxModal(team) {
  if (!team) return;
  state.editingTeamInbox = team;
  openModal({
    title: "Edit team inbox",
    subtitle: `${team.address} · owner and group are fixed after creation`,
    body: `<form id="editTeamInboxForm"><div class="field-row"><label class="field"><span>Name</span><input data-team-edit-field="name" value="${esc(team.name)}" required></label><label class="field"><span>Queue address</span><input data-team-edit-field="address" type="email" value="${esc(team.address)}"></label></div><div class="field-row"><label class="field"><span>Agent group</span><input value="${esc(team.groupName)}" disabled></label><label class="field"><span>Backing owner</span><input value="${esc(team.ownerEmail)}" disabled></label></div><div class="field-row"><label class="field"><span>Assignment</span><select data-team-edit-field="assignmentStrategy"><option value="manual" ${team.strategy === "manual" ? "selected" : ""}>Manual</option><option value="round_robin" ${team.strategy === "round_robin" ? "selected" : ""}>Round-robin</option></select></label><label class="field"><span>First-response SLA (minutes)</span><input data-team-edit-field="slaFirstResponseMinutes" type="number" min="0" max="10080" value="${Number(team.slaFirstResponseMinutes || 0)}"></label></div><div class="field-row"><label class="field"><span>Resolution SLA (minutes)</span><input data-team-edit-field="slaResolutionMinutes" type="number" min="0" max="10080" value="${Number(team.slaResolutionMinutes || 0)}"></label><label class="check-field"><input data-team-edit-field="canSend" type="checkbox" ${team.canSend ? "checked" : ""}> Agents may send from this queue</label></div><label class="check-field"><input data-team-edit-field="active" type="checkbox" ${team.active ? "checked" : ""}> Team inbox is active</label></form>`,
    footer: `<button class="btn danger" id="teamInboxDelete" type="button">${icon("trash")}Delete</button><span class="spacer"></span><button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="editTeamInboxForm">Save team inbox</button>`
  });
  $("#teamInboxDelete")?.addEventListener("click", async () => {
    if (!(await confirmDialog("Delete team inbox", `“${team.name}” will stop appearing for its agents. Mail in the backing mailbox is not deleted.`, "Delete team inbox", true))) return;
    try {
      await api("team-inboxes/" + encodeURIComponent(team.id), { method: "DELETE" });
      toast("Team inbox deleted");
      closeModal();
      renderTeamInboxes();
    } catch (error) { showError(error); }
  });
}

async function submitTeamInboxForm(formId, teamId) {
  const form = $("#" + formId);
  const body = {};
  $$(teamId ? "[data-team-edit-field]" : "[data-team-field]", form).forEach((input) => {
    const key = input.dataset.teamEditField || input.dataset.teamField;
    if (input.type === "checkbox") body[key] = input.checked;
    else if (input.type === "number") body[key] = Number(input.value || 0);
    else body[key] = input.value.trim();
  });
  if (teamId) {
    await api("team-inboxes/" + encodeURIComponent(teamId), { method: "PATCH", body });
    toast("Team inbox updated");
  } else {
    await api("team-inboxes", { method: "POST", body });
    toast("Team inbox created");
  }
  closeModal();
  renderTeamInboxes();
}

/* ------------------------------------------------------------
   Org units
------------------------------------------------------------ */
async function renderOrgUnits() {
  loading("Org units");
  try {
    const data = await api("org-units");
    const units = data.orgUnits || [];
    const byParent = new Map();
    units.forEach((unit) => { const key = unit.parentId || ""; if (!byParent.has(key)) byParent.set(key, []); byParent.get(key).push(unit); });
    const rows = [];
    const walk = (parent, depth) => {
      (byParent.get(parent) || []).forEach((unit) => {
        rows.push(`<div class="org-node" style="--depth:${depth}">${icon("tree", "org-caret")}<span class="org-name">${esc(unit.name)}</span><span class="org-meta">${unit.users} user${unit.users === 1 ? "" : "s"}</span><span class="org-meta muted">${esc(unit.description || "")}</span><span class="row-actions">${can("orgunits.manage") ? `<button class="icon-button" data-org-edit="${esc(unit.id)}" aria-label="Edit ${esc(unit.name)}">${icon("sliders")}</button><button class="icon-button" data-org-delete="${esc(unit.id)}" aria-label="Delete ${esc(unit.name)}">${icon("trash")}</button>` : ""}</span></div>`);
        walk(unit.id, depth + 1);
      });
    };
    walk("", 0);
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Org units</h1><p>Organizational hierarchy used for policy targeting, directory structure and reporting.</p></div>${can("orgunits.manage") ? `<div class="view-actions"><button class="btn primary" data-action="new-org-unit">${icon("plus")}New org unit</button></div>` : ""}</div><div class="card"><div class="org-tree">${rows.join("") || `<div class="empty">${icon("tree")}<h3>No org units yet</h3><p>Create a hierarchy for departments, teams and regions.</p></div>`}</div></div>`;
    state.orgUnits = units;
    $$("[data-org-edit]").forEach((button) => button.addEventListener("click", () => editOrgUnitModal(units.find((unit) => unit.id === button.dataset.orgEdit))));
    $$("[data-org-delete]").forEach((button) => button.addEventListener("click", async () => {
      if (!(await confirmDialog("Delete org unit", "Move child units and users first. This action cannot be undone.", "Delete", true))) return;
      try { await api("org-units/" + encodeURIComponent(button.dataset.orgDelete), { method: "DELETE" }); toast("Org unit deleted"); renderOrgUnits(); } catch (error) { showError(error); }
    }));
  } catch (error) { viewError(error); }
}

function orgUnitModal(unit = null) {
  const units = state.orgUnits || [];
  const options = units.filter((candidate) => !unit || candidate.id !== unit.id).map((candidate) => `<option value="${esc(candidate.id)}" ${unit && unit.parentId === candidate.id ? "selected" : ""}>${esc(candidate.name)}</option>`).join("");
  openModal({
    title: unit ? "Edit org unit" : "New org unit",
    subtitle: "Org units nest up to six levels.",
    body: `<form id="${unit ? "editOrgUnitForm" : "newOrgUnitForm"}"><div class="field-row"><label class="field"><span>Name</span><input data-org-field="name" value="${esc(unit?.name || "")}" placeholder="Engineering" required></label><label class="field"><span>Parent</span><select data-org-field="parentId"><option value="">Root</option>${options}</select></label></div><label class="field"><span>Description</span><input data-org-field="description" value="${esc(unit?.description || "")}" placeholder="Product engineering organization"></label></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="${unit ? "editOrgUnitForm" : "newOrgUnitForm"}">${unit ? "Save" : "Create"}</button>`
  });
}

function editOrgUnitModal(unit) { orgUnitModal(unit); }

async function submitOrgUnitForm(formId, unitId) {
  const body = {};
  $$("[data-org-field]", $("#" + formId)).forEach((input) => { body[input.dataset.orgField] = input.value.trim(); });
  if (unitId) {
    await api("org-units/" + encodeURIComponent(unitId), { method: "PATCH", body });
    toast("Org unit updated");
  } else {
    await api("org-units", { method: "POST", body });
    toast("Org unit created");
  }
  closeModal();
  renderOrgUnits();
}

/* ------------------------------------------------------------
   Domains
------------------------------------------------------------ */
async function renderDomains() {
  loading("Domains");
  try {
    const data = await api("domains");
    const domains = data.domains || [];
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Domains</h1><p>Mail domains, DNS ownership and authentication records for ${esc(state.activeTenant.name)}.</p></div>${can("domains.manage") ? `<div class="view-actions"><button class="btn primary" data-action="new-domain">${icon("plus")}Add domain</button></div>` : ""}</div><div class="entity-grid">${domains.map(domainCard).join("") || `<div class="card card-pad"><div class="empty">${icon("globe")}<h3>No domains</h3><p>Add the domain your organization sends and receives mail on.</p></div></div>`}</div>`;
    state.domains = domains;
    $$("[data-domain-id]").forEach((card) => card.addEventListener("click", () => openDomainDrawer(card.dataset.domainId)));
  } catch (error) { viewError(error); }
}

function domainCard(domain) {
  const checks = domain.checks || {};
  const authState = domain.mailReady ? "Mail authentication ready" : domain.dkimKeyReady ? "DNS needs attention" : "DKIM key provisioning";
  return `<article class="entity-card domain-card" data-domain-id="${esc(domain.id)}"><div class="entity-head"><span class="entity-icon">${icon("globe")}</span><div class="entity-title"><b>${esc(domain.domain)}</b><small>${domain.isPrimary ? "Primary sending domain" : "Alternate domain"}</small></div>${badge(domain.status)}</div><div class="check-grid">${[["Verification", checks.verification ?? (domain.status === "verified" || domain.status === "manual")], ["MX", checks.mx], ["SPF", checks.spf], ["DKIM", checks.dkim], ["DMARC", checks.dmarc], ["MTA-STS", checks.mtaSts]].map(([label, ok]) => `<span class="check-pill ${ok ? "ok" : ""}">${icon(ok ? "check" : "x")}${esc(label)}</span>`).join("")}</div><div class="entity-meta"><span>DKIM: ${esc(domain.dkimSelector || "a8m")} · ${esc(authState)}</span><span class="spacer"></span>${icon("chevron-right")}</div></article>`;
}

function openNewDomainModal() {
  openModal({
    title: "Add a mail domain",
    subtitle: "Add DNS records after creating the domain, then run verification.",
    body: `<form id="newDomainForm"><label class="field"><span>Domain</span><input data-domain-field="domain" placeholder="northwind.com" required></label><div class="field-row"><label class="field"><span>DKIM selector</span><input data-domain-field="dkimSelector" value="a8m"></label><label class="field"><span>Make primary</span><select data-domain-field="primary"><option value="false">No</option><option value="true">Yes</option></select></label></div></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="newDomainForm">Add domain</button>`
  });
}

async function openDomainDrawer(id) {
  try {
    const domains = state.domains || [];
    const domain = domains.find((item) => item.id === id);
    if (!domain) return;
    const records = domain.records || [];
    $("#drawerRoot").innerHTML = `<div class="drawer-backdrop" data-drawer-close></div><aside class="drawer" role="dialog" aria-modal="true"><div class="drawer-head"><div style="flex:1"><div class="avatar-block"><span class="avatar" style="background:var(--brand-soft);color:var(--brand)">${icon("globe")}</span><div><h2>${esc(domain.domain)}</h2><p>${domain.isPrimary ? "Primary mail domain" : "Mail domain"}</p></div></div>${badge(domain.status)}</div><button class="icon-button" data-drawer-close>${icon("x")}</button></div><div class="drawer-body">
      <div class="detail-list"><div class="detail-row"><span>Ownership</span><b>${domain.status === "verified" || domain.status === "manual" ? "Verified" : "Pending verification"}</b></div><div class="detail-row"><span>Mail authentication</span><b>${domain.mailReady ? "Ready" : "Needs attention"}</b></div><div class="detail-row"><span>DKIM selector</span><b>${esc(domain.dkimSelector || "a8m")}</b></div><div class="detail-row"><span>DKIM key</span><b>${domain.dkimKeyReady ? "Provisioned" : "Provisioning"}</b></div>${domain.dkimKeyError ? `<div class="detail-row"><span>DKIM error</span><b>${esc(domain.dkimKeyError)}</b></div>` : ""}<div class="detail-row"><span>DMARC policy</span><b>${esc(domain.dmarcPolicy || "none")}</b></div><div class="detail-row"><span>MTA-STS</span><b>${domain.mtaStsMode && domain.mtaStsMode !== "none" ? `${esc(domain.mtaStsMode)} · ${domain.mtaStsVerified ? "verified" : "pending"}` : "Disabled"}</b></div><div class="detail-row"><span>Catch-all</span><b>${esc(domain.catchAll || "Disabled")}</b></div></div>
      <div class="mini-section"><h3>DNS records</h3><div class="table-wrap" style="box-shadow:none"><div class="table-scroll"><table class="data"><thead><tr><th>Type</th><th>Name</th><th>Value</th></tr></thead><tbody>${records.map((record) => `<tr><td>${esc(record.type)}</td><td class="cell-sub">${esc(record.name)}</td><td>${record.status === "provisioning" ? `<span class="muted" style="font-size:10px">${esc(record.note || "Provisioning DKIM key…")}</span>` : `<div class="copy-box" style="padding:5px 7px;font-size:9.5px"><span>${esc(record.value)}</span></div>`}</td></tr>`).join("")}</tbody></table></div></div></div>
      ${can("domains.manage") ? `<div class="mini-section"><h3>Domain controls</h3><form id="domainSettingsForm"><div class="field-row"><label class="field"><span>DKIM selector</span><input data-domain-edit="dkimSelector" value="${esc(domain.dkimSelector || "a8m")}"></label><label class="field"><span>DMARC policy</span><select data-domain-edit="dmarcPolicy">${["none", "quarantine", "reject"].map((value) => `<option value="${value}" ${domain.dmarcPolicy === value ? "selected" : ""}>${value}</option>`).join("")}</select></label></div><label class="field"><span>Catch-all address</span><input data-domain-edit="catchAll" type="email" value="${esc(domain.catchAll || "")}" placeholder="Optional"></label><div class="field-row"><label class="field"><span>MTA-STS mode</span><select data-domain-edit="mtaStsMode">${["none", "testing", "enforce"].map((value) => `<option value="${value}" ${(domain.mtaStsMode || "none") === value ? "selected" : ""}>${value}</option>`).join("")}</select></label><label class="field"><span>MTA-STS max age (seconds)</span><input data-domain-edit="mtaStsMaxAge" type="number" min="86400" max="31557600" value="${esc(domain.mtaStsMaxAge || 604800)}"></label></div><p class="muted" style="font-size:10.5px;margin:4px 0 0">Enabling MTA-STS requires the <code>_mta-sts</code> TXT record and an HTTPS policy at <code>${esc(domain.mtaStsPolicyUrl || "")}</code>.</p></form></div>` : ""}
    </div><div class="drawer-foot">${can("domains.manage") ? `<button class="btn secondary" data-domain-verify="${esc(domain.id)}">${icon("refresh")}Run DNS verification</button><button class="btn primary" data-domain-save="${esc(domain.id)}">Save domain</button>` : ""}</div></aside>`;
    $$("[data-drawer-close]").forEach((button) => button.addEventListener("click", closeDrawer));
    $("[data-domain-verify]")?.addEventListener("click", async (event) => {
      setBusy(event.currentTarget, true, "Checking DNS…");
      try {
        const result = await api(`domains/${encodeURIComponent(domain.id)}/verify`, { method: "POST", body: {} });
        if (result.ready) toast("Domain verified · mail authentication is complete");
        else if (result.ok) toast("Ownership verified · " + (result.error || "DNS authentication is incomplete"), "error");
        else toast(result.error || "DNS records are not complete", "error");
        closeDrawer();
        await renderDomains();
      } catch (error) { showError(error); setBusy(event.currentTarget, false); }
    });
    $("[data-domain-save]")?.addEventListener("click", async (event) => {
      const body = {};
      $$("[data-domain-edit]").forEach((input) => {
        body[input.dataset.domainEdit] = input.dataset.domainEdit === "mtaStsMaxAge" ? Number(input.value) : input.value.trim();
      });
      setBusy(event.currentTarget, true, "Saving…");
      try { await api("domains/" + encodeURIComponent(domain.id), { method: "PATCH", body }); toast("Domain updated"); closeDrawer(); await renderDomains(); } catch (error) { showError(error); setBusy(event.currentTarget, false); }
    });
  } catch (error) { showError(error); }
}

async function submitDomainForm() {
  const body = {};
  $$("[data-domain-field]").forEach((input) => {
    body[input.dataset.domainField] = input.dataset.domainField === "primary" ? input.value === "true" : input.value.trim();
  });
  await api("domains", { method: "POST", body });
  toast("Domain added · add the DNS records to verify");
  closeModal();
  await renderDomains();
}

/* ------------------------------------------------------------
   Roles
------------------------------------------------------------ */
async function renderRoles() {
  loading("Roles & access");
  try {
    const data = await api("roles");
    const roles = data.roles || [];
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Roles &amp; access</h1><p>Delegate administration with granular, auditable permissions.</p></div>${can("roles.manage") ? `<div class="view-actions"><button class="btn primary" data-action="new-role">${icon("plus")}New role</button></div>` : ""}</div><div class="entity-grid">${roles.map(roleCard).join("")}</div><div class="card" style="margin-top:16px"><div class="card-head"><div><h2>Permission catalog</h2><p>Every delegated permission available in this organization.</p></div></div><div class="card-pad"><div class="permission-grid">${(state.catalog || []).map((permission) => `<div class="permission-check"><div><b>${esc(permission.label)}</b><small>${esc(permission.description)}</small></div></div>`).join("")}</div></div></div>`;
    state.roles = roles;
    $$("[data-role-edit]").forEach((button) => button.addEventListener("click", () => roleModal(roles.find((role) => role.id === button.dataset.roleEdit))));
    $$("[data-role-assign]").forEach((button) => button.addEventListener("click", () => { state.assignmentUser = null; assignRoleModal(null, button.dataset.roleAssign); }));
    $$("[data-role-delete]").forEach((button) => button.addEventListener("click", async () => {
      if (!(await confirmDialog("Delete role", "The role must have no assignments. This cannot be undone.", "Delete role", true))) return;
      try { await api("roles/" + encodeURIComponent(button.dataset.roleDelete), { method: "DELETE" }); toast("Role deleted"); renderRoles(); } catch (error) { showError(error); }
    }));
  } catch (error) { viewError(error); }
}

function roleCard(role) {
  return `<article class="role-card"><div class="role-top"><span class="entity-icon">${icon("key")}</span><div style="flex:1"><b>${esc(role.name)}</b><small>${esc(role.description || "Custom delegated role")}</small></div>${role.implicit || role.isSystem ? `<span class="badge system">${role.implicit ? "Built-in" : "Standard"}</span>` : ""}</div><div class="role-perms">${(role.permissions || []).slice(0, 6).map((permission) => `<span class="perm-chip">${esc(permission)}</span>`).join("")}${(role.permissions || []).length > 6 ? `<span class="perm-chip">+${role.permissions.length - 6}</span>` : ""}</div><div class="entity-meta"><span>${role.assignments} assignment${role.assignments === 1 ? "" : "s"}</span><span class="spacer"></span></div><div class="role-actions">${can("roles.manage") && !role.implicit ? `<button class="btn small secondary" data-role-assign="${esc(role.id)}">${icon("users")}Assign</button>` : ""}${can("roles.manage") && !role.isSystem && !role.implicit ? `<button class="btn small ghost" data-role-edit="${esc(role.id)}">Edit</button><button class="btn small ghost" data-role-delete="${esc(role.id)}">${icon("trash")}</button>` : ""}</div></article>`;
}

function roleModal(role = null) {
  const selected = new Set(role?.permissions || []);
  openModal({
    title: role ? "Edit role" : "New custom role",
    subtitle: "Choose only the permissions this delegated administrator needs.",
    wide: true,
    body: `<form id="${role ? "editRoleForm" : "newRoleForm"}"><div class="field-row"><label class="field"><span>Role name</span><input data-role-field="name" value="${esc(role?.name || "")}" placeholder="Regional support lead" required></label><label class="field"><span>Description</span><input data-role-field="description" value="${esc(role?.description || "")}" placeholder="What this role is for"></label></div><div class="mini-section" style="margin-top:10px"><h3>Permissions</h3><div class="permission-grid">${(state.catalog || []).map((permission) => `<label class="permission-check"><input type="checkbox" data-role-permission="${esc(permission.key)}" ${selected.has(permission.key) ? "checked" : ""}><div><b>${esc(permission.label)}</b><small>${esc(permission.description)}</small></div></label>`).join("")}</div></div></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="${role ? "editRoleForm" : "newRoleForm"}">${role ? "Save role" : "Create role"}</button>`
  });
}

async function submitRoleForm(formId, roleId) {
  const body = { name: $('[data-role-field="name"]', $("#" + formId)).value.trim(), description: $('[data-role-field="description"]', $("#" + formId)).value.trim(), permissions: $$("[data-role-permission]:checked", $("#" + formId)).map((input) => input.dataset.rolePermission) };
  if (roleId) {
    await api("roles/" + encodeURIComponent(roleId), { method: "PATCH", body });
    toast("Role updated");
  } else {
    await api("roles", { method: "POST", body });
    toast("Role created");
  }
  closeModal();
  renderRoles();
}

async function assignRoleModal(user = null, roleId = null) {
  let users = [];
  try { users = (await api("users?limit=100&admin=true")).users || []; } catch {}
  const roles = (state.roles || []).filter((role) => !role.implicit);
  const userOptions = users.map((candidate) => `<option value="${esc(candidate.id)}" ${user && user.id === candidate.id ? "selected" : ""}>${esc(candidate.name)} — ${esc(candidate.email)}</option>`).join("");
  const roleOptions = roles.map((role) => `<option value="${esc(role.id)}" ${roleId === role.id ? "selected" : ""}>${esc(role.name)}</option>`).join("");
  openModal({
    title: "Assign an admin role",
    subtitle: "Delegated permissions are scoped to this organization and expire if you set an expiry.",
    body: `<form id="assignRoleForm"><label class="field"><span>User</span><select data-assign-field="userId" ${user ? "disabled" : ""}>${userOptions || `<option value="${esc(user?.id || "")}">${esc(user?.name || "No users")}</option>`}</select></label><label class="field"><span>Role</span><select data-assign-field="roleId">${roleOptions}</select></label><label class="field"><span>Expires (optional)</span><input data-assign-field="expiresAt" type="datetime-local"></label></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="assignRoleForm">Assign role</button>`
  });
}

async function submitAssignRoleForm(user) {
  const roleId = $('[data-assign-field="roleId"]').value;
  const userId = user?.id || $('[data-assign-field="userId"]').value;
  const expires = $('[data-assign-field="expiresAt"]').value;
  await api(`roles/${encodeURIComponent(roleId)}/assign`, { method: "POST", body: { userId, expiresAt: expires ? new Date(expires).toISOString() : "" } });
  toast("Admin role assigned");
  closeModal();
  if (state.view === "user-detail") renderUserPage(userId);
  else if (state.drawerUser) openUserDrawer(userId);
}

/* ------------------------------------------------------------
   Provisioning (SCIM 2.0)
------------------------------------------------------------ */
function scimTokenStatusBadge(status) {
  const meta = { active: ["active", "Active"], expired: ["pending", "Expired"], revoked: ["suspended", "Revoked"] }[status] || ["neutral", status || "Unknown"];
  return `<span class="badge ${meta[0]}">${esc(meta[1])}</span>`;
}

async function renderProvisioning() {
  loading("Provisioning");
  try {
    const data = await api("scim/tokens");
    const tokens = data.tokens || [];
    const endpoint = state.scimURL;
    const endpointRow = endpoint
      ? `<div class="detail-row"><span>SCIM base URL</span><b><code>${esc(endpoint)}</code></b></div>`
      : `<div class="detail-row"><span>SCIM base URL</span><b>Not configured</b></div>`;
    const endpointCopy = endpoint
      ? "Use this URL and a token as Bearer credentials in your identity provider."
      : "The SCIM base URL is not configured on this deployment. Set <code>ALT8MAIL_PUBLIC_URL</code>; see <code>docs/SCIM.md</code>.";
    const rows = tokens.map((token) => `<tr>
      <td><b>${esc(token.name)}</b></td>
      <td class="cell-sub"><code>${esc(token.prefix || "")}…</code></td>
      <td class="cell-sub">${formatDate(token.createdAt, { time: true })}</td>
      <td class="cell-sub">${token.lastUsedAt ? relativeTime(token.lastUsedAt) : "Never"}</td>
      <td class="cell-sub">${token.expiresAt ? formatDate(token.expiresAt, { time: true }) : "No expiry"}</td>
      <td>${scimTokenStatusBadge(token.status)}</td>
      <td><div class="row-actions">${token.status === "active" ? `<button class="btn small secondary" data-scim-rotate="${esc(token.id)}" aria-label="Rotate ${esc(token.name)}">Rotate</button>` : ""}${token.status !== "revoked" ? `<button class="btn small ghost" data-scim-revoke="${esc(token.id)}" aria-label="Revoke ${esc(token.name)}">Revoke</button>` : ""}</div></td>
    </tr>`).join("");
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Provisioning</h1><p>Issue SCIM 2.0 bearer tokens so your identity provider can manage users and groups in ${esc(state.activeTenant.name)}.</p></div><div class="view-actions"><button class="btn secondary" id="scimRefreshBtn">${icon("refresh")}Refresh</button><button class="btn primary" id="scimCreateBtn">${icon("plus")}Create token</button></div></div>
      <div class="card"><div class="card-pad">${endpointRow}<p class="muted" style="font-size:11.5px;margin:10px 0 0">${endpointCopy} Token holders can change membership of role-mapped groups, so minting, listing, rotating and revoking tokens requires <code>roles.manage</code>. The full token is shown once and only its hash is stored.</p></div></div>
      <div class="card" style="margin-top:14px"><div class="card-head"><div><h2>SCIM tokens</h2><p>Prefixes identify each token. Full token values are never displayed again after creation.</p></div><span class="badge neutral">${tokens.length} token${tokens.length === 1 ? "" : "s"}</span></div>${tokens.length ? `<div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Name</th><th>Prefix</th><th>Created</th><th>Last used</th><th>Expires</th><th>Status</th><th></th></tr></thead><tbody>${rows}</tbody></table></div></div>` : `<div class="card-pad"><div class="empty">${icon("plug")}<h3>No SCIM tokens yet</h3><p>Create a token, then paste it into your identity provider's SCIM configuration.</p><button class="btn primary" data-scim-create>${icon("plus")}Create token</button></div></div>`}</div>`;
    $("#scimCreateBtn")?.addEventListener("click", openSCIMTokenModal);
    $$("[data-scim-create]").forEach((button) => button.addEventListener("click", openSCIMTokenModal));
    $("#scimRefreshBtn")?.addEventListener("click", async (event) => {
      setBusy(event.currentTarget, true, "Refreshing…");
      await renderProvisioning();
    });
    $$("[data-scim-rotate]").forEach((button) => button.addEventListener("click", () => rotateSCIMToken(tokens.find((token) => token.id === button.dataset.scimRotate), button)));
    $$("[data-scim-revoke]").forEach((button) => button.addEventListener("click", () => revokeSCIMToken(tokens.find((token) => token.id === button.dataset.scimRevoke), button)));
  } catch (error) { viewError(error); }
}

function openSCIMTokenModal() {
  openModal({
    title: "Create SCIM token",
    subtitle: "The token is shown once. Store it in your identity provider immediately.",
    body: `<form id="newScimTokenForm"><label class="field"><span>Token name</span><input id="scimTokenName" maxlength="100" placeholder="Entra ID production" required><small class="muted">A label to recognise the identity provider or environment.</small></label><label class="field" style="margin-top:10px"><span>Expiry <small class="muted">optional</small></span><input id="scimTokenExpiry" type="datetime-local"><small class="muted">Leave blank for a non-expiring token. Expired tokens stop working and cannot be rotated.</small></label></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="newScimTokenForm">Create token</button>`
  });
}

async function submitSCIMTokenForm() {
  const name = $("#scimTokenName").value.trim();
  if (!name) throw new Error("Token name is required");
  const body = { name };
  const expiry = $("#scimTokenExpiry").value;
  if (expiry) {
    const when = new Date(expiry);
    if (Number.isNaN(when.getTime())) throw new Error("Choose a valid expiry");
    if (when.getTime() <= Date.now()) throw new Error("Choose an expiry in the future");
    body.expiresAt = when.toISOString();
  }
  const data = await api("scim/tokens", { method: "POST", body });
  closeModal();
  toast("SCIM token created");
  // Show the only copy of the raw token before refreshing the list. A failed
  // refresh must never hide the one-time value.
  showOneTimeSCIMToken(data, "Token created");
  await renderProvisioning();
}

// The raw token is deliberately kept out of `state`: it lives only in this
// modal's closure and is discarded when the modal closes.
function showOneTimeSCIMToken(data, title) {
  const rawToken = String(data.token || "");
  if (!rawToken) {
    toast("The token was saved, but the one-time value was not returned. Rotate the token to issue a new value.", "error");
    return;
  }
  const endpointCopy = state.scimURL
    ? `Use it as the Bearer token for <code>${esc(state.scimURL)}</code>.`
    : `Use it with the SCIM base URL documented in <code>docs/SCIM.md</code>.`;
  openModal({
    title,
    subtitle: "Copy the token now. It is shown once and cannot be retrieved later.",
    body: `<div class="copy-box"><span data-scim-token-value>${esc(rawToken)}</span><button class="icon-button" data-copy-scim-token aria-label="Copy SCIM token">${icon("list")}</button></div><p class="muted" style="font-size:11.5px;margin:10px 0 0"><b>This is the only time the token is displayed.</b> Alt8Mail stores only a hash. If you lose it, rotate or revoke it from this page.</p><p class="muted" style="font-size:11.5px;margin:6px 0 0">${endpointCopy}</p>`,
    footer: `<button class="btn primary" data-modal-close>I have stored it</button>`
  });
  const tokenValue = $("[data-scim-token-value]");
  $("[data-copy-scim-token]")?.addEventListener("click", async () => {
    try {
      if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(rawToken);
      toast("SCIM token copied");
    } catch {
      if (tokenValue && typeof window.getSelection === "function") {
        const range = document.createRange();
        range.selectNodeContents(tokenValue);
        const selection = window.getSelection();
        if (selection) {
          selection.removeAllRanges();
          selection.addRange(range);
        }
      }
      toast("Copy failed. The token text is selected; copy it manually.", "error");
    }
  });
}

async function rotateSCIMToken(token, button) {
  if (!token) return;
  if (!(await confirmDialog("Rotate SCIM token", `“${token.name}” will stop working immediately. Update your identity provider with the new token.`, "Rotate token", true))) return;
  setBusy(button, true, "Rotating…");
  try {
    const data = await api(`scim/tokens/${encodeURIComponent(token.id)}/rotate`, { method: "POST", body: {} });
    showOneTimeSCIMToken(data, "Token rotated");
    await renderProvisioning();
  } catch (error) { showError(error); setBusy(button, false); }
}

async function revokeSCIMToken(token, button) {
  if (!token) return;
  if (!(await confirmDialog("Revoke SCIM token", `“${token.name}” will stop authenticating immediately. This cannot be undone.`, "Revoke token", true))) return;
  setBusy(button, true, "Revoking…");
  try {
    await api(`scim/tokens/${encodeURIComponent(token.id)}`, { method: "DELETE" });
    toast("SCIM token revoked");
    await renderProvisioning();
  } catch (error) { showError(error); setBusy(button, false); }
}

/* ------------------------------------------------------------
   Policies
------------------------------------------------------------ */
async function renderPolicies() {
  loading("Policies");
  try {
    const data = await api("policies");
    state.policy = data.policy;
    const policy = data.policy;
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Policies</h1><p>Organization-wide guardrails for authentication, mail, sharing and directory.</p></div><div class="view-actions"><span class="badge system">Enforced at sign-in where applicable</span></div></div><div class="policy-layout"><nav class="policy-nav"><button class="active" data-policy-tab="security">Security</button><button data-policy-tab="mail">Mail</button><button data-policy-tab="sharing">Sharing</button><button data-policy-tab="office">Office</button><button data-policy-tab="directory">Directory</button><button data-policy-tab="dlp">Classification &amp; DLP</button><button data-policy-tab="features">Features</button><button data-policy-tab="branding">Branding</button></nav><div><form id="policyForm">${policySections(policy)}</form><div class="policy-save"><span>Changes apply to the whole organization. Sensitive changes are audited.</span><button class="btn secondary" type="button" data-action="run-retention" title="Save the retention policy first">Run retention</button><button class="btn secondary" type="button" data-action="reload-policies">Reset changes</button><button class="btn primary" type="button" data-action="save-policies">${icon("check")}Save policies</button></div></div></div>`;
    bindPolicyForm();
  } catch (error) { viewError(error); }
}

function toggleRow(label, description, key, checked, { disabled = false } = {}) {
  return `<div class="setting-row"><div class="setting-copy"><b>${esc(label)}</b><small>${esc(description)}</small></div><div class="setting-control"><button class="switch" type="button" data-policy-toggle="${esc(key)}" aria-checked="${checked ? "true" : "false"}" aria-label="${esc(label)}" ${disabled ? "disabled" : ""}></button></div></div>`;
}

function policySections(policy) {
  const security = policy.security;
  const mail = policy.mail;
  const sharing = policy.sharing;
  const directory = policy.directory;
  const branding = policy.branding;
  const dlp = policy.dlp || { enabled: true, defaultClassification: "Internal", rules: [] };
  const features = policy.features || { stickyNotes: true, reactions: true };
  const office = policy.office || { enabled: true, internalSharing: true, externalEditing: true, ai: true, dlpOnSave: true, versionRetention: 0, allowedGroups: [], allowedDomains: [] };
  return `<section class="card policy-panel active" data-policy-panel="security">
    <div class="policy-section"><h3>Password policy</h3><p>Applied to new users and password resets issued from this console.</p>
      <div class="setting-row"><div class="setting-copy"><b>Minimum length</b><small>Passwords shorter than this are rejected.</small></div><div class="setting-control"><div class="input-group"><input class="input" type="number" min="8" max="64" data-policy-input="security.passwordMinLength" value="${security.passwordMinLength}"><span class="muted">chars</span></div></div></div>
      ${toggleRow("Require uppercase", "At least one A–Z character.", "security.passwordRequiresUpper", security.passwordRequiresUpper)}
      ${toggleRow("Require lowercase", "At least one a–z character.", "security.passwordRequiresLower", security.passwordRequiresLower)}
      ${toggleRow("Require a number", "At least one numeric character.", "security.passwordRequiresNumber", security.passwordRequiresNumber)}
      ${toggleRow("Require a symbol", "At least one non-alphanumeric character.", "security.passwordRequiresSymbol", security.passwordRequiresSymbol)}
      <div class="setting-row"><div class="setting-copy"><b>Password history</b><small>Number of previous passwords remembered. 0 disables.</small></div><div class="setting-control"><input class="input" type="number" min="0" max="24" data-policy-input="security.passwordHistory" value="${security.passwordHistory}"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Password expiry</b><small>Days before users must change passwords. 0 means never.</small></div><div class="setting-control"><input class="input" type="number" min="0" max="365" data-policy-input="security.passwordExpiryDays" value="${security.passwordExpiryDays}"></div></div>
    </div>
    <div class="policy-section"><h3>Authentication</h3><p>Second-factor and session controls. Admins are checked at console sign-in.</p>
      <div class="setting-row"><div class="setting-copy"><b>MFA enforcement</b><small>Require an authenticator for admins only, all users, or leave it optional.</small></div><div class="setting-control"><select class="input" data-policy-input="security.mfaEnforcement">${[["off", "Optional"], ["admins", "Admins only"], ["all", "All users"]].map(([value, label]) => `<option value="${value}" ${security.mfaEnforcement === value ? "selected" : ""}>${label}</option>`).join("")}</select></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Session timeout</b><small>Maximum session lifetime in minutes.</small></div><div class="setting-control"><input class="input" type="number" min="15" max="1440" data-policy-input="security.sessionTimeoutMinutes" value="${security.sessionTimeoutMinutes}"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Idle timeout</b><small>Minutes of inactivity before the admin session expires.</small></div><div class="setting-control"><input class="input" type="number" min="5" max="240" data-policy-input="security.idleTimeoutMinutes" value="${security.idleTimeoutMinutes}"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Maximum sessions</b><small>Maximum concurrent sessions per user.</small></div><div class="setting-control"><input class="input" type="number" min="1" max="20" data-policy-input="security.maxSessions" value="${security.maxSessions}"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Allowed admin networks</b><small>IP addresses or CIDR ranges. Empty allows any network.</small></div><div class="setting-control"><input class="input" data-policy-input="security.allowedIPRanges" value="${esc((security.allowedIPRanges || []).join(", "))}" placeholder="203.0.113.0/24"></div></div>
      ${toggleRow("Notify on new sign-in", "Send an alert when an admin signs in from a new device.", "security.loginAlerts", security.loginAlerts)}
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="mail">
    <div class="policy-section"><h3>Mail access</h3><p>Controls available to mail clients and forwarding behavior.</p>
      ${toggleRow("External forwarding", "Allow users to auto-forward mail outside the organization.", "mail.externalForwarding", mail.externalForwarding)}
      ${toggleRow("Internal auto-forwarding", "Allow forwarding within the organization.", "mail.autoForwarding", mail.autoForwarding)}
      ${toggleRow("IMAP access", "Permit IMAP clients to authenticate.", "mail.imapAccess", mail.imapAccess)}
      ${toggleRow("POP access", "Permit POP3 clients to authenticate.", "mail.popAccess", mail.popAccess)}
      ${toggleRow("SMTP submission", "Permit authenticated outbound SMTP.", "mail.smtpAccess", mail.smtpAccess)}
      ${toggleRow("Connected accounts", "Allow users to add their own IMAP accounts and send through those providers. Credentials are encrypted and never returned.", "mail.externalAccounts", mail.externalAccounts)}
    </div>
    <div class="policy-section"><h3>Content protection</h3><p>Attachment, spam and retention restrictions.</p>
      <div class="setting-row"><div class="setting-copy"><b>Attachment size limit</b><small>Maximum message attachment size in megabytes.</small></div><div class="setting-control"><input class="input" type="number" min="1" max="100" data-policy-input="mail.maxAttachmentMB" value="${mail.maxAttachmentMB}"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Blocked extensions</b><small>Comma-separated extensions blocked by mail policy.</small></div><div class="setting-control"><input class="input" data-policy-input="mail.blockedExtensions" value="${esc((mail.blockedExtensions || []).join(", "))}" placeholder="exe, js, bat"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Allowed external recipients</b><small>Optional allowlist of domains or addresses. Empty allows all external recipients.</small></div><div class="setting-control"><input class="input" data-policy-input="mail.allowedExternalRecipients" value="${esc((mail.allowedExternalRecipients || []).join(", "))}" placeholder="partner.com"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Spam sensitivity</b><small>How aggressively Rspamd should quarantine inbound mail.</small></div><div class="setting-control"><select class="input" data-policy-input="mail.spamSensitivity">${["low", "medium", "high"].map((value) => `<option value="${value}" ${mail.spamSensitivity === value ? "selected" : ""}>${value[0].toUpperCase() + value.slice(1)}</option>`).join("")}</select></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Retention</b><small>Days to retain mail before deletion. 0 keeps mail until user action.</small></div><div class="setting-control"><input class="input" type="number" min="0" max="3650" data-policy-input="mail.retentionDays" value="${mail.retentionDays}"></div></div>
      ${toggleRow("Restrict Junk release to administrators", "When on, users cannot move mail out of Junk and an administrator must release it from the Quarantine view. Off by default so users can report not spam or move their own Junk.", "mail.quarantine", mail.quarantine)}
      ${toggleRow("Tag external mail", "Show an external sender banner in the reading pane.", "mail.externalTag", mail.externalTag)}
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="sharing">
    <div class="policy-section"><h3>File sharing</h3><p>Controls for links and external collaboration.</p>
      ${toggleRow("External sharing", "Allow sharing files with people outside the organization.", "sharing.externalSharing", sharing.externalSharing)}
      ${toggleRow("Guest invitations", "Allow guest accounts to collaborate on shared files.", "sharing.guestInvites", sharing.guestInvites)}
      <div class="setting-row"><div class="setting-copy"><b>Default link access</b><small>Default permission for newly created links.</small></div><div class="setting-control"><select class="input" data-policy-input="sharing.linkDefault">${[["view", "View only"], ["edit", "Can edit"], ["disabled", "Disabled"]].map(([value, label]) => `<option value="${value}" ${sharing.linkDefault === value ? "selected" : ""}>${label}</option>`).join("")}</select></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Allowed sharing domains</b><small>Comma-separated domains permitted for external sharing.</small></div><div class="setting-control"><input class="input" data-policy-input="sharing.allowedDomains" value="${esc((sharing.allowedDomains || []).join(", "))}" placeholder="partner.com"></div></div>
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="office">
    <div class="policy-section"><h3>Alt8Mail Office</h3><p>Control document editing, internal collaboration and content inspection for the Office editor.</p>
      ${toggleRow("Office enabled", "Allow the Office editor, document creation, conversion and guest document links.", "office.enabled", office.enabled)}
      ${toggleRow("Internal sharing", "Let file owners grant view or edit access to colleagues and groups from the Cabinet.", "office.internalSharing", office.internalSharing)}
      ${toggleRow("Guest editing", "Allow edit links to change Office documents. When off, guest links are always view-only.", "office.externalEditing", office.externalEditing)}
      ${toggleRow("AI assistant", "Show the Office AI panel to users. Requires an AI provider in System Control.", "office.ai", office.ai)}
      ${toggleRow("DLP on save", "Scan document text when the Office editor saves a version and record the classification and matches.", "office.dlpOnSave", office.dlpOnSave)}
      <div class="setting-row"><div class="setting-copy"><b>Version retention</b><small>Keep this many newest versions per Office document. 0 keeps every version. Retention is applied after each save and releases version storage.</small></div><div class="setting-control"><div class="input-group"><input class="input" type="number" min="0" max="1000" data-policy-input="office.versionRetention" value="${Number(office.versionRetention) || 0}"><span class="muted">versions</span></div></div></div>
    </div>
    <div class="policy-section"><h3>Office audience</h3><p>Empty lists allow everyone in the organization. When either list is set, a user must match a domain or belong to one of the listed groups.</p>
      <div class="setting-row"><div class="setting-copy"><b>Allowed groups</b><small>Comma-separated group names, for example Legal, Finance. Group names are matched case-insensitively.</small></div><div class="setting-control"><input class="input" data-policy-input="office.allowedGroups" value="${esc((office.allowedGroups || []).join(", "))}" placeholder="Legal, Finance"></div></div>
      <div class="setting-row"><div class="setting-copy"><b>Allowed domains</b><small>Comma-separated email domains for internal users and verified guest recipients.</small></div><div class="setting-control"><input class="input" data-policy-input="office.allowedDomains" value="${esc((office.allowedDomains || []).join(", "))}" placeholder="company.com"></div></div>
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="directory">
    <div class="policy-section"><h3>Directory</h3><p>How the organization directory is exposed to users.</p>
      ${toggleRow("Share contacts", "Allow users to see the organization directory.", "directory.contactSharing", directory.contactSharing)}
      ${toggleRow("External contacts in directory", "Include personal contacts outside your mail domains in directory search and autocomplete.", "directory.externalDirectory", directory.externalDirectory)}
      ${toggleRow("Profile editing", "Allow users to edit their own profile fields.", "directory.profileEditing", directory.profileEditing)}
      ${toggleRow("Auto-add external contacts", "Automatically add external senders to personal contacts.", "directory.autoAddExternal", directory.autoAddExternal)}
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="dlp">
    <div class="policy-section"><h3>Classification &amp; data loss prevention</h3><p>Scan outbound mail for sensitive terms, label messages and stop restricted content before it leaves the organization.</p>
      ${toggleRow("Outbound DLP", "Evaluate classification rules whenever a user sends or schedules a message.", "dlp.enabled", dlp.enabled)}
      <div class="setting-row"><div class="setting-copy"><b>Default classification</b><small>Applied when a sender does not choose a label.</small></div><div class="setting-control"><select class="input" data-policy-input="dlp.defaultClassification">${dlpClassificationOptions(dlp.defaultClassification)}</select></div></div>
    </div>
    <div class="policy-section"><div style="display:flex;align-items:center;justify-content:space-between;gap:12px"><div><h3 style="margin:0">DLP rules</h3><p style="margin:4px 0 0">A rule can label a message, warn the sender, or block the send entirely.</p></div><button class="btn secondary" type="button" id="dlpAddRule">${icon("plus")}Add rule</button></div><div id="dlpRules" style="margin-top:14px">${dlpRulesMarkup(dlp.rules)}</div></div>
  </section>
  <section class="card policy-panel" data-policy-panel="features">
    <div class="policy-section"><h3>Mailbox features</h3><p>Turn optional personal and team tools on or off for every mailbox in this organization.</p>
      ${toggleRow("Sticky notes", "One short markdown-lite note per mailbox, including shared team notes for delegated mailboxes. Visible above Inbox and on Mail Home.", "features.stickyNotes", features.stickyNotes)}
      ${toggleRow("Message reactions", "Allow private and emailed reactions on inbound conversations. Disabling hides the React control and rejects new reactions.", "features.reactions", features.reactions)}
    </div>
  </section>
  <section class="card policy-panel" data-policy-panel="branding">
    <div class="policy-section"><h3>Organization branding</h3><p>Shown in the admin console and organization communications.</p>
      <div class="field-row"><label class="field"><span>Product name</span><input data-policy-input="branding.productName" value="${esc(branding.productName)}"></label><label class="field"><span>Primary color</span><input type="color" data-policy-input="branding.primaryColor" value="${esc(branding.primaryColor || "#0f6cbd")}"></label></div>
      <div class="field-row"><label class="field"><span>Support email</span><input type="email" data-policy-input="branding.supportEmail" value="${esc(branding.supportEmail || "")}" placeholder="support@company.com"></label><label class="field"><span>Login message</span><input data-policy-input="branding.loginMessage" value="${esc(branding.loginMessage || "")}" placeholder="A short message for users"></label></div>
    </div>
  </section>`;
}

function dlpClassifications() {
  return ["Public", "Internal", "Confidential", "Restricted"];
}

function dlpClassificationOptions(selected) {
  return dlpClassifications().map((label) => `<option value="${label}" ${label === selected ? "selected" : ""}>${label}</option>`).join("");
}

function dlpRulesMarkup(rules) {
  const list = Array.isArray(rules) ? rules : [];
  if (!list.length) return `<div class="empty-mini" data-dlp-empty>No rules yet. Mail is labelled with the default classification until a rule matches.</div>`;
  return list.map(dlpRuleMarkup).join("");
}

function dlpRuleMarkup(rule = {}) {
  const fields = Array.isArray(rule.fields) && rule.fields.length ? rule.fields : ["subject", "body"];
  const action = rule.action || "warn";
  return `<div class="dlp-rule" data-dlp-rule data-rule-id="${esc(rule.id || "")}" style="border:1px solid var(--line);border-radius:10px;padding:12px;margin-bottom:10px">
    <div class="field-row">
      <label class="field"><span>Rule name</span><input class="input" data-dlp-field="name" value="${esc(rule.name || "")}" placeholder="Customer identifiers"></label>
      <label class="field"><span>Action</span><select class="input" data-dlp-field="action">${[["label", "Label only"], ["warn", "Warn sender"], ["block", "Block send"]].map(([value, label]) => `<option value="${value}" ${action === value ? "selected" : ""}>${label}</option>`).join("")}</select></label>
      <label class="field"><span>Classification</span><select class="input" data-dlp-field="classification">${dlpClassificationOptions(rule.classification || "Confidential")}</select></label>
      <label class="field"><span>Direction</span><select class="input" data-dlp-field="direction">${[["outbound", "Outbound"], ["both", "Outbound and internal"]].map(([value, label]) => `<option value="${value}" ${(rule.direction || "outbound") === value ? "selected" : ""}>${label}</option>`).join("")}</select></label>
    </div>
    <div class="field-row">
      <label class="field"><span>Terms</span><input class="input" data-dlp-field="terms" value="${esc((rule.terms || []).join(", "))}" placeholder="account number, social security"><small class="muted">Comma-separated. Matching is case-insensitive.</small></label>
      <div class="field"><span>Search fields</span><div style="display:flex;gap:14px;flex-wrap:wrap;padding-top:6px">${["subject", "body", "attachment"].map((field) => `<label style="display:flex;align-items:center;gap:5px;font-size:12px"><input type="checkbox" data-dlp-field="fields" value="${field}" ${fields.includes(field) ? "checked" : ""}>${field[0].toUpperCase() + field.slice(1)}</label>`).join("")}</div></div>
    </div>
    <div style="display:flex;align-items:center;justify-content:space-between;gap:12px"><label style="display:flex;align-items:center;gap:6px;font-size:12px"><input type="checkbox" data-dlp-field="enabled" ${rule.enabled !== false ? "checked" : ""}>Rule enabled</label><button class="btn small ghost" type="button" data-dlp-remove>Remove rule</button></div>
  </div>`;
}

function bindPolicyForm() {
  $$("[data-policy-tab]").forEach((button) => button.addEventListener("click", () => {
    $$("[data-policy-tab]").forEach((tab) => tab.classList.toggle("active", tab === button));
    $$("[data-policy-panel]").forEach((panel) => panel.classList.toggle("active", panel.dataset.policyPanel === button.dataset.policyTab));
  }));
  $$("[data-policy-toggle]").forEach((button) => button.addEventListener("click", () => {
    button.setAttribute("aria-checked", button.getAttribute("aria-checked") === "true" ? "false" : "true");
  }));
  $("#dlpAddRule")?.addEventListener("click", () => {
    $("#dlpRules")?.querySelector("[data-dlp-empty]")?.remove();
    $("#dlpRules")?.insertAdjacentHTML("beforeend", dlpRuleMarkup({}));
  });
  $("#dlpRules")?.addEventListener("click", (event) => {
    const remove = event.target.closest("[data-dlp-remove]");
    if (remove) remove.closest("[data-dlp-rule]")?.remove();
  });
}

function collectPolicy() {
  const policy = JSON.parse(JSON.stringify(state.policy));
  if (!policy.office) {
    policy.office = { enabled: true, internalSharing: true, externalEditing: true, ai: true, dlpOnSave: true, versionRetention: 0, allowedGroups: [], allowedDomains: [] };
  }
  $$("[data-policy-input]").forEach((input) => {
    const path = input.dataset.policyInput.split(".");
    let target = policy;
    for (let index = 0; index < path.length - 1; index++) target = target[path[index]];
    const key = path[path.length - 1];
    if (input.type === "number") target[key] = Number(input.value);
    else if (Array.isArray(target[key])) target[key] = input.value.split(",").map((value) => value.trim()).filter(Boolean);
    else target[key] = input.value;
  });
  $$("[data-policy-toggle]").forEach((button) => {
    const path = button.dataset.policyToggle.split(".");
    let target = policy;
    for (let index = 0; index < path.length - 1; index++) target = target[path[index]];
    target[path[path.length - 1]] = button.getAttribute("aria-checked") === "true";
  });
  if (policy.dlp) {
    policy.dlp.rules = $$("[data-dlp-rule]").map((row) => ({
      id: row.dataset.ruleId || "",
      name: row.querySelector('[data-dlp-field="name"]')?.value.trim() || "",
      terms: String(row.querySelector('[data-dlp-field="terms"]')?.value || "").split(",").map((value) => value.trim()).filter(Boolean),
      fields: $$('[data-dlp-field="fields"]', row).filter((input) => input.checked).map((input) => input.value),
      classification: row.querySelector('[data-dlp-field="classification"]')?.value || "Confidential",
      action: row.querySelector('[data-dlp-field="action"]')?.value || "warn",
      direction: row.querySelector('[data-dlp-field="direction"]')?.value || "outbound",
      enabled: row.querySelector('[data-dlp-field="enabled"]')?.checked !== false
    }));
  }
  return policy;
}

async function savePolicies() {
  const button = $('[data-action="save-policies"]');
  setBusy(button, true, "Saving…");
  try {
    const data = await api("policies", { method: "PATCH", body: collectPolicy() });
    state.policy = data.policy;
    toast("Policies saved");
    await loadBootstrap();
  } catch (error) { showError(error); } finally { setBusy(button, false); }
}

/* ------------------------------------------------------------
   Add-ons
------------------------------------------------------------ */
function adminAddonStatus(addon) {
  if (!addon.systemEnabled) return { label: "Disabled system-wide", cls: "neutral" };
  if (!addon.tenantEnabled) return { label: "Workspace off", cls: "suspended" };
  if (addon.connectionScope === "tenant" && !addon.connectionLabel) return { label: "Needs setup", cls: "pending" };
  if (addon.connectionScope === "user") return { label: "Personal setup", cls: "active" };
  return { label: "Configured", cls: "active" };
}

function adminAddonLogo(addon) {
  const file = String(addon.logo || "").split("/").filter(Boolean).pop() || "";
  return file ? `addon-logos/${encodeURIComponent(file)}` : "";
}

function adminProviderLinks(addon) {
  const links = [
    ["Website", addon.websiteUrl],
    ["Support", addon.supportUrl],
    ["Docs", addon.docsUrl],
    ["Privacy", addon.privacyUrl],
  ].filter(([, url]) => url);
  if (!links.length) return "";
  return `<div class="addon-admin-links">${links.map(([label, url]) =>
    `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(label)}</a>`
  ).join("")}</div>`;
}

async function renderAddons() {
  loading("Add-ons");
  try {
    const data = await api("addons");
    const addons = Array.isArray(data.addons) ? data.addons : [];
    const requests = Array.isArray(data.requests) ? data.requests : [];
    const canManage = can("policies.manage");
    const activeCount = addons.filter((addon) => addon.systemEnabled && addon.tenantEnabled).length;
    const requestMarkup = requests.length ? `<div class="card card-pad addons-admin-requests">
      <div class="card-head" style="padding:0 0 12px"><div><h2>Add-on requests</h2><p>Teammates waiting for workspace setup or install approval.</p></div><span class="badge pending">${requests.length} pending</span></div>
      <div class="addons-admin-request-list">${requests.map((request) => `<div class="addons-admin-request">
        <span class="addons-admin-request-logo">${request.addonLogo ? `<img src="${esc(adminAddonLogo({ logo: request.addonLogo }))}" alt="" loading="lazy">` : icon("plug")}</span>
        <div><b>${esc(request.addonName)} <span class="badge neutral">${request.requestType === "install" ? "Install approval" : "Setup request"}</span></b><small>${esc(request.userName || request.userEmail)}${request.note ? ` · ${esc(request.note)}` : ""}</small></div>
        <div class="addons-admin-request-actions"><button class="btn secondary small" type="button" data-addon-request="${esc(request.id)}" data-addon-request-status="approved" ${canManage ? "" : "disabled"}>Approve</button><button class="btn ghost small" type="button" data-addon-request="${esc(request.id)}" data-addon-request-status="denied" ${canManage ? "" : "disabled"}>Deny</button></div>
      </div>`).join("")}</div>
    </div>` : "";
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Add-ons</h1><p>Control which reviewed integrations this workspace offers, who can install them and pending approvals.</p></div><div class="view-actions"><a class="btn secondary" href="${esc(data.webmailAddonsUrl || "/#/Settings/addons")}" target="_blank" rel="noopener noreferrer">${icon("external")}Open webmail Add-ons</a><button class="btn secondary" type="button" id="addonsRefresh">${icon("refresh")}Refresh</button></div></div>
      <div class="addons-admin-summary"><div class="card card-pad"><span class="addons-admin-stat">${icon("plug")}<b>${addons.length}</b><small>reviewed add-ons</small></span></div><div class="card card-pad"><span class="addons-admin-stat">${icon("check")}<b>${activeCount}</b><small>enabled for this workspace</small></span></div><div class="card card-pad"><span class="addons-admin-stat">${icon("alert")}<b>${requests.length}</b><small>pending requests</small></span></div></div>
      <div class="card card-pad addons-admin-notice"><div><b>Workspace policy is managed here.</b><span>Credentials and provider setup are configured in Webmail → Settings → Add-ons. This console controls availability, install policy and approvals.</span></div><span class="badge ${canManage ? "active" : "neutral"}">${canManage ? "You can manage" : "Read-only"}</span></div>
      ${requestMarkup}
      <div class="addons-admin-grid">${addons.map((addon) => {
        const status = adminAddonStatus(addon);
        const logo = adminAddonLogo(addon);
        const disabled = !canManage || !addon.systemEnabled;
        return `<article class="card addons-admin-card">
          <div class="addons-admin-head">
            <span class="addons-admin-logo">${logo ? `<img src="${esc(logo)}" alt="${esc(addon.name)} logo" loading="lazy" decoding="async">` : icon("plug")}</span>
            <div class="addons-admin-title"><h3>${esc(addon.name)}</h3><p>${esc(addon.vendor || "Alt8Mail")} · ${esc(addon.category || "Add-on")}</p></div>
            <span class="badge ${status.cls}">${esc(status.label)}</span>
          </div>
          <p class="addons-admin-tagline">${esc(addon.tagline || addon.description || "")}</p>
          ${adminProviderLinks(addon)}
          <div class="detail-list addons-admin-detail">
            <div class="detail-row"><span>Connection</span><b>${esc(addon.connectionScope === "tenant" ? (addon.connectionLabel || "Needs workspace setup") : "Personal connection")}</b></div>
            <div class="detail-row"><span>Authentication</span><b>${esc(addon.authType === "oauth2" ? "OAuth 2.0" : addon.authType === "webhook" ? "Webhook" : addon.authType === "email" ? "Email address" : "API key")}</b></div>
            <div class="detail-row"><span>Active installs</span><b>${Number(addon.installCount || 0)}</b></div>
            <div class="detail-row"><span>Connector</span><b>v${esc(addon.connectorVersion || "1.0")}</b></div>
          </div>
          <div class="addons-admin-controls">
            <label class="addons-admin-check"><input type="checkbox" data-addon-enabled="${esc(addon.id)}" ${addon.tenantEnabled ? "checked" : ""} ${disabled ? "disabled" : ""}><span>Enabled for this workspace</span></label>
            <label class="addons-admin-policy"><span>Install policy</span><select class="input" data-addon-policy="${esc(addon.id)}" ${disabled ? "disabled" : ""}><option value="self_service" ${addon.installPolicy !== "approval_required" ? "selected" : ""}>Self-service</option><option value="approval_required" ${addon.installPolicy === "approval_required" ? "selected" : ""}>Approval required</option></select></label>
          </div>
        </article>`;
      }).join("")}</div>`;
    $("#addonsRefresh")?.addEventListener("click", renderAddons);
    $$("[data-addon-enabled]").forEach((input) => input.addEventListener("change", async () => {
      const addonID = input.getAttribute("data-addon-enabled");
      const previous = !input.checked;
      input.disabled = true;
      try {
        await api(`addons/${encodeURIComponent(addonID)}`, { method: "PATCH", body: { enabled: input.checked } });
        toast(input.checked ? "Add-on enabled for this workspace" : "Add-on disabled for this workspace");
        await renderAddons();
      } catch (error) {
        input.checked = previous;
        input.disabled = false;
        showError(error);
      }
    }));
    $$("[data-addon-policy]").forEach((select) => select.addEventListener("change", async () => {
      const addonID = select.getAttribute("data-addon-policy");
      const previous = select.value === "approval_required" ? "self_service" : "approval_required";
      select.disabled = true;
      try {
        await api(`addons/${encodeURIComponent(addonID)}`, { method: "PATCH", body: { installPolicy: select.value } });
        toast("Add-on install policy updated");
        await renderAddons();
      } catch (error) {
        select.value = previous;
        select.disabled = false;
        showError(error);
      }
    }));
    $$("[data-addon-request]").forEach((button) => button.addEventListener("click", async () => {
      const requestID = button.getAttribute("data-addon-request");
      const status = button.getAttribute("data-addon-request-status");
      button.disabled = true;
      try {
        await api(`addon-requests/${encodeURIComponent(requestID)}/resolve`, { method: "POST", body: { status } });
        toast(status === "approved" ? "Request approved" : "Request denied");
        await renderAddons();
      } catch (error) {
        button.disabled = false;
        showError(error);
      }
    }));
  } catch (error) { viewError(error); }
}

/* ------------------------------------------------------------
   AI assistant
------------------------------------------------------------ */
async function renderAI() {
  loading("AI assistant");
  try {
    const data = await api("ai");
    state.ai = data.ai;
    const ai = data.ai;
    const providers = data.providers || [];
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>AI assistant</h1><p>Let people use your organization's own AI provider inside compose and the reading pane.</p></div><div class="view-actions"><span class="badge ${ai.enabled ? "ok" : "system"}">${ai.enabled ? "Enabled" : "Disabled"}</span></div></div>
      <div class="dashboard-grid">
        <div class="card card-pad">
          <div class="setting-row" style="padding-top:0"><div class="setting-copy"><b>Enable AI assistant</b><small>Adds compose generation, message summaries and translation to every mailbox in this organization.</small></div><div class="setting-control"><button class="switch" type="button" id="aiEnabledToggle" data-ai-toggle="enabled" aria-checked="${ai.enabled ? "true" : "false"}" aria-label="Enable AI assistant"></button></div></div>
          <div class="field-row"><label class="field"><span>Provider</span><select class="input" id="aiProvider">${providers.map((provider) => `<option value="${esc(provider.code)}" ${ai.provider === provider.code ? "selected" : ""}>${esc(provider.name)}</option>`).join("")}</select></label><label class="field"><span>Model</span><input class="input" id="aiModel" value="${esc(ai.model || "")}" placeholder="gpt-4o-mini / claude-sonnet-5-5 / deepseek-chat"></label></div>
          <label class="field"><span>API endpoint</span><input class="input" id="aiEndpoint" value="${esc(ai.endpoint || "")}" placeholder="https://api.openai.com/v1 or https://api.anthropic.com"><small class="muted">OpenAI-compatible endpoints are accepted for OpenAI, DeepSeek, Azure gateways and self-hosted models. Claude uses the Anthropic Messages API.</small></label>
          <label class="field"><span>API key ${ai.hasKey ? '<small class="muted">— a key is configured; leave blank to keep it</small>' : ""}</span><input class="input" id="aiKey" type="password" autocomplete="new-password" placeholder="${ai.hasKey ? "••••••••••••••••" : "sk-..."}"><small class="muted">Keys are encrypted with AES-GCM before they reach PostgreSQL and are never returned to the browser.</small></label>
          <div class="field-row"><label class="field"><span>Max tokens</span><input class="input" id="aiMaxTokens" type="number" min="64" max="8192" value="${ai.maxTokens || 1024}"></label><label class="field"><span>Temperature</span><input class="input" id="aiTemperature" type="number" min="0" max="2" step="0.05" value="${ai.temperature ?? 0.7}"></label><label class="field"><span>Timeout (seconds)</span><input class="input" id="aiTimeout" type="number" min="5" max="120" value="${ai.timeoutSeconds || 30}"></label></div>
          <label class="field"><span>Organization system prompt <small class="muted">optional</small></span><textarea class="input" id="aiSystemPrompt" rows="3" placeholder="Writing rules, tone and compliance guidance for every AI request.">${esc(ai.systemPrompt || "")}</textarea></label>
          <div class="policy-save" style="margin-top:18px"><span>AI requests leave Alt8Mail for the endpoint you configure. The provider's data policy applies.</span><button class="btn secondary" type="button" data-action="test-ai">${icon("sparkles")}Save &amp; test</button><button class="btn primary" type="button" data-action="save-ai">${icon("check")}Save AI settings</button></div>
          <div id="aiTestResult" class="form-error" style="margin-top:12px" hidden></div>
        </div>
        <div>
          <div class="card card-pad"><div class="card-head" style="padding:0 0 12px"><div><h2>What users get</h2><p>Every capability respects the workspace language and the organization's provider.</p></div></div>
            <div class="detail-list">
              <div class="detail-row"><span>Compose assistant</span><b>8 styles · 3 lengths · creativity control</b></div>
              <div class="detail-row"><span>Languages</span><b>36 languages, including cross-language drafting</b></div>
              <div class="detail-row"><span>Reading pane</span><b>Summaries and translation in one click</b></div>
              <div class="detail-row"><span>Data path</span><b>Tenant-scoped · no shared provider key</b></div>
            </div>
          </div>
          <div class="card card-pad" style="margin-top:14px"><div class="card-head" style="padding:0 0 12px"><div><h2>Security notes</h2><p>Before enabling this in production.</p></div></div><div class="detail-list"><div class="detail-row"><span>Endpoint allowlist</span><b>HTTPS recommended</b></div><div class="detail-row"><span>API key storage</span><b>AES-GCM sealed</b></div><div class="detail-row"><span>Request retention</span><b>Cached summaries stay tenant-scoped</b></div></div><p class="muted" style="font-size:11px;margin:12px 0 0">Use a dedicated provider key with a spending cap. AI output is untrusted text and is rendered as plain text in the webmail.</p></div>
        </div>
      </div>`;
    $("#aiEnabledToggle")?.addEventListener("click", (event) => {
      const button = event.currentTarget;
      button.setAttribute("aria-checked", button.getAttribute("aria-checked") === "true" ? "false" : "true");
    });
  } catch (error) { viewError(error); }
}

function collectAIForm() {
  const enabled = $("#aiEnabledToggle")?.getAttribute("aria-checked") === "true";
  return {
    enabled,
    provider: $("#aiProvider")?.value || "openai",
    endpoint: $("#aiEndpoint")?.value.trim() || "",
    model: $("#aiModel")?.value.trim() || "",
    apiKey: $("#aiKey")?.value.trim() || undefined,
    maxTokens: Number($("#aiMaxTokens")?.value || 1024),
    temperature: Number($("#aiTemperature")?.value || 0.7),
    timeoutSeconds: Number($("#aiTimeout")?.value || 30),
    systemPrompt: $("#aiSystemPrompt")?.value || ""
  };
}

async function saveAI({ test = false } = {}) {
  const button = $(test ? '[data-action="test-ai"]' : '[data-action="save-ai"]');
  setBusy(button, true, test ? "Testing…" : "Saving…");
  const result = $("#aiTestResult");
  if (result) { result.hidden = true; result.textContent = ""; }
  try {
    const data = await api("ai", { method: "PATCH", body: collectAIForm() });
    state.ai = data.ai;
    toast("AI settings saved");
    if (test) {
      const tested = await api("ai/test", { method: "POST", body: {} });
      if (result) {
        result.hidden = false;
        result.classList.remove("form-error");
        result.style.color = "var(--success, #12715b)";
        result.textContent = `Connection OK · ${tested.latencyMs} ms · ${tested.model} replied: ${tested.reply}`;
      }
    } else {
      renderAI();
    }
  } catch (error) {
    if (result) { result.hidden = false; result.style.color = ""; result.textContent = error.message; }
    else showError(error);
  } finally { setBusy(button, false); }
}

async function runRetention() {
  if (!(await confirmDialog("Run mail retention", "Messages older than the saved retention period will be permanently removed from synchronized mail folders. Drafts, Junk and Trash are excluded.", "Run retention", true))) return;
  const button = $('[data-action="run-retention"]');
  setBusy(button, true, "Running…");
  try {
    const data = await api("maintenance/retention", { method: "POST" });
    toast(`Retention removed ${data.stats.messages} message${data.stats.messages === 1 ? "" : "s"} across ${data.stats.users} mailbox${data.stats.users === 1 ? "" : "es"}`);
  } catch (error) { showError(error); } finally { setBusy(button, false); }
}

/* ------------------------------------------------------------
   Quarantine
------------------------------------------------------------ */
async function renderQuarantine() {
  loading("Quarantine");
  try {
    const data = await api("quarantine");
    const items = data.items || [];
    $("#viewRoot").innerHTML = `
      <div class="view-head">
        <div><h1>Quarantine</h1><p>Review mail held in Junk across ${esc(state.activeTenant.name)}. ${data.adminOnly ? "Organization policy restricts Junk release to administrators, so users cannot move these messages themselves." : "Users can release their own Junk, and administrators can also release any message below."}</p></div>
        <div class="view-actions"><span class="badge ${data.adminOnly ? "system" : "neutral"}">${items.length} message${items.length === 1 ? "" : "s"}</span></div>
      </div>
      <div class="table-wrap">
        <div class="table-scroll">
          <table class="data">
            <thead><tr><th>Recipient</th><th>Message</th><th>Sender</th><th>Received</th><th></th></tr></thead>
            <tbody>${items.map((item) => `<tr>
              <td><div class="user-cell"><span class="avatar" style="background:#34557a">${esc(initials(item.userName || item.userEmail))}</span><div><b>${esc(item.userName)}</b><small>${esc(item.userEmail)}</small></div></div></td>
              <td><b>${esc(item.subject)}</b><small class="cell-sub">${esc(item.snippet || "")}</small></td>
              <td>${esc(item.fromName || item.fromEmail)}<small class="cell-sub">${esc(item.fromEmail)}</small></td>
              <td class="cell-sub">${esc(item.date ? formatDate(item.date, { time: true }) : "—")}</td>
              <td><div class="row-actions">${can("users.security") ? `<button class="btn small primary" data-action="release-quarantine" data-user-id="${esc(item.userId)}" data-message-id="${esc(item.messageId)}">${icon("arrow-up")}Release</button>` : ""}</div></td>
            </tr>`).join("") || `<tr><td colspan="5"><div class="empty">${icon("alert")}<h3>Quarantine is empty</h3><p>No messages are currently held in Junk.</p></div></td></tr>`}</tbody>
          </table>
        </div>
      </div>`;
  } catch (error) { viewError(error); }
}

async function releaseQuarantine(button) {
  const userId = button.dataset.userId;
  const messageId = button.dataset.messageId;
  if (!(await confirmDialog("Release quarantined message", "The message will be moved from Junk back to the recipient's Inbox. The release is recorded in the audit log.", "Release to Inbox"))) return;
  setBusy(button, true, "Releasing…");
  try {
    await api(`quarantine/${encodeURIComponent(userId)}/${encodeURIComponent(messageId)}/release`, { method: "POST" });
    toast("Message released to Inbox");
    await renderQuarantine();
  } catch (error) { showError(error); setBusy(button, false); }
}

/* ------------------------------------------------------------
   Audit
------------------------------------------------------------ */
async function renderAudit() {
  loading("Audit log");
  try {
    const source = state.auditSource === "office" ? "office" : "admin";
    const endpoint = source === "office" ? "audit/office" : "audit";
    const query = state.auditQuery ? "&q=" + encodeURIComponent(state.auditQuery) : "";
    const data = await api(endpoint + "?limit=100" + query);
    const entries = data.entries || [];
    const officeSource = source === "office";
    const exportName = officeSource ? "audit/office.csv" : "audit.csv";
    const exportHref = "./api/" + exportName + (state.auditQuery ? "?q=" + encodeURIComponent(state.auditQuery) : "");
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Audit log</h1><p>${officeSource ? `Office document, sharing and AI activity for ${esc(state.activeTenant.name)}.` : `Immutable administrative activity for ${esc(state.activeTenant.name)}.`}</p></div><div class="view-actions"><a class="btn secondary" href="${exportHref}" download>${icon("download")}Export CSV</a></div></div><div class="toolbar"><div style="display:flex;gap:6px;flex-wrap:wrap"><button class="btn small ${officeSource ? "secondary" : "primary"}" type="button" data-audit-source="admin">Administrator actions</button><button class="btn small ${officeSource ? "primary" : "secondary"}" type="button" data-audit-source="office">Office activity</button></div><div class="search-box">${icon("search")}<input id="auditSearch" type="search" placeholder="Search actor, target or detail…" value="${esc(state.auditQuery || "")}"></div><span class="muted" style="font-size:11px">${data.total} event${data.total === 1 ? "" : "s"}</span></div><div class="table-wrap"><div class="table-scroll"><table class="data audit-table"><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Detail</th><th>IP</th></tr></thead><tbody>${entries.map(auditRow).join("") || `<tr><td colspan="6"><div class="empty">${icon("list")}<h3>No audit events</h3><p>${officeSource ? "Office document opens, saves, shares, DLP blocks and reviews will appear here." : "Administrative changes will appear here."}</p></div></td></tr>`}</tbody></table></div></div>`;
    $$("[data-audit-source]").forEach((button) => button.addEventListener("click", () => {
      state.auditSource = button.dataset.auditSource === "office" ? "office" : "admin";
      renderAudit();
    }));
    let timer;
    $("#auditSearch")?.addEventListener("input", (event) => { clearTimeout(timer); timer = setTimeout(() => { state.auditQuery = event.target.value.trim(); renderAudit(); }, 350); });
  } catch (error) { viewError(error); }
}
function auditRow(entry) {
  return `<tr><td class="audit-time">${formatDate(entry.createdAt, { time: true })}</td><td>${esc(entry.actor || "System")}</td><td><span class="audit-action">${esc(entry.action)}</span></td><td>${esc(entry.target || "—")}</td><td class="audit-detail" title="${esc(entry.detail)}">${esc(entry.detail || "—")}</td><td class="cell-sub">${esc(entry.ip || "—")}</td></tr>`;
}

/* ------------------------------------------------------------
   Compliance · legal holds & eDiscovery
------------------------------------------------------------ */
async function renderCompliance() {
  loading("Compliance");
  try {
    const [data, jobsData] = await Promise.all([
      api("legal/holds"),
      api("offboarding/jobs").catch(() => ({ jobs: [] }))
    ]);
    const holds = data.holds || [];
    const stats = data.stats || {};
    const jobs = jobsData.jobs || [];
    const canManage = can("legal.manage");
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Compliance</h1><p>Preserve mailboxes under legal hold and search the organization archive with eDiscovery.</p></div><div class="view-actions">${canManage ? `<button class="btn primary" id="createHoldBtn">${icon("shield")}Place legal hold</button>` : ""}</div></div>
      <div class="stat-grid">
        <div class="stat-card"><span class="stat-label">${icon("shield")}Active holds</span><span class="stat-value">${stats.active || 0}</span><span class="stat-meta">${stats.tenantWide ? "Tenant-wide preservation" : "Scoped to selected mailboxes"}</span></div>
        <div class="stat-card"><span class="stat-label">${icon("users")}Held users</span><span class="stat-value">${stats.heldUsers || 0}</span><span class="stat-meta">Retention and deletion are paused</span></div>
        <div class="stat-card"><span class="stat-label">${icon("mail")}Archive scope</span><span class="stat-value">${esc(state.activeTenant.name)}</span><span class="stat-meta">Every synchronized mailbox</span></div>
      </div>
      <div class="card"><div class="card-head"><div><h2>Legal holds</h2><p>Held mailboxes cannot be permanently deleted or purged by retention.</p></div><span class="badge ${stats.active ? "system" : "neutral"}">${stats.active || 0} active</span></div><div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Matter</th><th>Scope</th><th>Mailbox</th><th>Reason</th><th>Placed</th><th>Status</th><th></th></tr></thead><tbody>${holds.map((hold) => `<tr><td><b>${esc(hold.name || "Untitled hold")}</b>${hold.reason ? `<br><small class="muted">${esc(hold.reason)}</small>` : ""}</td><td>${esc(hold.scope === "tenant" ? "Tenant-wide" : "Mailbox")}</td><td>${esc(hold.userEmail || "All mailboxes")}${hold.userName ? `<br><small class="muted">${esc(hold.userName)}</small>` : ""}</td><td class="cell-sub">${esc(hold.createdBy || "—")}</td><td class="cell-sub">${formatDate(hold.createdAt, { time: true })}</td><td>${hold.status === "active" ? badge("active") : `<span class="badge neutral">Released</span>`}</td><td>${canManage && hold.status === "active" ? `<button class="btn small ghost" data-release-hold="${esc(hold.id)}">Release</button>` : ""}</td></tr>`).join("") || `<tr><td colspan="7"><div class="empty">${icon("shield")}<h3>No legal holds</h3><p>Place a hold before an employee leaves or when litigation is reasonably anticipated.</p></div></td></tr>`}</tbody></table></div></div></div>
      <div class="card" style="margin-top:16px"><div class="card-head"><div><h2>eDiscovery search</h2><p>Search subjects, participants and message bodies across every mailbox in this organization.</p></div><span class="badge system">Read-only</span></div><div class="card-pad"><form id="discoveryForm"><div class="field-row"><label class="field"><span>Keywords</span><input id="discoveryQuery" placeholder="contract, account number, project name" autocomplete="off"></label><label class="field"><span>Mailbox email</span><input id="discoveryUserEmail" type="email" placeholder="person@company.com" autocomplete="off"></label></div><div class="field-row"><label class="field"><span>Sent from</span><input id="discoveryFrom" type="date"></label><label class="field"><span>Sent to</span><input id="discoveryTo" type="date"></label></div><div class="field-row"><label class="field"><span>Folder</span><input id="discoveryFolder" placeholder="inbox, sent, archive"></label><label class="field"><span>Classification</span><select id="discoveryClassification"><option value="">Any classification</option>${dlpClassificationOptions("")}</select></label></div><div style="display:flex;align-items:center;justify-content:flex-end;gap:8px;flex-wrap:wrap"><a class="btn secondary" id="discoveryExport" href="./api/legal/discovery.csv" download>${icon("download")}Export mail CSV</a><a class="btn secondary" id="officeDiscoveryExport" href="./api/legal/discovery-office.csv" download>${icon("download")}Export Office CSV</a><button class="btn primary" type="submit">${icon("search")}Search archive</button></div></form><div id="discoveryResults" style="margin-top:18px"><div class="empty-mini">Enter a keyword or mailbox to search the archive.</div></div></div></div>
      <div class="card" style="margin-top:16px"><div class="card-head"><div><h2>Recent offboarding</h2><p>Accounts closed through the one-click workflow, including mailbox handoff and preservation choices.</p></div><span class="badge neutral">${jobs.length} recent</span></div><div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Time</th><th>User</th><th>Transfer</th><th>Preservation</th><th>Status</th></tr></thead><tbody>${jobs.slice(0, 10).map((job) => `<tr><td class="cell-sub">${formatDate(job.createdAt, { time: true })}</td><td><b>${esc(job.userEmail || "—")}</b><br><small class="muted">${esc(job.userName || "")}</small></td><td>${esc(job.transferToEmail || "No delegate")}</td><td>${job.createLegalHold ? "Legal hold" : job.retainMailbox ? "Mailbox retained" : "Mailbox deactivated"}${job.autoReply ? " · auto-reply" : ""}</td><td>${job.status === "completed" ? badge("active") : badge("failed")}</td></tr>`).join("") || `<tr><td colspan="5"><div class="empty-mini">No offboarding jobs yet.</div></td></tr>`}</tbody></table></div></div></div>`;
    $("#createHoldBtn")?.addEventListener("click", openHoldModal);
    $$("[data-release-hold]").forEach((button) => button.addEventListener("click", () => releaseLegalHold(button.dataset.releaseHold)));
    $("#discoveryForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      searchDiscovery(0).catch(showError);
    });
    $("#discoveryResults")?.addEventListener("click", (event) => {
      const pageButton = event.target.closest("[data-discovery-offset]");
      if (pageButton) searchDiscovery(Number(pageButton.dataset.discoveryOffset) || 0).catch(showError);
    });
  } catch (error) { viewError(error); }
}

function discoverySearchParams() {
  const params = new URLSearchParams();
  const values = {
    q: $("#discoveryQuery")?.value.trim(),
    userEmail: $("#discoveryUserEmail")?.value.trim(),
    from: $("#discoveryFrom")?.value,
    to: $("#discoveryTo")?.value,
    folder: $("#discoveryFolder")?.value.trim(),
    classification: $("#discoveryClassification")?.value
  };
  Object.entries(values).forEach(([key, value]) => { if (value) params.set(key, value); });
  return params;
}

async function searchDiscovery(offset = 0) {
  const params = discoverySearchParams();
  params.set("offset", String(Math.max(0, offset)));
  params.set("limit", "25");
  const results = $("#discoveryResults");
  if (results) results.innerHTML = `<div class="empty-mini">${icon("refresh")}Searching the archive…</div>`;
  const exportParams = discoverySearchParams();
  const exportQuery = exportParams.toString() ? "?" + exportParams.toString() : "";
  const exportLink = $("#discoveryExport");
  if (exportLink) exportLink.href = "./api/legal/discovery.csv" + exportQuery;
  const officeExportLink = $("#officeDiscoveryExport");
  if (officeExportLink) officeExportLink.href = "./api/legal/discovery-office.csv" + exportQuery;
  const data = await api("legal/discovery?" + params.toString());
  if (!results) return;
  const messages = data.messages || [];
  const total = Number(data.total) || 0;
  const current = Number(data.offset) || 0;
  const limit = Number(data.limit) || 25;
  const rows = messages.map((message) => `<tr><td class="cell-sub">${formatDate(message.sentAt, { time: true })}</td><td><b>${esc(message.userEmail)}</b><br><small class="muted">${esc(message.userName || "")}</small></td><td><b>${esc(message.subject || "(no subject)")}</b><div class="cell-sub" style="max-width:460px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(message.snippet || "")}">${esc(message.snippet || "")}</div></td><td><small>${esc(message.fromEmail || "")}</small><br><small class="muted">to ${esc(message.to || "")}</small></td><td><span class="badge neutral">${esc(message.folder || "")}</span></td><td>${esc(message.classification || "Internal")}</td></tr>`).join("");
  results.innerHTML = `<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px"><span class="muted" style="font-size:11px">${total} message${total === 1 ? "" : "s"} matched${total > limit ? ` · showing ${current + 1}–${Math.min(current + limit, total)}` : ""}</span></div><div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Sent</th><th>Mailbox</th><th>Subject &amp; excerpt</th><th>Participants</th><th>Folder</th><th>Class</th></tr></thead><tbody>${rows || `<tr><td colspan="6"><div class="empty">${icon("search")}<h3>No matching messages</h3><p>Try a broader keyword or remove a filter.</p></div></td></tr>`}</tbody></table></div></div><div style="display:flex;justify-content:flex-end;gap:8px;margin-top:10px">${current > 0 ? `<button class="btn secondary small" data-discovery-offset="${Math.max(0, current - limit)}">Previous</button>` : ""}${current + limit < total ? `<button class="btn secondary small" data-discovery-offset="${current + limit}">Next</button>` : ""}</div>`;
}

function openHoldModal() {
  openModal({
    title: "Place a legal hold",
    subtitle: "Preservation applies immediately. Release the hold only when the matter is formally closed.",
    wide: true,
    body: `<form id="holdForm"><div class="field-row"><label class="field"><span>Scope</span><select id="holdScope"><option value="user">One mailbox</option><option value="tenant">Tenant-wide</option></select></label><label class="field" id="holdEmailField"><span>Mailbox email</span><input id="holdEmail" type="email" placeholder="custodian@company.com"></label></div><label class="field"><span>Matter name</span><input id="holdName" placeholder="Litigation hold · Matter 2026-014" required></label><label class="field"><span>Reason or instructions</span><textarea id="holdReason" rows="3" placeholder="Describe the matter and preservation scope"></textarea></label><p class="muted" style="font-size:10.5px;margin:0">Held mail is excluded from retention and permanent deletion. eDiscovery remains available.</p></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="holdForm">${icon("shield")}Place hold</button>`
  });
  $("#holdScope")?.addEventListener("change", () => {
    const field = $("#holdEmailField");
    if (field) field.style.display = $("#holdScope").value === "tenant" ? "none" : "";
  });
}

async function submitHoldForm() {
  const scope = $("#holdScope")?.value || "user";
  const email = $("#holdEmail")?.value.trim() || "";
  const name = $("#holdName")?.value.trim() || "";
  const reason = $("#holdReason")?.value.trim() || "";
  if (scope === "user" && !email) throw new Error("Enter the mailbox email to preserve");
  if (!name) throw new Error("Give the hold a matter name");
  await api("legal/holds", { method: "POST", body: { scope, email, name, reason } });
  closeModal();
  toast("Legal hold placed");
  await renderCompliance();
}

async function releaseLegalHold(id) {
  if (!(await confirmDialog("Release legal hold", "Retention and permanent deletion will be allowed again for this scope. The release is audited.", "Release hold", true))) return;
  try {
    await api("legal/holds/" + encodeURIComponent(id) + "/release", { method: "POST", body: {} });
    toast("Legal hold released");
    await renderCompliance();
  } catch (error) { showError(error); }
}

/* ------------------------------------------------------------
   Integrations · signed SIEM audit webhooks
------------------------------------------------------------ */
async function renderIntegrations() {
  loading("Integrations");
  try {
    const [config, deliveries] = await Promise.all([api("integrations/siem"), api("integrations/siem/deliveries").catch(() => ({ deliveries: [] }))]);
    const counts = config.deliveries || {};
    const canManage = can("siem.manage");
    const statusBadge = config.enabled ? `<span class="badge active">Delivering</span>` : config.configured ? `<span class="badge pending">Paused</span>` : `<span class="badge neutral">Not configured</span>`;
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Integrations</h1><p>Stream signed audit events into your SIEM, SOC or compliance archive.</p></div><div class="view-actions">${statusBadge}</div></div>
      <div class="stat-grid">
        <div class="stat-card"><span class="stat-label">${icon("send")}Pending</span><span class="stat-value">${counts.pending || 0}</span><span class="stat-meta">Queued for delivery</span></div>
        <div class="stat-card"><span class="stat-label">${icon("check")}Delivered</span><span class="stat-value">${counts.delivered || 0}</span><span class="stat-meta">${config.lastDeliveryAt ? "Last success " + formatDate(config.lastDeliveryAt, { time: true }) : "No successful delivery yet"}</span></div>
        <div class="stat-card"><span class="stat-label">${icon("alert")}Failed</span><span class="stat-value">${counts.failed || 0}</span><span class="stat-meta">Retried with backoff</span></div>
      </div>
      <div class="dashboard-grid"><div class="card"><div class="card-head"><div><h2>SIEM webhook</h2><p>HMAC-SHA256 signed JSON over HTTPS. Signing covers the timestamp and raw request body.</p></div></div><div class="card-pad"><form id="siemForm"><label class="field"><span>Endpoint URL</span><input id="siemEndpoint" type="url" value="${esc(config.endpoint || "")}" placeholder="https://siem.example.com/a8m/audit" ${canManage ? "" : "disabled"}><small class="muted">HTTPS only, public host, port 443. Redirects are rejected.</small></label><div class="field-row"><label class="field"><span>Signing secret</span><input id="siemSecret" type="password" autocomplete="new-password" placeholder="${config.hasSecret ? "Leave blank to keep the current secret" : "A secret will be generated"}" ${canManage ? "" : "disabled"}><small class="muted">Optional custom secret, 16–256 characters. Shown only once when created or rotated.</small></label><label class="field"><span>Status</span><select id="siemEnabled" ${canManage ? "" : "disabled"}><option value="true" ${config.enabled ? "selected" : ""}>Enabled</option><option value="false" ${!config.enabled ? "selected" : ""}>Paused</option></select></label></div><div style="display:flex;gap:18px;flex-wrap:wrap;margin:8px 0 14px"><label style="display:flex;gap:6px;align-items:center;font-size:12px"><input id="siemUserEvents" type="checkbox" ${config.includeUserEvents !== false ? "checked" : ""} ${canManage ? "" : "disabled"}>User audit events</label><label style="display:flex;gap:6px;align-items:center;font-size:12px"><input id="siemAdminEvents" type="checkbox" ${config.includeAdminEvents !== false ? "checked" : ""} ${canManage ? "" : "disabled"}>Administrator audit events</label></div>${config.lastError ? `<div style="border:1px solid var(--danger);border-radius:8px;padding:10px;margin-bottom:12px;background:var(--danger-soft);font-size:12px"><b>Last delivery error</b><br><small>${esc(config.lastError)}</small></div>` : ""}<div style="display:flex;justify-content:flex-end;gap:8px;flex-wrap:wrap">${canManage ? `<button class="btn secondary" type="button" id="siemTestBtn">${icon("send")}Send test event</button><button class="btn secondary" type="button" id="siemRotateBtn">${icon("refresh")}Rotate secret</button><button class="btn primary" type="submit">${icon("check")}Save integration</button>` : ""}</div></form></div></div>
      <div class="card"><div class="card-head"><div><h2>Delivery activity</h2><p>The 100 most recent audit events queued for this endpoint.</p></div><span class="badge neutral">${(deliveries.deliveries || []).length} shown</span></div><div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Time</th><th>Source</th><th>Event</th><th>Status</th><th>Attempts</th><th>Last error</th></tr></thead><tbody>${(deliveries.deliveries || []).map((item) => `<tr><td class="cell-sub">${formatDate(item.createdAt, { time: true })}</td><td>${esc(item.source)}</td><td><span class="audit-action">${esc(item.action || "—")}</span><br><small class="muted">#${esc(item.sourceId)}</small></td><td>${item.status === "delivered" ? badge("active") : item.status === "failed" ? badge("failed") : badge("pending")}</td><td>${item.attempts || 0}</td><td class="cell-sub" title="${esc(item.lastError || "")}">${esc(item.lastError || "—")}</td></tr>`).join("") || `<tr><td colspan="6"><div class="empty-mini">No deliveries yet. Save the integration and send a test event.</div></td></tr>`}</tbody></table></div></div></div></div>`;
    $("#siemForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.target.querySelector('button[type="submit"]');
      setBusy(button, true, "Saving…");
      try {
        const body = {
          endpoint: $("#siemEndpoint").value.trim(),
          enabled: $("#siemEnabled").value === "true",
          includeUserEvents: $("#siemUserEvents").checked,
          includeAdminEvents: $("#siemAdminEvents").checked
        };
        const secret = $("#siemSecret").value.trim();
        if (secret) body.secret = secret;
        const result = await api("integrations/siem", { method: "PUT", body });
        if (result.secret) showSIEMSecret(result.secret);
        else toast("SIEM integration saved");
        await renderIntegrations();
      } catch (error) { showError(error); setBusy(button, false); }
    });
    $("#siemTestBtn")?.addEventListener("click", async (event) => {
      setBusy(event.currentTarget, true, "Sending…");
      try {
        await api("integrations/siem/test", { method: "POST", body: {} });
        toast("Test event delivered");
        await renderIntegrations();
      } catch (error) { showError(error); setBusy(event.currentTarget, false); }
    });
    $("#siemRotateBtn")?.addEventListener("click", async () => {
      if (!(await confirmDialog("Rotate signing secret", "The current secret stops working immediately. Update the receiver before rotating.", "Rotate secret", true))) return;
      try {
        const result = await api("integrations/siem/rotate-secret", { method: "POST", body: {} });
        showSIEMSecret(result.secret);
        await renderIntegrations();
      } catch (error) { showError(error); }
    });
  } catch (error) { viewError(error); }
}

function showSIEMSecret(secret) {
  openModal({
    title: "SIEM signing secret",
    subtitle: "Copy this value into your SIEM receiver now. Alt8Mail will not show it again.",
    body: `<div class="copy-box"><span>${esc(secret)}</span><button class="icon-button" data-copy-siem-secret="${esc(secret)}" aria-label="Copy secret">${icon("list")}</button></div><p class="muted" style="font-size:10.5px;margin:10px 0 0">Signature header: <code>X-A8M-Signature: sha256=&lt;hex&gt;</code>, computed over <code>&lt;timestamp&gt;.&lt;raw-body&gt;</code>. Timestamp header: <code>X-A8M-Timestamp</code>.</p>`,
    footer: `<button class="btn primary" data-modal-close>I have stored it</button>`
  });
  $("[data-copy-siem-secret]")?.addEventListener("click", async (event) => {
    await navigator.clipboard.writeText(event.currentTarget.dataset.copySiemSecret).catch(() => {});
    toast("Signing secret copied");
  });
}

/* ------------------------------------------------------------
   Tenants (system admin)
------------------------------------------------------------ */
async function renderTenants() {
  if (!can("system.tenants.manage")) { $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Organizations</h1><p>System administrator access is required.</p></div></div>`; return; }
  loading("Organizations");
  try {
    const data = await api("tenants");
    const admins = await api("system/admins").catch(() => ({ admins: [], currentUserId: "" }));
    const tenants = data.tenants || [];
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Organizations</h1><p>Every tenant on the Alt8Mail control plane.</p></div><div class="view-actions"><button class="btn primary" data-action="new-tenant">${icon("plus")}New organization</button></div></div><div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Organization</th><th>Status</th><th>Plan</th><th>Users</th><th>Domains</th><th>Storage</th><th>Region</th><th></th></tr></thead><tbody>${tenants.map((tenant) => `<tr class="clickable"><td><div class="user-cell"><span class="avatar" style="background:#34557a">${esc(initials(tenant.name))}</span><div><b>${esc(tenant.name)} ${tenant.id === state.activeTenant.id ? `<span class="badge neutral">Current</span>` : ""}</b><small>${esc(tenant.slug)}</small></div></div></td><td>${badge(tenant.status)}</td><td>${esc(tenant.plan)}</td><td>${tenant.activeUsers} active / ${tenant.users}</td><td>${tenant.domains}</td><td>${formatBytes(tenant.storage?.usedBytes)}</td><td>${esc(tenant.region)}</td><td><div class="row-actions">${tenant.id !== state.activeTenant.id ? `<button class="btn small secondary" data-tenant-switch="${esc(tenant.id)}">Open</button>` : ""}<button class="icon-button" data-tenant-suspend="${esc(tenant.id)}" data-tenant-status="${esc(tenant.status)}" aria-label="${tenant.status === "active" ? "Suspend" : "Activate"} ${esc(tenant.name)}">${icon(tenant.status === "active" ? "pause" : "play")}</button></div></td></tr>`).join("")}</tbody></table></div></div><div class="card" style="margin-top:16px"><div class="card-head"><div><h2>System administrators</h2><p>Platform-wide access across every organization. Keep this list small.</p></div><span class="badge system">${admins.count || 0} admin${(admins.count || 0) === 1 ? "" : "s"}</span></div><div class="card-pad"><div id="systemAdminList">${(admins.admins || []).map((admin) => `<div class="detail-row"><span style="display:flex;align-items:center;gap:8px"><span class="avatar" style="width:26px;height:26px;font-size:9px;background:${esc(admin.color)}">${esc(admin.initials)}</span>${esc(admin.name)} <small class="muted">${esc(admin.email)}</small></span><b>${admin.id === admins.currentUserId ? `<span class="badge neutral">You</span>` : `<button class="btn small ghost" data-revoke-system-admin="${esc(admin.id)}">Revoke</button>`}</b></div>`).join("") || `<div class="empty-mini">No system administrators.</div>`}</div><form id="grantSystemAdminForm" class="inline-form" style="margin-top:14px"><label class="field"><span>Grant by email</span><input id="grantSystemAdminEmail" type="email" placeholder="colleague@a8m.test" required></label><button class="btn secondary" type="submit">${icon("shield")}Grant access</button></form></div></div>`;
    $$("[data-tenant-switch]").forEach((button) => button.addEventListener("click", () => switchTenant(button.dataset.tenantSwitch)));
    $$("[data-tenant-suspend]").forEach((button) => button.addEventListener("click", () => toggleTenant(button.dataset.tenantSuspend, button.dataset.tenantStatus)));
    $$("[data-revoke-system-admin]").forEach((button) => button.addEventListener("click", async () => {
      if (!(await confirmDialog("Revoke system access", "This account will lose cross-organization administration access.", "Revoke", true))) return;
      try { await api("system/admins/" + encodeURIComponent(button.dataset.revokeSystemAdmin), { method: "DELETE" }); toast("System access revoked"); await loadBootstrap(); renderTenants(); } catch (error) { showError(error); }
    }));
    $("#grantSystemAdminForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      try { await api("system/admins", { method: "POST", body: { email: $("#grantSystemAdminEmail").value.trim() } }); toast("System administrator granted"); renderTenants(); } catch (error) { showError(error); }
    });
  } catch (error) { viewError(error); }
}

function openNewTenantModal() {
  openModal({
    title: "Create an organization",
    subtitle: "Provision a tenant, primary domain and optional owner account.",
    wide: true,
    body: `<form id="newTenantForm"><div class="field-row"><label class="field"><span>Organization name</span><input data-tenant-field="name" placeholder="Northwind Labs" required></label><label class="field"><span>Slug</span><input data-tenant-field="slug" placeholder="northwind" required></label></div><div class="field-row"><label class="field"><span>Primary mail domain</span><input data-tenant-field="primaryDomain" placeholder="northwind.com" required></label><label class="field"><span>Plan</span><select data-tenant-field="plan">${["starter", "business", "enterprise", "education"].map((plan) => `<option value="${plan}" ${plan === "business" ? "selected" : ""}>${plan[0].toUpperCase() + plan.slice(1)}</option>`).join("")}</select></label></div><div class="field-row"><label class="field"><span>Region</span><select data-tenant-field="region">${["us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-1"].map((region) => `<option value="${region}">${region}</option>`).join("")}</select></label><label class="field"><span>Owner name</span><input data-tenant-field="ownerName" placeholder="Avery Chen"></label><label class="field"><span>Owner email</span><input data-tenant-field="ownerEmail" type="email" placeholder="owner@northwind.com"></label></div><p class="muted" style="font-size:10.5px">The owner is created with a temporary web password and mailbox password, both shown once.</p></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="newTenantForm">Create organization</button>`
  });
}

async function submitTenantForm() {
  const body = {};
  $$("[data-tenant-field]").forEach((input) => { body[input.dataset.tenantField] = input.value.trim(); });
  const data = await api("tenants", { method: "POST", body });
  closeModal();
  if (data.owner) showCredentials("Organization created", { owner: data.owner, temporaryPassword: data.owner.temporaryPassword, mailPassword: data.owner.mailPassword }, `${data.tenant.name} is ready. Share the owner credentials securely.`);
  else toast("Organization created");
  await loadBootstrap();
  renderTenants();
}

async function switchTenant(id) {
  try {
    await api("context", { method: "POST", body: { tenantId: id } });
    await loadBootstrap();
    toast(`Switched to ${state.activeTenant.name}`);
    route();
  } catch (error) { showError(error); }
}

async function toggleTenant(id, status) {
  const next = status === "active" ? "suspended" : "active";
  if (!(await confirmDialog(next === "suspended" ? "Suspend organization" : "Activate organization", next === "suspended" ? "Users will be signed out and tenant access will be blocked until reactivated." : "The organization and its users will be able to sign in again.", next === "suspended" ? "Suspend" : "Activate", next === "suspended"))) return;
  try { await api("tenants/" + encodeURIComponent(id), { method: "PATCH", body: { status: next } }); toast(`Organization ${next}`); await loadBootstrap(); renderTenants(); } catch (error) { showError(error); }
}

/* ------------------------------------------------------------
   Settings
------------------------------------------------------------ */
async function renderSettings() {
  loading("Organization settings");
  try {
    const data = await api("settings");
    const tenant = data.tenant;
    const branding = data.branding || {};
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Organization settings</h1><p>Profile, billing contact and branding for ${esc(tenant.name)}.</p></div></div><div class="dashboard-grid"><div class="card"><div class="card-head"><div><h2>Organization profile</h2><p>Visible to administrators and used in communications.</p></div></div><div class="card-pad"><form id="tenantSettingsForm"><div class="field-row"><label class="field"><span>Organization name</span><input data-settings-field="name" value="${esc(tenant.name)}" required></label><label class="field"><span>Billing email</span><input data-settings-field="billingEmail" type="email" value="${esc(tenant.billingEmail || "")}" placeholder="billing@company.com"></label></div>${state.admin?.isSystemAdmin ? `<div class="field-row"><label class="field"><span>Plan</span><select data-settings-field="plan">${["starter", "business", "enterprise", "education"].map((plan) => `<option value="${plan}" ${tenant.plan === plan ? "selected" : ""}>${plan[0].toUpperCase() + plan.slice(1)}</option>`).join("")}</select></label><label class="field"><span>Region</span><select data-settings-field="region">${["us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-1"].map((region) => `<option value="${region}" ${tenant.region === region ? "selected" : ""}>${region}</option>`).join("")}</select></label></div>` : ""}<div class="field-row"><label class="field"><span>Product name</span><input data-settings-branding="productName" value="${esc(branding.productName || "")}"></label><label class="field"><span>Primary color</span><input data-settings-branding="primaryColor" type="color" value="${esc(branding.primaryColor || "#0f6cbd")}"></label></div><div class="field-row"><label class="field"><span>Support email</span><input data-settings-branding="supportEmail" type="email" value="${esc(branding.supportEmail || "")}"></label><label class="field"><span>Login message</span><input data-settings-branding="loginMessage" value="${esc(branding.loginMessage || "")}"></label></div><div class="field-row"><label class="field"><span>Logo URL</span><input data-settings-branding="logoUrl" type="url" value="${esc(branding.logoUrl || "")}" placeholder="https://cdn.company.com/logo.png"></label><label class="field"><span>Support URL</span><input data-settings-branding="supportUrl" type="url" value="${esc(branding.supportUrl || "")}" placeholder="https://company.com/support"></label></div><div class="field-row"><label class="field"><span>Legal URL</span><input data-settings-branding="legalUrl" type="url" value="${esc(branding.legalUrl || "")}" placeholder="https://company.com/legal"></label><label class="field"><span>Custom webmail hosts</span><input data-settings-branding="webmailHosts" value="${esc((branding.webmailHosts || []).join(", "))}" placeholder="mail.company.com, webmail.company.com"></label></div><label class="field" style="display:flex;align-items:center;gap:8px"><input data-settings-branding="hideAttribution" type="checkbox" ${branding.hideAttribution ? "checked" : ""}><span>Hide “via Alt8Mail” attribution</span></label><p class="muted" style="font-size:10.5px;margin:0 0 12px">Applied to the webmail sign-in screen, app shell and outbound mail footer when the product name or support address differs from Alt8Mail defaults. Logo/support/legal links, custom hosts and attribution removal require the paid white_label entitlement; each host needs an enabled webmail Web Net route and certificate. Up to 5 hosts, comma-separated.</p><div style="display:flex;justify-content:flex-end"><button class="btn primary" type="submit">${icon("check")}Save settings</button></div></form></div></div><div><div class="card"><div class="card-head"><div><h2>My security</h2><p>Admin console protection.</p></div></div><div class="card-pad"><div class="detail-list"><div class="detail-row"><span>Authenticator</span><b>${state.admin?.security?.twoFactor?.totpEnabled ? "Enabled" : "Not enrolled"}</b></div><div class="detail-row"><span>Recovery codes</span><b>${state.admin?.security?.twoFactor?.recoveryCodesRemaining || 0} remaining</b></div><div class="detail-row"><span>Active admin sessions</span><b>${state.admin?.security?.twoFactor?.passkeys?.length ? `${state.admin.security.twoFactor.passkeys.length} passkeys` : "Password + MFA"}</b></div></div><button class="btn secondary" data-action="open-security" style="margin-top:14px">${icon("shield")}Manage security</button></div></div><div class="card" style="margin-top:14px"><div class="card-head"><div><h2>Tenant lifecycle</h2><p>${tenant.status === "active" ? "Active organization" : "Suspended organization"}</p></div></div><div class="card-pad"><p class="muted" style="font-size:11px">${state.admin?.isSystemAdmin ? "System administrators can suspend this organization from the Organizations page." : "Organization lifecycle is managed by Alt8Mail support."}</p></div></div></div></div>`;
    $("#tenantSettingsForm").addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.target.querySelector("button[type=submit]");
      setBusy(button, true, "Saving…");
      const body = {};
      $$("[data-settings-field]").forEach((input) => { body[input.dataset.settingsField] = input.value.trim(); });
      const branding = {};
      $$("[data-settings-branding]").forEach((input) => {
        if (input.type === "checkbox") branding[input.dataset.settingsBranding] = input.checked;
        else branding[input.dataset.settingsBranding] = input.value.trim();
      });
      branding.webmailHosts = String(branding.webmailHosts || "").split(",").map((host) => host.trim().toLowerCase()).filter(Boolean);
      body.branding = branding;
      try { await api("settings", { method: "PATCH", body }); toast("Organization settings saved"); await loadBootstrap(); renderSettings(); } catch (error) { showError(error); setBusy(button, false); }
    });
  } catch (error) { viewError(error); }
}

/* ------------------------------------------------------------
   Security modal
------------------------------------------------------------ */
async function openSecurityModal() {
  try {
    const [security, sessions] = await Promise.all([api("security"), api("security/sessions")]);
    const summary = security.security?.twoFactor || {};
    openModal({
      title: "My security",
      subtitle: "Protect your administrative access with a second factor and revoke stale sessions.",
      wide: true,
      body: `<div class="detail-list"><div class="detail-row"><span>Authenticator</span><b>${summary.totpEnabled ? "Enabled" : "Not enrolled"}</b></div><div class="detail-row"><span>Recovery codes</span><b>${security.recoveryRemaining || 0} remaining</b></div><div class="detail-row"><span>Admin sessions</span><b>${sessions.count || 0} active</b></div></div><div class="mini-section"><h3>Sessions</h3>${(sessions.sessions || []).map((session) => `<div class="detail-row"><span>${esc(session.device)}<br><small class="muted">${esc(session.ip || "Unknown")}</small></span><b>${session.current ? "This session" : relativeTime(session.lastSeenAt)}</b></div>`).join("")}<button class="btn secondary small" data-action="revoke-other-admins" style="margin-top:9px">${icon("devices")}Sign out other admin sessions</button></div><div class="mini-section"><h3>Password</h3><p class="muted" style="font-size:11px">Changing your password also updates the mailbox password and signs out other admin and webmail sessions.</p><button class="btn secondary small" data-action="change-admin-password">${icon("key")}Change password</button></div>${summary.totpEnabled ? `<div class="mini-section"><h3>Remove authenticator</h3><p class="muted" style="font-size:11px">Requires your password and a current authenticator or recovery code.</p><button class="btn danger" data-action="disable-admin-totp">${icon("trash")}Remove authenticator</button></div>` : `<div class="mini-section"><h3>Add an authenticator</h3><p class="muted" style="font-size:11px">You will scan or enter the secret in your authenticator app, then verify a six-digit code.</p><button class="btn primary" data-action="begin-admin-totp">${icon("shield")}Set up authenticator</button></div>`}`,
      footer: `<button class="btn secondary" data-modal-close>Close</button>`
    });
    $('[data-action="revoke-other-admins"]')?.addEventListener("click", async () => {
      if (!(await confirmDialog("Sign out other sessions", "All other admin console sessions will be revoked.", "Sign out others"))) return;
      try { const result = await api("security/sessions/revoke-others", { method: "POST", body: {} }); toast(`${result.revoked || 0} admin session(s) revoked`); closeModal(); } catch (error) { showError(error); }
    });
    $('[data-action="begin-admin-totp"]')?.addEventListener("click", beginAdminTOTP);
    $('[data-action="change-admin-password"]')?.addEventListener("click", openChangePasswordModal);
    $('[data-action="disable-admin-totp"]')?.addEventListener("click", disableAdminTOTP);
  } catch (error) { showError(error); }
}

async function beginAdminTOTP() {
  try {
    const data = await api("security/totp/begin", { method: "POST", body: {} });
    openModal({
      title: "Set up authenticator",
      subtitle: "Add this secret to Google Authenticator, 1Password, Authy or another TOTP app.",
      body: `<div class="copy-box"><span>${esc(data.secret)}</span><button class="icon-button" data-copy="${esc(data.secret)}" aria-label="Copy secret">${icon("list")}</button></div><p class="muted" style="font-size:10.5px;margin:8px 0 14px">Or open <a href="${esc(data.otpauth)}" style="color:var(--brand)">this otpauth link</a> in a compatible app.</p><form id="adminTotpForm"><label class="field"><span>Six-digit code</span><input id="adminTotpCode" inputmode="numeric" maxlength="6" placeholder="000000" required></label></form>`,
      footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="adminTotpForm">Enable authenticator</button>`
    });
    $("[data-copy]")?.addEventListener("click", async () => { await navigator.clipboard.writeText(data.secret).catch(() => {}); toast("Secret copied"); });
  } catch (error) { showError(error); }
}

async function submitAdminTOTP() {
  const code = $("#adminTotpCode").value.trim();
  const data = await api("security/totp/finish", { method: "POST", body: { code } });
  closeModal();
  showRecoveryCodes(data.codes || []);
  await loadBootstrap();
}

function disableAdminTOTP() {
  openModal({
    title: "Remove authenticator",
    subtitle: "This reduces the protection on your admin account. Confirm with your password and a current code.",
    body: `<form id="disableTotpForm"><label class="field"><span>Current password</span><input id="disableTotpPassword" type="password" autocomplete="current-password" required></label><label class="field"><span>Authenticator or recovery code</span><input id="disableTotpCode" autocomplete="one-time-code" required></label></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn danger" data-modal-submit="disableTotpForm">Remove authenticator</button>`
  });
}

async function submitDisableAdminTOTP() {
  await api("security/totp/disable", { method: "POST", body: { password: $("#disableTotpPassword").value, code: $("#disableTotpCode").value.trim() } });
  closeModal();
  toast("Authenticator removed from the admin console");
  await loadBootstrap();
}

function showRecoveryCodes(codes) {
  openModal({
    title: "Save recovery codes",
    subtitle: "Each code works once if you lose your authenticator. Store them somewhere safe.",
    body: `<div class="copy-box" style="flex-direction:column;align-items:stretch;gap:6px">${codes.map((code) => `<span>${esc(code)}</span>`).join("")}</div>`,
    footer: `<button class="btn secondary" data-copy-codes="${esc(codes.join("\n"))}">Copy all</button><button class="btn primary" data-modal-close>Done</button>`
  });
  $("[data-copy-codes]")?.addEventListener("click", async (event) => { await navigator.clipboard.writeText(event.currentTarget.dataset.copyCodes).catch(() => {}); toast("Recovery codes copied"); });
}

/* ------------------------------------------------------------
   Modal / drawer / dialogs
------------------------------------------------------------ */
function openModal({ title, subtitle = "", body = "", footer = "", wide = false, onMount = null }) {
  $("#modalRoot").innerHTML = `<div class="modal-backdrop" data-modal-backdrop><div class="modal ${wide ? "wide" : ""}" role="dialog" aria-modal="true" aria-label="${esc(title)}"><div class="modal-head"><div class="modal-title"><h2>${esc(title)}</h2>${subtitle ? `<p>${esc(subtitle)}</p>` : ""}</div><button class="icon-button modal-close" data-modal-close aria-label="Close">${icon("x")}</button></div><div class="modal-body">${body}</div>${footer ? `<div class="modal-foot">${footer}</div>` : ""}</div></div>`;
  $$("[data-modal-close]").forEach((button) => button.addEventListener("click", closeModal));
  $("[data-modal-backdrop]")?.addEventListener("click", (event) => { if (event.target === event.currentTarget) closeModal(); });
  if (onMount) onMount();
  setTimeout(() => ($(".modal-body input, .modal-body select, .modal-body textarea") || $("[data-modal-close]"))?.focus(), 60);
}

function closeModal() { $("#modalRoot").innerHTML = ""; }

function confirmDialog(title, message, confirm = "Continue", danger = false) {
  return new Promise((resolve) => {
    openModal({
      title,
      subtitle: message,
      body: "",
      footer: `<button class="btn secondary" data-confirm-cancel>Cancel</button><button class="btn ${danger ? "danger" : "primary"}" data-confirm-ok>${esc(confirm)}</button>`
    });
    $("[data-confirm-cancel]")?.addEventListener("click", () => { closeModal(); resolve(false); });
    $("[data-confirm-ok]")?.addEventListener("click", () => { closeModal(); resolve(true); });
  });
}

/* ------------------------------------------------------------
   Global events
------------------------------------------------------------ */
$("#themeToggle").addEventListener("click", () => applyTheme(state.theme === "dark" ? "light" : "dark"));
$("#menuButton").addEventListener("click", () => $("#sidebar").classList.toggle("open"));
$("#sidebarClose").addEventListener("click", () => $("#sidebar").classList.remove("open"));
$("#securityButton").addEventListener("click", openSecurityModal);
$("#profileMenuBtn").addEventListener("click", () => {
  openModal({
    title: state.admin?.name || "Administrator",
    subtitle: state.admin?.email || "",
    body: `<div class="detail-list"><div class="detail-row"><span>Base role</span><b>${esc(roleLabel(state.admin?.role))}</b></div><div class="detail-row"><span>Platform admin</span><b>${state.admin?.isSystemAdmin ? "Yes" : "No"}</b></div><div class="detail-row"><span>Permissions</span><b>${state.permissions.length}</b></div></div>`,
    footer: `<button class="btn secondary" data-modal-close>Close</button><button class="btn danger" data-action="logout">${icon("logout")}Sign out</button>`
  });
  $('[data-action="logout"]')?.addEventListener("click", logout);
});
$("#tenantPicker").addEventListener("click", openTenantSwitcher);
$("#tenantSwitchMini").addEventListener("click", openTenantSwitcher);

function openTenantSwitcher() {
  if (!state.tenants.length) return;
  openModal({
    title: "Switch organization",
    subtitle: state.admin?.isSystemAdmin ? "Choose the tenant context for administration." : "You can only administer your own organization.",
    body: `<div class="entity-grid" style="grid-template-columns:1fr">${state.tenants.map((tenant) => `<button class="entity-card" data-switch-tenant="${esc(tenant.id)}" style="text-align:left;cursor:pointer"><div class="entity-head"><span class="entity-icon">${icon("building")}</span><div class="entity-title"><b>${esc(tenant.name)}</b><small>${esc(tenant.slug)} · ${esc(tenant.plan)}</small></div>${badge(tenant.status)}</div></button>`).join("")}</div>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button>`
  });
  $$("[data-switch-tenant]").forEach((button) => button.addEventListener("click", async () => {
    closeModal();
    if (button.dataset.switchTenant === state.activeTenant.id) return;
    await switchTenant(button.dataset.switchTenant);
  }));
}

$("#globalSearch").addEventListener("focus", openCommandPalette);

function openCommandPalette() {
  $("#commandPalette").hidden = false;
  $("#commandPalette").innerHTML = `<div class="palette-box"><div class="palette-input">${icon("search")}<input id="paletteInput" placeholder="Jump to a section or search users…" autofocus></div><div class="palette-results" id="paletteResults">${paletteStatic()}</div></div>`;
  $("#commandPalette").addEventListener("click", (event) => { if (event.target === event.currentTarget) closeCommandPalette(); });
  $("#paletteInput").addEventListener("input", paletteSearch);
  $("#paletteInput").addEventListener("keydown", (event) => { if (event.key === "Escape") closeCommandPalette(); });
  setTimeout(() => $("#paletteInput")?.focus(), 20);
}

function closeCommandPalette() { $("#commandPalette").hidden = true; $("#commandPalette").innerHTML = ""; }

function paletteStatic() {
  const items = [["overview", "Overview", "grid"], ["users", "Users", "users"], ["groups", "Groups", "group"], ["team-inboxes", "Team inboxes", "users"], ["org-units", "Org units", "tree"], ["domains", "Domains", "globe"], ["roles", "Roles & access", "key"], ["policies", "Policies", "sliders"], ["addons", "Add-ons", "plug"], ["ai", "AI assistant", "sparkles"], ["quarantine", "Quarantine", "alert"], ["audit", "Audit log", "list"]];
  return items.map(([view, label, iconName]) => `<div class="palette-result" data-palette-nav="${view}">${icon(iconName)}<span>${label}</span></div>`).join("");
}

let paletteTimer;
function paletteSearch(event) {
  clearTimeout(paletteTimer);
  const query = event.target.value.trim();
  if (!query) { $("#paletteResults").innerHTML = paletteStatic(); bindPaletteResults(); return; }
  paletteTimer = setTimeout(async () => {
    try {
      const data = await api("users?q=" + encodeURIComponent(query) + "&limit=6");
      const users = data.users || [];
      $("#paletteResults").innerHTML = users.length ? users.map((user) => `<div class="palette-result" data-palette-user="${esc(user.id)}">${icon("users")}<span>${esc(user.name)} <small class="muted">${esc(user.email)}</small></span></div>`).join("") : `<div class="palette-empty">No matching users</div>`;
      bindPaletteResults();
    } catch {}
  }, 220);
}

function bindPaletteResults() {
  $$("[data-palette-nav]").forEach((item) => item.addEventListener("click", () => { closeCommandPalette(); location.hash = "#/" + item.dataset.paletteNav; }));
  $$("[data-palette-user]").forEach((item) => item.addEventListener("click", () => { closeCommandPalette(); location.hash = "#/users/" + encodeURIComponent(item.dataset.paletteUser); }));
}
bindPaletteResults();

document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); openCommandPalette(); }
  if (event.key === "Escape") { closeCommandPalette(); }
});

/* Delegated clicks */
document.addEventListener("click", async (event) => {
  const domainsLink = event.target.closest("[data-open-domains]");
  if (domainsLink) {
    event.preventDefault();
    closeModal();
    location.hash = "#/domains";
    return;
  }
  const actionEl = event.target.closest("[data-action]");
  if (actionEl) {
    const action = actionEl.dataset.action;
    if (action === "new-user") return openNewUserModal();
    if (action === "import-users") { location.hash = "#/users/import"; return; }
    if (action === "import-mailboxes") { location.hash = "#/mailboxes/import"; return; }
    if (action === "back-users") { location.hash = "#/users"; return; }
    if (action === "new-group") return openNewGroupModal();
    if (action === "new-team-inbox") return openNewTeamInboxModal();
    if (action === "new-org-unit") return orgUnitModal();
    if (action === "new-domain") return openNewDomainModal();
    if (action === "new-role") return roleModal();
    if (action === "new-tenant") return openNewTenantModal();
    if (action === "go-policies") { location.hash = "#/policies"; return; }
    if (action === "go-audit") { location.hash = "#/audit"; return; }
    if (action === "save-policies") return savePolicies();
    if (action === "save-ai") return saveAI();
    if (action === "test-ai") return saveAI({ test: true });
    if (action === "run-retention") return runRetention();
    if (action === "release-quarantine") return releaseQuarantine(actionEl);
    if (action === "reload-policies") return renderPolicies();
    if (action === "open-security") return openSecurityModal();
    if (action === "users-prev") { state.userFilter.page = Math.max(1, state.userFilter.page - 1); return loadUsers().catch(showError); }
    if (action === "users-next") { state.userFilter.page += 1; return loadUsers().catch(showError); }
    if (action === "bulk-activate") return bulkUsers("activate");
    if (action === "bulk-suspend") return bulkUsers("suspend");
    if (action === "bulk-delete") return bulkUsers("delete");
    if (action === "retry-view") return route();
  }
  const userActionEl = event.target.closest("[data-user-action]");
  if (userActionEl) {
    event.stopPropagation();
    return userAction(userActionEl.dataset.userAction, userActionEl.dataset.userId, userActionEl);
  }
  const row = event.target.closest("tr[data-user-id]");
  if (row && !event.target.closest("button, input, a")) { location.hash = "#/users/" + encodeURIComponent(row.dataset.userId); return; }
  const submit = event.target.closest("[data-modal-submit]");
  if (submit) {
    const formId = submit.dataset.modalSubmit;
    setBusy(submit, true, "Saving…");
    try {
      if (formId === "newUserForm") await submitUserForm(formId);
      else if (formId === "editUserForm") await submitUserForm(formId, state.drawerUser?.id);
      else if (formId === "newGroupForm") await submitGroupForm(formId);
      else if (formId === "editGroupForm") await submitGroupForm(formId, state.group?.id);
      else if (formId === "newTeamInboxForm") await submitTeamInboxForm(formId);
      else if (formId === "editTeamInboxForm") await submitTeamInboxForm(formId, state.editingTeamInbox?.id);
      else if (formId === "newOrgUnitForm") await submitOrgUnitForm(formId);
      else if (formId === "editOrgUnitForm") await submitOrgUnitForm(formId, state.orgUnits?.find((unit) => unit.id === state.editingOrgUnitId)?.id);
      else if (formId === "newDomainForm") await submitDomainForm();
      else if (formId === "newRoleForm") await submitRoleForm(formId);
      else if (formId === "editRoleForm") await submitRoleForm(formId, state.editingRoleId);
      else if (formId === "assignRoleForm") await submitAssignRoleForm(state.assignmentUser);
      else if (formId === "newScimTokenForm") await submitSCIMTokenForm();
      else if (formId === "newTenantForm") await submitTenantForm();
      else if (formId === "adminTotpForm") await submitAdminTOTP();
      else if (formId === "disableTotpForm") await submitDisableAdminTOTP();
      else if (formId === "adminPasswordForm") await submitAdminPassword();
      else if (formId === "holdForm") await submitHoldForm();
    } catch (error) { showError(error); setBusy(submit, false); }
  }
});

/* Bind edit actions that require contextual state */
document.addEventListener("click", (event) => {
  const orgEdit = event.target.closest("[data-org-edit]");
  if (orgEdit) { state.editingOrgUnitId = orgEdit.dataset.orgEdit; editOrgUnitModal((state.orgUnits || []).find((unit) => unit.id === orgEdit.dataset.orgEdit)); }
  const roleEdit = event.target.closest("[data-role-edit]");
  if (roleEdit) { state.editingRoleId = roleEdit.dataset.roleEdit; roleModal((state.roles || []).find((role) => role.id === roleEdit.dataset.roleEdit)); }
  const tenantAction = event.target.closest("[data-tenant-action]");
  if (tenantAction) { /* reserved */ }
});

/* ------------------------------------------------------------
   Storage policy (Cabinet quotas)
------------------------------------------------------------ */
const STORAGE_GB = 1024 * 1024 * 1024;

function quotaGB(value) {
  const bytes = Number(value) || 0;
  if (!bytes) return 0;
  return Math.round((bytes / STORAGE_GB) * 10) / 10;
}

function quotaBytes(value) {
  const gb = Number(value) || 0;
  if (gb <= 0) return 0;
  return Math.round(gb * STORAGE_GB);
}

function storageModeLabel(mode) {
  return mode === "split" ? "Separate Cabinet quota" : "Shares mailbox quota";
}

function storageQuotaSummary(entry) {
  const email = quotaGB(entry.emailQuotaBytes);
  const storage = quotaGB(entry.storageQuotaBytes);
  if (entry.mode === "split") {
    return `${email ? `${email} GB mail` : "User mailbox quota"} · ${storage ? `${storage} GB Cabinet` : "No Cabinet limit"}`;
  }
  return `${email ? `${email} GB combined` : "Each user's mailbox quota"} · Cabinet included`;
}

function storagePolicyFields(prefix, entry = {}, { showEmail = true } = {}) {
  const mode = entry.mode || "combined";
  return `<div class="field-row">
    <label class="field"><span>Quota mode</span><select data-policy-field="mode">
      <option value="combined" ${mode === "combined" ? "selected" : ""}>Combined with mailbox quota</option>
      <option value="split" ${mode === "split" ? "selected" : ""}>Separate Cabinet quota</option>
    </select><small class="muted">Combined is Gmail-style: Cabinet files count toward the mailbox allowance.</small></label>
    ${showEmail ? `<label class="field"><span>Mailbox allowance <small class="muted">GB</small></span><input type="number" min="0" step="0.5" data-policy-field="emailQuotaGB" value="${quotaGB(entry.emailQuotaBytes) || ""}" placeholder="Use user quota"><small class="muted">0 uses each user's existing mailbox quota. A non-zero value also updates Dovecot's quota for affected users.</small></label>` : ""}
    <label class="field"><span>Cabinet allowance <small class="muted">GB</small></span><input type="number" min="0" step="0.5" data-policy-field="storageQuotaGB" value="${quotaGB(entry.storageQuotaBytes) || ""}" placeholder="No separate limit" ${mode === "combined" ? "disabled" : ""}><small class="muted">Used only when the mode is “Separate”.</small></label>
  </div>`;
}

async function renderStoragePolicy() {
  loading("Storage policy");
  try {
    const data = await api("storage/policies");
    const tenant = data.tenant || { mode: "combined" };
    const manage = can("policies.manage");
    const domainOverrides = data.domains || [];
    const userOverrides = data.users || [];
    const domains = data.availableDomains || [];
    const domainRows = domains.map((domain) => {
      const override = domainOverrides.find((entry) => entry.domain && entry.domain.toLowerCase() === domain.toLowerCase());
      return `<tr>
        <td><b>${esc(domain)}</b></td>
        <td>${override ? `<span class="badge ${override.mode === "split" ? "warning" : "neutral"}">${esc(storageModeLabel(override.mode))}</span>` : '<span class="badge neutral">Inherits organization default</span>'}</td>
        <td class="muted">${override ? esc(storageQuotaSummary(override)) : esc(storageQuotaSummary(tenant))}</td>
        <td class="row-actions">${override && manage ? `<button class="btn secondary small" data-domain-edit="${esc(domain)}" data-domain-mode="${esc(override.mode)}" data-domain-email="${quotaGB(override.emailQuotaBytes)}" data-domain-storage="${quotaGB(override.storageQuotaBytes)}">Edit</button><button class="icon-button" data-domain-remove="${esc(domain)}" title="Remove override" aria-label="Remove ${esc(domain)} override">${icon("trash")}</button>` : manage ? `<button class="btn secondary small" data-domain-edit="${esc(domain)}">Override</button>` : ""}</td>
      </tr>`;
    }).join("") || `<tr><td colspan="4"><div class="empty">${icon("globe")}<h3>No domains yet</h3><p>Add a mail domain before setting a domain-level storage policy.</p></div></td></tr>`;
    $("#viewRoot").innerHTML = `<div class="view-head"><div><h1>Storage policy</h1><p>Control how Cabinet files count against mail storage for every user in ${esc(state.activeTenant?.name || "your organization")}.</p></div><div class="view-actions"><span class="badge ${tenant.mode === "split" ? "warning" : "system"}">${esc(storageModeLabel(tenant.mode))}</span></div></div>
      <div class="storage-policy-grid">
        <div class="card"><div class="card-head"><div><h2>Organization default</h2><p>Applies to every user without a domain or personal override.</p></div></div><div class="card-pad">
          <form id="tenantStorageForm">${storagePolicyFields("tenant", tenant)}<div class="storage-policy-actions"><span class="muted" style="font-size:10.5px">Changes are enforced on the next Cabinet upload.</span><button class="btn primary" type="submit" ${manage ? "" : "disabled"}>${icon("check")}Save default</button></div></form>
        </div></div>
        <div class="card storage-policy-explainer"><div class="card-head"><div><h2>How quotas resolve</h2><p>Most specific policy wins.</p></div></div><div class="card-pad">
          <ol class="storage-policy-steps"><li><b>User override</b><span>A single person's Cabinet allowance.</span></li><li><b>Domain override</b><span>Everyone on a mail domain.</span></li><li><b>Organization default</b><span>The fallback for all other users.</span></li></ol>
          <div class="storage-policy-note">${icon("shield")}<span>Combined mode gives each user one Gmail-style allowance shared by mail and Cabinet. Split mode gives Cabinet a separate ceiling while mail keeps its mailbox quota.</span></div>
        </div></div>
      </div>
      <div class="card" style="margin-top:14px"><div class="card-head"><div><h2>Domain overrides</h2><p>Apply one storage rule to every user on a domain.</p></div><span class="badge neutral">${domainOverrides.length} override${domainOverrides.length === 1 ? "" : "s"}</span></div><div class="card-pad">
        ${manage ? `<form id="domainStorageForm" class="storage-policy-form"><label class="field"><span>Domain</span><select data-policy-field="domain" required><option value="">Choose a domain</option>${domains.map((domain) => `<option value="${esc(domain)}">${esc(domain)}</option>`).join("")}</select></label>${storagePolicyFields("domain", { mode: "combined" })}<div class="storage-policy-actions"><span class="muted" style="font-size:10.5px">Saving replaces an existing override.</span><button class="btn primary" type="submit">${icon("check")}Save domain policy</button></div></form>` : ""}
        <div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>Domain</th><th>Mode</th><th>Allowances</th><th></th></tr></thead><tbody>${domainRows}</tbody></table></div></div>
      </div></div>
      <div class="card" style="margin-top:14px"><div class="card-head"><div><h2>User overrides</h2><p>Personal rules always win over the domain and organization policy.</p></div><span class="badge neutral">${userOverrides.length} override${userOverrides.length === 1 ? "" : "s"}</span></div><div class="card-pad">
        ${manage ? `<form id="userStorageForm" class="storage-policy-form"><label class="field"><span>User email</span><input type="email" data-policy-field="email" placeholder="person@company.com" required><small class="muted">The user must already exist in this organization.</small></label>${storagePolicyFields("user", { mode: "combined" })}<div class="storage-policy-actions"><span class="muted" style="font-size:10.5px">Cabinet files count separately from mail attachments.</span><button class="btn primary" type="submit">${icon("check")}Save user policy</button></div></form>` : ""}
        <div class="table-wrap"><div class="table-scroll"><table class="data"><thead><tr><th>User</th><th>Mode</th><th>Allowances</th><th></th></tr></thead><tbody>${userOverrides.length ? userOverrides.map((entry) => `<tr><td><b>${esc(entry.name || entry.email)}</b><br><small class="muted">${esc(entry.email)}</small></td><td><span class="badge ${entry.mode === "split" ? "warning" : "neutral"}">${esc(storageModeLabel(entry.mode))}</span></td><td class="muted">${esc(storageQuotaSummary(entry))}</td><td class="row-actions">${manage ? `<button class="btn secondary small" data-user-edit="${esc(entry.email)}" data-user-mode="${esc(entry.mode)}" data-user-email-quota="${quotaGB(entry.emailQuotaBytes)}" data-user-storage="${quotaGB(entry.storageQuotaBytes)}">Edit</button><button class="icon-button" data-user-policy-remove="${esc(entry.userId)}" title="Remove override" aria-label="Remove override">${icon("trash")}</button>` : ""}</td></tr>`).join("") : `<tr><td colspan="4"><div class="empty">${icon("users")}<h3>No personal overrides</h3><p>Every user follows their domain or organization policy.</p></div></td></tr>`}</tbody></table></div></div>
      </div></div>`;

    const bindMode = (form) => {
      const select = form?.querySelector('[data-policy-field="mode"]');
      const storage = form?.querySelector('[data-policy-field="storageQuotaGB"]');
      if (select && storage) {
        select.addEventListener("change", () => { storage.disabled = select.value !== "split"; if (select.value === "split" && !storage.value) storage.value = "25"; });
      }
    };
    const tenantForm = $("#tenantStorageForm");
    bindMode(tenantForm);
    tenantForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.target.querySelector("button[type=submit]");
      setBusy(button, true, "Saving…");
      try {
        await api("storage/policies/tenant", { method: "PUT", body: readStoragePolicy(event.target) });
        toast("Organization storage policy saved");
        renderStoragePolicy();
      } catch (error) { showError(error); setBusy(button, false); }
    });
    const domainForm = $("#domainStorageForm");
    bindMode(domainForm);
    domainForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.target.querySelector("button[type=submit]");
      setBusy(button, true, "Saving…");
      try {
        await api("storage/policies/domain", { method: "PUT", body: { ...readStoragePolicy(event.target), domain: event.target.querySelector('[data-policy-field="domain"]').value } });
        toast("Domain storage policy saved");
        renderStoragePolicy();
      } catch (error) { showError(error); setBusy(button, false); }
    });
    const userForm = $("#userStorageForm");
    bindMode(userForm);
    userForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.target.querySelector("button[type=submit]");
      setBusy(button, true, "Saving…");
      try {
        await api("storage/policies/user", { method: "PUT", body: { ...readStoragePolicy(event.target), email: event.target.querySelector('[data-policy-field="email"]').value.trim() } });
        toast("User storage policy saved");
        renderStoragePolicy();
      } catch (error) { showError(error); setBusy(button, false); }
    });
    $$("[data-domain-remove]").forEach((button) => button.addEventListener("click", async () => {
      if (!(await confirmDialog("Remove domain override", `${button.dataset.domainRemove} will fall back to the organization storage policy.`, "Remove override", true))) return;
      try { await api(`storage/policies/domain/${encodeURIComponent(button.dataset.domainRemove)}`, { method: "DELETE" }); toast("Domain override removed"); renderStoragePolicy(); } catch (error) { showError(error); }
    }));
    $$("[data-user-policy-remove]").forEach((button) => button.addEventListener("click", async () => {
      if (!(await confirmDialog("Remove user override", "This user will fall back to their domain or organization storage policy.", "Remove override", true))) return;
      try { await api(`storage/policies/user/${encodeURIComponent(button.dataset.userPolicyRemove)}`, { method: "DELETE" }); toast("User override removed"); renderStoragePolicy(); } catch (error) { showError(error); }
    }));
    $$("[data-domain-edit]").forEach((button) => button.addEventListener("click", () => {
      const form = $("#domainStorageForm");
      const select = form?.querySelector('[data-policy-field="domain"]');
      if (!form || !select) return;
      select.value = button.dataset.domainEdit;
      const mode = form.querySelector('[data-policy-field="mode"]');
      const emailQuota = form.querySelector('[data-policy-field="emailQuotaGB"]');
      const storageQuota = form.querySelector('[data-policy-field="storageQuotaGB"]');
      if (mode) mode.value = button.dataset.domainMode || "combined";
      if (emailQuota) emailQuota.value = button.dataset.domainEmail || "";
      if (storageQuota) {
        storageQuota.value = button.dataset.domainStorage || "";
        storageQuota.disabled = (mode?.value || "combined") !== "split";
      }
      select.scrollIntoView({ behavior: "smooth", block: "center" });
    }));
    $$("[data-user-edit]").forEach((button) => button.addEventListener("click", () => {
      const form = $("#userStorageForm");
      const email = form?.querySelector('[data-policy-field="email"]');
      if (!form || !email) return;
      email.value = button.dataset.userEdit;
      const mode = form.querySelector('[data-policy-field="mode"]');
      const emailQuota = form.querySelector('[data-policy-field="emailQuotaGB"]');
      const storageQuota = form.querySelector('[data-policy-field="storageQuotaGB"]');
      if (mode) mode.value = button.dataset.userMode || "combined";
      if (emailQuota) emailQuota.value = button.dataset.userEmailQuota || "";
      if (storageQuota) {
        storageQuota.value = button.dataset.userStorage || "";
        storageQuota.disabled = (mode?.value || "combined") !== "split";
      }
      email.scrollIntoView({ behavior: "smooth", block: "center" });
    }));
  } catch (error) { viewError(error); }
}

function readStoragePolicy(form) {
  const field = (name) => form.querySelector(`[data-policy-field="${name}"]`);
  return {
    mode: field("mode")?.value || "combined",
    emailQuotaBytes: quotaBytes(field("emailQuotaGB")?.value),
    storageQuotaBytes: quotaBytes(field("storageQuotaGB")?.value)
  };
}

/* ------------------------------------------------------------
   Admin self password change
------------------------------------------------------------ */
function openChangePasswordModal() {
  openModal({
    title: "Change password",
    subtitle: "Your admin password also protects your mailbox. Other admin and webmail sessions will be signed out.",
    body: `<form id="adminPasswordForm">
      <label class="field"><span>Current password</span><input id="adminCurrentPassword" type="password" autocomplete="current-password" required></label>
      <label class="field" style="margin-top:10px"><span>New password</span><input id="adminNewPassword" type="password" autocomplete="new-password" required><small class="muted">Must satisfy the organization password policy.</small></label>
      <label class="field" style="margin-top:10px"><span>Confirm new password</span><input id="adminConfirmPassword" type="password" autocomplete="new-password" required></label>
    </form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" data-modal-submit="adminPasswordForm">${icon("key")}Change password</button>`
  });
}

async function submitAdminPassword() {
  const currentPassword = $("#adminCurrentPassword").value;
  const newPassword = $("#adminNewPassword").value;
  const confirmPassword = $("#adminConfirmPassword").value;
  if (newPassword.length < 8) throw new Error("Use a password of at least 8 characters");
  if (newPassword !== confirmPassword) throw new Error("The new passwords do not match");
  const result = await api("account/password", { method: "POST", body: { currentPassword, newPassword } });
  closeModal();
  toast(`Password changed${result.revokedAdminSessions ? ` · ${result.revokedAdminSessions} other session(s) signed out` : ""}`);
  await loadBootstrap();
}

/* Boot */
(async function boot() {
  updateChrome();
  if (new URLSearchParams(location.search).get("signedout") === "1") {
    history.replaceState(null, "", location.pathname);
    showLogin();
    return;
  }
  try {
    const session = await api("session");
    state.csrf = session.csrf || "";
    await finishLogin();
  } catch {
    showLogin();
  }
})();
