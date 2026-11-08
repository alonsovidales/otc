// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Compress and Settings › Largest files (src/components/compress.ts:
// the Compress dialog's rules, the job's answers, the list's paging and
// states, and the viewer's compressed photos). The web app has no test
// runner, so this loads the module through Vite's SSR loader in Node, as
// photo-edit-test.mjs does:
//
//   node web/scripts/compress-test.mjs        (needs web/node_modules)
//
// The case ids are the phones' too (CompressTests.swift, CompressTest.kt):
// the same rules, tested alike - change the three together.
//
//   C1  the videos' radio: the video presets offered, 1080p, 720p, HEVC in
//       that order, then "Leave as they are"; nothing without a video preset
//   C2  the radio's default: 1080p if any item offers it, else HEVC, else 720p
//   C3  a switch per photo preset offered by any item, in order, each on
//   C4  per item: a video the chosen preset, else HEVC, else nothing ("Leave
//       as they are": nothing); a photo 12 MP, else JPEG resave, else PNG to
//       JPEG, each only with its switch on; a preset the app doesn't know is
//       left out
//   C5  the tasks, in the order asked, with the path and hash the device
//       answered for
//   C6  a video choice's row: everything it would do if chosen (HEVC
//       fallbacks included), count, sizes now and estimates after
//   C7  a photo switch's row: the photos that would get its preset were it
//       on, the other switches as they are
//   C8  the foot's total: each task's size less its estimate, never below 0
//   C9  "videos take a while" only with a video among the tasks
//   C10 the files not offered (no known preset), with the device's reason;
//       "nothing can be made smaller" when that is every file
//   C11 GetCompressOptions' answer: options, an older device, a refusal in
//       the device's words, or nothing usable
//   C12 CompressFiles' answer: the job, an older device, a refusal, nothing
//   C13 what is asked: the photos and videos picked (documents left out),
//       each path once, the first 500 in the order picked - the title counts
//       them; and the requests' payloads
//   J1  GetCompressJob's answer: the job, gone (not_found, another id), or none
//   J2  the job is asked every 2 s while shown, every 10 s while only a page
//       follows it, never once over or unwatched
//   J3  the job's progress: tasks over plus a running video's percent
//   J4  what it came to: compressed, freed, and the files left as they were
//       (failed and skipped; gone, the tasks not over too)
//   J5  each done result is put in place once
//   J6  the file shown after: the device's over the old one, its copies kept
//   L1  ListLargestFiles' payload: pages of 50 after a token
//   L2  its answer: a page and the next token, an older device, a failure
//   L3  a page added under the rows: no path or content shown twice
//   L4  what the screen shows: loading, an older device, failed, empty, rows
//       with a spinner, a failure or nothing at their foot
//   L5  the next page is asked only while there is one and none is on its way
//   L6  "in N places" only for content at several paths
//   L7  photos and videos by their type, or a HEIC/HEIF by its name
//   L8  Compress is on while a photo or video is picked
//   L9  the list is read again after a job that compressed anything
//   L10 a row's folder
//   R1  no Revert to original for a compressed file (original_removed)
//   R2  Info's note for a compressed file
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // compress.ts
let e; // photoEdit.ts
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-compress-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // compress.ts imports photoEdit.ts, which imports only photoEditMath.ts,
    // uiText.ts and types: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/compress.ts");
  e = await server.ssrLoadModule("/src/components/photoEdit.ts");
});
after(() => server?.close());

