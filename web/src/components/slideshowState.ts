// SPDX-License-Identifier: AGPL-3.0-or-later

// Which slideshow is open (Slideshow.tsx, over the app: App mounts it):
// a collection's - its Slideshow button in Images - or the whole
// library's - the top bar's in Images. App also reads it to keep the
// unreachable screen off it for a moment the device is away: the show
// goes on with what it has (slideshowShowing).

import { useSyncExternalStore } from "react";

export interface SlideshowOpen {
  // A collection's id, or "" for every photo of the library.
  groupId: string;
  // The collection's name as shown ("" for the library).
  name: string;
}

let open: SlideshowOpen | null = null;
const listeners = new Set<() => void>();
const emit = () => { for (const fn of listeners) fn(); };

export function openSlideshow(show: SlideshowOpen): void {
  open = { ...show };
  emit();
}

export function closeSlideshow(): void {
  if (!open) return;
  open = null;
  emit();
}

export function subscribeSlideshow(fn: () => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

export const slideshowShowing = (): SlideshowOpen | null => open;

export function useSlideshow(): SlideshowOpen | null {
  return useSyncExternalStore(subscribeSlideshow, slideshowShowing);
}
