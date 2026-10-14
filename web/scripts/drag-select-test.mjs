// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for picking a run by dragging (issue #200,
// src/components/dragSelect.ts). The web app has no test runner, so this
// loads the module through Vite's SSR loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/drag-select-test.mjs        (needs web/node_modules)
//
// The rules are the same as the iPhone's DragSelectTests.swift and
// Android's DragSelectTest.kt: change all three together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let d; // the module under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-drag-select-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  d = await server.ssrLoadModule("/src/components/dragSelect.ts");
});
after(() => server?.close());

// Items as the grid shows them; the selection is kept by key.
const order = ["a", "b", "c", "d", "e", "f", "g"];
const id = (x) => x;
const run = (base, start, end, items = order, skip) => d.dragSelection(base, items, id, start, end, skip);

describe("the run a drag picks", () => {
  test("a long press without moving picks the item pressed", () => {
    assert.deepEqual(run([], 2, 2), ["c"]);
  });
  test("forwards: from the item pressed to the one under the pointer, both included", () => {
    assert.deepEqual(run([], 1, 4), ["b", "c", "d", "e"]);
  });
  test("backwards: the same run, in display order", () => {
    assert.deepEqual(run([], 4, 1), ["b", "c", "d", "e"]);
  });
  test("moving back shrinks it to what is now covered", () => {
    const base = [];
    assert.deepEqual(run(base, 1, 5), ["b", "c", "d", "e", "f"]);
    assert.deepEqual(run(base, 1, 2), ["b", "c"]);
    assert.deepEqual(run(base, 1, 1), ["b"]);
    // ...and past the start, the other way.
    assert.deepEqual(run(base, 1, 0), ["a", "b"]);
  });
  test("it adds to what was picked before, which stays first and in its order", () => {
    // f, then a were picked; the drag covers b..d: the post numbers run on.
    assert.deepEqual(run(["f", "a"], 1, 3), ["f", "a", "b", "c", "d"]);
  });
  test("what it passed over goes back to how it was when the run shrinks", () => {
    const base = ["c", "g"];
    assert.deepEqual(run(base, 0, 5), ["c", "g", "a", "b", "d", "e", "f"]);
    // Back to a..b: c was picked before the drag and stays; d, e, f go.
    assert.deepEqual(run(base, 0, 1), ["c", "g", "a", "b"]);
    // Back to the start: only what was picked before, and a.
    assert.deepEqual(run(base, 0, 0), ["c", "g", "a"]);
  });
  test("a press on something picked keeps it picked (a drag never unpicks)", () => {
    assert.deepEqual(run(["c"], 2, 2), ["c"]);
    assert.deepEqual(run(["c"], 2, 4), ["c", "d", "e"]);
  });
  test("the base is never changed", () => {
    const base = Object.freeze(["b"]);
    assert.deepEqual(run(base, 0, 3), ["b", "a", "c", "d"]);
    assert.deepEqual(base, ["b"]);
  });
  test("an end past either side of the list stops at its ends", () => {
    assert.deepEqual(run([], 5, 99), ["f", "g"]);
    assert.deepEqual(run([], 1, -3), ["a", "b"]);
  });
  test("Files' '..' is never picked, even inside the run", () => {
    const rows = ["..", "Docs", "a.txt", "b.txt"];
    const up = (k) => k === "..";
    assert.deepEqual(run([], 2, 0, rows, up), ["Docs", "a.txt"]);
    assert.deepEqual(run([], 3, 0, rows, up), ["Docs", "a.txt", "b.txt"]);
  });
  test("an item listed twice (a search sent it again) is picked once", () => {
    assert.deepEqual(run([], 0, 3, ["a", "b", "a", "c"]), ["a", "b", "c"]);
  });
  test("items, not just keys: the selection keeps the base's own objects", () => {
    const files = order.map((path) => ({ path }));
    const kept = { path: "c" }; // picked from an earlier search
    const next = d.dragSelection([kept], files, (f) => f.path, 1, 3);
    assert.equal(next[0], kept);
    assert.deepEqual(next.map((f) => f.path), ["c", "b", "d"]);
  });
});

