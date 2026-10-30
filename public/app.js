import { MailIndex, buildThreads, parseQuery, stripHtml } from "./search.mjs";
import { initI18n, setLocale, t, applyStaticTranslations, populateLanguageSelect } from "./i18n.mjs";

"use strict";

/* ============================================================
   helpers
   ============================================================ */
const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
const esc = (value) => String(value == null ? "" : value).replace(/[&<>"']/g, (ch) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[ch]));
const uid = (prefix) => `${prefix}_${(crypto.randomUUID ? crypto.randomUUID() : Math.random().toString(36).slice(2)).replace(/-/g, "").slice(0, 18)}`;
const clone = (value) => structuredClone(value);
const escapeRegExp = (value) => String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

const avatar = (person, size = "md") => {
  const p = person || { initials: "?", color: "#64748b" };
  return `<span class="avatar avatar-${size}" style="--av:${esc(p.color || "#64748b")}" aria-hidden="true">${esc(p.initials || "?")}</span>`;
};

const initialsFrom = (value) => {
  const parts = String(value || "").trim().split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  return String(value || "?").slice(0, 2).toUpperCase();
};

const personLabel = (person) => person ? (person.name || person.email) : "Unknown sender";
const recipientLabel = (message) => {
  const list = Array.isArray(message.to) ? message.to : message.to ? [message.to] : [];
  if (!list.length) return "no recipient";
  if (list.length === 1) return list[0].name || list[0].email;
  return `${list[0].name || list[0].email} +${list.length - 1}`;
};

const normalizeEmail = (email) => String(email || "").trim().toLowerCase();

const SAFE_EMAIL_STYLE_PROPERTIES = new Set([
  "color", "background-color", "background", "font-family", "font-size", "font-weight", "font-style",
  "font-variant", "text-decoration", "text-align", "text-indent", "line-height", "letter-spacing", "vertical-align",
  "margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
  "padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
  "border", "border-top", "border-right", "border-bottom", "border-left",
  "border-color", "border-width", "border-style", "border-radius", "border-collapse", "border-spacing",
  "width", "max-width", "height", "max-height", "display", "white-space", "word-break",
  "overflow-wrap", "list-style-type", "text-transform", "table-layout", "opacity",
]);

function safeEmailStyle(value) {
  const declarations = [];
  for (const part of String(value || "").split(";")) {
    const index = part.indexOf(":");
    if (index < 0) continue;
    const property = part.slice(0, index).trim().toLowerCase();
    const declaration = part.slice(index + 1).trim();
    if (!SAFE_EMAIL_STYLE_PROPERTIES.has(property) || !declaration) continue;
    if (/url\s*\(|expression\s*\(|@import|[<>]/i.test(declaration)) continue;
    declarations.push(`${property}: ${declaration}`);
  }
  return declarations.join("; ");
}

function emailImageProxyURL(source) {
  return `/api/mail/image?url=${encodeURIComponent(source)}`;
}

function emailImageSource(source) {
  const value = String(source || "").trim();
  if (!value) return "";
  if (/^cid:/i.test(value)) return "";
  if (/^data:image\/(?:png|jpe?g|gif|webp|avif|bmp|x-icon)/i.test(value)) return value;
  if (/^https?:\/\//i.test(value)) return emailImageProxyURL(value);
  if (/^\/\//.test(value)) return emailImageProxyURL(`https:${value}`);
  return "";
}

function cidImageMap(message) {
  const map = new Map();
  for (const attachment of (message?.attachments || [])) {
    if (!attachment.contentId || !String(attachment.mime || "").startsWith("image/")) continue;
    const key = String(attachment.contentId).replace(/^<|>$/g, "").toLowerCase();
    if (key && attachment.contentUrl) map.set(key, attachment.contentUrl);
  }
  return map;
}

function safeEmailHtml(html, { message = null, allowImages = true } = {}) {
  const template = document.createElement("template");
  template.innerHTML = String(html || "");
  template.content.querySelectorAll("script, style, iframe, object, embed, form, link, meta, base, svg, math").forEach((node) => node.remove());
  const cidImages = cidImageMap(message);
  template.content.querySelectorAll("*").forEach((node) => {
    for (const attribute of Array.from(node.attributes)) {
      const name = attribute.name.toLowerCase();
      if (name.startsWith("on") || name === "srcdoc" || name === "formaction" || name === "srcset") node.removeAttribute(attribute.name);
      if (name === "style") {
        const safe = safeEmailStyle(attribute.value);
        if (safe) node.setAttribute("style", safe);
        else node.removeAttribute(attribute.name);
      }
      if ((name === "href" || name === "src") && /^\s*javascript:/i.test(attribute.value)) node.removeAttribute(attribute.name);
    }
    if (node.tagName === "A") {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer nofollow");
    }
    if (node.tagName === "IMG") {
      const source = node.getAttribute("src") || "";
      if (source) node.setAttribute("data-email-src", source);
      let cidKey = /^cid:/i.test(source) ? source.slice(4).replace(/^<|>$/g, "").toLowerCase() : "";
      if (cidKey.includes("%")) {
        try { cidKey = decodeURIComponent(cidKey); } catch {}
      }
      const cidURL = cidKey ? cidImages.get(cidKey) : "";
      node.removeAttribute("width");
      node.removeAttribute("height");
      node.setAttribute("decoding", "async");
      node.setAttribute("referrerpolicy", "no-referrer");
      node.setAttribute("class", "email-image");
      if (cidURL) {
        node.setAttribute("src", cidURL);
      } else if (allowImages) {
        const proxied = emailImageSource(source);
        if (proxied) node.setAttribute("src", proxied);
        else {
          node.setAttribute("data-blocked-src", source);
          node.setAttribute("src", "data:image/gif;base64,R0lGODlhAQABAAD/ACwAAAAAAQABAAACADs=");
          node.classList.add("is-blocked-image");
        }
      } else {
        node.setAttribute("data-blocked-src", source);
        node.setAttribute("src", "data:image/gif;base64,R0lGODlhAQABAAD/ACwAAAAAAQABAAACADs=");
        node.classList.add("is-blocked-image");
      }
    }
  });
  return template.innerHTML;
}

function emailHasImages(html) {
  const template = document.createElement("template");
  template.innerHTML = String(html || "");
  return Boolean(template.content.querySelector("img"));
}

function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value >= 1024 ** 3) {
    const gb = value / 1024 ** 3;
    const text = gb >= 10 ? gb.toFixed(1) : gb.toFixed(2);
    return `${text.replace(/\.0+$/, "").replace(/(\.\d)0$/, "$1")} GB`;
  }
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1).replace(/\.0$/, "")} MB`;
  if (value >= 1024) return `${Math.round(value / 1024)} KB`;
  return `${value} B`;
}

function currentLocale() {
  return (state.preferences && state.preferences.language) || DEFAULT_PREFERENCES.language;
}

function currentTimeZone() {
  return (state.preferences && state.preferences.timeZone) || DEFAULT_PREFERENCES.timeZone;
}

function timeZoneParts(value, timeZone = currentTimeZone()) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  const formatter = new Intl.DateTimeFormat("en-CA", {
    timeZone,
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false
  });
  const parts = {};
  for (const part of formatter.formatToParts(date)) {
    if (part.type !== "literal") parts[part.type] = part.value;
  }
  let hour = Number(parts.hour);
  if (hour === 24) hour = 0;
  return { year: Number(parts.year), month: Number(parts.month), day: Number(parts.day), hour, minute: Number(parts.minute), second: Number(parts.second) };
}

function dateKeyInZone(value, timeZone = currentTimeZone()) {
  const parts = timeZoneParts(value, timeZone);
  return parts ? partsToKey(parts) : "";
}

function partsToKey(parts) {
  return `${parts.year}-${String(parts.month).padStart(2, "0")}-${String(parts.day).padStart(2, "0")}`;
}

function keyToParts(key) {
  const match = String(key || "").match(/^(\d{4})-(\d{2})-(\d{2})$/);
  return match ? { year: Number(match[1]), month: Number(match[2]), day: Number(match[3]) } : null;
}

function addDaysToKey(key, days) {
  const parts = keyToParts(key);
  if (!parts) return key;
  const date = new Date(Date.UTC(parts.year, parts.month - 1, parts.day + days, 12));
  return `${date.getUTCFullYear()}-${String(date.getUTCMonth() + 1).padStart(2, "0")}-${String(date.getUTCDate()).padStart(2, "0")}`;
}

function weekdayOfKey(key) {
  const parts = keyToParts(key);
  if (!parts) return 0;
  const day = new Date(Date.UTC(parts.year, parts.month - 1, parts.day, 12)).getUTCDay();
  return day === 0 ? 7 : day;
}

function timeZoneOffsetMs(date, timeZone = currentTimeZone()) {
  const parts = timeZoneParts(date, timeZone);
  if (!parts) return 0;
  const asUTC = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
  return asUTC - Math.floor(date.getTime() / 1000) * 1000;
}

function zonedWallTimeToUtc(dateKey, time, timeZone = currentTimeZone()) {
  const parts = keyToParts(dateKey);
  if (!parts) return null;
  const [hours, minutes] = String(time || "09:00").split(":").map(Number);
  const desired = Date.UTC(parts.year, parts.month - 1, parts.day, hours || 0, minutes || 0, 0);
  let stamp = desired;
  for (let index = 0; index < 3; index += 1) {
    const offset = timeZoneOffsetMs(new Date(stamp), timeZone);
    const next = desired - offset;
    if (Math.abs(next - stamp) < 1000) { stamp = next; break; }
    stamp = next;
  }
  return new Date(stamp);
}

function formatClock(value) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(currentLocale(), {
    hour: "numeric",
    minute: "2-digit",
    hour12: state.preferences?.timeFormat !== "24h",
    timeZone: currentTimeZone()
  }).format(date);
}

function formatListWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const key = dateKeyInZone(date);
  const today = dateKeyInZone(new Date());
  if (key === today) return formatClock(date);
  if (key === addDaysToKey(today, -1)) return "Yesterday";
  const todayParts = keyToParts(today);
  const dateParts = keyToParts(key);
  const dayDiff = Math.round((Date.UTC(todayParts.year, todayParts.month - 1, todayParts.day) - Date.UTC(dateParts.year, dateParts.month - 1, dateParts.day)) / 86400000);
  if (dayDiff >= 0 && dayDiff < 7) return new Intl.DateTimeFormat(currentLocale(), { weekday: "short", timeZone: currentTimeZone() }).format(date);
  if (dateParts.year === todayParts.year) return new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", year: "numeric", timeZone: currentTimeZone() }).format(date);
}

function formatDateOnly(value) {
  let key = "";
  let date = null;
  if (typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value)) {
    key = value;
    const parts = keyToParts(key);
    date = new Date(Date.UTC(parts.year, parts.month - 1, parts.day, 12));
  } else {
    date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";
    key = dateKeyInZone(date);
  }
  const format = state.preferences?.dateFormat || "auto";
  if (format === "ymd") return key;
  if (format === "dmy") return new Intl.DateTimeFormat(currentLocale(), { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" }).format(date);
  return new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" }).format(date);
}

function formatFullWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const key = dateKeyInZone(date);
  const today = dateKeyInZone(new Date());
  if (key === today) return `Today at ${formatClock(date)}`;
  if (key === addDaysToKey(today, -1)) return `Yesterday at ${formatClock(date)}`;
  const dateText = new Intl.DateTimeFormat(currentLocale(), { month: "long", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return `${dateText} at ${formatClock(date)}`;
}

function formatAuditWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const dateText = new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return `${dateText}, ${formatClock(date)}`;
}

function formatDateTimeFull(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const dateText = new Intl.DateTimeFormat(currentLocale(), {
    weekday: "short", month: "long", day: "numeric", year: "numeric",
    timeZone: currentTimeZone()
  }).format(date);
  return `${dateText} · ${formatClock(date)} ${formatTimeZoneShort(currentTimeZone(), date)}`;
}

function formatTimeZoneShort(timeZone = currentTimeZone(), date = new Date()) {
  try {
    const part = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "short" }).formatToParts(date).find((item) => item.type === "timeZoneName");
    return part ? part.value : timeZone;
  } catch {
    return timeZone;
  }
}

function formatTimeZoneLabel(timeZone = currentTimeZone(), date = new Date()) {
  const offset = timeZoneOffsetMs(date, timeZone);
  const sign = offset >= 0 ? "+" : "-";
  const minutes = Math.abs(Math.round(offset / 60000));
  return `${timeZone.replace(/_/g, " ")} · UTC${sign}${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")} · ${formatTimeZoneShort(timeZone, date)}`;
}

function formatRelativeTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const diff = date.getTime() - Date.now();
  const abs = Math.abs(diff);
  let unit = "minute";
  let divisor = 60000;
  if (abs >= 86400000) { unit = "day"; divisor = 86400000; }
  else if (abs >= 3600000) { unit = "hour"; divisor = 3600000; }
  return new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" }).format(Math.round(diff / divisor), unit);
}

function startOfDay(date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function parseDate(value) {
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function formatCountdown(ms) {
  return `${Math.max(0, Math.ceil(ms / 1000))}s`;
}

function htmlToText(html) {
  const div = document.createElement("div");
  div.innerHTML = String(html || "");
  return (div.textContent || "").replace(/\s+/g, " ").trim();
}

function textToEditorHTML(text) {
  const value = String(text || "");
  return value.split(/\n{2,}/).map((paragraph) =>
    `<p>${esc(paragraph).replace(/\n/g, "<br>")}</p>`
  ).join("") || "";
}

function sanitizeEditorHTML(raw) {
  const template = document.createElement("template");
  template.innerHTML = String(raw || "");
  template.content.querySelectorAll("script, style, iframe, object, embed, form, input, textarea, button, meta, link, svg, math").forEach((node) => node.remove());
  template.content.querySelectorAll("*").forEach((node) => {
    for (const attr of Array.from(node.attributes || [])) {
      const name = attr.name.toLowerCase();
      if (name.startsWith("on") || name === "srcdoc" || name === "formaction") node.removeAttribute(attr.name);
      if ((name === "href" || name === "src") && /^\s*javascript:/i.test(attr.value)) node.removeAttribute(attr.name);
    }
  });
  return template.innerHTML;
}

function editorPlainText(html) {
  const div = document.createElement("div");
  div.innerHTML = String(html || "");
  return (div.innerText || div.textContent || "").replace(/ /g, " ").trim();
}

const CATEGORIES = {
  decision: { label: "Decision", color: "var(--cat-decision)" },
  product: { label: "Product", color: "var(--cat-product)" },
  finance: { label: "Finance", color: "var(--cat-finance)" },
  legal: { label: "Legal", color: "var(--cat-legal)" },
  people: { label: "People", color: "var(--cat-people)" },
  analytics: { label: "Analytics", color: "var(--cat-analytics)" },
  sales: { label: "Sales", color: "var(--cat-sales)" }
};

const ACCENTS = {
  blue: { label: "Blue", value: "#0f6cbd" },
  teal: { label: "Teal", value: "#0f766e" },
  violet: { label: "Violet", value: "#6346d8" },
  rose: { label: "Rose", value: "#b8336a" }
};

const FOLDER_LABELS = { inbox: "Inbox", starred: "Starred", sent: "Sent", drafts: "Drafts", archive: "Archive", spam: "Spam", trash: "Trash" };

const AUDIT_LABELS = {
  "auth.login": "Sign-in",
  "auth.logout": "Sign-out",
  "auth.failed": "Failed sign-in",
  "send.queued": "Send queued",
  "send.committed": "Message sent",
  "send.cancelled": "Send undone",
  "message.archive": "Archive",
  "message.move": "Move",
  "message.delete": "Delete",
  "message.markRead": "Marked read",
  "message.markUnread": "Marked unread",
  "message.star": "Starred",
  "message.unstar": "Unstarred",
  "message.flag": "Flagged",
  "message.unflag": "Flag cleared",
  "message.spam": "Marked spam",
  "message.notSpam": "Moved to Inbox",
  "message.report": "Reported to security",
  "message.undo": "Undo",
  "message.received": "Received",
  "draft.created": "Draft started",
  "draft.deleted": "Draft discarded",
  "rule.saved": "Rules updated",
  "trust.marked": "Sender trusted",
  "trust.unmarked": "Trust removed",
  "send.scheduled": "Send scheduled",
  "send.rescheduled": "Schedule updated",
  "send.sent_now": "Sent ahead of schedule",
  "auth.challenge": "Second factor requested",
  "auth.2fa": "Two-factor verified",
  "auth.2fa.failed": "Two-factor failed",
  "task.created": "Task created",
  "task.updated": "Task updated",
  "task.completed": "Task completed",
  "task.reopened": "Task reopened",
  "task.deleted": "Task deleted",
  "file.created": "File added",
  "file.updated": "File updated",
  "file.shared": "File shared",
  "file.deleted": "File deleted",
  "storage.connected": "Storage connected",
  "storage.disconnected": "Storage disconnected",
  "attachment.uploaded": "Attachment uploaded",
  "preferences.updated": "Preferences updated",
  "security.recovery_regenerated": "Recovery codes regenerated",
  "demo.reset": "Demo reset"
};

const DEFAULT_PREFERENCES = {
  language: "en-US",
  timeFormat: "12h",
  dateFormat: "auto",
  timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  weekStart: "monday",
  workStart: "09:00",
  workEnd: "17:00",
  workDays: [1, 2, 3, 4, 5],
  defaultFrom: "",
  signatureEnabled: false,
  signature: "",
  saveCopyEnabled: true,
  spellCheckEnabled: true,
  autoLoadImages: true,
  autoAdvance: false,
  theme: "system",
  accent: "blue",
  density: "comfortable",
  markReadOnOpen: true,
  inboxView: "grouped",
  defaultPriority: "normal",
  defaultReceipt: false,
  undoSendSeconds: 8,
  notifyDesktop: true,
  notifySound: false,
  notifyCalendar: true,
  externalWarnings: true,
  composeMode: "html",
  composeWidth: "wide",
  composePosition: "center"
};

/* ============================================================
   preferences
   ============================================================ */
const prefs = {
  get(key) {
    try { return localStorage.getItem("a8m." + key); } catch { return null; }
  },
  set(key, value) {
    try { localStorage.setItem("a8m." + key, value); } catch {}
  },
  clear() {
    try {
      Object.keys(localStorage)
        .filter((key) => key.startsWith("a8m."))
        .forEach((key) => localStorage.removeItem(key));
    } catch {}
  }
};

function notificationStorageKey() {
  return "a8m.notifications.read." + normalizeEmail(state.user?.email || "guest");
}

function loadDismissedNotifications() {
  try {
    const raw = localStorage.getItem(notificationStorageKey());
    const list = raw ? JSON.parse(raw) : [];
    state.dismissedNotifications = new Set(Array.isArray(list) ? list.slice(-500) : []);
  } catch {
    state.dismissedNotifications = new Set();
  }
}

function persistDismissedNotifications() {
  try {
    const list = Array.from(state.dismissedNotifications).slice(-500);
    localStorage.setItem(notificationStorageKey(), JSON.stringify(list));
  } catch {}
}

/* ============================================================
   state
   ============================================================ */
const state = {
  task: "mail",
  folder: "inbox",
  filter: "all",
  query: "",
  sortDesc: true,
  user: null,
  csrf: null,
  aliases: [],
  identities: [],
  signatures: [],
  ai: { enabled: false, languages: [], styles: [] },
  recentAudit: [],
  aiReader: null,
  notifications: [],
  dismissedNotifications: new Set(),
  noticeFilter: "all",
  folders: [],
  messages: [],
  people: [],
  calendarEvents: [],
  sieve: null,
  quota: null,
  outbound: null,
  pendingSends: [],
  preferences: { ...DEFAULT_PREFERENCES },
  security: null,
  tasks: [],
  files: [],
  storageProviders: [],
  defaultStorage: "a8m",
  storageCredentialData: [],
  storageCredentialProvider: "",
  composeAttachProvider: "all",
  taskFilter: "all",
  selectedTaskId: null,
  editingTaskId: null,
  fileFilter: "all",
  fileProvider: "all",
  selectedFileId: null,
  scheduledCount: 0,
  scheduledEditId: null,
  scheduledEditPresets: [],
  sendNowId: null,
  twoFactor: null,
  twoFactorMode: "otp",
  enrollmentChallenge: null,
  scheduleSessionId: null,
  scheduleChoice: "tomorrow-morning",
  preview: null,
  previewZoom: 1,
  attachmentMaxBytes: 8 * 1024 * 1024,
  selectedThreadId: null,
  selectedIds: new Set(),
  expanded: new Set(),
  expandedThreadId: null,
  viewThreads: [],
  viewScope: [],
  searchParsed: null,
  searchMatched: [],
  searchResults: [],
  searchErrors: [],
  searchSeq: 0,
  searchTimer: null,
  searchLoading: false,
  theme: "system",
  accent: "blue",
  density: "comfortable",
  externalWarnings: true,
  markReadOnOpen: true,
  defaultPriority: "normal",
  defaultReceipt: false,
  undoSendSeconds: 8,
  notifyDesktop: true,
  notifySound: false,
  notifyCalendar: true,
  saveCopyEnabled: true,
  spellCheckEnabled: true,
  autoLoadImages: true,
  imageOverrides: new Set(),
  onboarding: { step: 0, saving: false, passwordChanged: false },
  folderMenuTarget: "",
  folderEditor: null,
  moveRuleMode: false,
  moveSearch: "",
  signatureEditor: null,
  signaturePickerSessionId: null,
  identityEditor: null,
  spellcheck: null,
  spellChecker: null,
  knownInboxIds: null,
  mailClient: null,
  mailMonitorTimer: null,
  calendarReminderTimer: null,
  remindedEventIds: new Set(),
  contactEmail: null,
  contactDept: "All",
  peopleQuery: "",
  peopleFilter: "all",
  calendarYear: new Date().getFullYear(),
  calendarMonth: new Date().getMonth(),
  calendarSelected: null,
  settingsTab: "appearance",
  searchHintsOpen: false,
  offline: false,
  statusPop: null,
  bulkMoveOpen: false,
  knownPendingIds: new Set()
};

state.calendarSelected = `${state.calendarYear}-${String(state.calendarMonth + 1).padStart(2, "0")}-${String(new Date().getDate()).padStart(2, "0")}`;

let composeSessions = [];
let suppressHashSync = false;
let lastAppliedHash = "";
let composeCounter = 0;
let composeZ = 0;
let activeComposeId = null;
let minimizedComposeFlashId = null;
let minimizedComposeFlashTimer = null;
let pendingAttachSessionId = null;
let attachPickerSessionId = null;
let composeAiSessionId = null;
let aiSummarySeq = 0;
let composePointer = null;
let toastTimer = null;
let toastInterval = null;
let outboxPollTimer = null;
let outboxPollBusy = false;
let pendingTickTimer = null;
let lastFocus = null;
let deferredInstallPrompt = null;
let searchHintsTimer = null;
let lastReaderId = null;
let settingsLoading = false;
let rulesLoading = false;
let auditLoading = false;
let loginBusy = false;
let currentRulesCache = null;
let editingRuleId = null;
let twoFactorChallenge = null;
let passkeyTimer = null;
let scheduleChoice = "tomorrow-morning";
let scheduleSession = null;
let taskEditorMode = false;
let fileShareTarget = null;
let uploadBusy = false;

/* ============================================================
   DOM
   ============================================================ */
const app = document.getElementById("app");
const loginScreen = document.getElementById("loginScreen");
const loginForm = document.getElementById("loginForm");
const loginEmail = document.getElementById("loginEmail");
const loginPassword = document.getElementById("loginPassword");
const loginError = document.getElementById("loginError");
const loginHint = document.getElementById("loginHint");
const loginSubmit = document.getElementById("loginSubmit");
const ssoBtn = document.getElementById("ssoBtn");
const searchInput = document.getElementById("searchInput");
const searchClear = document.getElementById("searchClear");
const searchWrap = document.getElementById("searchWrap");
const searchHints = document.getElementById("searchHints");
const messageList = document.getElementById("messageList");
const reader = document.getElementById("reader");
const readerContent = document.getElementById("readerContent");
const readerScroll = document.getElementById("readerScroll");
const listTitle = document.getElementById("listTitle");
const listCount = document.getElementById("listCount");
const folderList = document.getElementById("folderList");
const statusRange = document.getElementById("statusRange");
const statusMid = document.getElementById("statusMid");
const peopleList = document.getElementById("peopleList");
const peopleCount = document.getElementById("peopleCount");
const personDetail = document.getElementById("personDetail");
const composeLayer = document.getElementById("composeLayer");
const composeDock = document.getElementById("composeDock");
const composeDockTabs = document.getElementById("composeDockTabs");
const attachInput = document.getElementById("attachInput");
const toastEl = document.getElementById("toast");
const helpOverlay = document.getElementById("helpOverlay");
const scrim = document.getElementById("scrim");
const mailView = document.getElementById("mailView");
const peopleView = document.getElementById("peopleView");
const calendarView = document.getElementById("calendarView");
const settingsView = document.getElementById("settingsView");
const calGrid = document.getElementById("calGrid");
const calMonthLabel = document.getElementById("calMonthLabel");
const calWeekLabel = document.getElementById("calWeekLabel");
const agendaDate = document.getElementById("agendaDate");
const agendaList = document.getElementById("agendaList");
const agendaCount = document.getElementById("agendaCount");
const notifyPop = document.getElementById("notifyPop");
const notifyList = document.getElementById("notifyList");
const notifyCount = document.getElementById("notifyCount");
const notifySubtitle = document.getElementById("notifySubtitle");
const noticeLayer = document.getElementById("noticeLayer");
const noticeBody = document.getElementById("noticeBody");
const noticeCount = document.getElementById("noticeCount");
const bootSplash = document.getElementById("bootSplash");
const composeAttachOverlay = document.getElementById("composeAttachOverlay");
const composeAttachFileList = document.getElementById("composeAttachFileList");
const composeAttachCurrentList = document.getElementById("composeAttachCurrentList");
const composeAttachFileCount = document.getElementById("composeAttachFileCount");
const composeAttachCurrentCount = document.getElementById("composeAttachCurrentCount");
const aiReaderWraps = $$("[data-ai-wrap]");
const aiReaderToggles = $$("[data-ai-toggle]");
const aiReaderMenus = $$("[data-ai-menu]");
aiReaderMenus.forEach((menu) => { menu.aiOwner = menu.closest("[data-ai-wrap]"); });
const newMenu = document.getElementById("newMenu");
const moveMenu = document.getElementById("moveMenu");
const detailsOverlay = document.getElementById("detailsOverlay");
const detailsBody = document.getElementById("detailsBody");
let detailsCurrentId = "";
let detailsSourceMessage = null;
const eventOverlay = document.getElementById("eventOverlay");
const bulkBar = document.getElementById("bulkBar");
const bulkCount = document.getElementById("bulkCount");
const selectAll = document.getElementById("selectAll");
const searchScopeRow = document.getElementById("searchScopeRow");
const searchSummary = document.getElementById("searchSummary");
const statusPop = document.getElementById("statusPop");
const statusPopBody = document.getElementById("statusPopBody");
const statusPopTitle = document.getElementById("statusPopTitle");
const offlineBanner = document.getElementById("offlineBanner");
const twoFactorForm = document.getElementById("twoFactorForm");
const twoFactorError = document.getElementById("twoFactorError");
const twoFactorSubmit = document.getElementById("twoFactorSubmit");
const usePasskeyBtn = document.getElementById("usePasskeyBtn");
const passkeyOverlay = document.getElementById("passkeyOverlay");
const passkeyPromptStatus = document.getElementById("passkeyPromptStatus");
const tasksView = document.getElementById("tasksView");
const filesView = document.getElementById("filesView");
const tasksMeta = document.getElementById("tasksMeta");
const taskStats = document.getElementById("taskStats");
const taskFilters = document.getElementById("taskFilters");
const taskList = document.getElementById("taskList");
const taskDetail = document.getElementById("taskDetail");
const scheduledView = document.getElementById("scheduledView");
const scheduledMeta = document.getElementById("scheduledMeta");
const scheduledSummary = document.getElementById("scheduledSummary");
const scheduledList = document.getElementById("scheduledList");
const scheduledZoneLabel = document.getElementById("scheduledZoneLabel");
const scheduledQueueNote = document.getElementById("scheduledQueueNote");
const scheduledEditOverlay = document.getElementById("scheduledEditOverlay");
const scheduledEditOriginal = document.getElementById("scheduledEditOriginal");
const scheduledEditTo = document.getElementById("scheduledEditTo");
const scheduledEditCc = document.getElementById("scheduledEditCc");
const scheduledEditSubject = document.getElementById("scheduledEditSubject");
const scheduledEditBody = document.getElementById("scheduledEditBody");
const scheduledEditPresets = document.getElementById("scheduledEditPresets");
const scheduledEditDate = document.getElementById("scheduledEditDate");
const scheduledEditTime = document.getElementById("scheduledEditTime");
const scheduledEditInsight = document.getElementById("scheduledEditInsight");
const scheduledEditError = document.getElementById("scheduledEditError");
const scheduledEditAttachments = document.getElementById("scheduledEditAttachments");
const scheduledEditSave = document.getElementById("scheduledEditSave");
const sendNowOverlay = document.getElementById("sendNowOverlay");
const sendNowSummary = document.getElementById("sendNowSummary");
const sendNowConfirm = document.getElementById("sendNowConfirm");
const filesMeta = document.getElementById("filesMeta");
const storageStrip = document.getElementById("storageStrip");
const fileFilters = document.getElementById("fileFilters");
const fileProviderFilter = document.getElementById("fileProviderFilter");
const fileList = document.getElementById("fileList");
const fileDetail = document.getElementById("fileDetail");
const fileUploadInput = document.getElementById("fileUploadInput");
const scheduleOverlay = document.getElementById("scheduleOverlay");
const previewOverlay = document.getElementById("previewOverlay");
const reportOverlay = document.getElementById("reportOverlay");
const reportMessageCard = document.getElementById("reportMessageCard");
const reportNote = document.getElementById("reportNote");
const reportSubmit = document.getElementById("reportSubmit");
const readerFlagBtn = document.getElementById("readerFlagBtn");
const readerPinBtn = document.getElementById("readerPinBtn");
const readerPinLabel = document.getElementById("readerPinLabel");
const readerSpamBtn = document.getElementById("readerSpamBtn");
const readerSpamLabel = document.getElementById("readerSpamLabel");
const readerSpamIcon = document.getElementById("readerSpamIcon");
const storageOverlay = document.getElementById("storageOverlay");
const storageCredentialsOverlay = document.getElementById("storageCredentialsOverlay");
const storageCredentialsTabs = document.getElementById("storageCredentialsTabs");
const storageCredentialFields = document.getElementById("storageCredentialFields");
const storageCredentialStatus = document.getElementById("storageCredentialStatus");
const confirmOverlay = document.getElementById("confirmOverlay");
const domainBlockedOverlay = document.getElementById("domainBlockedOverlay");
const onboardingOverlay = document.getElementById("onboardingOverlay");
const signatureEditorOverlay = document.getElementById("signatureEditorOverlay");
const signaturePickerOverlay = document.getElementById("signaturePickerOverlay");
const spellcheckOverlay = document.getElementById("spellcheckOverlay");
const identityEditorOverlay = document.getElementById("identityEditorOverlay");
const folderMenu = document.getElementById("folderMenu");
const folderEditorOverlay = document.getElementById("folderEditorOverlay");
let confirmResolver = null;

function appConfirm({ title = "Confirm action", message = "", confirmLabel = "Confirm", cancelLabel = "Cancel", tone = "default", icon = "i-alert" } = {}) {
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

function settleConfirm(value) {
  if (!confirmResolver) return;
  const resolve = confirmResolver;
  confirmResolver = null;
  confirmOverlay.hidden = true;
  resolve(value);
}

function openDomainBlockedDialog(details = {}) {
  const domain = details.domain || "your sending domain";
  const missing = Array.isArray(details.missing) ? details.missing : [];
  const message = document.getElementById("domainBlockedMessage");
  if (message) message.textContent = `Sending from ${domain} is blocked until its DNS ownership and mail records are complete. Missing or invalid: ${missing.join(", ") || "DNS verification"}.`;
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
const shareOverlay = document.getElementById("shareOverlay");
const recoveryOverlay = document.getElementById("recoveryOverlay");
const scheduledChip = document.getElementById("scheduledChip");
const readerPopout = document.getElementById("readerPopout");
const languageSelect = document.getElementById("languageSelect");
const dateFormatSelect = document.getElementById("dateFormatSelect");
const timeZoneSelect = document.getElementById("timeZoneSelect");
const weekStartSelect = document.getElementById("weekStartSelect");
const workStartInput = document.getElementById("workStartInput");
const workEndInput = document.getElementById("workEndInput");
const workdayRow = document.getElementById("workdayRow");
const regionalPreview = document.getElementById("regionalPreview");
const defaultFromSelect = document.getElementById("defaultFromSelect");
const autoReplyToggle = document.getElementById("autoReplyToggle");
const autoReplyRangeToggle = document.getElementById("autoReplyRangeToggle");
const autoReplyRangeFields = document.getElementById("autoReplyRangeFields");
const autoReplyStart = document.getElementById("autoReplyStart");
const autoReplyEnd = document.getElementById("autoReplyEnd");
const autoReplyDays = document.getElementById("autoReplyDays");
const autoReplyExternalToggle = document.getElementById("autoReplyExternalToggle");
const autoReplyInternalSubject = document.getElementById("autoReplyInternalSubject");
const autoReplyInternalMessage = document.getElementById("autoReplyInternalMessage");
const autoReplyExternalSubject = document.getElementById("autoReplyExternalSubject");
const autoReplyExternalMessage = document.getElementById("autoReplyExternalMessage");
const autoReplyStatus = document.getElementById("autoReplyStatus");
const autoReplyPreview = document.getElementById("autoReplyPreview");
const twoFactorCard = document.getElementById("twoFactorCard");
const twoFactorStatus = document.getElementById("twoFactorStatus");
const twoFactorDetail = document.getElementById("twoFactorDetail");
const twoFactorMethods = document.getElementById("twoFactorMethods");
const recoveryCodeList = document.getElementById("recoveryCodeList");
const shareResult = document.getElementById("shareResult");
const shareUrl = document.getElementById("shareUrl");
const shareFileName = document.getElementById("shareFileName");
const storageConnectList = document.getElementById("storageConnectList");
const scheduleMessageCard = document.getElementById("scheduleMessageCard");
const schedulePresets = document.getElementById("schedulePresets");
const scheduleDate = document.getElementById("scheduleDate");
const scheduleTime = document.getElementById("scheduleTime");
const scheduleZoneInfo = document.getElementById("scheduleZoneInfo");
const scheduleZoneHint = document.getElementById("scheduleZoneHint");
const scheduleInsight = document.getElementById("scheduleInsight");
const scheduleWarning = document.getElementById("scheduleWarning");
const scheduleConfirm = document.getElementById("scheduleConfirm");
const previewTitle = document.getElementById("previewTitle");
const previewMeta = document.getElementById("previewMeta");
const previewBody = document.getElementById("previewBody");
const previewDownload = document.getElementById("previewDownload");
const previewOpen = document.getElementById("previewOpen");
const previewToolbar = document.getElementById("previewToolbar");
const previewZoomLabel = document.getElementById("previewZoomLabel");
const previewDimensions = document.getElementById("previewDimensions");
const previewFootNote = document.getElementById("previewFootNote");

const isMobile = () => window.matchMedia("(max-width: 719px)").matches;
const isDrawer = () => window.matchMedia("(max-width: 1119px)").matches;

/* ============================================================
   API
   ============================================================ */
class ApiFailure extends Error {
  constructor(status, code, message, details) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

async function api(path, options = {}) {
  const method = options.method || "GET";
  const headers = { Accept: "application/json", ...(options.headers || {}) };
  let body;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }
  if (state.csrf && method !== "GET") headers["X-CSRF-Token"] = state.csrf;
  let response;
  try {
    response = await fetch(path, { method, headers, body, credentials: "same-origin", cache: "no-store" });
  } catch (error) {
    setOffline(true);
    throw new ApiFailure(0, "NETWORK", "Cannot reach the mail server. Check your connection.");
  }
  setOffline(false);
  const type = response.headers.get("content-type") || "";
  let payload = null;
  if (type.includes("application/json")) {
    try { payload = await response.json(); } catch { payload = null; }
  } else if (type.includes("text/")) {
    payload = await response.text();
  }
  if (!response.ok) {
    if (response.status === 401 && !options.skipAuth) handleSessionExpired();
    const detail = payload && payload.error ? payload.error : {};
    throw new ApiFailure(response.status, detail.code || "HTTP_" + response.status, detail.message || `Request failed (${response.status})`, detail.details || null);
  }
  return payload;
}

function maybeShowPasswordChange() {
  const overlay = document.getElementById("passwordChangeOverlay");
  if (!overlay) return;
  if (state.user && state.user.onboardingRequired) {
    overlay.hidden = true;
    return;
  }
  const required = Boolean(state.user && state.user.passwordChangeRequired);
  if (!required) {
    overlay.hidden = true;
    return;
  }
  overlay.hidden = false;
  const reason = document.getElementById("passwordChangeReason");
  const expiresAt = state.user.passwordExpiresAt ? new Date(state.user.passwordExpiresAt) : null;
  if (reason) {
    reason.textContent = expiresAt && expiresAt.getTime() < Date.now()
      ? "Your password has expired. Set a new password to restore access to your mailbox."
      : "Your organization requires a new password before you can continue. Use the password rules configured by your administrator.";
  }
  setTimeout(() => document.getElementById("passwordCurrent")?.focus(), 80);
}

async function submitPasswordChange() {
  const current = document.getElementById("passwordCurrent");
  const next = document.getElementById("passwordNew");
  const confirm = document.getElementById("passwordConfirm");
  const error = document.getElementById("passwordChangeError");
  const button = document.getElementById("passwordChangeSubmit");
  error.hidden = true;
  if (!current.value) { error.textContent = "Enter your current password."; error.hidden = false; return; }
  if (next.value.length < 8) { error.textContent = "Choose a stronger new password."; error.hidden = false; return; }
  if (next.value !== confirm.value) { error.textContent = "The new passwords do not match."; error.hidden = false; return; }
  button.disabled = true;
  button.textContent = "Saving…";
  try {
    await api("/api/password/change", { method: "POST", body: { currentPassword: current.value, newPassword: next.value } });
    current.value = ""; next.value = ""; confirm.value = "";
    state.user.passwordChangeRequired = false;
    document.getElementById("passwordChangeOverlay").hidden = true;
    await loadBootstrap({ restoreDrafts: false, quiet: true });
    showToast("Password updated");
  } catch (changeError) {
    error.textContent = changeError.message || "Could not change the password.";
    error.hidden = false;
  } finally {
    button.disabled = false;
    button.textContent = "Save new password";
  }
}

/* ============================================================
   Mandatory first-login onboarding
   ============================================================ */
function onboardingDisplayNameFallback() {
  const email = state.user?.email || "";
  const local = email.split("@")[0] || "there";
  return local.split(/[._-]+/).filter(Boolean).map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ") || "there";
}

function onboardingName() {
  return String(document.getElementById("onboardingName")?.value || "").trim();
}

function onboardingSelection() {
  const selected = state.onboarding || {};
  return {
    timeFormat: selected.timeFormat || state.preferences.timeFormat || "12h",
    theme: selected.theme || state.preferences.theme || "system",
    accent: selected.accent || state.preferences.accent || "blue"
  };
}

function maybeShowOnboarding() {
  if (!onboardingOverlay) return false;
  const required = Boolean(state.user && state.user.onboardingRequired);
  if (!required) {
    onboardingOverlay.hidden = true;
    document.body.classList.remove("onboarding-active");
    if (app) app.inert = false;
    return false;
  }
  if (!onboardingOverlay.hidden) return true;
  state.onboarding = {
    step: 0,
    saving: false,
    passwordChanged: false,
    timeFormat: state.preferences.timeFormat || "12h",
    theme: state.preferences.theme || "system",
    accent: state.preferences.accent || "blue"
  };
  const name = state.user.name && !/^primary$/i.test(state.user.name) && state.user.name !== state.user.email
    ? state.user.name
    : onboardingDisplayNameFallback();
  document.getElementById("onboardingName").value = name;
  document.getElementById("onboardingEmail").value = state.user.email || "";
  const zoneSelect = document.getElementById("onboardingTimeZone");
  zoneSelect.innerHTML = timeZoneOptions().map((zone) => `<option value="${esc(zone)}">${esc(zone.replace(/_/g, " "))}</option>`).join("");
  zoneSelect.value = state.preferences.timeZone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const language = document.getElementById("onboardingLanguage");
  populateLanguageSelect(language);
  language.value = state.preferences.language || "en-US";
  const passwordRequired = Boolean(state.user.passwordChangeRequired);
  document.getElementById("onboardingPasswordSection").hidden = !passwordRequired;
  document.getElementById("onboardingPasswordClear").hidden = passwordRequired;
  document.getElementById("onboardingPasswordError").hidden = true;
  document.getElementById("onboardingTwoFactorBtn").disabled = false;
  document.getElementById("onboardingTwoFactorBtn").textContent = "Remind me to set this up";
  updateOnboardingAvatar();
  updateOnboardingControls();
  setOnboardingStep(0);
  onboardingOverlay.hidden = false;
  document.body.classList.add("onboarding-active");
  if (app) app.inert = true;
  setTimeout(() => document.getElementById("onboardingName")?.focus(), 120);
  return true;
}

function updateOnboardingAvatar() {
  const name = onboardingName() || onboardingDisplayNameFallback();
  document.getElementById("onboardingAvatar").textContent = initialsFrom(name);
  const finishName = document.getElementById("onboardingFinishName");
  if (finishName) finishName.textContent = name.split(/\s+/)[0] || "there";
}

function updateOnboardingControls() {
  const selection = onboardingSelection();
  $$("[data-onboarding-time-format]").forEach((button) => {
    const active = button.getAttribute("data-onboarding-time-format") === selection.timeFormat;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-pressed", active ? "true" : "false");
  });
  $$("[data-onboarding-theme]").forEach((button) => {
    const active = button.getAttribute("data-onboarding-theme") === selection.theme;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-pressed", active ? "true" : "false");
  });
  $$("[data-onboarding-accent]").forEach((button) => {
    const active = button.getAttribute("data-onboarding-accent") === selection.accent;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-pressed", active ? "true" : "false");
  });
}

function setOnboardingStep(step) {
  const next = Math.max(0, Math.min(3, Number(step) || 0));
  state.onboarding.step = next;
  $$(".onboarding-step", onboardingOverlay).forEach((panel) => { panel.hidden = Number(panel.getAttribute("data-onboarding-step")) !== next; });
  $$("[data-onboarding-step-dot]", onboardingOverlay).forEach((dot) => {
    const index = Number(dot.getAttribute("data-onboarding-step-dot"));
    dot.classList.toggle("is-active", index === next);
    dot.classList.toggle("is-complete", index < next);
  });
  const labels = ["Tell us who you are", "Set your workspace defaults", "Secure the account", "Review and finish"];
  document.getElementById("onboardingProgressLabel").textContent = `Step ${next + 1} of 4`;
  document.getElementById("onboardingProgressSub").textContent = labels[next];
  document.getElementById("onboardingProgressBar").style.width = `${((next + 1) / 4) * 100}%`;
  const back = document.getElementById("onboardingBack");
  back.disabled = next === 0 || state.onboarding.saving;
  const nextButton = document.getElementById("onboardingNext");
  nextButton.textContent = next === 3 ? "Finish setup" : "Continue";
  nextButton.disabled = false;
  const error = document.getElementById("onboardingError");
  error.hidden = true;
  if (next === 3) updateOnboardingSummary();
  const focusTarget = next === 0 ? document.getElementById("onboardingName") : next === 1 ? document.getElementById("onboardingTimeZone") : next === 2 ? document.getElementById("onboardingCurrentPassword") : document.getElementById("onboardingNext");
  setTimeout(() => focusTarget?.focus(), 60);
}

function updateOnboardingSummary() {
  const name = onboardingName();
  const selection = onboardingSelection();
  const zone = document.getElementById("onboardingTimeZone").value || state.preferences.timeZone || "UTC";
  const languageSelect = document.getElementById("onboardingLanguage");
  const language = languageSelect.options[languageSelect.selectedIndex]?.textContent || selection.language || "English";
  document.getElementById("onboardingSummaryName").textContent = name || "—";
  document.getElementById("onboardingSummaryIdentity").textContent = name ? `${name} <${state.user.email}>` : state.user.email;
  document.getElementById("onboardingSummaryZone").textContent = zone.replace(/_/g, " ");
  document.getElementById("onboardingSummaryTime").textContent = selection.timeFormat === "24h" ? "24-hour" : "12-hour";
  document.getElementById("onboardingSummaryLanguage").textContent = language;
  const themeLabel = { light: "Light", dark: "Dark", system: "System" }[selection.theme] || selection.theme;
  const accentLabel = selection.accent.charAt(0).toUpperCase() + selection.accent.slice(1);
  document.getElementById("onboardingSummaryAppearance").textContent = `${themeLabel} · ${accentLabel} accent`;
  updateOnboardingAvatar();
}

function onboardingError(message) {
  const error = document.getElementById("onboardingError");
  if (!error) return;
  error.textContent = message;
  error.hidden = !message;
}

async function submitOnboardingPassword() {
  const current = document.getElementById("onboardingCurrentPassword");
  const next = document.getElementById("onboardingNewPassword");
  const confirm = document.getElementById("onboardingConfirmPassword");
  const error = document.getElementById("onboardingPasswordError");
  error.hidden = true;
  if (!state.user.passwordChangeRequired || state.onboarding.passwordChanged) return true;
  if (state.onboarding.saving) return false;
  if (!current.value) { error.textContent = "Enter your current password."; error.hidden = false; current.focus(); return false; }
  if (next.value.length < 8) { error.textContent = "Choose a new password of at least 8 characters."; error.hidden = false; next.focus(); return false; }
  if (next.value !== confirm.value) { error.textContent = "The new passwords do not match."; error.hidden = false; confirm.focus(); return false; }
  state.onboarding.saving = true;
  const button = document.getElementById("onboardingNext");
  button.disabled = true;
  button.textContent = "Updating password…";
  try {
    await api("/api/password/change", { method: "POST", body: { currentPassword: current.value, newPassword: next.value } });
    state.user.passwordChangeRequired = false;
    state.onboarding.passwordChanged = true;
    current.value = ""; next.value = ""; confirm.value = "";
    document.getElementById("onboardingPasswordSection").hidden = true;
    const clear = document.getElementById("onboardingPasswordClear");
    clear.hidden = false;
    clear.innerHTML = `<div class="onboarding-section-label"><span>Password</span><span class="onboarding-chip verified">Updated</span></div><p>Your new password is active. Keep it in a password manager and continue.</p>`;
    return true;
  } catch (changeError) {
    error.textContent = changeError.message || "Could not change the password.";
    error.hidden = false;
    return false;
  } finally {
    state.onboarding.saving = false;
    button.disabled = false;
    button.textContent = "Continue";
  }
}

async function advanceOnboarding() {
  if (state.onboarding.saving) return;
  const step = state.onboarding.step;
  if (step === 0) {
    if (!onboardingName()) { onboardingError("Enter your full name so the From line is correct."); document.getElementById("onboardingName").focus(); return; }
    setOnboardingStep(1);
    return;
  }
  if (step === 1) {
    if (!document.getElementById("onboardingTimeZone").value) { onboardingError("Choose your time zone."); return; }
    if (!document.getElementById("onboardingLanguage").value) { onboardingError("Choose your preferred language."); return; }
    setOnboardingStep(2);
    return;
  }
  if (step === 2) {
    if (!(await submitOnboardingPassword())) return;
    setOnboardingStep(3);
    return;
  }
  await finishOnboarding();
}

async function finishOnboarding() {
  if (!onboardingName()) { setOnboardingStep(0); onboardingError("Enter your full name so the From line is correct."); return; }
  const selection = onboardingSelection();
  const zone = document.getElementById("onboardingTimeZone").value;
  const language = document.getElementById("onboardingLanguage").value;
  const button = document.getElementById("onboardingNext");
  state.onboarding.saving = true;
  button.disabled = true;
  button.textContent = "Saving your workspace…";
  onboardingError("");
  try {
    const data = await api("/api/onboarding/complete", {
      method: "POST",
      body: {
        displayName: onboardingName(),
        timeZone: zone,
        timeFormat: selection.timeFormat,
        language,
        theme: selection.theme,
        accent: selection.accent
      }
    });
    if (data.user) {
      state.user.onboardingRequired = false;
      state.user.name = data.user.name || state.user.name;
      state.user.initials = data.user.initials || state.user.initials;
      state.user.passwordChangeRequired = false;
    }
    if (data.preferences) state.preferences = { ...state.preferences, ...data.preferences };
    onboardingOverlay.hidden = true;
    document.body.classList.remove("onboarding-active");
    if (app) app.inert = false;
    applyServerPreferences();
    applyPreferenceChrome();
    syncPreferencesUi();
    renderAccountChrome();
    await loadBootstrap({ restoreDrafts: false, quiet: true });
    showToast("Workspace ready — your sender name is set");
  } catch (error) {
    if (error.code === "PASSWORD_CHANGE_REQUIRED") {
      state.user.passwordChangeRequired = true;
      document.getElementById("onboardingPasswordSection").hidden = false;
      setOnboardingStep(2);
      onboardingError("Set a new password before finishing setup.");
    } else {
      onboardingError(error.message || "Could not finish setup. Try again.");
    }
  } finally {
    state.onboarding.saving = false;
    button.disabled = false;
    button.textContent = state.onboarding.step === 3 ? "Finish setup" : "Continue";
  }
}

onboardingOverlay?.addEventListener("input", (event) => {
  if (event.target.id === "onboardingName") {
    updateOnboardingAvatar();
    updateOnboardingSummary();
  }
});
onboardingOverlay?.addEventListener("click", (event) => {
  const timeFormat = event.target.closest("[data-onboarding-time-format]");
  if (timeFormat) {
    state.onboarding.timeFormat = timeFormat.getAttribute("data-onboarding-time-format");
    updateOnboardingControls();
    updateOnboardingSummary();
    return;
  }
  const theme = event.target.closest("[data-onboarding-theme]");
  if (theme) {
    state.onboarding.theme = theme.getAttribute("data-onboarding-theme");
    setTheme(state.onboarding.theme, false);
    updateOnboardingControls();
    updateOnboardingSummary();
    return;
  }
  const accent = event.target.closest("[data-onboarding-accent]");
  if (accent) {
    state.onboarding.accent = accent.getAttribute("data-onboarding-accent");
    setAccent(state.onboarding.accent, false);
    updateOnboardingControls();
    updateOnboardingSummary();
    return;
  }
  if (event.target.closest("#onboardingBack")) { setOnboardingStep(state.onboarding.step - 1); return; }
  if (event.target.closest("#onboardingNext")) { advanceOnboarding(); return; }
  if (event.target.closest("#onboardingTwoFactorBtn")) {
    const twoFactor = event.target.closest("#onboardingTwoFactorBtn");
    twoFactor.disabled = true;
    twoFactor.textContent = "Reminder noted — enable it in Settings → Security & privacy";
    return;
  }
});

function handleSessionExpired() {
  if (loginScreen.hidden) {
    showLogin();
    loginError.textContent = "Your session expired. Sign in again to continue.";
    loginError.hidden = false;
  }
}

function setOffline(offline) {
  const next = !!offline;
  if (state.offline === next) return;
  state.offline = next;
  if (offlineBanner) offlineBanner.hidden = !next;
  const dot = document.getElementById("connDot");
  const text = document.getElementById("connText");
  if (dot) dot.classList.toggle("is-offline", next);
  if (text) text.textContent = next ? "Offline" : "Connected";
  if (statusMid && next) statusMid.textContent = "Offline — showing cached shell";
  if (!next) {
    syncStatusContext();
    if (state.user) refreshMailbox({ quiet: true }).catch(() => {});
  }
}

/* ============================================================
   theme + preferences
   ============================================================ */
function effectiveTheme() {
  const explicit = document.documentElement.getAttribute("data-theme");
  if (explicit) return explicit;
  return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function setTheme(mode, save = true) {
  if (!["light", "dark", "system"].includes(mode)) mode = "system";
  state.theme = mode;
  if (mode === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", mode);
  if (save) {
    prefs.set("theme", mode);
    if (state.user && state.csrf) patchPreferences({ theme: mode }, null);
  }
  syncThemeUI();
}

function syncThemeUI() {
  const mode = effectiveTheme();
  const themeBtn = document.getElementById("themeBtn");
  if (themeBtn) {
    const label = mode === "dark" ? "Switch to light theme" : "Switch to dark theme";
    themeBtn.setAttribute("aria-label", label);
    themeBtn.title = label;
  }
  $$("[data-set-theme]").forEach((button) => {
    const active = button.getAttribute("data-set-theme") === state.theme;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-checked", active ? "true" : "false");
  });
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = mode === "dark" ? "#0b1a2b" : "#093256";
}

function setAccent(key, save = true) {
  if (!ACCENTS[key]) key = "blue";
  state.accent = key;
  document.documentElement.style.setProperty("--accent", ACCENTS[key].value);
  if (save) {
    prefs.set("accent", key);
    if (state.user && state.csrf) patchPreferences({ accent: key }, null);
  }
  $$("[data-set-accent]").forEach((button) => {
    const active = button.getAttribute("data-set-accent") === key;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-checked", active ? "true" : "false");
  });
}

function setDensity(mode, save = true) {
  if (mode !== "compact") mode = "comfortable";
  state.density = mode;
  app.setAttribute("data-density", mode);
  if (save) {
    prefs.set("density", mode);
    if (state.user && state.csrf) patchPreferences({ density: mode }, null);
  }
  $$("[data-set-density]").forEach((button) => {
    const active = button.getAttribute("data-set-density") === mode;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-checked", active ? "true" : "false");
  });
  const hint = document.getElementById("densityHint");
  if (hint) hint.textContent = mode === "compact"
    ? "Two-line rows without the preview line — more conversations per screen."
    : "Three-line rows with a subject and preview line.";
}

function setPreference(key, value, save = true) {
  state[key] = !!value;
  $$(`[data-pref="${key}"]`).forEach((control) => {
    control.setAttribute("aria-checked", state[key] ? "true" : "false");
  });
  if (save) {
    prefs.set(key, state[key] ? "1" : "0");
    if (key === "notifyDesktop" && value && "Notification" in window && Notification.permission === "default") {
      Notification.requestPermission().catch(() => {});
    }
    if (state.user && state.csrf) patchPreferences({ [key]: state[key] }, "Preference saved");
  }
  if (key === "externalWarnings") {
    renderReader();
  }
}

function loadPreferences() {
  const read = (key, fallback) => {
    const value = prefs.get(key);
    return value === null ? fallback : value !== "0";
  };
  setPreference("markReadOnOpen", read("markReadOnOpen", true), false);
  setPreference("defaultReceipt", read("defaultReceipt", false), false);
  setPreference("notifyDesktop", read("notifyDesktop", true), false);
  setPreference("notifySound", read("notifySound", false), false);
  setPreference("notifyCalendar", read("notifyCalendar", true), false);
  setPreference("externalWarnings", read("externalWarnings", true), false);
  state.defaultPriority = ["low", "normal", "high"].includes(prefs.get("defaultPriority")) ? prefs.get("defaultPriority") : "normal";
  state.undoSendSeconds = Number(prefs.get("undoSendSeconds")) >= 5 && Number(prefs.get("undoSendSeconds")) <= 10 ? Number(prefs.get("undoSendSeconds")) : 8;
  const prioritySelect = document.getElementById("defaultPrioritySelect");
  if (prioritySelect) prioritySelect.value = state.defaultPriority;
  const undoSelect = document.getElementById("undoSendSelect");
  if (undoSelect) undoSelect.value = String(state.undoSendSeconds);
}

function applySavedPreferences() {
  // Pre-sign-in cache only: avoids a theme flash on the login card. Once a
  // session exists, applyServerPreferences() replaces every value from DB.
  const savedTheme = prefs.get("theme");
  if (savedTheme === "light" || savedTheme === "dark") setTheme(savedTheme, false);
  else { state.theme = "system"; syncThemeUI(); }
  setAccent(ACCENTS[prefs.get("accent")] ? prefs.get("accent") : "blue", false);
  setDensity(prefs.get("density") === "compact" ? "compact" : "comfortable", false);
  loadPreferences();
}

function applyServerPreferences() {
  const p = state.preferences || {};
  setTheme(p.theme || "system", false);
  setAccent(p.accent || "blue", false);
  setDensity(p.density || "comfortable", false);
  if (p.language) {
    prefs.set("language", p.language);
    setLocale(p.language).catch(() => {});
  }
  for (const key of ["markReadOnOpen", "defaultReceipt", "notifyDesktop", "notifySound", "notifyCalendar", "externalWarnings"]) {
    setPreference(key, p[key] !== false, false);
  }
  state.defaultPriority = ["low", "normal", "high"].includes(p.defaultPriority) ? p.defaultPriority : "normal";
  state.undoSendSeconds = Number(p.undoSendSeconds) >= 5 && Number(p.undoSendSeconds) <= 10 ? Number(p.undoSendSeconds) : 8;
  const prioritySelect = document.getElementById("defaultPrioritySelect");
  if (prioritySelect) prioritySelect.value = state.defaultPriority;
  const undoSelect = document.getElementById("undoSendSelect");
  if (undoSelect) undoSelect.value = String(state.undoSendSeconds);
  // Keep the offline/login cache aligned with the authoritative DB row.
  prefs.set("theme", state.theme);
  prefs.set("accent", state.accent);
  prefs.set("density", state.density);
  for (const key of ["markReadOnOpen", "defaultReceipt", "notifyDesktop", "notifySound", "notifyCalendar", "externalWarnings"]) {
    prefs.set(key, state[key] ? "1" : "0");
  }
  prefs.set("defaultPriority", state.defaultPriority);
  prefs.set("undoSendSeconds", String(state.undoSendSeconds));
}

/* ============================================================
   server-backed workspace preferences
   ============================================================ */
function timeZoneOptions() {
  let zones = [];
  try {
    zones = Intl.supportedValuesOf ? Intl.supportedValuesOf("timeZone") : [];
  } catch {}
  if (!zones.length) {
    zones = ["UTC", "America/Los_Angeles", "America/New_York", "America/Chicago", "America/Denver", "Europe/London", "Europe/Berlin", "Europe/Paris", "Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Tokyo", "Australia/Sydney"];
  }
  const current = currentTimeZone();
  if (current && !zones.includes(current)) zones = [current, ...zones];
  return zones;
}

function syncPreferencesUi() {
  if (!state.preferences) return;
  const preferences = state.preferences;
  setTheme(preferences.theme || "system", false);
  setAccent(preferences.accent || "blue", false);
  setDensity(preferences.density || "comfortable", false);
  for (const key of ["markReadOnOpen", "defaultReceipt", "notifyDesktop", "notifySound", "notifyCalendar", "externalWarnings"]) {
    state[key] = preferences[key] !== false;
    $$(`[data-pref="${key}"]`).forEach((control) => control.setAttribute("aria-checked", state[key] ? "true" : "false"));
  }
  const externalToggle = document.getElementById("externalWarningToggle");
  if (externalToggle) externalToggle.setAttribute("aria-checked", state.externalWarnings ? "true" : "false");
  state.defaultPriority = ["low", "normal", "high"].includes(preferences.defaultPriority) ? preferences.defaultPriority : "normal";
  state.undoSendSeconds = Number(preferences.undoSendSeconds) >= 5 && Number(preferences.undoSendSeconds) <= 10 ? Number(preferences.undoSendSeconds) : 8;
  const priorityControl = document.getElementById("defaultPrioritySelect");
  if (priorityControl) priorityControl.value = state.defaultPriority;
  const undoControl = document.getElementById("undoSendSelect");
  if (undoControl) undoControl.value = String(state.undoSendSeconds);
  const composeModeControl = document.getElementById("composeModeSelect");
  if (composeModeControl) composeModeControl.value = preferences.composeMode === "plain" ? "plain" : "html";
  const composeWidthControl = document.getElementById("composeWidthSelect");
  if (composeWidthControl) composeWidthControl.value = preferences.composeWidth || "wide";
  const composePositionControl = document.getElementById("composePositionSelect");
  if (composePositionControl) composePositionControl.value = preferences.composePosition || "center";
  const inboxViewControl = document.getElementById("inboxViewSelect");
  if (inboxViewControl) inboxViewControl.value = preferences.inboxView === "individual" ? "individual" : "grouped";
  if (languageSelect) {
    populateLanguageSelect(languageSelect);
    languageSelect.value = preferences.language || "en-US";
  }
  if (dateFormatSelect) dateFormatSelect.value = preferences.dateFormat || "auto";
  if (weekStartSelect) weekStartSelect.value = preferences.weekStart || "monday";
  if (timeZoneSelect) {
    if (timeZoneSelect.options.length < 3 || !Array.from(timeZoneSelect.options).some((option) => option.value === preferences.timeZone)) {
      timeZoneSelect.innerHTML = timeZoneOptions().map((zone) => `<option value="${esc(zone)}">${esc(zone.replace(/_/g, " "))}</option>`).join("");
    }
    timeZoneSelect.value = preferences.timeZone || "UTC";
  }
  $$("[data-time-format]").forEach((button) => {
    const active = button.getAttribute("data-time-format") === (preferences.timeFormat || "12h");
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-checked", active ? "true" : "false");
  });
  if (workStartInput) workStartInput.value = preferences.workStart || "09:00";
  if (workEndInput) workEndInput.value = preferences.workEnd || "17:00";
  if (workdayRow) {
    $$("[data-work-day]", workdayRow).forEach((button) => {
      const active = (preferences.workDays || []).includes(Number(button.getAttribute("data-work-day")));
      button.setAttribute("aria-pressed", active ? "true" : "false");
    });
  }
  if (defaultFromSelect) {
    const options = aliasesForSelect().map((alias) => `<option value="${esc(alias.email)}">${esc(alias.label)} — ${esc(alias.email)}</option>`).join("");
    if (defaultFromSelect.innerHTML !== options) defaultFromSelect.innerHTML = options;
    const fallback = aliasesForSelect()[0] ? aliasesForSelect()[0].email : "";
    defaultFromSelect.value = preferences.defaultFrom || fallback;
  }
  $$("[data-server-pref]").forEach((control) => {
    const key = control.getAttribute("data-server-pref");
    control.setAttribute("aria-checked", state.preferences[key] ? "true" : "false");
  });
  renderRegionalSettings();
  const zoneHint = document.getElementById("timeZoneHint");
  if (zoneHint) zoneHint.textContent = `All timestamps and scheduled sends use ${formatTimeZoneLabel()}.`;
}

function renderRegionalSettings() {
  if (!regionalPreview) return;
  const now = new Date();
  const full = new Intl.DateTimeFormat(currentLocale(), {
    weekday: "long", year: "numeric", month: "long", day: "numeric",
    hour: "numeric", minute: "2-digit",
    hour12: state.preferences.timeFormat !== "24h",
    timeZone: currentTimeZone()
  }).format(now);
  const start = zonedWallTimeToUtc(dateKeyInZone(now), state.preferences.workStart || "09:00");
  const end = zonedWallTimeToUtc(dateKeyInZone(now), state.preferences.workEnd || "17:00");
  const dayNames = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  const workDays = (state.preferences.workDays || []).map((day) => dayNames[day - 1]).join(", ");
  regionalPreview.innerHTML = `<span class="regional-preview-label">Preview in ${esc(currentTimeZone().replace(/_/g, " "))}</span>
    <strong>${esc(full)}</strong>
    <span>${esc(formatTimeZoneLabel())}</span>
    <span>Work hours ${start ? esc(formatClock(start)) : ""} – ${end ? esc(formatClock(end)) : ""} · ${esc(workDays || "No working days")}</span>
    <span>${esc(new Intl.DateTimeFormat(currentLocale(), { dateStyle: "full", timeZone: currentTimeZone() }).format(now))}</span>`;
}

async function patchPreferences(patch, message = "Preferences saved") {
  try {
    const data = await api("/api/preferences", { method: "PATCH", body: patch });
    state.preferences = { ...state.preferences, ...data.preferences };
    if (state.user) state.user.preferences = state.preferences;
    applyPreferenceChrome();
    syncPreferencesUi();
    rerenderAfterPreferenceChange();
    if (message) showToast(message);
    return true;
  } catch (error) {
    showError(error);
    syncPreferencesUi();
    return false;
  }
}

function rerenderAfterPreferenceChange() {
  if (state.task === "mail") {
    renderFolders();
    renderList();
    renderReader({ preserveScroll: true });
    syncStatusRange();
  } else if (state.task === "tasks") {
    renderTasks();
  } else if (state.task === "files") {
    renderFiles();
  } else if (state.task === "calendar") {
    renderCalendar();
  } else if (state.task === "settings") {
    renderRegionalSettings();
    renderAuditIfVisible();
  }
  if (!detailsOverlay.hidden) closeDetails();
}

function renderAuditIfVisible() {
  if (state.task === "settings" && state.settingsTab === "audit") renderAudit();
}

/* ============================================================
   toast
   ============================================================ */
function hideToast() {
  clearTimeout(toastTimer);
  clearInterval(toastInterval);
  toastInterval = null;
  toastEl.classList.remove("is-visible");
}

function showToast(message, actionLabel, action, options = {}) {
  clearTimeout(toastTimer);
  clearInterval(toastInterval);
  toastInterval = null;
  toastEl.innerHTML = "";
  const text = document.createElement("span");
  text.className = "toast-text";
  text.textContent = message;
  toastEl.appendChild(text);
  if (options.countdownAt) {
    const countdown = document.createElement("span");
    countdown.className = "toast-countdown";
    const update = () => {
      const remaining = Date.parse(options.countdownAt) - Date.now();
      countdown.textContent = remaining > 0 ? formatCountdown(remaining) : "sent";
      if (remaining <= 0 && toastInterval) {
        clearInterval(toastInterval);
        toastInterval = null;
        text.textContent = options.sentMessage || "Message sent";
        toastEl.classList.remove("is-undo-send");
        if (typeof options.onExpire === "function") options.onExpire();
      }
    };
    update();
    toastInterval = setInterval(update, 250);
    toastEl.appendChild(countdown);
    toastEl.classList.add("is-undo-send");
  }
  if (actionLabel && typeof action === "function") {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "toast-action";
    button.textContent = actionLabel;
    button.addEventListener("click", async () => {
      button.disabled = true;
      try { await action(); } catch (error) { showToast(error.message || "That action failed"); }
      hideToast();
    }, { once: true });
    toastEl.appendChild(button);
  }
  toastEl.classList.add("is-visible");
  const duration = options.duration != null ? options.duration : (actionLabel ? 10000 : 3200);
  toastTimer = setTimeout(hideToast, duration);
}

function showError(error) {
  const detail = error && error.details && error.details.errors ? ` ${error.details.errors.join(" · ")}` : "";
  showToast((error && error.message ? error.message : "Something went wrong") + detail);
}

/* ============================================================
   auth / boot
   ============================================================ */
const AUTH_SPLASH_HOLD_MS = 5000;
let bootSplashHideTimer = null;

function hideBootSplash() {
  if (!bootSplash) return;
  clearTimeout(bootSplashHideTimer);
  bootSplash.classList.add("is-hiding");
  bootSplashHideTimer = setTimeout(() => { bootSplash.hidden = true; }, 380);
}

function showBootSplash(note = "Restoring your secure session and mailbox…") {
  if (!bootSplash) return;
  clearTimeout(bootSplashHideTimer);
  bootSplash.hidden = false;
  bootSplash.classList.remove("is-hiding");
  const copy = bootSplash.querySelector("p:not(.eyebrow)");
  if (copy) copy.textContent = note;
  // Restart the progress animation when the splash is shown a second time.
  void bootSplash.offsetWidth;
}

function resetLoginButtons() {
  loginBusy = false;
  if (loginSubmit) { loginSubmit.disabled = false; loginSubmit.textContent = t("login.signIn"); }
  if (ssoBtn) { ssoBtn.disabled = false; ssoBtn.innerHTML = `<svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg><span data-i18n="login.sso">${esc(t("login.sso"))}</span>`; }
  if (twoFactorSubmit) { twoFactorSubmit.disabled = false; twoFactorSubmit.textContent = "Verify and sign in"; }
  if (usePasskeyBtn) usePasskeyBtn.disabled = false;
}

function showLogin() {
  composeSessions.slice().forEach((session) => removeComposeSessionUi(session.id));
  composeSessions = [];
  state.user = null;
  state.notifications = [];
  state.dismissedNotifications = new Set();
  state.aiReader = null;
  activeComposeId = null;
  updateComposeDock();
  hideNotify();
  closeNoticeCenter();
  closeComposeAttach();
  hideToast();
  closeMenus();
  closeHelp();
  closeDetails();
  closeEventDialog();
  closeStatusPop();
  closeDrawer();
  document.body.classList.remove("composing", "drawer-open", "search-open", "reader-open", "person-open");
  loginScreen.hidden = false;
  app.hidden = true;
  loginError.hidden = true;
  loginHint.hidden = true;
  loginForm.hidden = false;
  twoFactorForm.hidden = true;
  const enrollment = document.getElementById("enrollmentForm");
  if (enrollment) enrollment.hidden = true;
  twoFactorChallenge = null;
  state.enrollmentChallenge = null;
  hidePasskeyPrompt();
  loginPassword.type = "password";
  const toggle = document.getElementById("passwordToggle");
  if (toggle) toggle.textContent = "Show";
  resetLoginButtons();
  loginScreen.scrollTop = 0;
  setTimeout(() => { if (loginEmail && !isMobile()) loginEmail.focus(); }, 60);
  stopOutboxPolling();
  const passwordOverlay = document.getElementById("passwordChangeOverlay");
  if (passwordOverlay) passwordOverlay.hidden = true;
  if (onboardingOverlay) onboardingOverlay.hidden = true;
  if (signatureEditorOverlay) signatureEditorOverlay.hidden = true;
  if (signaturePickerOverlay) signaturePickerOverlay.hidden = true;
  if (spellcheckOverlay) spellcheckOverlay.hidden = true;
  if (identityEditorOverlay) identityEditorOverlay.hidden = true;
  if (folderEditorOverlay) folderEditorOverlay.hidden = true;
  if (folderMenu) folderMenu.hidden = true;
  state.folderEditor = null;
  state.signatureEditor = null;
  state.identityEditor = null;
  state.spellcheck = null;
  state.imageOverrides = new Set();
  document.body.classList.remove("onboarding-active");
  if (app) app.inert = false;
  if (state.pendingSends.length && state.user) {
    // A signed-out tab cannot cancel; data remains in the server queue.
  }
  hideBootSplash();
}

async function finishSignIn(data, { holdSplashMs = 0 } = {}) {
  const splashStartedAt = Date.now();
  if (holdSplashMs > 0) showBootSplash("Authentication confirmed. Opening your secure workspace…");
  state.csrf = data.csrf || state.csrf;
  loginScreen.hidden = true;
  app.hidden = false;
  loginForm.hidden = false;
  twoFactorForm.hidden = true;
  const enrollmentForm = document.getElementById("enrollmentForm");
  if (enrollmentForm) enrollmentForm.hidden = true;
  twoFactorChallenge = null;
  state.enrollmentChallenge = null;
  hidePasskeyPrompt();
  resetLoginButtons();
  await loadBootstrap({ restoreDrafts: true });
  restoreNavigationFromLocation();
  requestAnimationFrame(layoutComposeSessions);
  if (holdSplashMs > 0) {
    const copy = bootSplash?.querySelector("p:not(.eyebrow)");
    if (copy) copy.textContent = "Your mailbox is ready. Opening your inbox…";
    const remaining = holdSplashMs - (Date.now() - splashStartedAt);
    if (remaining > 0) await new Promise((resolve) => setTimeout(resolve, remaining));
  }
  hideBootSplash();
}

function finishAuthenticatedSignIn(data) {
  return finishSignIn(data, { holdSplashMs: AUTH_SPLASH_HOLD_MS }).catch((error) => {
    hideBootSplash();
    throw error;
  });
}

async function submitLogin() {
  if (loginBusy) return;
  const email = loginEmail.value.trim();
  const password = loginPassword.value;
  loginError.hidden = true;
  loginHint.hidden = true;
  if (!email || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
    loginError.textContent = t("login.invalidEmail");
    loginError.hidden = false;
    loginEmail.focus();
    return;
  }
  if (!password) {
    loginError.textContent = t("login.passwordRequired");
    loginError.hidden = false;
    loginPassword.focus();
    return;
  }
  loginBusy = true;
  loginSubmit.disabled = true;
  loginSubmit.textContent = t("login.signingIn");
  ssoBtn.disabled = true;
  try {
    const remember = document.getElementById("rememberMe").checked;
    const data = await api("/api/session", {
      method: "POST",
      skipAuth: true,
      body: { email, password, method: "password", remember }
    });
    if (data.twoFactorRequired) {
      resetLoginButtons();
      showTwoFactorChallenge(data, remember);
      return;
    }
    if (data.enrollmentRequired) {
      resetLoginButtons();
      showEnrollmentChallenge(data, remember);
      return;
    }
    state.csrf = data.csrf;
    await finishAuthenticatedSignIn(data);
  } catch (error) {
    loginError.textContent = error.message;
    loginError.hidden = false;
    resetLoginButtons();
  }
}

async function submitSso() {
  if (loginBusy) return;
  loginBusy = true;
  loginError.hidden = true;
  loginSubmit.disabled = true;
  ssoBtn.disabled = true;
  ssoBtn.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>Connecting to SSO…';
  try {
    const data = await api("/api/session", {
      method: "POST",
      skipAuth: true,
      body: { email: loginEmail.value.trim(), method: "sso", remember: document.getElementById("rememberMe").checked }
    });
    state.csrf = data.csrf;
    await finishAuthenticatedSignIn(data);
  } catch (error) {
    loginError.textContent = error.message;
    loginError.hidden = false;
    resetLoginButtons();
  }
}

function showTwoFactorChallenge(data, remember) {
  twoFactorChallenge = { ...data, remember };
  state.twoFactor = data;
  loginForm.hidden = true;
  twoFactorForm.hidden = false;
  loginError.hidden = true;
  loginHint.hidden = true;
  twoFactorError.hidden = true;
  const user = data.user || {};
  const avatarEl = document.getElementById("twoFactorAvatar");
  if (avatarEl) {
    avatarEl.textContent = user.initials || initialsFrom(user.name || "");
    avatarEl.style.setProperty("--av", user.color || "#34557a");
  }
  document.getElementById("twoFactorName").textContent = user.name || "Alt8Mail account";
  document.getElementById("twoFactorEmail").textContent = user.email || loginEmail.value.trim();
  const passkey = (data.passkeys || [])[0];
  const passkeyPanel = document.getElementById("passkeyPanel");
  const canPasskey = Boolean(data.methods && data.methods.passkey);
  passkeyPanel.hidden = !canPasskey;
  if (canPasskey) {
    document.getElementById("passkeyName").textContent = passkey ? passkey.name : "Registered passkey";
  }
  const canAuthenticator = Boolean(data.methods && data.methods.authenticator);
  const canRecovery = Boolean(data.methods && data.methods.recovery);
  const demo = canAuthenticator && data.demoCode ? data.demoCode : "";
  twoFactorDemo.hidden = !demo;
  if (demo) document.getElementById("twoFactorDemoCode").textContent = demo;
  document.getElementById("recoveryModeBtn").hidden = !canRecovery;
  document.getElementById("otpModeBtn").hidden = !canAuthenticator;
  $$("#otpInputs input").forEach((input) => { input.value = ""; input.disabled = !canAuthenticator; });
  document.getElementById("recoveryCode").value = "";
  document.getElementById("trustDevice").checked = false;
  setTwoFactorMode(canAuthenticator ? "otp" : "recovery");
  twoFactorForm.classList.remove("is-shaking");
  requestAnimationFrame(() => {
    const first = canAuthenticator ? document.getElementById("otp0") : document.getElementById("recoveryCode");
    if (first) first.focus();
  });
}

function setTwoFactorMode(mode) {
  state.twoFactorMode = mode === "recovery" ? "recovery" : "otp";
  const recovery = state.twoFactorMode === "recovery";
  document.getElementById("otpBlock").hidden = recovery;
  document.getElementById("recoveryBlock").hidden = !recovery;
  twoFactorSubmit.textContent = recovery ? "Verify recovery code" : "Verify and sign in";
  setTimeout(() => {
    const target = recovery ? document.getElementById("recoveryCode") : document.getElementById("otp0");
    if (target) target.focus();
  }, 40);
}

function otpCodeValue() {
  return $$("#otpInputs input").map((input) => input.value.replace(/\D/g, "")).join("");
}

function fillOtp(code) {
  const digits = String(code || "").replace(/\D/g, "").slice(0, 6).split("");
  $$("#otpInputs input").forEach((input, index) => { input.value = digits[index] || ""; });
  const target = $$("#otpInputs input").find((input) => !input.value) || document.getElementById("otp5");
  if (target) target.focus();
}

function hideTwoFactor() {
  twoFactorChallenge = null;
  twoFactorForm.hidden = true;
  loginForm.hidden = false;
  hidePasskeyPrompt();
  const first = document.getElementById("loginPassword");
  if (first) setTimeout(() => first.focus(), 40);
}

async function showPasskeyPrompt() {
  if (!twoFactorChallenge) return;
  if (!window.PublicKeyCredential || !navigator.credentials) {
    twoFactorError.textContent = "This browser does not support passkeys.";
    twoFactorError.hidden = false;
    return;
  }
  const passkey = (twoFactorChallenge.passkeys || [])[0];
  document.getElementById("passkeyPromptText").textContent = `${passkey ? passkey.name : "Your passkey"} is asking for your fingerprint, face or device PIN.`;
  passkeyPromptStatus.innerHTML = '<span class="passkey-spinner" aria-hidden="true"></span>Waiting for your device…';
  passkeyOverlay.hidden = false;
  twoFactorError.hidden = true;
  try {
    const begin = await api("/api/session/2fa/webauthn/begin", {
      method: "POST", skipAuth: true,
      body: { challengeId: twoFactorChallenge.challengeId }
    });
    const publicKey = prepareAssertionOptions(begin.publicKey);
    const credential = await navigator.credentials.get({ publicKey });
    const trust = document.getElementById("trustDevice").checked;
    const data = await api(`/api/session/2fa/webauthn/finish?flow=${encodeURIComponent(begin.flowId)}&trustDevice=${trust ? "1" : "0"}`, {
      method: "POST", skipAuth: true, body: serializeWebAuthnCredential(credential)
    });
    passkeyPromptStatus.innerHTML = '<svg class="icon" style="width:15px;height:15px;color:var(--success)" aria-hidden="true"><use href="#i-check"/></svg>Passkey confirmed';
    hidePasskeyPrompt();
    state.csrf = data.csrf;
    await finishAuthenticatedSignIn(data);
  } catch (error) {
    hidePasskeyPrompt();
    twoFactorError.textContent = error.name === "NotAllowedError" ? "Passkey verification was cancelled or timed out." : (error.message || "Passkey verification failed.");
    twoFactorError.hidden = false;
    twoFactorForm.classList.add("is-shaking");
    setTimeout(() => twoFactorForm.classList.remove("is-shaking"), 350);
    resetLoginButtons();
  }
}

function hidePasskeyPrompt() {
  clearTimeout(passkeyTimer);
  passkeyTimer = null;
  if (passkeyOverlay) passkeyOverlay.hidden = true;
}

async function submitTwoFactor(event) {
  if (event) event.preventDefault();
  if (!twoFactorChallenge) return;
  const method = state.twoFactorMode === "recovery" ? "recovery" : "totp";
  const code = method === "recovery" ? document.getElementById("recoveryCode").value.trim() : otpCodeValue();
  if (method === "totp" && code.length !== 6) {
    twoFactorError.textContent = "Enter the six-digit code from your authenticator app.";
    twoFactorError.hidden = false;
    twoFactorForm.classList.add("is-shaking");
    setTimeout(() => twoFactorForm.classList.remove("is-shaking"), 350);
    return;
  }
  if (method === "recovery" && !code) {
    twoFactorError.textContent = "Enter one of your recovery codes.";
    twoFactorError.hidden = false;
    return;
  }
  await verifyTwoFactor(method, code);
}

async function verifyTwoFactor(method, code) {
  if (!twoFactorChallenge) return;
  twoFactorError.hidden = true;
  twoFactorSubmit.disabled = true;
  twoFactorSubmit.textContent = "Checking code…";
  usePasskeyBtn.disabled = true;
  try {
    const data = await api("/api/session/2fa", {
      method: "POST",
      skipAuth: true,
      body: {
        challengeId: twoFactorChallenge.challengeId,
        method,
        code,
        trustDevice: document.getElementById("trustDevice").checked
      }
    });
    hidePasskeyPrompt();
    state.csrf = data.csrf;
    await finishAuthenticatedSignIn(data);
  } catch (error) {
    hidePasskeyPrompt();
    twoFactorError.textContent = error.message + (error.details && error.details.attemptsRemaining != null ? ` ${error.details.attemptsRemaining} attempt${error.details.attemptsRemaining === 1 ? "" : "s"} remaining.` : "");
    twoFactorError.hidden = false;
    twoFactorForm.classList.add("is-shaking");
    setTimeout(() => twoFactorForm.classList.remove("is-shaking"), 350);
    resetLoginButtons();
    if (state.twoFactorMode === "otp") fillOtp("");
  }
}

/* ---------- WebAuthn and first-time enrollment ---------- */
function decodeWebAuthnValue(value) {
  const text = String(value || "").replace(/-/g, "+").replace(/_/g, "/");
  const padded = text + "=".repeat((4 - text.length % 4) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes.buffer;
}

function encodeWebAuthnValue(buffer) {
  const bytes = new Uint8Array(buffer || new ArrayBuffer(0));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function prepareCreationOptions(publicKey) {
  const options = JSON.parse(JSON.stringify(publicKey || {}));
  options.challenge = decodeWebAuthnValue(options.challenge);
  if (options.user && options.user.id) options.user.id = decodeWebAuthnValue(options.user.id);
  if (Array.isArray(options.excludeCredentials)) {
    options.excludeCredentials = options.excludeCredentials.map((entry) => ({ ...entry, id: decodeWebAuthnValue(entry.id) }));
  }
  return options;
}

function prepareAssertionOptions(publicKey) {
  const options = JSON.parse(JSON.stringify(publicKey || {}));
  options.challenge = decodeWebAuthnValue(options.challenge);
  if (Array.isArray(options.allowCredentials)) {
    options.allowCredentials = options.allowCredentials.map((entry) => ({ ...entry, id: decodeWebAuthnValue(entry.id) }));
  }
  return options;
}

function serializeWebAuthnCredential(credential) {
  if (credential && typeof credential.toJSON === "function") return credential.toJSON();
  const response = credential.response;
  const result = {
    id: credential.id,
    rawId: encodeWebAuthnValue(credential.rawId),
    type: credential.type,
    clientExtensionResults: typeof credential.getClientExtensionResults === "function" ? credential.getClientExtensionResults() : {},
    response: {}
  };
  if (response.attestationObject) {
    result.response = {
      clientDataJSON: encodeWebAuthnValue(response.clientDataJSON),
      attestationObject: encodeWebAuthnValue(response.attestationObject),
      transports: typeof response.getTransports === "function" ? response.getTransports() : []
    };
  } else {
    result.response = {
      clientDataJSON: encodeWebAuthnValue(response.clientDataJSON),
      authenticatorData: encodeWebAuthnValue(response.authenticatorData),
      signature: encodeWebAuthnValue(response.signature),
      userHandle: response.userHandle ? encodeWebAuthnValue(response.userHandle) : null
    };
  }
  return result;
}

function showEnrollmentChallenge(data, remember) {
  state.enrollmentChallenge = { ...data, remember };
  loginForm.hidden = true;
  twoFactorForm.hidden = true;
  const form = document.getElementById("enrollmentForm");
  form.hidden = false;
  document.getElementById("enrollmentError").hidden = true;
  document.getElementById("enrollmentTotpBlock").hidden = true;
  const user = data.user || {};
  document.getElementById("enrollmentAvatar").textContent = user.initials || initialsFrom(user.name || "");
  document.getElementById("enrollmentAvatar").style.setProperty("--av", user.color || "#34557a");
  document.getElementById("enrollmentName").textContent = user.name || "Alt8Mail account";
  document.getElementById("enrollmentEmail").textContent = user.email || loginEmail.value.trim();
  requestAnimationFrame(() => document.getElementById("enrollmentTotpBtn").focus());
}

function cancelEnrollment() {
  state.enrollmentChallenge = null;
  document.getElementById("enrollmentForm").hidden = true;
  document.getElementById("loginForm").hidden = false;
  document.getElementById("enrollmentTotpBlock").hidden = true;
  resetLoginButtons();
}

async function beginEnrollmentTOTP() {
  const challenge = state.enrollmentChallenge;
  if (!challenge) return;
  const button = document.getElementById("enrollmentTotpBtn");
  button.disabled = true;
  try {
    const data = await api("/api/enrollment/totp/begin", { method: "POST", skipAuth: true, body: { token: challenge.enrollmentToken } });
    document.getElementById("enrollmentTotpSecret").textContent = data.secret;
    document.getElementById("enrollmentTotpBlock").hidden = false;
    document.getElementById("enrollmentTotpCode").value = "";
    document.getElementById("enrollmentTotpCode").focus();
  } catch (error) {
    document.getElementById("enrollmentError").textContent = error.message;
    document.getElementById("enrollmentError").hidden = false;
  } finally { button.disabled = false; }
}

async function verifyEnrollmentTOTP() {
  const challenge = state.enrollmentChallenge;
  if (!challenge) return;
  const code = document.getElementById("enrollmentTotpCode").value.replace(/\D/g, "");
  if (code.length !== 6) {
    document.getElementById("enrollmentError").textContent = "Enter the six-digit code from your authenticator app.";
    document.getElementById("enrollmentError").hidden = false;
    return;
  }
  const button = document.getElementById("enrollmentTotpVerify");
  button.disabled = true;
  button.textContent = "Verifying…";
  try {
    const data = await api("/api/enrollment/totp/finish", { method: "POST", skipAuth: true, body: { token: challenge.enrollmentToken, code } });
    await completeEnrollment(data);
  } catch (error) {
    document.getElementById("enrollmentError").textContent = error.message;
    document.getElementById("enrollmentError").hidden = false;
  } finally {
    button.disabled = false;
    button.textContent = "Verify and continue";
  }
}

async function beginEnrollmentPasskey() {
  const challenge = state.enrollmentChallenge;
  if (!challenge) return;
  if (!window.PublicKeyCredential || !navigator.credentials) {
    document.getElementById("enrollmentError").textContent = "This browser does not support passkeys.";
    document.getElementById("enrollmentError").hidden = false;
    return;
  }
  const button = document.getElementById("enrollmentPasskeyBtn");
  button.disabled = true;
  try {
    const begin = await api("/api/enrollment/webauthn/begin", { method: "POST", skipAuth: true, body: { token: challenge.enrollmentToken } });
    const credential = await navigator.credentials.create({ publicKey: prepareCreationOptions(begin.publicKey) });
    if (!credential) throw new Error("Passkey creation was cancelled");
    const data = await api(`/api/enrollment/webauthn/finish?flow=${encodeURIComponent(begin.flowId)}`, {
      method: "POST", skipAuth: true, body: serializeWebAuthnCredential(credential)
    });
    await completeEnrollment(data);
  } catch (error) {
    document.getElementById("enrollmentError").textContent = error.name === "NotAllowedError" ? "Passkey creation was cancelled or timed out." : (error.message || "Passkey enrollment failed.");
    document.getElementById("enrollmentError").hidden = false;
  } finally { button.disabled = false; }
}

async function completeEnrollment(data) {
  state.enrollmentChallenge = null;
  document.getElementById("enrollmentForm").hidden = true;
  state.csrf = data.csrf;
  await finishAuthenticatedSignIn(data);
  if (Array.isArray(data.recoveryCodes) && data.recoveryCodes.length) {
    showRecoveryCodes(data.recoveryCodes, "Save these recovery codes now. They are shown only once.");
  }
}

async function startTotpSetup() {
  const overlay = document.getElementById("totpSetupOverlay");
  const error = document.getElementById("totpSetupError");
  error.hidden = true;
  try {
    const data = await api("/api/security/2fa/totp/begin", { method: "POST", body: {} });
    document.getElementById("totpSetupSecret").textContent = data.secret;
    document.getElementById("totpSetupUri").textContent = data.otpauth;
    document.getElementById("totpSetupSecret").dataset.uri = data.otpauth;
    document.getElementById("totpSetupCode").value = "";
    overlay.hidden = false;
    setTimeout(() => document.getElementById("totpSetupCode").focus(), 40);
  } catch (err) { showError(err); }
}

async function verifyTotpSetup() {
  const code = document.getElementById("totpSetupCode").value.replace(/\D/g, "");
  const error = document.getElementById("totpSetupError");
  if (code.length !== 6) { error.textContent = "Enter the six-digit code."; error.hidden = false; return; }
  const button = document.getElementById("totpSetupVerify");
  button.disabled = true;
  try {
    const data = await api("/api/security/2fa/totp/finish", { method: "POST", body: { code } });
    state.security = data.security || state.security;
    document.getElementById("totpSetupOverlay").hidden = true;
    renderSecuritySettings();
    if (Array.isArray(data.codes) && data.codes.length) showRecoveryCodes(data.codes, "Save these recovery codes now. They are shown only once.");
    showToast("Authenticator enabled");
  } catch (err) { error.textContent = err.message; error.hidden = false; }
  finally { button.disabled = false; }
}

async function disableTotp() {
  const password = window.prompt("Enter your current account password to remove the authenticator.");
  if (!password) return;
  try {
    const data = await api("/api/security/2fa/totp/disable", { method: "POST", body: { password } });
    state.security = data.security || state.security;
    renderSecuritySettings();
    showToast("Authenticator removed");
  } catch (err) { showError(err); }
}

async function addPasskey() {
  if (!window.PublicKeyCredential || !navigator.credentials) { showToast("This browser does not support passkeys"); return; }
  const button = document.getElementById("addPasskeyBtn");
  button.disabled = true;
  try {
    const begin = await api("/api/security/webauthn/register/begin", { method: "POST", body: {} });
    const credential = await navigator.credentials.create({ publicKey: prepareCreationOptions(begin.publicKey) });
    if (!credential) throw new Error("Passkey creation was cancelled");
    const data = await api(`/api/security/webauthn/register/finish?flow=${encodeURIComponent(begin.flowId)}`, {
      method: "POST", body: serializeWebAuthnCredential(credential)
    });
    state.security = data.security || state.security;
    renderSecuritySettings();
    if (Array.isArray(data.codes) && data.codes.length) showRecoveryCodes(data.codes, "Save these recovery codes now. They are shown only once.");
    showToast("Passkey added");
  } catch (err) { showError(err); }
  finally { button.disabled = false; }
}

async function removePasskey(id) {
  if (!(await appConfirm({ title: "Remove this passkey?", message: "You will no longer be able to sign in with this passkey. Other sign-in methods remain available.", confirmLabel: "Remove passkey", tone: "danger" }))) return;
  try {
    const data = await api(`/api/security/webauthn/${encodeURIComponent(id)}`, { method: "DELETE", body: {} });
    state.security = data.security || state.security;
    renderSecuritySettings();
    showToast("Passkey removed");
  } catch (err) { showError(err); }
}

async function toggleEnrollmentPolicy() {
  const toggle = document.getElementById("enrollmentPolicyToggle");
  const next = toggle.getAttribute("aria-checked") !== "true";
  toggle.setAttribute("aria-checked", next ? "true" : "false");
  try {
    await api("/api/security/policy", { method: "PATCH", body: { enrollmentRequired: next } });
    if (state.security && state.security.twoFactor) state.security.twoFactor.enrollmentRequired = next;
    showToast(next ? "Enrollment required at next sign-in" : "First-time enrollment disabled");
  } catch (err) {
    toggle.setAttribute("aria-checked", next ? "false" : "true");
    showError(err);
  }
}

function showRecoveryCodes(codes, note) {
  const list = document.getElementById("recoveryCodeList");
  if (list) list.innerHTML = codes.map((code) => `<code>${esc(code)}</code>`).join("");
  const title = document.getElementById("recoveryTitle");
  if (title) title.textContent = "Recovery codes";
  recoveryOverlay.hidden = false;
}

async function logout() {
  // Persist the open/minimized compose state before the session is revoked so
  // the next sign-in, on this or another computer, restores it exactly.
  await persistComposeSessions();
  try { await api("/api/session", { method: "DELETE" }); } catch {}
  state.user = null;
  state.csrf = null;
  state.messages = [];
  state.people = [];
  state.aliases = [];
  state.sieve = null;
  state.quota = null;
  state.outbound = null;
  state.pendingSends = [];
  state.selectedThreadId = null;
  state.selectedIds.clear();
  rebuildIndex();
  if (window.location.hash) window.history.replaceState(null, "", window.location.pathname + window.location.search);
  showLogin();
}

async function loadBootstrap({ restoreDrafts = false, quiet = false } = {}) {
  const data = await api("/api/bootstrap");
  applyBootstrap(data, { restoreDrafts, quiet });
  if (!quiet) statusMid.textContent = "Synced just now";
}

function applyBootstrap(data, { restoreDrafts = false, quiet = false } = {}) {
  const previousThread = state.selectedThreadId;
  const firstUserLoad = !state.user;
  state.user = data.user;
  state.mailboxMode = data.mailboxMode || "demo";
  state.aliases = data.aliases || [];
  state.identities = data.identities || state.identities || [];
  state.signatures = data.signatures || state.signatures || [];
  state.ai = data.ai || { enabled: false };
  state.mailClient = data.mailClient || state.mailClient || null;
  state.recentAudit = data.recentAudit || [];
  renderMailClientSetup();
  if (firstUserLoad) loadDismissedNotifications();
  state.folders = data.folders || [];
  state.people = data.people || [];
  state.messages = data.messages || [];
  state.calendarEvents = data.calendarEvents || [];
  state.sieve = data.sieve || null;
  state.quota = data.quota || null;
  state.outbound = data.outbound || null;
  state.pendingSends = data.pendingSends || [];
  state.tasks = data.tasks || [];
  state.files = data.files || [];
  state.storageProviders = data.storageProviders || [];
  state.defaultStorage = data.defaultStorage || state.defaultStorage || "a8m";
  state.tenantPolicy = data.tenantPolicy || null;
  state.attachmentMaxBytes = data.attachmentMaxBytes || 8 * 1024 * 1024;
  state.preferences = { ...DEFAULT_PREFERENCES, ...(data.user && data.user.preferences ? data.user.preferences : {}) };
  state.security = data.user && data.user.security ? data.user.security : null;
  state.csrf = data.csrf || state.csrf;
  applyServerPreferences();
  if (!state.folders.some((folder) => folder.id === state.folder)) state.folder = "inbox";
  if (!state.contactEmail && state.people.length) state.contactEmail = state.people[0].email;
  applyPreferenceChrome();
  rebuildIndex();
  renderAccountChrome();
  renderFolders();
  renderList();
  renderNotifications();
  updateAiReaderUi();
  syncSelectionUi();
  updateQuotaUi();
  updateOutboundUi();
  updatePendingUi();
  updateScheduledUi();
  startOutboxPolling();
  syncPreferencesUi();
  renderSignatureLibrary();
  if (["account", "security", "regional", "autoreply", "storage"].includes(state.settingsTab)) renderSettingsDynamic();
  if (state.task === "settings" && ["rules", "audit"].includes(state.settingsTab)) renderSettingsDynamic();
  if (!quiet && previousThread && !threadById(previousThread)) state.selectedThreadId = null;
  renderReader();
  if (state.task === "tasks") renderTasks();
  if (state.task === "people") renderPeople();
  if (state.task === "files") renderFiles();
  const inboxIds = state.messages.filter((message) => message.folder === "inbox").map((message) => message.id);
  if (!state.knownInboxIds) state.knownInboxIds = new Set(inboxIds);
  else inboxIds.forEach((id) => state.knownInboxIds.add(id));
  startMailMonitor();
  const dataAction = document.getElementById("resetData");
  const dataNote = document.querySelector(".about-actions .about-note");
  if (dataAction && state.mailboxMode === "imap") {
    dataAction.classList.remove("danger-btn");
    dataAction.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-refresh"/></svg>Resync from Dovecot';
    if (dataNote) dataNote.textContent = "Live Dovecot mailbox · IMAP sync and Postfix submission are enabled.";
  }
  syncStatusRange();
  syncStatusContext();
  syncCommandBar();
  if (restoreDrafts) restoreDraftSessions();
  state.knownPendingIds = new Set(state.pendingSends.map((item) => item.id));
  const onboardingShown = maybeShowOnboarding();
  if (!onboardingShown) maybeShowPasswordChange();
}

function applyPreferenceChrome() {
  const language = state.preferences.language || "en-US";
  document.documentElement.lang = language;
  const zoneHint = document.getElementById("timeZoneHint");
  if (zoneHint) zoneHint.textContent = `All timestamps and scheduled sends use ${formatTimeZoneLabel()}.`;
}

function startMailMonitor() {
  if (state.mailMonitorTimer) return;
  state.mailMonitorTimer = setInterval(() => {
    pollNewMail().catch(() => {});
    checkCalendarReminders().catch(() => {});
  }, 15000);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") pollNewMail().catch(() => {});
  });
  pollNewMail().catch(() => {});
}

async function pollNewMail() {
  if (!state.user || document.visibilityState !== "visible") return;
  const data = await api("/api/messages?folder=inbox");
  const known = state.knownInboxIds || new Set();
  const fresh = (data.messages || []).filter((message) => !known.has(message.id));
  if (!fresh.length) return;
  fresh.forEach((message) => known.add(message.id));
  state.knownInboxIds = known;
  showNewMailNotification(fresh[0], fresh.length);
  await refreshMailbox({ quiet: true });
  renderNotifications();
}

function showBrowserNotification(title, body, tag = "a8m-new-mail") {
  if (!state.notifyDesktop || !("Notification" in window) || Notification.permission !== "granted") return false;
  try {
    const notification = new Notification(title, { body, tag, icon: "/icons/icon-192.png", badge: "/icons/icon-192.png" });
    notification.onclick = () => { window.focus(); notification.close(); };
    return true;
  } catch { return false; }
}

function playNotificationTone() {
  try {
    const AudioCtor = window.AudioContext || window.webkitAudioContext;
    if (!AudioCtor) return;
    const context = new AudioCtor();
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.type = "sine";
    oscillator.frequency.setValueAtTime(880, context.currentTime);
    oscillator.frequency.exponentialRampToValueAtTime(660, context.currentTime + 0.16);
    gain.gain.setValueAtTime(0.0001, context.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.08, context.currentTime + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.22);
    oscillator.connect(gain).connect(context.destination);
    oscillator.start();
    oscillator.stop(context.currentTime + 0.24);
    oscillator.onended = () => context.close();
  } catch {}
}

function showNewMailNotification(message, count) {
  const subject = message.subject || "(no subject)";
  const sender = message.from ? (message.from.name || message.from.email) : "New sender";
  const body = count > 1 ? `${count} new messages · ${sender}: ${subject}` : `${sender}: ${subject}`;
  if (state.notifySound) playNotificationTone();
  if (!showBrowserNotification(t("notify.newMail"), body, "a8m-new-mail")) {
    showToast(`New mail from ${sender}: ${subject}`, "Open", () => { setTask("mail"); setFolder("inbox"); });
  }
}

async function checkCalendarReminders() {
  if (!state.notifyCalendar || document.visibilityState !== "visible") return;
  const now = Date.now();
  for (const event of state.calendarEvents || []) {
    const key = String(event.id || `${event.date}-${event.start}-${event.title}`);
    if (state.remindedEventIds.has(key)) continue;
    const start = zonedWallTimeToUtc(event.date, event.start);
    if (!start) continue;
    const diff = start.getTime() - now;
    if (diff <= 0 || diff > 10 * 60 * 1000) continue;
    state.remindedEventIds.add(key);
    const body = `${event.start} · ${event.where || "Calendar"}`;
    if (!showBrowserNotification("Meeting in 10 minutes", `${event.title} — ${body}`, "a8m-calendar-" + key)) {
      showToast(`Starting soon: ${event.title} · ${body}`);
    }
  }
}

async function refreshMailbox({ quiet = true } = {}) {
  const data = await api("/api/bootstrap");
  applyBootstrap(data, { restoreDrafts: false });
  if (!quiet) showToast("Mailbox is up to date");
}

/* ============================================================
   derived data
   ============================================================ */
function rebuildIndex() {
  state.index = new MailIndex(state.messages);
}

function messageById(messageId) {
  return state.messages.find((message) => message.id === messageId) || null;
}

function selectionIdForMessage(message) {
  if (!message) return null;
  return inboxViewIndividual() ? message.id : (message.threadId || message.id);
}

function threadById(threadId) {
  if (!threadId) return null;
  if (inboxViewIndividual()) {
    const direct = state.messages.find((message) => message.id === threadId);
    if (direct) return individualThreadFor(direct);
    const siblings = state.messages
      .filter((message) => (message.threadId || message.id) === threadId)
      .sort((a, b) => (Date.parse(a.date) || 0) - (Date.parse(b.date) || 0));
    if (siblings.length) return individualThreadFor(siblings[siblings.length - 1]);
  }
  return buildThreads(
    state.messages.filter((message) => (message.threadId || message.id) === threadId),
    state.messages
  )[0] || null;
}

function folderDef(folderId) {
  return state.folders.find((folder) => folder.id === folderId) || { id: folderId, label: FOLDER_LABELS[folderId] || folderId, icon: "i-folder" };
}

function scopeMessages() {
  let messages;
  if (state.query) {
    state.searchParsed = state.searchParsed || parseQuery(state.query);
    state.searchMatched = state.searchResults || [];
    messages = state.searchMatched;
  } else {
    state.searchParsed = null;
    state.searchMatched = [];
    state.searchResults = [];
    state.searchErrors = [];
    state.searchLoading = false;
    if (state.folder === "starred") {
      messages = state.messages.filter((message) => message.starred && message.folder !== "spam" && message.folder !== "trash");
    } else {
      messages = state.messages.filter((message) => message.folder === state.folder);
    }
  }
  if (state.filter === "unread") messages = messages.filter((message) => message.unread);
  if (state.filter === "starred") messages = messages.filter((message) => message.starred);
  if (state.filter === "attachments") messages = messages.filter((message) => (message.attachments || []).length);
  return messages;
}

function inboxViewIndividual() {
  return state.preferences?.inboxView === "individual";
}

function individualThreadFor(message) {
  const sender = message.from && message.from.email ? message.from : null;
  const recipient = Array.isArray(message.to) && message.to.length ? message.to[0] : message.to;
  const person = sender || (recipient && recipient.email ? recipient : null);
  return {
    id: message.id,
    messages: [message],
    latest: message,
    subject: message.subject || "(no subject)",
    participants: person ? [person] : [],
    unreadCount: message.unread ? 1 : 0,
    starred: Boolean(message.starred),
    flagged: Boolean(message.flagged),
    pinned: Boolean(message.pinned),
    hasAttachments: (message.attachments || []).length > 0,
    folderSet: [message.folder],
    date: message.date,
    count: 1,
    individual: true
  };
}

function viewUnit(count) {
  return inboxViewIndividual()
    ? (count === 1 ? "message" : "messages")
    : (count === 1 ? "conversation" : "conversations");
}

function computeView() {
  const scope = scopeMessages();
  let threads;
  if (inboxViewIndividual()) {
    threads = scope.slice().sort((a, b) => (Date.parse(b.date) || 0) - (Date.parse(a.date) || 0)).map(individualThreadFor);
    if (!state.sortDesc) threads = threads.slice().reverse();
  } else {
    threads = buildThreads(scope, state.messages);
    if (!state.sortDesc) threads = threads.slice().reverse();
  }
  state.viewScope = scope;
  state.viewThreads = threads;
  return threads;
}

function scopeSet() {
  return new Set(state.viewScope.map((message) => message.id));
}

function threadScopeMessages(thread) {
  const set = scopeSet();
  return thread.messages.filter((message) => set.has(message.id));
}

function representativeMessage(thread) {
  if (state.folder === "sent" && !state.query) {
    const sent = thread.messages.filter((message) => message.folder === "sent");
    if (sent.length) return sent[sent.length - 1];
  }
  if (state.folder === "drafts" && !state.query) {
    const drafts = thread.messages.filter((message) => message.folder === "drafts");
    if (drafts.length) return drafts[drafts.length - 1];
  }
  if (state.query) {
    const inScope = threadScopeMessages(thread);
    if (inScope.length) return inScope[inScope.length - 1];
  }
  return thread.latest;
}

function isSentLike(message) {
  return message.folder === "sent";
}

function threadRowDisplay(thread) {
  const rep = representativeMessage(thread);
  const outgoing = isSentLike(rep);
  const person = (outgoing || rep.draft)
    ? (Array.isArray(rep.to) && rep.to.length ? rep.to[0] : rep.from)
    : rep.from;
  let label;
  if (rep.draft) label = `Draft to ${recipientLabel(rep)}`;
  else if (outgoing) label = `To: ${recipientLabel(rep)}`;
  else label = rep.from ? rep.from.name : "Unknown sender";
  return { rep, person, label, outgoing };
}

function currentThread() {
  return threadById(state.selectedThreadId);
}

function currentMessage() {
  const thread = currentThread();
  if (!thread) return null;
  return thread.latest;
}

function actionMessageIds(threads) {
  const ids = new Set();
  const includeAllFolders = Boolean(state.query) || state.folder === "starred";
  for (const thread of threads) {
    for (const message of thread.messages) {
      if (includeAllFolders) {
        if (message.folder !== "drafts" || state.folder === "drafts" || state.query) ids.add(message.id);
      } else if (message.folder === state.folder) {
        ids.add(message.id);
      }
    }
  }
  return Array.from(ids);
}

function selectedThreads() {
  return state.viewThreads.filter((thread) => state.selectedIds.has(thread.id));
}

/* ============================================================
   account chrome
   ============================================================ */
function renderAccountChrome() {
  const user = state.user;
  if (!user) return;
  const topAvatar = document.getElementById("accountBtn");
  if (topAvatar) {
    topAvatar.textContent = user.initials || initialsFrom(user.name);
    topAvatar.title = `${user.name} — account settings`;
  }
  const accountLine = $(".account-line");
  if (accountLine) {
    const av = $(".avatar", accountLine);
    if (av) { av.textContent = user.initials || initialsFrom(user.name); av.style.setProperty("--av", user.color || "#3f5d7a"); }
    const name = $(".account-id strong", accountLine);
    const email = $(".account-id span", accountLine);
    if (name) name.textContent = user.name;
    if (email) email.textContent = user.email;
  }
  const accountEmail = document.getElementById("accountEmail");
  if (accountEmail) accountEmail.textContent = user.email;
  const copyAccount = document.querySelector("[data-copy-email]");
  if (copyAccount && copyAccount.closest(".account-profile")) copyAccount.setAttribute("data-copy-email", user.email);
  const profile = $(".account-profile");
  if (profile) {
    const av = $(".avatar", profile);
    if (av) { av.textContent = user.initials || initialsFrom(user.name); av.style.setProperty("--av", user.color || "#34557a"); }
    const h3 = $(".account-profile-copy h3", profile);
    const p = $(".account-profile-copy p", profile);
    if (h3) h3.textContent = user.name;
    if (p) p.textContent = `${user.role || "Team member"} · ${user.company || "Alt8Mail"}`;
  }
}

async function loadProfile() {
  const name = document.getElementById("profileName");
  if (!name) return;
  try {
    const data = await api("/api/profile");
    const profile = data.profile || {};
    name.value = profile.name || "";
    document.getElementById("profileTitle").value = profile.title || "";
    document.getElementById("profileDepartment").value = profile.department || "";
    const editable = !!data.editable;
    [name, document.getElementById("profileTitle"), document.getElementById("profileDepartment")].forEach((input) => { if (input) input.disabled = !editable; });
    const button = document.getElementById("saveProfileBtn");
    if (button) button.disabled = !editable;
    const note = document.getElementById("profileEditNote");
    if (note) note.textContent = editable ? "You can update your own profile." : "Profile editing is managed by your organization.";
  } catch (error) {
    const note = document.getElementById("profileEditNote");
    if (note) note.textContent = "Profile details could not be loaded.";
  }
}

async function saveProfile() {
  const name = document.getElementById("profileName");
  const title = document.getElementById("profileTitle");
  const department = document.getElementById("profileDepartment");
  const button = document.getElementById("saveProfileBtn");
  if (!name || !name.value.trim()) { showToast("Enter your full name"); name?.focus(); return; }
  if (button) button.disabled = true;
  try {
    const data = await api("/api/profile", {
      method: "PATCH",
      body: { name: name.value.trim(), title: title?.value.trim() || "", department: department?.value.trim() || "" }
    });
    if (state.user && data.profile) {
      state.user.name = data.profile.name;
      state.user.initials = data.profile.initials;
      renderAccountChrome();
    }
    await refreshIdentities();
    showToast("Profile saved — sender name updated");
  } catch (error) {
    showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

async function loadAccountSessions() {
  const holder = document.getElementById("accountSessions");
  if (!holder) return;
  try {
    const data = await api("/api/security/sessions");
    renderAccountSessions(data.sessions || []);
  } catch (error) {
    holder.innerHTML = `<div class="settings-note">${esc(error.message || "Could not load sign-in sessions.")}</div>`;
  }
}

function renderAccountSessions(sessions) {
  const holder = document.getElementById("accountSessions");
  if (!holder) return;
  if (!sessions.length) {
    holder.innerHTML = '<div class="settings-note">No active sign-in sessions.</div>';
    return;
  }
  holder.innerHTML = sessions.map((session) => {
    const when = session.current ? "Current session" : `Last active ${formatRelativeTime(session.lastSeenAt)}`;
    const where = session.ip ? ` · ${esc(session.ip)}` : "";
    return `<div class="session-row">
      <span class="session-icon"><svg class="icon" aria-hidden="true"><use href="#${session.current ? "i-mail" : "i-people"}"/></svg></span>
      <div class="pref-copy"><strong>${esc(session.device || "Browser")}</strong><span>${when}${where}</span></div>
      ${session.current ? '<span class="policy-chip">Current</span>' : `<button class="link-btn" data-revoke-session="${esc(session.id)}" type="button">Sign out</button>`}
    </div>`;
  }).join("");
}

async function revokeSession(id) {
  if (!(await appConfirm({ title: "Sign out this session?", message: "That browser will need to sign in again.", confirmLabel: "Sign out", tone: "danger" }))) return;
  try {
    const data = await api(`/api/security/sessions/${encodeURIComponent(id)}/revoke`, { method: "POST", body: {} });
    if (data.current) { await logout(); return; }
    await loadAccountSessions();
    showToast("Session signed out");
  } catch (error) { showError(error); }
}

async function revokeOtherSessions() {
  if (!(await appConfirm({ title: "Sign out other sessions?", message: "Every other browser signed in to this account will be signed out immediately.", confirmLabel: "Sign out others", tone: "danger" }))) return;
  try {
    const data = await api("/api/security/sessions/revoke-others", { method: "POST", body: {} });
    await loadAccountSessions();
    showToast(`${data.revoked || 0} other session${data.revoked === 1 ? "" : "s"} signed out`);
  } catch (error) { showError(error); }
}

async function signOutCurrent() {
  await logout();
}

/* ============================================================
   folders
   ============================================================ */
function unreadCount(folderId) {
  if (folderId === "starred") return 0;
  return state.messages.filter((message) => message.folder === folderId && message.unread).length;
}

function folderBadge(folder) {
  if (folder.id === "starred" || folder.virtual) return null;
  const mode = folder.badgeMode || "unread";
  if (mode === "none") return null;
  const count = mode === "total"
    ? state.messages.filter((message) => message.folder === folder.id).length
    : unreadCount(folder.id);
  return count ? { count, neutral: mode === "total" } : null;
}

function folderSortValue(folder) {
  return Number.isFinite(Number(folder.order)) ? Number(folder.order) : 999;
}

function folderDepth(folder, folders = state.folders) {
  let depth = 0;
  let parentId = folder.parentId || "";
  const seen = new Set();
  while (parentId && depth < 5 && !seen.has(parentId)) {
    seen.add(parentId);
    const parent = folders.find((item) => item.id === parentId);
    if (!parent) break;
    depth += 1;
    parentId = parent.parentId || "";
  }
  return depth;
}

function renderFolders() {
  const folders = (state.folders || []).slice().sort((a, b) => folderSortValue(a) - folderSortValue(b));
  const sections = [
    { key: "favorites", label: "Favorites" },
    { key: "folders", label: "Folders" }
  ];
  let html = "";
  for (const section of sections) {
    html += `<div class="folder-section">${section.label}</div>`;
    folders.filter((folder) => folder.section === section.key).forEach((folder) => {
      const active = state.task === "mail" && state.folder === folder.id && !state.query;
      const badge = folderBadge(folder);
      const depth = folderDepth(folder, folders);
      const color = folder.color ? ` style="color:${esc(folder.color)}"` : "";
      html += `<div class="folder-row${active ? " is-active" : ""}${depth ? " is-child" : ""}" data-folder-row="${esc(folder.id)}" style="--folder-depth:${depth}">
        <button class="folder-item${active ? " is-active" : ""}" data-folder="${esc(folder.id)}" type="button"${active ? ' aria-current="page"' : ""} title="${esc(folder.label)}">
          <svg class="icon" aria-hidden="true"${color}><use href="#${esc(folder.icon || "i-folder")}"/></svg>
          <span class="folder-name">${esc(folder.label)}</span>
          ${badge ? `<span class="folder-badge${badge.neutral ? " neutral" : ""}" data-folder-badge="${esc(folder.id)}">${badge.count > 999 ? "999+" : badge.count}</span>` : ""}
        </button>
        <button class="folder-more" data-folder-menu="${esc(folder.id)}" type="button" aria-label="Folder actions for ${esc(folder.label)}" title="Folder actions"${folder.delegated ? " hidden" : ""}><span aria-hidden="true">⋯</span></button>
      </div>`;
    });
    if (section.key === "favorites") {
      const scheduledCount = state.pendingSends.filter((item) => item.scheduled).length;
      const active = state.task === "scheduled";
      html += `<div class="folder-row"><button class="folder-item${active ? " is-active" : ""}" data-open-scheduled type="button"${active ? ' aria-current="page"' : ""}>
        <svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>
        <span class="folder-name">Scheduled</span>
        ${scheduledCount ? `<span class="folder-badge neutral">${scheduledCount}</span>` : ""}
      </button></div>`;
    }
  }
  folderList.innerHTML = html;
}

function folderMenuSystem(folder) {
  return Boolean(folder.system || ["inbox", "starred", "sent", "drafts", "archive", "spam", "trash"].includes(folder.id));
}

const folderMenuIcon = (name) => `<svg class="icon" aria-hidden="true"><use href="#i-${name}"/></svg>`;

function folderMenuHTML(folder) {
  const system = folderMenuSystem(folder);
  const emptyDisabled = system && folder.id !== "spam" && folder.id !== "trash";
  const disabled = (condition) => condition ? " disabled" : "";
  return `<div class="folder-menu-head"><svg class="icon" aria-hidden="true"><use href="#${esc(folder.icon || "i-folder")}"/></svg><div><strong>${esc(folder.label)}</strong><span>${system ? "Primary folder" : "Custom folder"}</span></div></div>
    <button type="button" data-folder-action="create-subfolder"${disabled(Boolean(folder.virtual))}>${folderMenuIcon("plus")}Create new subfolder</button>
    <button type="button" data-folder-action="rename"${disabled(system)}>${folderMenuIcon("file")}Rename</button>
    <button type="button" data-folder-action="color">${folderMenuIcon("tag")}Change color<span class="menu-chevron">›</span></button>
    <button type="button" data-folder-action="count">${folderMenuIcon("sliders")}Change folder count<span class="menu-chevron">›</span></button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="move"${disabled(system)}>${folderMenuIcon("folder")}Move</button>
    <button type="button" data-folder-action="delete"${disabled(system)}>${folderMenuIcon("trash")}Delete</button>
    <button type="button" data-folder-action="empty"${disabled(emptyDisabled)}>${folderMenuIcon("archive")}Empty</button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="mark-read">${folderMenuIcon("mail")}Mark all as read</button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="favorite">${folderMenuIcon("star")}${folder.section === "favorites" ? "Remove from Favorites" : "Add to Favorites"}</button>
    <button type="button" data-folder-action="up">${folderMenuIcon("chevron-down")}Move up</button>
    <button type="button" data-folder-action="down">${folderMenuIcon("chevron-down")}Move down</button>`;
}

function openFolderMenu(folderId, event) {
  const folder = (state.folders || []).find((item) => item.id === folderId);
  if (!folder || !folderMenu || folder.delegated) return;
  state.folderMenuTarget = folderId;
  folderMenu.innerHTML = folderMenuHTML(folder);
  folderMenu.hidden = false;
  const rect = event?.currentTarget?.getBoundingClientRect?.() || { left: event?.clientX || 120, right: event?.clientX || 120, bottom: event?.clientY || 120, top: event?.clientY || 120 };
  const width = 286;
  const left = Math.min(window.innerWidth - width - 10, Math.max(10, event?.clientX ? event.clientX : rect.right + 6));
  const top = Math.min(window.innerHeight - folderMenu.offsetHeight - 10, Math.max(10, event?.clientY ? event.clientY : rect.bottom + 4));
  folderMenu.style.left = `${left}px`;
  folderMenu.style.top = `${top}px`;
  folderMenu.style.width = `${width}px`;
}

function closeFolderMenu() {
  if (folderMenu) folderMenu.hidden = true;
  state.folderMenuTarget = "";
}

function folderMenuFolder() {
  return (state.folders || []).find((item) => item.id === state.folderMenuTarget) || null;
}

function openFolderSubmenu(kind) {
  const folder = folderMenuFolder();
  if (!folder) return;
  if (kind === "color") {
    folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>Change color</strong></div>
      <div class="folder-color-grid">
        ${["", "#2563eb", "#0f766e", "#6d28d9", "#be123c", "#b45309", "#475569", "#0369a1", "#4d7c0f"].map((color) => `<button type="button" data-folder-color="${color}" class="${(folder.color || "") === color ? "is-active" : ""}" title="${color || "Default"}"><span style="${color ? `--swatch:${color}` : ""}"></span>${color ? "" : "Default"}</button>`).join("")}
      </div>`;
    return;
  }
  folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>Show in folder</strong></div>
    ${[["unread", "Unread messages"], ["total", "All messages"], ["none", "No count"]].map(([value, label]) => `<button type="button" data-folder-badge-mode="${value}" class="${(folder.badgeMode || "unread") === value ? "is-active" : ""}"><span class="folder-count-choice">${value === "none" ? "—" : value === "total" ? "All" : "Unread"}</span>${esc(label)}${(folder.badgeMode || "unread") === value ? `<span class="folder-check">✓</span>` : ""}</button>`).join("")}`;
}

function openFolderMoveMenu() {
  const folder = folderMenuFolder();
  if (!folder) return;
  const descendants = new Set();
  const collect = (id) => (state.folders || []).filter((item) => item.parentId === id).forEach((child) => { descendants.add(child.id); collect(child.id); });
  collect(folder.id);
  const candidates = (state.folders || []).filter((item) => item.id !== folder.id && !descendants.has(item.id) && !item.virtual && !item.delegated);
  folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>Move folder</strong></div>
    <button type="button" data-folder-parent-id=""><span class="folder-count-choice">/</span>Top level</button>
    ${candidates.map((item) => `<button type="button" data-folder-parent-id="${esc(item.id)}"><span class="folder-count-choice"><svg class="icon" aria-hidden="true"><use href="#i-folder"/></svg></span>${esc(item.label)}</button>`).join("")}`;
}

async function loadFolders() {
  try {
    const data = await api("/api/folders");
    state.folders = data.folders || [];
    renderFolders();
  } catch (error) { showError(error); }
}

function openFolderEditor(mode = "create", folder = null, parentId = "") {
  state.folderEditor = { mode, id: folder?.id || "", parentId: folder?.parentId || parentId };
  document.getElementById("folderEditorTitle").textContent = mode === "rename" ? "Rename folder" : "New folder";
  document.getElementById("folderEditorSubtitle").textContent = mode === "rename" ? "Rename a custom folder. Primary folders stay fixed." : "Create a folder for rules and for moving messages.";
  document.getElementById("folderEditorName").value = folder?.label || "";
  document.getElementById("folderEditorColor").value = folder?.color || "";
  const parent = document.getElementById("folderEditorParent");
  const excluded = new Set();
  if (folder) {
    excluded.add(folder.id);
    const collect = (id) => (state.folders || []).filter((item) => item.parentId === id).forEach((child) => { excluded.add(child.id); collect(child.id); });
    collect(folder.id);
  }
  parent.innerHTML = `<option value="">Top level</option>` + (state.folders || [])
    .filter((item) => !item.virtual && !item.delegated && !excluded.has(item.id))
    .map((item) => `<option value="${esc(item.id)}"${state.folderEditor.parentId === item.id ? " selected" : ""}>${esc(item.label)}</option>`).join("");
  parent.disabled = mode === "rename";
  document.getElementById("folderEditorError").hidden = true;
  document.getElementById("folderEditorSave").textContent = mode === "rename" ? "Save changes" : "Create folder";
  folderEditorOverlay.hidden = false;
  setTimeout(() => document.getElementById("folderEditorName")?.focus(), 70);
}

function closeFolderEditor() {
  folderEditorOverlay.hidden = true;
  state.folderEditor = null;
}

async function saveFolderEditor() {
  const editor = state.folderEditor;
  if (!editor) return;
  const name = document.getElementById("folderEditorName").value.trim();
  const parentId = document.getElementById("folderEditorParent").value;
  const color = document.getElementById("folderEditorColor").value;
  const error = document.getElementById("folderEditorError");
  const button = document.getElementById("folderEditorSave");
  if (!name) { error.textContent = "Enter a folder name."; error.hidden = false; return; }
  button.disabled = true;
  try {
    if (editor.mode === "rename") {
      await api(`/api/folders/${encodeURIComponent(editor.id)}`, { method: "PATCH", body: { label: name, color } });
    } else {
      await api("/api/folders", { method: "POST", body: { label: name, parentId, color } });
    }
    closeFolderEditor();
    closeFolderMenu();
    await loadFolders();
    await refreshMailbox({ quiet: true });
    if (editor.mode === "rename") {
      try { state.sieve = await api("/api/sieve"); renderRules(); } catch {}
    }
    showToast(editor.mode === "rename" ? "Folder renamed" : "Folder created");
  } catch (saveError) {
    error.textContent = saveError.message || "Could not save the folder.";
    error.hidden = false;
  } finally {
    button.disabled = false;
  }
}

async function patchFolder(id, body, message = "Folder updated") {
  try {
    await api(`/api/folders/${encodeURIComponent(id)}`, { method: "PATCH", body });
    await loadFolders();
    closeFolderMenu();
    showToast(message);
  } catch (error) { showError(error); }
}

async function folderMenuAction(action) {
  const folder = folderMenuFolder();
  if (!folder) return;
  if (action === "create-subfolder") { closeFolderMenu(); openFolderEditor("create", null, folder.id); return; }
  if (action === "rename") { closeFolderMenu(); openFolderEditor("rename", folder); return; }
  if (action === "color") { openFolderSubmenu("color"); return; }
  if (action === "count") { openFolderSubmenu("count"); return; }
  if (action === "move") { openFolderMoveMenu(); return; }
  if (action === "favorite") {
    await patchFolder(folder.id, { section: folder.section === "favorites" ? "folders" : "favorites" }, folder.section === "favorites" ? "Removed from Favorites" : "Added to Favorites");
    return;
  }
  if (action === "mark-read") {
    closeFolderMenu();
    try {
      const result = await api(`/api/folders/${encodeURIComponent(folder.id)}/mark-read`, { method: "POST", body: {} });
      await refreshMailbox({ quiet: true });
      showToast(`${result.affected || 0} message${result.affected === 1 ? "" : "s"} marked read`);
    } catch (error) { showError(error); }
    return;
  }
  if (action === "empty") {
    closeFolderMenu();
    if (!(await appConfirm({ title: `Empty ${folder.label}?`, message: "Every message in this folder will be permanently deleted. This cannot be undone.", confirmLabel: "Empty folder", tone: "danger" }))) return;
    try {
      const result = await api(`/api/folders/${encodeURIComponent(folder.id)}/empty`, { method: "POST", body: {} });
      await refreshMailbox({ quiet: true });
      showToast(`${result.removed || 0} message${result.removed === 1 ? "" : "s"} removed`);
    } catch (error) { showError(error); }
    return;
  }
  if (action === "delete") {
    closeFolderMenu();
    if (!(await appConfirm({ title: `Delete ${folder.label}?`, message: "Messages in this folder move to Trash. The folder and its rules are removed.", confirmLabel: "Delete folder", tone: "danger" }))) return;
    try {
      await api(`/api/folders/${encodeURIComponent(folder.id)}`, { method: "DELETE" });
      state.sieve = await api("/api/sieve");
      await loadFolders();
      await refreshMailbox({ quiet: true });
      renderRules();
      showToast("Folder deleted");
    } catch (error) { showError(error); }
    return;
  }
  if (action === "up" || action === "down") {
    closeFolderMenu();
    const sorted = (state.folders || []).slice().sort((a, b) => folderSortValue(a) - folderSortValue(b));
    const sectionFolders = sorted.filter((item) => item.section === folder.section && !item.virtual);
    const sectionIndex = sectionFolders.findIndex((item) => item.id === folder.id);
    const targetIndex = action === "up" ? sectionIndex - 1 : sectionIndex + 1;
    if (sectionIndex < 0 || targetIndex < 0 || targetIndex >= sectionFolders.length) return;
    const target = sectionFolders[targetIndex];
    const index = sorted.findIndex((item) => item.id === folder.id);
    const swap = sorted.findIndex((item) => item.id === target.id);
    [sorted[index], sorted[swap]] = [sorted[swap], sorted[index]];
    try {
      await api("/api/folders/order", { method: "PUT", body: { ids: sorted.map((item) => item.id) } });
      state.folders = sorted.map((item, order) => ({ ...item, order }));
      renderFolders();
      showToast("Folder order saved");
    } catch (error) { showError(error); }
  }
}

/* ============================================================
   list
   ============================================================ */
function unreadInboxCount() {
  return state.messages.filter((message) => message.folder === "inbox" && message.unread).length;
}

function threadSubject(thread) {
  const subject = (thread && (thread.subject || (thread.latest && thread.latest.subject))) || "(no subject)";
  return subject.replace(/^(?:(?:re|fwd|fw):s*)+/i, "").trim() || "(no subject)";
}

function categoryDots(categories) {
  return (categories || []).map((key) => CATEGORIES[key]
    ? `<span class="msg-cat-dot" style="--cat:${CATEGORIES[key].color}" title="${esc(CATEGORIES[key].label)}"></span>`
    : "").join("");
}

function threadSearchToken(thread) {
  if (!state.query) return "";
  const rep = representativeMessage(thread);
  const label = folderDef(rep.folder).label;
  return `<span class="msg-folder-tag" title="Found in ${esc(label)}">${esc(label)}</span>`;
}

function renderList() {
  const threads = computeView();
  const def = folderDef(state.folder);
  listTitle.textContent = state.query ? "Search results" : def.label;
  const scopeCount = state.viewScope.length;
  if (state.query) {
    listCount.textContent = inboxViewIndividual()
      ? `${scopeCount} ${scopeCount === 1 ? "message" : "messages"} · ${threads.length} shown`
      : `${scopeCount} ${scopeCount === 1 ? "message" : "messages"} · ${threads.length} ${viewUnit(threads.length)}`;
  } else if (state.filter === "unread") {
    listCount.textContent = `${threads.length} unread`;
  } else if (state.filter === "starred") {
    listCount.textContent = `${threads.length} starred`;
  } else if (state.folder === "inbox") {
    listCount.textContent = `${unreadInboxCount()} unread`;
  } else {
    listCount.textContent = `${threads.length} ${viewUnit(threads.length)}`;
  }

  if (!threads.length) {
    let emptyTitle = "This folder is empty";
    let emptyText = `Messages you move to ${def.label} will appear here.`;
    let emptyIcon = "i-folder";
    if (state.query) {
      emptyTitle = "No messages found";
      emptyText = state.searchErrors.length ? "Check the search operator in the summary above." : "Try a different search term, or clear the search to see all mail.";
      emptyIcon = "i-search";
    } else if (state.filter === "unread") {
      emptyTitle = "No unread messages";
      emptyText = `You are all caught up in ${def.label}.`;
      emptyIcon = "i-check";
    } else if (state.filter === "starred") {
      emptyTitle = "No starred conversations";
      emptyText = "Star a conversation to keep it close at hand.";
      emptyIcon = "i-star";
    } else if (state.filter === "attachments") {
      emptyTitle = "No messages with files";
      emptyText = "Attachments appear here when a conversation includes a file.";
      emptyIcon = "i-paperclip";
    }
    messageList.innerHTML = `<div class="list-empty">
      <div class="list-empty-icon"><svg class="icon" aria-hidden="true"><use href="#${emptyIcon}"/></svg></div>
      <h2>${emptyTitle}</h2>
      <p>${emptyText}</p>
      ${(state.query || state.filter !== "all") ? `<button class="btn-secondary" data-clear-filters type="button">Clear filters</button>` : ""}
    </div>`;
  } else {
    messageList.innerHTML = threads.map((thread) => {
      const { rep, person, label } = threadRowDisplay(thread);
      const selected = thread.id === state.selectedThreadId;
      const checked = state.selectedIds.has(thread.id);
      const hasFiles = thread.hasAttachments;
      const unread = thread.unreadCount > 0;
      const safeId = esc(thread.id);
      const threadCount = thread.count > 1 ? `<span class="msg-thread-count" title="${thread.count} messages in conversation">${thread.count}</span>` : "";
      return `<div class="msg${unread ? " is-unread" : ""}${selected ? " is-selected" : ""}${checked ? " is-checked" : ""}${rep.draft ? " is-draft" : ""}" data-thread-id="${safeId}" role="option" aria-selected="${selected ? "true" : "false"}" tabindex="0">
        <label class="row-check" title="Select conversation">
          <input type="checkbox" data-thread-check="${safeId}" ${checked ? "checked" : ""} aria-label="Select ${esc(thread.subject)}">
          <span class="check-visual" aria-hidden="true"><svg class="icon"><use href="#i-check"/></svg></span>
        </label>
        ${avatar(person, "md")}
        <div class="msg-main">
          <div class="msg-top">
            <span class="msg-from">${esc(label)}</span>
            ${threadCount}
            ${hasFiles ? `<span class="msg-tag" title="Has attachments"><svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg></span>` : ""}
            ${thread.flagged ? '<span class="msg-flag" title="Flagged for follow-up" aria-label="Flagged for follow-up"><svg class="icon" aria-hidden="true"><use href="#i-flag"/></svg></span>' : ""}
            ${threadSearchToken(thread)}
            <button class="star-btn pin-btn${thread.pinned ? " is-pinned" : ""}" data-thread-pin="${safeId}" type="button" aria-label="${thread.pinned ? "Unpin conversation" : "Pin conversation"}" aria-pressed="${thread.pinned ? "true" : "false"}" title="${thread.pinned ? "Unpin conversation" : "Pin conversation"}">
              <svg class="icon" aria-hidden="true"><use href="#i-pin"/></svg>
            </button>
            <button class="star-btn${thread.starred ? " is-starred" : ""}" data-thread-star="${safeId}" type="button" aria-label="${thread.starred ? "Remove star" : "Star conversation"}" aria-pressed="${thread.starred ? "true" : "false"}" title="${thread.starred ? "Remove star" : "Star conversation"}">
              <svg class="icon" aria-hidden="true"><use href="#i-star"/></svg>
            </button>
            <span class="msg-time">${esc(formatListWhen(rep.date))}</span>
          </div>
          <div class="msg-subject">${rep.draft ? '<span class="msg-draft-tag">Draft</span>' : ""}${categoryDots(rep.categories)}${esc(threadSubject(thread))}</div>
          <div class="msg-snippet">${esc(rep.snippet || htmlToText(rep.bodyHtml).slice(0, 150))}</div>
        </div>
      </div>`;
    }).join("");
  }

  statusRange.textContent = threads.length ? `1–${threads.length} of ${threads.length} ${viewUnit(threads.length)}` : `0 ${viewUnit(0)}`;
  renderFolders();
  renderSearchScope(threads);
  syncSelectionUi();
  syncCommandBar();
  syncSearchContext();
}

function renderSearchScope(threads) {
  if (!state.query) {
    searchScopeRow.hidden = true;
    return;
  }
  searchScopeRow.hidden = false;
  const parsed = state.searchParsed || {};
  const scope = parsed.folder ? folderDef(parsed.folder).label : "All mail";
  const chips = [];
  for (const value of parsed.from || []) chips.push(`<span class="search-token">from: ${esc(value)}</span>`);
  for (const value of parsed.subject || []) chips.push(`<span class="search-token">subject: ${esc(value)}</span>`);
  for (const value of parsed.filename || []) chips.push(`<span class="search-token">filename: ${esc(value)}</span>`);
  if (parsed.hasAttachment) chips.push('<span class="search-token">has: attachment</span>');
  if (parsed.unread !== null && parsed.unread !== undefined) chips.push(`<span class="search-token">is: ${parsed.unread ? "unread" : "read"}</span>`);
  if (parsed.starred) chips.push('<span class="search-token">is: starred</span>');
  if (parsed.before) chips.push(`<span class="search-token">before: ${new Date(parsed.before).toISOString().slice(0, 10)}</span>`);
  if (parsed.after) chips.push(`<span class="search-token">after: ${new Date(parsed.after).toISOString().slice(0, 10)}</span>`);
  const errors = state.searchErrors.length ? `<span class="search-error">${esc(state.searchErrors.join(" · "))}</span>` : "";
  const resultLabel = inboxViewIndividual() ? "shown" : viewUnit(threads.length);
  searchSummary.innerHTML = `<strong>${state.viewScope.length}</strong> message${state.viewScope.length === 1 ? "" : "s"} · <strong>${threads.length}</strong> ${resultLabel} in ${esc(scope)}${chips.length ? `<span class="search-tokens">${chips.join("")}</span>` : ""}${errors}`;
  const clearButton = searchScopeRow.querySelector("#searchClearInline");
  if (clearButton) clearButton.onclick = () => clearSearch();
}

/* ============================================================
   reader
   ============================================================ */
function trustStatus(message) {
  if (!message.external) return { tone: "ok", text: "Internal sender", detail: "Directory verified · DKIM pass · TLS 1.3" };
  const failed = ["fail", "softfail", "none"].some((value) => [message.auth?.spf, message.auth?.dkim, message.auth?.dmarc].includes(value));
  const authDetail = `SPF ${message.auth?.spf || "none"} · DKIM ${message.auth?.dkim || "none"} · DMARC ${message.auth?.dmarc || "none"} · TLS ${message.auth?.tls || "1.3"}`;
  if (!tenantExternalTag()) {
    return {
      tone: failed ? "danger" : "ok",
      text: failed ? "Sender authentication problem" : "Sender authentication passed",
      detail: authDetail
    };
  }
  if (message.trusted) return {
    tone: failed ? "external" : "ok",
    text: failed ? "Trusted sender · authentication still failing" : "Trusted external sender",
    detail: `SPF ${message.auth?.spf || "none"} · DKIM ${message.auth?.dkim || "none"} · DMARC ${message.auth?.dmarc || "none"}`
  };
  return {
    tone: failed ? "danger" : "external",
    text: failed ? "External sender · authentication problem" : "External sender",
    detail: authDetail
  };
}

function authChip(name, result) {
  const tone = ["pass"].includes(result) ? "pass" : ["fail", "softfail"].includes(result) ? "fail" : "none";
  return `<span class="auth-chip ${tone}" title="${esc(name)}: ${esc(result || "none")}">${esc(name)} <strong>${esc(result || "none")}</strong></span>`;
}

function threadAuthChips(message) {
  const auth = message.auth || {};
  return `<span class="trust-checks">${authChip("SPF", auth.spf)}${authChip("DKIM", auth.dkim)}${authChip("DMARC", auth.dmarc)}</span>`;
}

function tenantExternalTag() {
  const policy = state.tenantPolicy || {};
  return !policy.mail || policy.mail.externalTag !== false;
}

function tenantQuarantineAdminOnly() {
  const policy = state.tenantPolicy || {};
  return Boolean(policy.mail && policy.mail.quarantine === true);
}

function canReleaseQuarantinedMail() {
  if (!tenantQuarantineAdminOnly()) return true;
  return ["owner", "admin"].includes(String(state.user?.role || "").toLowerCase());
}

function externalBannerMarkup(thread) {
  if (!state.externalWarnings || !tenantExternalTag()) return "";
  const external = thread.messages.filter((message) => message.external && !message.trusted);
  if (!external.length) return "";
  const message = external[external.length - 1];
  const auth = message.auth || {};
  const failed = ["fail", "softfail"].includes(auth.spf) || auth.dkim === "fail" || auth.dmarc === "fail";
  const warnings = auth.warnings || [];
  return `<div class="external-banner${failed ? " is-danger" : ""}" role="note">
    <svg class="icon" aria-hidden="true"><use href="#${failed ? "i-alert" : "i-alert"}"/></svg>
    <div>
      <strong>${failed ? "This sender failed authentication" : "External sender"}</strong>
      <span>${failed
        ? `SPF ${esc(auth.spf || "none")} · DKIM ${esc(auth.dkim || "none")} · DMARC ${esc(auth.dmarc || "none")}. Do not share confidential information or open unexpected attachments.`
        : "This message came from outside Alt8Mail. Verify the sender before sharing confidential information or opening attachments."}</span>
      ${warnings.length ? `<ul class="external-warnings">${warnings.map((warning) => `<li>${esc(warning)}</li>`).join("")}</ul>` : ""}
    </div>
    <div class="banner-actions">
      <button class="banner-action" data-details="${esc(message.id)}" type="button">Security details</button>
      <button class="banner-action" data-trust-sender="${esc(message.id)}" type="button">Mark sender trusted</button>
    </div>
  </div>`;
}

function delegationBanner(message) {
  if (!message.delegateOwnerId) return "";
  const canSend = message.delegateCanSend === true;
  return `<div class="delegation-banner">
    <span class="delegation-badge"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg></span>
    <div><strong>Viewing ${esc(message.delegateOwnerName || "a delegated")}'s Inbox</strong><p>${canSend ? "You have read and reply access. Replies are sent from your own address." : "You have read-only access. Ask the mailbox owner or an administrator for reply permission."}</p></div>
    <span class="delegation-chip">${canSend ? "Read & reply" : "Read only"}</span>
  </div>`;
}

function delegatedThreadReadOnly(thread) {
  if (!thread || !thread.messages.some((message) => message.delegateOwnerId)) return false;
  return !thread.messages.some((message) => message.delegateCanSend === true);
}

function spamMessageBanner(message) {
  if (message.folder !== "spam") return "";
  const locked = !canReleaseQuarantinedMail();
  const copy = locked
    ? "Remote images, attachment previews/downloads and saving files are blocked. Your organization's policy requires an administrator to release this message from Junk."
    : "Remote images, attachment previews/downloads and saving files are blocked for your safety. Report it as not spam to trust this sender, or move it to Inbox once.";
  return `<div class="spam-quarantine-banner${locked ? " is-admin-only" : ""}" data-spam-banner="${esc(message.id)}">
    <span class="spam-shield"><svg class="icon" aria-hidden="true"><use href="#i-shield-alert"/></svg></span>
    <div class="spam-banner-copy">
      <strong>Alt8Mail flagged this message as Junk</strong>
      <p>${copy}</p>
    </div>
    <div class="spam-banner-actions">
      ${locked
        ? `<span class="spam-locked"><svg class="icon" aria-hidden="true"><use href="#i-lock"/></svg>Administrator release required</span>`
        : `<button class="btn-primary small" data-report-not-spam="${esc(message.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>Report Not Spam</button>
      <button class="btn-secondary small" data-move-inbox="${esc(message.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-inbox"/></svg>Move to Inbox</button>`}
    </div>
  </div>`;
}

function blockedImagesBanner(message, imagesAllowed) {
  if (message.folder === "spam" || imagesAllowed || !emailHasImages(message.bodyHtml)) return "";
  return `<div class="email-image-blocked"><svg class="icon" aria-hidden="true"><use href="#i-shield-alert"/></svg><div><strong>Images are blocked</strong><span>Load remote images through the Alt8Mail privacy proxy for this message.</span></div><button class="btn-secondary small" data-show-images="${esc(message.id)}" type="button">Show images</button></div>`;
}

function threadMessageMarkup(message, expanded) {
  const spamMessage = message.folder === "spam";
  const imagesAllowed = !spamMessage && (state.preferences.autoLoadImages !== false || state.imageOverrides.has(message.id));
  const outgoing = isSentLike(message);
  const sender = message.from || { name: "Unknown", email: "", initials: "?", color: "#64748b" };
  const peerLabel = outgoing ? `to ${recipientLabel(message)}` : "to me";
  const trust = trustStatus(message);
  const unread = message.unread;
  const cats = (message.categories || []).map((key) => CATEGORIES[key]
    ? `<span class="chip-cat" style="--cat:${CATEGORIES[key].color}"><span class="cat-dot"></span>${esc(CATEGORIES[key].label)}</span>`
    : "").join("");
  const body = expanded ? `<div class="thread-body">
      ${delegationBanner(message)}
      ${spamMessageBanner(message)}
      ${blockedImagesBanner(message, imagesAllowed)}
      <div class="trust-line${trust.tone === "danger" ? " is-external" : ""}">
        <svg class="icon" aria-hidden="true"><use href="#${trust.tone === "ok" ? "i-shield-check" : "i-alert"}"/></svg>
        <span class="trust-strong">${esc(trust.text)}</span>
        <span class="trust-dot" aria-hidden="true"></span>
        <span>${esc(trust.detail)}</span>
        ${message.external ? threadAuthChips(message) : ""}
        <button class="trust-details" data-details="${esc(message.id)}" type="button">Message details</button>
      </div>
      <div class="email-body${imagesAllowed ? "" : " images-blocked"}">${safeEmailHtml(message.bodyHtml || `<p>${esc(message.snippet || "")}</p>`, { message, allowImages: imagesAllowed })}</div>
      ${meetingMarkup(message)}
      ${attachmentMarkup(message.attachments, message)}
      <div class="thread-message-actions">
        <button type="button" data-reply-message="${esc(message.id)}"><svg class="icon" aria-hidden="true"><use href="#i-reply"/></svg>Reply</button>
        <button type="button" data-forward-message="${esc(message.id)}"><svg class="icon" aria-hidden="true"><use href="#i-forward"/></svg>Forward</button>
        <button type="button" data-details="${esc(message.id)}"><svg class="icon" aria-hidden="true"><use href="#i-info"/></svg>Details</button>
        <span class="thread-message-spacer"></span>
        ${unread ? '<span class="thread-unread-badge">Unread</span>' : ""}
      </div>
    </div>` : "";
  return `<article class="thread-msg${expanded ? " is-expanded" : ""}${unread ? " is-unread" : ""}${outgoing ? " is-outgoing" : ""}${message.external ? " is-external" : ""}" data-message-id="${esc(message.id)}">
    <div class="thread-msg-bar">
      <button class="thread-msg-toggle" data-toggle-message="${esc(message.id)}" type="button" aria-expanded="${expanded ? "true" : "false"}" aria-label="${expanded ? "Collapse" : "Expand"} message from ${esc(sender.name)}">
        ${avatar(sender, "md")}
        <span class="thread-msg-id">
          <span class="thread-msg-name">${esc(sender.name)}${outgoing ? '<span class="thread-msg-self">You</span>' : ""}</span>
          <span class="thread-msg-meta">${esc(peerLabel)}<span class="meta-dot" aria-hidden="true"></span>${esc(formatFullWhen(message.date))}</span>
        </span>
        ${expanded ? "" : `<span class="thread-msg-snippet">${esc(message.snippet || htmlToText(message.bodyHtml).slice(0, 120))}</span>`}
        <svg class="icon thread-chevron" aria-hidden="true"><use href="#i-chevron-${expanded ? "down" : "right"}"/></svg>
      </button>
      <button class="star-btn thread-msg-star${message.starred ? " is-starred" : ""}" data-message-star="${esc(message.id)}" type="button" aria-label="${message.starred ? "Remove star" : "Star message"}" aria-pressed="${message.starred ? "true" : "false"}">
        <svg class="icon" aria-hidden="true"><use href="#i-star"/></svg>
      </button>
    </div>
    ${body}
  </article>`;
}

function renderReader({ preserveScroll = false } = {}) {
  const thread = currentThread();
  if (!thread) {
    reader.classList.add("is-empty");
    const todayKey = dateKey(new Date());
    const todayEvents = eventsFor(todayKey);
    const nowMinutes = new Date().getHours() * 60 + new Date().getMinutes();
    const nextEvent = todayEvents.find((event) => {
      const [hours, minutes] = event.start.split(":").map(Number);
      return hours * 60 + minutes >= nowMinutes;
    }) || todayEvents[0] || null;
    readerContent.innerHTML = `<div class="reader-empty">
      <div class="empty-mark"><span class="empty-monogram" aria-hidden="true">M</span></div>
      <h2>No conversation selected</h2>
      <p>Select a conversation from the list to open it here. Threads expand in place, with security checks and attachments alongside.</p>
      <div class="empty-overview">
        <div class="empty-stat"><span class="empty-stat-value is-number">${unreadInboxCount()}</span><span class="empty-stat-label">unread in Inbox</span></div>
        <div class="empty-stat"><span class="empty-stat-value">${nextEvent ? esc(nextEvent.title) : "No meetings"}</span><span class="empty-stat-label">${nextEvent ? `Today · ${esc(nextEvent.start)}` : "Today"}</span></div>
        <div class="empty-stat"><span class="empty-stat-value empty-protected"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>Protected</span><span class="empty-stat-label">SPF · DKIM · DMARC</span></div>
      </div>
      <div class="empty-keys">
        <span class="empty-key"><kbd>J</kbd><kbd>K</kbd> move</span>
        <span class="empty-key"><kbd>X</kbd> select</span>
        <span class="empty-key"><kbd>C</kbd> compose</span>
        <span class="empty-key"><kbd>/</kbd> search</span>
      </div>
    </div>`;
    lastReaderId = null;
    syncCommandBar();
    syncReaderToolbar(false);
    updateAiReaderUi();
    return;
  }

  reader.classList.remove("is-empty");
  if (state.expandedThreadId !== thread.id) {
    state.expandedThreadId = thread.id;
    const unread = thread.messages.filter((message) => message.unread);
    const focus = unread.length ? unread[unread.length - 1] : thread.latest;
    state.expanded = new Set([focus.id]);
  }
  const scrollTop = preserveScroll ? readerScroll.scrollTop : 0;
  const categories = (thread.latest.categories || []).map((key) => {
    const category = CATEGORIES[key];
    return category ? `<span class="chip-cat" style="--cat:${category.color}"><span class="cat-dot"></span>${esc(category.label)}</span>` : "";
  }).join("");
  const classification = thread.latest.classification || "Internal";
  const external = thread.latest.external;
  const tagExternal = external && tenantExternalTag();
  const participantNames = thread.participants.map((person) => person.name).filter(Boolean);
  const participants = participantNames.length > 3
    ? `${participantNames.slice(0, 3).join(", ")} +${participantNames.length - 3}`
    : participantNames.join(", ");
  const unread = thread.unreadCount;
  const individualView = inboxViewIndividual();
  const first = thread.messages[0];
  const last = thread.latest;
  const dateRange = first && last && formatListWhen(first.date) !== formatListWhen(last.date)
    ? `${formatListWhen(first.date)} – ${formatListWhen(last.date)}`
    : formatListWhen(last.date);
  const readerIndex = state.viewThreads.findIndex((item) => item.id === thread.id);
  const canPrev = readerIndex > 0;
  const canNext = readerIndex >= 0 && readerIndex < state.viewThreads.length - 1;
  const canMoveInbox = thread.messages.some((message) => ["spam", "archive", "trash"].includes(message.folder));
  readerContent.innerHTML = `
    <div class="reader-subject-bar">
      <button class="reader-subject-close" data-close-reader type="button" aria-label="Close this conversation" title="Close conversation"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
      <div class="reader-subject-main reader-subject">
        <div class="reader-subject-line"><h2>${esc(threadSubject(thread))}</h2>
          <button class="star-btn${thread.starred ? " is-starred" : ""}" data-thread-star-reader="${esc(thread.id)}" type="button" aria-label="${thread.starred ? "Remove star" : "Star conversation"}" aria-pressed="${thread.starred ? "true" : "false"}"><svg class="icon" aria-hidden="true"><use href="#i-star"/></svg></button>
        </div>
        <div class="reader-subject-meta">
          ${tagExternal ? '<span class="chip-class external">External</span>' : external ? "" : `<span class="chip-class ${esc(classification.toLowerCase())}">${esc(classification)}</span>`}
          ${categories}
          <span class="thread-meta-chip">${thread.count} message${thread.count === 1 ? "" : "s"}</span>
          ${unread ? `<span class="thread-meta-chip is-unread">${unread} unread</span>` : ""}
          ${thread.flagged ? '<span class="thread-meta-chip is-flagged"><svg class="icon" aria-hidden="true"><use href="#i-flag"/></svg>Follow-up</span>' : ""}
          ${thread.pinned ? '<span class="thread-meta-chip is-pinned"><svg class="icon" aria-hidden="true"><use href="#i-pin"/></svg>Pinned</span>' : ""}
          ${thread.messages.some((message) => message.spamReport) ? '<span class="thread-meta-chip is-reported">Reported to security</span>' : ""}
          <span class="thread-meta-chip">${esc(dateRange)}</span>
          <span class="reader-subject-participants">${esc(participants || "No participants")}</span>
          ${individualView ? "" : `<button class="link-btn reader-subject-expand" id="readerExpandAll" type="button">Expand all</button><button class="link-btn reader-subject-expand" id="readerCollapseAll" type="button">Collapse all</button>`}
        </div>
      </div>
      <div class="reader-subject-actions">
        ${canMoveInbox ? `<button class="reader-subject-inbox" data-reader-inbox type="button" title="Move to Inbox"><svg class="icon" aria-hidden="true"><use href="#i-inbox"/></svg><span>Inbox</span></button>` : ""}
        <button class="reader-subject-nav" data-reader-prev type="button" aria-label="Previous conversation" title="Previous conversation"${canPrev ? "" : " disabled"}><svg class="icon" aria-hidden="true"><use href="#i-chevron-left"/></svg></button>
        <button class="reader-subject-nav" data-reader-next type="button" aria-label="Next conversation" title="Next conversation"${canNext ? "" : " disabled"}><svg class="icon" aria-hidden="true"><use href="#i-chevron-right"/></svg></button>
      </div>
    </div>
    ${readerAiCardMarkup(thread)}
    ${externalBannerMarkup(thread)}
    <div class="thread-messages">
      ${thread.messages.map((message) => threadMessageMarkup(message, state.expanded.has(message.id))).join("")}
    </div>
    <div class="thread-footer${individualView ? " is-individual" : ""}">
      ${individualView ? "" : `<button class="btn-secondary" data-reply-thread="1" type="button"><svg class="icon" aria-hidden="true"><use href="#i-reply"/></svg>Reply to conversation</button>
      <button class="btn-secondary" data-forward-thread="1" type="button"><svg class="icon" aria-hidden="true"><use href="#i-forward"/></svg>Forward</button>`}
      <span class="thread-footer-note"><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>${thread.messages.some((message) => message.external) ? "Transport checks recorded" : "Internal conversation · protected"}</span>
    </div>`;
  lastReaderId = thread.id;
  syncReaderToolbar(true);
  syncCommandBar();
  updateAiReaderUi();
  requestAnimationFrame(() => { readerScroll.scrollTop = preserveScroll ? scrollTop : 0; });
}

function updateAiReaderUi() {
  const thread = currentThread();
  const available = Boolean(state.ai?.enabled && thread && !thread.latest?.draft);
  aiReaderWraps.forEach((wrap) => { wrap.hidden = !available; });
  aiReaderToggles.forEach((button) => {
    button.setAttribute("aria-expanded", "false");
    button.disabled = !available;
  });
  closeReaderAiMenus();
  $$('[data-action="ai-reply"]').forEach((button) => {
    button.hidden = !available;
    button.classList.toggle("is-disabled", !available);
    if (available) button.removeAttribute("aria-disabled");
    else button.setAttribute("aria-disabled", "true");
  });
}

function readerAiCardMarkup(thread) {
  const result = state.aiReader;
  if (!state.ai?.enabled || !result || result.threadId !== thread.id) return "";
  const label = result.kind === "translation" ? "AI translation" : "AI summary";
  return `<div class="ai-summary-card" id="readerAiCard">
    <div class="ai-summary-card-head"><svg class="icon" aria-hidden="true"><use href="#${result.kind === "translation" ? "i-translate" : "i-sparkles"}"/></svg><strong>${label}</strong><button class="icon-btn ai-summary-close" data-ai-reader-close type="button" aria-label="Close AI result"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
    <div class="ai-summary-body${result.loading ? " ai-summary-loading" : ""}">${result.loading ? "Reading the message and preparing a concise result…" : esc(result.content || "")}</div>
    <div class="ai-summary-note">${result.loading ? "Working with your organization's AI provider." : `In ${esc(aiLanguageName(result.language))} · AI can make mistakes — verify important details.`}</div>
  </div>`;
}

function updateReaderAiCard() {
  const thread = currentThread();
  if (!thread) return;
  const existing = document.getElementById("readerAiCard");
  const markup = readerAiCardMarkup(thread);
  if (existing) {
    if (markup) existing.outerHTML = markup;
    else existing.remove();
  } else if (markup) {
    const subject = readerContent.querySelector(".reader-subject-bar");
    if (subject) subject.insertAdjacentHTML("afterend", markup);
  }
}

function aiLanguageName(code) {
  const item = (state.ai?.languages || []).find((entry) => entry.code === code);
  if (item) return item.name;
  try { return new Intl.DisplayNames([currentLocale()], { type: "language" }).of(String(code || "en").split("-")[0]) || code; }
  catch { return code || "English"; }
}

function closeReaderAiMenus() {
  aiReaderMenus.forEach((menu) => {
    menu.hidden = true;
    menu.classList.remove("is-portal");
    menu.style.left = menu.style.top = menu.style.right = menu.style.width = "";
    if (menu.aiOwner && menu.parentElement === document.body) menu.aiOwner.appendChild(menu);
  });
  aiReaderToggles.forEach((button) => { button.setAttribute("aria-expanded", "false"); });
}

function positionReaderAiMenu(wrap, menu) {
  if (!wrap || !menu) return;
  const rect = wrap.getBoundingClientRect();
  const width = Math.min(278, window.innerWidth - 24);
  const left = Math.min(window.innerWidth - width - 12, Math.max(12, rect.right - width));
  menu.style.width = `${width}px`;
  menu.style.left = `${left}px`;
  menu.style.right = "auto";
  const height = menu.offsetHeight;
  let top = rect.bottom + 8;
  if (top + height > window.innerHeight - 12) top = Math.max(12, rect.top - height - 8);
  menu.style.top = `${top}px`;
}

function toggleReaderAiMenu(wrap) {
  if (!wrap) return;
  const menu = aiReaderMenus.find((entry) => entry.aiOwner === wrap);
  if (!menu) return;
  const opening = menu.hidden;
  closeReaderAiMenus();
  if (!opening) return;
  menu.hidden = false;
  menu.classList.add("is-portal");
  document.body.appendChild(menu);
  positionReaderAiMenu(wrap, menu);
  requestAnimationFrame(() => positionReaderAiMenu(wrap, menu));
  wrap.querySelectorAll("[data-ai-toggle]").forEach((button) => button.setAttribute("aria-expanded", "true"));
}

async function runReaderAi(kind) {
  const thread = currentThread();
  if (!thread || !state.ai?.enabled) return;
  const message = thread.latest;
  if (!message?.id) return;
  closeReaderAiMenus();
  const language = state.preferences.language || "en-US";
  state.aiReader = { threadId: thread.id, kind, language, content: "", loading: true };
  updateReaderAiCard();
  try {
    const endpoint = kind === "translation" ? "translate" : "summary";
    const data = await api(`/api/ai/messages/${encodeURIComponent(message.id)}/${endpoint}`, { method: "POST", body: { language } });
    if (state.aiReader?.threadId !== thread.id) return;
    const content = kind === "translation" ? data.translation : data.summary;
    state.aiReader = { threadId: thread.id, kind, language: data.language || language, content, loading: false };
    updateReaderAiCard();
  } catch (error) {
    if (state.aiReader?.threadId !== thread.id) return;
    state.aiReader = { threadId: thread.id, kind, language, content: error.message || "AI request failed.", loading: false };
    updateReaderAiCard();
    showError(error);
  }
}

function closeReaderAi() {
  state.aiReader = null;
  document.getElementById("readerAiCard")?.remove();
}

function syncReaderToolbar(enabled) {
  const buttons = $$("#readerToolbar [data-action]");
  buttons.forEach((button) => {
    button.classList.toggle("is-disabled", !enabled);
    if (enabled) button.removeAttribute("aria-disabled");
    else button.setAttribute("aria-disabled", "true");
  });
  const expand = document.getElementById("threadExpandAll");
  const collapse = document.getElementById("threadCollapseAll");
  const individualView = inboxViewIndividual();
  if (expand) { expand.disabled = !enabled; expand.hidden = individualView; }
  if (collapse) { collapse.disabled = !enabled; collapse.hidden = individualView; }
  const thread = enabled ? currentThread() : null;
  const canReplyAll = Boolean(thread && replyAllAvailable(thread));
  $$('#readerToolbar [data-action="reply-all"]').forEach((button) => { button.hidden = !canReplyAll; });
  if (readerFlagBtn) {
    const flagged = Boolean(thread && thread.flagged);
    readerFlagBtn.setAttribute("aria-pressed", flagged ? "true" : "false");
    readerFlagBtn.setAttribute("aria-label", flagged ? "Remove follow-up flag" : "Flag for follow-up");
    readerFlagBtn.setAttribute("title", flagged ? "Remove follow-up flag" : "Flag for follow-up");
    const label = readerFlagBtn.querySelector(".tool-label");
    if (label) label.textContent = flagged ? "Unflag" : "Flag";
  }
  if (readerPinBtn) {
    const pinned = Boolean(thread && thread.pinned);
    readerPinBtn.setAttribute("aria-pressed", pinned ? "true" : "false");
    readerPinBtn.classList.toggle("is-pinned", pinned);
    readerPinBtn.setAttribute("aria-label", pinned ? "Unpin email" : "Pin email");
    readerPinBtn.setAttribute("title", pinned ? "Unpin email" : "Pin email to the top of this view");
    if (readerPinLabel) readerPinLabel.textContent = pinned ? "Unpin" : "Pin";
  }
  if (readerSpamBtn) {
    const isSpam = Boolean(thread && thread.messages.some((message) => message.folder === "spam"));
    const locked = isSpam && !canReleaseQuarantinedMail();
    readerSpamBtn.disabled = locked;
    const label = isSpam ? "Not junk" : "Junk";
    if (readerSpamLabel) readerSpamLabel.textContent = label;
    if (readerSpamIcon) readerSpamIcon.setAttribute("href", isSpam ? "#i-inbox" : "#i-alert");
    readerSpamBtn.setAttribute("aria-label", locked ? "An administrator must release this message from Junk" : isSpam ? "Not junk — move back to Inbox" : "Mark as spam");
    readerSpamBtn.setAttribute("title", locked ? "Your organization requires an administrator to release quarantined mail" : isSpam ? "Not junk — move back to Inbox" : "Mark as spam");
  }
}

function renderAll() {
  renderFolders();
  renderList();
  renderReader();
}

function syncCommandBar() {
  const thread = currentThread();
  const hasThread = Boolean(thread);
  const hasSelection = state.selectedIds.size > 0;
  const hasMessage = hasThread || hasSelection;
  const delegatedLocked = delegatedThreadReadOnly(thread);
  $$(".commandbar [data-action], .reader-toolbar [data-action], #cmdMove").forEach((button) => {
    const action = button.getAttribute("data-action");
    const needsMessage = ["reply", "reply-all", "forward", "ai-reply"].includes(action);
    const enabled = needsMessage ? hasThread && !(delegatedLocked && ["reply", "reply-all", "forward"].includes(action)) : hasMessage;
    button.classList.toggle("is-disabled", !enabled);
    if (enabled) button.removeAttribute("aria-disabled");
    else button.setAttribute("aria-disabled", "true");
  });
  const canReplyAll = Boolean(thread && replyAllAvailable(thread));
  $$('.commandbar [data-action="reply-all"]').forEach((button) => { button.hidden = !canReplyAll; });
}

function meetingMarkup(message) {
  if (!message.event) return "";
  const event = message.event;
  const responses = ["accept", "tentative", "decline"];
  return `<div class="meeting-card">
    <div class="meeting-icon"><svg class="icon" aria-hidden="true"><use href="#i-calendar"/></svg></div>
    <div class="meeting-main">
      <span class="meeting-kicker">Meeting · ${event.attendees} attendees</span>
      <h3>${esc(event.title)}</h3>
      <div class="meeting-meta">
        <span><svg class="icon" aria-hidden="true"><use href="#i-clock"/></svg>${esc(event.day)} · ${esc(event.time)}</span>
        <span><svg class="icon" aria-hidden="true"><use href="#i-location"/></svg>${esc(event.where)}</span>
      </div>
    </div>
    <div class="meeting-actions">
      ${responses.map((response) => `<button data-rsvp="${response}" data-rsvp-message="${esc(message.id)}" type="button" aria-pressed="${event.response === response}" class="${event.response === response ? "is-active" : ""}">${response === "accept" ? "Accept" : response === "tentative" ? "Tentative" : "Decline"}</button>`).join("")}
    </div>
  </div>`;
}

function attachmentMarkup(files, message = null) {
  if (!files || !files.length) return "";
  const messageId = message?.id || "";
  const blocked = message?.folder === "spam";
  return `<div class="attachments${blocked ? " is-blocked" : ""}" data-attachment-message="${esc(messageId)}">
    <div class="attach-title-row"><h3 class="attach-title"><svg class="icon" aria-hidden="true"><use href="#${blocked ? "i-lock" : "i-shield-check"}"/></svg>${files.length} attachment${files.length === 1 ? "" : "s"} · ${blocked ? "Blocked in Junk" : "Scanned"}</h3>
      ${messageId && !blocked && !message?.delegateOwnerId ? `<button class="link-btn attach-save-all" data-save-all-attachments="${esc(messageId)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-cabinet"/></svg>Save all to Cabinet</button>` : ""}
    </div>
    <div class="attach-grid">
      ${files.map((file) => `<div class="attach-item">
        <button class="attach${blocked ? " is-blocked" : ""}" data-attachment="${esc(file.name)}" data-file-id="${esc(file.id || "")}" data-file-type="${esc(file.type)}" data-file-size="${esc(file.size || formatBytes(file.sizeBytes))}" data-file-mime="${esc(file.mime || "")}" data-file-url="${esc(file.contentUrl || "")}" data-file-previewable="${file.previewable ? "true" : "false"}" type="button"${blocked ? ` aria-label="Attachment blocked while this message is in Junk"` : ""}>
          <span class="attach-badge" data-type="${esc(file.type)}">${esc(file.type)}</span>
          <span class="attach-info"><span class="attach-name">${esc(file.name)}</span><span class="attach-size">${blocked ? "Blocked in Junk" : esc(file.size || formatBytes(file.sizeBytes))}</span></span>
          <svg class="icon attach-safe" role="img" aria-label="${blocked ? "Blocked in Junk" : "Scanned, no threats"}" title="${blocked ? "Blocked in Junk" : "Scanned, no threats"}"><use href="#${blocked ? "i-lock" : "i-shield-check"}"/></svg>
          <span class="attach-preview-hint">${blocked ? "Locked" : file.previewable ? "Preview" : "Info"}</span>
        </button>
        ${file.id && !blocked ? `<button class="attach-save" data-save-attachment="${esc(file.id)}" data-save-attachment-name="${esc(file.name)}" type="button" title="Save to Cabinet" aria-label="Save ${esc(file.name)} to Cabinet"><svg class="icon" aria-hidden="true"><use href="#i-cabinet"/></svg></button>` : ""}
      </div>`).join("")}
    </div>
    ${blocked ? `<p class="attach-blocked-note"><svg class="icon" aria-hidden="true"><use href="#i-lock"/></svg>Previews, downloads and saving to Cabinet are disabled while this message is in Junk. Move it to Inbox to continue.</p>` : ""}
  </div>`;
}

function cabinetDestinationId() {
  const preferred = storageProvider(state.defaultStorage);
  return preferred && preferred.connected ? preferred.id : "a8m";
}

async function saveAttachmentToCabinet(attachmentId, name, { quiet = false } = {}) {
  if (!attachmentId) {
    showToast("This attachment is not available to save", "error");
    return false;
  }
  const providerId = cabinetDestinationId();
  try {
    const data = await api("/api/files", {
      method: "POST",
      body: {
        providerId, attachmentId,
        path: "Email attachments",
        description: "Saved from a mail message to the Cabinet."
      }
    });
    if (data.file) {
      state.files = [data.file, ...state.files.filter((file) => file.id !== data.file.id)];
      if (state.task === "files") renderFiles();
      if (!quiet) showToast(`${name || data.file.name} saved to ${storageProvider(providerId)?.name || "Cabinet"}`);
    }
    return true;
  } catch (error) {
    showError(error);
    return false;
  }
}

async function saveAllAttachmentsToCabinet(messageId) {
  const message = messageById(messageId);
  if (message?.folder === "spam") {
    showToast("Attachments in Junk cannot be saved. Move this message to Inbox first.", "error");
    return;
  }
  const attachments = (message?.attachments || []).filter((file) => file.id);
  if (!attachments.length) {
    showToast("No saved attachments were found on this message", "error");
    return;
  }
  let saved = 0;
  for (const file of attachments) {
    if (await saveAttachmentToCabinet(file.id, file.name, { quiet: true })) saved += 1;
  }
  if (saved) showToast(`${saved} attachment${saved === 1 ? "" : "s"} saved to ${storageProvider(cabinetDestinationId())?.name || "Cabinet"}`);
}

function closePreview() {
  previewOverlay.hidden = true;
  state.preview = null;
  state.previewZoom = 1;
  previewBody.innerHTML = "";
  previewDownload.removeAttribute("href");
  previewOpen.removeAttribute("href");
  previewToolbar.hidden = true;
}

function setPreviewZoom(zoom) {
  if (!state.preview || !state.preview.image) return;
  state.previewZoom = Math.max(0.5, Math.min(3, zoom));
  const image = previewBody.querySelector(".preview-image");
  if (image) {
    image.style.transform = `scale(${state.previewZoom})`;
    image.style.transformOrigin = "center top";
  }
  previewZoomLabel.textContent = `${Math.round(state.previewZoom * 100)}%`;
}

function openPreview(file) {
  if (!file) return;
  const url = file.contentUrl || (file.id ? `/api/attachments/${encodeURIComponent(file.id)}/content` : "");
  const mime = file.mime || (file.type === "PDF" ? "application/pdf" : String(file.type || "").startsWith("IMG") ? "image/*" : "");
  const isImage = String(mime).startsWith("image/") || /\.(png|jpe?g|gif|webp|svg)$/i.test(file.name || "");
  const isPdf = mime === "application/pdf" || /\.pdf$/i.test(file.name || "");
  state.preview = { ...file, image: isImage, url };
  state.previewZoom = 1;
  previewTitle.textContent = file.name || "Attachment";
  previewMeta.textContent = `${file.type || "FILE"} · ${file.size || formatBytes(file.sizeBytes)}`;
  previewFootNote.textContent = "Scanned by Alt8Mail attachment protection";
  if (url) {
    previewOpen.href = url;
    previewDownload.href = url;
    previewDownload.setAttribute("download", file.name || "attachment");
    previewOpen.hidden = false;
    previewDownload.hidden = false;
  } else {
    previewOpen.hidden = true;
    previewDownload.hidden = true;
  }
  previewToolbar.hidden = true;
  if (isImage && url) {
    previewBody.innerHTML = `<div class="preview-image-stage"><img class="preview-image" src="${esc(url)}" alt="${esc(file.name)}"></div>`;
    previewToolbar.hidden = false;
    previewZoomLabel.textContent = "100%";
    previewDimensions.textContent = "";
    const image = previewBody.querySelector(".preview-image");
    image.addEventListener("load", () => {
      previewDimensions.textContent = `${image.naturalWidth} × ${image.naturalHeight}`;
    });
    image.addEventListener("error", () => {
      previewBody.innerHTML = previewFallback(file, "This image could not be rendered.");
      previewToolbar.hidden = true;
    });
    previewFootNote.textContent = "Image preview · scanned and safe to view";
  } else if (isPdf && url) {
    previewBody.innerHTML = `<iframe class="preview-pdf" src="${esc(url)}" title="${esc(file.name)}"></iframe>`;
    previewFootNote.textContent = "PDF rendered by your browser’s secure viewer · scanned and safe to view";
  } else {
    previewBody.innerHTML = previewFallback(file, url ? "This file type does not have an inline preview. Download it to open in the associated application." : "No preview content is stored with this attachment in the demo.");
  }
  previewOverlay.hidden = false;
  setTimeout(() => document.getElementById("previewClose")?.focus(), 30);
}

function previewFallback(file, message) {
  return `<div class="preview-fallback"><span class="preview-fallback-icon ${fileTypeClass(file)}">${esc(file.type || "FILE")}</span><h3>${esc(file.name || "Attachment")}</h3><p>${esc(message)}</p>${file.previewable && state.preview && state.preview.url ? `<a class="btn-primary" href="${esc(state.preview.url)}" target="_blank" rel="noopener">Open in a new tab</a>` : ""}</div>`;
}

/* ============================================================
   selection
   ============================================================ */
function syncSelectionUi() {
  const count = state.selectedIds.size;
  bulkBar.hidden = count === 0;
  if (count) {
    bulkCount.textContent = `${count} ${viewUnit(count)} selected`;
  }
  if (selectAll) {
    const visible = state.viewThreads.map((thread) => thread.id);
    const selectedVisible = visible.filter((id) => state.selectedIds.has(id)).length;
    selectAll.checked = visible.length > 0 && selectedVisible === visible.length;
    selectAll.indeterminate = selectedVisible > 0 && selectedVisible < visible.length;
  }
  if (!state.bulkMoveOpen) {
    const menu = document.getElementById("bulkMoveMenu");
    if (menu) menu.hidden = true;
  }
  syncCommandBar();
}

function toggleThreadSelection(threadId, value) {
  if (value == null) value = !state.selectedIds.has(threadId);
  if (value) state.selectedIds.add(threadId);
  else state.selectedIds.delete(threadId);
  renderList();
  syncSelectionUi();
}

function clearSelection() {
  state.selectedIds.clear();
  state.bulkMoveOpen = false;
  renderList();
  syncSelectionUi();
}

function selectAllVisible(checked) {
  state.selectedIds.clear();
  if (checked) computeView().forEach((thread) => state.selectedIds.add(thread.id));
  renderList();
  syncSelectionUi();
}

function extendSelectionTo(threadId) {
  const threads = state.viewThreads;
  const index = threads.findIndex((thread) => thread.id === threadId);
  if (index === -1) return;
  const selectedIndexes = threads.map((thread, i) => state.selectedIds.has(thread.id) ? i : -1).filter((i) => i >= 0);
  const anchor = selectedIndexes.length ? (state.selectedThreadId && state.selectedIds.has(state.selectedThreadId) ? threads.findIndex((thread) => thread.id === state.selectedThreadId) : selectedIndexes[selectedIndexes.length - 1]) : index;
  const start = Math.min(anchor, index);
  const end = Math.max(anchor, index);
  for (let i = start; i <= end; i += 1) state.selectedIds.add(threads[i].id);
  renderList();
  syncSelectionUi();
}

/* ============================================================
   thread selection + actions
   ============================================================ */
function selectThread(threadId, options = {}) {
  const thread = threadById(threadId);
  if (!thread) return;
  if (!options.keepSelection && !options.additive && !options.extend) {
    state.selectedIds.clear();
  }
  if (options.additive) {
    toggleThreadSelection(threadId);
    return;
  }
  if (options.extend) {
    extendSelectionTo(threadId);
    state.selectedThreadId = threadId;
    renderReader();
    return;
  }
  if (options.keepSelection === false) state.selectedIds.clear();
  if (thread.latest.draft || thread.messages.every((message) => message.draft)) {
    openComposeFromDraft(thread.latest, { forceOpen: true });
    renderList();
    syncSelectionUi();
    return;
  }
  state.selectedThreadId = threadId;
  if (thread.unreadCount && state.markReadOnOpen) {
    const unreadIds = thread.messages.filter((message) => message.unread).map((message) => message.id);
    thread.messages.forEach((message) => { if (message.unread) message.unread = false; });
    unreadIds.forEach((id) => state.dismissedNotifications.add("mail:" + id));
    persistDismissedNotifications();
    rebuildIndex();
    api("/api/messages/read", { method: "POST", body: { ids: unreadIds } }).catch(() => {});
    renderNotifications();
  }
  renderList();
  renderReader();
  syncSelectionUi();
  writeAppHash({ replace: true });
  if (isMobile()) document.body.classList.add("reader-open");
  const row = messageList.querySelector(`[data-thread-id="${CSS.escape(threadId)}"]`);
  if (row) row.scrollIntoView({ block: "nearest" });
}

function navigateThread(step, extend = false) {
  const threads = state.viewThreads;
  if (!threads.length) return;
  let index = threads.findIndex((thread) => thread.id === state.selectedThreadId);
  if (index === -1) index = step > 0 ? -1 : threads.length;
  const next = Math.max(0, Math.min(index + step, threads.length - 1));
  const target = threads[next];
  if (!target) return;
  if (extend) {
    extendSelectionTo(target.id);
    state.selectedThreadId = target.id;
    renderReader();
    const row = messageList.querySelector(`[data-thread-id="${CSS.escape(target.id)}"]`);
    if (row) row.scrollIntoView({ block: "nearest" });
    return;
  }
  selectThread(target.id, { keepSelection: true });
}

function replyPeople(message) {
  if (!message) return { primary: null, recipients: [] };
  const outgoing = isSentLike(message);
  const primary = outgoing
    ? ((Array.isArray(message.to) ? message.to : [message.to]).filter(Boolean)[0] || null)
    : (message.from || null);
  const recipients = [
    ...(Array.isArray(message.to) ? message.to : message.to ? [message.to] : []),
    ...(Array.isArray(message.cc) ? message.cc : message.cc ? [message.cc] : [])
  ].filter(Boolean);
  return { primary, recipients };
}

function replyRecipients(message, all = false) {
  const { primary, recipients } = replyPeople(message);
  const ownEmails = new Set([
    normalizeEmail(state.user?.email),
    ...(state.aliases || []).map((alias) => normalizeEmail(alias.email))
  ].filter(Boolean));
  const primaryEmail = normalizeEmail(primary?.email);
  const seen = new Set([...ownEmails, primaryEmail].filter(Boolean));
  const cc = [];
  if (all) {
    for (const person of recipients) {
      const email = String(person.email || "").trim();
      const normalized = normalizeEmail(email);
      if (!email.includes("@") || seen.has(normalized)) continue;
      seen.add(normalized);
      cc.push(email);
    }
  }
  return { primary, to: primary?.email || "", cc };
}

function replyAllAvailable(thread) {
  const target = thread ? replyTarget(thread) : null;
  if (!target) return false;
  return replyRecipients(target, true).cc.length > 0;
}

function replyQuoteText(message) {
  const sender = message.from?.name && message.from.name !== message.from.email
    ? `${message.from.name} <${message.from.email}>`
    : (message.from?.email || "the sender");
  const body = message.bodyText || htmlToText(message.bodyHtml || "");
  return `On ${formatFullWhen(message.date)}, ${sender} wrote:\n${body.split("\n").map((line) => "> " + line).join("\n")}`;
}

function composeQuoteFor(message, kind = "reply") {
  const sender = message.from?.name && message.from.name !== message.from.email
    ? `${message.from.name} <${message.from.email}>`
    : (message.from?.email || "the sender");
  const imagesAllowed = message.folder !== "spam" && (state.preferences.autoLoadImages !== false || state.imageOverrides.has(message.id));
  const original = safeEmailHtml(message.bodyHtml || textToEditorHTML(message.bodyText || ""), { message, allowImages: imagesAllowed });
  if (kind === "forward") {
    const to = (message.to || []).map((person) => person.name && person.name !== person.email ? `${person.name} <${person.email}>` : person.email).filter(Boolean).join(", ");
    return {
      text: `\n\n---------- Forwarded message ----------\nFrom: ${sender}\nDate: ${formatFullWhen(message.date)}\nSubject: ${message.subject}\nTo: ${to}\n\n${message.bodyText || htmlToText(message.bodyHtml || "")}`,
      html: `<div class="compose-quote" data-compose-quote="forward"><p class="compose-quote-lead">---------- Forwarded message ----------</p><div class="compose-quote-meta"><span><b>From:</b> ${esc(sender)}</span><span><b>Date:</b> ${esc(formatFullWhen(message.date))}</span><span><b>Subject:</b> ${esc(message.subject)}</span><span><b>To:</b> ${esc(to || "undisclosed recipients")}</span></div><blockquote class="email-quote">${original}</blockquote></div>`
    };
  }
  return {
    text: replyQuoteText(message),
    html: `<div class="compose-quote" data-compose-quote="reply"><p class="compose-quote-lead">On ${esc(formatFullWhen(message.date))}, ${esc(sender)} wrote:</p><blockquote class="email-quote">${original}</blockquote></div>`
  };
}

function restoreComposeImageSources(html) {
  const template = document.createElement("template");
  template.innerHTML = String(html || "");
  template.content.querySelectorAll("img[data-email-src]").forEach((image) => {
    const original = String(image.getAttribute("data-email-src") || "").trim();
    if (/^https?:\/\//i.test(original) || /^\/\//.test(original)) {
      image.setAttribute("src", original.startsWith("//") ? `https:${original}` : original);
    }
    image.removeAttribute("data-email-src");
    image.removeAttribute("data-blocked-src");
  });
  return template.innerHTML;
}

function replyToMessage(message, all = false) {
  if (!message) return;
  if (message.delegateOwnerId && message.delegateCanSend !== true) {
    showToast("This delegated mailbox is read-only");
    return;
  }
  const plan = replyRecipients(message, all);
  const receivedAlias = state.aliases.find((alias) => alias.canSend && (message.to || []).some((person) => normalizeEmail(person.email) === normalizeEmail(alias.email)));
  const subject = /^re:/i.test(message.subject) ? message.subject : `Re: ${message.subject}`;
  const quote = composeQuoteFor(message, "reply");
  openCompose({
    toText: plan.to,
    ccText: all ? plan.cc.join(", ") : "",
    subject,
    bodyText: quote.text,
    bodyHtml: quote.html,
    fromEmail: receivedAlias ? receivedAlias.email : (state.aliases.find((alias) => alias.id === "primary") || state.aliases[0] || {}).email,
    threadId: message.threadId,
    inReplyTo: message.messageId,
    kind: "reply"
  });
}

async function replyWithAi(thread = currentThread()) {
  if (!state.ai?.enabled) return;
  const target = thread ? replyTarget(thread) : null;
  if (!target) return;
  const plan = replyRecipients(target, false);
  const receivedAlias = state.aliases.find((alias) => alias.canSend && (target.to || []).some((person) => normalizeEmail(person.email) === normalizeEmail(alias.email)));
  const subject = /^re:/i.test(target.subject) ? target.subject : `Re: ${target.subject}`;
  const quoteData = composeQuoteFor(target, "reply");
  const session = openCompose({
    toText: plan.to,
    ccText: "",
    subject,
    bodyText: quoteData.text,
    bodyHtml: quoteData.html,
    fromEmail: receivedAlias ? receivedAlias.email : (state.aliases.find((alias) => alias.id === "primary") || state.aliases[0] || {}).email,
    threadId: target.threadId,
    inReplyTo: target.messageId,
    kind: "reply"
  });
  if (!session) return;
  const quote = quoteData.text;
  session.aiReplyPending = true;
  const status = session.el?.querySelector("[data-compose-status]");
  if (status) status.textContent = "AI is drafting a reply from the conversation…";
  try {
    const language = state.preferences.language || "en-US";
    const data = await api(`/api/ai/messages/${encodeURIComponent(target.id)}/reply`, { method: "POST", body: { language } });
    if (!sessionById(session.id)) return;
    const generated = String(data.reply || "").trim();
    if (!generated) throw new Error("The AI provider returned an empty reply");
    const editor = session.el?.querySelector("[data-compose-editor]");
    const textarea = session.el?.querySelector('[data-compose-field="body"]');
    const current = String(editor ? editorPlainText(editor.innerHTML) : (textarea?.value || "")).trim();
    const untouched = !current || current === quote.trim();
    setComposeBodyText(session, untouched ? `${generated}\n\n${quote}` : `${generated}\n\n${current}`);
    session.aiReplyPending = false;
    updateComposeStatus(session);
    showToast("AI reply drafted — review it before sending");
  } catch (error) {
    session.aiReplyPending = false;
    updateComposeStatus(session);
    showError(error);
  }
}

function forwardMessage(message) {
  if (!message) return;
  const quote = composeQuoteFor(message, "forward");
  openCompose({
    toText: "",
    subject: /^fwd:/i.test(message.subject) ? message.subject : `Fwd: ${message.subject}`,
    bodyText: quote.text,
    bodyHtml: quote.html,
    fromEmail: (state.aliases.find((alias) => alias.id === "primary") || state.aliases[0] || {}).email,
    threadId: null,
    inReplyTo: null,
    kind: "forward"
  });
}

function replyTarget(thread) {
  if (!thread) return null;
  const incoming = thread.messages.filter((message) => !isSentLike(message) && !message.draft);
  return incoming.length ? incoming[incoming.length - 1] : thread.latest;
}

function senderEmailsForThreads(threads) {
  const senders = new Set();
  for (const thread of threads || []) {
    for (const message of thread.messages || []) {
      const email = normalizeEmail(message.from?.email);
      if (!email || message.draft || isSentLike(message)) continue;
      if ((state.aliases || []).some((alias) => normalizeEmail(alias.email) === email)) continue;
      senders.add(email);
    }
  }
  return Array.from(senders);
}

function autoSenderRule(rule) {
  return String(rule?.name || "").startsWith("Auto · ");
}

function ruleSenderAddresses(rule) {
  return (rule?.conditions || [])
    .filter((condition) => condition.field === "from" && ["is", "contains", "matches"].includes(condition.operator))
    .map((condition) => String(condition.value || "").toLowerCase());
}

function ruleMovesSenderTo(rule, email, folder) {
  if (String(rule?.action?.type) !== "move" || String(rule?.action?.value) !== folder) return false;
  const candidate = String(email || "").toLowerCase();
  return ruleSenderAddresses(rule).some((value) => value === candidate || value.includes(candidate));
}

async function updateSenderRules(emails, targetFolder, { keepRules = false, message = "" } = {}) {
  if (!emails.length || !state.sieve) return false;
  if (keepRules) return false;
  const target = (state.folders || []).find((folder) => folder.id === targetFolder);
  if (!target) return false;
  const rules = (state.sieve.rules || []).slice();
  const next = rules.filter((rule) => {
    if (String(rule?.action?.type) !== "move" || autoSenderRule(rule) === false && !ruleSenderAddresses(rule).length) return true;
    const addresses = ruleSenderAddresses(rule);
    return !addresses.some((value) => emails.some((email) => value === email || value.includes(email)));
  });
  for (const email of emails) {
    const rule = {
      id: `rule_${uid("r").slice(2, 14)}`,
      name: truncateText(`Auto · ${target.label}: ${email}`, 80),
      match: "all",
      conditions: [{ field: "from", operator: "is", value: email }],
      action: { type: "move", value: target.id },
      enabled: true,
      priority: next.length + 1
    };
    if (!next.some((item) => item.name.toLowerCase() === rule.name.toLowerCase())) next.push(rule);
  }
  const changed = JSON.stringify(next) !== JSON.stringify(state.sieve.rules || []);
  if (!changed) return false;
  return saveSieve(next, state.sieve.vacation, message || `Sender rule saved to route this address to ${target.label}`);
}

function truncateText(value, limit) {
  const text = String(value || "");
  return text.length > limit ? text.slice(0, limit) : text;
}

async function removeSenderFolderRules(emails, folder) {
  if (!emails.length || !state.sieve) return false;
  const rules = (state.sieve.rules || []).filter((rule) =>
    !emails.some((email) => ruleMovesSenderTo(rule, email, folder))
  );
  if (rules.length === (state.sieve.rules || []).length) return false;
  return saveSieve(rules, state.sieve.vacation, "Junk sender rule removed");
}

async function sendSpamActionForMessage(messageId, action) {
  try {
    await api("/api/messages/actions", { method: "POST", body: { ids: [messageId], action } });
    await refreshMailbox({ quiet: true });
    renderList();
    renderReader({ preserveScroll: true });
    syncSelectionUi();
    return true;
  } catch (error) {
    showError(error);
    return false;
  }
}

async function reportNotSpamMessage(messageId) {
  if (!canReleaseQuarantinedMail()) {
    showToast("Your organization requires an administrator to release quarantined mail");
    return;
  }
  const message = messageById(messageId);
  if (!message) return;
  const email = normalizeEmail(message.from?.email);
  if (!(await sendSpamActionForMessage(messageId, "notSpam"))) return;
  if (email) {
    await updateSenderRules([email], "inbox", { message: "Sender trusted — future mail goes to Inbox" });
  }
  showToast("Reported not spam · sender will reach Inbox");
  renderFolders();
}

async function moveSpamMessageToInbox(messageId) {
  if (!canReleaseQuarantinedMail()) {
    showToast("Your organization requires an administrator to release quarantined mail");
    return;
  }
  if (await sendSpamActionForMessage(messageId, "notSpam")) {
    showToast("Moved to Inbox · no sender rule created");
  }
}

async function performBulkAction(action, folderValue, options = {}) {
  const threads = state.selectedIds.size ? selectedThreads() : currentThread() ? [currentThread()] : [];
  if (!threads.length) {
    showToast("Select a conversation first");
    return;
  }
  const releasingQuarantine = action === "notSpam" && threads.some((thread) => thread.messages.some((message) => message.folder === "spam"));
  if (releasingQuarantine && !canReleaseQuarantinedMail()) {
    showToast("Your organization requires an administrator to release quarantined mail");
    return;
  }
  if (action === "notSpam" && !options.confirmed) {
    const ok = await appConfirm({
      title: "Report not junk?",
      message: "This moves the message to Inbox and removes any of your rules that sent this sender to Junk.",
      confirmLabel: "Yes, not junk",
      tone: "default",
      icon: "i-shield-check"
    });
    if (!ok) return;
    options = { ...options, confirmed: true };
  }
  const ids = actionMessageIds(threads);
  if (!ids.length) {
    showToast("There is nothing to change");
    return;
  }
  closeMenus();
  const previousThreadId = state.selectedThreadId;
  const previousIndex = Math.max(0, state.viewThreads.findIndex((thread) => thread.id === previousThreadId));
  setBulkBusy(true);
  try {
    const result = await api("/api/messages/actions", {
      method: "POST",
      body: { ids, action, folder: folderValue }
    });
    const verb = action === "archive" ? "Archived" : action === "move" ? `Moved to ${folderDef(folderValue).label}` : action === "delete" ? "Deleted" : action === "markRead" ? "Marked read" : action === "markUnread" ? "Marked unread" : action === "star" ? "Starred" : action === "spam" ? "Marked as spam" : action === "notSpam" ? "Moved back to Inbox" : action === "flag" ? "Flagged for follow-up" : action === "unflag" ? "Follow-up flag cleared" : "Unstarred";
    const count = threads.length;
    const undoable = ["archive", "move", "delete", "spam", "notSpam"].includes(action);
    state.selectedIds.clear();
    await refreshMailbox({ quiet: true });
    const stillVisible = state.selectedThreadId ? threadById(state.selectedThreadId) : null;
    let closedReader = false;
    if (!stillVisible || !threadScopeMessages(stillVisible).length) {
      state.selectedThreadId = null;
      state.expandedThreadId = null;
      document.body.classList.remove("reader-open");
      renderReader();
      closedReader = true;
    }
    if (closedReader && previousThreadId && state.preferences.autoAdvance && ["archive", "delete", "spam", "notSpam"].includes(action) && state.task === "mail") {
      const next = state.viewThreads[Math.min(previousIndex, Math.max(0, state.viewThreads.length - 1))];
      if (next) selectThread(next.id, { keepSelection: true });
    }
    const senders = senderEmailsForThreads(threads);
    if (!options.keepRules && senders.length) {
      if (action === "spam") await updateSenderRules(senders, "spam");
      else if (action === "move" && options.alwaysRule) await updateSenderRules(senders, folderValue);
      else if (action === "notSpam") {
        if (options.inboxRule) await updateSenderRules(senders, "inbox");
        else await removeSenderFolderRules(senders, "spam");
      }
    }
    if (undoable && result.undoToken) {
      showToast(`${verb} · ${count} conversation${count === 1 ? "" : "s"}`, "Undo", async () => {
        try {
          await api(`/api/undo/${encodeURIComponent(result.undoToken)}`, { method: "POST", body: {} });
          await refreshMailbox({ quiet: true });
          showToast("Restored");
        } catch (error) {
          showError(error);
        }
      }, { duration: 12000 });
    } else {
      showToast(`${verb} · ${count} conversation${count === 1 ? "" : "s"}`);
    }
    updateQuotaUi(result.quota || null);
    if (!state.selectedThreadId) writeAppHash({ replace: true });
  } catch (error) {
    showError(error);
  } finally {
    setBulkBusy(false);
  }
}

function setBulkBusy(busy) {
  bulkBar.classList.toggle("is-busy", busy);
  $$("#bulkBar button").forEach((button) => { button.disabled = busy; });
}

async function toggleThreadStar(threadId) {
  const thread = threadById(threadId);
  if (!thread) return;
  const starred = !thread.starred;
  thread.messages.forEach((message) => { message.starred = starred; });
  renderList();
  renderReader();
  try {
    await api("/api/messages/actions", {
      method: "POST",
      body: { ids: thread.messages.map((message) => message.id), action: starred ? "star" : "unstar" }
    });
    rebuildIndex();
    showToast(starred ? "Added to Starred" : "Removed from Starred");
  } catch (error) {
    thread.messages.forEach((message) => { message.starred = !starred; });
    renderList();
    renderReader();
    showError(error);
  }
}

async function toggleThreadPin(thread = currentThread()) {
  if (!thread) return;
  const pinned = !thread.pinned;
  thread.pinned = pinned;
  thread.messages.forEach((message) => { message.pinned = pinned; });
  rebuildIndex();
  renderList();
  renderReader({ preserveScroll: true });
  try {
    await api("/api/messages/actions", {
      method: "POST",
      body: { ids: thread.messages.filter((message) => !message.draft).map((message) => message.id), action: pinned ? "pin" : "unpin" }
    });
    rebuildIndex();
    renderList();
    renderReader({ preserveScroll: true });
    showToast(pinned ? "Email pinned" : "Email unpinned");
  } catch (error) {
    thread.pinned = !pinned;
    thread.messages.forEach((message) => { message.pinned = !pinned; });
    rebuildIndex();
    renderList();
    renderReader({ preserveScroll: true });
    showError(error);
  }
}

async function toggleMessageStar(messageId) {
  const message = messageById(messageId);
  if (!message) return;
  message.starred = !message.starred;
  renderList();
  renderReader({ preserveScroll: true });
  try {
    await api(`/api/messages/${encodeURIComponent(messageId)}`, { method: "PATCH", body: { starred: message.starred } });
    rebuildIndex();
  } catch (error) {
    message.starred = !message.starred;
    renderList();
    renderReader({ preserveScroll: true });
    showError(error);
  }
}

async function toggleThreadFlag(thread = currentThread()) {
  if (!thread) return;
  const flagged = !thread.flagged;
  thread.flagged = flagged;
  thread.messages.forEach((message) => { message.flagged = flagged; });
  renderList();
  renderReader({ preserveScroll: true });
  try {
    await api("/api/messages/actions", {
      method: "POST",
      body: { ids: thread.messages.filter((message) => !message.draft).map((message) => message.id), action: flagged ? "flag" : "unflag" }
    });
    rebuildIndex();
    renderList();
    showToast(flagged ? "Flagged for follow-up" : "Follow-up flag cleared");
  } catch (error) {
    thread.flagged = !flagged;
    thread.messages.forEach((message) => { message.flagged = !flagged; });
    renderList();
    renderReader({ preserveScroll: true });
    showError(error);
  }
}

let pendingReportThreadId = null;
function openReportDialog(thread = currentThread()) {
  if (!thread) {
    showToast("Select a conversation first");
    return;
  }
  pendingReportThreadId = thread.id;
  const latest = thread.latest;
  const sender = latest && latest.from ? `${latest.from.name || latest.from.email} <${latest.from.email}>` : "Unknown sender";
  reportMessageCard.innerHTML = `<strong>${esc(threadSubject(thread))}</strong><span>From ${esc(sender)} · ${thread.count} message${thread.count === 1 ? "" : "s"} selected for review</span>`;
  reportNote.value = "";
  const defaultReason = reportOverlay.querySelector('input[name="reportReason"][value="phishing"]');
  if (defaultReason) defaultReason.checked = true;
  reportOverlay.hidden = false;
  setTimeout(() => {
    const checked = reportOverlay.querySelector('input[name="reportReason"]:checked') || reportOverlay.querySelector('input[name="reportReason"]');
    if (checked) checked.focus();
  }, 40);
}

function closeReportDialog() {
  reportOverlay.hidden = true;
  reportSubmit.classList.remove("is-busy");
  reportSubmit.disabled = false;
  pendingReportThreadId = null;
}

async function submitSpamReport() {
  const thread = pendingReportThreadId ? threadById(pendingReportThreadId) : currentThread();
  if (!thread) {
    closeReportDialog();
    showToast("That conversation is no longer available");
    return;
  }
  const reasonInput = reportOverlay.querySelector('input[name="reportReason"]:checked');
  const ids = thread.messages.filter((message) => message.folder !== "drafts").map((message) => message.id);
  if (!ids.length) {
    closeReportDialog();
    showToast("There is nothing to report");
    return;
  }
  reportSubmit.classList.add("is-busy");
  reportSubmit.disabled = true;
  try {
    await api("/api/messages/actions", {
      method: "POST",
      body: {
        ids,
        action: "report",
        reason: reasonInput ? reasonInput.value : "spam",
        note: reportNote.value.trim()
      }
    });
    const previousThreadId = state.selectedThreadId;
    closeReportDialog();
    await refreshMailbox({ quiet: true });
    if (previousThreadId && !state.viewThreads.some((item) => item.id === previousThreadId)) {
      state.selectedThreadId = null;
      state.expandedThreadId = null;
      document.body.classList.remove("reader-open");
    }
    renderList();
    renderReader();
    syncSelectionUi();
    const senders = senderEmailsForThreads([thread]);
    if (senders.length) await updateSenderRules(senders, "spam", { message: "Reported sender set to go to Junk" });
    showToast("Reported to Alt8Mail Security · sender blocked pending review");
  } catch (error) {
    reportSubmit.classList.remove("is-busy");
    reportSubmit.disabled = false;
    showError(error);
  }
}

function markThreadUnread(thread) {
  if (!thread) return;
  const ids = thread.messages.filter((message) => !message.draft).map((message) => message.id);
  thread.messages.forEach((message) => { message.unread = true; });
  rebuildIndex();
  renderFolders();
  renderList();
  renderReader({ preserveScroll: true });
  api("/api/messages/actions", { method: "POST", body: { ids, action: "markUnread" } })
    .then(() => showToast("Marked as unread"))
    .catch((error) => { showError(error); refreshMailbox({ quiet: true }).catch(() => {}); });
}

function closeReaderPane() {
  state.selectedThreadId = null;
  state.expandedThreadId = null;
  state.expanded = new Set();
  document.body.classList.remove("reader-open");
  renderList();
  renderReader();
  writeAppHash({ replace: true });
}

function readerAction(action) {
  const thread = currentThread();
  if (!thread) return;
  if (action === "reply") return replyToMessage(replyTarget(thread));
  if (action === "reply-all") return replyToMessage(replyTarget(thread), true);
  if (action === "ai-reply") return replyWithAi(thread);
  if (action === "forward") return forwardMessage(thread.latest);
  if (action === "archive") return performBulkAction("archive");
  if (action === "delete") return performBulkAction("delete");
  if (action === "mark-unread") return markThreadUnread(thread);
  if (action === "flag") return toggleThreadFlag(thread);
  if (action === "pin") return toggleThreadPin(thread);
  if (action === "spam") return performBulkAction(thread.messages.some((message) => message.folder === "spam") ? "notSpam" : "spam");
  if (action === "report") return openReportDialog(thread);
}

/* ============================================================
   search
   ============================================================ */
function mergeSearchMessages(results) {
  const byId = new Map(state.messages.map((message) => [message.id, message]));
  for (const message of results || []) {
    if (message && message.id) byId.set(message.id, message);
  }
  state.messages = Array.from(byId.values());
  rebuildIndex();
}

async function runServerSearch(query) {
  const sequence = ++state.searchSeq;
  if (!query || state.task !== "mail") {
    state.searchLoading = false;
    if (state.task === "mail") {
      state.searchResults = [];
      renderList();
      renderReader();
    }
    return;
  }
  state.searchLoading = true;
  try {
    const data = await api("/api/search?q=" + encodeURIComponent(query));
    if (sequence !== state.searchSeq || state.query !== query) return;
    const results = data.messages || [];
    mergeSearchMessages(results);
    state.searchResults = results;
    state.searchParsed = parseQuery(query);
    state.searchErrors = (data.parsed && data.parsed.errors) || [];
    state.searchLoading = false;
    renderList();
    renderReader();
    syncSelectionUi();
    syncSearchContext();
  } catch (error) {
    if (sequence !== state.searchSeq) return;
    state.searchLoading = false;
    state.searchResults = [];
    state.searchErrors = [error.message || "Search is unavailable"];
    renderList();
    renderReader();
    syncSearchContext();
  }
}

function scheduleServerSearch() {
  clearTimeout(state.searchTimer);
  state.searchTimer = setTimeout(() => runServerSearch(state.query), 220);
}

function setQuery(value) {
  state.query = String(value || "");
  state.selectedIds.clear();
  state.selectedThreadId = null;
  state.expandedThreadId = null;
  state.searchResults = [];
  state.searchParsed = state.query ? parseQuery(state.query) : null;
  state.searchErrors = [];
  renderList();
  renderReader();
  syncSelectionUi();
  syncSearchContext();
  if (state.task === "mail") scheduleServerSearch();
  writeAppHash({ replace: true });
}

function clearSearch() {
  state.query = "";
  searchInput.value = "";
  updateSearchClear();
  clearTimeout(state.searchTimer);
  state.searchSeq += 1;
  state.searchResults = [];
  state.searchParsed = null;
  state.searchErrors = [];
  state.searchLoading = false;
  state.selectedIds.clear();
  state.selectedThreadId = null;
  if (state.task === "tasks") {
    renderTasks();
  } else if (state.task === "files") {
    renderFiles();
  } else if (state.task === "scheduled") {
    renderScheduled();
    syncStatusRange();
  } else {
    renderList();
    renderReader();
    syncSelectionUi();
  }
  syncSearchContext();
  if (state.task === "people") renderPeople();
  writeAppHash({ replace: true });
}

function updateSearchClear() {
  const has = searchInput.value.trim().length > 0;
  searchClear.classList.toggle("is-shown", has);
  searchClear.style.display = has ? "grid" : "none";
}

function focusSearch() {
  if (state.task === "calendar" || state.task === "settings") return;
  if (isMobile()) document.body.classList.add("search-open");
  searchInput.focus();
  searchInput.select();
  openSearchHints();
}

function closeMobileSearch() {
  document.body.classList.remove("search-open");
  searchInput.blur();
  closeSearchHints();
}

function openSearchHints() {
  if (state.task !== "mail") return;
  clearTimeout(searchHintsTimer);
  if (searchInput.value.trim()) {
    searchHints.hidden = true;
    state.searchHintsOpen = false;
    return;
  }
  searchHints.hidden = false;
  state.searchHintsOpen = true;
  const scope = document.getElementById("searchScope");
  if (scope) scope.textContent = state.messages.length ? `Instant local index · ${state.messages.length} messages` : "Instant local index";
}

function closeSearchHints() {
  searchHintsTimer = setTimeout(() => {
    searchHints.hidden = true;
    state.searchHintsOpen = false;
  }, 120);
}

function insertSearchOperator(operator) {
  const value = searchInput.value;
  const needsSpace = value && !/\s$/.test(value);
  searchInput.value = `${value}${needsSpace ? " " : ""}${operator}`;
  searchInput.focus();
  searchInput.setSelectionRange(searchInput.value.length, searchInput.value.length);
  updateSearchClear();
  const complete = operator.endsWith("attachment");
  if (complete) {
    setQuery(searchInput.value);
    searchHints.hidden = true;
  }
  openSearchHints();
}

function syncSearchContext() {
  if (state.task === "people") {
    searchInput.placeholder = "Search directory";
    searchInput.setAttribute("aria-label", "Search people");
  } else if (state.task === "tasks") {
    searchInput.placeholder = "Search tasks";
    searchInput.setAttribute("aria-label", "Search tasks");
  } else if (state.task === "files") {
    searchInput.placeholder = "Search Cabinet";
    searchInput.setAttribute("aria-label", "Search Cabinet");
  } else if (state.task === "scheduled") {
    searchInput.placeholder = "Search scheduled";
    searchInput.setAttribute("aria-label", "Search scheduled messages");
  } else if (state.task === "mail") {
    searchInput.placeholder = state.query ? "Search all mail" : "Search mail";
    searchInput.setAttribute("aria-label", "Search all mail");
  }
}

/* ============================================================
   compose manager
   ============================================================ */
function sessionById(id) {
  return composeSessions.find((session) => session.id === id) || null;
}

const RECIPIENT_FIELDS = ["to", "cc", "bcc"];

function splitRecipientText(value) {
  const parts = [];
  let current = "";
  let quote = false;
  let angle = 0;
  for (const ch of String(value || "")) {
    if (ch === '"' && angle === 0) { quote = !quote; current += ch; continue; }
    if (!quote) {
      if (ch === "<") angle += 1;
      else if (ch === ">" && angle > 0) angle -= 1;
    }
    if (!quote && angle === 0 && (ch === "," || ch === ";")) {
      parts.push(current);
      current = "";
    } else {
      current += ch;
    }
  }
  parts.push(current);
  return parts;
}

function parseRecipientEntry(value) {
  const raw = String(value || "").trim();
  if (!raw) return null;
  let name = "";
  let email = raw;
  const angled = raw.match(/^(?:"([^"]*)"|([^<]*?))\s*<([^>\s]+)>\s*$/);
  if (angled) {
    name = String(angled[1] || angled[2] || "").trim();
    email = angled[3];
  } else {
    const comment = raw.match(/^([^\s()]+@[^\s()]+)\s*\(([^)]*)\)\s*$/);
    if (comment) {
      email = comment[1];
      name = comment[2].trim();
    }
  }
  email = normalizeEmail(email);
  const valid = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
  return { raw, name, email, valid };
}

function parseRecipientList(value) {
  const seen = new Set();
  const entries = [];
  for (const part of splitRecipientText(value)) {
    const entry = parseRecipientEntry(part);
    if (!entry) continue;
    const key = normalizeEmail(entry.email);
    if (key && seen.has(key)) continue;
    if (key) seen.add(key);
    entries.push(entry);
  }
  return entries;
}

function formatRecipientName(name) {
  const value = String(name || "").trim();
  if (!value || value.includes(",") || value.includes("<") || value.includes(">")) {
    return value ? `"${value.replace(/"/g, '\\"')}"` : "";
  }
  return value;
}

function formatRecipientEntry(entry) {
  if (!entry) return "";
  if (entry.name && normalizeEmail(entry.name) !== normalizeEmail(entry.email)) {
    return `${formatRecipientName(entry.name)} <${entry.email}>`;
  }
  return entry.email;
}

function recipientTextFromEntries(entries) {
  return entries.map(formatRecipientEntry).filter(Boolean).join(", ");
}

function recipientTextWithPending(session, field) {
  const base = String(session[field] || "").trim();
  const pending = String(session.recipientInput?.[field] || "").trim();
  if (!pending) return base;
  return base ? `${base}, ${pending}` : pending;
}

function recipientEntriesFor(session, field) {
  return parseRecipientList(session[field] || "");
}

function recipientPersonFor(entry) {
  return state.people.find((person) => normalizeEmail(person.email) === normalizeEmail(entry.email)) || null;
}

function recipientChipsMarkup(session, field) {
  return recipientEntriesFor(session, field).map((entry, index) => {
    const person = recipientPersonFor(entry);
    const label = entry.name || (person && person.name) || entry.email;
    const showEmail = Boolean(entry.name && normalizeEmail(entry.name) !== normalizeEmail(entry.email));
    return `<span class="recipient-chip${entry.valid ? "" : " is-invalid"}" title="${esc(entry.email || entry.raw)}">
      ${person ? avatar(person, "xs") : ""}
      <span class="recipient-chip-copy"><span class="recipient-chip-name">${esc(label)}</span>${showEmail ? `<span class="recipient-chip-email">${esc(entry.email)}</span>` : ""}</span>
      <button type="button" class="recipient-chip-remove" data-recipient-remove="${field}" data-recipient-index="${index}" aria-label="Remove ${esc(entry.email || entry.raw)}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
    </span>`;
  }).join("");
}

function recipientInputPlaceholder(session, field) {
  return recipientEntriesFor(session, field).length ? "Add another recipient" : "name@example.com";
}

function recipientCandidates(session, field) {
  const query = String(session.recipientInput?.[field] || "").trim().toLowerCase();
  const selected = new Set(recipientEntriesFor(session, field).map((entry) => normalizeEmail(entry.email)));
  const candidates = [];
  const seen = new Set();
  for (const person of state.people) {
    const email = normalizeEmail(person.email);
    if (!email || selected.has(email) || seen.has(email)) continue;
    if (query) {
      const haystack = [person.name, person.email, person.company, person.title, person.team, person.role].join(" ").toLowerCase();
      if (!haystack.includes(query)) continue;
    }
    seen.add(email);
    candidates.push(person);
  }
  candidates.sort((a, b) => {
    const aName = String(a.name || "").toLowerCase();
    const bName = String(b.name || "").toLowerCase();
    const aEmail = normalizeEmail(a.email);
    const bEmail = normalizeEmail(b.email);
    const score = (person, name, email) => {
      if (!query) return person.source === "directory" ? 0 : 1;
      if (name.startsWith(query) || email.startsWith(query)) return 0;
      if (name.split(/\s+/).some((part) => part.startsWith(query))) return 1;
      return 2;
    };
    return score(a, aName, aEmail) - score(b, bName, bEmail) || aName.localeCompare(bName);
  });
  return candidates.slice(0, 8);
}

function recipientSuggestionMarkup(session, field) {
  const people = recipientCandidates(session, field);
  const queryEntry = parseRecipientEntry(session.recipientInput?.[field] || "");
  const selected = new Set(recipientEntriesFor(session, field).map((entry) => normalizeEmail(entry.email)));
  const rows = people.map((person, index) => `
    <button type="button" class="recipient-suggestion${session.recipientHighlight === index ? " is-active" : ""}" role="option" data-recipient-suggest-email="${esc(person.email)}" data-recipient-suggest-name="${esc(person.name || "")}">
      ${avatar(person, "sm")}
      <span class="recipient-suggestion-copy"><strong>${esc(person.name || person.email)}</strong><span>${esc(person.email)}${person.company ? ` · ${esc(person.company)}` : ""}</span></span>
      <span class="recipient-suggestion-source">${person.source === "directory" ? "Directory" : person.source === "auto" ? "Auto-saved" : "Contact"}</span>
    </button>`).join("");
  const addRow = queryEntry?.valid && !selected.has(queryEntry.email) && !people.some((person) => normalizeEmail(person.email) === queryEntry.email)
    ? `<button type="button" class="recipient-suggestion recipient-suggestion-add" role="option" data-recipient-add="${esc(queryEntry.email)}" data-recipient-add-name="${esc(queryEntry.name || "")}">
        <span class="recipient-suggestion-add-icon"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg></span>
        <span class="recipient-suggestion-copy"><strong>Add ${esc(queryEntry.email)}</strong><span>Save to your contacts and add as a recipient</span></span>
      </button>`
    : "";
  if (!rows && !addRow) return `<div class="recipient-suggest-empty">No matching people or contacts.</div>`;
  return rows + addRow;
}

function updateRecipientSuggestions(session, field) {
  const input = session.el?.querySelector(`[data-recipient-input="${field}"]`);
  const panel = session.el?.querySelector(`[data-recipient-suggest="${field}"]`);
  if (!input || !panel) return;
  if (document.activeElement !== input) { panel.hidden = true; input.setAttribute("aria-expanded", "false"); return; }
  panel.innerHTML = recipientSuggestionMarkup(session, field);
  panel.hidden = false;
  input.setAttribute("aria-expanded", "true");
}

function closeRecipientSuggestions(session, field) {
  for (const name of field ? [field] : RECIPIENT_FIELDS) {
    const panel = session.el?.querySelector(`[data-recipient-suggest="${name}"]`);
    const input = session.el?.querySelector(`[data-recipient-input="${name}"]`);
    if (panel) panel.hidden = true;
    if (input) input.setAttribute("aria-expanded", "false");
  }
  if (session) session.recipientHighlight = -1;
}

function updateRecipientField(session, field) {
  const chips = session.el?.querySelector(`[data-recipient-chips="${field}"]`);
  const input = session.el?.querySelector(`[data-recipient-input="${field}"]`);
  const wrap = session.el?.querySelector(`[data-compose-field-wrap="${field}"]`);
  if (chips) chips.innerHTML = recipientChipsMarkup(session, field);
  if (input) {
    input.placeholder = recipientInputPlaceholder(session, field);
    input.value = session.recipientInput?.[field] || "";
  }
  if (wrap) wrap.classList.remove("is-error");
}

function autoSaveRecipient(entry) {
  if (!entry?.valid) return;
  const existing = state.people.find((person) => normalizeEmail(person.email) === normalizeEmail(entry.email));
  if (existing && existing.source === "directory") return;
  if (existing && existing.source !== "auto") return;
  api("/api/contacts/auto", { method: "POST", body: { email: entry.email, name: entry.name || "" } })
    .then((data) => {
      if (!data?.contact) return;
      mergeContactPerson(data.contact);
      for (const session of composeSessions) {
        for (const field of RECIPIENT_FIELDS) {
          if (recipientEntriesFor(session, field).some((item) => normalizeEmail(item.email) === normalizeEmail(data.contact.email))) {
            updateRecipientField(session, field);
          }
        }
      }
    })
    .catch(() => {});
}

function addRecipientEntry(session, field, entry, { focus = true } = {}) {
  if (!entry?.valid) return false;
  const entries = recipientEntriesFor(session, field);
  if (entries.some((item) => normalizeEmail(item.email) === normalizeEmail(entry.email))) {
    session.recipientInput[field] = "";
    updateRecipientField(session, field);
    closeRecipientSuggestions(session, field);
    return true;
  }
  entries.push(entry);
  session[field] = recipientTextFromEntries(entries);
  session.recipientInput[field] = "";
  session.dirty = true;
  session.error = null;
  updateRecipientField(session, field);
  closeRecipientSuggestions(session, field);
  autoSaveRecipient(entry);
  scheduleComposeSave(session);
  if (focus) session.el?.querySelector(`[data-recipient-input="${field}"]`)?.focus();
  return true;
}

function commitRecipientInput(session, field, { focus = true } = {}) {
  const input = session.el?.querySelector(`[data-recipient-input="${field}"]`);
  const text = String(session.recipientInput?.[field] || "").trim();
  if (!text) { closeRecipientSuggestions(session, field); return true; }
  const entries = parseRecipientList(text);
  const valid = entries.filter((entry) => entry.valid);
  if (!valid.length) {
    const wrap = session.el?.querySelector(`[data-compose-field-wrap="${field}"]`);
    if (wrap) wrap.classList.add("is-error");
    if (input) input.setAttribute("aria-invalid", "true");
    return false;
  }
  for (const entry of valid) addRecipientEntry(session, field, entry, { focus: false });
  session.recipientInput[field] = "";
  if (input) { input.value = ""; input.removeAttribute("aria-invalid"); }
  updateRecipientField(session, field);
  if (focus) input?.focus();
  return true;
}

function flushRecipientInputs(session, { schedule = false } = {}) {
  if (!session) return;
  for (const field of RECIPIENT_FIELDS) {
    if (String(session.recipientInput?.[field] || "").trim()) {
      commitRecipientInput(session, field, { focus: false });
    }
  }
  if (schedule) scheduleComposeSave(session);
}

function recipientKeydown(event, session, input) {
  const field = input.getAttribute("data-recipient-input");
  if (!field) return;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    if (input.getAttribute("aria-expanded") !== "true") updateRecipientSuggestions(session, field);
    const count = session.el?.querySelectorAll(`[data-recipient-suggest="${field}"] .recipient-suggestion`).length || 0;
    if (!count) return;
    event.preventDefault();
    const step = event.key === "ArrowDown" ? 1 : -1;
    session.recipientHighlight = ((Number(session.recipientHighlight) || 0) + step + count) % count;
    updateRecipientSuggestions(session, field);
    return;
  }
  if (event.key === "Escape") { closeRecipientSuggestions(session, field); return; }
  if (event.key === "Backspace" && !input.value && !session.recipientInput?.[field]) {
    const entries = recipientEntriesFor(session, field);
    if (entries.length) {
      event.preventDefault();
      entries.pop();
      session[field] = recipientTextFromEntries(entries);
      session.dirty = true;
      updateRecipientField(session, field);
      scheduleComposeSave(session);
    }
    return;
  }
  if (event.key === "Enter" || event.key === "Tab" || event.key === "," || event.key === ";") {
    if ((event.key === "," || event.key === ";") && !input.value.trim()) return;
    const highlighted = session.el?.querySelector(`[data-recipient-suggest="${field}"] .recipient-suggestion.is-active`);
    if (event.key === "Enter" && highlighted) {
      event.preventDefault();
      const entry = parseRecipientEntry(highlighted.getAttribute("data-recipient-suggest-email") || highlighted.getAttribute("data-recipient-add") || "");
      if (entry?.valid) {
        entry.name = highlighted.getAttribute("data-recipient-suggest-name") || highlighted.getAttribute("data-recipient-add-name") || entry.name;
        addRecipientEntry(session, field, entry);
      }
      return;
    }
    if (event.key === "Tab" && !input.value.trim()) return;
    event.preventDefault();
    commitRecipientInput(session, field);
  }
}

function recipientPaste(event, session, input) {
  const field = input.getAttribute("data-recipient-input");
  const text = event.clipboardData?.getData("text/plain") || "";
  if (!field || !text.trim()) return;
  event.preventDefault();
  const parts = splitRecipientText(text);
  const endsDelimited = /[;,]\s*$/.test(text);
  const tail = endsDelimited ? "" : parts.pop();
  for (const part of parts) {
    const entry = parseRecipientEntry(part);
    if (entry?.valid) addRecipientEntry(session, field, entry, { focus: false });
  }
  if (tail) {
    session.recipientInput[field] = tail.trim();
    updateRecipientField(session, field);
    updateRecipientSuggestions(session, field);
  }
  input.value = session.recipientInput?.[field] || "";
}

function draftMessageFromSession(session) {
  return state.messages.find((message) => message.id === session.draftId) || null;
}

function isComposeEmpty(session) {
  return !session.to.trim() && !session.cc.trim() && !session.bcc.trim()
    && !Object.values(session.recipientInput || {}).some((value) => String(value).trim())
    && !session.subject.trim() && !session.body.trim() && !session.files.length;
}

function aliasesForSelect() {
  return state.aliases.filter((alias) => alias.canSend);
}

function aliasForEmail(email) {
  return state.aliases.find((alias) => normalizeEmail(alias.email) === normalizeEmail(email)) || null;
}

function aliasDisplay(email) {
  const alias = aliasForEmail(email);
  if (alias) return alias.label && normalizeEmail(alias.label) !== normalizeEmail(alias.email) ? `${alias.label} · ${alias.email}` : alias.email;
  return email || (state.user ? state.user.email : "");
}

function composePriorityMarkup(session) {
  return [["low", "Low"], ["normal", "Normal"], ["high", "High"]].map(([value, label]) =>
    `<button class="compose-priority-btn${session.priority === value ? " is-active" : ""}" data-compose-priority="${value}" type="button" role="radio" aria-checked="${session.priority === value ? "true" : "false"}" title="${label} priority"><span class="priority-dot ${value}"></span>${label}</button>`
  ).join("");
}

function composeEditorToolbar(session) {
  const button = (cmd, label, title, content = label) =>
    `<button type="button" data-editor-cmd="${cmd}" title="${esc(title)}" aria-label="${esc(title)}">${content}</button>`;
  return `<div class="compose-editor-toolbar" role="toolbar" aria-label="Formatting">
    ${button("undo", "↶", "Undo")}${button("redo", "↷", "Redo")}
    <span class="editor-sep"></span>
    <select class="editor-font-select" data-editor-font aria-label="Font family" title="Font family">
      <option value="">Font</option>
      <option value="Arial, Helvetica, sans-serif">Arial</option>
      <option value="'Segoe UI', Tahoma, sans-serif">Segoe UI</option>
      <option value="Calibri, Candara, sans-serif">Calibri</option>
      <option value="'Helvetica Neue', Helvetica, Arial, sans-serif">Helvetica</option>
      <option value="Verdana, Geneva, sans-serif">Verdana</option>
      <option value="Tahoma, Verdana, sans-serif">Tahoma</option>
      <option value="'Trebuchet MS', Helvetica, sans-serif">Trebuchet MS</option>
      <option value="Georgia, 'Times New Roman', serif">Georgia</option>
      <option value="'Times New Roman', Times, serif">Times New Roman</option>
      <option value="Garamond, Georgia, serif">Garamond</option>
      <option value="'Courier New', Courier, monospace">Courier New</option>
    </select>
    <select class="editor-size-select" data-editor-size aria-label="Font size" title="Font size">
      <option value="">Size</option>
      <option value="11">11</option>
      <option value="12">12</option>
      <option value="13">13</option>
      <option value="15">15</option>
      <option value="17">17</option>
      <option value="20">20</option>
      <option value="24">24</option>
      <option value="32">32</option>
    </select>
    <span class="editor-sep"></span>
    ${button("bold", "B", "Bold", "<strong>B</strong>")}${button("italic", "I", "Italic", "<em>I</em>")}${button("underline", "U", "Underline", "<u>U</u>")}${button("strikeThrough", "S", "Strikethrough", "<s>S</s>")}
    <span class="editor-sep"></span>
    <button type="button" data-editor-block="p" title="Normal text">¶</button>
    <button type="button" data-editor-block="h1" title="Heading 1">H1</button>
    <button type="button" data-editor-block="h2" title="Heading 2">H2</button>
    <button type="button" data-editor-block="blockquote" title="Quote">❝</button>
    <button type="button" data-editor-block="pre" title="Code block">&lt;/&gt;</button>
    <span class="editor-sep"></span>
    ${button("insertUnorderedList", "•", "Bulleted list", "•")}${button("insertOrderedList", "1.", "Numbered list", "1.")}
    <span class="editor-sep"></span>
    ${button("createLink", "🔗", "Insert link", "🔗")}${button("unlink", "⛓", "Remove link", "⛓")}
    ${button("justifyLeft", "⇤", "Align left", "⇤")}${button("justifyCenter", "↔", "Align center", "↔")}${button("justifyRight", "⇥", "Align right", "⇥")}
    <span class="editor-sep"></span>
    <label class="editor-color-wrap" title="Text color"><span class="editor-color-glyph">A</span><input type="color" data-editor-color value="#172230" title="Text color" aria-label="Text color"></label>
    <label class="editor-color-wrap" title="Highlight color"><span class="editor-color-glyph editor-highlight-glyph"><svg class="icon" aria-hidden="true"><use href="#i-sparkles"/></svg></span><input type="color" data-editor-highlight value="#fff2a8" title="Highlight color" aria-label="Highlight color"></label>
    ${button("removeFormat", "Tx", "Clear formatting", "Tx")}
    ${session.hasQuote ? `<span class="editor-sep"></span><button type="button" data-compose-action="toggle-quote" class="compose-quote-toggle${session.quoteCollapsed ? " is-active" : ""}" title="${session.quoteCollapsed ? "Show quoted message" : "Hide quoted message"}" aria-pressed="${session.quoteCollapsed ? "true" : "false"}">❝ Quote</button>` : ""}
    <span class="compose-toolbar-mode">
      <button type="button" data-compose-action="mode" data-mode="html" class="${session.htmlMode ? "is-active" : ""}">HTML</button>
      <button type="button" data-compose-action="mode" data-mode="plain" class="${session.htmlMode ? "" : "is-active"}">Plain</button>
    </span>
    ${state.ai.enabled ? `<button type="button" class="compose-ai-btn" data-compose-action="ai" title="AI writing assistant" aria-label="AI writing assistant"><svg class="icon" aria-hidden="true" style="width:15px;height:15px"><use href="#i-sparkles"/></svg></button>` : ""}
  </div>`;
}

function composeTemplate(session) {
  const title = session.subject.trim() || "New message";
  const fromOptions = aliasesForSelect().map((alias) =>
    `<option value="${esc(alias.email)}"${normalizeEmail(session.fromEmail) === normalizeEmail(alias.email) ? " selected" : ""}>${esc(alias.label)} — ${esc(alias.email)}</option>`
  ).join("") || `<option value="${esc(session.fromEmail)}">${esc(session.fromEmail)}</option>`;
  const htmlMode = session.htmlMode !== false;
  const editorHTML = sanitizeEditorHTML(session.bodyHtml || textToEditorHTML(session.body));
  const modeSwitch = `<span class="compose-toolbar-mode"><button type="button" data-compose-action="mode" data-mode="html" class="${htmlMode ? "is-active" : ""}">HTML</button><button type="button" data-compose-action="mode" data-mode="plain" class="${htmlMode ? "" : "is-active"}">Plain</button></span>`;
  const aiButton = state.ai.enabled ? `<button type="button" class="compose-ai-btn" data-compose-action="ai" title="AI writing assistant" aria-label="AI writing assistant"><svg class="icon" aria-hidden="true" style="width:15px;height:15px"><use href="#i-sparkles"/></svg></button>` : "";
  const bodyField = htmlMode
    ? `<div class="compose-editor-wrap">${composeEditorToolbar(session)}<div class="compose-editor${session.quoteCollapsed ? " is-quote-collapsed" : ""}" data-compose-editor contenteditable="true" role="textbox" aria-multiline="true" data-placeholder="Write a message…">${editorHTML}</div></div>`
    : `<div class="compose-editor-wrap"><div class="compose-editor-toolbar"><span class="settings-note" style="margin-right:auto">Plain text</span>${modeSwitch}${aiButton}</div><textarea class="compose-text" data-compose-field="body" placeholder="Write a message…">${esc(session.body)}</textarea></div>`;
  return `<section class="compose${htmlMode ? " has-html-editor" : ""}" data-compose-id="${esc(session.id)}" role="dialog" aria-label="${esc(title)}">
    <div class="compose-head" data-compose-head>
      <span class="compose-grip" aria-hidden="true"></span>
      <h2 data-compose-title>${esc(title)}</h2>
      <span class="compose-identity" data-compose-identity title="Sending as ${esc(session.fromEmail)}">${esc(aliasDisplay(session.fromEmail))}</span>
      <div class="compose-head-actions">
        <button class="compose-head-btn" data-compose-action="minimize" type="button" aria-label="Minimize to dock" title="Minimize to dock"><svg class="icon" aria-hidden="true"><use href="#i-minimize"/></svg></button>
        <button class="compose-head-btn compose-expand" data-compose-action="expand" type="button" aria-label="Expand into reading pane" title="Expand into reading pane">
          <svg class="icon icon-expand" aria-hidden="true"><use href="#i-expand"/></svg><svg class="icon icon-collapse" aria-hidden="true"><use href="#i-collapse"/></svg>
        </button>
        <button class="compose-head-btn" data-compose-action="close" type="button" aria-label="Save and close draft" title="Save and close"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
      </div>
    </div>
    <div class="compose-body">
      <div class="compose-field recipient-field" data-compose-field-wrap="to">
        <label for="compose-${esc(session.id)}-to">To</label>
        <div class="recipient-control" data-recipient-control="to">
          <div class="recipient-chips" data-recipient-chips="to">${recipientChipsMarkup(session, "to")}</div>
          <input id="compose-${esc(session.id)}-to" data-compose-field="to" data-recipient-input="to" type="text" value="" placeholder="${esc(recipientInputPlaceholder(session, "to"))}" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false">
          <div class="recipient-suggest" data-recipient-suggest="to" role="listbox" hidden></div>
        </div>
        <button class="cc-toggle" data-compose-action="cc" type="button">Cc Bcc</button>
      </div>
      <div class="compose-field recipient-field" data-compose-cc data-compose-field-wrap="cc" ${session.ccOpen ? "" : "hidden"}>
        <label for="compose-${esc(session.id)}-cc">Cc</label>
        <div class="recipient-control" data-recipient-control="cc">
          <div class="recipient-chips" data-recipient-chips="cc">${recipientChipsMarkup(session, "cc")}</div>
          <input id="compose-${esc(session.id)}-cc" data-compose-field="cc" data-recipient-input="cc" type="text" value="" placeholder="${esc(recipientInputPlaceholder(session, "cc"))}" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false">
          <div class="recipient-suggest" data-recipient-suggest="cc" role="listbox" hidden></div>
        </div>
      </div>
      <div class="compose-field recipient-field" data-compose-cc data-compose-field-wrap="bcc" ${session.ccOpen ? "" : "hidden"}>
        <label for="compose-${esc(session.id)}-bcc">Bcc</label>
        <div class="recipient-control" data-recipient-control="bcc">
          <div class="recipient-chips" data-recipient-chips="bcc">${recipientChipsMarkup(session, "bcc")}</div>
          <input id="compose-${esc(session.id)}-bcc" data-compose-field="bcc" data-recipient-input="bcc" type="text" value="" placeholder="${esc(recipientInputPlaceholder(session, "bcc"))}" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false">
          <div class="recipient-suggest" data-recipient-suggest="bcc" role="listbox" hidden></div>
        </div>
      </div>
      <div class="compose-field">
        <label for="compose-${esc(session.id)}-subject">Subject</label>
        <input id="compose-${esc(session.id)}-subject" data-compose-field="subject" type="text" value="${esc(session.subject)}" placeholder="Add a subject">
      </div>
      <div class="compose-options">
        <button class="compose-options-toggle" data-compose-action="options" type="button" aria-expanded="${session.optionsOpen ? "true" : "false"}">
          <svg class="icon" aria-hidden="true"><use href="#i-sliders"/></svg>Options<span class="compose-opt-chips" data-compose-opt-chips></span>
          <svg class="icon opt-chevron" aria-hidden="true"><use href="#i-chevron-down"/></svg>
        </button>
        <div class="compose-options-panel" data-compose-options-panel ${session.optionsOpen ? "" : "hidden"}>
          <label class="compose-option"><span>From</span><select data-compose-field="fromEmail">${fromOptions}</select></label>
          <div class="compose-option compose-signature-action"><span>Signature</span><button class="compose-insert-signature" data-compose-action="insert-signature" type="button"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Insert signature</button></div>
          <div class="compose-option"><span>Priority</span><div class="compose-priority" role="radiogroup" aria-label="Priority">${composePriorityMarkup(session)}</div></div>
          <label class="compose-check"><input type="checkbox" data-compose-field="receipt" ${session.receipt ? "checked" : ""}>Return receipt</label>
          <label class="compose-check"><input type="checkbox" data-compose-field="delivery" ${session.delivery ? "checked" : ""}>Delivery status notification</label>
          <label class="compose-check"><input type="checkbox" data-compose-field="saveCopy" ${session.saveCopy ? "checked" : ""}>Save a copy in Sent</label>
        </div>
      </div>
      ${bodyField}
      <div class="compose-ai-panel" data-compose-ai-panel hidden></div>
      <div class="compose-files" data-compose-files></div>
      <div class="compose-error" data-compose-error hidden></div>
    </div>
    <div class="compose-foot">
      <button class="btn-primary" data-compose-action="send" type="button"><svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Send</button>
      <button class="btn-secondary compose-send-later" data-compose-action="schedule" type="button"><svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>Send later</button>
      <div class="compose-actions">
        <button class="icon-btn" data-compose-action="attach" type="button" aria-label="Attach files" title="Attach files"><svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg></button>
        <button class="icon-btn" data-compose-action="discard" type="button" aria-label="Discard draft" title="Discard draft"><svg class="icon" aria-hidden="true"><use href="#i-trash"/></svg></button>
      </div>
      <span class="compose-note" data-compose-status>${session.lastSavedAt ? "Draft saved on server" : "Draft not saved yet"}</span>
    </div>
    <div class="compose-resize" data-compose-resize title="Resize" aria-hidden="true"></div>
  </section>`;
}

function openCompose(prefill = {}, options = {}) {
  const data = prefill || {};
  if (!options.restoring && isMobile()) {
    composeSessions.forEach((session) => {
      if (session.mode !== "minimized" && session.id !== options.id) {
        session.mode = "minimized";
        if (session.el) session.el.hidden = true;
      }
    });
  }
  const id = options.id || `c${++composeCounter}_${uid("draft")}`;
  const draftId = options.draftId || uid("draft");
  const primary = state.aliases.find((alias) => alias.id === "primary" && alias.canSend) || aliasesForSelect()[0] || { email: state.user?.email || "" };
  const isNewMessage = !options.restoring && !data.inReplyTo && !data.kind;
  const defaultAlias = aliasForEmail(state.preferences.defaultFrom) || primary;
  const requestedFrom = data.fromEmail || (isNewMessage ? defaultAlias.email : primary.email);
  const requestedAlias = aliasForEmail(requestedFrom);
  const fromEmail = requestedAlias && requestedAlias.canSend ? requestedFrom : defaultAlias.email;
  const htmlMode = data.htmlMode != null ? !!data.htmlMode : state.preferences.composeMode !== "plain";
  const context = data.kind === "reply" ? "reply" : data.kind === "forward" ? "forward" : "new";
  const signatureItem = options.restoring ? null : defaultSignatureForCompose(context, fromEmail);
  const signatureText = signatureItem ? signaturePlainValue(signatureItem) : "";
  const signatureHTML = signatureItem ? signatureRichValue(signatureItem) : "";
  const rawBodyText = data.bodyText != null && data.bodyText !== "" ? data.bodyText : "";
  const rawBodyHTML = data.bodyHtml || "";
  let bodyText = rawBodyText;
  let bodyHtml = rawBodyHTML;
  if (signatureItem && (signatureText || signatureHTML)) {
    if (!rawBodyText && !rawBodyHTML) {
      bodyText = signatureText;
      bodyHtml = signatureHTML;
    } else {
      bodyText = [signatureText, rawBodyText].filter((value) => value && value.trim()).join("\n\n");
      bodyHtml = `${signatureHTML}${signatureHTML && (rawBodyHTML || rawBodyText) ? "<p><br></p>" : ""}${rawBodyHTML || textToEditorHTML(rawBodyText)}`;
    }
  } else if (!bodyHtml && bodyText) {
    bodyHtml = textToEditorHTML(bodyText);
  }
  const hasQuote = htmlMode && /class="compose-quote"/.test(bodyHtml);
  const quoteIsRich = hasQuote && (/<img/i.test(bodyHtml) || bodyHtml.length > 1400);
  const session = {
    id,
    draftId,
    to: data.toText != null ? data.toText : (data.to || ""),
    cc: data.ccText != null ? data.ccText : (data.cc || ""),
    bcc: data.bccText != null ? data.bccText : (data.bcc || ""),
    recipientInput: { to: "", cc: "", bcc: "" },
    recipientHighlight: -1,
    subject: data.subject || "",
    body: bodyText,
    bodyHtml,
    htmlMode,
    hasQuote,
    quoteCollapsed: quoteIsRich,
    fromEmail,
    autoSignature: signatureItem ? { id: signatureItem.id, text: signatureText, html: signatureHTML } : null,
    signatureContext: context,
    priority: data.priority || state.defaultPriority,
    receipt: data.receipt != null ? !!data.receipt : state.defaultReceipt === true,
    delivery: !!data.delivery,
    saveCopy: data.saveCopy != null ? !!data.saveCopy : state.preferences.saveCopyEnabled !== false,
    files: Array.isArray(data.attachments) ? data.attachments.map((file) => ({ ...file })) : [],
    threadId: data.threadId || null,
    inReplyTo: data.inReplyTo || null,
    ccOpen: Boolean(data.ccText || data.cc || data.bccText || data.bcc),
    optionsOpen: false,
    mode: data.windowMode === "minimized" ? "minimized" : (options.mode || "floating"),
    cascade: composeSessions.length,
    manual: false,
    left: null,
    top: null,
    width: null,
    height: null,
    el: null,
    dirty: false,
    lastSavedAt: data.updatedAt || null,
    saveTimer: null,
    savingPromise: null,
    pendingSave: false,
    saveError: null,
    error: null,
    ensureDraft: options.restoring ? null : null,
    restoring: !!options.restoring
  };
  composeSessions.push(session);
  composeLayer.insertAdjacentHTML("beforeend", composeTemplate(session));
  session.el = composeLayer.querySelector(`[data-compose-id="${CSS.escape(id)}"]`);
  updateComposeWindow(session);
  updateComposeFiles(session);
  updateComposeStatus(session);
  document.body.classList.add("composing");
  setActiveCompose(id);
  updateComposeDock();
  requestAnimationFrame(() => {
    layoutComposeSessions();
    if (!options.restoring) focusComposeField(session);
    else if (session.mode !== "minimized") focusComposeField(session);
  });
  if (!options.restoring) {
    scheduleComposeSave(session, 500);
  }
  return session;
}

function openComposeFromDraft(draft, options = {}) {
  if (!draft) return;
  const existing = composeSessions.find((session) => session.draftId === draft.id);
  if (existing) {
    if (existing.mode === "minimized") restoreComposeSession(existing.id);
    else { setActiveCompose(existing.id); focusComposeField(existing); }
    return;
  }
  const toText = draft.toText != null ? draft.toText : (draft.to || []).map((person) => `${person.name} <${person.email}>`).join(", ");
  const ccText = draft.ccText != null ? draft.ccText : (draft.cc || []).map((person) => person.email).join(", ");
  const bccText = draft.bccText != null ? draft.bccText : (draft.bcc || []).map((person) => person.email).join(", ");
  openCompose({
    toText,
    ccText,
    bccText,
    subject: draft.subject || "",
    bodyText: draft.bodyText || "",
    bodyHtml: draft.bodyHtml || "",
    htmlMode: draft.bodyHtml ? true : undefined,
    fromEmail: draft.fromEmail || (draft.from && draft.from.email) || state.user.email,
    priority: draft.priority,
    receipt: draft.receipt,
    delivery: draft.delivery,
    saveCopy: draft.saveCopy,
    attachments: draft.attachments,
    threadId: draft.threadId,
    inReplyTo: draft.inReplyTo,
    windowMode: draft.windowMode || "floating",
    updatedAt: draft.updatedAt
  }, { restoring: true, draftId: draft.id, id: `c_draft_${draft.id}`, mode: options.forceOpen ? "floating" : (draft.windowMode === "minimized" ? "minimized" : "floating") });
}

function restoreDraftSessions() {
  const drafts = state.messages.filter((message) => message.folder === "drafts" && message.windowMode !== "closed");
  const openIds = new Set(composeSessions.map((session) => session.draftId));
  let floating = composeSessions.filter((session) => session.mode !== "minimized").length;
  for (const draft of drafts.sort((a, b) => Date.parse(a.updatedAt || a.date) - Date.parse(b.updatedAt || b.date))) {
    if (openIds.has(draft.id)) continue;
    const session = openComposeFromDraftWithReturn(draft);
    if (session) {
      if (session.mode !== "minimized") floating += 1;
      if (floating > 1) minimizeComposeSession(session.id);
    }
  }
  updateComposeDock();
}

function openComposeFromDraftWithReturn(draft) {
  const before = composeSessions.length;
  openComposeFromDraft(draft);
  return composeSessions.length > before ? composeSessions[composeSessions.length - 1] : composeSessions.find((session) => session.draftId === draft.id) || null;
}

function updateComposeWindow(session) {
  if (!session.el) return;
  const el = session.el;
  el.hidden = session.mode === "minimized";
  el.classList.toggle("is-inline", session.mode === "inline");
  const title = el.querySelector("[data-compose-title]");
  if (title) title.textContent = session.subject.trim() || "New message";
  el.querySelectorAll("[data-compose-cc]").forEach((row) => { row.hidden = !session.ccOpen; });
  const expand = el.querySelector('[data-compose-action="expand"]');
  if (expand) {
    const label = session.mode === "inline" ? "Restore to floating window" : "Expand into reading pane";
    expand.setAttribute("aria-label", label);
    expand.setAttribute("title", label);
  }
  updateComposeOptions(session);
}

function updateComposeOptions(session) {
  if (!session.el) return;
  const toggle = session.el.querySelector('[data-compose-action="options"]');
  const panel = session.el.querySelector("[data-compose-options-panel]");
  if (toggle) toggle.setAttribute("aria-expanded", session.optionsOpen ? "true" : "false");
  if (panel) panel.hidden = !session.optionsOpen;
  const chips = [];
  if (session.receipt) chips.push("Return receipt");
  if (session.delivery) chips.push("Delivery report");
  if (session.priority === "high") chips.push("High priority");
  else if (session.priority === "low") chips.push("Low priority");
  const alias = aliasForEmail(session.fromEmail);
  if (alias && alias.kind !== "primary") chips.push(alias.label);
  const holder = session.el.querySelector("[data-compose-opt-chips]");
  if (holder) holder.innerHTML = chips.map((chip) => `<span>${esc(chip)}</span>`).join("");
  updateComposeIdentity(session);
}

function updateComposeIdentity(session) {
  if (!session?.el) return;
  const identity = session.el.querySelector("[data-compose-identity]");
  if (identity) {
    identity.textContent = aliasDisplay(session.fromEmail);
    identity.title = `Sending as ${session.fromEmail}`;
  }
}

function composeFileCard(file, index) {
  return `<div class="compose-file-card">
    <span class="file-art"><svg class="icon" aria-hidden="true"><use href="#i-file"/></svg></span>
    <span class="file-copy"><strong>${esc(file.name || "Attachment")}</strong><span>${esc(file.mime || file.type || "File")} · ${esc(file.size || formatBytes(file.sizeBytes))}</span></span>
    ${index == null ? "" : `<button class="file-remove" data-compose-remove="${index}" type="button" aria-label="Remove ${esc(file.name || "attachment")}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>`}
  </div>`;
}

function updateComposeFiles(session) {
  if (!session.el) return;
  const box = session.el.querySelector("[data-compose-files]");
  if (!box) return;
  box.className = session.files.length ? "compose-file-grid" : "compose-files";
  box.innerHTML = session.files.map((file, index) => composeFileCard(file, index)).join("");
}

function toggleComposeQuote(session) {
  if (!session?.el || !session.hasQuote) return;
  session.quoteCollapsed = !session.quoteCollapsed;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (editor) editor.classList.toggle("is-quote-collapsed", session.quoteCollapsed);
  const button = session.el.querySelector('[data-compose-action="toggle-quote"]');
  if (button) {
    button.classList.toggle("is-active", session.quoteCollapsed);
    button.setAttribute("aria-pressed", session.quoteCollapsed ? "true" : "false");
    button.title = session.quoteCollapsed ? "Show quoted message" : "Hide quoted message";
  }
}

function reRenderCompose(session) {
  if (!session?.el) return;
  const wrapper = document.createElement("div");
  wrapper.innerHTML = composeTemplate(session);
  const next = wrapper.firstElementChild;
  if (!next) return;
  session.el.replaceWith(next);
  session.el = next;
  if (session.quoteCollapsed) next.querySelector("[data-compose-editor]")?.classList.add("is-quote-collapsed");
  updateComposeWindow(session);
  updateComposeFiles(session);
  updateComposeStatus(session);
  updateComposeOptions(session);
  for (const field of RECIPIENT_FIELDS) updateRecipientField(session, field);
  layoutComposeSessions();
  const aiPanel = session.el.querySelector("[data-compose-ai-panel]");
  if (aiPanel) aiPanel.hidden = !session.aiOpen;
  if (session.aiOpen) renderComposeAiPanel(session);
}

function setComposeMode(session, mode) {
  if (!session?.el) return;
  const htmlMode = mode !== "plain";
  if (session.htmlMode === htmlMode) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  const textarea = session.el.querySelector('[data-compose-field="body"]');
  if (editor) {
    session.bodyHtml = sanitizeEditorHTML(editor.innerHTML);
    session.body = editorPlainText(editor.innerHTML);
  } else if (textarea) {
    session.body = textarea.value;
  }
  session.htmlMode = htmlMode;
  session.bodyHtml = textToEditorHTML(session.body);
  session.dirty = true;
  reRenderCompose(session);
  scheduleComposeSave(session);
}

function restoreComposeEditorSelection(session, editor) {
  if (!session || !editor) return;
  const selection = window.getSelection();
  const selectionInside = Boolean(selection && selection.rangeCount && editor.contains(selection.anchorNode));
  editor.focus();
  if (selectionInside || !selection) return;
  const range = session.editorRange;
  if (!range || !editor.contains(range.commonAncestorContainer)) return;
  selection.removeAllRanges();
  selection.addRange(range);
}

function rememberComposeEditorSelection(session, editor) {
  if (!session || !editor) return;
  const selection = window.getSelection();
  if (!selection || !selection.rangeCount) return;
  if (!editor.contains(selection.anchorNode)) return;
  session.editorRange = selection.getRangeAt(0).cloneRange();
}

function normalizeEditorFontTags(editor, explicitSize = "") {
  if (!editor) return;
  for (const font of Array.from(editor.querySelectorAll("font"))) {
    const span = document.createElement("span");
    const face = font.getAttribute("face");
    const size = font.getAttribute("size");
    if (face) span.style.fontFamily = face;
    if (explicitSize) span.style.fontSize = `${explicitSize}px`;
    else if (size) {
      const legacy = { 1: "10px", 2: "13px", 3: "16px", 4: "18px", 5: "24px", 6: "32px", 7: "48px" };
      if (legacy[size]) span.style.fontSize = legacy[size];
    }
    while (font.firstChild) span.appendChild(font.firstChild);
    font.replaceWith(span);
  }
}

function applyComposeFontFamily(session, family) {
  if (!session?.el || !family) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (!editor) return;
  restoreComposeEditorSelection(session, editor);
  try { document.execCommand("styleWithCSS", false, true); } catch {}
  document.execCommand("fontName", false, family);
  normalizeEditorFontTags(editor);
  updateComposeEditor(session, editor);
  rememberComposeEditorSelection(session, editor);
}

function applyComposeFontSize(session, pixels) {
  if (!session?.el || !pixels) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (!editor) return;
  restoreComposeEditorSelection(session, editor);
  try { document.execCommand("styleWithCSS", false, false); } catch {}
  document.execCommand("fontSize", false, "7");
  normalizeEditorFontTags(editor, pixels);
  try { document.execCommand("styleWithCSS", false, true); } catch {}
  updateComposeEditor(session, editor);
  rememberComposeEditorSelection(session, editor);
}

function applyComposeHighlight(session, color) {
  if (!session?.el || !color) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (!editor) return;
  restoreComposeEditorSelection(session, editor);
  try { document.execCommand("styleWithCSS", false, true); } catch {}
  document.execCommand("hiliteColor", false, color);
  updateComposeEditor(session, editor);
  rememberComposeEditorSelection(session, editor);
}

function applyComposeEditorCommand(session, command, value = null, options = {}) {
  if (!session?.el) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (!editor) return;
  restoreComposeEditorSelection(session, editor);
  try { document.execCommand("styleWithCSS", false, true); } catch {}
  if (command === "createLink") {
    const href = window.prompt("Link URL", "https://");
    if (!href || !/^(https?:\/\/|mailto:)/i.test(href.trim())) {
      showToast("Links must start with https://, http:// or mailto:");
      return;
    }
    document.execCommand("createLink", false, href.trim());
  } else if (command === "formatBlock") {
    document.execCommand("formatBlock", false, value || "p");
  } else if (command === "removeFormat") {
    document.execCommand("removeFormat", false, null);
  } else {
    document.execCommand(command, false, value);
  }
  normalizeEditorFontTags(editor);
  updateComposeEditor(session, editor);
  if (!options.keepFocus) {
    editor.focus();
    rememberComposeEditorSelection(session, editor);
  }
}

function composeAiLanguages() {
  return state.ai?.languages?.length ? state.ai.languages : [
    { code: "en-US", name: "English (United States)" }, { code: "en-GB", name: "English (United Kingdom)" },
    { code: "es-ES", name: "Spanish" }, { code: "fr-FR", name: "French" }, { code: "de-DE", name: "German" },
    { code: "it-IT", name: "Italian" }, { code: "pt-BR", name: "Portuguese (Brazil)" }, { code: "nl-NL", name: "Dutch" },
    { code: "pl-PL", name: "Polish" }, { code: "ru-RU", name: "Russian" }, { code: "uk-UA", name: "Ukrainian" },
    { code: "tr-TR", name: "Turkish" }, { code: "ar-SA", name: "Arabic" }, { code: "he-IL", name: "Hebrew" },
    { code: "fa-IR", name: "Persian" }, { code: "hi-IN", name: "Hindi" }, { code: "bn-IN", name: "Bengali" },
    { code: "ur-PK", name: "Urdu" }, { code: "zh-CN", name: "Chinese (Simplified)" }, { code: "zh-TW", name: "Chinese (Traditional)" },
    { code: "ja-JP", name: "Japanese" }, { code: "ko-KR", name: "Korean" }, { code: "vi-VN", name: "Vietnamese" },
    { code: "th-TH", name: "Thai" }, { code: "id-ID", name: "Indonesian" }, { code: "ms-MY", name: "Malay" },
    { code: "fil-PH", name: "Filipino" }, { code: "sv-SE", name: "Swedish" }, { code: "no-NO", name: "Norwegian" },
    { code: "da-DK", name: "Danish" }, { code: "fi-FI", name: "Finnish" }, { code: "cs-CZ", name: "Czech" },
    { code: "sk-SK", name: "Slovak" }, { code: "hu-HU", name: "Hungarian" }, { code: "ro-RO", name: "Romanian" }, { code: "el-GR", name: "Greek" }
  ];
}

function composeAiStyles() {
  return state.ai?.styles?.length ? state.ai.styles : [
    { code: "assertive", name: "Assertive" }, { code: "casual", name: "Casual" }, { code: "enthusiastic", name: "Enthusiastic" },
    { code: "funny", name: "Funny" }, { code: "informational", name: "Informational" }, { code: "professional", name: "Professional" },
    { code: "urgent", name: "Urgent" }, { code: "witty", name: "Witty" }
  ];
}

function toggleComposeAi(session) {
  if (!session?.el) return;
  session.aiOpen = !session.aiOpen;
  const panel = session.el.querySelector("[data-compose-ai-panel]");
  if (!panel) return;
  panel.hidden = !session.aiOpen;
  if (session.aiOpen) renderComposeAiPanel(session);
}

function renderComposeAiPanel(session) {
  const panel = session?.el?.querySelector("[data-compose-ai-panel]");
  if (!panel || !state.ai.enabled) return;
  const languages = composeAiLanguages();
  const styles = composeAiStyles();
  const preferredLanguage = languages.some((item) => item.code === state.preferences.language) ? state.preferences.language : "en-US";
  panel.innerHTML = `<div class="compose-ai-head"><svg class="icon" aria-hidden="true"><use href="#i-sparkles"/></svg><strong>AI writing assistant</strong><button class="icon-btn" data-ai-compose-close type="button" aria-label="Close AI assistant"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
    <div class="compose-ai-grid">
      <label>Style<select data-ai-style>${styles.map((item) => `<option value="${esc(item.code)}" ${item.code === "professional" ? "selected" : ""}>${esc(item.name)}</option>`).join("")}</select></label>
      <label>Length<select data-ai-length><option value="short">Short</option><option value="medium" selected>Medium</option><option value="long">Long</option></select></label>
      <label>Language<select data-ai-language>${languages.map((item) => `<option value="${esc(item.code)}" ${item.code === preferredLanguage ? "selected" : ""}>${esc(item.name)}</option>`).join("")}</select></label>
      <label>Creativity<div class="compose-ai-range"><input type="range" min="0" max="100" value="50" data-ai-creativity><span data-ai-creativity-value>50</span></div></label>
      <label class="compose-ai-prompt">Instruction <small class="muted">optional</small><textarea data-ai-prompt placeholder="e.g. Follow up on the launch timeline and ask for a decision by Friday"></textarea></label>
    </div>
    <div class="compose-ai-actions"><button class="btn-primary small" data-ai-compose-generate type="button"><svg class="icon" aria-hidden="true"><use href="#i-sparkles"/></svg>Generate draft</button><span class="settings-note" data-ai-status>${session.aiResult ? "Draft ready" : "Uses this message's current text as context."}</span></div>
    <div class="compose-ai-result" data-ai-result ${session.aiResult ? "" : "hidden"}>${esc(session.aiResult || "")}</div>
    <div class="compose-ai-actions" data-ai-result-actions ${session.aiResult ? "" : "hidden"}><button class="btn-secondary small" data-ai-compose-insert type="button">Insert below</button><button class="btn-secondary small" data-ai-compose-replace type="button">Replace body</button></div>`;
  const range = panel.querySelector("[data-ai-creativity]");
  range?.addEventListener("input", () => {
    const value = panel.querySelector("[data-ai-creativity-value]");
    if (value) value.textContent = range.value;
  });
}

async function composeAiGenerate(session) {
  const panel = session?.el?.querySelector("[data-compose-ai-panel]");
  if (!panel) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  const textarea = session.el.querySelector('[data-compose-field="body"]');
  const text = editor ? editorPlainText(editor.innerHTML) : (textarea?.value || session.body || "");
  const status = panel.querySelector("[data-ai-status]");
  const button = panel.querySelector("[data-ai-compose-generate]");
  if (button) button.disabled = true;
  if (status) status.textContent = "Generating…";
  try {
    const data = await api("/api/ai/compose", {
      method: "POST",
      body: {
        text,
        prompt: panel.querySelector("[data-ai-prompt]")?.value || "",
        style: panel.querySelector("[data-ai-style]")?.value || "professional",
        length: panel.querySelector("[data-ai-length]")?.value || "medium",
        creativity: Number(panel.querySelector("[data-ai-creativity]")?.value || 50) / 100,
        language: panel.querySelector("[data-ai-language]")?.value || "en-US"
      }
    });
    session.aiResult = data.text || "";
    if (status) status.textContent = `${data.style || "professional"} · ${data.length || "medium"} · ${data.language || "en-US"}`;
    const result = panel.querySelector("[data-ai-result]");
    if (result) { result.hidden = false; result.textContent = session.aiResult; }
    const actions = panel.querySelector("[data-ai-result-actions]");
    if (actions) actions.hidden = false;
  } catch (error) {
    if (status) status.textContent = error.message || "AI generation failed";
    showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

function applyComposeAiResult(session, replace) {
  if (!session?.aiResult) return;
  const html = textToEditorHTML(session.aiResult);
  if (session.htmlMode) {
    const editor = session.el.querySelector("[data-compose-editor]");
    if (!editor) return;
    if (replace) editor.innerHTML = html;
    else {
      editor.focus();
      const selection = window.getSelection();
      if (selection && selection.rangeCount && editor.contains(selection.anchorNode)) document.execCommand("insertHTML", false, html);
      else editor.insertAdjacentHTML("beforeend", html);
    }
    updateComposeEditor(session, editor);
  } else {
    const textarea = session.el.querySelector('[data-compose-field="body"]');
    if (!textarea) return;
    textarea.value = replace ? session.aiResult : `${textarea.value}${textarea.value ? "\n\n" : ""}${session.aiResult}`;
    session.body = textarea.value;
    session.bodyHtml = textToEditorHTML(textarea.value);
    session.dirty = true;
    scheduleComposeSave(session);
  }
  showToast(replace ? "Draft replaced with AI text" : "AI draft inserted");
}

/* ---------- compose attachment picker ---------- */
function openComposeAttach(sessionId) {
  const session = sessionById(sessionId);
  if (!session) return;
  attachPickerSessionId = sessionId;
  state.composeAttachProvider = storageDefaultId();
  composeAttachOverlay.hidden = false;
  const limit = document.getElementById("composeAttachLimit");
  if (limit) limit.textContent = formatBytes(state.attachmentMaxBytes || 8 * 1024 * 1024);
  renderComposeAttachPicker();
}

function closeComposeAttach() {
  composeAttachOverlay.hidden = true;
  attachPickerSessionId = null;
}

function refreshAttachPickerIfOpen() {
  if (!composeAttachOverlay.hidden) renderComposeAttachPicker();
}

function renderComposeAttachPicker() {
  const session = sessionById(attachPickerSessionId);
  if (!session) { closeComposeAttach(); return; }
  const current = composeAttachCurrentList;
  if (current) {
    current.innerHTML = session.files.length ? session.files.map((file, index) => composeFileCard(file, index)).join("") : '<div class="settings-note">No attachments yet.</div>';
  }
  if (composeAttachCurrentCount) composeAttachCurrentCount.textContent = `${session.files.length} file${session.files.length === 1 ? "" : "s"}`;
  if (!composeAttachFileList) return;
  const files = (state.files || []).filter((file) => !file.folder || file.kind !== "folder").slice().sort((a, b) => Date.parse(b.modifiedAt || b.updatedAt || b.date || 0) - Date.parse(a.modifiedAt || a.updatedAt || a.date || 0));
  if (composeAttachFileCount) composeAttachFileCount.textContent = `${files.length} file${files.length === 1 ? "" : "s"} available`;
  composeAttachFileList.innerHTML = files.length ? files.map((file) => `<button class="compose-attach-pick" type="button" data-attach-file="${esc(file.id)}">
    <span class="file-art"><svg class="icon" aria-hidden="true"><use href="#i-file"/></svg></span>
    <span class="file-copy"><strong>${esc(file.name || "File")}</strong><span>${esc(file.providerLabel || file.providerId || file.provider || "Files")} · ${esc(formatBytes(file.sizeBytes || 0))}</span></span>
    <span class="file-add">Attach</span>
  </button>`).join("") : '<div class="settings-note">No files in your workspace yet. Upload one from the Files tab or browse this device.</div>';
}

async function uploadComposeFiles(session, files) {
  if (!session || !files?.length) return;
  const max = state.attachmentMaxBytes || 8 * 1024 * 1024;
  for (const file of files) {
    if (file.size > max) {
      showToast(`${file.name} is larger than ${formatBytes(max)}`);
      continue;
    }
    if (session.files.some((item) => item.name === file.name && item.sizeBytes === file.size)) {
      showToast(`${file.name} is already attached`);
      continue;
    }
    try {
      const status = session.el?.querySelector("[data-compose-status]");
      if (status) status.textContent = `Uploading ${file.name}…`;
      const dataBase64 = await fileToBase64(file);
      const data = await api("/api/attachments", {
        method: "POST",
        body: { name: file.name, mime: file.type || "application/octet-stream", dataBase64 }
      });
      session.files.push(data.attachment);
      updateComposeFiles(session);
      scheduleComposeSave(session, 200);
      refreshAttachPickerIfOpen();
    } catch (error) {
      showError(error);
    }
  }
  updateComposeStatus(session);
  renderComposeDockTabs();
}

async function attachWorkspaceFile(fileId) {
  const session = sessionById(attachPickerSessionId);
  const file = (state.files || []).find((item) => item.id === fileId);
  if (!session || !file) return;
  const max = state.attachmentMaxBytes || 8 * 1024 * 1024;
  if (Number(file.sizeBytes || 0) > max) {
    showToast(`${file.name} is larger than ${formatBytes(max)}`);
    return;
  }
  try {
    const response = await fetch(`/api/files/${encodeURIComponent(fileId)}/content`, { credentials: "same-origin" });
    if (!response.ok) throw new Error("That file could not be downloaded");
    const blob = await response.blob();
    const upload = new File([blob], file.name || "attachment", { type: file.mime || blob.type || "application/octet-stream" });
    await uploadComposeFiles(session, [upload]);
  } catch (error) { showError(error); }
}

function updateComposeStatus(session) {
  if (!session.el) return;
  const status = session.el.querySelector("[data-compose-status]");
  const error = session.el.querySelector("[data-compose-error]");
  if (status) {
    status.textContent = session.aiReplyPending
      ? "AI is drafting a reply from the conversation…"
      : session.saveError || (session.dirty ? "Saving…" : session.lastSavedAt ? "Draft saved on server" : "Draft not saved yet");
    status.classList.toggle("is-error", Boolean(session.saveError));
  }
  if (error) {
    error.hidden = !session.error;
    error.textContent = session.error || "";
  }
}

function setActiveCompose(id) {
  activeComposeId = id;
  const session = sessionById(id);
  if (session && session.el && session.mode !== "minimized") session.el.style.zIndex = String(++composeZ + 1);
  renderComposeDockTabs();
}

function focusComposeField(session, field) {
  if (!session || !session.el || session.mode === "minimized") return;
  const selector = field
    ? `[data-compose-field="${field}"]`
    : (session.to ? '[data-compose-field="body"]' : '[data-compose-field="to"]');
  const target = session.el.querySelector(selector);
  if (target) target.focus();
}

function updateComposeDock() {
  const visible = composeSessions.length > 1 || composeSessions.some((session) => session.mode === "minimized");
  const changed = composeDock.hidden !== !visible;
  composeDock.hidden = !visible;
  document.documentElement.style.setProperty("--compose-dock-h", visible ? "42px" : "0px");
  renderComposeDockTabs();
  document.body.classList.toggle("composing", composeSessions.length > 0);
  document.body.classList.toggle("compose-visible", composeSessions.some((session) => session.mode !== "minimized"));
  if (changed) requestAnimationFrame(layoutComposeSessions);
}

function renderComposeDockTabs() {
  if (composeDock.hidden) {
    composeDockTabs.innerHTML = "";
    return;
  }
  composeDockTabs.innerHTML = composeSessions.map((session) => {
    const title = session.subject.trim() || "New message";
    const recipient = session.to.trim() || "No recipient";
    const minimized = session.mode === "minimized";
    const flash = minimized && minimizedComposeFlashId === session.id;
    return `<div class="compose-tab${session.id === activeComposeId ? " is-active" : ""}${minimized ? " is-minimized" : ""}${flash ? " is-just-minimized" : ""}" data-compose-tab="${esc(session.id)}" role="button" tabindex="0" title="${minimized ? "Minimized draft — click to reopen" : "Open draft"}">
      <span class="compose-tab-icon"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg></span>
      <span class="compose-tab-text"><strong>${esc(title)}</strong><span>${esc(minimized ? `Minimized · ${recipient}` : recipient)}</span></span>
      <button class="compose-tab-close" data-compose-tab-close="${esc(session.id)}" type="button" aria-label="Close draft"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
    </div>`;
  }).join("");
}

function layoutComposeSessions() {
  if (isMobile()) {
    composeSessions.forEach((session) => { if (session.el) session.el.hidden = session.mode === "minimized"; });
    return;
  }
  const dockHeight = composeDock.hidden ? 0 : (composeDock.offsetHeight || 42);
  composeSessions.forEach((session) => {
    if (!session.el || session.mode === "minimized") return;
    session.el.hidden = false;
    if (session.mode === "inline") {
      applyInlineLayout(session);
      return;
    }
    session.el.classList.remove("is-inline");
    const sizePreference = state.preferences.composeWidth || "wide";
    const baseWidth = sizePreference === "full"
      ? Math.max(380, window.innerWidth - 56)
      : sizePreference === "wide"
        ? Math.min(1160, Math.max(620, window.innerWidth - 72))
        : Math.min(580, Math.max(380, window.innerWidth - 56));
    let width = session.manual && session.width ? session.width : baseWidth;
    let height = session.manual && session.height ? session.height : Math.min(sizePreference === "wide" ? 620 : 540, Math.max(300, window.innerHeight - 120));
    let left;
    let top;
    if (session.manual && session.left != null) {
      left = session.left;
      top = session.top;
    } else {
      const offset = (session.cascade % 6) * 16;
      const position = state.preferences.composePosition || "center";
      if (position === "center") {
        left = (window.innerWidth - width) / 2 + offset;
        top = (window.innerHeight - height) / 2 - offset;
      } else if (position === "bottom-left") {
        left = 28 + offset;
        top = window.innerHeight - (58 + dockHeight) - height - offset;
      } else {
        left = window.innerWidth - 28 - width - offset;
        top = window.innerHeight - (58 + dockHeight) - height - offset;
      }
      session.left = left;
      session.top = top;
      session.width = width;
      session.height = height;
    }
    const maxLeft = window.innerWidth - width - 12;
    const maxTop = window.innerHeight - height - 12;
    left = Math.max(12, Math.min(left, maxLeft));
    top = Math.max(12, Math.min(top, maxTop));
    session.left = left;
    session.top = top;
    session.el.style.left = left + "px";
    session.el.style.top = top + "px";
    session.el.style.right = "auto";
    session.el.style.bottom = "auto";
    session.el.style.width = width + "px";
    session.el.style.height = height + "px";
  });
}

function applyInlineLayout(session) {
  if (!session.el) return;
  if (state.task !== "mail") {
    session.mode = "minimized";
    session.el.hidden = true;
    return;
  }
  const rect = reader.getBoundingClientRect();
  if (rect.width < 60 || rect.height < 80) {
    session.mode = "minimized";
    session.el.hidden = true;
    return;
  }
  session.el.style.left = rect.left + "px";
  session.el.style.top = rect.top + "px";
  session.el.style.right = "auto";
  session.el.style.bottom = "auto";
  session.el.style.width = rect.width + "px";
  session.el.style.height = rect.height + "px";
}

function flashMinimizedComposeTab(id) {
  minimizedComposeFlashId = id;
  clearTimeout(minimizedComposeFlashTimer);
  renderComposeDockTabs();
  requestAnimationFrame(() => {
    composeDockTabs.querySelector(`[data-compose-tab="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
  minimizedComposeFlashTimer = setTimeout(() => {
    if (minimizedComposeFlashId !== id) return;
    minimizedComposeFlashId = null;
    renderComposeDockTabs();
  }, 2400);
}

function minimizeComposeSession(id) {
  const session = sessionById(id);
  if (!session) return;
  // The window mode is part of the persisted draft. Set it before the flush
  // so a reload or a sign-in on another computer restores the dock state.
  session.mode = "minimized";
  if (session.el) { session.el.hidden = true; session.el.classList.remove("is-inline"); }
  if (activeComposeId === id) {
    const next = composeSessions.find((item) => item.id !== id && item.mode !== "minimized");
    activeComposeId = next ? next.id : null;
  }
  updateComposeDock();
  flashMinimizedComposeTab(id);
  persistComposeSession(session);
  showToast("Draft minimized — open it from the blue dock tab below");
}

async function persistComposeSession(session) {
  if (!session) return null;
  try {
    await flushDraft(session, { silent: true });
    // A save already in flight captured the old mode; run the queued save too
    // so windowMode is guaranteed to reflect the latest UI state.
    if (session.pendingSave || session.dirty) await flushDraft(session, { silent: true });
    return session;
  } catch {
    return null;
  }
}

async function persistComposeSessions() {
  const sessions = composeSessions.slice();
  await Promise.allSettled(sessions.map((session) => persistComposeSession(session)));
}

function flushComposeSessionsKeepalive() {
  if (!state.user || !state.csrf) return;
  for (const session of composeSessions.slice()) {
    if (!session || session.mode === "closed") continue;
    const payload = JSON.stringify(composePayload(session));
    try {
      fetch(`/api/drafts/${encodeURIComponent(session.draftId)}`, {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": state.csrf,
          Accept: "application/json"
        },
        body: payload,
        credentials: "same-origin",
        keepalive: true
      }).catch(() => {});
    } catch {
      // The page is unloading; the best-effort request is enough.
    }
  }
}

function restoreComposeSession(id) {
  const session = sessionById(id);
  if (!session) return;
  if (minimizedComposeFlashId === id) {
    minimizedComposeFlashId = null;
    clearTimeout(minimizedComposeFlashTimer);
  }
  if (isMobile()) {
    composeSessions.forEach((item) => {
      if (item !== session && item.mode !== "minimized") {
        item.mode = "minimized";
        if (item.el) item.el.hidden = true;
      }
    });
  }
  session.mode = "floating";
  if (session.el) { session.el.hidden = false; session.el.classList.remove("is-inline"); }
  setActiveCompose(id);
  updateComposeDock();
  persistComposeSession(session);
  requestAnimationFrame(() => { layoutComposeSessions(); focusComposeField(session); });
}

function expandComposeSession(id) {
  const session = sessionById(id);
  if (!session || isMobile() || session.mode === "minimized") return;
  if (session.mode === "inline") {
    session.mode = "floating";
    updateComposeWindow(session);
    setActiveCompose(id);
    updateComposeDock();
    requestAnimationFrame(() => { layoutComposeSessions(); focusComposeField(session); });
    return;
  }
  if (state.task !== "mail") setTask("mail");
  composeSessions.forEach((item) => {
    if (item !== session && item.mode !== "minimized") {
      item.mode = "minimized";
      if (item.el) item.el.hidden = true;
    }
  });
  session.mode = "inline";
  updateComposeWindow(session);
  setActiveCompose(id);
  updateComposeDock();
  requestAnimationFrame(() => { layoutComposeSessions(); focusComposeField(session); });
}

function removeComposeSessionUi(id) {
  const session = sessionById(id);
  if (session && session.el) session.el.remove();
  if (attachPickerSessionId === id) closeComposeAttach();
  composeSessions = composeSessions.filter((item) => item.id !== id);
  if (activeComposeId === id) {
    const next = composeSessions.find((item) => item.mode !== "minimized") || composeSessions[0];
    activeComposeId = next ? next.id : null;
  }
  if (!composeSessions.length) document.body.classList.remove("composing");
  updateComposeDock();
}

async function closeComposeSession(id, { discard = false } = {}) {
  const session = sessionById(id);
  if (!session) return;
  clearTimeout(session.saveTimer);
  if (discard) {
    try { await api(`/api/drafts/${encodeURIComponent(session.draftId)}`, { method: "DELETE" }); } catch {}
    state.messages = state.messages.filter((message) => message.id !== session.draftId);
    removeComposeSessionUi(id);
    rebuildIndex();
    renderFolders();
    renderList();
    showToast("Draft discarded");
    return;
  }
  const empty = isComposeEmpty(session);
  if (empty) {
    try { await api(`/api/drafts/${encodeURIComponent(session.draftId)}`, { method: "DELETE" }); } catch {}
    state.messages = state.messages.filter((message) => message.id !== session.draftId);
    removeComposeSessionUi(id);
    rebuildIndex();
    renderFolders();
    renderList();
    return;
  }
  const previousMode = session.mode;
  session.mode = "closed";
  try {
    await flushDraft(session);
  } catch {
    session.mode = previousMode;
    return;
  }
  removeComposeSessionUi(id);
  showToast("Draft saved on the server");
}

function composePayload(session) {
  flushRecipientInputs(session);
  return {
    toText: recipientTextWithPending(session, "to"),
    ccText: recipientTextWithPending(session, "cc"),
    bccText: recipientTextWithPending(session, "bcc"),
    subject: session.subject,
    bodyText: session.body,
    bodyHtml: session.htmlMode ? session.bodyHtml : "",
    fromEmail: session.fromEmail,
    attachments: session.files,
    threadId: session.threadId,
    inReplyTo: session.inReplyTo,
    windowMode: session.mode,
    priority: session.priority,
    receipt: session.receipt,
    delivery: session.delivery,
    saveCopy: session.saveCopy
  };
}

function scheduleComposeSave(session, delay = 750) {
  clearTimeout(session.saveTimer);
  session.saveTimer = setTimeout(() => {
    flushDraft(session, { silent: true }).catch(() => {});
  }, delay);
}

function flushDraft(session, { silent = false } = {}) {
  if (!session) return Promise.resolve(null);
  clearTimeout(session.saveTimer);
  if (session.savingPromise) {
    session.pendingSave = true;
    return session.savingPromise;
  }
  session.dirty = true;
  session.saveError = null;
  updateComposeStatus(session);
  session.savingPromise = (async () => {
    try {
      const data = await api(`/api/drafts/${encodeURIComponent(session.draftId)}`, {
        method: "PUT",
        body: composePayload(session)
      });
      const saved = data.draft;
      session.lastSavedAt = data.savedAt;
      session.dirty = false;
      session.saveError = null;
      upsertDraftMessage(saved);
      rebuildIndex();
      if (state.task === "mail") { renderFolders(); renderList(); }
      updateComposeStatus(session);
      return saved;
    } catch (error) {
      session.saveError = error.message || "Draft could not be saved";
      session.dirty = true;
      updateComposeStatus(session);
      if (!silent) throw error;
      return null;
    } finally {
      session.savingPromise = null;
      if (session.pendingSave) {
        session.pendingSave = false;
        scheduleComposeSave(session, 350);
      }
    }
  })();
  return session.savingPromise;
}

function upsertDraftMessage(draft) {
  if (!draft) return;
  const existing = state.messages.find((message) => message.id === draft.id);
  if (existing) Object.assign(existing, draft);
  else state.messages.push(draft);
}

/* ============================================================
   Alt8Mail Intelligence · pre-send spell check
   ============================================================ */
const SPELLCHECK_PRODUCT_WORDS = new Set(["alt8mail", "a8m", "lynxkeep", "webmail", "dovecot", "postfix", "rspamd", "clamav"]);

function spellcheckIgnoredWords() {
  try {
    const parsed = JSON.parse(localStorage.getItem("a8m_spell_ignored") || "[]");
    return new Set(Array.isArray(parsed) ? parsed.map((word) => String(word).toLowerCase()) : []);
  } catch {
    return new Set();
  }
}

function addSpellIgnoredWord(word) {
  const key = String(word || "").toLowerCase();
  if (!key) return;
  const ignored = spellcheckIgnoredWords();
  ignored.add(key);
  try { localStorage.setItem("a8m_spell_ignored", JSON.stringify(Array.from(ignored).slice(-500))); } catch {}
}

function spellSuggestionCase(source, suggestion) {
  if (!suggestion) return suggestion;
  if (source && source === source.toUpperCase()) return suggestion.toUpperCase();
  if (source && /^[A-Z]/.test(source)) return suggestion.charAt(0).toUpperCase() + suggestion.slice(1);
  return suggestion;
}

async function loadSpellChecker() {
  if (state.spellChecker) return state.spellChecker;
  if (typeof window.Typo !== "function") throw new Error("Spell-check dictionary is unavailable");
  const [aff, dic] = await Promise.all([
    fetch("/vendor/en_US.aff", { credentials: "same-origin", cache: "force-cache" }).then((response) => {
      if (!response.ok) throw new Error("Spell-check dictionary could not be loaded");
      return response.text();
    }),
    fetch("/vendor/en_US.dic", { credentials: "same-origin", cache: "force-cache" }).then((response) => {
      if (!response.ok) throw new Error("Spell-check dictionary could not be loaded");
      return response.text();
    })
  ]);
  state.spellChecker = new window.Typo("en_US", aff, dic);
  return state.spellChecker;
}

function collectSpellIssues(checker, text) {
  const cleaned = String(text || "")
    .replace(/https?:\/\/\S+|www\.\S+|[\w.+-]+@[\w.-]+\.[a-z]{2,}|\b[\w-]+\.(?:png|jpe?g|gif|pdf|docx?|xlsx?|pptx?|zip|txt)\b/gi, " ");
  const ignored = spellcheckIgnoredWords();
  const seen = new Map();
  const words = [];
  if (typeof Intl !== "undefined" && Intl.Segmenter) {
    const segmenter = new Intl.Segmenter("en", { granularity: "word" });
    for (const part of segmenter.segment(cleaned)) {
      if (part.isWordLike) words.push(part.segment);
    }
  } else {
    words.push(...(cleaned.match(/[A-Za-z][A-Za-z'’-]*/g) || []));
  }
  for (const raw of words) {
    const word = String(raw || "").replace(/^['’-]+|['’-]+$/g, "");
    if (word.length < 3 || /\d/.test(word)) continue;
    if (/^[A-Z]{2,6}$/.test(word)) continue;
    const key = word.toLowerCase();
    if (ignored.has(key) || SPELLCHECK_PRODUCT_WORDS.has(key)) continue;
    if (checker.check(word) || checker.check(key)) continue;
    let issue = seen.get(key);
    if (!issue) {
      const suggestions = (checker.suggest(key, 4) || [])
        .filter((suggestion) => suggestion && suggestion.toLowerCase() !== key)
        .slice(0, 4)
        .map((suggestion) => spellSuggestionCase(word, suggestion));
      issue = { word, count: 0, suggestions };
      seen.set(key, issue);
    }
    issue.count += 1;
    if (seen.size >= 50) break;
  }
  return Array.from(seen.values());
}

function renderSpellcheckDialog(issues) {
  const holder = document.getElementById("spellcheckIssues");
  holder.innerHTML = issues.map((issue) => `
    <div class="spellcheck-issue" data-spell-issue="${esc(issue.word.toLowerCase())}">
      <div class="spellcheck-word"><strong>${esc(issue.word)}</strong><span>${issue.count} occurrence${issue.count === 1 ? "" : "s"}</span></div>
      <div class="spellcheck-suggestions">
        ${issue.suggestions.length ? issue.suggestions.map((suggestion) => `<button type="button" data-spell-word="${esc(issue.word)}" data-spell-suggestion="${esc(suggestion)}">${esc(suggestion)}</button>`).join("") : `<span class="spellcheck-no-suggestion">No suggestions</span>`}
        <button type="button" class="spellcheck-ignore" data-spell-ignore="${esc(issue.word)}">Ignore</button>
        <button type="button" class="spellcheck-ignore" data-spell-add="${esc(issue.word)}">Add to dictionary</button>
      </div>
    </div>`).join("");
}

function openSpellcheckDialog(session, issues, action) {
  state.spellcheck = { sessionId: session.id, issues, choices: new Map(), action: action === "schedule" ? "schedule" : "send" };
  document.getElementById("spellcheckSubtitle").textContent = `${issues.length} word${issues.length === 1 ? " needs" : "s need"} a second look before this message leaves your outbox.`;
  renderSpellcheckDialog(issues);
  spellcheckOverlay.hidden = false;
  const status = session.el?.querySelector("[data-compose-status]");
  if (status) status.textContent = "Review spelling before sending";
  setTimeout(() => document.querySelector("#spellcheckIssues [data-spell-suggestion]")?.focus(), 80);
}

function closeSpellcheckDialog() {
  const sessionId = state.spellcheck?.sessionId;
  spellcheckOverlay.hidden = true;
  state.spellcheck = null;
  const session = sessionId ? sessionById(sessionId) : null;
  if (session) updateComposeStatus(session);
}

async function runSpellCheckBeforeSend(session, action = "send") {
  if (!session || !state.preferences.spellCheckEnabled || session.spellCheckBypass) return true;
  if (!String(state.preferences.language || "en").toLowerCase().startsWith("en")) return true;
  const status = session.el?.querySelector("[data-compose-status]");
  if (status) status.textContent = "Alt8Mail Intelligence is checking spelling…";
  try {
    const checker = await loadSpellChecker();
    const issues = collectSpellIssues(checker, `${session.subject}\n${session.body}`);
    if (!issues.length) {
      updateComposeStatus(session);
      return true;
    }
    openSpellcheckDialog(session, issues, action);
    return false;
  } catch {
    updateComposeStatus(session);
    showToast("Spell check is unavailable right now — sending without it", "error");
    return true;
  }
}

function applySpellcheckChoices() {
  const review = state.spellcheck;
  const session = review ? sessionById(review.sessionId) : null;
  if (!review || !session) return;
  const replacements = [];
  for (const [key, value] of review.choices.entries()) {
    if (value == null) continue;
    const issue = review.issues.find((item) => item.word.toLowerCase() === key);
    if (issue) replacements.push({ source: issue.word, value });
  }
  if (!replacements.length) return;
  const replaceText = (text) => {
    let output = String(text || "");
    for (const replacement of replacements) {
      const pattern = new RegExp(`\\b${escapeRegExp(replacement.source)}\\b`, "gi");
      output = output.replace(pattern, (match) => spellSuggestionCase(match, replacement.value));
    }
    return output;
  };
  session.subject = replaceText(session.subject);
  const title = session.el?.querySelector("[data-compose-title]");
  if (title) title.textContent = session.subject.trim() || "New message";
  if (session.htmlMode) {
    const editor = session.el?.querySelector("[data-compose-editor]");
    if (editor) {
      const walker = document.createTreeWalker(editor, NodeFilter.SHOW_TEXT);
      const nodes = [];
      while (walker.nextNode()) nodes.push(walker.currentNode);
      for (const node of nodes) {
        const next = replaceText(node.nodeValue);
        if (next !== node.nodeValue) node.nodeValue = next;
      }
      updateComposeEditor(session, editor);
    }
  } else {
    const textarea = session.el?.querySelector('[data-compose-field="body"]');
    const next = replaceText(session.body);
    session.body = next;
    session.bodyHtml = textToEditorHTML(next);
    if (textarea) textarea.value = next;
    updateComposeStatus(session);
    scheduleComposeSave(session);
  }
  renderComposeDockTabs();
}

async function sendComposeSession(id) {
  const session = sessionById(id);
  if (!session || !session.el) return;
  if (validateComposeRecipients(session)) return;
  const sendButton = session.el.querySelector('[data-compose-action="send"]');
  if (sendButton) sendButton.disabled = true;
  try {
    const spellingOK = await runSpellCheckBeforeSend(session);
    if (!spellingOK) {
      if (sendButton) sendButton.disabled = false;
      return;
    }
    await flushDraft(session);
    const data = await api("/api/send", {
      method: "POST",
      body: composeSendPayload(session, { holdSeconds: state.undoSendSeconds })
    });
    state.messages = state.messages.filter((message) => message.id !== session.draftId);
    removeComposeSessionUi(id);
    rebuildIndex();
    renderFolders();
    renderList();
    if (data.queued) {
      const existing = state.pendingSends.find((item) => item.id === data.queued.id);
      if (!existing) state.pendingSends.push(data.queued);
      state.knownPendingIds.add(data.queued.id);
      if (data.outbound) state.outbound = data.outbound;
      updateOutboundUi();
      updatePendingUi();
      updateScheduledUi();
      stopOutboxPolling();
      startOutboxPolling();
      const holdMs = data.holdMs || state.undoSendSeconds * 1000;
      showToast(`Sending “${data.queued.subject}”`, "Undo", async () => {
        await cancelQueuedSend(data.queued.id);
      }, {
        countdownAt: data.queued.sendAt,
        sentMessage: "Message sent",
        duration: holdMs + 2500,
        onExpire: () => {
          setTimeout(() => {
            refreshMailbox({ quiet: true }).catch(() => {});
          }, 700);
        }
      });
    }
  } catch (error) {
    session.error = error.message;
    if (sendButton) sendButton.disabled = false;
    updateComposeStatus(session);
    if (error.code === "DOMAIN_DNS_REQUIRED") {
      openDomainBlockedDialog(error.details || {});
    } else {
      showError(error);
    }
    if (error.code === "OUTBOUND_CAP") openStatusPop("outbound");
  }
}

async function cancelQueuedSend(queueId) {
  const queued = state.pendingSends.find((item) => item.id === queueId);
  try {
    const data = await api(`/api/outbox/${encodeURIComponent(queueId)}/cancel`, { method: "POST", body: {} });
    state.pendingSends = state.pendingSends.filter((item) => item.id !== queueId);
    state.knownPendingIds.delete(queueId);
    if (data.outbound) state.outbound = data.outbound;
    updatePendingUi();
    updateScheduledUi();
    updateOutboundUi();
    stopOutboxPolling();
    startOutboxPolling();
    if (data.draft) {
      upsertDraftMessage(data.draft);
      rebuildIndex();
      renderFolders();
      renderList();
      openComposeFromDraft(data.draft);
    }
    await refreshMailbox({ quiet: true });
    showToast(queued && queued.scheduled ? "Scheduled send cancelled — draft restored" : "Send cancelled — draft restored");
  } catch (error) {
    showError(error);
    if (error.code === "SEND_ALREADY_COMMITTED") {
      state.pendingSends = state.pendingSends.filter((item) => item.id !== queueId);
      updatePendingUi();
      updateScheduledUi();
      refreshMailbox({ quiet: true }).catch(() => {});
    }
  }
}

function validateComposeRecipients(session) {
  if (!session || !session.el) return "Compose window is no longer open";
  flushRecipientInputs(session);
  const entries = parseRecipientList(session.to);
  const fieldWrap = session.el.querySelector('[data-compose-field-wrap="to"]');
  session.error = null;
  updateComposeStatus(session);
  if (!entries.length) {
    if (fieldWrap) fieldWrap.classList.add("is-error");
    session.el.querySelector('[data-recipient-input="to"]')?.focus();
    session.error = "Add at least one recipient.";
    updateComposeStatus(session);
    return session.error;
  }
  const pendingInvalid = RECIPIENT_FIELDS.some((field) => {
    const pending = String(session.recipientInput?.[field] || "").trim();
    if (!pending) return false;
    const parsed = parseRecipientList(pending);
    return !parsed.length || parsed.some((entry) => !entry.valid);
  });
  if (entries.some((entry) => !entry.valid) || pendingInvalid) {
    if (fieldWrap) fieldWrap.classList.add("is-error");
    session.error = "One or more recipient addresses look incomplete.";
    updateComposeStatus(session);
    return session.error;
  }
  if (fieldWrap) fieldWrap.classList.remove("is-error");
  updateComposeStatus(session);
  return null;
}

function composeSendPayload(session, extra = {}) {
  flushRecipientInputs(session);
  return {
    draftId: session.draftId,
    fromEmail: session.fromEmail,
    to: session.to,
    cc: session.cc,
    bcc: session.bcc,
    subject: session.subject,
    bodyText: session.body,
    bodyHtml: session.htmlMode ? restoreComposeImageSources(session.bodyHtml) : "",
    attachments: session.files,
    threadId: session.threadId,
    inReplyTo: session.inReplyTo,
    priority: session.priority,
    receipt: session.receipt,
    delivery: session.delivery,
    saveCopy: session.saveCopy,
    ...extra
  };
}

function minutesFromTime(value) {
  const [hours, minutes] = String(value || "00:00").split(":").map(Number);
  return (hours || 0) * 60 + (minutes || 0);
}

function timeFromMinutes(value) {
  const minutes = Math.max(0, Math.min(23 * 60 + 59, Math.round(value / 15) * 15));
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}

function buildSchedulePresets() {
  const preferences = state.preferences;
  const workStart = preferences.workStart || "09:00";
  const workEnd = preferences.workEnd || "17:00";
  const workDays = new Set(preferences.workDays && preferences.workDays.length ? preferences.workDays : [1, 2, 3, 4, 5]);
  const today = dateKeyInZone(new Date());
  const nowParts = timeZoneParts(new Date());
  const nowMinutes = nowParts.hour * 60 + nowParts.minute;
  const startMinutes = minutesFromTime(workStart);
  const endMinutes = minutesFromTime(workEnd);
  let nextKey = "";
  let nextTime = workStart;
  if (workDays.has(weekdayOfKey(today)) && nowMinutes + 60 < endMinutes) {
    const candidate = Math.max(startMinutes, Math.ceil((nowMinutes + 60) / 15) * 15);
    if (candidate < endMinutes) { nextKey = today; nextTime = timeFromMinutes(candidate); }
  }
  if (!nextKey) {
    for (let offset = 1; offset <= 8; offset += 1) {
      const key = addDaysToKey(today, offset);
      if (workDays.has(weekdayOfKey(key))) { nextKey = key; nextTime = workStart; break; }
    }
  }
  const tomorrow = addDaysToKey(today, 1);
  let monday = addDaysToKey(today, 1);
  for (let offset = 1; offset <= 8; offset += 1) {
    const key = addDaysToKey(today, offset);
    if (weekdayOfKey(key) === 1) { monday = key; break; }
  }
  const afternoonMinutes = Math.min(endMinutes - 30, Math.max(13 * 60, startMinutes + 4 * 60));
  const presets = [
    { id: "next-slot", title: "Recommended", kicker: "Next work slot", date: nextKey, time: nextTime, recommended: true },
    { id: "tomorrow-morning", title: "Tomorrow morning", kicker: "Morning", date: tomorrow, time: workStart },
    { id: "tomorrow-afternoon", title: "Tomorrow afternoon", kicker: "Afternoon", date: tomorrow, time: timeFromMinutes(afternoonMinutes) },
    { id: "monday-morning", title: "Monday morning", kicker: "Start of week", date: monday, time: workStart }
  ];
  const seen = new Set();
  return presets.filter((preset) => {
    const stamp = zonedWallTimeToUtc(preset.date, preset.time);
    if (!stamp || stamp.getTime() <= Date.now() + 5000) return false;
    const key = `${preset.date}T${preset.time}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function openScheduleDialog(session) {
  if (!session || !session.el) return;
  const error = validateComposeRecipients(session);
  if (error) { showToast(error); return; }
  scheduleSession = session;
  scheduleChoice = "next-slot";
  const presets = buildSchedulePresets();
  const alias = aliasForEmail(session.fromEmail);
  const fromLabel = alias ? `${alias.label} <${alias.email}>` : session.fromEmail;
  scheduleMessageCard.innerHTML = `<div class="schedule-message-row"><span class="schedule-message-label">From</span><strong>${esc(fromLabel)}</strong></div>
    <div class="schedule-message-row"><span class="schedule-message-label">To</span><span>${esc(session.to || "No recipient")}${session.cc ? ` · Cc ${esc(session.cc)}` : ""}</span></div>
    <div class="schedule-message-row"><span class="schedule-message-label">Subject</span><span>${esc(session.subject.trim() || "(no subject)")}</span></div>`;
  schedulePresets.innerHTML = presets.map((preset) => {
    const stamp = zonedWallTimeToUtc(preset.date, preset.time);
    return `<button class="schedule-preset${preset.recommended ? " is-recommended" : ""}" data-schedule-preset="${preset.id}" type="button" role="radio" aria-checked="false">
      <span class="schedule-preset-icon"><svg class="icon" aria-hidden="true"><use href="#${preset.id === "monday-morning" ? "i-calendar" : "i-calendar-clock"}"/></svg></span>
      <span class="schedule-preset-copy"><strong>${esc(preset.title)}</strong><span>${esc(formatDateOnly(preset.date))} · ${esc(formatClock(stamp))}</span><small>${esc(formatRelativeTime(stamp))}</small></span>
      ${preset.recommended ? '<span class="schedule-recommended">Recommended</span>' : ""}
      <span class="schedule-radio" aria-hidden="true"></span>
    </button>`;
  }).join("") + `<button class="schedule-preset schedule-custom-card${presets.length ? "" : " is-recommended"}" data-schedule-preset="custom" type="button" role="radio" aria-checked="false">
      <span class="schedule-preset-icon"><svg class="icon" aria-hidden="true"><use href="#i-sliders"/></svg></span>
      <span class="schedule-preset-copy"><strong>Pick a date and time</strong><span>Choose any future moment</span><small>Uses ${esc(formatTimeZoneShort())}</small></span>
      <span class="schedule-radio" aria-hidden="true"></span>
    </button>`;
  state.schedulePresets = presets;
  scheduleZoneInfo.textContent = formatTimeZoneLabel();
  scheduleZoneHint.textContent = `Scheduled times are interpreted in ${currentTimeZone().replace(/_/g, " ")}.`;
  scheduleDate.min = dateKeyInZone(new Date());
  const initial = presets[0] || { date: addDaysToKey(dateKeyInZone(new Date()), 1), time: state.preferences.workStart || "09:00" };
  scheduleChoice = presets[0] ? presets[0].id : "custom";
  scheduleDate.value = initial.date;
  scheduleTime.value = initial.time;
  scheduleWarning.hidden = true;
  scheduleOverlay.hidden = false;
  selectSchedulePreset(scheduleChoice);
  setTimeout(() => document.querySelector(`[data-schedule-preset="${scheduleChoice}"]`)?.focus(), 40);
}

function selectSchedulePreset(id) {
  scheduleChoice = id;
  const preset = (state.schedulePresets || []).find((item) => item.id === id);
  if (preset) {
    scheduleDate.value = preset.date;
    scheduleTime.value = preset.time;
  }
  $$("[data-schedule-preset]", schedulePresets).forEach((button) => {
    const active = button.getAttribute("data-schedule-preset") === id;
    button.classList.toggle("is-selected", active);
    button.setAttribute("aria-checked", active ? "true" : "false");
  });
  updateSchedulePreview();
}

function updateSchedulePreview() {
  if (!scheduleDate || !scheduleTime) return;
  const stamp = zonedWallTimeToUtc(scheduleDate.value, scheduleTime.value);
  const valid = stamp && !Number.isNaN(stamp.getTime());
  const future = valid && stamp.getTime() > Date.now() + 5000;
  if (!valid) {
    scheduleInsight.innerHTML = '<span class="schedule-insight-error">Choose a valid date and time.</span>';
    scheduleWarning.hidden = false;
    scheduleWarning.textContent = "Choose a valid date and time.";
    scheduleConfirm.disabled = true;
    return;
  }
  const time = scheduleTime.value;
  const start = state.preferences.workStart || "09:00";
  const end = state.preferences.workEnd || "17:00";
  const workDays = state.preferences.workDays || [1, 2, 3, 4, 5];
  const workDay = workDays.includes(weekdayOfKey(scheduleDate.value));
  const withinHours = time >= start && time < end;
  const outside = !workDay || !withinHours;
  scheduleInsight.innerHTML = `<div class="schedule-insight-main"><span class="schedule-insight-kicker">Leaves the outbox</span><strong>${esc(formatDateTimeFull(stamp))}</strong><span>${esc(formatRelativeTime(stamp))} · ${esc(formatTimeZoneLabel())}</span></div>
    <div class="schedule-insight-grid"><div><span>Calendar</span><strong>${esc(workDay ? "Working day" : "Outside working days")}</strong></div><div><span>Working hours</span><strong>${esc(withinHours ? "Within hours" : "Outside hours")}</strong></div><div><span>Delivery</span><strong>${esc(future ? "Queued on the server" : "Choose a future time")}</strong></div></div>`;
  scheduleConfirm.disabled = !future;
  if (!future) {
    scheduleWarning.hidden = false;
    scheduleWarning.textContent = "Scheduled send must be at least a few seconds in the future.";
  } else if (outside) {
    scheduleWarning.hidden = false;
    scheduleWarning.textContent = `Heads up: this is outside your working ${workDay ? "hours" : "days"}. The message will still send at the selected time.`;
  } else {
    scheduleWarning.hidden = true;
  }
  if (scheduleChoice !== "custom") { /* preset selection already styled */ }
}

async function confirmScheduledSend() {
  const session = scheduleSession;
  if (!session) return;
  const stamp = zonedWallTimeToUtc(scheduleDate.value, scheduleTime.value);
  if (!stamp || stamp.getTime() <= Date.now() + 5000) {
    scheduleWarning.hidden = false;
    scheduleWarning.textContent = "Choose a future date and time.";
    return;
  }
  const error = validateComposeRecipients(session);
  if (error) { showToast(error); return; }
  const button = scheduleConfirm;
  button.disabled = true;
  button.textContent = "Scheduling…";
  try {
    const spellingOK = await runSpellCheckBeforeSend(session, "schedule");
    if (!spellingOK) return;
    await flushDraft(session);
    const data = await api("/api/send", {
      method: "POST",
      body: composeSendPayload(session, { scheduledFor: stamp.toISOString() })
    });
    state.messages = state.messages.filter((message) => message.id !== session.draftId);
    removeComposeSessionUi(session.id);
    rebuildIndex();
    renderFolders();
    renderList();
    if (data.outbound) state.outbound = data.outbound;
    state.pendingSends.push(data.queued);
    updateOutboundUi();
    updateScheduledUi();
    stopOutboxPolling();
    startOutboxPolling();
    scheduleOverlay.hidden = true;
    scheduleSession = null;
    showToast(`Scheduled for ${formatDateTimeFull(data.queued.sendAt)}`, "Undo", async () => {
      await cancelQueuedSend(data.queued.id);
    }, { duration: 12000 });
  } catch (error) {
    scheduleWarning.hidden = false;
    scheduleWarning.textContent = error.message;
    if (error.code === "DOMAIN_DNS_REQUIRED") openDomainBlockedDialog(error.details || {});
    else showError(error);
  } finally {
    button.disabled = false;
    button.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Schedule send';
  }
}

function closeScheduleDialog() {
  scheduleOverlay.hidden = true;
  scheduleSession = null;
}

function swapComposeIdentitySignature(session, fromEmail) {
  const current = session.autoSignature;
  if (!current || (!current.text && !current.html)) return;
  const next = defaultSignatureForCompose(session.signatureContext || "new", fromEmail);
  const nextText = next ? signaturePlainValue(next) : "";
  const nextHTML = next ? signatureRichValue(next) : "";
  let changed = false;
  if (session.htmlMode) {
    const oldHTML = current.html || textToEditorHTML(current.text);
    const replacement = nextHTML || textToEditorHTML(nextText);
    if (oldHTML && session.bodyHtml.includes(oldHTML)) {
      session.bodyHtml = sanitizeEditorHTML(session.bodyHtml.replace(oldHTML, replacement));
      const editor = session.el?.querySelector("[data-compose-editor]");
      if (editor) editor.innerHTML = session.bodyHtml;
      session.body = editorPlainText(session.bodyHtml);
      changed = true;
    }
  } else {
    const oldText = current.text || htmlToText(current.html);
    if (oldText && session.body.trimStart().startsWith(oldText.trimStart())) {
      const rest = session.body.slice(session.body.indexOf(oldText) + oldText.length).replace(/^\s*\n+/, "");
      session.body = [nextText, rest].filter((part) => part && part.trim()).join("\n\n");
      session.bodyHtml = textToEditorHTML(session.body);
      const textarea = session.el?.querySelector('[data-compose-field="body"]');
      if (textarea) textarea.value = session.body;
      changed = true;
    }
  }
  if (!changed) return;
  session.autoSignature = next ? { id: next.id, text: nextText, html: nextHTML } : null;
  session.dirty = true;
  updateComposeStatus(session);
  scheduleComposeSave(session);
}

function updateComposeField(session, target) {
  const field = target.getAttribute("data-compose-field");
  if (!field) return;
  if (RECIPIENT_FIELDS.includes(field)) {
    session.recipientInput[field] = target.value;
    session.dirty = true;
    session.error = null;
    target.removeAttribute("aria-invalid");
    const wrap = target.closest("[data-compose-field-wrap]");
    if (wrap) wrap.classList.remove("is-error");
    updateRecipientSuggestions(session, field);
    return;
  }
  const value = target.type === "checkbox" ? target.checked : target.value;
  session[field] = value;
  if (field === "fromEmail") swapComposeIdentitySignature(session, value);
  session.dirty = true;
  session.error = null;
  if (field === "to") {
    const wrap = target.closest('[data-compose-field-wrap="to"]');
    if (wrap) wrap.classList.remove("is-error");
  }
  if (field === "subject") {
    const title = session.el.querySelector("[data-compose-title]");
    if (title) title.textContent = session.subject.trim() || "New message";
    renderComposeDockTabs();
  } else if (field === "to" || field === "cc") {
    renderComposeDockTabs();
  }
  if (["fromEmail", "priority", "receipt", "delivery", "saveCopy"].includes(field)) updateComposeOptions(session);
  updateComposeStatus(session);
  scheduleComposeSave(session);
}

function updateComposeEditor(session, editor) {
  if (!session || !editor) return;
  session.bodyHtml = sanitizeEditorHTML(editor.innerHTML);
  session.body = editorPlainText(editor.innerHTML);
  session.dirty = true;
  session.error = null;
  updateComposeStatus(session);
  scheduleComposeSave(session);
}

function setComposeBodyText(session, text) {
  if (!session?.el) return;
  const value = String(text || "");
  session.body = value;
  session.bodyHtml = textToEditorHTML(value);
  session.dirty = true;
  session.error = null;
  const editor = session.el.querySelector("[data-compose-editor]");
  const textarea = session.el.querySelector('[data-compose-field="body"]');
  if (editor) editor.innerHTML = session.bodyHtml;
  else if (textarea) textarea.value = value;
  updateComposeStatus(session);
  scheduleComposeSave(session);
}

function refreshComposeEditorContent(session) {
  if (!session?.el) return;
  const editor = session.el.querySelector("[data-compose-editor]");
  if (editor) editor.innerHTML = sanitizeEditorHTML(session.bodyHtml || textToEditorHTML(session.body || ""));
  const textarea = session.el.querySelector('[data-compose-field="body"]');
  if (textarea) textarea.value = session.body || "";
}

function beginComposePointer(event, session, mode) {
  if (isMobile() || !session || session.mode !== "floating") return;
  if (event.button !== undefined && event.button !== 0) return;
  const rect = session.el.getBoundingClientRect();
  session.left = rect.left;
  session.top = rect.top;
  session.width = rect.width;
  session.height = rect.height;
  session.manual = true;
  session.el.style.left = rect.left + "px";
  session.el.style.top = rect.top + "px";
  session.el.style.right = "auto";
  session.el.style.bottom = "auto";
  session.el.style.width = rect.width + "px";
  session.el.style.height = rect.height + "px";
  composePointer = { session, mode, startX: event.clientX, startY: event.clientY, left: rect.left, top: rect.top, width: rect.width, height: rect.height };
  document.body.dataset.composePointer = mode;
  document.addEventListener("pointermove", onComposePointerMove);
  document.addEventListener("pointerup", endComposePointer, { once: true });
  document.addEventListener("pointercancel", endComposePointer, { once: true });
  event.preventDefault();
}

function onComposePointerMove(event) {
  if (!composePointer) return;
  const { session, mode, startX, startY, left, top, width, height } = composePointer;
  const dx = event.clientX - startX;
  const dy = event.clientY - startY;
  const margin = 12;
  if (mode === "drag") {
    session.left = Math.max(margin, Math.min(left + dx, window.innerWidth - width - margin));
    session.top = Math.max(margin, Math.min(top + dy, window.innerHeight - height - margin));
    session.el.style.left = session.left + "px";
    session.el.style.top = session.top + "px";
  } else {
    session.width = Math.max(380, Math.min(width + dx, Math.max(380, window.innerWidth - left - margin)));
    session.height = Math.max(300, Math.min(height + dy, Math.max(300, window.innerHeight - top - margin)));
    session.el.style.width = session.width + "px";
    session.el.style.height = session.height + "px";
  }
}

function endComposePointer() {
  if (!composePointer) return;
  composePointer = null;
  delete document.body.dataset.composePointer;
  document.removeEventListener("pointermove", onComposePointerMove);
}

/* ============================================================
   status bar + quota + pending sends
   ============================================================ */
function renderMailClientSetup() {
  const host = document.getElementById("mailClientDetails");
  if (!host) return;
  const data = state.mailClient;
  if (!data) {
    host.innerHTML = `<div class="settings-note">Server connection details are available after the mailbox finishes loading.</div>`;
    return;
  }
  const card = (label, protocol, details, tone = "") => {
    if (!details) return "";
    return `<div class="mail-client-card${tone ? " " + tone : ""}">
      <div class="mail-client-card-head"><span class="mail-client-protocol">${esc(protocol)}</span>${tone === "recommended" ? '<span class="mail-client-recommended">Recommended</span>' : ""}</div>
      <strong class="mail-client-host">${esc(details.host || data.host || "—")}</strong>
      <dl class="mail-client-fields">
        <div><dt>Port</dt><dd>${esc(details.port || "—")}</dd></div>
        <div><dt>Security</dt><dd>${esc(details.security || "SSL/TLS")}</dd></div>
        <div><dt>Username</dt><dd class="wrap-anywhere">${esc(data.username || state.user?.email || "—")}</dd></div>
      </dl>
      <span class="mail-client-label">${esc(label)}</span>
    </div>`;
  };
  host.innerHTML = [
    card("Incoming mail", "IMAP", data.imap, "recommended"),
    card("Incoming mail", "POP3", data.pop3),
    card("Outgoing mail", "SMTP", data.smtp)
  ].join("") || `<div class="settings-note">Your administrator has not published mail client settings yet.</div>`;
  const note = document.getElementById("mailClientNote");
  if (note && data.note) note.textContent = data.note;
}

function updateQuotaUi(quota = null) {
  if (quota) state.quota = quota;
  const data = state.quota;
  if (!data) return;
  const label = formatBytes(data.usedBytes) + " of " + formatBytes(data.limitBytes);
  const percent = Math.min(100, Math.max(0, Number(data.percent) || 0));
  const quotaLabel = document.getElementById("quotaLabel");
  const quotaPct = document.getElementById("quotaPct");
  const quotaFill = document.getElementById("quotaBarFill");
  const quotaSource = document.getElementById("quotaSource");
  if (quotaLabel) quotaLabel.textContent = label;
  if (quotaPct) quotaPct.textContent = `${percent.toFixed(percent % 1 ? 1 : 0)}%`;
  if (quotaFill) quotaFill.style.width = `${percent}%`;
  if (quotaSource) quotaSource.textContent = "Alt8Mail live";
  const statusFill = document.getElementById("statusQuotaFill");
  const statusLabel = document.getElementById("statusQuotaLabel");
  if (statusFill) statusFill.style.width = `${percent}%`;
  if (statusLabel) statusLabel.textContent = label;
  const storageText = document.getElementById("accountStorageText");
  const storageBar = document.getElementById("accountStorageBar");
  const storagePct = document.getElementById("accountStoragePct");
  if (storageText) storageText.textContent = `${formatBytes(data.usedBytes)} of ${formatBytes(data.limitBytes)} used across mail and attachments.`;
  if (storageBar) storageBar.style.width = `${percent}%`;
  if (storagePct) storagePct.textContent = `${percent.toFixed(percent % 1 ? 1 : 0)}%`;
  if (state.statusPop === "quota") renderStatusPop();
}

function updateOutboundUi() {
  const data = state.outbound;
  const outboundChip = document.getElementById("outboundChip");
  const outboundText = document.getElementById("outboundText");
  if (data) {
    const value = data.usedWithQueued != null ? data.usedWithQueued : data.used;
    if (outboundText) outboundText.textContent = `${value} / ${data.limit} sent${data.queued ? ` · ${data.queued} queued` : ""}`;
    if (outboundChip) outboundChip.title = `Outbound caps enforced by postfwd · ${data.used} sent in the last ${data.windowHours}h${data.queued ? `, ${data.queued} queued` : ""}`;
    const usedValue = document.getElementById("outboundUsedValue");
    const limitChip = document.getElementById("outboundLimitChip");
    const fill = document.getElementById("outboundBarFill");
    const reset = document.getElementById("outboundResetText");
    const pct = document.getElementById("outboundPercentText");
    const percent = Math.min(100, Math.max(0, Number(data.percent) || 0));
    if (usedValue) usedValue.textContent = String(value);
    if (limitChip) limitChip.textContent = `of ${data.limit} per ${data.windowHours}h`;
    if (fill) fill.style.width = `${percent}%`;
    if (reset) reset.textContent = data.queued ? `${data.queued} queued in the undo window` : `Rolling ${data.windowHours}-hour window`;
    if (pct) pct.textContent = `${percent.toFixed(percent % 1 ? 1 : 0)}% used`;
  }
  if (state.statusPop === "outbound") renderStatusPop();
}

function updatePendingUi() {
  const chip = document.getElementById("pendingChip");
  const text = document.getElementById("pendingText");
  if (!chip) return;
  const pending = state.pendingSends.filter((item) => !item.scheduled);
  if (!pending.length) {
    chip.hidden = true;
    if (pendingTickTimer) { clearInterval(pendingTickTimer); pendingTickTimer = null; }
    if (state.statusPop === "pending" && !statusPop.hidden) renderStatusPop();
    return;
  }
  chip.hidden = false;
  const first = pending.slice().sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt))[0];
  const remaining = Date.parse(first.sendAt) - Date.now();
  if (text) text.textContent = pending.length === 1
    ? `Sending in ${formatCountdown(remaining)}`
    : `${pending.length} sending · ${formatCountdown(remaining)}`;
  if (!pendingTickTimer) pendingTickTimer = setInterval(() => updatePendingUi(), 500);
  if (state.statusPop === "pending" && !statusPop.hidden) renderStatusPop();
}

function updateScheduledUi() {
  if (!scheduledChip) return;
  const scheduled = state.pendingSends.filter((item) => item.scheduled).sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
  const countChanged = state.scheduledCount !== scheduled.length;
  state.scheduledCount = scheduled.length;
  if (!scheduled.length) {
    scheduledChip.hidden = true;
  } else {
    scheduledChip.hidden = false;
    const first = scheduled[0];
    const text = document.getElementById("scheduledText");
    if (text) text.textContent = scheduled.length === 1 ? `Scheduled · ${formatListWhen(first.sendAt)}` : `${scheduled.length} scheduled`;
    scheduledChip.title = scheduled.map((item) => `${item.subject} · ${formatDateTimeFull(item.sendAt)}`).join("\n");
  }
  if (countChanged) renderFolders();
  if (state.statusPop === "scheduled" && !statusPop.hidden) renderStatusPop();
  if (state.task === "scheduled") {
    renderScheduled();
    syncStatusRange();
    syncStatusContext();
  }
}

function startOutboxPolling() {
  if (outboxPollTimer) return;
  if (!state.pendingSends.length) return;
  const hasUndo = state.pendingSends.some((item) => !item.scheduled);
  const earliest = Math.min(...state.pendingSends.map((item) => Date.parse(item.sendAt) || Date.now()));
  const delay = hasUndo ? 1500 : Math.min(30000, Math.max(1000, earliest - Date.now() + 500));
  outboxPollTimer = setTimeout(async () => {
    outboxPollTimer = null;
    if (outboxPollBusy) { startOutboxPolling(); return; }
    outboxPollBusy = true;
    try {
      const data = await api("/api/outbox");
      const previous = state.pendingSends.slice();
      state.pendingSends = data.pending || [];
      if (data.outbound) state.outbound = data.outbound;
      updateOutboundUi();
      updatePendingUi();
      updateScheduledUi();
      const emptied = previous.length > 0 && state.pendingSends.length === 0;
      const hadScheduled = previous.some((item) => item.scheduled);
      if (emptied) {
        stopOutboxPolling();
        await refreshMailbox({ quiet: true });
        showToast(hadScheduled ? "Scheduled message sent" : "Message sent");
      } else {
        startOutboxPolling();
      }
    } catch {
      startOutboxPolling();
    } finally {
      outboxPollBusy = false;
    }
  }, delay);
}

function stopOutboxPolling() {
  if (outboxPollTimer) clearTimeout(outboxPollTimer);
  outboxPollTimer = null;
  outboxPollBusy = false;
}

function statusPopMarkup(kind) {
  if (kind === "quota") {
    const data = state.quota;
    if (!data) return "<p>Quota unavailable.</p>";
    return `<div class="status-pop-metric"><strong>${formatBytes(data.usedBytes)}</strong><span>of ${formatBytes(data.limitBytes)} used</span></div>
      <div class="usage-bar"><span style="width:${Math.min(100, data.percent)}%"></span></div>
      <div class="status-pop-meta"><span>Source: Alt8Mail storage</span><span>${data.percent.toFixed(data.percent % 1 ? 1 : 0)}%</span></div>
      <div class="status-breakdown">${(data.folders || []).map((folder) => `<div><span>${esc(folder.label)}</span><span>${formatBytes(folder.bytes)}</span></div>`).join("")}</div>
      <button class="btn-secondary status-refresh" data-status-refresh="quota" type="button"><svg class="icon" aria-hidden="true"><use href="#i-refresh"/></svg>Refresh from Alt8Mail</button>`;
  }
  if (kind === "outbound") {
    const data = state.outbound;
    if (!data) return "<p>Usage unavailable.</p>";
    const value = data.usedWithQueued != null ? data.usedWithQueued : data.used;
    return `<div class="status-pop-metric"><strong>${value}</strong><span>of ${data.limit} messages</span></div>
      <div class="usage-bar"><span style="width:${Math.min(100, data.percent)}%"></span></div>
      <div class="status-pop-meta"><span>Rolling ${data.windowHours}-hour window</span><span>${data.queued} queued</span></div>
      <div class="status-breakdown"><div><span>Sent</span><span>${data.used}</span></div><div><span>Waiting in undo queue</span><span>${data.queued}</span></div><div><span>Remaining</span><span>${data.remaining}</span></div></div>
      <p class="status-pop-note">Enforced per user by postfwd. The cap includes messages still in the undo-send queue.</p>
      <button class="btn-secondary status-refresh" data-status-refresh="outbound" type="button"><svg class="icon" aria-hidden="true"><use href="#i-refresh"/></svg>Refresh usage</button>`;
  }
  if (kind === "pending") {
    const pending = state.pendingSends.filter((item) => !item.scheduled);
    if (!pending.length) return "<p class=\"status-pop-note\">There are no messages waiting to send.</p>";
    return `<div class="pending-list">${pending.map((item) => {
      const remaining = Date.parse(item.sendAt) - Date.now();
      return `<div class="pending-item">
        <div class="pending-main"><strong>${esc(item.subject)}</strong><span>To ${esc((item.to || []).map((person) => person.name || person.email).join(", "))}</span></div>
        <span class="pending-time">${formatCountdown(remaining)}</span>
        <button class="link-btn" data-cancel-send="${esc(item.id)}" type="button" ${remaining <= 0 ? "disabled" : ""}>Undo</button>
      </div>`;
    }).join("")}</div><p class="status-pop-note">Messages can be undone until the countdown ends.</p>`;
  }
  if (kind === "scheduled") {
    const scheduled = state.pendingSends.filter((item) => item.scheduled).sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
    if (!scheduled.length) return "<p class=\"status-pop-note\">No messages are scheduled.</p>";
    return `<div class="pending-list">${scheduled.map((item) => `<div class="pending-item scheduled">
      <div class="pending-main"><strong>${esc(item.subject || "(no subject)")}</strong><span>${esc(formatDateTimeFull(item.sendAt))}</span><span class="pending-to">To ${esc(scheduledRecipients(item))}</span></div>
      <span class="pending-time">${esc(formatRelativeTime(item.sendAt))}</span>
      <div class="scheduled-item-actions">
        <button class="link-btn" data-scheduled-send-now="${esc(item.id)}" type="button">Send now</button>
        <button class="link-btn" data-scheduled-edit="${esc(item.id)}" type="button">Edit</button>
        <button class="link-btn is-danger" data-cancel-send="${esc(item.id)}" type="button">Cancel</button>
      </div>
    </div>`).join("")}</div><p class="status-pop-note">Send now, edit or cancel any message until its scheduled time. Canceling restores a server-backed draft.</p><button class="btn-secondary status-open-scheduled" data-open-scheduled type="button"><svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>Open scheduled center</button>`;
  }
  return "";
}

function renderStatusPop() {
  const kind = state.statusPop;
  if (!kind) return;
  statusPopTitle.textContent = kind === "quota" ? "Mailbox storage" : kind === "outbound" ? "Outbound sending" : kind === "scheduled" ? "Scheduled messages" : "Sending queue";
  statusPopBody.innerHTML = statusPopMarkup(kind);
  $$("[data-cancel-send]", statusPopBody).forEach((button) => {
    button.addEventListener("click", async () => {
      button.disabled = true;
      await cancelQueuedSend(button.getAttribute("data-cancel-send"));
    });
  });
  $$("[data-status-refresh]", statusPopBody).forEach((button) => {
    button.addEventListener("click", async () => {
      button.disabled = true;
      try {
        if (button.getAttribute("data-status-refresh") === "quota") {
          const quota = await api("/api/quota");
          updateQuotaUi(quota);
        } else {
          const data = await api("/api/outbound");
          state.outbound = data.outbound || data;
          updateOutboundUi();
        }
      } catch (error) { showError(error); }
    });
  });
}

function openStatusPop(kind) {
  const allowed = ["quota", "outbound", "pending", "scheduled"];
  state.statusPop = allowed.includes(kind) ? kind : "quota";
  renderStatusPop();
  statusPop.hidden = false;
}

function closeStatusPop() {
  state.statusPop = null;
  statusPop.hidden = true;
}

function syncStatusRange() {
  if (state.task === "calendar") {
    const prefix = `${state.calendarYear}-${String(state.calendarMonth + 1).padStart(2, "0")}`;
    const count = state.calendarEvents.filter((event) => String(event.date).startsWith(prefix)).length;
    statusRange.textContent = `${count} event${count === 1 ? "" : "s"} this month`;
  } else if (state.task === "people") {
    statusRange.textContent = peopleCount.textContent;
  } else if (state.task === "tasks") {
    const open = state.tasks.filter((task) => task.status === "open").length;
    statusRange.textContent = `${open} open · ${state.tasks.length - open} completed`;
  } else if (state.task === "files") {
    statusRange.textContent = `${state.files.length} items`;
  } else if (state.task === "scheduled") {
    statusRange.textContent = `${scheduledItems().length} scheduled`;
  } else if (state.task === "settings") {
    statusRange.textContent = "Workspace settings";
  } else if (!state.query) {
    statusRange.textContent = state.viewThreads.length ? `1–${state.viewThreads.length} of ${state.viewThreads.length}` : "0 conversations";
  }
}

function syncStatusContext() {
  if (state.offline) { statusMid.textContent = "Offline — changes will retry"; return; }
  if (state.task === "calendar") statusMid.textContent = "Calendar synced · Alt8Mail";
  else if (state.task === "people") statusMid.textContent = "Directory synced · Alt8Mail";
  else if (state.task === "tasks") statusMid.textContent = "Task workspace synced · Alt8Mail";
  else if (state.task === "files") statusMid.textContent = `Cabinet synced · ${state.storageProviders.filter((provider) => provider.connected).length} storage providers`;
  else if (state.task === "scheduled") statusMid.textContent = "Server outbox synced · Alt8Mail";
  else if (state.task === "settings") statusMid.textContent = "Policy managed by Alt8Mail";
  else if (state.query) statusMid.textContent = `Searching ${state.messages.length} message index`;
  else statusMid.textContent = "Synced just now";
}

/* ============================================================
   details, trust
   ============================================================ */
function openDetails(messageOrId) {
  const message = typeof messageOrId === "string" ? messageById(messageOrId) : messageOrId;
  if (!message) return;
  const auth = message.auth || {};
  const showExternalTag = tenantExternalTag();
  const externalLabel = showExternalTag ? "External sender" : "Sender verification";
  const rows = [
    ["From", `${message.from.name} <${message.from.email}>`],
    ["To", (message.to || []).map((person) => `${person.name} <${person.email}>`).join(", ") || "—"],
    ["Date", formatFullWhen(message.date)],
    ["Size", formatBytes(message.sizeBytes)],
    ["Folder", folderDef(message.folder).label],
    ["Classification", message.external ? (showExternalTag ? "Not applied · external sender" : "Hidden by organization policy") : (message.classification || "Internal")],
    ["Sender IP", auth.ip || "—"],
    ["TLS", auth.tls || "TLS 1.3"],
    ["Signing domain", auth.signer || "—"],
    ["Retention", "7 years · Alt8Mail policy"]
  ];
  const checks = [
    ["SPF", auth.spf || "none", "Authorised sending host"],
    ["DKIM", auth.dkim || "none", auth.signer ? `Signature domain ${auth.signer}` : "No valid signature"],
    ["DMARC", auth.dmarc || "none", message.external ? "Domain alignment policy" : "Internal domain policy"]
  ];
  detailsCurrentId = message.id;
  detailsSourceMessage = null;
  detailsBody.closest(".dialog")?.classList.remove("is-source");
  detailsBody.innerHTML = `<div class="details-tabs" role="tablist">
      <button class="details-tab is-active" data-details-tab="details" type="button" role="tab" aria-selected="true">Details</button>
      <button class="details-tab" data-details-tab="source" type="button" role="tab" aria-selected="false">Raw source</button>
      <span class="details-tabs-spacer"></span>
      <button class="details-source-action" data-copy-headers type="button" hidden>Copy headers</button>
      <button class="details-source-action" data-download-source type="button" hidden>Download .eml</button>
    </div>
    <div data-details-panel="details">
    <div class="details-security">
      <div class="details-security-head">
        <span class="trust-shield ${showExternalTag && message.external && !message.trusted ? "is-warn" : ""}"><svg class="icon" aria-hidden="true"><use href="#${showExternalTag && message.external ? "i-alert" : "i-shield-check"}"/></svg></span>
        <div><strong>${message.external ? (showExternalTag ? (message.trusted ? "Trusted external sender" : "External sender") : (message.trusted ? "Trusted sender · authentication verified" : externalLabel)) : "Verified internal sender"}</strong><span>${message.external ? "Evaluated by Alt8Mail DKIM, DMARC and DNS checks" : "Verified by the Alt8Mail directory"}</span></div>
      </div>
      <div class="auth-grid">${checks.map(([name, result, detail]) => `<div class="auth-card ${result === "pass" ? "pass" : result === "fail" || result === "softfail" ? "fail" : "none"}"><span class="auth-name">${name}</span><strong>${esc(result)}</strong><span>${esc(detail)}</span></div>`).join("")}</div>
      ${(auth.warnings || []).length ? `<ul class="details-warnings">${auth.warnings.map((warning) => `<li>${esc(warning)}</li>`).join("")}</ul>` : ""}
      ${message.external && showExternalTag ? `<div class="details-trust-actions"><button class="btn-secondary" data-trust-toggle="${esc(message.id)}" data-trust-value="${message.trusted ? "false" : "true"}" type="button">${message.trusted ? "Remove sender trust" : "Mark sender as trusted"}</button><span>Trusting a sender suppresses the external warning for this address.</span></div>` : ""}
    </div>
    <div class="details-grid">${rows.map(([key, value]) => `<div class="details-row"><dt>${esc(key)}</dt><dd>${esc(value)}</dd></div>`).join("")}</div>
    <p class="details-note">Transport results come from the receiving MTA. Open <strong>Raw source</strong> for the complete RFC 822 header block, including every Received hop and originating IP.</p>
    </div>
    <div data-details-panel="source" hidden>
      <div class="source-meta"><span>Complete header block and message body preview</span><span data-source-meta>Waiting to load…</span></div>
      <p class="source-note" data-source-note hidden></p>
      <pre class="source-view" data-source-view>Select Raw source to load the full message headers.</pre>
    </div>`;
  detailsOverlay.hidden = false;
  setTimeout(() => document.getElementById("detailsClose").focus(), 30);
}

function closeDetails() {
  detailsOverlay.hidden = true;
  detailsSourceMessage = null;
  detailsCurrentId = "";
}

function selectDetailsTab(tab) {
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

async function loadDetailsSource() {
  const view = detailsBody.querySelector("[data-source-view]");
  const meta = detailsBody.querySelector("[data-source-meta]");
  const note = detailsBody.querySelector("[data-source-note]");
  if (!view || !detailsCurrentId) return;
  if (detailsSourceMessage && detailsSourceMessage.id === detailsCurrentId) {
    renderDetailsSource(view, meta, note);
    return;
  }
  view.textContent = "Loading the complete header block from the mail server…";
  if (meta) meta.textContent = "Fetching source…";
  if (note) note.hidden = true;
  try {
    const data = await api(`/api/messages/${encodeURIComponent(detailsCurrentId)}/source`);
    detailsSourceMessage = { id: detailsCurrentId, ...data };
    renderDetailsSource(view, meta, note);
  } catch (error) {
    view.textContent = error && error.message ? error.message : "The message source could not be loaded.";
    if (meta) meta.textContent = "Unavailable";
  }
}

function renderDetailsSource(view, meta, note) {
  const data = detailsSourceMessage;
  if (!view || !data) return;
  view.textContent = data.source || data.headers || "";
  if (meta) {
    const size = formatBytes(Number(data.sizeBytes) || 0);
    meta.textContent = `${data.headerCount || 0} headers · ${size}${data.truncated ? " · preview capped" : ""}`;
  }
  if (note) {
    note.hidden = !data.note;
    note.textContent = data.note || "";
  }
}

async function copyDetailsHeaders() {
  if (!detailsSourceMessage) return;
  try {
    await navigator.clipboard.writeText(detailsSourceMessage.headers || "");
    showToast("Headers copied to clipboard");
  } catch {
    showToast("Clipboard access was denied");
  }
}

function downloadDetailsSource() {
  if (!detailsSourceMessage) return;
  const message = messageById(detailsCurrentId);
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
  showToast("Message downloaded as .eml");
}

async function toggleTrust(messageId, trusted) {
  try {
    await api(`/api/messages/${encodeURIComponent(messageId)}/trust`, { method: "POST", body: { trusted } });
    const message = messageById(messageId);
    if (message) {
      const email = normalizeEmail(message.from.email);
      state.messages.forEach((item) => {
        if (normalizeEmail(item.from && item.from.email) === email) item.trusted = trusted;
      });
    }
    rebuildIndex();
    renderList();
    renderReader({ preserveScroll: true });
    if (!detailsOverlay.hidden) openDetails(messageId);
    showToast(trusted ? "Sender marked as trusted" : "Sender trust removed");
  } catch (error) {
    showError(error);
  }
}

/* ============================================================
   settings
   ============================================================ */
const SETTINGS_META = {
  appearance: { title: "Appearance", eyebrow: "Workspace preferences" },
  mail: { title: "Mail & reading", eyebrow: "How you read and compose" },
  compose: { title: "Compose", eyebrow: "Editor, window and sending defaults" },
  storage: { title: "Storage", eyebrow: "Cabinet and connected drives" },
  identities: { title: "Identities & forwarding", eyebrow: "Verified senders and mail forwarding" },
  notifications: { title: "Notifications", eyebrow: "Delivery and alerts" },
  regional: { title: "Language & time", eyebrow: "Locale, time zone and work hours" },
  rules: { title: "Rules", eyebrow: "Server-side Sieve" },
  autoreply: { title: "Automatic replies", eyebrow: "Out of office" },
  audit: { title: "Audit log", eyebrow: "Account activity" },
  security: { title: "Security & privacy", eyebrow: "Alt8Mail policy" },
  account: { title: "Account", eyebrow: "Profile and sessions" },
  about: { title: "About", eyebrow: "Alt8Mail Email System" }
};

function showSettingsTab(tab) {
  if (!SETTINGS_META[tab]) tab = "appearance";
  state.settingsTab = tab;
  $$("[data-settings-tab]").forEach((button) => {
    const active = button.getAttribute("data-settings-tab") === tab;
    button.classList.toggle("is-active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  $$("[data-settings-panel]").forEach((panel) => {
    const active = panel.getAttribute("data-settings-panel") === tab;
    panel.classList.toggle("is-active", active);
    panel.hidden = !active;
  });
  const meta = SETTINGS_META[tab];
  const title = document.getElementById("settingsTitle");
  const eyebrow = document.getElementById("settingsEyebrow");
  if (title) title.textContent = meta.title;
  if (eyebrow) eyebrow.textContent = meta.eyebrow;
  const scroll = document.getElementById("settingsScroll");
  if (scroll) scroll.scrollTop = 0;
  renderSettingsDynamic();
  writeAppHash({ replace: true });
}

function openSettings(tab) {
  if (tab) state.settingsTab = tab;
  setTask("settings");
}

function renderSettingsDynamic() {
  if (state.task !== "settings" && !["rules", "audit", "account", "security", "regional", "autoreply", "identities"].includes(state.settingsTab)) return;
  if (state.settingsTab === "rules") renderRules();
  else if (state.settingsTab === "mail") { syncPreferencesUi(); renderSignatureLibrary(); loadSignatures(); }
  else if (["appearance", "compose", "notifications"].includes(state.settingsTab)) syncPreferencesUi();
  else if (state.settingsTab === "storage") renderStorageSettings();
  else if (state.settingsTab === "identities") { renderIdentities(); refreshIdentities(); loadForwardingSettings(); }
  else if (state.settingsTab === "autoreply") renderAutoReply();
  else if (state.settingsTab === "regional") syncPreferencesUi();
  else if (state.settingsTab === "audit") renderAudit();
  else if (state.settingsTab === "account") { renderAliases(); loadProfile(); loadAccountSessions(); }
  else if (state.settingsTab === "security") { updateOutboundUi(); renderSecuritySettings(); }
  else if (state.settingsTab === "about") updateServiceWorkerUi();
}

function vacationModel() {
  const raw = (state.sieve && state.sieve.vacation) || {};
  return {
    enabled: !!raw.enabled,
    start: raw.start || "",
    end: raw.end || "",
    days: raw.days || 7,
    internal: {
      subject: (raw.internal && raw.internal.subject) || raw.subject || "Out of office",
      message: (raw.internal && raw.internal.message) || raw.message || ""
    },
    external: {
      enabled: raw.external ? !!raw.external.enabled : false,
      subject: (raw.external && raw.external.subject) || "Away from the office",
      message: (raw.external && raw.external.message) || ""
    }
  };
}

function renderAutoReply() {
  if (!autoReplyToggle) return;
  const vacation = vacationModel();
  const hasRange = Boolean(vacation.start || vacation.end);
  autoReplyToggle.setAttribute("aria-checked", vacation.enabled ? "true" : "false");
  autoReplyRangeToggle.setAttribute("aria-checked", hasRange ? "true" : "false");
  autoReplyExternalToggle.setAttribute("aria-checked", vacation.external.enabled ? "true" : "false");
  autoReplyStart.value = vacation.start;
  autoReplyEnd.value = vacation.end;
  autoReplyDays.value = vacation.days;
  autoReplyInternalSubject.value = vacation.internal.subject;
  autoReplyInternalMessage.value = vacation.internal.message;
  autoReplyExternalSubject.value = vacation.external.subject;
  autoReplyExternalMessage.value = vacation.external.message;
  autoReplyRangeFields.classList.toggle("is-disabled", !hasRange);
  autoReplyStart.disabled = !hasRange;
  autoReplyEnd.disabled = !hasRange;
  document.querySelectorAll(".auto-reply-card")[1]?.classList.toggle("is-disabled", !vacation.external.enabled);
  autoReplyStatus.classList.toggle("is-on", vacation.enabled);
  autoReplyStatus.innerHTML = vacation.enabled
    ? `<span class="auto-reply-dot" aria-hidden="true"></span><div><strong>Automatic replies are on</strong><span>${hasRange ? `${esc(vacation.start)} to ${esc(vacation.end)}` : "Replying until you turn it off"} · ${vacation.external.enabled ? "inside and outside Alt8Mail" : "inside Alt8Mail only"}</span></div>`
    : '<span class="auto-reply-dot" aria-hidden="true"></span><div><strong>Automatic replies are off</strong><span>Your colleagues and external senders receive normal mail delivery.</span></div>';
  updateAutoReplyPreview();
}

function updateAutoReplyPreview() {
  if (!autoReplyPreview) return;
  const enabled = autoReplyToggle.getAttribute("aria-checked") === "true";
  const external = autoReplyExternalToggle.getAttribute("aria-checked") === "true";
  const start = autoReplyStart.value;
  const end = autoReplyEnd.value;
  const internalSubject = autoReplyInternalSubject.value.trim() || "Out of office";
  const internalMessage = autoReplyInternalMessage.value.trim() || "No message set.";
  const externalSubject = autoReplyExternalSubject.value.trim() || "Away from the office";
  const externalMessage = autoReplyExternalMessage.value.trim() || "No external message set.";
  autoReplyPreview.innerHTML = `<div class="auto-reply-preview-head"><span>Preview</span><span class="policy-chip ${enabled ? "is-live" : ""}">${enabled ? "Active" : "Off"}</span></div>
    <div class="auto-reply-mail">
      <span class="auto-reply-mail-kicker">Inside Alt8Mail</span>
      <strong>${esc(internalSubject)}</strong>
      <p>${esc(internalMessage).replace(/\n/g, "<br>")}</p>
    </div>
    ${external ? `<div class="auto-reply-mail external"><span class="auto-reply-mail-kicker">Outside Alt8Mail</span><strong>${esc(externalSubject)}</strong><p>${esc(externalMessage).replace(/\n/g, "<br>")}</p></div>` : ""}
    ${start && end ? `<span class="auto-reply-window">Active ${esc(start)} → ${esc(end)}</span>` : ""}`;
}

async function saveAutomaticReplies() {
  const enabled = autoReplyToggle.getAttribute("aria-checked") === "true";
  const rangeEnabled = autoReplyRangeToggle.getAttribute("aria-checked") === "true";
  const start = rangeEnabled ? autoReplyStart.value : "";
  const end = rangeEnabled ? autoReplyEnd.value : "";
  if (enabled && rangeEnabled && (!start || !end)) {
    showToast("Choose both a start and end date for automatic replies");
    return;
  }
  if (enabled && rangeEnabled && end < start) {
    showToast("The end date must be after the start date");
    return;
  }
  const vacation = {
    enabled,
    start,
    end,
    days: Math.max(1, Math.min(30, Number(autoReplyDays.value) || 7)),
    internal: {
      subject: autoReplyInternalSubject.value.trim() || "Out of office",
      message: autoReplyInternalMessage.value.trim()
    },
    external: {
      enabled: autoReplyExternalToggle.getAttribute("aria-checked") === "true",
      subject: autoReplyExternalSubject.value.trim() || "Away from the office",
      message: autoReplyExternalMessage.value.trim()
    }
  };
  if (enabled && !vacation.internal.message) { showToast("Add an inside-organization message"); return; }
  if (enabled && vacation.external.enabled && !vacation.external.message) { showToast("Add an outside-organization message"); return; }
  if (await saveSieve(state.sieve.rules, vacation, enabled ? "Automatic replies saved" : "Automatic replies turned off")) {
    renderAutoReply();
  }
}

function renderSecuritySettings() {
  if (!twoFactorCard) return;
  const factor = state.security && state.security.twoFactor;
  const realAuth = state.mailboxMode === "imap";
  const passkeys = (factor && factor.passkeys) || [];
  const totpEnabled = Boolean(factor && (factor.totpEnabled || (state.mailboxMode !== "imap" && factor.enabled)));
  const setupTotpBtn = document.getElementById("setupTotpBtn");
  const addPasskeyBtn = document.getElementById("addPasskeyBtn");
  const disableTotpBtn = document.getElementById("disableTotpBtn");
  const policyToggle = document.getElementById("enrollmentPolicyToggle");
  const policyRow = policyToggle && policyToggle.closest(".settings-block");
  if (setupTotpBtn) setupTotpBtn.hidden = !realAuth || totpEnabled;
  if (addPasskeyBtn) addPasskeyBtn.hidden = !realAuth;
  if (disableTotpBtn) disableTotpBtn.hidden = !realAuth || !totpEnabled;
  if (policyRow) policyRow.hidden = !realAuth;
  if (policyToggle) policyToggle.setAttribute("aria-checked", factor && factor.enrollmentRequired ? "true" : "false");
  const passkeyHolder = document.getElementById("passkeySettingsList");
  if (passkeyHolder) {
    passkeyHolder.innerHTML = passkeys.length ? passkeys.map((passkey) => `<div class="session-row">
      <span class="session-icon"><svg class="icon" aria-hidden="true"><use href="#i-fingerprint"/></svg></span>
      <div class="pref-copy"><strong>${esc(passkey.name || "Passkey")}</strong><span>Added ${esc(formatDateOnly(passkey.createdAt))}${passkey.lastUsedAt ? ` · last used ${esc(formatDateOnly(passkey.lastUsedAt))}` : ""}</span></div>
      <button class="link-btn" data-remove-passkey="${esc(passkey.id)}" type="button">Remove</button>
    </div>`).join("") : '<div class="settings-note">No passkeys registered.</div>';
  }
  refreshAppPasswords();
  if (!factor || !factor.enabled) {
    twoFactorStatus.textContent = "Two-factor authentication is off";
    twoFactorDetail.textContent = "Enable an authenticator app or a passkey to protect password sign-in.";
    twoFactorMethods.innerHTML = '<span class="security-method is-off">Not configured</span>';
    return;
  }
  twoFactorStatus.textContent = "Two-factor authentication is on";
  twoFactorDetail.textContent = "Password sign-in requires a passkey, authenticator code or recovery code.";
  twoFactorMethods.innerHTML = [
    passkeys.length ? `<span class="security-method"><svg class="icon" aria-hidden="true"><use href="#i-fingerprint"/></svg>${passkeys.length} passkey${passkeys.length === 1 ? "" : "s"}</span>` : "",
    totpEnabled ? '<span class="security-method"><svg class="icon" aria-hidden="true"><use href="#i-lock"/></svg>Authenticator</span>' : "",
    `<span class="security-method"><svg class="icon" aria-hidden="true"><use href="#i-key"/></svg>${factor.recoveryCodesRemaining || 0} recovery code${factor.recoveryCodesRemaining === 1 ? "" : "s"}</span>`
  ].filter(Boolean).join("");
}

async function refreshAppPasswords() {
  const holder = document.getElementById("appPasswordList");
  if (!holder) return;
  try {
    const data = await api("/api/security/app-passwords");
    const passwords = data.appPasswords || [];
    const counter = document.getElementById("appPasswordCount");
    if (counter) counter.textContent = `${data.count || 0} active${passwords.length ? ` · ${passwords.length} in history` : ""}`;
    holder.innerHTML = passwords.length ? passwords.map((item) => {
      const active = item.status === "active";
      const details = [];
      details.push(`Created ${formatDateOnly(item.createdAt)}`);
      if (item.lastUsedAt) details.push(`Last used ${formatDateOnly(item.lastUsedAt)}`);
      else if (active) details.push("Never used");
      if (item.expiresAt) details.push(`${item.status === "expired" ? "Expired" : "Expires"} ${formatDateOnly(item.expiresAt)}`);
      else if (item.status !== "revoked") details.push("No expiry");
      if (item.revokedAt) details.push(`Revoked ${formatDateOnly(item.revokedAt)}`);
      return `<div class="app-password-item${active ? "" : " is-inactive"}">
        <span class="session-icon"><svg class="icon" aria-hidden="true"><use href="#${active ? "i-key" : "i-history"}"/></svg></span>
        <div class="pref-copy"><strong>${esc(item.label || "Mail client")} <span class="app-password-status ${esc(item.status)}">${esc(item.status)}</span></strong><span>${esc(details.join(" · "))}</span></div>
        ${active ? `<button class="link-btn" data-remove-app-password="${esc(item.id)}" type="button">Revoke</button>` : ""}
      </div>`;
    }).join("") : '<div class="settings-note">No app passwords yet. Create one for a CalDAV/CardDAV client.</div>';
  } catch (error) {
    holder.innerHTML = '<div class="settings-note">App passwords could not be loaded.</div>';
  }
}

async function createAppPassword() {
  const label = document.getElementById("appPasswordLabel");
  const current = document.getElementById("appPasswordCurrent");
  const expiry = document.getElementById("appPasswordExpiry");
  const customDate = document.getElementById("appPasswordCustomDate");
  const target = document.getElementById("appPasswordToken");
  if (!current || !current.value) { showToast("Enter your current password"); current?.focus(); return; }
  const body = { label: label ? label.value.trim() : "", password: current.value };
  if (expiry && expiry.value === "custom") {
    if (!customDate || !customDate.value) { showToast("Choose an expiry date"); customDate?.focus(); return; }
    body.expiresAt = new Date(customDate.value + "T23:59:59").toISOString();
  } else if (expiry && expiry.value !== "0") {
    body.expiresInDays = Number(expiry.value);
  }
  try {
    const data = await api("/api/security/app-passwords", { method: "POST", body });
    if (target) {
      target.hidden = false;
      target.innerHTML = `<strong>App password created</strong><br>Copy it now — it will not be shown again:<br><code>${esc(data.token)}</code><button class="link-btn" data-copy-app-token="${esc(data.token)}" type="button">Copy token</button>${data.expiresAt ? `<br><span>Expires ${esc(formatDateOnly(data.expiresAt))}</span>` : ""}`;
    }
    if (current) current.value = "";
    if (label) label.value = "";
    if (customDate) customDate.value = "";
    showToast("App password created");
    refreshAppPasswords();
  } catch (error) {
    showError(error);
  }
}

async function revokeAppPassword(id) {
  if (!(await appConfirm({ title: "Revoke app password?", message: "Devices using this app password will lose access immediately.", confirmLabel: "Revoke", tone: "danger" }))) return;
  try {
    await api(`/api/security/app-passwords/${encodeURIComponent(id)}`, { method: "DELETE" });
    showToast("App password revoked");
    refreshAppPasswords();
  } catch (error) {
    showError(error);
  }
}

async function openRecoveryCodes() {
  try {
    const data = await api("/api/security/recovery-codes");
    recoveryCodeList.innerHTML = data.codes.length
      ? data.codes.map((code) => `<code>${esc(code)}</code>`).join("")
      : `<p class="settings-note">${data.remaining ? `${data.remaining} recovery codes remain. For security they are stored hashed and shown only when generated.` : "No recovery codes remain. Enable an authenticator or passkey, then generate a new set."}</p>`;
    recoveryOverlay.hidden = false;
  } catch (error) {
    showError(error);
  }
}

async function regenerateRecoveryCodes() {
  try {
    const data = await api("/api/security/recovery-codes", { method: "POST", body: {} });
    recoveryCodeList.innerHTML = data.codes.map((code) => `<code>${esc(code)}</code>`).join("");
    if (state.security && state.security.twoFactor) state.security.twoFactor.recoveryCodesRemaining = data.remaining;
    renderSecuritySettings();
    showToast("New recovery codes generated");
  } catch (error) {
    showError(error);
  }
}

function renderAliases() {
  const holder = document.getElementById("aliasList");
  if (!holder || !state.aliases.length) return;
  holder.innerHTML = state.aliases.map((alias) => `<div class="alias-row${alias.canSend ? "" : " is-disabled"}">
    <span class="alias-icon"><svg class="icon" aria-hidden="true"><use href="#${alias.kind === "shared" ? "i-people" : "i-mail"}"/></svg></span>
    <div class="alias-copy"><strong>${esc(alias.label)}</strong><span>${esc(alias.email)}</span></div>
    <span class="policy-chip">${alias.kind === "primary" ? "Primary" : alias.kind === "shared" ? "Shared" : alias.kind === "identity" ? "Identity" : "Alias"}</span>
    <span class="alias-state">${alias.canSend ? "Can send" : esc(alias.reason || "Read only")}</span>
  </div>`).join("");
}

/* ---------- sending identities and forwarding ---------- */
async function refreshIdentities() {
  try {
    const data = await api("/api/identities");
    state.identities = data.identities || [];
    state.aliases = state.identities.map((item) => ({
      id: item.id, email: item.email, label: item.label, kind: item.kind,
      canSend: item.canSend, reason: item.reason || "", source: item.source
    }));
    renderIdentities();
    renderAliases();
    refreshComposeIdentityOptions();
    syncPreferencesUi();
  } catch (error) {
    const holder = document.getElementById("identityList");
    if (holder) holder.innerHTML = `<div class="settings-note">${esc(error.message || "Identities could not be loaded.")}</div>`;
  }
}

function renderIdentities() {
  const holder = document.getElementById("identityList");
  if (!holder) return;
  const identities = state.identities || [];
  if (!identities.length) {
    holder.innerHTML = `<div class="settings-note">No sending identities are available.</div>`;
    return;
  }
  holder.innerHTML = identities.map((item) => {
    const pending = item.status === "pending" || (!item.canSend && item.source === "identity");
    const custom = item.source === "identity";
    const status = pending ? "Pending verification" : item.canSend ? "Verified" : esc(item.reason || "Read only");
    const hasSignature = Boolean(String(item.signature || "").trim() || String(item.signatureHtml || "").trim());
    const actions = [
      `<button class="link-btn" data-edit-identity="${esc(item.id)}" data-identity-email="${esc(item.email)}" type="button">Edit signature</button>`,
      pending ? `<button class="link-btn" data-resend-identity="${esc(item.id)}" type="button">Resend verification</button>` : "",
      custom ? `<button class="link-btn" data-delete-identity="${esc(item.id)}" type="button">Remove</button>` : ""
    ].join("");
    return `<div class="identity-card${pending ? " is-pending" : ""}">
      <span class="identity-art"><svg class="icon" aria-hidden="true"><use href="#${item.kind === "shared" ? "i-people" : item.kind === "identity" ? "i-key" : "i-mail"}"/></svg></span>
      <div class="identity-copy">
        <strong>${esc(item.label || item.email)}</strong>
        <span>${esc(item.email)}${item.replyTo ? ` · reply-to ${esc(item.replyTo)}` : ""}</span>
        <span>${esc(item.kind === "primary" ? "Primary account" : item.kind === "alias" ? "Organization alias" : item.kind === "shared" ? "Shared mailbox · read only" : "Personal sending identity")} · <span class="identity-signature-state${hasSignature ? " has-signature" : ""}">${hasSignature ? "Signature set" : "No signature"}</span></span>
      </div>
      <div class="identity-actions"><span class="identity-status${pending ? " pending" : ""}">${status}</span>${actions}</div>
    </div>`;
  }).join("");
}

/* ============================================================
   Sending identity editor
   ============================================================ */
function identityEditorFormatPayload() {
  const format = state.identityEditor?.format === "plain" ? "plain" : "rich";
  const editor = document.getElementById("identityEditorSignature");
  const plain = document.getElementById("identityEditorSignaturePlain");
  if (format === "plain") {
    const bodyText = String(plain?.value || "").trim();
    return { signature: bodyText, signatureHtml: "" };
  }
  const bodyHtml = sanitizeEditorHTML(editor?.innerHTML || "");
  const bodyText = editorPlainText(bodyHtml) || htmlToText(bodyHtml);
  return { signature: bodyText, signatureHtml: bodyHtml };
}

function setIdentitySignatureMode(mode) {
  if (!state.identityEditor) return;
  const next = mode === "plain" ? "plain" : "rich";
  const previous = state.identityEditor.format;
  const editor = document.getElementById("identityEditorSignature");
  const plain = document.getElementById("identityEditorSignaturePlain");
  if (previous === "rich" && next === "plain" && plain) {
    plain.value = editorPlainText(editor?.innerHTML || "");
  }
  if (previous === "plain" && next === "rich" && editor) {
    editor.innerHTML = textToEditorHTML(plain?.value || "");
  }
  state.identityEditor.format = next;
  document.getElementById("identitySignatureRichWrap").hidden = next !== "rich";
  if (plain) plain.hidden = next !== "plain";
  $$("[data-identity-signature-mode]").forEach((button) => button.classList.toggle("is-active", button.getAttribute("data-identity-signature-mode") === next));
}

function openIdentityEditor(identity = null) {
  const existing = identity || null;
  state.identityEditor = {
    mode: existing ? "edit" : "create",
    id: existing?.id || "",
    source: existing?.source || "identity",
    email: existing?.email || "",
    format: existing?.signatureHtml ? "rich" : "plain"
  };
  const title = document.getElementById("identityEditorTitle");
  const subtitle = document.getElementById("identityEditorSubtitle");
  const name = document.getElementById("identityEditorName");
  const email = document.getElementById("identityEditorEmail");
  const replyTo = document.getElementById("identityEditorReplyTo");
  const editor = document.getElementById("identityEditorSignature");
  const plain = document.getElementById("identityEditorSignaturePlain");
  const error = document.getElementById("identityEditorError");
  const managed = existing && existing.source !== "identity";
  title.textContent = existing ? "Edit sending identity" : "New sending identity";
  subtitle.textContent = existing
    ? (managed ? "This address is managed by IT. You can give it its own signature." : "Update the From name, reply-to and signature for this identity.")
    : "Verify an external address and give it its own From name and signature.";
  name.value = existing?.label || "";
  name.disabled = Boolean(managed);
  email.value = existing?.email || "";
  email.disabled = Boolean(existing);
  replyTo.value = existing?.replyTo || "";
  replyTo.disabled = Boolean(managed);
  const signatureHtml = existing?.signatureHtml || "";
  const signatureText = existing?.signature || "";
  editor.innerHTML = sanitizeEditorHTML(signatureHtml || (signatureText ? textToEditorHTML(signatureText) : ""));
  plain.value = signatureText || (signatureHtml ? htmlToText(signatureHtml) : "");
  error.hidden = true;
  setIdentitySignatureMode(state.identityEditor.format);
  identityEditorOverlay.hidden = false;
  setTimeout(() => (existing ? editor : name).focus(), 80);
}

function closeIdentityEditor() {
  identityEditorOverlay.hidden = true;
  state.identityEditor = null;
}

async function saveIdentityEditor() {
  const data = state.identityEditor;
  if (!data) return;
  const error = document.getElementById("identityEditorError");
  const name = document.getElementById("identityEditorName");
  const emailInput = document.getElementById("identityEditorEmail");
  const replyTo = document.getElementById("identityEditorReplyTo");
  const button = document.getElementById("identityEditorSave");
  error.hidden = true;
  const email = String(emailInput.value || "").trim().toLowerCase();
  if (data.mode === "create" && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
    error.textContent = "Enter a valid email address for the sending identity.";
    error.hidden = false;
    emailInput.focus();
    return;
  }
  button.disabled = true;
  button.textContent = "Saving…";
  try {
    const signature = identityEditorFormatPayload();
    if (data.mode === "create") {
      const result = await api("/api/identities", {
        method: "POST",
        body: { name: name.value.trim(), email, replyTo: replyTo.value.trim(), ...signature }
      });
      closeIdentityEditor();
      await refreshIdentities();
      if (result.verified) showToast("Sending identity added");
      else if (result.delivery === "sent") showToast("Verification email sent — check the external inbox");
      else showToast(result.message || "Identity saved. Use Resend verification in a moment.");
      return;
    }
    if (data.source === "identity") {
      await api(`/api/identities/${encodeURIComponent(data.id)}`, {
        method: "PATCH",
        body: { name: name.value.trim(), replyTo: replyTo.value.trim(), ...signature }
      });
    } else {
      await api("/api/identities/signature", {
        method: "PUT",
        body: { email: data.email, ...signature }
      });
    }
    closeIdentityEditor();
    await refreshIdentities();
    showToast(data.source === "identity" ? "Sending identity updated" : "Identity signature saved");
  } catch (saveError) {
    error.textContent = saveError.message || "Could not save the sending identity.";
    error.hidden = false;
  } finally {
    button.disabled = false;
    button.textContent = "Save identity";
  }
}

/* ============================================================
   Named signature library
   ============================================================ */
function signaturePlainValue(signature) {
  return String(signature?.bodyText || (signature?.bodyHtml ? htmlToText(signature.bodyHtml) : "") || "");
}

function signatureRichValue(signature) {
  return sanitizeEditorHTML(signature?.bodyHtml || textToEditorHTML(signaturePlainValue(signature)));
}

function renderSignatureLibrary() {
  const holder = document.getElementById("signatureList");
  if (!holder) return;
  const signatures = state.signatures || [];
  if (!signatures.length) {
    holder.innerHTML = `<div class="signature-empty"><span class="signature-empty-mark"><svg class="icon" aria-hidden="true"><use href="#i-mark"/></svg></span><div><strong>No signatures yet</strong><p>Create your first signature to use it in Compose and set it as the default for new messages, replies and forwards.</p></div><button class="btn-secondary btn-small" id="signatureEmptyCreate" type="button"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Create signature</button></div>`;
  } else {
    holder.innerHTML = signatures.map((signature) => {
      const chips = [
        signature.defaultNew ? `<span class="signature-chip new">New</span>` : "",
        signature.defaultReply ? `<span class="signature-chip reply">Reply</span>` : "",
        signature.defaultForward ? `<span class="signature-chip forward">Forward</span>` : ""
      ].filter(Boolean).join("");
      const preview = signaturePlainValue(signature).replace(/\s+/g, " ").trim();
      return `<article class="signature-card" data-signature-card="${esc(signature.id)}">
        <span class="signature-card-mark"><svg class="icon" aria-hidden="true"><use href="#i-mark"/></svg></span>
        <div class="signature-card-copy"><strong>${esc(signature.name)}</strong><p>${esc(preview.slice(0, 180) || "Empty signature")}</p><div class="signature-chips">${chips || `<span class="signature-chip muted">No automatic use</span>`}</div></div>
        <div class="signature-card-actions"><button class="link-btn" data-edit-signature="${esc(signature.id)}" type="button">Edit</button><button class="link-btn danger" data-delete-signature="${esc(signature.id)}" type="button">Delete</button></div>
      </article>`;
    }).join("");
  }
  renderSignatureDefaults();
}

function renderSignatureDefaults() {
  const signatures = state.signatures || [];
  const config = [
    ["new", "signatureDefaultNew", "defaultNew"],
    ["reply", "signatureDefaultReply", "defaultReply"],
    ["forward", "signatureDefaultForward", "defaultForward"]
  ];
  for (const [, id, flag] of config) {
    const select = document.getElementById(id);
    if (!select) continue;
    select.innerHTML = `<option value="">No signature</option>` + signatures.map((signature) =>
      `<option value="${esc(signature.id)}"${signature[flag] ? " selected" : ""}>${esc(signature.name)}</option>`
    ).join("");
    const current = signatures.find((signature) => signature[flag]);
    select.value = current ? current.id : "";
  }
}

async function loadSignatures() {
  try {
    const data = await api("/api/signatures");
    state.signatures = data.signatures || [];
  } catch {
    state.signatures = state.signatures || [];
  }
  renderSignatureLibrary();
}

function signatureEditorFormatPayload() {
  const format = state.signatureEditor?.format === "plain" ? "plain" : "rich";
  const editor = document.getElementById("signatureEditorBody");
  const plain = document.getElementById("signatureEditorPlain");
  if (format === "plain") {
    return { bodyText: String(plain?.value || "").trim(), bodyHtml: "" };
  }
  const bodyHtml = sanitizeEditorHTML(editor?.innerHTML || "");
  return { bodyText: editorPlainText(bodyHtml) || htmlToText(bodyHtml), bodyHtml };
}

function setSignatureEditorMode(mode) {
  if (!state.signatureEditor) return;
  const next = mode === "plain" ? "plain" : "rich";
  const previous = state.signatureEditor.format;
  const editor = document.getElementById("signatureEditorBody");
  const plain = document.getElementById("signatureEditorPlain");
  if (previous === "rich" && next === "plain" && plain) {
    plain.value = editorPlainText(editor?.innerHTML || "");
  }
  if (previous === "plain" && next === "rich" && editor) {
    editor.innerHTML = textToEditorHTML(plain?.value || "");
  }
  state.signatureEditor.format = next;
  document.getElementById("signatureRichWrap").hidden = next !== "rich";
  if (plain) plain.hidden = next !== "plain";
  $$("[data-signature-mode]").forEach((button) => button.classList.toggle("is-active", button.getAttribute("data-signature-mode") === next));
}

function openSignatureEditor(signature = null, { onSaved = null } = {}) {
  state.signatureEditor = {
    id: signature?.id || "",
    format: signature?.bodyHtml ? "rich" : (signature ? "plain" : "rich"),
    onSaved
  };
  document.getElementById("signatureEditorTitle").textContent = signature ? "Edit signature" : "New signature";
  document.getElementById("signatureEditorSubtitle").textContent = signature
    ? "Update the name, content and automatic uses for this signature."
    : "Name the signature and write it once. Use it in any message or make it the automatic default.";
  document.getElementById("signatureEditorName").value = signature?.name || "";
  document.getElementById("signatureEditorBody").innerHTML = sanitizeEditorHTML(signature?.bodyHtml || (signature?.bodyText ? textToEditorHTML(signature.bodyText) : ""));
  document.getElementById("signatureEditorPlain").value = signature?.bodyText || (signature?.bodyHtml ? htmlToText(signature.bodyHtml) : "");
  document.getElementById("signatureDefaultNewFlag").checked = signature ? Boolean(signature.defaultNew) : !(state.signatures || []).length;
  document.getElementById("signatureDefaultReplyFlag").checked = Boolean(signature?.defaultReply);
  document.getElementById("signatureDefaultForwardFlag").checked = Boolean(signature?.defaultForward);
  document.getElementById("signatureEditorStatus").textContent = "";
  setSignatureEditorMode(state.signatureEditor.format);
  signatureEditorOverlay.hidden = false;
  setTimeout(() => (signature ? document.getElementById("signatureEditorBody") : document.getElementById("signatureEditorName")).focus(), 80);
}

function closeSignatureEditor() {
  signatureEditorOverlay.hidden = true;
  state.signatureEditor = null;
}

async function saveSignatureEditor() {
  const editorState = state.signatureEditor;
  if (!editorState) return;
  const name = String(document.getElementById("signatureEditorName").value || "").trim();
  const status = document.getElementById("signatureEditorStatus");
  if (!name) {
    status.textContent = "Give this signature a name before saving.";
    document.getElementById("signatureEditorName").focus();
    return;
  }
  const payload = signatureEditorFormatPayload();
  if (!payload.bodyText && !payload.bodyHtml) {
    status.textContent = "Write something in the signature before saving.";
    return;
  }
  payload.name = name;
  payload.defaultNew = document.getElementById("signatureDefaultNewFlag").checked;
  payload.defaultReply = document.getElementById("signatureDefaultReplyFlag").checked;
  payload.defaultForward = document.getElementById("signatureDefaultForwardFlag").checked;
  const button = document.getElementById("signatureEditorSave");
  const onSaved = editorState.onSaved;
  button.disabled = true;
  button.textContent = "Saving…";
  try {
    const data = editorState.id
      ? await api(`/api/signatures/${encodeURIComponent(editorState.id)}`, { method: "PATCH", body: payload })
      : await api("/api/signatures", { method: "POST", body: payload });
    closeSignatureEditor();
    await loadSignatures();
    showToast(editorState.id ? "Signature updated" : "Signature created");
    if (typeof onSaved === "function" && data.signature) onSaved(data.signature);
  } catch (error) {
    status.textContent = error.message || "Could not save the signature.";
  } finally {
    button.disabled = false;
    button.textContent = "Save signature";
  }
}

async function deleteSignature(id) {
  const signature = (state.signatures || []).find((item) => item.id === id);
  if (!(await appConfirm({ title: "Delete signature?", message: `“${signature?.name || "This signature"}” will be removed from every message and default.`, confirmLabel: "Delete signature", tone: "danger" }))) return;
  try {
    await api(`/api/signatures/${encodeURIComponent(id)}`, { method: "DELETE" });
    await loadSignatures();
    showToast("Signature deleted");
  } catch (error) { showError(error); }
}

async function setSignatureDefault(kind, id) {
  const flags = { new: "defaultNew", reply: "defaultReply", forward: "defaultForward" };
  const flag = flags[kind];
  const current = (state.signatures || []).find((signature) => signature[flag]);
  try {
    if (current && current.id !== id) {
      await api(`/api/signatures/${encodeURIComponent(current.id)}`, { method: "PATCH", body: { [flag]: false } });
    }
    if (id) {
      await api(`/api/signatures/${encodeURIComponent(id)}`, { method: "PATCH", body: { [flag]: true } });
    }
    await loadSignatures();
    showToast("Signature defaults saved");
  } catch (error) { showError(error); await loadSignatures(); }
}

function defaultSignatureForCompose(context, fromEmail) {
  if (state.preferences.signatureEnabled === false) return null;
  const identity = (state.identities || []).find((item) =>
    normalizeEmail(item.email) === normalizeEmail(fromEmail) && (String(item.signature || "").trim() || String(item.signatureHtml || "").trim())
  );
  if (identity) {
    return { id: `identity:${identity.email}`, name: `${identity.label || identity.email} signature`, bodyText: identity.signature || "", bodyHtml: identity.signatureHtml || "" };
  }
  const flag = context === "reply" ? "defaultReply" : context === "forward" ? "defaultForward" : "defaultNew";
  return (state.signatures || []).find((signature) => signature[flag]) || null;
}

function insertSignatureIntoCompose(sessionId, signature) {
  const session = sessionById(sessionId);
  if (!session || !signature) return;
  const text = signaturePlainValue(signature);
  const html = signatureRichValue(signature);
  if (session.htmlMode) {
    const editor = session.el?.querySelector("[data-compose-editor]");
    if (editor) {
      const existing = editor.innerHTML.trim();
      editor.innerHTML = sanitizeEditorHTML(`${existing}${existing ? "<p><br></p>" : ""}<div class="a8m-signature">${html}</div>`);
      updateComposeEditor(session, editor);
      editor.focus();
    }
  } else {
    const textarea = session.el?.querySelector('[data-compose-field="body"]');
    const existing = String(textarea?.value || session.body || "");
    const value = `${existing}${existing.trim() ? "\n\n" : ""}${text}`;
    setComposeBodyText(session, value);
    textarea?.focus();
  }
  showToast("Signature inserted");
}

function openSignaturePicker(session) {
  if (!session) return;
  state.signaturePickerSessionId = session.id;
  if (!(state.signatures || []).length) {
    openSignatureEditor(null, { onSaved: (signature) => insertSignatureIntoCompose(session.id, signature) });
    return;
  }
  const holder = document.getElementById("signaturePickerList");
  holder.innerHTML = state.signatures.map((signature) => {
    const preview = signaturePlainValue(signature).replace(/\s+/g, " ").trim();
    return `<button class="signature-picker-item" type="button" data-insert-signature="${esc(signature.id)}"><span class="signature-picker-mark"><svg class="icon" aria-hidden="true"><use href="#i-mark"/></svg></span><span><strong>${esc(signature.name)}</strong><small>${esc(preview.slice(0, 140) || "Empty signature")}</small></span><span class="signature-picker-add">Insert</span></button>`;
  }).join("");
  signaturePickerOverlay.hidden = false;
}

function closeSignaturePicker() {
  signaturePickerOverlay.hidden = true;
  state.signaturePickerSessionId = null;
}

function insertPickedSignature(id) {
  const session = sessionById(state.signaturePickerSessionId);
  const signature = (state.signatures || []).find((item) => item.id === id);
  if (!session || !signature) { closeSignaturePicker(); return; }
  closeSignaturePicker();
  insertSignatureIntoCompose(session.id, signature);
}

async function resendIdentity(id) {
  try {
    await api(`/api/identities/${encodeURIComponent(id)}/resend`, { method: "POST", body: {} });
    showToast("Verification email sent");
    await refreshIdentities();
  } catch (error) { showError(error); }
}

async function deleteIdentity(id) {
  if (!(await appConfirm({ title: "Remove sending identity?", message: "This address will no longer be available in the From selector.", confirmLabel: "Remove", tone: "danger" }))) return;
  try {
    await api(`/api/identities/${encodeURIComponent(id)}`, { method: "DELETE" });
    showToast("Sending identity removed");
    await refreshIdentities();
  } catch (error) { showError(error); }
}

function refreshComposeIdentityOptions() {
  composeSessions.forEach((session) => {
    if (!session.el) return;
    const select = session.el.querySelector('[data-compose-field="fromEmail"]');
    if (select) {
      select.innerHTML = aliasesForSelect().map((alias) =>
        `<option value="${esc(alias.email)}"${normalizeEmail(session.fromEmail) === normalizeEmail(alias.email) ? " selected" : ""}>${esc(alias.label)} — ${esc(alias.email)}</option>`
      ).join("");
    }
    updateComposeIdentity(session);
  });
}

function switchToggle(element, value) {
  if (!element) return;
  element.setAttribute("aria-checked", value ? "true" : "false");
}

function toggleSwitch(element) {
  if (!element) return;
  switchToggle(element, element.getAttribute("aria-checked") !== "true");
}

function isoToDisplayDate(value) {
  if (!value) return "";
  const [year, month, day] = value.split("-");
  return year && month && day ? `${day}/${month}/${year}` : "";
}

function displayDateToISO(value) {
  const match = String(value || "").trim().match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})$/);
  if (!match) return "";
  const day = Number(match[1]);
  const month = Number(match[2]);
  const year = Number(match[3]);
  const date = new Date(Date.UTC(year, month - 1, day));
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return "";
  return `${year}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

async function loadForwardingSettings() {
  try {
    const data = await api("/api/forwarding");
    state.forwarding = data.forwarding;
    state.forwardingPolicy = data.policy;
    renderForwardingSettings();
  } catch (error) {
    const note = document.getElementById("forwardingNote");
    if (note) note.textContent = error.message || "Forwarding settings could not be loaded.";
  }
}

function renderForwardingSettings() {
  const forwarding = state.forwarding || { enabled: false, target: "", keepCopy: true, permanent: true, start: "", end: "" };
  const enabled = document.getElementById("forwardingEnabled");
  const permanent = document.getElementById("forwardingPermanent");
  const keepCopy = document.getElementById("forwardingKeepCopy");
  switchToggle(enabled, forwarding.enabled);
  switchToggle(permanent, forwarding.permanent !== false);
  switchToggle(keepCopy, forwarding.keepCopy !== false);
  const target = document.getElementById("forwardingTarget");
  if (target) target.value = forwarding.target || "";
  const start = document.getElementById("forwardingStart");
  const end = document.getElementById("forwardingEnd");
  if (start) start.value = isoToDisplayDate(forwarding.start || "");
  if (end) end.value = isoToDisplayDate(forwarding.end || "");
  const dates = document.getElementById("forwardingDates");
  if (dates) dates.hidden = forwarding.permanent !== false;
  const policy = state.forwardingPolicy || {};
  const blocked = policy.autoForwarding === false;
  if (enabled) enabled.disabled = blocked;
  const note = document.getElementById("forwardingNote");
  if (note) {
    note.textContent = blocked
      ? "Auto-forwarding is disabled by your organization."
      : policy.externalForwarding === false
        ? "External forwarding is disabled; only internal recipients are allowed."
        : "Forwarding is applied by Dovecot Sieve before your rules.";
  }
}

async function saveForwardingSettings() {
  const target = document.getElementById("forwardingTarget");
  const enabled = document.getElementById("forwardingEnabled")?.getAttribute("aria-checked") === "true";
  const permanent = document.getElementById("forwardingPermanent")?.getAttribute("aria-checked") === "true";
  const keepCopy = document.getElementById("forwardingKeepCopy")?.getAttribute("aria-checked") === "true";
  const startText = document.getElementById("forwardingStart")?.value || "";
  const endText = document.getElementById("forwardingEnd")?.value || "";
  const start = displayDateToISO(startText);
  const end = displayDateToISO(endText);
  if (enabled && permanent === false && (!start || !end)) {
    showToast("Use DD/MM/YYYY for the forwarding period");
    return;
  }
  if (enabled && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(target?.value.trim() || "")) {
    showToast("Enter a valid forwarding address");
    target?.focus();
    return;
  }
  const button = document.getElementById("saveForwardingBtn");
  if (button) button.disabled = true;
  try {
    const data = await api("/api/forwarding", {
      method: "PUT",
      body: { enabled, target: target?.value.trim() || "", permanent, keepCopy, start, end }
    });
    state.forwarding = data.forwarding;
    if (state.sieve) state.sieve.forwarding = data.forwarding;
    renderForwardingSettings();
    showToast(enabled ? "Mail forwarding saved" : "Mail forwarding disabled");
  } catch (error) {
    showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

const RULE_FIELDS = [["from", "From"], ["to", "To"], ["subject", "Subject"], ["body", "Body"], ["size", "Size"]];
const RULE_OPERATORS = [["contains", "contains"], ["is", "is"], ["matches", "matches"], ["notcontains", "does not contain"], ["over", "is larger than"], ["under", "is smaller than"]];
const RULE_ACTIONS = [["move", "Move to folder"], ["markRead", "Mark as read"], ["star", "Star message"], ["discard", "Discard silently"], ["redirect", "Redirect to address"], ["keep", "Keep in Inbox"]];

function ruleSummary(rule) {
  const conditions = (rule.conditions || []).map((condition) => `${condition.field} ${RULE_OPERATORS.find(([value]) => value === condition.operator)?.[1] || condition.operator} “${condition.value}”`).join(rule.match === "any" ? " or " : " and ");
  const action = rule.action.type === "move" ? `move to ${folderDef(rule.action.value).label}`
    : rule.action.type === "redirect" ? `redirect to ${rule.action.value}`
    : RULE_ACTIONS.find(([value]) => value === rule.action.type)?.[1].toLowerCase() || rule.action.type;
  return `If ${conditions} → ${action}`;
}

function renderRules() {
  if (!state.sieve) return;
  if (state.sieve.rules && !currentRulesCache) currentRulesCache = state.sieve.rules;
  const list = document.getElementById("ruleList");
  const count = document.getElementById("ruleCount");
  if (!list) return;
  const rules = (state.sieve.rules || []).slice().sort((a, b) => (a.priority || 0) - (b.priority || 0));
  if (count) count.textContent = `${rules.length} rule${rules.length === 1 ? "" : "s"} · revision ${state.sieve.revision || 0} · ${rules.filter((rule) => rule.enabled).length} active`;
  list.innerHTML = rules.length ? rules.map((rule, index) => `<div class="rule-card${rule.enabled ? "" : " is-disabled"}" data-rule-id="${esc(rule.id)}">
      <div class="rule-main">
        <button class="switch" data-rule-toggle="${esc(rule.id)}" type="button" role="switch" aria-checked="${rule.enabled ? "true" : "false"}" aria-label="Enable ${esc(rule.name)}"><span></span></button>
        <div class="rule-copy"><strong>${esc(rule.name)}</strong><span>${esc(ruleSummary(rule))}</span></div>
      </div>
      <div class="rule-actions">
        <span class="rule-priority">${index + 1}</span>
        <button class="icon-btn" data-rule-up="${esc(rule.id)}" type="button" aria-label="Move rule up" title="Move up" ${index === 0 ? "disabled" : ""}><svg class="icon" aria-hidden="true"><use href="#i-chevron-down" style="transform:rotate(180deg)"/></svg></button>
        <button class="icon-btn" data-rule-down="${esc(rule.id)}" type="button" aria-label="Move rule down" title="Move down" ${index === rules.length - 1 ? "disabled" : ""}><svg class="icon" aria-hidden="true"><use href="#i-chevron-down"/></svg></button>
        <button class="link-btn" data-rule-edit="${esc(rule.id)}" type="button">Edit</button>
        <button class="icon-btn danger-icon" data-rule-delete="${esc(rule.id)}" type="button" aria-label="Delete rule" title="Delete rule"><svg class="icon" aria-hidden="true"><use href="#i-trash"/></svg></button>
      </div>
    </div>`).join("") : `<div class="rules-empty">No rules yet. Create one to sort mail before it reaches your inbox.</div>`;
  const script = document.getElementById("sieveScript");
  if (script) script.textContent = state.sieve.script || "";
}

function openRuleEditor(rule = null) {
  editingRuleId = rule ? rule.id : null;
  const block = document.getElementById("ruleEditorBlock");
  const title = document.getElementById("ruleEditorTitle");
  if (!block) return;
  block.hidden = false;
  if (title) title.textContent = rule ? "Edit rule" : "New rule";
  const name = document.getElementById("ruleName");
  const match = document.getElementById("ruleMatch");
  if (name) name.value = rule ? rule.name : "";
  if (match) match.value = rule ? rule.match : "all";
  const action = document.getElementById("ruleAction");
  if (action) action.value = rule ? rule.action.type : "move";
  const actionValue = document.getElementById("ruleActionValue");
  if (actionValue) actionValue.value = rule && rule.action.value ? rule.action.value : "archive";
  const conditions = document.getElementById("ruleConditions");
  if (conditions) {
    const rows = rule && rule.conditions.length ? rule.conditions : [{ field: "from", operator: "contains", value: "" }];
    conditions.innerHTML = rows.map((condition, index) => conditionRowMarkup(condition, index)).join("");
  }
  syncRuleActionInputs();
  block.scrollIntoView({ block: "nearest", behavior: "smooth" });
  setTimeout(() => name && name.focus(), 50);
}

function conditionRowMarkup(condition, index) {
  const field = condition.field || "from";
  const size = field === "size";
  return `<div class="rule-condition" data-condition="${index}">
    <select data-condition-field aria-label="Condition field">${RULE_FIELDS.map(([value, label]) => `<option value="${value}"${value === field ? " selected" : ""}>${label}</option>`).join("")}</select>
    <select data-condition-operator aria-label="Condition operator">${(size ? RULE_OPERATORS.filter(([value]) => value === "over" || value === "under") : RULE_OPERATORS.filter(([value]) => !["over", "under"].includes(value))).map(([value, label]) => `<option value="${value}"${value === condition.operator ? " selected" : ""}>${label}</option>`).join("")}</select>
    <input data-condition-value type="${size ? "number" : "text"}" min="${size ? "1" : ""}" value="${esc(condition.value == null ? "" : condition.value)}" placeholder="${size ? "KB" : "value"}" aria-label="Condition value">
    <button class="icon-btn" data-remove-condition type="button" aria-label="Remove condition"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
  </div>`;
}

function syncRuleActionInputs() {
  const action = document.getElementById("ruleAction");
  const wrap = document.getElementById("ruleActionValueWrap");
  const label = document.getElementById("ruleActionValueLabel");
  const input = document.getElementById("ruleActionValue");
  if (!action || !wrap || !input) return;
  const type = action.value;
  const show = ["move", "redirect"].includes(type);
  wrap.hidden = !show;
  if (!show) return;
  if (label) label.textContent = type === "move" ? "Folder" : "Redirect address";
  const previousMode = wrap.dataset.mode || "";
  let value = input.value || "";
  if (previousMode && previousMode !== type) value = type === "move" ? "archive" : "";
  wrap.dataset.mode = type;
  if (type === "move") {
    const folders = (state.folders || []).filter((folder) => !folder.virtual && !folder.delegated).sort((a, b) => folderSortValue(a) - folderSortValue(b));
    if (!folders.some((folder) => folder.id === value)) value = folders.some((folder) => folder.id === "archive") ? "archive" : (folders[0]?.id || "inbox");
    input.outerHTML = `<select id="ruleActionValue" aria-label="Folder">${folders.map((folder) => `<option value="${esc(folder.id)}"${value === folder.id ? " selected" : ""}>${esc(folder.label)}</option>`).join("")}</select>`;
  } else {
    input.outerHTML = `<input id="ruleActionValue" type="email" value="${esc(value)}" placeholder="name@example.com" autocomplete="off">`;
  }
}

function collectRuleForm() {
  const conditions = $$("#ruleConditions .rule-condition").map((row) => ({
    field: $("[data-condition-field]", row).value,
    operator: $("[data-condition-operator]", row).value,
    value: $("[data-condition-value]", row).value.trim()
  })).filter((condition) => condition.value !== "");
  return {
    id: editingRuleId || `rule_${uid("r").slice(2, 14)}`,
    name: document.getElementById("ruleName").value.trim(),
    match: document.getElementById("ruleMatch").value,
    conditions,
    action: {
      type: document.getElementById("ruleAction").value,
      value: document.getElementById("ruleActionValue") ? document.getElementById("ruleActionValue").value.trim() : ""
    },
    enabled: true,
    priority: state.sieve.rules.length + 1
  };
}

async function saveSieve(rules, vacation, message = "Rules saved") {
  try {
    const data = await api("/api/sieve", { method: "PUT", body: { rules, vacation } });
    state.sieve = data;
    currentRulesCache = data.rules;
    renderRules();
    renderSettingsDynamic();
    showToast(message);
    return true;
  } catch (error) {
    showError(error);
    return false;
  }
}

async function saveRuleForm(event) {
  event.preventDefault();
  const rule = collectRuleForm();
  if (!rule.name) { showToast("Give the rule a name"); return; }
  if (!rule.conditions.length) { showToast("Add at least one condition with a value"); return; }
  if (["move", "redirect"].includes(rule.action.type) && !rule.action.value) { showToast("Add the action target"); return; }
  const rules = (state.sieve.rules || []).filter((item) => item.id !== rule.id);
  rules.push(rule);
  if (await saveSieve(rules, state.sieve.vacation, editingRuleId ? "Rule updated" : "Rule created")) {
    document.getElementById("ruleEditorBlock").hidden = true;
    editingRuleId = null;
  }
}

async function refreshRules() {
  if (rulesLoading) return;
  rulesLoading = true;
  try {
    state.sieve = await api("/api/sieve");
    currentRulesCache = state.sieve.rules;
    renderRules();
    showToast("Rules refreshed");
  } catch (error) { showError(error); }
  finally { rulesLoading = false; }
}

async function renderAudit() {
  if (auditLoading) return;
  auditLoading = true;
  const filter = document.getElementById("auditFilter")?.value || "";
  const body = document.getElementById("auditTableBody");
  if (body) body.innerHTML = '<tr><td colspan="5" class="audit-empty">Loading audit log…</td></tr>';
  try {
    const data = await api(`/api/audit?limit=100${filter ? `&category=${encodeURIComponent(filter)}` : ""}`);
    if (!body) return;
    if (!data.entries.length) {
      body.innerHTML = '<tr><td colspan="5" class="audit-empty">No audit entries for this filter.</td></tr>';
      return;
    }
    body.innerHTML = data.entries.map((entry) => `<tr>
      <td class="audit-time">${esc(formatAuditWhen(entry.ts))}</td>
      <td><span class="audit-action" data-tone="${esc((entry.action || "").split(".")[0])}">${esc(AUDIT_LABELS[entry.action] || entry.action)}</span><code>${esc(entry.action)}</code></td>
      <td>${esc(entry.target || "—")}</td>
      <td>${esc(entry.actor || "—")}</td>
      <td class="audit-detail">${esc(entry.detail || "")}<span class="audit-ip">${esc(entry.ip || "")}</span></td>
    </tr>`).join("");
  } catch (error) {
    if (body) body.innerHTML = `<tr><td colspan="5" class="audit-empty">${esc(error.message)}</td></tr>`;
  } finally {
    auditLoading = false;
  }
}

async function exportAudit() {
  const filter = document.getElementById("auditFilter")?.value || "";
  try {
    const response = await fetch(`/api/audit.csv?category=${encodeURIComponent(filter)}`, { credentials: "same-origin" });
    if (!response.ok) throw new Error("Export failed");
    const blob = await response.blob();
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = `a8m-audit-${new Date().toISOString().slice(0, 10)}.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    showToast("Audit CSV exported");
  } catch (error) { showError(error); }
}

function updateServiceWorkerUi() {
  const status = document.getElementById("swStatus");
  const chip = document.getElementById("swChip");
  if (!status) return;
  if (!("serviceWorker" in navigator)) {
    status.textContent = "Service workers are not supported by this browser.";
    if (chip) chip.textContent = "Unavailable";
    return;
  }
  navigator.serviceWorker.getRegistration().then((registration) => {
    if (registration) {
      status.textContent = "App shell cached · installable on this device.";
      if (chip) chip.textContent = "Active";
    } else {
      status.textContent = "Registration pending; reload after the first visit.";
      if (chip) chip.textContent = "Registering";
    }
  }).catch(() => {
    status.textContent = "Service worker registration failed.";
    if (chip) chip.textContent = "Error";
  });
  const install = document.getElementById("installApp");
  if (install) install.hidden = !deferredInstallPrompt;
}

async function resetDemoPreferences() {
  if (state.user && state.csrf) {
    try {
      const data = await api("/api/preferences/reset", { method: "POST", body: {} });
      state.preferences = { ...DEFAULT_PREFERENCES, ...(data.preferences || {}) };
      if (state.user) state.user.preferences = state.preferences;
      applyServerPreferences();
      syncPreferencesUi();
      state.contactDept = "All";
      state.settingsTab = "appearance";
      showSettingsTab("appearance");
      rerenderAfterPreferenceChange();
      if (state.task === "people") renderPeople();
      showToast("Preferences reset on the server");
      return;
    } catch (error) {
      if (error.status !== 404) { showError(error); return; }
      // Standalone reference server: fall through to its local cache.
    }
  }
  prefs.clear();
  state.theme = "system";
  state.accent = "blue";
  state.density = "comfortable";
  state.externalWarnings = true;
  state.contactDept = "All";
  state.settingsTab = "appearance";
  state.defaultPriority = "normal";
  state.undoSendSeconds = 8;
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.style.setProperty("--accent", ACCENTS.blue.value);
  setDensity("comfortable", false);
  setExternalWarningsUi(true);
  loadPreferences();
  syncThemeUI();
  setAccent("blue", false);
  showSettingsTab("appearance");
  if (state.task === "people") renderPeople();
  showToast("Preferences reset");
}

function setExternalWarningsUi(value) {
  state.externalWarnings = !!value;
  const toggle = document.getElementById("externalWarningToggle");
  if (toggle) {
    toggle.setAttribute("aria-checked", value ? "true" : "false");
    toggle.disabled = !tenantExternalTag();
    toggle.title = tenantExternalTag() ? "" : "External sender warnings are disabled by your organization";
  }
  prefs.set("externalWarnings", value ? "1" : "0");
  renderReader({ preserveScroll: true });
}

async function resyncMailbox() {
  if (!(await appConfirm({ title: "Refresh mailbox?", message: "Alt8Mail will ask Dovecot for new messages. Open compose windows are kept.", confirmLabel: "Refresh" }))) return;
  try {
    const result = await api("/api/mail/sync", { method: "POST", body: {} });
    await refreshMailbox({ quiet: true });
    const imported = result && result.sync ? result.sync.imported || 0 : 0;
    const removed = result && result.sync ? result.sync.removed || 0 : 0;
    showToast(`Mailbox synced from Dovecot · ${imported} new · ${removed} removed`);
  } catch (error) { showError(error); }
}

async function resetDemoData() {
  if (!(await appConfirm({ title: "Reset demo data?", message: "The demo mailbox, rules, drafts, send queue and audit log will return to the seeded state.", confirmLabel: "Reset demo", tone: "danger" }))) return;
  try {
    await api("/api/dev/reset", { method: "POST", body: {} });
    composeSessions.slice().forEach((session) => removeComposeSessionUi(session.id));
    composeSessions = [];
    editingRuleId = null;
    await loadBootstrap({ restoreDrafts: true });
    state.selectedThreadId = null;
    state.selectedIds.clear();
    state.folder = "inbox";
    state.filter = "all";
    state.query = "";
    searchInput.value = "";
    updateSearchClear();
    setTask("mail");
    showToast("Demo data reset");
  } catch (error) { showError(error); }
}

function handleDataAction() {
  return state.mailboxMode === "imap" ? resyncMailbox() : resetDemoData();
}

/* ============================================================
   tasks
   ============================================================ */
const TASK_FILTERS = [
  { id: "all", label: "All" },
  { id: "mine", label: "My tasks" },
  { id: "team", label: "Team" },
  { id: "today", label: "Due today" },
  { id: "overdue", label: "Overdue" },
  { id: "completed", label: "Completed" }
];

function taskIsMine(task) {
  return normalizeEmail(task.assignee && task.assignee.email) === normalizeEmail(state.user && state.user.email);
}

function taskDueState(task) {
  const today = dateKeyInZone(new Date());
  if (!task.due) return "none";
  if (task.status === "done") return "done";
  if (task.due < today) return "overdue";
  if (task.due === today) return "today";
  return "upcoming";
}

function taskMatchesFilter(task) {
  const filter = state.taskFilter;
  if (filter === "mine" && !taskIsMine(task)) return false;
  if (filter === "team" && taskIsMine(task)) return false;
  if (filter === "today" && (task.status === "done" || task.due !== dateKeyInZone(new Date()))) return false;
  if (filter === "overdue" && taskDueState(task) !== "overdue") return false;
  if (filter === "completed" && task.status !== "done") return false;
  const query = state.query.toLowerCase();
  if (query) {
    const haystack = [task.title, task.notes, task.linkedSubject, ...(task.tags || []), task.assignee && task.assignee.name].join(" ").toLowerCase();
    if (!haystack.includes(query)) return false;
  }
  return true;
}

function taskPriorityLabel(priority) {
  return { low: "Low", normal: "Normal", high: "High", urgent: "Urgent" }[priority] || "Normal";
}

function taskDueLabel(task) {
  if (!task.due) return "No due date";
  const date = formatDateOnly(task.due);
  if (!task.dueTime) return date;
  const stamp = zonedWallTimeToUtc(task.due, task.dueTime);
  return stamp ? `${date} · ${formatClock(stamp)}` : date;
}

function renderTasks() {
  if (!tasksView || !taskList) return;
  const filtered = state.tasks
    .filter(taskMatchesFilter)
    .sort((a, b) => {
      if (a.status !== b.status) return a.status === "done" ? 1 : -1;
      if (a.due && b.due && a.due !== b.due) return a.due.localeCompare(b.due);
      if (a.due && !b.due) return -1;
      if (!a.due && b.due) return 1;
      const weight = { urgent: 0, high: 1, normal: 2, low: 3 };
      return (weight[a.priority] ?? 2) - (weight[b.priority] ?? 2);
    });
  if (!state.editingTaskId && !filtered.some((task) => task.id === state.selectedTaskId)) {
    state.selectedTaskId = filtered[0] ? filtered[0].id : null;
  }
  const open = state.tasks.filter((task) => task.status === "open").length;
  const done = state.tasks.length - open;
  const today = dateKeyInZone(new Date());
  const dueToday = state.tasks.filter((task) => task.status === "open" && task.due === today).length;
  const overdue = state.tasks.filter((task) => taskDueState(task) === "overdue").length;
  tasksMeta.textContent = `${open} open · ${dueToday} due today · ${overdue} overdue`;
  taskStats.innerHTML = `
    <button class="task-stat" data-task-filter="all" type="button"><span class="task-stat-value">${state.tasks.length}</span><span class="task-stat-label">All tasks</span><span class="task-stat-hint">${done} completed</span></button>
    <button class="task-stat is-accent" data-task-filter="today" type="button"><span class="task-stat-value">${dueToday}</span><span class="task-stat-label">Due today</span><span class="task-stat-hint">Plan the day</span></button>
    <button class="task-stat${overdue ? " is-danger" : ""}" data-task-filter="overdue" type="button"><span class="task-stat-value">${overdue}</span><span class="task-stat-label">Overdue</span><span class="task-stat-hint">${overdue ? "Needs attention" : "All clear"}</span></button>
    <button class="task-stat" data-task-filter="completed" type="button"><span class="task-stat-value">${done}</span><span class="task-stat-label">Completed</span><span class="task-stat-hint">Workspace total</span></button>`;
  taskFilters.innerHTML = TASK_FILTERS.map((filter) => {
    const count = state.tasks.filter((task) => {
      const previous = state.taskFilter;
      state.taskFilter = filter.id;
      const matched = taskMatchesFilter(task);
      state.taskFilter = previous;
      return matched;
    }).length;
    return `<button class="chip${state.taskFilter === filter.id ? " is-active" : ""}" data-task-filter="${filter.id}" type="button">${filter.label}${count ? `<span class="chip-count">${count}</span>` : ""}</button>`;
  }).join("");
  taskList.innerHTML = filtered.length ? filtered.map((task) => {
    const dueState = taskDueState(task);
    const selected = task.id === state.selectedTaskId;
    return `<div class="task-row${selected ? " is-selected" : ""}${task.status === "done" ? " is-done" : ""}" data-task-id="${esc(task.id)}" tabindex="0" role="button" aria-pressed="${selected}">
      <button class="task-check${task.status === "done" ? " is-done" : ""}" data-task-toggle="${esc(task.id)}" type="button" aria-label="${task.status === "done" ? "Reopen task" : "Mark task complete"}" aria-pressed="${task.status === "done"}"><svg class="icon" aria-hidden="true"><use href="#i-check"/></svg></button>
      <div class="task-row-main">
        <div class="task-row-title">${esc(task.title)}</div>
        <div class="task-row-meta">
          <span class="task-priority ${esc(task.priority)}">${esc(taskPriorityLabel(task.priority))}</span>
          <span class="task-due ${dueState}">${dueState === "overdue" ? "Overdue · " : ""}${esc(taskDueLabel(task))}</span>
          ${task.linkedThreadId ? '<span class="task-linked"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg>Email</span>' : ""}
        </div>
        ${(task.tags || []).length ? `<div class="task-tags">${task.tags.map((tag) => `<span>${esc(tag)}</span>`).join("")}</div>` : ""}
      </div>
      ${avatar(task.assignee, "sm")}
    </div>`;
  }).join("") : `<div class="task-empty"><svg class="icon" aria-hidden="true"><use href="#i-task"/></svg><strong>No tasks here</strong><span>${state.query ? "Try a different search." : "Create a task or change the filter."}</span></div>`;
  renderTaskDetail();
  syncStatusRange();
  syncStatusContext();
}

function renderTaskDetail() {
  if (!taskDetail) return;
  if (state.editingTaskId) {
    const task = state.tasks.find((item) => item.id === state.editingTaskId) || null;
    const editing = Boolean(task);
    const assigneeOptions = [{ name: state.user.name, email: state.user.email, initials: state.user.initials, color: state.user.color }, ...state.people]
      .map((person) => `<option value="${esc(person.email)}"${task && task.assignee && normalizeEmail(task.assignee.email) === normalizeEmail(person.email) ? " selected" : ""}>${esc(person.name)}</option>`).join("");
    taskDetail.innerHTML = `<form class="task-editor" id="taskEditorForm">
      <div class="task-detail-head"><button class="icon-btn task-detail-back" data-task-back type="button" aria-label="Back to tasks"><svg class="icon" aria-hidden="true"><use href="#i-arrow-left"/></svg></button><div><span class="eyebrow">${editing ? "Edit task" : "New task"}</span><h2>${editing ? "Update the details" : "Plan the work"}</h2></div><button class="icon-btn" data-task-editor-close type="button" aria-label="Close editor"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
      <label class="form-field"><span>Title</span><input id="taskTitleInput" type="text" value="${esc(task ? task.title : "")}" placeholder="What needs to happen?" maxlength="200" required></label>
      <label class="form-field"><span>Notes</span><textarea id="taskNotesInput" rows="4" placeholder="Context, links or acceptance criteria">${esc(task ? task.notes : "")}</textarea></label>
      <div class="form-row">
        <label class="form-field"><span>Due date</span><input id="taskDueInput" type="date" value="${esc(task ? task.due : "")}"></label>
        <label class="form-field"><span>Due time</span><input id="taskDueTimeInput" type="time" value="${esc(task ? task.dueTime : "")}"></label>
        <label class="form-field"><span>Priority</span><select id="taskPriorityInput">${["low", "normal", "high", "urgent"].map((value) => `<option value="${value}"${task && task.priority === value ? " selected" : ""}>${taskPriorityLabel(value)}</option>`).join("")}</select></label>
      </div>
      <label class="form-field"><span>Assignee</span><select id="taskAssigneeInput">${assigneeOptions}</select></label>
      <label class="form-field"><span>Tags</span><input id="taskTagsInput" type="text" value="${esc(task ? (task.tags || []).join(", ") : "")}" placeholder="Finance, Board"></label>
      ${task && task.linkedThreadId ? `<div class="task-linked-card"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg><div><strong>Linked email</strong><span>${esc(task.linkedSubject || "Open conversation")}</span></div></div>` : ""}
      <div class="task-editor-actions"><button class="btn-secondary" data-task-editor-close type="button">Cancel</button><button class="btn-primary" id="saveTaskBtn" type="submit">${editing ? "Save changes" : "Create task"}</button></div>
    </form>`;
    setTimeout(() => document.getElementById("taskTitleInput")?.focus(), 40);
    return;
  }
  const task = state.tasks.find((item) => item.id === state.selectedTaskId);
  if (!task) {
    const open = state.tasks.filter((item) => item.status === "open").length;
    taskDetail.innerHTML = `<div class="task-detail-empty"><span class="task-detail-mark"><svg class="icon" aria-hidden="true"><use href="#i-task"/></svg></span><h2>Your work, in one place</h2><p>Select a task to see its notes, due date and linked email. ${open} open task${open === 1 ? "" : "s"} in the workspace.</p><button class="btn-primary" data-new-task type="button"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Create a task</button><div class="task-detail-tips"><span><kbd>Enter</kbd> open</span><span><kbd>C</kbd> compose from email</span><span>Linked tasks keep the conversation attached.</span></div></div>`;
    return;
  }
  const dueState = taskDueState(task);
  taskDetail.innerHTML = `<div class="task-detail-head">
      <button class="icon-btn task-detail-back" data-task-back type="button" aria-label="Back to tasks"><svg class="icon" aria-hidden="true"><use href="#i-arrow-left"/></svg></button>
      <div><span class="eyebrow">Task</span><div class="task-title-line"><h2>${esc(task.title)}</h2><span class="task-status ${task.status}">${task.status === "done" ? "Completed" : "Open"}</span></div></div>
      <button class="icon-btn" data-task-edit="${esc(task.id)}" type="button" aria-label="Edit task"><svg class="icon" aria-hidden="true"><use href="#i-compose"/></svg></button>
    </div>
    <div class="task-detail-badges"><span class="task-priority ${esc(task.priority)}">${esc(taskPriorityLabel(task.priority))}</span><span class="task-due ${dueState}">${esc(taskDueLabel(task))}</span>${(task.tags || []).map((tag) => `<span class="task-tag">${esc(tag)}</span>`).join("")}</div>
    <div class="task-detail-grid">
      <div><span>Assignee</span><strong>${esc(task.assignee ? task.assignee.name : "Unassigned")}</strong></div>
      <div><span>Created</span><strong>${esc(formatDateOnly(task.createdAt))}</strong></div>
      <div><span>Source</span><strong>${task.source === "email" ? "Email follow-up" : "Manual task"}</strong></div>
      <div><span>Completed</span><strong>${task.completedAt ? esc(formatDateOnly(task.completedAt)) : "—"}</strong></div>
    </div>
    <div class="task-notes"><h3>Notes</h3><p>${esc(task.notes || "No notes yet.").replace(/\n/g, "<br>")}</p></div>
    ${task.linkedThreadId ? `<button class="task-linked-card" data-task-open-thread="${esc(task.linkedThreadId)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg><div><strong>Linked email</strong><span>${esc(task.linkedSubject || "Open conversation")}</span></div><svg class="icon" aria-hidden="true"><use href="#i-chevron-right"/></svg></button>` : ""}
    <div class="task-detail-actions">
      <button class="btn-primary" data-task-toggle="${esc(task.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>${task.status === "done" ? "Reopen task" : "Mark complete"}</button>
      <button class="btn-secondary" data-task-edit="${esc(task.id)}" type="button">Edit</button>
      <button class="btn-secondary danger-btn" data-task-delete="${esc(task.id)}" type="button">Delete</button>
    </div>`;
}

async function saveTaskForm(event) {
  event.preventDefault();
  const title = document.getElementById("taskTitleInput").value.trim();
  if (!title) { showToast("Give the task a title"); document.getElementById("taskTitleInput").focus(); return; }
  const payload = {
    title,
    notes: document.getElementById("taskNotesInput").value,
    due: document.getElementById("taskDueInput").value,
    dueTime: document.getElementById("taskDueTimeInput").value,
    priority: document.getElementById("taskPriorityInput").value,
    assigneeEmail: document.getElementById("taskAssigneeInput").value,
    tags: document.getElementById("taskTagsInput").value.split(",").map((tag) => tag.trim()).filter(Boolean)
  };
  try {
    if (state.editingTaskId && state.editingTaskId !== "new") {
      const data = await api(`/api/tasks/${encodeURIComponent(state.editingTaskId)}`, { method: "PATCH", body: payload });
      const index = state.tasks.findIndex((task) => task.id === data.task.id);
      if (index >= 0) state.tasks[index] = data.task;
      state.selectedTaskId = data.task.id;
      showToast("Task updated");
    } else {
      const data = await api("/api/tasks", { method: "POST", body: payload });
      state.tasks.unshift(data.task);
      state.selectedTaskId = data.task.id;
      showToast("Task created");
    }
    state.editingTaskId = null;
    renderTasks();
  } catch (error) {
    showError(error);
  }
}

async function toggleTask(taskId) {
  const task = state.tasks.find((item) => item.id === taskId);
  if (!task) return;
  const status = task.status === "done" ? "open" : "done";
  try {
    const data = await api(`/api/tasks/${encodeURIComponent(taskId)}`, { method: "PATCH", body: { status } });
    const index = state.tasks.findIndex((item) => item.id === taskId);
    if (index >= 0) state.tasks[index] = data.task;
    showToast(status === "done" ? "Task completed" : "Task reopened");
    renderTasks();
  } catch (error) {
    showError(error);
  }
}

async function deleteTask(taskId) {
  const task = state.tasks.find((item) => item.id === taskId);
  if (!task) return;
  if (!(await appConfirm({ title: "Delete this task?", message: `“${task.title}” will be removed from the task workspace.`, confirmLabel: "Delete task", tone: "danger" }))) return;
  try {
    await api(`/api/tasks/${encodeURIComponent(taskId)}`, { method: "DELETE" });
    state.tasks = state.tasks.filter((item) => item.id !== taskId);
    state.selectedTaskId = null;
    state.editingTaskId = null;
    showToast("Task deleted");
    renderTasks();
  } catch (error) {
    showError(error);
  }
}

async function refreshTasks() {
  try {
    const data = await api("/api/tasks");
    state.tasks = data.tasks || [];
    renderTasks();
    showToast("Tasks refreshed");
  } catch (error) {
    showError(error);
  }
}

function openTaskFromEmail(threadId) {
  const thread = threadById(threadId);
  if (!thread) { showToast("The linked conversation is no longer available"); return; }
  const message = thread.messages.find((item) => item.folder !== "sent" && item.folder !== "drafts") || thread.latest;
  const selection = selectionIdForMessage(message);
  state.selectedThreadId = selection;
  state.folder = FOLDER_LABELS[message.folder] ? message.folder : "inbox";
  state.filter = "all";
  setTask("mail");
  state.selectedThreadId = selection;
  renderList();
  renderReader();
  syncSelectionUi();
  if (isMobile()) document.body.classList.add("reader-open");
}

/* ============================================================
   files + storage
   ============================================================ */
const FILE_FILTERS = [
  { id: "all", label: "All items" },
  { id: "recent", label: "Recent" },
  { id: "shared", label: "Shared" },
  { id: "images", label: "Images" },
  { id: "documents", label: "Documents" },
  { id: "starred", label: "Starred" }
];

function storageProvider(providerId) {
  return state.storageProviders.find((provider) => provider.id === providerId) || null;
}

function storageConnected(providerId) {
  const provider = storageProvider(providerId);
  return Boolean(provider && provider.connected);
}

function fileMatchesFilter(file) {
  if (!storageConnected(file.providerId)) return false;
  if (state.fileProvider !== "all" && file.providerId !== state.fileProvider) return false;
  if (state.fileFilter === "recent") {
    const modified = new Date(file.modifiedAt);
    if (Number.isNaN(modified.getTime()) || Date.now() - modified.getTime() > 7 * 86400000) return false;
  }
  if (state.fileFilter === "shared" && !(file.sharedWith || []).length) return false;
  if (state.fileFilter === "images" && !String(file.mime || "").startsWith("image/")) return false;
  if (state.fileFilter === "documents" && !["PDF", "DOC", "XLS", "PPT", "TXT"].includes(file.type)) return false;
  if (state.fileFilter === "starred" && !file.starred) return false;
  const query = state.query.toLowerCase();
  if (query) {
    const haystack = [file.name, file.path, file.description, file.owner && file.owner.name, file.type].join(" ").toLowerCase();
    if (!haystack.includes(query)) return false;
  }
  return true;
}

function fileTypeClass(file) {
  return String(file.type || "FILE").toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 5);
}

function renderFiles() {
  if (!filesView || !fileList) return;
  const connected = state.storageProviders.filter((provider) => provider.connected);
  const available = state.files.filter((file) => storageConnected(file.providerId));
  const filtered = available.filter(fileMatchesFilter).sort((a, b) => Date.parse(b.modifiedAt) - Date.parse(a.modifiedAt));
  if (!filtered.some((file) => file.id === state.selectedFileId)) state.selectedFileId = filtered[0] ? filtered[0].id : null;
  const used = connected.reduce((sum, provider) => sum + (Number(provider.usedBytes) || 0), 0);
  const limit = connected.reduce((sum, provider) => sum + (Number(provider.limitBytes) || 0), 0);
  filesMeta.textContent = `${available.length} items · ${connected.length} connected drive${connected.length === 1 ? "" : "s"} · ${formatBytes(used)}${limit ? ` of ${formatBytes(limit)}` : ""}`;
  storageStrip.innerHTML = state.storageProviders.map((provider) => {
    const percent = provider.limitBytes ? Math.min(100, (provider.usedBytes / provider.limitBytes) * 100) : 0;
    return `<article class="storage-card${provider.connected ? " is-connected" : ""}" style="--provider:${provider.color}">
      <div class="storage-card-head">
        ${storageProviderMarkup(provider)}
        <div class="storage-card-name"><strong>${esc(provider.name)}</strong><span>${provider.connected ? esc(provider.account || "Connected") : provider.kind === "internal" ? "Internal storage" : "Available to connect"}</span></div>
        ${provider.connected ? '<span class="storage-status"><span class="status-dot"></span>Synced</span>' : `<button class="link-btn" data-storage-connect="${esc(provider.id)}" type="button">Connect</button>`}
      </div>
      ${provider.connected ? `<div class="storage-usage">${provider.limitBytes ? `<div class="usage-bar"><span style="width:${percent}%"></span></div><div class="storage-usage-meta"><span>${formatBytes(provider.usedBytes)} of ${formatBytes(provider.limitBytes)}</span><span>${Math.round(percent)}%</span></div>` : `<div class="storage-usage-meta"><span>${formatBytes(provider.usedBytes)} used</span><span>Connected bucket</span></div>`}</div>` : `<p class="storage-card-note">${esc(provider.description)}</p>`}
    </article>`;
  }).join("");
  const providerOptions = [`<option value="all">All connected drives</option>`, ...connected.map((provider) => `<option value="${esc(provider.id)}"${state.fileProvider === provider.id ? " selected" : ""}>${esc(provider.name)}</option>`)].join("");
  if (fileProviderFilter.innerHTML !== providerOptions) fileProviderFilter.innerHTML = providerOptions;
  if (state.fileProvider !== "all" && !connected.some((provider) => provider.id === state.fileProvider)) state.fileProvider = "all";
  fileProviderFilter.value = state.fileProvider;
  fileFilters.innerHTML = FILE_FILTERS.map((filter) => `<button class="chip${state.fileFilter === filter.id ? " is-active" : ""}" data-file-filter="${filter.id}" type="button">${filter.label}</button>`).join("");
  syncStorageDefaultSelects();
  const manageCredentials = document.getElementById("manageStorageCredentialsBtn");
  if (manageCredentials) manageCredentials.hidden = !storageCanManage();
  fileList.innerHTML = filtered.length ? filtered.map((file) => {
    const provider = storageProvider(file.providerId);
    const selected = file.id === state.selectedFileId;
    return `<div class="file-row${selected ? " is-selected" : ""}" data-file-id="${esc(file.id)}" tabindex="0" role="button" aria-pressed="${selected}">
      <span class="file-type ${fileTypeClass(file)}">${esc(file.type)}</span>
      <div class="file-main"><strong>${esc(file.name)}</strong><span>${esc(file.path)} · ${esc(provider ? provider.name : "Storage")}</span></div>
      <span class="file-owner">${esc(file.owner ? file.owner.name : "—")}</span>
      <span class="file-modified">${esc(formatDateOnly(file.modifiedAt))}</span>
      <span class="file-size">${esc(formatBytes(file.sizeBytes))}</span>
      <span class="file-flags">${(file.sharedWith || []).length ? '<svg class="icon" aria-label="Shared" title="Shared"><use href="#i-people"/></svg>' : ""}${file.locked ? '<svg class="icon" aria-label="Locked" title="Locked"><use href="#i-lock"/></svg>' : ""}</span>
      <button class="star-btn${file.starred ? " is-starred" : ""}" data-file-star="${esc(file.id)}" type="button" aria-label="${file.starred ? "Remove star" : "Star file"}" aria-pressed="${file.starred ? "true" : "false"}"><svg class="icon" aria-hidden="true"><use href="#i-star"/></svg></button>
    </div>`;
  }).join("") : `<div class="file-empty"><svg class="icon" aria-hidden="true"><use href="#i-cabinet"/></svg><strong>Cabinet is empty</strong><span>${state.query ? "Try a different search." : "Save an email attachment, connect a drive or upload a document."}</span></div>`;
  renderFileDetail();
  syncStatusRange();
  syncStatusContext();
}

function renderFileDetail() {
  if (!fileDetail) return;
  const file = state.files.find((item) => item.id === state.selectedFileId);
  if (!file) {
    fileDetail.innerHTML = `<div class="file-detail-empty"><span class="file-detail-mark"><svg class="icon" aria-hidden="true"><use href="#i-cabinet"/></svg></span><h2>Your Cabinet, connected</h2><p>Keep documents, attachments and connected-drive files together in one secure workspace.</p><button class="btn-secondary" data-open-storage type="button"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Add storage</button><div class="file-detail-tips"><span>${state.files.filter((item) => storageConnected(item.providerId)).length} indexed items</span><span>OAuth-ready connectors</span><span>Enterprise sharing controls</span></div></div>`;
    return;
  }
  const provider = storageProvider(file.providerId);
  const image = String(file.mime || "").startsWith("image/") && file.contentUrl;
  const pdf = file.mime === "application/pdf" && file.contentUrl;
  fileDetail.innerHTML = `<div class="file-detail-head"><div><span class="eyebrow">File details</span><h2>${esc(file.name)}</h2></div><button class="icon-btn file-detail-back" data-file-back type="button" aria-label="Back to files"><svg class="icon" aria-hidden="true"><use href="#i-arrow-left"/></svg></button></div>
    <div class="file-preview-card ${fileTypeClass(file)}">
      ${image ? `<img src="${esc(file.contentUrl)}" alt="${esc(file.name)}" loading="lazy">` : pdf ? `<span class="file-preview-pdf"><svg class="icon" aria-hidden="true"><use href="#i-file"/></svg>PDF</span>` : `<span class="file-preview-generic"><svg class="icon" aria-hidden="true"><use href="#i-file"/></svg><strong>${esc(file.type)}</strong></span>`}
      <div class="file-preview-overlay"><button class="btn-secondary" data-file-preview="${esc(file.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-expand"/></svg>${file.previewable ? "Preview" : "Open details"}</button></div>
    </div>
    <div class="file-badges"><span class="provider-badge" style="--provider:${provider ? provider.color : "#64748b"}">${esc(provider ? provider.name : "Storage")}</span>${file.starred ? '<span class="policy-chip">Starred</span>' : ""}${file.locked ? '<span class="policy-chip">Locked</span>' : ""}${(file.sharedWith || []).length ? `<span class="policy-chip">${file.sharedWith.length} shared</span>` : ""}</div>
    <p class="file-description">${esc(file.description || "No description.")}</p>
    <div class="file-meta-grid">
      <div><span>Owner</span><strong>${esc(file.owner ? file.owner.name : "—")}</strong></div>
      <div><span>Modified</span><strong>${esc(formatDateOnly(file.modifiedAt))}</strong></div>
      <div><span>Size</span><strong>${esc(formatBytes(file.sizeBytes))}</strong></div>
      <div><span>Version</span><strong>${esc(file.version || "1")}</strong></div>
    </div>
    <div class="file-path"><svg class="icon" aria-hidden="true"><use href="#i-folder"/></svg>${esc(file.path)}</div>
    <div class="file-detail-actions">
      <button class="btn-primary" data-file-preview="${esc(file.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-expand"/></svg>Preview</button>
      <button class="btn-secondary" data-file-download="${esc(file.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-download"/></svg>Download</button>
      <button class="btn-secondary" data-file-share="${esc(file.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg>Share</button>
      <button class="btn-secondary" data-file-star="${esc(file.id)}" type="button">${file.starred ? "Unstar" : "Star"}</button>
      <button class="btn-secondary danger-btn" data-file-delete="${esc(file.id)}" type="button">Delete</button>
    </div>
    ${(file.sharedWith || []).length ? `<div class="file-shared-list"><h3>Shared with</h3>${file.sharedWith.map((email) => `<span>${avatar({ name: email.split("@")[0], email, initials: initialsFrom(email.split("@")[0]), color: colorForString(email) }, "sm")}${esc(email)}</span>`).join("")}</div>` : ""}`;
}

function colorForString(value) {
  const palette = ["#34557a", "#3d6b62", "#5b4a7e", "#7a4a5c", "#7a5a3a", "#3f6570", "#476b4f", "#4f5673"];
  let hash = 0;
  for (const char of String(value)) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length];
}

function storageCanManage() {
  return Boolean(state.user && (state.user.isSystemAdmin === true || ["owner", "admin"].includes(String(state.user.role || "").toLowerCase())));
}

function storageDefaultId() {
  const current = state.storageProviders.find((provider) => provider.id === state.defaultStorage && provider.connected);
  return current ? current.id : "a8m";
}

function storageDefaultOptions() {
  return state.storageProviders
    .filter((provider) => provider.connected)
    .map((provider) => `<option value="${esc(provider.id)}">${esc(provider.name)}</option>`)
    .join("");
}

function syncStorageDefaultSelects() {
  const options = storageDefaultOptions();
  const selected = storageDefaultId();
  for (const id of ["defaultStorageSelect", "settingsDefaultStorage"]) {
    const select = document.getElementById(id);
    if (!select) continue;
    if (select.innerHTML !== options) select.innerHTML = options;
    select.value = selected;
  }
}

async function setDefaultStorage(providerId) {
  try {
    const data = await api("/api/storage/default", { method: "PUT", body: { providerId } });
    state.defaultStorage = data.defaultProvider || providerId;
    syncStorageDefaultSelects();
    showToast(`${storageProvider(state.defaultStorage)?.name || "Storage"} is now the default`);
  } catch (error) {
    showError(error);
    syncStorageDefaultSelects();
  }
}

function storageProviderMarkup(provider) {
  if (provider.id === "a8m") {
    return `<span class="storage-provider-mark"><svg class="icon" aria-hidden="true"><use href="#i-drive-cloud"/></svg></span>`;
  }
  return `<span class="storage-provider-mark" style="--provider:${provider.color}">${esc(provider.name.split(" ").map((part) => part[0]).join("").slice(0, 2))}</span>`;
}

function renderStorageSettings() {
  const list = document.getElementById("settingsStorageList");
  if (!list) return;
  syncStorageDefaultSelects();
  list.innerHTML = state.storageProviders.map((provider) => `<div class="settings-storage-row">
    ${storageProviderMarkup(provider)}
    <div class="settings-storage-copy"><strong>${esc(provider.name)}</strong><span>${provider.connected ? `Connected${provider.account ? ` as ${esc(provider.account)}` : ""}` : provider.configured ? "Ready to connect" : "Credentials not configured"}</span></div>
    ${provider.connected ? `<span class="storage-status"><span class="status-dot"></span>${provider.id === state.defaultStorage ? "Default" : "Connected"}</span>` : ""}
    <div class="settings-storage-actions">
      ${provider.kind !== "internal" && !provider.configured ? `<button class="link-btn" data-storage-configure="${esc(provider.id)}" type="button">Configure</button>` : ""}
      ${provider.kind !== "internal" && provider.configured && !provider.connected ? `<button class="link-btn" data-storage-connect="${esc(provider.id)}" type="button">Connect</button>` : ""}
      ${provider.connected && provider.kind !== "internal" ? `<button class="link-btn" data-storage-sync="${esc(provider.id)}" type="button">Sync</button><button class="link-btn" data-storage-disconnect="${esc(provider.id)}" type="button">Disconnect</button>` : ""}
    </div>
  </div>`).join("");
  if (!storageCanManage()) {
    const manage = document.getElementById("settingsManageStorageBtn");
    if (manage) manage.hidden = true;
  }
}

async function refreshStorageProviders() {
  const data = await api("/api/storage");
  state.storageProviders = data.providers || state.storageProviders;
  if (data.defaultProvider) state.defaultStorage = data.defaultProvider;
  syncStorageDefaultSelects();
  if (state.task === "files") renderFiles();
  if (state.task === "settings" && state.settingsTab === "storage") renderStorageSettings();
}

function credentialProvider() {
  return state.storageCredentialData.find((provider) => provider.id === state.storageCredentialProvider) || state.storageCredentialData[0] || null;
}

function renderStorageCredentials() {
  if (!storageCredentialsTabs || !storageCredentialFields) return;
  storageCredentialsTabs.innerHTML = state.storageCredentialData.map((provider) => `<button class="storage-credentials-tab${provider.id === state.storageCredentialProvider ? " is-active" : ""}" data-credential-provider="${esc(provider.id)}" type="button" role="tab" aria-selected="${provider.id === state.storageCredentialProvider ? "true" : "false"}"><span>${esc(provider.name)}</span><span class="credential-state ${provider.configured ? "is-set" : ""}">${provider.configured ? "Configured" : "Not configured"}</span></button>`).join("");
  const provider = credentialProvider();
  if (!provider) {
    storageCredentialFields.innerHTML = `<div class="settings-note">No external storage providers are available.</div>`;
    return;
  }
  if (provider.kind === "s3") {
    storageCredentialFields.innerHTML = `
      <div class="credential-callout"><strong>S3 / MinIO bucket</strong><span>The bucket must already exist and allow the access key to list, read, write and delete objects.</span></div>
      <div class="credential-grid">
        <label class="form-field"><span>Endpoint <small>optional</small></span><input data-credential-field="endpoint" value="${esc(provider.endpoint || "")}" placeholder="https://s3.example.com"></label>
        <label class="form-field"><span>Region</span><input data-credential-field="region" value="${esc(provider.region || "us-east-1")}" placeholder="us-east-1"></label>
        <label class="form-field"><span>Bucket</span><input data-credential-field="bucket" value="${esc(provider.bucket || "")}" placeholder="alt8mail-cabinet"></label>
        <label class="form-field"><span>Access key</span><input data-credential-field="accessKey" value="${esc(provider.accessKey || "")}" autocomplete="off"></label>
        <label class="form-field"><span>Secret key</span><input data-credential-field="secretKey" type="password" placeholder="${provider.hasSecret ? "Leave blank to keep the saved secret" : "Enter the secret key"}" autocomplete="new-password"></label>
      </div>
      <div class="credential-checks"><label class="compose-check"><input type="checkbox" data-credential-field="useSSL" ${provider.useSSL !== false ? "checked" : ""}>Use TLS</label><label class="compose-check"><input type="checkbox" data-credential-field="pathStyle" ${provider.pathStyle !== false ? "checked" : ""}>Path-style addressing</label></div>`;
  } else {
    storageCredentialFields.innerHTML = `
      <div class="credential-callout"><strong>OAuth redirect URI</strong><span>Register this exact callback in the provider console before connecting.</span><div class="credential-uri"><code>${esc(provider.redirectUri || "")}</code><button class="link-btn" data-copy-credential-uri="${esc(provider.redirectUri || "")}" type="button">Copy</button></div></div>
      <div class="credential-grid">
        <label class="form-field"><span>Client ID</span><input data-credential-field="clientId" value="${esc(provider.clientId || "")}" autocomplete="off" spellcheck="false"></label>
        <label class="form-field"><span>Client secret</span><input data-credential-field="clientSecret" type="password" placeholder="${provider.hasClientSecret ? "Leave blank to keep the saved secret" : "Enter the client secret"}" autocomplete="new-password"></label>
      </div>`;
  }
  storageCredentialStatus.textContent = provider.configured
    ? `Configured${provider.source === "environment" ? " from the server environment" : ""}. Saving replaces the tenant configuration.`
    : "Not configured yet. Save credentials, then connect from the Cabinet.";
  if (storageCredentialsOverlay) storageCredentialsOverlay.hidden = false;
}

async function openStorageCredentials(providerId = "") {
  if (!storageCanManage()) {
    showToast("Only organization owners and administrators can manage storage credentials");
    return;
  }
  try {
    const data = await api("/api/storage/credentials");
    state.storageCredentialData = data.providers || [];
    state.storageCredentialProvider = providerId || state.storageCredentialData[0]?.id || "";
    renderStorageCredentials();
  } catch (error) {
    showError(error);
  }
}

function closeStorageCredentials() {
  if (storageCredentialsOverlay) storageCredentialsOverlay.hidden = true;
}

async function saveStorageCredentials() {
  const provider = credentialProvider();
  if (!provider) return;
  const body = {};
  for (const input of storageCredentialFields.querySelectorAll("[data-credential-field]")) {
    const key = input.getAttribute("data-credential-field");
    if (input.type === "checkbox") body[key] = input.checked;
    else body[key] = input.value.trim();
  }
  const button = document.getElementById("storageCredentialsSave");
  if (button) { button.disabled = true; button.textContent = "Saving…"; }
  try {
    const data = await api(`/api/storage/credentials/${encodeURIComponent(provider.id)}`, { method: "PUT", body });
    const index = state.storageCredentialData.findIndex((item) => item.id === provider.id);
    if (index >= 0) state.storageCredentialData[index] = data.provider;
    await refreshStorageProviders();
    renderStorageCredentials();
    showToast(`${provider.name} credentials saved`);
  } catch (error) {
    showError(error);
  } finally {
    if (button) { button.disabled = false; button.textContent = "Save credentials"; }
  }
}

async function clearStorageCredentials() {
  const provider = credentialProvider();
  if (!provider) return;
  const ok = await appConfirm({
    title: "Clear saved credentials?",
    message: `The ${provider.name} connector will stop working for this organization until credentials are configured again.`,
    confirmLabel: "Clear credentials",
    tone: "danger"
  });
  if (!ok) return;
  try {
    await api(`/api/storage/credentials/${encodeURIComponent(provider.id)}`, { method: "DELETE" });
    const index = state.storageCredentialData.findIndex((item) => item.id === provider.id);
    if (index >= 0) state.storageCredentialData[index] = { ...provider, configured: false, clientId: "", hasClientSecret: false, hasSecret: false, source: "none" };
    await refreshStorageProviders();
    renderStorageCredentials();
    showToast("Storage credentials cleared");
  } catch (error) { showError(error); }
}

function openStorageDialog() {
  storageConnectList.innerHTML = state.storageProviders.map((provider) => `<div class="storage-connect-row" data-provider="${esc(provider.id)}">
    ${storageProviderMarkup(provider)}
    <div class="storage-connect-copy"><strong>${esc(provider.name)}</strong><span>${provider.connected ? `Connected as ${esc(provider.account || state.user.email)}` : provider.configured ? esc(provider.description) : "Credentials are not configured for this connector"}</span></div>
    ${provider.connected ? `<span class="storage-status"><span class="status-dot"></span>Connected</span>${provider.kind !== "internal" ? `<button class="link-btn" data-storage-sync="${esc(provider.id)}" type="button">Sync</button>` : ""}<button class="link-btn" data-storage-disconnect="${esc(provider.id)}" type="button">Disconnect</button>` : provider.kind !== "internal" && !provider.configured ? `<button class="btn-secondary" data-storage-configure="${esc(provider.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-sliders"/></svg>Configure</button>` : `<button class="btn-secondary" data-storage-connect="${esc(provider.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-cloud"/></svg>Connect</button>`}
  </div>`).join("");
  storageOverlay.hidden = false;
}

async function connectStorage(providerId, button) {
  const provider = storageProvider(providerId);
  if (!provider) return;
  if (button) { button.disabled = true; button.innerHTML = '<span class="passkey-spinner" aria-hidden="true"></span>Connecting…'; }
  try {
    const data = await api(`/api/storage/${encodeURIComponent(providerId)}/connect`, { method: "POST", body: {} });
    if (data.authorizationUrl) { window.location.href = data.authorizationUrl; return; }
    const index = state.storageProviders.findIndex((item) => item.id === providerId);
    if (index >= 0) state.storageProviders[index] = data.provider;
    for (const file of data.files || []) if (!state.files.some((item) => item.id === file.id)) state.files.push(file);
    renderFiles();
    syncStorageDefaultSelects();
    openStorageDialog();
    showToast(`${provider.name} connected`);
  } catch (error) {
    showError(error);
    if (button) { button.disabled = false; button.textContent = "Connect"; }
  }
}

async function disconnectStorage(providerId) {
  const provider = storageProvider(providerId);
  if (!provider) return;
  if (!(await appConfirm({ title: `Disconnect ${provider.name}?`, message: "Its files will be hidden from the Cabinet until you reconnect the drive.", confirmLabel: "Disconnect", tone: "danger" }))) return;
  try {
    const data = await api(`/api/storage/${encodeURIComponent(providerId)}/disconnect`, { method: "POST", body: {} });
    const index = state.storageProviders.findIndex((item) => item.id === providerId);
    if (index >= 0) state.storageProviders[index] = data.provider;
    if (state.fileProvider === providerId) state.fileProvider = "all";
    renderFiles();
    syncStorageDefaultSelects();
    openStorageDialog();
    showToast(`${provider.name} disconnected`);
  } catch (error) {
    showError(error);
  }
}

async function syncStorage(providerId) {
  const provider = storageProvider(providerId);
  if (!provider) return;
  try {
    const data = await api(`/api/storage/${encodeURIComponent(providerId)}/sync`, { method: "POST", body: {} });
    const index = state.storageProviders.findIndex((item) => item.id === providerId);
    if (index >= 0) state.storageProviders[index] = data.provider;
    state.files = state.files.filter((file) => file.providerId !== providerId);
    for (const file of data.files || []) if (!state.files.some((item) => item.id === file.id)) state.files.push(file);
    renderFiles();
    syncStorageDefaultSelects();
    openStorageDialog();
    showToast(`${provider.name} synced`);
  } catch (error) { showError(error); }
}

function openShareDialog(fileId) {
  const file = state.files.find((item) => item.id === fileId);
  if (!file) return;
  fileShareTarget = file;
  shareFileName.textContent = file.name;
  shareResult.hidden = true;
  shareUrl.value = "";
  document.getElementById("createShare").hidden = false;
  shareOverlay.hidden = false;
}

async function createShareLink() {
  if (!fileShareTarget) return;
  const button = document.getElementById("createShare");
  button.disabled = true;
  button.textContent = "Creating…";
  try {
    const data = await api(`/api/files/${encodeURIComponent(fileShareTarget.id)}/share`, {
      method: "POST",
      body: {
        access: document.getElementById("shareAccess").value,
        expiresDays: Number(document.getElementById("shareExpiry").value),
        recipientEmail: (document.getElementById("shareRecipient")?.value || "").trim()
      }
    });
    shareUrl.value = data.share.url;
    shareResult.hidden = false;
    button.hidden = true;
    showToast("Share link created");
    if (!(fileShareTarget.sharedWith || []).includes("External link")) { fileShareTarget.sharedWith = [...(fileShareTarget.sharedWith || []), "External link"]; }
    renderFiles();
  } catch (error) {
    showError(error);
  } finally {
    button.disabled = false;
    button.textContent = "Create link";
  }
}

function previewFileRecord(file) {
  if (!file) return;
  const url = file.contentUrl || (file.id ? `/api/attachments/${encodeURIComponent(file.id)}/content` : "");
  openPreview({
    name: file.name,
    type: file.type,
    mime: file.mime,
    size: file.size || formatBytes(file.sizeBytes),
    contentUrl: url,
    previewable: file.previewable
  });
}

async function toggleFileStar(fileId) {
  const file = state.files.find((item) => item.id === fileId);
  if (!file) return;
  try {
    const data = await api(`/api/files/${encodeURIComponent(fileId)}`, { method: "PATCH", body: { starred: !file.starred } });
    const index = state.files.findIndex((item) => item.id === fileId);
    if (index >= 0) state.files[index] = data.file;
    renderFiles();
    showToast(data.file.starred ? "File starred" : "File unstarred");
  } catch (error) {
    showError(error);
  }
}

async function deleteFile(fileId) {
  const file = state.files.find((item) => item.id === fileId);
  if (!file) return;
  if (!(await appConfirm({ title: "Delete this file?", message: `${file.name} will be removed from the Cabinet index.`, confirmLabel: "Delete file", tone: "danger" }))) return;
  try {
    await api(`/api/files/${encodeURIComponent(fileId)}`, { method: "DELETE" });
    state.files = state.files.filter((item) => item.id !== fileId);
    state.selectedFileId = null;
    renderFiles();
    showToast("File removed");
  } catch (error) {
    showError(error);
  }
}

function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || "").replace(/^data:[^;]+;base64,/, ""));
    reader.onerror = () => reject(reader.error || new Error("Could not read the file"));
    reader.readAsDataURL(file);
  });
}

async function uploadFiles(files) {
  if (uploadBusy || !files || !files.length) return;
  uploadBusy = true;
  const max = state.attachmentMaxBytes || 8 * 1024 * 1024;
  const targetProvider = state.fileProvider !== "all" && storageConnected(state.fileProvider) ? state.fileProvider : "a8m";
  const targetName = targetProvider === "a8m" ? "Alt8Mail Drive" : (storageProvider(targetProvider)?.name || "connected storage");
  let uploaded = 0;
  for (const file of Array.from(files)) {
    if (file.size > max) {
      showToast(`${file.name} is larger than ${formatBytes(max)}`);
      continue;
    }
    try {
      const dataBase64 = await fileToBase64(file);
      const attachment = await api("/api/attachments", {
        method: "POST",
        body: { name: file.name, mime: file.type, dataBase64 }
      });
      const created = await api("/api/files", {
        method: "POST",
        body: { providerId: targetProvider, attachmentId: attachment.attachment.id, path: "Uploads" }
      });
      state.files.unshift(created.file);
      uploaded += 1;
    } catch (error) {
      showError(error);
    }
  }
  uploadBusy = false;
  fileUploadInput.value = "";
  if (uploaded) {
    renderFiles();
    showToast(`${uploaded} file${uploaded === 1 ? "" : "s"} uploaded to ${targetName}`);
  }
}

/* ============================================================
   scheduled outbox
   ============================================================ */
function scheduledItems() {
  return state.pendingSends
    .filter((item) => item.scheduled)
    .sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
}

function scheduledItemById(id) {
  return state.pendingSends.find((item) => item.scheduled && item.id === id) || null;
}

function scheduledRecipients(item) {
  const people = (item.to || []).map((person) => person.name || person.email).filter(Boolean);
  if (!people.length) return "No recipients";
  return people.length > 3 ? `${people.slice(0, 3).join(", ")} +${people.length - 3}` : people.join(", ");
}

function scheduledAliasLabel(item) {
  const alias = state.aliases.find((entry) => normalizeEmail(entry.email) === normalizeEmail(item.from && item.from.email));
  return alias ? alias.label : (item.from && (item.from.name || item.from.email)) || "Alt8Mail";
}

function scheduledAssessment(stamp) {
  const dateKey = dateKeyInZone(stamp);
  const parts = timeZoneParts(stamp);
  const time = `${String(parts.hour).padStart(2, "0")}:${String(parts.minute).padStart(2, "0")}`;
  const workDays = state.preferences.workDays && state.preferences.workDays.length ? state.preferences.workDays : [1, 2, 3, 4, 5];
  const start = state.preferences.workStart || "09:00";
  const end = state.preferences.workEnd || "17:00";
  const workDay = workDays.includes(weekdayOfKey(dateKey));
  const withinHours = time >= start && time < end;
  return {
    workDay,
    withinHours,
    ok: workDay && withinHours,
    label: !workDay ? "Outside working days" : withinHours ? "Within working hours" : "Outside working hours"
  };
}

function scheduledDayLabel(key) {
  const today = dateKeyInZone(new Date());
  if (key === today) return "Today";
  if (key === addDaysToKey(today, 1)) return "Tomorrow";
  const parts = keyToParts(key);
  if (!parts) return key;
  return new Intl.DateTimeFormat(currentLocale(), {
    weekday: "long", month: "long", day: "numeric", timeZone: "UTC"
  }).format(new Date(Date.UTC(parts.year, parts.month - 1, parts.day, 12)));
}

function scheduledDateBlock(stamp) {
  const parts = timeZoneParts(stamp);
  const date = new Date(Date.UTC(parts.year, parts.month - 1, parts.day, 12));
  return {
    day: parts.day,
    weekday: new Intl.DateTimeFormat(currentLocale(), { weekday: "short", timeZone: "UTC" }).format(date),
    month: new Intl.DateTimeFormat(currentLocale(), { month: "short", timeZone: "UTC" }).format(date).toUpperCase(),
    time: formatClock(stamp)
  };
}

function renderScheduled() {
  if (!scheduledView || !scheduledList) return;
  const allItems = scheduledItems();
  const query = state.query.trim().toLowerCase();
  const items = query ? allItems.filter((item) => {
    const haystack = [
      item.subject,
      item.bodyText,
      scheduledRecipients(item),
      scheduledAliasLabel(item),
      ...(item.attachments || []).map((file) => file.name)
    ].join(" ").toLowerCase();
    return haystack.includes(query);
  }) : allItems;
  const next = items[0];
  const withFiles = items.filter((item) => (item.attachments || []).length).length;
  const withinHours = items.filter((item) => scheduledAssessment(item.sendAt).ok).length;
  scheduledMeta.textContent = items.length
    ? `${items.length}${query ? ` of ${allItems.length}` : ""} waiting · ${formatRelativeTime(next.sendAt)}`
    : query ? `No matches in ${allItems.length} scheduled message${allItems.length === 1 ? "" : "s"}` : "No messages waiting";
  scheduledZoneLabel.textContent = formatTimeZoneLabel();
  scheduledQueueNote.textContent = items.length
    ? `Next message leaves the outbox ${formatRelativeTime(next.sendAt)}. All times are shown in ${currentTimeZone().replace(/_/g, " ")}.`
    : `Messages you schedule from compose appear here, held safely in the server outbox. Times use ${currentTimeZone().replace(/_/g, " ")}.`;
  scheduledSummary.innerHTML = `
    <div class="scheduled-stat${items.length ? " is-accent" : ""}">
      <span class="scheduled-stat-icon"><svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg></span>
      <div><span class="scheduled-stat-value">${items.length}</span><span class="scheduled-stat-label">Scheduled</span><span class="scheduled-stat-hint">Server-backed queue</span></div>
    </div>
    <div class="scheduled-stat">
      <span class="scheduled-stat-icon"><svg class="icon" aria-hidden="true"><use href="#i-clock"/></svg></span>
      <div><span class="scheduled-stat-value">${next ? formatRelativeTime(next.sendAt) : "—"}</span><span class="scheduled-stat-label">Next send</span><span class="scheduled-stat-hint">${next ? formatDateTimeFull(next.sendAt) : "Nothing queued"}</span></div>
    </div>
    <div class="scheduled-stat">
      <span class="scheduled-stat-icon"><svg class="icon" aria-hidden="true"><use href="#i-check"/></svg></span>
      <div><span class="scheduled-stat-value">${withinHours}</span><span class="scheduled-stat-label">Within work hours</span><span class="scheduled-stat-hint">Policy-aware delivery</span></div>
    </div>
    <div class="scheduled-stat">
      <span class="scheduled-stat-icon"><svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg></span>
      <div><span class="scheduled-stat-value">${withFiles}</span><span class="scheduled-stat-label">With attachments</span><span class="scheduled-stat-hint">${items.length - withFiles} text only</span></div>
    </div>`;
  if (!items.length) {
    scheduledList.innerHTML = `<div class="scheduled-empty">
      <span class="scheduled-empty-art"><svg class="icon" aria-hidden="true"><use href="#${query ? "i-search" : "i-calendar-clock"}"/></svg></span>
      <h2>${query ? "No scheduled messages match" : "Nothing on the schedule"}</h2>
      <p>${query ? `No match for “${esc(state.query)}”. Clear the search to see all scheduled mail.` : "Write a message, choose Send later, and it will wait here until the right moment. You can send now, edit or cancel at any time before delivery."}</p>
      ${query ? '<button class="btn-secondary" data-clear-filters type="button">Clear search</button>' : '<button class="btn-primary" data-scheduled-compose type="button"><svg class="icon" aria-hidden="true"><use href="#i-compose"/></svg>Compose a message</button>'}
      <div class="scheduled-empty-tips"><span>Server-persisted</span><span>Time-zone aware</span><span>Fully audited</span></div>
    </div>`;
    return;
  }
  const groups = new Map();
  for (const item of items) {
    const key = dateKeyInZone(item.sendAt);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(item);
  }
  scheduledList.innerHTML = Array.from(groups.entries()).map(([key, groupItems]) => {
    const cards = groupItems.map((item) => {
      const stamp = new Date(item.sendAt);
      const block = scheduledDateBlock(stamp);
      const assessment = scheduledAssessment(stamp);
      const files = (item.attachments || []).length;
      return `<article class="scheduled-card${assessment.ok ? "" : " is-outside"}" data-scheduled-id="${esc(item.id)}">
        <div class="scheduled-date">
          <span class="scheduled-date-weekday">${esc(block.weekday)}</span>
          <strong>${block.day}</strong>
          <span class="scheduled-date-month">${esc(block.month)}</span>
          <small>${esc(block.time)}</small>
        </div>
        <div class="scheduled-card-main">
          <div class="scheduled-card-top">
            <span class="scheduled-state ${assessment.ok ? "is-ok" : "is-warning"}">${esc(assessment.label)}</span>
            <span class="scheduled-relative"><svg class="icon" aria-hidden="true"><use href="#i-clock"/></svg>${esc(formatRelativeTime(stamp))}</span>
          </div>
          <h3>${esc(item.subject || "(no subject)")}</h3>
          <div class="scheduled-recipient-line"><span>To</span> ${esc(scheduledRecipients(item))}<i aria-hidden="true"></i><span>From</span> ${esc(scheduledAliasLabel(item))}</div>
          <p class="scheduled-preview">${esc((item.bodyText || "").slice(0, 180)) || "No message body yet."}</p>
          <div class="scheduled-card-meta">
            <span><svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>${esc(formatDateTimeFull(stamp))}</span>
            ${files ? `<span><svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg>${files} attachment${files === 1 ? "" : "s"}</span>` : ""}
            <span><svg class="icon" aria-hidden="true"><use href="#i-shield-check"/></svg>Server queue</span>
          </div>
        </div>
        <div class="scheduled-card-actions">
          <button class="btn-primary" data-scheduled-send-now="${esc(item.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Send now</button>
          <button class="btn-secondary" data-scheduled-edit="${esc(item.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-compose"/></svg>Edit</button>
          <button class="btn-secondary danger-btn" data-scheduled-cancel="${esc(item.id)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg>Cancel</button>
        </div>
      </article>`;
    }).join("");
    return `<section class="scheduled-group"><header class="scheduled-group-head"><h3>${esc(scheduledDayLabel(key))}</h3><span>${groupItems.length} message${groupItems.length === 1 ? "" : "s"}</span></header>${cards}</section>`;
  }).join("");
}

function openScheduledEdit(id) {
  const item = scheduledItemById(id);
  if (!item) { showToast("That scheduled message is no longer available"); return; }
  closeStatusPop();
  state.scheduledEditId = id;
  const stamp = new Date(item.sendAt);
  const parts = timeZoneParts(stamp);
  scheduledEditTo.value = (item.to || []).map((person) => person.email).join(", ");
  scheduledEditCc.value = (item.cc || []).map((person) => person.email).join(", ");
  scheduledEditSubject.value = item.subject || "";
  scheduledEditBody.value = item.bodyText || "";
  scheduledEditDate.min = dateKeyInZone(new Date());
  scheduledEditDate.value = dateKeyInZone(stamp);
  scheduledEditTime.value = `${String(parts.hour).padStart(2, "0")}:${String(parts.minute).padStart(2, "0")}`;
  scheduledEditOriginal.innerHTML = `<span class="eyebrow">Currently scheduled</span><strong>${esc(formatDateTimeFull(stamp))}</strong><span>${esc(formatRelativeTime(stamp))} · from ${esc(scheduledAliasLabel(item))}</span>`;
  const attachments = (item.attachments || []).length;
  scheduledEditAttachments.innerHTML = attachments
    ? `<svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg><span>${attachments} attachment${attachments === 1 ? "" : "s"} stay attached to this message.</span>`
    : `<svg class="icon" aria-hidden="true"><use href="#i-paperclip"/></svg><span>No attachments on this message.</span>`;
  state.scheduledEditPresets = buildSchedulePresets().slice(0, 3);
  scheduledEditPresets.innerHTML = state.scheduledEditPresets.map((preset) => `<button class="scheduled-preset" data-scheduled-edit-preset="${esc(preset.id)}" type="button"><strong>${esc(preset.title)}</strong><span>${esc(formatDateOnly(preset.date))} · ${esc(formatClock(zonedWallTimeToUtc(preset.date, preset.time)))}</span></button>`).join("");
  scheduledEditError.hidden = true;
  scheduledEditOverlay.hidden = false;
  updateScheduledEditInsight();
  setTimeout(() => scheduledEditSubject.focus(), 40);
}

function closeScheduledEdit() {
  scheduledEditOverlay.hidden = true;
  state.scheduledEditId = null;
  scheduledEditError.hidden = true;
  scheduledEditSave.disabled = false;
  scheduledEditSave.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>Save changes';
}

function updateScheduledEditInsight() {
  if (scheduledEditOverlay.hidden) return;
  const stamp = zonedWallTimeToUtc(scheduledEditDate.value, scheduledEditTime.value);
  if (!stamp || Number.isNaN(stamp.getTime())) {
    scheduledEditInsight.innerHTML = '<span class="scheduled-insight-error">Choose a valid send date and time.</span>';
    scheduledEditSave.disabled = true;
    return;
  }
  const future = stamp.getTime() > Date.now() + 5000;
  const assessment = scheduledAssessment(stamp);
  scheduledEditInsight.innerHTML = `<div class="scheduled-edit-insight-when"><span class="eyebrow">Leaves the outbox</span><strong>${esc(formatDateTimeFull(stamp))}</strong><span>${esc(formatRelativeTime(stamp))} · ${esc(formatTimeZoneLabel())}</span></div><span class="scheduled-state ${assessment.ok ? "is-ok" : "is-warning"}">${esc(assessment.label)}</span>`;
  if (!future) {
    scheduledEditInsight.innerHTML += '<span class="scheduled-insight-error">Choose a time at least a few seconds in the future.</span>';
  }
  scheduledEditSave.disabled = !future;
}

async function saveScheduledEdit() {
  const item = scheduledItemById(state.scheduledEditId);
  if (!item) { closeScheduledEdit(); showToast("That scheduled message is no longer available"); return; }
  const stamp = zonedWallTimeToUtc(scheduledEditDate.value, scheduledEditTime.value);
  if (!stamp || stamp.getTime() <= Date.now() + 5000) {
    scheduledEditError.textContent = "Choose a send time at least a few seconds in the future.";
    scheduledEditError.hidden = false;
    return;
  }
  scheduledEditSave.disabled = true;
  scheduledEditSave.textContent = "Saving…";
  try {
    const data = await api(`/api/outbox/${encodeURIComponent(item.id)}`, {
      method: "PATCH",
      body: {
        to: scheduledEditTo.value,
        cc: scheduledEditCc.value,
        subject: scheduledEditSubject.value,
        bodyText: scheduledEditBody.value,
        sendAt: stamp.toISOString()
      }
    });
    state.pendingSends = data.pending || state.pendingSends;
    if (data.outbound) state.outbound = data.outbound;
    updatePendingUi();
    updateScheduledUi();
    updateOutboundUi();
    stopOutboxPolling();
    startOutboxPolling();
    closeScheduledEdit();
    showToast("Scheduled message updated");
  } catch (error) {
    scheduledEditError.textContent = error.message;
    scheduledEditError.hidden = false;
    scheduledEditSave.disabled = false;
    scheduledEditSave.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>Save changes';
    showError(error);
  }
}

function openSendNowDialog(id) {
  const item = scheduledItemById(id);
  if (!item) { showToast("That scheduled message is no longer available"); return; }
  closeStatusPop();
  state.sendNowId = id;
  sendNowSummary.innerHTML = `<strong>${esc(item.subject || "(no subject)")}</strong>
    <span>To ${esc(scheduledRecipients(item))} · from ${esc(scheduledAliasLabel(item))}</span>
    <span class="send-now-original"><svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>Was scheduled for ${esc(formatDateTimeFull(item.sendAt))}</span>`;
  sendNowConfirm.disabled = false;
  sendNowConfirm.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Send now';
  sendNowOverlay.hidden = false;
  setTimeout(() => sendNowConfirm.focus(), 40);
}

function closeSendNowDialog() {
  sendNowOverlay.hidden = true;
  state.sendNowId = null;
  sendNowConfirm.disabled = false;
  sendNowConfirm.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Send now';
}

async function confirmSendNow() {
  const item = scheduledItemById(state.sendNowId);
  if (!item) { closeSendNowDialog(); showToast("That scheduled message is no longer available"); return; }
  const subject = item.subject || "(no subject)";
  sendNowConfirm.disabled = true;
  sendNowConfirm.textContent = "Sending…";
  try {
    const data = await api(`/api/outbox/${encodeURIComponent(item.id)}/send-now`, { method: "POST", body: {} });
    state.pendingSends = data.pending || state.pendingSends;
    if (data.outbound) state.outbound = data.outbound;
    updatePendingUi();
    updateScheduledUi();
    updateOutboundUi();
    stopOutboxPolling();
    startOutboxPolling();
    closeSendNowDialog();
    await refreshMailbox({ quiet: true });
    showToast(`“${subject}” sent now`, "View sent", () => {
      setTask("mail");
      setFolder("sent");
    }, { duration: 9000 });
  } catch (error) {
    if (error.code === "SEND_ALREADY_COMMITTED") {
      state.pendingSends = state.pendingSends.filter((entry) => entry.id !== item.id);
      updatePendingUi();
      updateScheduledUi();
      closeSendNowDialog();
      refreshMailbox({ quiet: true }).catch(() => {});
    } else {
      sendNowConfirm.disabled = false;
      sendNowConfirm.innerHTML = '<svg class="icon" aria-hidden="true"><use href="#i-send"/></svg>Send now';
    }
    showError(error);
  }
}

async function refreshScheduled() {
  try {
    const data = await api("/api/outbox");
    state.pendingSends = data.pending || [];
    if (data.outbound) state.outbound = data.outbound;
    updatePendingUi();
    updateScheduledUi();
    updateOutboundUi();
    renderScheduled();
    showToast("Schedule refreshed");
  } catch (error) {
    showError(error);
  }
}

/* ============================================================
   people
   ============================================================ */
function peopleSourceLabel(person) {
  if (person.source === "directory") return "Directory";
  if (person.source === "auto") return "Auto-saved";
  return "Contact";
}

function peopleSourceClass(person) {
  if (person.source === "directory") return "directory";
  if (person.source === "auto") return "auto";
  return "contact";
}

function peopleRoleLine(person) {
  const primary = String(person.title || (person.source === "directory" ? person.role : "Contact") || "").trim();
  const company = String(person.company || "").trim();
  if (primary && company && primary !== company) return `${primary} · ${company}`;
  return primary || company || "Saved contact";
}

function peopleListDeduped() {
  const seen = new Set();
  const result = [];
  for (const person of state.people) {
    const key = normalizeEmail(person.email);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    result.push(person);
  }
  return result;
}

function renderPeople() {
  const list = document.getElementById("peopleList");
  if (!list) return;
  const everyone = peopleListDeduped();
  const query = state.peopleQuery.trim().toLowerCase();
  const filter = state.peopleFilter || "all";
  const matches = (person) => {
    if (filter === "directory" && person.source !== "directory") return false;
    if (filter === "contacts" && person.source === "directory") return false;
    if (!query) return true;
    return [person.name, person.email, person.company, person.title, person.team, person.role]
      .join(" ").toLowerCase().includes(query);
  };
  const visible = everyone.filter(matches).sort((a, b) => {
    if (a.source === "directory" && b.source !== "directory") return -1;
    if (a.source !== "directory" && b.source === "directory") return 1;
    return String(a.name || "").localeCompare(String(b.name || ""));
  });
  const internal = everyone.filter((person) => person.source === "directory").length;
  const contacts = everyone.length - internal;
  const count = document.getElementById("peopleCount");
  if (count) count.textContent = `${visible.length} shown · ${internal} directory · ${contacts} saved`;
  document.querySelectorAll("[data-people-filter]").forEach((button) => {
    const active = button.getAttribute("data-people-filter") === filter;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  if (!visible.some((person) => normalizeEmail(person.email) === normalizeEmail(state.contactEmail))) {
    state.contactEmail = visible[0] ? visible[0].email : null;
  }
  if (!visible.length) {
    list.innerHTML = `<div class="people-empty-rich">
      <div class="empty-icon"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg></div>
      <strong>${query ? "No people found" : "No contacts here yet"}</strong>
      <span>${query ? "Try a different name, email or company." : "Recipients you email are saved automatically and appear here."}</span>
    </div>`;
    renderPersonDetail();
    syncStatusRange();
    return;
  }
  const row = (person) => `<button class="contact${normalizeEmail(person.email) === normalizeEmail(state.contactEmail) ? " is-active" : ""}" data-contact="${esc(person.email)}" type="button">
      ${avatar(person, "md")}
      <span class="contact-id"><span class="contact-name">${esc(person.name || person.email)}</span><span class="contact-role">${esc(peopleRoleLine(person))}</span><span class="contact-email">${esc(person.email)}</span></span>
      <span class="people-source ${peopleSourceClass(person)}">${peopleSourceLabel(person)}</span>
    </button>`;
  const groups = [];
  if (filter !== "contacts") groups.push({ key: "directory", label: "Internal directory", people: visible.filter((person) => person.source === "directory") });
  if (filter !== "directory") groups.push({ key: "contacts", label: "Your contacts", people: visible.filter((person) => person.source !== "directory") });
  list.innerHTML = groups.filter((group) => group.people.length).map((group) => `
    <section class="people-section" aria-label="${esc(group.label)}">
      <div class="people-section-head"><span>${esc(group.label)}</span><span>${group.people.length}</span></div>
      ${group.people.map(row).join("")}
    </section>`).join("");
  renderPersonDetail();
  syncStatusRange();
}

let contactEditingId = null;
let contactEditingEmail = null;

async function loadContactRecord(email) {
  const data = await api("/api/contacts");
  const contacts = data.contacts || [];
  return contacts.find((item) => item.email === email) || null;
}

async function openContactDialog(email = null) {
  contactEditingId = null;
  contactEditingEmail = email;
  const record = email ? await loadContactRecord(email) : null;
  if (email && !record) { showToast("Contact not found"); return; }
  contactEditingId = record ? record.id : null;
  document.getElementById("contactDialogTitle").textContent = record ? "Edit contact" : "New contact";
  document.getElementById("contactName").value = record ? record.name : "";
  document.getElementById("contactEmailField").value = record ? record.email : "";
  document.getElementById("contactEmailField").disabled = Boolean(record);
  document.getElementById("contactCompany").value = record ? record.company || "" : "";
  document.getElementById("contactTitle").value = record ? record.title || "" : "";
  document.getElementById("contactPhone").value = record ? record.phone || "" : "";
  document.getElementById("contactLocation").value = record ? record.location || "" : "";
  document.getElementById("contactNotes").value = record ? record.notes || "" : "";
  document.getElementById("contactDelete").hidden = !record;
  document.getElementById("contactSave").innerHTML = record
    ? `<svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>Save contact`
    : `<svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Create contact`;
  document.getElementById("contactOverlay").hidden = false;
  setTimeout(() => document.getElementById("contactName").focus(), 30);
}

function closeContactDialog() {
  document.getElementById("contactOverlay").hidden = true;
  contactEditingId = null;
  contactEditingEmail = null;
}

function contactToPerson(contact) {
  const source = contact.source || "contact";
  return {
    name: contact.name, email: contact.email, role: source === "directory" ? contact.role || "Contact" : "Contact",
    company: contact.company || "", team: "", title: contact.title || "", phone: contact.phone || "",
    location: contact.location || "", reportsTo: "", initials: contact.initials || initialsFrom(contact.name),
    color: contact.color || "#3d6b62", source
  };
}

function mergeContactPerson(contact) {
  if (!contact?.email) return;
  const person = contactToPerson(contact);
  const index = state.people.findIndex((item) => normalizeEmail(item.email) === normalizeEmail(person.email));
  if (index >= 0) state.people[index] = { ...state.people[index], ...person };
  else state.people.push(person);
  if (state.task === "people") renderPeople();
}

async function saveContact() {
  const name = document.getElementById("contactName").value.trim();
  const email = document.getElementById("contactEmailField").value.trim();
  if (!name) { showToast("Add a contact name"); return; }
  if (!email || !email.includes("@")) { showToast("Add a valid email address"); return; }
  const body = {
    name, email,
    company: document.getElementById("contactCompany").value.trim(),
    title: document.getElementById("contactTitle").value.trim(),
    phone: document.getElementById("contactPhone").value.trim(),
    location: document.getElementById("contactLocation").value.trim(),
    notes: document.getElementById("contactNotes").value.trim()
  };
  try {
    const editingId = contactEditingId;
    const data = await api(editingId ? `/api/contacts/${encodeURIComponent(editingId)}` : "/api/contacts", {
      method: editingId ? "PATCH" : "POST", body
    });
    const updated = contactToPerson(data.contact);
    state.people = state.people.filter((person) => person.email !== contactEditingEmail && person.email !== updated.email);
    state.people.push(updated);
    state.contactEmail = updated.email;
    closeContactDialog();
    if (state.task === "people") renderPeople();
    showToast(editingId ? "Contact updated" : "Contact created");
  } catch (error) { showError(error); }
}

async function deleteContactByEmail(email) {
  if (!email || !(await appConfirm({ title: "Delete this contact?", message: "The contact will be removed from People. Existing messages are not affected.", confirmLabel: "Delete contact", tone: "danger" }))) return;
  try {
    const record = await loadContactRecord(email);
    if (!record) { showToast("Contact not found"); return; }
    await api(`/api/contacts/${encodeURIComponent(record.id)}`, { method: "DELETE" });
    state.people = state.people.filter((person) => person.email !== email);
    if (state.contactEmail === email) state.contactEmail = state.people[0]?.email || null;
    closeContactDialog();
    renderPeople();
    showToast("Contact deleted");
  } catch (error) { showError(error); }
}

function renderPersonDetail() {
  const person = state.people.find((item) => normalizeEmail(item.email) === normalizeEmail(state.contactEmail));
  if (!person) {
    personDetail.innerHTML = `<div class="people-empty-rich">
      <div class="empty-icon"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg></div>
      <strong>Choose a person</strong>
      <span>Select someone from the directory or your saved contacts to see their details.</span>
    </div>`;
    return;
  }
  const recent = state.messages
    .filter((message) => normalizeEmail(message.from?.email) === normalizeEmail(person.email) || (message.to || []).some((recipient) => normalizeEmail(recipient.email) === normalizeEmail(person.email)))
    .sort((a, b) => Date.parse(b.date) - Date.parse(a.date))
    .slice(0, 3);
  const sourceClass = peopleSourceClass(person);
  const sourceLabel = person.source === "directory" ? "Internal directory" : person.source === "auto" ? "Auto-saved recipient" : "Personal contact";
  const contactActions = person.source !== "directory"
    ? `<button class="btn-secondary" data-edit-contact="${esc(person.email)}" type="button">Edit contact</button><button class="btn-secondary danger-btn" data-delete-contact="${esc(person.email)}" type="button">Delete</button>`
    : "";
  personDetail.innerHTML = `
    <div class="person-hero">
      ${avatar(person, "xl")}
      <div class="person-hero-id">
        <h2>${esc(person.name || person.email)}</h2>
        <p>${esc(peopleRoleLine(person))}</p>
        <span class="directory-badge ${sourceClass}"><svg class="icon" aria-hidden="true"><use href="#${person.source === "directory" ? "i-shield-check" : "i-people"}"/></svg>${sourceLabel}</span>
      </div>
      <div class="person-actions">
        <button class="btn-primary" data-message-contact="${esc(person.email)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-compose"/></svg>New message</button>
        <button class="btn-secondary" data-copy-email="${esc(person.email)}" type="button">Copy email</button>
        ${contactActions}
      </div>
    </div>
    <div class="info-card">
      <div class="info-row"><span class="info-label">Email</span><span class="info-value">${esc(person.email)}</span></div>
      <div class="info-row"><span class="info-label">Phone</span><span class="info-value">${esc(person.phone || "—")}</span></div>
      <div class="info-row"><span class="info-label">Company</span><span class="info-value">${esc(person.company || "—")}</span></div>
      <div class="info-row"><span class="info-label">Title</span><span class="info-value">${esc(person.title || "—")}</span></div>
      <div class="info-row"><span class="info-label">Team</span><span class="info-value">${esc(person.team || "—")}</span></div>
      <div class="info-row"><span class="info-label">Location</span><span class="info-value">${esc(person.location || "—")}</span></div>
      <div class="info-row"><span class="info-label">Reports to</span><span class="info-value">${esc(person.reportsTo || "—")}</span></div>
    </div>
    ${person.notes ? `<div class="person-notes"><h3>Notes</h3><p>${esc(person.notes)}</p></div>` : ""}
    <div class="recent-block">
      <h3>Recent conversations</h3>
      ${recent.length ? recent.map((message) => `<button class="recent-item" data-recent="${esc(message.id)}" type="button">
        <span class="recent-icon"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg></span>
        <span class="recent-main"><span class="recent-subject">${esc(message.subject)}</span><span class="recent-when">${esc(formatListWhen(message.date))} · ${esc(folderDef(message.folder).label)}</span></span>
        <svg class="icon recent-arrow" aria-hidden="true"><use href="#i-chevron-right"/></svg>
      </button>`).join("") : `<p class="people-empty">No recent messages.</p>`}
    </div>`;
}

function copyText(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(
      () => showToast("Email address copied"),
      () => showToast("Copy failed — select the address instead")
    );
  } else {
    showToast("Copy failed — select the address instead");
  }
}

function openMessageFromPeople(messageId) {
  const message = messageById(messageId);
  if (!message) return;
  state.task = "mail";
  app.setAttribute("data-task", "mail");
  mailView.hidden = false;
  calendarView.hidden = true;
  peopleView.hidden = true;
  settingsView.hidden = true;
  state.folder = ["starred", "spam", "trash"].includes(message.folder) ? "inbox" : message.folder;
  state.filter = "all";
  state.query = "";
  searchInput.value = "";
  updateSearchClear();
  const threadId = selectionIdForMessage(message);
  state.selectedThreadId = threadId;
  if (message.unread && state.markReadOnOpen) message.unread = false;
  syncChips();
  syncNav();
  syncSearchContext();
  renderAll();
  syncStatusContext();
  document.body.classList.remove("person-open");
  if (isMobile()) document.body.classList.add("reader-open");
}

/* ============================================================
   calendar
   ============================================================ */
const CAL_MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

function dateKey(date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

function isoWeek(date) {
  const d = new Date(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()));
  const day = d.getUTCDay() || 7;
  d.setUTCDate(d.getUTCDate() + 4 - day);
  const yearStart = new Date(Date.UTC(d.getUTCFullYear(), 0, 1));
  return Math.ceil((((d - yearStart) / 86400000) + 1) / 7);
}

function eventsFor(key) {
  return state.calendarEvents.filter((event) => event.date === key).sort((a, b) => a.start.localeCompare(b.start));
}

function renderAgenda() {
  const selectedDate = new Date(state.calendarSelected + "T12:00:00");
  const events = eventsFor(state.calendarSelected);
  agendaDate.textContent = selectedDate.toLocaleDateString("en-US", { weekday: "long", month: "long", day: "numeric" });
  agendaCount.textContent = `${events.length} ${events.length === 1 ? "event" : "events"}`;
  const upcoming = state.calendarEvents
    .filter((event) => event.date > state.calendarSelected)
    .sort((a, b) => (a.date + a.start).localeCompare(b.date + b.start))
    .slice(0, 3);
  const upcomingHtml = upcoming.length ? `<div class="agenda-upcoming">
    <div class="agenda-upcoming-head">Coming up</div>
    ${upcoming.map((event) => {
      const date = new Date(event.date + "T12:00:00");
      return `<div class="upcoming-item" style="--ev:${event.color}">
        <span class="upcoming-date">${date.toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric" })}</span>
        <div class="upcoming-main"><div class="agenda-title">${esc(event.title)}</div><div class="upcoming-meta">${esc(event.start)} · ${esc(event.where)}</div></div>
      </div>`;
    }).join("")}
  </div>` : "";
  agendaList.innerHTML = (events.length
    ? events.map((event) => `<button class="agenda-event" style="--ev:${event.color}" data-edit-event="${esc(event.id)}" type="button" title="Edit ${esc(event.title)}">
        <span class="agenda-bar" aria-hidden="true"></span>
        <div class="agenda-time"><strong>${esc(event.start)}</strong>${esc(event.end)}</div>
        <div class="agenda-main"><div class="agenda-title">${esc(event.title)}</div><div class="agenda-meta"><span><svg class="icon" aria-hidden="true"><use href="#i-location"/></svg>${esc(event.where)}</span><span><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg>${event.attendees}</span></div></div>
        <svg class="icon agenda-edit-icon" aria-hidden="true"><use href="#i-chevron-right"/></svg>
      </button>`).join("")
    : `<div class="cal-empty">No events scheduled.</div>`) + upcomingHtml;
}

function renderCalendar() {
  const year = state.calendarYear;
  const month = state.calendarMonth;
  const monthStart = new Date(year, month, 1);
  const offset = (monthStart.getDay() + 6) % 7;
  const gridStart = new Date(year, month, 1 - offset);
  calMonthLabel.textContent = `${CAL_MONTHS[month]} ${year}`;
  const selectedDate = new Date(state.calendarSelected + "T12:00:00");
  calWeekLabel.textContent = "Week " + isoWeek(selectedDate.getMonth() === month ? selectedDate : monthStart);
  let html = "";
  for (let i = 0; i < 42; i += 1) {
    const date = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + i);
    const key = dateKey(date);
    const outside = date.getMonth() !== month;
    const events = eventsFor(key);
    const isToday = key === dateKey(new Date());
    const selected = key === state.calendarSelected;
    const weekend = (i % 7) >= 5;
    const ariaLabel = date.toLocaleDateString("en-US", { weekday: "long", month: "long", day: "numeric", year: "numeric" }) + (events.length ? `, ${events.length} events` : ", no events");
    html += `<button class="cal-cell${outside ? " is-outside" : ""}${weekend ? " is-weekend" : ""}${isToday ? " is-today" : ""}${selected ? " is-selected" : ""}" data-cal-date="${key}" type="button" role="gridcell" aria-selected="${selected}" aria-label="${esc(ariaLabel)}">
      <span class="cal-daynum">${date.getDate()}</span>
      <span class="cal-events">${events.slice(0, 3).map((event) => `<span class="cal-event" style="--ev:${event.color}">${esc(event.title)}</span>`).join("")}${events.length > 3 ? `<span class="cal-more">+${events.length - 3} more</span>` : ""}</span>
    </button>`;
  }
  calGrid.innerHTML = html;
  renderAgenda();
  syncStatusRange();
}

function shiftMonth(delta) {
  const next = new Date(state.calendarYear, state.calendarMonth + delta, 1);
  state.calendarYear = next.getFullYear();
  state.calendarMonth = next.getMonth();
  const today = new Date();
  state.calendarSelected = today.getMonth() === state.calendarMonth && today.getFullYear() === state.calendarYear ? dateKey(today) : dateKey(new Date(state.calendarYear, state.calendarMonth, 1));
  renderCalendar();
}

function selectCalendarDate(key, navigate = true) {
  const date = new Date(key + "T12:00:00");
  state.calendarSelected = key;
  if (navigate) { state.calendarYear = date.getFullYear(); state.calendarMonth = date.getMonth(); }
  renderCalendar();
}

let calendarEditingId = null;

function openEventDialog(eventId = null) {
  hideNotify();
  closeMenus();
  const event = eventId ? state.calendarEvents.find((item) => item.id === eventId) : null;
  calendarEditingId = event ? event.id : null;
  document.getElementById("eventDialogTitle").textContent = event ? "Edit event" : "New event";
  document.getElementById("eventName").value = event ? event.title : "";
  document.getElementById("eventDate").value = event ? event.date : (state.calendarSelected || dateKey(new Date()));
  document.getElementById("eventStart").value = event ? event.start : "10:00";
  document.getElementById("eventEnd").value = event ? event.end : "10:30";
  document.getElementById("eventWhere").value = event ? event.where || "" : "";
  document.getElementById("eventSave").innerHTML = event
    ? `<svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>Save event`
    : `<svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>Add event`;
  document.getElementById("eventDelete").hidden = !event;
  eventOverlay.hidden = false;
  setTimeout(() => document.getElementById("eventName").focus(), 30);
}

function closeEventDialog() {
  eventOverlay.hidden = true;
  calendarEditingId = null;
}

async function saveEvent() {
  const title = document.getElementById("eventName").value.trim();
  const date = document.getElementById("eventDate").value;
  const start = document.getElementById("eventStart").value;
  const end = document.getElementById("eventEnd").value;
  const where = document.getElementById("eventWhere").value.trim();
  if (!title) { showToast("Add an event title"); document.getElementById("eventName").focus(); return; }
  if (!date || !start || !end) { showToast("Choose a date, start and end time"); return; }
  if (end <= start) { showToast("End time must be after the start time"); return; }
  try {
    const editingId = calendarEditingId;
    const data = await api(editingId ? `/api/calendar/events/${encodeURIComponent(editingId)}` : "/api/calendar/events", {
      method: editingId ? "PATCH" : "POST",
      body: { title, date, start, end, where: where || "Alt8Mail HQ", color: "#3d6b62" }
    });
    if (editingId) {
      const index = state.calendarEvents.findIndex((item) => item.id === editingId);
      if (index >= 0) state.calendarEvents[index] = data.event;
    } else {
      state.calendarEvents.push(data.event);
    }
    const selected = new Date(date + "T12:00:00");
    state.calendarSelected = date;
    state.calendarYear = selected.getFullYear();
    state.calendarMonth = selected.getMonth();
    closeEventDialog();
    if (state.task === "calendar") renderCalendar();
    showToast(editingId ? "Event updated" : "Event added to calendar");
  } catch (error) { showError(error); }
}

async function deleteCalendarEvent(id) {
  if (!id || !(await appConfirm({ title: "Delete this event?", message: "The calendar event will be removed from your calendar.", confirmLabel: "Delete event", tone: "danger" }))) return;
  try {
    await api(`/api/calendar/events/${encodeURIComponent(id)}`, { method: "DELETE" });
    state.calendarEvents = state.calendarEvents.filter((item) => item.id !== id);
    closeEventDialog();
    if (state.task === "calendar") renderCalendar();
    showToast("Event deleted");
  } catch (error) { showError(error); }
}

/* ============================================================
   navigation + chrome
   ============================================================ */
const HASH_TASK_LABELS = {
  calendar: "Calendar", people: "People", tasks: "Tasks",
  files: "Files", scheduled: "Scheduled"
};
const HASH_TASK_ROUTES = Object.fromEntries(Object.entries(HASH_TASK_LABELS).map(([task, label]) => [label.toLowerCase(), task]));

function currentAppHash() {
  if (!state.user) return "";
  if (state.task === "settings") {
    return state.settingsTab === "appearance" ? "#/Settings" : `#/Settings/${encodeURIComponent(state.settingsTab || "appearance")}`;
  }
  if (HASH_TASK_LABELS[state.task]) return `#/${HASH_TASK_LABELS[state.task]}`;
  const label = folderDef(state.folder).label || FOLDER_LABELS[state.folder] || "Inbox";
  const base = `#/${encodeURIComponent(label)}`;
  return state.selectedThreadId ? `${base}/${encodeURIComponent(state.selectedThreadId)}` : base;
}

function writeAppHash({ replace = true } = {}) {
  if (!state.user || suppressHashSync) return;
  const next = currentAppHash();
  if (!next) return;
  lastAppliedHash = next;
  if (window.location.hash === next) return;
  const url = window.location.pathname + window.location.search + next;
  try {
    if (replace) window.history.replaceState({ alt8mailRoute: true }, "", url);
    else window.history.pushState({ alt8mailRoute: true }, "", url);
  } catch {
    window.location.hash = next;
  }
}

function folderFromHashSegment(segment) {
  let value = "";
  try { value = decodeURIComponent(segment || "").trim().toLowerCase(); } catch { value = String(segment || "").trim().toLowerCase(); }
  if (!value) return null;
  if (value === "mail") value = "inbox";
  if (value === "junk") value = "spam";
  const known = state.folders.find((folder) => folder.id.toLowerCase() === value || String(folder.label || "").toLowerCase() === value);
  if (known) return known.id;
  const builtin = Object.entries(FOLDER_LABELS).find(([id, label]) => id === value || label.toLowerCase() === value);
  return builtin ? builtin[0] : null;
}

function applyHashRoute() {
  if (!state.user) return false;
  const raw = window.location.hash.replace(/^#\/?/, "");
  if (!raw) return false;
  let parts;
  try {
    parts = raw.split("/").filter(Boolean).map((part) => decodeURIComponent(part));
  } catch {
    return false;
  }
  const root = String(parts[0] || "").trim().toLowerCase();
  if (!root) return false;
  suppressHashSync = true;
  try {
    if (root === "settings") {
      const requestedTab = String(parts[1] || "appearance").toLowerCase();
      const tab = SETTINGS_META[requestedTab] ? requestedTab : "appearance";
      setTask("settings");
      showSettingsTab(tab);
      return true;
    }
    if (HASH_TASK_ROUTES[root]) {
      setTask(HASH_TASK_ROUTES[root]);
      return true;
    }
    const folderId = folderFromHashSegment(parts[0]);
    if (!folderId) return false;
    setTask("mail");
    if (state.folder !== folderId) setFolder(folderId);
    let targetId = parts[1] || "";
    if (!targetId) {
      if (state.selectedThreadId) {
        state.selectedThreadId = null;
        state.expandedThreadId = null;
        document.body.classList.remove("reader-open");
        renderList();
        renderReader();
        syncSelectionUi();
      }
      return true;
    }
    const message = messageById(targetId);
    if (message) targetId = selectionIdForMessage(message);
    if (!threadById(targetId)) {
      showToast("That conversation is no longer available");
      if (state.selectedThreadId) {
        state.selectedThreadId = null;
        state.expandedThreadId = null;
        renderList();
        renderReader();
        syncSelectionUi();
      }
      return true;
    }
    selectThread(targetId);
    return true;
  } finally {
    suppressHashSync = false;
    lastAppliedHash = currentAppHash();
  }
}

function restoreNavigationFromLocation() {
  if (window.location.hash && window.location.hash !== "#") {
    if (applyHashRoute()) {
      writeAppHash({ replace: true });
      return true;
    }
  }
  setTask("mail");
  handleDeepLink();
  writeAppHash({ replace: true });
  return false;
}

function handleHashLocationChange() {
  if (!state.user) return;
  if (window.location.hash === lastAppliedHash) return;
  if (!applyHashRoute()) {
    writeAppHash({ replace: true });
    return;
  }
  writeAppHash({ replace: true });
}

function setTask(task) {
  if (!["mail", "calendar", "people", "tasks", "files", "scheduled", "settings"].includes(task)) return;
  state.task = task;
  app.setAttribute("data-task", task);
  if (task !== "mail") {
    const inlineDraft = composeSessions.find((session) => session.mode === "inline");
    if (inlineDraft) minimizeComposeSession(inlineDraft.id);
  }
  mailView.hidden = task !== "mail";
  calendarView.hidden = task !== "calendar";
  peopleView.hidden = task !== "people";
  tasksView.hidden = task !== "tasks";
  filesView.hidden = task !== "files";
  scheduledView.hidden = task !== "scheduled";
  settingsView.hidden = task !== "settings";
  state.query = "";
  searchInput.value = "";
  updateSearchClear();
  closeSearchHints();
  document.body.classList.remove("reader-open", "person-open", "search-open", "file-open", "task-detail-open");
  if (task === "calendar") renderCalendar();
  if (task === "people") {
    state.peopleQuery = "";
    state.peopleFilter = "all";
    const peopleSearch = document.getElementById("peopleSearch");
    if (peopleSearch) peopleSearch.value = "";
    renderPeople();
  }
  if (task === "tasks") renderTasks();
  if (task === "files") renderFiles();
  if (task === "scheduled") renderScheduled();
  if (task === "settings") {
    syncThemeUI();
    showSettingsTab(state.settingsTab);
  }
  if (task === "mail") { renderFolders(); renderList(); renderReader(); }
  syncNav();
  syncSearchContext();
  syncStatusRange();
  syncStatusContext();
  closeDrawer();
  hideNotify();
  closeMenus();
  writeAppHash({ replace: true });
}

function syncNav() {
  const navTask = state.task === "scheduled" ? "mail" : state.task;
  $$("button[data-task]").forEach((button) => {
    const active = button.getAttribute("data-task") === navTask;
    button.classList.toggle("is-active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
}

function syncChips() {
  $$("button[data-filter]").forEach((button) => {
    button.classList.toggle("is-active", button.getAttribute("data-filter") === state.filter);
  });
}

function setFolder(folder) {
  if (!state.folders.some((item) => item.id === folder)) return;
  state.folder = folder;
  state.filter = "all";
  state.query = "";
  state.selectedIds.clear();
  state.selectedThreadId = null;
  state.expandedThreadId = null;
  searchInput.value = "";
  updateSearchClear();
  document.body.classList.remove("reader-open");
  renderFolders();
  renderList();
  renderReader();
  syncChips();
  syncSearchContext();
  syncStatusContext();
  closeDrawer();
  writeAppHash({ replace: true });
}

function setFilter(filter) {
  if (state.filter === filter) return;
  state.filter = filter;
  state.selectedIds.clear();
  syncChips();
  renderList();
  syncSelectionUi();
}

function openDrawer() {
  if (!isDrawer()) return;
  document.body.classList.add("drawer-open");
  scrim.classList.add("is-open");
  scrim.setAttribute("aria-hidden", "false");
}

function closeDrawer() {
  document.body.classList.remove("drawer-open");
  scrim.classList.remove("is-open");
  scrim.setAttribute("aria-hidden", "true");
}

function openHelp() {
  lastFocus = document.activeElement;
  helpOverlay.hidden = false;
  setTimeout(() => document.getElementById("helpClose").focus(), 30);
}

function closeHelp() {
  helpOverlay.hidden = true;
  if (lastFocus && typeof lastFocus.focus === "function") {
    try { lastFocus.focus(); } catch {}
  }
}

function moveRecentKey() {
  return `a8m_move_recent_${normalizeEmail(state.user?.email || "anonymous")}`;
}

function moveRecentFolders() {
  try {
    const ids = JSON.parse(localStorage.getItem(moveRecentKey()) || "[]");
    return (Array.isArray(ids) ? ids : []).filter((id) => (state.folders || []).some((folder) => folder.id === id)).slice(0, 4);
  } catch {
    return [];
  }
}

function rememberMoveFolder(id) {
  const current = moveRecentFolders().filter((item) => item !== id);
  current.unshift(id);
  try { localStorage.setItem(moveRecentKey(), JSON.stringify(current.slice(0, 4))); } catch {}
}

function moveFolderRow(folder) {
  const current = state.task === "mail" && state.query === "" && state.folder === folder.id;
  return `<button class="move-folder-row${current ? " is-current" : ""}" type="button" data-move-target="${esc(folder.id)}"><span class="move-folder-icon"${folder.color ? ` style="color:${esc(folder.color)}"` : ""}><svg class="icon" aria-hidden="true"><use href="#${esc(folder.icon || "i-folder")}"/></svg></span><span>${esc(folder.label)}</span>${current ? `<small>current</small>` : ""}</button>`;
}

function moveMenuListMarkup() {
  const query = state.moveSearch.trim().toLowerCase();
  const folders = (state.folders || [])
    .filter((folder) => !folder.virtual && !folder.delegated && folder.id !== "starred")
    .filter((folder) => !query || folder.label.toLowerCase().includes(query))
    .sort((a, b) => folderSortValue(a) - folderSortValue(b));
  return folders.map(moveFolderRow).join("") || `<div class="move-empty">No folder matches “${esc(state.moveSearch)}”.</div>`;
}

function renderMoveMenu() {
  const recent = moveRecentFolders();
  const recentRows = (recent.length ? recent : ["trash", "archive"]).map((id) => (state.folders || []).find((folder) => folder.id === id)).filter(Boolean);
  moveMenu.innerHTML = `<div class="move-panel-head"><span><svg class="icon" aria-hidden="true"><use href="#i-folder"/></svg>${state.moveRuleMode ? "Always move to" : "Move to"}</span><button class="icon-btn" data-move-close type="button" aria-label="Close move menu"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button></div>
    <div class="move-search"><svg class="icon" aria-hidden="true"><use href="#i-search"/></svg><input data-move-search type="search" value="${esc(state.moveSearch)}" placeholder="Search for a folder" autocomplete="off"></div>
    <div class="move-scroll">
      <div class="move-section-label">Most Recent</div>
      ${recentRows.map(moveFolderRow).join("")}
      <div class="move-create-row"><svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg><input data-move-new-folder type="text" maxlength="60" placeholder="Create new folder"><button type="button" data-move-create>Save</button></div>
      <button class="move-rule-mode${state.moveRuleMode ? " is-active" : ""}" type="button" data-move-rule-toggle aria-pressed="${state.moveRuleMode ? "true" : "false"}"><span class="move-rule-check">${state.moveRuleMode ? "✓" : ""}</span><span><strong>Always move mail from this sender</strong><small>${state.moveRuleMode ? "Choosing a folder also creates a rule and a matching destination in Settings → Rules." : "Create a rule so future messages skip the wrong folder."}</small></span></button>
      <div class="move-section-label">${state.moveRuleMode ? "Choose the permanent destination" : "All folders"}</div>
      <div class="move-folder-list" data-move-list>${moveMenuListMarkup()}</div>
    </div>`;
}

function openMoveMenu(anchor) {
  if (!currentThread() && !state.selectedIds.size) return;
  state.moveSearch = "";
  state.moveRuleMode = false;
  renderMoveMenu();
  positionMenu(moveMenu, anchor);
  setTimeout(() => moveMenu.querySelector("[data-move-search]")?.focus(), 50);
}

async function chooseMoveTarget(folderId) {
  const folder = (state.folders || []).find((item) => item.id === folderId);
  if (!folder) return;
  const always = state.moveRuleMode;
  closeMenus();
  rememberMoveFolder(folderId);
  await performBulkAction("move", folderId, { alwaysRule: always });
  if (always) showToast(`Future mail from this sender will go to ${folder.label}`);
}

async function createFolderFromMoveMenu() {
  const input = moveMenu.querySelector("[data-move-new-folder]");
  const label = String(input?.value || "").trim();
  if (!label) { input?.focus(); return; }
  try {
    const data = await api("/api/folders", { method: "POST", body: { label, parentId: "", color: "" } });
    await loadFolders();
    state.moveSearch = "";
    if (data.folder?.id) await chooseMoveTarget(data.folder.id);
  } catch (error) { showError(error); }
}

function positionMenu(menu, anchor) {
  menu.hidden = false;
  const rect = anchor.getBoundingClientRect();
  const width = menu.offsetWidth || 190;
  const left = Math.max(8, Math.min(rect.left, window.innerWidth - width - 8));
  menu.style.left = left + "px";
  menu.style.top = Math.round(rect.bottom + 6) + "px";
}

function closeMenus() {
  newMenu.hidden = true;
  moveMenu.hidden = true;
  if (folderMenu) folderMenu.hidden = true;
  state.folderMenuTarget = "";
  state.bulkMoveOpen = false;
  const bulkMenu = document.getElementById("bulkMoveMenu");
  if (bulkMenu) bulkMenu.hidden = true;
}

function notificationIconName(kind) {
  if (kind === "calendar") return "i-calendar";
  if (kind === "mail") return "i-mail";
  if (kind === "security") return "i-shield-check";
  if (kind === "scheduled") return "i-calendar-clock";
  if (kind === "quota") return "i-alert";
  return "i-bell";
}

function buildNotifications() {
  const items = [];
  const now = Date.now();
  const seen = new Set();
  const add = (item) => {
    if (!item || seen.has(item.id)) return;
    seen.add(item.id);
    items.push(item);
  };
  const inbox = state.messages
    .filter((message) => message.folder === "inbox" && message.unread && !message.draft)
    .sort((a, b) => Date.parse(b.date) - Date.parse(a.date))
    .slice(0, 14);
  inbox.forEach((message) => {
    const sender = message.from?.name || message.from?.email || "New sender";
    add({
      id: "mail:" + message.id,
      kind: "mail",
      title: `New message from ${sender}`,
      body: message.subject || "(no subject)",
      detail: message.snippet || htmlToText(message.bodyHtml || "").slice(0, 140),
      time: message.date,
      unread: true,
      action: { type: "message", messageId: message.id, threadId: message.threadId || message.id }
    });
  });
  (state.pendingSends || []).forEach((send) => {
    const when = send.sendAt ? Date.parse(send.sendAt) : now;
    if (when < now - 24 * 3600 * 1000) return;
    add({
      id: "scheduled:" + send.id,
      kind: "scheduled",
      title: send.scheduled ? "Scheduled message queued" : "Message waiting to send",
      body: send.subject || "(no subject)",
      detail: `${send.to?.[0]?.email || "Recipient"} · ${formatListWhen(send.sendAt)}`,
      time: send.sendAt || new Date().toISOString(),
      unread: true,
      action: { type: "scheduled" }
    });
  });
  (state.calendarEvents || []).forEach((event) => {
    const start = zonedWallTimeToUtc(event.date, event.start);
    if (!start || start.getTime() < now - 30 * 60 * 1000 || start.getTime() > now + 36 * 3600 * 1000) return;
    add({
      id: "calendar:" + (event.id || `${event.date}-${event.start}-${event.title}`),
      kind: "calendar",
      title: event.title || "Upcoming event",
      body: `${formatClock(start)} · ${event.where || "Calendar"}`,
      detail: start.getTime() <= now + 15 * 60 * 1000 ? "Starting soon" : `Starts ${formatRelativeTime(start)}`,
      time: start.toISOString(),
      unread: true,
      action: { type: "calendar" }
    });
  });
  const securityGroups = new Set();
  (state.recentAudit || []).forEach((entry) => {
    const action = String(entry.action || "");
    if (!/^(auth|security)\./.test(action)) return;
    const stamp = Date.parse(entry.ts || "");
    if (!Number.isFinite(stamp) || stamp < now - 7 * 24 * 3600 * 1000) return;
    const group = `${action}|${entry.target || ""}|${entry.ip || ""}`;
    if (securityGroups.has(group)) return;
    securityGroups.add(group);
    add({
      id: "audit:" + (entry.id || entry.ts + action),
      kind: "security",
      title: AUDIT_LABELS[action] || "Account security activity",
      body: entry.detail || entry.target || "Security event",
      detail: entry.ip ? `IP ${entry.ip}` : "Alt8Mail security",
      time: entry.ts,
      unread: true,
      action: { type: "security" }
    });
  });
  const quotaPercent = Number(state.quota?.percent || 0);
  if (quotaPercent >= 80) {
    add({
      id: "quota:warning",
      kind: "quota",
      title: "Mailbox storage is almost full",
      body: `${quotaPercent.toFixed(0)}% of your quota is in use`,
      detail: "Archive or remove large attachments to free space.",
      time: new Date().toISOString(),
      unread: true,
      action: { type: "account" }
    });
  }
  const outboundPercent = Number(state.outbound?.percent || 0);
  if (outboundPercent >= 80) {
    add({
      id: "outbound:warning",
      kind: "quota",
      title: "Outbound sending limit is almost reached",
      body: `${state.outbound.usedWithQueued || 0} of ${state.outbound.limit || 200} messages used`,
      detail: "The rolling 24-hour window resets automatically.",
      time: new Date().toISOString(),
      unread: true,
      action: { type: "security" }
    });
  }
  return items
    .map((item) => ({ ...item, unread: !state.dismissedNotifications.has(item.id) }))
    .sort((a, b) => Date.parse(b.time || 0) - Date.parse(a.time || 0));
}

function notificationRowMarkup(item, compact = true) {
  const icon = notificationIconName(item.kind);
  const iconClass = item.kind === "security" ? "security" : item.kind === "calendar" ? "accent" : "";
  return `<button class="notify-item${item.unread ? " is-unread" : ""}" type="button" data-notify-id="${esc(item.id)}">
    <span class="notify-icon ${iconClass}"><svg class="icon" aria-hidden="true"><use href="#${icon}"/></svg></span>
    <span class="notify-main"><strong>${item.unread ? '<span class="notify-unread-dot"></span>' : ""}${esc(item.title)}</strong><span>${esc(item.body || "")}</span></span>
    <time>${esc(formatListWhen(item.time))}</time>
  </button>`;
}

function noticeCenterRowMarkup(item) {
  const icon = notificationIconName(item.kind);
  const iconClass = item.kind === "security" ? "security" : item.kind === "calendar" ? "accent" : "";
  return `<div class="notice-center-item${item.unread ? " is-unread" : ""}">
    <span class="notify-icon ${iconClass}"><svg class="icon" aria-hidden="true"><use href="#${icon}"/></svg></span>
    <div class="notice-center-main"><strong>${esc(item.title)}</strong><p>${esc(item.body || "")}</p><div class="notice-center-meta"><span>${esc(item.detail || "")}</span><span>${esc(formatListWhen(item.time))}</span></div></div>
    <div class="notice-center-actions">
      ${item.unread ? `<button class="link-btn" data-notice-read="${esc(item.id)}" type="button">Mark read</button>` : ""}
      <button class="icon-btn" data-notice-open="${esc(item.id)}" type="button" aria-label="Open notification" title="Open"><svg class="icon" aria-hidden="true"><use href="#i-chevron-right"/></svg></button>
    </div>
  </div>`;
}

function renderNotifications() {
  state.notifications = buildNotifications();
  const unread = state.notifications.filter((item) => item.unread).length;
  const count = document.getElementById("notifyCount");
  const dot = document.getElementById("notifyDot");
  if (count) {
    count.hidden = unread === 0;
    count.textContent = unread > 99 ? "99+" : String(unread);
  }
  if (dot) dot.hidden = unread === 0;
  if (notifySubtitle) notifySubtitle.textContent = unread ? `${unread} unread notification${unread === 1 ? "" : "s"}` : "You’re all caught up";
  if (notifyList) {
    const recent = state.notifications.slice(0, 7);
    notifyList.innerHTML = recent.length ? recent.map((item) => notificationRowMarkup(item)).join("") : `<div class="notify-empty">No notifications yet. New mail, meetings and security activity appear here.</div>`;
  }
  if (noticeLayer && !noticeLayer.hidden) renderNoticeCenter();
}

function openNoticeCenter(filter = "all") {
  state.noticeFilter = filter;
  noticeLayer.hidden = false;
  hideNotify();
  renderNoticeCenter();
}

function closeNoticeCenter() {
  noticeLayer.hidden = true;
}

function renderNoticeCenter() {
  if (!noticeBody) return;
  const filter = state.noticeFilter || "all";
  let list = state.notifications || buildNotifications();
  if (filter === "unread") list = list.filter((item) => item.unread);
  else if (filter === "mail") list = list.filter((item) => item.kind === "mail" || item.kind === "scheduled");
  else if (filter === "system") list = list.filter((item) => item.kind === "security" || item.kind === "calendar" || item.kind === "quota");
  const unread = list.filter((item) => item.unread).length;
  if (noticeCount) noticeCount.textContent = `${list.length} notification${list.length === 1 ? "" : "s"}${unread ? ` · ${unread} unread` : ""}`;
  if (!list.length) {
    noticeBody.innerHTML = `<div class="notice-empty"><svg class="icon" aria-hidden="true"><use href="#i-bell"/></svg><h3>Nothing here yet</h3><p>${filter === "unread" ? "You’ve read every notification." : "New activity will appear here automatically."}</p></div>`;
    return;
  }
  const groups = new Map();
  list.forEach((item) => {
    const date = new Date(item.time || Date.now());
    const key = Number.isNaN(date.getTime()) ? "Earlier" : date.toDateString() === new Date().toDateString() ? "Today" : "Earlier";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(item);
  });
  noticeBody.innerHTML = Array.from(groups.entries()).map(([label, entries]) =>
    `<section class="notice-group"><h3>${label}</h3>${entries.map(noticeCenterRowMarkup).join("")}</section>`
  ).join("");
}

function markNotificationRead(id) {
  state.dismissedNotifications.add(id);
  persistDismissedNotifications();
  state.notifications = buildNotifications();
  const unread = state.notifications.filter((item) => item.unread).length;
  const count = document.getElementById("notifyCount");
  if (count) { count.hidden = unread === 0; count.textContent = unread > 99 ? "99+" : String(unread); }
  const dot = document.getElementById("notifyDot");
  if (dot) dot.hidden = unread === 0;
  if (notifyList) notifyList.innerHTML = state.notifications.slice(0, 7).map((item) => notificationRowMarkup(item)).join("") || `<div class="notify-empty">No notifications yet.</div>`;
  if (!noticeLayer.hidden) renderNoticeCenter();
}

function markAllNotificationsRead() {
  state.notifications.forEach((item) => state.dismissedNotifications.add(item.id));
  persistDismissedNotifications();
  renderNotifications();
  if (!noticeLayer.hidden) renderNoticeCenter();
}

function handleNotificationAction(id) {
  const item = (state.notifications || []).find((entry) => entry.id === id);
  if (!item) return;
  markNotificationRead(id);
  hideNotify();
  closeNoticeCenter();
  if (item.action?.type === "message") {
    setTask("mail");
    setFolder("inbox");
    const target = item.action.messageId ? messageById(item.action.messageId) : null;
    const selection = target ? selectionIdForMessage(target) : item.action.threadId;
    if (selection) selectThread(selection);
  } else if (item.action?.type === "scheduled") {
    setTask("scheduled");
  } else if (item.action?.type === "calendar") {
    setTask("calendar");
  } else if (item.action?.type === "account") {
    openSettings("account");
  } else {
    openSettings("security");
  }
}

function showNotify() {
  notifyPop.hidden = false;
  renderNotifications();
}

function hideNotify() {
  if (notifyPop) notifyPop.hidden = true;
}

function toggleNotify() {
  if (notifyPop.hidden) showNotify(); else hideNotify();
}

/* ============================================================
   PWA
   ============================================================ */
function registerServiceWorker() {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.register("/sw.js").then(() => {
    updateServiceWorkerUi();
  }).catch(() => {
    updateServiceWorkerUi();
  });
  navigator.serviceWorker.addEventListener("message", (event) => {
    if (event.data && event.data.type === "SW_UPDATED") {
      showToast("A new app version is ready — reload to update");
    }
  });
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    deferredInstallPrompt = event;
    const install = document.getElementById("installApp");
    if (install) install.hidden = false;
  });
  window.addEventListener("appinstalled", () => {
    deferredInstallPrompt = null;
    const install = document.getElementById("installApp");
    if (install) install.hidden = true;
    showToast("Alt8Mail installed");
  });
}

async function installApp() {
  if (!deferredInstallPrompt) {
    showToast("Use your browser’s Install App menu to add Alt8Mail");
    return;
  }
  deferredInstallPrompt.prompt();
  try { await deferredInstallPrompt.userChoice; } catch {}
  deferredInstallPrompt = null;
  const install = document.getElementById("installApp");
  if (install) install.hidden = true;
}

function handleDeepLink() {
  const params = new URLSearchParams(window.location.search);
  if (params.get("popout") === "1") document.body.classList.add("popout");
  if (params.get("compose") === "1") openCompose();
  if (params.get("settings")) openSettings(params.get("settings"));
  if (params.get("view") === "scheduled") setTask("scheduled");
  if (params.get("search")) {
    searchInput.value = params.get("search");
    setQuery(params.get("search"));
    focusSearch();
  }
  const requestedThread = params.get("thread") || params.get("message");
  if (requestedThread) {
    const message = messageById(requestedThread);
    const threadId = message ? selectionIdForMessage(message) : requestedThread;
    const thread = threadById(threadId);
    if (thread) {
      const target = thread.messages.find((item) => item.folder !== "drafts") || thread.latest;
      state.folder = FOLDER_LABELS[target.folder] ? target.folder : "inbox";
      state.filter = "all";
      state.selectedThreadId = threadId;
      renderFolders();
      renderList();
      renderReader();
      syncSelectionUi();
      if (isMobile()) document.body.classList.add("reader-open");
      document.title = `${threadSubject(thread)} — Alt8Mail`;
    } else {
      showToast("That conversation is no longer available");
    }
  }
  if (params.has("compose") || params.has("settings") || params.has("search") || params.get("view") === "scheduled") {
    history.replaceState({}, "", "/");
  }
}

function openThreadWindow(threadId) {
  if (!threadId) return;
  const url = `${location.origin}/?thread=${encodeURIComponent(threadId)}&popout=1`;
  const windowName = `a8m-thread-${String(threadId).replace(/[^A-Za-z0-9_-]/g, "")}`;
  const win = window.open(url, windowName, "popup=yes,width=1040,height=820,left=120,top=90,resizable=yes,scrollbars=yes");
  if (!win) showToast("Allow pop-ups to open conversations in a new window");
}

/* ============================================================
   events
   ============================================================ */
function handleFeatureClick(event) {
  const target = event.target;

  const serverPref = target.closest("[data-server-pref]");
  if (serverPref) {
    event.preventDefault();
    const key = serverPref.getAttribute("data-server-pref");
    const next = serverPref.getAttribute("aria-checked") !== "true";
    serverPref.setAttribute("aria-checked", next ? "true" : "false");
    patchPreferences({ [key]: next }, next ? "Preference enabled" : "Preference disabled");
    return true;
  }
  if (target.closest("#useDeviceZone")) {
    event.preventDefault();
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    patchPreferences({ timeZone: zone }, `Using ${zone}`);
    return true;
  }
  const timeFormat = target.closest("[data-time-format]");
  if (timeFormat) {
    event.preventDefault();
    patchPreferences({ timeFormat: timeFormat.getAttribute("data-time-format") });
    return true;
  }
  const workday = target.closest("[data-work-day]");
  if (workday) {
    event.preventDefault();
    const day = Number(workday.getAttribute("data-work-day"));
    const days = new Set(state.preferences.workDays || []);
    if (days.has(day)) days.delete(day); else days.add(day);
    if (!days.size) { showToast("Keep at least one working day"); return true; }
    workday.setAttribute("aria-pressed", days.has(day) ? "true" : "false");
    patchPreferences({ workDays: Array.from(days).sort() });
    return true;
  }
  if (target.closest("#viewRecoveryCodes")) { event.preventDefault(); openRecoveryCodes(); return true; }
  if (target.closest("#createAppPasswordBtn")) { event.preventDefault(); createAppPassword(); return true; }
  const copyAppToken = target.closest("[data-copy-app-token]");
  if (copyAppToken) { event.preventDefault(); copyText(copyAppToken.getAttribute("data-copy-app-token")); return true; }
  if (target.closest("#newIdentityBtn") || target.closest("#addIdentityBtn")) { event.preventDefault(); openIdentityEditor(); return true; }
  const resendIdentityButton = target.closest("[data-resend-identity]");
  if (resendIdentityButton) { event.preventDefault(); resendIdentity(resendIdentityButton.getAttribute("data-resend-identity")); return true; }
  const deleteIdentityButton = target.closest("[data-delete-identity]");
  if (deleteIdentityButton) { event.preventDefault(); deleteIdentity(deleteIdentityButton.getAttribute("data-delete-identity")); return true; }
  const editIdentityButton = target.closest("[data-edit-identity]");
  if (editIdentityButton) {
    event.preventDefault();
    const identity = (state.identities || []).find((item) => item.id === editIdentityButton.getAttribute("data-edit-identity")) ||
      (state.identities || []).find((item) => normalizeEmail(item.email) === normalizeEmail(editIdentityButton.getAttribute("data-identity-email")));
    openIdentityEditor(identity);
    return true;
  }
  if (target.closest("#newSignatureBtn") || target.closest("#signatureEmptyCreate")) { event.preventDefault(); openSignatureEditor(); return true; }
  const editSignatureButton = target.closest("[data-edit-signature]");
  if (editSignatureButton) {
    event.preventDefault();
    const signature = (state.signatures || []).find((item) => item.id === editSignatureButton.getAttribute("data-edit-signature"));
    openSignatureEditor(signature);
    return true;
  }
  const deleteSignatureButton = target.closest("[data-delete-signature]");
  if (deleteSignatureButton) { event.preventDefault(); deleteSignature(deleteSignatureButton.getAttribute("data-delete-signature")); return true; }
  const pickSignatureButton = target.closest("[data-insert-signature]");
  if (pickSignatureButton) { event.preventDefault(); insertPickedSignature(pickSignatureButton.getAttribute("data-insert-signature")); return true; }
  if (target.closest("#forwardingEnabled")) { event.preventDefault(); toggleSwitch(target.closest("#forwardingEnabled")); return true; }
  if (target.closest("#forwardingPermanent")) {
    event.preventDefault();
    const button = target.closest("#forwardingPermanent");
    toggleSwitch(button);
    const dates = document.getElementById("forwardingDates");
    if (dates) dates.hidden = button.getAttribute("aria-checked") === "true";
    return true;
  }
  if (target.closest("#forwardingKeepCopy")) { event.preventDefault(); toggleSwitch(target.closest("#forwardingKeepCopy")); return true; }
  if (target.closest("#saveForwardingBtn")) { event.preventDefault(); saveForwardingSettings(); return true; }
  if (target.closest("#saveProfileBtn")) { event.preventDefault(); saveProfile(); return true; }
  const removeAppPassword = target.closest("[data-remove-app-password]");
  if (removeAppPassword) { event.preventDefault(); revokeAppPassword(removeAppPassword.getAttribute("data-remove-app-password")); return true; }
  if (target.closest("#regenerateRecovery")) { event.preventDefault(); regenerateRecoveryCodes(); return true; }
  if (target.closest("#copyRecoveryCodes")) {
    event.preventDefault();
    const codes = $$("code", recoveryCodeList).map((code) => code.textContent).join("\n");
    if (codes) copyText(codes); else showToast("There are no recovery codes to copy");
    return true;
  }
  if (target.closest("#recoveryClose") || target === recoveryOverlay) { recoveryOverlay.hidden = true; return true; }

  if (target.closest("#autoReplyToggle")) {
    event.preventDefault();
    autoReplyToggle.setAttribute("aria-checked", autoReplyToggle.getAttribute("aria-checked") === "true" ? "false" : "true");
    updateAutoReplyPreview();
    return true;
  }
  if (target.closest("#autoReplyRangeToggle")) {
    event.preventDefault();
    const next = autoReplyRangeToggle.getAttribute("aria-checked") !== "true";
    autoReplyRangeToggle.setAttribute("aria-checked", next ? "true" : "false");
    autoReplyStart.disabled = !next;
    autoReplyEnd.disabled = !next;
    autoReplyRangeFields.classList.toggle("is-disabled", !next);
    updateAutoReplyPreview();
    return true;
  }
  if (target.closest("#autoReplyExternalToggle")) {
    event.preventDefault();
    const next = autoReplyExternalToggle.getAttribute("aria-checked") !== "true";
    autoReplyExternalToggle.setAttribute("aria-checked", next ? "true" : "false");
    document.querySelectorAll(".auto-reply-card")[1]?.classList.toggle("is-disabled", !next);
    updateAutoReplyPreview();
    return true;
  }
  if (target.closest("#saveAutoReplyBtn")) { event.preventDefault(); saveAutomaticReplies(); return true; }

  if (target.closest("#newTaskBtn") || target.closest("[data-new-task]")) {
    event.preventDefault();
    state.editingTaskId = "new";
    state.selectedTaskId = null;
    state.taskFilter = "all";
    state.query = "";
    searchInput.value = "";
    updateSearchClear();
    if (isMobile()) document.body.classList.add("task-detail-open");
    renderTasks();
    return true;
  }
  if (target.closest("#tasksRefreshBtn")) { event.preventDefault(); refreshTasks(); return true; }
  const taskFilter = target.closest("[data-task-filter]");
  if (taskFilter) {
    event.preventDefault();
    state.taskFilter = taskFilter.getAttribute("data-task-filter");
    renderTasks();
    return true;
  }
  const taskToggle = target.closest("[data-task-toggle]");
  if (taskToggle) { event.preventDefault(); toggleTask(taskToggle.getAttribute("data-task-toggle")); return true; }
  const taskEdit = target.closest("[data-task-edit]");
  if (taskEdit) {
    event.preventDefault();
    state.selectedTaskId = taskEdit.getAttribute("data-task-edit");
    state.editingTaskId = state.selectedTaskId;
    renderTasks();
    return true;
  }
  const taskDelete = target.closest("[data-task-delete]");
  if (taskDelete) { event.preventDefault(); deleteTask(taskDelete.getAttribute("data-task-delete")); return true; }
  const taskThread = target.closest("[data-task-open-thread]");
  if (taskThread) { event.preventDefault(); openTaskFromEmail(taskThread.getAttribute("data-task-open-thread")); return true; }
  if (target.closest("[data-task-editor-close]")) {
    event.preventDefault();
    state.editingTaskId = null;
    renderTasks();
    return true;
  }
  const taskRow = target.closest("[data-task-id]");
  if (taskRow) {
    event.preventDefault();
    state.selectedTaskId = taskRow.getAttribute("data-task-id");
    state.editingTaskId = null;
    if (isMobile()) document.body.classList.add("task-detail-open");
    renderTasks();
    return true;
  }
  if (target.closest("[data-task-back]")) {
    event.preventDefault();
    document.body.classList.remove("task-detail-open");
    return true;
  }

  if (target.closest("#connectStorageBtn") || target.closest("[data-open-storage]")) {
    event.preventDefault();
    openStorageDialog();
    return true;
  }
  if (target.closest("#storageClose") || target === storageOverlay) { storageOverlay.hidden = true; return true; }
  if (target.closest("#storageCredentialsClose") || target.closest("#storageCredentialsCancel") || target === storageCredentialsOverlay) { closeStorageCredentials(); return true; }
  const storageConnect = target.closest("[data-storage-connect]");
  if (storageConnect) { event.preventDefault(); connectStorage(storageConnect.getAttribute("data-storage-connect"), storageConnect); return true; }
  const storageConfigure = target.closest("[data-storage-configure]");
  if (storageConfigure) { event.preventDefault(); openStorageCredentials(storageConfigure.getAttribute("data-storage-configure")); return true; }
  const credentialProviderButton = target.closest("[data-credential-provider]");
  if (credentialProviderButton) { event.preventDefault(); state.storageCredentialProvider = credentialProviderButton.getAttribute("data-credential-provider"); renderStorageCredentials(); return true; }
  const copyCredentialURI = target.closest("[data-copy-credential-uri]");
  if (copyCredentialURI) { event.preventDefault(); copyText(copyCredentialURI.getAttribute("data-copy-credential-uri")); return true; }
  const storageDisconnect = target.closest("[data-storage-disconnect]");
  if (storageDisconnect) { event.preventDefault(); disconnectStorage(storageDisconnect.getAttribute("data-storage-disconnect")); return true; }
  const storageSync = target.closest("[data-storage-sync]");
  if (storageSync) { event.preventDefault(); syncStorage(storageSync.getAttribute("data-storage-sync")); return true; }
  if (target.closest("#uploadFileBtn")) { event.preventDefault(); fileUploadInput.click(); return true; }
  const fileFilter = target.closest("[data-file-filter]");
  if (fileFilter) { event.preventDefault(); state.fileFilter = fileFilter.getAttribute("data-file-filter"); renderFiles(); return true; }
  const fileStar = target.closest("[data-file-star]");
  if (fileStar) { event.preventDefault(); event.stopPropagation(); toggleFileStar(fileStar.getAttribute("data-file-star")); return true; }
  const filePreview = target.closest("[data-file-preview]");
  if (filePreview) {
    event.preventDefault();
    previewFileRecord(state.files.find((file) => file.id === filePreview.getAttribute("data-file-preview")));
    return true;
  }
  const fileDownload = target.closest("[data-file-download]");
  if (fileDownload) {
    event.preventDefault();
    const file = state.files.find((item) => item.id === fileDownload.getAttribute("data-file-download"));
    if (file && file.contentUrl) {
      const link = document.createElement("a");
      link.href = file.contentUrl;
      link.download = file.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
    } else {
      showToast("Download is not available for this demo file");
    }
    return true;
  }
  const fileShare = target.closest("[data-file-share]");
  if (fileShare) { event.preventDefault(); openShareDialog(fileShare.getAttribute("data-file-share")); return true; }
  const fileDelete = target.closest("[data-file-delete]");
  if (fileDelete) { event.preventDefault(); deleteFile(fileDelete.getAttribute("data-file-delete")); return true; }
  if (target.closest("[data-file-back]")) {
    event.preventDefault();
    document.body.classList.remove("file-open");
    return true;
  }
  const fileRow = target.closest(".file-row[data-file-id]");
  if (fileRow) {
    event.preventDefault();
    state.selectedFileId = fileRow.getAttribute("data-file-id");
    renderFiles();
    if (isMobile()) document.body.classList.add("file-open");
    return true;
  }

  if (target.closest("#shareClose") || target.closest("#shareCancel") || target === shareOverlay) {
    event.preventDefault();
    shareOverlay.hidden = true;
    return true;
  }
  if (target.closest("#createShare")) { event.preventDefault(); createShareLink(); return true; }
  if (target.closest("#copyShareLink")) {
    event.preventDefault();
    if (shareUrl.value) copyText(shareUrl.value);
    return true;
  }

  if (target.closest("#previewClose") || target === previewOverlay) { event.preventDefault(); closePreview(); return true; }
  const zoom = target.closest("[data-preview-zoom]");
  if (zoom) {
    event.preventDefault();
    const action = zoom.getAttribute("data-preview-zoom");
    if (action === "in") setPreviewZoom(state.previewZoom + 0.25);
    else if (action === "out") setPreviewZoom(state.previewZoom - 0.25);
    else setPreviewZoom(1);
    return true;
  }

  if (target.closest("#reportClose") || target.closest("#reportCancel") || target === reportOverlay) {
    event.preventDefault();
    closeReportDialog();
    return true;
  }
  if (target.closest("#reportSubmit")) {
    event.preventDefault();
    submitSpamReport();
    return true;
  }

  if (target.closest("#scheduleClose") || target.closest("#scheduleCancel") || target === scheduleOverlay) {
    event.preventDefault();
    closeScheduleDialog();
    return true;
  }
  const schedulePreset = target.closest("[data-schedule-preset]");
  if (schedulePreset) {
    event.preventDefault();
    const id = schedulePreset.getAttribute("data-schedule-preset");
    selectSchedulePreset(id);
    if (id === "custom") setTimeout(() => scheduleDate.focus(), 40);
    return true;
  }
  if (target.closest("#scheduleSendNow")) {
    event.preventDefault();
    const session = scheduleSession;
    closeScheduleDialog();
    if (session) sendComposeSession(session.id);
    return true;
  }
  if (target.closest("#scheduleConfirm")) { event.preventDefault(); confirmScheduledSend(); return true; }
  if (target.closest("#scheduleEditRegion")) {
    event.preventDefault();
    closeScheduleDialog();
    openSettings("regional");
    showToast("Adjust the time zone, then reopen Schedule send");
    return true;
  }
  if (target.closest("#scheduledChip")) {
    event.preventDefault();
    openStatusPop("scheduled");
    return true;
  }

  if (target.closest("[data-open-scheduled]")) {
    event.preventDefault();
    closeStatusPop();
    setTask("scheduled");
    return true;
  }
  if (target.closest("#scheduledBack")) { event.preventDefault(); setTask("mail"); return true; }
  if (target.closest("#scheduledRefresh")) { event.preventDefault(); refreshScheduled(); return true; }
  if (target.closest("#scheduledCompose") || target.closest("[data-scheduled-compose]")) {
    event.preventDefault();
    openCompose();
    return true;
  }
  const scheduledSendNow = target.closest("[data-scheduled-send-now]");
  if (scheduledSendNow) { event.preventDefault(); openSendNowDialog(scheduledSendNow.getAttribute("data-scheduled-send-now")); return true; }
  const scheduledEdit = target.closest("[data-scheduled-edit]");
  if (scheduledEdit) { event.preventDefault(); openScheduledEdit(scheduledEdit.getAttribute("data-scheduled-edit")); return true; }
  const scheduledCancel = target.closest("[data-scheduled-cancel]");
  if (scheduledCancel) { event.preventDefault(); cancelQueuedSend(scheduledCancel.getAttribute("data-scheduled-cancel")); return true; }

  if (target.closest("#scheduledEditClose") || target.closest("#scheduledEditCancel") || target === scheduledEditOverlay) {
    event.preventDefault();
    closeScheduledEdit();
    return true;
  }
  const editPreset = target.closest("[data-scheduled-edit-preset]");
  if (editPreset) {
    event.preventDefault();
    const preset = (state.scheduledEditPresets || []).find((entry) => entry.id === editPreset.getAttribute("data-scheduled-edit-preset"));
    if (preset) {
      scheduledEditDate.value = preset.date;
      scheduledEditTime.value = preset.time;
      updateScheduledEditInsight();
    }
    return true;
  }
  if (target.closest("#scheduledEditSave")) { event.preventDefault(); saveScheduledEdit(); return true; }
  if (target.closest("#sendNowClose") || target.closest("#sendNowCancel") || target === sendNowOverlay) {
    event.preventDefault();
    closeSendNowDialog();
    return true;
  }
  if (target.closest("#sendNowConfirm")) { event.preventDefault(); confirmSendNow(); return true; }

  return false;
}

function applySignatureRichCommand(editor, command, value = null) {
  if (!editor) return;
  editor.focus();
  try { document.execCommand("styleWithCSS", false, true); } catch {}
  if (command === "createLink") {
    const href = window.prompt("Link URL", "https://");
    if (!href || !/^(https?:\/\/|mailto:)/i.test(href.trim())) return;
    document.execCommand("createLink", false, href.trim());
    return;
  }
  if (command === "formatBlock") {
    document.execCommand("formatBlock", false, value || "p");
    return;
  }
  document.execCommand(command, false, value);
}

function bindEvents() {
  document.addEventListener("click", (event) => {
    const target = event.target;

    if (handleFeatureClick(event)) return;

    // Compose layer is handled separately.
    const composeTabClose = target.closest("[data-compose-tab-close]");
    if (composeTabClose) {
      event.stopPropagation();
      closeComposeSession(composeTabClose.getAttribute("data-compose-tab-close"));
      return;
    }
    const composeTab = target.closest("[data-compose-tab]");
    if (composeTab) {
      event.stopPropagation();
      const session = sessionById(composeTab.getAttribute("data-compose-tab"));
      if (!session) return;
      if (session.mode === "minimized") restoreComposeSession(session.id);
      else { setActiveCompose(session.id); focusComposeField(session); }
      return;
    }

    const searchOp = target.closest("[data-search-op]");
    if (searchOp) { event.preventDefault(); insertSearchOperator(searchOp.getAttribute("data-search-op")); return; }
    const searchExample = target.closest("[data-search-example]");
    if (searchExample) {
      event.preventDefault();
      searchInput.value = searchExample.getAttribute("data-search-example");
      updateSearchClear();
      setQuery(searchInput.value);
      searchHints.hidden = true;
      return;
    }

    const rowCheck = target.closest(".row-check");
    if (rowCheck) {
      event.stopPropagation();
      return;
    }
    const threadPin = target.closest("[data-thread-pin]");
    if (threadPin) { event.stopPropagation(); event.preventDefault(); toggleThreadPin(threadById(threadPin.getAttribute("data-thread-pin"))); return; }
    const threadStar = target.closest("[data-thread-star]");
    if (threadStar) { event.stopPropagation(); event.preventDefault(); toggleThreadStar(threadStar.getAttribute("data-thread-star")); return; }
    if (target.closest("[data-close-reader]")) { event.preventDefault(); closeReaderPane(); return; }
    if (target.closest("[data-reader-prev]")) { event.preventDefault(); navigateThread(-1); return; }
    if (target.closest("[data-reader-next]")) { event.preventDefault(); navigateThread(1); return; }
    if (target.closest("[data-reader-inbox]")) { event.preventDefault(); performBulkAction("notSpam", null, { confirmed: true, keepRules: true }); return; }
    const threadStarReader = target.closest("[data-thread-star-reader]");
    if (threadStarReader) { event.preventDefault(); toggleThreadStar(threadStarReader.getAttribute("data-thread-star-reader")); return; }
    const messageStar = target.closest("[data-message-star]");
    if (messageStar) { event.preventDefault(); toggleMessageStar(messageStar.getAttribute("data-message-star")); return; }
    const toggleMessage = target.closest("[data-toggle-message]");
    if (toggleMessage) {
      event.preventDefault();
      const messageId = toggleMessage.getAttribute("data-toggle-message");
      if (state.expanded.has(messageId)) state.expanded.delete(messageId); else state.expanded.add(messageId);
      renderReader({ preserveScroll: true });
      return;
    }
    const detailsButton = target.closest("[data-details]");
    if (detailsButton) { event.preventDefault(); openDetails(detailsButton.getAttribute("data-details")); return; }
    const detailsTab = target.closest("[data-details-tab]");
    if (detailsTab) { event.preventDefault(); selectDetailsTab(detailsTab.getAttribute("data-details-tab")); return; }
    if (target.closest("[data-copy-headers]")) { event.preventDefault(); copyDetailsHeaders(); return; }
    if (target.closest("[data-download-source]")) { event.preventDefault(); downloadDetailsSource(); return; }
    const trustSender = target.closest("[data-trust-sender]");
    if (trustSender) { event.preventDefault(); toggleTrust(trustSender.getAttribute("data-trust-sender"), true); return; }
    const trustToggle = target.closest("[data-trust-toggle]");
    if (trustToggle) {
      event.preventDefault();
      toggleTrust(trustToggle.getAttribute("data-trust-toggle"), trustToggle.getAttribute("data-trust-value") === "true");
      return;
    }
    const showImagesButton = target.closest("[data-show-images]");
    if (showImagesButton) {
      event.preventDefault();
      state.imageOverrides.add(showImagesButton.getAttribute("data-show-images"));
      renderReader({ preserveScroll: true });
      return;
    }
    const reportNotSpamButton = target.closest("[data-report-not-spam]");
    if (reportNotSpamButton) {
      event.preventDefault();
      reportNotSpamButton.disabled = true;
      reportNotSpamMessage(reportNotSpamButton.getAttribute("data-report-not-spam"));
      return;
    }
    const moveInboxButton = target.closest("[data-move-inbox]");
    if (moveInboxButton) {
      event.preventDefault();
      moveInboxButton.disabled = true;
      moveSpamMessageToInbox(moveInboxButton.getAttribute("data-move-inbox"));
      return;
    }
    const attachmentMessageId = target.closest("[data-attachment-message]")?.getAttribute("data-attachment-message");
    if (attachmentMessageId && messageById(attachmentMessageId)?.folder === "spam") {
      event.preventDefault();
      showToast("Attachments in Junk are blocked. Move this message to Inbox first.", "error");
      return;
    }
    const saveAllAttachments = target.closest("[data-save-all-attachments]");
    if (saveAllAttachments) { event.preventDefault(); saveAllAttachmentsToCabinet(saveAllAttachments.getAttribute("data-save-all-attachments")); return; }
    const saveAttachment = target.closest("[data-save-attachment]");
    if (saveAttachment) { event.preventDefault(); saveAttachmentToCabinet(saveAttachment.getAttribute("data-save-attachment"), saveAttachment.getAttribute("data-save-attachment-name")); return; }
    const attachment = target.closest("[data-attachment]");
    if (attachment) {
      openPreview({
        name: attachment.getAttribute("data-attachment"),
        id: attachment.getAttribute("data-file-id"),
        type: attachment.getAttribute("data-file-type"),
        size: attachment.getAttribute("data-file-size"),
        mime: attachment.getAttribute("data-file-mime"),
        contentUrl: attachment.getAttribute("data-file-url"),
        previewable: attachment.getAttribute("data-file-previewable") === "true"
      });
      return;
    }

    const taskButton = target.closest("button[data-task]");
    if (taskButton) { event.preventDefault(); setTask(taskButton.getAttribute("data-task")); return; }
    const folderButton = target.closest("button[data-folder]");
    if (folderButton) { event.preventDefault(); setFolder(folderButton.getAttribute("data-folder")); return; }
    const filterButton = target.closest("button[data-filter]");
    if (filterButton) { event.preventDefault(); setFilter(filterButton.getAttribute("data-filter")); return; }
    const clearFilters = target.closest("[data-clear-filters]");
    if (clearFilters) { clearSearch(); state.filter = "all"; syncChips(); renderList(); return; }
    const searchClearInline = target.closest("#searchClearInline");
    if (searchClearInline) { event.preventDefault(); clearSearch(); return; }

    const bulkAction = target.closest("[data-bulk-action]");
    if (bulkAction) {
      event.preventDefault();
      const action = bulkAction.getAttribute("data-bulk-action");
      if (action === "clear") clearSelection();
      else performBulkAction(action);
      return;
    }
    const bulkMove = target.closest("[data-bulk-move]");
    if (bulkMove) {
      event.preventDefault();
      state.bulkMoveOpen = !state.bulkMoveOpen;
      const menu = document.getElementById("bulkMoveMenu");
      if (menu) {
        menu.innerHTML = (state.folders || []).filter((folder) => !folder.virtual && !folder.delegated).map((folder) =>
          `<button type="button" data-bulk-move-folder="${esc(folder.id)}"><svg class="icon" aria-hidden="true"><use href="#${esc(folder.icon || "i-folder")}"/></svg>${esc(folder.label)}</button>`
        ).join("");
        menu.hidden = !state.bulkMoveOpen;
      }
      return;
    }
    const bulkMoveFolder = target.closest("[data-bulk-move-folder]");
    if (bulkMoveFolder) {
      event.preventDefault();
      closeMenus();
      performBulkAction("move", bulkMoveFolder.getAttribute("data-bulk-move-folder"));
      return;
    }

    const row = target.closest(".msg[data-thread-id]");
    if (row) {
      if (event.metaKey || event.ctrlKey) { toggleThreadSelection(row.getAttribute("data-thread-id")); return; }
      if (event.shiftKey) { extendSelectionTo(row.getAttribute("data-thread-id")); return; }
      selectThread(row.getAttribute("data-thread-id"));
      return;
    }

    const action = target.closest("[data-action]");
    if (action) {
      if (action.classList.contains("is-disabled")) return;
      event.preventDefault();
      if (state.selectedIds.size && ["archive", "delete", "mark-unread"].includes(action.getAttribute("data-action"))) {
        performBulkAction(action.getAttribute("data-action") === "mark-unread" ? "markUnread" : action.getAttribute("data-action"));
      } else {
        readerAction(action.getAttribute("data-action"));
      }
      return;
    }

    const replyMessage = target.closest("[data-reply-message]");
    if (replyMessage) { event.preventDefault(); replyToMessage(messageById(replyMessage.getAttribute("data-reply-message"))); return; }
    const forwardMessageButton = target.closest("[data-forward-message]");
    if (forwardMessageButton) { event.preventDefault(); forwardMessage(messageById(forwardMessageButton.getAttribute("data-forward-message"))); return; }
    const replyThread = target.closest("[data-reply-thread]");
    if (replyThread) { event.preventDefault(); const thread = currentThread(); if (thread) replyToMessage(replyTarget(thread)); return; }
    const forwardThread = target.closest("[data-forward-thread]");
    if (forwardThread) { event.preventDefault(); const thread = currentThread(); if (thread) forwardMessage(thread.latest); return; }

    const expandAll = target.closest("#readerExpandAll, #threadExpandAll");
    if (expandAll) {
      event.preventDefault();
      const thread = currentThread();
      if (thread) { state.expanded = new Set(thread.messages.map((message) => message.id)); renderReader({ preserveScroll: true }); }
      return;
    }
    const collapseAll = target.closest("#readerCollapseAll, #threadCollapseAll");
    if (collapseAll) {
      event.preventDefault();
      const thread = currentThread();
      if (thread) { state.expanded = new Set(); renderReader({ preserveScroll: true }); }
      return;
    }

    const contact = target.closest("[data-contact]");
    if (contact) { event.preventDefault(); state.contactEmail = contact.getAttribute("data-contact"); renderPeople(); if (isMobile()) document.body.classList.add("person-open"); return; }
    const editContact = target.closest("[data-edit-contact]");
    if (editContact) { event.preventDefault(); openContactDialog(editContact.getAttribute("data-edit-contact")); return; }
    const deleteContact = target.closest("[data-delete-contact]");
    if (deleteContact) { event.preventDefault(); deleteContactByEmail(deleteContact.getAttribute("data-delete-contact")); return; }
    const messageContact = target.closest("[data-message-contact]");
    if (messageContact) { event.preventDefault(); openCompose({ toText: messageContact.getAttribute("data-message-contact") }); return; }
    const copyEmail = target.closest("[data-copy-email]");
    if (copyEmail) { event.preventDefault(); copyText(copyEmail.getAttribute("data-copy-email")); return; }
    const recent = target.closest("[data-recent]");
    if (recent) { event.preventDefault(); openMessageFromPeople(recent.getAttribute("data-recent")); return; }

    const rsvp = target.closest("[data-rsvp]");
    if (rsvp) {
      const message = messageById(rsvp.getAttribute("data-rsvp-message"));
      if (message && message.event) {
        message.event.response = rsvp.getAttribute("data-rsvp");
        renderReader({ preserveScroll: true });
        showToast(`${message.event.response === "accept" ? "Accepted" : message.event.response === "tentative" ? "Marked tentative" : "Declined"} — response sent`);
      }
      return;
    }

    const peopleFilter = target.closest("[data-people-filter]");
    if (peopleFilter) {
      event.preventDefault();
      state.peopleFilter = peopleFilter.getAttribute("data-people-filter") || "all";
      renderPeople();
      return;
    }
    const calDate = target.closest("[data-cal-date]");
    if (calDate) { event.preventDefault(); selectCalendarDate(calDate.getAttribute("data-cal-date")); return; }
    const editEvent = target.closest("[data-edit-event]");
    if (editEvent) { event.preventDefault(); openEventDialog(editEvent.getAttribute("data-edit-event")); return; }
    const newItem = target.closest("[data-new]");
    if (newItem) {
      event.preventDefault();
      closeMenus();
      const kind = newItem.getAttribute("data-new");
      if (kind === "message") openCompose();
      else if (kind === "event") { setTask("calendar"); openEventDialog(); }
      return;
    }
    const moveFolder = target.closest("[data-move-folder]");
    if (moveFolder) {
      event.preventDefault();
      closeMenus();
      performBulkAction("move", moveFolder.getAttribute("data-move-folder"));
      return;
    }
    const notifyItem = target.closest("[data-notify-id]");
    if (notifyItem) { event.preventDefault(); handleNotificationAction(notifyItem.getAttribute("data-notify-id")); return; }
    const noticeOpen = target.closest("[data-notice-open]");
    if (noticeOpen) { event.preventDefault(); handleNotificationAction(noticeOpen.getAttribute("data-notice-open")); return; }
    const noticeRead = target.closest("[data-notice-read]");
    if (noticeRead) { event.preventDefault(); markNotificationRead(noticeRead.getAttribute("data-notice-read")); return; }
    const revokeButton = target.closest("[data-revoke-session]");
    if (revokeButton) { event.preventDefault(); revokeSession(revokeButton.getAttribute("data-revoke-session")); return; }
    const removePasskeyButton = target.closest("[data-remove-passkey]");
    if (removePasskeyButton) { event.preventDefault(); removePasskey(removePasskeyButton.getAttribute("data-remove-passkey")); return; }
    const settingsTab = target.closest("[data-settings-tab]");
    if (settingsTab) { event.preventDefault(); showSettingsTab(settingsTab.getAttribute("data-settings-tab")); return; }
    const prefSwitch = target.closest("[data-pref]");
    if (prefSwitch) {
      event.preventDefault();
      const key = prefSwitch.getAttribute("data-pref");
      setPreference(key, prefSwitch.getAttribute("aria-checked") !== "true", true);
      showToast("Preference saved");
      return;
    }
    const themeOption = target.closest("[data-set-theme]");
    if (themeOption) { event.preventDefault(); setTheme(themeOption.getAttribute("data-set-theme"), true); return; }
    const accentOption = target.closest("[data-set-accent]");
    if (accentOption) { event.preventDefault(); setAccent(accentOption.getAttribute("data-set-accent"), true); return; }
    const densityOption = target.closest("[data-set-density]");
    if (densityOption) { event.preventDefault(); setDensity(densityOption.getAttribute("data-set-density"), true); return; }

    const ruleToggle = target.closest("[data-rule-toggle]");
    if (ruleToggle) {
      event.preventDefault();
      const rule = state.sieve.rules.find((item) => item.id === ruleToggle.getAttribute("data-rule-toggle"));
      if (rule) { rule.enabled = !rule.enabled; saveSieve(state.sieve.rules, state.sieve.vacation, rule.enabled ? "Rule enabled" : "Rule disabled"); }
      return;
    }
    const ruleEdit = target.closest("[data-rule-edit]");
    if (ruleEdit) { event.preventDefault(); const rule = state.sieve.rules.find((item) => item.id === ruleEdit.getAttribute("data-rule-edit")); if (rule) openRuleEditor(rule); return; }
    const ruleDelete = target.closest("[data-rule-delete]");
    if (ruleDelete) {
      event.preventDefault();
      const id = ruleDelete.getAttribute("data-rule-delete");
      appConfirm({ title: "Delete this rule?", message: "The Sieve rule will stop filtering incoming mail immediately.", confirmLabel: "Delete rule", tone: "danger" }).then((ok) => {
        if (ok) saveSieve(state.sieve.rules.filter((item) => item.id !== id), state.sieve.vacation, "Rule deleted");
      });
      return;
    }
    const ruleUp = target.closest("[data-rule-up]");
    if (ruleUp) {
      event.preventDefault();
      const id = ruleUp.getAttribute("data-rule-up");
      const rules = state.sieve.rules.slice().sort((a, b) => (a.priority || 0) - (b.priority || 0));
      const index = rules.findIndex((rule) => rule.id === id);
      if (index > 0) { [rules[index - 1], rules[index]] = [rules[index], rules[index - 1]]; rules.forEach((rule, i) => { rule.priority = i + 1; }); saveSieve(rules, state.sieve.vacation, "Rule order updated"); }
      return;
    }
    const ruleDown = target.closest("[data-rule-down]");
    if (ruleDown) {
      event.preventDefault();
      const id = ruleDown.getAttribute("data-rule-down");
      const rules = state.sieve.rules.slice().sort((a, b) => (a.priority || 0) - (b.priority || 0));
      const index = rules.findIndex((rule) => rule.id === id);
      if (index >= 0 && index < rules.length - 1) { [rules[index + 1], rules[index]] = [rules[index], rules[index + 1]]; rules.forEach((rule, i) => { rule.priority = i + 1; }); saveSieve(rules, state.sieve.vacation, "Rule order updated"); }
      return;
    }
    const addCondition = target.closest("#addConditionBtn");
    if (addCondition) {
      event.preventDefault();
      const conditions = document.getElementById("ruleConditions");
      const index = conditions.children.length;
      conditions.insertAdjacentHTML("beforeend", conditionRowMarkup({ field: "from", operator: "contains", value: "" }, index));
      return;
    }
    const removeCondition = target.closest("[data-remove-condition]");
    if (removeCondition) {
      event.preventDefault();
      if ($$("#ruleConditions .rule-condition").length > 1) removeCondition.closest(".rule-condition").remove();
      return;
    }
    const addRule = target.closest("#addRuleBtn");
    if (addRule) { event.preventDefault(); openRuleEditor(); return; }
    const cancelRule = target.closest("#cancelRuleBtn");
    if (cancelRule) { event.preventDefault(); document.getElementById("ruleEditorBlock").hidden = true; editingRuleId = null; return; }
    const refreshRulesButton = target.closest("#refreshRulesBtn");
    if (refreshRulesButton) { event.preventDefault(); refreshRules(); return; }
    const refreshAudit = target.closest("#refreshAuditBtn");
    if (refreshAudit) { event.preventDefault(); renderAudit(); return; }
    const exportAuditButton = target.closest("#exportAuditBtn");
    if (exportAuditButton) { event.preventDefault(); exportAudit(); return; }

    const statusRefresh = target.closest("[data-status-refresh]");
    if (statusRefresh) return;
    const cancelSend = target.closest("[data-cancel-send]");
    if (cancelSend) return;
    const statusChip = target.closest("#pendingChip, #outboundChip, #quotaButtonStatus");
    if (statusChip) {
      event.preventDefault();
      openStatusPop(statusChip.id === "pendingChip" ? "pending" : statusChip.id === "outboundChip" ? "outbound" : "quota");
      return;
    }
    if (target.closest("#quotaButton")) { event.preventDefault(); openStatusPop("quota"); return; }
    if (target.closest("#statusPopClose")) { event.preventDefault(); closeStatusPop(); return; }

    if (target.closest("#scrim")) { closeDrawer(); return; }
    if (target === helpOverlay) { closeHelp(); return; }
  });

  messageList.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const row = event.target.closest(".msg[data-thread-id]");
    if (!row) return;
    event.preventDefault();
    selectThread(row.getAttribute("data-thread-id"));
  });

  messageList.addEventListener("change", (event) => {
    const check = event.target.closest("[data-thread-check]");
    if (!check) return;
    toggleThreadSelection(check.getAttribute("data-thread-check"), check.checked);
  });

  messageList.addEventListener("dblclick", (event) => {
    const row = event.target.closest(".msg[data-thread-id]");
    if (!row) return;
    if (event.target.closest(".row-check, .star-btn")) return;
    event.preventDefault();
    openThreadWindow(row.getAttribute("data-thread-id"));
  });

  if (selectAll) selectAll.addEventListener("change", () => selectAllVisible(selectAll.checked));

  searchInput.addEventListener("input", () => {
    state.query = searchInput.value.trim();
    updateSearchClear();
    if (state.task === "people") renderPeople();
    else if (state.task === "tasks") renderTasks();
    else if (state.task === "files") renderFiles();
    else if (state.task === "scheduled") renderScheduled();
    else if (state.task === "mail") {
      state.selectedIds.clear();
      state.expandedThreadId = null;
      state.selectedThreadId = null;
      state.searchResults = [];
      state.searchParsed = state.query ? parseQuery(state.query) : null;
      state.searchErrors = [];
      renderList();
      renderReader();
      syncSelectionUi();
      scheduleServerSearch();
      openSearchHints();
    }
  });
  searchInput.addEventListener("focus", openSearchHints);
  searchInput.addEventListener("blur", closeSearchHints);
  searchClear.addEventListener("click", () => { clearSearch(); searchInput.focus(); });
  document.getElementById("searchBack").addEventListener("click", () => { closeMobileSearch(); clearSearch(); });
  document.getElementById("openSearch").addEventListener("click", focusSearch);
  $("#searchHints").addEventListener("mousedown", (event) => event.preventDefault());
  document.addEventListener("click", (event) => {
    if (!event.target.closest("#searchWrap")) { searchHints.hidden = true; }
    if (!event.target.closest("#statusPop") && !event.target.closest("#pendingChip") && !event.target.closest("#outboundChip") && !event.target.closest("#scheduledChip") && !event.target.closest("#quotaButtonStatus") && !event.target.closest("#quotaButton")) closeStatusPop();
    if (!event.target.closest("#notifyPop") && !event.target.closest("#notifyBtn")) hideNotify();
    if (!event.target.closest("#newMenu") && !event.target.closest("#cmdNewMenuBtn")) newMenu.hidden = true;
    if (!event.target.closest("#moveMenu") && !event.target.closest("#cmdMove")) moveMenu.hidden = true;
    if (!event.target.closest("#bulkMoveMenu") && !event.target.closest("[data-bulk-move]")) {
      state.bulkMoveOpen = false;
      const menu = document.getElementById("bulkMoveMenu");
      if (menu) menu.hidden = true;
    }
  });

  moveMenu?.addEventListener("click", (event) => {
    event.stopPropagation();
    if (event.target.closest("[data-move-close]")) { closeMenus(); return; }
    if (event.target.closest("[data-move-rule-toggle]")) {
      state.moveRuleMode = !state.moveRuleMode;
      renderMoveMenu();
      moveMenu.querySelector("[data-move-search]")?.focus();
      return;
    }
    if (event.target.closest("[data-move-create]")) { createFolderFromMoveMenu(); return; }
    const target = event.target.closest("[data-move-target]");
    if (target) chooseMoveTarget(target.getAttribute("data-move-target"));
  });
  moveMenu?.addEventListener("input", (event) => {
    if (!event.target.matches("[data-move-search]")) return;
    state.moveSearch = event.target.value;
    const list = moveMenu.querySelector("[data-move-list]");
    if (list) list.innerHTML = moveMenuListMarkup();
  });
  document.getElementById("folderAddBtn")?.addEventListener("click", () => openFolderEditor("create"));
  folderMenu?.addEventListener("click", (event) => {
    event.stopPropagation();
    if (event.target.closest("[data-folder-submenu-back]")) {
      openFolderMenu(state.folderMenuTarget, { clientX: parseInt(folderMenu.style.left, 10) || 120, clientY: parseInt(folderMenu.style.top, 10) || 120 });
      return;
    }
    const color = event.target.closest("[data-folder-color]");
    if (color) { const folder = folderMenuFolder(); if (folder) patchFolder(folder.id, { color: color.getAttribute("data-folder-color") }, "Folder color saved"); return; }
    const badgeMode = event.target.closest("[data-folder-badge-mode]");
    if (badgeMode) { const folder = folderMenuFolder(); if (folder) patchFolder(folder.id, { badgeMode: badgeMode.getAttribute("data-folder-badge-mode") }, "Folder count setting saved"); return; }
    const parent = event.target.closest("[data-folder-parent-id]");
    if (parent) { const folder = folderMenuFolder(); if (folder) { patchFolder(folder.id, { parentId: parent.getAttribute("data-folder-parent-id") }, "Folder moved"); } return; }
    const action = event.target.closest("[data-folder-action]");
    if (action) { event.preventDefault(); folderMenuAction(action.getAttribute("data-folder-action")); }
  });
  document.addEventListener("click", (event) => {
    const more = event.target.closest("[data-folder-menu]");
    if (more) {
      event.preventDefault();
      event.stopPropagation();
      openFolderMenu(more.getAttribute("data-folder-menu"), event);
      return;
    }
    if (!event.target.closest("#folderMenu")) closeFolderMenu();
  });
  document.addEventListener("contextmenu", (event) => {
    const row = event.target.closest("[data-folder-row]");
    if (!row) return;
    event.preventDefault();
    openFolderMenu(row.getAttribute("data-folder-row"), { clientX: event.clientX, clientY: event.clientY });
  });
  folderEditorOverlay?.addEventListener("click", (event) => {
    if (event.target === folderEditorOverlay || event.target.closest("#folderEditorClose") || event.target.closest("#folderEditorCancel")) {
      closeFolderEditor();
      return;
    }
    if (event.target.closest("#folderEditorSave")) saveFolderEditor();
  });
  document.getElementById("folderToggle").addEventListener("click", openDrawer);
  document.getElementById("drawerClose").addEventListener("click", closeDrawer);
  document.getElementById("cmdCompose").addEventListener("click", () => openCompose());
  document.getElementById("drawerCompose").addEventListener("click", () => { closeDrawer(); openCompose(); });
  document.getElementById("composeFab").addEventListener("click", () => openCompose());
  document.getElementById("sortBtn").addEventListener("click", () => {
    state.sortDesc = !state.sortDesc;
    const button = document.getElementById("sortBtn");
    button.title = state.sortDesc ? "Sort newest first" : "Sort oldest first";
    button.setAttribute("aria-label", button.title);
    renderList();
    showToast(state.sortDesc ? "Sorted newest first" : "Sorted oldest first");
  });
  document.getElementById("themeBtn").addEventListener("click", () => setTheme(effectiveTheme() === "dark" ? "light" : "dark", true));
  document.getElementById("helpBtn").addEventListener("click", openHelp);
  document.getElementById("railHelp").addEventListener("click", openHelp);
  document.getElementById("helpClose").addEventListener("click", closeHelp);
  document.getElementById("settingsBtn").addEventListener("click", () => openSettings());
  document.getElementById("accountBtn").addEventListener("click", () => openSettings("account"));
  document.getElementById("settingsDone").addEventListener("click", () => setTask("mail"));
  document.getElementById("railLogout").addEventListener("click", logout);
  document.getElementById("signOutOtherSessions")?.addEventListener("click", revokeOtherSessions);
  document.getElementById("signOutSettings")?.addEventListener("click", signOutCurrent);
  document.getElementById("readerBack").addEventListener("click", () => {
    if (document.body.classList.contains("popout")) { window.close(); return; }
    document.body.classList.remove("reader-open");
  });
  document.getElementById("readerPopout").addEventListener("click", () => {
    if (document.body.classList.contains("popout")) { window.close(); return; }
    if (state.selectedThreadId) openThreadWindow(state.selectedThreadId);
  });
  document.getElementById("personBack").addEventListener("click", () => document.body.classList.remove("person-open"));

  document.getElementById("appPasswordForm")?.addEventListener("submit", (event) => event.preventDefault());
  document.getElementById("notifyBtn").addEventListener("click", toggleNotify);
  document.getElementById("notifyClose").addEventListener("click", hideNotify);
  document.getElementById("notifyViewAll")?.addEventListener("click", () => openNoticeCenter("all"));
  document.getElementById("notifyMarkAll")?.addEventListener("click", markAllNotificationsRead);
  document.getElementById("noticeMarkAll")?.addEventListener("click", markAllNotificationsRead);
  document.getElementById("noticeClose")?.addEventListener("click", closeNoticeCenter);
  document.getElementById("noticeDone")?.addEventListener("click", closeNoticeCenter);
  aiReaderToggles.forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      toggleReaderAiMenu(button.closest("[data-ai-wrap]"));
    });
  });
  window.addEventListener("resize", closeReaderAiMenus);
  document.addEventListener("click", (event) => {
    if (event.target.closest("[data-ai-reader-close]")) { closeReaderAi(); return; }
    const aiAction = event.target.closest("[data-ai-action]");
    if (aiAction) {
      event.preventDefault();
      runReaderAi(aiAction.getAttribute("data-ai-action") === "translate" ? "translation" : "summary");
      return;
    }
    if (aiReaderMenus.some((menu) => !menu.hidden) && !event.target.closest("[data-ai-wrap]")) closeReaderAiMenus();
  });
  noticeLayer?.addEventListener("click", (event) => {
    if (event.target === noticeLayer) closeNoticeCenter();
    const filter = event.target.closest("[data-notice-filter]");
    if (filter) {
      $$("[data-notice-filter]").forEach((button) => {
        const active = button === filter;
        button.classList.toggle("is-active", active);
        button.setAttribute("aria-selected", active ? "true" : "false");
      });
      state.noticeFilter = filter.getAttribute("data-notice-filter");
      renderNoticeCenter();
    }
  });
  document.getElementById("cmdNewMenuBtn").addEventListener("click", (event) => {
    event.stopPropagation();
    moveMenu.hidden = true;
    if (newMenu.hidden) positionMenu(newMenu, event.currentTarget.closest(".cmd-split") || event.currentTarget);
    else newMenu.hidden = true;
  });
  document.getElementById("cmdMove").addEventListener("click", (event) => {
    if (!currentThread() && !state.selectedIds.size) return;
    event.stopPropagation();
    newMenu.hidden = true;
    if (!moveMenu.hidden) { moveMenu.hidden = true; return; }
    openMoveMenu(event.currentTarget);
  });
  document.getElementById("cmdRefresh").addEventListener("click", () => {
    refreshMailbox({ quiet: false }).catch(showError);
  });

  document.getElementById("calPrev").addEventListener("click", () => shiftMonth(-1));
  document.getElementById("calNext").addEventListener("click", () => shiftMonth(1));
  document.getElementById("calToday").addEventListener("click", () => {
    const today = new Date();
    state.calendarYear = today.getFullYear();
    state.calendarMonth = today.getMonth();
    state.calendarSelected = dateKey(today);
    renderCalendar();
  });
  document.getElementById("newContactBtn")?.addEventListener("click", () => openContactDialog());
  document.getElementById("peopleSearch")?.addEventListener("input", (event) => {
    state.peopleQuery = event.target.value;
    renderPeople();
  });
  document.getElementById("confirmAccept")?.addEventListener("click", () => settleConfirm(true));
  document.getElementById("confirmCancel")?.addEventListener("click", () => settleConfirm(false));
  confirmOverlay?.addEventListener("click", (event) => { if (event.target === confirmOverlay) settleConfirm(false); });
  document.getElementById("domainBlockedClose")?.addEventListener("click", () => { domainBlockedOverlay.hidden = true; });
  domainBlockedOverlay?.addEventListener("click", (event) => { if (event.target === domainBlockedOverlay) domainBlockedOverlay.hidden = true; });
  storageCredentialsOverlay?.addEventListener("click", (event) => { if (event.target === storageCredentialsOverlay) closeStorageCredentials(); });
  document.getElementById("manageStorageCredentialsBtn")?.addEventListener("click", () => openStorageCredentials());
  document.getElementById("settingsManageStorageBtn")?.addEventListener("click", () => openStorageCredentials());
  document.getElementById("settingsConnectStorageBtn")?.addEventListener("click", openStorageDialog);
  document.getElementById("storageCredentialsSave")?.addEventListener("click", saveStorageCredentials);
  document.getElementById("storageCredentialsClear")?.addEventListener("click", clearStorageCredentials);
  document.getElementById("defaultStorageSelect")?.addEventListener("change", (event) => setDefaultStorage(event.target.value));
  document.getElementById("settingsDefaultStorage")?.addEventListener("change", (event) => setDefaultStorage(event.target.value));
  document.getElementById("contactClose")?.addEventListener("click", closeContactDialog);
  document.getElementById("contactCancel")?.addEventListener("click", closeContactDialog);
  document.getElementById("contactSave")?.addEventListener("click", saveContact);
  document.getElementById("contactDelete")?.addEventListener("click", () => deleteContactByEmail(contactEditingEmail));
  document.getElementById("contactOverlay")?.addEventListener("click", (event) => { if (event.target.id === "contactOverlay") closeContactDialog(); });
  document.getElementById("calNew").addEventListener("click", () => openEventDialog());
  document.getElementById("detailsClose").addEventListener("click", closeDetails);
  document.getElementById("eventClose").addEventListener("click", closeEventDialog);
  document.getElementById("eventCancel").addEventListener("click", closeEventDialog);
  document.getElementById("eventSave").addEventListener("click", saveEvent);
  document.getElementById("eventDelete").addEventListener("click", () => deleteCalendarEvent(calendarEditingId));
  detailsOverlay.addEventListener("click", (event) => { if (event.target === detailsOverlay) closeDetails(); });
  eventOverlay.addEventListener("click", (event) => { if (event.target === eventOverlay) closeEventDialog(); });
  document.getElementById("externalWarningToggle").addEventListener("click", (event) => {
    setPreference("externalWarnings", event.currentTarget.getAttribute("aria-checked") !== "true", true);
  });
  document.getElementById("defaultPrioritySelect").addEventListener("change", (event) => {
    state.defaultPriority = event.target.value;
    prefs.set("defaultPriority", event.target.value);
    patchPreferences({ defaultPriority: state.defaultPriority }, "Default importance saved");
  });
  document.getElementById("undoSendSelect").addEventListener("change", (event) => {
    state.undoSendSeconds = Number(event.target.value);
    prefs.set("undoSendSeconds", String(state.undoSendSeconds));
    patchPreferences({ undoSendSeconds: state.undoSendSeconds }, "Undo send window saved");
  });
  document.getElementById("resetDemo").addEventListener("click", resetDemoPreferences);
  document.getElementById("resetData").addEventListener("click", handleDataAction);
  document.getElementById("installApp").addEventListener("click", installApp);
  document.getElementById("auditFilter").addEventListener("change", renderAudit);
  document.getElementById("ruleAction").addEventListener("change", syncRuleActionInputs);
  document.getElementById("ruleForm").addEventListener("submit", saveRuleForm);
  document.getElementById("ruleConditions").addEventListener("change", (event) => {
    if (event.target.matches("[data-condition-field]")) {
      const row = event.target.closest(".rule-condition");
      const size = event.target.value === "size";
      const operator = row.querySelector("[data-condition-operator]");
      const current = operator.value;
      operator.innerHTML = (size ? RULE_OPERATORS.filter(([value]) => value === "over" || value === "under") : RULE_OPERATORS.filter(([value]) => !["over", "under"].includes(value)))
        .map(([value, label]) => `<option value="${value}">${label}</option>`).join("");
      operator.value = size ? "over" : (["contains", "is", "matches", "notcontains"].includes(current) ? current : "contains");
      const valueInput = row.querySelector("[data-condition-value]");
      if (valueInput) { valueInput.type = size ? "number" : "text"; valueInput.min = size ? "1" : ""; valueInput.placeholder = size ? "KB" : "value"; }
    }
  });

  /* login */
  loginForm.addEventListener("submit", (event) => { event.preventDefault(); submitLogin(); });
  ssoBtn.addEventListener("click", submitSso);
  document.getElementById("passwordToggle").addEventListener("click", () => {
    const show = loginPassword.type === "password";
    loginPassword.type = show ? "text" : "password";
    document.getElementById("passwordToggle").textContent = show ? "Hide" : "Show";
  });
  document.getElementById("forgotPassword").addEventListener("click", () => {
    loginHint.textContent = "Password resets are handled by Alt8Mail. Contact your administrator for help.";
    loginHint.hidden = false;
  });
  [loginEmail, loginPassword].forEach((input) => input.addEventListener("input", () => { loginError.hidden = true; }));

  /* two-factor */
  twoFactorForm.addEventListener("submit", submitTwoFactor);
  document.getElementById("enrollmentTotpBtn")?.addEventListener("click", beginEnrollmentTOTP);
  document.getElementById("enrollmentTotpVerify")?.addEventListener("click", verifyEnrollmentTOTP);
  document.getElementById("enrollmentPasskeyBtn")?.addEventListener("click", beginEnrollmentPasskey);
  document.getElementById("enrollmentCancel")?.addEventListener("click", cancelEnrollment);
  document.getElementById("enrollmentCopySecret")?.addEventListener("click", () => copyText(document.getElementById("enrollmentTotpSecret")?.textContent || ""));
  document.getElementById("setupTotpBtn")?.addEventListener("click", startTotpSetup);
  document.getElementById("addPasskeyBtn")?.addEventListener("click", addPasskey);
  document.getElementById("disableTotpBtn")?.addEventListener("click", disableTotp);
  document.getElementById("enrollmentPolicyToggle")?.addEventListener("click", toggleEnrollmentPolicy);
  document.getElementById("totpSetupVerify")?.addEventListener("click", verifyTotpSetup);
  document.getElementById("totpSetupCancel")?.addEventListener("click", () => { document.getElementById("totpSetupOverlay").hidden = true; });
  document.getElementById("totpCopySecret")?.addEventListener("click", () => copyText(document.getElementById("totpSetupSecret")?.textContent || ""));

  usePasskeyBtn.addEventListener("click", showPasskeyPrompt);
  document.getElementById("passkeyCancel").addEventListener("click", hidePasskeyPrompt);
  passkeyOverlay.addEventListener("click", (event) => { if (event.target === passkeyOverlay) hidePasskeyPrompt(); });
  document.getElementById("recoveryModeBtn").addEventListener("click", () => setTwoFactorMode("recovery"));
  document.getElementById("otpModeBtn").addEventListener("click", () => setTwoFactorMode("otp"));
  document.getElementById("twoFactorBack").addEventListener("click", hideTwoFactor);
  document.getElementById("fillDemoCode").addEventListener("click", () => {
    fillOtp(document.getElementById("twoFactorDemoCode").textContent);
  });
  const otpInputs = $$("#otpInputs input");
  otpInputs.forEach((input, index) => {
    input.addEventListener("input", () => {
      const digits = input.value.replace(/\D/g, "");
      input.value = digits.slice(-1);
      if (input.value && index < otpInputs.length - 1) otpInputs[index + 1].focus();
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Backspace" && !input.value && index > 0) {
        otpInputs[index - 1].focus();
        otpInputs[index - 1].value = "";
        event.preventDefault();
      } else if (event.key === "ArrowLeft" && index > 0) {
        otpInputs[index - 1].focus();
      } else if (event.key === "ArrowRight" && index < otpInputs.length - 1) {
        otpInputs[index + 1].focus();
      }
    });
  });
  document.getElementById("otpInputs").addEventListener("paste", (event) => {
    event.preventDefault();
    const text = (event.clipboardData || window.clipboardData).getData("text");
    fillOtp(text);
  });

  /* regional, task, file and schedule inputs */
  document.addEventListener("change", (event) => {
    const target = event.target;
    if (target.id === "languageSelect") {
      prefs.set("language", target.value);
      setLocale(target.value).catch(() => {});
      patchPreferences({ language: target.value }, "Language updated");
      return;
    }
    if (target.id === "dateFormatSelect") { patchPreferences({ dateFormat: target.value }, "Date format updated"); return; }
    if (target.id === "weekStartSelect") { patchPreferences({ weekStart: target.value }, "Calendar week updated"); return; }
    if (target.id === "timeZoneSelect") { patchPreferences({ timeZone: target.value }, "Time zone updated"); return; }
    if (target.id === "workStartInput" || target.id === "workEndInput") {
      const start = workStartInput.value;
      const end = workEndInput.value;
      if (end <= start) { showToast("Work hours must end after they start"); syncPreferencesUi(); return; }
      patchPreferences({ workStart: start, workEnd: end }, "Work hours saved");
      return;
    }
    if (target.id === "defaultFromSelect") { patchPreferences({ defaultFrom: target.value }, "Default From identity saved"); return; }
    if (target.id === "composeModeSelect") { patchPreferences({ composeMode: target.value }, "Compose format saved"); return; }
    if (target.id === "composeWidthSelect") { patchPreferences({ composeWidth: target.value }, "Compose window size saved").then(() => layoutComposeSessions()); return; }
    if (target.id === "composePositionSelect") { patchPreferences({ composePosition: target.value }, "Compose window position saved").then(() => layoutComposeSessions()); return; }
    if (target.id === "inboxViewSelect") {
      state.selectedThreadId = null;
      state.selectedIds.clear();
      state.expandedThreadId = null;
      state.aiReader = null;
      document.getElementById("readerAiCard")?.remove();
      patchPreferences({ inboxView: target.value === "individual" ? "individual" : "grouped" }, target.value === "individual" ? "Individual message view saved" : "Conversation view saved");
      return;
    }
    if (target.id === "appPasswordExpiry") {
      const wrap = document.getElementById("appPasswordCustomWrap");
      if (wrap) wrap.hidden = target.value !== "custom";
      return;
    }
    if (target.id === "fileProviderFilter") { state.fileProvider = target.value; renderFiles(); return; }
    if (target.id === "scheduleDate" || target.id === "scheduleTime") {
      scheduleChoice = "custom";
      $$("[data-schedule-preset]", schedulePresets).forEach((button) => button.classList.toggle("is-selected", button.getAttribute("data-schedule-preset") === "custom"));
      updateSchedulePreview();
      return;
    }
    if (target.id === "fileUploadInput") { uploadFiles(target.files); return; }
    if (target.id === "scheduledEditDate" || target.id === "scheduledEditTime") { updateScheduledEditInsight(); return; }
  });
  document.addEventListener("input", (event) => {
    const target = event.target;
    if (["autoReplyInternalSubject", "autoReplyInternalMessage", "autoReplyExternalSubject", "autoReplyExternalMessage", "autoReplyStart", "autoReplyEnd"].includes(target.id)) {
      updateAutoReplyPreview();
      return;
    }
    if (target.id === "scheduleDate" || target.id === "scheduleTime") updateSchedulePreview();
    if (target.id === "scheduledEditDate" || target.id === "scheduledEditTime") updateScheduledEditInsight();
  });
  document.addEventListener("submit", (event) => {
    if (event.target && event.target.id === "taskEditorForm") { saveTaskForm(event); }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const taskRow = event.target.closest("[data-task-id]");
    if (taskRow && event.target === taskRow) {
      event.preventDefault();
      state.selectedTaskId = taskRow.getAttribute("data-task-id");
      state.editingTaskId = null;
      renderTasks();
      return;
    }
    const fileRow = event.target.closest("[data-file-id]");
    if (fileRow && event.target === fileRow) {
      event.preventDefault();
      state.selectedFileId = fileRow.getAttribute("data-file-id");
      renderFiles();
      if (isMobile()) document.body.classList.add("file-open");
    }
  });

  /* compose layer */
  composeLayer.addEventListener("click", (event) => {
    const section = event.target.closest(".compose[data-compose-id]");
    if (!section) return;
    const session = sessionById(section.getAttribute("data-compose-id"));
    if (!session) return;
    const recipientRemove = event.target.closest("[data-recipient-remove]");
    if (recipientRemove) {
      event.preventDefault();
      const field = recipientRemove.getAttribute("data-recipient-remove");
      const index = Number(recipientRemove.getAttribute("data-recipient-index"));
      const entries = recipientEntriesFor(session, field);
      if (entries[index]) {
        entries.splice(index, 1);
        session[field] = recipientTextFromEntries(entries);
        session.dirty = true;
        updateRecipientField(session, field);
        scheduleComposeSave(session);
        session.el.querySelector(`[data-recipient-input="${field}"]`)?.focus();
      }
      return;
    }
    const recipientSuggestion = event.target.closest("[data-recipient-suggest-email], [data-recipient-add]");
    if (recipientSuggestion) {
      event.preventDefault();
      const panel = recipientSuggestion.closest("[data-recipient-suggest]");
      const field = panel?.getAttribute("data-recipient-suggest");
      const email = recipientSuggestion.getAttribute("data-recipient-suggest-email") || recipientSuggestion.getAttribute("data-recipient-add");
      const name = recipientSuggestion.getAttribute("data-recipient-suggest-name") || recipientSuggestion.getAttribute("data-recipient-add-name") || "";
      const entry = parseRecipientEntry(email);
      if (entry?.valid) {
        entry.name = name || entry.name;
        addRecipientEntry(session, field, entry);
      }
      return;
    }
    const remove = event.target.closest("[data-compose-remove]");
    if (remove) {
      session.files.splice(Number(remove.getAttribute("data-compose-remove")), 1);
      updateComposeFiles(session);
      refreshAttachPickerIfOpen();
      session.dirty = true;
      scheduleComposeSave(session);
      return;
    }
    if (event.target.closest(".compose-file-card")) { openComposeAttach(session.id); return; }
    if (event.target.closest("[data-ai-compose-close]")) {
      session.aiOpen = false;
      const panel = session.el.querySelector("[data-compose-ai-panel]");
      if (panel) panel.hidden = true;
      return;
    }
    if (event.target.closest("[data-ai-compose-generate]")) { composeAiGenerate(session); return; }
    if (event.target.closest("[data-ai-compose-insert]")) { applyComposeAiResult(session, false); return; }
    if (event.target.closest("[data-ai-compose-replace]")) { applyComposeAiResult(session, true); return; }
    const priority = event.target.closest("[data-compose-priority]");
    if (priority) {
      session.priority = priority.getAttribute("data-compose-priority");
      session.el.querySelectorAll("[data-compose-priority]").forEach((button) => {
        const active = button === priority;
        button.classList.toggle("is-active", active);
        button.setAttribute("aria-checked", active ? "true" : "false");
      });
      updateComposeOptions(session);
      session.dirty = true;
      scheduleComposeSave(session);
      return;
    }
    const editorCommand = event.target.closest("[data-editor-cmd]");
    if (editorCommand) {
      applyComposeEditorCommand(session, editorCommand.getAttribute("data-editor-cmd"));
      return;
    }
    const editorBlock = event.target.closest("[data-editor-block]");
    if (editorBlock) {
      applyComposeEditorCommand(session, "formatBlock", editorBlock.getAttribute("data-editor-block"));
      return;
    }
    const action = event.target.closest("[data-compose-action]");
    if (action) {
      const kind = action.getAttribute("data-compose-action");
      if (kind === "minimize") minimizeComposeSession(session.id);
      else if (kind === "expand") expandComposeSession(session.id);
      else if (kind === "close") closeComposeSession(session.id);
      else if (kind === "send") sendComposeSession(session.id);
      else if (kind === "schedule") openScheduleDialog(session);
      else if (kind === "discard") {
        appConfirm({ title: "Discard this draft?", message: "Your recipient, subject and message content will be deleted.", confirmLabel: "Discard draft", tone: "danger" }).then((ok) => {
          if (ok) closeComposeSession(session.id, { discard: true });
        });
      } else if (kind === "attach") { openComposeAttach(session.id); }
      else if (kind === "mode") { setComposeMode(session, action.getAttribute("data-mode") === "plain" ? "plain" : "html"); }
      else if (kind === "toggle-quote") { toggleComposeQuote(session); }
      else if (kind === "insert-signature") { openSignaturePicker(session); }
      else if (kind === "ai") { toggleComposeAi(session); }
      else if (kind === "cc") {
        session.ccOpen = !session.ccOpen;
        updateComposeWindow(session);
        if (session.ccOpen) session.el.querySelector('[data-compose-field="cc"]')?.focus();
      } else if (kind === "options") {
        session.optionsOpen = !session.optionsOpen;
        updateComposeOptions(session);
      }
      return;
    }
    setActiveCompose(session.id);
  });

  const handleComposeFieldEvent = (event) => {
    const section = event.target.closest(".compose[data-compose-id]");
    if (!section) return;
    const session = sessionById(section.getAttribute("data-compose-id"));
    if (!session) return;
    if (event.target.matches("[data-compose-editor]")) {
      updateComposeEditor(session, event.target);
      return;
    }
    const field = event.target.closest("[data-compose-field]");
    if (field) updateComposeField(session, field);
  };
  composeLayer.addEventListener("input", handleComposeFieldEvent);
  composeLayer.addEventListener("change", handleComposeFieldEvent);
  composeLayer.addEventListener("pointerdown", (event) => {
    if (event.target.closest(".recipient-suggestion")) event.preventDefault();
  }, true);
  composeLayer.addEventListener("keydown", (event) => {
    const input = event.target.closest("[data-recipient-input]");
    if (!input) return;
    const section = input.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (session) recipientKeydown(event, session, input);
  });
  composeLayer.addEventListener("paste", (event) => {
    const input = event.target.closest("[data-recipient-input]");
    if (!input) return;
    const section = input.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (session) recipientPaste(event, session, input);
  });
  composeLayer.addEventListener("focusout", (event) => {
    const input = event.target.closest("[data-recipient-input]");
    if (!input) return;
    const field = input.getAttribute("data-recipient-input");
    const section = input.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (!session) return;
    setTimeout(() => {
      if (document.activeElement === input) return;
      const text = String(session.recipientInput?.[field] || "").trim();
      const parsed = parseRecipientList(text);
      if (text && parsed.length && parsed.every((entry) => entry.valid)) {
        commitRecipientInput(session, field, { focus: false });
      } else {
        closeRecipientSuggestions(session, field);
      }
    }, 180);
  });
  composeLayer.addEventListener("focusin", (event) => {
    const section = event.target.closest(".compose[data-compose-id]");
    if (section) setActiveCompose(section.getAttribute("data-compose-id"));
    const input = event.target.closest("[data-recipient-input]");
    if (input) {
      const session = sessionById(section?.getAttribute("data-compose-id"));
      if (session) updateRecipientSuggestions(session, input.getAttribute("data-recipient-input"));
    }
  });
  composeLayer.addEventListener("mousedown", (event) => {
    if (event.target.closest(".compose-editor-toolbar button")) event.preventDefault();
  });
  composeLayer.addEventListener("input", (event) => {
    if (!event.target.matches("[data-editor-color]")) return;
    const section = event.target.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (session) applyComposeEditorCommand(session, "foreColor", event.target.value, { keepFocus: true });
  });
  composeLayer.addEventListener("change", (event) => {
    const section = event.target.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (!session) return;
    if (event.target.matches("[data-editor-font]")) {
      applyComposeFontFamily(session, event.target.value);
      return;
    }
    if (event.target.matches("[data-editor-size]")) {
      applyComposeFontSize(session, event.target.value);
      return;
    }
    if (event.target.matches("[data-editor-highlight]")) {
      applyComposeHighlight(session, event.target.value);
    }
  });
  document.addEventListener("selectionchange", () => {
    const selection = window.getSelection();
    if (!selection || !selection.rangeCount) return;
    const node = selection.anchorNode;
    const element = node ? (node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement) : null;
    const editor = element?.closest?.("[data-compose-editor]");
    if (!editor) return;
    const section = editor.closest(".compose[data-compose-id]");
    const session = sessionById(section?.getAttribute("data-compose-id"));
    if (session) rememberComposeEditorSelection(session, editor);
  });
  composeLayer.addEventListener("pointerdown", (event) => {
    const section = event.target.closest(".compose[data-compose-id]");
    if (!section) return;
    const session = sessionById(section.getAttribute("data-compose-id"));
    if (!session) return;
    setActiveCompose(session.id);
    if (event.target.closest("[data-compose-resize]")) { beginComposePointer(event, session, "resize"); return; }
    if (event.target.closest("[data-compose-head]") && !event.target.closest("button") && !event.target.closest("[data-compose-action]")) beginComposePointer(event, session, "drag");
  });

  attachInput.addEventListener("change", async (event) => {
    const session = sessionById(attachPickerSessionId || pendingAttachSessionId);
    const files = Array.from(event.target.files || []);
    pendingAttachSessionId = null;
    event.target.value = "";
    if (!session) return;
    await uploadComposeFiles(session, files);
    refreshAttachPickerIfOpen();
  });

  document.getElementById("composeAttachProviderFilter")?.addEventListener("change", (event) => {
    state.composeAttachProvider = event.target.value || "all";
    renderComposeAttachPicker();
  });
  composeAttachOverlay?.addEventListener("click", (event) => {
    if (event.target === composeAttachOverlay || event.target.closest("#composeAttachClose") || event.target.closest("#composeAttachDone")) {
      closeComposeAttach();
      return;
    }
    if (event.target.closest("#composeAttachBrowse")) {
      attachInput.click();
      return;
    }
    const workspaceFile = event.target.closest("[data-attach-file]");
    if (workspaceFile) { attachWorkspaceFile(workspaceFile.getAttribute("data-attach-file")); return; }
    const remove = event.target.closest("[data-compose-remove]");
    if (remove) {
      const session = sessionById(attachPickerSessionId);
      if (session) {
        session.files.splice(Number(remove.getAttribute("data-compose-remove")), 1);
        updateComposeFiles(session);
        renderComposeAttachPicker();
        session.dirty = true;
        scheduleComposeSave(session);
      }
    }
  });
  const dropZone = document.getElementById("composeAttachDrop");
  if (dropZone) {
    dropZone.addEventListener("click", (event) => {
      if (!event.target.closest("#composeAttachBrowse")) attachInput.click();
    });
    dropZone.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); attachInput.click(); }
    });
    ["dragenter", "dragover"].forEach((name) => dropZone.addEventListener(name, (event) => {
      event.preventDefault();
      dropZone.classList.add("is-over");
    }));
    ["dragleave", "drop"].forEach((name) => dropZone.addEventListener(name, (event) => {
      event.preventDefault();
      dropZone.classList.remove("is-over");
    }));
    dropZone.addEventListener("drop", async (event) => {
      const session = sessionById(attachPickerSessionId);
      if (!session) return;
      await uploadComposeFiles(session, Array.from(event.dataTransfer?.files || []));
      refreshAttachPickerIfOpen();
    });
  }

  /* Signature library editor, identity editor and spell-check review */
  signatureEditorOverlay?.addEventListener("mousedown", (event) => {
    if (event.target.closest(".signature-editor-toolbar button")) event.preventDefault();
  });
  signatureEditorOverlay?.addEventListener("click", (event) => {
    if (event.target === signatureEditorOverlay || event.target.closest("#signatureEditorClose") || event.target.closest("#signatureEditorCancel")) {
      closeSignatureEditor();
      return;
    }
    const mode = event.target.closest("[data-signature-mode]");
    if (mode) { setSignatureEditorMode(mode.getAttribute("data-signature-mode")); return; }
    const command = event.target.closest("[data-signature-cmd]");
    if (command) { applySignatureRichCommand(document.getElementById("signatureEditorBody"), command.getAttribute("data-signature-cmd")); return; }
    const block = event.target.closest("[data-signature-block]");
    if (block) { applySignatureRichCommand(document.getElementById("signatureEditorBody"), "formatBlock", block.getAttribute("data-signature-block")); return; }
    if (event.target.closest("#signatureEditorSave")) { saveSignatureEditor(); return; }
  });
  signaturePickerOverlay?.addEventListener("click", (event) => {
    if (event.target === signaturePickerOverlay || event.target.closest("#signaturePickerClose") || event.target.closest("#signaturePickerCancel")) {
      closeSignaturePicker();
      return;
    }
    if (event.target.closest("#signaturePickerNew")) {
      const sessionId = state.signaturePickerSessionId;
      closeSignaturePicker();
      openSignatureEditor(null, { onSaved: (signature) => insertSignatureIntoCompose(sessionId, signature) });
      return;
    }
  });
  identityEditorOverlay?.addEventListener("mousedown", (event) => {
    if (event.target.closest(".signature-editor-toolbar button")) event.preventDefault();
  });
  identityEditorOverlay?.addEventListener("click", (event) => {
    if (event.target === identityEditorOverlay || event.target.closest("#identityEditorClose") || event.target.closest("#identityEditorCancel")) {
      closeIdentityEditor();
      return;
    }
    const mode = event.target.closest("[data-identity-signature-mode]");
    if (mode) { setIdentitySignatureMode(mode.getAttribute("data-identity-signature-mode")); return; }
    const command = event.target.closest("[data-identity-signature-cmd]");
    if (command) { applySignatureRichCommand(document.getElementById("identityEditorSignature"), command.getAttribute("data-identity-signature-cmd")); return; }
    if (event.target.closest("#identityEditorSave")) { saveIdentityEditor(); return; }
  });
  for (const [id, kind] of [["signatureDefaultNew", "new"], ["signatureDefaultReply", "reply"], ["signatureDefaultForward", "forward"]]) {
    document.getElementById(id)?.addEventListener("change", (event) => { setSignatureDefault(kind, event.target.value); });
  }
  spellcheckOverlay?.addEventListener("click", (event) => {
    if (event.target === spellcheckOverlay || event.target.closest("#spellcheckClose") || event.target.closest("#spellcheckBack")) {
      closeSpellcheckDialog();
      return;
    }
    if (event.target.closest("#spellcheckSendAnyway")) {
      const review = state.spellcheck;
      const session = review ? sessionById(review.sessionId) : null;
      const action = review?.action === "schedule" ? "schedule" : "send";
      closeSpellcheckDialog();
      if (session) {
        session.spellCheckBypass = true;
        if (action === "schedule") confirmScheduledSend(); else sendComposeSession(session.id);
      }
      return;
    }
    if (event.target.closest("#spellcheckApplySend")) {
      const review = state.spellcheck;
      const session = review ? sessionById(review.sessionId) : null;
      const action = review?.action === "schedule" ? "schedule" : "send";
      applySpellcheckChoices();
      closeSpellcheckDialog();
      if (session) {
        session.spellCheckBypass = true;
        if (action === "schedule") confirmScheduledSend(); else sendComposeSession(session.id);
      }
      return;
    }
    const suggestion = event.target.closest("[data-spell-suggestion]");
    if (suggestion) {
      const word = suggestion.getAttribute("data-spell-word");
      const value = suggestion.getAttribute("data-spell-suggestion");
      state.spellcheck.choices.set(word.toLowerCase(), value);
      suggestion.closest(".spellcheck-issue")?.querySelectorAll("[data-spell-suggestion]").forEach((button) => button.classList.toggle("is-picked", button === suggestion));
      return;
    }
    const ignore = event.target.closest("[data-spell-ignore]");
    if (ignore) {
      const word = ignore.getAttribute("data-spell-ignore");
      state.spellcheck.choices.set(word.toLowerCase(), null);
      ignore.closest(".spellcheck-issue")?.classList.add("is-ignored");
      return;
    }
    const addWord = event.target.closest("[data-spell-add]");
    if (addWord) {
      const word = addWord.getAttribute("data-spell-add");
      addSpellIgnoredWord(word);
      state.spellcheck.choices.set(word.toLowerCase(), null);
      addWord.closest(".spellcheck-issue")?.classList.add("is-ignored");
    }
  });

  window.addEventListener("resize", () => {
    if (!isDrawer()) closeDrawer();
    if (!isMobile()) document.body.classList.remove("search-open", "reader-open", "person-open");
    else {
      composeSessions.forEach((session) => {
        if (session.mode === "inline") { session.mode = "floating"; if (session.el) session.el.classList.remove("is-inline"); }
      });
      const visible = composeSessions.filter((session) => session.mode !== "minimized");
      if (visible.length > 1) {
        const keep = visible.find((session) => session.id === activeComposeId) || visible[0];
        visible.forEach((session) => { if (session !== keep) { session.mode = "minimized"; if (session.el) session.el.hidden = true; } });
      }
    }
    updateComposeDock();
    layoutComposeSessions();
  });

  window.addEventListener("online", () => { setOffline(false); });
  window.addEventListener("offline", () => { setOffline(true); });

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      composeSessions.forEach((session) => {
        if (session.dirty && !session.savingPromise) {
          const payload = JSON.stringify(composePayload(session));
          try {
            fetch(`/api/drafts/${encodeURIComponent(session.draftId)}`, {
              method: "PUT", credentials: "same-origin", keepalive: true,
              headers: { "Content-Type": "application/json", "X-CSRF-Token": state.csrf },
              body: payload
            }).catch(() => {});
          } catch {}
        }
      });
    }
  });

  if (window.matchMedia) {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => { if (state.theme === "system") syncThemeUI(); };
    if (media.addEventListener) media.addEventListener("change", onChange);
    else if (media.addListener) media.addListener(onChange);
  }

  document.addEventListener("keydown", (event) => {
    if (loginScreen.hidden === false) return;
    if (state.user && state.user.onboardingRequired) return;
    if (spellcheckOverlay && !spellcheckOverlay.hidden) {
      if (event.key === "Escape") { event.preventDefault(); closeSpellcheckDialog(); }
      return;
    }
    if (signatureEditorOverlay && !signatureEditorOverlay.hidden) {
      if (event.key === "Escape") { event.preventDefault(); closeSignatureEditor(); }
      return;
    }
    if (signaturePickerOverlay && !signaturePickerOverlay.hidden) {
      if (event.key === "Escape") { event.preventDefault(); closeSignaturePicker(); }
      return;
    }
    if (identityEditorOverlay && !identityEditorOverlay.hidden) {
      if (event.key === "Escape") { event.preventDefault(); closeIdentityEditor(); }
      return;
    }
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName) || event.target.isContentEditable;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      if (state.task === "settings" || state.task === "calendar") return;
      event.preventDefault();
      focusSearch();
      return;
    }
    if (event.key === "Escape") {
      if (!confirmOverlay.hidden) { settleConfirm(false); return; }
      if (!domainBlockedOverlay.hidden) { domainBlockedOverlay.hidden = true; return; }
      if (storageCredentialsOverlay && !storageCredentialsOverlay.hidden) { closeStorageCredentials(); return; }
      if (!passkeyOverlay.hidden) { hidePasskeyPrompt(); return; }
      if (!sendNowOverlay.hidden) { closeSendNowDialog(); return; }
      if (!scheduledEditOverlay.hidden) { closeScheduledEdit(); return; }
      if (!reportOverlay.hidden) { closeReportDialog(); return; }
      if (!scheduleOverlay.hidden) { closeScheduleDialog(); return; }
      if (!previewOverlay.hidden) { closePreview(); return; }
      if (!storageOverlay.hidden) { storageOverlay.hidden = true; return; }
      if (!shareOverlay.hidden) { shareOverlay.hidden = true; return; }
      if (!recoveryOverlay.hidden) { recoveryOverlay.hidden = true; return; }
      if (folderEditorOverlay && !folderEditorOverlay.hidden) { closeFolderEditor(); return; }
      if (folderMenu && !folderMenu.hidden) { closeFolderMenu(); return; }
      if (state.editingTaskId) { state.editingTaskId = null; renderTasks(); return; }
      if (!detailsOverlay.hidden) { closeDetails(); return; }
      if (!eventOverlay.hidden) { closeEventDialog(); return; }
      if (!statusPop.hidden) { closeStatusPop(); return; }
      if (composeAttachOverlay && !composeAttachOverlay.hidden) { closeComposeAttach(); return; }
      if (noticeLayer && !noticeLayer.hidden) { closeNoticeCenter(); return; }
      if (!notifyPop.hidden) { hideNotify(); return; }
      if (!newMenu.hidden || !moveMenu.hidden) { closeMenus(); return; }
      if (helpOverlay.hidden === false) { closeHelp(); return; }
      if (searchHints.hidden === false) { searchHints.hidden = true; searchInput.blur(); return; }
      const visibleDraft = composeSessions.find((session) => session.mode !== "minimized");
      if (visibleDraft) { minimizeComposeSession(visibleDraft.id); return; }
      if (document.body.classList.contains("search-open")) { closeMobileSearch(); return; }
      if (document.body.classList.contains("drawer-open")) { closeDrawer(); return; }
      if (document.body.classList.contains("reader-open")) { document.body.classList.remove("reader-open"); return; }
      if (document.body.classList.contains("person-open")) { document.body.classList.remove("person-open"); return; }
      if (state.query) { clearSearch(); return; }
      return;
    }
    if (typing || helpOverlay.hidden === false || !sendNowOverlay.hidden || !scheduledEditOverlay.hidden || !reportOverlay.hidden || !scheduleOverlay.hidden || !previewOverlay.hidden || !storageOverlay.hidden || !shareOverlay.hidden || !recoveryOverlay.hidden || !passkeyOverlay.hidden || composeSessions.some((session) => session.mode !== "minimized") || !detailsOverlay.hidden || !eventOverlay.hidden || state.task !== "mail") return;
    if (event.key === "j" || event.key === "k") {
      event.preventDefault();
      navigateThread(event.key === "j" ? 1 : -1, event.shiftKey);
    } else if (event.key === "x" || event.key === "X") {
      if (!state.selectedThreadId) return;
      event.preventDefault();
      toggleThreadSelection(state.selectedThreadId);
    } else if (event.key === "c" || event.key === "C") {
      event.preventDefault();
      openCompose();
    } else if (event.key === "/") {
      event.preventDefault();
      focusSearch();
    } else if (event.key === "?") {
      event.preventDefault();
      openHelp();
    } else if (event.key === "e" || event.key === "E") {
      if (!currentThread() && !state.selectedIds.size) return;
      event.preventDefault();
      if (state.selectedIds.size) performBulkAction("archive"); else performBulkAction("archive");
    } else if (event.key === "#" || (event.key === "3" && event.shiftKey)) {
      if (!currentThread() && !state.selectedIds.size) return;
      event.preventDefault();
      performBulkAction("delete");
    } else if (event.key === "u" || event.key === "U") {
      if (!currentThread() && !state.selectedIds.size) return;
      event.preventDefault();
      if (state.selectedIds.size) performBulkAction("markUnread"); else markThreadUnread(currentThread());
    } else if (event.key === "r" || event.key === "R") {
      const thread = currentThread();
      if (!thread) return;
      event.preventDefault();
      replyToMessage(replyTarget(thread));
    } else if (event.key === "a" || event.key === "A") {
      const thread = currentThread();
      if (!thread) return;
      event.preventDefault();
      replyToMessage(replyTarget(thread), replyAllAvailable(thread));
    } else if (event.key === "f" || event.key === "F") {
      const thread = currentThread();
      if (!thread) return;
      event.preventDefault();
      forwardMessage(thread.latest);
    } else if (event.key === "s" || event.key === "S") {
      if (!state.selectedThreadId) return;
      event.preventDefault();
      toggleThreadStar(state.selectedThreadId);
    }
  });
}

/* ============================================================
   boot
   ============================================================ */
window.addEventListener("hashchange", handleHashLocationChange);
window.addEventListener("popstate", handleHashLocationChange);
window.addEventListener("pagehide", flushComposeSessionsKeepalive);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") {
    flushComposeSessionsKeepalive();
    composeSessions.forEach((session) => {
      if (session.mode !== "closed") flushDraft(session, { silent: true }).catch(() => {});
    });
  }
});

async function boot() {
  document.getElementById("passwordChangeSubmit")?.addEventListener("click", submitPasswordChange);
  await initI18n(prefs.get("language") || navigator.language || "en-US");
  applyStaticTranslations(document);
  applySavedPreferences();
  bindEvents();
  registerServiceWorker();
  if (!navigator.onLine) setOffline(true);
  try {
    const session = await api("/api/session", { skipAuth: true });
    state.csrf = session.csrf;
    await finishSignIn(session);
    const params = new URLSearchParams(window.location.search);
    if (params.get("storage") === "connected") {
      showToast("Storage provider connected");
      window.history.replaceState({}, "", window.location.pathname + window.location.hash);
    } else if (params.get("storage_error")) {
      showToast(params.get("storage_error"), "error");
      window.history.replaceState({}, "", window.location.pathname + window.location.hash);
    }
  } catch (error) {
    if (error.status === 0) {
      // Server unreachable: keep the shell and explain rather than trapping the login form.
      showLogin();
      loginError.textContent = !navigator.onLine
        ? "You’re offline. The app shell is installed; sign in again when your connection returns."
        : "The mail server is not reachable yet. Start it on port 8001 and refresh.";
      loginError.hidden = false;
      return;
    }
    showLogin();
  }
}

boot();
