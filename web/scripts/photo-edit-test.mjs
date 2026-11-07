// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the photo editor's rules (src/components/photoEditMath.ts, the
// crop frame's arithmetic, and src/components/photoEdit.ts, when Edit and
// Revert are offered, the requests, the answers and what a refusal comes
// to). The web app has no
// test runner, so this loads the modules through Vite's SSR loader in Node,
// as photo-copies-test.mjs does:
//
//   node web/scripts/photo-edit-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too (their photo editors): change them together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let g; // photoEditMath.ts
let m; // photoEdit.ts
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-photo-edit-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Two modules, the second importing only the first, uiText.ts and
    // types: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  g = await server.ssrLoadModule("/src/components/photoEditMath.ts");
  m = await server.ssrLoadModule("/src/components/photoEdit.ts");
});
after(() => server?.close());

const close = (a, b, eps = 1e-9) => Math.abs(a - b) <= eps;
const sameRect = (a, b, eps = 1e-9) => {
  assert.ok(close(a.x, b.x, eps) && close(a.y, b.y, eps) && close(a.w, b.w, eps) && close(a.h, b.h, eps),
    `${JSON.stringify(a)} != ${JSON.stringify(b)}`);
};
// A frame's pixel ratio over a picture.
const pxRatio = (r, pic) => (r.w * pic.w) / (r.h * pic.h);
const landscape = { w: 4000, h: 3000 };
const upright = { w: 3000, h: 4000 };
const lim = { minW: 0.05, minH: 0.05 };

describe("turning the frame with the picture", () => {
  test("clockwise: (x, y, w, h) becomes (1-y-h, x, h, w)", () => {
    sameRect(g.turnRect({ x: 0.1, y: 0.2, w: 0.3, h: 0.4 }, 1), { x: 0.4, y: 0.1, w: 0.4, h: 0.3 });
  });
  test("anticlockwise: (x, y, w, h) becomes (y, 1-x-w, h, w)", () => {
    sameRect(g.turnRect({ x: 0.1, y: 0.2, w: 0.3, h: 0.4 }, -1), { x: 0.2, y: 0.6, w: 0.4, h: 0.3 });
  });
  test("one way and back, or four turns, is where it started", () => {
    const r = { x: 0.12, y: 0.34, w: 0.5, h: 0.25 };
    sameRect(g.turnRect(g.turnRect(r, 1), -1), r);
    sameRect(g.turnRect(g.turnRect(r, -1), 1), r);
    let q = r;
    for (let i = 0; i < 4; i++) q = g.turnRect(q, 1);
    sameRect(q, r);
  });
  test("keeps the same part of the photo: its corners turned as pixels are", () => {
    // A W x H photo turned clockwise is H x W, and its pixel (px, py) is
    // then at (H - py, px).
    const W = 400, H = 300;
    const r = { x: 0.1, y: 0.2, w: 0.5, h: 0.3 };
    const corners = [[r.x * W, r.y * H], [(r.x + r.w) * W, (r.y + r.h) * H]].map(([px, py]) => [H - py, px]);
    const xs = corners.map((c) => c[0]), ys = corners.map((c) => c[1]);
    const t = g.turnRect(r, 1);
    sameRect(t, { x: Math.min(...xs) / H, y: Math.min(...ys) / W, w: (Math.max(...xs) - Math.min(...xs)) / H, h: (Math.max(...ys) - Math.min(...ys)) / W });
  });
  test("the photo's size swaps with each odd turn", () => {
    assert.deepEqual(g.turnedSize(landscape, 0), landscape);
    assert.deepEqual(g.turnedSize(landscape, 1), upright);
    assert.deepEqual(g.turnedSize(landscape, 2), landscape);
    assert.deepEqual(g.turnedSize(landscape, 3), upright);
    assert.deepEqual(g.turnedSize(landscape, -1), upright);
  });
  test("a view's turns go round 0 to 3, the frame with them", () => {
    let v = g.cStartView;
    v = g.rotateView(v, -1, landscape);
    assert.equal(v.turns, 3);
    v = g.rotateView(v, 1, landscape);
    v = g.rotateView(v, 1, landscape);
    assert.equal(v.turns, 1);
    const cropped = { ...v, rect: { x: 0.1, y: 0.2, w: 0.3, h: 0.4 } };
    sameRect(g.rotateView(cropped, 1, landscape).rect, g.turnRect(cropped.rect, 1));
  });
});

