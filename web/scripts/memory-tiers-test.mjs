// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for what a device's memory allows on the web
// (src/components/settings/memoryTiers.ts, release 160): Settings' Image
// Tagging switch off and disabled where Settings.image_tagging_available is
// false, Settings › Users without Add User where the users list's can_add is
// false, both as before on an older device that leaves them unset, and the
// device's refusals (tagging_unavailable, users_unavailable) doing the same.
// The device's answers go through the generated bindings, as the socket's
// do. Loaded through Vite's SSR loader in Node, as library-sort-test.mjs:
//
//   node web/scripts/memory-tiers-test.mjs        (needs web/node_modules)
//
// The phones follow the same rules: change them together.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // the module under test
let pb; // the generated bindings
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-memory-tiers-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/settings/memoryTiers.ts");
  pb = await server.ssrLoadModule("/src/proto/messages.ts");
});
after(() => server?.close());

// An answer as it comes off the socket: encoded by the device, decoded here.
const wire = (resp) => pb.RespEnvelope.decode(pb.RespEnvelope.encode(pb.RespEnvelope.fromPartial(resp)).finish());
const settings = (s) => wire({ payload: { $case: "respSettings", respSettings: s } }).payload.respSettings;
const users = (r) => wire({ payload: { $case: "respUsers", respUsers: r } });
const ack = (ok) => wire({ payload: { $case: "respAck", respAck: { ok } } });
const refused = (errorCode, errorMessage = "words") => wire({ error: true, errorCode, errorMessage });

const user = { uuid: "u1", username: "ana", port: 8081, active: true };

describe("Image Tagging's switch from Settings", () => {
  test("an older device leaves the field out: available, on or off as it says", () => {
    const s = settings({ imageTaggingEnabled: true });
    assert.equal(s.imageTaggingAvailable, undefined);
    assert.deepEqual(m.taggingSwitch(s), { on: true, available: true });
    assert.deepEqual(m.taggingSwitch(settings({ imageTaggingEnabled: false })), { on: false, available: true });
  });
  test("a device that can have it: as it says", () => {
    assert.deepEqual(m.taggingSwitch(settings({ imageTaggingEnabled: true, imageTaggingAvailable: true })), { on: true, available: true });
    assert.deepEqual(m.taggingSwitch(settings({ imageTaggingEnabled: false, imageTaggingAvailable: true })), { on: false, available: true });
  });
  test("a device under 6 GB: off and unavailable, whatever the setting", () => {
    const s = settings({ imageTaggingEnabled: false, imageTaggingAvailable: false });
    assert.equal(s.imageTaggingAvailable, false);
    assert.deepEqual(m.taggingSwitch(s), { on: false, available: false });
    // Never shown on, even if a device said so.
    assert.deepEqual(m.taggingSwitch({ imageTaggingEnabled: true, imageTaggingAvailable: false }), { on: false, available: false });
  });
});

describe("Image Tagging's switch after SetImageTaggingEnabled", () => {
  const on = { on: true, available: true };
  const off = { on: false, available: true };
  test("taken: the new state, still available", () => {
    assert.deepEqual(m.taggingAfterSet(off, true, ack(true)), on);
    assert.deepEqual(m.taggingAfterSet(on, false, ack(true)), off);
  });
  test("refused for the device's memory: off and unavailable", () => {
    assert.deepEqual(m.taggingAfterSet(off, true, refused("tagging_unavailable")), { on: false, available: false });
    assert.deepEqual(m.taggingAfterSet(on, true, refused("tagging_unavailable")), { on: false, available: false });
  });
  test("refused otherwise, or a failed Ack: as it was", () => {
    assert.deepEqual(m.taggingAfterSet(off, true, refused("")), off);
    assert.deepEqual(m.taggingAfterSet(off, true, refused("demo")), off);
    assert.deepEqual(m.taggingAfterSet(on, false, ack(false)), on);
    assert.deepEqual(m.taggingAfterSet(off, true, wire({})), off);
  });
});

describe("Settings › Users' Add User", () => {
  test("an older device leaves can_add out: offered", () => {
    const r = users({ users: [user] });
    assert.equal(r.payload.respUsers.canAdd, undefined);
    assert.equal(m.canAddUsers(r.payload.respUsers), true);
    assert.equal(m.canAddAfter(true, r), true);
  });
  test("a list that says so: offered or not, the users listed either way", () => {
    assert.equal(m.canAddAfter(false, users({ users: [user], canAdd: true })), true);
    const r = users({ users: [user], canAdd: false });
    assert.equal(r.payload.respUsers.canAdd, false);
    assert.equal(r.payload.respUsers.users.length, 1);
    assert.equal(m.canAddAfter(true, r), false);
    assert.equal(m.canAddAfter(true, users({ users: [], canAdd: false })), false);
  });
  test("a CreateUser that worked says it again", () => {
    assert.equal(m.canAddAfter(true, users({ users: [user], passwordSet: true, canAdd: true })), true);
    // An older device's answer, without the field.
    assert.equal(m.canAddAfter(true, users({ users: [user] })), true);
  });
  test("a CreateUser refused for the device's memory: no more Add User", () => {
    assert.equal(m.canAddAfter(true, refused("users_unavailable")), false);
  });
  test("refused otherwise, or no list in the answer: as it was", () => {
    assert.equal(m.canAddAfter(true, refused("")), true);
    assert.equal(m.canAddAfter(true, refused("demo")), true);
    assert.equal(m.canAddAfter(false, refused("")), false);
    assert.equal(m.canAddAfter(true, ack(false)), true);
  });
});
