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
  // The device sends every push with its title and body already written in
  // the user's language (push.Text). These are only for a push that
  // somehow came without them: a service worker can't read the page's
  // texts, so they stay English.
  const title = data.title || "Off The Cloud";
  const body = data.body || "New activity from a friend"; // i18n-ignore: fallback for a push without a body; the device writes it in the user's language
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
