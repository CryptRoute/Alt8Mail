"use strict";

/* Admin import wizards: create users from CSV/XLSX and migrate mailboxes from
   another IMAP/POP3 provider. Both jobs continue on the server after the page
   is closed; the dashboard and history blocks expose their live progress. */
let importUsersState = { rows: [], sourceName: "", jobId: "", timer: null, job: null, items: [] };
let mailImportState = { jobId: "", timer: null, job: null, items: [], candidates: [], manualRows: [], source: { protocol: "imap", host: "", port: 993, security: "ssl", includeJunk: false, includeTrash: false, includeDrafts: true } };

function stopImportPolling() {
  if (importUsersState.timer) clearTimeout(importUsersState.timer);
  if (mailImportState.timer) clearTimeout(mailImportState.timer);
  importUsersState.timer = null;
  mailImportState.timer = null;
}

/* ------------------------------------------------------------
   User import
------------------------------------------------------------ */
async function renderImportUsers() {
  stopImportPolling();
  const history = await safeAdminList("users/import/jobs");
  if (importUsersState.jobId) {
    await loadUserImportJob(importUsersState.jobId, false);
  }
  const validRows = importUsersState.rows.filter((row) => row.valid);
  const invalidRows = importUsersState.rows.filter((row) => !row.valid);
  $("#viewRoot").innerHTML = `
    <div class="view-head">
      <div class="view-head-with-back"><button class="icon-button" data-action="back-users" aria-label="Back to users"><span aria-hidden="true">←</span></button><div><h1>Import users</h1><p>Create Alt8Mail accounts from a CSV or Excel sheet. Full name and email are required; an unknown role becomes Member.</p></div></div>
      <div class="view-actions"><a class="btn secondary" href="#/users">Back to users</a></div>
    </div>
    <div class="import-layout">
      <section class="card import-card">
        <div class="import-step"><span>1</span><div><h2>Upload a file</h2><p>CSV, TSV or XLSX. The file is read in your browser and only the parsed rows are sent to the server.</p></div></div>
        <label class="import-drop" id="userImportDrop">
          ${icon("arrow-up")}
          <b>Drop a CSV or XLSX file here</b>
          <span>or choose a file from your computer</span>
          <input type="file" id="userImportFile" accept=".csv,.tsv,.txt,.xlsx,.xlsm" hidden>
        </label>
        <div class="import-toolbar">
          <button class="btn secondary" type="button" id="userImportChoose">${icon("list")}Choose file</button>
          <button class="btn ghost small" type="button" id="userImportTemplate">Download CSV template</button>
          ${importUsersState.sourceName ? `<span class="muted" style="font-size:11px">${esc(importUsersState.sourceName)} · ${importUsersState.rows.length} rows</span>` : ""}
        </div>
        <div class="import-schema"><b>Accepted columns</b><span><code>name</code> or <code>full_name</code> (required)</span><span><code>email</code> (required)</span><span><code>role</code></span><span><code>quota_gb</code></span><span><code>notify_email</code></span></div>
      </section>
      ${importUsersState.rows.length ? `<section class="card import-card">
        <div class="import-step"><span>2</span><div><h2>Review ${importUsersState.rows.length} row${importUsersState.rows.length === 1 ? "" : "s"}</h2><p>${validRows.length} ready to create${invalidRows.length ? ` · ${invalidRows.length} need attention` : ""}. Passwords are generated and shown only once.</p></div></div>
        <div class="table-wrap"><div class="table-scroll"><table class="data import-table"><thead><tr><th>Name</th><th>Email</th><th>Role</th><th>Quota</th><th>Notify</th><th>Status</th></tr></thead><tbody>
          ${importUsersState.rows.map((row) => `<tr class="${row.valid ? "" : "row-invalid"}"><td>${esc(row.name || "—")}</td><td>${esc(row.email || "—")}</td><td>${esc(roleLabel(row.role))}</td><td>${esc(row.quotaGb ? row.quotaGb + " GB" : "16 GB")}</td><td>${esc(row.notifyEmail || "—")}</td><td>${row.valid ? `<span class="badge active">Ready</span>` : `<span class="badge suspended" title="${esc(row.error)}">${esc(row.error)}</span>`}</td></tr>`).join("")}
        </tbody></table></div></div>
        <div class="import-actions"><button class="btn secondary" type="button" id="userImportClear">Clear</button><button class="btn primary" type="button" id="userImportStart" ${validRows.length ? "" : "disabled"}>${icon("arrow-up")}Create ${validRows.length} user${validRows.length === 1 ? "" : "s"}</button></div>
      </section>` : ""}
      <section class="card import-card" id="userImportProgress">${importUsersProgressMarkup()}</section>
      ${(history.jobs || []).length ? `<section class="card import-card"><div class="import-step"><span>${icon("history")}</span><div><h2>Recent imports</h2><p>Results stay available while their credentials are retained.</p></div></div><div class="import-history">${(history.jobs || []).slice(0, 8).map((job) => `<button class="import-history-row" data-view-user-import="${esc(job.id)}"><div><b>${esc(job.total)} users</b><small>${esc(job.createdAt)} · ${esc(job.createdBy || "Administrator")}</small></div><span class="badge ${job.status === "completed" ? "active" : job.status === "failed" ? "suspended" : "pending"}">${esc(job.status)}</span><span>${job.created} created · ${job.failed} failed</span></button>`).join("")}</div></section>` : ""}
    </div>`;
  bindImportUsers();
  if (importUsersState.jobId && importUsersState.job && !["completed", "failed"].includes(importUsersState.job.status)) scheduleUserImportPoll();
}

