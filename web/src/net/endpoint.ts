// SPDX-License-Identifier: AGPL-3.0-or-later

// Where the web app's socket goes, from the page's own address.
//
// The device serves this page, so its socket is the page's own host - on
// a device's name, its address at home, a tunnel to 127.0.0.1, or
// localhost on a server running the container image. Only the developer's
// own Vite servers have no device behind them: `npm run dev` (dev) and
// `vite preview` (its port, 4173), which go to VITE_DEVICE, or Cala.
// VITE_DEVICE is read at build time, so this is decided by how the page
// was built and served, never by a variable a release could carry.

// cPreviewHost is `vite preview`'s own address.
const cPreviewHost = "localhost:4173";

export interface PageEndpoint {
  // The socket's URL.
  endpoint: string;
  // The device itself served the page: only then does a device update
  // bring a new web app (DeviceUpdatedNotice).
  servedByDevice: boolean;
}

// pageEndpoint for a page at loc, dev being Vite's dev server
// (import.meta.env.DEV) and viteDevice its VITE_DEVICE. Inside the phone
// apps (inApp) the app's own endpoint replaces this one.
export function pageEndpoint(
  loc: { protocol: string; host: string },
  dev: boolean,
  viteDevice: string | undefined,
  inApp: boolean,
): PageEndpoint {
  const developer = dev || loc.host === cPreviewHost;
  if (developer) {
    return { endpoint: "wss://" + (viteDevice || "cala.off-the.cloud") + "/ws", servedByDevice: false };
  }
  const scheme = loc.protocol === "https:" ? "wss://" : "ws://";
  return { endpoint: scheme + loc.host + "/ws", servedByDevice: !inApp };
}
