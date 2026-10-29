// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Places' switch on the web (src/components/places.ts and what
// placesData.ts does with the device's answers): the map is there only
// while the device says Places is on, a device before Places has none, a
// map told "places_off" goes, and while the device is still reading where
// the photos were taken its answers are asked again sooner. Loaded through
// Vite's SSR loader in Node, as places-test.mjs, with the socket's request
// replaced by the test's:
//
//   node web/scripts/places-switch-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too: change them together.
import { after, before, beforeEach, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
globalThis.window = {
  localStorage: globalThis.localStorage,
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en" }, configurable: true, writable: true });

let server;
let places, data, filter, ws;
// What the test's device answers each request with.
let answer = () => ({});
const asked = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-places-switch-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  ws = await server.ssrLoadModule("/src/net/useWS.ts");
  ws.useWS.request = async (build) => {
    const env = {};
    build(env);
    asked.push(env.payload?.$case);
    return answer(env);
  };
  places = await server.ssrLoadModule("/src/components/places.ts");
  data = await server.ssrLoadModule("/src/components/placesData.ts");
  filter = await server.ssrLoadModule("/src/components/photoFilter.ts");
});
after(() => server?.close());
beforeEach(() => { asked.length = 0; });

const settings = (placesEnabled) => ({ payload: { $case: "respSettings", respSettings: { placesEnabled } } });
const until = async (ok) => {
  for (let i = 0; i < 100 && !ok(); i++) await new Promise((r) => setTimeout(r, 5));
};

describe("whether Places shows", () => {
  test("follows the device's Settings, asked at sign-in", async () => {
    for (const [said, want] of [[true, true], [false, false], [undefined, false]]) {
      answer = () => settings(said);
      const stop = places.watchPlaces();
      await until(() => places.getPlacesOn() === want);
      stop();
      assert.equal(places.getPlacesOn(), want, `placesEnabled ${said}`);
      assert.equal(store.get("otc_places"), want ? "1" : "0", "remembered for the next load");
    }
    assert.ok(asked.every((c) => c === "reqGetSettings"), "asks Settings, not the map");
  });

  test("turned off, an open area of the map goes", () => {
    places.setPlacesOn(true);
    filter.showArea({ south: 1, west: 2, north: 3, east: 4 });
    assert.ok(filter.getPhotoFilter().area);
    places.setPlacesOn(false);
    assert.equal(filter.getPhotoFilter().area, null);
    assert.equal(places.getPlacesOn(), false);
  });

  test("a map the device tells places_off goes", async () => {
    places.setPlacesOn(true);
    answer = () => ({ error: true, errorCode: "places_off", errorMessage: "Places is off" });
    const loc = await data.askLocated({ tags: [], personIds: [], groupId: "" });
    assert.equal(loc, null);
    assert.equal(places.getPlacesOn(), false);
  });
});

describe("while the device reads where the photos were taken", () => {
  test("its answers are kept only a short while", async () => {
    places.setPlacesOn(true);
    const f = { tags: ["beach"], personIds: [], groupId: "" };
    const fk = data.filterKeyOf(f);
    const reply = (reading) => () => ({ payload: { $case: "respPhotoPlaces", respPhotoPlaces: { places: [], locatedTotal: 3, locatedArea: { south: 1, west: 1, north: 2, east: 2 }, zoom: 0, reading } } });
    const realNow = Date.now;
    try {
      let now = 1_000_000;
      Date.now = () => now;
      answer = reply(true);
      const loc = await data.askLocated(f);
      assert.equal(loc.reading, true);
      now += data.cKeepReadingMs - 1;
      assert.ok(data.locatedOf(fk), "kept until it ages");
      now += 2;
      assert.equal(data.locatedOf(fk), null, "asked again once it aged");
      answer = reply(false);
      assert.equal((await data.askLocated(f)).reading, false);
      now += data.cKeepReadingMs * 2;
      assert.ok(data.locatedOf(fk), "done reading: kept the usual 5 minutes");
    } finally {
      Date.now = realNow;
    }
  });
});
