/* Pure UI helpers and avatar rendering. */
import { t } from "../../i18n.mjs";
import { state } from "./state.js";

export const $ = (selector, root = document) => root.querySelector(selector);
export const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
export const esc = (value) => String(value == null ? "" : value).replace(/[&<>"']/g, (ch) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[ch]));
export const uid = (prefix) => `${prefix}_${(crypto.randomUUID ? crypto.randomUUID() : Math.random().toString(36).slice(2)).replace(/-/g, "").slice(0, 18)}`;
export const clone = (value) => structuredClone(value);
export const escapeRegExp = (value) => String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
export const avatarFailures = new Set();
export const normalizedEmailDomain = (value) => normalizeEmail(value).split("@")[1] || "";
export const senderAvatarSource = (person) => {
  const email = normalizeEmail(person?.email);
  const domain = normalizedEmailDomain(email);
  if (!email || !domain || !domain.includes(".") || avatarFailures.has(domain)) return "";
  const ownDomain = normalizedEmailDomain(state?.user?.email);
  if (ownDomain && domain === ownDomain) return "";
  return `/api/mail/avatar?address=${encodeURIComponent(email)}`;
};
export const avatar = (person, size = "md") => {
  const p = person || { initials: "?", color: "#64748b" };
  const photo = senderAvatarSource(p);
  return `<span class="avatar avatar-${size}" style="--av:${esc(p.color || "#64748b")}" aria-hidden="true">${photo ? `<img class="avatar-photo" src="${esc(photo)}" alt="" loading="lazy" decoding="async" referrerpolicy="no-referrer" data-avatar-domain="${esc(normalizedEmailDomain(p.email))}">` : ""}<span class="avatar-initials">${esc(p.initials || "?")}</span></span>`;
};
document.addEventListener("load", (event) => {
  const image = event.target;
  if (image instanceof HTMLImageElement && image.matches("img[data-avatar-domain]")) image.closest(".avatar")?.classList.add("has-photo");
}, true);
document.addEventListener("error", (event) => {
  const image = event.target;
  if (!(image instanceof HTMLImageElement) || !image.matches("img[data-avatar-domain]")) return;
  const domain = image.getAttribute("data-avatar-domain") || "";
  if (domain) avatarFailures.add(domain);
  const holder = image.closest(".avatar");
  image.remove();
  holder?.classList.remove("has-photo");
}, true);

export const initialsFrom = (value) => {
  const parts = String(value || "").trim().split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  return String(value || "?").slice(0, 2).toUpperCase();
};

export const displayNameFromEmail = (value) => {
  const local = String(value || "").split("@")[0].replace(/[._-]+/g, " ").trim();
  if (!local) return t("helpers.guest");
  return local.replace(/\b\w/g, (letter) => letter.toUpperCase());
};

export const personLabel = (person) => person ? (person.name || person.email) : t("helpers.unknown-sender");
export const recipientLabel = (message) => {
  const list = Array.isArray(message.to) ? message.to : message.to ? [message.to] : [];
  if (!list.length) return t("helpers.no-recipient");
  if (list.length === 1) return list[0].name || list[0].email;
  return `${list[0].name || list[0].email} +${list.length - 1}`;
};

const SYSTEM_FOLDER_KEYS = {
  inbox: "ui.folder-labels.inbox", starred: "ui.folder-labels.starred", snoozed: "ui.folder-labels.snoozed",
  sent: "ui.folder-labels.sent", drafts: "ui.folder-labels.drafts", archive: "ui.folder-labels.archive",
  spam: "ui.folder-labels.spam", trash: "ui.folder-labels.trash"
};

export function localizedFolderLabel(folder) {
  if (!folder) return "";
  const key = SYSTEM_FOLDER_KEYS[folder.id];
  if (key) return t(key);
  return folder.label || folder.id || "";
}

export const normalizeEmail = (email) => String(email || "").trim().toLowerCase();
