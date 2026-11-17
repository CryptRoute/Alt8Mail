/* DOM references and shared in-app dialogs. Loaded after the document is parsed. */
import { $$, esc } from "./helpers.js";
import { currentLocale } from "./format.js";
import { t, tn } from "../../i18n.mjs";

/* ============================================================
   DOM
   ============================================================ */
export const app = document.getElementById("app");
export const loginScreen = document.getElementById("loginScreen");
export const loginForm = document.getElementById("loginForm");
export const loginEmail = document.getElementById("loginEmail");
export const loginPassword = document.getElementById("loginPassword");
export const loginError = document.getElementById("loginError");
export const loginHint = document.getElementById("loginHint");
export const loginSubmit = document.getElementById("loginSubmit");
export const ssoBtn = document.getElementById("ssoBtn");
export const samlBtn = document.getElementById("samlBtn");
export const ssoDivider = document.querySelector("#loginForm .login-divider");
export const searchInput = document.getElementById("searchInput");
export const searchClear = document.getElementById("searchClear");
export const searchWrap = document.getElementById("searchWrap");
export const searchHints = document.getElementById("searchHints");
export const messageList = document.getElementById("messageList");
export const reader = document.getElementById("reader");
export const readerContent = document.getElementById("readerContent");
export const readerScroll = document.getElementById("readerScroll");
export const listTitle = document.getElementById("listTitle");
export const listCount = document.getElementById("listCount");
export const folderList = document.getElementById("folderList");
export const statusRange = document.getElementById("statusRange");
export const statusMid = document.getElementById("statusMid");
export const peopleList = document.getElementById("peopleList");
export const peopleCount = document.getElementById("peopleCount");
export const personDetail = document.getElementById("personDetail");
export const composeLayer = document.getElementById("composeLayer");
export const composeDock = document.getElementById("composeDock");
export const composeDockTabs = document.getElementById("composeDockTabs");
export const attachInput = document.getElementById("attachInput");
export const toastEl = document.getElementById("toast");
export const helpOverlay = document.getElementById("helpOverlay");
export const scrim = document.getElementById("scrim");
export const mailView = document.getElementById("mailView");
export const peopleView = document.getElementById("peopleView");
export const calendarView = document.getElementById("calendarView");
export const settingsView = document.getElementById("settingsView");
export const calMain = document.getElementById("calMain");
export const calRangeLabel = document.getElementById("calRangeLabel");
export const calMini = document.getElementById("calMini");
export const calMiniLabel = document.getElementById("calMiniLabel");
export const calMyCalendars = document.getElementById("calMyCalendars");
export const calSharedCalendars = document.getElementById("calSharedCalendars");
export const calOtherCalendars = document.getElementById("calOtherCalendars");
export const calSharedHead = document.getElementById("calSharedHead");
export const calFilterMenu = document.getElementById("calFilterMenu");
export const calSearchInput = document.getElementById("calSearch");
export const calAgendaPane = document.getElementById("calAgendaPane");
export const agendaDate = document.getElementById("agendaDate");
export const agendaList = document.getElementById("agendaList");
export const agendaCount = document.getElementById("agendaCount");
export const notifyPop = document.getElementById("notifyPop");
export const notifyList = document.getElementById("notifyList");
export const notifyCount = document.getElementById("notifyCount");
export const notifySubtitle = document.getElementById("notifySubtitle");
export const noticeLayer = document.getElementById("noticeLayer");
export const noticeBody = document.getElementById("noticeBody");
export const noticeCount = document.getElementById("noticeCount");
export const bootSplash = document.getElementById("bootSplash");
export const composeAttachOverlay = document.getElementById("composeAttachOverlay");
export const composeAttachFileList = document.getElementById("composeAttachFileList");
export const composeAttachCurrentList = document.getElementById("composeAttachCurrentList");
export const composeAttachFileCount = document.getElementById("composeAttachFileCount");
export const composeAttachCurrentCount = document.getElementById("composeAttachCurrentCount");
export const aiReaderWraps = $$("[data-ai-wrap]");
export const aiReaderToggles = $$("[data-ai-toggle]");
export const aiReaderMenus = $$("[data-ai-menu]");
aiReaderMenus.forEach((menu) => { menu.aiOwner = menu.closest("[data-ai-wrap]"); });
export const newMenu = document.getElementById("newMenu");
export const moveMenu = document.getElementById("moveMenu");
export const detailsOverlay = document.getElementById("detailsOverlay");
export const detailsBody = document.getElementById("detailsBody");
export const eventOverlay = document.getElementById("eventOverlay");
export const bulkBar = document.getElementById("bulkBar");
export const bulkCount = document.getElementById("bulkCount");
export const selectAll = document.getElementById("selectAll");
export const searchScopeRow = document.getElementById("searchScopeRow");
export const searchSummary = document.getElementById("searchSummary");
export const statusPop = document.getElementById("statusPop");
export const statusPopBody = document.getElementById("statusPopBody");
export const statusPopTitle = document.getElementById("statusPopTitle");
export const offlineBanner = document.getElementById("offlineBanner");
export const twoFactorForm = document.getElementById("twoFactorForm");
export const twoFactorError = document.getElementById("twoFactorError");
export const twoFactorSubmit = document.getElementById("twoFactorSubmit");
export const usePasskeyBtn = document.getElementById("usePasskeyBtn");
export const passkeyOverlay = document.getElementById("passkeyOverlay");
export const passkeyPromptStatus = document.getElementById("passkeyPromptStatus");
export const passkeySetupOverlay = document.getElementById("passkeySetupOverlay");
export const totpSetupOverlay = document.getElementById("totpSetupOverlay");
export const totpDisableOverlay = document.getElementById("totpDisableOverlay");
export const tasksView = document.getElementById("tasksView");
export const filesView = document.getElementById("filesView");
export const tasksMeta = document.getElementById("tasksMeta");
export const taskStats = document.getElementById("taskStats");
export const taskFilters = document.getElementById("taskFilters");
export const taskList = document.getElementById("taskList");
export const taskDetail = document.getElementById("taskDetail");
export const scheduledView = document.getElementById("scheduledView");
export const scheduledMeta = document.getElementById("scheduledMeta");
export const scheduledSummary = document.getElementById("scheduledSummary");
export const scheduledList = document.getElementById("scheduledList");
export const scheduledZoneLabel = document.getElementById("scheduledZoneLabel");
export const scheduledQueueNote = document.getElementById("scheduledQueueNote");
export const scheduledEditOverlay = document.getElementById("scheduledEditOverlay");
export const scheduledEditOriginal = document.getElementById("scheduledEditOriginal");
export const scheduledEditTo = document.getElementById("scheduledEditTo");
export const scheduledEditCc = document.getElementById("scheduledEditCc");
export const scheduledEditSubject = document.getElementById("scheduledEditSubject");
export const scheduledEditBody = document.getElementById("scheduledEditBody");
export const scheduledEditPresets = document.getElementById("scheduledEditPresets");
export const scheduledEditDate = document.getElementById("scheduledEditDate");
export const scheduledEditTime = document.getElementById("scheduledEditTime");
export const scheduledEditInsight = document.getElementById("scheduledEditInsight");
export const scheduledEditError = document.getElementById("scheduledEditError");
export const scheduledEditAttachments = document.getElementById("scheduledEditAttachments");
export const scheduledEditSave = document.getElementById("scheduledEditSave");
export const sendNowOverlay = document.getElementById("sendNowOverlay");
export const sendNowSummary = document.getElementById("sendNowSummary");
export const sendNowConfirm = document.getElementById("sendNowConfirm");
export const filesMeta = document.getElementById("filesMeta");
export const fileFilters = document.getElementById("fileFilters");
export const fileBreadcrumb = document.getElementById("fileBreadcrumb");
export const cabinetFolderTree = document.getElementById("cabinetFolderTree");
export const cabinetProviderList = document.getElementById("cabinetProviderList");
export const cabinetQuota = document.getElementById("cabinetQuota");
export const cabinetDropOverlay = document.getElementById("cabinetDropOverlay");
export const cabinetDropTarget = document.getElementById("cabinetDropTarget");
export const fileSortSelect = document.getElementById("fileSortSelect");
export const fileGroupSelect = document.getElementById("fileGroupSelect");
export const fileViewSwitch = document.getElementById("fileViewSwitch");
export const fileSortDirection = document.getElementById("fileSortDirection");
export const fileContextMenu = document.getElementById("fileContextMenu");
export const folderOverlay = document.getElementById("folderOverlay");
export const fileActionOverlay = document.getElementById("fileActionOverlay");
export const calendarAddOverlay = document.getElementById("calendarAddOverlay");
export const calendarShareOverlay = document.getElementById("calendarShareOverlay");
export const calendarPollOverlay = document.getElementById("calendarPollOverlay");
export const calendarPollsOverlay = document.getElementById("calendarPollsOverlay");
export const fileProviderFilter = document.getElementById("fileProviderFilter");
export const fileList = document.getElementById("fileList");
export const fileDetail = document.getElementById("fileDetail");
export const fileUploadInput = document.getElementById("fileUploadInput");
export const scheduleOverlay = document.getElementById("scheduleOverlay");
export const previewOverlay = document.getElementById("previewOverlay");
export const reportOverlay = document.getElementById("reportOverlay");
export const reportMessageCard = document.getElementById("reportMessageCard");
export const reportNote = document.getElementById("reportNote");
export const reportSubmit = document.getElementById("reportSubmit");
export const readerFlagBtn = document.getElementById("readerFlagBtn");
export const readerPinBtn = document.getElementById("readerPinBtn");
export const readerPinLabel = document.getElementById("readerPinLabel");
export const readerSpamBtn = document.getElementById("readerSpamBtn");
export const readerSpamLabel = document.getElementById("readerSpamLabel");
export const readerSpamIcon = document.getElementById("readerSpamIcon");
export const storageOverlay = document.getElementById("storageOverlay");
export const storageCredentialsOverlay = document.getElementById("storageCredentialsOverlay");
export const storageCredentialsTabs = document.getElementById("storageCredentialsTabs");
export const storageCredentialFields = document.getElementById("storageCredentialFields");
export const storageCredentialStatus = document.getElementById("storageCredentialStatus");
export const confirmOverlay = document.getElementById("confirmOverlay");
export const domainBlockedOverlay = document.getElementById("domainBlockedOverlay");
export const onboardingOverlay = document.getElementById("onboardingOverlay");
export const signatureEditorOverlay = document.getElementById("signatureEditorOverlay");
export const signaturePickerOverlay = document.getElementById("signaturePickerOverlay");
export const spellcheckOverlay = document.getElementById("spellcheckOverlay");
export const identityEditorOverlay = document.getElementById("identityEditorOverlay");
export const folderMenu = document.getElementById("folderMenu");
export const messageMenu = document.getElementById("messageMenu");
export const htmlSourceOverlay = document.getElementById("htmlSourceOverlay");
export const quickActionsOverlay = document.getElementById("quickActionsOverlay");
export const labelEditorOverlay = document.getElementById("labelEditorOverlay");
export const folderEditorOverlay = document.getElementById("folderEditorOverlay");
export let confirmResolver = null;

