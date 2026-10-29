// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Places' arithmetic (src/components/placesMath.ts): which tiles
// the device is asked for, at which zoom, and how places become pins. The
// web app has no test runner, so this loads the module through Vite's SSR
// loader in Node, as newer-photos-test.mjs does:
//
//   node web/scripts/places-test.mjs        (needs web/node_modules)
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
    cacheDir: join(tmpdir(), "otc-places-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/placesMath.ts");
});
after(() => server?.close());

describe("the device's zoom", () => {
  test("is MapLibre's plus one, whole, within 0 to 20", () => {
    assert.equal(m.deviceZoom(0), 1);
    assert.equal(m.deviceZoom(0.99), 1);
    assert.equal(m.deviceZoom(1), 2);
    assert.equal(m.deviceZoom(5.5), 6);
    assert.equal(m.deviceZoom(-1), 0);
    assert.equal(m.deviceZoom(19.5), 20);
    assert.equal(m.deviceZoom(25), 20);
  });
});

describe("tile numbers", () => {
  test("match the slippy-map formula the device uses", () => {
    // Madrid at zoom 10: the tile every web map names 501/386.
    assert.equal(m.tileX(-3.7038, 10), 501);
    assert.equal(m.tileY(40.4168, 10), 386);
    // The world is one tile at zoom 0.
    assert.equal(m.tileX(179.99, 0), 0);
    assert.equal(m.tileY(-84, 0), 0);
  });
  test("clamp the poles to the map's edge", () => {
    assert.equal(m.tileY(90, 3), 0);
    assert.equal(m.tileY(-90, 3), 7);
  });
  test("leave a longitude past the antimeridian unwrapped, for the device's modulo", () => {
    assert.equal(m.tileX(181, 2), 4);
    assert.equal(m.wrapX(4, 2), 0);
    assert.equal(m.wrapX(-1, 2), 3);
  });
});

describe("the tiles a view needs", () => {
  test("cover the view and a margin, once each, the middle first", () => {
    const tiles = m.tilesFor({ west: -4, east: -3.4, south: 40.3, north: 40.6 }, 10, 1);
    const keys = new Set(tiles.map((t) => `${t.x}/${t.y}`));
    assert.equal(keys.size, tiles.length);
    // Columns 500 to 502 and rows 385 to 386, and one more all round.
    assert.equal(tiles.length, 5 * 4);
    for (const k of ["501/386", "499/384", "503/387"]) assert.ok(keys.has(k), k);
    // The middle of the view comes first.
    assert.ok(["501/385", "501/386"].includes(`${tiles[0].x}/${tiles[0].y}`), `${tiles[0].x}/${tiles[0].y}`);
  });
  test("across the antimeridian name the tiles on both sides, wrapped", () => {
    const tiles = m.tilesFor({ west: 170, east: 190, south: -20, north: -10 }, 3, 0);
    const xs = new Set(tiles.map((t) => t.x));
    assert.ok(xs.has(7) && xs.has(0), [...xs].join(","));
    assert.ok(tiles.every((t) => t.x >= 0 && t.x < 8));
  });
  test("of a view wider than the world are the world's columns, once", () => {
    const tiles = m.tilesFor({ west: -400, east: 400, south: -80, north: 80 }, 1, 1);
    assert.equal(tiles.length, 4);
  });
  test("go in batches of at most 64", () => {
    const b = m.batches(Array.from({ length: 150 }, (_, i) => i));
    assert.deepEqual(b.map((x) => x.length), [64, 64, 22]);
  });
});

describe("pins", () => {
  const place = (key, x, y, count, coverTime = 0, area = { south: 0, west: 0, north: 0, east: 0 }) =>
    ({ key, latitude: y, longitude: x, count, area, cover: `c${key}`, coverTime });
  const at = (p) => ({ x: p.longitude, y: p.latitude });

  test("too close on screen become one", () => {
    const pins = m.mergePins([
      place("a", 0, 0, 3, 1, { south: 0, west: 0, north: 1, east: 1 }),
      place("b", 30, 0, 10, 5, { south: 2, west: 2, north: 3, east: 3 }),
      place("c", 200, 0, 1),
    ], at, 54);
    assert.equal(pins.length, 2);
    const big = pins.find((p) => p.key === "b");
    assert.equal(big.count, 13);
    assert.equal(big.x, 30);
    assert.deepEqual(big.area, { south: 0, west: 0, north: 3, east: 3 });
    // The newest cover stands for the pin.
    assert.equal(big.cover, "cb");
  });
  test("stand at the member nearest their weighted mean, never between them", () => {
    // 5 photos at 0, 4 at 20 and 1 at 40: the mean is 12, nearest the 4.
    const pins = m.mergePins([place("a", 0, 0, 5), place("c", 20, 0, 4), place("b", 40, 0, 1)], at, 54);
    assert.equal(pins.length, 1);
    assert.equal(pins[0].count, 10);
    assert.equal(pins[0].x, 20);
    assert.equal(pins[0].longitude, 20);
    // The element stays the biggest place's.
    assert.equal(pins[0].key, "a");
  });
  test("far enough apart stay apart", () => {
    const pins = m.mergePins([place("a", 0, 0, 1), place("b", 60, 0, 1)], at, 54);
    assert.equal(pins.length, 2);
  });
  test("join across the antimeridian the short way, the area crossing it", () => {
    // Drawn side by side (the map shows the copy of the world nearest its
    // middle), at 179 and -179.
    const pins = m.mergePins([
      { key: "a", latitude: 0, longitude: 179, count: 2, area: { south: 0, west: 178.5, north: 1, east: 179.5 }, cover: 1, coverTime: 0 },
      { key: "b", latitude: 0, longitude: -179, count: 1, area: { south: 0, west: -179.5, north: 1, east: -178.5 }, cover: 2, coverTime: 0 },
    ], (p) => ({ x: p.longitude > 0 ? 0 : 20, y: 0 }), 54);
    assert.equal(pins.length, 1);
    assert.deepEqual(pins[0].area, { south: 0, west: 178.5, north: 1, east: -178.5 });
  });
  test("take the newer cover from a smaller place joining", () => {
    const pins = m.mergePins([place("a", 0, 0, 5, 1), place("b", 10, 0, 2, 9)], at, 54);
    assert.equal(pins.length, 1);
    assert.equal(pins[0].cover, "cb");
  });
});

describe("areas", () => {
  test("a spot is 0 m across, a city block about a hundred", () => {
    assert.equal(m.areaSpanMetres({ south: 1, west: 1, north: 1, east: 1 }), 0);
    const block = m.areaSpanMetres({ south: 40.4168, west: -3.7038, north: 40.4175, east: -3.7030 });
    assert.ok(block > 80 && block < 130, String(block));
  });
  test("framed across the antimeridian run on past 180", () => {
    assert.deepEqual(m.fitCorners({ south: -20, west: 175, north: -10, east: -175 }), [[175, -20], [185, -10]]);
    assert.deepEqual(m.fitCorners({ south: 1, west: 2, north: 3, east: 4 }), [[2, 1], [4, 3]]);
  });
});
