/* Message details, raw source and sender trust. */
import { api } from "../core/api.js";
import { detailsBody, detailsOverlay } from "../core/dom.js";
import { formatBytes, formatFullWhen } from "../core/format.js";
import { esc, normalizeEmail } from "../core/helpers.js";
import { state } from "../core/state.js";
import { showError, showToast } from "../core/toast.js";
import { t, tn } from "../../i18n.mjs";

let hooks = {
  messageById: () => null, tenantExternalTag: () => false,
  folderDef: () => ({ label: "" }), rebuildIndex: () => {},
  renderList: () => {}, renderReader: () => {}
};
export function configureDetails(next = {}) { hooks = { ...hooks, ...next }; }

export let detailsCurrentId = "";
export let detailsSourceMessage = null;

export function openDetails(messageOrId) {
  const message = typeof messageOrId === "string" ? hooks.messageById?.(messageOrId) : messageOrId;
  if (!message) return;
  const auth = message.auth || {};
  const showExternalTag = hooks.tenantExternalTag?.();
  const externalLabel = showExternalTag ? t("mail.details.externalSender") : t("mail.details.senderVerification");
  const rows = [
    [t("mail.details.from"), `${message.from.name} <${message.from.email}>`],
    [t("mail.details.to"), (message.to || []).map((person) => `${person.name} <${person.email}>`).join(", ") || "—"],
    [t("mail.details.date"), formatFullWhen(message.date)],
    [t("mail.details.size"), formatBytes(message.sizeBytes)],
    [t("mail.details.folder"), hooks.folderDef?.(message.folder).label],
    [t("mail.details.classification"), message.external ? (showExternalTag ? t("mail.details.notAppliedExternal") : t("mail.details.hiddenPolicy")) : (message.classification || t("mail.details.internal"))],
    [t("mail.details.senderIp"), auth.ip || "—"],
    [t("mail.details.tls"), auth.tls || "TLS 1.3"],
    [t("mail.details.signingDomain"), auth.signer || "—"],
    [t("mail.details.retention"), t("mail.details.seePolicy")]
  ];
  const checks = [
    ["SPF", auth.spf || "none", t("mail.details.spfHost")],
    ["DKIM", auth.dkim || "none", auth.signer ? t("mail.details.signatureDomain", { domain: auth.signer }) : t("mail.details.noSignature")],
    ["DMARC", auth.dmarc || "none", message.external ? t("mail.details.dmarcExternal") : t("mail.details.dmarcInternal")]
  ];
  detailsCurrentId = message.id;
  detailsSourceMessage = null;
  detailsBody.closest(".dialog")?.classList.remove("is-source");
  detailsBody.innerHTML = `<div class="details-tabs" role="tablist">
      <button class="details-tab is-active" data-details-tab="details" type="button" role="tab" aria-selected="true">${esc(t("mail.details.tab.details"))}</button>
      <button class="details-tab" data-details-tab="source" type="button" role="tab" aria-selected="false">${esc(t("mail.details.tab.source"))}</button>
      <span class="details-tabs-spacer"></span>
      <button class="details-source-action" data-copy-headers type="button" hidden>${esc(t("mail.details.copyHeaders"))}</button>
      <button class="details-source-action" data-download-source type="button" hidden>${esc(t("mail.details.download"))}</button>
    </div>
    <div data-details-panel="details">
    <div class="details-security">
      <div class="details-security-head">
        <span class="trust-shield ${showExternalTag && message.external && !message.trusted ? "is-warn" : ""}"><svg class="icon" aria-hidden="true"><use href="#${showExternalTag && message.external ? "i-alert" : "i-shield-check"}"/></svg></span>
        <div><strong>${message.external ? (showExternalTag ? (message.trusted ? t("mail.details.trustedExternal") : t("mail.details.externalSender")) : (message.trusted ? t("mail.details.trustedVerified") : externalLabel)) : t("mail.details.verifiedInternal")}</strong><span>${esc(message.external ? t("mail.details.evaluated") : t("mail.details.verifiedDirectory"))}</span></div>
      </div>
      <div class="auth-grid">${checks.map(([name, result, detail]) => `<div class="auth-card ${result === "pass" ? "pass" : result === "fail" || result === "softfail" ? "fail" : "none"}"><span class="auth-name">${name}</span><strong>${esc(result)}</strong><span>${esc(detail)}</span></div>`).join("")}</div>
      ${(auth.warnings || []).length ? `<ul class="details-warnings">${auth.warnings.map((warning) => `<li>${esc(warning)}</li>`).join("")}</ul>` : ""}
      ${message.external && showExternalTag ? `<div class="details-trust-actions"><button class="btn-secondary" data-trust-toggle="${esc(message.id)}" data-trust-value="${message.trusted ? "false" : "true"}" type="button">${esc(message.trusted ? t("mail.details.removeTrust") : t("mail.details.markTrusted"))}</button><span>${esc(t("mail.details.trustHint"))}</span></div>` : ""}
    </div>
    <div class="trust-explanation" data-trust-explanation><span class="muted">${esc(t("mail.details.buildingExplanation"))}</span></div>
    <div class="details-grid">${rows.map(([key, value]) => `<div class="details-row"><dt>${esc(key)}</dt><dd>${esc(value)}</dd></div>`).join("")}</div>
    <p class="details-note">${esc(t("mail.details.transportNote"))}</p>
    </div>
    <div data-details-panel="source" hidden>
      <div class="source-meta"><span>${esc(t("mail.details.sourceMeta"))}</span><span data-source-meta>${esc(t("mail.details.waiting"))}</span></div>
      <p class="source-note" data-source-note hidden></p>
      <pre class="source-view" data-source-view>${esc(t("mail.details.sourceHint"))}</pre>
    </div>`;
  detailsOverlay.hidden = false;
  loadTrustExplanation(message.id);
  setTimeout(() => document.getElementById("detailsClose").focus(), 30);
}

