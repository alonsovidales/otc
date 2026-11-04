// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a post whose answer never came (src/components/postConfirm.ts,
// the composer's rules): what publishing came to - posted, refused by the
// device, unknown (it went and no answer came: the socket closed, or the
// bridge answered for a device it lost on the way) or unsent (it never left
// this browser) - and which of the newest posts is the one sent. With them
// the pieces they rest on: NoAnswer.sent (src/net/ws.ts, src/net/useWS.ts:
// a request after a sign-in that failed never goes) and the bridge's answer
// for the device (src/net/deviceStatus.ts), when the unreachable screen
// comes over the app (coverAfterMs), and a load that failed meanwhile
// running again as the device answers again (whenDeviceBack, behind
// src/components/useDeviceBack.ts). Through Vite's SSR loader in Node with
// a fake WebSocket, as net-test.mjs:
//
//   node web/scripts/post-confirm-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const store = new Map();
globalThis.window = {
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  },
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
globalThis.localStorage = globalThis.window.localStorage;
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en", onLine: true }, configurable: true, writable: true });

// A socket that opens at once; what it is sent waits in `sent`.
let socket = null;
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  readyState = 0;
  bufferedAmount = 0;
  sent = [];
  closed = false;
  constructor(url) {
    this.url = url;
    socket = this;
    setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
  }
  send(bytes) { this.sent.push(bytes); }
  close() { this.closed = true; this.readyState = 3; }
}
globalThis.WebSocket = FakeWebSocket;

let server;
let proto, ws, status, useWSMod, pc;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-post-confirm-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  proto = await server.ssrLoadModule("/src/proto/messages.ts");
  ws = await server.ssrLoadModule("/src/net/ws.ts");
  status = await server.ssrLoadModule("/src/net/deviceStatus.ts");
  useWSMod = await server.ssrLoadModule("/src/net/useWS.ts");
  pc = await server.ssrLoadModule("/src/components/postConfirm.ts");
});
after(() => server?.close());

const tick = () => new Promise((r) => setImmediate(r));
async function until(fn, what) {
  for (let i = 0; i < 500; i++) {
    if (fn()) return;
    await tick();
  }
  assert.fail(`timed out waiting for ${what}`);
}
// An answer as it comes off the socket.
const wire = (resp) => proto.RespEnvelope.decode(proto.RespEnvelope.encode(proto.RespEnvelope.fromPartial(resp)).finish());
const answer = (s, partial) => s.onmessage?.({ data: proto.RespEnvelope.encode(proto.RespEnvelope.fromPartial(partial)).finish() });
const decoded = (s, i) => proto.ReqEnvelope.decode(s.sent[i]);
// The bridge's own answer for a device it couldn't reach, or lost.
const bridgeAck = (code, id = 0) => ({ id, payload: { $case: "respAck", respAck: { ok: false, code, errorMsg: "This device isn't reachable right now." } } });

