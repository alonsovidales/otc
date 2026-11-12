// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Text in photos on the web (release 180,
// Settings.text_in_photos_enabled): when the top bar's search offers
// "Search photos for …" (src/components/smartSearch.ts' offersSearchText:
// Smart search on with its model there, or Text in photos on - the device
// then answers the photos whose text holds the words, alone without Smart
// search), how its switch is kept (asked with Smart search's at sign-in,
// remembered for the next load, set at once by Settings), when the words
// in the search go, and what the viewer's Info shows of a photo's text
// (src/components/photoText.ts). Loaded through Vite's SSR loader in Node,
// as smart-search-test.mjs, with the socket's request replaced by the
// test's:
//
//   node web/scripts/text-in-photos-test.mjs        (needs web/node_modules)
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

// What this browser had last time, before the page loads: Text in photos
// on (Smart search's switch never seen).
store.set("otc_text_in_photos", "1");

let server;
let smart, filter, text, ws;
// What the test's device answers each request with.
let answer = () => ({});
const asked = [];
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-text-in-photos-test-vite"),
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
  smart = await server.ssrLoadModule("/src/components/smartSearch.ts");
  filter = await server.ssrLoadModule("/src/components/photoFilter.ts");
  text = await server.ssrLoadModule("/src/components/photoText.ts");
});
after(() => server?.close());
beforeEach(() => { asked.length = 0; });

const settings = (smartSearchEnabled, smartSearchModel, textInPhotosEnabled) =>
  ({ payload: { $case: "respSettings", respSettings: { smartSearchEnabled, smartSearchModel, textInPhotosEnabled } } });
const model = (m) => ({ ready: false, downloading: false, percent: 0, problem: "", ...m });
const offered = () => smart.offersSearchText(smart.getSmartSearchOn(), smart.getSmartSearchModel(), smart.getTextInPhotosOn());
const until = async (ok) => {
  for (let i = 0; i < 200 && !ok(); i++) await new Promise((r) => setTimeout(r, 5));
};

describe("when the search offers the words", () => {
  test("while Smart search can answer them, or Text in photos is on", () => {
    const ready = smart.cModelReady;
    const downloading = smart.modelFrom(model({ downloading: true, percent: 10 }));
    const neither = smart.modelFrom(model({}));
    const cases = [
      // Smart search, its model, Text in photos: offered
      [true, ready, null, true],
      [true, ready, false, true],
      [true, ready, true, true],
      [true, downloading, true, true],
      [true, downloading, false, false],
      [true, downloading, null, false],
      [true, neither, true, true],
      [false, ready, true, true],
      [false, neither, true, true],
      [false, ready, false, false],
      [false, ready, null, false],
      [null, ready, true, true],
      [null, ready, null, false],
    ];
    for (const [on, m, textOn, want] of cases) {
      assert.equal(smart.offersSearchText(on, m, textOn), want, `Smart search ${on}, ready ${m.ready}, Text in photos ${textOn}`);
    }
  });

  test("Smart search's own part is as before", () => {
    assert.equal(smart.offersSmartSearch(true, smart.cModelReady), true);
    assert.equal(smart.offersSmartSearch(false, smart.cModelReady), false);
    assert.equal(smart.offersSmartSearch(true, smart.modelFrom(model({ downloading: true }))), false);
  });
});

describe("Text in photos' switch", () => {
  test("what this browser had last time stands in until the device answers", () => {
    assert.equal(smart.getTextInPhotosOn(), true);
    assert.equal(smart.getSmartSearchOn(), null);
    assert.equal(offered(), true, "the words are offered at once");
  });

  test("asked with Smart search's at sign-in - one question - and remembered", async () => {
    const cases = [
      // Smart search, its model, Text in photos: Text in photos on, offered
      [true, undefined, true, true, true],
      [false, undefined, true, true, true],
      [true, model({ downloading: true, percent: 30 }), true, true, true],
      [true, model({ downloading: true, percent: 30 }), false, false, false],
      [false, undefined, false, false, false],
      [true, undefined, false, false, true],
      // A device before Text in photos: Smart search's alone, as before.
      [true, undefined, undefined, false, true],
      [false, undefined, undefined, false, false],
    ];
    for (const [on, m, textSaid, wantText, wantOffered] of cases) {
      answer = () => settings(on, m, textSaid);
      asked.length = 0;
      const stop = smart.watchSmartSearch();
      assert.equal(await smart.refreshSmartSearch(), true, "the sign-in's question, shared");
      stop();
      const name = `Smart search ${on}, model ${JSON.stringify(m)}, Text in photos ${textSaid}`;
      assert.deepEqual(asked, ["reqGetSettings"], name);
      assert.equal(smart.getTextInPhotosOn(), wantText, name);
      assert.equal(offered(), wantOffered, name);
      assert.equal(store.get("otc_text_in_photos"), wantText ? "1" : "0", `${name}: remembered for the next load`);
    }
  });

  test("Settings' switch, as the device acknowledged it, at once", () => {
    smart.applySmartSearchSettings({ smartSearchEnabled: false, textInPhotosEnabled: false });
    assert.equal(offered(), false);
    smart.setTextInPhotosOn(true);
    assert.equal(smart.getTextInPhotosOn(), true);
    assert.equal(offered(), true, "Smart search off: the words searched for in the photos' text alone");
    assert.equal(store.get("otc_text_in_photos"), "1");
    smart.setTextInPhotosOn(false);
    assert.equal(offered(), false);
    assert.equal(store.get("otc_text_in_photos"), "0");
  });

  test("an answer asked for before the switch changed doesn't undo it", async () => {
    let reply;
    answer = () => new Promise((r) => { reply = r; });
    smart.setTextInPhotosOn(true);
    const stopWatch = smart.watchSmartSearch();
    await until(() => reply !== undefined);
    smart.setTextInPhotosOn(false);
    // A refresh after the change asks again rather than sharing the old one.
    const first = reply;
    const again = smart.refreshSmartSearch();
    await until(() => reply !== first);
    first(settings(false, undefined, true));
    reply(settings(false, undefined, false));
    assert.equal(await again, true);
    assert.equal(smart.getTextInPhotosOn(), false);
    stopWatch();
  });
});

