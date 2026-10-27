// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a reloaded tab signing in again with its stored token
// (src/net/sessionRestore.ts): only the device's refusal lands signed out;
// no answer keeps trying, backing off from 1 s to 30 s, at once when the
// browser is back online or the device answers again, each time with the
// token stored then. Loaded through Vite's SSR loader in Node, as the
// other scripts' tests are:
//
//   node web/scripts/session-restore-test.mjs        (needs web/node_modules)
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
    cacheDir: join(tmpdir(), "otc-session-restore-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/net/sessionRestore.ts");
});
after(() => server?.close());

const tick = () => new Promise((r) => setImmediate(r));
async function ticks(n = 5) { for (let i = 0; i < n; i++) await tick(); }

class NoAnswer extends Error {}

// A restore with hand-run timers and scripted answers: each redeem takes
// the next answer ("ok", "refused", "none" - no answer - or a function).
function harness({ answers = [], token = () => "t1" } = {}) {
  const h = { timers: [], redeemed: [], waked: null, unwaked: 0, noAnswers: [] };
  const deps = {
    token,
    redeem: async (tk) => {
      h.redeemed.push(tk);
      const a = answers.shift() ?? "none";
      if (typeof a === "function") return a();
      if (a === "ok") return true;
      if (a === "refused") return false;
      throw new NoAnswer("no answer");
    },
    wake: (now) => { h.waked = now; return () => { h.unwaked++; }; },
    onNoAnswer: (n, err) => h.noAnswers.push([n, err instanceof NoAnswer]),
    setTimer: (fn, ms) => { const t = { fn, ms, cleared: false }; h.timers.push(t); return t; },
    clearTimer: (t) => { t.cleared = true; },
  };
  h.run = m.restoreSession(deps);
  // Runs the wait that is on (the last timer set).
  h.elapse = async () => {
    const t = h.timers.at(-1);
    assert.ok(t && !t.cleared, "a wait is on");
    t.fn();
    await ticks();
  };
  return h;
}

describe("the device's answer", () => {
  test("signed in at once", async () => {
    const h = harness({ answers: ["ok"] });
    assert.equal(await h.run.done, "signed_in");
    assert.equal(h.timers.length, 0);
    assert.equal(h.unwaked, 1);
  });

  test("a refusal lands signed out, without trying again", async () => {
    const h = harness({ answers: ["refused"] });
    assert.equal(await h.run.done, "refused");
    assert.deepEqual(h.redeemed, ["t1"]);
  });

  test("nothing stored", async () => {
    const h = harness({ token: () => null });
    assert.equal(await h.run.done, "no_token");
    assert.deepEqual(h.redeemed, []);
  });
});

describe("no answer", () => {
  test("keeps trying, 1 s then twice as long each time, until the device answers", async () => {
    const h = harness({ answers: ["none", "none", "none", "ok"] });
    await ticks();
    assert.deepEqual(h.timers.map((t) => t.ms), [1000]);
    await h.elapse();
    assert.deepEqual(h.timers.map((t) => t.ms), [1000, 2000]);
    await h.elapse();
    assert.deepEqual(h.timers.map((t) => t.ms), [1000, 2000, 4000]);
    await h.elapse();
    assert.equal(await h.run.done, "signed_in");
    assert.equal(h.redeemed.length, 4);
    assert.deepEqual(h.noAnswers, [[1, true], [2, true], [3, true]]);
  });

  test("the wait stops growing at 30 s", async () => {
    const h = harness({ answers: Array(9).fill("none") });
    await ticks();
    for (let i = 0; i < 8; i++) await h.elapse();
    assert.deepEqual(h.timers.map((t) => t.ms), [1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000, 30000]);
    h.run.stop();
    assert.equal(await h.run.done, "stopped");
  });

  test("back online, or the device back: tried again at once", async () => {
    const h = harness({ answers: ["none", "none", "ok"] });
    await ticks();
    assert.equal(h.redeemed.length, 1);
    h.waked();
    await ticks();
    assert.equal(h.timers[0].cleared, true);
    assert.equal(h.redeemed.length, 2);
    h.waked();
    assert.equal(await h.run.done, "signed_in");
    assert.equal(h.redeemed.length, 3);
  });

  test("a wake while a try is on its way changes nothing", async () => {
    let answer;
    const h = harness({ answers: [() => new Promise((r) => { answer = r; })] });
    await ticks();
    h.waked();
    h.waked();
    answer(true);
    assert.equal(await h.run.done, "signed_in");
    assert.equal(h.redeemed.length, 1);
  });

  test("each try takes the token stored then", async () => {
    const stored = ["t1", "t2"];
    const h = harness({ answers: ["none", "ok"], token: () => stored[0] });
    await ticks();
    stored.shift(); // another tab redeemed t1 and stored t2
    await h.elapse();
    assert.equal(await h.run.done, "signed_in");
    assert.deepEqual(h.redeemed, ["t1", "t2"]);
  });

  test("storage emptied meanwhile (signed out in another tab)", async () => {
    let stored = "t1";
    const h = harness({ answers: ["none"], token: () => stored });
    await ticks();
    stored = null;
    await h.elapse();
    assert.equal(await h.run.done, "no_token");
  });
});

describe("stopping", () => {
  test("during a wait", async () => {
    const h = harness({ answers: ["none"] });
    await ticks();
    h.run.stop();
    assert.equal(await h.run.done, "stopped");
    assert.equal(h.timers[0].cleared, true);
    assert.equal(h.unwaked, 1);
  });

  test("while a try is on its way that then gets no answer", async () => {
    let fail;
    const h = harness({ answers: [() => new Promise((_, rej) => { fail = rej; })] });
    await ticks();
    h.run.stop();
    fail(new NoAnswer("closed"));
    assert.equal(await h.run.done, "stopped");
    assert.equal(h.timers.length, 0);
  });
});
