// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #194: after a jump to a date (a search started with `before` = C),
// scrolling up near the top loads the photos newer than C, so the timeline
// reads as one list. A second cursor asks SearchPhotos with newer_than = C
// (never `before`), its own token and its own `have`, and the device
// answers those pages OLDEST first (ListOfFiles.oldest_first); they go above
// the grid newest first, without what it already shows. A device before
// #194 ignores newer_than and answers an ordinary page with oldest_first
// false: that page is dropped and the device isn't asked again while this
// page is open (signing out reloads it). The rules here are pure (tested
// by web/scripts/newer-photos-test.mjs) and the same as the phones'.

/** Where the upward search is: the token to send ("" for its first
 *  page), how many photos it brought (its own `have`, never the downward
 *  search's), and whether the top was reached. */
export type UpCursor = { token: string; have: number; end: boolean };
export const cUpStart: UpCursor = { token: "", have: 0, end: false };

// A device that answered a newer_than page without oldest_first.
let ignoresNewerThan = false;
/** The device answered newer_than as a device before #194 does. */
export function deviceIgnoresNewerThan() { ignoresNewerThan = true; }
/** Whether it did, while this page has been open. */
export const newerThanIgnored = () => ignoresNewerThan;
/** For the tests. */
export function forgetDeviceAnswers() { ignoresNewerThan = false; }

/** Whether a grid keeps an upward cursor: after a jump (its cutoff), in
 *  date order with the scrubber shown (no tags), on a device that honours
 *  newer_than. A plain search has nothing newer than its first photo. */
export function upwardApplies(g: { cutoff: Date | null | undefined; dateOrdered: boolean; scrubber: boolean }): boolean {
  return g.cutoff != null && g.dateOrdered && g.scrubber && !ignoresNewerThan;
}

/** The SearchPhotos fields of an upward page: the jump's filters,
 *  newer_than = exactly its cutoff on every page (retries too), the
 *  upward token and have, a small first page (`firstLimit`, as a new
 *  search's) and the device's own size after it. */
export function upwardRequest(
  filter: { personIds: string[]; groupId: string; area?: { south: number; west: number; north: number; east: number } },
  cutoff: Date,
  cursor: UpCursor,
  firstLimit: number,
) {
  return {
    tags: [] as string[],
    personIds: filter.personIds,
    groupId: filter.groupId,
    // A place opened from Places: inside its area too.
    ...(filter.area ? { area: filter.area } : {}),
    includeVideos: true,
    newerThan: cutoff,
    token: cursor.token,
    have: cursor.have,
    limit: cursor.token ? 0 : firstLimit,
    smallThumbnails: true,
  };
}

/** An upward answer: "old" (a device before #194: drop it, stop), or the
 *  page turned newest first, the token to send next, and whether the top
 *  is reached (no token, or an empty page). */
export type UpAnswer<F> = { kind: "old" } | { kind: "page"; files: F[]; token: string; end: boolean };

export function readUpward<F>(lof: { files?: F[]; token: string; oldestFirst: boolean }): UpAnswer<F> {
  if (!lof.oldestFirst) return { kind: "old" };
  const files = [...(lof.files ?? [])].reverse();
  return { kind: "page", files, token: lof.token, end: !lof.token || files.length === 0 };
}

/** The photos of `page` the grid doesn't show yet, by hash (a photo's
 *  oldest copy can change between searches, and then turn up on the other
 *  side), in the page's order; nor twice from the page itself. */
export function notShown<F extends { hash: string }>(page: readonly F[], shown: readonly F[]): F[] {
  const seen = new Set<string>();
  for (const f of shown) if (f.hash) seen.add(f.hash);
  const out: F[] = [];
  for (const f of page) {
    if (f.hash) {
      if (seen.has(f.hash)) continue;
      seen.add(f.hash);
    }
    out.push(f);
  }
  return out;
}

/** How near the grid's top counts as near, in px above the screen's top:
 *  from there the page above is asked for (the top mark's observer
 *  margin), a scroll up lets it be, and its spinner or error shows. */
export const cUpReach = 600;

/** Whether the grid's top is within reach: `markBottom` is where the top
 *  mark's bottom is on screen (px from the screen's top; below it, the
 *  page is above the grid). */
export const nearTop = (markBottom: number, reach = cUpReach): boolean => markBottom > -reach;

/** Where the page is scrolled to and how tall it is, at a scroll. */
export type ScrollAt = { y: number; height: number };

/** Whether a scroll lets the top ask for the photos newer than a jump:
 *  the first scroll UP near the top, however it was made - a wheel, a
 *  key, a finger, the scrollbar dragged (the same rule as the phones').
 *  The page went up by more than a pixel since the last scroll (`prev`)
 *  with the grid's top within reach (`markBottom`, see nearTop) and its
 *  height unchanged: the jump, photos put above it and a change of width
 *  scroll it by themselves, and change its height as they do. */
export function scrollArmsUp(prev: ScrollAt, now: ScrollAt, markBottom: number, reach = cUpReach): boolean {
  return now.height === prev.height && now.y < prev.y - 1 && nearTop(markBottom, reach);
}

/** An index into the grid once `n` photos went above it. */
export const shiftIndex = (i: number | null, n: number): number | null => (i == null ? null : i + n);

/** The month at the top while newer photos are still to come keeps the
 *  room its date bucket says it needs (`room`), so the photos arriving for
 *  it fill grey tiles in place instead of moving the ones shown: the grey
 *  ones go BEFORE its `loaded` photos (they are its newest). With `cols`
 *  tiles a row, whole grey rows beyond `maxRows` are one spacer
 *  (`spacerRows` rows high), so a month of thousands draws a few dozen
 *  nodes; `greys` grey tiles follow it. */
export function topReserve(room: number, loaded: number, cols: number, maxRows = 4): { spacerRows: number; greys: number } {
  const g = Math.max(0, room - loaded);
  if (g === 0) return { spacerRows: 0, greys: 0 };
  if (cols <= 0 || room < cols) return { spacerRows: 0, greys: g };
  const rows = Math.floor(g / cols);
  const drawn = Math.min(rows, maxRows);
  return { spacerRows: rows - drawn, greys: g - (rows - drawn) * cols };
}
