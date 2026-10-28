// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour's rules (Tour.tsx draws it, App.tsx opens it and
// shows each step's section; whether it opens by itself is tourSeen.ts):
// its steps in order and moving between them, the section each step shows,
// the real controls it rings there, the holes the dimmed layer leaves
// around them, and where the card goes so it covers none of them and its
// text is never cut (after what scroll, with which drawing). Pure -
// no React, no DOM - so node web/scripts/tour-test.mjs tests it. Loaded
// with the tour, never in the app's first chunk.
import type { TabKey } from "./nav";

/** A step, by id: the tour keeps its place by id, so People can come or go while it is open. */
export type TourStepId =
  | "welcome"
  | "search"
  | "dates"
  | "select"
  | "viewer"
  | "people"
  | "collections"
  | "files"
  | "upload"
  | "social"
  | "post"
  | "friends"
  | "users"
  | "alerts";

// Every step, in the order shown.
const cAllSteps: readonly TourStepId[] = [
  "welcome", "search", "dates", "select", "viewer", "people", "collections", "files", "upload", "social", "post", "friends", "users", "alerts",
];

/**
 * The steps shown: People only while face recognition is on (as the
 * menu's item), Users only to the device's owner - the primary instance's,
 * the one Settings › Users shows to (UsersPanel). Unknown means hidden.
 */
export function tourSteps(faces: boolean | null, owner: boolean | null): TourStepId[] {
  return cAllSteps.filter((id) => (id !== "people" || faces === true) && (id !== "users" || owner === true));
}

const orderOf = (id: TourStepId) => cAllSteps.indexOf(id);

/** The step after id among steps (id itself need not be one of them); null after the last. */
export function stepAfter(id: TourStepId, steps: readonly TourStepId[]): TourStepId | null {
  return steps.find((s) => orderOf(s) > orderOf(id)) ?? null;
}

/** The step before id among steps; null before the first. */
export function stepBefore(id: TourStepId, steps: readonly TourStepId[]): TourStepId | null {
  for (let i = steps.length - 1; i >= 0; i--) if (orderOf(steps[i]) < orderOf(id)) return steps[i];
  return null;
}

/** Where the tour stands: id, or - when its step went (People, face recognition turned off) - the next one, else the one before. */
export function stepPlace(id: TourStepId, steps: readonly TourStepId[]): TourStepId {
  if (steps.includes(id)) return id;
  return stepAfter(id, steps) ?? stepBefore(id, steps) ?? steps[0] ?? "welcome";
}

/**
 * The section each step shows, through App (never the menu's own way in,
 * which would clear the search or a Files search): null for the welcome,
 * which stays on the section the tour opened over. The last step, Alerts
 * and Settings, shows Settings: Alerts marks every alert read as it opens,
 * and the tour changes nothing on the device.
 */
export const cStepSection: Readonly<Record<TourStepId, TabKey | null>> = {
  welcome: null,
  search: "PhotoGallery",
  dates: "PhotoGallery",
  select: "PhotoGallery",
  viewer: "PhotoGallery",
  people: "People",
  collections: "Collections",
  files: "AdminPannel",
  upload: "AdminPannel",
  social: "Social",
  post: "Social",
  friends: "Friends",
  users: "Settings",
  alerts: "Settings",
};

/**
 * What each step rings when it is on screen (Tour.tsx looks them up): the
 * first group that finds a control there, each control found ringed with
 * a hole of its own. A selector is the first element it matches that is
 * on screen. The menu's items only in the menu beside the page (.sb-dock):
 * the drawer is closed and inert while the tour shows. With an empty
 * library most of these aren't there: the card's drawing shows the
 * control instead.
 */
