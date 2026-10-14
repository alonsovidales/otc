// SPDX-License-Identifier: AGPL-3.0-or-later

// The device serves this web app, so after the device updates, a tab
// opened before keeps running the old app until it is reloaded - and a
// single-page app never reloads by itself. A build here is the web app's
// own entry script (Vite's content-hashed assets/index-*.js): the page's is
// the one it was loaded with, and after every sign-in - a reconnect,
// typically after the update's restart - the device's index.html is asked
// again for the one it serves now; another one raises the notice
// (DeviceUpdatedNotice.tsx). Comparing the web app itself, not the
// device's version number, works for every account on the device and
// needs nothing from the internet.
//
// One notice per change: dismissed, it stays away for that build and
// comes back only for another one. The device back on the page's own
// build (a rollback) takes it down. Never reloads anything itself.
//
// Rules only, no imports: `node web/scripts/device-build-test.mjs`.

let pageBuild = "";  // the build this page started with; "" until known
let toldFor = "";    // the build the notice went up for (dismissed or not)
let shown = false;
const listeners = new Set<() => void>();

function show(next: boolean) {
  if (next === shown) return;
  shown = next;
  listeners.forEach((l) => l());
}

export function subscribeDeviceUpdated(l: () => void): () => void {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

/** Whether the notice is up. */
export const deviceUpdatedShown = () => shown;

/** The build the page started with ("": not known yet). */
export const pageDeviceBuild = () => pageBuild;

/** The entry script's file name in a page's HTML ("index-CeiDWNvJ.js"),
 *  or "" where there is none (a development page from Vite has
 *  /src/main.tsx, which never changes, and says nothing either). */
export function entryScript(html: string): string {
  const tag = /<script\b[^>]*\btype=["']module["'][^>]*>/i.exec(html)?.[0] ?? "";
  const src = /\bsrc=["']([^"']+)["']/i.exec(tag)?.[1] ?? "";
  const name = src.split(/[?#]/)[0].split("/").pop() ?? "";
  return /^index-[\w-]+\.js$/.test(name) ? name : "";
}

/** A build: the first one is the page's own, later ones the device's.
 *  "" says nothing (no answer, or a page with no hashed entry script). */
export function noteDeviceBuild(build: string) {
  if (typeof build !== "string" || !build) return;
  if (!pageBuild) {
    pageBuild = build;
    return;
  }
  if (build === pageBuild) {
    toldFor = "";
    show(false);
    return;
  }
  if (build === toldFor) return;
  toldFor = build;
  show(true);
}

/** The notice's ×: away until the device reports yet another build. */
export function dismissDeviceUpdated() {
  show(false);
}

/** Tests: back to a page that has heard nothing. */
export function forgetDeviceBuild() {
  pageBuild = "";
  toldFor = "";
  shown = false;
}
