// nessy playground service worker (#10) — precache the shell so the
// playground loads offline after the first visit. Bump CACHE when any
// shell asset changes so clients pick up the new version.
'use strict';

const CACHE = 'nessy-v1';
const ASSETS = [
  '.',
  'index.html',
  'app.js',
  'wasm_exec.js',
  'nessy.wasm',
  'manifest.webmanifest',
  'icon.svg',
];

// Precache best-effort: a missing asset (e.g. wasm_exec.js not yet copied
// in a dev checkout) must not fail the whole install.
self.addEventListener('install', (e) => {
  e.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    await Promise.all(ASSETS.map((a) => cache.add(a).catch(() => {})));
    self.skipWaiting();
  })());
});

// Drop old cache versions on activate.
self.addEventListener('activate', (e) => {
  e.waitUntil((async () => {
    const keys = await caches.keys();
    await Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)));
    self.clients.claim();
  })());
});

// Cache-first for same-origin GETs, falling back to network (and caching
// the result). Cross-origin + non-GET pass straight through.
self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET' || new URL(req.url).origin !== self.location.origin) return;
  e.respondWith((async () => {
    const cached = await caches.match(req);
    if (cached) return cached;
    try {
      const resp = await fetch(req);
      if (resp.ok) {
        const cache = await caches.open(CACHE);
        cache.put(req, resp.clone());
      }
      return resp;
    } catch (err) {
      return cached || Response.error();
    }
  })());
});
