// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the video cut's rules (src/components/videoEdit.ts: when Edit
// is offered for a video, what the cut screen lets Save, the EditVideo
// request, and which words Revert to original says for a video). The
// questions, answers and refusals around it are the photo edit's, tested by
// photo-edit-test.mjs. The web app has no test runner, so this loads the
// modules through Vite's SSR loader in Node, as photo-edit-test.mjs does:
//
//   node web/scripts/video-edit-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too (their video cuts): change them together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let v; // videoEdit.ts
let p; // photoEdit.ts
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-video-edit-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Two modules of types and plain functions: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  v = await server.ssrLoadModule("/src/components/videoEdit.ts");
  p = await server.ssrLoadModule("/src/components/photoEdit.ts");
});
after(() => server?.close());

const close = (a, b, eps = 1e-9) => Math.abs(a - b) <= eps;

describe("when Edit is offered for a video", () => {
  test("the videos the device cuts, by type: parameters and case don't matter", () => {
    for (const mime of [
      "video/mp4", "video/quicktime", "video/x-m4v", "video/3gpp", "video/3gpp2", "video/x-matroska", "video/webm", "video/x-msvideo",
      "VIDEO/MP4", "video/quicktime; codecs=hvc1", " video/webm ",
    ]) {
      assert.equal(v.canCut({ path: "/Videos/a", mime }), true, mime);
    }
  });
  test("typed as nothing in particular, by the name's extension, any case", () => {
    for (const path of ["/a.mp4", "/a.MOV", "/a.m4v", "/a.3gp", "/a.3G2", "/a.mkv", "/a.webm", "/a.Avi", "/x/IMG_0001.MP4"]) {
      assert.equal(v.canCut({ path, mime: "" }), true, path);
      assert.equal(v.canCut({ path, mime: "application/octet-stream" }), true, path);
      assert.equal(v.canCut({ path }), true, path);
    }
  });
  test("never a photo, whatever its name", () => {
    for (const [path, mime] of [
      ["/a.jpg", "image/jpeg"], ["/a.heic", "image/heic"], ["/a.png", "image/png"], ["/a.mp4", "image/jpeg"], ["/a.gif", "image/gif"],
      ["/a.jpg", ""], ["/a.heic", "application/octet-stream"],
    ]) {
      assert.equal(v.canCut({ path, mime }), false, `${path} ${mime}`);
    }
  });
  test("nor a video in a format the device can't cut", () => {
    for (const [path, mime] of [
      ["/a.ts", "video/mp2t"], ["/a.wmv", "video/x-ms-wmv"], ["/a.flv", "video/x-flv"], ["/a.mpg", "video/mpeg"], ["/a.ogv", "video/ogg"],
      // The type says what it is: an MP4's name doesn't make an MPEG-TS cuttable.
      ["/a.mp4", "video/mp2t"],
      // Typed as nothing, a name the device has no container for.
      ["/a.ts", ""], ["/a.wmv", "application/octet-stream"], ["/a", ""],
    ]) {
      assert.equal(v.canCut({ path, mime }), false, `${path} ${mime}`);
    }
  });
  test("a photo's Edit and a video's never both: the photo editor takes no video", () => {
    for (const [path, mime] of [["/a.mp4", "video/mp4"], ["/a.mov", "video/quicktime"], ["/a.mp4", ""], ["/a.jpg", "image/jpeg"]]) {
      assert.ok(!(v.canCut({ path, mime }) && p.canEdit({ path, mime })), `${path} ${mime}`);
    }
  });
});

describe("the cut the handles make", () => {
  test("untouched, the whole video: from 0 to the end", () => {
    const c = v.cutOf({ start: 0, end: 12.34 }, 12.34);
    assert.deepEqual(c, { startSecs: 0, endSecs: 0, kept: 12.34, whole: true });
  });
  test("an end at the video's end is sent as 0, \"to the end\"", () => {
    const c = v.cutOf({ start: 3, end: 20 }, 20);
    assert.equal(c.startSecs, 3);
    assert.equal(c.endSecs, 0);
    assert.ok(close(c.kept, 17));
    assert.equal(c.whole, false);
  });
  test("the slider's last step short of a length that isn't whole tenths is still the end", () => {
    // A 10.04 s video: the end handle at the far right reads 10.0.
    assert.equal(v.cutOf({ start: 0, end: 10 }, 10.04).endSecs, 0);
    assert.equal(v.cutOf({ start: 0, end: 10 }, 10.04).whole, true);
    assert.equal(v.cutOf({ start: 0, end: 10 }, 10.09).endSecs, 0);
    // A step before that is a cut.
    assert.equal(v.cutOf({ start: 0, end: 9.9 }, 10.04).endSecs, 9.9);
    assert.equal(v.cutOf({ start: 0, end: 9.9 }, 10).endSecs, 9.9);
    // An end of 0 is the trimmer's "to the end" too.
    assert.equal(v.cutOf({ start: 2, end: 0 }, 10).endSecs, 0);
  });
  test("an end moved in is sent as it is, the start too", () => {
    const c = v.cutOf({ start: 1.5, end: 8.2 }, 30);
    assert.deepEqual(c, { startSecs: 1.5, endSecs: 8.2, kept: c.kept, whole: false });
    assert.ok(close(c.kept, 6.7));
  });
  test("a start at the slider's start is 0", () => {
    assert.equal(v.cutOf({ start: 0.04, end: 5 }, 10).startSecs, 0);
    assert.equal(v.cutOf({ start: 0.1, end: 10 }, 10).startSecs, 0.1);
  });
});