describe("an item found again after the items changed", () => {
  test("found where it was while it is still there (pages only add after it)", () => {
    assert.equal(d.findItem(order, id, "c", 2), 2);
    assert.equal(d.findItem([...order, "h", "i"], id, "c", 2), 2);
  });
  test("found where it moved to", () => {
    assert.equal(d.findItem(["a", "c", "d"], id, "c", 2), 1);
  });
  test("gone: -1", () => {
    assert.equal(d.findItem(["a", "b", "d"], id, "c", 2), -1);
    assert.equal(d.findItem([], id, "c", 0), -1);
  });
  test("an item that vanished inside the run just isn't picked", () => {
    const now = ["a", "b", "d", "e"];
    const start = d.findItem(now, id, "b", 1);
    assert.deepEqual(d.dragSelection([], now, id, start, 3), ["b", "d", "e"]);
  });
});

// What a drag does when the items change under it (a page came, the
// listing came again) - the same rule as the phones'.
describe("a drag whose items change", () => {
  const ends = (startKey, start, endKey, end) => ({ startKey, start, endKey, end });
  test("a page added after them: both ends stay where they were", () => {
    assert.deepEqual(d.relocate(ends("b", 1, "e", 4), [...order, "h"], id), ends("b", 1, "e", 4));
  });
  test("items before them removed: both ends found where they went", () => {
    assert.deepEqual(d.relocate(ends("c", 2, "f", 5), ["c", "d", "e", "f", "g"], id), ends("c", 0, "f", 3));
  });
  test("the item pressed gone: the drag is over (null), however the rest moved", () => {
    assert.equal(d.relocate(ends("b", 1, "e", 4), ["a", "c", "d", "e"], id), null);
    assert.equal(d.relocate(ends("b", 1, "b", 1), ["a", "c"], id), null);
    assert.equal(d.relocate(ends("b", 1, "e", 4), [], id), null);
  });
  test("only the item under the pointer gone: end -1, the selection waits", () => {
    assert.deepEqual(d.relocate(ends("b", 1, "e", 4), ["a", "b", "c", "d", "f"], id), ends("b", 1, "e", -1));
  });
  test("an end already lost stays lost until the pointer is over another item", () => {
    assert.deepEqual(d.relocate(ends("b", 1, "e", -1), ["a", "b", "c", "d", "e"], id), ends("b", 1, "e", -1));
  });
  // The hook: with the pressed item gone it commits nothing more (the
  // selection stays as it stood) and the release is still no click; with
  // only the end gone it commits nothing until a move finds another item.
  test("after the end went, the next item under the pointer picks the run to it", () => {
    const now = ["a", "b", "c", "d", "f", "g"]; // e went
    const at = d.relocate(ends("b", 1, "e", 4), now, id);
    assert.equal(at.end, -1);
    // The pointer is now over f (index 4 now).
    assert.deepEqual(d.dragSelection([], now, id, at.start, 4), ["b", "c", "d", "f"]);
  });
});

// A pick the view drops mid-drag (People's refresh dropping a face that
// went, Images dropping a deleted photo) - the same rule as the phones'.
describe("a pick that goes from the items mid-drag", () => {
  test("it goes from the base too, so the next move doesn't bring it back", () => {
    const now = ["a", "b", "c", "d", "e", "g"]; // f went
    const base = d.keptBase(["f", "a"], order, now, id);
    assert.deepEqual(base, ["a"]);
    assert.deepEqual(d.dragSelection(base, now, id, 2, 4), ["a", "c", "d", "e"]);
  });
  test("nor when it comes back later: it was no longer picked", () => {
    const mid = ["a", "b", "c", "d", "e", "g"];
    let base = d.keptBase(["f", "a"], order, mid, id);
    base = d.keptBase(base, mid, order, id);
    assert.deepEqual(base, ["a"]);
    // A run that doesn't cover it leaves it out; one that does takes it.
    assert.deepEqual(d.dragSelection(base, order, id, 1, 2), ["a", "b", "c"]);
    assert.deepEqual(d.dragSelection(base, order, id, 4, 6), ["a", "e", "f", "g"]);
  });
  test("one never among the items (picked in an earlier search) stays", () => {
    const now = ["a", "b", "d", "e", "f", "g"]; // c went
    assert.deepEqual(d.keptBase(["x", "c", "b"], order, now, id), ["x", "b"]);
  });
  test("nothing went (a page came, the items only moved): the same array", () => {
    const base = ["b", "x"];
    assert.equal(d.keptBase(base, order, [...order, "h"], id), base);
    assert.equal(d.keptBase(base, order, ["g", "f", "e", "d", "c", "b", "a"], id), base);
    assert.equal(d.keptBase(base, order, order, id), base);
    // Something not picked went: the same array too.
    assert.equal(d.keptBase(base, order, ["a", "b", "c"], id), base);
  });
  test("an empty base stays empty", () => {
    const base = [];
    assert.equal(d.keptBase(base, order, [], id), base);
  });
  test("every pick gone: an empty base", () => {
    assert.deepEqual(d.keptBase(["a", "b"], order, ["c"], id), []);
  });
  test("the base keeps its own objects and order, and is never changed", () => {
    const files = order.map((path) => ({ path }));
    const kept = { path: "x" }; // from an earlier search
    const base = Object.freeze([files[5], kept, files[1], files[3]]);
    const next = d.keptBase(base, files, files.filter((f) => f.path !== "b"), (f) => f.path);
    assert.deepEqual(next, [files[5], kept, files[3]]);
    assert.equal(next[1], kept);
    assert.equal(base.length, 4);
  });
});

