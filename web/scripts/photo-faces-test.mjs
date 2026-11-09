// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a photo's faces in the viewer's Info (src/components/
// photoFaces.ts): when Info asks GetPhotoFaces, what its answer means, what
// a face's × takes off and where the focus goes after it, and when a face
// leaves the grid or opens a person Images already shows - plus the note
// once a face is taken off (personFixDone.ts); and Add a person (AddToPerson,
// 2026-11-09): when Info offers it, whom it offers, what it sends and what
// the device's Ack means, and a by-hand row's ×. Loaded through Vite's SSR
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
const common = JSON.parse(readFileSync(join(WEB, "src/i18n/locales/en/common.json"), "utf8"));
// A name is isolated (FSI ... PDI) as the page shows it; left out here.
const t = (key, args = {}, catalog = en) => {
  let text = catalog[key];
  assert.ok(text !== undefined, `no English for ${key}`);
  if (typeof text === "object") text = args[text.arg] === 1 ? text.one : text.other;
  return text.replace(/\{(\w+)\}/g, (_, a) => String(args[a])).replace(/[⁨⁩]/g, "");
};

const face = (personId, name, x = 10, extra = {}) => ({ personId, name, thumbnail: new Uint8Array([0xff, 0xd8, 0xff]), x, y: 20, w: 30, h: 30, ...extra });
const byHand = (personId, name) => ({ personId, name, thumbnail: new Uint8Array(0), x: 0, y: 0, w: 0, h: 0, byHand: true });
const answer = (faces, faceRecognition = true, hash = "h1", addsPeople = false) => ({
  error: false, errorCode: "", payload: { $case: "respPhotoFaces", respPhotoFaces: { faces, faceRecognition, hash, addsPeople } },
});
const ack = (ok, over = {}) => ({ id: 1, error: !ok, errorCode: "", errorMessage: "", payload: { $case: "respAck", respAck: { ok, errorMsg: "", code: "" } }, ...over });
const person = (id, name = "") => ({ id, name, faceCount: 3, thumbnail: new Uint8Array(0) });
const filter = (over = {}) => ({ tags: [], personIds: [], group: null, area: null, text: "", ...over });

describe("when Info asks for a photo's faces", () => {
  test("a photo, while face recognition is on or not known yet", () => {
    assert.equal(m.asksFaces({ mime: "image/jpeg" }, true, false), true);
    assert.equal(m.asksFaces({ mime: "image/heic" }, null, false), true);
    assert.equal(m.asksFaces({}, true, false), true); // no mime: a photo
  });
  test("a video too: its people added by hand (a device from before answers none)", () => {
    assert.equal(m.asksFaces({ mime: "video/mp4" }, true, false), true);
    assert.equal(m.asksFaces({ mime: "video/quicktime" }, null, false), true);
    assert.deepEqual(m.facesAnswer(answer([])), { kind: "faces", faces: [], hash: "h1", addsPeople: false });
  });
  test("never with face recognition off", () => {
    assert.equal(m.asksFaces({ mime: "image/jpeg" }, false, false), false);
    assert.equal(m.asksFaces({ mime: "video/mp4" }, false, false), false);
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
    assert.deepEqual(m.facesAnswer(answer(faces)), { kind: "faces", faces, hash: "h1", addsPeople: false });
  });
  test("the people added by hand after them, as the device sent them", () => {
    const faces = [face("p1", "Ana", 10), byHand("p3", "Leo"), byHand("p2", "")];
    assert.deepEqual(m.facesAnswer(answer(faces, true, "h1", true)), { kind: "faces", faces, hash: "h1", addsPeople: true });
  });
  test("none at all while face recognition is off, whatever came - nor Add", () => {
    assert.deepEqual(m.facesAnswer(answer([face("p1", "Ana")], false)), { kind: "faces", faces: [], hash: "h1", addsPeople: false });
    assert.deepEqual(m.facesAnswer(answer([byHand("p1", "Ana")], false, "h1", true)), { kind: "faces", faces: [], hash: "h1", addsPeople: false });
  });
  test("a photo without faces lists none", () => {
    assert.deepEqual(m.facesAnswer(answer([])), { kind: "faces", faces: [], hash: "h1", addsPeople: false });
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
    const keys = [face("p1", "Ana", 10), face("p1", "Ana", 300), face("p2", "Leo", 100), byHand("p3", "Eva")].map(m.faceKey);
    assert.equal(new Set(keys).size, 4);
    assert.equal(m.faceKey(face("p2", "Leo", 100)), keys[2]);
  });
});

