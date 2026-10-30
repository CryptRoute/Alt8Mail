"use strict";

/* Dedicated user page: profile, aliases, signatures, vacation responder and
   delegated mailbox access. Replaces the old drawer with a full workspace. */
let userPageState = null;

async function renderUserPage(id) {
  loading("User");
  try {
    const [detail, signatures, vacation, delegates, candidates] = await Promise.all([
      api("users/" + encodeURIComponent(id)),
      api("users/" + encodeURIComponent(id) + "/signatures"),
      api("users/" + encodeURIComponent(id) + "/vacation"),
      api("users/" + encodeURIComponent(id) + "/delegates"),
      loadUserPageCandidates()
    ]);
    const user = detail.user;
    userPageState = {
      user,
      signatures: signatures.signatures || [],
      signatureDefaults: signatures.defaults || {},
      identities: [],
      vacation: vacation.vacation || {},
      delegates: delegates.delegates || [],
      delegations: delegates.delegations || [],
      candidates
    };
    try {
      const identityData = await api("users/" + encodeURIComponent(id) + "/identity-signatures");
      userPageState.identities = identityData.identities || [];
    } catch {}
    renderUserPageShell();
  } catch (error) {
    viewError(error);
  }
}

async function loadUserPageCandidates() {
  if (Array.isArray(state.userCandidates)) return state.userCandidates;
  try {
    const data = await api("users?limit=200&status=active");
    state.userCandidates = (data.users || []).filter((user) => !user.deletedAt);
  } catch {
    state.userCandidates = [];
  }
  return state.userCandidates;
}

function renderUserPageShell() {
  const data = userPageState;
  if (!data) return;
  const user = data.user;
  const status = user.deletedAt ? "deleted" : user.status;
  $("#viewRoot").innerHTML = `
    <div class="view-head user-page-head">
      <div class="user-page-identity">
        <button class="icon-button user-page-back" data-action="back-users" aria-label="Back to users"><span aria-hidden="true">←</span></button>
        <span class="avatar user-page-avatar" style="background:${esc(user.color || "#34557a")}">${esc(user.initials || initials(user.name))}</span>
        <div class="user-page-title">
          <div class="user-page-eyebrow">User account</div>
          <h1>${esc(user.name)}</h1>
          <p>${esc(user.email)} · ${esc(roleLabel(user.role))}</p>
          <div class="user-page-badges">${badge(status)}${user.mfaEnabled ? `<span class="badge system">${icon("shield")} MFA</span>` : `<span class="badge neutral">MFA not enrolled</span>`}${user.isSystemAdmin ? `<span class="badge system">System administrator</span>` : ""}${user.forcePasswordChange ? `<span class="badge pending">Password change required</span>` : ""}</div>
        </div>
      </div>
      <div class="view-actions">
        ${can("users.update") && status !== "deleted" ? `<button class="btn secondary" data-user-page-action="edit">${icon("sliders")}Edit profile</button>` : ""}
        ${can("users.reset_password") ? `<button class="btn secondary" data-user-page-action="reset-password">${icon("key")}Reset password</button>` : ""}
        ${can("users.update") && status !== "deleted" ? `<button class="btn ${status === "active" ? "secondary" : "primary"}" data-user-page-action="${status === "active" ? "suspend" : "activate"}">${icon(status === "active" ? "pause" : "play")}${status === "active" ? "Suspend" : "Activate"}</button>` : ""}
        ${can("users.delete") && status !== "deleted" ? `<button class="btn danger" data-user-page-action="delete">${icon("trash")}Close account</button>` : ""}
        ${can("users.delete") && status === "deleted" ? `<button class="btn primary" data-user-page-action="restore">${icon("refresh")}Restore account</button>` : ""}
      </div>
    </div>
    <div class="user-page-grid">
      <aside class="card user-page-summary">
        <div class="user-page-summary-head">${icon("mail")}<div><b>${esc(user.email)}</b><small>Primary mailbox</small></div></div>
        <div class="detail-list">
          <div class="detail-row"><span>Role</span><b>${esc(roleLabel(user.role))}</b></div>
          <div class="detail-row"><span>Status</span><b>${badge(status)}</b></div>
          <div class="detail-row"><span>Org unit</span><b>${esc(user.orgUnitName || "Unassigned")}</b></div>
          <div class="detail-row"><span>Department</span><b>${esc(user.department || "—")}</b></div>
          <div class="detail-row"><span>Storage</span><b>${esc(formatBytes(user.usedBytes))} / ${esc(formatBytes(user.quotaBytes))}</b></div>
          <div class="detail-row"><span>Mailbox</span><b>${user.mailboxStatus === "ready" ? "Provisioned" : user.mailboxStatus === "error" ? "Error" : "Provisioning"}</b></div>
          <div class="detail-row"><span>Last sign-in</span><b>${user.lastLoginAt ? relativeTime(user.lastLoginAt) : "Never"}</b></div>
          <div class="detail-row"><span>Created</span><b>${formatDate(user.createdAt)}</b></div>
        </div>
        <div class="user-page-summary-foot">
          <span>${(data.delegates || []).length} delegate${(data.delegates || []).length === 1 ? "" : "s"} · ${(data.signatures || []).length} signature${(data.signatures || []).length === 1 ? "" : "s"}</span>
          <span class="badge ${data.vacation?.enabled ? "active" : "neutral"}">${data.vacation?.enabled ? "Away" : "Available"}</span>
        </div>
      </aside>
      <section class="card user-page-main">
        <div class="tabs user-page-tabs" role="tablist">
          <button class="active" data-user-page-tab="overview" role="tab">Overview</button>
          <button data-user-page-tab="mailbox" role="tab">Mailbox</button>
          <button data-user-page-tab="delegates" role="tab">Delegates</button>
          <button data-user-page-tab="security" role="tab">Security</button>
          <button data-user-page-tab="activity" role="tab">Activity</button>
        </div>
        <div class="user-page-panels">
          <section class="tab-panel active" data-user-page-panel="overview">${userPageOverview(user)}</section>
          <section class="tab-panel" data-user-page-panel="mailbox">${userPageMailbox(data)}</section>
          <section class="tab-panel" data-user-page-panel="delegates">${userPageDelegates(data)}</section>
          <section class="tab-panel" data-user-page-panel="security">${userPageSecurity(user)}</section>
          <section class="tab-panel" data-user-page-panel="activity"><div id="userPageSessions"><div class="empty-mini">Loading sessions…</div></div></section>
        </div>
      </section>
    </div>`;
  bindUserPage();
}

