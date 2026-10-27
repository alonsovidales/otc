// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A reloaded tab signs in again with the session token it stored (issue
// #101, App.tsx). F8 of the 2026-10 review: a restore that got no answer -
// the socket didn't open, the bridge said the device can't be reached, the
// device didn't answer in time - landed signed out, though the token still
// worked: one blip at a reload, and the password had to be typed again. Now
// only the device's refusal (an expired or spent token) ends it. No answer
// keeps "Signing in…" and tries again: after 1 s, then twice as long each
// time up to 30 s, and at once when the browser is back online or the
// device answers again. Each try reads the token again: another tab may
// have replaced it (its redemption stores the next one) or signed out.
// Tested by node web/scripts/session-restore-test.mjs.

/** How a restore ended: signed in, refused by the device, nothing stored,
 *  or stopped (the page went). */
export type RestoreOutcome = "signed_in" | "refused" | "no_token" | "stopped";

export interface RestoreDeps {
  /** The stored token, read again before each try. */
  token: () => string | null;
  /** Redeems it: true signed in, false refused; throws when nothing answered. */
  redeem: (token: string) => Promise<boolean>;
  /** Calls `now` whenever trying again at once is worth it (online, the
   *  device back); returns the unsubscribe. */
  wake: (now: () => void) => () => void;
  /** Told of each try that got no answer. */
  onNoAnswer?: (attempt: number, err: unknown) => void;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (timer: unknown) => void;
}

/** The first wait after no answer, doubled each time up to cRestoreMaxMs. */
export const cRestoreFirstMs = 1000;
export const cRestoreMaxMs = 30_000;

export function restoreSession(deps: RestoreDeps): { done: Promise<RestoreOutcome>; stop: () => void } {
  const setTimer = deps.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = deps.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>));
  let stopped = false;
  // The wait between two tries, ended early by a wake or stop.
  let waiting: { timer: unknown; resolve: () => void } | null = null;
  const endWait = () => {
    if (!waiting) return;
    clearTimer(waiting.timer);
    const { resolve } = waiting;
    waiting = null;
    resolve();
  };
  const unwake = deps.wake(endWait);

  const done = (async (): Promise<RestoreOutcome> => {
    try {
      let delay = cRestoreFirstMs;
      for (let attempt = 1; ; attempt++) {
        if (stopped) return "stopped";
        const token = deps.token();
        if (!token) return "no_token";
        try {
          return (await deps.redeem(token)) ? "signed_in" : "refused";
        } catch (err) {
          if (stopped) return "stopped";
          deps.onNoAnswer?.(attempt, err);
        }
        await new Promise<void>((resolve) => {
          waiting = { timer: setTimer(() => { waiting = null; resolve(); }, delay), resolve };
        });
        delay = Math.min(delay * 2, cRestoreMaxMs);
      }
    } finally {
      unwake();
    }
  })();

  return {
    done,
    stop: () => {
      stopped = true;
      endWait();
    },
  };
}
