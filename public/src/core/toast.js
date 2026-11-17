/* Toast and inline error surface. */
import { toastEl } from "./dom.js";
import { formatCountdown } from "./format.js";
import { t } from "../../i18n.mjs";

let toastTimer = null;
let toastInterval = null;

export function hideToast() {
  clearTimeout(toastTimer);
  clearInterval(toastInterval);
  toastInterval = null;
  toastEl.classList.remove("is-visible");
}

export function showToast(message, actionLabel, action, options = {}) {
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
      countdown.textContent = remaining > 0 ? formatCountdown(remaining) : t("toast.sent");
      if (remaining <= 0 && toastInterval) {
        clearInterval(toastInterval);
        toastInterval = null;
        text.textContent = options.sentMessage || t("toast.message-sent");
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
      try { await action(); } catch (error) { showToast(error.message || t("toast.action-failed")); }
      hideToast();
    }, { once: true });
    toastEl.appendChild(button);
  }
  toastEl.classList.add("is-visible");
  const duration = options.duration != null ? options.duration : (actionLabel ? 10000 : 3200);
  toastTimer = setTimeout(hideToast, duration);
}

export function showError(error) {
  const detail = error && error.details && error.details.errors ? ` ${error.details.errors.join(" · ")}` : "";
  let message = error && error.message ? error.message : t("common.error");
  if (error && error.code) {
    const key = `errors.${error.code}`;
    const translated = t(key, error.params || {});
    if (translated !== key) message = translated;
  }
  showToast(message + detail);
}
