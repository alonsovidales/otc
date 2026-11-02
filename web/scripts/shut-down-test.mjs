// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Settings > Shut Down Device on the web (issue #202, release
// 164): what the device's answer to ShutDownDevice says
// (src/components/settings/shutDown.ts) - agreed (the generic Ack), a
// device before it (unknown_payload: "needs an update"), any other refusal
// in the device's own words, ours when it gave none - and the 90 s the app
// then leaves the device alone (src/net/shutDownHold.ts: no new socket, no
// unreachable screen or "Reconnecting…" pill), as the phones do. The
// device's answers go through the generated bindings, as the socket's do.
// Loaded through Vite's SSR loader in Node, as memory-tiers-test.mjs:
//
//   node web/scripts/shut-down-test.mjs        (needs web/node_modules)
//
// The phones follow the same rules: change them together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // the answer's rules
let hold; // the hold
let pb; // the generated bindings
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-shut-down-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/settings/shutDown.ts");
  hold = await server.ssrLoadModule("/src/net/shutDownHold.ts");
  pb = await server.ssrLoadModule("/src/proto/messages.ts");
});
after(() => server?.close());

// An answer as it comes off the socket: encoded by the device, decoded here.
const wire = (resp) => pb.RespEnvelope.decode(pb.RespEnvelope.encode(pb.RespEnvelope.fromPartial(resp)).finish());
const ack = (ok, extra = {}) => wire({ payload: { $case: "respAck", respAck: { ok, ...extra } } });
const refused = (errorCode, errorMessage = "") => wire({ error: true, errorCode, errorMessage });

// A UiText shown with a t that names the key, so ours and the device's
// words can be told apart.
const t = (key) => `<${key}>`;
const shown = (text) => (text === null ? null : text(t));

describe("the device's answer to ShutDownDevice", () => {
  test("the generic Ack: agreed, nothing to show", () => {
    assert.equal(m.shutDownRefusal(ack(true)), null);
  });
  test("a device before it: needs an update", () => {
    assert.equal(shown(m.shutDownRefusal(refused("unknown_payload", "unknown payload"))), "<web.settings.shutdown.needs_update>");
  });
  test("any other refusal: the device's words, in the request's language", () => {
    assert.equal(shown(m.shutDownRefusal(refused("update_running", "se está instalando una actualización; vuelve a intentarlo cuando termine"))),
      "se está instalando una actualización; vuelve a intentarlo cuando termine");
    assert.equal(shown(m.shutDownRefusal(refused("wifi_changing", "the device's Wi-Fi is being changed; try again in a few minutes"))),
      "the device's Wi-Fi is being changed; try again in a few minutes");
    assert.equal(shown(m.shutDownRefusal(refused("", "not available on this instance"))), "not available on this instance");
    // A demo device answers with an Ack that isn't ok.
    assert.equal(shown(m.shutDownRefusal(ack(false, { code: "demo", errorMsg: "This is a demo device: changes are turned off." }))),
      "This is a demo device: changes are turned off.");
  });
  test("a refusal without words: ours", () => {
    assert.equal(shown(m.shutDownRefusal(refused(""))), "<web.settings.shutdown.failed>");
    assert.equal(shown(m.shutDownRefusal(ack(false))), "<web.settings.shutdown.failed>");
    assert.equal(shown(m.shutDownRefusal(wire({}))), "<web.settings.shutdown.failed>");
  });
});

// A clock of our own: the timers the hold sets, run by hand.
function fakeTimers() {
  const pending = new Map();
  let next = 1;
  return {
    schedule: (fn, ms) => { const id = next++; pending.set(id, { fn, ms }); return id; },
    cancel: (id) => { pending.delete(id); },
    pending,
    fire() { const all = [...pending.values()]; pending.clear(); all.forEach(({ fn }) => fn()); },
  };
}

describe("the device left alone after it agreed", () => {
  test("never asked: nothing held", () => {
    hold.endShutDownHold();
    assert.equal(hold.shutDownHeld(), false);
  });
  test("agreed: held for the phones' 90 s, then as usual", () => {
    const clock = fakeTimers();
    const heard = [];
    const off = hold.subscribeShutDownHold(() => heard.push(hold.shutDownHeld()));
    hold.noteShutDown(clock.schedule, clock.cancel);
    assert.equal(hold.cShutDownHoldMs, 90_000);
    assert.equal(hold.shutDownHeld(), true);
    assert.deepEqual([...clock.pending.values()].map((p) => p.ms), [90_000]);
    clock.fire();
    assert.equal(hold.shutDownHeld(), false);
    assert.deepEqual(heard, [true, false]);
    off();
  });
  test("agreed again meanwhile: 90 s from the last answer, one timer", () => {
    const clock = fakeTimers();
    hold.noteShutDown(clock.schedule, clock.cancel);
    hold.noteShutDown(clock.schedule, clock.cancel);
    assert.equal(clock.pending.size, 1);
    assert.equal(hold.shutDownHeld(), true);
    clock.fire();
    assert.equal(hold.shutDownHeld(), false);
  });
  test("ended early: nothing left to fire, said once", () => {
    const clock = fakeTimers();
    const heard = [];
    const off = hold.subscribeShutDownHold(() => heard.push(hold.shutDownHeld()));
    hold.noteShutDown(clock.schedule, clock.cancel);
    hold.endShutDownHold();
    hold.endShutDownHold();
    assert.equal(clock.pending.size, 0);
    assert.equal(hold.shutDownHeld(), false);
    assert.deepEqual(heard, [true, false]);
    off();
  });
});
