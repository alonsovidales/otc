// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a collection's Rating on the web (src/components/rating.ts and
// the Show and Sort by of src/components/photoFilter.ts): what Show and Sort
// by send in SearchPhotos (nothing at their defaults), when the grid is
// ranked, a star tap, the comment's trimming and its 2000 characters, the
// shared gallery page's own Show and Sort by over its items, the stars and
// comments shown and sent one request at a time per photo (a refusal puts
// them back, a quick second change goes after the first), the hint shown
// once, and the open collection's view going back to its default. Loaded
// through Vite's SSR loader in Node, as places-switch-test.mjs does:
//
//   node web/scripts/rating-test.mjs        (needs web/node_modules)
//
// The phones follow the same rules: change them together.
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
const throwingStorage = {
  getItem() { throw new Error("blocked"); },
  setItem() { throw new Error("blocked"); },
  removeItem() { throw new Error("blocked"); },
};
const useStorage = (s) => Object.defineProperty(globalThis, "localStorage", { value: s, configurable: true, writable: true });
useStorage(goodStorage);
globalThis.window = {
  get localStorage() { return globalThis.localStorage; },
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en" }, configurable: true, writable: true });

let server;
let m; // rating.ts
let filter; // photoFilter.ts
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-rating-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/rating.ts");
  filter = await server.ssrLoadModule("/src/components/photoFilter.ts");
});
after(() => server?.close());

// Lets the store's own awaits run.
const settle = () => new Promise((r) => setTimeout(r, 0));

describe("Show and Sort by in SearchPhotos", () => {
  test("each Show choice maps to the device's RatingFilter", () => {
    const f = (min, unrated = false, withComment = false) => ({ minStars: min, unrated, withComment });
    assert.equal(m.ratingFilterOf("all"), undefined);
    assert.deepEqual(m.ratingFilterOf("5"), f(5));
    assert.deepEqual(m.ratingFilterOf("4"), f(4));
    assert.deepEqual(m.ratingFilterOf("3"), f(3));
    assert.deepEqual(m.ratingFilterOf("2"), f(2));
    assert.deepEqual(m.ratingFilterOf("rated"), f(1));
    assert.deepEqual(m.ratingFilterOf("unrated"), f(0, true));
    assert.deepEqual(m.ratingFilterOf("comment"), f(0, false, true));
    assert.deepEqual([...m.ratingShows], ["all", "5", "4", "3", "2", "rated", "unrated", "comment"], "the menu's order");
  });

  test("the request: nothing outside a rated collection or at the defaults", () => {
    assert.deepEqual(m.ratingSearch(null), {});
    assert.deepEqual(m.ratingSearch({ show: "all", sort: "date" }), {}, "byte for byte the request before Rating");
    assert.deepEqual(m.ratingSearch({ show: "all", sort: "rating" }), { byRating: true });
    assert.deepEqual(m.ratingSearch({ show: "4", sort: "date" }), { rating: { minStars: 4, unrated: false, withComment: false } });
    assert.deepEqual(m.ratingSearch({ show: "comment", sort: "rating" }), { rating: { minStars: 0, unrated: false, withComment: true }, byRating: true });
  });

  test("either away from its default ranks the grid", () => {
    assert.equal(m.isRatingRanked({ show: "all", sort: "date" }), false);
    assert.equal(m.isRatingRanked({ show: "all", sort: "rating" }), true);
    assert.equal(m.isRatingRanked({ show: "unrated", sort: "date" }), true);
  });

  test("a menu's value: anything unknown is the default", () => {
    assert.equal(m.parseShow("rated"), "rated");
    assert.equal(m.parseShow("6"), "all");
    assert.equal(m.parseShow(undefined), "all");
    assert.equal(m.parseSort("rating"), "rating");
    assert.equal(m.parseSort("stars"), "date");
  });
});

