// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for what Images says after Not this person and Someone else…
// (src/components/personFixDone.ts), loaded through Vite's SSR loader in
// Node as photo-copies-test.mjs does:
//
//   node web/scripts/person-fix-test.mjs        (needs web/node_modules)
//
// The phones say the same (app.photos.person.*).
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
    cacheDir: join(tmpdir(), "otc-person-fix-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Types only: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/personFixDone.ts");
});
after(() => server?.close());

// English as the page writes it: a small t over the generated catalog.
const en = JSON.parse(readFileSync(join(WEB, "src/i18n/locales/en/web.images.json"), "utf8"));
// A name is isolated (FSI ... PDI) as the page shows it; left out here.
const t = (key, args = {}) => {
  let text = en[key];
  assert.ok(text !== undefined, `no English for ${key}`);
  if (typeof text === "object") text = args[text.arg] === 1 ? text.one : text.other;
  return text.replace(/\{(\w+)\}/g, (_, a) => String(args[a])).replace(/[\u2068\u2069]/g, "");
};

const ana = { id: "p1", name: "Ana", faceCount: 4 };
const nameless = { id: "p2", name: "  ", faceCount: 2 };

describe("the photos a selection names", () => {
  test("each content once, none without a hash", () => {
    assert.deepEqual(m.uniqueHashes([{ hash: "a" }, { hash: "b" }, { hash: "a" }, { hash: "" }, {}]), ["a", "b"]);
    assert.deepEqual(m.uniqueHashes([]), []);
  });
});

describe("what Images says once done (as the phones)", () => {
  test("taken off a named person", () => {
    assert.equal(m.personFixText(t, { hashes: new Set(["a"]), from: ana, moved: false }), "Removed 1 photo from Ana");
    assert.equal(m.personFixText(t, { hashes: new Set(["a", "b", "c"]), from: ana, moved: false }), "Removed 3 photos from Ana");
  });
  test("taken off someone without a name, or not in the list", () => {
    assert.equal(m.personFixText(t, { hashes: new Set(["a", "b"]), from: nameless, moved: false }), "Removed 2 photos from this person");
    assert.equal(m.personFixText(t, { hashes: new Set(["a"]), moved: false }), "Removed 1 photo from this person");
  });
  test("moved to someone else, named or not", () => {
    assert.equal(m.personFixText(t, { hashes: new Set(["a", "b"]), from: nameless, to: ana, moved: true }), "Moved 2 photos to Ana");
    assert.equal(m.personFixText(t, { hashes: new Set(["a"]), from: ana, to: nameless, moved: true }), "Moved 1 photo to an unnamed person");
  });
});
