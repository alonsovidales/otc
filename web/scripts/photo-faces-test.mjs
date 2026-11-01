// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a photo's faces in the viewer's Info (src/components/
// photoFaces.ts): when Info asks GetPhotoFaces, what its answer means, what
// a face's × takes off and where the focus goes after it, and when a face
// leaves the grid or opens a person Images already shows - plus the note
// once a face is taken off (personFixDone.ts). Loaded through Vite's SSR
// loader in Node as person-fix-test.mjs does:
//
//   node web/scripts/photo-faces-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // the module under test
let done; // personFixDone.ts
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-photo-faces-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Types only: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/photoFaces.ts");
  done = await server.ssrLoadModule("/src/components/personFixDone.ts");
});
after(() => server?.close());

// English as the page writes it: a small t over the generated catalog.
const en = JSON.parse(readFileSync(join(WEB, "src/i18n/locales/en/web.images.json"), "utf8"));
// A name is isolated (FSI ... PDI) as the page shows it; left out here.
const t = (key, args = {}) => {
  let text = en[key];
  assert.ok(text !== undefined, `no English for ${key}`);
  if (typeof text === "object") text = args[text.arg] === 1 ? text.one : text.other;
  return text.replace(/\{(\w+)\}/g, (_, a) => String(args[a])).replace(/[⁨⁩]/g, "");
};

const face = (personId, name, x = 10, extra = {}) => ({ personId, name, thumbnail: new Uint8Array([0xff, 0xd8, 0xff]), x, y: 20, w: 30, h: 30, ...extra });
const answer = (faces, faceRecognition = true, hash = "h1") => ({
  error: false, errorCode: "", payload: { $case: "respPhotoFaces", respPhotoFaces: { faces, faceRecognition, hash } },
});
const filter = (over = {}) => ({ tags: [], personIds: [], group: null, area: null, text: "", ...over });

describe("when Info asks for a photo's faces", () => {
  test("a photo, while face recognition is on or not known yet", () => {
    assert.equal(m.asksFaces({ mime: "image/jpeg" }, true, false), true);
    assert.equal(m.asksFaces({ mime: "image/heic" }, null, false), true);
    assert.equal(m.asksFaces({}, true, false), true); // no mime: a photo
  });
  test("never for a video, nor with face recognition off", () => {
    assert.equal(m.asksFaces({ mime: "video/mp4" }, true, false), false);
    assert.equal(m.asksFaces({ mime: "image/jpeg" }, false, false), false);
  });
  test("never on a device that didn't know the request, nor for no item", () => {
    assert.equal(m.asksFaces({ mime: "image/jpeg" }, true, true), false);
    assert.equal(m.asksFaces(undefined, true, false), false);
  });
  test("asks for the photo's path", () => {
    assert.deepEqual(m.facesPayload("/Photos/a.jpg"), { $case: "reqGetPhotoFaces", reqGetPhotoFaces: { path: "/Photos/a.jpg" } });
  });
});

describe("what the answer means", () => {
  test("the faces in the device's order, and the photo's hash", () => {
    const faces = [face("p1", "Ana", 10), face("p2", "", 200)];
    assert.deepEqual(m.facesAnswer(answer(faces)), { kind: "faces", faces, hash: "h1" });
  });
  test("none at all while face recognition is off, whatever came", () => {
    assert.deepEqual(m.facesAnswer(answer([face("p1", "Ana")], false)), { kind: "faces", faces: [], hash: "h1" });
  });
  test("a photo without faces lists none", () => {
    assert.deepEqual(m.facesAnswer(answer([])), { kind: "faces", faces: [], hash: "h1" });
  });
  test("a device from before: unknown, asked no more", () => {
    assert.deepEqual(m.facesAnswer({ error: true, errorCode: "unknown_payload", payload: undefined }), { kind: "unknown" });
  });
  test("a refusal or another answer: nothing to show", () => {
    assert.deepEqual(m.facesAnswer({ error: true, errorCode: "", payload: undefined }), { kind: "failed" });
    assert.deepEqual(m.facesAnswer({ error: false, errorCode: "", payload: { $case: "respAck", respAck: { ok: true, errorMsg: "", code: "" } } }), { kind: "failed" });
    // The bridge's own answer for an unreachable device.
    assert.deepEqual(m.facesAnswer({ error: true, errorCode: "device_unreachable", payload: undefined }), { kind: "failed" });
  });
});

describe("a face's ×", () => {
  test("names the answer's hash, else the item's, else offers none", () => {
    assert.equal(m.removalHash("h1", "h2"), "h1");
    assert.equal(m.removalHash("", "h2"), "h2");
    assert.equal(m.removalHash("", undefined), "");
    assert.equal(m.removalHash("", ""), "");
  });
  test("takes every face of that person off the list", () => {
    const ana1 = face("p1", "Ana", 10);
    const leo = face("p2", "Leo", 100);
    const ana2 = face("p1", "Ana", 300);
    assert.deepEqual(m.withoutPerson([ana1, leo, ana2], "p1"), [leo]);
    assert.deepEqual(m.withoutPerson([leo], "p1"), [leo]);
    assert.deepEqual(m.withoutPerson([ana1], "p1"), []);
  });
  test("the photo leaves the grid only when it is that person's photos", () => {
    assert.equal(m.leavesGrid("p1", "p1"), true);
    assert.equal(m.leavesGrid("p2", "p1"), false);
    assert.equal(m.leavesGrid(undefined, "p1"), false);
    assert.equal(m.leavesGrid("", "p1"), false);
  });
  test("from the keyboard, the focus goes to the × now in its place, else the last, else none", () => {
    assert.equal(m.focusAfterRemoval(0, 2), 0);
    assert.equal(m.focusAfterRemoval(1, 2), 1);
    assert.equal(m.focusAfterRemoval(2, 2), 1); // the last went
    assert.equal(m.focusAfterRemoval(0, 0), -1); // none left: Info's ×
  });
  test("rows keep their keys when another goes, two faces of one person apart", () => {
    const keys = [face("p1", "Ana", 10), face("p1", "Ana", 300), face("p2", "Leo", 100)].map(m.faceKey);
    assert.equal(new Set(keys).size, 3);
    assert.equal(m.faceKey(face("p2", "Leo", 100)), keys[2]);
  });
});

describe("a face opening a person", () => {
  test("Images already on exactly their photos keeps its grid", () => {
    assert.equal(m.onlyPerson(filter({ personIds: ["p1"] }), "p1"), true);
  });
  test("anything else is a new search for them", () => {
    assert.equal(m.onlyPerson(filter(), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p2"] }), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p1", "p2"] }), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p1"], tags: ["beach"] }), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p1"], group: { id: "g", name: "Trip", fileCount: 3 } }), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p1"], area: { south: 0, west: 0, north: 1, east: 1 } }), "p1"), false);
    assert.equal(m.onlyPerson(filter({ personIds: ["p1"], text: "dog" }), "p1"), false);
  });
});

describe("the note once a face is taken off (as Not this person's)", () => {
  test("in the face's own name, which People may not have listed yet", () => {
    const f = face("p1", "Ana");
    assert.equal(done.personFixText(t, { hashes: new Set(["h1"]), from: { id: f.personId, name: f.name }, moved: false }), "Removed 1 photo from Ana");
  });
  test("an unnamed person's", () => {
    assert.equal(done.personFixText(t, { hashes: new Set(["h1"]), from: { id: "p2", name: "" }, moved: false }), "Removed 1 photo from this person");
  });
  test("the face's tooltip has its English", () => {
    assert.equal(t("web.images.viewer.face_photos"), "Show this person's photos");
  });
});