describe("telling selections apart", () => {
  test("same items in the same order", () => {
    assert.equal(d.sameSelection(["a", "b"], ["a", "b"], id), true);
    assert.equal(d.sameSelection(["a", "b"], ["b", "a"], id), false);
    assert.equal(d.sameSelection(["a"], ["a", "b"], id), false);
    assert.equal(d.sameSelection([], [], id), true);
  });
});

describe("when a press is a drag", () => {
  test("more than 8 px in any direction; a click's wobble is still a click", () => {
    assert.equal(d.cDragThresholdPx, 8);
    assert.equal(d.pastThreshold(0, 0), false);
    assert.equal(d.pastThreshold(8, 0), false);
    assert.equal(d.pastThreshold(0, -8), false);
    assert.equal(d.pastThreshold(6, 6), true); // 8.49 px
    assert.equal(d.pastThreshold(9, 0), true);
    assert.equal(d.pastThreshold(-3, 8.5), true);
  });
  test("a touch is held 450 ms, as the long press always was", () => {
    assert.equal(d.cLongPressMs, 450);
  });
});

describe("scrolling near the edges", () => {
  const top = 64; // below the top bar
  const bottom = 800; // the window's bottom
  test("nothing between the zones", () => {
    assert.equal(d.edgeSpeed(400, top, bottom), 0);
    assert.equal(d.edgeSpeed(top + 56, top, bottom), 0);
    assert.equal(d.edgeSpeed(bottom - 56, top, bottom), 0);
  });
  test("up near the top, down near the bottom, in proportion to the depth", () => {
    assert.equal(d.edgeSpeed(top + 42, top, bottom), -300); // a quarter in
    assert.equal(d.edgeSpeed(top + 28, top, bottom), -600); // half way
    assert.equal(d.edgeSpeed(bottom - 14, top, bottom), 900); // three quarters
    const a = d.edgeSpeed(top + 50, top, bottom);
    const b = d.edgeSpeed(top + 20, top, bottom);
    assert.ok(a < 0 && b < a, `${a} ${b}`);
  });
  test("at most 1200 px/s, at the edge and past it (a mouse outside the window)", () => {
    assert.equal(d.edgeSpeed(top, top, bottom), -d.cEdgeMaxSpeed);
    assert.equal(d.edgeSpeed(-300, top, bottom), -1200);
    assert.equal(d.edgeSpeed(bottom, top, bottom), 1200);
    assert.equal(d.edgeSpeed(bottom + 500, top, bottom), 1200);
  });
  test("the same speed at the same depth at either end", () => {
    assert.equal(d.edgeSpeed(top + 30, top, bottom), -d.edgeSpeed(bottom - 30, top, bottom));
  });
  test("a short window: each zone a third of what can be seen", () => {
    // 64..184: zones of 40, so 104..144 stops.
    assert.equal(d.edgeSpeed(124, 64, 184), 0);
    assert.equal(d.edgeSpeed(104, 64, 184), 0);
    assert.equal(d.edgeSpeed(144, 64, 184), 0);
    assert.equal(d.edgeSpeed(84, 64, 184), -600);
    assert.equal(d.edgeSpeed(64, 64, 184), -1200);
    assert.equal(d.edgeSpeed(174, 64, 184), 900);
    assert.equal(d.edgeSpeed(184, 64, 184), 1200);
    // Up to 168 px, a third is still the full 56.
    assert.equal(d.edgeSpeed(64 + 28, 64, 64 + 168), -600);
  });
  test("a window whose third is under 8 px scrolls nothing", () => {
    assert.equal(d.cEdgeMinZonePx, 8);
    // 64..87: a third is 7.67 px.
    for (const y of [-100, 64, 70, 80, 87, 200]) assert.equal(d.edgeSpeed(y, 64, 87), 0, `${y}`);
    // 64..88: 8 px exactly still scrolls.
    assert.equal(d.edgeSpeed(64, 64, 88), -1200);
    assert.equal(d.edgeSpeed(88, 64, 88), 1200);
    assert.equal(d.edgeSpeed(76, 64, 88), 0);
    assert.equal(d.edgeSpeed(100, 100, 100), 0);
    assert.equal(d.edgeSpeed(100, 200, 100), 0);
  });
});