describe("the shapes", () => {
  test("Free has none; Original is the picture's own; Square is 1", () => {
    assert.equal(g.aspectRatio("free", landscape), null);
    assert.equal(g.aspectRatio("original", landscape), 4 / 3);
    assert.equal(g.aspectRatio("original", upright), 3 / 4);
    assert.equal(g.aspectRatio("square", upright), 1);
  });
  test("4:3 and 16:9 lie as the picture does: an upright one gets 3:4 and 9:16", () => {
    assert.equal(g.aspectRatio("4:3", landscape), 4 / 3);
    assert.equal(g.aspectRatio("4:3", upright), 3 / 4);
    assert.equal(g.aspectRatio("16:9", landscape), 16 / 9);
    assert.equal(g.aspectRatio("16:9", upright), 9 / 16);
    // A square photo counts as lying down.
    assert.equal(g.aspectRatio("16:9", { w: 1000, h: 1000 }), 16 / 9);
  });
  test("in fractions, a frame's ratio is its pixel ratio times the picture's height over its width", () => {
    assert.ok(close(g.frameRatio("square", landscape), 0.75));
    assert.ok(close(g.frameRatio("original", landscape), 1));
    assert.ok(close(g.frameRatio("16:9", landscape), (16 / 9) * 0.75));
    assert.equal(g.frameRatio("free", landscape), null);
  });
  test("chosen, the frame is the largest of that shape, centred where it was", () => {
    // The whole landscape photo, Square: the full height, in the middle.
    const v = g.aspectView(g.cStartView, "square", landscape);
    assert.equal(v.aspect, "square");
    sameRect(v.rect, { x: 0.125, y: 0, w: 0.75, h: 1 });
    // 16:9 over 4:3: the full width.
    const wide = g.aspectView(g.cStartView, "16:9", landscape);
    assert.ok(close(wide.rect.w, 1));
    assert.ok(close(pxRatio(wide.rect, landscape), 16 / 9));
    assert.ok(close(wide.rect.y, (1 - wide.rect.h) / 2));
  });
  test("centred where the frame was, and moved inside the picture", () => {
    const left = { ...g.cStartView, rect: { x: 0, y: 0.2, w: 0.2, h: 0.2 } };
    const v = g.aspectView(left, "square", landscape);
    sameRect(v.rect, { x: 0, y: 0, w: 0.75, h: 1 });
    const mid = { ...g.cStartView, rect: { x: 0.4, y: 0.4, w: 0.2, h: 0.2 } };
    sameRect(g.aspectView(mid, "square", landscape).rect, { x: 0.125, y: 0, w: 0.75, h: 1 });
  });
  test("Free keeps the frame as it is", () => {
    const r = { x: 0.1, y: 0.1, w: 0.5, h: 0.3 };
    sameRect(g.aspectView({ ...g.cStartView, rect: r, aspect: "square" }, "free", landscape).rect, r);
  });
  test("a turn keeps the shape, lying the other way with the picture", () => {
    let v = g.aspectView(g.cStartView, "4:3", upright);
    assert.ok(close(pxRatio(v.rect, upright), 3 / 4));
    v = g.rotateView(v, 1, upright);
    // Turned, the picture lies down and so does the frame: 4:3.
    const pic = g.turnedSize(upright, v.turns);
    assert.ok(close(pxRatio(v.rect, pic), 4 / 3));
    assert.ok(close(pxRatio(v.rect, pic), g.aspectRatio(v.aspect, pic)));
  });
  test("over a square photo, 4:3 lies down whichever way it is turned: fitted again", () => {
    const sq = { w: 2000, h: 2000 };
    const v = g.rotateView(g.aspectView(g.cStartView, "4:3", sq), 1, sq);
    assert.ok(close(pxRatio(v.rect, sq), 4 / 3));
  });
});

