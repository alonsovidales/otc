// SPDX-License-Identifier: AGPL-3.0-or-later

// Screen mode: the web app on a screen plugged into the device itself. The
// device opens it in a kiosk browser (cage + Chromium, no address bar, no
// back button, no tabs) at http://127.0.0.1:<port>/?screen=1, on a touch
// screen with no keyboard. Then:
//   - the on-screen keyboard comes up in every text field
//     (ScreenKeyboard.tsx);
//   - nothing leaves the device's own pages or opens a second window: in
//     the kiosk a new window covers the page for good, and another site's
//     page has no way back. A link to another site, a download or a file
//     to pick says where to do it instead (screenNotice);
//   - controls that can only download (DownloadButton) aren't shown.
// The kiosk's browser refuses downloads and file choosers by itself too
// (its policies, scripts/screen-runner): this is so that a press says why.
//
// ?screen=1 turns it on, ?screen=0 off; it lasts for the tab
// (sessionStorage), so a reload keeps it, and the kiosk opens a new tab
// with ?screen=1 whenever it starts.

const cStorageKey = "otc_screen";

/** Whether a page at search (location.search) is in screen mode, stored
 *  being what the tab kept ("1", or null). */
export function screenModeFrom(search: string, stored: string | null): boolean {
  const asked = new URLSearchParams(search).get("screen");
  if (asked === "1") return true;
  if (asked === "0") return false;
  return stored === "1";
}

function readMode(): boolean {
  if (typeof window === "undefined") return false;
  let stored: string | null = null;
  try {
    stored = window.sessionStorage.getItem(cStorageKey);
  } catch {
    // Storage refused: the address alone decides.
  }
  const on = screenModeFrom(window.location.search, stored);
  try {
    if (on) window.sessionStorage.setItem(cStorageKey, "1");
    else window.sessionStorage.removeItem(cStorageKey);
  } catch {
    // Kept for this page only.
  }
  return on;
}

const cOn = readMode();

/** Whether this page is on the device's own screen. Decided once, at load. */
export function screenMode(): boolean {
  return cOn;
}

/** What a press asked for that this screen can't do. */
export type ScreenNoticeKind = "elsewhere" | "download" | "upload";

type NoticeListener = (kind: ScreenNoticeKind) => void;
const noticeListeners = new Set<NoticeListener>();

/** Says (ScreenLayer) that kind is for a phone or a computer. */
export function screenNotice(kind: ScreenNoticeKind): void {
  for (const fn of noticeListeners) fn(kind);
}

export function subscribeScreenNotice(fn: NoticeListener): () => void {
  noticeListeners.add(fn);
  return () => { noticeListeners.delete(fn); };
}

/** What screen mode does with a press that would open href:
 *  "follow" lets it go, "here" opens it in this tab instead of a new one,
 *  otherwise the notice of that kind. newWindow: it would open a window
 *  (target=_blank, window.open); download: an <a download>. Only the app's
 *  own pages open in this tab (the app, a shared gallery's page): anything
 *  else on the device (a file, an archive) has no way back from it. */
export function screenNavigation(
  href: string,
  origin: string,
  { newWindow, download }: { newWindow: boolean; download: boolean },
): "follow" | "here" | ScreenNoticeKind {
  let url: URL;
  try {
    url = new URL(href, origin + "/");
  } catch {
    return "elsewhere";
  }
  if (url.protocol === "javascript:") return "follow";
  if (url.protocol === "blob:" || url.protocol === "data:") return "download";
  // Another site's page, a mail or phone link (origin "null").
  if (url.origin !== origin) return "elsewhere";
  if (download) return "download";
  const appPage = url.pathname === "/" || url.pathname === "/index.html" || url.pathname === "/shared";
  if (!newWindow) return appPage ? "follow" : "download";
  return appPage ? "here" : "download";
}

let guarded = false;

/** Keeps the page on the device's own pages (see the top): links, window.open
 *  and file choosers. Called once by the entries, in screen mode only. */
export function installScreenGuards(): void {
  if (guarded || typeof window === "undefined") return;
  guarded = true;
  const origin = window.location.origin;

  // Captured first, so the app's own handlers never see a press that
  // doesn't happen. Clicks the app makes itself (a.click() on an anchor it
  // made, input.click() on a hidden file field) come through here too.
  document.addEventListener("click", (e) => {
    const el = e.target instanceof Element ? e.target : null;
    if (!el) return;
    if (el instanceof HTMLInputElement && el.type === "file") {
      e.preventDefault();
      screenNotice("upload");
      return;
    }
    const a = el.closest("a[href]");
    if (!(a instanceof HTMLAnchorElement)) return;
    const target = a.target.toLowerCase();
    const what = screenNavigation(a.href, origin, {
      newWindow: target !== "" && target !== "_self" && target !== "_top" && target !== "_parent",
      download: a.hasAttribute("download"),
    });
    if (what === "follow") return;
    e.preventDefault();
    if (what === "here") window.location.assign(a.href);
    else screenNotice(what);
  }, true);

  // No second window, ever: null is a blocked popup, which every caller
  // already handles. A blank one (a tab opened at a press, to fill once an
  // answer comes) is simply not opened.
  const nativeOpen = window.open.bind(window);
  window.open = (url?: string | URL, target?: string, features?: string): Window | null => {
    const href = url === undefined ? "" : String(url);
    const name = (target ?? "_blank").toLowerCase();
    if (name === "_self" || name === "_top" || name === "_parent") return nativeOpen(url, target, features);
    if (href === "" || href === "about:blank") return null;
    const what = screenNavigation(href, origin, { newWindow: true, download: false });
    if (what === "here" || what === "follow") window.location.assign(new URL(href, origin + "/").href);
    else screenNotice(what);
    return null;
  };
}
