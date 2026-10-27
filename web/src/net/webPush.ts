// SPDX-License-Identifier: AGPL-3.0-or-later

import { useWS } from "./useWS";
import { t } from "../i18n/runtime";

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
        throw new Error(t("web.shell.connection.no_push_key"));
      }
      return reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(resp.payload.respVapidPublicKey.key),
      });
    })());

  await registerSubscription(sub);
  return true;
}

async function registerSubscription(sub: PushSubscription): Promise<void> {
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
}

/** Sends this browser's subscription to the device again, if it has one:
 * at each sign-in and whenever the language the page shows changes, so
 * the device writes this browser's pushes in that language (ws.ts puts it
 * on the request as lang; docs/i18n.md, last_ui_language). Never asks for
 * permission or subscribes. Best effort. */
export async function reregisterPush(): Promise<void> {
  if (!pushSupported() || typeof Notification === "undefined" || Notification.permission !== "granted") return;
  try {
    const reg = await navigator.serviceWorker.getRegistration("/sw.js");
    const sub = await reg?.pushManager.getSubscription();
    if (sub) await registerSubscription(sub);
  } catch (err) {
    console.error("Could not register push again:", err);
  }
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

/** Issue #131: tells the device to forget this browser's subscription
 * (and, through the device, the bridge), then drops it locally. For Sign
 * Out, where the lazy 404/410 cleanup above would never get its chance:
 * the browser is about to forget the device, so the device must forget
 * the browser now. Best effort - a device that never had it says ok too. */
export async function unregisterPushOnSignOut(): Promise<void> {
  if (!pushSupported()) return;
  const reg = await navigator.serviceWorker.getRegistration("/sw.js");
  const sub = await reg?.pushManager.getSubscription();
  if (!sub) return;
  try {
    await useWS.request((e) => {
      (e as any).payload = { $case: "reqUnregisterWebPush", reqUnregisterWebPush: { endpoint: sub.endpoint } };
    });
  } catch (err) {
    console.error("Could not unregister push on sign out:", err);
  }
  await sub.unsubscribe();
}

/** Whether this browser can have notifications and hasn't been asked to
 *  (or closed the question unanswered): the feed's card offers them then
 *  (PushCard). Asked only from a click: Safari refuses a question no click
 *  asked for. */
export function pushUndecided(): boolean {
  return pushSupported() && typeof Notification !== "undefined" && Notification.permission === "default";
}