export function appConfirm({ title = t("common.confirmAction"), message = "", confirmLabel = t("common.confirm"), cancelLabel = t("common.cancel"), tone = "default", icon = "i-alert" } = {}) {
  if (window.__a8mAutoConfirm) return Promise.resolve(true);
  if (confirmResolver) { confirmResolver(false); confirmResolver = null; }
  return new Promise((resolve) => {
    confirmResolver = resolve;
    document.getElementById("confirmTitle").textContent = title;
    document.getElementById("confirmMessage").textContent = message;
    const iconBox = document.getElementById("confirmIcon");
    if (iconBox) {
      iconBox.className = `confirm-icon${tone === "danger" ? " is-danger" : tone === "warning" ? " is-warning" : ""}`;
      iconBox.innerHTML = `<svg class="icon" aria-hidden="true"><use href="#${icon}"/></svg>`;
    }
    const accept = document.getElementById("confirmAccept");
    accept.textContent = confirmLabel;
    accept.className = `btn-primary${tone === "danger" ? " danger-btn" : ""}`;
    const cancel = document.getElementById("confirmCancel");
    cancel.textContent = cancelLabel;
    confirmOverlay.hidden = false;
    setTimeout(() => (tone === "danger" ? cancel : accept).focus(), 30);
  });
}

