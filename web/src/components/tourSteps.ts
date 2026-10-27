// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour's rules (Tour.tsx draws it, App.tsx opens it): its
// steps in order, moving between them, whether it opens by itself, and the
// hole the dimmed layer leaves around a step's control. Pure - no React,
// no DOM - so node web/scripts/tour-test.mjs tests it. The phones' tour
// (TourRules in Tour.swift and ui/Tour.kt) follows the same rules with its
// own steps.

/** A step, by id: the tour keeps its place by id, so People can come or go while it is open. */
export type TourStepId =
  | "welcome"
  | "menu"
  | "images"
  | "people"
  | "collections"
  | "files"
  | "social"
  | "post"
  | "friends"
  | "alerts"
  | "done";

// Every step, in the order shown.
const cAllSteps: readonly TourStepId[] = [
  "welcome", "menu", "images", "people", "collections", "files", "social", "post", "friends", "alerts", "done",
];

/** Where this browser keeps the version of the tour it has seen (only here, never on the device). */
export const cTourKey = "otc_tour_seen";
/** Raised only to show a rewritten tour again to everyone. */
export const cTourVersion = 1;

/** The steps shown: People only while face recognition is on (as the menu's item; unknown means hidden). */
export function tourSteps(faces: boolean | null): TourStepId[] {
  return cAllSteps.filter((id) => id !== "people" || faces === true);
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

/** The storage the seen flag lives in (localStorage). */
export interface TourStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/**
 * Whether the tour opens by itself now: this browser hasn't seen this
 * version of it. It is marked seen here, before it shows, so Skip, Done,
 * closing the tab or a crash all leave it seen; a replay never comes here.
 * Storage that can't be read or written (private modes, blocked site data)
 * means no tour by itself, rather than one at every load.
 */
export function claimAutoTour(storage: () => TourStorage | null | undefined): boolean {
  try {
    const s = storage();
    if (!s) return false;
    const seen = Number.parseInt(s.getItem(cTourKey) ?? "", 10);
    if (Number.isFinite(seen) && seen >= cTourVersion) return false;
    s.setItem(cTourKey, String(cTourVersion));
    return true;
  } catch {
    return false;
  }
}

/**
 * What each step rings when it is on screen (Tour.tsx looks them up): the
 * first group that finds a control there, its controls ringed together.
 * The menu's items only in the menu beside the page (.sb-dock): the
 * drawer is closed and inert while the tour shows.
 */
export const cStepTargets: Readonly<Record<TourStepId, readonly (readonly string[])[]>> = {
  welcome: [],
  menu: [['.sb-dock[data-tour="menu"]'], ['[data-tour="menu-button"]']],
  images: [['.sb-dock [data-tour="PhotoGallery"]']],
  people: [['.sb-dock [data-tour="People"]']],
  collections: [['.sb-dock [data-tour="Collections"]']],
  files: [['.sb-dock [data-tour="AdminPannel"]']],
  social: [['.sb-dock [data-tour="Social"]']],
  post: [['[data-tour="new-post"]']],
  friends: [['.sb-dock [data-tour="Friends"]']],
  alerts: [['.sb-dock [data-tour="Notifications"]', '.sb-dock [data-tour="Settings"]']],
  done: [['[data-tour="account"]']],
};

/** A box in the window's coordinates (getBoundingClientRect's). */
export interface Box {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

/** The hole's margin around its controls, in px. */
export const cHolePad = 6;
// Kept this far inside the window, so the 2px ring shows whole.
const cHoleInset = 2;
// The corner of a hole around something square (a rail item, the menu).
const cHoleCorner = 14;

/**
 * The hole the dimmed layer leaves for a step: the boxes of its controls
 * together, cHolePad around, kept inside the window. null when none of
 * them is on screen: no hole and no ring.
 */
export function holeFor(boxes: readonly Box[], width: number, height: number): Box | null {
  const shown = boxes.filter((b) => b.right > b.left && b.bottom > b.top && b.right > 0 && b.bottom > 0 && b.left < width && b.top < height);
  if (!shown.length || width <= 2 * cHoleInset || height <= 2 * cHoleInset) return null;
  const left = Math.max(cHoleInset, Math.min(...shown.map((b) => b.left)) - cHolePad);
  const top = Math.max(cHoleInset, Math.min(...shown.map((b) => b.top)) - cHolePad);
  const right = Math.min(width - cHoleInset, Math.max(...shown.map((b) => b.right)) + cHolePad);
  const bottom = Math.min(height - cHoleInset, Math.max(...shown.map((b) => b.bottom)) + cHolePad);
  if (right <= left || bottom <= top) return null;
  return { left, top, right, bottom };
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

/** The dimmed layer: the whole window, less the hole (drawn with fill-rule evenodd). */
export function dimPath(width: number, height: number, hole: Box | null, radius: number): string {
  const all = `M0 0H${n(width)}V${n(height)}H0Z`;
  return hole ? all + roundedBoxPath(hole, radius) : all;
}

/** Whether a point (a click's clientX/clientY) is inside the hole. */
export function insideHole(hole: Box | null, x: number, y: number): boolean {
  return !!hole && x >= hole.left && x <= hole.right && y >= hole.top && y <= hole.bottom;
}