describe("when the edges may scroll", () => {
  const top = 64;
  const bottom = 800;
  test("a mouse drag scrolls at once: it moved more than 8 px to begin", () => {
    const p = d.dragPointer(400, bottom - 20, true);
    assert.equal(p.armed, true);
    assert.ok(d.pointerSpeed(p, top, bottom) > 0);
  });
  test("a long press held still in the bottom band never scrolls", () => {
    const p = d.dragPointer(200, bottom - 25, false);
    assert.equal(d.pointerSpeed(p, top, bottom), 0);
    // A finger's tremor while it is held: still nothing.
    d.movePointer(p, 203, bottom - 20);
    d.movePointer(p, 197, bottom - 30);
    assert.equal(p.armed, false);
    assert.equal(d.pointerSpeed(p, top, bottom), 0);
  });
  test("nor in the top band", () => {
    const p = d.dragPointer(200, top + 10, false);
    d.movePointer(p, 200, top + 4);
    assert.equal(d.pointerSpeed(p, top, bottom), 0);
  });
  test("once moved more than 8 px from where the long press fired, it does", () => {
    const p = d.dragPointer(200, bottom - 25, false);
    d.movePointer(p, 200, bottom - 16); // 9 px down
    assert.equal(p.armed, true);
    assert.equal(d.pointerSpeed(p, top, bottom), d.edgeSpeed(bottom - 16, top, bottom));
    assert.ok(d.pointerSpeed(p, top, bottom) > 0);
  });
  test("measured from where it fired, not from where the press began", () => {
    // The finger crept 7 px during the hold (still a press), the long
    // press fired there; 7 px more is under 8 from that point.
    const p = d.dragPointer(200, bottom - 18, false);
    d.movePointer(p, 200, bottom - 11);
    assert.equal(p.armed, false);
  });
  test("dragged into a band after moving: it scrolls; and armed it stays", () => {
    const p = d.dragPointer(200, 400, false);
    d.movePointer(p, 200, 420);
    assert.equal(d.pointerSpeed(p, top, bottom), 0); // not in a band yet
    d.movePointer(p, 200, bottom - 10);
    assert.ok(d.pointerSpeed(p, top, bottom) > 0);
    d.movePointer(p, 200, 400); // back where it fired
    assert.equal(p.armed, true);
    d.movePointer(p, 200, top + 5);
    assert.ok(d.pointerSpeed(p, top, bottom) < 0);
  });
  test("a long press held still in a short window's band: nothing either", () => {
    const p = d.dragPointer(200, 80, false);
    assert.equal(d.pointerSpeed(p, 64, 184), 0);
  });
});

describe("where the item under the pointer is looked for", () => {
  const grid = { left: 100, right: 900 };
  test("the pointer itself inside the grid and the window", () => {
    assert.deepEqual(d.hitPoint(400, 300, grid, 64, 800), { x: 400, y: 300 });
  });
  test("beside the grid: its nearest edge, so the rows still count", () => {
    assert.deepEqual(d.hitPoint(20, 300, grid, 64, 800), { x: 101, y: 300 });
    assert.deepEqual(d.hitPoint(1200, 300, grid, 64, 800), { x: 899, y: 300 });
  });
  test("above or below what can be seen: the first or last row in sight", () => {
    assert.deepEqual(d.hitPoint(400, 10, grid, 64, 800), { x: 400, y: 65 });
    assert.deepEqual(d.hitPoint(400, 950, grid, 64, 800), { x: 400, y: 799 });
  });
});
