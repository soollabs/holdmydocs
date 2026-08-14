// caches static assets only, not auth-gated page content —
// upgrade to a page cache if full offline browsing of wiki pages is needed.
const CACHE = 'hmd-static-v3';
const ASSETS = ['/_/static/style.css?v=3', '/_/static/skins.css?v=2', '/_/static/app.js?v=2', '/_/static/editor.js?v=2', '/_/static/manifest.json'];

self.addEventListener('install', e => {
  e.waitUntil(caches.open(CACHE).then(c => c.addAll(ASSETS)));
  self.skipWaiting();
});

self.addEventListener('activate', e => {
  e.waitUntil(
    caches.keys().then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k))))
  );
  self.clients.claim();
});

self.addEventListener('fetch', e => {
  const url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.pathname.indexOf('/_/static/') !== 0) return;
  e.respondWith(
    fetch(e.request).then(response => {
      if (response.ok) {
        const copy = response.clone();
        e.waitUntil(caches.open(CACHE).then(cache => cache.put(e.request, copy)));
      }
      return response;
    }).catch(() => caches.match(e.request))
  );
});