describe("the stars and the comment", () => {
  test("a star sets that many; the photo's own again clears it", () => {
    assert.equal(m.nextStars(0, 3), 3);
    assert.equal(m.nextStars(3, 5), 5);
    assert.equal(m.nextStars(5, 2), 2);
    assert.equal(m.nextStars(3, 3), 0, "tapping the current value clears");
    assert.equal(m.nextStars(0, 9), 5, "never more than 5");
    assert.equal(m.clampStars(-2), 0);
    assert.equal(m.clampStars(Number.NaN), 0);
  });

  test("a comment is trimmed and kept to 2000 characters, counted as the device does", () => {
    assert.equal(m.cleanComment("  more contrast, please \n"), "more contrast, please");
    assert.equal(m.cleanComment("   "), "");
    const long = "a".repeat(2005);
    assert.equal(m.cleanComment(long).length, 2000);
    // 2000 emoji are 2000 characters (code points), 4000 UTF-16 units.
    const emoji = "😀".repeat(2000);
    assert.equal(m.cleanComment(emoji), emoji);
    assert.equal([...m.cleanComment("😀".repeat(2001))].length, 2000);
    // Cut at the limit, it doesn't end in a space.
    assert.equal(m.cleanComment("x".repeat(1999) + " yz"), "x".repeat(1999));
  });

  test("what a listing says: unset is no rating", () => {
    assert.deepEqual(m.listedRating(undefined), { stars: 0, comment: "", fromLink: false });
    assert.deepEqual(m.listedRating({ stars: 4, comment: "nice", fromLink: true, updated: new Date() }), { stars: 4, comment: "nice", fromLink: true });
    assert.deepEqual(m.listedRating({ stars: 7, comment: "" }), { stars: 5, comment: "", fromLink: false });
  });
});

describe("a shared gallery's own Show and Sort by", () => {
  // As a gallery lists them: newest first.
  const day = (d) => new Date(Date.UTC(2026, 9, d));
  const items = [
    { index: 0, stars: 0, comment: "", taken: day(20) },
    { index: 1, stars: 3, comment: "brighter?", taken: day(19) },
    { index: 2, stars: 5, comment: "", taken: day(18) },
    { index: 3, stars: 3, comment: "", taken: day(25) },
    { index: 4, stars: 0, comment: "crop it", taken: day(17) },
    { index: 5, stars: 5, comment: "", taken: undefined },
    { index: 6, stars: 4, comment: "  ", taken: day(16) },
    { index: 7, stars: 5, comment: "", taken: day(18) },
  ];
  const ratingOf = (it) => ({ stars: it.stars, comment: it.comment });
  const takenOf = (it) => it.taken;
  const arrange = (show, sort) => m.arrangeItems(items, { show, sort }, ratingOf, takenOf).map((it) => it.index);

  test("Show keeps what the device's filter would", () => {
    assert.deepEqual(arrange("all", "date"), [0, 1, 2, 3, 4, 5, 6, 7], "the gallery's own order");
    assert.deepEqual(arrange("5", "date"), [2, 5, 7]);
    assert.deepEqual(arrange("4", "date"), [2, 5, 6, 7]);
    assert.deepEqual(arrange("3", "date"), [1, 2, 3, 5, 6, 7]);
    assert.deepEqual(arrange("2", "date"), [1, 2, 3, 5, 6, 7]);
    assert.deepEqual(arrange("rated", "date"), [1, 2, 3, 5, 6, 7]);
    assert.deepEqual(arrange("unrated", "date"), [0, 4], "a comment without stars is not rated");
    assert.deepEqual(arrange("comment", "date"), [1, 4], "spaces are no comment");
  });

  test("Sort by Rating: more stars first, then the newest taken, no stars last", () => {
    // 5 stars: 2 and 7 taken the same day keep the gallery's order, 5 has
    // no date and comes after them; 4 stars; 3 stars, the newer first;
    // then the photos without stars, the newer first.
    assert.deepEqual(arrange("all", "rating"), [2, 7, 5, 6, 3, 1, 0, 4]);
    assert.deepEqual(arrange("comment", "rating"), [1, 4]);
    assert.deepEqual(arrange("unrated", "rating"), [0, 4]);
  });

  test("what passes Show", () => {
    assert.equal(m.passesShow({ stars: 1, comment: "" }, "rated"), true);
    assert.equal(m.passesShow({ stars: 0, comment: "x" }, "rated"), false);
    assert.equal(m.passesShow({ stars: 2, comment: "" }, "3"), false);
    assert.equal(m.passesShow({ stars: 0, comment: "" }, "all"), true);
  });
});

// A device whose answers the test gives: each request waits in `sent`
// until answered (or failed: no answer).
function device() {
  const sent = [];
  const send = (key, want) => new Promise((ok, fail) => sent.push({ key, want, ok, fail }));
  return { sent, send };
}
const none = { stars: 0, comment: "", fromLink: false };
const stored = (stars, comment = "", fromLink = false) => ({ ok: true, stored: { stars, comment, fromLink } });
const refused = (code = "", said = "") => ({ ok: false, code, said });