function importUsersProgressMarkup() {
  const job = importUsersState.job;
  if (!job) return `<div class="import-empty">${icon("users")}<h3>No import running</h3><p>Upload a sheet to create accounts. Progress is preserved if you navigate away.</p></div>`;
  const items = importUsersState.items || [];
  return `<div class="import-step"><span>${job.status === "completed" ? icon("check") : job.status === "failed" ? icon("alert") : icon("refresh")}</span><div><h2>${job.status === "completed" ? "Import complete" : job.status === "failed" ? "Import failed" : "Creating accounts"}</h2><p>${job.created} created · ${job.failed} failed · ${esc(job.total)} total</p></div><span class="badge ${job.status === "completed" ? "active" : job.status === "failed" ? "suspended" : "pending"}">${esc(job.status)}</span></div>
    <div class="import-progress"><span style="width:${job.total ? Math.round(((job.created + job.failed) / job.total) * 100) : 0}%"></span></div>
    ${job.lastError ? `<p class="form-error">${esc(job.lastError)}</p>` : ""}
    ${items.length ? `<div class="table-wrap"><div class="table-scroll"><table class="data import-table"><thead><tr><th>User</th><th>Status</th><th>Web password</th><th>Mailbox password</th><th>Notification</th></tr></thead><tbody>
      ${items.map((item) => `<tr><td><b>${esc(item.name || item.email)}</b><small class="cell-sub">${esc(item.email)}</small></td><td><span class="badge ${item.status === "created" ? "active" : item.status === "failed" ? "suspended" : "pending"}">${esc(item.status)}</span>${item.error ? `<small class="cell-sub error-text">${esc(item.error)}</small>` : ""}</td><td>${item.temporaryPassword ? credentialCell(item.temporaryPassword) : "—"}</td><td>${item.mailPassword ? credentialCell(item.mailPassword) : "—"}</td><td>${item.notifyEmail ? item.emailed ? `<span class="badge active">Emailed</span>` : `<span class="badge ${item.emailError ? "suspended" : "neutral"}">${item.emailError ? esc(item.emailError) : "Not sent"}</span>` : `<span class="muted">—</span>`}</td></tr>`).join("")}
    </tbody></table></div></div><div class="import-actions"><button class="btn secondary" type="button" id="userImportDownload">${icon("download")}Download results CSV</button><button class="btn primary" type="button" id="userImportDone">Done</button></div>` : ""}`;
}

function credentialCell(value) {
  return `<span class="credential-inline"><code>${esc(value)}</code><button class="icon-button" type="button" data-copy-credential="${esc(value)}" aria-label="Copy password">${icon("list")}</button></span>`;
}

function bindImportUsers() {
  const fileInput = $("#userImportFile");
  const drop = $("#userImportDrop");
  $("#userImportChoose")?.addEventListener("click", () => fileInput?.click());
  fileInput?.addEventListener("change", () => { if (fileInput.files?.[0]) readUserImportFile(fileInput.files[0]); });
  if (drop) {
    drop.addEventListener("dragover", (event) => { event.preventDefault(); drop.classList.add("is-dragging"); });
    drop.addEventListener("dragleave", () => drop.classList.remove("is-dragging"));
    drop.addEventListener("drop", (event) => {
      event.preventDefault(); drop.classList.remove("is-dragging");
      if (event.dataTransfer.files?.[0]) readUserImportFile(event.dataTransfer.files[0]);
    });
  }
  $("#userImportTemplate")?.addEventListener("click", downloadUserImportTemplate);
  $("#userImportClear")?.addEventListener("click", () => { importUsersState.rows = []; importUsersState.sourceName = ""; importUsersState.jobId = ""; importUsersState.job = null; importUsersState.items = []; renderImportUsers(); });
  $("#userImportStart")?.addEventListener("click", startUserImport);
  $("#userImportDone")?.addEventListener("click", () => { importUsersState.jobId = ""; importUsersState.job = null; importUsersState.items = []; renderImportUsers(); });
  $("#userImportDownload")?.addEventListener("click", () => downloadUserImportResults());
  $$("[data-view-user-import]").forEach((button) => button.addEventListener("click", async () => {
    importUsersState.jobId = button.dataset.viewUserImport;
    await loadUserImportJob(importUsersState.jobId, true);
    renderImportUsers();
  }));
  $$("[data-copy-credential]").forEach((button) => button.addEventListener("click", async () => {
    await navigator.clipboard.writeText(button.dataset.copyCredential).catch(() => {});
    toast("Copied to clipboard");
  }));
}