describe("deviceStatus: the bridge's answer, and when its screen comes", () => {
  test("the bridge answering for the device is no answer from it", () => {
    assert.equal(status.isBridgeAnswer(wire(bridgeAck("device_unreachable"))), true);
    assert.equal(status.isBridgeAnswer(wire(bridgeAck("account_disabled"))), true);
    // The device's own refusals, and its own storage code, are its answers.
    assert.equal(status.isBridgeAnswer(wire({ payload: { $case: "respAck", respAck: { ok: false, code: "upload_only" } } })), false);
    assert.equal(status.isBridgeAnswer(wire({ error: true, errorCode: "storage_unavailable" })), false);
    assert.equal(status.isBridgeAnswer(wire({ payload: { $case: "respAck", respAck: { ok: true } } })), false);
    assert.equal(status.isBridgeAnswer(wire({ payload: { $case: "respNewSocial", respNewSocial: { uuid: "u1" } } })), false);
  });

  test("a load that failed runs again each time the device answers again, even before anything listened", () => {
    status.clearDeviceStatus();
    // A load failed while the device was away: its count, noted then.
    status.noteResponse("device_unreachable", "This device isn't reachable right now.", true);
    const since = status.deviceBackCount();
    let runs = 0;
    const off = status.whenDeviceBack(since, () => runs++);
    assert.equal(runs, 0, "not while the device is still away");
    // Every request that fails meanwhile reports the same: no run.
    status.noteResponse("device_unreachable", "This device isn't reachable right now.", true);
    status.noteResponse(undefined, undefined, true);
    assert.equal(runs, 0, "the device's own error while away is no answer again");
    status.noteResponse(undefined, undefined, false);
    assert.equal(runs, 1, "the device answered: once");
    status.noteResponse(undefined, undefined, false);
    assert.equal(runs, 1, "answers after it change nothing");
    // Its screen again, and the device back again: again.
    status.noteResponse("storage_unavailable", "The disks are unplugged.", true);
    status.noteResponse(undefined, undefined, true);
    assert.equal(runs, 2, "a device that could reach its storage again answers with anything");
    off();
    status.noteResponse("device_unreachable", "", true);
    status.clearDeviceStatus();
    assert.equal(runs, 2, "stopped");
    // The device came back between the failure and the listening.
    status.noteResponse("device_unreachable", "", true);
    const before = status.deviceBackCount();
    status.clearDeviceStatus();
    let late = 0;
    const off2 = status.whenDeviceBack(before, () => late++);
    assert.equal(late, 1, "run at once");
    off2();
    let none = 0;
    status.whenDeviceBack(status.deviceBackCount(), () => none++)();
    assert.equal(none, 0, "nothing to run when it never went away since");
  });

  test("a device the bridge couldn't reach gets a few seconds before its screen covers the app; the rest at once", () => {
    assert.equal(status.coverAfterMs("device_unreachable"), status.cCoverGraceMs);
    assert.ok(status.cCoverGraceMs >= 3_000 && status.cCoverGraceMs <= 10_000);
    assert.ok(status.cCoverLookMs < status.cCoverGraceMs, "the device is asked again before the screen comes");
    assert.equal(status.coverAfterMs("account_disabled"), 0);
    assert.equal(status.coverAfterMs("storage_unavailable"), 0);
  });
});

describe("ws.ts: whether a request with no answer had gone", () => {
  test("no socket: it never went", async () => {
    const client = new ws.WSClient();
    await assert.rejects(client.request((e) => { e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; }),
      (err) => err instanceof ws.NoAnswer && err.sent === false);
  });

  test("the socket closed before its answer: it went", async () => {
    const client = new ws.WSClient();
    await client.connect("wss://test.invalid/ws");
    const s = socket;
    const p = client.request((e) => { e.payload = { $case: "reqNewSocialPublication", reqNewSocialPublication: { text: "hi", paths: [], trims: [] } }; });
    assert.equal(s.sent.length, 1);
    s.onclose?.();
    await assert.rejects(p, (err) => err instanceof ws.NoAnswer && err.sent === true);
  });
});

describe("useWS: a request behind a sign-in that failed never goes", () => {
  test("the replayed password met the bridge's answer: the post isn't sent, and says so", async () => {
    useWSMod.useWS.init("wss://test.invalid/ws", () => {});
    // A password the device accepted once is kept for the reconnect's
    // replay: here its first try got the bridge's answer (kept, not refused).
    const first = useWSMod.useWS.sendAuth("pw");
    await until(() => socket && socket.sent.length > 0, "the first GetPubKey");
    answer(socket, bridgeAck("device_unreachable", decoded(socket, 0).id));
    await assert.rejects(first);
    status.clearDeviceStatus();
    // The bridge hung up after its answer: the next request reconnects and
    // signs in again first.
    socket.onclose?.();
    const post = useWSMod.useWS.request((e) => {
      e.payload = { $case: "reqNewSocialPublication", reqNewSocialPublication: { text: "hello", paths: ["/a.jpg"], trims: [] } };
    });
    await until(() => socket && socket.sent.length > 0 && !socket.closed, "the reconnect's GetPubKey");
    const s = socket;
    const key = decoded(s, 0);
    assert.equal(key.payload?.$case, "reqGetPubKey");
    answer(s, bridgeAck("device_unreachable", key.id));
    await assert.rejects(post, (err) => err instanceof ws.NoAnswer && err.sent === false);
    assert.ok(s.sent.every((b) => proto.ReqEnvelope.decode(b).payload?.$case !== "reqNewSocialPublication"), "the post never went");
    assert.equal(pc.publishOutcome({ error: await post.catch((e) => e) }), "unsent");
    status.clearDeviceStatus();
  });
});

