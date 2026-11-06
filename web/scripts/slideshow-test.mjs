// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the slideshow's rules (src/components/slideshowRules.ts): its
// rounds over the device's pages - every photo once a round, a new order
// each round, never the photo just shown to start one, a page that fails
// asked again -, each photo's move, the size a photo is loaded at and
// which touches are swipes. Loaded through Vite's SSR loader in Node, as
// the other scripts' tests are:
//
//   node web/scripts/slideshow-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-slideshow-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/slideshowRules.ts");
});
after(() => server?.close());

const photo = (n) => ({ hash: n.toString(16).padStart(64, "0"), path: `/p/${n}.jpg`, mime: "image/jpeg" });

// A device as RandomPhotos answers: the photos ordered by md5(seed + hash),
// pages of `size` after a key, the total on a round's first page.
function device(photos, size = 3) {
  const calls = [];
  const fetchPage = async (seed, after) => {
    calls.push({ seed, after });
    const keyed = photos
      .map((f) => ({ f, k: createHash("md5").update(seed + f.hash).digest("hex") + f.hash }))
      .sort((a, b) => (a.k < b.k ? -1 : a.k > b.k ? 1 : 0));
    const from = after === "" ? 0 : keyed.findIndex((x) => x.k > after);
    const rest = from < 0 ? [] : keyed.slice(from);
    const page = rest.slice(0, size);
    const token = rest.length > size ? page[page.length - 1].k : "";
    return { files: page.map((x) => x.f), token, total: after === "" ? photos.length : 0 };
  };
  return { fetchPage, calls };
}

let seedN = 0;
const seeds = () => `seed${seedN++}`;

