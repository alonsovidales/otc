// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Smart search's state on the web (src/components/smartSearch.ts):
// the switch and where the device's model is (Settings.smart_search_model,
// release 152: downloaded only once Smart search is first turned on) - when
// the search offers "Search photos for …", what Settings' section says and
// tells a screen reader, and how Settings asks the device again while the
// model downloads. Loaded through Vite's SSR loader in Node, as
// places-switch-test.mjs, with the socket's request replaced by the test's:
//
//   node web/scripts/smart-search-test.mjs        (needs web/node_modules)
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
let smart, filter, ws;
// What the test's device answers each request with.
let answer = () => ({});
const asked = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-smart-search-test-vite"),
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
  smart = await server.ssrLoadModule("/src/components/smartSearch.ts");
  filter = await server.ssrLoadModule("/src/components/photoFilter.ts");
});
after(() => server?.close());
beforeEach(() => { asked.length = 0; });

const settings = (smartSearchEnabled, smartSearchModel) =>
  ({ payload: { $case: "respSettings", respSettings: { smartSearchEnabled, smartSearchModel } } });
const model = (m) => ({ ready: false, downloading: false, percent: 0, problem: "", ...m });
const until = async (ok) => {
  for (let i = 0; i < 200 && !ok(); i++) await new Promise((r) => setTimeout(r, 5));
};

// The follow-up questions' timers (3 s), held here to be run by hand; every
// shorter timer (the test's own) runs as usual.
const realSetTimeout = globalThis.setTimeout;
const realClearTimeout = globalThis.clearTimeout;
const held = new Set();
function holdLongTimers() {
  globalThis.setTimeout = (fn, ms, ...args) => {
    if (ms < 3000) return realSetTimeout(fn, ms, ...args);
    const h = { fn, ms };
    held.add(h);
    return h;
  };
  globalThis.clearTimeout = (h) => (held.has(h) ? held.delete(h) : realClearTimeout(h));
}
function releaseTimers() {
  globalThis.setTimeout = realSetTimeout;
  globalThis.clearTimeout = realClearTimeout;
  held.clear();
}
const heldDelays = () => [...held].map((h) => h.ms);
function runHeld() {
  const all = [...held];
  held.clear();
  all.forEach((h) => h.fn());
}

describe("the model's state", () => {
  test("a device that doesn't say (before release 152) has it", () => {
    assert.deepEqual(smart.modelFrom(undefined), smart.cModelReady);
    assert.equal(smart.modelFrom(model({ ready: true })).ready, true);
  });

  test("the percent is a whole number from 0 to 100; a ready model has no problem", () => {
    assert.equal(smart.modelFrom(model({ downloading: true, percent: 41.6 })).percent, 42);
    assert.equal(smart.modelFrom(model({ downloading: true, percent: 140 })).percent, 100);
    assert.equal(smart.modelFrom(model({ downloading: true, percent: -3 })).percent, 0);
    assert.equal(smart.modelFrom(model({ ready: true, problem: "old" })).problem, "");
    assert.equal(smart.modelFrom(model({ downloading: true, problem: "  No connection  " })).problem, "No connection");
  });
});

describe("whether the search offers the description", () => {
  test("only while on, with the model there", () => {
    const ready = smart.cModelReady;
    const downloading = smart.modelFrom(model({ downloading: true, percent: 10 }));
    const neither = smart.modelFrom(model({}));
    assert.equal(smart.offersSmartSearch(true, ready), true);
    assert.equal(smart.offersSmartSearch(true, downloading), false);
    assert.equal(smart.offersSmartSearch(true, neither), false);
    assert.equal(smart.offersSmartSearch(false, ready), false);
    assert.equal(smart.offersSmartSearch(null, ready), false);
  });
});

describe("what Settings' section says", () => {
  test("nothing once ready; the note before; the download while it goes", () => {
    assert.deepEqual(smart.modelView(smart.cModelReady), { kind: "ready" });
    assert.deepEqual(smart.modelView(smart.modelFrom(model({}))), { kind: "note" });
    assert.deepEqual(smart.modelView(smart.modelFrom(model({ downloading: true, percent: 30 }))),
      { kind: "downloading", percent: 30, problem: "" });
    assert.deepEqual(smart.modelView(smart.modelFrom(model({ downloading: true, percent: 30, problem: "No connection" }))),
      { kind: "downloading", percent: 30, problem: "No connection" });
  });

  test("a screen reader hears the start, a problem and the end - not every percent", () => {
    const note = { kind: "note" };
    const ready = { kind: "ready" };
    const at = (percent, problem = "") => ({ kind: "downloading", percent, problem });
    assert.equal(smart.announcement(null, at(5)), null, "what shows at first isn't announced");
    assert.deepEqual(smart.announcement(note, at(0)), { kind: "progress", percent: 0 }, "started");
    assert.equal(smart.announcement(at(0), at(1)), null);
    assert.equal(smart.announcement(at(40), at(41)), null);
    assert.deepEqual(smart.announcement(at(41), at(41, "No connection")), { kind: "problem", text: "No connection" });
    assert.equal(smart.announcement(at(41, "No connection"), at(41, "No connection")), null);
    assert.deepEqual(smart.announcement(at(41, "No connection"), at(41, "Disk full")), { kind: "problem", text: "Disk full" });
    assert.deepEqual(smart.announcement(at(41, "Disk full"), at(42)), { kind: "progress", percent: 42 }, "going again");
    assert.deepEqual(smart.announcement(at(99), ready), { kind: "progress", percent: 100 }, "done");
    assert.equal(smart.announcement(ready, ready), null);
    assert.equal(smart.announcement(note, note), null);
    assert.equal(smart.announcement(note, ready), null, "ready without a download seen here");
  });
});

