// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A collection's Rating (release 163; owner, 2026-11-02): with it on, every
// photo of the collection can have 1 to 5 stars and a comment. The owner
// sees one rating a photo, the latest of anyone's (PhotoRating.from_link
// for one through a shared link); a shared link's visitors see and change
// only their link's own (release 169). The rules every screen shares, the
// phones' as well:
//
// - Show (RatingShow) narrows the grid to "5 stars", "4 stars or more"...
//   "Rated" (1 or more), "Not rated" or "With a comment", and Sort by
//   (RatingSort) puts the best rated first. Inside a collection the device
//   does both (SearchPhotos.rating, by_rating: ratingSearch); on a shared
//   gallery's page every item is loaded already and the page does them
//   itself (arrangeItems), by the same rules. Either away from its default
//   ranks the grid: no month titles, no date scrubber.
// - A star tapped sets that many; the one the photo has, tapped again,
//   clears it (nextStars). A comment is trimmed and at most 2000 characters
//   (cleanComment); no stars and no comment is no rating.
// - Ratings (createRatings): what the stars and the comment show, by key (a
//   photo's path in a collection, an item's index on a gallery's page). A
//   change shows at once and is sent, one request at a time per key, until
//   the device holds what shows - so quick taps can't reach it in the
//   wrong order -; refused, it goes back to the device's last word, unless
//   it changed again meanwhile.
//
// No imports but types: `node web/scripts/rating-test.mjs` loads it alone.

import type { RatingFilter } from "../proto/messages";

/** Show: which photos of a rated collection the grid keeps. */
export type RatingShow = "all" | "5" | "4" | "3" | "2" | "rated" | "unrated" | "comment";
/** Sort by: the device's date order, or the best rated first. */
export type RatingSort = "date" | "rating";
export type RatingView = { show: RatingShow; sort: RatingSort };

/** The default: every photo, by date. */
export const cRatingDefault: RatingView = Object.freeze({ show: "all", sort: "date" }) as RatingView;
/** The choices, in the order the menus list them. */
export const ratingShows: readonly RatingShow[] = ["all", "5", "4", "3", "2", "rated", "unrated", "comment"];
export const ratingSorts: readonly RatingSort[] = ["date", "rating"];

export const cMaxStars = 5;
export const cMaxComment = 2000;

/** A menu's value as a choice; anything else is the default. */
export const parseShow = (s: string | null | undefined): RatingShow =>
  (ratingShows as readonly string[]).includes(s ?? "") ? (s as RatingShow) : "all";
export const parseSort = (s: string | null | undefined): RatingSort => (s === "rating" ? "rating" : "date");

/** Whether the view ranks the grid (no month titles, no scrubber). */
export const isRatingRanked = (v: RatingView) => v.show !== "all" || v.sort === "rating";

/** SearchPhotos.rating for a Show choice: none for every photo. */
export function ratingFilterOf(show: RatingShow): RatingFilter | undefined {
  switch (show) {
    case "5": return { minStars: 5, unrated: false, withComment: false };
    case "4": return { minStars: 4, unrated: false, withComment: false };
    case "3": return { minStars: 3, unrated: false, withComment: false };
    case "2": return { minStars: 2, unrated: false, withComment: false };
    case "rated": return { minStars: 1, unrated: false, withComment: false };
    case "unrated": return { minStars: 0, unrated: true, withComment: false };
    case "comment": return { minStars: 0, unrated: false, withComment: true };
    default: return undefined;
  }
}

/** SearchPhotos' rating fields: only for a collection whose Rating is on
 *  (null otherwise), and nothing at the default - the request is then
 *  byte for byte what it was before Rating. */
export function ratingSearch(view: RatingView | null): { rating?: RatingFilter; byRating?: boolean } {
  if (!view) return {};
  const rating = ratingFilterOf(view.show);
  return {
    ...(rating ? { rating } : {}),
    ...(view.sort === "rating" ? { byRating: true } : {}),
  };
}

/** Stars as a whole number from 0 (none) to 5. */
export const clampStars = (n: number) => (Number.isFinite(n) ? Math.min(cMaxStars, Math.max(0, Math.round(n))) : 0);

