self.addEventListener("push", (event) => {
  const data = event.data ? event.data.json() : {};
  const title = data.title || "Off The Cloud";
  const body = data.body || "New activity from a friend";
  event.waitUntil(self.registration.showNotification(title, { body }));
});