export let promptResolver = null;
export let promptReturnFocus = null;

export function ensurePromptOverlay() {
  let overlay = document.getElementById("promptOverlay");
  if (overlay) return overlay;
  document.body.insertAdjacentHTML("beforeend", `<div class="overlay prompt-overlay" id="promptOverlay" hidden>
    <div class="dialog prompt-dialog" role="dialog" aria-modal="true" aria-labelledby="promptTitle" aria-describedby="promptMessage">
      <div class="dialog-head"><div class="dialog-title-wrap"><span class="dialog-icon"><svg class="icon" aria-hidden="true"><use href="#i-pencil"/></svg></span><div><p class="eyebrow" id="promptEyebrow">Alt8Mail</p><h2 id="promptTitle">${t("common.enterValue")}</h2><p class="dialog-note" id="promptMessage"></p></div></div><button class="icon-btn" type="button" data-prompt-cancel aria-label="${esc(t("common.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
      <div class="dialog-body"><label class="field"><span id="promptLabel">${t("common.value")}</span><input class="input" id="promptInput" autocomplete="off" spellcheck="false"><select class="input" id="promptSelect" hidden></select><small id="promptHint"></small></label><p class="prompt-error" id="promptError" hidden></p></div>
      <div class="dialog-foot"><button class="btn-secondary" type="button" data-prompt-cancel id="promptCancel">${t("common.cancel")}</button><button class="btn-primary" type="button" id="promptSubmit">${t("common.save")}</button></div>
    </div>
  </div>`);
  overlay = document.getElementById("promptOverlay");
  overlay.addEventListener("click", (event) => {
    if (event.target === overlay || event.target.closest("[data-prompt-cancel]")) closeAppPrompt(null);
  });
  overlay.addEventListener("keydown", (event) => {
    if (event.key === "Escape") { event.preventDefault(); closeAppPrompt(null); }
    if (event.key === "Enter" && event.target.id !== "promptSelect") { event.preventDefault(); document.getElementById("promptSubmit")?.click(); }
  });
  document.getElementById("promptSubmit")?.addEventListener("click", () => {
    const input = document.getElementById("promptInput");
    const select = document.getElementById("promptSelect");
    const error = document.getElementById("promptError");
    const value = select.hidden ? input.value.trim() : select.value;
    const validate = overlay.__a8mValidate;
    const required = overlay.__a8mRequired !== false;
    const problem = typeof validate === "function" ? validate(value) : (required && !value ? t("common.valueRequired") : "");
    if (problem) {
      if (error) { error.hidden = false; error.textContent = problem; }
      (select.hidden ? input : select).focus();
      return;
    }
    closeAppPrompt(value);
  });
  return overlay;
}