describe("rounds", () => {
  test("every photo once a round, a new order each round", async () => {
    const photos = Array.from({ length: 10 }, (_, i) => photo(i + 1));
    const dev = device(photos, 3);
    const r = m.createRounds(dev.fetchPage, seeds);
    const round = async () => {
      const got = [];
      for (let i = 0; i < 10; i++) got.push(await r.take());
      return got;
    };
    const one = await round();
    assert.deepEqual(one.map((e) => e.index), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    assert.ok(one.every((e) => e.round === 1 && e.total === 10));
    assert.deepEqual(new Set(one.map((e) => e.file.hash)).size, 10);
    const two = await round();
    assert.ok(two.every((e) => e.round === 2));
    assert.deepEqual(new Set(two.map((e) => e.file.hash)), new Set(one.map((e) => e.file.hash)));
    assert.notDeepEqual(two.map((e) => e.file.hash), one.map((e) => e.file.hash));
    // Pages of 3: four pages a round, each round with a seed of its own.
    const rounds = new Set(dev.calls.map((c) => c.seed));
    assert.equal(rounds.size, 2);
  });

  test("a new round never starts with the photo just shown", async () => {
    // A device whose every round has the same order but backwards from
    // the last: the next round's first is the photo just shown.
    const photos = [photo(1), photo(2), photo(3), photo(4)];
    let n = 0;
    const fetchPage = async (_seed, after) => {
      if (after !== "") return { files: [], token: "", total: 0 };
      const order = n++ % 2 === 0 ? photos : [...photos].reverse();
      return { files: order, token: "", total: 4 };
    };
    const r = m.createRounds(fetchPage, seeds);
    const shown = [];
    for (let i = 0; i < 12; i++) shown.push((await r.take()).file.hash);
    for (let i = 1; i < shown.length; i++) assert.notEqual(shown[i], shown[i - 1], `photo ${i} repeats the one before`);
  });

  test("held back when the new round's first page has nothing else yet", async () => {
    const a = photo(1), b = photo(2);
    let round = 0;
    const fetchPage = async (_seed, after) => {
      if (after === "") round++;
      if (round === 1) return { files: [a, b], token: "", total: 2 };
      // Round 2: page one is only the photo just shown (b), then a.
      if (after === "") return { files: [b], token: "k1", total: 2 };
      return { files: [a], token: "", total: 0 };
    };
    const r = m.createRounds(fetchPage, seeds);
    const got = [];
    for (let i = 0; i < 4; i++) got.push((await r.take()).file.hash);
    assert.deepEqual(got, [a.hash, b.hash, a.hash, b.hash]);
  });

  test("one photo is shown again and again", async () => {
    const r = m.createRounds(device([photo(7)]).fetchPage, seeds);
    for (let i = 0; i < 3; i++) assert.equal((await r.take()).file.hash, photo(7).hash);
  });

  test("nothing to show", async () => {
    const r = m.createRounds(device([]).fetchPage, seeds);
    assert.equal(await r.take(), null);
    // Only photos the device passes over (not processed): nothing either.
    const empty = m.createRounds(async (_s, after) => ({ files: [], token: after === "" ? "k" : "", total: 3 }), seeds);
    assert.equal(await empty.take(), null);
  });

  test("a page that fails is asked again", async () => {
    const dev = device([photo(1), photo(2), photo(3), photo(4)], 2);
    let fail = true;
    const fetchPage = async (seed, after) => {
      if (after !== "" && fail) { fail = false; throw new Error("no answer"); }
      return dev.fetchPage(seed, after);
    };
    const r = m.createRounds(fetchPage, seeds);
    const got = [(await r.take()).file.hash, (await r.take()).file.hash];
    await assert.rejects(r.take());
    got.push((await r.take()).file.hash, (await r.take()).file.hash);
    assert.equal(new Set(got).size, 4);
    const afters = dev.calls.map((c) => c.after);
    assert.equal(afters[1], afters[2] ?? afters[1], "the failed page is the one asked again");
  });

  test("prefetch asks the next page early, once", async () => {
    const dev = device(Array.from({ length: 9 }, (_, i) => photo(i + 1)), 3);
    const r = m.createRounds(dev.fetchPage, seeds);
    await r.take();
    r.prefetch(3);
    r.prefetch(3);
    await new Promise((res) => setTimeout(res, 0));
    assert.equal(dev.calls.length, 2);
  });

  test("seeds are 16 hex digits", () => {
    assert.match(m.newSeed(), /^[0-9a-f]{16}$/);
    assert.notEqual(m.newSeed(), m.newSeed());
  });
});

describe("moves, sizes and swipes", () => {
  test("neighbours move differently, never past the zoom loaded for", () => {
    for (let i = 0; i < 12; i++) {
      const a = m.kenBurnsOf(i), b = m.kenBurnsOf(i + 1);
      assert.notDeepEqual(a, b);
      for (const p of [a.from, a.to]) {
        assert.ok(p.s >= 1 && p.s <= m.cMaxZoom, `scale ${p.s}`);
        assert.ok(Math.abs(p.x) <= 2 && Math.abs(p.y) <= 2);
      }
      assert.notEqual(a.from.s, a.to.s);
    }
    assert.deepEqual(m.kenBurnsOf(-1), m.kenBurnsOf(5));
    assert.equal(m.cMoveMs, m.cPhotoMs + 2 * m.cFadeMs);
    assert.equal(m.cPhotoMs, 5000);
  });

  test("a photo is loaded at the size the screen draws it", () => {
    // 4032 x 3024 on a 1024 x 768 screen at 2x: 2048 x 1536, zoomed.
    assert.deepEqual(m.fitSize(4032, 3024, 1024, 768, 2), { w: 2212, h: 1659 });
    // A portrait one: as high as the screen.
    assert.deepEqual(m.fitSize(3024, 4032, 1024, 768, 1), { w: 622, h: 829 });
    // Never larger than it is.
    assert.deepEqual(m.fitSize(800, 600, 1920, 1080, 2), { w: 800, h: 600 });
    assert.deepEqual(m.fitSize(0, 600, 1920, 1080, 1), { w: 0, h: 0 });
  });

  test("swipes", () => {
    assert.equal(m.swipeOf(-120, 10, 300), "next");
    assert.equal(m.swipeOf(120, -20, 300), "prev");
    assert.equal(m.swipeOf(-40, 0, 300), null, "too short");
    assert.equal(m.swipeOf(-120, 100, 300), null, "too steep");
    assert.equal(m.swipeOf(-120, 0, 2000), null, "too slow");
  });
});
