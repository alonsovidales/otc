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
import { readFileSync } from "node:fs";
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
    // One module with no imports (a type only): nothing to pre-bundle.
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

// English as the page writes it: a small t over the generated catalog.
const en = JSON.parse(readFileSync(join(WEB, "src/i18n/locales/en/web.images.json"), "utf8"));
const t = (key, args = {}) => {
  let text = en[key];
  assert.ok(text !== undefined, `no English for ${key}`);
  if (typeof text === "object") text = args[text.arg] === 1 ? text.one : text.other;
  return text.replace(/\{(\w+)\}/g, (_, a) => String(args[a]));
};

const ok = { outcome: "deleted" };
const changed = (said = "this file has changed since it was shown, so nothing was deleted: refresh and try again") => ({ outcome: "changed", said });
const copyUp = (said = "a copy is in the upload-only folder /Backups: no copy was deleted") => ({ outcome: "copy_upload_only", said });
const upOnly = (said = "this folder is upload only: nothing in it can be deleted") => ({ outcome: "upload_only", said });
const failed = (said = "") => ({ outcome: "failed", said });

describe("what a delete says once done (the phones say the same)", () => {
  test("all deleted: nothing to say", () => {
    assert.deepEqual(m.deleteNotice([ok, ok]), { lines: [], reload: false });
    assert.deepEqual(m.deleteNotice([]), { lines: [], reload: false });
  });
  test("a single refusal: the device's own words, whatever the others did", () => {
    assert.deepEqual(m.deleteNotice([ok, changed("It changed"), ok]), { lines: [{ kind: "said", text: "It changed" }], reload: true });
    assert.deepEqual(m.deleteNotice([copyUp("a copy is in /Backups")]), { lines: [{ kind: "said", text: "a copy is in /Backups" }], reload: false });
  });
  test("a single refusal without words: counted", () => {
    assert.deepEqual(m.deleteNotice([ok, changed("")]), { lines: [{ kind: "changed", count: 1 }], reload: true });
    assert.deepEqual(m.deleteNotice([{ outcome: "changed" }]), { lines: [{ kind: "changed", count: 1 }], reload: true });
  });
  test("upload_only and a failure keep their own texts", () => {
    assert.deepEqual(m.deleteNotice([upOnly()]), { lines: [{ kind: "upload_only", count: 1 }], reload: false });
    assert.deepEqual(m.deleteNotice([ok, failed("no such file or directory")]), { lines: [{ kind: "failed", count: 1 }], reload: false });
  });
  test("two or more for one reason: that reason, counted", () => {
    assert.deepEqual(m.deleteNotice([changed("a"), ok, changed("b")]), { lines: [{ kind: "changed", count: 2 }], reload: true });
    assert.deepEqual(m.deleteNotice([copyUp(), copyUp()]), { lines: [{ kind: "copy_upload_only", count: 2 }], reload: false });
    assert.deepEqual(m.deleteNotice([upOnly(), upOnly()]), { lines: [{ kind: "upload_only", count: 2 }], reload: false });
    assert.deepEqual(m.deleteNotice([failed(), failed("x")]), { lines: [{ kind: "failed", count: 2 }], reload: false });
  });
  test("different reasons: changed counted, then copies counted (iOS keptText, Android deleteRefusalNotes)", () => {
    assert.deepEqual(m.deleteNotice([copyUp(), ok, changed()]),
      { lines: [{ kind: "changed", count: 1 }, { kind: "copy_upload_only", count: 1 }], reload: true });
    assert.deepEqual(m.deleteNotice([changed(), copyUp(), changed(), copyUp(), copyUp()]),
      { lines: [{ kind: "changed", count: 2 }, { kind: "copy_upload_only", count: 3 }], reload: true });
  });
  test("different reasons: any other refusal in the device's words, each text once", () => {
    assert.deepEqual(m.deleteNotice([upOnly(), changed(), upOnly(), failed("no such file or directory")]),
      { lines: [{ kind: "changed", count: 1 }, { kind: "said", text: "this folder is upload only: nothing in it can be deleted" }, { kind: "said", text: "no such file or directory" }], reload: true });
    assert.deepEqual(m.deleteNotice([upOnly(), failed("no such file or directory")]),
      { lines: [{ kind: "said", text: "this folder is upload only: nothing in it can be deleted" }, { kind: "said", text: "no such file or directory" }], reload: false });
  });
  test("different reasons: one with no words (no answer came) counted by its reason", () => {
    assert.deepEqual(m.deleteNotice([changed(), failed(), failed()]),
      { lines: [{ kind: "changed", count: 1 }, { kind: "failed", count: 2 }], reload: true });
    assert.deepEqual(m.deleteNotice([copyUp(), upOnly(""), { outcome: "upload_only" }]),
      { lines: [{ kind: "copy_upload_only", count: 1 }, { kind: "upload_only", count: 2 }], reload: false });
  });
});

describe("how it reads", () => {
  test("a refusal by itself: in \"Delete failed: …\", as the phones' delete_failed", () => {
    assert.equal(m.refusedText(t, "this file has changed since it was shown, so nothing was deleted: refresh and try again"),
      "Delete failed: this file has changed since it was shown, so nothing was deleted: refresh and try again");
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([ok, changed()]).lines),
      "Delete failed: this file has changed since it was shown, so nothing was deleted: refresh and try again");
  });
  test("counted lines", () => {
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([changed(), changed()]).lines),
      "2 items changed on your device since they were shown, so they weren't deleted. Check them and try again.");
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([upOnly()]).lines), "1 item is in an upload-only folder and can't be deleted");
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([failed()]).lines), "1 item couldn't be deleted. Try again.");
  });
  test("a mix: one line per reason, in the phones' order", () => {
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([copyUp(), changed()]).lines),
      "1 item changed on your device since it was shown, so it wasn't deleted. Check it and try again.\n" +
      "1 item wasn't deleted: a copy of it is in an upload-only folder");
    assert.equal(m.deleteNoticeText(t, m.deleteNotice([upOnly(), copyUp(), copyUp()]).lines),
      "2 items weren't deleted: they have copies in an upload-only folder\n" +
      "Delete failed: this folder is upload only: nothing in it can be deleted");
  });
});
