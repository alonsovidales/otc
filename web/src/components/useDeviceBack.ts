// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A load that runs once and failed runs again when the device answers
// again. The unreachable screen lies over the app, which stays mounted
// under it (App.tsx), so nothing loads afresh as the screen lifts: a
// section that asked once while the device was away - a folder's listing,
// the settings, the users, the profile, the alerts, the friends - kept its
// error until Refresh, though the screen says the page picks up by itself
// (when it replaced the app, the app mounted again and loaded everything).
// Grids that page have usePageRetry for this, and the library's lists ask
// again while they show (libraryStore.ts).
//
//   const back = useDeviceBack(() => void load());
//   ... back.ok() as a load succeeds, back.failed() as it fails.
//
// From a failure on, until a success: the device answering again (the
// bridge's answer for it, or its storage's, cleared - even before this
// hook's effect ran: deviceBackCount) or the browser back online runs the
// load again. Tested by web/scripts/post-confirm-test.mjs (whenDeviceBack).
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { deviceBackCount, whenDeviceBack } from "../net/deviceStatus";

export type DeviceBack = { ok: () => void; failed: () => void };

export function useDeviceBack(reload: () => void): DeviceBack {
  const reloadRef = useRef(reload);
  useLayoutEffect(() => { reloadRef.current = reload; });
  // deviceBackCount as the last load failed; null after a success.
  const [failedAt, setFailedAt] = useState<number | null>(null);
  useEffect(() => {
    if (failedAt === null) return;
    const again = () => reloadRef.current();
    const off = whenDeviceBack(failedAt, again);
    window.addEventListener("online", again);
    return () => {
      off();
      window.removeEventListener("online", again);
    };
  }, [failedAt]);
  return useMemo(() => ({
    ok: () => setFailedAt(null),
    failed: () => setFailedAt(deviceBackCount()),
  }), []);
}
