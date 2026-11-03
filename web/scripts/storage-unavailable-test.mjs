// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for issue #207 on the web: a device that can't reach its storage
// answers every request, GetPubKey first, with error_code
// storage_unavailable and its own words. The app holds that as a device
// status of its own (src/net/deviceStatus.ts: the screen over the app, asked
// again every 30 s), drops it at the first answer that isn't that code, and
// never takes it for a refused password or token (src/net/useWS.ts): both
// are kept, and the sign-in goes on once the device answers. Through Vite's
// SSR loader in Node with a fake WebSocket, as net-test.mjs:
//
//   node web/scripts/storage-unavailable-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
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

// A socket that opens at once; what it is sent waits in `sent`, and the
// test answers with reply().
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
let proto, status, useWSMod, pw, restoreMod;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-storage-unavailable-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  proto = await server.ssrLoadModule("/src/proto/messages.ts");
  status = await server.ssrLoadModule("/src/net/deviceStatus.ts");
  useWSMod = await server.ssrLoadModule("/src/net/useWS.ts");
  pw = await server.ssrLoadModule("/src/net/pwCrypto.ts");
  restoreMod = await server.ssrLoadModule("/src/net/sessionRestore.ts");
});
after(() => server?.close());

const cWords = "Your device can't reach its disks (test).";
const tick = () => new Promise((r) => setImmediate(r));
async function until(fn, what) {
  for (let i = 0; i < 500; i++) {
    if (fn()) return;
    await tick();
  }
  assert.fail(`timed out waiting for ${what}`);
}
const decoded = (s, i) => proto.ReqEnvelope.decode(s.sent[i]);
const answer = (s, partial) =>
  s.onmessage?.({ data: proto.RespEnvelope.encode(proto.RespEnvelope.fromPartial(partial)).finish() });
// The device's answer while it can't reach its storage: no payload.
const noStorage = (id) => ({ id, error: true, errorCode: "storage_unavailable", errorMessage: cWords });
// Waits for request number `i` on the socket and returns its envelope.
async function sentAt(i) {
  await until(() => socket && socket.sent.length > i, `request ${i}`);
  return decoded(socket, i);
}
const spki = generateKeyPairSync("rsa", { modulusLength: 2048 }).publicKey.export({ type: "spki", format: "der" });

describe("deviceStatus: the state and its clock", () => {
  test("the device's code puts the app in it, with its words; any other answer drops it", () => {
    status.noteResponse("storage_unavailable", cWords, true);
    assert.deepEqual(status.getDeviceStatus(), { code: "storage_unavailable", message: cWords });
    // A device back but this socket signed out says "not authenticated":
    // not storage, so the screen goes and the sign-in deals with it.
    status.noteResponse("not_authenticated", "not authenticated", true);
    assert.equal(status.getDeviceStatus(), null);
    status.noteResponse("storage_unavailable", "", true);
    status.noteResponse(undefined, "", false);
    assert.equal(status.getDeviceStatus(), null);
  });

  test("the bridge's verdicts keep their rule: only an answer that isn't an error ends them", () => {
    status.noteResponse("device_unreachable", "offline", true);
    status.noteResponse("not_authenticated", "", true);
    assert.equal(status.getDeviceStatus()?.code, "device_unreachable");
    // The device answering storage_unavailable through the bridge again.
    status.noteResponse("storage_unavailable", cWords, true);
    assert.equal(status.getDeviceStatus()?.code, "storage_unavailable");
    status.clearDeviceStatus();
  });

  test("asked again every 30 s while it can't reach its storage", () => {
    assert.equal(status.recheckMs("storage_unavailable"), 30_000);
    assert.equal(status.recheckMs("device_unreachable"), 5_000);
    assert.equal(status.recheckMs("account_disabled"), null);
    assert.equal(status.isDeviceStatusCode("storage_unavailable"), false, "not the bridge's");
  });
});

