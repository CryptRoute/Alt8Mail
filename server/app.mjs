import http from "node:http";
import fs from "node:fs/promises";
import { createReadStream } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { randomBytes, scryptSync } from "node:crypto";
import { DataStore } from "./store.mjs";
import { seedDatabase, DEMO_EMAIL } from "./seed.mjs";
import { MailIndex, buildThreads } from "../public/search.mjs";
import { normalizeSieve, renderSieve } from "./sieve.mjs";
import { generateTotp, generateRecoveryCodes, verifyTotp } from "./twofactor.mjs";
import {
  ApiError, CSRF_COOKIE, SESSION_COOKIE, SESSION_TTL_MS,
  cookieHeader, csvRow, escapeHtml, formatBytes, id, isSecure, looksLikeEmail,
  normalizeEmail, newThreadId, nowIso, parseCookies, publicUser, requestIp, safeEqual,
  snippetOf, textToHtml
} from "./util.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "..");
const UNDO_TTL_MS = 30_000;
const MAX_JSON_BYTES = 1.5 * 1024 * 1024;
const MAX_UPLOAD_JSON_BYTES = 13 * 1024 * 1024;
const MAX_UPLOAD_BYTES = 8 * 1024 * 1024;
const MOCK_BRANDING = { productName: "Alt8Mail", primaryColor: "#0f6cbd", supportEmail: "", loginMessage: "" };
const MOCK_DLP = {
  enabled: true,
  defaultClassification: "Internal",
  labels: ["Public", "Internal", "Confidential", "Restricted"],
  rules: [
    { id: "dlp_customer", name: "Customer identifiers", terms: ["account number", "customer id"], fields: ["subject", "body", "attachment"], action: "warn", classification: "Confidential", direction: "outbound", enabled: true },
    { id: "dlp_secret", name: "Highly restricted material", terms: ["top secret"], fields: ["subject", "body"], action: "block", classification: "Restricted", direction: "outbound", enabled: true }
  ]
};
const MOCK_REACTION_CATEGORIES = [
  { name: "Smileys & emotion", emojis: [
    { emoji: "😀", label: "Grinning", keywords: "happy smile", semantic: "happiness" },
    { emoji: "😂", label: "Tears of joy", keywords: "laugh funny", semantic: "humour" },
    { emoji: "😊", label: "Warm smile", keywords: "happy warm", semantic: "appreciation" },
    { emoji: "😍", label: "Heart eyes", keywords: "love", semantic: "appreciation" },
    { emoji: "🥳", label: "Partying", keywords: "celebrate", semantic: "celebration" },
    { emoji: "🤔", label: "Thinking", keywords: "think", semantic: "needs thought" },
    { emoji: "😢", label: "Sad", keywords: "sad", semantic: "sympathy" },
    { emoji: "🫡", label: "Salute", keywords: "respect", semantic: "respect" }
  ]},
  { name: "Gestures & people", emojis: [
    { emoji: "👍", label: "Thumbs up", keywords: "approve yes", semantic: "acknowledgement" },
    { emoji: "👎", label: "Thumbs down", keywords: "no", semantic: "disagreement" },
    { emoji: "👏", label: "Applause", keywords: "clap", semantic: "appreciation" },
    { emoji: "🙌", label: "Raised hands", keywords: "hooray", semantic: "celebration" },
    { emoji: "🙏", label: "Folded hands", keywords: "thanks", semantic: "gratitude" },
    { emoji: "🤝", label: "Handshake", keywords: "deal", semantic: "agreement" },
    { emoji: "👀", label: "Eyes", keywords: "attention", semantic: "attention" },
    { emoji: "💪", label: "Strong", keywords: "power", semantic: "encouragement" }
  ]},
  { name: "Hearts & appreciation", emojis: [
    { emoji: "❤️", label: "Red heart", keywords: "love", semantic: "appreciation" },
    { emoji: "🧡", label: "Orange heart", keywords: "love", semantic: "appreciation" },
    { emoji: "💙", label: "Blue heart", keywords: "love", semantic: "appreciation" },
    { emoji: "💜", label: "Purple heart", keywords: "love", semantic: "appreciation" },
    { emoji: "💯", label: "Hundred points", keywords: "perfect", semantic: "strong agreement" }
  ]},
  { name: "Celebration & work", emojis: [
    { emoji: "🎉", label: "Party popper", keywords: "celebrate", semantic: "celebration" },
    { emoji: "🎊", label: "Confetti", keywords: "party", semantic: "celebration" },
    { emoji: "🏆", label: "Trophy", keywords: "win", semantic: "celebration" },
    { emoji: "🚀", label: "Rocket", keywords: "launch", semantic: "excitement" },
    { emoji: "✅", label: "Check", keywords: "done approve", semantic: "approval" },
    { emoji: "📌", label: "Pin", keywords: "reminder", semantic: "attention" },
    { emoji: "💡", label: "Idea", keywords: "insight", semantic: "needs thought" },
    { emoji: "🔥", label: "Fire", keywords: "hot", semantic: "excitement" }
  ]},
  { name: "Nature & food", emojis: [
    { emoji: "🌈", label: "Rainbow", keywords: "hope", semantic: "happiness" },
    { emoji: "🌸", label: "Blossom", keywords: "flower", semantic: "appreciation" },
    { emoji: "🍀", label: "Clover", keywords: "luck", semantic: "hope" },
    { emoji: "🍕", label: "Pizza", keywords: "food", semantic: "playfulness" },
    { emoji: "☕", label: "Coffee", keywords: "break", semantic: "acknowledgement" }
  ]}
];
const MOCK_REACTION_EMOJIS = new Set(MOCK_REACTION_CATEGORIES.flatMap((category) => category.emojis.map((item) => item.emoji)));

const TRUSTED_DEVICE_TTL_MS = 30 * 24 * 60 * 60 * 1000;
const FOLDER_IDS = new Set(["inbox", "sent", "drafts", "archive", "spam", "trash"]);
const GIB = 1024 ** 3;
const MIB = 1024 ** 2;
const FILE_MIME_BY_TYPE = {
  PDF: "application/pdf", XLS: "application/excel", DOC: "application/msword",
  PPT: "application/vnd.ms-powerpoint", ZIP: "application/zip", IMG: "image/png", TXT: "text/plain"
};
const UPLOAD_MIME = new Set(["application/pdf", "image/png", "image/jpeg", "image/webp", "image/gif", "text/plain"]);
const EXTENSION_MIME = {
  pdf: "application/pdf", png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg",
  webp: "image/webp", gif: "image/gif", txt: "text/plain", csv: "text/plain",
  zip: "application/zip", xls: "application/vnd.ms-excel", xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  doc: "application/msword", docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ppt: "application/vnd.ms-powerpoint", pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation"
};

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".webmanifest": "application/manifest+json; charset=utf-8",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".txt": "text/plain; charset=utf-8",
  ".map": "application/json; charset=utf-8"
};

function setSecurityHeaders(res) {
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Referrer-Policy", "strict-origin-when-cross-origin");
  res.setHeader("X-Frame-Options", "SAMEORIGIN");
  res.setHeader("Permissions-Policy", "camera=(), microphone=(), geolocation=()");
  res.setHeader("Cross-Origin-Resource-Policy", "same-origin");
  res.setHeader(
    "Content-Security-Policy",
    "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; " +
    "script-src 'self'; connect-src 'self'; font-src 'self'; worker-src 'self'; " +
    "manifest-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; " +
    "form-action 'self'; frame-ancestors 'self'"
  );
}

function sendJson(res, status, body, headers = {}) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(payload),
    "Cache-Control": "no-store",
    ...headers
  });
  res.end(payload);
}

async function readJson(req, limit = MAX_JSON_BYTES) {
  const type = String(req.headers["content-type"] || "");
  if (!type.includes("application/json")) throw new ApiError(415, "CONTENT_TYPE", "Send JSON with Content-Type: application/json");
  let size = 0;
  const chunks = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new ApiError(413, "PAYLOAD_TOO_LARGE", "Request body is too large");
    chunks.push(chunk);
  }
  if (!chunks.length) return {};
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw new ApiError(400, "BAD_JSON", "Request body is not valid JSON");
  }
}

function initialsFrom(name, email) {
  const source = String(name || email || "?").trim();
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  return source.slice(0, 2).toUpperCase();
}

function colorForEmail(email) {
  const palette = ["#34557a", "#3d6b62", "#5b4a7e", "#7a4a5c", "#7a5a3a", "#3f6570", "#476b4f", "#4f5673", "#0e7490", "#b8336a", "#64748b"];
  let hash = 0;
  for (const char of String(email)) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length];
}

