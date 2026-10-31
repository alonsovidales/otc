// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The page's Favourites (favourites.ts) on the device's socket, and what
// the viewer reads of them in a render: the heart of one path, and whether
// the device has Favourites at all. Kept for the page's session (signing
// out reloads the page).

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope } from "../proto/messages";
import { createFavourites } from "./favourites";

export const favourites = createFavourites((payload) =>
  useWS.request((e: Partial<ReqEnvelope>) => { e.payload = payload; })
);

const subscribe = (l: () => void) => favourites.subscribe(l);

/** Whether the photo at `path` is a favourite; undefined until the device
 *  said (or without a path). */
export function useFavourite(path: string | undefined): boolean | undefined {
  return useSyncExternalStore(subscribe, () => (path ? favourites.get(path) : undefined));
}

/** The device has no Favourites: no heart. */
export function useNoFavourites(): boolean {
  return useSyncExternalStore(subscribe, favourites.unsupported);
}
