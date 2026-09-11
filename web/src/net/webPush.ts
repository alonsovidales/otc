import { useWS } from "./useWS";

// Issue #43: browser push notifications for new posts from friends. Fully
// self-hosted — the device generates its own VAPID keypair (see
// push.Init on the Go side), so this needs no third-party push provider
// account, just the browser's own built-in push service.

/** PushManager wants the VAPID public key as a raw Uint8Array, not the
 * base64url string the server hands out. */
function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window;
}

/** Whether this browser already has an active push subscription — used to
 * show "Notifications on" vs. an enable button in Settings. */
export async function isPushSubscribed(): Promise<boolean> {
  if (!pushSupported()) return false;
  const reg = await navigator.serviceWorker.getRegistration("/sw.js");
  if (!reg) return false;
  return !!(await reg.pushManager.getSubscription());
}

/** Registers the service worker, asks for notification permission, and
 * subscribes to push — mirroring the mobile app's APNs device-token
 * registration (see OTCConnection+push on iOS), just with the browser's
 * own PushManager instead of UIApplication.registerForRemoteNotifications. */
export async function enablePush(): Promise<boolean> {
  if (!pushSupported()) return false;

  const permission = await Notification.requestPermission();
  if (permission !== "granted") return false;

  const reg = await navigator.serviceWorker.register("/sw.js");
  await navigator.serviceWorker.ready;

  const existing = await reg.pushManager.getSubscription();
  const sub =
    existing ??
    (await (async () => {
      const resp = await useWS.request((e) => {
        (e as any).payload = { $case: "reqGetVapidPublicKey", reqGetVapidPublicKey: {} };
      });
      if (resp.payload?.$case !== "respVapidPublicKey" || !resp.payload.respVapidPublicKey.key) {
        throw new Error("Device has no VAPID public key configured");
      }
      return reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(resp.payload.respVapidPublicKey.key),
      });
    })());

  const json = sub.toJSON();
  await useWS.request((e) => {
    (e as any).payload = {
      $case: "reqRegisterWebPush",
      reqRegisterWebPush: {
        endpoint: json.endpoint ?? "",
        p256dh: json.keys?.p256dh ?? "",
        auth: json.keys?.auth ?? "",
      },
    };
  });

  return true;
}

export async function disablePush(): Promise<void> {
  if (!pushSupported()) return;
  const reg = await navigator.serviceWorker.getRegistration("/sw.js");
  const sub = await reg?.pushManager.getSubscription();
  await sub?.unsubscribe();
  // The device drops it lazily too (a 404/410 on next send, see
  // push.sendWebPush), but there's no need to wait for that — nothing else
  // reads this subscription server-side until then anyway.
}

// Ask once, proactively, right after sign-in, rather than requiring the
// user to find "Enable Notifications" in Settings themselves — the
// browser's native Allow/Block prompt still can't be skipped (no API does
// that, by design), but at least the ask itself doesn't have to be
// buried. "Once" here means once ever per browser, not once per session:
// if the user already granted, denied, or was already asked and dismissed
// it, Notification.permission is no longer "default" and this is a no-op.
const cPromptedStorageKey = "otc_push_prompted";

export function promptForPushIfNeverAsked(): void {
  if (!pushSupported() || typeof Notification === "undefined") return;
  if (Notification.permission !== "default") return;
  try {
    if (localStorage.getItem(cPromptedStorageKey)) return;
    localStorage.setItem(cPromptedStorageKey, "1");
  } catch {
    // No localStorage — fall through and ask anyway rather than never
    // asking; worst case is one repeat prompt per page load in that
    // (rare) environment, not silence forever.
  }
  void enablePush();
}
