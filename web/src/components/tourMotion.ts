// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The tour's motion (Tour.tsx, TourArt.tsx): whether the person asked for
// less of it, whether the pointer is a finger, and the timer a drawing's
// states step through.
import { useEffect, useState, useSyncExternalStore } from "react";

const cReduceQuery = "(prefers-reduced-motion: reduce)"; // i18n-ignore: a media query
const cCoarseQuery = "(pointer: coarse)"; // i18n-ignore: a media query

function mediaStore(query: string) {
  return {
    subscribe(on: () => void) {
      const mq = window.matchMedia(query);
      mq.addEventListener("change", on);
      return () => mq.removeEventListener("change", on);
    },
    get: () => window.matchMedia(query).matches,
  };
}
const reduceStore = mediaStore(cReduceQuery);
const coarseStore = mediaStore(cCoarseQuery);

/** Whether the main pointer is a finger (the drawings show one instead of the mouse). */
export function useCoarsePointer(): boolean {
  return useSyncExternalStore(coarseStore.subscribe, coarseStore.get);
}

/** Whether the person asked for less motion (the drawings then stand still). */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(reduceStore.subscribe, reduceStore.get);
}

/**
 * The state a scene shows: 0, 1, ... each for its time in `times` (ms),
 * then round again; `still` for good with reduced motion. `times` is a
 * module-level constant.
 */
export function useScene(times: readonly number[], still: number): number {
  const reduced = useReducedMotion();
  const [at, setAt] = useState(0);
  useEffect(() => {
    if (reduced) return;
    const id = window.setTimeout(() => setAt((p) => (p + 1) % times.length), times[at]);
    return () => window.clearTimeout(id);
  }, [at, reduced, times]);
  return reduced ? still : at;
}