describe("dragging the frame", () => {
  test("a move keeps its size and stops at the picture's edges", () => {
    const r = { x: 0.2, y: 0.2, w: 0.5, h: 0.5 };
    sameRect(g.moveRect(r, 0.1, -0.05), { x: 0.3, y: 0.15, w: 0.5, h: 0.5 });
    sameRect(g.moveRect(r, 1, 1), { x: 0.5, y: 0.5, w: 0.5, h: 0.5 });
    sameRect(g.moveRect(r, -1, -1), { x: 0, y: 0, w: 0.5, h: 0.5 });
  });
  test("free, an edge follows the pointer and the one across stays", () => {
    const r = { x: 0.2, y: 0.2, w: 0.6, h: 0.6 };
    sameRect(g.resizeRect(r, "e", 0.1, 0.3, null, lim), { x: 0.2, y: 0.2, w: 0.7, h: 0.6 });
    sameRect(g.resizeRect(r, "w", 0.1, 0, null, lim), { x: 0.3, y: 0.2, w: 0.5, h: 0.6 });
    sameRect(g.resizeRect(r, "n", 0.4, -0.1, null, lim), { x: 0.2, y: 0.1, w: 0.6, h: 0.7 });
    sameRect(g.resizeRect(r, "s", 0, -0.2, null, lim), { x: 0.2, y: 0.2, w: 0.6, h: 0.4 });
  });
  test("free, a corner moves both sides it holds", () => {
    const r = { x: 0.2, y: 0.2, w: 0.6, h: 0.6 };
    sameRect(g.resizeRect(r, "nw", -0.1, 0.1, null, lim), { x: 0.1, y: 0.3, w: 0.7, h: 0.5 });
    sameRect(g.resizeRect(r, "se", -0.2, 0.1, null, lim), { x: 0.2, y: 0.2, w: 0.4, h: 0.7 });
  });
  test("free, never past the picture's edges", () => {
    const r = { x: 0.2, y: 0.2, w: 0.6, h: 0.6 };
    sameRect(g.resizeRect(r, "ne", 0.5, -0.5, null, lim), { x: 0.2, y: 0, w: 0.8, h: 0.8 });
    sameRect(g.resizeRect(r, "sw", -0.5, 0.5, null, lim), { x: 0, y: 0.2, w: 0.8, h: 0.8 });
  });
  test("never smaller than the limits, nor turned inside out", () => {
    const r = { x: 0.2, y: 0.2, w: 0.6, h: 0.6 };
    const tiny = g.resizeRect(r, "e", -0.9, 0, null, lim);
    assert.ok(close(tiny.w, 0.05));
    assert.ok(close(tiny.x, 0.2));
    const up = g.resizeRect(r, "n", 0, 0.9, null, lim);
    assert.ok(close(up.h, 0.05));
    assert.ok(close(up.y + up.h, 0.8));
  });
  test("with a shape, a corner keeps the corner across it and the shape", () => {
    const ratio = g.frameRatio("square", landscape);
    const r = { x: 0.2, y: 0.2, w: 0.3, h: 0.4 }; // square in pixels
    assert.ok(close(pxRatio(r, landscape), 1));
    const big = g.resizeRect(r, "se", 0.1, 0.02, ratio, lim);
    assert.ok(close(pxRatio(big, landscape), 1));
    assert.ok(close(big.x, 0.2) && close(big.y, 0.2));
    // As far as the pointer went along either side: 0.1 across is 0.4/0.3
    // as much down.
    assert.ok(close(big.h, 0.4 + 0.1 / ratio));
    const nw = g.resizeRect(r, "nw", 0.05, 0.05, ratio, lim);
    assert.ok(close(nw.x + nw.w, 0.5) && close(nw.y + nw.h, 0.6));
    assert.ok(close(pxRatio(nw, landscape), 1));
  });
  test("with a shape, a corner stops where either side reaches the edge", () => {
    const ratio = g.frameRatio("square", landscape);
    const r = { x: 0.2, y: 0.2, w: 0.3, h: 0.4 };
    const all = g.resizeRect(r, "se", 2, 2, ratio, lim);
    assert.ok(close(all.y + all.h, 1));
    assert.ok(all.x + all.w <= 1 + 1e-9);
    assert.ok(close(pxRatio(all, landscape), 1));
  });
  test("with a shape, an edge carries the sides across it, centred and inside", () => {
    const ratio = g.frameRatio("square", landscape);
    const r = { x: 0.2, y: 0.3, w: 0.3, h: 0.4 };
    const wider = g.resizeRect(r, "e", 0.15, 0, ratio, lim);
    assert.ok(close(wider.x, 0.2) && close(wider.w, 0.45));
    assert.ok(close(pxRatio(wider, landscape), 1));
    // Its middle stays where it was: 0.5 down.
    assert.ok(close(wider.y + wider.h / 2, 0.5));
    // Grown past the bottom: moved up to stay inside.
    const low = { x: 0.2, y: 0.55, w: 0.3, h: 0.4 };
    const tall = g.resizeRect(low, "w", -0.2, 0, ratio, lim);
    assert.ok(tall.y + tall.h <= 1 + 1e-9);
    assert.ok(close(pxRatio(tall, landscape), 1));
    const down = g.resizeRect(r, "s", 0, 0.2, ratio, lim);
    assert.ok(close(down.y, 0.3) && close(down.h, 0.6));
    assert.ok(close(down.x + down.w / 2, 0.35));
  });
  test("with a shape, never smaller than the limits on either side", () => {
    const ratio = g.frameRatio("16:9", landscape);
    const r = g.aspectView(g.cStartView, "16:9", landscape).rect;
    const small = g.resizeRect(r, "se", -2, -2, ratio, lim);
    assert.ok(small.w >= 0.05 - 1e-9 && small.h >= 0.05 - 1e-9);
    assert.ok(close(pxRatio(small, landscape), 16 / 9));
  });
  test("the limits: a twentieth of the photo's side, or a few dozen pixels on screen", () => {
    assert.deepEqual(g.limitsFor({ w: 2000, h: 1000 }), { minW: 0.05, minH: 0.05 });
    const shown = g.limitsFor({ w: 400, h: 300 });
    assert.ok(close(shown.minW, 40 / 400) && close(shown.minH, 40 / 300));
    assert.deepEqual(g.limitsFor({ w: 0, h: 0 }), { minW: 1, minH: 1 });
  });
});