function userPageOverview(user) {
  return `<div class="mini-section" style="margin-top:0"><h3>Profile</h3><div class="detail-list">
      <div class="detail-row"><span>Full name</span><b>${esc(user.name)}</b></div>
      <div class="detail-row"><span>Title</span><b>${esc(user.title || "—")}</b></div>
      <div class="detail-row"><span>Employee ID</span><b>${esc(user.employeeId || "—")}</b></div>
      <div class="detail-row"><span>Manager</span><b>${esc(user.managerEmail || "—")}</b></div>
      <div class="detail-row"><span>Mailbox path</span><b class="mono-cell">${esc(user.maildirPath || "—")}</b></div>
      <div class="detail-row"><span>Password expires</span><b>${user.passwordExpiresAt ? formatDate(user.passwordExpiresAt) : "Never"}</b></div>
    </div></div>
    <div class="mini-section"><h3>Organization roles</h3>${user.isSystemAdmin ? `<div class="detail-row"><span>Platform</span><b>System administrator</b></div>` : ""}${(user.adminRoles || []).length ? (user.adminRoles || []).map((role) => `<div class="detail-row"><span>${esc(role.name)}</span><b>${(role.permissions || []).length} permissions</b></div>`).join("") : `<div class="empty-mini">No custom admin roles assigned.</div>`}${can("roles.manage") ? `<button class="btn secondary small" data-user-page-action="assign-role" style="margin-top:10px">${icon("plus")}Assign a role</button>` : ""}</div>
    <div class="mini-section"><h3>Groups</h3>${(user.groups || []).length ? (user.groups || []).map((group) => `<div class="detail-row"><span>${esc(group.name)}</span><b>${esc(group.memberRole)}</b></div>`).join("") : `<div class="empty-mini">Not a member of any group.</div>`}</div>`;
}

