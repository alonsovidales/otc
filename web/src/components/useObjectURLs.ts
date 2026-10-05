// SPDX-License-Identifier: AGPL-3.0-or-later

// Object URLs for a grid's thumbnails, one per item. Minting them inline
// during render (bytes -> Blob -> URL.createObjectURL) copied every loaded
// thumbnail into a new Blob on each render - every keystroke, tick of a
// checkbox or step of the viewer - and freed none of them, so a large
// library grew the tab by hundreds of MB, and each new src made the browser
// reload and decode the whole grid again.
import { useCallback, useEffect, useRef } from "react";

/**
 * Returns urlFor(item): the item's object URL, made by make(item) the first
 * time it is asked for and kept while the item is in `live`. Keyed by the
 * item object itself, so a reply that repeats a path never shares or swaps
 * another tile's URL. URLs of items that have left `live` are revoked after
 * the commit that dropped them (when their <img> is already gone), and all
 * of them on unmount. make must be stable (a module-level function), and
 * urlFor must only be called for items in the `live` being rendered.
 */
export function useObjectURLs<T extends object>(live: readonly T[], make: (item: T) => string): (item: T) => string {
  const urls = useRef<Map<T, string>>(new Map());

  const urlFor = useCallback((item: T) => {
    let u = urls.current.get(item);
    // undefined, not falsy: an item with no thumbnail ("") is remembered
    // too rather than retried on every render.
    if (u === undefined) {
      u = make(item);
      urls.current.set(item, u);
    }
    return u;
  }, [make]);

  useEffect(() => {
    const keep = new Set(live);
    urls.current.forEach((u, item) => {
      if (keep.has(item)) return;
      if (u) URL.revokeObjectURL(u);
      urls.current.delete(item);
    });
  }, [live]);

  useEffect(() => {
    const map = urls.current;
    return () => {
      map.forEach(u => { if (u) URL.revokeObjectURL(u); });
      map.clear();
    };
  }, []);

  return urlFor;
}
