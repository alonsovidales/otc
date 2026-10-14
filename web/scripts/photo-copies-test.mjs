// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for deleting from Images a photo the device holds at several paths
// (issue #201, src/components/photoCopies.ts). The web app has no test
// runner, so this loads the module through Vite's SSR loader in Node, as
// i18n-test.mjs does:
//
//   node web/scripts/photo-copies-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too (iOS PhotoGallery, Android
// PhotoGalleryViewModel): change them together.
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
    cacheDir: join(tmpdir(), "otc-photo-copies-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/photoCopies.ts");
});
after(() => server?.close());

const photo = (path, copies, hash = `h-${path}`) => ({ path, hash, copies });

describe("what the confirmation asks", () => {
  test("one photo with copies: only this one, or all of them", () => {
    assert.deepEqual(m.copiesChoice([photo("/a.jpg", 3)]), { kind: "single", count: 3 });
    assert.deepEqual(m.copiesChoice([photo("/a.jpg", 2)]), { kind: "single", count: 2 });
  });
  test("one photo at one path, or a device that doesn't say: the dialog as before", () => {
    assert.deepEqual(m.copiesChoice([photo("/a.jpg", 1)]), { kind: "plain" });
    assert.deepEqual(m.copiesChoice([photo("/a.jpg", undefined)]), { kind: "plain" });
    assert.deepEqual(m.copiesChoice([photo("/a.jpg", 0)]), { kind: "plain" });
  });
  test("several: how many of them have copies", () => {
    assert.deepEqual(m.copiesChoice([photo("/a", 1), photo("/b", 4), photo("/c", undefined), photo("/d", 2)]), { kind: "several", count: 2 });
    assert.deepEqual(m.copiesChoice([photo("/a", 2), photo("/b", 2)]), { kind: "several", count: 2 });
  });
  test("several without copies (or from an older device): the dialog as before", () => {
    assert.deepEqual(m.copiesChoice([photo("/a", 1), photo("/b", undefined)]), { kind: "plain" });
    assert.deepEqual(m.copiesChoice([]), { kind: "plain" });
  });
});

describe("DelFile", () => {
  test("only this one: the path and the hash shown, no every_copy", () => {
    assert.deepEqual(m.delFileFields(photo("/a.jpg", 3), false), { path: "/a.jpg", hash: "h-/a.jpg" });
  });
  test("all: every_copy for a photo with copies", () => {
    assert.deepEqual(m.delFileFields(photo("/a.jpg", 3), true), { path: "/a.jpg", hash: "h-/a.jpg", everyCopy: true });
  });
  test("every copy of a selection: only for the photos that have copies", () => {
    assert.deepEqual(m.delFileFields(photo("/b.jpg", 1), true), { path: "/b.jpg", hash: "h-/b.jpg" });
  });
  test("never every_copy when copies is unset (an older device would delete only the path)", () => {
    assert.deepEqual(m.delFileFields(photo("/c.jpg", undefined), true), { path: "/c.jpg", hash: "h-/c.jpg" });
  });
  test("no hash, no hash field", () => {
    assert.deepEqual(m.delFileFields(photo("/d.jpg", 2, ""), false), { path: "/d.jpg" });
    assert.deepEqual(m.delFileFields({ path: "/e.jpg" }, true), { path: "/e.jpg" });
  });
});

describe("answers", () => {
  test("an ok Ack is a delete; the device's codes are kept; anything else failed", () => {
    assert.equal(m.deleteOutcome(true, ""), "deleted");
    assert.equal(m.deleteOutcome(false, "upload_only"), "upload_only");
    assert.equal(m.deleteOutcome(false, "copy_upload_only"), "copy_upload_only");
    assert.equal(m.deleteOutcome(false, "changed"), "changed");
    assert.equal(m.deleteOutcome(false, ""), "failed");
    assert.equal(m.deleteOutcome(false, "unknown_payload"), "failed");
  });
  test("summed up: done, one shared reason, or failed for a mix", () => {
    assert.deepEqual(m.deleteSummary(["deleted", "deleted"]), { kind: "done", count: 0, reload: false });
    assert.deepEqual(m.deleteSummary(["deleted", "copy_upload_only", "copy_upload_only"]), { kind: "copy_upload_only", count: 2, reload: false });
    assert.deepEqual(m.deleteSummary(["changed", "deleted"]), { kind: "changed", count: 1, reload: true });
    assert.deepEqual(m.deleteSummary(["upload_only"]), { kind: "upload_only", count: 1, reload: false });
    assert.deepEqual(m.deleteSummary(["changed", "failed", "deleted"]), { kind: "failed", count: 2, reload: true });
    assert.deepEqual(m.deleteSummary(["copy_upload_only", "upload_only"]), { kind: "failed", count: 2, reload: false });
    assert.deepEqual(m.deleteSummary([]), { kind: "done", count: 0, reload: false });
  });
});

describe("what a delete says once done (the phones say the same)", () => {
  test("a single refusal: the device's own words, whatever the others did", () => {
    assert.deepEqual(m.deleteNotice(["deleted", "changed", "deleted"], "It changed: refresh and try again"),
      { kind: "said", text: "It changed: refresh and try again", count: 1, reload: true });
    assert.deepEqual(m.deleteNotice(["copy_upload_only"], "a copy is in the upload-only folder /Backups"),
      { kind: "said", text: "a copy is in the upload-only folder /Backups", count: 1, reload: false });
  });
  test("a single refusal without words: summed up", () => {
    assert.deepEqual(m.deleteNotice(["deleted", "changed"], ""), { kind: "changed", count: 1, reload: true });
  });
  test("upload_only and a failure keep their own texts", () => {
    assert.deepEqual(m.deleteNotice(["upload_only"], "words"), { kind: "upload_only", count: 1, reload: false });
    assert.deepEqual(m.deleteNotice(["deleted", "failed"], "words"), { kind: "failed", count: 1, reload: false });
  });
  test("two or more refusals: summed up by kind, a mix as failed", () => {
    assert.deepEqual(m.deleteNotice(["changed", "deleted", "changed"], "the last one's words"), { kind: "changed", count: 2, reload: true });
    assert.deepEqual(m.deleteNotice(["copy_upload_only", "copy_upload_only"], "words"), { kind: "copy_upload_only", count: 2, reload: false });
    assert.deepEqual(m.deleteNotice(["changed", "copy_upload_only"], "words"), { kind: "failed", count: 2, reload: true });
  });
  test("all deleted", () => {
    assert.deepEqual(m.deleteNotice(["deleted", "deleted"], ""), { kind: "done", count: 0, reload: false });
  });
});
