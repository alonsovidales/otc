// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the viewer's session cache of full-size photos
// (src/components/fullSizeCache.ts). The web app has no test runner, so
// this loads the module through Vite's SSR loader in Node, as
// photo-copies-test.mjs does:
//
//   node web/scripts/full-size-cache-test.mjs     (needs web/node_modules)
//
// The phones' FullPhotoCache keeps the same rules: change them together.
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
    cacheDir: join(tmpdir(), "otc-full-size-cache-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/fullSizeCache.ts");
});
after(() => server?.close());

const photo = (bytes) => new Blob([new Uint8Array(bytes)], { type: "image/jpeg" });

describe("the full-size cache", () => {
  test("a tenth of the apps' default 1 GB thumbnail cache", () => {
    assert.equal(m.cFullSizeBudget, 100_000_000);
  });

  test("keeps a photo by hash and gives it back", () => {
    const c = m.createFullSizeCache(100);
    const a = photo(10);
    c.put("ha", a);
    assert.equal(c.get("ha"), a);
    assert.equal(c.get("hb"), null);
    assert.equal(c.used, 10);
  });

  test("lets the least recently used go when over the budget", () => {
    const c = m.createFullSizeCache(100);
    c.put("a", photo(25));
    c.put("b", photo(25));
    c.put("c", photo(25));
    c.put("d", photo(25));
    c.put("e", photo(25));
    assert.equal(c.get("a"), null);
    for (const h of ["b", "c", "d", "e"]) assert.ok(c.get(h), h);
    assert.equal(c.used, 100);
  });

  test("a photo looked at again is the last to go", () => {
    const c = m.createFullSizeCache(100);
    c.put("a", photo(25));
    c.put("b", photo(25));
    c.put("c", photo(25));
    c.put("d", photo(25));
    assert.ok(c.get("a"));
    c.put("e", photo(25));
    assert.ok(c.get("a"));
    assert.equal(c.get("b"), null);
  });

  test("a photo above a quarter of the budget isn't kept, nor pushes others out", () => {
    const c = m.createFullSizeCache(100);
    c.put("a", photo(20));
    c.put("big", photo(26));
    assert.equal(c.get("big"), null);
    assert.ok(c.get("a"));
    assert.equal(c.used, 20);
  });

  test("no hash: not kept, nothing found", () => {
    const c = m.createFullSizeCache(100);
    c.put("", photo(10));
    c.put(undefined, photo(10));
    assert.equal(c.used, 0);
    assert.equal(c.get(""), null);
    assert.equal(c.get(undefined), null);
  });

  test("the same hash again replaces it rather than counting it twice", () => {
    const c = m.createFullSizeCache(100);
    c.put("a", photo(10));
    const again = photo(12);
    c.put("a", again);
    assert.equal(c.get("a"), again);
    assert.equal(c.used, 12);
  });

  test("clear lets every photo go", () => {
    const c = m.createFullSizeCache(100);
    c.put("a", photo(10));
    c.put("b", photo(10));
    c.clear();
    assert.equal(c.used, 0);
    assert.equal(c.get("a"), null);
  });
});
