/* Alt8Mail — offline app shell.
   API calls are always network-only; mailbox data is never cached here. */
const VERSION = "2026.11.04-02";
const CACHE = `a8m-shell-${VERSION}`;
const SHELL = [
  "/",
  "/styles.css?v=0.20.20",
  "/features.css?v=0.20.20",
  "/workspace.css?v=0.20.20",
  "/cabinet-calendar.css?v=0.20.20",
  "/features-0.7.css?v=0.20.20",
  "/onboarding.css?v=0.20.20",
  "/email-ui.css?v=0.20.20",
  "/vendor/typo.js",
  "/vendor/qrcode.min.js",
  "/vendor/en_US.aff",
  "/vendor/en_US.dic",
  "/app.js?v=0.20.20",
  "/search.mjs",
  "/manifest.webmanifest",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/icon-maskable-512.png",
  "/icons/apple-touch-icon.png"
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE)
      .then((cache) => cache.addAll(SHELL))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => {
        const hadOld = keys.some((key) => key !== CACHE);
        return Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)))
          .then(() => self.clients.claim())
          .then(() => self.clients.matchAll({ type: "window" }))
          .then((clients) => {
            if (hadOld) for (const client of clients) client.postMessage({ type: "SW_UPDATED", version: VERSION });
          });
      })
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting();
});

// Web Push. The server only sends a push when a message is new; if a visible
// app window is already on screen the in-app notifier owns the alert so the
// user never sees the same message twice.
self.addEventListener("push", (event) => {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch {
    payload = { body: event.data ? event.data.text() : "" };
  }
  const title = payload.title || "New Alt8Mail message";
  const body = payload.body || "Open Alt8Mail to read the new message.";
  const tag = payload.tag || "a8m-new-mail";
  const target = payload.url || "/";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      if (!payload.force && clients.some((client) => client.visibilityState === "visible")) return undefined;
      return self.registration.showNotification(title, {
        body,
        tag,
        icon: payload.icon || "/icons/icon-192.png",
        badge: payload.badge || "/icons/icon-192.png",
        timestamp: Number(payload.timestamp) || Date.now(),
        data: { url: target },
        renotify: Boolean(payload.force)
      }).catch(() => undefined);
    }).catch(() => undefined)
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  let target = "/";
  try {
    const raw = event.notification.data && event.notification.data.url;
    if (raw) target = new URL(raw, self.location.origin).href;
  } catch {}
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if (!client.url.startsWith(self.location.origin)) continue;
        if ("navigate" in client) return client.navigate(target).then(() => client.focus()).catch(() => client.focus());
        return client.focus();
      }
      return self.clients.openWindow(target);
    }).catch(() => undefined)
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;

  // The mailbox API, auth and draft autosave are never served from cache.
  if (url.pathname.startsWith("/api/")) return;

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request, { cache: "no-store" })
        .then((response) => {
          if (response && response.ok) {
            const copy = response.clone();
            caches.open(CACHE).then((cache) => cache.put("/", copy)).catch(() => {});
          }
          return response;
        })
        .catch(() => caches.match("/").then((cached) => cached || new Response("Offline", { status: 503, headers: { "Content-Type": "text/plain" } })))
    );
    return;
  }

  event.respondWith(
    caches.match(request).then((cached) => {
      const network = fetch(request)
        .then((response) => {
          if (response && response.ok) {
            const copy = response.clone();
            caches.open(CACHE).then((cache) => cache.put(request, copy)).catch(() => {});
          }
          return response;
        })
        .catch(() => cached);
      return cached || network;
    })
  );
});