const GB = 1 << 30;
const MB = 1 << 20;
// Whole bytes, as the device sends them.
const gb = (x) => Math.round(x * GB);
// An option as the device answers it (sizes are bigints in the generated code).
const opt = (path, size, choices = [], reason = "", reasonCode = "") => ({
  path, hash: `h:${path}`, size: BigInt(size), copies: 1,
  choices: choices.map(([preset, est]) => ({ preset, estimatedSize: BigInt(est) })), reason, reasonCode,
});
const fourKHevc = opt("/v/4k-hevc.mov", gb(4), [["video_1080p", gb(1.2)], ["video_720p", gb(0.6)]]);
const fourKH264 = opt("/v/4k-h264.mp4", gb(6), [["video_1080p", gb(1.5)], ["video_720p", gb(0.8)], ["video_hevc", gb(3)]]);
const hdH264 = opt("/v/1080p.mp4", gb(2), [["video_720p", gb(0.5)], ["video_hevc", gb(1)]]);
const big = opt("/p/48mp.heic", 12 * MB, [["photo_12mp", 4 * MB]]);
const bigJpeg = opt("/p/48mp.jpg", 20 * MB, [["photo_12mp", 4 * MB], ["jpeg_resave", 10 * MB]]);
const fatJpeg = opt("/p/12mp.jpg", 8 * MB, [["jpeg_resave", 3 * MB]]);
const png = opt("/p/shot.png", 6 * MB, [["png_to_jpeg", 1 * MB]]);
const pdf = opt("/docs/scan.pdf", 30 * MB, [], "only photos and videos can be compressed", "not_media");
const small = opt("/v/720p.mov", 300 * MB, [], "the device can't make this file noticeably smaller", "nothing_to_gain");
const all = [fourKHevc, fourKH264, hdH264, big, bigJpeg, fatJpeg, png, pdf, small];
const pick = (video, off = []) => ({ video, photos: Object.fromEntries(off.map((p) => [p, false])) });

describe("C1 the videos' radio", () => {
  test("the presets offered, in the radio's order, then Leave as they are", () => {
    assert.deepEqual(m.videoChoices(all), ["video_1080p", "video_720p", "video_hevc", "video_none"]);
    assert.deepEqual(m.videoChoices([hdH264]), ["video_720p", "video_hevc", "video_none"]);
    assert.deepEqual(m.videoChoices([fourKHevc]), ["video_1080p", "video_720p", "video_none"]);
  });
  test("no video preset: no radio at all", () => {
    assert.deepEqual(m.videoChoices([big, png, pdf]), []);
    assert.deepEqual(m.videoChoices([]), []);
  });
});

describe("C2 the radio's default", () => {
  test("1080p when any item offers it", () => assert.equal(m.defaultVideo([hdH264, fourKHevc]), "video_1080p"));
  test("else HEVC when any does", () => assert.equal(m.defaultVideo([hdH264]), "video_hevc"));
  test("else 720p", () => assert.equal(m.defaultVideo([opt("/v/x.mov", GB, [["video_720p", gb(0.3)]])]), "video_720p"));
  test("no video: none, and the dialog's pick is Leave as they are", () => {
    assert.equal(m.defaultVideo([big]), null);
    assert.deepEqual(m.defaultPick([big]), { video: "video_none", photos: {} });
  });
});

describe("C3 the photo switches", () => {
  test("one per preset offered by any item, strongest first", () => {
    assert.deepEqual(m.photoSwitches(all), ["photo_12mp", "jpeg_resave", "png_to_jpeg"]);
    assert.deepEqual(m.photoSwitches([png, fatJpeg]), ["jpeg_resave", "png_to_jpeg"]);
    assert.deepEqual(m.photoSwitches([fourKH264, pdf]), []);
  });
  test("each on as the dialog opens", () => {
    const v = m.compressView(all, m.defaultPick(all));
    assert.deepEqual(v.photos.map((p) => p.on), [true, true, true]);
  });
});