/** A tap on star `tapped`: that many - or none, when it is the photo's. */
export const nextStars = (current: number, tapped: number) => (clampStars(tapped) === clampStars(current) ? 0 : clampStars(tapped));

/** The comment as it is sent and kept: trimmed, at most 2000 characters
 *  (code points, as the device counts them). */
export function cleanComment(s: string): string {
  const t = s.trim();
  const chars = [...t];
  return chars.length > cMaxComment ? chars.slice(0, cMaxComment).join("").trimEnd() : t;
}

/** A rating as shown: stars, comment, and whether the last change came
 *  through the shared link. */
export type RatingValue = { stars: number; comment: string };
export type ShownRating = RatingValue & { fromLink: boolean };

export const cNoRating: ShownRating = Object.freeze({ stars: 0, comment: "", fromLink: false }) as ShownRating;

/** What a listing says (File.rating, unset for a photo without one). */
export function listedRating(r: { stars?: number; comment?: string; fromLink?: boolean } | null | undefined): ShownRating {
  if (!r) return cNoRating;
  return { stars: clampStars(r.stars ?? 0), comment: r.comment ?? "", fromLink: !!r.fromLink };
}

/** Whether a rating passes a Show choice (the device's rules, for a page
 *  that has every item: a shared gallery's). */
export function passesShow(r: RatingValue, show: RatingShow): boolean {
  switch (show) {
    case "5": return r.stars >= 5;
    case "4": return r.stars >= 4;
    case "3": return r.stars >= 3;
    case "2": return r.stars >= 2;
    case "rated": return r.stars >= 1;
    case "unrated": return r.stars <= 0;
    case "comment": return r.comment.trim() !== "";
    default: return true;
  }
}

/** A shared gallery's items as the view shows them: those passing Show,
 *  in the gallery's own order for Date; for Rating, more stars first, then
 *  the newest taken (one without a date after those with one), photos
 *  without stars last - as the device sorts a collection (by_rating). Ties
 *  keep the gallery's order. */
export function arrangeItems<T>(items: readonly T[], view: RatingView, ratingOf: (item: T) => RatingValue, takenOf: (item: T) => Date | undefined): T[] {
  const kept = items.filter((it) => passesShow(ratingOf(it), view.show));
  if (view.sort !== "rating") return kept;
  const time = (it: T) => {
    const d = takenOf(it);
    const ms = d ? d.getTime() : NaN;
    return Number.isNaN(ms) ? -Infinity : ms;
  };
  return kept
    .map((it, i) => ({ it, i, stars: ratingOf(it).stars, at: time(it) }))
    .sort((a, b) => (b.stars - a.stars) || (b.at === a.at ? 0 : b.at > a.at ? 1 : -1) || (a.i - b.i))
    .map((x) => x.it);
}

// ---- the stars and comments shown, and their requests ---------------------------

/** How one request went: the rating the device keeps now, or why not -
 *  its code ("" for none, or no answer) and its words ("" for none). */
export type RatingSendResult =
  | { ok: true; stored: ShownRating }
  | { ok: false; code: string; said: string };
/** A change the device didn't take: the stars and comment are back. */
export type RatingRefusal = { code: string; said: string };
/** Sends one change; may throw (no answer), which counts as a refusal
 *  without words. */
export type RatingsSend = (key: string, want: RatingValue) => Promise<RatingSendResult>;

export type Ratings = {
  /** What shows for the key, once anything was set for it; undefined
   *  until then (the listing's rating shows). */
  get(key: string): ShownRating | undefined;
  /** A change for the key is on its way. */
  busy(key: string): boolean;
  /** Stars and comment set: shown at once, then sent. `listed` is what
   *  showed before (the listing's), for a refusal to go back to. Resolves
   *  null once the device holds what shows (or nothing had to be sent),
   *  else with the refusal - the change gone back. */
  set(key: string, want: RatingValue, listed: ShownRating): Promise<RatingRefusal | null>;
  /** A new listing has the device's word: what nothing is on its way for
   *  is forgotten, and shows as listed. */
  settle(): void;
  /** Goes up with every change shown (useSyncExternalStore's snapshot). */
  version(): number;
  subscribe(listener: () => void): () => void;
};

