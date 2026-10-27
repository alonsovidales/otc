// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Whether the first-run tour opens by itself (App.tsx asks; the tour
// itself, Tour.tsx and its rules in tourSteps.ts, is loaded only when it
// opens). Pure - no React, no DOM - so node web/scripts/tour-test.mjs
// tests it.

/** Where this browser keeps the version of the tour it has seen (only here, never on the device). */
export const cTourKey = "otc_tour_seen";
/** Raised only to show a rewritten tour again to everyone: 2 is the tour that shows how things work (v1 said what each section was for). */
export const cTourVersion = 2;

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