async function readUserImportFile(file) {
  try {
    const parsed = await window.A8MImport.parseFile(file);
    importUsersState.sourceName = file.name;
    importUsersState.rows = (parsed.rows || []).map(mapUserImportRow).filter((row) => row.name || row.email);
    importUsersState.jobId = ""; importUsersState.job = null; importUsersState.items = [];
    if (!importUsersState.rows.length) toast("No data rows were found in that file", "error");
    renderImportUsers();
  } catch (error) { showError(error); }
}

function mapUserImportRow(record) {
  const pick = window.A8MImport.pick;
  const name = pick(record, ["full name", "full_name", "name", "display name"]);
  const email = pick(record, ["email", "email address", "e-mail", "mail"]).toLowerCase();
  const roleInput = pick(record, ["role", "user role"]).toLowerCase();
  const role = ["owner", "admin", "member", "readonly"].includes(roleInput) ? roleInput : "member";
  const quotaRaw = pick(record, ["quota_gb", "quota", "storage", "storage_gb", "quota (gb)"]);
  const quotaGb = Number(String(quotaRaw).replace(/[^0-9.]/g, "")) || 0;
  const notifyEmail = pick(record, ["notify_email", "notification email", "notify email", "alternate email"]).toLowerCase();
  let error = "";
  if (!name) error = "Full name is required";
  else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) error = "A valid email is required";
  else if (notifyEmail && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(notifyEmail)) error = "notify_email is invalid";
  return { name, email, role, quotaGb, notifyEmail, valid: !error, error };
}

function downloadUserImportTemplate() {
  const csv = "name,email,role,quota_gb,notify_email\nAvery Chen,avery.chen@example.com,member,16,\n";
  downloadText("alt8mail-user-import-template.csv", csv, "text/csv");
}

async function startUserImport() {
  const rows = importUsersState.rows.filter((row) => row.valid).map((row) => ({
    name: row.name, email: row.email, role: row.role, quotaGb: row.quotaGb || 16,
    notifyEmail: row.notifyEmail
  }));
  if (!rows.length) return;
  const button = $("#userImportStart");
  setBusy(button, true, "Starting…");
  try {
    const data = await api("users/import", { method: "POST", body: { rows } });
    importUsersState.jobId = data.jobId;
    importUsersState.job = { status: "queued", total: data.total, created: 0, failed: 0, createdBy: state.admin?.name, createdAt: new Date().toISOString() };
    importUsersState.items = [];
    renderImportUsers();
  } catch (error) { showError(error); setBusy(button, false); }
}

async function loadUserImportJob(jobId, render = true) {
  try {
    const data = await api("users/import/jobs/" + encodeURIComponent(jobId));
    importUsersState.jobId = jobId;
    importUsersState.job = data.job;
    importUsersState.items = data.items || [];
    if (render) renderImportUsers();
    return data;
  } catch (error) { showError(error); return null; }
}

function scheduleUserImportPoll() {
  if (!importUsersState.jobId) return;
  importUsersState.timer = setTimeout(async () => {
    if (state.view !== "import-users") return;
    await loadUserImportJob(importUsersState.jobId, false);
    renderImportUsers();
  }, 1600);
}

function downloadUserImportResults() {
  const rows = [["name", "email", "role", "status", "web_password", "mail_password", "notify_email", "emailed", "error"]];
  (importUsersState.items || []).forEach((item) => rows.push([item.name, item.email, item.role, item.status, item.temporaryPassword, item.mailPassword, item.notifyEmail, item.emailed ? "yes" : "no", item.error]));
  downloadText("alt8mail-user-import-results.csv", rows.map((row) => row.map((value) => `"${String(value == null ? "" : value).replace(/"/g, '""')}"`).join(",")).join("\n"), "text/csv");
}

