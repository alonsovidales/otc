// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Files' order (issue #196, src/components/filesSort.ts). The web
// app has no test runner, so this loads the module through Vite's SSR
// loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/files-sort-test.mjs        (needs web/node_modules)
//
// The cases are the same as the iPhone's FilesSortTests.swift and
// Android's FilesSortTest.kt: change all three together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// What the module keeps between visits: this browser's storage.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};

let server;
let s; // the module under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-files-sort-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle, and no scan of
    // the whole app still running when the tests are done.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  s = await server.ssrLoadModule("/src/components/filesSort.ts");
});
after(() => server?.close());

// A folder's listing: "..", two folders, and files whose sizes and dates
// tie in places, one without a creation date, one without a modification
// date, and two names that differ only by an accent.
const entry = (path, isDir, size = 0, created, modified) => ({
  path, isDir, size, created, modified, name: path === ".." ? ".." : path.slice(path.lastIndexOf("/") + 1),
});
const listing = [
  entry("/d/IMG_10.jpg", false, 300, 3000, 1000),
  entry("/d/é.txt", false, 50, undefined, 500),
  entry("/d/Zeta", true),
  entry("/d/apple.txt", false, 200, 2000, 2000),
  entry("..", true),
  entry("/d/IMG_2.jpg", false, 100, 4000, 3000),
  entry("/d/Banana.txt", false, 200, 2000, undefined),
  entry("/d/alpha", true),
  entry("/d/e.txt", false, 50, undefined, 500),
];

const names = (order, items = listing) => s.sortEntries(items, (f) => f, order, s.nameCollator("en")).map((f) => f.name);

// Names the three apps once put in different orders: a name ending where
// another goes on with a number, a space before the number, numbers inside
// names, case and accents (ties, which the paths decide), and digits that
// aren't ASCII - a fullwidth 5 is the number 5, a superscript 2 and a
// circled 5 aren't numbers at all (ICU's numeric collation, which all
// three use).
const tricky = ["photo10.jpg", "IMG_10.jpg", "Photo 2.jpg", "é.txt", "photo.jpg", "10.txt", "img_2.jpg", "５.txt",
  "photo1.jpg", "E.txt", "IMG_2.jpg", "5.txt", "e.txt", "⑤.txt", "².txt", "2.txt"].map((n) => entry(`/d/${n}`, false));
const trickyAToZ = ["2.txt", "5.txt", "５.txt", "10.txt", "².txt", "⑤.txt", "E.txt", "e.txt", "é.txt",
  "IMG_2.jpg", "img_2.jpg", "IMG_10.jpg", "Photo 2.jpg", "photo.jpg", "photo1.jpg", "photo10.jpg"];

