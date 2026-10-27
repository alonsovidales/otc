// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the web app's socket rules (src/net/ws.ts: how long a request
// waits, when a socket is taken for dead) and the password's encryption
// (src/net/pwCrypto.ts: crypto.subtle, and node-forge on a plain-HTTP
// page), through Vite's SSR loader in Node with a fake WebSocket:
//
//   node web/scripts/net-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { constants, generateKeyPairSync, privateDecrypt } from "node:crypto";
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
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en" }, configurable: true, writable: true });

// A socket that opens at once; what it is sent waits in `sent`, and the
// test answers (or doesn't) with reply(). bufferedAmount is the test's.
let socket = null;
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static autoOpen = true;
  readyState = 0;
  bufferedAmount = 0;
  sent = [];
  closed = false;
  constructor(url) {
    this.url = url;
    socket = this;
    if (FakeWebSocket.autoOpen) setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
  }
  send(bytes) { this.sent.push(bytes); }
  close() { this.closed = true; this.readyState = 3; }
}
globalThis.WebSocket = FakeWebSocket;

let server;
let proto, ws, pw;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-net-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  proto = await server.ssrLoadModule("/src/proto/messages.ts");
  ws = await server.ssrLoadModule("/src/net/ws.ts");
  pw = await server.ssrLoadModule("/src/net/pwCrypto.ts");
});
after(() => server?.close());

const reply = (s, id, payload) =>
  s.onmessage?.({ data: proto.RespEnvelope.encode(proto.RespEnvelope.fromPartial({ id, payload })).finish() });
const idOf = (bytes) => proto.ReqEnvelope.decode(bytes).id;
const ask = (client, $case) => {
  const p = client.request((e) => { e.payload = { $case, [$case]: {} }; });
  p.catch(() => {}); // looked at by the test, when it cares
  return p;
};
const settled = async (p) => {
  let state = "pending";
  p.then(() => { state = "resolved"; }, (e) => { state = e instanceof ws.NoAnswer ? "no-answer" : "rejected"; });
  await new Promise((r) => setTimeout(r, 0));
  return state;
};

async function open() {
  const client = new ws.WSClient();
  await client.connect("wss://test.invalid/ws");
  return { client, s: socket };
}

describe("ws.ts: how long a request waits", () => {
  test("the kinds", () => {
    assert.equal(ws.answerTimeoutMs("reqGetStatus"), 60_000);
    assert.equal(ws.answerTimeoutMs("reqReadFile"), 300_000);
    assert.equal(ws.answerTimeoutMs("reqUploadChunk"), 300_000);
    assert.equal(ws.answerTimeoutMs("reqNewSocialPublication"), 0);
  });

  test("an answer in time resolves; one past its time fails with NoAnswer, the socket kept when frames came", async () => {
    const { client, s } = await open();
    const a = ask(client, "reqGetSettings");
    const b = ask(client, "reqListFiles");
    reply(s, idOf(s.sent[0]), { $case: "respSettings", respSettings: {} });
    assert.equal(await settled(a), "resolved");
    client.watchNowForTest(Date.now() + 61_000);
    assert.equal(await settled(b), "no-answer");
    assert.equal(client.connected, true, "a frame came after b went: the device is there");
    assert.equal(s.closed, false);
  });

  test("past its time with no frame at all since it went: the socket is dropped and opened again next time", async () => {
    const { client, s } = await open();
    const a = ask(client, "reqListFiles");
    client.watchNowForTest(Date.now() + 61_000);
    assert.equal(await settled(a), "no-answer");
    assert.equal(client.connected, false);
    assert.equal(s.closed, true);
    await client.connect("wss://test.invalid/ws");
    assert.notEqual(socket, s);
    assert.equal(client.connected, true);
  });

  test("open-ended work never runs out by itself", async () => {
    const { client, s } = await open();
    const a = ask(client, "reqNewSocialPublication");
    client.watchNowForTest(Date.now() + 3_600_000);
    assert.equal(await settled(a), "pending");
    assert.equal(s.closed, false);
  });
});

