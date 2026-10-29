// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for loading the photos newer than a jump to a date (issue #194,
// src/components/newerPhotos.ts). The web app has no test runner, so this
// loads the module through Vite's SSR loader in Node, as i18n-test.mjs
// does:
//
//   node web/scripts/newer-photos-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too: change them together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // the module under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-newer-photos-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/newerPhotos.ts");
});
after(() => server?.close());

const f = (hash, path = `/p/${hash}.jpg`) => ({ hash, path });
const cutoff = new Date(2026, 5, 30, 23, 59, 59, 999);

describe("when there is an upward cursor", () => {
  test("only after a jump, in date order, with the scrubber", () => {
    m.forgetDeviceAnswers();
    assert.equal(m.upwardApplies({ cutoff, dateOrdered: true, scrubber: true }), true);
    assert.equal(m.upwardApplies({ cutoff: null, dateOrdered: true, scrubber: true }), false);
    assert.equal(m.upwardApplies({ cutoff: undefined, dateOrdered: true, scrubber: true }), false);
    assert.equal(m.upwardApplies({ cutoff, dateOrdered: false, scrubber: true }), false);
    assert.equal(m.upwardApplies({ cutoff, dateOrdered: true, scrubber: false }), false);
  });
  test("never again once the device answered as one before #194", () => {
    m.forgetDeviceAnswers();
    m.deviceIgnoresNewerThan();
    assert.equal(m.newerThanIgnored(), true);
    assert.equal(m.upwardApplies({ cutoff, dateOrdered: true, scrubber: true }), false);
    m.forgetDeviceAnswers();
  });
});

describe("the request", () => {
  const filter = { personIds: ["p1"], groupId: "g1" };
  test("the first page: newer_than the jump's own cutoff, no token, a small page, no before", () => {
    const r = m.upwardRequest(filter, cutoff, m.cUpStart, 12);
    assert.deepEqual(r, {
      tags: [], personIds: ["p1"], groupId: "g1", includeVideos: true, newerThan: cutoff,
      token: "", have: 0, limit: 12, smallThumbnails: true,
    });
    assert.equal("before" in r, false);
    assert.equal(r.newerThan.getTime(), cutoff.getTime());
  });
  test("the next pages: the upward token and have, the device's own size", () => {
    const r = m.upwardRequest(filter, cutoff, { token: "up1", have: 42, end: false }, 12);
    assert.equal(r.token, "up1");
    assert.equal(r.have, 42);
    assert.equal(r.limit, 0);
    assert.equal(r.newerThan, cutoff);
  });
  test("a place opened from Places: its area on every page", () => {
    const area = { south: 40.4, west: -3.8, north: 40.5, east: -3.6 };
    assert.deepEqual(m.upwardRequest({ ...filter, area }, cutoff, m.cUpStart, 12).area, area);
    assert.deepEqual(m.upwardRequest({ ...filter, area }, cutoff, { token: "up1", have: 3, end: false }, 12).area, area);
  });
});

describe("the answer", () => {
  test("oldest first from a device that honours it: turned newest first", () => {
    const a = m.readUpward({ files: [f("a"), f("b"), f("c")], token: "t", oldestFirst: true });
    assert.deepEqual(a, { kind: "page", files: [f("c"), f("b"), f("a")], token: "t", end: false });
  });
  test("the last page (no token) reaches the top", () => {
    assert.deepEqual(m.readUpward({ files: [f("a")], token: "", oldestFirst: true }), { kind: "page", files: [f("a")], token: "", end: true });
  });
  test("an empty page reaches the top, token or not", () => {
    assert.deepEqual(m.readUpward({ files: [], token: "t", oldestFirst: true }), { kind: "page", files: [], token: "t", end: true });
    assert.deepEqual(m.readUpward({ token: "", oldestFirst: true }), { kind: "page", files: [], token: "", end: true });
  });
  test("oldest_first false: a device before #194, whatever the page holds", () => {
    assert.deepEqual(m.readUpward({ files: [f("a")], token: "t", oldestFirst: false }), { kind: "old" });
    assert.deepEqual(m.readUpward({ files: [], token: "", oldestFirst: false }), { kind: "old" });
  });
  test("the page leaves its input alone", () => {
    const files = [f("a"), f("b")];
    m.readUpward({ files, token: "", oldestFirst: true });
    assert.deepEqual(files, [f("a"), f("b")]);
  });
});