describe("the stars and comments shown, and their requests", () => {
  test("a change shows at once and is sent; the device's answer is what then shows", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    assert.equal(r.get("/a.jpg"), undefined, "nothing set: the listing's shows");
    const v0 = r.version();
    const done = r.set("/a.jpg", { stars: 4, comment: "  warmer " }, { stars: 2, comment: "", fromLink: true });
    assert.deepEqual(r.get("/a.jpg"), { stars: 4, comment: "warmer", fromLink: false }, "shown at once, trimmed");
    assert.ok(r.version() > v0);
    await settle();
    assert.equal(d.sent.length, 1);
    assert.deepEqual(d.sent[0].want, { stars: 4, comment: "warmer" });
    assert.equal(r.busy("/a.jpg"), true);
    d.sent[0].ok(stored(4, "warmer"));
    assert.equal(await done, null);
    assert.equal(r.busy("/a.jpg"), false);
    assert.deepEqual(r.get("/a.jpg"), { stars: 4, comment: "warmer", fromLink: false });
  });

  test("one request at a time per photo: a quick second change goes after the first, the last wins", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    const first = r.set("/a.jpg", { stars: 3, comment: "" }, none);
    await settle();
    assert.deepEqual(d.sent[0].want, { stars: 3, comment: "" });
    const second = r.set("/a.jpg", { stars: 5, comment: "" }, none);
    const third = r.set("/a.jpg", { stars: 2, comment: "ok" }, none);
    await settle();
    assert.equal(d.sent.length, 1, "nothing more while one is out");
    assert.deepEqual(r.get("/a.jpg"), { stars: 2, comment: "ok", fromLink: false });
    d.sent[0].ok(stored(3));
    await settle();
    assert.deepEqual(r.get("/a.jpg"), { stars: 2, comment: "ok", fromLink: false }, "an older answer never undoes the latest change");
    assert.equal(d.sent.length, 2);
    assert.deepEqual(d.sent[1].want, { stars: 2, comment: "ok" }, "only what shows now goes");
    d.sent[1].ok(stored(2, "ok"));
    assert.deepEqual(await Promise.all([first, second, third]), [null, null, null]);
    assert.equal(d.sent.length, 2);
  });

  test("other photos don't wait for each other", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    void r.set("/a.jpg", { stars: 1, comment: "" }, none);
    void r.set("/b.jpg", { stars: 2, comment: "" }, none);
    await settle();
    assert.deepEqual(d.sent.map((s) => s.key), ["/a.jpg", "/b.jpg"]);
  });

  test("refused: back to what showed before, with the device's code and words", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    const listed = { stars: 2, comment: "first", fromLink: true };
    const done = r.set("/a.jpg", { stars: 5, comment: "first" }, listed);
    await settle();
    d.sent[0].ok(refused("rating_off", "Rating is off for this collection."));
    assert.deepEqual(await done, { code: "rating_off", said: "Rating is off for this collection." });
    assert.deepEqual(r.get("/a.jpg"), listed, "the listing's again, its note included");
    assert.equal(r.busy("/a.jpg"), false);
  });

  test("no answer is a refusal without words", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    const done = r.set("/a.jpg", { stars: 1, comment: "" }, none);
    await settle();
    d.sent[0].fail(new Error("no answer"));
    assert.deepEqual(await done, { code: "", said: "" });
    assert.deepEqual(r.get("/a.jpg"), none);
  });

  test("refused after a change made meanwhile: that change is sent instead", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    const done = r.set("/a.jpg", { stars: 1, comment: "" }, none);
    await settle();
    void r.set("/a.jpg", { stars: 4, comment: "" }, none);
    d.sent[0].ok(refused("", "busy"));
    await settle();
    assert.deepEqual(r.get("/a.jpg"), { stars: 4, comment: "", fromLink: false }, "not put back");
    assert.equal(d.sent.length, 2);
    d.sent[1].ok(stored(4));
    assert.equal(await done, null);
  });

  test("back to the device's last word, not the listing's, after an earlier change it took", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    void r.set("/a.jpg", { stars: 3, comment: "" }, none);
    await settle();
    d.sent[0].ok(stored(3));
    await settle();
    const done = r.set("/a.jpg", { stars: 5, comment: "" }, none);
    await settle();
    d.sent[1].ok(refused());
    await done;
    assert.deepEqual(r.get("/a.jpg"), { stars: 3, comment: "", fromLink: false });
  });

  test("the same rating again sends nothing", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    assert.equal(await r.set("/a.jpg", { stars: 2, comment: " hi " }, { stars: 2, comment: "hi", fromLink: false }), null);
    await settle();
    assert.equal(d.sent.length, 0);
  });

  test("clearing: no stars and no comment", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    void r.set("/a.jpg", { stars: 0, comment: "" }, { stars: 4, comment: "x", fromLink: false });
    await settle();
    assert.deepEqual(d.sent[0].want, { stars: 0, comment: "" });
  });

  test("a new listing: what nothing is on its way for is forgotten", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    void r.set("/a.jpg", { stars: 3, comment: "" }, none);
    await settle();
    d.sent[0].ok(stored(3));
    await settle();
    void r.set("/b.jpg", { stars: 4, comment: "" }, none);
    await settle();
    r.settle();
    assert.equal(r.get("/a.jpg"), undefined, "the listing has the device's word");
    assert.deepEqual(r.get("/b.jpg"), { stars: 4, comment: "", fromLink: false }, "one on its way stays");
    d.sent[1].ok(stored(4));
    await settle();
    assert.deepEqual(r.get("/b.jpg"), { stars: 4, comment: "", fromLink: false });
  });

  test("subscribers hear every change", async () => {
    const d = device();
    const r = m.createRatings(d.send);
    let heard = 0;
    const stop = r.subscribe(() => { heard++; });
    void r.set("/a.jpg", { stars: 1, comment: "" }, none);
    assert.equal(heard, 1);
    stop();
    void r.set("/a.jpg", { stars: 2, comment: "" }, none);
    assert.equal(heard, 1);
  });
});