function userPageMailbox(data) {
  const user = data.user;
  return `<div class="mini-section" style="margin-top:0">
      <div class="mini-section-head"><h3>Email aliases</h3><span class="badge neutral">${(user.aliases || []).length}</span></div>
      <p class="mini-help">Secondary addresses that deliver to this mailbox. Aliases can send from compose when allowed.</p>
      <div class="user-alias-list">${(user.aliases || []).map((alias) => `<div class="user-alias-row"><div><b>${esc(alias.email)}</b><small>${alias.kind === "primary" ? "Primary address" : alias.canSend ? "Can send and receive" : "Receive only"}</small></div><div class="row-actions">${alias.kind === "primary" ? `<span class="badge neutral">Primary</span>` : can("users.aliases") ? `<button class="icon-button" data-alias-edit="${esc(alias.id)}" aria-label="Edit alias">${icon("sliders")}</button><button class="icon-button" data-alias-delete="${esc(alias.id)}" aria-label="Remove alias">${icon("trash")}</button>` : ""}</div></div>`).join("") || `<div class="empty-mini">No aliases yet.</div>`}</div>
      ${can("users.aliases") ? `<form id="userPageAliasForm" class="inline-form" style="margin-top:12px"><label class="field grow"><span>New alias</span><input id="userPageAliasEmail" type="email" placeholder="sales@${esc((user.email || "").split("@")[1] || "company.com")}" required></label><label class="field"><span>Label</span><input id="userPageAliasLabel" placeholder="Sales"></label><button class="btn primary" type="submit">${icon("plus")}Add alias</button></form>` : ""}
    </div>
    <div class="mini-section">
      <div class="mini-section-head"><h3>Signature library</h3><button class="btn secondary small" data-signature-new>${icon("plus")}New signature</button></div>
      <p class="mini-help">Named signatures with optional New, Reply and Forward defaults. HTML is sanitized before it is stored.</p>
      <div class="signature-grid">${(data.signatures || []).map((signature) => userSignatureCard(signature)).join("") || `<div class="empty-mini">No signatures yet.</div>`}</div>
    </div>
    <div class="mini-section">
      <div class="mini-section-head"><h3>Per-address signatures</h3></div>
      <p class="mini-help">Each sending address can carry its own signature. Empty means the named defaults apply.</p>
      <div class="user-alias-list">${(data.identities || []).map((identity) => `<div class="user-alias-row"><div><b>${esc(identity.email)}</b><small>${identity.bodyText ? esc(identity.bodyText.slice(0, 110)) : "No dedicated signature"}</small></div><div class="row-actions"><button class="btn small secondary" data-identity-signature="${esc(identity.email)}">Edit</button></div></div>`).join("") || `<div class="empty-mini">No sending addresses.</div>`}</div>
    </div>
    <div class="mini-section">
      <div class="mini-section-head"><h3>Vacation responder</h3><span class="badge ${data.vacation?.enabled ? "active" : "neutral"}">${data.vacation?.enabled ? "Enabled" : "Off"}</span></div>
      <p class="mini-help">Dovecot sends the automatic reply on the scheduled dates. Internal and external replies can say different things.</p>
      <form id="userPageVacationForm" class="vacation-form">
        <label class="switch-row"><input type="checkbox" id="vacationEnabled" ${data.vacation?.enabled ? "checked" : ""}><span><b>Enable vacation responder</b><small>Pause automatic replies any time by switching this off.</small></span></label>
        <div class="field-row"><label class="field"><span>Start date</span><input type="date" id="vacationStart" value="${esc(data.vacation?.start || "")}"></label><label class="field"><span>End date</span><input type="date" id="vacationEnd" value="${esc(data.vacation?.end || "")}"></label><label class="field"><span>Reply interval (days)</span><input type="number" id="vacationDays" min="1" max="90" value="${Number(data.vacation?.days) || 7}"></label></div>
        <div class="vacation-columns">
          <div class="vacation-block"><h4>Internal replies</h4><label class="field"><span>Subject</span><input id="vacationInternalSubject" value="${esc(data.vacation?.internal?.subject || "Out of office")}"></label><label class="field"><span>Message</span><textarea id="vacationInternalMessage" rows="4">${esc(data.vacation?.internal?.message || "")}</textarea></label></div>
          <div class="vacation-block"><h4>External replies <label class="switch-inline"><input type="checkbox" id="vacationExternalEnabled" ${data.vacation?.external?.enabled ? "checked" : ""}><span>Send</span></label></h4><label class="field"><span>Subject</span><input id="vacationExternalSubject" value="${esc(data.vacation?.external?.subject || "Away from the office")}"></label><label class="field"><span>Message</span><textarea id="vacationExternalMessage" rows="4">${esc(data.vacation?.external?.message || "")}</textarea></label></div>
        </div>
        <div class="form-actions"><button class="btn primary" type="submit">${icon("check")}Save vacation responder</button></div>
      </form>
    </div>`;
}