/* ------------------------------------------------------------
   Mailbox migration
------------------------------------------------------------ */
async function renderImportMailboxes() {
  stopImportPolling();
  const history = await safeAdminList("mail-import/jobs");
  mailImportState.candidates = await loadUserPageCandidates();
  if (mailImportState.jobId) await loadMailImportJob(mailImportState.jobId, false);
  const job = mailImportState.job;
  const items = mailImportState.items || [];
  $("#viewRoot").innerHTML = `
    <div class="view-head">
      <div class="view-head-with-back"><button class="icon-button" data-action="back-users" aria-label="Back to users"><span aria-hidden="true">←</span></button><div><h1>Import mailboxes</h1><p>Bring users and their mail from another IMAP or POP3 provider. Validation, mapping and import run on the server and survive a closed tab.</p></div></div>
      <div class="view-actions"><a class="btn secondary" href="#/users">Back to users</a></div>
    </div>
    <div class="import-layout">
      ${job ? renderMailImportJob(job, items) : renderMailImportSetup()}
      ${(history.jobs || []).length ? `<section class="card import-card"><div class="import-step"><span>${icon("history")}</span><div><h2>Migration history</h2><p>Resume monitoring any job from here.</p></div></div><div class="import-history">${(history.jobs || []).slice(0, 10).map((entry) => `<button class="import-history-row" data-view-mail-import="${esc(entry.id)}"><div><b>${esc(entry.host)} · ${esc(entry.protocol.toUpperCase())}</b><small>${esc(entry.createdAt)} · ${entry.total} mailbox${entry.total === 1 ? "" : "es"}</small></div><span class="badge ${mailImportStatusClass(entry.status)}">${esc(entry.status.replace(/_/g, " "))}</span><span>${entry.importedMessages || 0} messages</span></button>`).join("")}</div></section>` : ""}
    </div>`;
  bindImportMailboxes();
  if (mailImportState.jobId && mailImportState.job && !["completed", "failed", "cancelled"].includes(mailImportState.job.status)) scheduleMailImportPoll();
}

function mailImportStatusClass(status) {
  if (status === "completed") return "active";
  if (status === "failed") return "suspended";
  if (status === "cancelled") return "neutral";
  return "pending";
}

function renderMailImportSetup() {
  const source = mailImportState.source;
  return `<section class="card import-card">
    <div class="import-step"><span>1</span><div><h2>Connect to the old mail server</h2><p>Mail is copied, never deleted from the old provider unless you explicitly enable that option.</p></div></div>
    <div class="field-row">
      <label class="field"><span>Protocol</span><select id="mailImportProtocol"><option value="imap" ${source.protocol === "imap" ? "selected" : ""}>IMAP (recommended)</option><option value="pop3" ${source.protocol === "pop3" ? "selected" : ""}>POP3</option></select></label>
      <label class="field grow"><span>Server hostname</span><input id="mailImportHost" value="${esc(source.host)}" placeholder="mail.old-provider.com" autocomplete="off"></label>
      <label class="field"><span>Port</span><input id="mailImportPort" type="number" min="1" max="65535" value="${Number(source.port) || 993}"></label>
      <label class="field"><span>Security</span><select id="mailImportSecurity"><option value="ssl" ${source.security === "ssl" ? "selected" : ""}>SSL / TLS</option><option value="starttls" ${source.security === "starttls" ? "selected" : ""}>STARTTLS</option><option value="none" ${source.security === "none" ? "selected" : ""}>Unencrypted (not recommended)</option></select></label>
    </div>
    <div class="import-options">
      <label class="switch-inline"><input type="checkbox" id="mailImportJunk" ${source.includeJunk ? "checked" : ""}><span>Include Junk / Spam</span></label>
      <label class="switch-inline"><input type="checkbox" id="mailImportTrash" ${source.includeTrash ? "checked" : ""}><span>Include Trash / Deleted</span></label>
      <label class="switch-inline"><input type="checkbox" id="mailImportDrafts" ${source.includeDrafts !== false ? "checked" : ""}><span>Include Drafts</span></label>
      <span class="muted">Messages above 50 MB are skipped for safety.</span>
    </div>
    <div class="import-step" style="margin-top:22px"><span>2</span><div><h2>Add mailbox credentials</h2><p>Upload a CSV with <code>email</code> and <code>password</code>, plus optional <code>notify_email</code>, or add rows manually.</p></div></div>
    <label class="import-drop" id="mailImportDrop">${icon("arrow-up")}<b>Drop the mailbox CSV here</b><span>email, password, notify_email</span><input type="file" id="mailImportFile" accept=".csv,.tsv,.txt,.xlsx,.xlsm" hidden></label>
    <div class="import-toolbar"><button class="btn secondary" type="button" id="mailImportChoose">${icon("list")}Choose file</button><button class="btn ghost small" type="button" id="mailImportTemplate">Download CSV template</button><button class="btn ghost small" type="button" id="mailImportAddRow">${icon("plus")}Add row manually</button></div>
    <div class="manual-mailbox-list">${mailImportState.manualRows.map((row, index) => `<div class="manual-mailbox-row"><input data-manual-email="${index}" placeholder="user@old-provider.com" value="${esc(row.email)}"><input data-manual-password="${index}" type="password" placeholder="Mailbox password" value="${esc(row.password)}"><input data-manual-notify="${index}" type="email" placeholder="Notify email (optional)" value="${esc(row.notifyEmail || "")}"><button class="icon-button" type="button" data-manual-remove="${index}" aria-label="Remove row">${icon("x")}</button></div>`).join("") || `<div class="empty-mini">No manual rows. Upload a CSV or add one below.</div>`}</div>
    <div class="import-actions"><span class="muted" style="font-size:11px">${mailImportState.manualRows.length} manual row${mailImportState.manualRows.length === 1 ? "" : "s"} · credentials are sealed with AES-GCM before storage</span><button class="btn primary" type="button" id="mailImportValidate">${icon("shield")}Validate credentials</button></div>
  </section>`;
}

