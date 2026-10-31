// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the viewer's heart (src/components/favourites.ts): what it
// shows for a path - nothing until the device said -, one GetFavourites for
// the open photo and its neighbours, a press shown at once and put back
// when refused, one SetFavourite at a time per path, an old answer never
// undoing a press, copies of a photo, stale() and forget(), and a device
// without Favourites. Loaded through Vite's SSR loader in Node, as
// full-size-cache-test.mjs does:
//
//   node web/scripts/favourites-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
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
    cacheDir: join(tmpdir(), "otc-favourites-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Types are its only imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/favourites.ts");
});
after(() => server?.close());

// A device whose answers the test gives: each request waits in `sent`
// until answered (or failed: no answer).
function device() {
  const sent = [];
  const send = (payload) => new Promise((ok, fail) => sent.push({ payload, ok, fail }));
  return { sent, send };
}
const favouritesAre = (...paths) => ({ errorCode: "", errorMessage: "", payload: { $case: "respFavourites", respFavourites: { paths } } });
const acked = { errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok: true, errorMsg: "", code: "" } } };
const refused = (words = "", code = "") => ({ errorCode: code, errorMessage: words, payload: undefined });
// Lets the store's own awaits run.
const settle = () => new Promise((r) => setTimeout(r, 0));
const asked = (r) => r.payload.reqGetFavourites.paths;
const setTo = (r) => [r.payload.reqSetFavourite.paths, r.payload.reqSetFavourite.favourite];

// A store whose answers for `paths` are known: `on` the favourites.
async function knowing(paths, on = []) {
  const d = device();
  const f = m.createFavourites(d.send);
  const done = f.ask(paths.map((path) => ({ path })));
  d.sent.shift().ok(favouritesAre(...on));
  assert.equal(await done, "ok");
  return { d, f };
}

describe("asking", () => {
  test("nothing is known until the device says, then its answer", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    assert.equal(f.get("/a.jpg"), undefined);
    const done = f.ask([{ path: "/b.jpg" }, { path: "/c.jpg" }, { path: "/a.jpg" }]);
    assert.equal(d.sent.length, 1);
    assert.deepEqual(asked(d.sent[0]), ["/b.jpg", "/c.jpg", "/a.jpg"]);
    assert.equal(f.get("/b.jpg"), undefined);
    d.sent[0].ok(favouritesAre("/c.jpg"));
    assert.equal(await done, "ok");
    assert.equal(f.get("/b.jpg"), false);
    assert.equal(f.get("/c.jpg"), true);
    assert.equal(f.get("/a.jpg"), false);
  });

  test("what is known isn't asked again; only the rest, in one request", async () => {
    const { d, f } = await knowing(["/a", "/b"], ["/a"]);
    assert.equal(await f.ask([{ path: "/a" }, { path: "/b" }]), "ok");
    assert.equal(d.sent.length, 0);
    const done = f.ask([{ path: "/b" }, { path: "/c" }, { path: "/a" }, { path: "/c" }]);
    assert.equal(d.sent.length, 1);
    assert.deepEqual(asked(d.sent[0]), ["/c"]);
    d.sent[0].ok(favouritesAre("/c"));
    assert.equal(await done, "ok");
    assert.equal(f.get("/c"), true);
  });

  test("a path asked about already waits for that answer: one request", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    const first = f.ask([{ path: "/a" }, { path: "/b" }]);
    const second = f.ask([{ path: "/b" }]);
    assert.equal(d.sent.length, 1);
    d.sent[0].ok(favouritesAre("/b"));
    assert.equal(await second, "ok");
    assert.equal(await first, "ok");
    assert.equal(f.get("/b"), true);
  });

  test("no answer or an error reply: still unknown, and asked again next time", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    let done = f.ask([{ path: "/a" }]);
    d.sent.shift().fail(new Error("no answer"));
    assert.equal(await done, "failed");
    assert.equal(f.get("/a"), undefined);
    done = f.ask([{ path: "/a" }]);
    d.sent.shift().ok(refused("Something went wrong"));
    assert.equal(await done, "failed");
    assert.equal(f.get("/a"), undefined);
    done = f.ask([{ path: "/a" }]);
    assert.equal(d.sent.length, 1);
    d.sent.shift().ok(favouritesAre());
    assert.equal(await done, "ok");
    assert.equal(f.get("/a"), false);
  });

  test("a device without Favourites: no heart, and nothing asked again", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    const done = f.ask([{ path: "/a" }]);
    d.sent.shift().ok(refused("unknown request", "unknown_payload"));
    assert.equal(await done, "unsupported");
    assert.equal(f.unsupported(), true);
    assert.equal(await f.ask([{ path: "/b" }]), "unsupported");
    assert.equal(d.sent.length, 0);
  });

  test("listeners hear an answer", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    let heard = 0;
    const off = f.subscribe(() => heard++);
    const done = f.ask([{ path: "/a" }]);
    d.sent.shift().ok(favouritesAre("/a"));
    await done;
    off();
    assert.equal(heard, 1);
  });
});

