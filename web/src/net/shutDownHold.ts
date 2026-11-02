// SPDX-License-Identifier: AGPL-3.0-or-later

// Settings > Shut Down Device (issue #202, release 164): once the device has
// agreed, the app leaves it alone for cShutDownHoldMs, as the phones do.
// The device switches itself off a few seconds after its answer, so for
// that while its going away is the shutdown, not a fault: useWS opens no
// new socket (a request that would need one fails at once, in the
// shutdown's words), App shows neither the unreachable screen nor the
// "Reconnecting…" pill, and Settings keeps "Shutting down… Once its lights
// stop blinking you can unplug it." on screen. After it everything is as
// usual: the polls reconnect, and the unreachable screen comes up while the
// device stays off. Signing out reloads the page, which drops it.
//
// Here, in net/, because the socket code reads it; the panel's rules are in
// components/settings/shutDown.ts. `node web/scripts/shut-down-test.mjs`.

/** How long after the device agreed it is left alone (the phones' 90 s). */
export const cShutDownHoldMs = 90_000;

type Schedule = (fn: () => void, ms: number) => unknown;
type Cancel = (timer: unknown) => void;

// A timer that never keeps Node's test process alive.
const defaultSchedule: Schedule = (fn, ms) => {
  const timer = setTimeout(fn, ms);
  if (typeof timer === "object" && timer !== null && "unref" in timer) timer.unref();
  return timer;
};
const defaultCancel: Cancel = (timer) => clearTimeout(timer as ReturnType<typeof setTimeout>);

let held = false;
let timer: unknown = undefined;
let cancel: Cancel = defaultCancel;
const listeners = new Set<() => void>();

function publish() {
  listeners.forEach((fn) => {
    try { fn(); } catch (e) { console.error("shutdown listener failed:", e); }
  });
}

/** The device agreed to shut down: hold everything for cShutDownHoldMs. */
export function noteShutDown(schedule: Schedule = defaultSchedule, cancelTimer: Cancel = defaultCancel): void {
  if (timer !== undefined) cancel(timer);
  cancel = cancelTimer;
  timer = schedule(endShutDownHold, cShutDownHoldMs);
  if (!held) {
    held = true;
    publish();
  }
}

/** Ends the hold now (its time is up, or it is dropped). */
export function endShutDownHold(): void {
  if (timer !== undefined) cancel(timer);
  timer = undefined;
  if (!held) return;
  held = false;
  publish();
}

/** Whether the app is leaving a device that is shutting down alone. */
export const shutDownHeld = (): boolean => held;

/** fn hears the hold start and end; returns the unsubscribe. */
export function subscribeShutDownHold(fn: () => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}
