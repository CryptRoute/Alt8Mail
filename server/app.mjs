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
  normalizeEmail, nowIso, parseCookies, publicUser, requestIp, safeEqual,
  snippetOf, textToHtml
} from "./util.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "..");
const UNDO_TTL_MS = 30_000;
const MAX_JSON_BYTES = 1.5 * 1024 * 1024;
const MAX_UPLOAD_JSON_BYTES = 13 * 1024 * 1024;
const MAX_UPLOAD_BYTES = 8 * 1024 * 1024;
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

  const store = new DataStore({ file: dataFile, seed: seedDatabase });
  await store.load();
  const db = store.db;
  if (Number.isFinite(options.outboundLimit)) db.user.outbound.limit = Number(options.outboundLimit);
  if (!db.attachments) db.attachments = {};
  if (!Array.isArray(db.tasks)) db.tasks = [];
  if (!Array.isArray(db.files)) db.files = [];
  if (!Array.isArray(db.storageProviders)) db.storageProviders = [];
  if (!db.user.preferences) db.user.preferences = {};
  if (!db.user.security) db.user.security = { twoFactor: { enabled: false } };

  const sessions = new Map();
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
    return { ...message, trusted: isTrusted(message) };
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

  function sessionFrom(req) {
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
    addAudit(req, "auth.login", db.user.email, detail || "Password sign-in · browser session");
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
    return {
      enabled: !!factor.enabled,
      enabledAt: factor.enabledAt || null,
      passkeys: (factor.passkeys || []).map(({ id, name, createdAt, lastUsedAt }) => ({ id, name, createdAt, lastUsedAt })),
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

  const PREFERENCE_LANGUAGES = new Set(["en-US", "en-GB", "de-DE", "fr-FR", "es-ES", "pt-BR", "nl-NL", "it-IT"]);

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
    if (!person) throw new ApiError(400, "TASK_ASSIGNEE", "Choose a person from the Meridian directory");
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
      categories: [],
      classification: "Internal",
      external: false,
      auth: {
        spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
        ip: "127.0.0.1", signer: "meridian.example", firstContact: false, warnings: []
      },
      event: null,
      sizeBytes,
      messageId: existing ? existing.messageId : `<${id("draft")}@meridian.example>`,
      inReplyTo: input.inReplyTo == null ? (existing && existing.inReplyTo) || null : String(input.inReplyTo).slice(0, 400),
      threadId: String(input.threadId || (existing && existing.threadId) || id("t")).slice(0, 100),
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
      date: now,
      updatedAt: now,
      createdAt: snapshot.createdAt || now,
      unread: false,
      starred: false,
      sizeBytes: 4 * 1024 + Buffer.byteLength(item.bodyText || "", "utf8") + (item.attachments || []).reduce((sum, file) => sum + (file.sizeBytes || 0), 0),
      threadId: item.threadId || id("t"),
      inReplyTo: item.inReplyTo || null,
      windowMode: "floating",
      external: false,
      auth: snapshot.auth || {
        spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
        ip: "127.0.0.1", signer: "meridian.example", firstContact: false, warnings: []
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
        threadId: item.threadId || id("t"),
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
        classification: "Internal",
        external: false,
        auth: {
          spf: "pass", dkim: "pass", dmarc: "pass", tls: "TLS 1.3",
          ip: "127.0.0.1", signer: "meridian.example", firstContact: false, warnings: []
        },
        event: null,
        sizeBytes: 4 * 1024 + Buffer.byteLength(item.bodyText || "", "utf8") + (item.attachments || []).reduce((sum, file) => sum + (file.sizeBytes || 0), 0),
        messageId: `<${id("sent")}@meridian.example>`,
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
    return {
      user: publicUser(db.user),
      csrf: session.csrf,
      expiresAt: new Date(session.expiresAt).toISOString()
    };
  }

  function bootstrapPayload(session) {
    return {
      user: publicUser(db.user),
      aliases: db.user.aliases,
      folders: db.folders,
      people: db.people,
      messages: db.messages.map(publicMessage),
      calendarEvents: db.calendarEvents,
      tasks: db.tasks,
      files: db.files,
      storageProviders: db.storageProviders,
      sieve: publicSieve(),
      quota: quotaInfo(),
      outbound: outboundInfo(),
      pendingSends: db.outbox.map(publicOutboxItem),
      auditCount: db.audit.length,
      capabilities: ["threads", "instant-search", "bulk-actions", "undo", "server-drafts", "undo-send", "scheduled-send", "sieve", "automatic-replies", "sender-trust", "outbound-caps", "quota", "audit-log", "pwa", "calendar", "directory", "tasks", "files", "storage-connectors", "attachment-preview", "two-factor", "passkeys", "preferences"],
      attachmentMaxBytes: MAX_UPLOAD_BYTES,
      csrf: session.csrf,
      demo: { email: DEMO_EMAIL, holdSeconds: holdOverride != null ? holdOverride / 1000 : 8 },
      serverTime: nowIso()
    };
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
          throw new ApiError(401, "BAD_CREDENTIALS", "That email and password do not match a Meridian account");
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
          detail: method === "sso" ? "Single sign-on · browser session" : "Password sign-in · browser session"
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
        sessions.delete(session.sid);
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
      if (pathname === "/api/bootstrap" && req.method === "GET") {
        sendJson(res, 200, bootstrapPayload(session));
        return;
      }
      if (pathname === "/api/folders" && req.method === "GET") {
        sendJson(res, 200, { folders: db.folders });
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
        sendJson(res, 200, { providers: db.storageProviders });
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
        const provider = providerById(String(body.providerId || "meridian"));
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
      const fileMatch = pathname.match(/^\/api\/files\/([A-Za-z0-9_-]{1,100})$/);
      if (fileMatch && req.method === "PATCH") {
        const file = findFile(fileMatch[1]);
        if (!file) throw new ApiError(404, "NOT_FOUND", "File not found");
        const body = await readJson(req);
        if (typeof body.starred === "boolean") file.starred = body.starred;
        if (body.description !== undefined) file.description = String(body.description).slice(0, 500);
        addAudit(req, "file.updated", file.name, "File metadata updated");
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
        const token = randomBytes(15).toString("base64url");
        const expiresAt = new Date(Date.now() + expiresDays * 24 * 3600 * 1000).toISOString();
        if (!Array.isArray(file.shares)) file.shares = [];
        file.shares.push({ token, access, expiresAt, createdAt: nowIso() });
        if (!file.sharedWith.includes("External link")) file.sharedWith.push("External link");
        addAudit(req, "file.shared", file.name, `External ${access} link · expires in ${expiresDays} days`);
        await store.save();
        sendJson(res, 200, {
          share: { url: `https://files.meridian.example/s/${token}`, access, expiresAt, expiresDays }
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
        let messages;
        if (query) messages = getIndex().search(query).messages;
        else if (folder === "starred") messages = db.messages.filter((message) => message.starred);
        else messages = db.messages.filter((message) => message.folder === folder);
        if (filter === "unread") messages = messages.filter((message) => message.unread);
        if (filter === "starred") messages = messages.filter((message) => message.starred);
        if (filter === "attachments") messages = messages.filter((message) => (message.attachments || []).length);
        sendJson(res, 200, {
          folder,
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
        let permanentlyDeleted = 0;
        for (const message of targets) {
          snapshot.push({
            id: message.id,
            folder: message.folder,
            unread: message.unread,
            starred: message.starred,
            flagged: message.flagged === true,
            spamReport: message.spamReport ? structuredClone(message.spamReport) : null,
            full: action === "delete" && message.folder === "trash" ? structuredClone(message) : null
          });
          if (action === "archive") message.folder = "archive";
          else if (action === "move") message.folder = String(body.folder);
          else if (action === "delete") {
            if (message.folder === "trash" || message.folder === "drafts") permanentlyDeleted += 1;
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
            message.folder = "inbox";
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
          db.messages = db.messages.filter((message) => !remove.has(message.id));
        }
        db.undo.push({
          token,
          createdAt: now,
          expiresAt: now + (undoOverride != null ? undoOverride : UNDO_TTL_MS),
          action,
          snapshot
        });
        if (db.undo.length > 50) db.undo = db.undo.slice(-50);
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
          undoToken: token,
          undoExpiresAt: new Date(now + (undoOverride != null ? undoOverride : UNDO_TTL_MS)).toISOString(),
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
        draft.messageId = existing ? existing.messageId : `<${draftId}@meridian.example>`;
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
          threadId: String(body.threadId || (draft && draft.threadId) || id("t")).slice(0, 100),
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
            saveCopy: draft.saveCopy
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
      const trustMatch = pathname.match(/^\/api\/messages\/([A-Za-z0-9_-]{1,100})\/trust$/);
      if (trustMatch && req.method === "GET") {
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
          policy: message.external ? "External sender · verified against Meridian DMARC policy" : "Internal sender · verified by Meridian directory",
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
          "Content-Disposition": `attachment; filename="meridian-audit-${new Date().toISOString().slice(0, 10)}.csv"`,
          "Content-Length": Buffer.byteLength(payload),
          "Cache-Control": "no-store"
        });
        res.end(payload);
        return;
      }

      if (pathname === "/api/dev/reset" && req.method === "POST" && allowReset) {
        for (const timer of sendTimers.values()) clearTimeout(timer);
        sendTimers.clear();
        await store.reset();
        const fresh = store.db;
        Object.assign(db, fresh);
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
          threadId: id("t"),
          folder: "inbox",
          from: sender,
          to: [senderPerson(db.user)],
          cc: [], bcc: [],
          subject: String(body.subject || "Injected test message").slice(0, 500),
          snippet: "",
          bodyHtml: textToHtml(body.bodyText || "Test message"),
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
