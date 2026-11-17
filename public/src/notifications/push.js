/* Web Push subscription, settings UI and calendar reminders. */
import { api } from "../core/api.js";
import { state } from "../core/state.js";
import { showError, showToast } from "../core/toast.js";
import { showBrowserNotification } from "./desktop.js";
import { t, tn } from "../../i18n.mjs";

export function pushSupported() {
  return Boolean(window.isSecureContext) && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

export function pushKeyBytes(base64String) {
  const value = String(base64String || "").replace(/-/g, "+").replace(/_/g, "/");
  const padded = value + "=".repeat((4 - (value.length % 4)) % 4);
  const raw = atob(padded);
  return Uint8Array.from(raw, (character) => character.charCodeAt(0));
}

export async function pushServiceWorker() {
  if (!pushSupported()) return null;
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise((_, reject) => setTimeout(() => reject(new Error(t("push.service-worker-not-ready"))), 8000))
  ]);
}

export function pushSubscriptionMatchesKey(subscription) {
  try {
    const existing = subscription.options && subscription.options.applicationServerKey;
    if (!existing) return false;
    const current = pushKeyBytes(state.push && state.push.publicKey);
    const installed = new Uint8Array(existing);
    if (installed.length !== current.length) return false;
    return installed.every((byte, index) => byte === current[index]);
  } catch {
    return false;
  }
}

export async function refreshPushLocalState() {
  let local = false;
  if (pushSupported() && state.push && state.push.enabled) {
    try {
      const registration = await pushServiceWorker();
      const subscription = registration ? await registration.pushManager.getSubscription() : null;
      local = Boolean(subscription && pushSubscriptionMatchesKey(subscription));
    } catch {}
  }
  state.pushLocal = local;
  syncPushUi();
  return local;
}

export function syncPushUi() {
  const enableButton = document.getElementById("pushEnableBtn");
  if (!enableButton) return;
  const testButton = document.getElementById("pushTestBtn");
  const disableButton = document.getElementById("pushDisableBtn");
  const statusText = document.getElementById("pushStatusText");
  const statusBadge = document.getElementById("pushStatusBadge");
  const hint = document.getElementById("pushHint");
  const config = state.push || {};
  const supported = pushSupported();
  const configured = supported && Boolean(config.enabled && config.publicKey);
  const permission = "Notification" in window ? Notification.permission : "denied";
  const active = configured && Boolean(state.pushLocal);
  let status = t("push.status.checking");
  let badge = t("push.badge.checking");
  let note = t("push.note.background-alerts");
  if (!supported) {
    status = window.isSecureContext
      ? t("push.status.unsupported")
      : t("push.status.https-required");
    badge = t("push.badge.unsupported");
    note = window.isSecureContext
      ? t("push.note.install-app")
      : t("push.note.open-https");
  } else if (!configured) {
    status = t("push.status.disabled");
    badge = t("push.badge.off");
    note = t("push.note.in-app");
  } else if (permission === "denied") {
    status = t("push.status.blocked");
    badge = t("push.badge.blocked");
    note = t("push.note.allow-notifications");
  } else if (active) {
    status = t("push.status.active");
    badge = t("push.badge.active");
    note = config.includeSender !== false || config.includeSubject !== false
      ? t("push.note.details-included")
      : t("push.note.details-hidden");
  } else {
    status = t("push.status.available");
    badge = t("push.badge.available");
  }
  if (state.pushError) {
    status = state.pushError;
    badge = t("push.badge.issue");
    note = t("push.note.fix-condition");
  }
  if (statusText) statusText.textContent = status;
  if (statusBadge) statusBadge.textContent = badge;
  if (hint) hint.textContent = note;
  enableButton.hidden = active;
  enableButton.disabled = !configured || permission === "denied" || state.pushBusy;
  enableButton.textContent = state.pushBusy ? t("push.enabling") : t("push.enable-device");
  if (testButton) {
    testButton.hidden = !active;
    testButton.disabled = state.pushBusy;
  }
  if (disableButton) {
    disableButton.hidden = !active;
    disableButton.disabled = state.pushBusy;
  }
}

export async function maybeAutoRegisterPush() {
  if (state.pushAutoChecked) return;
  state.pushAutoChecked = true;
  if (!pushSupported() || !state.notifyDesktop) return;
  if (!state.push || !state.push.enabled || !state.push.publicKey) return;
  if (Notification.permission !== "granted") return;
  await registerPushSubscription({ quiet: true });
}

export function pushErrorMessage(error) {
  const code = error && error.code;
  if (code === "PUSH_OWNED") return t("push.error.owned");
  if (code === "PUSH_DISABLED") return t("push.error.disabled");
  if (code === "PUSH_KEY") return t("push.error.key");
  if (code === "PUSH_NO_SUBSCRIPTION") return t("push.error.no-subscription");
  if (code === "PUSH_DELIVERY") return error.message || t("push.error.delivery");
  if (error && error.name === "NotAllowedError") return t("push.error.blocked");
  if (error && error.message) return error.message;
  return t("push.error.generic");
}