describe("Save on the cut screen", () => {
  const save = (start, end, duration) => v.canSaveCut(v.cutOf({ start, end }, duration));
  test("off for the whole video", () => {
    assert.equal(save(0, 30, 30), false);
    assert.equal(save(0, 29.95, 30), false);
  });
  test("on for a real cut at either end, or both", () => {
    assert.equal(save(2, 30, 30), true);
    assert.equal(save(0, 25, 30), true);
    assert.equal(save(4.2, 9.7, 30), true);
  });
  test("a cut keeps at least half a second", () => {
    assert.equal(save(5, 5.5, 30), true);
    assert.equal(save(9.54, 10.04, 10.04), true);
    assert.equal(save(5, 5.4, 30), false);
    assert.equal(save(29.7, 30, 30), false);
    assert.equal(v.cMinCutSecs, 0.5);
    assert.equal(v.cCutStep, 0.1);
  });
  test("nothing to save in a video shorter than half a second", () => {
    assert.equal(save(0, 0.4, 0.4), false);
    assert.equal(save(0.1, 0.4, 0.4), false);
  });
});

describe("what is sent", () => {
  const item = { path: "/Videos/IMG_1.MOV", hash: "h1" };
  test("EditVideo: the path, the hash shown, the start and the end", () => {
    const c = v.cutOf({ start: 1.5, end: 8.2 }, 30);
    assert.deepEqual(v.cutPayload(item, c, { asCopy: false, every: false }), {
      $case: "reqEditVideo",
      reqEditVideo: { path: "/Videos/IMG_1.MOV", hash: "h1", startSecs: 1.5, endSecs: 8.2, asCopy: false, everyCopy: false },
    });
  });
  test("to the end: 0", () => {
    const c = v.cutOf({ start: 4, end: 30 }, 30);
    const sent = v.cutPayload(item, c, { asCopy: false, every: false }).reqEditVideo;
    assert.equal(sent.startSecs, 4);
    assert.equal(sent.endSecs, 0);
  });
  test("every copy only for a Replace; a copy changes no path", () => {
    const c = v.cutOf({ start: 4, end: 30 }, 30);
    assert.equal(v.cutPayload(item, c, { asCopy: false, every: true }).reqEditVideo.everyCopy, true);
    const copy = v.cutPayload(item, c, { asCopy: true, every: true }).reqEditVideo;
    assert.equal(copy.asCopy, true);
    assert.equal(copy.everyCopy, false);
    assert.equal(v.cutPayload(item, c, { asCopy: true, every: false }).reqEditVideo.asCopy, true);
  });
  test("no hash shown: an empty one, which checks nothing", () => {
    const c = v.cutOf({ start: 4, end: 30 }, 30);
    assert.equal(v.cutPayload({ path: "/a.mp4" }, c, { asCopy: false, every: false }).reqEditVideo.hash, "");
  });
});

describe("Revert to original's words", () => {
  test("a video's cut undone", () => {
    for (const f of [{ path: "/a.mp4", mime: "video/mp4" }, { path: "/a.MOV", mime: "video/quicktime" }, { path: "/a.mp4", mime: "application/octet-stream" }, { path: "/a.mkv" }]) {
      assert.equal(v.revertTextKey(f), "web.images.edit.video_revert_text", `${f.path} ${f.mime}`);
    }
  });
  test("a photo's edit removed", () => {
    for (const f of [{ path: "/a.jpg", mime: "image/jpeg" }, { path: "/IMG_1.HEIC", mime: "application/octet-stream" }, { path: "/a.png" }]) {
      assert.equal(v.revertTextKey(f), "web.images.edit.revert_text", `${f.path} ${f.mime}`);
    }
  });
});
