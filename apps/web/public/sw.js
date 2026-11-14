// Crewbase service worker: makes the app installable and opens it from
// notifications. It caches only the app shell (HTML, JS, CSS, icons) —
// never API responses, so nothing private is stored by it.
const SHELL = "crewbase-shell-v1";

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== SHELL).map((k) => caches.delete(k)))).then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const u = new URL(e.request.url);
  if (e.request.method !== "GET" || u.origin !== location.origin) return;
  if (u.pathname.startsWith("/api/") || u.pathname.startsWith("/v1/")) return; // never cache the API or the relay
  // network first; the cached shell only when offline
  e.respondWith(
    fetch(e.request)
      .then((r) => {
        if (r.ok && (u.pathname === "/" || u.pathname.startsWith("/assets/") || /\.(png|svg|webmanifest)$/.test(u.pathname))) {
          const copy = r.clone();
          caches.open(SHELL).then((c) => c.put(e.request.mode === "navigate" ? "/" : e.request, copy));
        }
        return r;
      })
      .catch(() => caches.match(e.request.mode === "navigate" ? "/" : e.request).then((r) => r || Response.error())),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.url) || "/";
  e.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((cs) => {
      for (const c of cs) {
        if ("focus" in c) {
          c.postMessage({ type: "open", url });
          return c.focus();
        }
      }
      return self.clients.openWindow(url);
    }),
  );
});
