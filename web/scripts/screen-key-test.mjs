// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the screen's key on the web (src/net/useWS.ts, pwCrypto.ts): on
// the device's own screen (screen mode) a sign-in with the password asks the
// device for a screen key (session/screenkey.go), which then signs the
// screen in after a reload or a reconnect - the device restarting for an
// update included - with nothing used up or rotated, until the device
// refuses it. A device that gives none leaves it on session tokens. Through
// Vite's SSR loader in Node with a fake WebSocket, as net-test.mjs:
//
//   node web/scripts/screen-key-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const local = new Map();
const session = new Map();
const storage = (m) => ({
  getItem: (k) => (m.has(k) ? m.get(k) : null),
  setItem: (k, v) => m.set(k, String(v)),
  removeItem: (k) => m.delete(k),
});
// The kiosk's page: http://127.0.0.1:8080/?screen=1.
globalThis.window = {
  localStorage: storage(local),
  sessionStorage: storage(session),
  location: { search: "?screen=1" },
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
globalThis.localStorage = globalThis.window.localStorage;
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en", onLine: true }, configurable: true, writable: true });

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
let proto, useWSMod, pw, screen;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-screen-key-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  proto = await server.ssrLoadModule("/src/proto/messages.ts");
  useWSMod = await server.ssrLoadModule("/src/net/useWS.ts");
  pw = await server.ssrLoadModule("/src/net/pwCrypto.ts");
  screen = await server.ssrLoadModule("/src/components/screenMode.ts");
  useWSMod.useWS.init("ws://127.0.0.1:8080/ws", () => {});
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
const answer = (partial) =>
  socket.onmessage?.({ data: proto.RespEnvelope.encode(proto.RespEnvelope.fromPartial(partial)).finish() });
async function sentAt(i) {
  await until(() => socket && socket.sent.length > i, `request ${i}`);
  return proto.ReqEnvelope.decode(socket.sent[i]);
}
const spki = generateKeyPairSync("rsa", { modulusLength: 2048 }).publicKey.export({ type: "spki", format: "der" });
const ok = (id) => ({ id, payload: { $case: "respAck", respAck: { ok: true } } });
const refused = (id) => ({ id, payload: { $case: "respAck", respAck: { ok: false, errorMsg: "Session expired" } } });
const token = (id, value, expiresAtUnixMs) =>
  ({ id, payload: { $case: "respSessionToken", respSessionToken: { sessionToken: { token: value, expiresAtUnixMs } } } });

// Signs in with the password up to the question for a key, whose request
// is returned.
async function signInUpToTheKey(password) {
  const n = socket ? socket.sent.length : 0;
  const done = useWSMod.useWS.sendAuth(password);
  const key = await sentAt(n);
  assert.equal(key.payload?.$case, "reqGetPubKey");
  answer({ id: key.id, payload: { $case: "respPubKey", respPubKey: { publicKey: new Uint8Array(spki) } } });
  const auth = await sentAt(n + 1);
  assert.equal(auth.payload?.$case, "reqAuth");
  answer(ok(auth.id));
  const ask = await sentAt(n + 2);
  return { ask, done, n };
}

function forget() {
  pw.clearScreenKey();
  pw.clearPersistedToken();
}

describe("the screen's key", () => {
  test("this page is in screen mode", () => {
    assert.equal(screen.screenMode(), true);
  });

  test("a sign-in with the password asks for a key and keeps it, no session token beside it", async () => {
    forget();
    const { ask, done } = await signInUpToTheKey("pw-1");
    assert.equal(ask.payload?.$case, "reqIssueSessionToken");
    assert.equal(ask.payload.reqIssueSessionToken.screen, true);
    answer(token(ask.id, "screen-key", 0n));
    assert.equal(await done, true);
    assert.equal(pw.loadScreenKey(), "screen-key");
    assert.equal(pw.loadPersistedToken(), null);
    assert.equal(useWSMod.loadStoredSignIn(), "screen-key");
  });

  test("a reload or a reconnect signs in with it, again and again: nothing used up, nothing asked after", async () => {
    for (let i = 0; i < 2; i++) {
      const n = socket.sent.length;
      const p = useWSMod.useWS.authWithToken(useWSMod.loadStoredSignIn());
      const req = await sentAt(n);
      assert.equal(req.payload?.$case, "reqAuthWithToken");
      assert.deepEqual({ ...req.payload.reqAuthWithToken }, { token: "screen-key", screen: true });
      answer(ok(req.id));
      assert.equal(await p, true);
      for (let j = 0; j < 20; j++) await tick();
      assert.equal(socket.sent.length, n + 1, "no new key or token asked for");
      assert.equal(pw.loadScreenKey(), "screen-key");
    }
  });

  test("refused (signed out, the password changed, the device switched off): gone, and at once", async () => {
    const n = socket.sent.length;
    const started = Date.now();
    const p = useWSMod.useWS.authWithToken("screen-key");
    const req = await sentAt(n);
    answer(refused(req.id));
    assert.equal(await p, false);
    assert.ok(Date.now() - started < 1500, "no wait for another tab's successor");
    assert.equal(pw.loadScreenKey(), null);
    assert.equal(useWSMod.loadStoredSignIn(), null);
  });

  test("a device that can't read its storage is no verdict: the key stays", async () => {
    pw.saveScreenKey("screen-key");
    const n = socket.sent.length;
    const p = useWSMod.useWS.authWithToken("screen-key");
    const req = await sentAt(n);
    answer({ id: req.id, error: true, errorCode: "storage_unavailable", errorMessage: "No disks (test)." });
    await assert.rejects(p, useWSMod.StorageUnavailable);
    assert.equal(pw.loadScreenKey(), "screen-key");
  });

  test("a password change drops it, and the refresh after it asks for a new one", async () => {
    useWSMod.useWS.passwordChanged("pw-2");
    assert.equal(pw.loadScreenKey(), null);
    const n = socket.sent.length;
    const p = useWSMod.useWS.refreshSessionToken();
    const ask = await sentAt(n);
    assert.equal(ask.payload.reqIssueSessionToken.screen, true);
    answer(token(ask.id, "screen-key-2", 0n));
    await p;
    assert.equal(pw.loadScreenKey(), "screen-key-2");
  });

  test("a device that gives none (an error) leaves the screen on session tokens", async () => {
    forget();
    const { ask, done, n } = await signInUpToTheKey("pw-3");
    assert.equal(ask.payload.reqIssueSessionToken.screen, true);
    answer({ id: ask.id, error: true, errorMessage: "Could not issue a session token" });
    const plain = await sentAt(n + 3);
    assert.equal(plain.payload?.$case, "reqIssueSessionToken");
    assert.equal(plain.payload.reqIssueSessionToken.screen, false);
    answer(token(plain.id, "session-token", BigInt(Date.now() + 3_600_000)));
    assert.equal(await done, true);
    assert.equal(pw.loadScreenKey(), null);
    assert.equal(pw.loadPersistedToken(), "session-token");
    assert.equal(useWSMod.loadStoredSignIn(), "session-token");
  });

  test("a device from before screen keys answers a session token: kept as one", async () => {
    forget();
    const { ask, done } = await signInUpToTheKey("pw-4");
    answer(token(ask.id, "old-device-token", BigInt(Date.now() + 3_600_000)));
    assert.equal(await done, true);
    assert.equal(pw.loadScreenKey(), null);
    assert.equal(pw.loadPersistedToken(), "old-device-token");
  });
});
