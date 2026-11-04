// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for asking the device to stream a file (src/net/media.ts): what
// its answer comes to. The post composer's trimmer reads a video in
// pieces only when the device says it isn't worth streaming, or can't
// stream at all (a device before issue #110); a failure is a failure -
// falling back would read a long video whole into the page. Loaded
// through Vite's SSR loader in Node, the socket's request replaced by the
// test's, as smart-search-test.mjs:
//
//   node web/scripts/stream-url-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
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
let media, ws;
// What the test's device answers with (or throws).
let answer = () => ({});
const asked = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-stream-url-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  ws = await server.ssrLoadModule("/src/net/useWS.ts");
  ws.useWS.request = async (build) => {
    const env = {};
    build(env);
    asked.push(env.payload);
    return answer(env);
  };
  media = await server.ssrLoadModule("/src/net/media.ts");
});
after(() => server?.close());

const bridge = { error: true, errorMessage: "This device isn't reachable right now.", payload: { $case: "respAck", respAck: { ok: false, code: "device_unreachable", errorMsg: "" } } };

describe("asking to stream a file", () => {
  test("a URL is where to play it from, asked by the file's path", async () => {
    answer = () => ({ payload: { $case: "respMediaUrl", respMediaUrl: { url: "/media/abc" } } });
    asked.length = 0;
    assert.deepEqual(await media.askStreamURL({ path: "/Videos/a.mp4" }), { url: "/media/abc" });
    assert.deepEqual(asked, [{ $case: "reqGetMediaUrl", reqGetMediaUrl: { path: "/Videos/a.mp4" } }]);
    assert.equal(await media.requestStreamURL({ path: "/Videos/a.mp4" }), "/media/abc");
  });
  test("no URL: not worth streaming, read it the old way", async () => {
    answer = () => ({ payload: { $case: "respMediaUrl", respMediaUrl: { url: "" } } });
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { whole: true });
    assert.equal(await media.requestStreamURL({ path: "/a.mp4" }), null);
  });
  test("a device too old to stream says so by not knowing the request", async () => {
    answer = () => ({ error: true, errorCode: "unknown_payload", errorMessage: "unknown payload" });
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { old: true });
  });
  test("the device's refusal is a failure, in its words", async () => {
    answer = () => ({ error: true, errorCode: "not_found", errorMessage: "no such file" });
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { failed: "no such file" });
    assert.equal(await media.requestStreamURL({ path: "/a.mp4" }), null);
  });
  test("so is its refusal as an Ack", async () => {
    answer = () => ({ payload: { $case: "respAck", respAck: { ok: false, errorMsg: "the file is gone", code: "" } } });
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { failed: "the file is gone" });
  });
  test("the bridge's answer for the device is a failure with no words of the device's", async () => {
    answer = () => bridge;
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { failed: "" });
  });
  test("no answer at all is a failure, never a fallback", async () => {
    answer = () => { throw new Error("Connection closed"); };
    assert.deepEqual(await media.askStreamURL({ path: "/a.mp4" }), { failed: "" });
    assert.equal(await media.requestStreamURL({ path: "/a.mp4" }), null);
  });
});