describe("the hint the first time Rating is turned on", () => {
  beforeEach(() => {
    store = new Map();
    useStorage(goodStorage);
    m.forgetRatingHint();
  });

  test("once per browser", () => {
    assert.equal(m.claimRatingHint(), true);
    assert.equal(m.claimRatingHint(), false);
    m.forgetRatingHint(); // a new page in the same browser
    assert.equal(m.claimRatingHint(), false);
  });

  test("storage that throws: once for the visit", () => {
    useStorage(throwingStorage);
    assert.equal(m.claimRatingHint(), true);
    assert.equal(m.claimRatingHint(), false);
  });
});

describe("the open collection's Show and Sort by (photoFilter.ts)", () => {
  const group = (rating) => ({ id: "g1", name: "Wedding", fileCount: 40, rating });

  test("they apply only while the collection's Rating is on", () => {
    filter.openGroup(group(false));
    assert.equal(filter.ratingViewOf(filter.getPhotoFilter()), null);
    filter.setRatingView({ show: "5", sort: "rating" });
    assert.deepEqual(filter.getPhotoFilter().rating, { show: "all", sort: "date" }, "nothing to narrow without Rating");
    assert.equal(filter.isDateOrdered(filter.getPhotoFilter()), true);
  });

  test("narrowed or sorted by rating, the grid isn't in date order", () => {
    filter.openGroup(group(true));
    const f = () => filter.getPhotoFilter();
    assert.deepEqual(filter.ratingViewOf(f()), { show: "all", sort: "date" });
    assert.equal(filter.isDateOrdered(f()), true, "the defaults keep the months");
    filter.setRatingView({ show: "4", sort: "date" });
    assert.equal(filter.isDateOrdered(f()), false);
    filter.setRatingView({ show: "all", sort: "rating" });
    assert.equal(filter.isDateOrdered(f()), false);
    filter.setRatingView({ show: "all", sort: "date" });
    assert.equal(filter.isDateOrdered(f()), true);
  });

  test("back to every photo by date: Rating turned off, another collection, leaving it", () => {
    filter.openGroup(group(true));
    filter.setRatingView({ show: "unrated", sort: "rating" });
    filter.updateOpenGroup({ ...group(true), name: "Renamed" });
    assert.deepEqual(filter.getPhotoFilter().rating, { show: "unrated", sort: "rating" }, "a rename keeps them");
    filter.updateOpenGroup(group(false));
    assert.deepEqual(filter.getPhotoFilter().rating, { show: "all", sort: "date" });

    filter.openGroup(group(true));
    filter.setRatingView({ show: "5", sort: "date" });
    filter.openGroup({ ...group(true), id: "g2" });
    assert.deepEqual(filter.getPhotoFilter().rating, { show: "all", sort: "date" }, "not carried to another collection");

    filter.setRatingView({ show: "rated", sort: "date" });
    filter.leaveGroup();
    assert.deepEqual(filter.getPhotoFilter().rating, { show: "all", sort: "date" });
    assert.equal(filter.ratingViewOf(filter.getPhotoFilter()), null);
  });
});