function userSignatureCard(signature) {
  const badges = [
    signature.defaultNew ? "New" : "",
    signature.defaultReply ? "Reply" : "",
    signature.defaultForward ? "Forward" : ""
  ].filter(Boolean);
  return `<article class="signature-card"><div class="signature-card-head"><div><b>${esc(signature.name)}</b><small>${badges.length ? "Default for " + badges.join(", ") : "No default selected"}</small></div><div class="row-actions"><button class="icon-button" data-signature-edit="${esc(signature.id)}" aria-label="Edit signature">${icon("sliders")}</button><button class="icon-button" data-signature-delete="${esc(signature.id)}" aria-label="Delete signature">${icon("trash")}</button></div></div><pre class="signature-preview">${esc(signature.bodyText || signature.bodyHtml || "")}</pre></article>`;
}

function userPageDelegates(data) {
  const user = data.user;
  const candidates = (data.candidates || []).filter((candidate) => candidate.id !== user.id && !(data.delegates || []).some((delegate) => delegate.userId === candidate.id));
  return `<div class="mini-section" style="margin-top:0">
      <div class="mini-section-head"><h3>People who can access this mailbox</h3><span class="badge neutral">${(data.delegates || []).length}</span></div>
      <p class="mini-help">Delegates see this mailbox's Inbox in their webmail folder rail. Read access lets them read; send access lets them reply from their own account.</p>
      <div class="delegate-list">${(data.delegates || []).map((delegate) => `<div class="delegate-card"><span class="avatar" style="background:${esc(delegate.color || "#34557a")}">${esc(delegate.initials || initials(delegate.name))}</span><div class="delegate-copy"><b>${esc(delegate.name)}</b><small>${esc(delegate.email)}</small><div class="delegate-perms"><label class="switch-inline"><input type="checkbox" data-delegate-read="${esc(delegate.userId)}" ${delegate.canRead ? "checked" : ""}><span>Read</span></label><label class="switch-inline"><input type="checkbox" data-delegate-send="${esc(delegate.userId)}" ${delegate.canSend ? "checked" : ""}><span>Reply</span></label></div></div><button class="icon-button" data-delegate-remove="${esc(delegate.userId)}" aria-label="Remove delegate">${icon("trash")}</button></div>`).join("") || `<div class="empty-mini">No one else can open this mailbox.</div>`}</div>
      ${can("users.update") && candidates.length ? `<form id="userPageDelegateForm" class="inline-form" style="margin-top:12px"><label class="field grow"><span>Add a delegate</span><select id="delegateUserId">${candidates.map((candidate) => `<option value="${esc(candidate.id)}">${esc(candidate.name)} · ${esc(candidate.email)}</option>`).join("")}</select></label><label class="switch-inline"><input type="checkbox" id="delegateCanRead" checked><span>Read</span></label><label class="switch-inline"><input type="checkbox" id="delegateCanSend"><span>Reply</span></label><button class="btn primary" type="submit">${icon("plus")}Grant access</button></form>` : ""}
    </div>
    <div class="mini-section">
      <div class="mini-section-head"><h3>Mailboxes ${esc(user.name)} can access</h3></div>
      <div class="delegate-list">${(data.delegations || []).map((delegation) => `<div class="delegate-card"><span class="avatar" style="background:${esc(delegation.color || "#34557a")}">${esc(delegation.initials || initials(delegation.name))}</span><div class="delegate-copy"><b>${esc(delegation.name)}</b><small>${esc(delegation.email)} · ${delegation.canSend ? "read and reply" : "read only"}</small></div><a class="btn small secondary" href="#/users/${encodeURIComponent(delegation.userId)}">Open</a></div>`).join("") || `<div class="empty-mini">This user cannot access another mailbox.</div>`}</div>
    </div>`;
}

function userPageSecurity(user) {
  const security = user.security || {};
  const summary = security.summary || {};
  return `<div class="detail-list">
      <div class="detail-row"><span>Authenticator</span><b>${summary.twoFactor?.totpEnabled ? "Enabled" : "Not enrolled"}</b></div>
      <div class="detail-row"><span>Passkeys</span><b>${summary.twoFactor?.passkeys?.length || 0}</b></div>
      <div class="detail-row"><span>Recovery codes remaining</span><b>${summary.twoFactor?.recoveryCodesRemaining || 0}</b></div>
      <div class="detail-row"><span>Active web sessions</span><b>${security.activeSessions || 0}</b></div>
      <div class="detail-row"><span>Trusted devices</span><b>${security.trustedDevices || 0}</b></div>
    </div>
    <div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:16px">
      ${can("users.security") ? `<button class="btn secondary" data-user-page-action="reset-mfa">${icon("shield")}Reset MFA</button>` : ""}
      ${can("sessions.manage") ? `<button class="btn secondary" data-user-page-action="revoke-sessions">${icon("devices")}Revoke sessions</button>` : ""}
    </div>`;
}

function bindUserPage() {
  const user = userPageState?.user;
  if (!user) return;
  $$("[data-user-page-tab]").forEach((button) => button.addEventListener("click", () => {
    $$("[data-user-page-tab]").forEach((tab) => tab.classList.toggle("active", tab === button));
    $$("[data-user-page-panel]").forEach((panel) => panel.classList.toggle("active", panel.dataset.userPagePanel === button.dataset.userPageTab));
    if (button.dataset.userPageTab === "activity") loadUserPageSessions(user.id);
  }));
  $$("[data-user-page-action]").forEach((button) => button.addEventListener("click", () => userPageAction(button.dataset.userPageAction)));
  $("#userPageAliasForm")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const email = $("#userPageAliasEmail").value.trim();
    const label = $("#userPageAliasLabel").value.trim();
    if (!email) return;
    try {
      await api(`users/${encodeURIComponent(user.id)}/aliases`, { method: "POST", body: { email, label } });
      toast("Alias added");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  });
  $$("[data-alias-delete]").forEach((button) => button.addEventListener("click", async () => {
    if (!(await confirmDialog("Remove alias", "This address will stop routing to the user.", "Remove alias"))) return;
    try {
      await api(`users/${encodeURIComponent(user.id)}/aliases/${encodeURIComponent(button.dataset.aliasDelete)}`, { method: "DELETE" });
      toast("Alias removed");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  }));
  $$("[data-alias-edit]").forEach((button) => button.addEventListener("click", () => {
    const alias = (userPageState.user.aliases || []).find((item) => item.id === button.dataset.aliasEdit);
    if (alias) openAliasEditor(user.id, alias);
  }));
  $$("[data-signature-new]").forEach((button) => button.addEventListener("click", () => openSignatureEditor(user.id, null)));
  $$("[data-signature-edit]").forEach((button) => button.addEventListener("click", () => openSignatureEditor(user.id, (userPageState.signatures || []).find((item) => item.id === button.dataset.signatureEdit))));
  $$("[data-signature-delete]").forEach((button) => button.addEventListener("click", async () => {
    if (!(await confirmDialog("Delete signature", "This signature will no longer be available in compose.", "Delete signature"))) return;
    try {
      await api(`users/${encodeURIComponent(user.id)}/signatures/${encodeURIComponent(button.dataset.signatureDelete)}`, { method: "DELETE" });
      toast("Signature deleted");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  }));
  $$("[data-identity-signature]").forEach((button) => button.addEventListener("click", () => openIdentitySignatureEditor(user.id, button.dataset.identitySignature)));
  $("#userPageVacationForm")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.target.querySelector("button[type=submit]");
    setBusy(button, true, "Saving…");
    try {
      await api(`users/${encodeURIComponent(user.id)}/vacation`, {
        method: "PUT",
        body: {
          enabled: $("#vacationEnabled").checked,
          start: $("#vacationStart").value,
          end: $("#vacationEnd").value,
          days: Number($("#vacationDays").value) || 7,
          internal: { subject: $("#vacationInternalSubject").value, message: $("#vacationInternalMessage").value },
          external: { enabled: $("#vacationExternalEnabled").checked, subject: $("#vacationExternalSubject").value, message: $("#vacationExternalMessage").value }
        }
      });
      toast("Vacation responder saved");
      await renderUserPage(user.id);
    } catch (error) { showError(error); setBusy(button, false); }
  });
  $("#userPageDelegateForm")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    try {
      await api(`users/${encodeURIComponent(user.id)}/delegates`, {
        method: "POST",
        body: { userId: $("#delegateUserId").value, canRead: $("#delegateCanRead").checked, canSend: $("#delegateCanSend").checked }
      });
      toast("Mailbox access granted");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  });
  $$("[data-delegate-remove]").forEach((button) => button.addEventListener("click", async () => {
    if (!(await confirmDialog("Remove delegate", "This person will immediately lose access to the mailbox.", "Remove access"))) return;
    try {
      await api(`users/${encodeURIComponent(user.id)}/delegates/${encodeURIComponent(button.dataset.delegateRemove)}`, { method: "DELETE" });
      toast("Mailbox access removed");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  }));
  $$("[data-delegate-read], [data-delegate-send]").forEach((checkbox) => checkbox.addEventListener("change", async () => {
    const delegateId = checkbox.dataset.delegateRead || checkbox.dataset.delegateSend;
    const delegate = (userPageState.delegates || []).find((item) => item.userId === delegateId);
    try {
      await api(`users/${encodeURIComponent(user.id)}/delegates/${encodeURIComponent(delegateId)}`, {
        method: "PATCH",
        body: {
          canRead: delegateId === checkbox.dataset.delegateRead ? checkbox.checked : Boolean(delegate?.canRead),
          canSend: delegateId === checkbox.dataset.delegateSend ? checkbox.checked : Boolean(delegate?.canSend)
        }
      });
      toast("Delegate permissions updated");
      await renderUserPage(user.id);
    } catch (error) { showError(error); }
  }));
}

async function loadUserPageSessions(userId) {
  try {
    const data = await api(`users/${encodeURIComponent(userId)}/sessions`);
    const target = $("#userPageSessions");
    if (!target) return;
    target.innerHTML = (data.sessions || []).length ? (data.sessions || []).map((session) => `<div class="detail-row"><span>${esc(session.device)}<br><small class="muted">${esc(session.ip || "Unknown IP")}</small></span><b>${relativeTime(session.lastSeenAt)}</b></div>`).join("") : `<div class="empty-mini">No active web sessions.</div>`;
  } catch (error) { showError(error); }
}

async function userPageAction(action) {
  const user = userPageState?.user;
  if (!user) return;
  if (action === "edit") { state.drawerUser = user; return editUserModal(user); }
  if (action === "assign-role") { state.assignmentUser = user; return assignRoleModal(user); }
  if (action === "reset-password") {
    await resetPasswordModal(user.id);
    if (state.view === "user-detail") await renderUserPage(user.id);
    return;
  }
  if (action === "reset-mfa") {
    await resetMfaModal(user.id);
    if (state.view === "user-detail") await renderUserPage(user.id);
    return;
  }
  if (action === "revoke-sessions") {
    await revokeSessionsModal(user.id);
    if (state.view === "user-detail") await renderUserPage(user.id);
    return;
  }
  if (action === "delete") {
    await deleteUserModal(user.id);
    location.hash = "#/users";
    return;
  }
  if (action === "restore") {
    await restoreUserModal(user.id);
    await renderUserPage(user.id);
    return;
  }
  if (action === "suspend" || action === "activate") {
    await toggleUserStatus(user.id, action);
    await renderUserPage(user.id);
  }
}

function openAliasEditor(userId, alias) {
  openModal({
    title: "Edit alias",
    subtitle: alias.email,
    body: `<form id="aliasEditorForm"><label class="field"><span>Label</span><input id="aliasEditorLabel" value="${esc(alias.label || "")}" maxlength="80"></label><div class="field-row alias-editor-flags"><label class="switch-inline"><input type="checkbox" id="aliasEditorSend" ${alias.canSend ? "checked" : ""}><span>Can send from this address</span></label><label class="switch-inline"><input type="checkbox" id="aliasEditorActive" ${alias.active !== false ? "checked" : ""}><span>Active</span></label></div></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" id="aliasEditorSave">${icon("check")}Save alias</button>`,
    onMount: () => {
      $("#aliasEditorSave").addEventListener("click", async (event) => {
        const button = event.currentTarget;
        setBusy(button, true, "Saving…");
        try {
          await api(`users/${encodeURIComponent(userId)}/aliases/${encodeURIComponent(alias.id)}`, {
            method: "PATCH",
            body: { label: $("#aliasEditorLabel").value, canSend: $("#aliasEditorSend").checked, active: $("#aliasEditorActive").checked }
          });
          closeModal();
          toast("Alias updated");
          await renderUserPage(userId);
        } catch (error) { showError(error); setBusy(button, false); }
      });
    }
  });
}

