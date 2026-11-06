// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for screen mode (src/components/screenMode.ts) and its on-screen
// keyboard's keys (src/components/screenKeyboardLayout.ts): what turns the
// mode on, where a press may go, each language's rows, what a field asks
// for, the capitals at a sentence's start and a long press's accents.
// Loaded through Vite's SSR loader in Node, as the other scripts' tests are:
//
//   node web/scripts/screen-keyboard-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let kb;
let mode;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-screen-keyboard-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  kb = await server.ssrLoadModule("/src/components/screenKeyboardLayout.ts");
  mode = await server.ssrLoadModule("/src/components/screenMode.ts");
});
after(() => server?.close());

const cLangs = ["en", "es", "fr", "de", "it", "pt", "nl"];

const attrs = (over) => ({
  tag: "INPUT", type: "text", inputMode: "", enterHint: "", capitalize: "",
  contentEditable: false, readOnly: false, disabled: false, ...over,
});

const width = (row) => row.reduce((n, key) => n + (key.w ?? 1), 0);
const letters = (row) => row.filter((key) => key.k === "char").map((key) => key.ch).join("");

describe("screen mode", () => {
  test("?screen=1 turns it on, ?screen=0 off, and the tab keeps it", () => {
    assert.equal(mode.screenModeFrom("?screen=1", null), true);
    assert.equal(mode.screenModeFrom("?x=2&screen=1", null), true);
    assert.equal(mode.screenModeFrom("", "1"), true);
    assert.equal(mode.screenModeFrom("?screen=0", "1"), false);
    assert.equal(mode.screenModeFrom("", null), false);
    assert.equal(mode.screenModeFrom("?screen=yes", null), false);
  });

  test("Node has no window: off", () => {
    assert.equal(mode.screenMode(), false);
  });

  test("only the app's own pages are followed; nothing opens a window", () => {
    const o = "http://127.0.0.1:8080";
    const tab = { newWindow: false, download: false };
    const win = { newWindow: true, download: false };
    assert.equal(mode.screenNavigation("/#people", o, tab), "follow");
    assert.equal(mode.screenNavigation("http://127.0.0.1:8080/?screen=1", o, tab), "follow");
    assert.equal(mode.screenNavigation("/shared#abc.def", o, win), "here");
    assert.equal(mode.screenNavigation("/", o, win), "here");
    assert.equal(mode.screenNavigation("https://off-the.cloud/privacy", o, win), "elsewhere");
    assert.equal(mode.screenNavigation("https://off-the.cloud/privacy", o, tab), "elsewhere");
    assert.equal(mode.screenNavigation("http://otc.local:8080/shared#x", o, win), "elsewhere");
    assert.equal(mode.screenNavigation("mailto:someone@example.org", o, tab), "elsewhere");
    assert.equal(mode.screenNavigation("/media/abc?download=1", o, win), "download");
    assert.equal(mode.screenNavigation("/media/abc", o, tab), "download");
    assert.equal(mode.screenNavigation("/", o, { newWindow: false, download: true }), "download");
    assert.equal(mode.screenNavigation("blob:http://127.0.0.1:8080/1234", o, tab), "download");
    assert.equal(mode.screenNavigation("data:text/plain,hi", o, tab), "download");
  });
});