describe("postConfirm: what publishing came to", () => {
  test("the post's uuid: posted", () => {
    assert.equal(pc.publishOutcome({ resp: wire({ payload: { $case: "respNewSocial", respNewSocial: { uuid: "p1" } } }) }), "posted");
  });
  test("the device's own refusal: refused", () => {
    assert.equal(pc.publishOutcome({ resp: wire({ error: true, errorMessage: "Couldn't publish: the video couldn't be cut." }) }), "refused");
    assert.equal(pc.publishOutcome({ resp: wire({ payload: { $case: "respAck", respAck: { ok: false, code: "demo" } } }) }), "refused");
    assert.equal(pc.publishOutcome({ resp: wire({ payload: { $case: "respNewSocial", respNewSocial: { uuid: "" } } }) }), "refused");
  });
  test("the bridge answering for the device, or no answer at all: unknown - it may be posted", () => {
    assert.equal(pc.publishOutcome({ resp: wire(bridgeAck("device_unreachable")) }), "unknown");
    assert.equal(pc.publishOutcome({ error: new ws.NoAnswer("closed") }), "unknown");
    assert.equal(pc.publishOutcome({ error: new ws.NoAnswer("closed", true) }), "unknown");
    assert.equal(pc.publishOutcome({ error: new Error("something else") }), "unknown", "nothing says it didn't go");
  });
  test("never left this browser: unsent", () => {
    assert.equal(pc.publishOutcome({ error: new ws.NoAnswer("not connected", false) }), "unsent");
  });
});

describe("postConfirm: the post sent, among the newest", () => {
  const sentAt = Date.UTC(2026, 10, 4, 10, 0, 0);
  const sent = { text: "Our trip ✈️", files: 2, sentAt };
  const file = { hash: "h", mime: "image/jpeg", path: "", size: 0, size64: 0n, uploadOnly: false, versions: 0, outOfImages: false };
  const pub = (over) => ({ uuid: "x", text: sent.text, files: [file, file], comments: [], likes: 0, liked: false, own: true, dateTime: new Date(sentAt + 90_000), ...over });

  test("one of the owner's own, same caption and as many items, made after it was sent", () => {
    const mine = pub({ uuid: "mine" });
    assert.equal(pc.findSentPost([pub({ uuid: "f", own: false }), mine], sent)?.uuid, "mine");
  });
  test("a friend's post with the same words, another caption or more items is not it", () => {
    assert.equal(pc.findSentPost([pub({ own: false })], sent), null);
    assert.equal(pc.findSentPost([pub({ text: "Our trip" })], sent), null);
    assert.equal(pc.findSentPost([pub({ files: [file, file, file] })], sent), null);
  });
  test("an item whose thumbnail the feed left out still counts it", () => {
    assert.equal(pc.findSentPost([pub({ uuid: "short", files: [file] })], sent)?.uuid, "short");
  });
  test("the device's clock a few minutes behind still counts; an older post of the same words doesn't", () => {
    assert.equal(pc.findSentPost([pub({ uuid: "behind", dateTime: new Date(sentAt - 5 * 60_000) })], sent)?.uuid, "behind");
    assert.equal(pc.findSentPost([pub({ dateTime: new Date(sentAt - pc.cClockSlackMs - 1) })], sent), null);
  });
  test("a post the feed showed as it went is not it, however well it matches", () => {
    // Posted a few minutes ago with the same words (or none), then this one
    // sent, and its answer lost.
    const older = pub({ uuid: "older", files: [file], dateTime: new Date(sentAt - 6 * 60_000) });
    const withBefore = { ...sent, before: ["older", "f1"] };
    assert.equal(pc.findSentPost([older], withBefore), null);
    assert.equal(pc.findSentPost([older], { text: "", files: 3, sentAt, before: ["older"] }), null);
    assert.equal(pc.findSentPost([pub({ ...older, text: "" })], { text: "", files: 3, sentAt, before: ["older"] }), null);
    // The new one, once the device made it, above it.
    const made = pub({ uuid: "new" });
    assert.equal(pc.findSentPost([made, older], withBefore)?.uuid, "new");
    // Nothing known of the feed (it hadn't loaded): as before.
    assert.equal(pc.findSentPost([older], { ...sent, before: [] })?.uuid, "older");
  });
  test("looked for soon, then every half minute", () => {
    assert.ok(pc.nextLookMs(0) <= 5_000);
    assert.ok(pc.nextLookMs(1) >= pc.nextLookMs(0));
    assert.equal(pc.nextLookMs(10), 30_000);
    assert.equal(pc.nextLookMs(1000), 30_000);
  });
});
