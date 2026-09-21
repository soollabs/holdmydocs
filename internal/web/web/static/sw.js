// __ASSET_VERSION__ is substituted with the build's content hash when the
// service worker is served. It names the cache and versions every precached
// URL, so a new build installs a fresh cache and old entries are dropped.
const VERSION = '__ASSET_VERSION__';
const CACHE = 'hmd-static-' + VERSION;
const FILES = ['style.css', 'skins.css', 'app.js', 'page.js', 'editor.js', 'toc.js', 'sidebar.js', 'manifest.json', 'fonts/JetBrainsMono-Regular.woff2', 'fonts/JetBrainsMono-Bold.woff2'];
const ASSETS = FILES.map(name => '/_/static/' + name + '?v=' + VERSION);

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
