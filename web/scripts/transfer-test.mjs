// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the web's chunked uploads (src/net/upload.ts), the SHA-256
// behind their resume key (src/net/sha256.ts) and reading a library file
// in pieces (src/components/mediaDownload.ts' readPieces: a piece with no
// answer asked again at the same offset), against a stand-in device
// that keeps an upload by its key as release 166 does (issue #205) - or,
// as an older one, starts every BeginUpload afresh. The apps' ChunkedUpload
// follows the same rules: a piece that fails on the way is begun again with
// the same key after a pause and goes on from what the device holds; the
// first BeginUpload, storage_full and FinishUpload are never resumed; at
// most cMaxResumes in a row without the device getting further than it
// ever had. Through Vite's SSR loader in Node:
//
//   node web/scripts/transfer-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let up, sha, ws, md;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-transfer-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  sha = await server.ssrLoadModule("/src/net/sha256.ts");
  ws = await server.ssrLoadModule("/src/net/ws.ts");
  up = await server.ssrLoadModule("/src/net/upload.ts");
  md = await server.ssrLoadModule("/src/components/mediaDownload.ts");
});
after(() => server?.close());

const hex = (b) => createHash("sha256").update(b).digest("hex");
const MiB = 1 << 20;

// A device: uploads by id, and by key when `resumes` (release 166). `fail`
// is what goes wrong, in order: { kind, at? (an offset), as: "drop" (no
// answer), "lost" (taken, its answer lost), "bridge", "full", "refuse" }.
// A BeginUpload's failure is a resume's, unless `first`.
class Device {
  constructor({ resumes = true } = {}) {
    this.resumes = resumes;
    this.uploads = new Map();
    this.byKey = new Map();
    this.files = new Map();
    this.next = 1;
    this.log = [];
    this.fail = [];
    this.bytesTaken = 0;
  }
  failing(kind, at, first = false) {
    const i = this.fail.findIndex((f) => f.kind === kind && (f.at === undefined || f.at === at) && !!f.first === first);
    return i < 0 ? null : this.fail.splice(i, 1)[0].as;
  }
  async send(payload) {
    const kind = payload.$case;
    const p = payload[kind];
    this.log.push(kind === "reqUploadChunk" ? `chunk@${Number(p.offset)}` : kind);
    if (kind === "reqBeginUpload") {
      this.begins = (this.begins ?? 0) + 1;
      const as = this.failing(kind, undefined, this.begins === 1);
      if (as === "drop") throw new ws.NoAnswer("no answer");
      if (as === "bridge") return bridge();
      if (as === "refuse") return { id: 0, error: true, errorCode: "", errorMessage: "too many uploads at once" };
      const key = p.sha256 ? `${p.path}|${p.size}|${p.sha256}` : "";
      const kept = this.resumes && key ? this.byKey.get(key) : undefined;
      const u = kept ?? { id: `u${this.next++}`, path: p.path, size: Number(p.size), key: p.sha256, parts: [], received: 0 };
      this.uploads.set(u.id, u);
      if (key) this.byKey.set(key, u);
      // A newer BeginUpload of the key takes the upload over: its id stays.
      return { id: 0, error: false, errorCode: "", errorMessage: "", payload: { $case: "respUploadStarted", respUploadStarted: { uploadId: u.id, received: BigInt(u.received) } } };
    }
    if (kind === "reqUploadChunk") {
      const u = this.uploads.get(p.uploadId);
      const off = Number(p.offset);
      const as = this.failing(kind, off);
      if (as === "drop") throw new ws.NoAnswer("closed");
      if (as === "bridge") return bridge();
      if (as === "full") return { id: 0, error: true, errorCode: "storage_full", errorMessage: "this needs 4 MB, and only 1 MB is left" };
      if (!u || off !== u.received) return { id: 0, error: true, errorCode: "", errorMessage: "unknown or expired upload" };
      u.parts.push(Buffer.from(p.data));
      u.received += p.data.length;
      this.bytesTaken += p.data.length;
      if (as === "lost") throw new ws.NoAnswer("closed");
      return { id: 0, error: false, errorCode: "", errorMessage: "", payload: { $case: "respUploadProgress", respUploadProgress: { received: BigInt(u.received) } } };
    }
    if (kind === "reqFinishUpload") {
      const u = this.uploads.get(p.uploadId);
      const whole = Buffer.concat(u.parts);
      const h = hex(whole);
      if (u.received !== u.size) return { id: 0, error: true, errorCode: "", errorMessage: "upload incomplete" };
      if ((p.sha256 && p.sha256 !== h) || (u.key && u.key !== h)) return { id: 0, error: true, errorCode: "", errorMessage: "bad hash" };
      this.files.set(u.path, whole);
      this.uploads.delete(u.id);
      for (const [k, v] of this.byKey) if (v === u) this.byKey.delete(k);
      return { id: 0, error: false, errorCode: "", errorMessage: "", payload: { $case: "respFile", respFile: { path: u.path, hash: h } } };
    }
    throw new Error(`unexpected ${kind}`);
  }
}
const bridge = () => ({ id: 0, error: true, errorCode: "", errorMessage: "This device isn't reachable right now.", payload: { $case: "respAck", respAck: { ok: false, code: "device_unreachable", errorMsg: "This device isn't reachable right now." } } });