describe("what there is to save", () => {
  test("nothing as it opened, nor after four turns", () => {
    assert.equal(g.hasChanges(g.cStartView), false);
    let v = g.cStartView;
    for (let i = 0; i < 4; i++) v = g.rotateView(v, 1, landscape);
    assert.equal(g.hasChanges(v), false);
    // Original over the whole photo crops nothing.
    assert.equal(g.hasChanges(g.aspectView(g.cStartView, "original", landscape)), false);
  });
  test("a turn, or a crop", () => {
    assert.equal(g.hasChanges(g.rotateView(g.cStartView, 1, landscape)), true);
    assert.equal(g.hasChanges(g.aspectView(g.cStartView, "square", landscape)), true);
    assert.equal(g.hasChanges({ ...g.cStartView, rect: g.moveRect({ x: 0, y: 0, w: 0.5, h: 1 }, 0.1, 0) }), true);
  });
  test("the crop sent: none for the whole photo, else inside it", () => {
    assert.equal(g.cropOf(g.cWhole), undefined);
    assert.equal(g.cropOf({ x: 1e-9, y: 0, w: 1, h: 1 }), undefined);
    assert.deepEqual(g.cropOf({ x: 0.1, y: 0.2, w: 0.3, h: 0.4 }), { x: 0.1, y: 0.2, width: 0.3, height: 0.4 });
    const over = g.cropOf({ x: 0.5, y: 0.5, w: 0.6, h: 0.6 });
    assert.ok(over.x + over.width <= 1 && over.y + over.height <= 1);
  });
  test("the picture fitted on the stage, turned", () => {
    assert.deepEqual(g.fitPicture(landscape, 0, { w: 800, h: 800 }), { x: 0, y: 100, w: 800, h: 600 });
    assert.deepEqual(g.fitPicture(landscape, 1, { w: 800, h: 800 }), { x: 100, y: 0, w: 600, h: 800 });
    // A thumbnail fills the stage as the full size will.
    assert.deepEqual(g.fitPicture({ w: 400, h: 300 }, 0, { w: 800, h: 800 }), { x: 0, y: 100, w: 800, h: 600 });
  });
});