function renderMailImportJob(job, items) {
  const progress = job.total ? Math.round(((job.validated || 0) + (job.completed || 0)) / Math.max(job.total, 1) * 100) : 0;
  const header = `<div class="import-step"><span>${job.status === "completed" ? icon("check") : job.status === "awaiting_mapping" ? icon("sliders") : job.status === "failed" ? icon("alert") : icon("refresh")}</span><div><h2>${job.protocol.toUpperCase()} migration · ${esc(job.host)}</h2><p>${job.status === "validating" ? "Validating each source login…" : job.status === "awaiting_mapping" ? "Credentials validated. Map each source mailbox to a destination." : job.status === "queued" ? "Queued for import…" : job.status === "running" ? `Importing ${esc(job.currentItem || "")}` : job.status === "completed" ? "Migration complete" : job.status === "cancelled" ? "Migration cancelled" : "Migration failed"}</p></div><span class="badge ${mailImportStatusClass(job.status)}">${esc(job.status.replace(/_/g, " "))}</span></div>`;
  if (job.status === "validating") {
    return `<section class="card import-card">${header}<div class="import-progress"><span style="width:${job.total ? Math.round((job.validated || 0) / job.total * 100) : 15}%"></span></div><div class="mailbox-status-list">${items.map((item) => mailImportItemStatus(item)).join("")}</div><div class="import-actions"><button class="btn secondary" type="button" id="mailImportBack">Start another</button><button class="btn danger" type="button" id="mailImportCancel">Cancel</button></div></section>`;
  }
  if (job.status === "awaiting_mapping") {
    return `<section class="card import-card">${header}<div class="mapping-list">${items.map((item) => mailImportMappingRow(item)).join("")}</div><div class="import-actions"><button class="btn secondary" type="button" id="mailImportBack">Cancel</button><button class="btn primary" type="button" id="mailImportStart">${icon("arrow-up")}Start migration</button></div></section>`;
  }
  if (job.status === "queued" || job.status === "running") {
    return `<section class="card import-card">${header}<div class="import-progress"><span style="width:${progress}%"></span></div><div class="import-metrics"><div><b>${job.importedMessages || 0}</b><span>messages copied</span></div><div><b>${job.completed || 0}</b><span>mailboxes done</span></div><div><b>${job.failed || 0}</b><span>failed</span></div><div><b>${job.total}</b><span>total</span></div></div><div class="mailbox-status-list">${items.map((item) => mailImportItemStatus(item)).join("")}</div><div class="import-actions"><button class="btn secondary" type="button" id="mailImportBack">Back to setup</button><button class="btn danger" type="button" id="mailImportCancel">Cancel migration</button></div></section>`;
  }
  return `<section class="card import-card">${header}<div class="table-wrap"><div class="table-scroll"><table class="data import-table"><thead><tr><th>Source</th><th>Destination</th><th>Status</th><th>Imported</th><th>Password / notification</th><th>Error</th></tr></thead><tbody>${items.map((item) => `<tr><td><b>${esc(item.sourceEmail)}</b></td><td>${esc(item.targetEmail || "—")}<small class="cell-sub">${esc(roleLabel(item.targetRole))}</small></td><td><span class="badge ${item.status === "completed" ? "active" : item.status === "failed" ? "suspended" : "neutral"}">${esc(item.status)}</span></td><td>${item.importedMessages || 0}${item.totalMessages ? ` / ${item.totalMessages}` : ""}</td><td>${item.notifyEmail && item.notified ? `<span class="badge active">Emailed</span>` : item.initialPassword ? credentialCell(item.initialPassword) : item.notifyEmail ? `<span class="badge neutral">Not emailed</span>` : "—"}${item.mailPassword ? `<small class="cell-sub">Mailbox password: <code>${esc(item.mailPassword)}</code></small>` : ""}</td><td>${esc(item.error || item.authError || "—")}</td></tr>`).join("")}</tbody></table></div></div><div class="import-actions"><button class="btn secondary" type="button" id="mailImportDownload">${icon("download")}Download results</button><button class="btn primary" type="button" id="mailImportBack">Start another migration</button></div></section>`;
}