export async function registerPushSubscription({ prompt = false, quiet = false, manageBusy = true } = {}) {
  if (!pushSupported()) {
    if (!quiet) showToast(t("push.toast.unsupported"));
    syncPushUi();
    return false;
  }
  const config = state.push || {};
  if (!config.enabled || !config.publicKey) {
    state.pushError = t("push.status.disabled");
    if (!quiet) showToast(t("push.toast.disabled"));
    syncPushUi();
    return false;
  }
  if (manageBusy) {
    if (state.pushBusy) return false;
    state.pushBusy = true;
    syncPushUi();
  }
  try {
    let permission = Notification.permission;
    if (permission === "default" && prompt) permission = await Notification.requestPermission();
    if (permission !== "granted") {
      state.pushError = t("push.status.blocked");
      if (!quiet) showToast(t("push.toast.allow-notifications"));
      return false;
    }
    const registration = await pushServiceWorker();
    if (!registration) throw new Error(t("push.service-worker-not-ready"));
    let subscription = await registration.pushManager.getSubscription();
    if (subscription && !pushSubscriptionMatchesKey(subscription)) {
      try { await subscription.unsubscribe(); } catch {}
      subscription = null;
    }
    if (!subscription) {
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: pushKeyBytes(config.publicKey)
      });
    }
    const submit = () => api("/api/push/subscribe", { method: "POST", body: subscription.toJSON() });
    try {
      await submit();
    } catch (error) {
      if (!error || error.code !== "PUSH_OWNED") throw error;
      // The push service endpoint is still tied to a previous Alt8Mail account.
      // Drop it and obtain a fresh endpoint before retrying once.
      try { await subscription.unsubscribe(); } catch {}
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: pushKeyBytes(config.publicKey)
      });
      await submit();
    }
    state.pushError = "";
    state.pushLocal = true;
    state.push = { ...config, subscribed: true };
    if (!quiet) showToast(t("push.toast.enabled"));
    return true;
  } catch (error) {
    state.pushLocal = false;
    state.pushError = pushErrorMessage(error);
    if (!quiet) showError(error);
    return false;
  } finally {
    if (manageBusy) {
      state.pushBusy = false;
      syncPushUi();
    }
  }
}

export async function unregisterPushSubscription({ silent = false } = {}) {
  if (!pushSupported()) {
    state.pushLocal = false;
    syncPushUi();
    return false;
  }
  try {
    const registration = await pushServiceWorker();
    const subscription = registration ? await registration.pushManager.getSubscription() : null;
    if (subscription) {
      await api("/api/push/subscribe", { method: "DELETE", body: { endpoint: subscription.endpoint } }).catch(() => {});
      await subscription.unsubscribe().catch(() => {});
    }
    state.pushLocal = false;
    state.pushError = "";
    if (state.push) state.push = { ...state.push, subscribed: false };
    if (!silent) showToast(t("push.toast.disabled-device"));
    return true;
  } catch (error) {
    if (!silent) showError(error);
    return false;
  } finally {
    syncPushUi();
  }
}

export async function sendPushTestNotification() {
  if (state.pushBusy) return;
  state.pushBusy = true;
  state.pushError = "";
  syncPushUi();
  try {
    // Make sure the server still has this browser's current endpoint. The
    // subscription can be rotated or removed while the page stays open.
    if (state.pushLocal) {
      const registered = await registerPushSubscription({ quiet: true, manageBusy: false });
      if (!registered) {
        showToast(state.pushError || t("push.toast.not-registered"));
        return;
      }
    }
    const result = await api("/api/push/test", { method: "POST" });
    const sent = Number(result && result.sent) || 0;
    const failed = Number(result && result.failed) || 0;
    const lastError = result && result.lastError ? String(result.lastError) : "";
    if (sent > 0) {
      const extra = failed > 0 ? tn("push.test.other-browsers-failed", failed, { count: failed }) : "";
      showToast(tn("push.test.sent", sent, { count: sent, extra }));
    } else {
      state.pushError = lastError
        ? t("push.test.gateway-rejected", { error: lastError })
        : t("push.test.no-browser");
      showToast(state.pushError);
    }
  } catch (error) {
    state.pushError = pushErrorMessage(error);
    showError(error);
  } finally {
    state.pushBusy = false;
    syncPushUi();
  }
}

export async function refreshPushConfig() {
  if (!state.user) return;
  try {
    const data = await api("/api/push/config");
    if (data) state.push = { ...(state.push || {}), ...data };
    await refreshPushLocalState();
    if (data && data.enabled && state.notifyDesktop && pushSupported() && !data.subscribed && Notification.permission === "granted") {
      registerPushSubscription({ quiet: true }).catch(() => {});
    }
  } catch {}
}

export async function checkCalendarReminders() {
  if (!state.notifyCalendar || document.visibilityState !== "visible") return;
  const now = Date.now();
  const today = dateKey(new Date());
  const tomorrow = addDaysToKey(today, 1);
  for (const event of calendarOccurrences(today, tomorrow, { all: true })) {
    if (event.allDay || event.status === "cancelled" || event.myStatus === "declined") continue;
    const minutes = Number.isFinite(Number(event.reminderMinutes)) ? Number(event.reminderMinutes) : 15;
    if (minutes <= 0) continue;
    const key = `${event.id}|${event.occurrenceDate}|${minutes}`;
    if (state.remindedEventIds.has(key)) continue;
    const start = zonedWallTimeToUtc(event.occurrenceDate, event.start);
    if (!start) continue;
    const diff = start.getTime() - now;
    if (diff <= 0 || diff > minutes * 60 * 1000) continue;
    state.remindedEventIds.add(key);
    const label = minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} hour${minutes === 60 ? "" : "s"}` : `${minutes} minutes`;
    const body = `${event.start} · ${event.where || t("push.calendar-fallback")}`;
    showBrowserNotification(t("push.event-in", { in: label }), `${event.title} — ${body}`, "a8m-calendar-" + key);
  }
}
