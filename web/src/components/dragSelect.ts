// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #200: picking several photos or files in one gesture. Press on one
// and drag - with a mouse, a press that moves more than cDragThresholdPx;
// with a finger, a long press (cLongPressMs) and then a move without
// lifting - and every item from the pressed one to the one under the
// pointer, in the order they are shown, joins the selection. Moving back
// shrinks the run: what it passed over goes back to how it was before the
// drag. The drag only adds, and never clears what was picked before it.
//
// The pure part, with no DOM: useDragSelect.ts is the gesture on the page,
// and scripts/drag-select-test.mjs tests this. The iPhone's DragSelect.swift
// and Android's DragSelect.kt follow the same rules.

// A mouse press that moves further than this is a drag; less is a click,
// which opens the item (or picks it, once something is picked) as before.
export const cDragThresholdPx = 8;
// A touch held this long picks the item under it, as the long press always
// has; moving further than cDragThresholdPx first is a scroll.
export const cLongPressMs = 450;
// Near the window's top or bottom the page scrolls under the pointer:
// within this many pixels of the edge, ever faster towards it, up to this
// speed (px/s) at the edge and past it - the phones' numbers.
export const cEdgeZonePx = 56;
export const cEdgeMaxSpeed = 1200;
// A band thinner than this (a very short window) scrolls nothing.
export const cEdgeMinZonePx = 8;

/** What the selection is while a drag covers the items from `start` to
 *  `end` (indices into `order`, either way round, both included): `base` -
 *  the selection when the drag began, in its own order - then each item of
 *  the run that isn't in it yet, in display order (so the numbers a post
 *  shows its photos by run as the grid does, as Shift+click's do). Items
 *  `skip` says can't be picked (Files' ".."), never join. */
export function dragSelection<T>(
  base: readonly T[],
  order: readonly T[],
  keyOf: (item: T) => string,
  start: number,
  end: number,
  skip?: (item: T) => boolean,
): T[] {
  const from = Math.max(0, Math.min(start, end));
  const to = Math.min(order.length - 1, Math.max(start, end));
  const have = new Set(base.map(keyOf));
  const next = base.slice();
  for (let i = from; i <= to; i++) {
    const item = order[i];
    if (skip?.(item)) continue;
    const k = keyOf(item);
    if (have.has(k)) continue;
    have.add(k);
    next.push(item);
  }
  return next;
}

/** Where an item is now: `at` while it is still there (pages only ever
 *  add photos after it), else wherever its key went, or -1 once it is
 *  gone. */
export function findItem<T>(order: readonly T[], keyOf: (item: T) => string, key: string, at: number): number {
  if (at >= 0 && at < order.length && keyOf(order[at]) === key) return at;
  return order.findIndex((item) => keyOf(item) === key);
}

/** A drag's two ends: the item pressed and the one the pointer was last
 *  over, by key and where each was last seen (`end` is -1 while the one
 *  last under the pointer is gone). */
export type DragEnds = { startKey: string; start: number; endKey: string; end: number };

/** The drag's ends in `order` as it is now (a page came, a face went),
 *  each found by its key. null once the item pressed is gone: the drag is
 *  over, the selection stays as it stands, later moves change nothing and
 *  the lift is still no tap. When only the item under the pointer is gone,
 *  `end` is -1: the selection stays as it is until the pointer is over
 *  another item. (The base goes through keptBase at the same time.) */
export function relocate<T>(ends: DragEnds, order: readonly T[], keyOf: (item: T) => string): DragEnds | null {
  const start = findItem(order, keyOf, ends.startKey, ends.start);
  if (start < 0) return null;
  const end = ends.end < 0 ? -1 : findItem(order, keyOf, ends.endKey, ends.end);
  return { ...ends, start, end };
}

/** The selection a drag started from (`base`), once the items changed
 *  from `before` to `after` mid-drag: a pick that has gone from the items
 *  (a face merged away, a photo deleted - the view drops it from its
 *  selection) goes from the base too, so the next move doesn't bring it
 *  back, nor Escape. A pick that was never among the items (Images keeps
 *  one from an earlier search) stays. The same array when nothing went. */