describe("useWS: no verdict on the password or the token", () => {
  let signedIn = null;
  before(() => {
    useWSMod.useWS.init("wss://test.invalid/ws", (v) => { signedIn = v; });
    pw.savePersistedToken("stored-token", Date.now() + 3_600_000);
  });

  test("GetPubKey answered storage_unavailable: nothing more is sent, the password and token are kept", async () => {
    const p = useWSMod.useWS.sendAuth("pw-1");
    const first = await sentAt(0);
    assert.equal(first.payload?.$case, "reqGetPubKey");
    answer(socket, noStorage(first.id));
    await assert.rejects(p, (err) => err instanceof useWSMod.StorageUnavailable && err.message === cWords);
    assert.equal(socket.sent.length, 1, "no Auth went out");
    assert.deepEqual(status.getDeviceStatus(), { code: "storage_unavailable", message: cWords });
    assert.equal(useWSMod.useWS.signedIn(), true, "the password is kept for the next try");
    assert.equal(pw.loadPersistedToken(), "stored-token");
    assert.notEqual(signedIn, false, "not signed out");
  });

  test("the next ask (the screen's 30 s, a wake) signs in again, and the device back ends the state", async () => {
    const n = socket.sent.length;
    const q = useWSMod.useWS.request((e) => { e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; });
    const key = await sentAt(n);
    assert.equal(key.payload?.$case, "reqGetPubKey", "the kept password goes first");
    answer(socket, { id: key.id, payload: { $case: "respPubKey", respPubKey: { publicKey: new Uint8Array(spki) } } });
    const auth = await sentAt(n + 1);
    assert.equal(auth.payload?.$case, "reqAuth");
    assert.equal(status.getDeviceStatus(), null, "the device answered: the screen goes");
    answer(socket, { id: auth.id, payload: { $case: "respAck", respAck: { ok: true } } });
    const tok = await sentAt(n + 2);
    assert.equal(tok.payload?.$case, "reqIssueSessionToken");
    answer(socket, {
      id: tok.id,
      payload: { $case: "respSessionToken", respSessionToken: { sessionToken: { token: "next-token", expiresAtUnixMs: BigInt(Date.now() + 3_600_000) } } },
    });
    const st = await sentAt(n + 3);
    assert.equal(st.payload?.$case, "reqGetStatus");
    answer(socket, { id: st.id, payload: { $case: "respStatus", respStatus: {} } });
    await q;
    assert.equal(signedIn, true);
    assert.equal(pw.loadPersistedToken(), "next-token");
  });

  test("Auth answered storage_unavailable (the disks went between the key and the password): kept", async () => {
    const n = socket.sent.length;
    const p = useWSMod.useWS.sendAuth("pw-2");
    const key = await sentAt(n);
    answer(socket, { id: key.id, payload: { $case: "respPubKey", respPubKey: { publicKey: new Uint8Array(spki) } } });
    const auth = await sentAt(n + 1);
    answer(socket, noStorage(auth.id));
    await assert.rejects(p, useWSMod.StorageUnavailable);
    assert.equal(status.getDeviceStatus()?.code, "storage_unavailable");
    assert.equal(pw.loadPersistedToken(), "next-token", "the stored token stays");
    assert.equal(useWSMod.useWS.signedIn(), true);
  });

  test("a stored token answered storage_unavailable is kept, not cleared as refused", async () => {
    const n = socket.sent.length;
    const p = useWSMod.useWS.authWithToken("next-token");
    const req = await sentAt(n);
    assert.equal(req.payload?.$case, "reqAuthWithToken");
    answer(socket, noStorage(req.id));
    await assert.rejects(p, useWSMod.StorageUnavailable);
    assert.equal(pw.loadPersistedToken(), "next-token");
  });

  test("an answer with the device's words missing still says why, in the app's words", async () => {
    const n = socket.sent.length;
    const p = useWSMod.useWS.authWithToken("next-token");
    const req = await sentAt(n);
    answer(socket, { id: req.id, error: true, errorCode: "storage_unavailable" });
    await assert.rejects(p, (err) => err instanceof useWSMod.StorageUnavailable && /can't reach its storage disks/.test(err.message));
    status.clearDeviceStatus();
  });
});

describe("sessionRestore: a restore that met storage_unavailable", () => {
  test("waits 30 s, not the doubling 1 s, and a wake still asks at once", async () => {
    const timers = [];
    let wake = null;
    const answers = ["storage", "storage", "ok"];
    const run = restoreMod.restoreSession({
      token: () => "t1",
      redeem: async () => {
        const a = answers.shift();
        if (a === "ok") return true;
        throw new useWSMod.StorageUnavailable(cWords);
      },
      wake: (now) => { wake = now; return () => {}; },
      waitAfter: (err) => (err instanceof useWSMod.StorageUnavailable ? status.cStorageRetryMs : null),
      setTimer: (fn, ms) => { const t = { fn, ms }; timers.push(t); return t; },
      clearTimer: () => {},
    });
    await until(() => timers.length === 1, "the first wait");
    assert.equal(timers[0].ms, 30_000);
    timers[0].fn();
    await until(() => timers.length === 2, "the second wait");
    assert.equal(timers[1].ms, 30_000, "never the doubling clock");
    wake();
    assert.equal(await run.done, "signed_in");
  });
});