export const cStepTargets: Readonly<Record<TourStepId, readonly (readonly string[])[]>> = {
  welcome: [['.sb-dock[data-tour="menu"]'], ['[data-tour="menu-button"]']],
  search: [[".ts-field"]],
  // A mouse's column of years; a finger's handle only while it shows.
  dates: [[".ds-root.ds-fine"], [".ds-root.ds-touch.is-shown .ds-handle"]], // i18n-ignore: CSS selectors
  // The first photo, its circle shown (Tour.css: data-tour-lit).
  select: [[".pg-grid .pg-tile:not(.pg-tile-skel)"]], // i18n-ignore: CSS selectors
  viewer: [[".pg-grid .pg-tile-open"]], // i18n-ignore: CSS selectors
  people: [[".pv-grid .pv-person:not([aria-hidden])"]], // i18n-ignore: CSS selectors
  collections: [[".cv-grid .cv-card"]], // i18n-ignore: CSS selectors
  // The view switch, and a column's heading where the list has them, else Sort by.
  files: [[".fb-viewmode", ".fb-contents .fb-head .fb-sort-btn"], [".fb-viewmode", ".fb-sort"]], // i18n-ignore: CSS selectors
  // A row's (or a tile's) tick, and the line saying files can be dragged here.
  upload: [['.fb-contents .fb-row:not([data-key=".."]) .c-check', ".fb-tip"], [".fb-contents .fb-tile-check", ".fb-tip"], [".fb-tip"]], // i18n-ignore: CSS selectors
  social: [[".sv-post .sv-actions .sv-btn", ".sv-post .sv-newcomment"]], // i18n-ignore: CSS selectors
  post: [['[data-tour="new-post"]'], [".sv-empty-new"]],
  friends: [[".fr-address", ".add-friend"], [".add-friend"]],
  // The name field and Add User, and the bridge box under them - once the
  // list above them is in, so they don't move after the scroll.
  users: [[".up-list ~ .up-add-row", ".up-list ~ .up-bridge-opt"]], // i18n-ignore: CSS selectors
  alerts: [['[data-tour="show-tour"]', '.sb-dock [data-tour="Notifications"]', '.sb-dock [data-tour="Settings"]'], ['[data-tour="show-tour"]']],
};

