// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for People's and Collections' Sort by (src/components/librarySort.ts):
// the two orders of each, names with accents, case, numbers and spaces, the
// unnamed last, equal names kept in the device's order, Favourites first
// among the collections whatever the order, and the choice kept
// in this browser's storage - or for the visit only where it throws. Loaded
// through Vite's SSR loader in Node, as files-sort-test.mjs does:
//
//   node web/scripts/library-sort-test.mjs        (needs web/node_modules)
//
// The phones sort by the same rules: change them together.
import { after, before, beforeEach, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// This browser's storage: a map, or one that throws.
let store = new Map();
const goodStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
const useStorage = (s) => Object.defineProperty(globalThis, "localStorage", { value: s, configurable: true, writable: true });
useStorage(goodStorage);

let server;
let m; // the module under test
let collator;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-library-sort-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/librarySort.ts");
  const fs = await server.ssrLoadModule("/src/components/filesSort.ts");
  collator = (lang = "en") => fs.nameCollator(lang);
});
after(() => server?.close());

// People as the device lists them: most photos first. Some without a name
// (and one whose name is only spaces), "Ana" and "ana" equal to the
// collation, numbers, accents, spaces around a name.
const people = [
  { id: "p1", name: "", faceCount: 140 },
  { id: "p2", name: "Émile", faceCount: 120 },
  { id: "p3", name: "zoë", faceCount: 95 },
  { id: "p4", name: "Ana 10", faceCount: 60 },
  { id: "p5", name: "Ana 2", faceCount: 50 },
  { id: "p6", name: "   ", faceCount: 44 },
  { id: "p7", name: "Bruno", faceCount: 40 },
  { id: "p8", name: "ana", faceCount: 30 },
  { id: "p9", name: "Ana", faceCount: 25 },
  { id: "p10", name: "Óscar", faceCount: 20 },
  { id: "p11", name: "", faceCount: 12 },
  { id: "p12", name: "  Carla  ", faceCount: 8 },
  { id: "p13", name: "emma", faceCount: 5 },
];
const ids = (list) => list.map((p) => p.id);

describe("People", () => {
  test("Most photos: the named first, then the unnamed, each in the device's order", () => {
    assert.deepEqual(ids(m.sortPeople(people, "photos", collator())),
      ["p2", "p3", "p4", "p5", "p7", "p8", "p9", "p10", "p12", "p13", "p1", "p6", "p11"]);
  });
  test("Name: A to Z, accents and case ignored, numbers by value, spaces trimmed, the unnamed last", () => {
    assert.deepEqual(ids(m.sortPeople(people, "name", collator())),
      // ana, Ana (equal: the device's order), Ana 2, Ana 10, Bruno, Carla,
      // Émile, emma, Óscar, zoë; then the unnamed as the device lists them.
      ["p8", "p9", "p5", "p4", "p7", "p12", "p2", "p13", "p10", "p3", "p1", "p6", "p11"]);
  });
  test("Name: equal names keep the device's order, whichever that is", () => {
    const twins = [{ id: "a", name: "Ana" }, { id: "b", name: "ana" }, { id: "c", name: "ANA" }, { id: "d", name: "Aña" }];
    assert.deepEqual(ids(m.sortPeople(twins, "name", collator())), ["a", "b", "c", "d"]);
    assert.deepEqual(ids(m.sortPeople([...twins].reverse(), "name", collator())), ["d", "c", "b", "a"]);
  });
  test("Name: the language's collation (ñ is its own letter in Spanish)", () => {
    const list = [{ id: "nube", name: "Nube" }, { id: "nandu", name: "Ñandú" }, { id: "oso", name: "Oso" }];
    assert.deepEqual(ids(m.sortPeople(list, "name", collator("en"))), ["nandu", "nube", "oso"]);
    assert.deepEqual(ids(m.sortPeople(list, "name", collator("es"))), ["nube", "nandu", "oso"]);
  });
  test("a copy: the device's list stays as it came", () => {
    const before = ids(people);
    const out = m.sortPeople(people, "name", collator());
    assert.notEqual(out, people);
    assert.deepEqual(ids(people), before);
    assert.notEqual(m.sortPeople(people, "photos", collator()), people);
  });
  test("nobody, or only the unnamed", () => {
    assert.deepEqual(m.sortPeople([], "name", collator()), []);
    const unnamed = [{ id: "x", name: "" }, { id: "y", name: " " }];
    assert.deepEqual(ids(m.sortPeople(unnamed, "name", collator())), ["x", "y"]);
    assert.deepEqual(ids(m.sortPeople(unnamed, "photos", collator())), ["x", "y"]);
  });
});

// Collections as the device lists them: newest first.
const groups = [
  { id: "g1", name: "Summer 2024" },
  { id: "g2", name: "zoo trip" },
  { id: "g3", name: "Álbum de boda" },
  { id: "g4", name: "Birthday 10" },
  { id: "g5", name: "" },
  { id: "g6", name: "Birthday 2" },
  { id: "g7", name: "beach" },
];

