// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for where the web app's socket goes (src/net/endpoint.ts), loaded
// through Vite's SSR loader in Node as device-build-test.mjs does:
//
//   node web/scripts/endpoint-test.mjs        (needs web/node_modules)
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
    cacheDir: join(tmpdir(), "otc-endpoint-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/net/endpoint.ts");
});
after(() => server?.close());

const at = (protocol, host) => ({ protocol, host });

describe("a page the device served", () => {
  for (const [protocol, host, endpoint] of [
    ["https:", "pit.off-the.cloud", "wss://pit.off-the.cloud/ws"],
    ["http:", "192.168.1.20:8080", "ws://192.168.1.20:8080/ws"],
    ["http:", "otc.local:8080", "ws://otc.local:8080/ws"],
    // The container image on a Linux desktop, and SSH tunnels.
    ["http:", "localhost:8080", "ws://localhost:8080/ws"],
    ["http:", "localhost", "ws://localhost/ws"],
    ["http:", "localhost:18080", "ws://localhost:18080/ws"],
    // The demo device's tunnel.
    ["http:", "127.0.0.1:18080", "ws://127.0.0.1:18080/ws"],
    ["http:", "127.0.0.1:4173", "ws://127.0.0.1:4173/ws"],
  ]) {
    test(`at ${protocol}//${host} talks to it`, () => {
      assert.deepEqual(m.pageEndpoint(at(protocol, host), false, "pit.off-the.cloud", false), { endpoint, servedByDevice: true });
    });
  }
});

describe("the developer's Vite servers", () => {
  test("npm run dev goes to VITE_DEVICE, wherever it is opened", () => {
    for (const host of ["localhost:5173", "127.0.0.1:5173", "192.168.1.9:5173"]) {
      assert.deepEqual(m.pageEndpoint(at("http:", host), true, "pit.off-the.cloud", false), { endpoint: "wss://pit.off-the.cloud/ws", servedByDevice: false });
    }
  });
  test("vite preview goes to VITE_DEVICE", () => {
    assert.deepEqual(m.pageEndpoint(at("http:", "localhost:4173"), false, "pit.off-the.cloud", false), { endpoint: "wss://pit.off-the.cloud/ws", servedByDevice: false });
  });
  test("without VITE_DEVICE, Cala", () => {
    assert.deepEqual(m.pageEndpoint(at("http:", "localhost:4173"), false, undefined, false), { endpoint: "wss://cala.off-the.cloud/ws", servedByDevice: false });
    assert.deepEqual(m.pageEndpoint(at("http:", "localhost:5173"), true, "", false), { endpoint: "wss://cala.off-the.cloud/ws", servedByDevice: false });
  });
});

describe("inside the phone apps", () => {
  test("the page is not the device's own", () => {
    assert.equal(m.pageEndpoint(at("https:", "pit.off-the.cloud"), false, undefined, true).servedByDevice, false);
  });
});
