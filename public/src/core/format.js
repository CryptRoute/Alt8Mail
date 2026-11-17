/* Locale-aware date, time, number and byte formatting. */
import { currentLocale as resolvedLocale, t } from "../../i18n.mjs";
import { state, DEFAULT_PREFERENCES } from "./state.js";

export function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  const format = (amount, unit, digits) => {
    try {
      return new Intl.NumberFormat(currentLocale(), {
        style: "unit", unit, unitDisplay: "short",
        minimumFractionDigits: 0, maximumFractionDigits: digits
      }).format(amount);
    } catch {
      return `${amount} ${unit === "gigabyte" ? "GB" : unit === "megabyte" ? "MB" : unit === "kilobyte" ? "KB" : "B"}`;
    }
  };
  if (value >= 1024 ** 3) return format(value / 1024 ** 3, "gigabyte", value / 1024 ** 3 >= 10 ? 1 : 2);
  if (value >= 1024 ** 2) return format(value / 1024 ** 2, "megabyte", 1);
  if (value >= 1024) return format(Math.round(value / 1024), "kilobyte", 0);
  return format(value, "byte", 0);
}

export function currentLocale() {
  return resolvedLocale();
}

export function currentTimeZone() {
  return (state.preferences && state.preferences.timeZone) || DEFAULT_PREFERENCES.timeZone;
}

export function timeZoneParts(value, timeZone = currentTimeZone()) {
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

export function dateKeyInZone(value, timeZone = currentTimeZone()) {
  const parts = timeZoneParts(value, timeZone);
  return parts ? partsToKey(parts) : "";
}

export function partsToKey(parts) {
  return `${parts.year}-${String(parts.month).padStart(2, "0")}-${String(parts.day).padStart(2, "0")}`;
}

export function keyToParts(key) {
  const match = String(key || "").match(/^(\d{4})-(\d{2})-(\d{2})$/);
  return match ? { year: Number(match[1]), month: Number(match[2]), day: Number(match[3]) } : null;
}

export function addDaysToKey(key, days) {
  const parts = keyToParts(key);
  if (!parts) return key;
  const date = new Date(Date.UTC(parts.year, parts.month - 1, parts.day + days, 12));
  return `${date.getUTCFullYear()}-${String(date.getUTCMonth() + 1).padStart(2, "0")}-${String(date.getUTCDate()).padStart(2, "0")}`;
}

export function weekdayOfKey(key) {
  const parts = keyToParts(key);
  if (!parts) return 0;
  const day = new Date(Date.UTC(parts.year, parts.month - 1, parts.day, 12)).getUTCDay();
  return day === 0 ? 7 : day;
}

export function timeZoneOffsetMs(date, timeZone = currentTimeZone()) {
  const parts = timeZoneParts(date, timeZone);
  if (!parts) return 0;
  const asUTC = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
  return asUTC - Math.floor(date.getTime() / 1000) * 1000;
}

export function zonedWallTimeToUtc(dateKey, time, timeZone = currentTimeZone()) {
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

export function formatClock(value) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(currentLocale(), {
    hour: "numeric",
    minute: "2-digit",
    hour12: state.preferences?.timeFormat !== "24h",
    timeZone: currentTimeZone()
  }).format(date);
}

export function formatListWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const key = dateKeyInZone(date);
  const today = dateKeyInZone(new Date());
  if (key === today) return formatClock(date);
  if (key === addDaysToKey(today, -1)) return t("format.yesterday");
  const todayParts = keyToParts(today);
  const dateParts = keyToParts(key);
  const dayDiff = Math.round((Date.UTC(todayParts.year, todayParts.month - 1, todayParts.day) - Date.UTC(dateParts.year, dateParts.month - 1, dateParts.day)) / 86400000);
  if (dayDiff >= 0 && dayDiff < 7) return new Intl.DateTimeFormat(currentLocale(), { weekday: "short", timeZone: currentTimeZone() }).format(date);
  if (dateParts.year === todayParts.year) return new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", year: "numeric", timeZone: currentTimeZone() }).format(date);
}

export function formatDateOnly(value) {
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

export function formatDate(value, { time = false } = {}) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const dateText = new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", year: "numeric", timeZone: currentTimeZone() }).format(date);
  return time ? `${dateText} · ${formatClock(date)}` : dateText;
}

export function formatFullWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const key = dateKeyInZone(date);
  const today = dateKeyInZone(new Date());
  if (key === today) return t("format.today-at", { time: formatClock(date) });
  if (key === addDaysToKey(today, -1)) return t("format.yesterday-at", { time: formatClock(date) });
  const dateText = new Intl.DateTimeFormat(currentLocale(), { month: "long", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return t("format.date-at", { date: dateText, time: formatClock(date) });
}

export function formatAuditWhen(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const dateText = new Intl.DateTimeFormat(currentLocale(), { month: "short", day: "numeric", timeZone: currentTimeZone() }).format(date);
  return `${dateText}, ${formatClock(date)}`;
}

export function formatDateTimeFull(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const dateText = new Intl.DateTimeFormat(currentLocale(), {
    weekday: "short", month: "long", day: "numeric", year: "numeric",
    timeZone: currentTimeZone()
  }).format(date);
  return `${dateText} · ${formatClock(date)} ${formatTimeZoneShort(currentTimeZone(), date)}`;
}

export function formatTimeZoneShort(timeZone = currentTimeZone(), date = new Date()) {
  try {
    const part = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "short" }).formatToParts(date).find((item) => item.type === "timeZoneName");
    return part ? part.value : timeZone;
  } catch {
    return timeZone;
  }
}

export function formatTimeZoneLabel(timeZone = currentTimeZone(), date = new Date()) {
  const offset = timeZoneOffsetMs(date, timeZone);
  const sign = offset >= 0 ? "+" : "-";
  const minutes = Math.abs(Math.round(offset / 60000));
  return `${timeZone.replace(/_/g, " ")} · UTC${sign}${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")} · ${formatTimeZoneShort(timeZone, date)}`;
}

export function formatRelativeTime(value) {
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

export function startOfDay(date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

export function parseDate(value) {
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function formatCountdown(ms) {
  return `${Math.max(0, Math.ceil(ms / 1000))}s`;
}