function mailImportItemStatus(item) {
  return `<div class="mailbox-status-row"><div><b>${esc(item.sourceEmail)}</b><small>${esc(item.authError || item.targetEmail || "Waiting…")}</small></div><span class="badge ${item.status === "ready" ? "active" : item.status === "auth_failed" || item.status === "failed" ? "suspended" : "pending"}">${esc(item.status === "auth_failed" ? "authentication failed" : item.status)}</span><span class="mono">${item.importedMessages || 0}${item.totalMessages ? ` / ${item.totalMessages}` : ""}</span></div>`;
}

function mailImportMappingRow(item) {
  const candidates = mailImportState.candidates || [];
  const match = candidates.find((candidate) => candidate.email && candidate.email.toLowerCase() === item.sourceEmail.toLowerCase());
  const mode = item.targetMode || (match ? "existing" : "new");
  const disabled = item.status === "auth_failed" || item.status === "skipped";
  if (disabled) {
    return `<div class="mapping-row is-disabled"><div class="mapping-source"><b>${esc(item.sourceEmail)}</b><small>${esc(item.authError || "Credentials could not be validated")}</small></div><span class="badge suspended">Skipped</span></div>`;
  }
  return `<div class="mapping-row" data-mapping-item="${esc(item.id)}">
    <div class="mapping-source"><b>${esc(item.sourceEmail)}</b><small>Validated source mailbox</small></div>
    <div class="mapping-target">
      <select data-mapping-mode="${esc(item.id)}"><option value="new" ${mode === "new" ? "selected" : ""}>Create new user</option><option value="existing" ${mode === "existing" ? "selected" : ""}>Map to existing user</option></select>
      <select data-mapping-user="${esc(item.id)}" ${mode === "existing" ? "" : "hidden"}>${candidates.map((candidate) => `<option value="${esc(candidate.id)}" ${match?.id === candidate.id ? "selected" : ""}>${esc(candidate.name)} · ${esc(candidate.email)}</option>`).join("")}</select>
      <div class="mapping-new-fields" ${mode === "new" ? "" : "hidden"}>
        <input data-mapping-name="${esc(item.id)}" placeholder="Full name" value="${esc(item.targetName || "")}">
        <select data-mapping-role="${esc(item.id)}">${["member", "admin", "owner", "readonly"].map((role) => `<option value="${role}" ${item.targetRole === role ? "selected" : ""}>${roleLabel(role)}</option>`).join("")}</select>
        <input data-mapping-quota="${esc(item.id)}" type="number" min="1" max="1024" value="${Math.round((item.targetQuotaBytes || 17179869184) / 1073741824)}" title="Quota in GB">
      </div>
      <input data-mapping-notify="${esc(item.id)}" type="email" placeholder="Notify email (optional)" value="${esc(item.notifyEmail || "")}">
    </div>
  </div>`;
}

