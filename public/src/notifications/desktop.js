/* Desktop notification and sound helpers. */
import { state } from "../core/state.js";

export function showBrowserNotification(title, body, tag = "a8m-new-mail") {
  if (!state.notifyDesktop || !("Notification" in window) || Notification.permission !== "granted") return false;
  try {
    const notification = new Notification(title, { body, tag, icon: "/icons/icon-192.png", badge: "/icons/icon-192.png" });
    notification.onclick = () => { window.focus(); notification.close(); };
    return true;
  } catch { return false; }
}

export function playNotificationTone() {
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