export function appPrompt({ title = t("common.enterValue"), message = "", eyebrow = "Alt8Mail", label = t("common.value"), value = "", placeholder = "", type = "text", options = null, hint = "", confirmLabel = t("common.save"), cancelLabel = t("common.cancel"), required = true, validate = null } = {}) {
  const overlay = ensurePromptOverlay();
  if (promptResolver) { promptResolver(null); promptResolver = null; }
  promptReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  document.getElementById("promptEyebrow").textContent = eyebrow;
  document.getElementById("promptTitle").textContent = title;
  const messageNode = document.getElementById("promptMessage");
  messageNode.textContent = message || "";
  messageNode.hidden = !message;
  document.getElementById("promptLabel").textContent = label;
  const input = document.getElementById("promptInput");
  const select = document.getElementById("promptSelect");
  const hintNode = document.getElementById("promptHint");
  hintNode.textContent = hint || "";
  hintNode.hidden = !hint;
  if (Array.isArray(options) && options.length) {
    input.hidden = true;
    select.hidden = false;
    select.innerHTML = options.map((option) => {
      const item = typeof option === "string" ? { value: option, label: option } : option;
      return `<option value="${esc(item.value)}"${String(item.value) === String(value) ? " selected" : ""}>${esc(item.label)}</option>`;
    }).join("");
  } else {
    select.hidden = true;
    input.hidden = false;
    input.type = type;
    input.value = value;
    input.placeholder = placeholder;
    input.min = type === "number" ? "1" : "";
    input.max = type === "number" ? "90" : "";
  }
  overlay.__a8mValidate = validate;
  overlay.__a8mRequired = required;
  const error = document.getElementById("promptError");
  if (error) { error.hidden = true; error.textContent = ""; }
  document.getElementById("promptSubmit").textContent = confirmLabel;
  document.getElementById("promptCancel").textContent = cancelLabel;
  overlay.hidden = false;
  setTimeout(() => {
    (select.hidden ? input : select).focus();
    if (!select.hidden) select.focus();
    else if (input.value) input.select();
  }, 40);
  return new Promise((resolve) => { promptResolver = resolve; });
}

