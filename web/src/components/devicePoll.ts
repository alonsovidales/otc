// SPDX-License-Identifier: AGPL-3.0-or-later

// A poll of the device (the menu's storage status, the Alerts badge). They
// used to skip a turn whenever the socket was closed, so after a close -
// a restart, a network change - nothing opened it again until someone
// clicked. Each turn now goes through useWS.request(), which opens a new
// socket and signs in again when the old one is gone, and a turn that
// failed waits longer before the next: everyMs doubled each time, up to
// cPollMaxMs, back to everyMs after the next answer.
const cPollMaxMs = 60_000;

/**
 * Runs `turn` now and then every everyMs: it answers whether the device
 * answered. Returns the stop.
 */
export function pollDevice(turn: () => Promise<boolean>, everyMs: number): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let delay = everyMs;
  const run = async () => {
    timer = null;
    let ok = false;
    try {
      ok = await turn();
    } catch {
      ok = false;
    }
    if (stopped) return;
    delay = ok ? everyMs : Math.min(delay * 2, cPollMaxMs);
    timer = setTimeout(() => void run(), delay);
  };
  void run();
  return () => {
    stopped = true;
    if (timer != null) clearTimeout(timer);
  };
}