describe("ws.ts: a dead socket", () => {
  test("small requests unanswered for 20 s with no frame: dropped, they fail at once", async () => {
    const { client, s } = await open();
    const a = ask(client, "reqGetStatus");
    const b = ask(client, "reqGetNotificationCount");
    client.watchNowForTest(Date.now() + 10_000);
    assert.equal(await settled(a), "pending");
    client.watchNowForTest(Date.now() + 21_000);
    assert.equal(await settled(a), "no-answer");
    assert.equal(await settled(b), "no-answer");
    assert.equal(s.closed, true);
    assert.equal(client.connected, false);
  });

  test("never while a chunked read may be arriving: a 4 MB piece on a slow line holds every answer behind it", async () => {
    const { client, s } = await open();
    const read = ask(client, "reqReadFile");
    const status = ask(client, "reqGetStatus");
    // Two minutes of nothing: the piece is still within its five.
    client.watchNowForTest(Date.now() + 120_000);
    assert.equal(await settled(read), "pending");
    assert.equal(await settled(status), "pending", "the status waits behind the piece");
    assert.equal(s.closed, false);
    reply(s, idOf(s.sent[0]), { $case: "respFileChunk", respFileChunk: { data: new Uint8Array(8), size: 8n } });
    reply(s, idOf(s.sent[1]), { $case: "respStatus", respStatus: {} });
    assert.equal(await settled(read), "resolved");
    assert.equal(await settled(status), "resolved");
  });

  test("never while an upload's piece is still leaving this browser", async () => {
    const { client, s } = await open();
    s.bufferedAmount = 4 << 20;
    const chunk = ask(client, "reqUploadChunk");
    const status = ask(client, "reqGetStatus");
    const t0 = Date.now();
    // The bytes go out slowly for ten minutes: nothing runs out.
    for (let m = 1; m <= 10; m++) {
      s.bufferedAmount -= 400_000;
      client.watchNowForTest(t0 + m * 60_000);
    }
    assert.equal(await settled(chunk), "pending");
    assert.equal(await settled(status), "pending");
    assert.equal(s.closed, false);
    // All out; the device's answers come.
    s.bufferedAmount = 0;
    reply(s, idOf(s.sent[0]), { $case: "respUploadProgress", respUploadProgress: { received: 4n << 20n } });
    reply(s, idOf(s.sent[1]), { $case: "respStatus", respStatus: {} });
    assert.equal(await settled(chunk), "resolved");
    assert.equal(await settled(status), "resolved");
  });

  test("a transfer past its own five minutes with nothing since: dropped", async () => {
    const { client, s } = await open();
    const read = ask(client, "reqReadFile");
    client.watchNowForTest(Date.now() + 301_000);
    assert.equal(await settled(read), "no-answer");
    assert.equal(s.closed, true);
  });

  test("a socket that fails before it opens fails its connect, and the next connect opens another", async () => {
    FakeWebSocket.autoOpen = false;
    const client = new ws.WSClient();
    const p = client.connect("wss://test.invalid/ws");
    const first = socket;
    FakeWebSocket.autoOpen = true;
    first.onerror?.(new Error("refused"));
    first.onclose?.();
    await assert.rejects(p);
    assert.equal(client.connected, false);
    await client.connect("wss://test.invalid/ws");
    assert.notEqual(socket, first);
    assert.equal(client.connected, true);
  });
});

describe("pwCrypto: what the device decrypts", () => {
  const { publicKey, privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const der = new Uint8Array(publicKey.export({ type: "spki", format: "der" }));
  const keyResp = () => ({ id: 1, error: false, errorMessage: "", errorCode: "", payload: { $case: "respPubKey", respPubKey: { publicKey: der, isNewDevice: false, isPrimary: true, container: false } } });
  // As rsa.DecryptOAEP(sha256.New(), ...) does on the device.
  const decrypt = (bytes) => privateDecrypt({ key: privateKey, padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: "sha256" }, Buffer.from(bytes)).toString("utf8");

  test("crypto.subtle (a secure page)", async () => {
    const out = await pw.encryptWithPubKey(keyResp(), "correct horse ✓");
    assert.equal(out.length, 256);
    assert.equal(decrypt(out), "correct horse ✓");
  });

  test("node-forge (a plain-HTTP page)", async () => {
    const out = await pw.encryptWithPubKey(keyResp(), "correct horse ✓", true);
    assert.equal(out.length, 256);
    assert.equal(decrypt(out), "correct horse ✓");
  });

  test("not a key: the device's words, or ours", async () => {
    const ack = { id: 1, error: false, errorMessage: "", errorCode: "", payload: { $case: "respAck", respAck: { ok: false, errorMsg: "nope", code: "" } } };
    await assert.rejects(pw.encryptWithPubKey(ack, "x"), /nope/);
  });
});