const sameRating = (a: RatingValue, b: RatingValue) => a.stars === b.stars && a.comment === b.comment;

export function createRatings(send: RatingsSend): Ratings {
  // What shows, by key: the latest change, or what the device took.
  const shown = new Map<string, ShownRating>();
  // The device's last word, by key: what a refused change goes back to.
  const confirmed = new Map<string, ShownRating>();
  // A key's requests, one at a time, until what it shows is sent.
  const sending = new Map<string, Promise<RatingRefusal | null>>();
  let version = 0;
  const listeners = new Set<() => void>();
  const emit = () => {
    version++;
    listeners.forEach((l) => l());
  };

  // Sends what the key shows until the device holds it. It leaves
  // `sending` in the step that decides it is done, so a change can't land
  // between that and its end and go unsent.
  const drain = (key: string): Promise<RatingRefusal | null> => {
    const run = (async (): Promise<RatingRefusal | null> => {
      // In `sending` before anything below runs.
      await undefined;
      for (;;) {
        const want = shown.get(key);
        const have = confirmed.get(key);
        if (!want || (have && sameRating(want, have))) {
          sending.delete(key);
          return null;
        }
        const asked: RatingValue = { stars: want.stars, comment: want.comment };
        let result: RatingSendResult;
        try {
          result = await send(key, asked);
        } catch {
          result = { ok: false, code: "", said: "" };
        }
        if (result.ok) {
          const stored = listedRating(result.stored);
          confirmed.set(key, stored);
          // Unchanged meanwhile: what the device keeps shows (its own
          // trimming, from_link now false). Changed: that goes next.
          if (sameRating(shown.get(key) ?? stored, asked)) {
            shown.set(key, stored);
            emit();
          }
          continue;
        }
        // Changed again meanwhile: what shows now decides.
        const now = shown.get(key);
        if (now && !sameRating(now, asked)) continue;
        const back = confirmed.get(key);
        if (back) shown.set(key, back);
        else shown.delete(key);
        sending.delete(key);
        emit();
        return { code: result.code, said: result.said };
      }
    })();
    sending.set(key, run);
    return run;
  };

  return {
    get: (key) => shown.get(key),
    busy: (key) => sending.has(key),
    set(key, want, listed) {
      const next: ShownRating = { stars: clampStars(want.stars), comment: cleanComment(want.comment), fromLink: false };
      if (!confirmed.has(key)) confirmed.set(key, listedRating(listed));
      const now = shown.get(key) ?? confirmed.get(key)!;
      if (sameRating(now, next)) return sending.get(key) ?? Promise.resolve(null);
      shown.set(key, next);
      emit();
      // One on its way already: it sends this too once answered.
      return sending.get(key) ?? drain(key);
    },
    settle() {
      let changed = false;
      for (const key of [...shown.keys(), ...confirmed.keys()]) {
        if (sending.has(key)) continue;
        changed = shown.delete(key) || changed;
        confirmed.delete(key);
      }
      if (changed) emit();
    },
    version: () => version,
    subscribe(listener) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
  };
}

// ---- the first time Rating is turned on ------------------------------------------

/** The browser's flag for the one-line hint shown the first time Rating is
 *  turned on (once per browser; for the visit where storage throws). */
export const cRatingHintKey = "otc_rating_hint_seen";
let hintShown = false;

/** True the first time only: the hint is to show. */
export function claimRatingHint(): boolean {
  if (hintShown) return false;
  hintShown = true;
  try {
    const store = globalThis.localStorage;
    if (!store) return true;
    if (store.getItem(cRatingHintKey)) return false;
    store.setItem(cRatingHintKey, "1");
  } catch {
    // Storage refused: once for this visit.
  }
  return true;
}

/** For the tests: as on a new page. */
export function forgetRatingHint() {
  hintShown = false;
}
