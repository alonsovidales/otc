// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for On this day's thumbnails (src/components/memories.ts): an
// answer on its way when the memories are asked again (a hide or its Undo,
// the 2-minute re-ask while the device measures) is kept - it was dropped,
// and the strip's and the viewer's effects, keyed on the same paths, never
// asked again, so covers stayed blank - while one from before another
// sign-in (another library) is still dropped. Loaded through Vite's SSR
// loader in Node, the socket's request replaced by the test's, as
// smart-search-test.mjs:
//
//   node web/scripts/memories-thumbs-test.mjs        (needs web/node_modules)
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
let ws, mem;
// Every request the module makes waits here until the test answers it.
const waiting = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-memories-thumbs-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  ws = await server.ssrLoadModule("/src/net/useWS.ts");
  ws.useWS.request = (build) => {
    const env = {};
    build(env);
    return new Promise((resolve) => waiting.push({ kind: env.payload?.$case, payload: env.payload, resolve }));
  };
  mem = await server.ssrLoadModule("/src/components/memories.ts");
});
after(() => server?.close());

const tick = () => new Promise((r) => setTimeout(r, 0));
const take = async (kind) => {
  for (let i = 0; i < 100; i++) {
    const at = waiting.findIndex((w) => w.kind === kind);
    if (at >= 0) return waiting.splice(at, 1)[0];
    await tick();
  }
  assert.fail(`no ${kind} asked`);
};
const thumbsAnswer = (paths) => ({
  payload: { $case: "respListOfFiles", respListOfFiles: { files: paths.map((path) => ({ path, content: new Uint8Array([0xff, 0xd8, 0xff, 0xd9]) })), askAgainFrom: 0 } },
});
// Every memories question answered "off": nothing more is asked.
const answerMemories = async () => {
  for (let i = 0; i < 5; i++) {
    await tick();
    const at = waiting.findIndex((w) => w.kind === "reqGetMemories");
    if (at < 0) continue;
    waiting.splice(at, 1)[0].resolve({ error: true, errorCode: "memories_off", errorMessage: "" });
  }
};

describe("On this day's thumbnails", () => {
  test("an answer on its way while the memories are asked again is kept", async () => {
    const done = mem.fetchThumbs(["/2023/a.jpg"], true);
    const q = await take("reqGetThumbnails");
    void mem.refreshMemories();
    q.resolve(thumbsAnswer(["/2023/a.jpg"]));
    await done;
    await answerMemories();
    assert.match(mem.thumbFor("/2023/a.jpg", true) ?? "", /^blob:/);
  });

  test("one from before another sign-in is dropped, and asked again after it", async () => {
    const done = mem.fetchThumbs(["/2023/b.jpg"], true);
    const q = await take("reqGetThumbnails");
    const stop = mem.watchMemories();
    q.resolve(thumbsAnswer(["/2023/b.jpg"]));
    await done;
    await answerMemories();
    assert.equal(mem.thumbFor("/2023/b.jpg", true), undefined);
    // Wanted again (the strip's effect for the new sign-in): it is asked.
    const again = mem.fetchThumbs(["/2023/b.jpg"], true);
    const q2 = await take("reqGetThumbnails");
    q2.resolve(thumbsAnswer(["/2023/b.jpg"]));
    await again;
    assert.match(mem.thumbFor("/2023/b.jpg", true) ?? "", /^blob:/);
    stop();
  });
});
