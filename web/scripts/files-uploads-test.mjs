// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Files' upload queue (src/components/filesUploads.ts): one at a
// time in the order they came, a row each by id, going on with nobody
// watching (Files closed while another section shows), Try again for the
// failed ones, the panel's ×, the panel going once all are done, where the
// files landed, and the end of a sign-in forgetting it all. Against a
// stand-in for net/upload.ts whose answers come when the test says.
// Through Vite's SSR loader in Node:
//
//   node web/scripts/files-uploads-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let fu;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-files-uploads-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  fu = await server.ssrLoadModule("/src/components/filesUploads.ts");
  // A failed upload is logged; the tests fail some on purpose.
  console.error = () => {};
});
after(() => server?.close());

const tick = () => new Promise((r) => setImmediate(r));
const t = (key) => key;

// The device: each file sent waits in `sent` until the test answers it.
function stand() {
  const sent = [];
  const send = (path, file, onProgress, onReading) => new Promise((resolve, reject) => {
    sent.push({ path, file, onProgress, onReading, resolve, reject });
  });
  const ok = (path) => ({ payload: { $case: "respFile", respFile: { path } } });
  const refuse = (words) => ({ error: true, errorMessage: words });
  const bridge = () => ({ payload: { $case: "respAck", respAck: { ok: false, code: "device_unreachable", errorMsg: "This device isn't reachable right now." } } });
  // A clock the test moves.
  const timers = [];
  const clock = {
    set: (fn, ms) => { const id = { fn, ms }; timers.push(id); return id; },
    clear: (id) => { const i = timers.indexOf(id); if (i >= 0) timers.splice(i, 1); },
    run: () => { while (timers.length) timers.shift().fn(); },
  };
  return { sent, send, ok, refuse, bridge, clock };
}
const file = (name, size = 10) => ({ name, size });
const item = (name, dir = "/Photos/") => ({ file: file(name), path: dir + name, name });
const statuses = (q) => q.rows().map((r) => `${r.name}:${r.status}`);

describe("filesUploads: one queue, going on with Files closed", () => {
  test("one at a time, in order, with nobody watching the rows", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg"), item("b.jpg"), item("c.jpg")]);
    assert.deepEqual(statuses(q), ["a.jpg:sending", "b.jpg:waiting", "c.jpg:waiting"]);
    assert.equal(d.sent.length, 1, "one at a time");
    // Files was closed here: nothing subscribes, the queue goes on.
    for (const name of ["a.jpg", "b.jpg", "c.jpg"]) {
      const s = d.sent[d.sent.length - 1];
      assert.equal(s.path, `/Photos/${name}`);
      s.resolve(d.ok(s.path));
      await tick();
    }
    assert.equal(d.sent.length, 3, "every file of the batch was sent");
    assert.deepEqual(statuses(q), ["a.jpg:done", "b.jpg:done", "c.jpg:done"]);
  });

  test("Files opened again shows the rows where they are, and hears them change", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg"), item("b.jpg")]);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    let heard = 0;
    const off = q.subscribe(() => heard++);
    assert.deepEqual(statuses(q), ["a.jpg:done", "b.jpg:sending"]);
    d.sent[1].onReading();
    d.sent[1].onProgress(4);
    assert.equal(q.rows()[1].status, "sending");
    assert.equal(q.rows()[1].sent, 4);
    assert.ok(heard >= 2);
    off();
  });

  test("a drop while others are on their way joins the queue; each row is its own", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg")]);
    q.add([item("x.jpg", "/Docs/")]);
    assert.deepEqual(statuses(q), ["a.jpg:sending", "x.jpg:waiting"]);
    const ids = q.rows().map((r) => r.id);
    assert.equal(new Set(ids).size, 2);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    assert.equal(d.sent[1].path, "/Docs/x.jpg");
    d.sent[1].resolve(d.ok("/Docs/x.jpg"));
    await tick();
    assert.deepEqual(statuses(q), ["a.jpg:done", "x.jpg:done"]);
  });

  test("where files landed is heard once the queue is empty", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    const heard = [];
    q.onLanded((paths) => heard.push(paths));
    q.add([item("a.jpg"), item("b.jpg")]);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    assert.deepEqual(heard, [], "not while others wait");
    d.sent[1].resolve(d.refuse("Duplicated file"));
    await tick();
    assert.deepEqual(heard, [["/Photos/a.jpg"]], "only what landed");
  });
});