describe("C4 what each item gets", () => {
  const p = pick("video_1080p");
  test("a video: the preset chosen when it offers it", () => {
    assert.equal(m.presetFor(fourKHevc, p), "video_1080p");
    assert.equal(m.presetFor(fourKH264, pick("video_720p")), "video_720p");
  });
  test("else HEVC when it offers that", () => assert.equal(m.presetFor(hdH264, p), "video_hevc"));
  test("else nothing", () => assert.equal(m.presetFor(fourKHevc, pick("video_hevc")), null));
  test("Leave as they are: nothing for any video", () => {
    for (const o of [fourKHevc, fourKH264, hdH264]) assert.equal(m.presetFor(o, pick("video_none")), null);
  });
  test("a photo: 12 MP, then the JPEG resave, then PNG to JPEG, each with its switch on", () => {
    assert.equal(m.presetFor(bigJpeg, p), "photo_12mp");
    assert.equal(m.presetFor(bigJpeg, pick("video_1080p", ["photo_12mp"])), "jpeg_resave");
    assert.equal(m.presetFor(bigJpeg, pick("video_1080p", ["photo_12mp", "jpeg_resave"])), null);
    assert.equal(m.presetFor(png, p), "png_to_jpeg");
    assert.equal(m.presetFor(png, pick("video_1080p", ["png_to_jpeg"])), null);
    assert.equal(m.presetFor(big, pick("video_none")), "photo_12mp", "the videos' choice leaves photos alone");
  });
  test("a preset this app doesn't know is left out", () => {
    const newer = opt("/v/new.mov", GB, [["video_av1", gb(0.2)], ["video_hevc", gb(0.5)]]);
    assert.equal(m.presetFor(newer, p), "video_hevc");
    assert.deepEqual(m.videoChoices([newer]), ["video_hevc", "video_none"]);
    const onlyNew = opt("/p/new.avif", 9 * MB, [["photo_avif", 2 * MB]], "");
    assert.equal(m.presetFor(onlyNew, p), null);
    assert.deepEqual(m.notOffered([onlyNew]).map((x) => x.name), ["new.avif"]);
  });
  test("nothing offered: nothing", () => assert.equal(m.presetFor(pdf, p), null));
});

describe("C5 the tasks", () => {
  test("in the order asked, the device's path and hash, with sizes for the sums", () => {
    const tasks = m.tasksOf(all, m.defaultPick(all));
    assert.deepEqual(tasks.map((t) => [t.path, t.preset]), [
      ["/v/4k-hevc.mov", "video_1080p"], ["/v/4k-h264.mp4", "video_1080p"], ["/v/1080p.mp4", "video_hevc"],
      ["/p/48mp.heic", "photo_12mp"], ["/p/48mp.jpg", "photo_12mp"], ["/p/12mp.jpg", "jpeg_resave"], ["/p/shot.png", "png_to_jpeg"],
    ]);
    assert.equal(tasks[0].hash, "h:/v/4k-hevc.mov");
    assert.equal(tasks[0].size, gb(4));
    assert.equal(tasks[0].estimate, gb(1.2));
  });
  test("nothing chosen: no task", () => {
    assert.deepEqual(m.tasksOf(all, pick("video_none", ["photo_12mp", "jpeg_resave", "png_to_jpeg"])), []);
    assert.deepEqual(m.tasksOf([pdf, small], m.defaultPick([pdf, small])), []);
  });
});

describe("C6 a video choice's row", () => {
  const p = pick("video_1080p");
  test("1080p: the two 4K videos at 1080p and the 1080p one's fallback to HEVC", () => {
    assert.deepEqual(m.videoRow(all, p, "video_1080p"), { count: 3, now: gb(12), after: gb(1.2) + gb(1.5) + gb(1) });
  });
  test("720p: every video offers it", () => {
    assert.deepEqual(m.videoRow(all, p, "video_720p"), { count: 3, now: gb(12), after: gb(0.6) + gb(0.8) + gb(0.5) });
  });
  test("HEVC: only those offering it (a 4K HEVC video gets nothing)", () => {
    assert.deepEqual(m.videoRow(all, p, "video_hevc"), { count: 2, now: gb(8), after: gb(4) });
  });
  test("the same whichever is chosen now, and photos never count", () => {
    assert.deepEqual(m.videoRow(all, pick("video_none"), "video_720p"), m.videoRow(all, p, "video_720p"));
  });
  test("no row under Leave as they are", () => {
    const v = m.compressView(all, p);
    assert.deepEqual(v.videos.map((x) => [x.choice, x.sums && x.sums.count]),
      [["video_1080p", 3], ["video_720p", 3], ["video_hevc", 2], ["video_none", null]]);
  });
});

