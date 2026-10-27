// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a share link's archive address (src/components/downloadLink.ts),
// loaded through Vite's SSR loader in Node as endpoint-test.mjs does:
//
//   node web/scripts/download-link-test.mjs        (needs web/node_modules)
import { after, before, test } from "node:test";
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
    cacheDir: join(tmpdir(), "otc-download-link-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/downloadLink.ts");
});
after(() => server?.close());

const q = (s) => new URLSearchParams(s);

test("the device's links carry the archive in the fragment", () => {
  assert.equal(m.downloadLinkOf("#download=4f1c_ab12", q("")), "4f1c_ab12");
});

test("links from before, in the query, still work", () => {
  assert.equal(m.downloadLinkOf("", q("?download=4f1c_ab12")), "4f1c_ab12");
});

test("the fragment wins over the query", () => {
  assert.equal(m.downloadLinkOf("#download=new_1", q("?download=old_2")), "new_1");
});

test("any other page is no download", () => {
  for (const [hash, search] of [["", ""], ["#", ""], ["#other=1", ""], ["#download=", ""], ["#download=a&b=c", ""]]) {
    assert.equal(m.downloadLinkOf(hash, q(search)), null, `${hash}${search}`);
  }
});