describe("the heart pressed", () => {
  test("shows the change at once, sends it, and keeps it once the device took it", async () => {
    const { d, f } = await knowing(["/a"]);
    const done = f.toggle({ path: "/a" });
    assert.equal(f.get("/a"), true);
    await settle();
    assert.equal(d.sent.length, 1);
    assert.deepEqual(setTo(d.sent[0]), [["/a"], true]);
    d.sent.shift().ok(acked);
    assert.equal(await done, null);
    assert.equal(f.get("/a"), true);
    // And out again.
    const out = f.toggle({ path: "/a" });
    assert.equal(f.get("/a"), false);
    await settle();
    assert.deepEqual(setTo(d.sent[0]), [["/a"], false]);
    d.sent.shift().ok(acked);
    assert.equal(await out, null);
    assert.equal(f.get("/a"), false);
  });

  test("refused: back as it was, with the device's words", async () => {
    const { d, f } = await knowing(["/a"], ["/a"]);
    const done = f.toggle({ path: "/a" });
    assert.equal(f.get("/a"), false);
    await settle();
    d.sent.shift().ok(refused("La base de datos no responde"));
    assert.equal(await done, "La base de datos no responde");
    assert.equal(f.get("/a"), true);
  });

  test("refused in an Ack, or without words, or no answer: back, with no words of the device's", async () => {
    const { d, f } = await knowing(["/a"]);
    let done = f.toggle({ path: "/a" });
    await settle();
    d.sent.shift().ok({ errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok: false, errorMsg: "Ack's words", code: "" } } });
    assert.equal(await done, "Ack's words");
    assert.equal(f.get("/a"), false);
    done = f.toggle({ path: "/a" });
    await settle();
    d.sent.shift().ok(refused());
    assert.equal(await done, "");
    assert.equal(f.get("/a"), false);
    done = f.toggle({ path: "/a" });
    await settle();
    d.sent.shift().fail(new Error("no answer"));
    assert.equal(await done, "");
    assert.equal(f.get("/a"), false);
  });

  test("a press before the answer is known does nothing", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    assert.equal(await f.toggle({ path: "/a" }), null);
    assert.equal(f.get("/a"), undefined);
    assert.equal(d.sent.length, 0);
  });

  test("pressed twice quickly: one request at a time, and nothing more once back where it was", async () => {
    const { d, f } = await knowing(["/a"]);
    const first = f.toggle({ path: "/a" });
    await settle();
    const second = f.toggle({ path: "/a" });
    assert.equal(f.get("/a"), false);
    assert.equal(d.sent.length, 1, "the second waits for the first's answer");
    d.sent.shift().ok(acked);
    await settle();
    // The device holds true; the heart shows false: that goes now.
    assert.equal(d.sent.length, 1);
    assert.deepEqual(setTo(d.sent[0]), [["/a"], false]);
    d.sent.shift().ok(acked);
    assert.equal(await first, null);
    assert.equal(await second, null);
    assert.equal(f.get("/a"), false);

    // Twice before the first answer, and the first refused: the device
    // still holds what the heart shows, so nothing else goes, no note.
    const third = f.toggle({ path: "/a" });
    await settle();
    f.toggle({ path: "/a" });
    d.sent.shift().ok(refused("nope"));
    assert.equal(await third, null);
    assert.equal(d.sent.length, 0);
    assert.equal(f.get("/a"), false);
  });

  test("three presses, the last refused: back to the device's last word", async () => {
    const { d, f } = await knowing(["/a"]);
    const done = f.toggle({ path: "/a" }); // true
    await settle();
    f.toggle({ path: "/a" }); // false
    f.toggle({ path: "/a" }); // true again
    d.sent.shift().ok(acked); // the device holds true: nothing more to send
    assert.equal(await done, null);
    assert.equal(d.sent.length, 0);
    assert.equal(f.get("/a"), true);
    const out = f.toggle({ path: "/a" }); // false
    await settle();
    d.sent.shift().ok(refused());
    assert.equal(await out, "");
    assert.equal(f.get("/a"), true);
  });

  test("an answer asked for before a press doesn't undo it", async () => {
    const { d, f } = await knowing(["/a"]);
    f.stale();
    const asking = f.ask([{ path: "/a" }]);
    assert.equal(d.sent.length, 1);
    const pressed = f.toggle({ path: "/a" });
    assert.equal(f.get("/a"), true);
    // The old question's answer: not a favourite.
    d.sent.shift().ok(favouritesAre());
    assert.equal(await asking, "ok");
    assert.equal(f.get("/a"), true);
    await settle();
    d.sent.shift().ok(acked);
    assert.equal(await pressed, null);
    assert.equal(f.get("/a"), true);
  });

  test("nothing is asked about a path while its change is on its way", async () => {
    const { d, f } = await knowing(["/a"]);
    f.toggle({ path: "/a" });
    f.stale();
    await settle();
    assert.equal(d.sent.length, 1); // the SetFavourite
    assert.equal(await f.ask([{ path: "/a" }]), "ok");
    assert.equal(d.sent.length, 1);
    d.sent.shift().ok(acked);
  });

  test("listeners hear the press and its undoing", async () => {
    const { d, f } = await knowing(["/a"]);
    let heard = 0;
    f.subscribe(() => heard++);
    const done = f.toggle({ path: "/a" });
    assert.equal(heard, 1);
    await settle();
    d.sent.shift().ok(refused());
    await done;
    assert.equal(heard, 2);
  });

  test("a device without Favourites refusing the change: back, and no heart from then on", async () => {
    const { d, f } = await knowing(["/a"]);
    const done = f.toggle({ path: "/a" });
    await settle();
    d.sent.shift().ok(refused("unknown request", "unknown_payload"));
    assert.equal(await done, "unknown request");
    assert.equal(f.get("/a"), false);
    assert.equal(f.unsupported(), true);
  });
});

