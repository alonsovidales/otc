// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #196: the order of Files' list, grid and search results, chosen
// once per browser. The same rules as the iPhone's FilesSort.swift and
// Android's FilesSort.kt, tested with the same cases
// (web/scripts/files-sort-test.mjs):
// - by Name, Size, Created or Modified; each starts in its natural
//   direction (A to Z, largest first, newest first) and picking the same
//   one again reverses it;
// - ".." first, then the folders, then the files, in every order;
// - folders carry no size or dates in a listing, so by Size, Created or
//   Modified they stay in name order (A to Z) above the files;
// - names in the app language's collation, ignoring case and accents,
//   with the numbers in them in counting order (file2 before file10);
// - a file without the date sorted by goes last, either way;
// - ties by name (A to Z), then by path code unit by code unit, so the
//   order never depends on the order the device sent.
// The device's own order is never used: the choice can't be told to it.

export type SortKey = "name" | "size" | "created" | "modified";
export type SortDir = "asc" | "desc";
export type SortOrder = { readonly key: SortKey; readonly dir: SortDir };

export const sortKeys: readonly SortKey[] = ["name", "size", "created", "modified"];
export const defaultOrder: SortOrder = { key: "name", dir: "asc" };

/** Where it is kept in this browser (survives Sign Out, as List/Grid does). */
export const cSortStorageKey = "otc.files.sort";

/** The direction a key starts in: A to Z, largest first, newest first. */
export function naturalDir(key: SortKey): SortDir {
  return key === "name" ? "asc" : "desc";
}

/** The order after picking `key`: the same key reverses, another starts in its natural direction. */
export function pickOrder(current: SortOrder, key: SortKey): SortOrder {
  if (current.key === key) return { key, dir: current.dir === "asc" ? "desc" : "asc" };
  return { key, dir: naturalDir(key) };
}

/** "size:desc"; read back by parseOrder. */
export function formatOrder(o: SortOrder): string {
  return `${o.key}:${o.dir}`;
}

/** A stored order, or the default for anything else (nothing, garbage, an older value). */
export function parseOrder(s: string | null | undefined): SortOrder {
  const m = /^(name|size|created|modified):(asc|desc)$/.exec(s ?? "");
  return m ? { key: m[1] as SortKey, dir: m[2] as SortDir } : defaultOrder;
}

export function loadOrder(): SortOrder {
  try { return parseOrder(localStorage.getItem(cSortStorageKey)); } catch { return defaultOrder; }
}

export function saveOrder(o: SortOrder) {
  try { localStorage.setItem(cSortStorageKey, formatOrder(o)); } catch { /* kept for this visit only */ }
}

/** What an entry is sorted by. Dates in milliseconds, undefined when the device sent none. */
export type SortFields = {
  readonly name: string;
  readonly path: string;
  readonly isDir: boolean;
  readonly size: number;
  readonly created?: number;
  readonly modified?: number;
};

/**
 * The collation names are compared with: the language's, case and accents
 * ignored, numbers by value - ICU's, as iOS's String.compare and Android's
 * NameCollator (the tricky names in files-sort-test.mjs hold all three to
 * one order).
 */
export function nameCollator(tag: string): Intl.Collator {
  return new Intl.Collator(tag, { numeric: true, sensitivity: "base" });
}

function binary(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

// ".." first, then folders, then files.
function group(f: SortFields): number {
  return f.path === ".." ? 0 : f.isDir ? 1 : 2;
}

function byDate(a: number | undefined, b: number | undefined, dir: SortDir): number {
  // Missing last, whichever the direction.
  if (a === undefined || b === undefined) return a === b ? 0 : a === undefined ? 1 : -1;
  return dir === "asc" ? a - b : b - a;
}

/** Negative when `a` comes first. */
export function compareEntries(a: SortFields, b: SortFields, order: SortOrder, collator: Intl.Collator): number {
  const g = group(a) - group(b);
  if (g !== 0) return g;
  const sign = order.dir === "asc" ? 1 : -1;
  if (order.key === "name") {
    return sign * (collator.compare(a.name, b.name) || binary(a.path, b.path));
  }
  let c = 0;
  // Folders have no size or dates of their own: name order, A to Z.
  if (!a.isDir) {
    if (order.key === "size") c = sign * (a.size - b.size);
    else c = byDate(a[order.key], b[order.key], order.dir);
  }
  return c || collator.compare(a.name, b.name) || binary(a.path, b.path);
}

/** A copy of `items` in `order`; `fields` says what each one is sorted by. */
export function sortEntries<T>(items: readonly T[], fields: (t: T) => SortFields, order: SortOrder, collator: Intl.Collator): T[] {
  const keyed = items.map((item) => ({ item, f: fields(item) }));
  keyed.sort((x, y) => compareEntries(x.f, y.f, order, collator));
  return keyed.map((k) => k.item);
}

/** A Date as sortEntries takes it. */
export function dateMs(d: Date | undefined): number | undefined {
  if (!d) return undefined;
  const ms = d.getTime();
  return Number.isNaN(ms) ? undefined : ms;
}
