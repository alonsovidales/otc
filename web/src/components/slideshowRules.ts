// SPDX-License-Identifier: AGPL-3.0-or-later

// The slideshow's rules (Slideshow.tsx): its rounds of photos, how each one
// moves, how big it is loaded, and which touches are swipes. No DOM, so
// `node web/scripts/slideshow-test.mjs` tests it alone.
//
// The order is the device's (RandomPhotos: a collection's photos, or the
// whole library's, shuffled by a seed): every photo once a round, a page
// at a time, then a new seed and a new shuffle. A new round never starts
// with the photo just shown - it goes second, or last when the round has
// no other yet.

import type { File as MsgFile } from "../proto/messages";

// A photo's time on screen, its fade in included; and the fade.
export const cPhotoMs = 5000;
export const cFadeMs = 1200;
// The Ken Burns move outlasts the photo's time by the fade that takes it
// away, so it never stops on screen.
export const cMoveMs = cPhotoMs + 2 * cFadeMs;
// The most a photo is zoomed by its move: it is loaded this much bigger.
export const cMaxZoom = 1.08;
// Photos a page asks for: about a minute and a half of the show.
export const cPageSize = 20;

/** A page of a round, as the device answered it. */
export interface RoundPage {
  files: MsgFile[];
  // The next page's after; "" once the round is over.
  token: string;
  // How many photos the round holds (its first page only; else 0).
  total: number;
}

/** A photo of the show: which round it came in, its place there. */
export interface ShowEntry {
  file: MsgFile;
  round: number;
  // 1-based, within its round.
  index: number;
  // The round's photos, as its first page said (0: not known).
  total: number;
}

export type FetchPage = (seed: string, after: string) => Promise<RoundPage>;

/** 16 random hex digits: a round's order. */
export function newSeed(random: () => number = Math.random): string {
  let s = "";
  for (let i = 0; i < 16; i++) s += Math.floor(random() * 16).toString(16);
  return s;
}

/** The show's photos, round after round, fetched as they are taken. take()
 *  gives the next one, or null when there is nothing to show at all (no
 *  photos where it was opened); a page that fails rejects it, and the next
 *  take() asks that page again. */
export function createRounds(fetchPage: FetchPage, makeSeed: () => string = () => newSeed()) {
  let round = 0;
  let seed = "";
  let after = "";
  let done = true;
  let total = 0;
  let taken = 0;
  let queue: MsgFile[] = [];
  // The photo shown last, which a new round must not start with, and that
  // photo held back while the new round has nothing else yet.
  let lastHash = "";
  let held: MsgFile | null = null;
  let startChecked = true;
  // One page at a time; a take() meanwhile waits for it.
  let pending: Promise<void> | null = null;

  const begin = () => {
    round++;
    seed = makeSeed();
    after = "";
    done = false;
    total = 0;
    taken = 0;
    queue = [];
    held = null;
    startChecked = false;
  };

  const fetchNext = () => {
    if (!pending) {
      const asked = { seed, after, round };
      pending = fetchPage(asked.seed, asked.after)
        .then((page) => {
          if (asked.round !== round || asked.after !== after) return;
          if (asked.after === "") total = page.total;
          let files = page.files.slice();
          if (!startChecked && files.length > 0) {
            startChecked = true;
            if (files[0].hash !== "" && files[0].hash === lastHash) {
              if (files.length > 1) [files[0], files[1]] = [files[1], files[0]];
              else { held = files[0]; files = []; }
            }
          }
          queue.push(...files);
          if (held && queue.length > 0) {
            queue.splice(1, 0, held);
            held = null;
          }
          after = page.token;
          done = page.token === "";
          if (done && held) {
            queue.push(held);
            held = null;
          }
        })
        .finally(() => { pending = null; });
    }
    return pending;
  };

  return {
    async take(): Promise<ShowEntry | null> {
      for (let tries = 0; ; tries++) {
        if (queue.length > 0) {
          const file = queue.shift()!;
          taken++;
          lastHash = file.hash;
          return { file, round, index: taken, total };
        }
        if (done) {
          // A round that gave nothing is a show with nothing to show; else
          // the next round, shuffled again.
          if (round > 0 && taken === 0) return null;
          // A round of only pages the device passed over (photos not
          // processed yet): a few new rounds, then nothing.
          if (tries > 4) return null;
          begin();
        }
        await fetchNext();
      }
    },
    /** Starts fetching the next page now when the show is near the end of
     *  what it holds. */
    prefetch(within = 3) {
      if (!done && queue.length < within) void fetchNext().catch(() => { /* asked again by take() */ });
    },
    get round() { return round; },
  };
}

export type Rounds = ReturnType<typeof createRounds>;

/** One photo's slow move: zoom and pan, from and to (scale; translate in
 *  percent of the screen). By its place in the show, so neighbours move
 *  differently; zooming out as often as in. */
export interface KenBurns {
  from: { s: number; x: number; y: number };
  to: { s: number; x: number; y: number };
}

const cMoves: KenBurns[] = [
  { from: { s: 1, x: 0, y: 0 }, to: { s: 1.07, x: -1.5, y: -1 } },
  { from: { s: 1.08, x: 1.5, y: 1 }, to: { s: 1.01, x: 0, y: 0 } },
  { from: { s: 1.01, x: -1.2, y: 0.8 }, to: { s: 1.07, x: 1.2, y: -0.6 } },
  { from: { s: 1.07, x: 0, y: -1.2 }, to: { s: 1, x: 0, y: 0.8 } },
  { from: { s: 1, x: 1.2, y: -0.8 }, to: { s: 1.06, x: -1.2, y: 0.8 } },
  { from: { s: 1.06, x: -1.5, y: 0 }, to: { s: 1.01, x: 1.5, y: 0 } },
];

export function kenBurnsOf(i: number): KenBurns {
  return cMoves[((i % cMoves.length) + cMoves.length) % cMoves.length];
}

/** The size to load a w x h photo at for a box x box screen (CSS pixels
 *  times dpr): what "contain" draws it at, zoomed by cMaxZoom, never larger
 *  than the photo. */
export function fitSize(w: number, h: number, boxW: number, boxH: number, dpr: number): { w: number; h: number } {
  if (w <= 0 || h <= 0) return { w: 0, h: 0 };
  const scale = Math.min(1, Math.min((boxW * dpr) / w, (boxH * dpr) / h) * cMaxZoom);
  return { w: Math.max(1, Math.round(w * scale)), h: Math.max(1, Math.round(h * scale)) };
}

/** A touch's move, if it is a swipe: left goes on, right goes back. */
export function swipeOf(dx: number, dy: number, ms: number): "next" | "prev" | null {
  if (ms > 900 || Math.abs(dx) < 60 || Math.abs(dx) < 1.5 * Math.abs(dy)) return null;
  return dx < 0 ? "next" : "prev";
}
