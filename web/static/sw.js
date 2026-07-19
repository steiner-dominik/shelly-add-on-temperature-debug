"use strict";
// Service worker: offline app shell so the page installs as a PWA. The cache
// name carries the release version (substituted at server start like in
// index.html), so every release invalidates the previous cache cleanly.
// Live data (/api/, /metrics, /healthz) is never cached — a temperature
// debug tool must not show stale readings as if they were fresh.
const CACHE = "shelly-debug-__VERSION__";
const SHELL = [
  "./", "app.css", "app.js", "favicon.svg", "manifest.webmanifest",
  "icon-192.png", "icon-512.png", "icon-light-192.png", "icon-light-512.png",
  "icon-maskable.png", "locales/index.json",
];

self.addEventListener("install", e => {
  e.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", e => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", e => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.origin !== self.location.origin) return;
  if (/\/(api\/|metrics$|healthz$)/.test(url.pathname)) return; // network only
  // Network first (assets are tiny and should be fresh), cache as fallback
  // so the shell still opens without a connection.
  e.respondWith(
    fetch(e.request).then(resp => {
      if (resp.ok) {
        const copy = resp.clone();
        caches.open(CACHE).then(c => c.put(e.request, copy));
      }
      return resp;
    }).catch(() => caches.match(e.request))
  );
});
