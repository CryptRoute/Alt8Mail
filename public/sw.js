/* Alt8Mail — offline app shell.
   API calls are always network-only; mailbox data is never cached here. */
const VERSION = "2026.10.30-9";
const CACHE = `a8m-shell-${VERSION}`;
const SHELL = [
  "/",
  "/styles.css?v=0.11.6",
  "/features.css?v=0.11.6",
  "/workspace.css?v=0.11.6",
  "/features-0.7.css?v=0.11.6",
  "/onboarding.css?v=0.11.6",
  "/email-ui.css?v=0.11.6",
  "/vendor/typo.js",
  "/vendor/en_US.aff",
  "/vendor/en_US.dic",
  "/app.js?v=0.11.6",
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

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;

  // The mailbox API, auth and draft autosave are never served from cache.
  if (url.pathname.startsWith("/api/")) return;

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
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