describe("when Edit and Revert are offered", () => {
  test("the photos the device edits, by type", () => {
    for (const mime of ["image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp", "IMAGE/JPEG", "image/jpeg; charset=binary"]) {
      assert.equal(m.canEdit({ path: "/Photos/a", mime }), true, mime);
    }
  });
  test("a HEIC or HEIF by its name, whatever its type", () => {
    assert.equal(m.canEdit({ path: "/Photos/IMG_1.HEIC", mime: "application/octet-stream" }), true);
    assert.equal(m.canEdit({ path: "/Photos/IMG_1.heif", mime: "" }), true);
    assert.equal(m.canEdit({ path: "/Photos/IMG_1.Heic" }), true);
  });
  test("never a video, a GIF, a camera raw, a TIFF or an SVG", () => {
    for (const [path, mime] of [
      ["/a.mp4", "video/mp4"], ["/a.MOV", "video/quicktime"], ["/clip.heic", "video/quicktime"], ["/a.gif", "image/gif"],
      ["/a.cr2", "image/x-canon-cr2"], ["/a.dng", "image/x-adobe-dng"], ["/a.dng", "image/dng"], ["/a.tif", "image/tiff"],
      ["/a.svg", "image/svg+xml"], ["/a.pdf", "application/pdf"], ["/a", ""],
    ]) {
      assert.equal(m.canEdit({ path, mime }), false, `${path} ${mime}`);
    }
  });
  test("Revert only for an edit: one with the original's hash", () => {
    assert.equal(m.canRevert({ originalHash: "abc" }), true);
    assert.equal(m.canRevert({ originalHash: "" }), false);
    assert.equal(m.canRevert({}), false);
  });
});

describe("what is sent", () => {
  const item = { path: "/Photos/IMG_1.HEIC", hash: "h1" };
  test("EditImage: the path, the hash shown, the turns and the frame", () => {
    const v = { turns: 1, rect: { x: 0.1, y: 0.2, w: 0.3, h: 0.4 }, aspect: "free" };
    assert.deepEqual(m.editPayload(item, v, { asCopy: false, every: false }), {
      $case: "reqEditImage",
      reqEditImage: { path: "/Photos/IMG_1.HEIC", hash: "h1", quarterTurns: 1, crop: { x: 0.1, y: 0.2, width: 0.3, height: 0.4 }, asCopy: false, everyCopy: false },
    });
  });
  test("only turned: no crop", () => {
    const p = m.editPayload(item, { turns: 3, rect: g.cWhole, aspect: "free" }, { asCopy: false, every: false });
    assert.equal(p.reqEditImage.crop, undefined);
    assert.equal(p.reqEditImage.quarterTurns, 3);
  });
  test("every copy only for a Replace; a copy changes no path", () => {
    const v = g.rotateView(g.cStartView, 1, landscape);
    assert.equal(m.editPayload(item, v, { asCopy: false, every: true }).reqEditImage.everyCopy, true);
    const copy = m.editPayload(item, v, { asCopy: true, every: true }).reqEditImage;
    assert.equal(copy.asCopy, true);
    assert.equal(copy.everyCopy, false);
  });
  test("no hash shown: an empty one, which checks nothing", () => {
    assert.equal(m.editPayload({ path: "/a.jpg" }, g.rotateView(g.cStartView, 1, landscape), { asCopy: true, every: false }).reqEditImage.hash, "");
  });
  test("RevertImage and GetEditState", () => {
    assert.deepEqual(m.revertPayload(item, true), { $case: "reqRevertImage", reqRevertImage: { path: "/Photos/IMG_1.HEIC", hash: "h1", everyCopy: true } });
    assert.deepEqual(m.revertPayload({ path: "/a.jpg" }, false), { $case: "reqRevertImage", reqRevertImage: { path: "/a.jpg", hash: "", everyCopy: false } });
    assert.deepEqual(m.editStatePayload("/a.jpg"), { $case: "reqGetEditState", reqGetEditState: { path: "/a.jpg" } });
  });
});