describe("filesUploads: failures, Try again and the panel", () => {
  test("the device's words; the bridge's answer and no answer are ours", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg"), item("b.jpg"), item("c.jpg")]);
    d.sent[0].resolve(d.refuse("Duplicated file"));
    await tick();
    d.sent[1].resolve(d.bridge());
    await tick();
    d.sent[2].reject(new Error(""));
    await tick();
    const [a, b, c] = q.rows();
    assert.deepEqual([a.status, b.status, c.status], ["failed", "failed", "failed"]);
    assert.equal(a.error(t), "Duplicated file");
    assert.equal(b.error(t), "web.shell.connection.no_answer", "never the bridge's English words");
    assert.equal(c.error(t), "web.shell.connection.no_answer");
  });

  test("Try again sends the failed ones only; the panel stays until then", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg"), item("b.jpg")]);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    d.sent[1].resolve(d.refuse("Disk write failed"));
    await tick();
    d.clock.run();
    assert.deepEqual(statuses(q), ["a.jpg:done", "b.jpg:failed"], "a failure keeps the panel");
    q.retry();
    assert.equal(d.sent.length, 3);
    assert.equal(d.sent[2].path, "/Photos/b.jpg");
    assert.deepEqual(statuses(q), ["a.jpg:done", "b.jpg:sending"]);
    d.sent[2].resolve(d.ok("/Photos/b.jpg"));
    await tick();
    assert.deepEqual(statuses(q), ["a.jpg:done", "b.jpg:done"]);
  });

  test("all done and none failed: the panel goes in a moment, and a new drop starts afresh", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg")]);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    assert.equal(q.rows().length, 1, "shown done for a moment");
    q.add([item("b.jpg")]);
    assert.deepEqual(statuses(q), ["b.jpg:sending"], "a panel all done starts afresh");
    d.sent[1].resolve(d.ok("/Photos/b.jpg"));
    await tick();
    d.clock.run();
    assert.deepEqual(q.rows(), []);
  });

  test("the panel's × leaves only what is still to send", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    q.add([item("a.jpg"), item("b.jpg")]);
    q.add([]);
    d.sent[0].resolve(d.refuse("no"));
    await tick();
    q.close();
    assert.deepEqual(statuses(q), ["b.jpg:sending"]);
    q.retry();
    assert.equal(d.sent.length, 2, "a closed row isn't tried again");
  });
});

describe("filesUploads: the end of a sign-in", () => {
  test("nothing more is sent and every row goes; the next sign-in's queue goes on", async () => {
    const d = stand();
    const q = fu.createUploads(d.send, d.clock);
    const heard = [];
    q.onLanded((paths) => heard.push(paths));
    q.add([item("a.jpg"), item("b.jpg"), item("c.jpg")]);
    q.forget();
    assert.deepEqual(q.rows(), []);
    // Someone else signs in and drops a file while a.jpg's answer is still out.
    q.add([item("mine.jpg", "/Other/")]);
    d.sent[0].resolve(d.ok("/Photos/a.jpg"));
    await tick();
    assert.deepEqual(d.sent.map((s) => s.path), ["/Photos/a.jpg", "/Other/mine.jpg"], "b and c never go");
    assert.deepEqual(statuses(q), ["mine.jpg:sending"], "the answer for the old file changes nothing");
    d.sent[1].resolve(d.ok("/Other/mine.jpg"));
    await tick();
    assert.deepEqual(heard, [["/Other/mine.jpg"]], "the old sign-in's file isn't said to have landed");
  });

  test("watchUploads' stop forgets the page's queue", () => {
    const stop = fu.watchUploads();
    stop();
    assert.deepEqual(fu.filesUploads.rows(), []);
  });
});