describe("when the words in the search go", () => {
  test("they stay while Text in photos answers them: Settings opened, or Smart search turned off", () => {
    smart.applySmartSearchSettings({ smartSearchEnabled: false, textInPhotosEnabled: true });
    filter.searchText("GTS-04-R");
    assert.equal(filter.getPhotoFilter().text, "GTS-04-R");
    // Settings asks again and is told the same.
    smart.applySmartSearchSettings({ smartSearchEnabled: false, textInPhotosEnabled: true });
    assert.equal(filter.getPhotoFilter().text, "GTS-04-R");
    smart.setSmartSearchOn(true);
    smart.setSmartSearchOn(false);
    assert.equal(filter.getPhotoFilter().text, "GTS-04-R", "Smart search off: the photos' text still answers them");
  });

  test("they stay while Smart search answers them: Text in photos turned off", () => {
    smart.applySmartSearchSettings({ smartSearchEnabled: true, textInPhotosEnabled: true });
    filter.searchText("kids in the snow");
    smart.setTextInPhotosOn(false);
    assert.equal(filter.getPhotoFilter().text, "kids in the snow");
  });

  test("they go once neither answers them", () => {
    smart.applySmartSearchSettings({ smartSearchEnabled: false, textInPhotosEnabled: true });
    filter.searchText("invoice");
    smart.setTextInPhotosOn(false);
    assert.equal(filter.getPhotoFilter().text, "", "both off");

    smart.applySmartSearchSettings({ smartSearchEnabled: true, textInPhotosEnabled: true, smartSearchModel: model({ downloading: true, percent: 5 }) });
    filter.searchText("invoice");
    assert.equal(filter.getPhotoFilter().text, "invoice", "Text in photos answers them while the model downloads");
    smart.setTextInPhotosOn(false);
    assert.equal(filter.getPhotoFilter().text, "", "Smart search's model not there yet");

    smart.applySmartSearchSettings({ smartSearchEnabled: true, textInPhotosEnabled: true });
    filter.searchText("invoice");
    smart.applySmartSearchSettings({ smartSearchEnabled: false, textInPhotosEnabled: false });
    assert.equal(filter.getPhotoFilter().text, "", "both turned off on another app");
  });

  test("and at the end of a sign-in, as before", async () => {
    answer = () => settings(false, undefined, true);
    const stop = smart.watchSmartSearch();
    await smart.refreshSmartSearch();
    filter.searchText("receipt");
    assert.equal(filter.getPhotoFilter().text, "receipt");
    stop();
    assert.equal(filter.getPhotoFilter().text, "");
  });
});

describe("Info's Text in this photo", () => {
  test("nothing to show: no block", () => {
    for (const none of [undefined, "", " ", "\n", "  \n\t\n  "]) {
      assert.equal(text.photoText(none), "", JSON.stringify(none));
    }
  });

  test("as the device read it, a line a line", () => {
    assert.equal(text.photoText("GTS-04-R"), "GTS-04-R");
    assert.equal(text.photoText("EXIT\nSALIDA\nAUSGANG"), "EXIT\nSALIDA\nAUSGANG");
    assert.equal(text.photoText("Total\n\n12,50 €"), "Total\n\n12,50 €", "a blank line inside stays");
    assert.equal(text.photoText("  indented\nnot"), "  indented\nnot", "spaces on a line stay");
  });

  test("without the blank lines and spaces around it; a Windows line end is a line end", () => {
    assert.equal(text.photoText("\n\n  Menu\nSoup  \n\n"), "  Menu\nSoup");
    assert.equal(text.photoText("Line one\r\nLine two\rLine three"), "Line one\nLine two\nLine three");
  });
});