describe("keyboard rows", () => {
  for (const lang of cLangs) {
    test(`${lang}: every row of every layer is the layer's width`, () => {
      for (const layer of ["letters", "symbols", "more", "digits"]) {
        for (const kind of ["text", "email", "url", "number", "tel", "password", "search"]) {
          const field = { kind, multiline: false, enterHint: "", capitalize: "" };
          const rows = kb.keyboardRows(lang, layer, field);
          for (const row of rows) {
            assert.equal(width(row), kb.layerUnits(lang, layer), `${lang} ${layer} ${kind}: ${JSON.stringify(row)}`);
          }
          // Every layer has Enter and a way to put the keyboard away, and
          // every key on a layer is a key once.
          const flat = rows.flat();
          assert.ok(flat.some((key) => key.k === "enter"), `${lang} ${layer} has Enter`);
          assert.ok(flat.some((key) => key.k === "hide"), `${lang} ${layer} has Hide`);
          assert.ok(flat.some((key) => key.k === "back"), `${lang} ${layer} has Backspace`);
          const typed = flat.filter((key) => key.k === "char").map((key) => key.ch);
          assert.equal(new Set(typed).size, typed.length, `${lang} ${layer} ${kind}: a key twice`);
        }
      }
    });
  }

  const text = { kind: "text", multiline: false, enterHint: "", capitalize: "" };
  test("the letters follow the language", () => {
    const top = (lang) => kb.keyboardRows(lang, "letters", text).map(letters);
    assert.deepEqual(top("en").slice(0, 3), ["qwertyuiop", "asdfghjkl", "zxcvbnm"]);
    assert.deepEqual(top("nl").slice(0, 3), ["qwertyuiop", "asdfghjkl", "zxcvbnm"]);
    assert.deepEqual(top("it").slice(0, 3), ["qwertyuiop", "asdfghjkl", "zxcvbnm"]);
    assert.deepEqual(top("es").slice(0, 3), ["qwertyuiop", "asdfghjklñ", "zxcvbnm"]);
    assert.deepEqual(top("pt").slice(0, 3), ["qwertyuiop", "asdfghjklç", "zxcvbnm"]);
    assert.deepEqual(top("fr").slice(0, 3), ["azertyuiop", "qsdfghjklm", "wxcvbn'"]);
    assert.deepEqual(top("de").slice(0, 3), ["qwertzuiopü", "asdfghjklöä", "yxcvbnm"]);
  });

  test("every Latin letter is on the letters in every language", () => {
    for (const lang of cLangs) {
      const all = kb.keyboardRows(lang, "letters", text).map(letters).join("");
      for (const ch of "abcdefghijklmnopqrstuvwxyz") assert.ok(all.includes(ch), `${lang} lacks ${ch}`);
    }
  });

  test("an address gets its mark on the letters, digits on their own pad", () => {
    const bottom = (kind) => letters(kb.keyboardRows("en", "letters", { ...text, kind })[3]);
    assert.equal(bottom("text"), ",.");
    assert.equal(bottom("email"), "@.");
    assert.equal(bottom("url"), "/.");
    const pad = (kind) => kb.keyboardRows("en", "digits", { ...text, kind }).map(letters);
    assert.deepEqual(pad("number"), ["123", "456-", "789.", "0"]);
    assert.deepEqual(pad("tel"), ["123", "456+", "789", "0"]);
  });
});