describe("copies of a photo", () => {
  test("a press shows on every path known to hold the same content", async () => {
    const d = device();
    const f = m.createFavourites(d.send);
    const done = f.ask([{ path: "/Photos/a.jpg", hash: "h1" }, { path: "/Phone/a.jpg", hash: "h1" }, { path: "/b.jpg", hash: "h2" }]);
    d.sent.shift().ok(favouritesAre());
    await done;
    const pressed = f.toggle({ path: "/Photos/a.jpg", hash: "h1" });
    assert.equal(f.get("/Phone/a.jpg"), true);
    assert.equal(f.get("/b.jpg"), false);
    await settle();
    assert.equal(d.sent.length, 1);
    assert.deepEqual(setTo(d.sent[0]), [["/Photos/a.jpg"], true]);
    d.sent.shift().ok(acked);
    await pressed;
    // The other copy is in too, as far as the device is concerned: its
    // own press takes it out.
    const out = f.toggle({ path: "/Phone/a.jpg", hash: "h1" });
    assert.equal(f.get("/Photos/a.jpg"), false);
    await settle();
    assert.deepEqual(setTo(d.sent[0]), [["/Phone/a.jpg"], false]);
    d.sent.shift().ok(refused());
    assert.equal(await out, "");
    assert.equal(f.get("/Photos/a.jpg"), true);
    assert.equal(f.get("/Phone/a.jpg"), true);
  });
});

describe("kept answers", () => {
  test("stale(): still shown, asked again when next wanted, and the answer replaces them", async () => {
    const { d, f } = await knowing(["/a", "/b"], ["/a"]);
    f.stale();
    assert.equal(f.get("/a"), true);
    const done = f.ask([{ path: "/a" }]);
    assert.equal(d.sent.length, 1);
    assert.deepEqual(asked(d.sent[0]), ["/a"]);
    d.sent.shift().ok(favouritesAre());
    await done;
    assert.equal(f.get("/a"), false);
    assert.equal(f.get("/b"), false);
  });

  test("knownOut: emptied, or said to be out, since the last stale()", async () => {
    const { d, f } = await knowing(["/a", "/b", "/c"], ["/a", "/b"]);
    f.stale();
    assert.equal(f.knownOut("/c"), false, "kept from before");
    const pressed = f.toggle({ path: "/a" });
    assert.equal(f.knownOut("/a"), true);
    assert.equal(f.knownOut("/b"), false);
    await settle();
    d.sent.shift().ok(acked);
    await pressed;
    const done = f.ask([{ path: "/c" }]);
    d.sent.shift().ok(favouritesAre());
    await done;
    assert.equal(f.knownOut("/c"), true);
    f.stale();
    assert.equal(f.knownOut("/a"), false);
    assert.equal(f.knownOut("/never-asked"), false);
  });

  test("forget(): nothing known, and an answer on its way is dropped", async () => {
    const { d, f } = await knowing(["/a"], ["/a"]);
    const done = f.ask([{ path: "/b" }]);
    let heard = 0;
    f.subscribe(() => heard++);
    f.forget();
    assert.equal(heard, 1);
    assert.equal(f.get("/a"), undefined);
    d.sent.shift().ok(favouritesAre("/b"));
    assert.equal(await done, "failed", "asked again");
    assert.equal(f.get("/b"), undefined);
    const again = f.ask([{ path: "/b" }]);
    assert.equal(d.sent.length, 1);
    d.sent.shift().ok(favouritesAre("/b"));
    assert.equal(await again, "ok");
    assert.equal(f.get("/b"), true);
  });

  test("forget() while a change is on its way: its answer changes nothing", async () => {
    const { d, f } = await knowing(["/a"]);
    const pressed = f.toggle({ path: "/a" });
    await settle();
    f.forget();
    d.sent.shift().ok(refused("late"));
    assert.equal(await pressed, null);
    assert.equal(f.get("/a"), undefined);
  });
});
