// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the "device was updated" notice's rules
// (src/components/deviceBuild.ts). The web app has no test runner, so this
// loads the module through Vite's SSR loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/device-build-test.mjs        (needs web/node_modules)
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
    cacheDir: join(tmpdir(), "otc-device-build-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/deviceBuild.ts");
});
after(() => server?.close());
beforeEach(() => m.forgetDeviceBuild());

describe("the page's build", () => {
  test("is the first one noted, and says nothing", () => {
    m.noteDeviceBuild("index-AAAA.js");
    assert.equal(m.pageDeviceBuild(), "index-AAAA.js");
    assert.equal(m.deviceUpdatedShown(), false);
  });
  test("an empty or junk build is no build", () => {
    m.noteDeviceBuild("");
    m.noteDeviceBuild(undefined);
    m.noteDeviceBuild(142);
    assert.equal(m.pageDeviceBuild(), "");
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("");
    assert.equal(m.pageDeviceBuild(), "index-AAAA.js");
    assert.equal(m.deviceUpdatedShown(), false);
  });
});

describe("the entry script", () => {
  test("is the hashed module script's file name, as the device's index.html names it", () => {
    const built = '<head><script type="module" crossorigin src="./assets/index-CeiDWNvJ.js"></script><link rel="modulepreload" href="./assets/common-B5R_z4lW.js"></head>';
    assert.equal(m.entryScript(built), "index-CeiDWNvJ.js");
    assert.equal(m.entryScript('<script crossorigin type="module" src="/assets/index-rXzdtMCh.js?v=1"></script>'), "index-rXzdtMCh.js");
  });
  test("a development page or a page without one says nothing", () => {
    assert.equal(m.entryScript('<script type="module" src="/src/main.tsx"></script>'), "");
    assert.equal(m.entryScript("<html><body>Not found</body></html>"), "");
    assert.equal(m.entryScript('<script src="./assets/index-X1.js"></script>'), "", "not a module script");
  });
});

describe("the notice", () => {
  test("not for the same build after a reconnect", () => {
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-AAAA.js");
    assert.equal(m.deviceUpdatedShown(), false);
  });
  test("up for another build, and it tells its listeners", () => {
    let told = 0;
    const off = m.subscribeDeviceUpdated(() => told++);
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-BBBB.js");
    assert.equal(m.deviceUpdatedShown(), true);
    assert.equal(told, 1);
    m.noteDeviceBuild("index-BBBB.js"); // the next reconnect: still up, nothing new
    assert.equal(told, 1);
    off();
    assert.equal(m.pageDeviceBuild(), "index-AAAA.js", "the page's own build doesn't move");
  });
  test("once per change: dismissed, it stays away for that build", () => {
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-BBBB.js");
    m.dismissDeviceUpdated();
    assert.equal(m.deviceUpdatedShown(), false);
    m.noteDeviceBuild("index-BBBB.js");
    assert.equal(m.deviceUpdatedShown(), false);
  });
  test("comes back for yet another build", () => {
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-BBBB.js");
    m.dismissDeviceUpdated();
    m.noteDeviceBuild("index-CCCC.js");
    assert.equal(m.deviceUpdatedShown(), true);
  });
  test("goes when the device is back on the page's build, and a later change counts again", () => {
    m.noteDeviceBuild("index-AAAA.js");
    m.noteDeviceBuild("index-BBBB.js");
    m.noteDeviceBuild("index-AAAA.js");
    assert.equal(m.deviceUpdatedShown(), false);
    m.noteDeviceBuild("index-BBBB.js");
    assert.equal(m.deviceUpdatedShown(), true);
  });
});
