// SPDX-License-Identifier: AGPL-3.0-or-later

// The order of the People and Collections pages (Sort by), chosen once per
// browser (it survives Sign Out, as Files' Sort by does), by the phones'
// rules (tested by node web/scripts/library-sort-test.mjs):
// - People: Most photos is the order the page always had, the default:
//   the named people first, then the unnamed, each in the device's order
//   (ListPeople's: most-photographed first); Name is A to Z, and the
//   people without a name come after everyone named, in the device's
//   order.
// - Collections: Newest first is the device's order (ListImageGroups'),
//   the default; Name is A to Z, and the collections without a name come
//   last, in the device's order. Favourites (ImageGroup.favourites) is
//   first in both, whatever its name in the language on screen.
// - Names in the app language's collation (filesSort's nameCollator: case
//   and accents ignored, the numbers in them in counting order, so
//   "Birthday 2" before "Birthday 10"), spaces at either end and runs of
//   them inside not counted; names the collation counts as equal ("Ana"
//   and "ana") stay in the device's order, so the list never reshuffles
//   by itself.
// Only the two pages follow it: the pickers ("Someone else…", "Add to
// collection") keep the device's order, as on the phones.
// Remembered in localStorage (otc_people_sort, otc_collections_sort); a
// browser whose storage throws keeps the choice for the visit only.
import { useSyncExternalStore } from "react";

export type PeopleOrder = "photos" | "name";
export type CollectionsOrder = "newest" | "name";

export const cPeopleSortKey = "otc_people_sort";
export const cCollectionsSortKey = "otc_collections_sort";

export const peopleOrders: readonly PeopleOrder[] = ["photos", "name"];
export const collectionsOrders: readonly CollectionsOrder[] = ["newest", "name"];

/** A stored choice, or the default for anything else (nothing, garbage). */
export const parsePeopleOrder = (s: string | null | undefined): PeopleOrder => (s === "name" ? "name" : "photos");
export const parseCollectionsOrder = (s: string | null | undefined): CollectionsOrder => (s === "name" ? "name" : "newest");

/** This browser's storage, or null where reaching it throws (blocked, a
 *  sandbox): read lazily, so a test can set it first. */
type KeyStore = Pick<Storage, "getItem" | "setItem">;
function storage(): KeyStore | null {
  try { return globalThis.localStorage ?? null; } catch { return null; }
}

function load(key: string): string | null {
  try { return storage()?.getItem(key) ?? null; } catch { return null; }
}

function save(key: string, value: string) {
  try { storage()?.setItem(key, value); } catch { /* kept for this visit only */ }
}

// The choices, read once, then kept here (a storage that throws on write
// still changes the order on screen).
let peopleOrder: PeopleOrder | null = null;
let collectionsOrder: CollectionsOrder | null = null;
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => { listeners.delete(l); };
};

export function getPeopleOrder(): PeopleOrder {
  peopleOrder ??= parsePeopleOrder(load(cPeopleSortKey));
  return peopleOrder;
}

export function getCollectionsOrder(): CollectionsOrder {
  collectionsOrder ??= parseCollectionsOrder(load(cCollectionsSortKey));
  return collectionsOrder;
}

export function setPeopleOrder(o: PeopleOrder) {
  save(cPeopleSortKey, o);
  if (o === getPeopleOrder()) return;
  peopleOrder = o;
  listeners.forEach((l) => l());
}

export function setCollectionsOrder(o: CollectionsOrder) {
  save(cCollectionsSortKey, o);
  if (o === getCollectionsOrder()) return;
  collectionsOrder = o;
  listeners.forEach((l) => l());
}

export const usePeopleOrder = () => useSyncExternalStore(subscribe, getPeopleOrder);
export const useCollectionsOrder = () => useSyncExternalStore(subscribe, getCollectionsOrder);

/** A name as it is compared: no spaces at either end, one space inside. */
const tidy = (s: string) => s.replace(/\s+/g, " ").trim();

// The named ones - A to Z with a collator (the device's order between
// equal names), else in the device's order - then the unnamed in the
// device's order.
function namedFirst<T extends { name: string }>(items: readonly T[], collator: Intl.Collator | null): T[] {
  const named: { item: T; name: string; i: number }[] = [];
  const unnamed: T[] = [];
  items.forEach((item, i) => {
    const name = tidy(item.name);
    if (name) named.push({ item, name, i });
    else unnamed.push(item);
  });
  if (collator) named.sort((a, b) => collator.compare(a.name, b.name) || a.i - b.i);
  return [...named.map((n) => n.item), ...unnamed];
}
const byName = <T extends { name: string }>(items: readonly T[], collator: Intl.Collator) => namedFirst(items, collator);

/** People in `order`: a copy, the device's list left as it is. */
export function sortPeople<T extends { name: string }>(items: readonly T[], order: PeopleOrder, collator: Intl.Collator): T[] {
  return namedFirst(items, order === "name" ? collator : null);
}

/** Collections in `order`, Favourites first: a copy, the device's list
 *  left as it is. */
export function sortCollections<T extends { name: string; favourites?: boolean }>(items: readonly T[], order: CollectionsOrder, collator: Intl.Collator): T[] {
  const first = items.filter((g) => g.favourites);
  const rest = items.filter((g) => !g.favourites);
  return [...first, ...(order === "name" ? byName(rest, collator) : rest)];
}

/** Forgets the choices read (tests: another storage). */
export function resetLibrarySortForTests() {
  peopleOrder = null;
  collectionsOrder = null;
}
