// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the demo device's flag (src/net/demo.ts), loaded through Vite's
// SSR loader in Node as device-build-test.mjs does:
//
//   node web/scripts/demo-test.mjs        (needs web/node_modules)
import { after, before, beforeEach, describe, test } from "node:test";
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
    cacheDir: join(tmpdir(), "otc-demo-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/net/demo.ts");
});
after(() => server?.close());
beforeEach(() => m.forgetDemo());

const pubKey = (demo, demoPassword = "") => ({ id: 1, payload: { $case: "respPubKey", respPubKey: { demo, demoPassword } } });
const status = (demo) => ({ id: 2, payload: { $case: "respStatus", respStatus: { demo } } });

describe("a demo device", () => {
  test("is told by its PubKey, with the password", () => {
    m.noteDemoReply(pubKey(true, "try-it"));
    assert.equal(m.isDemo(), true);
    assert.equal(m.demoPassword(), "try-it");
  });
  test("is told by its Status, keeping the password", () => {
    m.noteDemoReply(pubKey(true, "try-it"));
    m.noteDemoReply(status(true));
    assert.equal(m.isDemo(), true);
    assert.equal(m.demoPassword(), "try-it");
  });
  test("stays one for the page's life", () => {
    m.noteDemoReply(status(true));
    m.noteDemoReply(status(false));
    m.noteDemoReply(pubKey(false));
    assert.equal(m.isDemo(), true);
  });
  test("tells its listeners once", () => {
    let calls = 0;
    const off = m.subscribeDemo(() => calls++);
    m.noteDemoReply(pubKey(true, "try-it"));
    m.noteDemoReply(pubKey(true, "try-it"));
    m.noteDemoReply(status(true));
    off();
    assert.equal(calls, 1);
  });
});

describe("any other device", () => {
  test("is not one", () => {
    m.noteDemoReply(pubKey(false));
    m.noteDemoReply(status(false));
    m.noteDemoReply({ id: 3, error: true, errorMessage: "x" });
    assert.equal(m.isDemo(), false);
    assert.equal(m.demoPassword(), "");
  });
});