function openSignatureEditor(userId, signature) {
  const editing = Boolean(signature);
  openModal({
    title: editing ? "Edit signature" : "New signature",
    subtitle: "The signature is available in compose and can be the default for new mail, replies and forwards.",
    wide: true,
    body: `<form id="signatureEditorForm">
      <label class="field"><span>Name</span><input id="signatureName" value="${esc(signature?.name || "Signature")}" required maxlength="80"></label>
      <label class="field"><span>Plain text</span><textarea id="signatureText" rows="4" placeholder="Avery Chen&#10;Product Lead · Alt8Mail">${esc(signature?.bodyText || "")}</textarea></label>
      <label class="field"><span>HTML <small class="muted">optional</small></span><textarea id="signatureHtml" rows="5" placeholder="<p><strong>Avery Chen</strong><br>Product Lead</p>">${esc(signature?.bodyHtml || "")}</textarea><small class="muted">Scripts, event handlers and unsafe styles are removed when saved.</small></label>
      <div class="field-row signature-defaults">
        <label class="switch-inline"><input type="checkbox" id="signatureDefaultNew" ${signature?.defaultNew ? "checked" : ""}><span>Default for new mail</span></label>
        <label class="switch-inline"><input type="checkbox" id="signatureDefaultReply" ${signature?.defaultReply ? "checked" : ""}><span>Default for replies</span></label>
        <label class="switch-inline"><input type="checkbox" id="signatureDefaultForward" ${signature?.defaultForward ? "checked" : ""}><span>Default for forwards</span></label>
      </div>
    </form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" id="signatureEditorSave">${icon("check")}Save signature</button>`,
    onMount: () => {
      $("#signatureEditorSave").addEventListener("click", async (event) => {
        const button = event.currentTarget;
        setBusy(button, true, "Saving…");
        try {
          const body = {
            name: $("#signatureName").value.trim(),
            bodyText: $("#signatureText").value,
            bodyHtml: $("#signatureHtml").value,
            defaultNew: $("#signatureDefaultNew").checked,
            defaultReply: $("#signatureDefaultReply").checked,
            defaultForward: $("#signatureDefaultForward").checked
          };
          const path = editing ? `users/${encodeURIComponent(userId)}/signatures/${encodeURIComponent(signature.id)}` : `users/${encodeURIComponent(userId)}/signatures`;
          await api(path, { method: editing ? "PATCH" : "POST", body });
          closeModal();
          toast(editing ? "Signature updated" : "Signature created");
          await renderUserPage(userId);
        } catch (error) { showError(error); setBusy(button, false); }
      });
    }
  });
}

