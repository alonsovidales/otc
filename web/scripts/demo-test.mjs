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

describe("a change on a demo device", () => {
  test("is refused with the notice, before anything else", () => {
    let notices = 0;
    const off = m.subscribeDemoRefusals(() => notices++);
    assert.equal(m.refuseOnDemo(), false);
    m.noteDemoReply(pubKey(true, "try-it"));
    assert.equal(m.refuseOnDemo(), true);
    assert.equal(m.refuseOnDemo(), true);
    off();
    assert.equal(notices, 2);
  });
  test("is answered by ws.ts as the device would, unsent", () => {
    const req = (kind) => ({ id: 9, payload: { $case: kind } });
    assert.equal(m.demoRequestReply(req("reqDelFile"), "refused"), undefined);
    m.noteDemoReply(status(true));
    const del = m.demoRequestReply(req("reqDelFile"), "refused");
    assert.equal(del.id, 9);
    assert.equal(del.error, true);
    assert.equal(del.errorCode, "demo");
    assert.equal(del.errorMessage, "refused");
    assert.equal(del.payload.respAck.code, "demo");
    // People's Not this person / Someone else… changes the device too.
    assert.equal(m.demoRequestReply(req("reqRemoveFromPerson"), "refused").errorCode, "demo");
    // The viewer's heart changes Favourites; asking what it shows doesn't.
    assert.equal(m.demoRequestReply(req("reqSetFavourite"), "refused").errorCode, "demo");
    assert.equal(m.demoRequestReply(req("reqGetFavourites"), "refused"), undefined);
    const like = m.demoRequestReply(req("reqLikePublication"), "refused");
    assert.equal(like.error, false);
    assert.equal(like.payload.respAck.ok, true);
    assert.equal(m.demoRequestReply(req("reqListFiles"), "refused"), undefined);
    assert.equal(m.demoRequestReply(req("reqSetLanguage"), "refused"), undefined);
  });
});