export function closeAppPrompt(value) {
  const overlay = document.getElementById("promptOverlay");
  if (overlay) overlay.hidden = true;
  const resolve = promptResolver;
  promptResolver = null;
  const returnFocus = promptReturnFocus;
  promptReturnFocus = null;
  if (returnFocus && returnFocus.isConnected) returnFocus.focus();
  if (resolve) resolve(value);
}

export let followUpResolver = null;
export let followUpReturnFocus = null;

export function ensureFollowUpOverlay() {
  let overlay = document.getElementById("followupOverlay");
  if (overlay) return overlay;
  document.body.insertAdjacentHTML("beforeend", `<div class="overlay followup-overlay" id="followupOverlay" hidden>
    <div class="dialog followup-dialog" role="dialog" aria-modal="true" aria-labelledby="followupTitle">
      <div class="dialog-head"><div class="dialog-title-wrap"><span class="dialog-icon"><svg class="icon" aria-hidden="true"><use href="#i-clock"/></svg></span><div><p class="eyebrow" id="followupEyebrow">${t("followup.eyebrow")}</p><h2 id="followupTitle">${t("followup.remind-title")}</h2><p class="dialog-note" id="followupMessage"></p></div></div><button class="icon-btn" type="button" data-followup-close aria-label="${esc(t("common.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
      <div class="dialog-body followup-body">
        <div class="followup-presets" role="group" aria-label="${esc(t("followup.when"))}">
          ${[1, 3, 7, 14, 30].map((days) => `<button class="followup-preset" type="button" data-followup-days-preset="${days}">${esc(tn("followup.days-count", days, { count: days }))}</button>`).join("")}
          <label class="followup-custom"><input id="followupDays" type="number" min="1" max="90" value="1" aria-label="${esc(t("followup.custom-days"))}"><span>${t("followup.days-label")}</span></label>
        </div>
        <div class="followup-note"><svg class="icon" aria-hidden="true"><use href="#i-clock"/></svg><span id="followupDueLabel">${t("followup.initial-due")}</span></div>
        <p class="prompt-error" id="followupError" hidden></p>
      </div>
      <div class="dialog-foot"><button class="btn-secondary danger-text" id="followupRemove" type="button" hidden>${t("followup.remove")}</button><span class="followup-foot-spacer"></span><button class="btn-secondary" type="button" data-followup-close>${t("common.cancel")}</button><button class="btn-primary" type="button" id="followupSave">${t("followup.schedule")}</button></div>
    </div>
  </div>`);
  overlay = document.getElementById("followupOverlay");
  overlay.addEventListener("click", (event) => {
    if (event.target === overlay || event.target.closest("[data-followup-close]")) closeFollowUpDialog(null);
    const preset = event.target.closest("[data-followup-days-preset]");
    if (preset) {
      const input = document.getElementById("followupDays");
      if (input) input.value = preset.getAttribute("data-followup-days-preset");
      updateFollowUpDialog();
    }
  });
  overlay.addEventListener("keydown", (event) => {
    if (event.key === "Escape") { event.preventDefault(); closeFollowUpDialog(null); }
    if (event.key === "Enter" && event.target.id !== "followupRemove") { event.preventDefault(); document.getElementById("followupSave")?.click(); }
  });
  document.getElementById("followupDays")?.addEventListener("input", updateFollowUpDialog);
  document.getElementById("followupSave")?.addEventListener("click", () => {
    const days = Math.max(1, Math.min(90, Number(document.getElementById("followupDays")?.value) || 0));
    if (!days) {
      const error = document.getElementById("followupError");
      if (error) { error.hidden = false; error.textContent = t("followup.invalid-days"); }
      return;
    }
    closeFollowUpDialog({ action: "save", days });
  });
  document.getElementById("followupRemove")?.addEventListener("click", () => closeFollowUpDialog({ action: "remove" }));
  return overlay;
}