describe("going above the grid", () => {
  test("what the grid shows already stays out, by hash, whatever the path", () => {
    const shown = [f("x", "/old/x.jpg"), f("y")];
    assert.deepEqual(m.notShown([f("w"), f("x", "/new/x.jpg"), f("v")], shown), [f("w"), f("v")]);
  });
  test("nor twice from the page itself", () => {
    assert.deepEqual(m.notShown([f("a"), f("a", "/b/a.jpg"), f("b")], []), [f("a"), f("b")]);
  });
  test("an entry without a hash is never taken for another", () => {
    assert.deepEqual(m.notShown([f(""), f("")], [f("")]), [f(""), f("")]);
  });
  test("indices kept by place move down by what went above", () => {
    assert.equal(m.shiftIndex(0, 12), 12);
    assert.equal(m.shiftIndex(5, 0), 5);
    assert.equal(m.shiftIndex(null, 12), null);
  });
});

describe("a downward page after a jump", () => {
  test("leaves out what the upward search put above, by hash (its oldest copy changed between them)", () => {
    // The grid as shown: two photos from above, one from the jump's own pages.
    const shown = [f("n2"), f("n1"), f("j1")];
    // The next page down brings n1 again at another path, and two new ones.
    assert.deepEqual(m.notShown([f("j2"), f("n1", "/Phones/n1.jpg"), f("j3")], shown), [f("j2"), f("j3")]);
  });
});

describe("the first scroll up near the top starts the upward search", () => {
  const at = (y, height = 9000) => ({ y, height });
  test("near the top: the grid's top within reach", () => {
    assert.equal(m.nearTop(0), true);
    assert.equal(m.nearTop(400), true, "the page is above the grid");
    assert.equal(m.nearTop(-599), true);
    assert.equal(m.nearTop(-m.cUpReach), false);
    assert.equal(m.nearTop(-5000), false);
    assert.equal(m.cUpReach, 600);
  });
  test("a scroll up near the top, however made (the scrollbar dragged too)", () => {
    assert.equal(m.scrollArmsUp(at(900), at(880), -300), true);
    assert.equal(m.scrollArmsUp(at(500), at(0), 64), true, "dragged right up to the top");
  });
  test("not far from the top: the next scroll up near it will", () => {
    assert.equal(m.scrollArmsUp(at(9000), at(8000), -7000), false);
  });
  test("never a scroll down, nor one of a pixel or less", () => {
    assert.equal(m.scrollArmsUp(at(880), at(900), -300), false);
    assert.equal(m.scrollArmsUp(at(900), at(900), -300), false);
    assert.equal(m.scrollArmsUp(at(900), at(899), -300), false);
    assert.equal(m.scrollArmsUp(at(900), at(898.5), -300), true);
  });
  test("never when the page's height changed: the jump, photos put above, a new width", () => {
    // The grid emptied for a jump: the page shrinks and its scroll is cut down.
    assert.equal(m.scrollArmsUp(at(4000, 9000), at(1200, 2100), -100), false);
    // Photos put above, the page scrolled by what they took (it goes down, too).
    assert.equal(m.scrollArmsUp(at(300, 9000), at(1500, 10200), -1300), false);
    // A narrower window: smaller tiles, the photo on top kept in place.
    assert.equal(m.scrollArmsUp(at(900, 9000), at(700, 7600), -200), false);
  });
});

describe("the room kept for the month at the top", () => {
  test("whole: nothing kept", () => {
    assert.deepEqual(m.topReserve(12, 12, 6), { spacerRows: 0, greys: 0 });
    assert.deepEqual(m.topReserve(10, 12, 6), { spacerRows: 0, greys: 0 });
  });
  test("narrower than a row: grey tiles only", () => {
    assert.deepEqual(m.topReserve(4, 1, 6), { spacerRows: 0, greys: 3 });
  });
  test("a few rows: grey tiles, the partial row first", () => {
    // 40 kept, 12 loaded: 28 grey = 4 whole rows of 6 and 4 more.
    assert.deepEqual(m.topReserve(40, 12, 6), { spacerRows: 0, greys: 28 });
  });
  test("thousands: whole rows past the first few are one block", () => {
    // 2000 kept, 12 loaded: 1988 grey = 331 rows of 6 and 2 more; 4 rows drawn.
    const r = m.topReserve(2000, 12, 6);
    assert.deepEqual(r, { spacerRows: 327, greys: 4 * 6 + 2 });
    assert.equal(r.spacerRows * 6 + r.greys, 1988);
  });
  test("the grey tiles always end where the photos start", () => {
    for (const cols of [1, 3, 5, 6, 9]) {
      for (const room of [1, 2, 7, 30, 61, 500]) {
        for (const loaded of [0, 1, 5, 29]) {
          const r = m.topReserve(room, loaded, cols);
          assert.equal(r.spacerRows * cols + r.greys, Math.max(0, room - loaded), `${room}/${loaded}/${cols}`);
          assert.ok(r.greys <= 5 * cols, `${room}/${loaded}/${cols}`);
        }
      }
    }
  });
  test("columns not measured yet: grey tiles only", () => {
    assert.deepEqual(m.topReserve(20, 5, 0), { spacerRows: 0, greys: 15 });
  });
});
