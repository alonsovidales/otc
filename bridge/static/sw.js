// Service worker for Web Push (issue #43). Deliberately minimal — this app
// has no offline/caching story, so the only two events handled are the ones
// push notifications actually need.

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    // Not JSON — fall back to defaults below rather than dropping the push.
  }
  const title = data.title || "Off The Cloud";
  const body = data.body || "New activity from a friend";
  event.waitUntil(self.registration.showNotification(title, { body }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    clients.matchAll({ type: "window" }).then((matched) => {
      const existing = matched.find((c) => "focus" in c);
      if (existing) return existing.focus();
      if (clients.openWindow) return clients.openWindow("/");
    })
  );
});