describe("C7 a photo switch's row", () => {
  test("12 MP: the photos over 13 MP", () => {
    assert.deepEqual(m.photoRow(all, pick("video_none"), "photo_12mp"), { count: 2, now: 32 * MB, after: 8 * MB });
  });
  test("the JPEG resave: only the JPEGs 12 MP leaves, while 12 MP is on", () => {
    assert.deepEqual(m.photoRow(all, pick("video_none"), "jpeg_resave"), { count: 1, now: 8 * MB, after: 3 * MB });
  });
  test("with 12 MP off, the JPEG it would have shrunk is saved again instead", () => {
    assert.deepEqual(m.photoRow(all, pick("video_none", ["photo_12mp"]), "jpeg_resave"), { count: 2, now: 28 * MB, after: 13 * MB });
  });
  test("a switch off still shows what turning it on would do", () => {
    const off = pick("video_none", ["png_to_jpeg"]);
    assert.deepEqual(m.photoRow(all, off, "png_to_jpeg"), { count: 1, now: 6 * MB, after: 1 * MB });
    const v = m.compressView(all, off);
    assert.deepEqual(v.photos.map((x) => [x.preset, x.on, x.sums.count]),
      [["photo_12mp", true, 2], ["jpeg_resave", true, 1], ["png_to_jpeg", false, 1]]);
    assert.ok(!v.tasks.some((t) => t.preset === "png_to_jpeg"));
  });
});

describe("C8 the total", () => {
  test("each task's size less its estimate", () => {
    const tasks = m.tasksOf([fourKHevc, png], pick("video_1080p"));
    assert.equal(m.savedOf(tasks), gb(4) - gb(1.2) + 5 * MB);
    assert.equal(m.compressView([fourKHevc, png], pick("video_1080p")).saved, gb(4) - gb(1.2) + 5 * MB);
  });
  test("an estimate above the file's size frees nothing, never less", () => {
    assert.equal(m.savedOf([{ path: "/a", hash: "h", preset: "jpeg_resave", size: 10, estimate: 12 }]), 0);
  });
  test("no task: nothing", () => assert.equal(m.savedOf([]), 0));
});

describe("C9 videos take a while", () => {
  test("with a video among the tasks", () => {
    assert.equal(m.compressView(all, pick("video_1080p")).slow, true);
    assert.equal(m.hasVideoTask(m.tasksOf([hdH264], pick("video_720p"))), true);
  });
  test("not with photos alone, nor with the videos left as they are", () => {
    assert.equal(m.compressView([big, png], m.defaultPick([big, png])).slow, false);
    assert.equal(m.compressView(all, pick("video_none")).slow, false);
  });
});

describe("C10 the files not offered", () => {
  test("each with its name and the device's reason, in order", () => {
    assert.deepEqual(m.notOffered(all), [
      { path: "/docs/scan.pdf", name: "scan.pdf", reason: "only photos and videos can be compressed" },
      { path: "/v/720p.mov", name: "720p.mov", reason: "the device can't make this file noticeably smaller" },
    ]);
  });
  test("a file that changed meanwhile is one, in the device's words", () => {
    const changed = opt("/p/x.jpg", 5 * MB, [], "this file has changed - refresh and try again", "changed");
    assert.deepEqual(m.notOffered([changed]).map((x) => x.reason), ["this file has changed - refresh and try again"]);
  });
  test("nothing at all can be made smaller", () => {
    assert.equal(m.compressView([pdf, small], m.defaultPick([pdf, small])).nothing, true);
    assert.equal(m.compressView(all, m.defaultPick(all)).nothing, false);
    assert.equal(m.compressView([], m.defaultPick([])).nothing, true, "an empty answer: nothing either");
  });
});

