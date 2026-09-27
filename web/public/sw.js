// Service worker for the /app/ dashboards. Keeps the shell available offline
// without ever touching the API: only navigations and the app's own built
// assets are cached.
// Stamped per build by scripts/stamp-sw.ts; 'app-dev' only outside a build.
const CACHE = 'app-dev'
const SHELL_URLS = ['/app/', '/app/index.html']

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(SHELL_URLS)),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))),
      ),
  )
})

self.addEventListener('fetch', (event) => {
  const { request } = event
  const url = new URL(request.url)

  // Never intercept anything outside /app/, and never the API.
  if (!url.pathname.startsWith('/app/') || url.pathname.startsWith('/api/')) {
    return
  }

  // Navigations: network-first, falling back to the cached shell so the app
  // still opens offline (the client-side router takes it from there).
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request).catch(() => caches.match('/app/index.html')),
    )
    return
  }

  // Built assets: cache-first, filling the cache on first fetch. Only a
  // 2xx is kept: a 404 for an asset of another release would otherwise be
  // served from the cache forever, its name never changing.
  if (url.pathname.startsWith('/app/assets/')) {
    event.respondWith(
      caches.match(request).then(
        (cached) =>
          cached ||
          fetch(request).then((response) => {
            if (!response.ok) return response
            const copy = response.clone()
            caches.open(CACHE).then((cache) => cache.put(request, copy))
            return response
          }),
      ),
    )
  }
})