function run(device, bytes, opts = {}) {
  const pauses = [];
  const progress = [];
  const deps = { send: (payload) => device.send(payload), sleep: async (ms) => { pauses.push(ms); } };
  const p = up.uploadWith(deps, "/Uploads/clip.mov", new Blob([bytes]), { onProgress: (n) => progress.push(n), ...opts });
  return { p, pauses, progress };
}

describe("sha256: the same digest as node:crypto, fed in any pieces", () => {
  test("lengths around the block and padding edges", () => {
    for (const n of [0, 1, 55, 56, 63, 64, 65, 119, 120, 127, 128, 1000, 3 * MiB + 7]) {
      const b = randomBytes(n);
      assert.equal(new sha.Sha256().update(new Uint8Array(b)).hex(), hex(b), `${n} bytes`);
      const s = new sha.Sha256();
      for (let at = 0, k = 1; at < n; k = (k * 7 + 3) % 997 + 1) {
        const take = Math.min(k, n - at);
        s.update(new Uint8Array(b.subarray(at, at + take)));
        at += take;
      }
      assert.equal(s.hex(), hex(b), `${n} bytes in pieces`);
    }
  });
  test("a file's key, a piece at a time", async () => {
    const b = randomBytes(9 * MiB + 3);
    const read = [];
    assert.equal(await up.contentKey(new Blob([b]), (n) => read.push(n)), hex(b));
    assert.deepEqual(read, [4 * MiB, 8 * MiB, 9 * MiB + 3]);
  });
});

describe("upload: as before when nothing fails", () => {
  test("one piece goes without a key", async () => {
    const d = new Device();
    const b = randomBytes(MiB);
    const { p } = run(d, b);
    const resp = await p;
    assert.equal(resp.payload.$case, "respFile");
    assert.deepEqual(d.files.get("/Uploads/clip.mov"), b);
    assert.deepEqual(d.log, ["reqBeginUpload", "chunk@0", "reqFinishUpload"]);
  });
  test("more than one piece is hashed, and the key goes with BeginUpload and FinishUpload", async () => {
    const d = new Device();
    const b = randomBytes(10 * MiB + 123);
    const hashed = [];
    const { p, progress } = run(d, b, { onHashing: (n) => hashed.push(n) });
    const resp = await p;
    assert.equal(resp.payload.$case, "respFile");
    assert.equal(resp.payload.respFile.hash, hex(b));
    assert.deepEqual(progress, [4 * MiB, 8 * MiB, 10 * MiB + 123]);
    assert.equal(hashed.at(-1), 10 * MiB + 123);
    assert.equal(d.bytesTaken, b.length, "every byte once");
  });
  test("a device that already holds part of it (an earlier try): goes on from there", async () => {
    const d = new Device();
    const b = randomBytes(10 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "drop" }, { kind: "reqBeginUpload", as: "drop" });
    for (let i = 0; i < up.cMaxResumes; i++) d.fail.push({ kind: "reqBeginUpload", as: "drop" });
    await assert.rejects(run(d, b).p);
    // The page tried again later (Try again): the device still keeps it.
    d.fail = [];
    const second = run(d, b);
    const resp = await second.p;
    assert.equal(resp.payload.$case, "respFile");
    assert.equal(second.progress[0], 4 * MiB, "from what it held");
    assert.equal(d.bytesTaken, b.length);
  });
});

