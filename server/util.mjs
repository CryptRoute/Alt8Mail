import { randomBytes, timingSafeEqual } from "node:crypto";
import { stripHtml } from "../public/search.mjs";

export const SESSION_COOKIE = "meridian_sid";
export const CSRF_COOKIE = "meridian_csrf";
export const SESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000;

export function id(prefix) {
  return `${prefix}_${randomBytes(9).toString("base64url")}`;
}

export function nowIso() {
  return new Date().toISOString();
}

export function escapeHtml(value) {
  return String(value == null ? "" : value).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;"
  }[ch]));
}

export function textToHtml(text) {
  const normalized = String(text == null ? "" : text).replace(/\r\n/g, "\n").trim();
  if (!normalized) return "";
  const paragraphs = normalized.split(/\n{2,}/).map((paragraph) => {
    const lines = paragraph.split("\n").map((line) => escapeHtml(line)).join("<br>");
    return `<p>${lines}</p>`;
  });
  return paragraphs.join("");
}

export function snippetOf(text, max = 150) {
  const clean = String(text || "").replace(/\s+/g, " ").trim();
  return clean.length > max ? clean.slice(0, max - 1).trimEnd() + "…" : clean;
}

export function stripHtmlToText(html) {
  return stripHtml(html);
}

export function safeEqual(a, b) {
  const left = Buffer.from(String(a));
  const right = Buffer.from(String(b));
  if (left.length !== right.length) return false;
  return timingSafeEqual(left, right);
}

export function parseCookies(header) {
  const cookies = {};
  if (!header) return cookies;
  for (const pair of String(header).split(";")) {
    const index = pair.indexOf("=");
    if (index === -1) continue;
    const key = pair.slice(0, index).trim();
    const value = pair.slice(index + 1).trim();
    if (!key) continue;
    try {
      cookies[key] = decodeURIComponent(value);
    } catch {
      cookies[key] = value;
    }
  }
  return cookies;
}

export function cookieHeader(name, value, options = {}) {
  const parts = [`${name}=${encodeURIComponent(value)}`, `Path=${options.path || "/"}`];
  if (options.maxAge != null) parts.push(`Max-Age=${Math.floor(options.maxAge / 1000)}`);
  if (options.httpOnly) parts.push("HttpOnly");
  parts.push(`SameSite=${options.sameSite || "Lax"}`);
  if (options.secure) parts.push("Secure");
  return parts.join("; ");
}

export function requestIp(req) {
  const forwarded = req.headers["x-forwarded-for"];
  if (forwarded) return String(forwarded).split(",")[0].trim();
  return req.socket && req.socket.remoteAddress ? req.socket.remoteAddress : "unknown";
}

export function isSecure(req) {
  return !!(req.socket && req.socket.encrypted) || String(req.headers["x-forwarded-proto"] || "").split(",")[0].trim() === "https";
}

export function publicUser(user) {
  const { password, security, ...safe } = user;
  if (security && security.twoFactor) {
    const factor = security.twoFactor;
    safe.security = {
      twoFactor: {
        enabled: !!factor.enabled,
        enabledAt: factor.enabledAt || null,
        methods: ["passkey", "authenticator", "recovery"],
        passkeys: (factor.passkeys || []).map(({ id, name, createdAt, lastUsedAt }) => ({ id, name, createdAt, lastUsedAt })),
        recoveryCodesRemaining: (factor.recoveryCodes || []).filter((item) => !item.used).length
      }
    };
  }
  return safe;
}

export function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2).replace(/\.?0+$/, "")} GB`;
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1).replace(/\.0$/, "")} MB`;
  if (value >= 1024) return `${Math.round(value / 1024)} KB`;
  return `${value} B`;
}

export function parseBytes(value) {
  const match = String(value || "").trim().match(/^([\d.]+)\s*(B|KB|MB|GB)$/i);
  if (!match) return 0;
  const number = Number(match[1]);
  const unit = match[2].toUpperCase();
  if (unit === "GB") return Math.round(number * 1024 ** 3);
  if (unit === "MB") return Math.round(number * 1024 ** 2);
  if (unit === "KB") return Math.round(number * 1024);
  return Math.round(number);
}

export function csvCell(value) {
  const text = String(value == null ? "" : value);
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

export function csvRow(cells) {
  return cells.map(csvCell).join(",");
}

export function normalizeEmail(email) {
  return String(email || "").trim().toLowerCase();
}

export function looksLikeEmail(value) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(String(value || "").trim());
}

export class ApiError extends Error {
  constructor(status, code, message, details) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}
