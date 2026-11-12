// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for People while Pets is on (release 180, Settings.pets_enabled, on
// by default; face recognition is off by default): a pet the device finds
// is a person in People, so People - the menu item, its page, the people
// in the search, Info's People block, the tour's People step - is there
// while face recognition or Pets is on (src/components/faceRecognition.ts'
// peopleOn), and a device whose Settings lack pets_enabled has People as
// before, with face recognition alone. Loaded through Vite's SSR loader in
// Node, as places-switch-test.mjs, with the socket's request replaced by
// the test's:
//
//   node web/scripts/people-pets-test.mjs        (needs web/node_modules)
//
// The rules are the phones' too: change them together.
import { after, before, beforeEach, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
globalThis.window = {
  localStorage: globalThis.localStorage,
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en"], language: "en" }, configurable: true, writable: true });

// What this browser had last time, before the page loads: face recognition
// off, Pets on.
store.set("otc_face_recognition", "0");
store.set("otc_pets", "1");

let server;
let people, filter, faces, tour, ws;
// What the test's device answers each request with.
let answer = () => ({});
const asked = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-people-pets-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  ws = await server.ssrLoadModule("/src/net/useWS.ts");
  ws.useWS.request = async (build) => {
    const env = {};
    build(env);
    asked.push(env.payload?.$case);
    return answer(env);
  };
  people = await server.ssrLoadModule("/src/components/faceRecognition.ts");
  filter = await server.ssrLoadModule("/src/components/photoFilter.ts");
  faces = await server.ssrLoadModule("/src/components/photoFaces.ts");
  tour = await server.ssrLoadModule("/src/components/tourSteps.ts");
});
after(() => server?.close());
beforeEach(() => { asked.length = 0; });

const settings = (faceRecognitionEnabled, petsEnabled) =>
  ({ payload: { $case: "respSettings", respSettings: { faceRecognitionEnabled, petsEnabled } } });
const until = async (ok) => {
  for (let i = 0; i < 200 && !ok(); i++) await new Promise((r) => setTimeout(r, 5));
};

describe("whether People is there", () => {
  test("while face recognition or Pets is on", () => {
    const cases = [
      // face recognition, Pets: People
      [true, true, true],
      [true, false, true],
      [true, null, true],
      [false, true, true],
      [null, true, true],
      [false, false, false],
      [null, false, null],
      [null, null, null],
      // Pets not known yet counts as off: People waits for the device.
      [false, null, false],
    ];
    for (const [f, p, want] of cases) assert.equal(people.peopleOn(f, p), want, `face recognition ${f}, Pets ${p}`);
  });

  test("what this browser had last time stands in until the device answers", () => {
    assert.equal(people.getFaceRecognition(), false);
    assert.equal(people.getPetsOn(), true);
    assert.equal(people.getPeopleOn(), true);
  });

  test("asked at sign-in - one question for both switches - and remembered", async () => {
    const cases = [
      // face recognition, Pets: People
      [false, true, true],
      [true, false, true],
      [false, false, false],
      [true, true, true],
      // A device before Pets: face recognition alone, as before.
      [false, undefined, false],
      [true, undefined, true],
    ];
    for (const [f, p, want] of cases) {
      answer = () => settings(f, p);
      asked.length = 0;
      const stop = people.watchFaceRecognition();
      await until(() => asked.length > 0 && people.getFaceRecognition() === f && people.getPetsOn() === (p === true));
      await new Promise((r) => setTimeout(r, 5));
      stop();
      const name = `face recognition ${f}, Pets ${p}`;
      assert.deepEqual(asked, ["reqGetSettings"], name);
      assert.equal(people.getPeopleOn(), want, name);
      assert.equal(store.get("otc_face_recognition"), f ? "1" : "0", `${name}: remembered`);
      assert.equal(store.get("otc_pets"), p ? "1" : "0", `${name}: remembered`);
    }
  });
});

describe("the people in the search", () => {
  test("stay while Pets keeps People there, and go with it", () => {
    people.applyPeopleSettings({ faceRecognitionEnabled: true, petsEnabled: true });
    filter.togglePerson("rex");
    assert.deepEqual(filter.getPhotoFilter().personIds, ["rex"]);
    people.setFaceRecognition(false);
    assert.deepEqual(filter.getPhotoFilter().personIds, ["rex"], "face recognition off, Pets on");
    people.setPetsOn(false);
    assert.equal(people.getPeopleOn(), false);
    assert.deepEqual(filter.getPhotoFilter().personIds, [], "both off: People goes");
  });

  test("face recognition off on a device before Pets: they go, as before", () => {
    people.applyPeopleSettings({ faceRecognitionEnabled: true });
    filter.togglePerson("ana");
    people.setFaceRecognition(false);
    assert.deepEqual(filter.getPhotoFilter().personIds, []);
  });

  test("an answer that swaps the switches leaves them: both change at once", () => {
    people.applyPeopleSettings({ faceRecognitionEnabled: true, petsEnabled: false });
    filter.togglePerson("rex");
    assert.deepEqual(filter.getPhotoFilter().personIds, ["rex"]);
    // Face recognition turned off and Pets on, on another app: never a
    // moment without People in between.
    people.applyPeopleSettings({ faceRecognitionEnabled: false, petsEnabled: true });
    assert.equal(people.getPeopleOn(), true);
    assert.deepEqual(filter.getPhotoFilter().personIds, ["rex"]);
  });
});

describe("what follows People", () => {
  test("Info asks for a photo's faces and offers Add while Pets is on, face recognition off", () => {
    const item = { path: "/photos/rex.jpg" };
    assert.equal(faces.asksFaces(item, people.peopleOn(false, true), false), true);
    assert.equal(faces.offersAdd(true, people.peopleOn(false, true), "h1"), true);
    assert.equal(faces.asksFaces(item, people.peopleOn(false, false), false), false);
    assert.equal(faces.offersAdd(true, people.peopleOn(false, false), "h1"), false);
    assert.equal(faces.asksFaces(item, people.peopleOn(null, null), false), true, "not known yet: the device's answer says");
  });

  test("the tour's People step", () => {
    assert.ok(tour.tourSteps(people.peopleOn(false, true), true).includes("people"));
    assert.ok(!tour.tourSteps(people.peopleOn(false, false), true).includes("people"));
    assert.ok(!tour.tourSteps(people.peopleOn(false, undefined), true).includes("people"), "a device before Pets, face recognition off");
  });
});