describe("upload: resumed after a piece fails on the way", () => {
  test("the socket dropped: begun again with the same key, on from what the device holds", async () => {
    const d = new Device();
    const b = randomBytes(12 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 8 * MiB, as: "drop" });
    const { p, pauses } = run(d, b);
    const resp = await p;
    assert.equal(resp.payload.$case, "respFile");
    assert.deepEqual(d.files.get("/Uploads/clip.mov"), b);
    assert.deepEqual(pauses, [up.cResumePauses[0]]);
    assert.deepEqual(d.log, ["reqBeginUpload", "chunk@0", "chunk@4194304", "chunk@8388608", "reqBeginUpload", "chunk@8388608", "reqFinishUpload"]);
    assert.equal(d.bytesTaken, b.length, "nothing the device had was sent again");
  });
  test("the piece reached the device and only its answer was lost: not sent again", async () => {
    const d = new Device();
    const b = randomBytes(12 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "lost" });
    const resp = await run(d, b).p;
    assert.equal(resp.payload.$case, "respFile");
    assert.equal(d.bytesTaken, b.length);
    assert.ok(!d.log.slice(d.log.indexOf("reqBeginUpload", 1)).includes("chunk@4194304"));
  });
  test("the bridge answering for the device: resumed too", async () => {
    const d = new Device();
    const b = randomBytes(9 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "bridge" }, { kind: "reqBeginUpload", as: "bridge" });
    const { p, pauses } = run(d, b);
    assert.equal((await p).payload.$case, "respFile");
    assert.deepEqual(pauses, [up.cResumePauses[0], up.cResumePauses[1]], "a BeginUpload that doesn't get through is another try");
  });
  test("a device before release 166 starts again from 0: the upload still finishes", async () => {
    const d = new Device({ resumes: false });
    const b = randomBytes(9 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 8 * MiB, as: "drop" });
    const { p, progress } = run(d, b);
    assert.equal((await p).payload.$case, "respFile");
    assert.ok(progress.includes(0), "told it went back to 0");
    assert.deepEqual(d.files.get("/Uploads/clip.mov"), b);
  });
  test("gives up after cMaxResumes tries in a row that bring nothing, with the last failure", async () => {
    const d = new Device();
    const b = randomBytes(9 * MiB);
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "drop" });
    for (let i = 0; i < up.cMaxResumes; i++) d.fail.push({ kind: "reqBeginUpload", as: "drop" });
    const { p, pauses } = run(d, b);
    await assert.rejects(p, (e) => e instanceof ws.NoAnswer);
    assert.equal(pauses.length, up.cMaxResumes);
    assert.deepEqual(pauses, up.cResumePauses);
  });
  test("a try that gets further starts the count again", async () => {
    const d = new Device();
    const b = randomBytes(20 * MiB);
    // A drop at every piece: each resume brings one more piece.
    for (let off = 4 * MiB; off < 20 * MiB; off += 4 * MiB) d.fail.push({ kind: "reqUploadChunk", at: off, as: "lost" });
    const { p } = run(d, b);
    assert.equal((await p).payload.$case, "respFile");
    assert.deepEqual(d.files.get("/Uploads/clip.mov"), b);
  });
});

describe("upload: never resumed", () => {
  test("the first BeginUpload's failure is the caller's", async () => {
    const d = new Device();
    d.fail.push({ kind: "reqBeginUpload", as: "bridge", first: true });
    const resp = await run(d, randomBytes(9 * MiB)).p;
    assert.equal(resp.payload.$case, "respAck");
    assert.deepEqual(d.log, ["reqBeginUpload"]);
  });
  test("storage_full: the device's answer at once", async () => {
    const d = new Device();
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "full" });
    const { p, pauses } = run(d, randomBytes(9 * MiB));
    const resp = await p;
    assert.equal(resp.errorCode, "storage_full");
    assert.deepEqual(pauses, []);
  });
  test("the device turning the resume down: its answer", async () => {
    const d = new Device();
    d.fail.push({ kind: "reqUploadChunk", at: 4 * MiB, as: "drop" }, { kind: "reqBeginUpload", as: "refuse" });
    const resp = await run(d, randomBytes(9 * MiB)).p;
    assert.equal(resp.errorMessage, "too many uploads at once");
  });
  test("a file of one piece has no key to resume with: the failure as before", async () => {
    const d = new Device();
    d.fail.push({ kind: "reqUploadChunk", at: 0, as: "drop" });
    const { p, pauses } = run(d, randomBytes(MiB));
    await assert.rejects(p, (e) => e instanceof ws.NoAnswer);
    assert.deepEqual(pauses, []);
  });
});