describe("Info's People and its Add a person", () => {
  test("Add shows where the device takes AddToPerson and the content's hash is known", () => {
    assert.equal(m.offersAdd(true, true, "h1"), true);
    assert.equal(m.offersAdd(true, null, "h1"), true); // not known here yet: the device's answer says
  });
  test("no Add on a device from before, with face recognition off, or with no hash", () => {
    assert.equal(m.offersAdd(false, true, "h1"), false);
    assert.equal(m.offersAdd(true, false, "h1"), false);
    assert.equal(m.offersAdd(true, true, ""), false);
    assert.equal(m.offersAdd(true, true, m.removalHash("", undefined)), false);
  });
  test("the block shows with faces, or with Add alone (a photo nobody was found in, a video)", () => {
    assert.equal(m.showsPeople([face("p1", "Ana")], false), true);
    assert.equal(m.showsPeople([], true), true);
    assert.equal(m.showsPeople([byHand("p1", "Ana")], false), true);
    assert.equal(m.showsPeople([], false), false); // an old device's video, as before
  });
  test("offers People's people but everyone in it already, faces and by hand, in People's order", () => {
    const people = [person("p1", "Ana"), person("p2", "Leo"), person("p3"), person("p4", "Eva")];
    assert.deepEqual(m.notInIt(people, [face("p1", "Ana"), byHand("p3", "")]).map((p) => p.id), ["p2", "p4"]);
    assert.deepEqual(m.notInIt(people, []).map((p) => p.id), ["p1", "p2", "p3", "p4"]);
    // One person's two faces leave them out once.
    assert.deepEqual(m.notInIt(people, [face("p2", "Leo", 10), face("p2", "Leo", 300)]).map((p) => p.id), ["p1", "p3", "p4"]);
    assert.deepEqual(m.notInIt(people, people.map((p) => byHand(p.id, p.name))), []);
  });
  test("an empty list says People has no one, or that everyone is in it", () => {
    assert.equal(m.addNone(0), "none");
    assert.equal(m.addNone(3), "all");
    assert.equal(t("web.images.viewer.face_add_none"), "No one has been found yet.");
    assert.equal(t("web.images.viewer.face_add_all"), "Everyone found so far is in it already.");
  });
  test("the list's title names a photo or a video", () => {
    assert.equal(t("web.images.viewer.face_add_title_photo"), "Who is in this photo?");
    assert.equal(t("web.images.viewer.face_add_title_video"), "Who is in this video?");
    assert.equal(t("web.images.viewer.face_add"), "Add a person");
  });
  test("a pick puts that person in this one content", () => {
    assert.deepEqual(m.addPayload("p2", "h1"), { $case: "reqAddToPerson", reqAddToPerson: { personId: "p2", hashes: ["h1"] } });
  });
  test("once added, the focus goes to their row (the last of theirs), else to Add", () => {
    const after = [face("p1", "Ana", 10), byHand("p3", "Eva"), byHand("p2", "Leo")];
    assert.equal(m.addedAt(after, "p2"), 2);
    assert.equal(m.addedAt(after, "p1"), 0);
    assert.equal(m.addedAt([face("p1", "Ana", 10), face("p1", "Ana", 300)], "p1"), 1);
    assert.equal(m.addedAt(after, "p9"), -1);
    assert.equal(m.addedAt([], "p2"), -1);
  });
});

describe("what the device's Ack means (Add, and a by-hand row's ×)", () => {
  test("done", () => {
    assert.deepEqual(m.ackOutcome(ack(true)), { kind: "done" });
  });
  test("a refusal in the device's words, from the envelope or the Ack", () => {
    assert.deepEqual(m.ackOutcome(ack(false, { errorMessage: "Diese Person gibt es nicht mehr." })), { kind: "refused", said: "Diese Person gibt es nicht mehr." });
    const inAck = ack(false);
    inAck.error = false;
    inAck.payload.respAck.errorMsg = "Face recognition is off.";
    assert.deepEqual(m.ackOutcome(inAck), { kind: "refused", said: "Face recognition is off." });
  });
  test("a refusal with no words, or a device that doesn't know the request: ours", () => {
    assert.deepEqual(m.ackOutcome(ack(false)), { kind: "failed" });
    assert.deepEqual(m.ackOutcome({ id: 1, error: true, errorCode: "unknown_payload", errorMessage: "Unknown request", payload: undefined }), { kind: "failed" });
    assert.deepEqual(m.ackOutcome({ id: 1, error: false, errorCode: "", errorMessage: "", payload: undefined }), { kind: "failed" });
    assert.equal(t("web.images.viewer.face_add_failed"), "Couldn't add this person. Try again.");
  });
  test("the bridge's answer for a device it can't reach: unreachable, whatever its words", () => {
    const relay = ack(false);
    relay.payload.respAck.code = "device_unreachable";
    assert.deepEqual(m.ackOutcome(relay), { kind: "unreachable" });
    relay.payload.respAck.code = "account_disabled";
    assert.deepEqual(m.ackOutcome(relay), { kind: "unreachable" });
  });
  test("a by-hand row's × takes them off this content, to no one", () => {
    assert.deepEqual(m.byHandRemovalPayload("p3", "h1"), { $case: "reqRemoveFromPerson", reqRemoveFromPerson: { personId: "p3", hashes: ["h1"], toPersonId: "" } });
    assert.deepEqual(m.withoutPerson([face("p1", "Ana"), byHand("p3", "Eva")], "p3"), [face("p1", "Ana")]);
    assert.equal(t("common.remove", {}, common), "Remove");
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
  test("a person added by hand taken off a video: the video, named or not", () => {
    const f = byHand("p3", "Eva");
    assert.equal(done.personFixText(t, { hashes: new Set(["h1"]), from: { id: f.personId, name: f.name }, moved: false, video: true }), "Removed this video from Eva");
    assert.equal(done.personFixText(t, { hashes: new Set(["h1"]), from: { id: "p2", name: "" }, moved: false, video: true }), "Removed this video from this person");
    // Off a photo, as a face's.
    assert.equal(done.personFixText(t, { hashes: new Set(["h1"]), from: { id: f.personId, name: f.name }, moved: false, video: false }), "Removed 1 photo from Eva");
  });
  test("the face's tooltip has its English", () => {
    assert.equal(t("web.images.viewer.face_photos"), "Show this person's photos");
  });
});