/** A box in the window's coordinates (getBoundingClientRect's). */
export interface Box {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

/** A width and a height. */
export interface Size {
  width: number;
  height: number;
}

/** The hole's margin around its controls, in px. */
export const cHolePad = 6;
// Kept this far inside the window, so the 2px ring shows whole.
const cHoleInset = 2;
// The corner of a hole around something square (a rail item, the menu).
const cHoleCorner = 14;

const onScreen = (b: Box, width: number, height: number) =>
  b.right > b.left && b.bottom > b.top && b.right > 0 && b.bottom > 0 && b.left < width && b.top < height;

const overlaps = (a: Box, b: Box) => a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;

const union = (boxes: readonly Box[]): Box => ({
  left: Math.min(...boxes.map((b) => b.left)),
  top: Math.min(...boxes.map((b) => b.top)),
  right: Math.max(...boxes.map((b) => b.right)),
  bottom: Math.max(...boxes.map((b) => b.bottom)),
});

// A box cHolePad around, kept inside the window; null when nothing is left.
function padded(b: Box, width: number, height: number): Box | null {
  const left = Math.max(cHoleInset, b.left - cHolePad);
  const top = Math.max(cHoleInset, b.top - cHolePad);
  const right = Math.min(width - cHoleInset, b.right + cHolePad);
  const bottom = Math.min(height - cHoleInset, b.bottom + cHolePad);
  return right > left && bottom > top ? { left, top, right, bottom } : null;
}

/**
 * The hole the dimmed layer leaves around some controls together: their
 * boxes, cHolePad around, kept inside the window. null when none of them
 * is on screen: no hole and no ring.
 */
export function holeFor(boxes: readonly Box[], width: number, height: number): Box | null {
  const shown = boxes.filter((b) => onScreen(b, width, height));
  if (!shown.length || width <= 2 * cHoleInset || height <= 2 * cHoleInset) return null;
  return padded(union(shown), width, height);
}

/**
 * The holes for a step's controls, one each (cHolePad around, inside the
 * window), those on screen only; holes that would touch are one, around
 * both - the dimmed layer is drawn even-odd, where two holes over each
 * other would dim their overlap again.
 */
export function holesFor(boxes: readonly Box[], width: number, height: number): Box[] {
  if (width <= 2 * cHoleInset || height <= 2 * cHoleInset) return [];
  let holes = boxes
    .filter((b) => onScreen(b, width, height))
    .map((b) => padded(b, width, height))
    .filter((b): b is Box => !!b);
  // Merged until no two touch (a merge can make a box reach a third).
  for (let merged = true; merged;) {
    merged = false;
    outer: for (let i = 0; i < holes.length; i++) {
      for (let j = i + 1; j < holes.length; j++) {
        const a = holes[i];
        const b = holes[j];
        if (a.left <= b.right && b.left <= a.right && a.top <= b.bottom && b.top <= a.bottom) {
          holes = [...holes.slice(0, i), union([a, b]), ...holes.slice(i + 1, j), ...holes.slice(j + 1)];
          merged = true;
          break outer;
        }
      }
    }
  }
  return holes;
}

/** The hole's corner radius: round around a round control (the account picture, a pill), else slightly rounded. */
export function holeRadius(hole: Box, round: boolean): number {
  const half = Math.min(hole.right - hole.left, hole.bottom - hole.top) / 2;
  return round ? half : Math.min(cHoleCorner, half);
}

const n = (v: number) => String(Math.round(v * 100) / 100);

/** A rounded box as an SVG path, clockwise. */
export function roundedBoxPath(b: Box, radius: number): string {
  const r = Math.max(0, Math.min(radius, (b.right - b.left) / 2, (b.bottom - b.top) / 2));
  const { left: l, top: t, right: rt, bottom: bm } = b;
  if (r === 0) return `M${n(l)} ${n(t)}H${n(rt)}V${n(bm)}H${n(l)}Z`;
  return (
    `M${n(l + r)} ${n(t)}H${n(rt - r)}A${n(r)} ${n(r)} 0 0 1 ${n(rt)} ${n(t + r)}` +
    `V${n(bm - r)}A${n(r)} ${n(r)} 0 0 1 ${n(rt - r)} ${n(bm)}` +
    `H${n(l + r)}A${n(r)} ${n(r)} 0 0 1 ${n(l)} ${n(bm - r)}` +
    `V${n(t + r)}A${n(r)} ${n(r)} 0 0 1 ${n(l + r)} ${n(t)}Z`
  );
}

/** A hole and its corner radius. */
export interface Hole {
  box: Box;
  radius: number;
}

/** The dimmed layer: the whole window, less the holes (drawn with fill-rule evenodd; holesFor keeps them apart). */
export function dimPath(width: number, height: number, holes: readonly Hole[]): string {
  return `M0 0H${n(width)}V${n(height)}H0Z` + holes.map((h) => roundedBoxPath(h.box, h.radius)).join("");
}

/** Whether a point (a click's clientX/clientY) is inside a hole. */
export function insideHole(holes: readonly Box[], x: number, y: number): boolean {
  return holes.some((h) => x >= h.left && x <= h.right && y >= h.top && y <= h.bottom);
}

/** Between the card and a hole beside it, in px. */
export const cCardGap = 16;
/** Between the card and the window's edges, in px. */
export const cCardMargin = 16;

/** Where the card goes: its top left corner. It is never cut: its text never scrolls where it can help it. */
export interface CardPlace {
  left: number;
  top: number;
}

// The card's room in the window: kept cCardMargin inside its edges.
function cardRoom(card: Size, view: Size) {
  const m = cCardMargin;
  const clampLeft = (l: number) => Math.max(m, Math.min(l, view.width - m - card.width));
  const clampTop = (t: number) => Math.max(m, Math.min(t, view.height - m - card.height));
  const fits = (p: CardPlace) =>
    p.left >= m - 0.5 && p.top >= m - 0.5 && p.left + card.width <= view.width - m + 0.5 && p.top + card.height <= view.height - m + 0.5;
  const middle = { left: clampLeft((view.width - card.width) / 2), top: clampTop((view.height - card.height) / 2) };
  return { m, clampLeft, clampTop, fits, middle };
}

// How much of the holes the card covers there, in px².
function coveredBy(holes: readonly Box[], card: Size, p: CardPlace): number {
  const b = { left: p.left, top: p.top, right: p.left + card.width, bottom: p.top + card.height };
  return holes.reduce((sum, h) => {
    const w = Math.min(b.right, h.right) - Math.max(b.left, h.left);
    const hh = Math.min(b.bottom, h.bottom) - Math.max(b.top, h.top);
    return sum + (w > 0 && hh > 0 ? w * hh : 0);
  }, 0);
}

/**
 * The card beside the holes, whole: to the right, below, to the left or
 * above them - the side the window leaves most room on first - inside the
 * window and covering none of them. null when no side has the room.
 */
export function cardBeside(holes: readonly Box[], card: Size, view: Size): CardPlace | null {
  if (!holes.length) return null;
  const { m, clampLeft, clampTop, fits } = cardRoom(card, view);
  const u = union(holes);
  const midX = (u.left + u.right) / 2;
  const midY = (u.top + u.bottom) / 2;
  // Each side, with the room the window leaves there for the card (as a
  // share of the card's own width or height): the roomiest first.
  const sides = [
    { room: (view.width - u.right - cCardGap - m) / card.width, at: { left: u.right + cCardGap, top: clampTop(midY - card.height / 2) } },
    { room: (view.height - u.bottom - cCardGap - m) / card.height, at: { left: clampLeft(midX - card.width / 2), top: u.bottom + cCardGap } },
    { room: (u.left - cCardGap - m) / card.width, at: { left: u.left - cCardGap - card.width, top: clampTop(midY - card.height / 2) } },
    { room: (u.top - cCardGap - m) / card.height, at: { left: clampLeft(midX - card.width / 2), top: u.top - cCardGap - card.height } },
  ].sort((a, b) => b.room - a.room);
  for (const side of sides) if (fits(side.at) && coveredBy(holes, card, side.at) === 0) return side.at;
  return null;
}

/**
 * Where the card goes in a window at least 600px wide (a phone's is a
 * sheet at the foot), card being its whole height: in the middle when
 * nothing is ringed; else beside the holes (cardBeside); and when no side
 * has the room, the corner or middle of the window that covers the least
 * of them. Never cut, never past the window's edges where it can help it.
 */
export function placeCard(holes: readonly Box[], card: Size, view: Size): CardPlace {
  const { m, clampLeft, clampTop, middle } = cardRoom(card, view);
  if (!holes.length) return middle;
  const beside = cardBeside(holes, card, view);
  if (beside) return beside;
  const right = view.width - m - card.width;
  const bottom = view.height - m - card.height;
  const spots = [
    { left: clampLeft(right), top: clampTop(bottom) },
    { left: clampLeft(m), top: clampTop(bottom) },
    { left: clampLeft(right), top: clampTop(m) },
    { left: clampLeft(m), top: clampTop(m) },
    middle,
  ];
  let best = spots[0];
  let least = coveredBy(holes, card, best);
  for (const p of spots.slice(1)) {
    const c = coveredBy(holes, card, p);
    if (c < least) { best = p; least = c; }
  }
  return best;
}

/** What planCard weighs. */
export interface CardPlanInput {
  /** The step's controls that stay put as the page scrolls (in the top bar, the menu beside the page). */
  fixed: readonly Box[];
  /** The ones that scroll with the page, in the step's order. */
  moving: readonly Box[];
  /** The card, whole, with each size of its drawing in turn, the largest first. */
  cards: readonly Size[];
  /** The tallest the card may be (Tour.css): any taller and its text would scroll. */
  maxHeight: number;
  view: Size;
  /** Where a control the page is scrolled to may be: below the top bar, above the window's foot. */
  band: { top: number; bottom: number };
  /** How far the page can still scroll up and down from where it is, in px (none: it doesn't scroll now). */
  scroll: { up: number; down: number };
}

/** Where the card goes, with which drawing, after what scroll. */
export interface CardPlan {
  /** Which of the card's sizes (CardPlanInput.cards): its drawing's. */
  art: number;
  /** How far to scroll the page first (window.scrollBy's top), 0 for not at all. */
  scroll: number;
  /** Where the card goes, once the page has scrolled. */
  place: CardPlace;
}

const shiftBox = (b: Box, by: number): Box => ({ left: b.left, right: b.right, top: b.top - by, bottom: b.bottom - by });

// The scrolls that show the step's moving controls in the band - all of
// them when they fit in it together, else the first - and the one that
// brings them to its middle (scrollToShow). null: none moves.
function shownBy(moving: readonly Box[], band: { top: number; bottom: number }): { range: [number, number]; middle: number } | null {
  if (!moving.length) return null;
  const all = union(moving);
  const room = band.bottom - band.top;
  const need = all.bottom - all.top <= room ? all : moving[0];
  const middle = need.top - (band.top + (room - (need.bottom - need.top)) / 2);
  if (need.bottom - need.top >= room) return { range: [need.top - band.top, need.top - band.top], middle: need.top - band.top };
  return { range: [need.bottom - band.bottom, need.top - band.top], middle };
}

// Every few px of a range is worth a look, and these besides.
const cScrollStep = 4;

/**
 * Where the card goes in a window at least 600px wide, so its text is
 * never cut: beside the step's controls, whole, with the largest drawing
 * it can have - first as the page is, then scrolled so the card fits
 * whole beside them, for example the controls high up and the card below
 * (the moving controls kept in the band; as little scroll as it takes
 * when they show already, else as near as it can be to the one that
 * brings them to the band's middle);
 * then in a corner that covers none of them; and only then with a
 * smaller drawing (compact, then none), the same way. When nothing works
 * the card, with the largest drawing that keeps its text whole, goes
 * where it covers the least of them, over the dimmed page rather than
 * cut. Nothing ringed: in the middle.
 */
export function planCard(p: CardPlanInput): CardPlan {
  const { fixed, moving, cards, view, band } = p;
  const fitsWhole = (c: Size) => c.height <= Math.min(p.maxHeight, view.height - 2 * cCardMargin) + 0.5;
  const firstWhole = Math.max(0, cards.findIndex(fitsWhole));
  const largest = cards.findIndex(fitsWhole) < 0 ? cards.length - 1 : firstWhole;
  if (!fixed.length && !moving.length) return { art: largest, scroll: 0, place: cardRoom(cards[largest], view).middle };
  // The scrolls worth a look, the least first.
  const lo0 = -Math.max(0, p.scroll.up);
  const hi0 = Math.max(0, p.scroll.down);
  const shown = shownBy(moving, band);
  let scrolls: number[];
  if (!shown) scrolls = [0];
  else {
    const lo = Math.max(shown.range[0], lo0);
    const hi = Math.min(shown.range[1], hi0);
    if (lo > hi) scrolls = [shown.range[1] < lo0 ? lo0 : hi0]; // as near as the page goes
    else {
      const set = new Set<number>([lo, hi]);
      if (lo <= 0 && 0 <= hi) set.add(0);
      for (let d = Math.ceil(lo / cScrollStep) * cScrollStep; d <= hi; d += cScrollStep) set.add(d);
      // Exactly where the card just fits below or above them.
      const u = union(moving);
      for (const c of cards) {
        const below = u.bottom + cHolePad + cCardGap + c.height - (view.height - cCardMargin);
        const above = u.top - cHolePad - cCardGap - c.height - cCardMargin;
        for (const d of [Math.ceil(below), Math.floor(above)]) if (d >= lo && d <= hi) set.add(d);
      }
      // None when they show already, else as near their middle as it can be.
      const want = lo <= 0 && 0 <= hi ? 0 : Math.max(lo, Math.min(hi, shown.middle));
      scrolls = [...set].sort((a, b) => Math.abs(a - want) - Math.abs(b - want) || Math.abs(a) - Math.abs(b));
    }
  }
  const holesAt = (d: number) => holesFor([...fixed, ...moving.map((b) => shiftBox(b, d))], view.width, view.height);
  const nearest = scrolls[0];
  for (let art = firstWhole; art < cards.length; art++) {
    const card = cards[art];
    if (!fitsWhole(card)) continue;
    for (const d of scrolls) {
      const beside = cardBeside(holesAt(d), card, view);
      if (beside) return { art, scroll: d, place: beside };
    }
    // Nowhere beside them: a corner (or the middle) that covers none of them.
    const holes = holesAt(nearest);
    const spot = placeCard(holes, card, view);
    if (coveredBy(holes, card, spot) === 0) return { art, scroll: nearest, place: spot };
  }
  return { art: largest, scroll: nearest, place: placeCard(holesAt(nearest), cards[largest], view) };
}

/**
 * How far to scroll the window (a delta for scrollBy) to bring a box into
 * the band of it between top and bottom - below the top bar, above a
 * phone's sheet: 0 when it is in it already; else its middle to the band's
 * middle, or its top to the band's top when it is taller than the band.
 */
export function scrollToShow(box: Box, top: number, bottom: number): number {
  if (bottom <= top) return 0;
  if (box.top >= top && box.bottom <= bottom) return 0;
  const h = box.bottom - box.top;
  if (h >= bottom - top) return box.top - top;
  return box.top - (top + (bottom - top - h) / 2);
}

/** Whether two boxes overlap (a card over a hole). */
export function boxesOverlap(a: Box, b: Box): boolean {
  return overlaps(a, b);
}