function bindImportMailboxes() {
  $("#mailImportProtocol")?.addEventListener("change", (event) => {
    mailImportState.source.protocol = event.target.value;
    mailImportState.source.port = event.target.value === "pop3" ? 995 : 993;
    renderImportMailboxes();
  });
  $("#mailImportHost")?.addEventListener("input", (event) => { mailImportState.source.host = event.target.value; });
  $("#mailImportPort")?.addEventListener("input", (event) => { mailImportState.source.port = Number(event.target.value) || 0; });
  $("#mailImportSecurity")?.addEventListener("change", (event) => { mailImportState.source.security = event.target.value; });
  $("#mailImportJunk")?.addEventListener("change", (event) => { mailImportState.source.includeJunk = event.target.checked; });
  $("#mailImportTrash")?.addEventListener("change", (event) => { mailImportState.source.includeTrash = event.target.checked; });
  $("#mailImportDrafts")?.addEventListener("change", (event) => { mailImportState.source.includeDrafts = event.target.checked; });
  $("#mailImportChoose")?.addEventListener("click", () => $("#mailImportFile")?.click());
  $("#mailImportFile")?.addEventListener("change", () => { const file = $("#mailImportFile").files?.[0]; if (file) readMailImportFile(file); });
  const drop = $("#mailImportDrop");
  if (drop) {
    drop.addEventListener("dragover", (event) => { event.preventDefault(); drop.classList.add("is-dragging"); });
    drop.addEventListener("dragleave", () => drop.classList.remove("is-dragging"));
    drop.addEventListener("drop", (event) => { event.preventDefault(); drop.classList.remove("is-dragging"); const file = event.dataTransfer.files?.[0]; if (file) readMailImportFile(file); });
  }
  $("#mailImportTemplate")?.addEventListener("click", () => downloadText("alt8mail-mailbox-migration.csv", "email,password,notify_email\nuser@old-provider.com,temporary-password,,", "text/csv"));
  $("#mailImportAddRow")?.addEventListener("click", () => { mailImportState.manualRows.push({ email: "", password: "", notifyEmail: "" }); renderImportMailboxes(); });
  $$("[data-manual-email]").forEach((input) => input.addEventListener("input", () => { const row = mailImportState.manualRows[Number(input.dataset.manualEmail)]; if (row) row.email = input.value; }));
  $$("[data-manual-password]").forEach((input) => input.addEventListener("input", () => { const row = mailImportState.manualRows[Number(input.dataset.manualPassword)]; if (row) row.password = input.value; }));
  $$("[data-manual-notify]").forEach((input) => input.addEventListener("input", () => { const row = mailImportState.manualRows[Number(input.dataset.manualNotify)]; if (row) row.notifyEmail = input.value; }));
  $$("[data-manual-remove]").forEach((button) => button.addEventListener("click", () => { mailImportState.manualRows.splice(Number(button.dataset.manualRemove), 1); renderImportMailboxes(); }));
  $("#mailImportValidate")?.addEventListener("click", startMailImportValidation);
  $("#mailImportStart")?.addEventListener("click", submitMailImportMapping);
  $("#mailImportBack")?.addEventListener("click", () => { mailImportState.jobId = ""; mailImportState.job = null; mailImportState.items = []; renderImportMailboxes(); });
  $("#mailImportCancel")?.addEventListener("click", cancelMailImport);
  $("#mailImportDownload")?.addEventListener("click", downloadMailImportResults);
  $$("[data-mapping-mode]").forEach((select) => select.addEventListener("change", () => {
    const id = select.dataset.mappingMode;
    const existing = $(`[data-mapping-user="${CSS.escape(id)}"]`);
    const fields = select.closest(".mapping-row")?.querySelector(".mapping-new-fields");
    if (existing) existing.hidden = select.value !== "existing";
    if (fields) fields.hidden = select.value !== "new";
  }));
  $$("[data-view-mail-import]").forEach((button) => button.addEventListener("click", async () => {
    mailImportState.jobId = button.dataset.viewMailImport;
    await loadMailImportJob(mailImportState.jobId, false);
    renderImportMailboxes();
  }));
}

async function readMailImportFile(file) {
  try {
    const parsed = await window.A8MImport.parseFile(file);
    const rows = (parsed.rows || []).map((record) => ({
      email: window.A8MImport.pick(record, ["email", "email address", "mail"]).toLowerCase(),
      password: window.A8MImport.pick(record, ["password", "mailbox password", "pass"]),
      notifyEmail: window.A8MImport.pick(record, ["notify_email", "notification email", "notify email"]).toLowerCase()
    })).filter((row) => row.email || row.password);
    if (!rows.length) { toast("No mailbox rows were found", "error"); return; }
    mailImportState.manualRows = rows;
    renderImportMailboxes();
  } catch (error) { showError(error); }
}