describe("files order", () => {
  test("name, A to Z: numbers in counting order, case and accents ignored, folders first", () => {
    assert.deepEqual(names({ key: "name", dir: "asc" }),
      ["..", "alpha", "Zeta", "apple.txt", "Banana.txt", "e.txt", "é.txt", "IMG_2.jpg", "IMG_10.jpg"]);
  });
  test("name, Z to A", () => {
    assert.deepEqual(names({ key: "name", dir: "desc" }),
      ["..", "Zeta", "alpha", "IMG_10.jpg", "IMG_2.jpg", "é.txt", "e.txt", "Banana.txt", "apple.txt"]);
  });
  test("size: folders by name above the files, ties by name then path", () => {
    assert.deepEqual(names({ key: "size", dir: "desc" }),
      ["..", "alpha", "Zeta", "IMG_10.jpg", "apple.txt", "Banana.txt", "IMG_2.jpg", "e.txt", "é.txt"]);
    assert.deepEqual(names({ key: "size", dir: "asc" }),
      ["..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_2.jpg", "apple.txt", "Banana.txt", "IMG_10.jpg"]);
  });
  test("created: a missing date last either way", () => {
    assert.deepEqual(names({ key: "created", dir: "desc" }),
      ["..", "alpha", "Zeta", "IMG_2.jpg", "IMG_10.jpg", "apple.txt", "Banana.txt", "e.txt", "é.txt"]);
    assert.deepEqual(names({ key: "created", dir: "asc" }),
      ["..", "alpha", "Zeta", "apple.txt", "Banana.txt", "IMG_10.jpg", "IMG_2.jpg", "e.txt", "é.txt"]);
  });
  test("modified: a missing date last either way", () => {
    assert.deepEqual(names({ key: "modified", dir: "desc" }),
      ["..", "alpha", "Zeta", "IMG_2.jpg", "apple.txt", "IMG_10.jpg", "e.txt", "é.txt", "Banana.txt"]);
    assert.deepEqual(names({ key: "modified", dir: "asc" }),
      ["..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_10.jpg", "apple.txt", "IMG_2.jpg", "Banana.txt"]);
  });
  test("the order the device sent doesn't matter", () => {
    for (const key of s.sortKeys) {
      for (const dir of ["asc", "desc"]) {
        assert.deepEqual(names({ key, dir }, [...listing].reverse()), names({ key, dir }), `${key}:${dir}`);
      }
    }
  });
  test("search results (no '..', whole paths) by their names", () => {
    const found = [entry("/b/file10", false, 1), entry("/a/file2", false, 1), entry("/c/Docs", true), entry("/a/file2.bak", false, 1)];
    assert.deepEqual(names({ key: "name", dir: "asc" }, found), ["Docs", "file2", "file2.bak", "file10"]);
  });
  test("tricky names in the same order as the iPhone and Android", () => {
    assert.deepEqual(names({ key: "name", dir: "asc" }, tricky), trickyAToZ);
    assert.deepEqual(names({ key: "name", dir: "desc" }, tricky), [...trickyAToZ].reverse());
  });
  test("tricky names: one order whichever order they come in", () => {
    for (const from of [tricky, [...tricky].reverse()]) {
      for (let k = 0; k < from.length; k++) {
        assert.deepEqual(names({ key: "name", dir: "asc" }, [...from.slice(k), ...from.slice(0, k)]), trickyAToZ, `from ${k}`);
      }
    }
  });
  test("the names in the app's language: Swedish puts ä after z", () => {
    const sv = s.sortEntries([entry("/d/ä", false), entry("/d/z", false), entry("/d/a", false)], (f) => f, s.defaultOrder, s.nameCollator("sv"));
    assert.deepEqual(sv.map((f) => f.name), ["a", "z", "ä"]);
  });
});

describe("picking an order", () => {
  test("each key starts in its natural direction; the same key again reverses it", () => {
    assert.deepEqual(s.defaultOrder, { key: "name", dir: "asc" });
    let o = s.pickOrder(s.defaultOrder, "name");
    assert.deepEqual(o, { key: "name", dir: "desc" });
    o = s.pickOrder(o, "size");
    assert.deepEqual(o, { key: "size", dir: "desc" });
    o = s.pickOrder(o, "size");
    assert.deepEqual(o, { key: "size", dir: "asc" });
    assert.deepEqual(s.pickOrder(o, "created"), { key: "created", dir: "desc" });
    assert.deepEqual(s.pickOrder(o, "modified"), { key: "modified", dir: "desc" });
  });
});

describe("the stored order", () => {
  test("written and read back", () => {
    for (const key of s.sortKeys) {
      for (const dir of ["asc", "desc"]) {
        assert.deepEqual(s.parseOrder(s.formatOrder({ key, dir })), { key, dir });
      }
    }
    assert.equal(s.formatOrder({ key: "size", dir: "desc" }), "size:desc");
  });
  test("anything else is the default", () => {
    for (const v of [null, undefined, "", "garbage", "size", "name:up", "kind:asc", "SIZE:DESC", " size:desc", "size:desc:x"]) {
      assert.deepEqual(s.parseOrder(v), s.defaultOrder, String(v));
    }
  });
  test("kept in this browser", () => {
    assert.deepEqual(s.loadOrder(), s.defaultOrder);
    s.saveOrder({ key: "modified", dir: "asc" });
    assert.equal(store.get(s.cSortStorageKey), "modified:asc");
    assert.deepEqual(s.loadOrder(), { key: "modified", dir: "asc" });
    store.set(s.cSortStorageKey, "nonsense");
    assert.deepEqual(s.loadOrder(), s.defaultOrder);
  });
});
