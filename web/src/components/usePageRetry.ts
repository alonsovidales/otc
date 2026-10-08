// SPDX-License-Identifier: AGPL-3.0-or-later

// Backoff for an infinite-scroll grid whose next page failed. The grids
// re-create their IntersectionObserver whenever `loading` flips, and a new
// observer reports at once while the sentinel is in view - so a page that
// failed (an error reply, or no connection at all) was asked for again
// straight away, in a loop with no delay: a request every round trip
// against a device that keeps erroring, a new WebSocket attempt after
// another while the network is down. MediaViewer asks for a big
// thumbnail that failed again the same way.
import { useCallback, useEffect, useRef, useState } from "react";
import { subscribeDeviceStatus } from "../net/deviceStatus";

const cRetryFirstMs = 1000;
// Kept short: the grid should refill soon after the device is back.
const cRetryMaxMs = 10_000;

/**
 * failed() after a page fails; reset() after one arrives, and before a
 * search the user started (that one always goes out at once). ready()
 * says whether the observer may ask again yet. `tick` changes when the
 * wait is over - or as soon as the device answers again or the browser
 * comes back online - so it belongs in the observer effect's deps: the
 * new observer then fetches if the sentinel is still in view.
 */
export function usePageRetry() {
  const failsRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [tick, setTick] = useState(0);

  const reset = useCallback(() => {
    if (timerRef.current != null) clearTimeout(timerRef.current);
    timerRef.current = null;
    failsRef.current = 0;
  }, []);

  const failed = useCallback(() => {
    const n = ++failsRef.current;
    const delay = Math.min(cRetryFirstMs * 2 ** (n - 1), cRetryMaxMs);
    if (timerRef.current != null) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      setTick(t => t + 1);
    }, delay);
  }, []);

  // Ready once the timer has fired (or was cleared), by that one clock: a
  // wall-clock check could still read a hair early when the timer fires
  // (rounded or slewed Date.now()), skip the fetch, and nothing would
  // bump `tick` again while the sentinel stays in view.
  const ready = useCallback(() => timerRef.current == null, []);

  useEffect(() => {
    // The device status clears on the first good reply from the device,
    // and "online" covers a dropped network, where that status never
    // changes - either way the grid refills at once, as the old loop did.
    const wake = () => {
      if (failsRef.current === 0) return;
      reset();
      setTick(t => t + 1);
    };
    const unsubscribe = subscribeDeviceStatus(s => { if (s === null) wake(); });
    window.addEventListener("online", wake);
    return () => {
      unsubscribe();
      window.removeEventListener("online", wake);
      if (timerRef.current != null) clearTimeout(timerRef.current);
      timerRef.current = null;
    };
  }, [reset]);

  return { tick, failed, reset, ready };
}
