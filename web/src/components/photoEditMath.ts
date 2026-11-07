// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The photo editor's crop frame (PhotoEditor.tsx), as arithmetic: pure, so
// web/scripts/photo-edit-test.mjs tests it in Node. A frame is a rectangle
// in fractions (0 to 1) of the photo as the editor shows it - already
// turned by its quarter turns, upright - from its top left corner, which is
// what EditCrop carries (messages.proto). The device turns first and crops
// what it turned, so a turn turns the frame with the picture.
//
// A shape (Aspect) other than Free holds the frame's width over its height
// in pixels: Original is the picture's own, Square 1, and 4:3 and 16:9 lie
// the way the picture lies as it is turned now - an upright one gets 3:4 and
// 9:16. Chosen, the frame becomes the largest of that shape the picture
// holds, centred where the frame was and moved only as far as it must to
// stay inside. The phones' editors follow the same rules: change them
// together.

/** A frame, in fractions of the turned photo's width and height. */
export type Rect = { x: number; y: number; w: number; h: number };
/** A size in pixels (the photo's, or the picture's on screen). */
export type Size = { w: number; h: number };
/** The frame's shapes, in the menu's order. */
export type Aspect = "free" | "original" | "square" | "4:3" | "16:9";
export const cAspects: readonly Aspect[] = ["free", "original", "square", "4:3", "16:9"];
/** What a drag holds: the frame's edges and corners by compass point. */
export type Handle = "n" | "s" | "e" | "w" | "ne" | "nw" | "se" | "sw";
/** The smallest frame, in fractions of the turned photo's sides. */
export type Limits = { minW: number; minH: number };

/** The whole photo: no crop. */
export const cWhole: Rect = { x: 0, y: 0, w: 1, h: 1 };
// The smallest frame is about a twentieth of the photo's side - a crop of
// a few pixels is a mistake, not a picture - and never under a finger's
// few dozen pixels on screen, so its handles can still be told apart.
export const cMinShare = 1 / 20;
export const cMinPx = 40;
// Two frames this close are the same one (floating point after a drag).
const cEps = 1e-6;

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** The photo's size once turned `turns` quarter turns (taken modulo 4). */
export function turnedSize(photo: Size, turns: number): Size {
  return ((turns % 4) + 4) % 2 === 1 ? { w: photo.h, h: photo.w } : photo;
}

/** The frame turned with the picture a quarter turn: clockwise (1) or
 *  anticlockwise (-1). The fractions are of the turned photo, whose sides
 *  swap: clockwise, (x, y, w, h) becomes (1-y-h, x, h, w), anticlockwise
 *  (y, 1-x-w, h, w). */
export function turnRect(r: Rect, dir: 1 | -1): Rect {
  return dir === 1
    ? { x: 1 - r.y - r.h, y: r.x, w: r.h, h: r.w }
    : { x: r.y, y: 1 - r.x - r.w, w: r.h, h: r.w };
}

/** The smallest frame for a picture shown at `shown` pixels. */
export function limitsFor(shown: Size): Limits {
  const min = (side: number) => Math.min(1, Math.max(cMinShare, side > 0 ? cMinPx / side : 1));
  return { minW: min(shown.w), minH: min(shown.h) };
}

/** A shape's width over its height in pixels, for a picture of size `pic`
 *  (turned as shown); null for Free. 4:3 and 16:9 lie as the picture does
 *  (a square one counts as lying down). */
export function aspectRatio(a: Aspect, pic: Size): number | null {
  const upright = pic.h > pic.w;
  switch (a) {
    case "free": return null;
    case "original": return pic.w / pic.h;
    case "square": return 1;
    case "4:3": return upright ? 3 / 4 : 4 / 3;
    case "16:9": return upright ? 9 / 16 : 16 / 9;
  }
}

/** The same in fractions of the picture: a frame w/h pixels wide over a
 *  W x H picture is (w/W)/(h/H) in fractions. */
export function frameRatio(a: Aspect, pic: Size): number | null {
  const r = aspectRatio(a, pic);
  return r == null ? null : (r * pic.h) / pic.w;
}

/** The largest frame of fraction ratio `ratio` (width over height) the
 *  picture holds, centred where `r` is and moved inside. */
export function fitRatio(r: Rect, ratio: number): Rect {
  const w = ratio >= 1 ? 1 : ratio;
  const h = ratio >= 1 ? 1 / ratio : 1;
  const cx = r.x + r.w / 2;
  const cy = r.y + r.h / 2;
  return { x: clamp(cx - w / 2, 0, 1 - w), y: clamp(cy - h / 2, 0, 1 - h), w, h };
}

/** The frame dragged by (dx, dy) fractions, its size kept, stopping at the
 *  picture's edges. */
export function moveRect(r: Rect, dx: number, dy: number): Rect {
  return { x: clamp(r.x + dx, 0, Math.max(0, 1 - r.w)), y: clamp(r.y + dy, 0, Math.max(0, 1 - r.h)), w: r.w, h: r.h };
}

/** The frame as it was when a drag of `handle` began (`start`), with the
 *  pointer moved (dx, dy) fractions since. Free: each side held follows,
 *  never past the picture's edge nor closer to the one across than the
 *  limits. With a shape (`ratio`, fractions, width over height): a corner
 *  keeps the corner across it where it was and grows or shrinks the frame
 *  as far as the pointer went along either side; an edge keeps the edge
 *  across it and the other two sides follow, centred where they were and
 *  moved inside. */