export async function loadTrustExplanation(id) {
  const host = detailsBody.querySelector("[data-trust-explanation]");
  if (!host) return;
  try {
    const data = await api(`/api/messages/${encodeURIComponent(id)}/trust-explanation`);
    if (detailsCurrentId !== id) return;
    const explanation = Array.isArray(data.explanation) ? data.explanation : [];
    host.innerHTML = `<div class="trust-explanation-head">
        <span class="trust-explanation-icon"><svg class="icon" aria-hidden="true"><use href="#${data.junk ? "i-alert" : "i-shield-check"}"/></svg></span>
        <div><strong>${esc(data.title || (data.junk ? t("mail.details.whyJunk") : t("mail.details.whySafe")))}</strong><span>${esc(data.trusted ? t("mail.details.trustedSender") : t("mail.details.basedOnHistory"))}</span></div>
      </div>
      <ul>${explanation.map((line) => `<li>${esc(line)}</li>`).join("")}</ul>`;
  } catch (error) {
    if (detailsCurrentId !== id) return;
    host.innerHTML = `<span class="muted">${esc(error.message || t("mail.details.explanationFailed"))}</span>`;
  }
}

export function closeDetails() {
  detailsOverlay.hidden = true;
  detailsSourceMessage = null;
  detailsCurrentId = "";
}

export function selectDetailsTab(tab) {
  detailsBody.closest(".dialog")?.classList.toggle("is-source", tab === "source");
  detailsBody.querySelectorAll("[data-details-tab]").forEach((button) => {
    const active = button.getAttribute("data-details-tab") === tab;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  detailsBody.querySelectorAll("[data-details-panel]").forEach((panel) => {
    panel.hidden = panel.getAttribute("data-details-panel") !== tab;
  });
  detailsBody.querySelectorAll("[data-copy-headers], [data-download-source]").forEach((button) => {
    button.hidden = tab !== "source";
  });
  if (tab === "source") loadDetailsSource();
}

export async function loadDetailsSource() {
  const view = detailsBody.querySelector("[data-source-view]");
  const meta = detailsBody.querySelector("[data-source-meta]");
  const note = detailsBody.querySelector("[data-source-note]");
  if (!view || !detailsCurrentId) return;
  if (detailsSourceMessage && detailsSourceMessage.id === detailsCurrentId) {
    renderDetailsSource(view, meta, note);
    return;
  }
  view.textContent = t("mail.details.loadingSource");
  if (meta) meta.textContent = t("mail.details.fetchingSource");
  if (note) note.hidden = true;
  try {
    const data = await api(`/api/messages/${encodeURIComponent(detailsCurrentId)}/source`);
    detailsSourceMessage = { id: detailsCurrentId, ...data };
    renderDetailsSource(view, meta, note);
  } catch (error) {
    view.textContent = error && error.message ? error.message : t("mail.details.sourceUnavailable");
    if (meta) meta.textContent = t("mail.details.unavailable");
  }
}

export function renderDetailsSource(view, meta, note) {
  const data = detailsSourceMessage;
  if (!view || !data) return;
  view.textContent = data.source || data.headers || "";
  if (meta) {
    const size = formatBytes(Number(data.sizeBytes) || 0);
    meta.textContent = `${tn("mail.details.headersAndSize", data.headerCount || 0, { count: data.headerCount || 0, size })}${data.truncated ? ` · ${t("mail.details.previewCapped")}` : ""}`;
  }
  if (note) {
    note.hidden = !data.note;
    note.textContent = data.note || "";
  }
}

export async function copyDetailsHeaders() {
  if (!detailsSourceMessage) return;
  try {
    await navigator.clipboard.writeText(detailsSourceMessage.headers || "");
    showToast(t("mail.details.headersCopied"));
  } catch {
    showToast(t("mail.details.clipboardDenied"));
  }
}

export function downloadDetailsSource() {
  if (!detailsSourceMessage) return;
  const message = hooks.messageById?.(detailsCurrentId);
  const subject = String(message?.subject || "message").replace(/[\\/:*?"<>|]+/g, "-").slice(0, 80);
  const blob = new Blob([detailsSourceMessage.source || detailsSourceMessage.headers || ""], { type: "message/rfc822" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `${subject || "message"}.eml`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
  showToast(t("mail.details.downloaded"));
}

export async function toggleTrust(messageId, trusted) {
  try {
    await api(`/api/messages/${encodeURIComponent(messageId)}/trust`, { method: "POST", body: { trusted } });
    const message = hooks.messageById?.(messageId);
    if (message) {
      const email = normalizeEmail(message.from.email);
      state.messages.forEach((item) => {
        if (normalizeEmail(item.from && item.from.email) === email) item.trusted = trusted;
      });
    }
    hooks.rebuildIndex?.();
    hooks.renderList?.();
    hooks.renderReader?.({ preserveScroll: true });
    if (!detailsOverlay.hidden) openDetails(messageId);
    showToast(trusted ? t("mail.details.trustedToast") : t("mail.details.untrustedToast"));
  } catch (error) {
    showError(error);
  }
}
