// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for when Alerts marks everything read (src/components/alertsRead.ts):
// the device only marks all of them, so the page first lists as many as
// are unread - past the first 50, and those that came in while the list
// was on its way - and marks them read only once they are on screen.
// Through Vite's SSR loader in Node:
//
//   node web/scripts/alerts-read-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-alerts-read-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/alertsRead.ts");
});
after(() => server?.close());

describe("alertsLimit: every unread alert on screen before they are marked read", () => {
  test("every unread one is in the list: nothing more to ask", () => {
    assert.equal(m.alertsLimit(50, 3, 3), null);
    assert.equal(m.alertsLimit(12, 12, 12), null);
    assert.equal(m.alertsLimit(50, 0, 0), null);
    // Fewer unread than listed as unread: one was marked read elsewhere.
    assert.equal(m.alertsLimit(50, 5, 2), null);
  });
  test("unread ones past the first 50: as many more listed", () => {
    assert.equal(m.alertsLimit(50, 50, 120), 120);
    assert.equal(m.alertsLimit(50, 40, 45), 55);
  });
  test("one that came in while the list was on its way: listed again with it", () => {
    assert.equal(m.alertsLimit(50, 2, 3), 51);
    assert.equal(m.alertsLimit(7, 7, 8), 8);
  });
  test("never past the most it lists", () => {
    assert.equal(m.alertsLimit(50, 50, 10_000), m.cAlertsMost);
    assert.equal(m.alertsLimit(m.cAlertsMost, 400, 900), null, "no more can be shown");
    assert.equal(m.alertsLimit(50, 10, 60, 55), 55);
  });
  test("the first look is the newest 50", () => {
    assert.equal(m.cAlertsFirst, 50);
  });
});
