// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The viewer's full-size photos (MediaViewer), kept for this tab's session:
// going back to a photo shows it at once instead of its small tile while
// GetFile runs again - every step back and forth used to show the low-res
// tile first. By content hash, the one in the device's answer (a path whose
// file changed holds other content; a copy at another path is the same
// photo). In memory only, so nothing stays in the browser once the tab
// closes or signs out (signOut reloads the page).
//
// 100 MB: a tenth of the apps' default thumbnail cache (1 GB), the share
// the phones give their own full-size cache (FullPhotoCache). One photo
// takes at most a quarter of it, so a huge one can't push out the rest.
//
// No imports, so `node web/scripts/full-size-cache-test.mjs` loads it alone.

export const cFullSizeBudget = 100_000_000;

export type FullSizeCache = {
  /** The photo with this hash, now the most recently used; null if not kept. */
  get(hash: string | undefined): Blob | null;
  /** Whether it is kept, leaving the order as it is (the viewer's
   *  read-ahead asks about neighbours it isn't showing). */
  has(hash: string | undefined): boolean;
  /** Keeps a photo, then lets the least recently used go until the rest
   *  fits. One without a hash, or bigger than a quarter of the budget,
   *  isn't kept. */
  put(hash: string | undefined, blob: Blob): void;
  /** The bytes kept now. */
  readonly used: number;
  clear(): void;
};

export function createFullSizeCache(budget = cFullSizeBudget): FullSizeCache {
  // A Map iterates in insertion order: least recently used first.
  const entries = new Map<string, Blob>();
  let used = 0;
  const drop = (hash: string) => {
    const b = entries.get(hash);
    if (!b) return;
    entries.delete(hash);
    used -= b.size;
  };
  return {
    get(hash) {
      const b = hash ? entries.get(hash) : undefined;
      if (!hash || !b) return null;
      entries.delete(hash);
      entries.set(hash, b);
      return b;
    },
    has(hash) {
      return !!hash && entries.has(hash);
    },
    put(hash, blob) {
      if (!hash || blob.size > budget / 4) return;
      drop(hash);
      entries.set(hash, blob);
      used += blob.size;
      // The new one is last and at most a quarter of the budget: it stays.
      for (const oldest of entries.keys()) {
        if (used <= budget) break;
        drop(oldest);
      }
    },
    get used() { return used; },
    clear() { entries.clear(); used = 0; },
  };
}

export const fullSizeCache = createFullSizeCache();