describe("Collections", () => {
  test("Newest first: the device's order, untouched", () => {
    assert.deepEqual(ids(m.sortCollections(groups, "newest", collator())), ["g1", "g2", "g3", "g4", "g5", "g6", "g7"]);
  });
  test("Name: A to Z, accents and case ignored, numbers by value, the untitled last", () => {
    assert.deepEqual(ids(m.sortCollections(groups, "name", collator())), ["g3", "g7", "g6", "g4", "g1", "g2", "g5"]);
  });
  // Favourites: always first, whatever the order and its name in the
  // language on screen ("Favoritos" would go after "beach" by name), and
  // wherever the device put it.
  const withFavourites = [
    { id: "g1", name: "Summer 2024" },
    { id: "g2", name: "zoo trip" },
    { id: "fav", name: "Favoritos", favourites: true },
    { id: "g3", name: "Álbum de boda" },
    { id: "g5", name: "" },
    { id: "g7", name: "beach", favourites: false },
  ];
  test("Favourites first, by Newest first: the rest in the device's order", () => {
    assert.deepEqual(ids(m.sortCollections(withFavourites, "newest", collator("es"))), ["fav", "g1", "g2", "g3", "g5", "g7"]);
    const first = [withFavourites[2], ...withFavourites.filter((g) => !g.favourites)];
    assert.deepEqual(ids(m.sortCollections(first, "newest", collator())), ["fav", "g1", "g2", "g3", "g5", "g7"]);
  });
  test("Favourites first, by Name: the rest A to Z, the untitled last", () => {
    assert.deepEqual(ids(m.sortCollections(withFavourites, "name", collator("es"))), ["fav", "g3", "g7", "g1", "g2", "g5"]);
  });
  test("Favourites alone, or untitled, is still first", () => {
    assert.deepEqual(ids(m.sortCollections([{ id: "fav", name: "Favourites", favourites: true }], "name", collator())), ["fav"]);
    const untitled = [{ id: "a", name: "Zebra" }, { id: "fav", name: "", favourites: true }];
    assert.deepEqual(ids(m.sortCollections(untitled, "name", collator())), ["fav", "a"]);
    assert.deepEqual(ids(m.sortCollections(untitled, "newest", collator())), ["fav", "a"]);
  });
  test("a copy: the device's list stays as it came", () => {
    const before = ids(withFavourites);
    assert.notEqual(m.sortCollections(withFavourites, "newest", collator()), withFavourites);
    m.sortCollections(withFavourites, "name", collator());
    assert.deepEqual(ids(withFavourites), before);
  });
});

describe("the choice, kept in this browser", () => {
  beforeEach(() => {
    store = new Map();
    useStorage(goodStorage);
    m.resetLibrarySortForTests();
  });
  test("the defaults: Most photos and Newest first", () => {
    assert.equal(m.getPeopleOrder(), "photos");
    assert.equal(m.getCollectionsOrder(), "newest");
  });
  test("anything stored but a choice is the default", () => {
    for (const v of ["", "Name", "names", "newest", "photos ", "{}"]) {
      assert.equal(m.parsePeopleOrder(v), "photos", JSON.stringify(v));
    }
    for (const v of ["", "Name", "photos", "oldest"]) {
      assert.equal(m.parseCollectionsOrder(v), "newest", JSON.stringify(v));
    }
    assert.equal(m.parsePeopleOrder(null), "photos");
    assert.equal(m.parseCollectionsOrder(undefined), "newest");
  });
  test("a choice is saved, and read back on the next visit", () => {
    m.setPeopleOrder("name");
    m.setCollectionsOrder("name");
    assert.equal(store.get(m.cPeopleSortKey), "name");
    assert.equal(store.get(m.cCollectionsSortKey), "name");
    assert.equal(m.cPeopleSortKey, "otc_people_sort");
    assert.equal(m.cCollectionsSortKey, "otc_collections_sort");
    m.resetLibrarySortForTests();
    assert.equal(m.getPeopleOrder(), "name");
    assert.equal(m.getCollectionsOrder(), "name");
    m.setPeopleOrder("photos");
    m.resetLibrarySortForTests();
    assert.equal(m.getPeopleOrder(), "photos");
    assert.equal(m.getCollectionsOrder(), "name");
  });
  test("a storage that throws: the defaults, and a choice for the visit only", () => {
    useStorage({
      getItem: () => { throw new Error("SecurityError"); },
      setItem: () => { throw new Error("QuotaExceededError"); },
    });
    assert.equal(m.getPeopleOrder(), "photos");
    assert.doesNotThrow(() => m.setPeopleOrder("name"));
    assert.equal(m.getPeopleOrder(), "name");
    assert.doesNotThrow(() => m.setCollectionsOrder("name"));
    assert.equal(m.getCollectionsOrder(), "name");
  });
  test("no storage at all, or one whose very reading throws (blocked site data)", () => {
    useStorage(undefined);
    assert.equal(m.getPeopleOrder(), "photos");
    assert.doesNotThrow(() => m.setPeopleOrder("name"));
    assert.equal(m.getPeopleOrder(), "name");
    m.resetLibrarySortForTests();
    Object.defineProperty(globalThis, "localStorage", { get() { throw new Error("SecurityError"); }, configurable: true });
    assert.equal(m.getCollectionsOrder(), "newest");
    assert.doesNotThrow(() => m.setCollectionsOrder("name"));
    assert.equal(m.getCollectionsOrder(), "name");
  });
});
