// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The rules of a share link's ZIP page (ZipLinkPage.tsx; issue #104), tested
// by node web/scripts/zip-download-test.mjs.
//
// The page fetches the archive in parts, then hands it to the browser, which
// saves it. Until release 141 it then stayed on "Downloading… 100%" for good
// (user feedback, 2026-10: "it gets to 100% and stalls; the file did
// download"). Release 141 said "Your download has started" and closed the tab
// two seconds later - also when the browser was still asking about the file
// (Safari's "Allow downloads on this site?", a browser set to ask where to
// save it), so the question went with the tab, and the file too. Now:
// - the percentage is the bytes in, rounded down: 100% only once every byte
//   is (rounded, 99.5% showed 100% while the last part was still on its way);
// - once handed over the page says so at once, and keeps a Download button
//   that saves the same file again: a click the browser always lets save,
//   for one that refused or asked;
// - it closes itself after a moment only if nothing happened meanwhile and
//   it is in the background or has the focus: the page losing the focus or
//   being hidden or shown (a dialog or a question from the browser, someone
//   looking elsewhere), or shown without the focus, keeps it, saying it is
//   done. At worst a tab stays open that could have closed.

/** The whole percent of `received` of `total` bytes, rounded down (never
 *  100 before the last byte); null while the size isn't known. */
export function receivedPercent(received: number, total: number): number | null {
  if (!(total > 0)) return null;
  if (received >= total) return 100;
  return Math.max(0, Math.min(99, Math.floor((received / total) * 100)));
}

/** How long after the hand-off the page closes itself, when it may. */
export const cCloseAfterMs = 2000;

/** What the hand-off watches: the window's blur and focus, the document's
 *  visibility. */
export interface EventSource {
  addEventListener(type: string, fn: () => void): void;
  removeEventListener(type: string, fn: () => void): void;
}

export interface Clock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
}

/** Watches the page for `ms` after the file was handed to the browser, then
 *  says whether nothing took the focus or changed the page's visibility
 *  meanwhile (mayClose decides). Returns a stop (unmount). */
export function watchHandOff(
  win: EventSource,
  doc: EventSource,
  ms: number,
  settled: (mayClose: boolean) => void,
  clock: Clock = { setTimeout: (fn, t) => setTimeout(fn, t), clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>) },
): () => void {
  let interrupted = false;
  let over = false;
  const mark = () => { interrupted = true; };
  win.addEventListener("blur", mark);
  win.addEventListener("focus", mark);
  doc.addEventListener("visibilitychange", mark);
  const stop = () => {
    if (over) return;
    over = true;
    clock.clearTimeout(timer);
    win.removeEventListener("blur", mark);
    win.removeEventListener("focus", mark);
    doc.removeEventListener("visibilitychange", mark);
  };
  // Read by stop only once it is set: stop runs from it, or after return.
  const timer = clock.setTimeout(() => {
    stop();
    settled(!interrupted);
  }, ms);
  return stop;
}

/** Whether the page closes itself once watchHandOff is done: nothing
 *  happened meanwhile, and it is in the background (nobody is looking at
 *  it, and a question for it would have shown it) or has the focus (a
 *  question would have taken it). */
export function mayClose(quiet: boolean, hidden: boolean, focused: boolean): boolean {
  return quiet && (hidden || focused);
}