export function resizeRect(start: Rect, handle: Handle, dx: number, dy: number, ratio: number | null, lim: Limits): Rect {
  const left = start.x;
  const top = start.y;
  const right = start.x + start.w;
  const bottom = start.y + start.h;
  const east = handle.includes("e");
  const west = handle.includes("w");
  const north = handle.includes("n");
  const south = handle.includes("s");
  if (ratio == null) {
    let l = left;
    let t = top;
    let r = right;
    let b = bottom;
    if (west) l = clamp(left + dx, 0, Math.max(0, right - lim.minW));
    if (east) r = clamp(right + dx, Math.min(1, left + lim.minW), 1);
    if (north) t = clamp(top + dy, 0, Math.max(0, bottom - lim.minH));
    if (south) b = clamp(bottom + dy, Math.min(1, top + lim.minH), 1);
    return { x: l, y: t, w: r - l, h: b - t };
  }
  // The smallest height that keeps both sides at their limits.
  const minH = Math.max(lim.minH, lim.minW / ratio);
  if ((east || west) && (north || south)) {
    const ax = west ? right : left;
    const ay = north ? bottom : top;
    const maxH = Math.min(north ? ay : 1 - ay, (west ? ax : 1 - ax) / ratio);
    const wanted = Math.max(start.h + (south ? dy : -dy), (start.w + (east ? dx : -dx)) / ratio);
    const h = clamp(wanted, Math.min(minH, maxH), maxH);
    const w = h * ratio;
    return { x: west ? ax - w : ax, y: north ? ay - h : ay, w, h };
  }
  if (east || west) {
    const ax = west ? right : left;
    // As wide as the room on that side, and no taller than the picture.
    const maxW = Math.min(west ? ax : 1 - ax, ratio);
    const w = clamp(start.w + (east ? dx : -dx), Math.min(minH * ratio, maxW), maxW);
    const h = w / ratio;
    const cy = start.y + start.h / 2;
    return { x: west ? ax - w : ax, y: clamp(cy - h / 2, 0, 1 - h), w, h };
  }
  const ay = north ? bottom : top;
  // As tall as the room on that side, and no wider than the picture.
  const maxH = Math.min(north ? ay : 1 - ay, 1 / ratio);
  const h = clamp(start.h + (south ? dy : -dy), Math.min(minH, maxH), maxH);
  const w = h * ratio;
  const cx = start.x + start.w / 2;
  return { x: clamp(cx - w / 2, 0, 1 - w), y: north ? ay - h : ay, w, h };
}

/** Whether the frame is the whole photo (nothing cropped). */
export const isWhole = (r: Rect): boolean =>
  Math.abs(r.x) < cEps && Math.abs(r.y) < cEps && Math.abs(r.w - 1) < cEps && Math.abs(r.h - 1) < cEps;

/** What the editor holds: the quarter turns (clockwise, 0 to 3), the frame
 *  over the photo so turned, and the shape it keeps. */
export type EditView = { turns: number; rect: Rect; aspect: Aspect };

/** The photo as it opened: not turned, not cropped, any shape. */
export const cStartView: EditView = { turns: 0, rect: cWhole, aspect: "free" };

/** A quarter turn, clockwise (1) or anticlockwise (-1), of a photo whose
 *  own size (shown upright, not yet turned) is `photo`: the frame turns
 *  with it. A shape lies the other way with it - except 4:3 and 16:9 over
 *  a square photo, which lie down whichever way it is turned: the frame is
 *  fitted again then. */
export function rotateView(v: EditView, dir: 1 | -1, photo: Size): EditView {
  const turns = (((v.turns + dir) % 4) + 4) % 4;
  let rect = turnRect(v.rect, dir);
  const ratio = frameRatio(v.aspect, turnedSize(photo, turns));
  if (ratio != null && Math.abs(rect.w / rect.h - ratio) > 1e-3 * ratio) rect = fitRatio(rect, ratio);
  return { ...v, turns, rect };
}

/** A shape chosen from the menu: Free keeps the frame as it is; any other
 *  gives the largest frame of that shape, centred where it was. */
export function aspectView(v: EditView, a: Aspect, photo: Size): EditView {
  const ratio = frameRatio(a, turnedSize(photo, v.turns));
  return { ...v, aspect: a, rect: ratio == null ? v.rect : fitRatio(v.rect, ratio) };
}

/** Whether there is anything to save: a turn (four make none) or a crop. */
export const hasChanges = (v: EditView): boolean => v.turns % 4 !== 0 || !isWhole(v.rect);

/** EditCrop's fields for the frame, kept inside the photo; undefined for
 *  the whole photo (the device keeps it whole). */
export function cropOf(r: Rect): { x: number; y: number; width: number; height: number } | undefined {
  if (isWhole(r)) return undefined;
  const x = clamp(r.x, 0, 1);
  const y = clamp(r.y, 0, 1);
  return { x, y, width: clamp(r.w, 0, 1 - x), height: clamp(r.h, 0, 1 - y) };
}

/** Where the picture goes on screen: the turned photo fitted into `room`
 *  (both in pixels), centred, scaled up too - a thumbnail fills the screen
 *  as the full size will. */
export function fitPicture(photo: Size, turns: number, room: Size): { x: number; y: number; w: number; h: number } {
  const pic = turnedSize(photo, turns);
  if (pic.w <= 0 || pic.h <= 0 || room.w <= 0 || room.h <= 0) return { x: 0, y: 0, w: 0, h: 0 };
  const scale = Math.min(room.w / pic.w, room.h / pic.h);
  const w = pic.w * scale;
  const h = pic.h * scale;
  return { x: (room.w - w) / 2, y: (room.h - h) / 2, w, h };
}
