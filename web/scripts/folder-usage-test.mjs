// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Files' Storage view (issue #195, src/components/folderUsage.ts).
// The web app has no test runner, so this loads the module through Vite's
// SSR loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/folder-usage-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too (iOS and Android FilesExplorerView):
// change them together.
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
    cacheDir: join(tmpdir(), "otc-folder-usage-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/folderUsage.ts");
});
after(() => server?.close());

const GB = 1n << 30n;
const MB = 1n << 20n;
const entry = (path, isDir, filesSize, fileCount, versionsSize = 0n) => ({ path, isDir, filesSize, fileCount, versionsSize });
const listed = (path, dir, extra = {}) => ({ path, mime: dir ? "inode/directory" : "text/plain", ...extra });

describe("which answer is the folder's", () => {
  test("FolderUsage.path ends with a slash; the folder shown may not", () => {
    assert.equal(m.usageIsFor({ path: "/Music/" }, "/Music/"), true);
    assert.equal(m.usageIsFor({ path: "/Music/" }, "/Music"), true);
    assert.equal(m.usageIsFor({ path: "/" }, "/"), true);
    assert.equal(m.usageIsFor({ path: "/" }, ""), true);
  });
  test("another folder's answer is dropped", () => {
    assert.equal(m.usageIsFor({ path: "/Music/" }, "/Music/Live/"), false);
    assert.equal(m.usageIsFor({ path: "/Music/Live/" }, "/Music/"), false);
    assert.equal(m.usageIsFor({ path: "/music/" }, "/Music/"), false);
  });
});

describe("which answer is shown", () => {
  test("the latest request's, for the folder shown", () => {
    assert.equal(m.usageAnswerFits(3, 3, { path: "/Music/" }, "/Music"), true);
  });
  test("an older request's for the same folder (before a delete or a refresh) is dropped", () => {
    assert.equal(m.usageAnswerFits(2, 3, { path: "/Music/" }, "/Music"), false);
  });
  test("the latest request's for another folder is dropped", () => {
    assert.equal(m.usageAnswerFits(3, 3, { path: "/Music/Live/" }, "/Music"), false);
  });
});

describe("shares", () => {
  test("no folder total: no bar", () => {
    assert.deepEqual(m.shareOf(0, 0), { kind: "none" });
    assert.equal(m.barWidth(0, 0), 0);
  });
  test("whole percentages", () => {
    assert.deepEqual(m.shareOf(50, 100), { kind: "percent", value: 50 });
    assert.deepEqual(m.shareOf(100, 100), { kind: "percent", value: 100 });
    assert.deepEqual(m.shareOf(1, 100), { kind: "percent", value: 1 });
    assert.deepEqual(m.shareOf(2, 3), { kind: "percent", value: 67 });
  });
  test("above nothing but under 1%: under 1%, never 0% or a rounded 1%", () => {
    assert.deepEqual(m.shareOf(1, 1000), { kind: "small" });
    assert.deepEqual(m.shareOf(0.6, 100), { kind: "small" });
  });
  test("nothing at all is 0%", () => {
    assert.deepEqual(m.shareOf(0, 100), { kind: "percent", value: 0 });
  });
  test("a bar shows a sliver for anything above nothing", () => {
    assert.equal(m.barWidth(1, 1e9), 0.5);
    assert.equal(m.barWidth(25, 100), 25);
    assert.equal(m.barWidth(0, 100), 0);
  });
});

describe("rows", () => {
  const usage = {
    path: "/",
    entries: [
      entry("/Camera Uploads", true, 3n * GB, 900),
      entry("/Music/Live", true, 200n * MB, 12),
      entry("/Music/Live", false, 200n * MB, 1),
      entry("/Backups/Old laptop/Drafts", true, 0n, 0, 5n * MB),
      entry("/notes.txt", false, 1000n, 1),
    ],
    filesSize: 3n * GB + 400n * MB + 1000n,
    fileCount: 914,
    versionsSize: 5n * MB,
  };
  const listing = [
    listed("/notes.txt", false),
    listed("/Music/Live", false, { uploadOnly: true }),
    listed("/Music/Live", true, { outOfImages: true }),
    listed("/Camera Uploads", true),
  ];
  // Once the module is loaded (describe's body runs before that).
  let out;
  before(() => { out = m.usageRows(usage, listing, (x) => x.path); });
  test("in the device's order, never sorted again", () => {
    assert.deepEqual(out.rows.map((r) => `${r.isDir ? "d" : "f"}:${r.path}`),
      ["d:/Camera Uploads", "d:/Music/Live", "f:/Music/Live", "d:/Backups/Old laptop/Drafts", "f:/notes.txt"]);
  });
  test("a file and a folder of one name each join their own listing entry", () => {
    assert.equal(out.rows[1].file.outOfImages, true);
    assert.equal(out.rows[2].file.uploadOnly, true);
    assert.notEqual(out.rows[1].key, out.rows[2].key);
  });
  test("a child holding only older versions isn't listed: no file, still a row", () => {
    assert.equal(out.rows[3].file, null);
    assert.equal(out.rows[3].name, "Drafts");
    assert.equal(out.rows[3].total, Number(5n * MB));
  });
  test("a row's size and the folder's total include older versions", () => {
    assert.equal(out.total, Number(3n * GB + 405n * MB + 1000n));
    assert.equal(out.versions, Number(5n * MB));
    assert.equal(out.files, 914);
    assert.equal(out.rows[0].total, Number(3n * GB));
    assert.deepEqual(out.rows[0].share, { kind: "percent", value: 88 });
    assert.deepEqual(out.rows[4].share, { kind: "small" });
  });
  test("names are the last part of the path", () => {
    assert.deepEqual(out.rows.map((r) => r.name), ["Camera Uploads", "Live", "Live", "Drafts", "notes.txt"]);
  });
  test("a listing with names only, joined through the full path", () => {
    const rows = m.usageRows({ path: "/a/", entries: [entry("/a/b", true, 10n, 1)], filesSize: 10n, fileCount: 1, versionsSize: 0n },
      [listed("b", true)], (x) => `/a/${x.path}`).rows;
    assert.ok(rows[0].file);
  });
  test("an empty folder: no rows, zero totals", () => {
    const empty = m.usageRows({ path: "/x/", entries: [], filesSize: 0n, fileCount: 0, versionsSize: 0n }, [], (x) => x.path);
    assert.deepEqual(empty, { rows: [], total: 0, versions: 0, files: 0 });
  });
  test("a folder that takes no space: no share shows, the label says 0%", () => {
    assert.equal(m.shareShown({ kind: "none" }), false);
    assert.deepEqual(m.labelShare({ kind: "none" }), { kind: "percent", value: 0 });
    assert.equal(m.shareShown({ kind: "percent", value: 0 }), true, "0% of a folder that takes space shows");
    assert.deepEqual(m.labelShare({ kind: "small" }), { kind: "small" });
    assert.deepEqual(m.labelShare({ kind: "percent", value: 45 }), { kind: "percent", value: 45 });
  });
  test("a folder that takes no space: rows without bars", () => {
    const zero = m.usageRows({ path: "/z/", entries: [entry("/z/empty.txt", false, 0n, 1)], filesSize: 0n, fileCount: 1, versionsSize: 0n }, [], (x) => x.path);
    assert.deepEqual(zero.rows[0].share, { kind: "none" });
    assert.equal(zero.rows[0].bar, 0);
  });
});