describe("the answers", () => {
  const state = (copies, edited = false) => ({ error: false, errorCode: "", payload: { $case: "respEditState", respEditState: { copies, edited } } });
  test("how many paths hold the photo, at least one", () => {
    assert.equal(m.copiesFrom(state(3)), 3);
    assert.equal(m.copiesFrom(state(1, true)), 1);
    assert.equal(m.copiesFrom(state(0)), 1);
  });
  test("an older device, a refusal or no answer: one, and nothing asked about copies", () => {
    assert.equal(m.copiesFrom({ error: true, errorCode: "unknown_payload", errorMessage: "Update your device", payload: undefined }), 1);
    assert.equal(m.copiesFrom({ error: true, errorCode: "", payload: { $case: "respAck", respAck: { ok: false } } }), 1);
    assert.equal(m.copiesFrom(null), 1);
    assert.deepEqual(m.copiesAsk(1), { kind: "plain" });
    assert.deepEqual(m.copiesAsk(4), { kind: "copies", count: 4 });
  });
  test("the edited file", () => {
    const file = { path: "/Photos/IMG_1.JPG", hash: "h2", mime: "image/jpeg", originalHash: "h1" };
    assert.deepEqual(m.editAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respFile", respFile: file } }), { kind: "file", file });
  });
  test("a refusal in the device's words, its code kept", () => {
    assert.deepEqual(m.editAnswer({ error: true, errorCode: "changed", errorMessage: "This photo changed on the device.", payload: undefined }),
      { kind: "refused", said: "This photo changed on the device.", code: "changed" });
    assert.deepEqual(m.editAnswer({ error: true, errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok: false, errorMsg: "No room", code: "" } } }),
      { kind: "refused", said: "No room", code: "" });
    // None: the caller says it failed.
    assert.deepEqual(m.editAnswer({ error: true, errorCode: "storage_full", errorMessage: "", payload: undefined }), { kind: "refused", said: "", code: "storage_full" });
    assert.deepEqual(m.editAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respFile", respFile: { path: "" } } }), { kind: "refused", said: "", code: "" });
  });
});