function displayNameFromEmail(email) {
  const local = String(email).split("@")[0];
  return local.split(/[._-]+/).filter(Boolean).map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function senderPerson(person) {
  const name = person.name || displayNameFromEmail(person.email);
  return {
    name,
    email: normalizeEmail(person.email),
    initials: person.initials || initialsFrom(name, person.email),
    color: person.color || colorForEmail(person.email)
  };
}

export async function createApp(options = {}) {
  const publicDir = path.resolve(options.publicDir || path.join(ROOT, "public"));
  const dataFile = path.resolve(options.dataFile || path.join(ROOT, "data", "db.json"));
  const uploadsDir = path.resolve(options.uploadsDir || path.join(path.dirname(dataFile), "uploads"));
  const holdOverride = Number.isFinite(options.holdMs) ? Number(options.holdMs) : null;
  const undoOverride = Number.isFinite(options.undoTtlMs) ? Number(options.undoTtlMs) : null;
  const allowDemoTwoFactorCode = options.demoTwoFactorCode !== false;
  const allowReset = options.allowReset !== false;
  const allowDevOutbox = options.allowDevOutbox === true;
  const aiEnabled = options.aiEnabled === true;
  const aiDelayMs = Math.max(0, Math.min(5000, Number(options.aiDelayMs) || 0));

  const store = new DataStore({ file: dataFile, seed: seedDatabase });
  await store.load();
  const db = store.db;
  if (Number.isFinite(options.outboundLimit)) db.user.outbound.limit = Number(options.outboundLimit);
  if (!db.attachments) db.attachments = {};
  if (!Array.isArray(db.tasks)) db.tasks = [];
  if (!Array.isArray(db.files)) db.files = [];
  if (!Array.isArray(db.uploadSessions)) db.uploadSessions = [];
  if (!Array.isArray(db.storageProviders)) db.storageProviders = [];
  if (!Array.isArray(db.signatures)) db.signatures = [];
  if (!db.signatures.length) {
    db.signatures.push({
      id: "sig_workspace_reply",
      name: "Workspace reply",
      bodyText: "Avery Chen\nVP Product · Alt8Mail\n+1 (415) 555-0101",
      bodyHtml: "<p><strong>Avery Chen</strong><br>VP Product · Alt8Mail<br>+1 (415) 555-0101</p>",
      defaultNew: true,
      defaultReply: true,
      defaultForward: false,
      updatedAt: nowIso()
    });
  }
  if (!db.user.preferences) db.user.preferences = {};
  if (!db.user.security) db.user.security = { twoFactor: { enabled: false } };
  if (!db.user.id) db.user.id = "user_demo_avery";
  if (!Array.isArray(db.delegatedMailboxes) || db.delegatedMailboxes.length === 0) {
    db.delegatedMailboxes = [{
      id: "user_demo_marcus",
      ownerId: "user_demo_marcus",
      name: "Marcus Webb",
      email: "marcus.webb@a8m.example",
      initials: "MW",
      color: "#3d6b62",
      canRead: true,
      canSend: true,
      messages: [{
        id: "m_delegated_1",
        threadId: "t_delegated_1",
        folder: "inbox",
        from: { name: "Priya Shah", email: "priya.shah@a8m.example", initials: "PS", color: "#7a4a5c" },
        to: [{ name: "Marcus Webb", email: "marcus.webb@a8m.example" }],
        cc: [], bcc: [],
        subject: "Delegated inbox: quarterly forecast sign-off",
        snippet: "Marcus, please approve the revised quarterly forecast before the finance review.",
        bodyHtml: "<p>Marcus,</p><p>Please approve the revised quarterly forecast before the finance review. The delegated mailbox window is working independently from your own session.</p><p>Priya</p>",
        bodyText: "Marcus, please approve the revised quarterly forecast before the finance review. The delegated mailbox window is working independently from your own session. Priya",
        date: nowIso(),
        unread: false, starred: false, draft: false,
        attachments: [], categories: ["finance"], classification: "Internal", external: false,
        auth: { spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3", ip: "127.0.0.1", signer: "a8m.example", firstContact: false, warnings: [] },
        event: null, sizeBytes: 4096, priority: "normal", receipt: false, delivery: false,
        saveCopy: true, viaApi: false, windowMode: "floating",
        messageId: "<m_delegated_1@a8m.example>", inReplyTo: null
      }]
    }, {
      id: "user_demo_aisha",
      ownerId: "user_demo_aisha",
      name: "Aisha Bello",
      email: "aisha.bello@a8m.example",
      initials: "AB",
      color: "#4f5673",
      canRead: true,
      canSend: false,
      messages: [{
        id: "m_delegated_2",
        threadId: "t_delegated_2",
        folder: "inbox",
        from: { name: "Daniel Kim", email: "daniel.kim@a8m.example", initials: "DK", color: "#476b4f" },
        to: [{ name: "Aisha Bello", email: "aisha.bello@a8m.example" }],
        cc: [], bcc: [],
        subject: "Shared inbox: engineering hiring loop",
        snippet: "Aisha, the interview panel needs one more reviewer for Thursday.",
        bodyHtml: "<p>Aisha,</p><p>The interview panel needs one more reviewer for Thursday. This read-only delegated mailbox is here for your review.</p><p>Daniel</p>",
        bodyText: "Aisha, the interview panel needs one more reviewer for Thursday. This read-only delegated mailbox is here for your review. Daniel",
        date: nowIso(),
        unread: true, starred: false, draft: false,
        attachments: [], categories: ["hiring"], classification: "Internal", external: false,
        auth: { spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3", ip: "127.0.0.1", signer: "a8m.example", firstContact: false, warnings: [] },
        event: null, sizeBytes: 3584, priority: "normal", receipt: false, delivery: false,
        saveCopy: true, viaApi: false, windowMode: "floating",
        messageId: "<m_delegated_2@a8m.example>", inReplyTo: null
      }]
    }];
  }
  ensureCollaborationSeed();

  function ensureCollaborationSeed() {
    if (!Array.isArray(db.threadMeta)) db.threadMeta = [];
    if (!Array.isArray(db.threadNotes)) db.threadNotes = [];
    if (!Array.isArray(db.notifications)) db.notifications = [];
    if (!Array.isArray(db.reactions)) db.reactions = [];
    if (!Array.isArray(db.reactionRules)) db.reactionRules = [];
    if (!Array.isArray(db.unsubscribeActions)) db.unsubscribeActions = [];
    if (db.threadMeta.length || db.threadNotes.length || db.notifications.length) return;
    db.threadMeta.push({
      ownerId: db.user.id, threadId: "N2Acmhe6TzJMY7c6L5yRfg", status: "in_progress",
      assignedTo: { id: "user_elena", name: "Elena Vasquez", email: "elena.vasquez@a8m.example" },
      closed: false, updatedAt: nowIso()
    });
    db.threadMeta.push({
      ownerId: db.user.id, threadId: "2z5Ft5gFLRaxGO7OORpmcw", status: "done",
      assignedTo: { id: "user_priya", name: "Priya Shah", email: "priya.shah@a8m.example" },
      closed: true, closedAt: nowIso(), updatedAt: nowIso()
    });
    db.reactions.push({
      id: "rct_demo_sent", ownerId: db.user.id, userId: "user_elena", userName: "Elena Vasquez",
      messageId: "m3", threadId: "N2Acmhe6TzJMY7c6L5yRfg", emoji: "👍", semantic: "acknowledgement",
      visibility: "sent", status: "sent", sentAt: nowIso(), createdAt: nowIso()
    });
    db.notifications.push({
      id: "ntf_demo_mention", kind: "mention", title: "Elena Vasquez mentioned you",
      body: "Q4 roadmap review — Thursday 10:00",
      detail: "Could you bring the latest macro library estimate?",
      actorName: "Elena Vasquez", actorEmail: "elena.vasquez@a8m.example",
      threadId: "N2Acmhe6TzJMY7c6L5yRfg", messageId: "m3", ownerId: db.user.id,
      readAt: null, createdAt: nowIso()
    });
  }

  const sessions = new Map();
  const delegatedSessions = new Map();
  const delegatedLaunchCodes = new Map();
  const sendTimers = new Map();
  const loginAttempts = new Map();
  const twoFactorChallenges = new Map();
  let index = null;

  function invalidateIndex() {
    index = null;
  }

  function getIndex() {
    if (!index) index = new MailIndex(db.messages);
    return index;
  }

  function isTrusted(message) {
    if (!message.external) return true;
    return db.user.trustedSenders.includes(normalizeEmail(message.from && message.from.email));
  }

  function publicMessage(message) {
    const words = String(message.bodyText || "").trim().match(/\S+/g)?.length || 0;
    return {
      ...message,
      trusted: isTrusted(message),
      readingMinutes: words ? Math.max(1, Math.ceil(words / 220)) : 0,
      wordCount: words
    };
  }

  function quotaInfo() {
    const dynamicBytes = db.messages.reduce((sum, message) => sum + (Number(message.sizeBytes) || 0), 0);
    const limitBytes = db.user.quota.limitBytes;
    const usedBytes = Math.min(limitBytes, db.user.quota.baselineUsedBytes + dynamicBytes);
    const folders = [
      { id: "_baseline", label: "Provider baseline", bytes: db.user.quota.baselineUsedBytes }
    ].concat(db.folders
      .filter((folder) => !folder.virtual)
      .map((folder) => {
        const bytes = db.messages
          .filter((message) => message.folder === folder.id)
          .reduce((sum, message) => sum + (Number(message.sizeBytes) || 0), 0);
        return { id: folder.id, label: folder.label, bytes };
      }));
    return {
      source: "dovecot",
      limitBytes,
      usedBytes,
      dynamicBytes,
      percent: limitBytes ? Math.round((usedBytes / limitBytes) * 1000) / 10 : 0,
      label: `${formatBytes(usedBytes)} of ${formatBytes(limitBytes)}`,
      folders,
      refreshedAt: nowIso()
    };
  }

  function pruneOutbound() {
    const outbound = db.user.outbound;
    const cutoff = Date.now() - outbound.windowHours * 3600 * 1000;
    outbound.events = outbound.events.filter((event) => Date.parse(event.at) >= cutoff);
  }

  function outboundInfo() {
    pruneOutbound();
    const outbound = db.user.outbound;
    const committed = outbound.events.length;
    const used = outbound.baselineUsed + committed;
    const queued = db.outbox.length;
    const scheduled = db.outbox.filter((item) => item.scheduled).length;
    return {
      limit: outbound.limit,
      windowHours: outbound.windowHours,
      baseline: outbound.baselineUsed,
      committed,
      used,
      queued,
      undoQueued: queued - scheduled,
      scheduled,
      usedWithQueued: used + queued,
      remaining: Math.max(0, outbound.limit - used - queued),
      percent: outbound.limit ? Math.round(((used + queued) / outbound.limit) * 1000) / 10 : 0,
      source: "postfwd",
      refreshedAt: nowIso()
    };
  }

  function publicOutboxItem(item) {
    return {
      id: item.id,
      subject: item.subject || "(no subject)",
      from: item.from,
      to: item.to || [],
      cc: item.cc || [],
      bcc: item.bcc || [],
      bodyText: item.scheduled ? item.bodyText || "" : "",
      attachments: item.attachments || [],
      queuedAt: item.queuedAt,
      sendAt: item.sendAt,
      holdMs: item.holdMs,
      canCancel: Date.now() < Date.parse(item.sendAt),
      attachmentCount: (item.attachments || []).length,
      saveCopy: item.saveCopy !== false,
      classification: item.classification || "Internal",
      threadId: item.threadId,
      scheduled: !!item.scheduled,
      kind: item.scheduled ? "scheduled" : "undo"
    };
  }

  function addAudit(req, action, target, detail, actor) {
    db.audit.unshift({
      id: id("aud"),
      ts: nowIso(),
      action,
      target: String(target || "").slice(0, 240),
      actor: actor || db.user.name,
      detail: String(detail || "").slice(0, 500),
      ip: requestIp(req),
      userAgent: String(req.headers["user-agent"] || "").slice(0, 240)
    });
    if (db.audit.length > 1000) db.audit.length = 1000;
  }

  function delegatedMailboxFor(session) {
    const ownerId = session?.delegatedOwnerId;
    if (!ownerId) return null;
    return (db.delegatedMailboxes || []).find((mailbox) => mailbox.id === ownerId || mailbox.ownerId === ownerId) || null;
  }

  function delegatedMailboxView(mailbox) {
    return {
      id: mailbox.id, ownerId: mailbox.ownerId || mailbox.id,
      name: mailbox.name, email: mailbox.email,
      initials: mailbox.initials || initialsFrom(mailbox.name, mailbox.email),
      color: mailbox.color || colorForEmail(mailbox.email),
      canRead: mailbox.canRead !== false, canSend: mailbox.canSend === true
    };
  }

  function delegatedPublicUser(mailbox) {
    return {
      ...publicUser(db.user),
      id: mailbox.id,
      name: mailbox.name,
      email: mailbox.email,
      initials: mailbox.initials || initialsFrom(mailbox.name, mailbox.email),
      color: mailbox.color || colorForEmail(mailbox.email),
      role: "Delegated mailbox",
      isSystemAdmin: false,
      security: null,
      aliases: [{
        id: "primary", email: mailbox.email, label: mailbox.name,
        kind: "primary", canSend: mailbox.canSend === true, reason: mailbox.canSend === true ? "" : "Read-only delegated access"
      }]
    };
  }

  function delegatedFolders(mailbox) {
    const messages = Array.isArray(mailbox.messages) ? mailbox.messages : [];
    return db.folders.map((folder) => ({
      ...folder,
      unread: messages.filter((message) => message.folder === folder.id && message.unread).length,
      total: messages.filter((message) => message.folder === folder.id).length
    }));
  }

  function sessionFrom(req) {
    const delegatedToken = String(req.headers["x-a8m-delegated-session"] || "").trim();
    if (delegatedToken) {
      const session = delegatedSessions.get(delegatedToken);
      if (!session) return null;
      if (session.expiresAt < Date.now()) {
        delegatedSessions.delete(delegatedToken);
        return null;
      }
      const parent = sessions.get(session.parentSid);
      if (!parent || parent.expiresAt < Date.now()) {
        delegatedSessions.delete(delegatedToken);
        return null;
      }
      session.lastSeen = Date.now();
      return { sid: delegatedToken, ...session };
    }
    const cookies = parseCookies(req.headers.cookie);
    const sid = cookies[SESSION_COOKIE];
    if (!sid) return null;
    const session = sessions.get(sid);
    if (!session) return null;
    if (session.expiresAt < Date.now()) {
      sessions.delete(sid);
      return null;
    }
    session.lastSeen = Date.now();
    return { sid, ...session };
  }

  function requireSession(req) {
    const session = sessionFrom(req);
    if (!session) throw new ApiError(401, "UNAUTHENTICATED", "Sign in to continue");
    return session;
  }

  function requireCsrf(req, session) {
    const token = String(req.headers["x-csrf-token"] || "");
    if (!token || !safeEqual(token, session.csrf)) {
      throw new ApiError(403, "CSRF", "Missing or invalid CSRF token");
    }
  }

  function startSession(req, { remember = true, ttlMs = SESSION_TTL_MS, detail = "" } = {}) {
    const sid = randomBytes(32).toString("hex");
    const csrf = randomBytes(24).toString("hex");
    const session = {
      email: db.user.email,
      csrf,
      createdAt: Date.now(),
      expiresAt: Date.now() + ttlMs,
      lastSeen: Date.now(),
      ip: requestIp(req),
      userAgent: String(req.headers["user-agent"] || "").slice(0, 240)
    };
    sessions.set(sid, session);
    const secure = isSecure(req);
    const cookieOptions = { secure };
    if (remember) cookieOptions.maxAge = ttlMs;
    const headers = {
      "Set-Cookie": [
        cookieHeader(SESSION_COOKIE, sid, { httpOnly: true, ...cookieOptions }),
        cookieHeader(CSRF_COOKIE, csrf, cookieOptions)
      ]
    };
    addAudit(req, "auth.login", db.user.email, detail || "New sign-in detected on your email account");
    return {
      session,
      headers,
      payload: {
        ...authPayload(session),
        method: "password"
      }
    };
  }

  function twoFactorSummary(user) {
    const factor = user.security && user.security.twoFactor ? user.security.twoFactor : null;
    if (!factor) return { enabled: false };
    const passkeys = (factor.passkeys || []).map(({ id, name, createdAt, lastUsedAt }) => ({ id, name, createdAt, lastUsedAt }));
    return {
      enabled: Boolean(factor.totpSecret) || passkeys.length > 0,
      enabledAt: factor.enabledAt || null,
      passkeys,
      totpEnabled: Boolean(factor.totpSecret),
      recoveryCodesRemaining: (factor.recoveryCodes || []).filter((entry) => !entry.used).length
    };
  }

  function createTwoFactorChallenge(req, remember) {
    const now = Date.now();
    for (const [key, challenge] of twoFactorChallenges) {
      if (challenge.expiresAt <= now) twoFactorChallenges.delete(key);
    }
    const challenge = {
      id: randomBytes(24).toString("base64url"),
      email: db.user.email,
      createdAt: now,
      expiresAt: now + 5 * 60 * 1000,
      attempts: 0,
      remember: remember !== false,
      ip: requestIp(req),
      userAgent: String(req.headers["user-agent"] || "").slice(0, 240)
    };
    twoFactorChallenges.set(challenge.id, challenge);
    return challenge;
  }

  function passkeyList() {
    const factor = db.user.security && db.user.security.twoFactor;
    return factor ? factor.passkeys || [] : [];
  }

  const PREFERENCE_LANGUAGES = new Set(["en-US", "en-GB", "de-DE", "fr-FR", "es-ES", "pt-BR", "nl-NL", "it-IT", "ar"]);

  function validTimeZone(value) {
    try {
      new Intl.DateTimeFormat("en-US", { timeZone: value }).format(new Date());
      return true;
    } catch {
      return false;
    }
  }

  function normalizePreferencesPatch(input) {
    if (!input || typeof input !== "object") throw new ApiError(400, "PREFERENCES", "Preferences are required");
    const next = { ...db.user.preferences };
    const changed = [];
    const assign = (key, value) => {
      if (JSON.stringify(next[key]) === JSON.stringify(value)) return;
      next[key] = value;
      changed.push(key);
    };
    if (input.language !== undefined) {
      if (!PREFERENCE_LANGUAGES.has(input.language)) throw new ApiError(400, "PREFERENCES", "Unsupported interface language");
      assign("language", input.language);
    }
    if (input.timeFormat !== undefined) {
      if (!["12h", "24h"].includes(input.timeFormat)) throw new ApiError(400, "PREFERENCES", "Time format must be 12h or 24h");
      assign("timeFormat", input.timeFormat);
    }
    if (input.dateFormat !== undefined) {
      if (!["auto", "mdy", "dmy", "ymd"].includes(input.dateFormat)) throw new ApiError(400, "PREFERENCES", "Unsupported date format");
      assign("dateFormat", input.dateFormat);
    }
    if (input.timeZone !== undefined) {
      if (!validTimeZone(input.timeZone)) throw new ApiError(400, "PREFERENCES", "Unknown IANA time zone");
      assign("timeZone", input.timeZone);
    }
    if (input.weekStart !== undefined) {
      if (!["monday", "sunday"].includes(input.weekStart)) throw new ApiError(400, "PREFERENCES", "Week start must be monday or sunday");
      assign("weekStart", input.weekStart);
    }
    for (const key of ["workStart", "workEnd"]) {
      if (input[key] !== undefined) {
        if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(input[key])) throw new ApiError(400, "PREFERENCES", "Work hours must use HH:MM");
        assign(key, input[key]);
      }
    }
    if (next.workStart >= next.workEnd) throw new ApiError(400, "PREFERENCES", "Work end must be after work start");
    if (input.workDays !== undefined) {
      if (!Array.isArray(input.workDays) || !input.workDays.length || input.workDays.some((day) => !Number.isInteger(day) || day < 1 || day > 7)) {
        throw new ApiError(400, "PREFERENCES", "Work days must be ISO weekdays 1–7");
      }
      assign("workDays", Array.from(new Set(input.workDays)).sort());
    }
    if (input.defaultFrom !== undefined) {
      if (!aliasForEmail(input.defaultFrom)) throw new ApiError(400, "PREFERENCES", "Default From must be a sendable identity");
      assign("defaultFrom", normalizeEmail(input.defaultFrom));
    }
    if (input.signatureEnabled !== undefined) assign("signatureEnabled", !!input.signatureEnabled);
    if (input.signature !== undefined) {
      if (String(input.signature).length > 4000) throw new ApiError(400, "PREFERENCES", "Signature is too long");
      assign("signature", String(input.signature));
    }
    if (input.autoAdvance !== undefined) assign("autoAdvance", !!input.autoAdvance);
    for (const key of ["markReadOnOpen", "defaultReceipt", "notifyDesktop", "notifySound", "notifyCalendar", "externalWarnings"]) {
      if (input[key] !== undefined) {
        if (typeof input[key] !== "boolean") throw new ApiError(400, "PREFERENCES", `${key} must be true or false`);
        assign(key, input[key]);
      }
    }
    if (input.inboxView !== undefined) {
      if (!["grouped", "individual"].includes(input.inboxView)) throw new ApiError(400, "PREFERENCES", "Inbox view must be grouped or individual");
      assign("inboxView", input.inboxView);
    }
    if (input.theme !== undefined) {
      if (!["light", "dark", "system"].includes(input.theme)) throw new ApiError(400, "PREFERENCES", "Theme must be light, dark or system");
      assign("theme", input.theme);
    }
    if (input.accent !== undefined) {
      if (!["blue", "teal", "violet", "rose"].includes(input.accent)) throw new ApiError(400, "PREFERENCES", "Unsupported accent color");
      assign("accent", input.accent);
    }
    if (input.density !== undefined) {
      if (!["comfortable", "compact"].includes(input.density)) throw new ApiError(400, "PREFERENCES", "Density must be comfortable or compact");
      assign("density", input.density);
    }
    if (input.defaultPriority !== undefined) {
      if (!["low", "normal", "high"].includes(input.defaultPriority)) throw new ApiError(400, "PREFERENCES", "Default priority must be low, normal or high");
      assign("defaultPriority", input.defaultPriority);
    }
    if (input.undoSendSeconds !== undefined) {
      if (!Number.isInteger(input.undoSendSeconds) || input.undoSendSeconds < 5 || input.undoSendSeconds > 10) {
        throw new ApiError(400, "PREFERENCES", "Undo send window must be between 5 and 10 seconds");
      }
      assign("undoSendSeconds", input.undoSendSeconds);
    }
    if (input.composeMode !== undefined) {
      if (!["html", "plain"].includes(input.composeMode)) throw new ApiError(400, "PREFERENCES", "Compose mode must be html or plain");
      assign("composeMode", input.composeMode);
    }
    if (input.composeWidth !== undefined) {
      if (!["standard", "wide", "full"].includes(input.composeWidth)) throw new ApiError(400, "PREFERENCES", "Compose width must be standard, wide or full");
      assign("composeWidth", input.composeWidth);
    }
    if (input.composePosition !== undefined) {
      if (!["center", "bottom-right", "bottom-left"].includes(input.composePosition)) throw new ApiError(400, "PREFERENCES", "Compose position must be center, bottom-right or bottom-left");
      assign("composePosition", input.composePosition);
    }
    return { next, changed };
  }

  function attachmentPublic(record) {
    return {
      id: record.id,
      name: record.name,
      type: record.type,
      mime: record.mime,
      size: record.size || formatBytes(record.sizeBytes),
      sizeBytes: record.sizeBytes,
      previewable: !!record.previewable,
      contentUrl: `/api/attachments/${record.id}/content`,
      downloadUrl: `/api/attachments/${record.id}/content`
    };
  }

  function safeFileName(name) {
    return String(name || "attachment").replace(/[\\/:*?"<>|\r\n\t]/g, "_").slice(0, 180) || "attachment";
  }

  function detectUpload(buffer, declaredMime, fileName) {
    const extension = String(fileName).split(".").pop().toLowerCase();
    const byExtension = EXTENSION_MIME[extension] || null;
    if (buffer.length >= 5 && buffer.subarray(0, 5).toString("ascii") === "%PDF-") return { mime: "application/pdf", previewable: true };
    if (buffer.length >= 8 && buffer.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) return { mime: "image/png", previewable: true };
    if (buffer.length >= 3 && buffer[0] === 0xff && buffer[1] === 0xd8 && buffer[2] === 0xff) return { mime: "image/jpeg", previewable: true };
    if (buffer.length >= 6 && ["GIF87a", "GIF89a"].includes(buffer.subarray(0, 6).toString("ascii"))) return { mime: "image/gif", previewable: true };
    if (buffer.length >= 12 && buffer.subarray(0, 4).toString("ascii") === "RIFF" && buffer.subarray(8, 12).toString("ascii") === "WEBP") return { mime: "image/webp", previewable: true };
    const mime = UPLOAD_MIME.has(declaredMime) ? declaredMime : (byExtension || "application/octet-stream");
    return { mime, previewable: UPLOAD_MIME.has(mime) && mime !== "text/plain" };
  }

  async function readUploadedAttachment(record) {
    return fs.readFile(path.join(uploadsDir, record.storedName));
  }

  function taskPerson(email) {
    const normalized = normalizeEmail(email);
    if (normalized === normalizeEmail(db.user.email)) return senderPerson(db.user);
    const person = db.people.find((item) => normalizeEmail(item.email) === normalized);
    if (!person) throw new ApiError(400, "TASK_ASSIGNEE", "Choose a person from the Alt8Mail directory");
    return senderPerson(person);
  }

  function validateTaskInput(input, existing = null) {
    const next = existing ? { ...existing } : {
      id: id("task"),
      status: "open",
      priority: "normal",
      notes: "",
      due: "",
      dueTime: "",
      tags: [],
      assignee: senderPerson(db.user),
      createdBy: db.user.email,
      source: "manual",
      createdAt: nowIso(),
      updatedAt: nowIso(),
      completedAt: null,
      linkedThreadId: null,
      linkedSubject: null
    };
    if (input.title !== undefined) {
      const title = String(input.title).trim();
      if (!title || title.length > 200) throw new ApiError(400, "TASK_TITLE", "Task title must be between 1 and 200 characters");
      next.title = title;
    }
    if (!next.title) throw new ApiError(400, "TASK_TITLE", "Task title is required");
    if (input.notes !== undefined) next.notes = String(input.notes).slice(0, 5000);
    if (input.due !== undefined) {
      if (input.due && !/^\d{4}-\d{2}-\d{2}$/.test(input.due)) throw new ApiError(400, "TASK_DUE", "Due date must be YYYY-MM-DD");
      next.due = input.due || "";
    }
    if (input.dueTime !== undefined) {
      if (input.dueTime && !/^([01]\d|2[0-3]):[0-5]\d$/.test(input.dueTime)) throw new ApiError(400, "TASK_DUE", "Due time must be HH:MM");
      next.dueTime = input.dueTime || "";
    }
    if (input.priority !== undefined) {
      if (!["low", "normal", "high", "urgent"].includes(input.priority)) throw new ApiError(400, "TASK_PRIORITY", "Choose a valid priority");
      next.priority = input.priority;
    }
    if (input.status !== undefined) {
      if (!["open", "done"].includes(input.status)) throw new ApiError(400, "TASK_STATUS", "Choose a valid task status");
      next.status = input.status;
      next.completedAt = input.status === "done" ? (existing && existing.completedAt) || nowIso() : null;
    }
    if (input.assigneeEmail !== undefined || (input.assignee && input.assignee.email)) {
      next.assignee = taskPerson(input.assigneeEmail || input.assignee.email);
    }
    if (input.tags !== undefined) {
      if (!Array.isArray(input.tags) || input.tags.length > 10) throw new ApiError(400, "TASK_TAGS", "Use up to ten tags");
      next.tags = input.tags.map((tag) => String(tag).trim().slice(0, 24)).filter(Boolean);
    }
    if (input.linkedThreadId !== undefined) next.linkedThreadId = input.linkedThreadId ? String(input.linkedThreadId).slice(0, 80) : null;
    if (input.linkedSubject !== undefined) next.linkedSubject = input.linkedSubject ? String(input.linkedSubject).slice(0, 240) : null;
    next.updatedAt = nowIso();
    return next;
  }

  function findFile(fileId) {
    return db.files.find((file) => file.id === fileId) || null;
  }

  function providerById(providerId) {
    return db.storageProviders.find((provider) => provider.id === providerId) || null;
  }

  function attachmentBadgeType(mime, name) {
    if (mime === "application/pdf") return "PDF";
    if (String(mime).startsWith("image/")) return "IMG";
    if (mime.includes("spreadsheet") || mime.includes("ms-excel")) return "XLS";
    if (mime.includes("word")) return "DOC";
    if (mime.includes("presentation") || mime.includes("powerpoint")) return "PPT";
    if (mime.includes("zip")) return "ZIP";
    if (mime.startsWith("text/")) return "TXT";
    const extension = String(name || "").split(".").pop();
    return extension ? extension.toUpperCase().slice(0, 4) : "FILE";
  }

  function providerSeedFiles(providerId) {
    const now = nowIso();
    const base = [
      {
        dropbox: [
          ["file_dropbox_brand_assets", "Brand-assets-2026.zip", "ZIP", "156 MB", "Design / Brand", "Campaign source files from the agency."],
          ["file_dropbox_recap", "Partner-summit-recap.docx", "DOC", "2.2 MB", "Partners / Summit", "Recap and follow-ups from the partner summit."]
        ],
        gdrive: [
          ["file_gdrive_planning", "Hiring-plan-2027.xlsx", "XLS", "880 KB", "People / Planning", "Approved headcount plan by team."],
          ["file_gdrive_research", "Market-research-2026.pdf", "PDF", "3.4 MB", "Strategy / Research", "Market landscape study."]
        ],
        s3: [
          ["file_s3_inventory", "bucket-inventory.csv", "TXT", "24 KB", "Finance / Billing", "S3 bucket inventory export."],
          ["file_s3_media", "launch-media-exports.zip", "ZIP", "1.2 GB", "Marketing / Launch", "Approved launch media exports."]
        ]
      }[providerId] || []
    ].flat();
    return base.map(([id, name, type, size, pathName, description]) => ({
      id,
      name,
      type,
      mime: FILE_MIME_BY_TYPE[type] || "application/octet-stream",
      size,
      sizeBytes: Number(size.split(" ")[0]) * (size.includes("GB") ? GIB : size.includes("MB") ? MIB : size.includes("KB") ? 1024 : 1),
      providerId,
      path: pathName,
      owner: senderPerson(db.user),
      modifiedAt: now,
      createdAt: now,
      starred: false,
      sharedWith: [],
      locked: false,
      version: "1",
      previewable: false,
      contentUrl: null,
      description
    }));
  }

  function createFileRecord({ name, type, mime, sizeBytes, providerId, path: filePath, contentUrl = null, description = "", attachmentId = null }) {
    return {
      id: id("file"),
      name: safeFileName(name || "Untitled"),
      type: String(type || attachmentBadgeType(mime, name)).toUpperCase().slice(0, 5),
      mime: mime || "application/octet-stream",
      size: formatBytes(sizeBytes),
      sizeBytes: Number(sizeBytes) || 0,
      providerId,
      path: String(filePath || "Uploads").slice(0, 160),
      owner: senderPerson(db.user),
      modifiedAt: nowIso(),
      createdAt: nowIso(),
      starred: false,
      sharedWith: [],
      locked: false,
      version: "1",
      previewable: !!contentUrl,
      contentUrl,
      description: String(description || "").slice(0, 500),
      attachmentId
    };
  }

  function findMessage(messageId) {
    return db.messages.find((message) => message.id === messageId) || null;
  }

  function parseRecipients(input) {
    const raw = [];
    if (Array.isArray(input)) {
      for (const item of input) {
        if (typeof item === "string") raw.push(...item.split(/[,;]+/));
        else if (item && item.email) raw.push(item.email);
      }
    } else if (input) {
      raw.push(...String(input).split(/[,;]+/));
    }
    const people = [];
    const invalid = [];
    const seen = new Set();
    for (const chunk of raw) {
      const text = String(chunk || "").trim();
      if (!text) continue;
      const angle = text.match(/^(.*?)<([^>]+)>$/);
      const email = normalizeEmail(angle ? angle[2] : text);
      if (!looksLikeEmail(email)) {
        invalid.push(text);
        continue;
      }
      if (seen.has(email)) continue;
      seen.add(email);
      const known = db.people.find((person) => normalizeEmail(person.email) === email);
      const name = angle && angle[1].trim() ? angle[1].trim() : known ? known.name : displayNameFromEmail(email);
      people.push(senderPerson({
        name,
        email,
        initials: known ? known.initials : undefined,
        color: known ? known.color : undefined
      }));
    }
    return { people, invalid };
  }

  function normalizeAttachments(input) {
    if (!Array.isArray(input)) return [];
    return input.slice(0, 20).map((file) => {
      if (file && typeof file.id === "string" && db.attachments[file.id]) {
        return attachmentPublic(db.attachments[file.id]);
      }
      const name = safeFileName((file && file.name) || "attachment");
      const sizeBytes = Math.max(0, Math.min(100 * 1024 * 1024, Number(file && file.sizeBytes) || 0));
      const type = String((file && file.type) || name.split(".").pop() || "FILE").replace(/[^A-Za-z0-9]/g, "").slice(0, 8).toUpperCase() || "FILE";
      const sampleUrl = file && typeof file.contentUrl === "string" && /^\/samples\/[A-Za-z0-9._-]+$/.test(file.contentUrl)
        ? file.contentUrl
        : null;
      const mime = sampleUrl ? (String(file.mime || "").startsWith("image/") ? String(file.mime) : "application/pdf") : (file && file.mime ? String(file.mime).slice(0, 120) : EXTENSION_MIME[name.split(".").pop().toLowerCase()] || "application/octet-stream");
      return {
        id: file && typeof file.id === "string" && /^att_[\w-]{3,40}$/.test(file.id) ? file.id : id("att"),
        name,
        size: file && file.size ? String(file.size).slice(0, 24) : formatBytes(sizeBytes),
        sizeBytes,
        type,
        mime,
        previewable: !!sampleUrl,
        contentUrl: sampleUrl,
        downloadUrl: sampleUrl
      };
    });
  }

  function normalizeMentions(input) {
    if (!Array.isArray(input)) return [];
    const out = [];
    const seen = new Set();
    for (const raw of input) {
      const email = normalizeEmail(typeof raw === "string" ? raw : (raw && raw.email) || "");
      if (!email || !looksLikeEmail(email) || seen.has(email)) continue;
      seen.add(email);
      out.push({ email });
      if (out.length >= 20) break;
    }
    return out;
  }

  function aliasForEmail(email) {
    return db.user.aliases.find((alias) => alias.canSend && normalizeEmail(alias.email) === normalizeEmail(email)) || null;
  }

  function aliasPerson(alias) {
    return {
      name: alias.label,
      email: alias.email,
      initials: initialsFrom(alias.label, alias.email),
      color: db.user.color
    };
  }

  function publicSieve() {
    return {
      revision: db.sieve.revision,
      updatedAt: db.sieve.updatedAt,
      rules: db.sieve.rules,
      vacation: db.sieve.vacation,
      script: db.sieve.script || renderSieve(db.sieve, db.user)
    };
  }

  function drainDraft(input, existing) {
    const subject = String(input.subject || "").slice(0, 500);
    const bodyText = String(input.bodyText == null ? "" : input.bodyText).slice(0, 200_000);
    const toText = String(input.toText == null ? input.to || "" : input.toText).slice(0, 2000);
    const ccText = String(input.ccText == null ? input.cc || "" : input.ccText).slice(0, 2000);
    const bccText = String(input.bccText == null ? input.bcc || "" : input.bccText).slice(0, 2000);
    const to = parseRecipients(toText).people;
    const cc = parseRecipients(ccText).people;
    const bcc = parseRecipients(bccText).people;
    const fromEmail = String(input.fromEmail || (existing && existing.from && existing.from.email) || db.user.email);
    const alias = aliasForEmail(fromEmail);
    if (!alias) throw new ApiError(403, "BAD_FROM", "That From identity is not available for sending");
    const attachments = normalizeAttachments(input.attachments || (existing && existing.attachments) || []);
    const bodyHtml = textToHtml(bodyText);
    const sizeBytes = 4 * 1024 + Buffer.byteLength(bodyText, "utf8") + attachments.reduce((sum, file) => sum + file.sizeBytes, 0);
    const now = nowIso();
    return {
      id: existing ? existing.id : null,
      folder: "drafts",
      from: aliasPerson(alias),
      fromEmail: alias.email,
      to,
      toText,
      cc,
      ccText,
      bcc,
      bccText,
      subject,
      snippet: snippetOf(bodyText || subject || "Draft"),
      bodyHtml,
      bodyText,
      date: now,
      updatedAt: now,
      createdAt: existing ? existing.createdAt : now,
      unread: false,
      starred: false,
      draft: true,
      attachments,
      mentions: normalizeMentions(input.mentions || (existing && existing.mentions) || []),
      categories: [],
      classification: "Internal",
      external: false,
      auth: {
        spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
        ip: "127.0.0.1", signer: "a8m.example", firstContact: false, warnings: []
      },
      event: null,
      sizeBytes,
      messageId: existing ? existing.messageId : `<${id("draft")}@a8m.example>`,
      inReplyTo: input.inReplyTo == null ? (existing && existing.inReplyTo) || null : String(input.inReplyTo).slice(0, 400),
      threadId: String(input.threadId || (existing && existing.threadId) || newThreadId()).slice(0, 100),
      priority: ["low", "normal", "high"].includes(input.priority) ? input.priority : (existing && existing.priority) || "normal",
      receipt: !!input.receipt,
      delivery: !!input.delivery,
      saveCopy: input.saveCopy !== false,
      windowMode: ["floating", "minimized", "inline"].includes(input.windowMode)
        ? input.windowMode
        : (existing && existing.windowMode) || "floating",
      viaApi: true
    };
  }

  function resolveHold(input) {
    if (holdOverride != null) return holdOverride;
    const requested = Number(input.holdSeconds);
    const base = Number.isFinite(requested) && requested > 0 ? requested * 1000 : 8000;
    return Math.max(5000, Math.min(10_000, base));
  }

  function restoreDraftFromQueue(item) {
    const now = nowIso();
    const snapshot = item.draftSnapshot || {};
    const draft = {
      ...snapshot,
      id: item.draftId || id("draft"),
      folder: "drafts",
      draft: true,
      from: item.from,
      fromEmail: item.from && item.from.email,
      to: item.to,
      toText: snapshot.toText || (item.to || []).map((person) => person.email).join(", "),
      cc: item.cc || [],
      ccText: snapshot.ccText || (item.cc || []).map((person) => person.email).join(", "),
      bcc: item.bcc || [],
      bccText: snapshot.bccText || (item.bcc || []).map((person) => person.email).join(", "),
      subject: item.subject || "",
      snippet: snippetOf(item.bodyText || item.subject || "Draft"),
      bodyText: item.bodyText || "",
      bodyHtml: textToHtml(item.bodyText || ""),
      attachments: item.attachments || [],
      mentions: normalizeMentions(item.mentions || snapshot.mentions || []),
      date: now,
      updatedAt: now,
      createdAt: snapshot.createdAt || now,
      unread: false,
      starred: false,
      sizeBytes: 4 * 1024 + Buffer.byteLength(item.bodyText || "", "utf8") + (item.attachments || []).reduce((sum, file) => sum + (file.sizeBytes || 0), 0),
      threadId: item.threadId || newThreadId(),
      inReplyTo: item.inReplyTo || null,
      windowMode: "floating",
      external: false,
      auth: snapshot.auth || {
        spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
        ip: "127.0.0.1", signer: "a8m.example", firstContact: false, warnings: []
      }
    };
    return draft;
  }

  function commitSend(itemId) {
    const item = db.outbox.find((entry) => entry.id === itemId);
    if (!item) return null;
    const timer = sendTimers.get(itemId);
    if (timer) clearTimeout(timer);
    sendTimers.delete(itemId);
    db.outbox = db.outbox.filter((entry) => entry.id !== itemId);
    const now = nowIso();
    let sentMessageId = null;
    if (item.saveCopy !== false) {
      const sent = {
        id: id("msg"),
        threadId: item.threadId || newThreadId(),
        folder: "sent",
        from: item.from,
        to: item.to,
        cc: item.cc || [],
        bcc: item.bcc || [],
        subject: item.subject || "(no subject)",
        snippet: snippetOf(item.bodyText || "(no body)"),
        bodyHtml: textToHtml(item.bodyText || ""),
        bodyText: item.bodyText || "",
        date: now,
        unread: false,
        starred: false,
        draft: false,
        attachments: item.attachments || [],
        categories: [],
        classification: item.classification || "Internal",
        external: false,
        auth: {
          spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
          ip: "127.0.0.1", signer: "a8m.example", firstContact: false, warnings: []
        },
        event: null,
        sizeBytes: 4 * 1024 + Buffer.byteLength(item.bodyText || "", "utf8") + (item.attachments || []).reduce((sum, file) => sum + (file.sizeBytes || 0), 0),
        messageId: `<${id("sent")}@a8m.example>`,
        inReplyTo: item.inReplyTo || null,
        priority: item.priority || "normal",
        receipt: !!item.receipt,
        delivery: !!item.delivery,
        saveCopy: true,
        viaApi: true,
        windowMode: "floating"
      };
      db.messages.push(sent);
      sentMessageId = sent.id;
    }
    db.user.outbound.events.push({ at: now, id: item.id, messageId: sentMessageId });
    addAudit(
      { headers: { "user-agent": item.userAgent || "" }, socket: { remoteAddress: item.ip || "127.0.0.1" } },
      "send.committed",
      item.subject || "(no subject)",
      `Delivered to ${(item.to || []).map((person) => person.email).join(", ")} · alias ${item.from.email}`,
      db.user.name
    );
    invalidateIndex();
    store.save().catch((error) => console.error("send commit save failed", error));
    return sentMessageId;
  }

  function scheduleSend(itemId) {
    const item = db.outbox.find((entry) => entry.id === itemId);
    if (!item) return;
    if (sendTimers.has(itemId)) clearTimeout(sendTimers.get(itemId));
    const delay = Math.max(0, Date.parse(item.sendAt) - Date.now());
    const timer = setTimeout(() => commitSend(itemId), delay);
    timer.unref?.();
    sendTimers.set(itemId, timer);
  }

  function authPayload(session) {
    const mailbox = delegatedMailboxFor(session);
    return {
      user: mailbox ? delegatedPublicUser(mailbox) : publicUser(db.user),
      csrf: session.csrf,
      expiresAt: new Date(session.expiresAt).toISOString(),
      sessionKind: mailbox ? "delegated" : "standard",
      delegated: Boolean(mailbox),
      delegatedCanSend: mailbox ? mailbox.canSend === true : false
    };
  }

  function calendarEventPayload(event) {
    const date = String(event.date || "");
    const sequence = Number(event.sequence) || 0;
    const attendeeList = (Array.isArray(event.attendeeList) ? event.attendeeList
      : (Array.isArray(event.attendeeEmails) ? event.attendeeEmails.filter(Boolean).map((email) => ({
          email: String(email).toLowerCase(), name: String(event.attendeeNames?.[email] || ""),
          status: "needs-action", organizer: false, role: "REQ-PARTICIPANT", rsvp: true,
          lastInviteSequence: sequence, lastResponseSequence: -1, respondedAt: ""
        })) : [])).map((guest) => ({ ...guest, lastDeliveryStatus: guest.lastDeliveryStatus || "sent" }));
    return {
      calendarId: "default", calendarName: "Calendar", calendarColor: "#0f6cbd",
      calendarKind: "personal", timezone: "UTC", allDay: false, recurrenceRule: "",
      status: "confirmed", visibility: "default", organizer: db.user.email,
      organizerName: db.user.name, attachments: [], reminderMinutes: 15, transparency: "busy",
      canEdit: true, owned: true, access: "owner", invited: false, myStatus: "",
      responseRequested: true, createdAt: event.createdAt || nowIso(), updatedAt: event.updatedAt || nowIso(),
      startsAt: `${date}T${event.start || "09:00"}:00Z`, endsAt: `${date}T${event.end || "10:00"}:00Z`,
      ...event,
      sequence,
      attendeeList,
      exceptions: Array.isArray(event.exceptions) ? event.exceptions : [],
      proposals: Array.isArray(event.proposals) ? event.proposals : [],
      resourceList: Array.isArray(event.resourceList) ? event.resourceList
        : (Array.isArray(event.resourceIds) ? calendarResourceList().filter((resource) => event.resourceIds.includes(resource.id)) : []),
      senderEmail: String(event.senderEmail || ""), senderName: String(event.senderName || ""),
      rsvpAnalytics: calendarRSVPAnalytics(attendeeList.filter((guest) => !guest.organizer)),
      attendees: attendeeList.length || Number(event.attendees) || 0
    };
  }

  function calendarRSVPAnalytics(guests) {
    if (guests.length <= 10) return null;
    const responded = guests.filter((guest) => ["accepted", "tentative", "declined"].includes(guest.status)).length;
    const delivered = guests.filter((guest) => guest.lastDeliveryStatus === "sent").length;
    const failed = guests.filter((guest) => guest.lastDeliveryStatus === "failed").length;
    const domains = new Map();
    guests.forEach((guest) => {
      const domain = String(guest.email || "").split("@")[1] || "unknown";
      const item = domains.get(domain) || { domain, total: 0, responded: 0, delivered: 0, failed: 0 };
      item.total += 1;
      if (["accepted", "tentative", "declined"].includes(guest.status)) item.responded += 1;
      if (guest.lastDeliveryStatus === "sent") item.delivered += 1;
      if (guest.lastDeliveryStatus === "failed") item.failed += 1;
      domains.set(domain, item);
    });
    return {
      total: guests.length, responded, delivered, failed,
      responseRate: Math.round(responded * 1000 / guests.length) / 10,
      averageResponseMinutes: 0,
      domains: [...domains.values()].map((item) => ({
        ...item, responseRate: Math.round(item.responded * 1000 / item.total) / 10
      })).sort((a, b) => a.domain.localeCompare(b.domain))
    };
  }

  function noteTitle(content) {
    const line = String(content || "").split("\n").map((value) => value.trim())
      .find((value) => value && !/^[-*]\s*\[[ xX]\]\s*$/.test(value));
    return line ? line.replace(/^[-*]\s*(\[[ xX]\]\s*)?/, "").slice(0, 90) : "Untitled note";
  }

  function normalizeMockNoteLink(link) {
    if (!link) return null;
    if (typeof link === "string") return { type: "thread", id: link, threadId: link };
    if (typeof link !== "object") return null;
    let type = String(link.type || "").toLowerCase();
    let linkId = String(link.id || "");
    if (!type) {
      if (link.threadId) type = "thread";
      else if (link.eventId) type = "event";
      else if (link.fileId) type = "file";
    }
    if (!linkId) {
      if (type === "thread") linkId = String(link.threadId || "");
      else if (type === "event") linkId = String(link.eventId || "");
      else if (type === "file") linkId = String(link.fileId || "");
    }
    if (!["thread", "event", "file"].includes(type) || !linkId) return null;
    return { ...link, type, id: linkId };
  }

  function mockNoteLink(link) {
    const normalized = normalizeMockNoteLink(link);
    if (!normalized) return null;
    if (normalized.type === "thread") {
      const latest = db.messages.filter((message) => (message.threadId || message.id) === normalized.id)
        .sort((a, b) => Date.parse(b.date) - Date.parse(a.date))[0];
      if (!latest) return null;
      return {
        type: "thread", id: normalized.id, threadId: normalized.id,
        subject: latest.subject || "(no subject)",
        fromName: latest.from?.name || latest.from?.email || "",
        fromEmail: latest.from?.email || "",
        date: latest.date
      };
    }
    if (normalized.type === "event") {
      const event = db.calendarEvents.find((item) => item.id === normalized.id);
      if (!event) return null;
      const date = String(event.date || "");
      return {
        type: "event", id: event.id, title: event.title || "Calendar event",
        startAt: event.startsAt || `${date}T${event.start || "09:00"}:00Z`,
        endAt: event.endsAt || `${date}T${event.end || "10:00"}:00Z`,
        allDay: Boolean(event.allDay), location: event.where || "",
        calendarName: "Calendar", status: event.status || "confirmed", date
      };
    }
    const file = db.files.find((item) => item.id === normalized.id);
    if (!file) return null;
    return {
      type: "file", id: file.id, name: file.name, mime: file.mime,
      sizeBytes: Number(file.sizeBytes) || 0, size: file.size || formatBytes(Number(file.sizeBytes) || 0),
      folderId: file.folderId || "", date: file.modifiedAt || nowIso()
    };
  }

  function mockNoteLinks(links) {
    const result = [];
    const seen = new Set();
    for (const link of Array.isArray(links) ? links : []) {
      const normalized = mockNoteLink(link);
      if (!normalized) continue;
      const key = `${normalized.type}:${normalized.id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      result.push(normalized);
      if (result.length >= 12) break;
    }
    return result;
  }

  function noteBoard(notes, includeArchived = true) {
    const all = Array.isArray(notes) ? notes : [];
    for (const note of all) note.links = mockNoteLinks(note.links);
    const active = all.filter((note) => !note.archived);
    const pinned = active.find((note) => note.inboxPinned) || null;
    const team = Boolean(db.notesTeam);
    return {
      notes: includeArchived ? all : active,
      pinned,
      activeCount: active.length,
      archivedCount: all.filter((note) => note.archived).length,
      shared: team, team, teamCount: team ? 1 : 0, canEdit: true,
      mailboxes: [{ id: db.user.id, name: db.user.name, email: db.user.email, initials: db.user.initials, color: db.user.color, canEdit: true, self: true }],
      owner: { id: db.user.id, name: db.user.name, email: db.user.email, initials: db.user.initials, color: db.user.color }
    };
  }

  function calendarResourceList() {
    return Array.isArray(db.calendarResources) ? db.calendarResources : [];
  }

  function calendarList() {
    return [{
      id: "default", name: "Calendar", color: "#0f6cbd", kind: "personal", visible: true,
      isDefault: true, owned: true, access: "owner", shared: false, published: false,
      eventCount: db.calendarEvents.length, ownerEmail: db.user.email, ownerName: db.user.name,
      createdAt: nowIso(), updatedAt: nowIso()
    }];
  }

  function addressBookList() {
    return [{
      id: "default", name: "Contacts", color: "#3d6b62", isDefault: true, source: "local",
      count: db.people.filter((person) => person.source !== "directory").length, createdAt: nowIso()
    }];
  }

  function storageQuotaInfo() {
    const quota = quotaInfo();
    const cabinetUsed = db.files.filter((file) => file.providerId === "a8m").reduce((sum, file) => sum + (Number(file.sizeBytes) || 0), 0);
    const used = quota.usedBytes + cabinetUsed;
    return {
      policy: { mode: "combined", emailQuotaBytes: 0, storageQuotaBytes: 0, source: "organization", domain: db.user.email.split("@")[1] || "" },
      mode: "combined", combined: true, source: "organization",
      mailUsedBytes: quota.usedBytes, mailLimitBytes: quota.limitBytes,
      cabinetUsedBytes: cabinetUsed, storageLimitBytes: 0,
      usedBytes: used, limitBytes: quota.limitBytes,
      percent: quota.limitBytes ? Math.round((used / quota.limitBytes) * 1000) / 10 : 0,
      label: `${formatBytes(used)} of ${formatBytes(quota.limitBytes)}`,
      refreshedAt: nowIso()
    };
  }

  function mockDlpEvaluate(input = {}) {
    const classificationRank = { Public: 1, Internal: 2, Confidential: 3, Restricted: 4 };
    let classification = MOCK_DLP.labels.includes(input.classification) ? input.classification : MOCK_DLP.defaultClassification;
    const warnings = [];
    const matches = [];
    let blocked = false;
    const subject = String(input.subject || "").toLowerCase();
    const body = String(input.bodyText || input.bodyHtml || "").toLowerCase();
    const attachments = (Array.isArray(input.attachments) ? input.attachments : []).map((item) => String(item?.name || item?.filename || "")).join("\n").toLowerCase();
    for (const rule of MOCK_DLP.rules) {
      if (!rule.enabled) continue;
      for (const term of rule.terms) {
        const needle = String(term).toLowerCase();
        for (const field of rule.fields) {
          const haystack = field === "subject" ? subject : field === "attachment" ? attachments : body;
          if (!haystack.includes(needle)) continue;
          matches.push({ ruleId: rule.id, ruleName: rule.name, term, field, action: rule.action, classification: rule.classification });
          if ((classificationRank[rule.classification] || 0) > (classificationRank[classification] || 0)) classification = rule.classification;
          if (rule.action === "block") {
            blocked = true;
            warnings.push(`Blocked by "${rule.name}": "${term}" appears in the ${field}.`);
          } else if (rule.action === "warn") {
            warnings.push(`Policy "${rule.name}" matched "${term}" in the ${field}.`);
          }
        }
      }
    }
    return { allowed: !blocked, blocked, classification, defaultClassification: MOCK_DLP.defaultClassification, labels: MOCK_DLP.labels, enabled: MOCK_DLP.enabled, warnings, matches };
  }

  function bootstrapPayload(session) {
    const mailbox = delegatedMailboxFor(session);
    const payload = {
      user: mailbox ? delegatedPublicUser(mailbox) : publicUser(db.user),
      organizationName: "Alt8Mail",
      aliases: db.user.aliases,
      folders: db.folders,
      people: db.people.map((person) => ({ ...person, source: person.source || "directory" })),
      messages: db.messages.map(publicMessage),
      calendarEvents: db.calendarEvents.map(calendarEventPayload),
      calendars: calendarList(),
      calendarSubscriptions: [],
      addressBooks: addressBookList(),
      fileFolders: [],
      storageQuota: storageQuotaInfo(),
      tasks: db.tasks,
      files: db.files,
      storageProviders: db.storageProviders,
      defaultStorage: db.defaultStorage || "a8m",
      sieve: publicSieve(),
      quota: quotaInfo(),
      outbound: outboundInfo(),
      pendingSends: db.outbox.map(publicOutboxItem),
      threadMeta: db.threadMeta.slice(),
      serverNotifications: db.notifications.slice().sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)).slice(0, 50),
      unsubscribeActions: db.unsubscribeActions.slice(),
      signatures: db.signatures,
      ai: { enabled: aiEnabled, languages: [], styles: [] },
      push: { enabled: false, publicKey: "", subscribed: false, includeSender: true, includeSubject: true },
      tenantPolicy: {
        mail: { externalTag: true, quarantine: false },
        directory: { contactSharing: true, externalDirectory: false, profileEditing: true, autoAddExternal: true },
        sharing: { externalSharing: true, linkDefault: "view", allowedDomains: [], guestInvites: false },
        dlp: { enabled: MOCK_DLP.enabled, defaultClassification: MOCK_DLP.defaultClassification, labels: MOCK_DLP.labels },
        features: { stickyNotes: true, reactions: true },
        branding: { ...MOCK_BRANDING }
      },
      branding: { ...MOCK_BRANDING },
      auditCount: db.audit.length,
      capabilities: ["threads", "instant-search", "bulk-actions", "undo", "server-drafts", "undo-send", "scheduled-send", "sieve", "automatic-replies", "sender-trust", "outbound-caps", "quota", "audit-log", "pwa", "calendar", "directory", "tasks", "files", "storage-connectors", "attachment-preview", "two-factor", "passkeys", "preferences", "mail-pulse", "sticky-notes", "reaction-catalog"],
      attachmentMaxBytes: MAX_UPLOAD_BYTES,
      csrf: session.csrf,
      mailSync: { lastError: null, lastSyncAt: nowIso() },
      demo: { email: DEMO_EMAIL, holdSeconds: holdOverride != null ? holdOverride / 1000 : 8 },
      serverTime: nowIso()
    };
    if (mailbox) {
      payload.folders = delegatedFolders(mailbox);
      payload.messages = (mailbox.messages || []).map(publicMessage);
      payload.calendarEvents = [];
      payload.calendars = [];
      payload.calendarSubscriptions = [];
      payload.addressBooks = [];
      payload.tasks = [];
      payload.files = [];
      payload.fileFolders = [];
      payload.storageProviders = [];
      payload.pendingSends = [];
      payload.threadMeta = [];
      payload.serverNotifications = [];
      payload.unsubscribeActions = [];
      payload.signatures = [];
      payload.sieve = null;
      payload.delegatedMailboxes = [];
      payload.sessionKind = "delegated";
      payload.delegated = true;
      payload.delegatedCanSend = mailbox.canSend === true;
      payload.aliases = delegatedPublicUser(mailbox).aliases;
    } else {
      payload.delegatedMailboxes = (db.delegatedMailboxes || []).map(delegatedMailboxView);
      payload.sessionKind = "standard";
      payload.delegated = false;
      payload.delegatedCanSend = false;
    }
    return payload;
  }

  function pruneUndo() {
    const now = Date.now();
    db.undo = db.undo.filter((entry) => entry.expiresAt > now);
  }

  async function serveStatic(req, res, pathname) {
    if (req.method !== "GET" && req.method !== "HEAD") {
      res.writeHead(405, { Allow: "GET, HEAD" });
      res.end("Method not allowed");
      return;
    }
    let relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
    try {
      relative = decodeURIComponent(relative);
    } catch {
      res.writeHead(400); res.end("Bad path"); return;
    }
    if (relative.includes("\0")) {
      res.writeHead(400); res.end("Bad path"); return;
    }
    const normalized = path.normalize(relative);
    if (normalized.startsWith("..") || path.isAbsolute(normalized)) {
      res.writeHead(403, { "Content-Type": "text/plain; charset=utf-8" });
      res.end("Forbidden");
      return;
    }
    const target = path.join(publicDir, normalized);
    if (target !== publicDir && !target.startsWith(publicDir + path.sep)) {
      res.writeHead(403, { "Content-Type": "text/plain; charset=utf-8" });
      res.end("Forbidden");
      return;
    }
    let stat;
    try {
      stat = await fs.stat(target);
    } catch {
      res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" });
      res.end("Not found");
      return;
    }
    if (!stat.isFile()) {
      res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
      res.end("Not found");
      return;
    }
    const ext = path.extname(target).toLowerCase();
    const etag = `"${stat.size.toString(16)}-${Math.round(stat.mtimeMs).toString(16)}"`;
    if (req.headers["if-none-match"] === etag) {
      res.writeHead(304, { ETag: etag, "Cache-Control": "no-cache" });
      res.end();
      return;
    }
    const headers = {
      "Content-Type": MIME[ext] || "application/octet-stream",
      "Content-Length": stat.size,
      ETag: etag,
      "Cache-Control": normalized === "index.html" || ext === ".webmanifest" || normalized === "sw.js" ? "no-cache" : "public, max-age=0, must-revalidate",
      "Last-Modified": new Date(stat.mtimeMs).toUTCString()
    };
    if (normalized === "sw.js") headers["Service-Worker-Allowed"] = "/";
    res.writeHead(200, headers);
    if (req.method === "HEAD") res.end();
    else createReadStream(target).pipe(res);
  }

  const server = http.createServer(async (req, res) => {
    setSecurityHeaders(res);
    try {
      const url = new URL(req.url || "/", `http://${req.headers.host || "localhost"}`);
      const pathname = url.pathname;
      if (!pathname.startsWith("/api/")) {
        await serveStatic(req, res, pathname);
        return;
      }
      if (req.method === "OPTIONS") {
        res.writeHead(204, { Allow: "GET, POST, PUT, PATCH, DELETE" });
        res.end();
        return;
      }

      // ------- public API -------
      if (pathname === "/api/health" && req.method === "GET") {
        sendJson(res, 200, { ok: true, time: nowIso(), version: 2 });
        return;
      }

      if (pathname === "/api/branding" && req.method === "GET") {
        sendJson(res, 200, { ...MOCK_BRANDING });
        return;
      }

      if (pathname === "/api/session" && req.method === "POST") {
        const ip = requestIp(req);
        const attempt = loginAttempts.get(ip);
        if (attempt && attempt.resetAt > Date.now() && attempt.count >= 30) {
          throw new ApiError(429, "LOGIN_RATE_LIMIT", "Too many sign-in attempts. Try again later.");
        }
        const body = await readJson(req);
        const email = normalizeEmail(body.email);
        const method = body.method === "sso" ? "sso" : "password";
        const known = email === normalizeEmail(db.user.email) || db.user.aliases.some((alias) => normalizeEmail(alias.email) === email);
        let ok = false;
        if (known && method === "sso") ok = true;
        else if (known && body.password) {
          const candidate = scryptSync(String(body.password), db.user.password.salt, 64);
          const expected = Buffer.from(db.user.password.hash, "hex");
          ok = candidate.length === expected.length && safeEqual(candidate.toString("hex"), expected.toString("hex"));
        }
        if (!ok) {
          const current = loginAttempts.get(ip) || { count: 0, resetAt: Date.now() + 10 * 60 * 1000 };
          current.count += 1;
          loginAttempts.set(ip, current);
          addAudit(req, "auth.failed", email || "(blank)", `Failed ${method} sign-in`, "Unknown");
          await store.save();
          throw new ApiError(401, "BAD_CREDENTIALS", "That email and password do not match a Alt8Mail account");
        }
        loginAttempts.delete(ip);
        const factor = db.user.security && db.user.security.twoFactor;
        if (method !== "sso" && factor && factor.enabled) {
          const challenge = createTwoFactorChallenge(req, body.remember !== false);
          addAudit(req, "auth.challenge", db.user.email, "Password verified · second factor required");
          await store.save();
          sendJson(res, 200, {
            twoFactorRequired: true,
            challengeId: challenge.id,
            expiresAt: new Date(challenge.expiresAt).toISOString(),
            user: { name: db.user.name, email: db.user.email, initials: db.user.initials, color: db.user.color, role: db.user.role },
            methods: {
              passkey: passkeyList().length > 0,
              authenticator: !!factor.totpSecret,
              recovery: (factor.recoveryCodes || []).some((entry) => !entry.used)
            },
            passkeys: twoFactorSummary(db.user).passkeys,
            demoCode: allowDemoTwoFactorCode && factor.totpSecret ? "123456" : null
          });
          return;
        }
        const established = startSession(req, {
          remember: body.remember !== false,
          detail: method === "sso" ? "New sign-in detected on your email account · single sign-on" : "New sign-in detected on your email account"
        });
        await store.save();
        sendJson(res, 200, { ...established.payload, method }, established.headers);
        return;
      }

      if (pathname === "/api/session/2fa" && req.method === "POST") {
        const body = await readJson(req);
        const challenge = twoFactorChallenges.get(String(body.challengeId || ""));
        if (!challenge) throw new ApiError(401, "TWO_FACTOR_CHALLENGE", "This verification request has expired. Sign in again.");
        if (challenge.expiresAt <= Date.now()) {
          twoFactorChallenges.delete(challenge.id);
          throw new ApiError(410, "TWO_FACTOR_EXPIRED", "This verification request has expired. Sign in again.");
        }
        if (challenge.attempts >= 5) {
          twoFactorChallenges.delete(challenge.id);
          throw new ApiError(429, "TWO_FACTOR_LOCKED", "Too many verification attempts. Sign in again.");
        }
        const factor = db.user.security && db.user.security.twoFactor;
        if (!factor || !factor.enabled) throw new ApiError(409, "TWO_FACTOR_DISABLED", "Two-factor authentication is not enabled");
        const method = String(body.method || "totp");
        let verified = false;
        let detail = "Second factor verified";
        if (method === "passkey") {
          const passkey = (factor.passkeys || []).find((item) => item.id === body.passkeyId);
          if (passkey) {
            passkey.lastUsedAt = nowIso();
            verified = true;
            detail = `Password + passkey · ${passkey.name}`;
          }
        } else if (method === "recovery") {
          const code = String(body.code || "").trim().toUpperCase();
          const entry = (factor.recoveryCodes || []).find((item) => !item.used && item.code === code);
          if (entry) {
            entry.used = true;
            verified = true;
            detail = "Password + recovery code";
          }
        } else {
          const code = String(body.code || "").replace(/\D/g, "");
          if (verifyTotp(factor.totpSecret, code, { window: 1 }) || (allowDemoTwoFactorCode && code === "123456")) {
            verified = true;
            detail = "Password + authenticator code";
          }
        }
        if (!verified) {
          challenge.attempts += 1;
          addAudit(req, "auth.2fa.failed", db.user.email, `Failed second factor · ${method}`, "Unknown");
          await store.save();
          const remaining = Math.max(0, 5 - challenge.attempts);
          if (remaining === 0) twoFactorChallenges.delete(challenge.id);
          throw new ApiError(401, "TWO_FACTOR_INVALID", "That verification code did not match.", { attemptsRemaining: remaining });
        }
        twoFactorChallenges.delete(challenge.id);
        const trustDevice = !!body.trustDevice;
        const ttlMs = trustDevice ? TRUSTED_DEVICE_TTL_MS : SESSION_TTL_MS;
        const established = startSession(req, { remember: challenge.remember, ttlMs, detail: `${detail}${trustDevice ? " · trusted device" : ""}` });
        addAudit(req, "auth.2fa", db.user.email, `Second factor verified · ${method}`);
        await store.save();
        sendJson(res, 200, { ...established.payload, method: "password", twoFactorMethod: method, trustedDevice: trustDevice }, established.headers);
        return;
      }

      // ------- authenticated API -------
      const session = requireSession(req);
      if (["POST", "PUT", "PATCH", "DELETE"].includes(req.method)) requireCsrf(req, session);

      if (pathname === "/api/session" && req.method === "GET") {
        sendJson(res, 200, authPayload(session));
        return;
      }
      if (pathname === "/api/session" && req.method === "DELETE") {
        if (session.delegatedOwnerId) {
          delegatedSessions.delete(session.sid);
          sendJson(res, 200, { ok: true, delegated: true });
          return;
        }
        sessions.delete(session.sid);
        for (const [token, child] of delegatedSessions) {
          if (child.parentSid === session.sid) delegatedSessions.delete(token);
        }
        addAudit(req, "auth.logout", db.user.email, "Signed out of browser session");
        await store.save();
        const secure = isSecure(req);
        sendJson(res, 200, { ok: true }, {
          "Set-Cookie": [
            cookieHeader(SESSION_COOKIE, "", { httpOnly: true, secure, maxAge: 0 }),
            cookieHeader(CSRF_COOKIE, "", { secure, maxAge: 0 })
          ]
        });
        return;
      }
      if (pathname === "/api/delegation/launch" && req.method === "POST") {
        if (session.delegatedOwnerId) throw new ApiError(403, "DELEGATED_CHAIN", "Open another mailbox from your primary window");
        const body = await readJson(req);
        const requested = String(body.ownerId || body.email || "");
        const mailbox = (db.delegatedMailboxes || []).find((item) =>
          item.id === requested || item.ownerId === requested || normalizeEmail(item.email) === normalizeEmail(requested)
        );
        if (!mailbox) throw new ApiError(403, "DELEGATE_DENIED", "You do not have access to that mailbox");
        const code = randomBytes(24).toString("hex");
        delegatedLaunchCodes.set(code, {
          parentSid: session.sid, ownerId: mailbox.id, expiresAt: Date.now() + 90_000
        });
        sendJson(res, 201, {
          code,
          expiresAt: new Date(Date.now() + 90_000).toISOString(),
          mailbox: delegatedMailboxView(mailbox)
        });
        return;
      }
      if (pathname === "/api/delegation/redeem" && req.method === "POST") {
        if (session.delegatedOwnerId) throw new ApiError(403, "DELEGATED_CHAIN", "Open another mailbox from your primary window");
        const body = await readJson(req);
        const code = String(body.code || "");
        const launch = delegatedLaunchCodes.get(code);
        if (!launch || launch.expiresAt < Date.now() || launch.parentSid !== session.sid) {
          throw new ApiError(410, "DELEGATE_LINK_EXPIRED", "This delegated mailbox link has expired. Open it again from your main mailbox.");
        }
        delegatedLaunchCodes.delete(code);
        const mailbox = (db.delegatedMailboxes || []).find((item) => item.id === launch.ownerId);
        if (!mailbox) throw new ApiError(410, "DELEGATE_LINK_EXPIRED", "That delegated mailbox is no longer available");
        const token = randomBytes(32).toString("hex");
        const csrf = randomBytes(24).toString("hex");
        const expiresAt = Math.min(session.expiresAt, Date.now() + SESSION_TTL_MS);
        delegatedSessions.set(token, {
          parentSid: session.sid, delegatedOwnerId: mailbox.id,
          csrf, createdAt: Date.now(), lastSeen: Date.now(), expiresAt
        });
        sendJson(res, 200, {
          token, csrf,
          expiresAt: new Date(expiresAt).toISOString(),
          sessionKind: "delegated", delegated: true,
          delegatedCanSend: mailbox.canSend === true,
          mailbox: delegatedMailboxView(mailbox)
        });
        return;
      }
      if (pathname === "/api/delegation/session" && req.method === "DELETE") {
        if (!session.delegatedOwnerId) throw new ApiError(400, "NOT_DELEGATED", "This is not a delegated mailbox window");
        delegatedSessions.delete(session.sid);
        sendJson(res, 200, { ok: true, delegated: true });
        return;
      }
      if (pathname === "/api/bootstrap" && req.method === "GET") {
        sendJson(res, 200, bootstrapPayload(session));
        return;
      }
      if (pathname === "/api/dlp/check" && req.method === "POST") {
        sendJson(res, 200, mockDlpEvaluate(await readJson(req)));
        return;
      }
      if (pathname === "/api/notes" && req.method === "GET") {
        const archived = new URL(req.url, "http://localhost").searchParams.get("archived") === "1";
        sendJson(res, 200, noteBoard(db.stickyNotes || [], archived));
        return;
      }
      if (pathname === "/api/notes" && req.method === "POST") {
        const body = await readJson(req);
        const notes = db.stickyNotes || [];
        if (notes.length >= 100) throw new ApiError(400, "STICKY_NOTE_LIMIT", "You can keep up to 100 notes in this mailbox");
        const content = String(body.content || "").replace(/[ \t\r\n]+$/, "");
        if (content.length > 2000) throw new ApiError(400, "STICKY_NOTE", "Keep each sticky note under 2,000 characters");
        const color = String(body.color || "yellow").toLowerCase();
        if (!["yellow", "blue", "pink", "green"].includes(color)) throw new ApiError(400, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color");
        let pinned = body.inboxPinned === undefined ? !notes.some((note) => !note.archived && note.inboxPinned) : Boolean(body.inboxPinned);
        if (pinned) notes.forEach((note) => { if (!note.archived) note.inboxPinned = false; });
        const note = {
          id: id("note"), title: noteTitle(content), content, color, collapsed: false,
          inboxPinned: pinned, archived: false,
          position: { x: Math.max(0.04, 0.7 - (notes.length % 4) * 0.06), y: Math.min(0.85, 0.12 + (notes.length % 6) * 0.09) },
          links: mockNoteLinks(body.links), updatedBy: db.user.name, updatedAt: nowIso(), createdAt: nowIso(), revision: 0
        };
        db.stickyNotes = [note, ...notes];
        addAudit(req, "sticky_note.created", note.title, "Created a sticky note");
        await store.save();
        const result = noteBoard(db.stickyNotes);
        result.created = note.id;
        sendJson(res, 201, result);
        return;
      }
      if (pathname === "/api/notes/search" && req.method === "GET") {
        const url = new URL(req.url, "http://localhost");
        const query = String(url.searchParams.get("q") || "").trim().slice(0, 200);
        const archivedParam = String(url.searchParams.get("archived") || "").toLowerCase();
        const archived = ["1", "true", "archived"].includes(archivedParam) ? true : ["0", "false", "active"].includes(archivedParam) ? false : null;
        const ownerId = String(url.searchParams.get("ownerId") || "");
        const limit = Math.max(1, Math.min(100, Number(url.searchParams.get("limit")) || 50));
        noteBoard(db.stickyNotes || []);
        const needle = query.toLowerCase();
        let notes = (db.stickyNotes || []).filter((note) => archived === null || Boolean(note.archived) === archived);
        if (ownerId && ownerId !== db.user.id) notes = [];
        if (needle) notes = notes.filter((note) => `${note.title || ""} ${note.content || ""} ${JSON.stringify(note.links || [])}`.toLowerCase().includes(needle));
        notes = notes.slice().sort((a, b) => (Number(b.inboxPinned) - Number(a.inboxPinned)) || String(b.updatedAt).localeCompare(String(a.updatedAt))).slice(0, limit);
        const owner = { id: db.user.id, name: db.user.name, email: db.user.email, initials: db.user.initials, color: db.user.color };
        const team = Boolean(db.notesTeam);
        sendJson(res, 200, {
          query, scope: url.searchParams.get("scope") === "all" ? "all" : "mailbox", count: notes.length,
          notes: notes.map((note) => ({ ...note, owner, team, teamCount: team ? 1 : 0, canEdit: true })),
          mailboxes: [{ ...owner, canEdit: true, self: true }]
        });
        return;
      }
      if (pathname === "/api/notes/attach" && req.method === "GET") {
        const url = new URL(req.url, "http://localhost");
        const kind = String(url.searchParams.get("type") || "").toLowerCase();
        const query = String(url.searchParams.get("q") || "").trim().toLowerCase();
        let items = [];
        if (kind === "thread") {
          const seen = new Set();
          const ids = [];
          for (const message of db.messages) {
            const threadId = message.threadId || message.id;
            if (!threadId || seen.has(threadId)) continue;
            seen.add(threadId);
            ids.push(threadId);
          }
          items = ids.map((threadId) => mockNoteLink({ type: "thread", id: threadId })).filter(Boolean);
        } else if (kind === "event") {
          items = db.calendarEvents.map((event) => mockNoteLink({ type: "event", id: event.id })).filter(Boolean);
        } else if (kind === "file") {
          items = db.files.map((file) => mockNoteLink({ type: "file", id: file.id })).filter(Boolean);
        } else {
          throw new ApiError(400, "STICKY_LINK_TYPE", "Choose conversations, events or files");
        }
        if (query) {
          items = items.filter((item) => `${item.subject || ""} ${item.title || ""} ${item.name || ""} ${item.fromName || ""} ${item.fromEmail || ""} ${item.location || ""} ${item.mime || ""}`.toLowerCase().includes(query));
        }
        sendJson(res, 200, { type: kind, items: items.slice(0, 40) });
        return;
      }
      const noteMatch = pathname.match(/^\/api\/notes\/([A-Za-z0-9_-]{1,100})$/);
      if (noteMatch && req.method === "PATCH") {
        const body = await readJson(req);
        const notes = db.stickyNotes || [];
        const note = notes.find((item) => item.id === noteMatch[1]);
        if (!note) throw new ApiError(404, "STICKY_NOTE_NOT_FOUND", "Sticky note not found");
        const nextArchived = body.archived !== undefined ? Boolean(body.archived) : Boolean(note.archived);
        if (body.inboxPinned && nextArchived) throw new ApiError(400, "STICKY_NOTE_STATE", "Restore the note before pinning it");
        if (body.content !== undefined) {
          const content = String(body.content || "").replace(/[ \t\r\n]+$/, "");
          if (content.length > 2000) throw new ApiError(400, "STICKY_NOTE", "Keep each sticky note under 2,000 characters");
          note.content = content;
          note.title = noteTitle(content);
        }
        if (body.color !== undefined) {
          const color = String(body.color || "").toLowerCase();
          if (!["yellow", "blue", "pink", "green"].includes(color)) throw new ApiError(400, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color");
          note.color = color;
        }
        if (body.collapsed !== undefined) note.collapsed = Boolean(body.collapsed);
        if (body.position && Number.isFinite(Number(body.position.x)) && Number.isFinite(Number(body.position.y))) {
          note.position = { x: Math.max(0, Math.min(1, Number(body.position.x))), y: Math.max(0, Math.min(1, Number(body.position.y))) };
        }
        if (Array.isArray(body.links)) note.links = mockNoteLinks(body.links);
        if (body.archived !== undefined) {
          note.archived = Boolean(body.archived);
          if (note.archived) note.inboxPinned = false;
        }
        if (body.inboxPinned !== undefined) {
          if (body.inboxPinned && note.archived) throw new ApiError(400, "STICKY_NOTE_STATE", "Restore the note before pinning it");
          note.inboxPinned = Boolean(body.inboxPinned);
          if (note.inboxPinned) notes.forEach((item) => { if (item.id !== note.id && !item.archived) item.inboxPinned = false; });
        }
        note.updatedBy = db.user.name;
        note.updatedAt = nowIso();
        note.revision = Number(note.revision || 0) + 1;
        addAudit(req, note.archived ? "sticky_note.archived" : "sticky_note.updated", note.title, "Updated a sticky note");
        await store.save();
        const result = noteBoard(notes);
        result.updated = note.id;
        sendJson(res, 200, result);
        return;
      }
      if (pathname === "/api/sticky-note" && req.method === "GET") {
        const note = db.stickyNote || {
          content: "", color: "yellow", collapsed: false, enabled: true,
          links: [], position: { x: 0.7, y: 0.14 }, updatedBy: "", updatedAt: ""
        };
        sendJson(res, 200, { note, shared: false, owner: { id: db.user.id, name: db.user.name, email: db.user.email } });
        return;
      }
      if (pathname === "/api/sticky-note" && req.method === "PUT") {
        const body = await readJson(req);
        const current = db.stickyNote || {
          content: "", color: "yellow", collapsed: false, enabled: true,
          links: [], position: { x: 0.7, y: 0.14 }, updatedBy: "", updatedAt: ""
        };
        const note = { ...current };
        if (body.content !== undefined) {
          const content = String(body.content || "").replace(/[ \t\r\n]+$/, "");
          if (content.length > 2000) throw new ApiError(400, "STICKY_NOTE", "Keep the sticky note under 2,000 characters");
          note.content = content;
        }
        if (body.color !== undefined) {
          const color = String(body.color || "").toLowerCase();
          if (!["yellow", "blue", "pink", "green"].includes(color)) throw new ApiError(400, "STICKY_NOTE_COLOR", "Choose a valid sticky-note color");
          note.color = color;
        }
        if (body.collapsed !== undefined) note.collapsed = Boolean(body.collapsed);
        if (body.enabled !== undefined) note.enabled = Boolean(body.enabled);
        if (body.position && Number.isFinite(Number(body.position.x)) && Number.isFinite(Number(body.position.y))) {
          note.position = { x: Math.max(0, Math.min(1, Number(body.position.x))), y: Math.max(0, Math.min(1, Number(body.position.y))) };
        }
        if (Array.isArray(body.links)) {
          const links = [];
          const seen = new Set();
          for (const rawId of body.links.slice(0, 12)) {
            const threadId = String(rawId || "").trim();
            if (!threadId || seen.has(threadId)) continue;
            const candidates = db.messages.filter((message) => message.threadId === threadId).sort((a, b) => Date.parse(b.date) - Date.parse(a.date));
            const latest = candidates[0];
            if (!latest) continue;
            seen.add(threadId);
            links.push({
              threadId, subject: latest.subject || "(no subject)",
              fromName: latest.from?.name || latest.from?.email || "", fromEmail: latest.from?.email || "",
              date: latest.date
            });
          }
          note.links = links;
        }
        note.updatedBy = db.user.name;
        note.updatedAt = nowIso();
        db.stickyNote = note;
        addAudit(req, "sticky_note.updated", "Sticky note", "Updated the mailbox sticky note");
        await store.save();
        sendJson(res, 200, { ok: true, note, shared: false });
        return;
      }
      if (pathname === "/api/mail/avatar" && req.method === "GET") {
        const address = normalizeEmail(new URL(req.url, "http://localhost").searchParams.get("address") || "");
        if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(address)) throw new ApiError(400, "AVATAR_ADDRESS", "Provide a valid sender address");
        const initials = initialsFrom(displayNameFromEmail(address), address);
        const color = colorForEmail(address);
        const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64"><rect width="64" height="64" rx="32" fill="${color}"/><text x="32" y="39" text-anchor="middle" font-family="Segoe UI, Arial, sans-serif" font-size="23" font-weight="700" fill="#fff">${escapeHtml(initials)}</text></svg>`;
        res.writeHead(200, {
          "Content-Type": "image/svg+xml",
          "Cache-Control": "private, max-age=86400",
          "Content-Security-Policy": "sandbox; default-src 'none'; style-src 'unsafe-inline'",
          "X-Content-Type-Options": "nosniff"
        });
        res.end(svg);
        return;
      }
      if (pathname === "/api/signatures" && req.method === "GET") {
        sendJson(res, 200, { signatures: db.signatures });
        return;
      }
      if (pathname === "/api/signatures" && req.method === "POST") {
        const body = await readJson(req);
        const name = String(body.name || "").trim().slice(0, 80);
        const bodyText = String(body.bodyText || "").trim();
        const bodyHtml = String(body.bodyHtml || "").trim();
        if (!name) throw new ApiError(400, "SIGNATURE_NAME", "Give this signature a name");
        if (!bodyText && !bodyHtml) throw new ApiError(400, "SIGNATURE_EMPTY", "Write something in the signature");
        const signature = {
          id: id("sig"), name, bodyText, bodyHtml,
          defaultNew: !!body.defaultNew, defaultReply: !!body.defaultReply, defaultForward: !!body.defaultForward,
          updatedAt: nowIso()
        };
        for (const flag of ["defaultNew", "defaultReply", "defaultForward"]) {
          if (signature[flag]) db.signatures.forEach((item) => { item[flag] = false; });
        }
        db.signatures.push(signature);
        await store.save();
        sendJson(res, 201, { signature });
        return;
      }
      const signatureMatch = pathname.match(/^\/api\/signatures\/([A-Za-z0-9_-]{1,100})$/);
      if (signatureMatch && req.method === "PATCH") {
        const signature = db.signatures.find((item) => item.id === signatureMatch[1]);
        if (!signature) throw new ApiError(404, "SIGNATURE_NOT_FOUND", "Signature not found");
        const body = await readJson(req);
        if (body.name !== undefined) signature.name = String(body.name || "").trim().slice(0, 80) || signature.name;
        if (body.bodyText !== undefined) signature.bodyText = String(body.bodyText || "");
        if (body.bodyHtml !== undefined) signature.bodyHtml = String(body.bodyHtml || "");
        for (const flag of ["defaultNew", "defaultReply", "defaultForward"]) {
          if (body[flag] === true) db.signatures.forEach((item) => { item[flag] = item.id === signature.id; });
          else if (body[flag] === false) signature[flag] = false;
        }
        signature.updatedAt = nowIso();
        await store.save();
        sendJson(res, 200, { signature });
        return;
      }
      if (signatureMatch && req.method === "DELETE") {
        const before = db.signatures.length;
        db.signatures = db.signatures.filter((item) => item.id !== signatureMatch[1]);
        if (db.signatures.length === before) throw new ApiError(404, "SIGNATURE_NOT_FOUND", "Signature not found");
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }
      const aiRoute = pathname.match(/^\/api\/ai\/messages\/([^/]+)\/(reply|summary|translate)$/);
      if (aiRoute && req.method === "POST") {
        if (!aiEnabled) throw new ApiError(403, "AI_DISABLED", "The AI assistant is disabled for this organization");
        const body = await readJson(req);
        const messageId = decodeURIComponent(aiRoute[1]);
        const message = db.messages.find((item) => item.id === messageId);
        if (!message) throw new ApiError(404, "MESSAGE_NOT_FOUND", "Message not found");
        if (aiDelayMs) await new Promise((resolve) => setTimeout(resolve, aiDelayMs));
        const action = aiRoute[2];
        const attachmentCount = Array.isArray(body.attachmentIds) ? body.attachmentIds.length : 0;
        if (action === "reply") {
          sendJson(res, 200, {
            reply: "Thank you for the update. I reviewed the conversation and the supporting context, and I will send the confirmed next steps by Friday.",
            language: "en-US", threadId: message.threadId, provider: "preview", model: "alt8mail-preview",
            attachmentCount
          });
          return;
        }
        if (action === "translate") {
          sendJson(res, 200, { translation: "The conversation confirms the next steps are under review.", language: "en-US", cached: false, provider: "preview", model: "alt8mail-preview" });
          return;
        }
        sendJson(res, 200, { summary: "The conversation confirms the next steps are under review.", language: "en-US", cached: false, provider: "preview", model: "alt8mail-preview" });
        return;
      }
      if (pathname === "/api/ai/compose" && req.method === "POST") {
        if (!aiEnabled) throw new ApiError(403, "AI_DISABLED", "The AI assistant is disabled for this organization");
        const body = await readJson(req);
        if (aiDelayMs) await new Promise((resolve) => setTimeout(resolve, aiDelayMs));
        const attachmentCount = Array.isArray(body.attachmentIds) ? body.attachmentIds.length : 0;
        sendJson(res, 200, {
          text: "Thank you for your message. I reviewed the details and will follow up with the confirmed next steps shortly.",
          style: body.style || "professional", length: body.length || "medium",
          language: body.language || "en-US", creativity: body.creativity ?? 0.5,
          provider: "preview", model: "alt8mail-preview", attachmentCount
        });
        return;
      }
      const aiCommitmentRoute = pathname.match(/^\/api\/ai\/threads\/([^/]+)\/commitments$/);
      if (aiCommitmentRoute && req.method === "POST") {
        if (!aiEnabled) throw new ApiError(403, "AI_DISABLED", "The AI assistant is disabled for this organization");
        const threadId = decodeURIComponent(aiCommitmentRoute[1]);
        const threadMessages = db.messages.filter((message) => message.threadId === threadId && !message.draft)
          .sort((a, b) => Date.parse(a.date) - Date.parse(b.date));
        if (!threadMessages.length) throw new ApiError(404, "THREAD_NOT_FOUND", "Conversation not found");
        if (aiDelayMs) await new Promise((resolve) => setTimeout(resolve, aiDelayMs));
        const message = threadMessages[0];
        const sentence = String(message.bodyText || "").split(/(?<=[.!?])\s/)[0] || "I will follow up with the confirmed next steps.";
        sendJson(res, 200, {
          language: "en-US",
          items: [{
            kind: "promise", text: "Follow up with the confirmed next steps",
            quote: sentence, messageId: message.id, due: "",
            messageSubject: message.subject, messageDate: message.date,
            from: message.from?.email || ""
          }]
        });
        return;
      }
      if (pathname === "/api/ai/tone" && req.method === "POST") {
        if (!aiEnabled) throw new ApiError(403, "AI_DISABLED", "The AI assistant is disabled for this organization");
        const body = await readJson(req);
        if (!String(body.recipient || "").includes("@")) throw new ApiError(400, "RECIPIENT", "Enter a valid recipient address");
        sendJson(res, 200, {
          available: true, style: "professional", label: "Formal",
          reason: "You usually write formally to this client.",
          history: { sent: 14, received: 9, firstContact: "2024-02-01", lastContact: nowIso().slice(0, 10) },
          language: "en-US"
        });
        return;
      }
      if (pathname === "/api/catch-up" && req.method === "GET") {
        sendJson(res, 200, {
          available: true, since: new Date(Date.now() - 3 * 86400000).toISOString(),
          changed: 4, needReply: 2, unread: 3
        });
        return;
      }
      const trustExplanationMatch = pathname.match(/^\/api\/messages\/([^/]+)\/trust-explanation$/);
      if (trustExplanationMatch && req.method === "GET") {
        const message = findMessage(decodeURIComponent(trustExplanationMatch[1]));
        if (!message) throw new ApiError(404, "MESSAGE_NOT_FOUND", "Message not found");
        const junk = message.folder === "spam";
        sendJson(res, 200, {
          junk, trusted: !!message.trusted, auth: message.auth || {},
          explanation: junk
            ? ["This is the first message from this sender address.", "The message was placed in Junk because it did not have enough trusted history."]
            : ["The sending server is authorised by this domain (SPF passed).", "The message carries a valid digital signature (DKIM passed).", "You have exchanged 14 messages with this sender since February 1, 2024."],
          stats: { messages: 14, inbound: 9, domainMessages: 22, firstContact: "2024-02-01", lastContact: nowIso().slice(0, 10) }
        });
        return;
      }
      if (pathname === "/api/contacts/auto" && req.method === "POST") {
        const body = await readJson(req);
        const email = normalizeEmail(body.email);
        if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) throw new ApiError(400, "CONTACT_EMAIL", "Enter a valid recipient email");
        let person = db.people.find((item) => normalizeEmail(item.email) === email);
        if (!person) {
          const name = String(body.name || "").trim().slice(0, 240) || displayNameFromEmail(email);
          person = {
            name, email, role: "Contact", company: "", team: "", title: "", phone: "", location: "",
            reportsTo: "", initials: initialsFrom(name, email), color: colorForEmail(email), source: "auto"
          };
          db.people.push(person);
          await store.save();
        }
        sendJson(res, 200, { ok: true, contact: { ...person, id: `contact_${email.replace(/[^a-z0-9]/g, "_")}`, source: person.source || "directory" } });
        return;
      }
      if (pathname === "/api/folders" && req.method === "GET") {
        sendJson(res, 200, { folders: db.folders.map((folder) => ({ collapsed: false, system: ["inbox", "starred", "snoozed", "sent", "drafts", "archive", "spam", "trash"].includes(folder.id), badgeMode: "unread", order: db.folders.indexOf(folder), ...folder })) });
        return;
      }
      const mailFolderMatch = pathname.match(/^\/api\/folders\/([A-Za-z0-9_-]{1,100})$/);
      if (pathname === "/api/folders" && req.method === "POST") {
        const body = await readJson(req);
        const label = String(body.label || "").trim();
        if (!label) throw new ApiError(400, "FOLDER_NAME", "Enter a folder name");
        if (db.folders.some((folder) => String(folder.label).toLowerCase() === label.toLowerCase())) throw new ApiError(409, "FOLDER_EXISTS", "A folder with that name already exists");
        const parentId = String(body.parentId || "").trim();
        if (parentId) {
          const parent = db.folders.find((folder) => folder.id === parentId);
          if (!parent || parent.virtual) throw new ApiError(400, "FOLDER_PARENT", "Parent folder not found");
          parent.collapsed = false;
        }
        const folder = { id: id("folder"), label, icon: "i-folder", section: "folders", system: false, virtual: false, imapFolder: label, order: db.folders.length, badgeMode: "unread", color: String(body.color || ""), parentId, collapsed: false, createdAt: nowIso() };
        db.folders.push(folder);
        addAudit(req, "folder.created", label, `Created folder ${label}`);
        await store.save();
        sendJson(res, 201, { ok: true, folder });
        return;
      }
      if (mailFolderMatch && req.method === "PATCH") {
        const folder = db.folders.find((item) => item.id === mailFolderMatch[1]);
        if (!folder) throw new ApiError(404, "FOLDER_NOT_FOUND", "Folder not found");
        const body = await readJson(req);
        const system = ["inbox", "starred", "snoozed", "sent", "drafts", "archive", "spam", "trash"].includes(folder.id);
        if (body.label !== undefined && !system) {
          const label = String(body.label || "").trim();
          if (!label) throw new ApiError(400, "FOLDER_NAME", "Enter a folder name");
          if (db.folders.some((item) => item.id !== folder.id && String(item.label).toLowerCase() === label.toLowerCase())) throw new ApiError(409, "FOLDER_EXISTS", "A folder with that name already exists");
          folder.label = label;
          folder.imapFolder = label;
        }
        if (body.color !== undefined) folder.color = String(body.color || "");
        if (body.section !== undefined) folder.section = body.section === "favorites" ? "favorites" : "folders";
        if (body.badgeMode !== undefined) folder.badgeMode = ["unread", "total", "none"].includes(body.badgeMode) ? body.badgeMode : "unread";
        if (body.collapsed !== undefined) folder.collapsed = Boolean(body.collapsed);
        if (body.parentId !== undefined) {
          if (system) throw new ApiError(400, "FOLDER_SYSTEM", "Primary folders cannot be moved");
          const parentId = String(body.parentId || "").trim();
          if (parentId) {
            const parent = db.folders.find((item) => item.id === parentId);
            if (!parent || parent.virtual) throw new ApiError(400, "FOLDER_PARENT", "Parent folder not found");
            let cursor = parentId;
            const seen = new Set();
            while (cursor) {
              if (cursor === folder.id) throw new ApiError(400, "FOLDER_CYCLE", "A folder cannot be moved inside itself");
              if (seen.has(cursor)) break;
              seen.add(cursor);
              cursor = String(db.folders.find((item) => item.id === cursor)?.parentId || "");
            }
            parent.collapsed = false;
          }
          folder.parentId = parentId;
        }
        addAudit(req, "folder.updated", folder.label, "Updated folder settings");
        await store.save();
        sendJson(res, 200, { ok: true, folder });
        return;
      }
      const emptyFolderMatch = pathname.match(/^\/api\/folders\/([A-Za-z0-9_-]{1,100})\/empty$/);
      if (emptyFolderMatch && req.method === "POST") {
        if (emptyFolderMatch[1] === "subscriptions") {
          const before = db.messages.length;
          db.messages = db.messages.filter((message) => message.folder !== "subscriptions");
          const removed = before - db.messages.length;
          addAudit(req, "folder.empty", "Subscriptions", `Emptied ${removed} messages from Subscriptions`);
          await store.save();
          sendJson(res, 200, { ok: true, removed });
          return;
        }
        const folder = db.folders.find((item) => item.id === emptyFolderMatch[1]);
        if (!folder) throw new ApiError(404, "FOLDER_NOT_FOUND", "Folder not found");
        if (["inbox", "sent", "drafts", "archive"].includes(folder.id)) {
          throw new ApiError(400, "FOLDER_SYSTEM", "That folder cannot be emptied");
        }
        const before = db.messages.length;
        db.messages = db.messages.filter((message) => message.folder !== folder.id);
        const removed = before - db.messages.length;
        addAudit(req, "folder.empty", folder.label, `Emptied ${removed} messages from ${folder.label}`);
        await store.save();
        sendJson(res, 200, { ok: true, removed });
        return;
      }
      const markFolderReadMatch = pathname.match(/^\/api\/folders\/([A-Za-z0-9_-]{1,100})\/mark-read$/);
      if (markFolderReadMatch && req.method === "POST") {
        let affected = 0;
        for (const message of db.messages) {
          if (message.folder === markFolderReadMatch[1] && message.unread) {
            message.unread = false;
            affected += 1;
          }
        }
        addAudit(req, "folder.mark_read", markFolderReadMatch[1], `Marked ${affected} messages read`);
        await store.save();
        sendJson(res, 200, { ok: true, affected });
        return;
      }
      if (mailFolderMatch && req.method === "DELETE") {
        const folder = db.folders.find((item) => item.id === mailFolderMatch[1]);
        if (!folder) throw new ApiError(404, "FOLDER_NOT_FOUND", "Folder not found");
        if (["inbox", "starred", "snoozed", "sent", "drafts", "archive", "spam", "trash"].includes(folder.id)) throw new ApiError(400, "FOLDER_SYSTEM", "Primary folders cannot be deleted");
        for (const child of db.folders) if (child.parentId === folder.id) child.parentId = folder.parentId || "";
        db.messages.forEach((message) => { if (message.folder === folder.id) message.folder = "trash"; });
        db.folders = db.folders.filter((item) => item.id !== folder.id);
        addAudit(req, "folder.deleted", folder.label, "Deleted folder");
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }
      if (pathname === "/api/aliases" && req.method === "GET") {
        sendJson(res, 200, { aliases: db.user.aliases });
        return;
      }
      if (pathname === "/api/preferences" && req.method === "GET") {
        sendJson(res, 200, { preferences: db.user.preferences });
        return;
      }
      if (pathname === "/api/push/config" && req.method === "GET") {
        sendJson(res, 200, { enabled: false, publicKey: "", subscribed: false, includeSender: true, includeSubject: true });
        return;
      }
      if (pathname === "/api/push/subscribe" && (req.method === "POST" || req.method === "DELETE")) {
        await readJson(req);
        sendJson(res, 200, { ok: true });
        return;
      }
      if (pathname === "/api/push/test" && req.method === "POST") {
        sendJson(res, 200, { ok: true, sent: 0, failed: 0 });
        return;
      }
      if (pathname === "/api/preferences" && req.method === "PATCH") {
        const body = await readJson(req);
        const { next, changed } = normalizePreferencesPatch(body);
        db.user.preferences = next;
        addAudit(req, "preferences.updated", "Workspace preferences", changed.length ? `Updated ${changed.join(", ")}` : "Preferences saved");
        await store.save();
        sendJson(res, 200, { preferences: next, changed });
        return;
      }

      if (pathname === "/api/tasks" && req.method === "GET") {
        sendJson(res, 200, { count: db.tasks.length, tasks: db.tasks });
        return;
      }
      if (pathname === "/api/tasks" && req.method === "POST") {
        const body = await readJson(req);
        const task = validateTaskInput(body);
        db.tasks.unshift(task);
        addAudit(req, "task.created", task.title, `Due ${task.due || "unscheduled"} · ${task.priority} priority`);
        await store.save();
        sendJson(res, 201, { task });
        return;
      }
      const taskMatch = pathname.match(/^\/api\/tasks\/([A-Za-z0-9_-]{1,100})$/);
      if (taskMatch && req.method === "PATCH") {
        const existing = db.tasks.find((task) => task.id === taskMatch[1]);
        if (!existing) throw new ApiError(404, "NOT_FOUND", "Task not found");
        const body = await readJson(req);
        const updated = validateTaskInput(body, existing);
        Object.assign(existing, updated);
        const action = body.status === "done" && existing.completedAt ? "task.completed" : body.status === "open" ? "task.reopened" : "task.updated";
        addAudit(req, action, existing.title, action === "task.completed" ? "Task marked complete" : "Task updated");
        await store.save();
        sendJson(res, 200, { task: existing });
        return;
      }
      if (taskMatch && req.method === "DELETE") {
        const existing = db.tasks.find((task) => task.id === taskMatch[1]);
        if (!existing) throw new ApiError(404, "NOT_FOUND", "Task not found");
        db.tasks = db.tasks.filter((task) => task.id !== existing.id);
        addAudit(req, "task.deleted", existing.title, "Task removed");
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }

      if (pathname === "/api/storage" && req.method === "GET") {
        sendJson(res, 200, { providers: db.storageProviders, defaultProvider: db.defaultStorage || "a8m" });
        return;
      }
      if (pathname === "/api/storage/default" && req.method === "PUT") {
        const body = await readJson(req);
        const provider = providerById(String(body.providerId || "a8m"));
        if (!provider) throw new ApiError(400, "STORAGE_PROVIDER", "Choose a valid storage provider");
        if (!provider.connected && provider.kind !== "internal") throw new ApiError(409, "PROVIDER_OFFLINE", "Connect that storage provider first");
        db.defaultStorage = provider.id;
        addAudit(req, "storage.default", provider.name, "Default storage changed");
        await store.save();
        sendJson(res, 200, { ok: true, defaultProvider: provider.id });
        return;
      }
      if (pathname === "/api/storage/credentials" && req.method === "GET") {
        const providers = db.storageProviders.filter((provider) => provider.kind !== "internal").map((provider) => ({
          ...provider, clientId: provider.clientId || "", hasClientSecret: Boolean(provider.hasClientSecret), hasSecret: Boolean(provider.hasSecret),
          endpoint: provider.endpoint || "", region: provider.region || "us-east-1", bucket: provider.bucket || "", accessKey: provider.accessKey || "",
          useSSL: provider.useSSL !== false, pathStyle: provider.pathStyle !== false,
          redirectUri: `${isSecure(req) ? "https" : "http"}://${req.headers.host || "localhost"}/api/storage/oauth/${provider.id}/callback`
        }));
        sendJson(res, 200, { providers });
        return;
      }
      const credentialMatch = pathname.match(/^\/api\/storage\/credentials\/([A-Za-z0-9_-]{1,40})$/);
      if (credentialMatch && (req.method === "PUT" || req.method === "DELETE")) {
        const provider = providerById(credentialMatch[1]);
        if (!provider) throw new ApiError(400, "STORAGE_PROVIDER", "Choose a valid storage provider");
        if (req.method === "DELETE") {
          provider.configured = false;
          delete provider.hasClientSecret;
          delete provider.hasSecret;
          addAudit(req, "storage.credentials_cleared", provider.name, "Storage credentials cleared");
          await store.save();
          sendJson(res, 200, { ok: true });
          return;
        }
        const body = await readJson(req);
        provider.configured = true;
        if (provider.kind === "s3") {
          provider.endpoint = String(body.endpoint || "");
          provider.region = String(body.region || "us-east-1");
          provider.bucket = String(body.bucket || "");
          provider.accessKey = String(body.accessKey || "");
          provider.hasSecret = Boolean(body.secretKey || provider.hasSecret);
        } else {
          provider.clientId = String(body.clientId || "");
          provider.hasClientSecret = Boolean(body.clientSecret || provider.hasClientSecret);
        }
        addAudit(req, "storage.credentials_updated", provider.name, "Storage credentials updated");
        await store.save();
        sendJson(res, 200, { ok: true, provider });
        return;
      }
      const storageActionMatch = pathname.match(/^\/api\/storage\/([A-Za-z0-9_-]{1,40})\/(connect|disconnect)$/);
      if (storageActionMatch && req.method === "POST") {
        const provider = providerById(storageActionMatch[1]);
        if (!provider) throw new ApiError(404, "NOT_FOUND", "Storage provider not found");
        const action = storageActionMatch[2];
        if (action === "connect") {
          if (provider.connected) {
            sendJson(res, 200, { provider, files: db.files.filter((file) => file.providerId === provider.id) });
            return;
          }
          provider.connected = true;
          provider.status = "synced";
          provider.account = db.user.email;
          provider.lastSyncAt = nowIso();
          if (!provider.usedBytes) {
            provider.usedBytes = (provider.kind === "s3" ? 126 : provider.id === "dropbox" ? 74 : 58) * GIB;
            provider.limitBytes = provider.limitBytes || 2 * 1024 * GIB;
          }
          const samples = providerSeedFiles(provider.id);
          for (const sample of samples) {
            if (!db.files.some((file) => file.id === sample.id)) db.files.push(sample);
          }
          addAudit(req, "storage.connected", provider.name, `Connected as ${provider.account}`);
          await store.save();
          sendJson(res, 200, { provider, files: db.files.filter((file) => file.providerId === provider.id) });
          return;
        }
        provider.connected = false;
        provider.status = "disconnected";
        provider.account = "";
        provider.lastSyncAt = null;
        addAudit(req, "storage.disconnected", provider.name, "Storage connector disconnected");
        await store.save();
        sendJson(res, 200, { provider, files: db.files.filter((file) => file.providerId === provider.id) });
        return;
      }

      if (pathname === "/api/files" && req.method === "GET") {
        const files = db.files.slice().sort((a, b) => Date.parse(b.modifiedAt) - Date.parse(a.modifiedAt));
        sendJson(res, 200, { count: files.length, files, providers: db.storageProviders });
        return;
      }
      if (pathname === "/api/files" && req.method === "POST") {
        const body = await readJson(req);
        const provider = providerById(String(body.providerId || "a8m"));
        if (!provider) throw new ApiError(400, "FILE_PROVIDER", "Choose a valid storage provider");
        if (!provider.connected) throw new ApiError(409, "PROVIDER_OFFLINE", `${provider.name} is not connected`);
        let attachment = null;
        if (body.attachmentId) {
          attachment = db.attachments[String(body.attachmentId)];
          if (!attachment) throw new ApiError(404, "ATTACHMENT_NOT_FOUND", "Attachment not found");
        }
        const name = attachment ? attachment.name : safeFileName(body.name || "Untitled");
        const sizeBytes = attachment ? attachment.sizeBytes : Math.max(0, Math.min(5 * 1024 ** 3, Number(body.sizeBytes) || 0));
        if (!sizeBytes && !attachment) throw new ApiError(400, "FILE_SIZE", "File size is required");
        const mime = attachment ? attachment.mime : (String(body.mime || "").slice(0, 120) || "application/octet-stream");
        const record = createFileRecord({
          name,
          type: attachment ? attachment.type : attachmentBadgeType(mime, name),
          mime,
          sizeBytes,
          providerId: provider.id,
          path: String(body.path || (provider.kind === "internal" ? "Uploads" : provider.name)),
          contentUrl: attachment ? attachmentPublic(attachment).contentUrl : null,
          description: body.description || (attachment ? "Uploaded from the webmail compose window." : "Uploaded file"),
          attachmentId: attachment ? attachment.id : null
        });
        db.files.unshift(record);
        addAudit(req, "file.created", record.name, `Added to ${provider.name} · ${formatBytes(record.sizeBytes)}`);
        await store.save();
        sendJson(res, 201, { file: record });
        return;
      }
      const uploadSessionPayload = (session) => ({
        id: session.id, providerId: "a8m", folderId: session.folderId || "",
        expiresAt: session.expiresAt, createdAt: session.createdAt,
        uploads: session.uploads || [], active: !session.revokedAt && Date.parse(session.expiresAt) > Date.now(),
        ...(session.url ? { url: session.url } : {})
      });
      if (pathname === "/api/upload-sessions" && req.method === "POST") {
        const body = await readJson(req);
        const token = randomBytes(16).toString("base64url");
        const session = {
          id: id("uplink"), token, folderId: String(body.folderId || ""),
          createdAt: nowIso(), expiresAt: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
          uploads: [], url: `https://${req.headers.host || "mail.a8m.example"}/upload/${token}`
        };
        db.uploadSessions.unshift(session);
        await store.save();
        sendJson(res, 201, { session: uploadSessionPayload(session) });
        return;
      }
      const uploadSessionMatch = pathname.match(/^\/api\/upload-sessions\/([A-Za-z0-9_-]{1,100})$/);
      if (uploadSessionMatch && req.method === "GET") {
        const session = db.uploadSessions.find((item) => item.id === uploadSessionMatch[1]);
        if (!session) throw new ApiError(404, "UPLOAD_SESSION", "Upload session not found");
        sendJson(res, 200, { session: uploadSessionPayload(session) });
        return;
      }
      const uploadExtendMatch = pathname.match(/^\/api\/upload-sessions\/([A-Za-z0-9_-]{1,100})\/extend$/);
      if (uploadExtendMatch && req.method === "POST") {
        const session = db.uploadSessions.find((item) => item.id === uploadExtendMatch[1]);
        if (!session) throw new ApiError(404, "UPLOAD_SESSION", "Upload session not found");
        const body = await readJson(req);
        const hours = Math.max(1, Math.min(24, Number(body.hours) || 1));
        session.expiresAt = new Date(Math.max(Date.now(), Date.parse(session.expiresAt)) + hours * 3600 * 1000).toISOString();
        session.url = session.url || `https://${req.headers.host || "mail.a8m.example"}/upload/${session.token}`;
        await store.save();
        sendJson(res, 200, { session: uploadSessionPayload(session) });
        return;
      }
      if (uploadSessionMatch && req.method === "DELETE") {
        const session = db.uploadSessions.find((item) => item.id === uploadSessionMatch[1]);
        if (!session) throw new ApiError(404, "UPLOAD_SESSION", "Upload session not found");
        session.revokedAt = nowIso();
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }
      if (pathname === "/api/files/import-url" && req.method === "POST") {
        const body = await readJson(req);
        const source = String(body.url || "").trim();
        if (!/^https?:\/\//i.test(source)) throw new ApiError(400, "IMPORT_URL", "Enter a valid http or https URL");
        let fileName = "download";
        try { fileName = decodeURIComponent(new URL(source).pathname.split("/").filter(Boolean).pop() || "download"); } catch {}
        const record = createFileRecord({
          name: fileName, mime: "application/octet-stream", sizeBytes: 2048, providerId: "a8m",
          path: "Imports", description: `Imported from ${source.slice(0, 300)}`
        });
        db.files.unshift(record);
        addAudit(req, "file.imported_url", record.name, `Imported from ${source.slice(0, 300)}`);
        await store.save();
        sendJson(res, 201, { file: record });
        return;
      }
      const fileMatch = pathname.match(/^\/api\/files\/([A-Za-z0-9_-]{1,100})$/);
      if (fileMatch && req.method === "PATCH") {
        const file = findFile(fileMatch[1]);
        if (!file) throw new ApiError(404, "NOT_FOUND", "File not found");
        const body = await readJson(req);
        if (typeof body.starred === "boolean") file.starred = body.starred;
        if (typeof body.locked === "boolean") file.locked = body.locked;
        if (body.description !== undefined) file.description = String(body.description).slice(0, 500);
        addAudit(req, "file.updated", file.name, body.locked !== undefined ? (body.locked ? "File link locked" : "File link unlocked") : "File metadata updated");
        await store.save();
        sendJson(res, 200, { file });
        return;
      }
      const shareMatch = pathname.match(/^\/api\/files\/([A-Za-z0-9_-]{1,100})\/share$/);
      if (shareMatch && req.method === "POST") {
        const file = findFile(shareMatch[1]);
        if (!file) throw new ApiError(404, "NOT_FOUND", "File not found");
        const body = await readJson(req);
        const access = body.access === "edit" ? "edit" : "view";
        const expiresDays = Math.max(1, Math.min(90, Number(body.expiresDays) || 7));
        const recipientEmail = String(body.recipientEmail || "").trim().toLowerCase();
        const token = randomBytes(15).toString("base64url");
        const expiresAt = new Date(Date.now() + expiresDays * 24 * 3600 * 1000).toISOString();
        if (!Array.isArray(file.shares)) file.shares = [];
        file.shares.push({ token, access, expiresAt, createdAt: nowIso(), recipientEmail });
        if (recipientEmail && !file.sharedWith.includes(recipientEmail)) file.sharedWith.push(recipientEmail);
        if (!recipientEmail && !file.sharedWith.includes("External link")) file.sharedWith.push("External link");
        addAudit(req, "file.shared", file.name, `External ${access} link · expires in ${expiresDays} days`);
        await store.save();
        sendJson(res, 200, {
          share: { url: `https://files.a8m.example/s/${token}`, access, expiresAt, expiresDays, recipientEmail }
        });
        return;
      }
      if (fileMatch && req.method === "DELETE") {
        const file = findFile(fileMatch[1]);
        if (!file) throw new ApiError(404, "NOT_FOUND", "File not found");
        db.files = db.files.filter((item) => item.id !== file.id);
        addAudit(req, "file.deleted", file.name, "File removed from the workspace");
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }

      if (pathname === "/api/attachments" && req.method === "POST") {
        const body = await readJson(req, MAX_UPLOAD_JSON_BYTES);
        const raw = String(body.dataBase64 || "").replace(/^data:[^;]+;base64,/, "");
        if (!raw) throw new ApiError(400, "ATTACHMENT_EMPTY", "Choose a file to upload");
        let buffer;
        try {
          buffer = Buffer.from(raw, "base64");
        } catch {
          throw new ApiError(400, "ATTACHMENT_INVALID", "The file could not be decoded");
        }
        if (!buffer.length) throw new ApiError(400, "ATTACHMENT_EMPTY", "Choose a file to upload");
        if (buffer.length > MAX_UPLOAD_BYTES) throw new ApiError(413, "ATTACHMENT_TOO_LARGE", `Attachments are limited to ${formatBytes(MAX_UPLOAD_BYTES)}`);
        const name = safeFileName(body.name || "attachment");
        const detected = detectUpload(buffer, String(body.mime || ""), name);
        const attachmentId = id("att");
        const record = {
          id: attachmentId,
          name,
          type: attachmentBadgeType(detected.mime, name),
          mime: detected.mime,
          size: formatBytes(buffer.length),
          sizeBytes: buffer.length,
          storedName: attachmentId,
          previewable: detected.previewable,
          uploadedAt: nowIso(),
          owner: db.user.email
        };
        await fs.mkdir(uploadsDir, { recursive: true });
        await fs.writeFile(path.join(uploadsDir, record.storedName), buffer);
        db.attachments[attachmentId] = record;
        addAudit(req, "attachment.uploaded", name, `${formatBytes(buffer.length)} · ${detected.mime}`);
        await store.save();
        sendJson(res, 201, { attachment: attachmentPublic(record) });
        return;
      }
      const attachmentContentMatch = pathname.match(/^\/api\/attachments\/([A-Za-z0-9_-]{1,100})\/content$/);
      if (attachmentContentMatch && req.method === "GET") {
        const record = db.attachments[attachmentContentMatch[1]];
        if (!record) throw new ApiError(404, "NOT_FOUND", "Attachment not found");
        let buffer;
        try {
          buffer = await readUploadedAttachment(record);
        } catch {
          throw new ApiError(404, "NOT_FOUND", "Attachment content is no longer available");
        }
        const disposition = `${record.previewable ? "inline" : "attachment"}; filename*=UTF-8''${encodeURIComponent(record.name)}`;
        const headers = {
          "Content-Type": record.mime,
          "Content-Length": buffer.length,
          "Content-Disposition": disposition,
          "Cache-Control": "private, max-age=300",
          "X-Content-Type-Options": "nosniff",
          "Cross-Origin-Resource-Policy": "same-origin"
        };
        // User-supplied SVG can carry script. Sandboxing the response keeps the
        // image preview safe even if the file is opened directly in a tab.
        if (record.mime === "image/svg+xml") headers["Content-Security-Policy"] = "sandbox; default-src 'none'; style-src 'unsafe-inline'";
        res.writeHead(200, headers);
        res.end(buffer);
        return;
      }

      if (pathname === "/api/quota" && req.method === "GET") {
        sendJson(res, 200, quotaInfo());
        return;
      }
      if (pathname === "/api/outbound" && req.method === "GET") {
        sendJson(res, 200, outboundInfo());
        return;
      }
      if (pathname === "/api/search" && req.method === "GET") {
        const query = String(url.searchParams.get("q") || "");
        if (query.length > 500) throw new ApiError(400, "QUERY_TOO_LONG", "Search queries are limited to 500 characters");
        const result = getIndex().search(query);
        sendJson(res, 200, {
          query,
          parsed: result.parsed,
          count: result.messages.length,
          messages: result.messages.map(publicMessage)
        });
        return;
      }
      if (pathname === "/api/messages" && req.method === "GET") {
        const folder = String(url.searchParams.get("folder") || "inbox");
        const query = String(url.searchParams.get("q") || "");
        const filter = String(url.searchParams.get("filter") || "all");
        const requestedThread = String(url.searchParams.get("thread") || "").trim();
        let messages;
        if (requestedThread) messages = db.messages.filter((message) => message.threadId === requestedThread || message.id === requestedThread);
        else if (query) messages = getIndex().search(query).messages;
        else if (folder === "starred") messages = db.messages.filter((message) => message.starred);
        else if (folder === "subscriptions") messages = db.messages.filter((message) => message.folder === "subscriptions");
        else messages = db.messages.filter((message) => message.folder === folder);
        if (!query && !requestedThread) {
          const closedThreads = new Set(db.threadMeta.filter((item) => item.ownerId === db.user.id && item.closed).map((item) => item.threadId));
          messages = filter === "closed"
            ? messages.filter((message) => closedThreads.has(message.threadId))
            : messages.filter((message) => !closedThreads.has(message.threadId));
        }
        if (filter === "unread") messages = messages.filter((message) => message.unread);
        if (filter === "starred") messages = messages.filter((message) => message.starred);
        if (filter === "attachments") messages = messages.filter((message) => (message.attachments || []).length);
        sendJson(res, 200, {
          folder,
          thread: requestedThread || undefined,
          count: messages.length,
          messages: messages.map(publicMessage),
          threads: buildThreads(messages, db.messages).map((thread) => ({
            id: thread.id,
            subject: thread.subject,
            count: thread.count,
            unreadCount: thread.unreadCount,
            date: thread.date,
            latestId: thread.latest.id
          }))
        });
        return;
      }

      const messageMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})$/);
      if (messageMatch && req.method === "PATCH") {
        const message = findMessage(messageMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        const body = await readJson(req);
        if (typeof body.unread === "boolean") message.unread = body.unread;
        if (typeof body.starred === "boolean") message.starred = body.starred;
        if (typeof body.flagged === "boolean") message.flagged = body.flagged;
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { message: publicMessage(message), quota: quotaInfo() });
        return;
      }

      if (pathname === "/api/messages/read" && req.method === "POST") {
        const body = await readJson(req);
        const ids = Array.isArray(body.ids) ? body.ids.filter((value) => typeof value === "string").slice(0, 500) : [];
        let changed = 0;
        for (const messageId of ids) {
          const message = findMessage(messageId);
          if (message && message.unread) {
            message.unread = false;
            changed += 1;
          }
        }
        if (changed) {
          invalidateIndex();
          await store.save();
        }
        sendJson(res, 200, { ok: true, changed });
        return;
      }

      if (pathname === "/api/messages/actions" && req.method === "POST") {
        const body = await readJson(req);
        const ids = Array.isArray(body.ids) ? Array.from(new Set(body.ids.filter((value) => typeof value === "string"))).slice(0, 500) : [];
        if (!ids.length) throw new ApiError(400, "NO_MESSAGES", "Select at least one message");
        const action = String(body.action || "");
        const allowed = new Set(["archive", "move", "delete", "markRead", "markUnread", "star", "unstar", "spam", "notSpam", "report", "flag", "unflag"]);
        if (!allowed.has(action)) throw new ApiError(400, "BAD_ACTION", "Unknown bulk action");
        if (action === "move" && !FOLDER_IDS.has(String(body.folder))) throw new ApiError(400, "BAD_FOLDER", "Choose a valid destination folder");
        const targets = ids.map((messageId) => findMessage(messageId)).filter(Boolean);
        if (!targets.length) throw new ApiError(404, "NOT_FOUND", "No matching messages");
        const now = Date.now();
        pruneUndo();
        const token = randomBytes(18).toString("base64url");
        const snapshot = [];
        const hardDeleteIds = new Set();
        let permanentlyDeleted = 0;
        for (const message of targets) {
          const hardDelete = action === "delete" && message.folder === "subscriptions";
          if (!hardDelete) {
            snapshot.push({
              id: message.id,
              folder: message.folder,
              unread: message.unread,
              starred: message.starred,
              flagged: message.flagged === true,
              spamReport: message.spamReport ? structuredClone(message.spamReport) : null,
              full: action === "delete" && message.folder === "trash" ? structuredClone(message) : null
            });
          }
          if (action === "archive") message.folder = "archive";
          else if (action === "move") message.folder = String(body.folder) === "inbox" && message.unsubscribe ? "subscriptions" : String(body.folder);
          else if (action === "delete") {
            if (message.folder === "subscriptions") {
              permanentlyDeleted += 1;
              hardDeleteIds.add(message.id);
            } else if (message.folder === "trash" || message.folder === "drafts") permanentlyDeleted += 1;
            else message.folder = "trash";
          } else if (action === "markRead") message.unread = false;
          else if (action === "markUnread") message.unread = true;
          else if (action === "star") message.starred = true;
          else if (action === "unstar") message.starred = false;
          else if (action === "flag") message.flagged = true;
          else if (action === "unflag") message.flagged = false;
          else if (action === "spam") {
            message.folder = "spam";
            message.unread = false;
          } else if (action === "notSpam") {
            message.folder = message.unsubscribe ? "subscriptions" : "inbox";
            delete message.spamReport;
          } else if (action === "report") {
            message.folder = "spam";
            message.unread = false;
            message.flagged = true;
            message.spamReport = {
              at: nowIso(),
              reason: String(body.reason || "spam").slice(0, 40),
              note: String(body.note || "").slice(0, 500),
              source: "reading-pane"
            };
          }
        }
        if (permanentlyDeleted) {
          const remove = new Set(snapshot.filter((entry) => entry.full).map((entry) => entry.id));
          for (const id of hardDeleteIds) remove.add(id);
          db.messages = db.messages.filter((message) => !remove.has(message.id));
        }
        if (snapshot.length) {
          db.undo.push({
            token,
            createdAt: now,
            expiresAt: now + (undoOverride != null ? undoOverride : UNDO_TTL_MS),
            action,
            snapshot
          });
          if (db.undo.length > 50) db.undo = db.undo.slice(-50);
        }
        const auditAction = {
          archive: "message.archive",
          move: "message.move",
          delete: "message.delete",
          markRead: "message.markRead",
          markUnread: "message.markUnread",
          star: "message.star",
          unstar: "message.unstar",
          flag: "message.flag",
          unflag: "message.unflag",
          spam: "message.spam",
          notSpam: "message.notSpam",
          report: "message.report"
        }[action];
        const pastTense = {
          archive: "archived",
          delete: "deleted",
          markRead: "marked read",
          markUnread: "marked unread",
          star: "starred",
          unstar: "unstarred",
          flag: "flagged for follow-up",
          unflag: "flag cleared",
          spam: "marked as spam",
          notSpam: "moved back to Inbox",
          report: "reported to security"
        }[action] || action;
        const count = `${targets.length} message${targets.length === 1 ? "" : "s"}`;
        const detail = action === "move"
          ? `Moved ${count} to ${body.folder}`
          : action === "delete"
            ? `${count} deleted${permanentlyDeleted ? ` · ${permanentlyDeleted} removed permanently` : " to Trash"}`
            : action === "report"
              ? `${count} reported · reason: ${String(body.reason || "spam").slice(0, 40)}${body.note ? " · analyst note recorded" : ""}`
              : `${count} ${pastTense}`;
        addAudit(req, auditAction, `${targets.length} message${targets.length === 1 ? "" : "s"}`, detail);
        invalidateIndex();
        await store.save();
        sendJson(res, 200, {
          ok: true,
          affected: targets.length,
          permanentlyDeleted,
          undoToken: snapshot.length ? token : "",
          undoExpiresAt: snapshot.length ? new Date(now + (undoOverride != null ? undoOverride : UNDO_TTL_MS)).toISOString() : "",
          quota: quotaInfo(),
          outbound: outboundInfo()
        });
        return;
      }

      const undoMatch = pathname.match(/^\/api\/undo\/([A-Za-z0-9_-]{10,80})$/);
      if (undoMatch && req.method === "POST") {
        pruneUndo();
        const entry = db.undo.find((item) => item.token === undoMatch[1]);
        if (!entry) throw new ApiError(410, "UNDO_EXPIRED", "The undo window has closed");
        let restored = 0;
        for (const snapshot of entry.snapshot) {
          let message = findMessage(snapshot.id);
          if (!message && snapshot.full) {
            message = snapshot.full;
            db.messages.push(message);
          }
          if (!message) continue;
          message.folder = snapshot.folder;
          message.unread = snapshot.unread;
          message.starred = snapshot.starred;
          message.flagged = snapshot.flagged === true;
          if (snapshot.spamReport) message.spamReport = structuredClone(snapshot.spamReport);
          else delete message.spamReport;
          restored += 1;
        }
        db.undo = db.undo.filter((item) => item.token !== entry.token);
        addAudit(req, "message.undo", `${restored} message${restored === 1 ? "" : "s"}`, `Undid ${entry.action}`);
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { ok: true, restored, quota: quotaInfo() });
        return;
      }

      if (pathname === "/api/drafts" && req.method === "GET") {
        const drafts = db.messages.filter((message) => message.folder === "drafts").map(publicMessage);
        sendJson(res, 200, { count: drafts.length, drafts });
        return;
      }

      const draftMatch = pathname.match(/^\/api\/drafts\/([A-Za-z0-9_-]{1,100})$/);
      if (draftMatch && (req.method === "PUT" || req.method === "POST")) {
        const body = await readJson(req);
        const draftId = draftMatch[1];
        const existing = findMessage(draftId);
        if (existing && existing.folder !== "drafts") throw new ApiError(409, "NOT_A_DRAFT", "That id belongs to a message, not a draft");
        const draft = drainDraft(body, existing);
        draft.id = draftId;
        draft.messageId = existing ? existing.messageId : `<${draftId}@a8m.example>`;
        if (existing) {
          Object.assign(existing, draft);
        } else {
          db.messages.push(draft);
          addAudit(req, "draft.created", draft.subject || "(no subject)", "Draft started in compose");
        }
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { ok: true, savedAt: nowIso(), draft: publicMessage(existing || draft) });
        return;
      }
      if (draftMatch && req.method === "DELETE") {
        const existing = findMessage(draftMatch[1]);
        if (!existing || existing.folder !== "drafts") throw new ApiError(404, "NOT_FOUND", "Draft not found");
        db.messages = db.messages.filter((message) => message.id !== existing.id);
        addAudit(req, "draft.deleted", existing.subject || "(no subject)", "Draft discarded");
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }

      if (pathname === "/api/thread-meta" && req.method === "GET") {
        const threadId = String(url.searchParams.get("threadId") || "");
        const ownerId = String(url.searchParams.get("ownerId") || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        if (threadId) {
          const meta = db.threadMeta.find((item) => item.ownerId === ownerId && item.threadId === threadId)
            || { ownerId, threadId, status: "new", assignedTo: null, closed: false };
          sendJson(res, 200, { meta });
        } else {
          sendJson(res, 200, { meta: db.threadMeta.filter((item) => item.ownerId === ownerId) });
        }
        return;
      }

      const threadMetaMatch = pathname.match(/^\/api\/threads\/([^/]{1,120})\/meta$/);
      if (threadMetaMatch && req.method === "PATCH") {
        const body = await readJson(req);
        const threadId = decodeURIComponent(threadMetaMatch[1]);
        const ownerId = String(body.ownerId || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        if (!db.messages.some((message) => message.threadId === threadId)) throw new ApiError(404, "THREAD_NOT_FOUND", "Conversation not found");
        let meta = db.threadMeta.find((item) => item.ownerId === ownerId && item.threadId === threadId);
        if (!meta) {
          meta = { ownerId, threadId, status: "new", assignedTo: null, closed: false };
          db.threadMeta.push(meta);
        }
        if (body.status !== undefined) {
          const status = String(body.status || "new");
          if (!["new", "in_progress", "done"].includes(status)) throw new ApiError(400, "THREAD_STATUS", "Status must be new, in_progress or done");
          meta.status = status;
        }
        if (Object.prototype.hasOwnProperty.call(body, "assignedTo")) {
          const email = normalizeEmail(body.assignedTo || "");
          if (!email) meta.assignedTo = null;
          else if (email === normalizeEmail(db.user.email)) {
            meta.assignedTo = { id: db.user.id, name: db.user.name, email: db.user.email };
          } else {
            const person = db.people.find((entry) => (entry.source || "directory") === "directory" && normalizeEmail(entry.email) === email);
            if (!person) throw new ApiError(400, "THREAD_ASSIGNEE", "Choose an active person in this organization");
            meta.assignedTo = { id: "user_" + email.split("@")[0].replace(/[^a-z0-9]/g, ""), name: person.name, email: person.email };
          }
        }
        if (typeof body.closed === "boolean") {
          meta.closed = body.closed;
          if (body.closed) meta.closedAt = nowIso();
          else delete meta.closedAt;
        }
        meta.updatedAt = nowIso();
        await store.save();
        sendJson(res, 200, { ok: true, meta });
        return;
      }

      if (pathname === "/api/thread-notes" && req.method === "GET") {
        const threadId = String(url.searchParams.get("threadId") || "");
        const ownerId = String(url.searchParams.get("ownerId") || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        if (!threadId) throw new ApiError(400, "THREAD", "A conversation is required");
        const notes = db.threadNotes
          .filter((note) => note.ownerId === ownerId && note.threadId === threadId)
          .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
        sendJson(res, 200, { notes });
        return;
      }

      const threadNotesMatch = pathname.match(/^\/api\/threads\/([^/]{1,120})\/notes$/);
      if (threadNotesMatch && req.method === "POST") {
        const body = await readJson(req);
        const threadId = decodeURIComponent(threadNotesMatch[1]);
        const ownerId = String(body.ownerId || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        if (!db.messages.some((message) => message.threadId === threadId)) throw new ApiError(404, "THREAD_NOT_FOUND", "Conversation not found");
        const text = String(body.body || "").trim();
        if (!text || [...text].length > 2000) throw new ApiError(400, "THREAD_NOTE", "Write a note of 1–2000 characters");
        const note = {
          id: id("note"), authorId: db.user.id, authorName: db.user.name, authorEmail: db.user.email,
          body: text, createdAt: nowIso(), ownerId, threadId
        };
        db.threadNotes.push(note);
        addAudit(req, "thread.note_added", threadId, "Added an internal team note");
        await store.save();
        sendJson(res, 200, { ok: true, note });
        return;
      }

      const threadNoteMatch = pathname.match(/^\/api\/threads\/([^/]{1,120})\/notes\/([A-Za-z0-9_-]{1,100})$/);
      if (threadNoteMatch && req.method === "DELETE") {
        const body = await readJson(req);
        const threadId = decodeURIComponent(threadNoteMatch[1]);
        const noteId = threadNoteMatch[2];
        const ownerId = String(body.ownerId || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        const index = db.threadNotes.findIndex((note) => note.id === noteId && note.threadId === threadId && note.ownerId === ownerId);
        if (index < 0) throw new ApiError(404, "THREAD_NOTE_NOT_FOUND", "Team note not found");
        db.threadNotes.splice(index, 1);
        addAudit(req, "thread.note_deleted", threadId, "Removed an internal team note");
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }

      if (pathname === "/api/notifications" && req.method === "GET") {
        const notifications = db.notifications.slice().sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
        sendJson(res, 200, { notifications });
        return;
      }
      if (pathname === "/api/notifications/read-all" && req.method === "POST") {
        for (const notification of db.notifications) {
          if (!notification.readAt) notification.readAt = nowIso();
        }
        await store.save();
        sendJson(res, 200, { ok: true });
        return;
      }
      const notificationReadMatch = pathname.match(/^\/api\/notifications\/([A-Za-z0-9_-]{1,100})\/read$/);
      if (notificationReadMatch && req.method === "POST") {
        const notification = db.notifications.find((item) => item.id === notificationReadMatch[1]);
        if (notification) {
          notification.readAt = notification.readAt || nowIso();
          await store.save();
        }
        sendJson(res, 200, { ok: true });
        return;
      }

      if (pathname === "/api/unsubscribe-actions" && req.method === "GET") {
        const actions = db.unsubscribeActions.filter((item) => item.userId === db.user.id);
        sendJson(res, 200, { actions });
        return;
      }

      if (pathname === "/api/folders/subscriptions/unsubscribe-all" && req.method === "POST") {
        const groups = new Map();
        for (const message of db.messages.filter((item) => item.folder === "subscriptions" && item.unsubscribe)) {
          const sender = normalizeEmail(message.from?.email || "");
          if (!sender) continue;
          const group = groups.get(sender) || { sender, name: message.from?.name || "", messages: [] };
          group.messages.push(message);
          groups.set(sender, group);
        }
        const results = [];
        const removeIds = new Set();
        let unsubscribed = 0, failed = 0, manual = 0;
        for (const group of groups.values()) {
          const metadata = group.messages[0].unsubscribe || {};
          const method = metadata.oneClick ? "one-click" : metadata.url ? "link" : metadata.mailto ? "mailto" : "";
          const status = method ? (metadata.simulateFailure ? "failed" : "sent") : "manual";
          const error = metadata.simulateFailure ? "unsubscribe request failed: dial tcp: connection refused" : "";
          if (method) for (const message of group.messages) removeIds.add(message.id);
          if (status === "sent") unsubscribed += 1;
          else if (status === "failed") failed += 1;
          else manual += 1;
          const record = {
            id: id("unsub"), userId: db.user.id, messageId: group.messages[0].id,
            senderEmail: group.sender, method: method || "manual",
            target: metadata.url || metadata.mailto || "", status, error, updatedAt: nowIso()
          };
          db.unsubscribeActions = db.unsubscribeActions.filter((item) => !(item.userId === db.user.id && normalizeEmail(item.senderEmail) === group.sender));
          db.unsubscribeActions.push(record);
          results.push({
            senderEmail: group.sender, senderName: group.name, method, status, error,
            messages: group.messages.length, deleted: method ? group.messages.length : 0
          });
        }
        db.messages = db.messages.filter((message) => !removeIds.has(message.id));
        addAudit(req, "unsubscribe.bulk", "Subscriptions", `Processed ${results.length} senders; ${unsubscribed} unsubscribed, ${failed} failed, ${removeIds.size} messages removed`);
        await store.save();
        sendJson(res, 200, {
          ok: true, processed: results.length, unsubscribed, failed, manual,
          deleted: removeIds.size, skipped: 0, results
        });
        return;
      }

      const unsubscribeMatch = pathname.match(/^\/api\/messages\/([^/]{1,160})\/unsubscribe$/);
      if (unsubscribeMatch && req.method === "POST") {
        const message = findMessage(decodeURIComponent(unsubscribeMatch[1]));
        if (!message) throw new ApiError(404, "MESSAGE_NOT_FOUND", "Message not found");
        const metadata = message.unsubscribe;
        if (!metadata || typeof metadata !== "object") throw new ApiError(400, "UNSUBSCRIBE_UNAVAILABLE", "This message does not offer an unsubscribe link");
        const sender = normalizeEmail(message.from?.email || "");
        if (metadata.simulateFailure) {
          const record = {
            id: id("unsub"), userId: db.user.id, messageId: message.id, senderEmail: sender,
            method: "one-click", target: metadata.url || "", status: "failed",
            error: "unsubscribe request failed: dial tcp: connection refused", updatedAt: nowIso()
          };
          db.unsubscribeActions = db.unsubscribeActions.filter((item) => !(item.userId === db.user.id && normalizeEmail(item.senderEmail) === sender));
          db.unsubscribeActions.push(record);
          db.messages = db.messages.filter((item) => item.id !== message.id);
          addAudit(req, "unsubscribe.failed", message.subject || "(no subject)", `Unsubscribe failed for ${sender}; message permanently deleted`);
          await store.save();
          sendJson(res, 200, {
            ok: false, action: "failed", deleted: true,
            error: "unsubscribe request failed: dial tcp: connection refused",
            actionRecord: record
          });
          return;
        }
        const action = metadata.oneClick ? "sent" : metadata.url ? "manual" : "manual";
        const record = {
          id: db.unsubscribeActions.find((item) => item.userId === db.user.id && normalizeEmail(item.senderEmail) === sender)?.id || id("unsub"),
          userId: db.user.id, messageId: message.id, senderEmail: sender,
          method: metadata.oneClick ? "one-click" : metadata.url ? "link" : "mailto",
          target: metadata.oneClick || metadata.url ? metadata.url : metadata.mailto,
          status: action, error: "", updatedAt: nowIso()
        };
        db.unsubscribeActions = db.unsubscribeActions.filter((item) => !(item.userId === db.user.id && normalizeEmail(item.senderEmail) === sender));
        db.unsubscribeActions.push(record);
        addAudit(req, metadata.oneClick ? "unsubscribe.sent" : "unsubscribe.manual", message.subject || "(no subject)", `Unsubscribe requested for ${sender}`);
        await store.save();
        sendJson(res, 200, {
          ok: true,
          action: metadata.oneClick ? "sent" : metadata.url ? "open" : "mailto",
          url: metadata.url || "", email: metadata.mailto || "",
          actionRecord: record
        });
        return;
      }

      if (pathname === "/api/reaction-catalog" && req.method === "GET") {
        sendJson(res, 200, { categories: MOCK_REACTION_CATEGORIES, quick: ["👍", "❤️", "😂", "🎉", "👀", "✅"] });
        return;
      }

      if (pathname === "/api/reactions" && req.method === "GET") {
        const threadId = String(url.searchParams.get("threadId") || "");
        const ownerId = String(url.searchParams.get("ownerId") || db.user.id);
        if (ownerId !== db.user.id) throw new ApiError(403, "THREAD_FORBIDDEN", "You do not have access to this shared mailbox");
        const reactions = db.reactions
          .filter((item) => item.ownerId === ownerId && item.threadId === threadId && !item.dismissedAt && (item.visibility === "sent" || item.userId === db.user.id))
          .map((item) => ({ ...item, own: item.userId === db.user.id }))
          .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
        sendJson(res, 200, { reactions, quick: ["👍", "❤️", "😂", "🎉", "👀", "✅"] });
        return;
      }

      const reactionMatch = pathname.match(/^\/api\/messages\/([^/]{1,160})\/reaction$/);
      if (reactionMatch && (req.method === "POST" || req.method === "DELETE")) {
        const messageId = decodeURIComponent(reactionMatch[1]);
        const message = findMessage(messageId);
        if (!message) throw new ApiError(404, "MESSAGE_NOT_FOUND", "Message not found");
        if (message.folder === "sent" || message.folder === "drafts" || message.folder === "spam") {
          throw new ApiError(400, "REACTION_UNAVAILABLE", "Reactions are unavailable in this folder");
        }
        if (message.unsubscribe && typeof message.unsubscribe === "object" && Object.keys(message.unsubscribe).length) {
          throw new ApiError(400, "REACTION_UNAVAILABLE", "Reactions are unavailable for newsletter and unsubscribe mail");
        }
        if (message.threadId && db.messages.some((item) => item.threadId === message.threadId && item.unsubscribe && typeof item.unsubscribe === "object" && Object.keys(item.unsubscribe).length)) {
          throw new ApiError(400, "REACTION_UNAVAILABLE", "Reactions are unavailable for newsletter and unsubscribe mail");
        }
        const existing = db.reactions.find((item) => item.ownerId === db.user.id && item.messageId === message.id && item.userId === db.user.id && !item.dismissedAt);
        if (req.method === "DELETE") {
          if (!existing) throw new ApiError(404, "REACTION_NOT_FOUND", "Reaction not found");
          const wasSent = existing.status === "sent";
          const pending = existing.status === "pending";
          existing.dismissedAt = nowIso();
          await store.save();
          sendJson(res, 200, { ok: true, wasSent, pending });
          return;
        }
        const body = await readJson(req);
        const emoji = String(body.emoji || "");
        const allowed = MOCK_REACTION_EMOJIS;
        if (!allowed.has(emoji)) throw new ApiError(400, "REACTION_EMOJI", "Choose one of the available reactions");
        const visibility = body.visibility === "sent" ? "sent" : "private";
        if (visibility === "sent" && normalizeEmail(message.from?.email || "") === normalizeEmail(db.user.email)) {
          throw new ApiError(400, "REACTION_SELF", "Send a reaction to a message from someone else, or keep it private");
        }
        const reaction = existing || {
          id: id("reaction"), ownerId: db.user.id, userId: db.user.id,
          messageId: message.id, threadId: message.threadId || message.id,
          createdAt: nowIso()
        };
        Object.assign(reaction, {
          emoji, semantic: String(body.semantic || ""), visibility,
          status: visibility === "sent" ? "sent" : "private",
          sentAt: visibility === "sent" ? nowIso() : null, dismissedAt: null,
          updatedAt: nowIso()
        });
        if (!existing) db.reactions.push(reaction);
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { ok: true, reaction: { ...reaction, own: true } });
        return;
      }

      if (pathname === "/api/ai/reactions" && req.method === "POST") {
        const body = await readJson(req);
        const message = findMessage(String(body.messageId || "").replace(/^dlg:[^:]+:/, ""));
        if (!message) throw new ApiError(404, "MESSAGE_NOT_FOUND", "Message not found");
        const text = `${message.subject || ""} ${message.bodyText || ""}`.toLowerCase();
        const picks = [];
        const addPick = (emoji, label, reason) => {
          if (!picks.some((item) => item.emoji === emoji)) picks.push({ emoji, label, reason, semantic: emoji === "👍" ? "acknowledgement" : emoji === "✅" ? "approval" : emoji === "🎉" ? "celebration" : "acknowledgement" });
        };
        if (text.includes("thank")) addPick("🙏", "Thank them", "The message contains gratitude");
        else if (text.includes("congrat")) addPick("🎉", "Celebrate", "The message shares good news");
        else if (text.includes("approv") || text.includes("lgtm") || text.includes("signed")) addPick("✅", "Approve", "The message mentions approval");
        else if (text.includes("urgent") || text.includes("asap") || text.includes("action required")) addPick("👀", "Acknowledge urgency", "The message signals urgency");
        else if (text.includes("great") || text.includes("excellent")) addPick("❤️", "Appreciate", "The message is positive");
        addPick("👍", "Acknowledge", "A quick acknowledgement fits");
        addPick("✅", "Confirm", "A clear confirmation fits");
        sendJson(res, 200, { suggestions: picks.slice(0, 3), quick: ["👍", "❤️", "😂", "🎉", "👀", "✅"], ai: false });
        return;
      }

      if (pathname === "/api/reaction-rules" && req.method === "GET") {
        sendJson(res, 200, { rules: db.reactionRules.filter((item) => item.userId === db.user.id), quick: ["👍", "❤️", "😂", "🎉", "👀", "✅"] });
        return;
      }
      if (pathname === "/api/reaction-rules" && req.method === "POST") {
        const body = await readJson(req);
        const senderContains = String(body.senderContains || "").trim().toLowerCase();
        const subjectContains = String(body.subjectContains || "").trim().toLowerCase();
        if (!senderContains && !subjectContains) throw new ApiError(400, "REACTION_RULE_MATCH", "Add a sender or subject condition");
        const rule = {
          id: id("rrule"), userId: db.user.id, name: String(body.name || "Reaction rule").trim().slice(0, 120),
          senderContains, subjectContains, emoji: String(body.emoji || "👍"),
          enabled: body.enabled !== false, createdAt: nowIso(), updatedAt: nowIso()
        };
        db.reactionRules.push(rule);
        addAudit(req, "reaction_rule.created", rule.id, `Created reaction rule ${rule.name}`);
        await store.save();
        sendJson(res, 200, { ok: true, id: rule.id, rule });
        return;
      }
      const reactionRuleMatch = pathname.match(/^\/api\/reaction-rules\/([A-Za-z0-9_-]{1,100})$/);
      if (reactionRuleMatch && (req.method === "PATCH" || req.method === "DELETE")) {
        const rule = db.reactionRules.find((item) => item.id === reactionRuleMatch[1] && item.userId === db.user.id);
        if (!rule) throw new ApiError(404, "REACTION_RULE_NOT_FOUND", "Reaction rule not found");
        if (req.method === "DELETE") {
          db.reactionRules = db.reactionRules.filter((item) => item !== rule);
          addAudit(req, "reaction_rule.deleted", rule.id, `Deleted reaction rule ${rule.name}`);
          await store.save();
          sendJson(res, 200, { ok: true });
          return;
        }
        const body = await readJson(req);
        if (body.name !== undefined) rule.name = String(body.name || "Reaction rule").trim().slice(0, 120);
        if (body.senderContains !== undefined) rule.senderContains = String(body.senderContains || "").trim().toLowerCase();
        if (body.subjectContains !== undefined) rule.subjectContains = String(body.subjectContains || "").trim().toLowerCase();
        if (body.emoji !== undefined) rule.emoji = String(body.emoji || "👍");
        if (body.enabled !== undefined) rule.enabled = body.enabled !== false;
        if (!rule.senderContains && !rule.subjectContains) throw new ApiError(400, "REACTION_RULE_MATCH", "Add a sender or subject condition");
        rule.updatedAt = nowIso();
        await store.save();
        sendJson(res, 200, { ok: true, rule });
        return;
      }

      if (pathname === "/api/send-time-suggestions" && req.method === "POST") {
        const body = await readJson(req);
        const emails = Array.isArray(body.to) ? body.to.map((value) => normalizeEmail(value)).filter(Boolean).slice(0, 20) : [];
        const suggestions = [];
        const seenTimes = new Set();
        for (const email of emails) {
          const inbound = db.messages.filter((message) => normalizeEmail(message.from?.email) === email);
          const known = inbound.find((message) => Number.isFinite(Number(message.senderUtcOffsetMinutes)));
          const offset = known ? Number(known.senderUtcOffsetMinutes) : null;
          const name = inbound[0]?.from?.name || "";
          if (offset == null) continue;
          const now = new Date();
          for (let day = 0; day < 8; day += 1) {
            const local = new Date(now.getTime() + (offset * 60000) + day * 86400000);
            const weekday = local.getUTCDay();
            if (weekday === 0 || weekday === 6) continue;
            const candidate = new Date(Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate(), 9, 30) - offset * 60000);
            if (candidate.getTime() <= Date.now() + 600000) continue;
            if (!seenTimes.has(candidate.getTime())) {
              seenTimes.add(candidate.getTime());
              const localDate = new Date(candidate.getTime() + offset * 60000);
              const localLabel = localDate.toLocaleString("en-US", { weekday: "short", hour: "numeric", minute: "2-digit", timeZone: "UTC" });
              suggestions.push({
                sendAt: candidate.toISOString(), recipient: email, recipientName: name,
                known: true, offsetMinutes: offset,
                recipientLocal: localLabel,
                reason: "Usually active around this time"
              });
            }
            break;
          }
        }
        if (!suggestions.length) {
          const now = new Date();
          for (let day = 1; day <= 3 && suggestions.length < 2; day += 1) {
            const local = new Date(now.getTime() + day * 86400000);
            const weekday = local.getUTCDay();
            if (weekday === 0 || weekday === 6) continue;
            const candidate = new Date(Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate(), 9, 30));
            if (candidate.getTime() <= Date.now() + 600000) continue;
            suggestions.push({
              sendAt: candidate.toISOString(), recipient: "", recipientName: "",
              known: false, offsetMinutes: 0, recipientLocal: "", reason: "Based on your working hours"
            });
          }
        }
        sendJson(res, 200, { suggestions, recipients: emails.map((email) => ({ email, known: db.messages.some((message) => normalizeEmail(message.from?.email) === email && Number.isFinite(Number(message.senderUtcOffsetMinutes))) })) });
        return;
      }

      if (pathname === "/api/yearly-review" && req.method === "GET") {
        const requested = Number(url.searchParams.get("year"));
        const now = new Date();
        const year = Number.isFinite(requested) && requested >= 2000 ? requested : now.getUTCFullYear();
        const start = Date.UTC(year, 0, 1);
        const end = Date.UTC(year + 1, 0, 1);
        const inYear = db.messages.filter((message) => {
          const stamp = Date.parse(message.date);
          return stamp >= start && stamp < end && !message.draft;
        });
        const sent = inYear.filter((message) => message.folder === "sent");
        const received = inYear.filter((message) => message.folder !== "sent");
        const monthly = Array.from({ length: 12 }, (_, index) => ({
          month: index + 1,
          label: new Date(Date.UTC(year, index, 1)).toLocaleString("en-US", { month: "long" }),
          count: 0, sent: 0, received: 0,
          start: new Date(Date.UTC(year, index, 1)).toISOString().slice(0, 10),
          end: new Date(Date.UTC(year, index + 1, 1)).toISOString().slice(0, 10)
        }));
        for (const message of inYear) {
          const month = new Date(Date.parse(message.date)).getUTCMonth();
          if (!monthly[month]) continue;
          monthly[month].count += 1;
          if (message.folder === "sent") monthly[month].sent += 1;
          else monthly[month].received += 1;
        }
        const weeklyMap = new Map();
        for (const message of inYear) {
          const date = new Date(Date.parse(message.date));
          const day = date.getUTCDay();
          const offset = (day + 6) % 7;
          const weekStart = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate() - offset));
          const key = weekStart.toISOString().slice(0, 10);
          const current = weeklyMap.get(key) || { weekStart, count: 0, sent: 0, received: 0 };
          current.count += 1;
          if (message.folder === "sent") current.sent += 1;
          else current.received += 1;
          weeklyMap.set(key, current);
        }
        const weekly = Array.from(weeklyMap.values()).sort((a, b) => a.weekStart - b.weekStart).map((item) => ({
          week: (() => { const date = new Date(item.weekStart); const first = new Date(Date.UTC(date.getUTCFullYear(), 0, 1)); return Math.ceil((((date - first) / 86400000) + first.getUTCDay() + 1) / 7); })(),
          label: `Week of ${item.weekStart.toLocaleString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })}`,
          count: item.count, sent: item.sent, received: item.received,
          start: item.weekStart.toISOString().slice(0, 10),
          end: new Date(item.weekStart.getTime() + 6 * 86400000).toISOString().slice(0, 10)
        }));
        const senderCounts = new Map();
        for (const message of received) {
          const key = normalizeEmail(message.from?.email);
          if (!key) continue;
          const current = senderCounts.get(key) || { name: message.from?.name || key, email: key, count: 0 };
          current.count += 1;
          senderCounts.set(key, current);
        }
        const recipientCounts = new Map();
        for (const message of sent) {
          for (const person of Array.isArray(message.to) ? message.to : []) {
            const key = normalizeEmail(person.email);
            if (!key) continue;
            const current = recipientCounts.get(key) || { name: person.name || key, email: key, count: 0 };
            current.count += 1;
            recipientCounts.set(key, current);
          }
        }
        const topSenders = Array.from(senderCounts.values()).sort((a, b) => b.count - a.count).slice(0, 5);
        const topRecipients = Array.from(recipientCounts.values()).sort((a, b) => b.count - a.count).slice(0, 5);
        const reactions = db.reactions.filter((item) => item.userId === db.user.id && !item.dismissedAt && Date.parse(item.createdAt) >= start && Date.parse(item.createdAt) < end);
        const topMap = new Map();
        for (const reaction of reactions) topMap.set(reaction.emoji, (topMap.get(reaction.emoji) || 0) + 1);
        const topReactions = Array.from(topMap.entries()).map(([emoji, count]) => ({ emoji, count, semantic: "" })).sort((a, b) => b.count - a.count).slice(0, 5);
        const readingMinutes = inYear.reduce((sum, message) => sum + (Number(message.readingMinutes) || Math.max(1, Math.ceil(String(message.bodyText || "").split(/\s+/).filter(Boolean).length / 220))), 0);
        const busiestMonth = monthly.slice().sort((a, b) => b.count - a.count)[0] || { month: 0, label: "", count: 0 };
        const busiestDayMap = new Map();
        for (const message of inYear) {
          const key = new Date(Date.parse(message.date)).toISOString().slice(0, 10);
          busiestDayMap.set(key, (busiestDayMap.get(key) || 0) + 1);
        }
        const busiestDay = Array.from(busiestDayMap.entries()).sort((a, b) => b[1] - a[1])[0] || ["", 0];
        const years = Array.from(new Set(db.messages.map((message) => new Date(Date.parse(message.date)).getUTCFullYear()))).sort((a, b) => b - a);
        const highlights = [];
        highlights.push(`You handled ${inYear.length} messages across ${new Set(inYear.map((message) => message.threadId)).size} conversations.`);
        highlights.push(`You sent ${sent.length} and received ${received.length}.`);
        if (busiestMonth.count) highlights.push(`${busiestMonth.label} was your busiest month with ${busiestMonth.count} messages.`);
        if (topSenders[0]) highlights.push(`${topSenders[0].name} was your most frequent correspondent.`);
        if (readingMinutes) highlights.push(`You spent about ${readingMinutes} minutes reading email.`);
        if (reactions.length) highlights.push(`You shared ${reactions.length} reactions.`);
        sendJson(res, 200, {
          year, years: years.length ? years : [year], timeZone: "UTC",
          totals: { messages: inYear.length, sent: sent.length, received: received.length, threads: new Set(inYear.map((message) => message.threadId)).size, unread: inYear.filter((message) => message.unread).length, newsletters: inYear.filter((message) => message.unsubscribe).length, readingMinutes, readingHours: readingMinutes / 60 },
          topSenders, topRecipients, monthly,
          weekly,
          busiestMonth: { month: busiestMonth.month, label: busiestMonth.label, count: busiestMonth.count },
          busiestDay: { date: busiestDay[0], count: busiestDay[1] },
          busiestHour: { hour: 10, count: Math.max(1, Math.round(inYear.length / 8)) },
          reactions: { given: reactions.length, received: 0, top: topReactions },
          unsubscribed: db.unsubscribeActions.length, highlights
        });
        return;
      }

      if (pathname === "/api/send" && req.method === "POST") {
        const body = await readJson(req);
        const fromEmail = String(body.fromEmail || body.from || db.user.email);
        const alias = aliasForEmail(fromEmail);
        if (!alias) throw new ApiError(403, "BAD_FROM", "That From identity is not available for sending");
        const recipients = parseRecipients(body.to);
        if (recipients.invalid.length) {
          throw new ApiError(400, "BAD_RECIPIENT", `Check these recipients: ${recipients.invalid.join(", ")}`);
        }
        if (!recipients.people.length) throw new ApiError(400, "NO_RECIPIENT", "Add at least one recipient");
        const cc = parseRecipients(body.cc);
        const bcc = parseRecipients(body.bcc);
        if (cc.invalid.length || bcc.invalid.length) {
          throw new ApiError(400, "BAD_RECIPIENT", `Check these recipients: ${[...cc.invalid, ...bcc.invalid].join(", ")}`);
        }
        if (recipients.people.length + cc.people.length + bcc.people.length > 50) {
          throw new ApiError(400, "TOO_MANY_RECIPIENTS", "A message can have at most 50 recipients");
        }
        const subject = String(body.subject || "").slice(0, 500);
        const bodyText = String(body.bodyText == null ? body.body || "" : body.bodyText).slice(0, 200_000);
        const attachments = normalizeAttachments(body.attachments || []);
        const draftId = body.draftId ? String(body.draftId) : null;
        let draft = null;
        if (draftId) {
          draft = findMessage(draftId);
          if (!draft || draft.folder !== "drafts") throw new ApiError(409, "DRAFT_MISSING", "This draft is no longer available");
        }
        const usage = outboundInfo();
        if (usage.usedWithQueued >= usage.limit) {
          throw new ApiError(429, "OUTBOUND_CAP", `Daily outbound cap reached (${usage.usedWithQueued}/${usage.limit}). Try again after the window resets.`, { outbound: usage });
        }
        const now = Date.now();
        let scheduledFor = null;
        if (body.scheduledFor || body.sendAt) {
          const parsed = Date.parse(String(body.scheduledFor || body.sendAt));
          if (!Number.isFinite(parsed)) throw new ApiError(400, "SCHEDULE_INVALID", "Choose a valid date and time");
          if (parsed <= now + 5000) throw new ApiError(400, "SCHEDULE_INVALID", "Scheduled time must be at least a few seconds in the future");
          if (parsed > now + 366 * 24 * 3600 * 1000) throw new ApiError(400, "SCHEDULE_INVALID", "Scheduled time can be at most one year ahead");
          scheduledFor = new Date(parsed).toISOString();
        }
        const holdMs = scheduledFor ? 0 : resolveHold(body);
        const dlp = mockDlpEvaluate({
          subject,
          bodyText,
          bodyHtml: body.bodyHtml || "",
          attachments,
          classification: body.classification || (draft && draft.classification) || MOCK_DLP.defaultClassification
        });
        if (dlp.blocked) {
          throw new ApiError(422, "DLP_BLOCKED", "This message is blocked by your organization's data protection policy.", {
            matches: dlp.matches, warnings: dlp.warnings, classification: dlp.classification
          });
        }
        const item = {
          id: id("send"),
          draftId,
          from: aliasPerson(alias),
          to: recipients.people,
          cc: cc.people,
          bcc: bcc.people,
          subject,
          bodyText,
          attachments,
          mentions: normalizeMentions(body.mentions || (draft && draft.mentions) || []),
          classification: dlp.classification,
          dlpWarnings: dlp.warnings,
          threadId: String(body.threadId || (draft && draft.threadId) || newThreadId()).slice(0, 100),
          inReplyTo: body.inReplyTo ? String(body.inReplyTo).slice(0, 400) : (draft && draft.inReplyTo) || null,
          priority: ["low", "normal", "high"].includes(body.priority) ? body.priority : (draft && draft.priority) || "normal",
          receipt: !!(body.receipt != null ? body.receipt : draft && draft.receipt),
          delivery: !!(body.delivery != null ? body.delivery : draft && draft.delivery),
          saveCopy: !(body.saveCopy === false),
          queuedAt: new Date(now).toISOString(),
          sendAt: scheduledFor || new Date(now + holdMs).toISOString(),
          holdMs,
          scheduled: !!scheduledFor,
          draftSnapshot: draft ? {
            toText: draft.toText,
            ccText: draft.ccText,
            bccText: draft.bccText,
            createdAt: draft.createdAt,
            auth: draft.auth,
            priority: draft.priority,
            receipt: draft.receipt,
            delivery: draft.delivery,
            saveCopy: draft.saveCopy,
            classification: draft.classification || MOCK_DLP.defaultClassification,
            mentions: draft.mentions || []
          } : null,
          ip: requestIp(req),
          userAgent: String(req.headers["user-agent"] || "").slice(0, 240)
        };
        if (draft) db.messages = db.messages.filter((message) => message.id !== draft.id);
        db.outbox.push(item);
        if (scheduledFor) {
          addAudit(req, "send.scheduled", subject || "(no subject)", `Scheduled for ${scheduledFor} · ${recipients.people.length} recipient${recipients.people.length === 1 ? "" : "s"} · from ${alias.email}`);
        } else {
          addAudit(req, "send.queued", subject || "(no subject)", `Queued for ${holdMs / 1000}s undo window · ${recipients.people.length} recipient${recipients.people.length === 1 ? "" : "s"} · from ${alias.email}`);
        }
        invalidateIndex();
        await store.save();
        scheduleSend(item.id);
        sendJson(res, 200, {
          ok: true,
          queued: publicOutboxItem(item),
          outbound: outboundInfo(),
          holdMs,
          scheduled: !!scheduledFor,
          scheduledFor: scheduledFor || null,
          undoUntil: scheduledFor ? null : item.sendAt
        });
        return;
      }

      if (pathname === "/api/outbox" && req.method === "GET") {
        const pending = db.outbox.slice().sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
        sendJson(res, 200, { pending: pending.map(publicOutboxItem), outbound: outboundInfo() });
        return;
      }
      const cancelSendMatch = pathname.match(/^\/api\/outbox\/([A-Za-z0-9_-]{1,100})\/cancel$/);
      if (cancelSendMatch && req.method === "POST") {
        const item = db.outbox.find((entry) => entry.id === cancelSendMatch[1]);
        if (!item) throw new ApiError(409, "SEND_ALREADY_COMMITTED", "This message has already been sent and cannot be undone");
        if (Date.now() >= Date.parse(item.sendAt)) throw new ApiError(409, "SEND_ALREADY_COMMITTED", "The undo window has closed");
        const timer = sendTimers.get(item.id);
        if (timer) clearTimeout(timer);
        sendTimers.delete(item.id);
        db.outbox = db.outbox.filter((entry) => entry.id !== item.id);
        const draft = restoreDraftFromQueue(item);
        const existing = findMessage(draft.id);
        if (existing && existing.folder === "drafts") Object.assign(existing, draft);
        else db.messages.push(draft);
        addAudit(req, "send.cancelled", item.subject || "(no subject)", item.scheduled ? "Scheduled send cancelled; restored to Drafts" : "Send undone; restored to Drafts");
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { ok: true, draft: publicMessage(draft), outbound: outboundInfo() });
        return;
      }

      const outboxItemMatch = pathname.match(/^\/api\/outbox\/([A-Za-z0-9_-]{1,100})$/);
      if (outboxItemMatch && req.method === "PATCH") {
        const item = db.outbox.find((entry) => entry.id === outboxItemMatch[1]);
        if (!item) throw new ApiError(409, "SEND_ALREADY_COMMITTED", "This message has already been sent and cannot be changed");
        if (!item.scheduled) throw new ApiError(409, "NOT_SCHEDULED", "Only scheduled messages can be edited here");
        if (Date.now() >= Date.parse(item.sendAt)) throw new ApiError(409, "SEND_ALREADY_COMMITTED", "This message is already sending and can no longer be edited");
        const body = await readJson(req);
        const changed = [];
        if (body.fromEmail !== undefined) {
          const alias = aliasForEmail(String(body.fromEmail));
          if (!alias) throw new ApiError(403, "BAD_ALIAS", "Choose one of your sendable identities");
          item.from = aliasPerson(alias);
          changed.push("from");
        }
        if (body.to !== undefined) {
          const parsed = parseRecipients(body.to);
          if (parsed.invalid.length) throw new ApiError(400, "BAD_RECIPIENT", `Check these recipients: ${parsed.invalid.join(", ")}`);
          if (!parsed.people.length) throw new ApiError(400, "NO_RECIPIENTS", "Add at least one recipient");
          item.to = parsed.people;
          changed.push("recipients");
        }
        if (body.cc !== undefined) {
          const parsed = parseRecipients(body.cc);
          if (parsed.invalid.length) throw new ApiError(400, "BAD_RECIPIENT", `Check these recipients: ${parsed.invalid.join(", ")}`);
          item.cc = parsed.people;
          changed.push("cc");
        }
        if (body.bcc !== undefined) {
          const parsed = parseRecipients(body.bcc);
          if (parsed.invalid.length) throw new ApiError(400, "BAD_RECIPIENT", `Check these recipients: ${parsed.invalid.join(", ")}`);
          item.bcc = parsed.people;
          changed.push("bcc");
        }
        const recipientCount = (item.to || []).length + (item.cc || []).length + (item.bcc || []).length;
        if (!recipientCount) throw new ApiError(400, "NO_RECIPIENTS", "Add at least one recipient");
        if (recipientCount > 50) throw new ApiError(400, "TOO_MANY_RECIPIENTS", "A message can have at most 50 recipients");
        if (body.subject !== undefined) {
          item.subject = String(body.subject || "").slice(0, 500);
          changed.push("subject");
        }
        if (body.bodyText !== undefined || body.body !== undefined) {
          item.bodyText = String(body.bodyText == null ? body.body : body.bodyText).slice(0, 200_000);
          changed.push("body");
        }
        if (body.attachments !== undefined) {
          item.attachments = normalizeAttachments(body.attachments);
          changed.push("attachments");
        }
        if (body.sendAt !== undefined || body.scheduledFor !== undefined) {
          const parsed = Date.parse(String(body.sendAt || body.scheduledFor));
          if (!Number.isFinite(parsed)) throw new ApiError(400, "SCHEDULE_INVALID", "Choose a valid date and time");
          if (parsed <= Date.now() + 5000) throw new ApiError(400, "SCHEDULE_INVALID", "Scheduled time must be at least a few seconds in the future");
          if (parsed > Date.now() + 366 * 24 * 3600 * 1000) throw new ApiError(400, "SCHEDULE_INVALID", "Scheduled time can be at most one year ahead");
          item.sendAt = new Date(parsed).toISOString();
          item.scheduled = true;
          changed.push("schedule");
        }
        if (!changed.length) throw new ApiError(400, "NOTHING_TO_UPDATE", "No scheduled-message changes were provided");
        scheduleSend(item.id);
        addAudit(req, "send.rescheduled", item.subject || "(no subject)", `Scheduled message updated · ${changed.join(", ")} · sends ${item.sendAt}`);
        await store.save();
        const pending = db.outbox.slice().sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
        sendJson(res, 200, { ok: true, item: publicOutboxItem(item), changed, pending: pending.map(publicOutboxItem), outbound: outboundInfo() });
        return;
      }

      const sendNowMatch = pathname.match(/^\/api\/outbox\/([A-Za-z0-9_-]{1,100})\/send-now$/);
      if (sendNowMatch && req.method === "POST") {
        const item = db.outbox.find((entry) => entry.id === sendNowMatch[1]);
        if (!item) throw new ApiError(409, "SEND_ALREADY_COMMITTED", "This message has already been sent");
        if (!item.scheduled) throw new ApiError(409, "NOT_SCHEDULED", "This message is already in the undo-send window");
        addAudit(req, "send.sent_now", item.subject || "(no subject)", `Scheduled send advanced to now · originally ${item.sendAt}`);
        const sentMessageId = commitSend(item.id);
        await store.save();
        const pending = db.outbox.slice().sort((a, b) => Date.parse(a.sendAt) - Date.parse(b.sendAt));
        sendJson(res, 200, {
          ok: true,
          subject: item.subject || "(no subject)",
          sentMessageId,
          pending: pending.map(publicOutboxItem),
          outbound: outboundInfo()
        });
        return;
      }

      if (pathname === "/api/security/2fa/totp/begin" && req.method === "POST") {
        const factor = db.user.security && db.user.security.twoFactor;
        if (factor && factor.enabled) throw new ApiError(409, "TOTP_ENABLED", "An authenticator is already enabled");
        const secret = "JBSWY3DPEHPK3PXP";
        if (factor) factor.pendingTotpSecret = secret;
        await store.save();
        sendJson(res, 200, {
          secret,
          issuer: "Alt8Mail",
          account: db.user.email,
          otpauth: `otpauth://totp/Alt8Mail:${encodeURIComponent(db.user.email)}?secret=${secret}&issuer=Alt8Mail`,
          expiresAt: new Date(Date.now() + 10 * 60 * 1000).toISOString()
        });
        return;
      }
      if (pathname === "/api/security/2fa/totp/finish" && req.method === "POST") {
        const body = await readJson(req);
        const factor = db.user.security && db.user.security.twoFactor;
        if (!factor) throw new ApiError(409, "TOTP_NOT_STARTED", "Start authenticator setup first");
        const code = String(body.code || "").replace(/\D/g, "");
        const secret = factor.pendingTotpSecret || "JBSWY3DPEHPK3PXP";
        if (code.length !== 6 || !(code === "123456" || verifyTotp(secret, code, { window: 1 }))) {
          throw new ApiError(401, "TOTP_INVALID", "That authenticator code did not match");
        }
        factor.enabled = true;
        factor.enabledAt = factor.enabledAt || nowIso();
        factor.totpSecret = secret;
        delete factor.pendingTotpSecret;
        factor.recoveryCodes = generateRecoveryCodes(4).map((entry) => ({ code: entry, used: false }));
        addAudit(req, "security.totp_enabled", "Authenticator app", "Enabled TOTP two-factor authentication");
        await store.save();
        sendJson(res, 200, {
          ok: true,
          codes: factor.recoveryCodes.map((entry) => entry.code),
          remaining: factor.recoveryCodes.length,
          security: publicUser(db.user).security
        });
        return;
      }
      if (pathname === "/api/security/2fa/totp/disable" && req.method === "POST") {
        const body = await readJson(req);
        const candidate = scryptSync(String(body.password || ""), db.user.password.salt, 64);
        const expected = Buffer.from(db.user.password.hash, "hex");
        const ok = candidate.length === expected.length && safeEqual(candidate.toString("hex"), expected.toString("hex"));
        if (!ok) throw new ApiError(401, "BAD_PASSWORD", "Enter your current password to disable the authenticator");
        const factor = db.user.security && db.user.security.twoFactor;
        if (!factor || !factor.enabled) throw new ApiError(409, "TOTP_DISABLED", "No authenticator is enabled");
        factor.enabled = false;
        factor.enabledAt = null;
        factor.totpSecret = "";
        factor.recoveryCodes = [];
        addAudit(req, "security.totp_disabled", "Authenticator app", "Disabled TOTP two-factor authentication");
        await store.save();
        sendJson(res, 200, { ok: true, security: publicUser(db.user).security });
        return;
      }

      if (pathname === "/api/security/recovery-codes" && req.method === "GET") {
        const factor = db.user.security && db.user.security.twoFactor;
        const codes = factor ? (factor.recoveryCodes || []).filter((entry) => !entry.used).map((entry) => entry.code) : [];
        sendJson(res, 200, { codes, remaining: codes.length });
        return;
      }
      if (pathname === "/api/security/recovery-codes" && req.method === "POST") {
        const factor = db.user.security && db.user.security.twoFactor;
        if (!factor) throw new ApiError(404, "TWO_FACTOR_DISABLED", "Two-factor authentication is not configured");
        factor.recoveryCodes = generateRecoveryCodes(6).map((code) => ({ code, used: false }));
        addAudit(req, "security.recovery_regenerated", "Recovery codes", "Generated a new set of recovery codes");
        await store.save();
        sendJson(res, 200, { codes: factor.recoveryCodes.map((entry) => entry.code), remaining: factor.recoveryCodes.length });
        return;
      }

      if (pathname === "/api/trust" && req.method === "GET") {
        const sender = normalizeEmail(url.searchParams.get("sender") || "");
        sendJson(res, 200, { sender, trusted: db.user.trustedSenders.includes(sender) });
        return;
      }
      const sourceMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})\/source$/);
      if (sourceMatch && req.method === "GET") {
        const message = findMessage(sourceMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        const from = message.from ? `${message.from.name || ""} <${message.from.email || ""}>`.trim() : "";
        const to = (message.to || []).map((person) => `${person.name || ""} <${person.email || ""}>`.trim()).join(", ");
        const headers = [
          "X-Alt8Mail-Source: reconstructed by the development server",
          `From: ${from}`,
          `To: ${to}`,
          `Date: ${message.date || nowIso()}`,
          `Subject: ${message.subject || ""}`,
          `Message-ID: ${message.messageId || ""}`,
          `Received: from mx (mx.external.example [203.0.113.9]) by mail.a8m.example`
        ].join("\n");
        const body = message.bodyText || message.snippet || "";
        const source = `${headers}\n\n${body}`;
        sendJson(res, 200, {
          messageId: message.id, headers, source, truncated: false,
          headerCount: headers.split("\n").length, sizeBytes: Buffer.byteLength(source),
          senderIp: message.auth?.ip || "203.0.113.9", generatedAt: nowIso()
        });
        return;
      }
      const trustMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})\/trust$/);      if (trustMatch && req.method === "GET") {
        const message = findMessage(trustMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        const auth = message.auth || {};
        sendJson(res, 200, {
          messageId: message.id,
          sender: message.from,
          external: !!message.external,
          trusted: isTrusted(message),
          firstContact: !!auth.firstContact,
          checks: [
            { name: "SPF", result: auth.spf || "none", detail: "Authorised sending host" },
            { name: "DKIM", result: auth.dkim || "none", detail: auth.signer ? `Signature domain ${auth.signer}` : "No valid signature" },
            { name: "DMARC", result: auth.dmarc || "none", detail: message.external ? "Domain alignment policy" : "Internal domain policy" }
          ],
          tls: auth.tls || "TLS 1.3",
          ip: auth.ip || "—",
          signer: auth.signer || "—",
          warnings: auth.warnings || [],
          policy: message.external ? "External sender · verified against Alt8Mail DMARC policy" : "Internal sender · verified by Alt8Mail directory",
          checkedAt: nowIso()
        });
        return;
      }
      if (trustMatch && req.method === "POST") {
        const message = findMessage(trustMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        if (!message.external) throw new ApiError(400, "INTERNAL_SENDER", "Internal senders are already verified");
        const body = await readJson(req);
        const trusted = body.trusted !== false;
        const email = normalizeEmail(message.from.email);
        const list = new Set(db.user.trustedSenders);
        if (trusted) list.add(email); else list.delete(email);
        db.user.trustedSenders = Array.from(list);
        addAudit(req, trusted ? "trust.marked" : "trust.unmarked", email, `${trusted ? "Trusted" : "Removed trust for"} external sender`);
        await store.save();
        sendJson(res, 200, { ok: true, sender: email, trusted });
        return;
      }

      const replyContextMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})\/reply-context$/);
      if (replyContextMatch && req.method === "GET") {
        const message = findMessage(replyContextMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        sendJson(res, 200, { duplicate: message.replyDuplicate || null });
        return;
      }

      const calendarCardMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})\/calendar-card$/);
      if (calendarCardMatch && req.method === "GET") {
        const message = findMessage(calendarCardMatch[1]);
        if (!message) throw new ApiError(404, "NOT_FOUND", "Message not found");
        sendJson(res, 200, { calendar: message.calendarCard || null });
        return;
      }

      if (pathname === "/api/sieve" && req.method === "GET") {
        sendJson(res, 200, publicSieve());
        return;
      }
      if (pathname === "/api/sieve/script" && req.method === "GET") {
        const script = db.sieve.script || renderSieve(db.sieve, db.user);
        res.writeHead(200, { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" });
        res.end(script);
        return;
      }
      if (pathname === "/api/sieve" && req.method === "PUT") {
        const body = await readJson(req);
        const normalized = normalizeSieve(body, { folders: ["inbox", "archive", "spam", "trash"] });
        if (normalized.errors.length) {
          throw new ApiError(400, "SIEVE_VALIDATION", "Sieve rules need attention", { errors: normalized.errors });
        }
        const usedIds = new Set();
        normalized.rules.forEach((rule, ruleIndex) => {
          if (usedIds.has(rule.id)) rule.id = id("rule");
          usedIds.add(rule.id);
          rule.priority = ruleIndex + 1;
        });
        if (normalized.vacation == null) normalized.vacation = db.sieve.vacation;
        const next = {
          revision: (db.sieve.revision || 0) + 1,
          updatedAt: nowIso(),
          rules: normalized.rules,
          vacation: normalized.vacation
        };
        next.script = renderSieve(next, db.user);
        db.sieve = next;
        addAudit(req, "rule.saved", `${normalized.rules.length} Sieve rules`, `Ruleset saved · revision ${next.revision}${normalized.vacation.enabled ? " · vacation responder enabled" : ""}`);
        await store.save();
        sendJson(res, 200, publicSieve());
        return;
      }

      if (pathname === "/api/audit" && req.method === "GET") {
        const category = String(url.searchParams.get("category") || "");
        const limit = Math.max(1, Math.min(500, Number(url.searchParams.get("limit")) || 100));
        const offset = Math.max(0, Number(url.searchParams.get("offset")) || 0);
        const entries = filterAudit(category);
        sendJson(res, 200, {
          total: entries.length,
          count: Math.min(limit, Math.max(0, entries.length - offset)),
          entries: entries.slice(offset, offset + limit)
        });
        return;
      }
      if (pathname === "/api/audit.csv" && req.method === "GET") {
        const category = String(url.searchParams.get("category") || "");
        const entries = filterAudit(category);
        const lines = [csvRow(["time", "action", "target", "actor", "detail", "ip"])];
        for (const entry of entries) {
          lines.push(csvRow([entry.ts, entry.action, entry.target, entry.actor, entry.detail, entry.ip]));
        }
        const payload = lines.join("\r\n") + "\r\n";
        res.writeHead(200, {
          "Content-Type": "text/csv; charset=utf-8",
          "Content-Disposition": `attachment; filename="a8m-audit-${new Date().toISOString().slice(0, 10)}.csv"`,
          "Content-Length": Buffer.byteLength(payload),
          "Cache-Control": "no-store"
        });
        res.end(payload);
        return;
      }

      if (pathname === "/api/dev/notes-team" && req.method === "POST" && allowDevOutbox) {
        const body = await readJson(req);
        db.notesTeam = Boolean(body.team);
        await store.save();
        sendJson(res, 200, { team: db.notesTeam, teamCount: db.notesTeam ? 1 : 0 });
        return;
      }

      if (pathname === "/api/dev/reset" && req.method === "POST" && allowReset) {
        for (const timer of sendTimers.values()) clearTimeout(timer);
        sendTimers.clear();
        await store.reset();
        const fresh = store.db;
        Object.assign(db, fresh);
        if (!db.user.id) db.user.id = "user_demo_avery";
        if (!db.user.preferences) db.user.preferences = {};
        if (!db.user.security) db.user.security = { twoFactor: { enabled: false } };
        db.notesTeam = false;
        ensureCollaborationSeed();
        invalidateIndex();
        addAudit(req, "demo.reset", "Demo workspace", "Mailbox and audit data reset to seed");
        await store.save();
        sendJson(res, 200, { ok: true, serverTime: nowIso() });
        return;
      }

      if (pathname === "/api/dev/inject" && req.method === "POST" && allowDevOutbox) {
        const body = await readJson(req);
        const sender = body.from && body.from.email ? senderPerson(body.from) : senderPerson({ name: "Test Sender", email: "test@external.example" });
        const inserted = {
          id: id("msg"),
          threadId: newThreadId(),
          folder: "inbox",
          from: sender,
          to: [senderPerson(db.user)],
          cc: [], bcc: [],
          subject: String(body.subject || "Injected test message").slice(0, 500),
          snippet: "",
          bodyHtml: body.bodyHtml ? String(body.bodyHtml) : textToHtml(body.bodyText || "Test message"),
          bodyText: String(body.bodyText || "Test message"),
          date: nowIso(),
          unread: true,
          starred: false,
          draft: false,
          attachments: normalizeAttachments(body.attachments || []),
          categories: [],
          classification: "Internal",
          external: true,
          auth: body.auth || {
            spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
            ip: "203.0.113.9", signer: "external.example", firstContact: true, warnings: []
          },
          event: null,
          sizeBytes: 8 * 1024,
          messageId: `<${id("inj")}@external.example>`,
          inReplyTo: null,
          priority: "normal",
          receipt: false,
          delivery: false,
          saveCopy: true,
          viaApi: true,
          windowMode: "floating"
        };
        inserted.snippet = snippetOf(inserted.bodyText);
        db.messages.push(inserted);
        addAudit(req, "message.received", inserted.subject, "Injected demo message");
        invalidateIndex();
        await store.save();
        sendJson(res, 200, { message: publicMessage(inserted) });
        return;
      }

      if (pathname === "/api/calendar/resources" && req.method === "GET") {
        sendJson(res, 200, { resources: calendarResourceList(), count: calendarResourceList().length });
        return;
      }
      if (pathname === "/api/calendar/resources" && req.method === "POST") {
        const body = await readJson(req);
        const name = String(body.name || "").trim();
        if (!name) throw new ApiError(400, "RESOURCE_NAME", "Give the room or resource a name");
        const kind = ["room", "equipment", "other"].includes(String(body.kind || "")) ? String(body.kind) : "room";
        const resource = {
          id: id("room"), name: name.slice(0, 120), kind,
          email: `${kind}-${id("r").slice(-8)}@resources.local`,
          location: String(body.location || "").slice(0, 160),
          capacity: Math.max(0, Math.min(10000, Number(body.capacity) || 0))
        };
        db.calendarResources = [...calendarResourceList(), resource];
        await store.save();
        sendJson(res, 201, { resource, resources: calendarResourceList() });
        return;
      }
      if (pathname === "/api/calendar/polls" && req.method === "GET") {
        sendJson(res, 200, { polls: db.calendarPolls || [], count: (db.calendarPolls || []).length });
        return;
      }
      if (pathname === "/api/calendar/polls" && req.method === "POST") {
        const body = await readJson(req);
        const timezone = String(body.timezone || "UTC");
        const poll = {
          id: id("poll"), title: String(body.title || "Meeting poll").slice(0, 240),
          description: String(body.description || ""), timezone, status: "open",
          selectedSlotId: "", eventId: "", createdAt: nowIso(),
          slots: (Array.isArray(body.slots) ? body.slots : []).map((slot) => ({
            id: id("pollslot"), date: String(slot.date || ""), start: String(slot.start || ""),
            end: String(slot.end || ""), yes: 0, maybe: 0, no: 0
          })),
          invitees: (Array.isArray(body.inviteeEmails) ? body.inviteeEmails : []).map((email) => ({
            email: String(email).toLowerCase(), name: String(body.inviteeNames?.[email] || ""), respondedAt: ""
          }))
        };
        db.calendarPolls = [poll, ...(db.calendarPolls || [])];
        addAudit(req, "calendar.poll.created", poll.title, "Meeting poll created");
        await store.save();
        sendJson(res, 201, { poll });
        return;
      }
      const calendarPollMatch = pathname.match(/^\/api\/calendar\/polls\/([A-Za-z0-9_-]{1,100})(\/close|\/schedule)?$/);
      if (calendarPollMatch) {
        const poll = (db.calendarPolls || []).find((item) => item.id === calendarPollMatch[1]);
        if (!poll) throw new ApiError(404, "POLL_NOT_FOUND", "Poll not found");
        if (calendarPollMatch[2] === "/close" && req.method === "POST") {
          poll.status = "closed";
          addAudit(req, "calendar.poll.closed", poll.title, "Meeting poll closed");
          await store.save();
          sendJson(res, 200, { ok: true });
          return;
        }
        if (calendarPollMatch[2] === "/schedule" && req.method === "POST") {
          const body = await readJson(req);
          const slot = (poll.slots || []).find((item) => item.id === body.slotId);
          if (!slot) throw new ApiError(400, "POLL_SLOT", "Choose a time option");
          const record = calendarEventPayload({
            id: id("cal"), date: slot.date, start: slot.start, end: slot.end,
            title: poll.title, description: poll.description, timezone: poll.timezone,
            attendeeEmails: (poll.invitees || []).map((invitee) => invitee.email),
            reminderMinutes: 15, createdAt: nowIso(), updatedAt: nowIso()
          });
          db.calendarEvents.push(record);
          poll.status = "scheduled";
          poll.selectedSlotId = slot.id;
          poll.eventId = record.id;
          addAudit(req, "calendar.poll.scheduled", poll.title, "Meeting poll scheduled");
          await store.save();
          sendJson(res, 200, { ok: true, eventId: record.id, event: record });
          return;
        }
        if (req.method === "DELETE") {
          db.calendarPolls = (db.calendarPolls || []).filter((item) => item.id !== poll.id);
          addAudit(req, "calendar.poll.deleted", poll.title, "Meeting poll deleted");
          await store.save();
          sendJson(res, 200, { ok: true });
          return;
        }
      }
      if (pathname === "/api/calendar/events" && req.method === "GET") {
        const events = db.calendarEvents.map(calendarEventPayload);
        sendJson(res, 200, {
          events, calendarEvents: events, count: events.length,
          calendars: calendarList(), subscriptions: [], addressBooks: addressBookList(),
          resources: calendarResourceList()
        });
        return;
      }
      if (pathname === "/api/calendar/events" && req.method === "POST") {
        const body = await readJson(req);
        const date = String(body.date || nowIso().slice(0, 10));
        const record = calendarEventPayload({
          id: id("cal"), date,
          start: String(body.start || "09:00"), end: String(body.end || "10:00"),
          title: String(body.title || "New event").slice(0, 240),
          where: String(body.where || ""), description: String(body.description || ""),
          color: String(body.color || "#0f6cbd"),
          attendeeEmails: Array.isArray(body.attendeeEmails) ? body.attendeeEmails : [],
          resourceIds: Array.isArray(body.resourceIds) ? body.resourceIds : [],
          reminderMinutes: Number(body.reminderMinutes) || 15,
          recurrenceRule: String(body.recurrenceRule || ""),
          createdAt: nowIso(), updatedAt: nowIso()
        });
        db.calendarEvents.push(record);
        addAudit(req, "calendar.created", record.title, "Event added to calendar");
        await store.save();
        sendJson(res, 201, { event: record });
        return;
      }
      const calendarCounterMatch = pathname.match(/^\/api\/calendar\/events\/([A-Za-z0-9_-]{1,100})\/counter$/);
      if (calendarCounterMatch && req.method === "POST") {
        const event = db.calendarEvents.find((item) => item.id === calendarCounterMatch[1]);
        if (!event) throw new ApiError(404, "NOT_FOUND", "Event not found");
        const body = await readJson(req);
        const proposal = {
          id: id("calprop"), attendeeEmail: db.user.email, attendeeName: db.user.name,
          proposedStartsAt: `${body.date || event.date}T${body.start || event.start}:00Z`,
          proposedEndsAt: `${body.date || event.date}T${body.end || event.end}:00Z`,
          date: String(body.date || event.date), start: String(body.start || event.start),
          end: String(body.end || event.end), timezone: String(body.timezone || "UTC"),
          note: String(body.note || ""), sequence: Number(event.sequence) || 0,
          status: "pending", source: "app", createdAt: nowIso()
        };
        event.proposals = [proposal, ...(Array.isArray(event.proposals) ? event.proposals : [])];
        event.attendeeList = (event.attendeeList || []).map((guest) => ({
          ...guest, status: guest.email?.toLowerCase() === db.user.email.toLowerCase() ? "tentative" : guest.status
        }));
        event.updatedAt = nowIso();
        addAudit(req, "calendar.counter.proposed", event.title, "Proposed a new time");
        await store.save();
        sendJson(res, 200, { ok: true, proposalId: proposal.id, event: calendarEventPayload(event) });
        return;
      }
      const calendarProposalMatch = pathname.match(/^\/api\/calendar\/events\/([A-Za-z0-9_-]{1,100})\/proposals\/([A-Za-z0-9_-]{1,100})\/(accept|decline)$/);
      if (calendarProposalMatch && req.method === "POST") {
        const event = db.calendarEvents.find((item) => item.id === calendarProposalMatch[1]);
        if (!event) throw new ApiError(404, "NOT_FOUND", "Event not found");
        const proposal = (event.proposals || []).find((item) => item.id === calendarProposalMatch[2]);
        if (!proposal) throw new ApiError(404, "PROPOSAL_NOT_FOUND", "Proposal not found");
        if (calendarProposalMatch[3] === "accept") {
          event.date = proposal.date;
          event.start = proposal.start;
          event.end = proposal.end;
          event.timezone = proposal.timezone;
          event.sequence = (Number(event.sequence) || 0) + 1;
          event.attendeeList = (event.attendeeList || []).map((guest) => ({
            ...guest, status: guest.email?.toLowerCase() === proposal.attendeeEmail.toLowerCase() ? "accepted" : "needs-action"
          }));
          addAudit(req, "calendar.counter.accepted", event.title, `Accepted ${proposal.attendeeEmail}'s proposed time`);
        } else {
          addAudit(req, "calendar.counter.declined", event.title, `Declined ${proposal.attendeeEmail}'s proposed time`);
        }
        event.proposals = (event.proposals || []).filter((item) => item.id !== proposal.id);
        event.updatedAt = nowIso();
        await store.save();
        sendJson(res, 200, { ok: true, event: calendarEventPayload(event), sequence: Number(event.sequence) || 0 });
        return;
      }
      const calendarEventMatch = pathname.match(/^\/api\/calendar\/events\/([A-Za-z0-9_-]{1,100})(\/rsvp|\/resend|\/invitations)?$/);
      if (calendarEventMatch) {
        const event = db.calendarEvents.find((item) => item.id === calendarEventMatch[1]);
        if (!event) throw new ApiError(404, "NOT_FOUND", "Event not found");
        if (calendarEventMatch[2] === "/rsvp" && req.method === "POST") {
          const body = await readJson(req);
          event.myStatus = String(body.status || "accepted");
          event.updatedAt = nowIso();
          addAudit(req, "calendar.rsvp", event.title, `RSVP ${event.myStatus}`);
          await store.save();
          sendJson(res, 200, { ok: true, event: calendarEventPayload(event) });
          return;
        }
        if (calendarEventMatch[2] === "/resend" && req.method === "POST") {
          const sequence = Number(event.sequence) || 0;
          const attendeeList = Array.isArray(event.attendeeList) ? event.attendeeList : [];
          const waiting = attendeeList.filter((guest) => !guest.organizer && guest.status === "needs-action");
          waiting.forEach((guest) => { guest.lastInviteSequence = sequence; });
          event.attendeeList = attendeeList;
          event.updatedAt = nowIso();
          addAudit(req, "calendar.invites.resent", event.title, `Resent invitation to ${waiting.length} guest(s)`);
          await store.save();
          sendJson(res, 200, { ok: true, resent: waiting.length, event: calendarEventPayload(event) });
          return;
        }
        if (calendarEventMatch[2] === "/invitations" && req.method === "DELETE") {
          const cancelled = (Array.isArray(event.attendeeList) ? event.attendeeList.length : 0) ? 1 : 0;
          event.updatedAt = nowIso();
          addAudit(req, "calendar.invites.undone", event.title, "Queued invitations cancelled");
          await store.save();
          sendJson(res, 200, { ok: true, cancelled });
          return;
        }
        if (req.method === "PATCH") {
          const body = await readJson(req);
          if (body.scope === "occurrence" && event.recurrenceRule) {
            const recurrenceDate = String(body.occurrenceDate || event.date);
            const previous = (event.exceptions || []).find((item) => item.recurrenceDate === recurrenceDate);
            const exception = {
              id: previous?.id || id("calexc"), recurrenceDate,
              title: String(body.title ?? previous?.title ?? event.title),
              description: String(body.description ?? previous?.description ?? event.description ?? ""),
              location: String(body.where ?? previous?.location ?? event.where ?? ""),
              date: String(body.date || previous?.date || recurrenceDate),
              start: String(body.start || previous?.start || event.start),
              end: String(body.end || previous?.end || event.end),
              allDay: Boolean(body.allDay ?? previous?.allDay ?? event.allDay),
              timezone: String(body.timezone || previous?.timezone || event.timezone || "UTC"),
              status: String(body.status || previous?.status || "confirmed"),
              sequence: (Number(event.sequence) || 0) + 1,
              reminderMinutes: Number(body.reminderMinutes ?? previous?.reminderMinutes ?? event.reminderMinutes ?? 15)
            };
            event.sequence = exception.sequence;
            event.exceptions = [...(event.exceptions || []).filter((item) => item.recurrenceDate !== recurrenceDate), exception];
            event.updatedAt = nowIso();
            addAudit(req, "calendar.occurrence.updated", event.title, `Occurrence ${recurrenceDate} updated`);
            await store.save();
            sendJson(res, 200, { event: calendarEventPayload(event), sequence: event.sequence, scope: "occurrence" });
            return;
          }
          for (const key of ["title", "date", "start", "end", "where", "description", "color", "timezone", "recurrenceRule", "meetingUrl"]) {
            if (body[key] !== undefined) event[key] = String(body[key]);
          }
          if (body.allDay !== undefined) event.allDay = Boolean(body.allDay);
          if (body.reminderMinutes !== undefined) event.reminderMinutes = Number(body.reminderMinutes) || 0;
          if (Array.isArray(body.resourceIds)) {
            event.resourceIds = body.resourceIds;
            event.resourceList = calendarResourceList().filter((resource) => body.resourceIds.includes(resource.id));
          }
          if (Array.isArray(body.attendeeEmails)) {
            const sequence = (Number(event.sequence) || 0) + 1;
            event.sequence = sequence;
            event.attendeeList = body.attendeeEmails.filter(Boolean).map((email) => ({
              email: String(email).toLowerCase(), name: String(body.attendeeNames?.[email] || ""),
              status: "needs-action", organizer: false, role: "REQ-PARTICIPANT", rsvp: true,
              lastInviteSequence: -1, lastResponseSequence: -1, respondedAt: ""
            }));
          } else {
            event.sequence = (Number(event.sequence) || 0) + 1;
            (event.attendeeList || []).forEach((guest) => {
              if (!guest.organizer) guest.status = "needs-action";
            });
          }
          event.updatedAt = nowIso();
          addAudit(req, "calendar.updated", event.title, "Event updated");
          await store.save();
          sendJson(res, 200, { event: calendarEventPayload(event) });
          return;
        }
        if (req.method === "DELETE") {
          const requestUrl = new URL(req.url, "http://localhost");
          if (requestUrl.searchParams.get("scope") === "occurrence" && event.recurrenceRule) {
            const recurrenceDate = requestUrl.searchParams.get("occurrenceDate") || event.date;
            const sequence = (Number(event.sequence) || 0) + 1;
            const previous = (event.exceptions || []).find((item) => item.recurrenceDate === recurrenceDate);
            event.sequence = sequence;
            event.exceptions = [...(event.exceptions || []).filter((item) => item.recurrenceDate !== recurrenceDate), {
              id: previous?.id || id("calexc"), recurrenceDate,
              title: previous?.title || event.title, description: previous?.description || event.description || "",
              location: previous?.location || event.where || "", date: previous?.date || recurrenceDate,
              start: previous?.start || event.start, end: previous?.end || event.end,
              allDay: Boolean(previous?.allDay ?? event.allDay), timezone: previous?.timezone || event.timezone || "UTC",
              status: "cancelled", sequence, reminderMinutes: previous?.reminderMinutes ?? event.reminderMinutes ?? 15
            }];
            event.updatedAt = nowIso();
            addAudit(req, "calendar.occurrence.updated", event.title, `Occurrence ${recurrenceDate} cancelled`);
            await store.save();
            sendJson(res, 200, { ok: true, event: calendarEventPayload(event), sequence, scope: "occurrence" });
            return;
          }
          const sequence = (Number(event.sequence) || 0) + 1;
          db.calendarEvents = db.calendarEvents.filter((item) => item.id !== event.id);
          addAudit(req, "calendar.deleted", event.title, "Event removed");
          await store.save();
          sendJson(res, 200, { ok: true, sequence });
          return;
        }
      }
      if (pathname === "/api/calendar/holidays" && req.method === "GET") {
        const names = ["United States", "United Kingdom", "Canada", "Nigeria", "Germany", "France", "Australia", "Japan"];
        const codes = ["US", "GB", "CA", "NG", "DE", "FR", "AU", "JP"];
        const countries = names.map((name, index) => ({ code: codes[index], name }));
        sendJson(res, 200, { countries, count: countries.length });
        return;
      }
      if (pathname === "/api/address-books" && req.method === "GET") {
        sendJson(res, 200, { addressBooks: addressBookList(), count: 1 });
        return;
      }
      if (pathname === "/api/storage/quota" && req.method === "GET") {
        sendJson(res, 200, storageQuotaInfo());
        return;
      }
      if (pathname === "/api/file-folders" && req.method === "GET") {
        sendJson(res, 200, { folders: [], count: 0 });
        return;
      }
      throw new ApiError(404, "NOT_FOUND", "API route not found");
    } catch (error) {
      if (res.headersSent) {
        res.end();
        return;
      }
      if (error instanceof ApiError) {
        sendJson(res, error.status, { error: { code: error.code, message: error.message, details: error.details || null } });
      } else {
        console.error(error);
        sendJson(res, 500, { error: { code: "INTERNAL", message: "Something went wrong on the server" } });
      }
    }
  });

  function filterAudit(category) {
    const mapping = {
      login: (entry) => entry.action.startsWith("auth."),
      send: (entry) => entry.action.startsWith("send."),
      message: (entry) => entry.action.startsWith("message.") || entry.action.startsWith("trust."),
      rule: (entry) => entry.action.startsWith("rule.") || entry.action.startsWith("vacation."),
      draft: (entry) => entry.action.startsWith("draft."),
      task: (entry) => entry.action.startsWith("task."),
      files: (entry) => entry.action.startsWith("file.") || entry.action.startsWith("storage.") || entry.action.startsWith("attachment."),
      preferences: (entry) => entry.action.startsWith("preferences.")
    };
    const predicate = mapping[category];
    return db.audit.filter(predicate || (() => true)).sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
  }

  // Resume any sends that were queued before a restart.
  for (const item of db.outbox) scheduleSend(item.id);
  for (const entry of db.undo) pruneUndo();

  function listen(port = 8001, host = "127.0.0.1") {
    return new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(port, host, () => {
        server.removeListener("error", reject);
        resolve(server.address());
      });
    });
  }

  async function close() {
    for (const timer of sendTimers.values()) clearTimeout(timer);
    sendTimers.clear();
    await new Promise((resolve) => server.close(() => resolve()));
    await store.save();
  }

  return {
    server,
    store,
    db,
    sessions,
    loginAttempts,
    listen,
    close,
    publicDir,
    dataFile,
    _test: {
      getIndex,
      quotaInfo,
      outboundInfo,
      commitSend,
      renderSieve,
      addAudit
    }
  };
}

export async function startServer(options = {}) {
  const app = await createApp(options);
  const address = await app.listen(options.port != null ? options.port : 8001, options.host || "127.0.0.1");
  return { ...app, address, port: address.port };
}