async function startMailImportValidation() {
  const source = {
    protocol: $("#mailImportProtocol").value,
    host: $("#mailImportHost").value.trim(),
    port: Number($("#mailImportPort").value) || 993,
    security: $("#mailImportSecurity").value,
    includeJunk: $("#mailImportJunk").checked,
    includeTrash: $("#mailImportTrash").checked,
    includeDrafts: $("#mailImportDrafts").checked
  };
  if (!source.host) { toast("Enter the old mail server hostname", "error"); return; }
  const items = mailImportState.manualRows
    .map((row) => ({
      email: String(row.email || "").trim().toLowerCase(),
      password: String(row.password || ""),
      notifyEmail: String(row.notifyEmail || "").trim().toLowerCase()
    }))
    .filter((row) => row.email && row.password);
  if (!items.length) { toast("Add at least one mailbox with an email and password", "error"); return; }
  const invalid = items.find((row) => !row.email.includes("@"));
  if (invalid) { toast("Every mailbox needs a valid email address", "error"); return; }
  mailImportState.source = source;
  const button = $("#mailImportValidate");
  setBusy(button, true, "Starting validation…");
  try {
    const data = await api("mail-import/jobs", { method: "POST", body: { ...source, items, options: { includeJunk: source.includeJunk, includeTrash: source.includeTrash, includeDrafts: source.includeDrafts } } });
    mailImportState.jobId = data.jobId;
    mailImportState.job = { id: data.jobId, protocol: source.protocol, host: source.host, port: source.port, security: source.security, status: "validating", total: data.total, validated: 0, completed: 0, failed: 0, importedMessages: 0 };
    mailImportState.items = [];
    renderImportMailboxes();
  } catch (error) { showError(error); setBusy(button, false); }
}

async function submitMailImportMapping() {
  const rows = $$("[data-mapping-item]").map((row) => {
    const id = row.dataset.mappingItem;
    const mode = row.querySelector(`[data-mapping-mode="${CSS.escape(id)}"]`).value;
    return {
      id,
      targetMode: mode,
      targetUserId: row.querySelector(`[data-mapping-user="${CSS.escape(id)}"]`)?.value || "",
      targetName: row.querySelector(`[data-mapping-name="${CSS.escape(id)}"]`)?.value || "",
      targetRole: row.querySelector(`[data-mapping-role="${CSS.escape(id)}"]`)?.value || "member",
      targetQuotaGb: Number(row.querySelector(`[data-mapping-quota="${CSS.escape(id)}"]`)?.value) || 16,
      notifyEmail: row.querySelector(`[data-mapping-notify="${CSS.escape(id)}"]`)?.value || ""
    };
  });
  if (!rows.length) { toast("There are no validated mailboxes to import", "error"); return; }
  const button = $("#mailImportStart");
  setBusy(button, true, "Starting…");
  try {
    await api(`mail-import/jobs/${encodeURIComponent(mailImportState.jobId)}/mapping`, { method: "PUT", body: { items: rows } });
    await loadMailImportJob(mailImportState.jobId, false);
    renderImportMailboxes();
  } catch (error) { showError(error); setBusy(button, false); }
}

async function cancelMailImport() {
  if (!(await confirmDialog("Cancel migration", "The import stops after the current batch. Messages already copied stay in the destination mailbox.", "Cancel migration", true))) return;
  try {
    await api(`mail-import/jobs/${encodeURIComponent(mailImportState.jobId)}/cancel`, { method: "POST" });
    await loadMailImportJob(mailImportState.jobId, false);
    renderImportMailboxes();
  } catch (error) { showError(error); }
}

async function loadMailImportJob(jobId, render = true) {
  try {
    const data = await api("mail-import/jobs/" + encodeURIComponent(jobId));
    mailImportState.jobId = jobId;
    mailImportState.job = data;
    mailImportState.items = data.items || [];
    if (render) renderImportMailboxes();
    return data;
  } catch (error) { showError(error); return null; }
}

function scheduleMailImportPoll() {
  if (!mailImportState.jobId) return;
  mailImportState.timer = setTimeout(async () => {
    if (state.view !== "import-mailboxes") return;
    await loadMailImportJob(mailImportState.jobId, false);
    renderImportMailboxes();
  }, 2000);
}

function downloadMailImportResults() {
  const rows = [["source_email", "target_email", "status", "imported_messages", "total_messages", "notify_email", "notified", "initial_password", "error"]];
  (mailImportState.items || []).forEach((item) => rows.push([item.sourceEmail, item.targetEmail, item.status, item.importedMessages, item.totalMessages, item.notifyEmail, item.notified ? "yes" : "no", item.initialPassword, item.error || item.authError]));
  downloadText("alt8mail-mailbox-migration-results.csv", rows.map((row) => row.map((value) => `"${String(value == null ? "" : value).replace(/"/g, '""')}"`).join(",")).join("\n"), "text/csv");
}

/* ------------------------------------------------------------
   Shared helpers
------------------------------------------------------------ */
async function safeAdminList(path) {
  try { return await api(path); } catch { return { jobs: [] }; }
}

function downloadText(filename, content, type) {
  const blob = new Blob(["﻿" + content], { type: type || "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
