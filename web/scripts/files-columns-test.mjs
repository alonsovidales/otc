// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the widths of Files' list columns (src/components/filesColumns.ts):
// the minimums, the columns hidden as the list narrows, a resize, a reset and
// the widths kept in this browser. The web app has no test runner, so this
// loads the module through Vite's SSR loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/files-columns-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// This browser's storage, which can also refuse (private browsing, blocked
// site data).
const store = new Map();
let refuse = false;
globalThis.localStorage = {
  getItem: (k) => { if (refuse) throw new Error("SecurityError"); return store.has(k) ? store.get(k) : null; },
  setItem: (k, v) => { if (refuse) throw new Error("QuotaExceededError"); store.set(k, String(v)); },
  removeItem: (k) => { if (refuse) throw new Error("SecurityError"); store.delete(k); },
};

let server;
let c; // the module under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-files-columns-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  c = await server.ssrLoadModule("/src/components/filesColumns.ts");
});
after(() => server?.close());

// What a page in English measured (15px system font): the cells' padding,
// "0000000000…", "1023.0 MB" / "1.5 KB" and a date with and without its time.
const samples = {
  pad: { name: 24, size: 24, created: 24, modified: 24 },
  name: 100, size: 70, sizeShort: 40, date: 150, dateShort: 80,
};
const metrics = () => c.columnMetrics(samples);
const total = (l, m) => m.check + l.shown.reduce((s, k) => s + l.width[k], 0);

describe("minimums and defaults", () => {
  test("Name has room for 10 characters, its icons and its padding", () => {
    const m = metrics();
    assert.equal(m.nameText, 100);
    assert.equal(m.nameMin, 24 + 100 + c.cNameTrail + 2);
    assert.equal(c.cNameTrail, 75, "a gap, the lock, a gap and the narrowest of the switch (24) and the versions pill (35)");
    assert.ok(m.nameRoom > m.nameMin, "the date columns show only while Name keeps more than its minimum");
  });
  test("the others fit a long value by default and a short one at their minimum", () => {
    const m = metrics();
    assert.deepEqual(m.def, { size: 98, created: 178, modified: 178 });
    assert.deepEqual(m.min, { size: 66, created: 108, modified: 108 });
    for (const k of ["size", "created", "modified"]) assert.ok(m.min[k] < m.def[k]);
  });
  test("measured widths round up, never down", () => {
    const m = c.columnMetrics({ ...samples, name: 100.2, date: 150.01 });
    assert.equal(m.nameText, 101);
    assert.equal(m.def.created, 179);
  });
});

describe("the columns a width shows", () => {
  test("wide: all four, Name taking what the others leave", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    assert.deepEqual(l.shown, ["name", "size", "created", "modified"]);
    assert.deepEqual([l.width.size, l.width.created, l.width.modified], [98, 178, 178]);
    assert.equal(l.width.name, 1000 - 26 - 98 - 178 - 178);
    assert.equal(total(l, m), 1000);
    assert.equal(c.columnsTemplate(l, m), "26px minmax(0, 1fr) 98px 178px 178px");
  });
  test("narrower: Created goes first, then Modified; Size always stays", () => {
    const m = metrics();
    const all = m.check + m.nameRoom + m.def.size + m.def.created + m.def.modified;
    assert.deepEqual(c.shownColumns(all, m), ["name", "size", "created", "modified"]);
    assert.deepEqual(c.shownColumns(all - 1, m), ["name", "size", "modified"]);
    const two = m.check + m.nameRoom + m.def.size + m.def.modified;
    assert.deepEqual(c.shownColumns(two, m), ["name", "size", "modified"]);
    assert.deepEqual(c.shownColumns(two - 1, m), ["name", "size"]);
    assert.deepEqual(c.shownColumns(100, m), ["name", "size"]);
    const l = c.fitColumns(two - 1, {}, m);
    assert.equal(c.columnsTemplate(l, m), `26px minmax(0, 1fr) ${m.def.size}px`);
  });
  test("which columns show doesn't hang on the widths given (a drag never hides one)", () => {
    const m = metrics();
    const wide = c.fitColumns(900, { created: 600, modified: 600 }, m);
    assert.deepEqual(wide.shown, c.fitColumns(900, {}, m).shown);
  });
});

describe("Name's minimum is kept", () => {
  test("columns given more than fits give way, the last first, down to their minimums", () => {
    const m = metrics();
    const room = 800;
    const l = c.fitColumns(room, { size: 150, created: 400, modified: 300 }, m);
    assert.deepEqual(l.shown, ["name", "size", "created", "modified"]);
    assert.equal(l.width.name, m.nameMin);
    assert.equal(total(l, m), room);
    // Modified gave first, then Created; Size kept what it was given.
    assert.equal(l.width.modified, m.min.modified);
    assert.equal(l.width.size, 150);
    assert.ok(l.width.created < 400 && l.width.created > m.min.created);
  });
  test("a list too narrow even for the minimums: Name gets what is left, never less than nothing", () => {
    const m = metrics();
    const l = c.fitColumns(150, {}, m);
    assert.deepEqual(l.shown, ["name", "size"]);
    assert.equal(l.width.size, m.min.size);
    assert.equal(l.width.name, 150 - 26 - m.min.size);
    assert.equal(c.fitColumns(20, {}, m).width.name, 0);
  });
  test("a width given below a column's minimum is taken as the minimum", () => {
    const m = metrics();
    assert.equal(c.fitColumns(1000, { size: 10 }, m).width.size, m.min.size);
  });
});