export function keptBase<T>(
  base: readonly T[],
  before: readonly T[],
  after: readonly T[],
  keyOf: (item: T) => string,
): readonly T[] {
  if (base.length === 0 || before === after) return base;
  const now = new Set(after.map(keyOf));
  if (base.every((item) => now.has(keyOf(item)))) return base;
  const was = new Set(before.map(keyOf));
  const next = base.filter((item) => {
    const k = keyOf(item);
    return now.has(k) || !was.has(k);
  });
  return next.length === base.length ? base : next;
}

/** Whether two selections hold the same items in the same order. */
export function sameSelection<T>(a: readonly T[], b: readonly T[], keyOf: (item: T) => string): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (keyOf(a[i]) !== keyOf(b[i])) return false;
  return true;
}

/** Whether a press has moved far enough to be a drag. */
export const pastThreshold = (dx: number, dy: number, px = cDragThresholdPx) => Math.hypot(dx, dy) > px;

/** How fast the page scrolls (px/s; up is negative) with the pointer at
 *  `y`, when what can be seen runs from `top` (below the top bar) to
 *  `bottom` (the window's): nothing outside the zones, then in proportion
 *  to how deep into one it is, up to cEdgeMaxSpeed at the window's edge,
 *  and as fast past it (a mouse held above or below the window). Each zone
 *  is at most a third of what can be seen, and a window so short that it
 *  would be under cEdgeMinZonePx has none (the phones' rule). */
export function edgeSpeed(y: number, top: number, bottom: number, zone = cEdgeZonePx): number {
  const z = Math.min(zone, (bottom - top) / 3);
  if (!(z >= cEdgeMinZonePx)) return 0;
  if (y < top + z) return -cEdgeMaxSpeed * Math.min(1, (top + z - y) / z);
  if (y > bottom - z) return cEdgeMaxSpeed * Math.min(1, (y - (bottom - z)) / z);
  return 0;
}

/** The pointer of a drag: where it is, where the drag began (with a
 *  touch, where the long press fired) and whether the edges may scroll the
 *  page yet. A mouse drag already moved more than cDragThresholdPx to
 *  begin; a long press must move that far from where it fired first, so a
 *  long press held still near the top or bottom never scrolls. */
export type DragPointer = { x: number; y: number; fromX: number; fromY: number; armed: boolean };

export const dragPointer = (x: number, y: number, mouse: boolean): DragPointer =>
  ({ x, y, fromX: x, fromY: y, armed: mouse });

/** The pointer moved to (x, y). */
export function movePointer(p: DragPointer, x: number, y: number): void {
  p.x = x;
  p.y = y;
  if (!p.armed && pastThreshold(x - p.fromX, y - p.fromY)) p.armed = true;
}

/** How fast the page scrolls under the drag's pointer (edgeSpeed), and
 *  not at all until it is armed. */
export const pointerSpeed = (p: DragPointer, top: number, bottom: number): number =>
  (p.armed ? edgeSpeed(p.y, top, bottom) : 0);

/** Where what can be seen of the items starts: `top` (the bottom of the
 *  window's fixed top bar) or, when bars stick under it one below the
 *  other (Files' selection bar, then its column headings), the bottom of
 *  the last of them. `boxes` are their edges on the screen, in page order;
 *  one with no height (not shown) or not up against the ones above (still
 *  in the page's flow, lower down) is passed by, and one pushed out above
 *  them (its list scrolled away) adds nothing. The phones' lists have only
 *  their top bar over them: this is the web's. */
export function stuckTop(top: number, boxes: readonly { top: number; bottom: number }[]): number {
  let at = top;
  for (const b of boxes) {
    if (b.bottom > b.top && b.top <= at + 1 && b.bottom > at) at = b.bottom;
  }
  return at;
}

/** The point to look for an item under: the pointer, kept within the grid
 *  across (a pointer beside it still runs along its rows) and within what
 *  can be seen from top to bottom (above the window, the first row in
 *  sight). */
export function hitPoint(
  x: number, y: number,
  grid: { left: number; right: number }, top: number, bottom: number,
): { x: number; y: number } {
  const clamp = (v: number, lo: number, hi: number) => (hi < lo ? lo : Math.min(hi, Math.max(lo, v)));
  return { x: clamp(x, grid.left + 1, grid.right - 1), y: clamp(y, top + 1, bottom - 1) };
}
