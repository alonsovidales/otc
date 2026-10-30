// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a share link's ZIP page's rules (src/components/zipDownload.ts):
// the percentage never says 100 before the last byte, and after the file is
// handed to the browser the tab closes itself only when nothing took the
// focus or changed its visibility meanwhile (a browser asking where to save,
// or whether the site may download, keeps it). Loaded through Vite's SSR
// loader in Node, as download-link-test.mjs does:
//
//   node web/scripts/zip-download-test.mjs        (needs web/node_modules)
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
    cacheDir: join(tmpdir(), "otc-zip-download-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/zipDownload.ts");
});
after(() => server?.close());

describe("the percentage", () => {
  test("rounded down: 100 only once every byte is in", () => {
    const MB = 1 << 20;
    assert.equal(m.receivedPercent(0, 100), 0);
    assert.equal(m.receivedPercent(1, 3), 33);
    assert.equal(m.receivedPercent(2, 3), 66);
    assert.equal(m.receivedPercent(99, 100), 99);
    // A 1 GB archive with its last 4 MB part on its way: 99.6% read as
    // 100% when rounded.
    assert.equal(m.receivedPercent(1020 * MB, 1024 * MB), 99);
    assert.equal(m.receivedPercent(1024 * MB - 1, 1024 * MB), 99);
    assert.equal(m.receivedPercent(1024 * MB, 1024 * MB), 100);
    assert.equal(m.receivedPercent(150, 100), 100);
  });
  test("no size known yet: no percentage", () => {
    assert.equal(m.receivedPercent(0, 0), null);
    assert.equal(m.receivedPercent(10, -1), null);
    assert.equal(m.receivedPercent(10, Number.NaN), null);
  });
});

// A window or document that records its listeners and fires events.
function source() {
  const listeners = new Map();
  return {
    addEventListener: (type, fn) => listeners.set(type, [...(listeners.get(type) ?? []), fn]),
    removeEventListener: (type, fn) => listeners.set(type, (listeners.get(type) ?? []).filter((f) => f !== fn)),
    fire: (type) => (listeners.get(type) ?? []).forEach((fn) => fn()),
    count: () => [...listeners.values()].reduce((n, l) => n + l.length, 0),
  };
}
// A clock moved by hand.
function clock() {
  let now = 0;
  let timers = [];
  return {
    setTimeout: (fn, ms) => { const t = { fn, at: now + ms }; timers.push(t); return t; },
    clearTimeout: (t) => { timers = timers.filter((x) => x !== t); },
    advance(ms) {
      now += ms;
      for (const t of timers.filter((x) => x.at <= now)) { timers = timers.filter((x) => x !== t); t.fn(); }
    },
    pending: () => timers.length,
  };
}

describe("after the hand-off", () => {
  test("nothing happened: the tab may close, after the wait and not before", () => {
    const win = source(), doc = source(), c = clock();
    const said = [];
    m.watchHandOff(win, doc, m.cCloseAfterMs, (ok) => said.push(ok), c);
    c.advance(m.cCloseAfterMs - 1);
    assert.deepEqual(said, []);
    c.advance(1);
    assert.deepEqual(said, [true]);
    assert.equal(win.count() + doc.count(), 0, "listeners removed");
  });
  test("the page lost the focus (a dialog, a question from the browser): it stays", () => {
    const win = source(), doc = source(), c = clock();
    const said = [];
    m.watchHandOff(win, doc, 2000, (ok) => said.push(ok), c);
    c.advance(300);
    win.fire("blur");
    c.advance(2000);
    assert.deepEqual(said, [false]);
    assert.equal(win.count() + doc.count(), 0);
  });
  test("hidden or shown meanwhile, or the focus coming back: it stays", () => {
    for (const [target, type] of [["doc", "visibilitychange"], ["win", "focus"]]) {
      const win = source(), doc = source(), c = clock();
      const said = [];
      m.watchHandOff(win, doc, 2000, (ok) => said.push(ok), c);
      (target === "doc" ? doc : win).fire(type);
      c.advance(2000);
      assert.deepEqual(said, [false], type);
    }
  });
  test("an event after the wait changes nothing", () => {
    const win = source(), doc = source(), c = clock();
    const said = [];
    m.watchHandOff(win, doc, 2000, (ok) => said.push(ok), c);
    c.advance(2000);
    win.fire("blur");
    c.advance(5000);
    assert.deepEqual(said, [true]);
  });
  test("it closes only when quiet, and then in the background or with the focus", () => {
    assert.equal(m.mayClose(true, false, true), true, "shown, focused: nothing asked");
    assert.equal(m.mayClose(true, true, false), true, "in the background: nobody looking");
    assert.equal(m.mayClose(true, false, false), false, "shown without the focus: something may be asking");
    assert.equal(m.mayClose(false, false, true), false);
    assert.equal(m.mayClose(false, true, false), false);
  });
  test("stopped (the page went): nothing said, nothing left listening", () => {
    const win = source(), doc = source(), c = clock();
    const said = [];
    const stop = m.watchHandOff(win, doc, 2000, (ok) => said.push(ok), c);
    stop();
    stop();
    c.advance(5000);
    assert.deepEqual(said, []);
    assert.equal(c.pending(), 0);
    assert.equal(win.count() + doc.count(), 0);
  });
});