describe("C11 GetCompressOptions' answer", () => {
  test("the options", () => {
    const a = m.optionsAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respCompressOptions", respCompressOptions: { items: [pdf] } } });
    assert.equal(a.kind, "options");
    assert.equal(a.items.length, 1);
  });
  test("an older device: it needs an update", () => {
    assert.deepEqual(m.optionsAnswer({ error: true, errorCode: "unknown_payload", errorMessage: "unknown payload" }), { kind: "old" });
  });
  test("a refusal in the device's words", () => {
    assert.deepEqual(m.optionsAnswer({ error: true, errorCode: "", errorMessage: "this file has changed - refresh and try again" }),
      { kind: "refused", said: "this file has changed - refresh and try again" });
  });
  test("nothing usable: no answer, the bridge's (its words gone), an answer of another kind", () => {
    assert.deepEqual(m.optionsAnswer(null), { kind: "failed" });
    assert.deepEqual(m.optionsAnswer({ error: true, errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok: false, errorMsg: "", code: "device_unreachable" } } }), { kind: "failed" });
    assert.deepEqual(m.optionsAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok: true, errorMsg: "", code: "" } } }), { kind: "failed" });
  });
});

describe("C12 CompressFiles' answer", () => {
  const job = { id: "j1", total: 3, done: 0, saved: 0n, finished: false, results: [] };
  test("the job, at once", () => {
    assert.deepEqual(m.startAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respCompressJob", respCompressJob: job } }), { kind: "job", job });
  });
  test("an older device, a refusal (the demo's), no answer", () => {
    assert.deepEqual(m.startAnswer({ error: true, errorCode: "unknown_payload", errorMessage: "x" }), { kind: "old" });
    assert.deepEqual(m.startAnswer({ error: true, errorCode: "demo", errorMessage: "changes are turned off" }), { kind: "refused", said: "changes are turned off" });
    assert.deepEqual(m.startAnswer(null), { kind: "failed" });
  });
});

describe("C13 what is asked, and the requests", () => {
  const f = (path, mime) => ({ path, hash: `h:${path}`, mime });
  test("only the photos and videos picked, in the order picked", () => {
    const picks = [f("/d/a.pdf", "application/pdf"), f("/p/b.jpg", "image/jpeg"), f("/z/c.zip", "application/zip"), f("/v/d.mov", "video/quicktime"), f("/p/e.HEIC", "application/octet-stream")];
    assert.deepEqual(m.compressItems(picks).map((x) => x.path), ["/p/b.jpg", "/v/d.mov", "/p/e.HEIC"]);
  });
  test("each path once", () => {
    assert.deepEqual(m.compressItems([f("/p/b.jpg", "image/jpeg"), f("/p/b.jpg", "image/jpeg"), f("/p/c.jpg", "image/jpeg")]).map((x) => x.path), ["/p/b.jpg", "/p/c.jpg"]);
  });
  test("at most the first 500, counted after documents and repeats are left out", () => {
    const many = [f("/d/x.pdf", "application/pdf"), ...Array.from({ length: 620 }, (_, i) => f(`/p/${i}.jpg`, "image/jpeg"))];
    many.splice(10, 0, f("/p/3.jpg", "image/jpeg"));
    const asked = m.compressItems(many);
    assert.equal(m.cMaxCompress, 500);
    assert.equal(asked.length, 500);
    assert.equal(asked[0].path, "/p/0.jpg");
    assert.equal(asked[499].path, "/p/499.jpg");
  });
  test("nothing picked that can be: nothing asked", () => {
    assert.deepEqual(m.compressItems([f("/d/a.pdf", "application/pdf")]), []);
  });
  test("GetCompressOptions with each file's path and the content shown", () => {
    assert.deepEqual(m.optionsPayload([{ path: "/a.jpg", hash: "h1" }, { path: "/b.mov" }]), {
      $case: "reqGetCompressOptions", reqGetCompressOptions: { items: [{ path: "/a.jpg", hash: "h1" }, { path: "/b.mov", hash: "" }] },
    });
  });
  test("CompressFiles with the tasks, GetCompressJob with the job's id", () => {
    const tasks = m.tasksOf([png], pick("video_none"));
    assert.deepEqual(m.compressPayload(tasks), { $case: "reqCompressFiles", reqCompressFiles: { tasks: [{ path: "/p/shot.png", hash: "h:/p/shot.png", preset: "png_to_jpeg" }] } });
    assert.deepEqual(m.jobPayload("j1"), { $case: "reqGetCompressJob", reqGetCompressJob: { id: "j1" } });
  });
});

const result = (path, state, extra = {}) => ({ path, state, percent: 0, message: "", ...extra });
const file = (path, hash, extra = {}) => ({ path, hash, mime: "image/jpeg", size: 0, size64: 0n, uploadOnly: false, versions: 0, outOfImages: false, originalHash: "", originalRemoved: false, ...extra });

describe("J1 GetCompressJob's answer", () => {
  const job = { id: "j1", total: 1, done: 1, saved: 5n, finished: true, results: [] };
  const answer = (j) => ({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respCompressJob", respCompressJob: j } });
  test("the job", () => assert.deepEqual(m.pollAnswer(answer(job), "j1"), { kind: "job", job }));
  test("not_found, or another job's: gone", () => {
    assert.deepEqual(m.pollAnswer({ error: true, errorCode: "not_found", errorMessage: "there is no compress job" }, "j1"), { kind: "gone" });
    assert.deepEqual(m.pollAnswer(answer({ ...job, id: "j2" }), "j1"), { kind: "gone" });
  });
  test("no answer: asked again later", () => {
    assert.deepEqual(m.pollAnswer(null, "j1"), { kind: "failed" });
    assert.deepEqual(m.pollAnswer({ error: true, errorCode: "", errorMessage: "" }, "j1"), { kind: "failed" });
  });
});

describe("J2 how often the job is asked about", () => {
  test("every 2 s while the dialog shows it", () => assert.equal(m.pollDelay(true, true, false), 2000));
  test("every 10 s while only a page follows it", () => assert.equal(m.pollDelay(false, true, false), 10000));
  test("never once it is over, or nothing looks", () => {
    assert.equal(m.pollDelay(true, true, true), null);
    assert.equal(m.pollDelay(false, false, false), null);
  });
});

describe("J3 the job's progress", () => {
  test("tasks over, and a running video's share of the next", () => {
    const p = m.jobProgress({ done: 1, total: 4, results: [result("/a", "done"), result("/b", "running", { percent: 50 }), result("/c", "waiting"), result("/d", "waiting")] });
    assert.deepEqual(p, { done: 1, total: 4, fraction: 1.5 / 4 });
  });
  test("a total that grew (tasks joined) is the device's", () => {
    assert.equal(m.jobProgress({ done: 2, total: 9, results: [] }).total, 9);
  });
  test("never past the whole, nor a percent outside 0-100", () => {
    assert.equal(m.jobProgress({ done: 3, total: 3, results: [result("/x", "running", { percent: 400 })] }).fraction, 1);
    assert.equal(m.jobProgress({ done: 0, total: 0, results: [] }).fraction, 0);
  });
});

describe("J4 what the job came to", () => {
  const job = {
    saved: 3000n,
    results: [
      result("/p/a.jpg", "done", { file: file("/p/a.jpg", "n1") }),
      result("/p/b.png", "done", { file: file("/p/b.jpg", "n2") }),
      result("/p/c.jpg", "skipped", { message: "compressed, it came out no smaller, so the file was left as it was" }),
      result("/v/d.mov", "failed", { message: "the device could not compress this file" }),
      result("/v/e.mov", "waiting"),
    ],
  };
  test("compressed, freed, and the files left as they were with the device's words", () => {
    assert.deepEqual(m.jobOutcome(job), {
      compressed: 2, saved: 3000,
      failed: [
        { path: "/p/c.jpg", name: "c.jpg", message: "compressed, it came out no smaller, so the file was left as it was" },
        { path: "/v/d.mov", name: "d.mov", message: "the device could not compress this file" },
      ],
    });
  });
  test("gone mid-job: the tasks not over weren't compressed either", () => {
    assert.deepEqual(m.jobOutcome(job, true).failed.map((f) => f.name), ["c.jpg", "d.mov", "e.mov"]);
    assert.equal(m.jobOver({ finished: false }, true), true);
    assert.equal(m.jobOver({ finished: false }, false), false);
  });
});

describe("J5 each done result put in place once", () => {
  const job = { results: [result("/p/a.jpg", "done", { file: file("/p/a.jpg", "n1") }), result("/p/b.jpg", "running"), result("/p/c.jpg", "done")] };
  test("the done ones with a file, not yet seen", () => {
    const first = m.newlyDone(job, new Set());
    assert.deepEqual(first.map((r) => r.path), ["/p/a.jpg"]);
    const seen = new Set(first.map(m.resultKey));
    assert.deepEqual(m.newlyDone(job, seen), []);
  });
});

describe("J6 the file shown after", () => {
  const shown = file("/p/b.png", "old", { mime: "image/png", copies: 3, created: new Date(0), size64: 6000n });
  test("the device's name, hash, size and original over the old one; its copies kept", () => {
    const now = m.compressedFile(shown, { file: file("/p/b.jpg", "new", { size64: 1000n, originalHash: "old", originalRemoved: true }) });
    assert.equal(now.path, "/p/b.jpg");
    assert.equal(now.hash, "new");
    assert.equal(now.size64, 1000n);
    assert.equal(now.originalHash, "old");
    assert.equal(now.originalRemoved, true);
    assert.equal(now.copies, 3);
    assert.deepEqual(now.created, new Date(0));
  });
});

describe("L1 ListLargestFiles' payload", () => {
  test("pages of 50, after the previous page's token", () => {
    assert.deepEqual(m.largestPayload(""), { $case: "reqListLargestFiles", reqListLargestFiles: { after: "", limit: 50 } });
    assert.deepEqual(m.largestPayload("123:abc").reqListLargestFiles.after, "123:abc");
  });
});

describe("L2 its answer", () => {
  test("a page and the next token", () => {
    const a = m.largestAnswer({ error: false, errorCode: "", errorMessage: "", payload: { $case: "respListOfFiles", respListOfFiles: { files: [file("/a", "h")], token: "9:h" } } });
    assert.equal(a.kind, "page");
    assert.equal(a.token, "9:h");
  });
  test("an older device", () => assert.deepEqual(m.largestAnswer({ error: true, errorCode: "unknown_payload", errorMessage: "x" }), { kind: "old" }));
  test("a failure, in the device's words when it said why", () => {
    assert.deepEqual(m.largestAnswer({ error: true, errorCode: "", errorMessage: "database is down" }), { kind: "failed", said: "database is down" });
    assert.deepEqual(m.largestAnswer(null), { kind: "failed", said: "" });
  });
});

describe("L3 a page added under the rows", () => {
  test("appended in order", () => {
    assert.deepEqual(m.addPage([file("/a", "1")], [file("/b", "2"), file("/c", "3")]).map(m.pathKey), ["/a", "/b", "/c"]);
  });
  test("no path shown twice, nor content shown at another path", () => {
    const out = m.addPage([file("/a", "1"), file("/b", "2")], [file("/b", "2"), file("/z", "1"), file("/c", "3"), file("/c", "4")]);
    assert.deepEqual(out.map((f) => `${f.path}#${f.hash}`), ["/a#1", "/b#2", "/c#3"]);
  });
});

describe("L4 what the screen shows", () => {
  test("the first page on its way", () => assert.deepEqual(m.largestView(0, { kind: "loading" }), { kind: "loading" }));
  test("an older device, whatever was shown", () => {
    assert.deepEqual(m.largestView(0, { kind: "old" }), { kind: "old" });
    assert.deepEqual(m.largestView(3, { kind: "old" }), { kind: "old" });
  });
  test("the first page failed: the device's words or ours, with Try again", () => {
    assert.deepEqual(m.largestView(0, { kind: "failed", said: "" }), { kind: "failed", said: "" });
  });
  test("no files at all", () => assert.deepEqual(m.largestView(0, { kind: "end" }), { kind: "empty" }));
  test("rows, with at their foot a spinner, a failure or nothing", () => {
    assert.deepEqual(m.largestView(50, { kind: "loading" }), { kind: "rows", foot: "loading" });
    assert.deepEqual(m.largestView(50, { kind: "failed", said: "" }), { kind: "rows", foot: "failed" });
    assert.deepEqual(m.largestView(50, { kind: "more" }), { kind: "rows", foot: "none" });
    assert.deepEqual(m.largestView(12, { kind: "end" }), { kind: "rows", foot: "none" });
  });
});

describe("L5 the next page", () => {
  test("asked only while there is one and nothing is on its way", () => {
    assert.equal(m.wantsMore({ kind: "more" }), true);
    for (const k of [{ kind: "loading" }, { kind: "end" }, { kind: "old" }, { kind: "failed", said: "" }]) assert.equal(m.wantsMore(k), false);
  });
});

describe("L6 in N places", () => {
  test("only for content at several paths", () => {
    assert.equal(m.placesOf({ copies: 3 }), 3);
    assert.equal(m.placesOf({ copies: 1 }), 0);
    assert.equal(m.placesOf({}), 0);
  });
});

describe("L7 photos and videos", () => {
  test("by their type", () => {
    assert.equal(m.isMediaFile({ path: "/a", mime: "image/png" }), true);
    assert.equal(m.isMediaFile({ path: "/a", mime: "video/quicktime" }), true);
    assert.equal(m.isMediaFile({ path: "/a.pdf", mime: "application/pdf" }), false);
  });
  test("a HEIC or HEIF by its name, whatever its type", () => {
    assert.equal(m.isMediaFile({ path: "/IMG_1.HEIC", mime: "application/octet-stream" }), true);
    assert.equal(m.isMediaFile({ path: "/x.heif", mime: "" }), true);
  });
});

describe("L8 Compress in the selection bar", () => {
  test("on while a photo or a video is picked", () => {
    assert.equal(m.canCompress([{ path: "/a.pdf", mime: "application/pdf" }, { path: "/b.mov", mime: "video/quicktime" }]), true);
    assert.equal(m.canCompress([{ path: "/a.pdf", mime: "application/pdf" }]), false);
    assert.equal(m.canCompress([]), false);
  });
});

describe("L9 the list after a job", () => {
  test("read again when anything was compressed", () => {
    assert.equal(m.reloadsAfter({ compressed: 1 }), true);
    assert.equal(m.reloadsAfter({ compressed: 0 }), false);
  });
});

describe("L10 a row's folder", () => {
  test("the folder it is in, / at the top", () => {
    assert.equal(m.folderOf("/Photos/2024/IMG_1.jpg"), "/Photos/2024");
    assert.equal(m.folderOf("/notes.txt"), "/");
    assert.equal(m.leafOf("/Photos/2024/IMG_1.jpg"), "IMG_1.jpg");
  });
});

describe("R1 no Revert to original for a compressed file", () => {
  test("an edit offers it; a compressed copy, whose original is gone, doesn't", () => {
    assert.equal(e.canRevert({ originalHash: "abc" }), true);
    assert.equal(e.canRevert({ originalHash: "abc", originalRemoved: true }), false);
    assert.equal(e.canRevert({ originalHash: "", originalRemoved: true }), false);
  });
});

describe("R2 Info's note", () => {
  test("for a compressed file only", () => {
    assert.equal(m.compressedNote({ originalRemoved: true }), true);
    assert.equal(m.compressedNote({ originalRemoved: false }), false);
    assert.equal(m.compressedNote({}), false);
  });
});
