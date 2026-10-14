// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "The device was updated" with Reload: a tab opened before an update
// keeps the old web app until it reloads (deviceBuild.ts has the rules).
// A small card at the foot of the page, never a dialog, and nothing
// reloads until Reload is pressed. Only where the device served this
// page: in development the page comes from Vite, and reloading it would
// bring nothing new.
import { useEffect, useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import { useT } from "../i18n";
import { deviceUpdatedShown, dismissDeviceUpdated, entryScript, noteDeviceBuild, subscribeDeviceUpdated } from "./deviceBuild";
import "./DeviceUpdatedNotice.css";

// The page's own build first, then, after every sign-in - the first and
// each a reconnect makes - the one the device's index.html names now
// (no-store: never an answer the browser kept).
function watchDeviceBuild(): () => void {
  let stopped = false;
  noteDeviceBuild(entryScript(document.documentElement.outerHTML));
  const ask = async () => {
    try {
      const page = await fetch("/", { cache: "no-store", credentials: "same-origin" });
      if (!page.ok) return;
      const html = await page.text();
      if (!stopped) noteDeviceBuild(entryScript(html));
    } catch {
      // No answer: the next sign-in asks again.
    }
  };
  const off = useWS.onSignedIn(() => void ask());
  // Mounted after a sign-in already happened.
  if (useWS.signedIn()) void ask();
  return () => { stopped = true; off(); };
}

export default function DeviceUpdatedNotice() {
  const t = useT();
  const shown = useSyncExternalStore(subscribeDeviceUpdated, deviceUpdatedShown);
  useEffect(() => watchDeviceBuild(), []);
  // The status region stays in the page, empty while there's nothing to
  // say: screen readers announce text put into a live region that already
  // exists, not one that arrives with its text.
  return (
    <div className="dun-wrap" role="status">
      {shown && (
        <div className="dun-card">
          <span className="dun-text">{t("web.shell.device_updated.text")}</span>
          <button type="button" className="dun-reload" onClick={() => window.location.reload()}>
            {t("web.shell.device_updated.reload")}
          </button>
          <button type="button" className="dun-close" onClick={dismissDeviceUpdated} aria-label={t("common.close")} data-tip={t("common.close")}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
              <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
            </svg>
          </button>
        </div>
      )}
    </div>
  );
}