export function updateFollowUpDialog() {
  const days = Math.max(1, Math.min(90, Number(document.getElementById("followupDays")?.value) || 1));
  $$("[data-followup-days-preset]").forEach((button) => {
    button.classList.toggle("is-active", Number(button.getAttribute("data-followup-days-preset")) === days);
  });
  const due = new Date(Date.now() + days * 86400000);
  const label = new Intl.DateTimeFormat(currentLocale(), { weekday: "long", month: "long", day: "numeric" }).format(due);
  const node = document.getElementById("followupDueLabel");
  if (node) node.textContent = t("followup.due-label", { date: label });
}

export function openFollowUpDialog({ kind = "awaiting_reply", days = 1, existing = false } = {}) {
  const overlay = ensureFollowUpOverlay();
  followUpReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const remind = kind === "remind_me";
  document.getElementById("followupEyebrow").textContent = remind ? t("followup.reply-reminder") : t("followup.eyebrow");
  document.getElementById("followupTitle").textContent = remind ? t("followup.remind-title") : t("followup.follow-up-title");
  document.getElementById("followupMessage").textContent = remind
    ? t("followup.remind-message")
    : t("followup.follow-up-message");
  document.getElementById("followupSave").textContent = existing ? t("followup.update") : t("followup.schedule");
  document.getElementById("followupRemove").hidden = !existing;
  document.getElementById("followupDays").value = String(days);
  const error = document.getElementById("followupError");
  if (error) { error.hidden = true; error.textContent = ""; }
  updateFollowUpDialog();
  overlay.hidden = false;
  setTimeout(() => { const input = document.getElementById("followupDays"); input?.focus(); input?.select?.(); }, 40);
  return new Promise((resolve) => { followUpResolver = resolve; });
}

export function closeFollowUpDialog(result) {
  const overlay = document.getElementById("followupOverlay");
  if (overlay) overlay.hidden = true;
  const resolve = followUpResolver;
  followUpResolver = null;
  const returnFocus = followUpReturnFocus;
  followUpReturnFocus = null;
  if (returnFocus && returnFocus.isConnected) returnFocus.focus();
  if (resolve) resolve(result);
}

export function settleConfirm(value) {
  if (!confirmResolver) return;
  const resolve = confirmResolver;
  confirmResolver = null;
  confirmOverlay.hidden = true;
  resolve(value);
}