describe("fields", () => {
  test("what a field is", () => {
    assert.deepEqual(kb.fieldFrom(attrs({})), { kind: "text", multiline: false, enterHint: "", capitalize: "" });
    assert.equal(kb.fieldFrom(attrs({ type: "password" })).kind, "password");
    assert.equal(kb.fieldFrom(attrs({ type: "email" })).kind, "email");
    assert.equal(kb.fieldFrom(attrs({ type: "number" })).kind, "number");
    assert.equal(kb.fieldFrom(attrs({ inputMode: "numeric" })).kind, "number");
    assert.equal(kb.fieldFrom(attrs({ inputMode: "decimal" })).kind, "number");
    assert.equal(kb.fieldFrom(attrs({ type: "password", inputMode: "numeric" })).kind, "password");
    assert.equal(kb.fieldFrom(attrs({ type: "search", enterHint: "Search" })).enterHint, "search");
    assert.deepEqual(kb.fieldFrom(attrs({ tag: "TEXTAREA", type: "" })), { kind: "text", multiline: true, enterHint: "", capitalize: "" });
    assert.equal(kb.fieldFrom(attrs({ tag: "DIV", type: "", contentEditable: true })).multiline, true);
    assert.equal(kb.fieldFrom(attrs({ tag: "DIV", type: "" })), null);
    for (const type of ["checkbox", "radio", "file", "date", "range", "color", "submit", "button", "hidden"]) {
      assert.equal(kb.fieldFrom(attrs({ type })), null, type);
    }
    assert.equal(kb.fieldFrom(attrs({ readOnly: true })), null);
    assert.equal(kb.fieldFrom(attrs({ disabled: true })), null);
    assert.equal(kb.fieldFrom(attrs({ inputMode: "none" })), null);
    assert.equal(kb.firstLayer(kb.fieldFrom(attrs({ type: "tel" }))), "digits");
    assert.equal(kb.firstLayer(kb.fieldFrom(attrs({ type: "email" }))), "letters");
  });

  test("capitals where a sentence starts, in text a person writes only", () => {
    const f = (over) => kb.fieldFrom(attrs(over));
    const area = f({ tag: "TEXTAREA", type: "" });
    assert.equal(kb.startsCapital(area, ""), true);
    assert.equal(kb.startsCapital(area, "Hello"), false);
    assert.equal(kb.startsCapital(area, "Hello. "), true);
    assert.equal(kb.startsCapital(area, "Really? "), true);
    assert.equal(kb.startsCapital(area, "Hello "), false);
    assert.equal(kb.startsCapital(area, "Line one\n"), true);
    assert.equal(kb.startsCapital(f({ tag: "DIV", type: "", contentEditable: true }), ""), true);
    // A one-line field only when it asks: a name, a code or a port doesn't.
    assert.equal(kb.startsCapital(f({}), ""), false);
    assert.equal(kb.startsCapital(f({ capitalize: "sentences" }), ""), true);
    assert.equal(kb.startsCapital(f({ capitalize: "sentences" }), "Hi. "), true);
    assert.equal(kb.startsCapital(f({ capitalize: "on" }), ""), true);
    assert.equal(kb.startsCapital(f({ capitalize: "words" }), "Ana "), true);
    assert.equal(kb.startsCapital(f({ capitalize: "characters" }), "AB"), true);
    assert.equal(kb.startsCapital(f({ capitalize: "none" }), ""), false);
    assert.equal(kb.startsCapital(f({ capitalize: "off" }), ""), false);
    for (const type of ["password", "email", "url", "search", "number", "tel"]) {
      assert.equal(kb.startsCapital(f({ type }), ""), false, type);
    }
  });
});

describe("accents", () => {
  test("the language's own first", () => {
    assert.deepEqual(kb.accentsOf("fr", "e").slice(0, 4), ["é", "è", "ê", "ë"]);
    assert.deepEqual(kb.accentsOf("es", "n"), ["ñ", "ń"]);
    assert.equal(kb.accentsOf("pt", "a")[0], "á");
    assert.equal(kb.accentsOf("pt", "a")[3], "ã");
    assert.equal(kb.accentsOf("de", "s")[0], "ß");
    assert.equal(kb.accentsOf("it", "o")[0], "ò");
    assert.equal(kb.accentsOf("en", "q").length, 0);
    for (const lang of cLangs) {
      for (const base of "acdeilnosuyz") {
        const list = kb.accentsOf(lang, base);
        assert.equal(new Set(list).size, list.length, `${lang} ${base}: twice`);
      }
    }
  });

  test("Shift's capitals, ß left as it is", () => {
    assert.equal(kb.upperOf("é", "fr"), "É");
    assert.equal(kb.upperOf("ñ", "es"), "Ñ");
    assert.equal(kb.upperOf("ß", "de"), "ß");
    assert.equal(kb.upperOf("i", "en"), "I");
    assert.equal(kb.hasCase("a"), true);
    assert.equal(kb.hasCase("1"), false);
    assert.equal(kb.hasCase("@"), false);
  });
});