function openIdentitySignatureEditor(userId, email) {
  const current = (userPageState.identities || []).find((item) => item.email === email) || { email, bodyText: "", bodyHtml: "" };
  openModal({
    title: "Signature for " + email,
    subtitle: "This signature is used when the address is selected in compose. Leave both fields empty to remove it.",
    wide: true,
    body: `<form id="identitySignatureForm"><label class="field"><span>Plain text</span><textarea id="identitySignatureText" rows="4">${esc(current.bodyText || "")}</textarea></label><label class="field"><span>HTML <small class="muted">optional</small></span><textarea id="identitySignatureHtml" rows="5">${esc(current.bodyHtml || "")}</textarea></label></form>`,
    footer: `<button class="btn secondary" data-modal-close>Cancel</button><button class="btn primary" id="identitySignatureSave">${icon("check")}Save signature</button>`,
    onMount: () => {
      $("#identitySignatureSave").addEventListener("click", async (event) => {
        const button = event.currentTarget;
        setBusy(button, true, "Saving…");
        try {
          await api(`users/${encodeURIComponent(userId)}/identity-signatures`, {
            method: "PUT",
            body: { email, bodyText: $("#identitySignatureText").value, bodyHtml: $("#identitySignatureHtml").value }
          });
          closeModal();
          toast("Address signature saved");
          await renderUserPage(userId);
        } catch (error) { showError(error); setBusy(button, false); }
      });
    }
  });
}