export function openDomainBlockedDialog(details = {}) {
  const domain = details.domain || t("followup.your-sending-domain");
  const missing = Array.isArray(details.missing) ? details.missing : [];
  const message = document.getElementById("domainBlockedMessage");
  if (message) message.textContent = t("followup.domain-blocked", { domain, missing: missing.join(", ") || t("followup.dns-verification") });
  const records = details.records && typeof details.records === "object" ? details.records : {};
  const holder = document.getElementById("domainBlockedRecords");
  if (holder) {
    holder.innerHTML = Object.entries(records).map(([key, record]) => {
      if (!record || !record.name) return "";
      return `<div class="domain-record-row"><span class="domain-record-purpose">${esc(key)}</span><div><strong>${esc(record.type)}</strong><code>${esc(record.name)}</code><code>${esc(record.value || "")}</code></div></div>`;
    }).join("") || `<div class="settings-note">${esc(details.ownership?.name || "_a8m-verification." + domain)} TXT ${esc(details.ownership?.value || "a8m-verify=<token>")}</div>`;
  }
  domainBlockedOverlay.hidden = false;
}
export const shareOverlay = document.getElementById("shareOverlay");
export const recoveryOverlay = document.getElementById("recoveryOverlay");
export const scheduledChip = document.getElementById("scheduledChip");
export const readerPopout = document.getElementById("readerPopout");
export const languageSelect = document.getElementById("languageSelect");
export const dateFormatSelect = document.getElementById("dateFormatSelect");
export const timeZoneSelect = document.getElementById("timeZoneSelect");
export const weekStartSelect = document.getElementById("weekStartSelect");
export const workStartInput = document.getElementById("workStartInput");
export const workEndInput = document.getElementById("workEndInput");
export const workdayRow = document.getElementById("workdayRow");
export const regionalPreview = document.getElementById("regionalPreview");
export const defaultFromSelect = document.getElementById("defaultFromSelect");
export const autoReplyToggle = document.getElementById("autoReplyToggle");
export const autoReplyRangeToggle = document.getElementById("autoReplyRangeToggle");
export const autoReplyRangeFields = document.getElementById("autoReplyRangeFields");
export const autoReplyStart = document.getElementById("autoReplyStart");
export const autoReplyEnd = document.getElementById("autoReplyEnd");
export const autoReplyDays = document.getElementById("autoReplyDays");
export const autoReplyExternalToggle = document.getElementById("autoReplyExternalToggle");
export const autoReplyInternalSubject = document.getElementById("autoReplyInternalSubject");
export const autoReplyInternalMessage = document.getElementById("autoReplyInternalMessage");
export const autoReplyExternalSubject = document.getElementById("autoReplyExternalSubject");
export const autoReplyExternalMessage = document.getElementById("autoReplyExternalMessage");
export const autoReplyStatus = document.getElementById("autoReplyStatus");
export const autoReplyPreview = document.getElementById("autoReplyPreview");
export const twoFactorCard = document.getElementById("twoFactorCard");
export const twoFactorStatus = document.getElementById("twoFactorStatus");
export const twoFactorDetail = document.getElementById("twoFactorDetail");
export const twoFactorMethods = document.getElementById("twoFactorMethods");
export const recoveryCodeList = document.getElementById("recoveryCodeList");
export const shareResult = document.getElementById("shareResult");
export const shareUrl = document.getElementById("shareUrl");
export const shareFileName = document.getElementById("shareFileName");
export const storageConnectList = document.getElementById("storageConnectList");
export const scheduleMessageCard = document.getElementById("scheduleMessageCard");
export const schedulePresets = document.getElementById("schedulePresets");
export const scheduleDate = document.getElementById("scheduleDate");
export const scheduleTime = document.getElementById("scheduleTime");
export const scheduleZoneInfo = document.getElementById("scheduleZoneInfo");
export const scheduleZoneHint = document.getElementById("scheduleZoneHint");
export const scheduleInsight = document.getElementById("scheduleInsight");
export const scheduleWarning = document.getElementById("scheduleWarning");
export const scheduleConfirm = document.getElementById("scheduleConfirm");
export const previewTitle = document.getElementById("previewTitle");
export const previewMeta = document.getElementById("previewMeta");
export const previewBody = document.getElementById("previewBody");
export const previewDownload = document.getElementById("previewDownload");
export const previewOpen = document.getElementById("previewOpen");
export const previewToolbar = document.getElementById("previewToolbar");
export const previewZoomLabel = document.getElementById("previewZoomLabel");
export const previewDimensions = document.getElementById("previewDimensions");
export const previewFootNote = document.getElementById("previewFootNote");
export const previewNav = document.getElementById("previewNav");
export const previewPrev = document.getElementById("previewPrev");
export const previewNext = document.getElementById("previewNext");
export const previewCounter = document.getElementById("previewCounter");
export const aiAttachmentOverlay = document.getElementById("aiAttachmentOverlay");
export const aiAttachmentList = document.getElementById("aiAttachmentList");
export const aiAttachmentSubject = document.getElementById("aiAttachmentSubject");
export const aiAttachmentMessage = document.getElementById("aiAttachmentMessage");
export const aiAttachSelectAll = document.getElementById("aiAttachSelectAll");
export const aiAttachmentCount = document.getElementById("aiAttachmentCount");
export const aiAttachmentBudgetLabel = document.getElementById("aiAttachmentBudgetLabel");
export const aiAttachmentMeter = document.getElementById("aiAttachmentMeter");
export const aiAttachmentBar = document.getElementById("aiAttachmentBar");
export const aiAttachmentHint = document.getElementById("aiAttachmentHint");
export const aiAttachmentConfirm = document.getElementById("aiAttachmentConfirm");

export const isMobile = () => window.matchMedia("(max-width: 719px)").matches;
export const isDrawer = () => window.matchMedia("(max-width: 1119px)").matches;