describe("asked at sign-in", () => {
  test("the switch and the model, as the device says", async () => {
    const cases = [
      [true, undefined, true, true],
      [true, model({ ready: true }), true, true],
      [true, model({ downloading: true, percent: 12 }), true, false],
      [false, model({}), false, false],
      [undefined, undefined, false, false],
    ];
    for (const [on, m, wantOn, wantOffered] of cases) {
      answer = () => settings(on, m);
      const stop = smart.watchSmartSearch();
      await until(() => asked.length > 0 && smart.getSmartSearchOn() === wantOn);
      await until(() => smart.offersSmartSearch(smart.getSmartSearchOn(), smart.getSmartSearchModel()) === wantOffered);
      stop();
      assert.equal(smart.getSmartSearchOn(), wantOn, `on ${on}`);
      assert.equal(smart.offersSmartSearch(smart.getSmartSearchOn(), smart.getSmartSearchModel()), wantOffered, `model ${JSON.stringify(m)}`);
      asked.length = 0;
    }
    assert.ok(asked.every((c) => c === "reqGetSettings"));
  });

  test("off, the description goes", () => {
    smart.setSmartSearchOn(true);
    filter.searchText("kids in the snow");
    assert.equal(filter.getPhotoFilter().text, "kids in the snow");
    smart.setSmartSearchOn(false);
    assert.equal(filter.getPhotoFilter().text, "");
  });
});

describe("while the model downloads", () => {
  test("Settings asks every 3 s until it is there, then stops", async () => {
    holdLongTimers();
    try {
      let percent = 10;
      answer = () => settings(true, model({ downloading: true, percent }));
      const stopWatch = smart.watchSmartSearch();
      await until(() => smart.getSmartSearchModel().percent === 10);
      await new Promise((r) => realSetTimeout(r, 20));
      assert.equal(held.size, 0, "outside Settings, nothing is asked again");
      const stopFollow = smart.followSmartSearchDownload();
      assert.deepEqual(heldDelays(), [smart.cFollowMs], "Settings shows it: every 3 s");
      percent = 55;
      asked.length = 0;
      runHeld();
      await until(() => smart.getSmartSearchModel().percent === 55);
      assert.deepEqual(asked, ["reqGetSettings"]);
      await until(() => held.size === 1);
      assert.deepEqual(heldDelays(), [smart.cFollowMs]);
      answer = () => settings(true, model({ ready: true }));
      runHeld();
      await until(() => smart.getSmartSearchModel().ready);
      await new Promise((r) => realSetTimeout(r, 20));
      assert.equal(held.size, 0, "ready: nothing more is asked");
      assert.equal(smart.offersSmartSearch(smart.getSmartSearchOn(), smart.getSmartSearchModel()), true);
      stopFollow();
      stopWatch();
    } finally {
      releaseTimers();
    }
  });

  test("leaving Settings, nothing more is asked", async () => {
    holdLongTimers();
    try {
      answer = () => settings(true, model({ downloading: true, percent: 20 }));
      const stopWatch = smart.watchSmartSearch();
      await until(() => smart.getSmartSearchModel().percent === 20);
      await new Promise((r) => realSetTimeout(r, 20));
      const stopFollow = smart.followSmartSearchDownload();
      assert.deepEqual(heldDelays(), [smart.cFollowMs]);
      stopFollow();
      assert.equal(held.size, 0);
      stopWatch();
    } finally {
      releaseTimers();
    }
  });

  test("turned off, nothing more is asked", async () => {
    holdLongTimers();
    try {
      answer = () => settings(true, model({ downloading: true, percent: 21 }));
      const stopWatch = smart.watchSmartSearch();
      await until(() => smart.getSmartSearchModel().percent === 21);
      await new Promise((r) => realSetTimeout(r, 20));
      const stopFollow = smart.followSmartSearchDownload();
      assert.equal(held.size, 1);
      smart.setSmartSearchOn(false);
      assert.equal(held.size, 0);
      stopFollow();
      stopWatch();
    } finally {
      releaseTimers();
    }
  });

  test("an answer asked for before the switch changed doesn't undo it", async () => {
    let reply;
    answer = () => new Promise((r) => { reply = r; });
    const stopWatch = smart.watchSmartSearch();
    await until(() => reply !== undefined);
    smart.setSmartSearchOn(false);
    // A refresh after the change asks again rather than sharing the old one.
    const first = reply;
    const again = smart.refreshSmartSearch();
    await until(() => reply !== first);
    first(settings(true, model({ downloading: true, percent: 5 })));
    reply(settings(false, model({ downloading: false })));
    assert.equal(await again, true);
    assert.equal(smart.getSmartSearchOn(), false);
    assert.equal(smart.getSmartSearchModel().downloading, false);
    stopWatch();
  });
});
