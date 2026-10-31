// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for a person's face chosen by the owner (src/components/personCover.ts):
// which photo Use this face takes, the picture the device takes, the
// request and what its answer means. Loaded through Vite's SSR loader in
// Node as person-fix-test.mjs does:
//
//   node web/scripts/person-cover-test.mjs        (needs web/node_modules)
//
// The phones offer the same (app.photos.person.cover_*).
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
    cacheDir: join(tmpdir(), "otc-person-cover-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Types only: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/personCover.ts");
});
after(() => server?.close());

// English as the page writes it: a small t over the generated catalog.
const en = JSON.parse(readFileSync(join(WEB, "src/i18n/locales/en/web.images.json"), "utf8"));
const t = (key) => {
  const text = en[key];
  assert.ok(text !== undefined, `no English for ${key}`);
  return text;
};

const photo = (hash, mime = "image/jpeg") => ({ path: `/p/${hash}.jpg`, mime, hash });

describe("Use this face", () => {
  test("is offered for exactly one photo, by its content hash", () => {
    assert.equal(m.faceHash([photo("a")]), "a");
    assert.equal(m.faceHash([photo("a", "image/heic")]), "a");
    assert.equal(m.faceHash([{ hash: "a" }]), "a"); // no mime: a photo
  });
  test("is not offered for none or several", () => {
    assert.equal(m.faceHash([]), null);
    assert.equal(m.faceHash([photo("a"), photo("b")]), null);
    // Two tiles of the same photo are still two picked.
    assert.equal(m.faceHash([photo("a"), photo("a")]), null);
  });
  test("is not offered for a video, or a photo without a hash", () => {
    assert.equal(m.faceHash([photo("v", "video/mp4")]), null);
    assert.equal(m.faceHash([photo("v", "video/quicktime")]), null);
    assert.equal(m.faceHash([{ path: "/p/x.jpg", mime: "image/jpeg" }]), null);
    assert.equal(m.faceHash([photo("")]), null);
  });
});

describe("a picture of the owner's own", () => {
  const jpeg = (size) => {
    const b = new Uint8Array(size);
    b.set([0xff, 0xd8, 0xff, 0xe0]);
    return b;
  };
  test("goes when it is a JPEG of at most 256 KB", () => {
    assert.equal(m.cCoverMaxBytes, 256 * 1024);
    assert.equal(m.coverImageFits(jpeg(40 * 1024)), true);
    assert.equal(m.coverImageFits(jpeg(256 * 1024)), true);
  });
  test("doesn't when it is bigger, empty or not a JPEG", () => {
    assert.equal(m.coverImageFits(jpeg(256 * 1024 + 1)), false);
    assert.equal(m.coverImageFits(new Uint8Array(0)), false);
    assert.equal(m.coverImageFits(new Uint8Array([0xff, 0xd8])), false);
    const png = new Uint8Array(1024);
    png.set([0x89, 0x50, 0x4e, 0x47]);
    assert.equal(m.coverImageFits(png), false);
  });
});

describe("the request", () => {
  test("carries the photo, the picture or neither", () => {
    assert.deepEqual(m.coverPayload("p1", { $case: "hash", hash: "a" }),
      { $case: "reqSetPersonCover", reqSetPersonCover: { personId: "p1", cover: { $case: "hash", hash: "a" } } });
    const image = new Uint8Array([0xff, 0xd8, 0xff]);
    assert.deepEqual(m.coverPayload("p1", { $case: "image", image }),
      { $case: "reqSetPersonCover", reqSetPersonCover: { personId: "p1", cover: { $case: "image", image } } });
    // Choose automatically: no cover at all.
    assert.deepEqual(m.coverPayload("p1", undefined),
      { $case: "reqSetPersonCover", reqSetPersonCover: { personId: "p1", cover: undefined } });
  });
});

describe("what the device answered", () => {
  const ack = (ok, errorMsg = "") => ({ $case: "respAck", respAck: { ok, errorMsg, code: "" } });
  test("an ok Ack is done", () => {
    assert.equal(m.coverOutcome({ error: false, errorMessage: "", payload: ack(true) }), null);
  });
  test("a refusal says the device's words", () => {
    const said = "That photo has no face of this person.";
    assert.equal(m.coverOutcome({ error: true, errorMessage: said, payload: undefined })(t), said);
    // A demo device's refusal (ws.ts answers it unsent) has both.
    assert.equal(m.coverOutcome({ error: true, errorMessage: "Not on the demo.", payload: ack(false, "Not on the demo.") })(t), "Not on the demo.");
    // An Ack that isn't ok, with its words there only.
    assert.equal(m.coverOutcome({ error: false, errorMessage: "", payload: ack(false, "No.") })(t), "No.");
  });
  test("one without words says ours", () => {
    assert.equal(m.coverOutcome({ error: true, errorMessage: "", payload: undefined })(t), "Couldn't change the face. Try again.");
    assert.equal(m.coverOutcome({ error: false, errorMessage: "", payload: ack(false) })(t), "Couldn't change the face. Try again.");
    // Anything but an Ack is no answer to this.
    assert.equal(m.coverOutcome({ error: false, errorMessage: "", payload: { $case: "respPeople", respPeople: { people: [] } } })(t),
      "Couldn't change the face. Try again.");
  });
  test("an ok Ack on an error envelope is not done", () => {
    assert.equal(m.coverOutcome({ error: true, errorMessage: "", payload: ack(true) })(t), "Couldn't change the face. Try again.");
  });
});