describe("a resize", () => {
  test("Name wider takes from Size, then Created, then Modified, down to their minimums", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    const a = c.resizeColumn(l, m, "name", 20);
    assert.equal(a.width.name, l.width.name + 20);
    assert.equal(a.width.size, l.width.size - 20);
    assert.equal(a.width.created, l.width.created);
    const far = c.resizeColumn(l, m, "name", 5000);
    assert.deepEqual([far.width.size, far.width.created, far.width.modified], [m.min.size, m.min.created, m.min.modified]);
    assert.equal(total(far, m), 1000, "the list stays as wide as it was: nothing scrolls sideways");
  });
  test("narrower gives the room to the next column, and stops at its own minimum", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    const a = c.resizeColumn(l, m, "name", -30);
    assert.equal(a.width.name, l.width.name - 30);
    assert.equal(a.width.size, l.width.size + 30);
    const far = c.resizeColumn(l, m, "name", -5000);
    assert.equal(far.width.name, m.nameMin);
    assert.equal(total(far, m), 1000);
    const created = c.resizeColumn(l, m, "created", -5000);
    assert.equal(created.width.created, m.min.created);
    assert.equal(created.width.modified, l.width.modified + (l.width.created - m.min.created));
    assert.equal(created.width.size, l.width.size, "a column before it doesn't move");
  });
  test("a resize goes from where the drag began, so going back undoes it", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    assert.deepEqual(c.resizeColumn(l, m, "size", 0), l);
    const there = c.resizeColumn(l, m, "size", 5000);
    assert.deepEqual(c.resizeColumn(l, m, "size", 0.4).width, l.width, "under half a pixel is nothing");
    assert.ok(there.width.size > l.width.size);
  });
  test("the last column shown has no edge, nor does a hidden one", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    assert.equal(c.resizeColumn(l, m, "modified", 40), l);
    const narrow = c.fitColumns(500, {}, m);
    assert.deepEqual(narrow.shown, ["name", "size"]);
    assert.equal(c.resizeColumn(narrow, m, "created", 40), narrow);
    assert.equal(c.resizeColumn(narrow, m, "size", 40), narrow);
    // With Created hidden, Size's edge gives to and takes from Modified.
    const mid = c.fitColumns(m.check + m.nameRoom + m.def.size + m.def.modified, {}, m);
    const s = c.resizeColumn(mid, m, "size", 10);
    assert.equal(s.width.modified, mid.width.modified - 10);
  });
  test("the keyboard's step", () => {
    assert.equal(c.cKeyStep, 16);
  });
  test("what a separator says: the column's width, its minimum and its most", () => {
    const m = metrics();
    const l = c.fitColumns(1000, {}, m);
    const v = c.edgeValues(l, m, "size");
    assert.equal(v.now, l.width.size);
    assert.equal(v.min, m.min.size);
    assert.equal(v.max, l.width.size + (l.width.created - m.min.created) + (l.width.modified - m.min.modified));
    assert.equal(c.edgeValues(l, m, "name").min, m.nameMin);
    assert.equal(c.resizeColumn(l, m, "size", 10_000).width.size, v.max);
  });
});

describe("the widths kept", () => {
  test("only the columns a resize changed are kept", () => {
    const m = metrics();
    const l = c.fitColumns(1000, { modified: 200 }, m);
    const a = c.resizeColumn(l, m, "name", 20);
    assert.deepEqual(c.widthsAfter({ modified: 200 }, l, a), { modified: 200, size: l.width.size - 20 });
  });
  test("a double-click puts that column back; Name's puts every column back", () => {
    assert.deepEqual(c.resetColumn({ size: 150, created: 120 }, "size"), { created: 120 });
    assert.deepEqual(c.resetColumn({ size: 150, created: 120 }, "name"), {});
    assert.deepEqual(c.resetColumn({}, "modified"), {});
    const m = metrics();
    const back = c.fitColumns(1000, c.resetColumn({ size: 150 }, "size"), m);
    assert.deepEqual(back, c.fitColumns(1000, {}, m));
  });
  test("stored as JSON under otc_files_columns, and read back", () => {
    store.clear();
    assert.deepEqual(c.loadColumnWidths(), {});
    c.saveColumnWidths({ size: 150, modified: 210 });
    assert.equal(store.get("otc_files_columns"), JSON.stringify({ size: 150, modified: 210 }));
    assert.deepEqual(c.loadColumnWidths(), { size: 150, modified: 210 });
    c.saveColumnWidths({});
    assert.equal(store.has("otc_files_columns"), false, "nothing given: nothing kept");
  });
  test("a stored value that isn't a sane width is left out", () => {
    assert.deepEqual(c.parseWidths(null), {});
    assert.deepEqual(c.parseWidths("not json"), {});
    assert.deepEqual(c.parseWidths("[120, 130]"), {});
    assert.deepEqual(c.parseWidths("null"), {});
    assert.deepEqual(c.parseWidths(JSON.stringify({ size: -5, created: "120", modified: 1e9, name: 300 })), {});
    assert.deepEqual(c.parseWidths(JSON.stringify({ size: 120.6, created: 0, modified: 180 })), { size: 121, modified: 180 });
  });
  test("storage that refuses: the defaults, and nothing thrown", () => {
    store.set("otc_files_columns", JSON.stringify({ size: 150 }));
    refuse = true;
    try {
      assert.deepEqual(c.loadColumnWidths(), {});
      assert.doesNotThrow(() => c.saveColumnWidths({ size: 160 }));
      assert.doesNotThrow(() => c.saveColumnWidths({}));
    } finally { refuse = false; }
    assert.deepEqual(c.loadColumnWidths(), { size: 150 });
  });
});