describe("what a refusal comes to", () => {
  // A stand-in for the page's t: the key, or the device's words as they are.
  const t = (key) => key;
  test("\"changed\" closes the editor and the viewer, in the device's words", () => {
    const r = m.refusalOf({ said: "This photo changed on the device.", code: "changed" }, false);
    assert.equal(r.closes, true);
    assert.equal(r.text(t), "This photo changed on the device.");
    const back = m.refusalOf({ said: "This photo changed on the device.", code: "changed" }, true);
    assert.equal(back.closes, true);
  });
  test("\"changed\" with no words: that it failed", () => {
    assert.equal(m.refusalOf({ said: "", code: "changed" }, false).text(t), "web.images.edit.failed");
    assert.equal(m.refusalOf({ said: "", code: "changed" }, true).text(t), "web.images.edit.revert_failed");
  });
  test("every other refusal stays in the question, to try again", () => {
    for (const code of ["not_editable", "storage_full", "not_edited", "unknown_payload", "demo", ""]) {
      const r = m.refusalOf({ said: code ? `Refused: ${code}` : "", code }, false);
      assert.equal(r.closes, false, code);
      assert.equal(r.text(t), code ? `Refused: ${code}` : "web.images.edit.failed");
    }
    assert.equal(m.refusalOf({ said: "", code: "not_edited" }, true).text(t), "web.images.edit.revert_failed");
  });
  test("an answer read as a refusal keeps the code that decides it", () => {
    const changed = m.editAnswer({ error: true, errorCode: "changed", errorMessage: "Changed.", payload: undefined });
    assert.equal(m.refusalOf(changed, false).closes, true);
    const full = m.editAnswer({ error: true, errorCode: "storage_full", errorMessage: "No room.", payload: undefined });
    assert.equal(m.refusalOf(full, false).closes, false);
  });
});

describe("what the page shows after", () => {
  const created = new Date("2021-06-01T10:00:00Z");
  const shown = { path: "/Photos/IMG_1.HEIC", hash: "h1", mime: "image/heic", created, copies: 3, rating: { stars: 4, comment: "" }, size: 10, originalHash: "" };
  test("a Replace: the answer's path, hash and original, the rating kept", () => {
    const file = { path: "/Photos/IMG_1.JPG", hash: "h2", mime: "image/jpeg", originalHash: "h1", size: 5 };
    const now = m.shownAfter(shown, { kind: "replace", file, every: false });
    assert.equal(now.path, "/Photos/IMG_1.JPG");
    assert.equal(now.hash, "h2");
    assert.equal(now.mime, "image/jpeg");
    assert.equal(now.originalHash, "h1");
    assert.equal(now.created, created);
    assert.deepEqual(now.rating, shown.rating);
    // Only here: the others still hold the photo as it was.
    assert.equal(now.copies, undefined);
    assert.equal(m.shownAfter(shown, { kind: "replace", file, every: true }).copies, 3);
  });
  test("a copy: its own file, with the photo's date and nothing else of it", () => {
    const file = { path: "/Photos/IMG_1 (edited).jpg", hash: "h3", mime: "image/jpeg", originalHash: "h1" };
    const now = m.shownAfter(shown, { kind: "copy", file, every: false });
    assert.equal(now.path, "/Photos/IMG_1 (edited).jpg");
    assert.equal(now.created, created);
    assert.equal(now.copies, undefined);
    assert.equal(now.rating, undefined);
  });
  test("a revert: the original back, no longer an edit", () => {
    const now = m.shownAfter({ ...shown, originalHash: "h0" }, { kind: "revert", file: { path: "/Photos/IMG_1.HEIC", hash: "h0", mime: "image/heic", originalHash: "" }, every: false });
    assert.equal(now.hash, "h0");
    assert.equal(now.originalHash, "");
  });
  test("other paths left as they were: only a Replace or Revert of a photo held at several, not of all", () => {
    assert.equal(m.leftOthers({ kind: "replace", copies: 3, every: false }), true);
    assert.equal(m.leftOthers({ kind: "revert", copies: 2, every: false }), true);
    assert.equal(m.leftOthers({ kind: "replace", copies: 3, every: true }), false);
    assert.equal(m.leftOthers({ kind: "replace", copies: 1, every: false }), false);
    assert.equal(m.leftOthers({ kind: "copy", copies: 3, every: false }), false);
  });
  test("the copy's name in the note: the path's last part", () => {
    assert.equal(m.leafOf("/Photos/2024/IMG_1 (edited).jpg"), "IMG_1 (edited).jpg");
    assert.equal(m.leafOf("a.jpg"), "a.jpg");
  });
});