// A device answering ReadFile: `file` (its bytes and hash), with `fail` as
// above by offset ("drop", "bridge", "refuse"), and `swap` replacing the
// content once at that offset (a sync client overriding it mid-read).
function reader(file, fail = [], swap = null) {
  const log = [];
  let cur = file;
  return {
    log,
    send: async (payload) => {
      const p = payload.reqReadFile;
      const off = Number(p.offset);
      log.push(off);
      const i = fail.findIndex((f) => f.at === off);
      const as = i < 0 ? null : fail.splice(i, 1)[0].as;
      if (as === "drop") throw new ws.NoAnswer("closed");
      if (as === "bridge") return bridge();
      if (as === "refuse") return { id: 0, error: true, errorCode: "", errorMessage: "permission denied" };
      if (swap && off === swap.at) { cur = swap.to; swap = null; }
      const data = cur.subarray(off, off + p.length);
      return { id: 0, error: false, errorCode: "", errorMessage: "", payload: { $case: "respFileChunk", respFileChunk: { data: new Uint8Array(data), size: BigInt(cur.length), hash: hex(cur), mime: "video/mp4", offset: BigInt(off) } } };
    },
  };
}

describe("readPieces: a library file in 4 MB pieces", () => {
  const sleepRec = () => { const pauses = []; return { pauses, sleep: async (ms) => { pauses.push(ms); } }; };
  test("every piece once, with its type", async () => {
    const b = randomBytes(9 * MiB);
    const dev = reader(b);
    const clock = sleepRec();
    const got = await md.readPieces("/a.mp4", "", undefined, { send: dev.send, sleep: clock.sleep });
    assert.deepEqual(Buffer.concat(got.parts), b);
    assert.equal(got.mime, "video/mp4");
    assert.deepEqual(dev.log, [0, 4 * MiB, 8 * MiB]);
  });
  test("a piece with no answer, or the bridge's, is asked again at the same offset", async () => {
    const b = randomBytes(9 * MiB);
    const dev = reader(b, [{ at: 4 * MiB, as: "drop" }, { at: 4 * MiB, as: "bridge" }]);
    const clock = sleepRec();
    const got = await md.readPieces("/a.mp4", "", undefined, { send: dev.send, sleep: clock.sleep });
    assert.deepEqual(Buffer.concat(got.parts), b);
    assert.deepEqual(dev.log, [0, 4 * MiB, 4 * MiB, 4 * MiB, 8 * MiB], "nothing before it read again");
    assert.deepEqual(clock.pauses, md.cPieceRetryPauses.slice(0, 2));
  });
  test("past its pauses it throws, as a request with no answer does", async () => {
    const b = randomBytes(9 * MiB);
    const fails = Array.from({ length: md.cPieceRetryPauses.length + 1 }, () => ({ at: 4 * MiB, as: "drop" }));
    const dev = reader(b, fails);
    const clock = sleepRec();
    await assert.rejects(md.readPieces("/a.mp4", "", undefined, { send: dev.send, sleep: clock.sleep }), (e) => e instanceof ws.NoAnswer);
    assert.deepEqual(clock.pauses, md.cPieceRetryPauses);
  });
  test("the device turning it down: its words, not asked again", async () => {
    const dev = reader(randomBytes(MiB), [{ at: 0, as: "refuse" }]);
    const clock = sleepRec();
    assert.deepEqual(await md.readPieces("/a.mp4", "", undefined, { send: dev.send, sleep: clock.sleep }), { failed: "permission denied" });
    assert.deepEqual(clock.pauses, []);
  });
  test("a file replaced while read starts over once, for the new content", async () => {
    const b = randomBytes(9 * MiB);
    const c = randomBytes(5 * MiB);
    const dev = reader(b, [], { at: 4 * MiB, to: c });
    const clock = sleepRec();
    const got = await md.readPieces("/a.mp4", "", undefined, { send: dev.send, sleep: clock.sleep });
    assert.deepEqual(Buffer.concat(got.parts), c);
  });
});
