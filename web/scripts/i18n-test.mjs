// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the web app's localization runtime (src/i18n) and the lang line
// in src/net/ws.ts. The web app has no test runner, so this loads the real
// modules through Vite's SSR loader (import.meta.glob included) in Node,
// with a fake WebSocket, localStorage and navigator:
//
//   node web/scripts/i18n-test.mjs        (needs web/node_modules)
//
// It passes on the committed tree (English only) and on a scratch copy
// built with make i18n DRAFT=1, where it also checks a real choice.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// The browser this runs in: British English, nothing stored.
const store = new Map();
globalThis.window = {
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  },
  addEventListener() {},
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (t) => clearTimeout(t),
};
Object.defineProperty(globalThis, "navigator", { value: { languages: ["en-GB", "en"], language: "en-GB" }, configurable: true, writable: true });

// A socket that opens at once and keeps what is sent.
const sent = [];
class FakeWebSocket {
  static OPEN = 1;
  readyState = 1;
  constructor(url) {
    this.url = url;
    setTimeout(() => this.onopen?.(), 0);
  }
  send(bytes) { sent.push(bytes); }
  close() { this.readyState = 3; this.onclose?.(); }
}
globalThis.WebSocket = FakeWebSocket;

let server;
let m; // the modules under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-i18n-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
  });
  const load = (p) => server.ssrLoadModule(p);
  m = {
    proto: await load("/src/proto/messages.ts"),
    messages: await load("/src/i18n/messages.ts"),
    match: await load("/src/i18n/match.ts"),
    choice: await load("/src/i18n/choice.ts"),
    runtime: await load("/src/i18n/runtime.ts"),
    format: await load("/src/i18n/format.ts"),
    ws: await load("/src/net/ws.ts"),
  };
});
after(() => server?.close());

// A language table like the one DRAFT builds have, whatever this build ships.
const L = (code, tag, name, status = "draft") => ({ code, tag, name, status });
const table = [L("en", "en", "English", "shipping"), L("es", "es", "Español"), L("fr", "fr", "Français"), L("de", "de", "Deutsch"),
  L("pt", "pt-PT", "Português"), L("qps", "en-XA", "Pseudo")];

describe("ws.ts sets lang after the request's build", () => {
  const lastSent = () => m.proto.ReqEnvelope.decode(sent.at(-1));

  test("on every request, whatever the build closure did to the envelope", async () => {
    const { wsClient } = m.ws;
    await wsClient.connect("wss://test.invalid/ws");
    const want = m.choice.requestLanguage();
    assert.match(want, /^[a-z]{2,3}$/);

    // A closure that only sets the payload.
    void wsClient.request((e) => { e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; });
    assert.equal(lastSent().lang, want);
    assert.equal(lastSent().payload?.$case, "reqGetStatus");

    // One that replaces every field of the envelope (lang "" included),
    // the shape of the iOS and macOS closures that assign a whole envelope.
    void wsClient.request((e) => {
      Object.assign(e, m.proto.ReqEnvelope.fromPartial({ id: e.id, payload: { $case: "reqGetSettings", reqGetSettings: {} } }));
    });
    assert.equal(lastSent().lang, want);
    assert.equal(lastSent().payload?.$case, "reqGetSettings");

    // One that sets a lang of its own: the effective language still wins.
    void wsClient.request((e) => { e.lang = "zz"; e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; });
    assert.equal(lastSent().lang, want);
  });

  test("follows a change of language at once (DRAFT builds)", async (t) => {
    const es = m.messages.languages.find((l) => l.code === "es");
    if (!es) return t.skip("this build ships English only");
    const { wsClient } = m.ws;
    void m.choice.languageChoice.choose("es"); // no sender in this test: stays pending
    void wsClient.request((e) => { e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; });
    assert.equal(lastSent().lang, "es");
    void m.choice.languageChoice.choose("");
    void wsClient.request((e) => { e.payload = { $case: "reqGetStatus", reqGetStatus: {} }; });
    assert.equal(lastSent().lang, "en"); // en-GB matches English
  });
});

describe("matching", () => {
  test("system languages by base language, never the pseudo-locale", () => {
    const { matchLanguage } = m.match;
    assert.equal(matchLanguage(["es-MX"], table), "es");
    assert.equal(matchLanguage(["pt-BR"], table), "pt");
    assert.equal(matchLanguage(["ca-ES", "es-ES"], table), "es");
    assert.equal(matchLanguage(["nl-BE"], table), null);
    assert.equal(matchLanguage(["en-XA"], table), "en");
    assert.equal(matchLanguage([], table), null);
  });

  test("formatters take the system's full locale for the same base language", () => {
    const { formatLocale } = m.match;
    assert.equal(formatLocale(table[1], ["es-MX"]), "es-MX");
    assert.equal(formatLocale(table[4], ["pt-BR"]), "pt-BR");
    assert.equal(formatLocale(table[1], ["de-DE"]), "es");
    assert.equal(formatLocale(table[0], ["fr-FR", "en-GB"]), "en-GB");
  });
});

// A LanguageChoice over the table above, with a clock, storage and sender of the test's own.
function harness({ system = ["en-US"], stored = {} } = {}) {
  const storage = new Map(Object.entries(stored));
  const h = {
    now: 1_000_000,
    sends: [],
    answers: [],
    refetches: 0,
    storage,
  };
  h.choice = new m.choice.LanguageChoice({
    storage: { get: (k) => (storage.has(k) ? storage.get(k) : null), set: (k, v) => storage.set(k, v), remove: (k) => storage.delete(k) },
    now: () => h.now,
    systemLanguages: () => system,
    languages: table,
  });
  h.choice.connect(async (language, expected) => {
    h.sends.push({ language, expected });
    return h.answers.shift() ?? "ok";
  }, () => { h.refetches++; });
  return h;
}

describe("the stored choice", () => {
  test("effective language: choice, else the system's, else English", () => {
    assert.equal(harness({ system: ["es-MX"] }).choice.snapshot().effective, "es");
    assert.equal(harness({ system: ["sv-SE"] }).choice.snapshot().effective, "en");
    const h = harness({ system: ["es-MX"], stored: { otc_language: "de" } });
    assert.equal(h.choice.snapshot().effective, "de");
    assert.equal(h.choice.snapshot().automatic, "es");
    // A code this build doesn't ship: English on screen, still the choice.
    const sv = harness({ stored: { otc_language: "sv" } });
    assert.equal(sv.choice.snapshot().effective, "en");
    assert.equal(sv.choice.snapshot().choice, "sv");
    // Garbage in storage is Automatic.
    assert.equal(harness({ stored: { otc_language: "<b>" } }).choice.snapshot().choice, "");
  });

  test("a change: local copy, pending, SetLanguage with what the device had", async () => {
    const h = harness();
    h.choice.noteDevice("fr");
    assert.equal(h.choice.snapshot().effective, "fr"); // adopted
    const p = h.choice.choose("de");
    assert.equal(h.choice.snapshot().effective, "de"); // at once
    assert.equal(h.storage.get("otc_language"), "de");
    assert.ok(h.storage.has("otc_language_pending"));
    assert.equal(await p, "ok");
    assert.deepEqual(h.sends, [{ language: "de", expected: "fr" }]);
    assert.equal(h.choice.snapshot().pending, false);
    assert.ok(!h.storage.has("otc_language_pending"));
  });

  test("expected is left out when no device value was ever seen", async () => {
    const h = harness();
    h.choice.noteDevice(undefined); // a device from before localization
    await h.choice.choose("es");
    assert.deepEqual(h.sends, [{ language: "es", expected: undefined }]);
  });

  test("changed: the device's value wins, read again", async () => {
    const h = harness();
    h.choice.noteDevice("");
    h.answers.push("changed");
    assert.equal(await h.choice.choose("es"), "changed");
    assert.equal(h.refetches, 1);
    assert.equal(h.choice.snapshot().pending, false);
    h.choice.noteDevice("fr");
    assert.equal(h.choice.snapshot().effective, "fr");
    assert.equal(h.storage.get("otc_language"), "fr");
  });

  test("unknown_payload: pending here until the next connection, then sent", async () => {
    const h = harness();
    h.answers.push("unknown_payload");
    assert.equal(await h.choice.choose("es"), "too_old");
    assert.equal(h.choice.snapshot().tooOld, true);
    assert.equal(h.choice.snapshot().pending, true);
    assert.equal(await h.choice.choose("de"), "too_old"); // not sent again
    assert.equal(h.sends.length, 1);
    assert.equal(h.choice.snapshot().effective, "de");
    h.choice.noteDevice(undefined); // its Status has no language
    assert.equal(h.choice.snapshot().pending, true);
    // Updated meanwhile: the next connection's first reply sends it, and
    // the device's default in that reply doesn't undo it.
    h.now += 3_600_000;
    h.choice.noteConnection();
    assert.equal(h.choice.snapshot().tooOld, false);
    h.choice.noteDevice("");
    assert.deepEqual(h.sends[1], { language: "de", expected: undefined });
    await new Promise((r) => setTimeout(r, 0));
    assert.equal(h.choice.snapshot().pending, false);
    assert.equal(h.choice.snapshot().choice, "de");
  });

  test("after the device's ok, an older reply arriving late doesn't undo the change", async () => {
    const h = harness();
    h.choice.noteDevice("");
    assert.equal(await h.choice.choose("es"), "ok");
    h.now += 1_000;
    h.choice.noteDevice(""); // read before the change, delivered after its ok
    assert.equal(h.choice.snapshot().choice, "es");
    h.now += 30_000;
    h.choice.noteDevice("fr"); // changed from another app since: taken
    assert.equal(h.choice.snapshot().choice, "fr");
  });

  test("another failure: pending, other values ignored for 30 s, then adopted", async () => {
    const h = harness();
    h.choice.noteDevice("");
    h.answers.push("failed");
    assert.equal(await h.choice.choose("es"), "failed");
    assert.equal(h.choice.snapshot().pending, true);
    h.now += 10_000;
    h.choice.noteDevice(""); // a poll from before the change
    assert.equal(h.choice.snapshot().effective, "es");
    assert.equal(h.sends.length, 1); // same connection: not retried
    h.now += 21_000;
    h.choice.noteDevice("");
    assert.equal(h.choice.snapshot().choice, "");
    assert.equal(h.choice.snapshot().pending, false);
  });

  test("another failure: retried at the next connection, before any value is adopted", async () => {
    const h = harness();
    h.choice.noteDevice("");
    h.answers.push("failed");
    await h.choice.choose("es");
    h.now += 120_000;
    h.choice.noteConnection();
    h.choice.noteDevice(""); // the new connection's first Status
    assert.equal(h.choice.snapshot().effective, "es");
    assert.equal(h.sends.length, 2);
    assert.deepEqual(h.sends[1], { language: "es", expected: "" });
    await new Promise((r) => setTimeout(r, 0));
    assert.equal(h.choice.snapshot().pending, false);
  });

  test("seeing its own value settles a pending change", async () => {
    const h = harness();
    h.answers.push("failed");
    await h.choice.choose("es");
    h.choice.noteDevice("es");
    assert.equal(h.choice.snapshot().pending, false);
  });

  test("a value seen with nothing pending is adopted; an absent one changes nothing", () => {
    const h = harness({ stored: { otc_language: "de" } });
    h.choice.noteDevice(undefined);
    assert.equal(h.choice.snapshot().choice, "de");
    h.choice.noteDevice("es");
    assert.equal(h.choice.snapshot().choice, "es");
    assert.equal(h.storage.get("otc_language"), "es");
    h.choice.noteDevice("NOT A CODE");
    assert.equal(h.choice.snapshot().choice, "es");
  });

  test("a second change while the first is on its way goes next, over the first", async () => {
    const h = harness();
    h.choice.noteDevice("");
    const first = h.choice.choose("es");
    const second = h.choice.choose("de");
    assert.equal(await first, "ok");
    assert.equal(await second, "ok");
    assert.deepEqual(h.sends, [{ language: "es", expected: "" }, { language: "de", expected: "es" }]);
    assert.equal(h.choice.snapshot().choice, "de");
  });

  test("a change pending across a reload is sent at the first device reply", () => {
    const h = harness({ stored: { otc_language: "es", otc_language_pending: JSON.stringify({ at: 5, expected: "" }) } });
    assert.equal(h.choice.snapshot().pending, true);
    h.choice.noteDevice("");
    assert.deepEqual(h.sends, [{ language: "es", expected: "" }]);
    assert.equal(h.choice.snapshot().effective, "es");
  });

  test("Sign Out drops a pending change and keeps the choice", async () => {
    const h = harness();
    h.answers.push("failed");
    await h.choice.choose("es");
    h.choice.dropPending();
    assert.ok(!h.storage.has("otc_language_pending"));
    assert.equal(h.storage.get("otc_language"), "es");
  });

  test("SetLanguage replies", () => {
    const { setLanguageResult } = m.choice;
    const ack = (a, extra = {}) => ({ id: 1, error: false, errorMessage: "", errorCode: "", payload: { $case: "respAck", respAck: { ok: false, errorMsg: "", code: "", retryAfterSeconds: 0, ...a } }, ...extra });
    assert.equal(setLanguageResult(ack({ ok: true })), "ok");
    assert.equal(setLanguageResult(ack({ code: "changed" })), "changed");
    assert.equal(setLanguageResult({ id: 1, error: true, errorCode: "unknown_payload", errorMessage: "This device does not understand that request", payload: undefined }), "unknown_payload");
    assert.equal(setLanguageResult({ id: 1, error: true, errorCode: "", errorMessage: "unknown payload", payload: undefined }), "unknown_payload");
    assert.equal(setLanguageResult(ack({ code: "device_unreachable", errorMsg: "unknown payload" })), "failed");
    assert.equal(setLanguageResult(ack({ ok: true }, { error: true })), "failed");
  });
});

describe("texts", () => {
  const es = table[1];
  const render = (lang, locale, messages, key, args) => m.runtime.renderForTest(lang, locale, messages, key, args);

  test("arguments are inserted once, as plain text", () => {
    const msgs = { "web.x.renamed": "{oldName} es ahora {newName}" };
    const r = render(es, "es", msgs, "web.x.renamed", { oldName: "$& {newName} $1", newName: "⁨<b>x</b>⁩" });
    assert.equal(r.plain, "$& {newName} $1 es ahora ⁨<b>x</b>⁩");
  });

  test("numbers use the formatting locale, plurals the language's rules", () => {
    const msgs = { "web.x.files": { one: "{count} archivo", other: "{count} archivos", arg: "count" } };
    assert.equal(render(es, "es", msgs, "web.x.files", { count: 1 }).plain, "1 archivo");
    assert.equal(render(es, "es", msgs, "web.x.files", { count: 0 }).plain, "0 archivos");
    assert.equal(render(es, "es", msgs, "web.x.files", { count: 1234567 }).plain, "1.234.567 archivos");
    const fr = { "web.x.files": { one: "{count} fichier", other: "{count} fichiers", arg: "count" } };
    assert.equal(render(table[2], "fr", fr, "web.x.files", { count: 0 }).plain, "0 fichier");
    const pt = { "web.x.files": { one: "{count} ficheiro", other: "{count} ficheiros", arg: "count" } };
    assert.equal(render(table[4], "pt-BR", pt, "web.x.files", { count: 0 }).plain, "0 ficheiros"); // pt-PT rules
  });

  test("a missing key falls back to English, then to the key", () => {
    assert.equal(render(es, "es", {}, "common.save").plain, "Save");
    assert.equal(render(es, "es", {}, "web.no.such_key").plain, "web.no.such_key");
  });

  test("rich texts: tags come from the template only", () => {
    const msgs = { "web.x.policy": "Lee la <link>política de {name}</link> ahora" };
    const r = render(es, "es", msgs, "web.x.policy", { name: "<link>evil</link>" });
    assert.deepEqual(r.rich, [
      { text: "Lee la " },
      { text: "política de <link>evil</link>", tag: "link" },
      { text: " ahora" },
    ]);
    const bad = render(es, "es", { "web.x.bad": "a <b>b</i> c" }, "web.x.bad").rich;
    assert.deepEqual(bad, [{ text: "a " }, { text: "b</i> c" }]);
  });

  test("t() renders the language on screen; English is always loaded", () => {
    assert.equal(m.runtime.t("common.save").length > 0, true);
    const snap = m.runtime.i18nSnapshot();
    assert.equal(snap.ready, true);
    assert.equal(snap.t.lang, snap.lang);
  });
});

describe("formatters", () => {
  test("English in a British browser", () => {
    const f = m.format;
    assert.equal(f.fmtDate("en", Date.UTC(2026, 9, 11, 12), { dateStyle: "medium", timeZone: "UTC" }), "11 Oct 2026");
    assert.equal(f.fmtNumber("en", 1234.5), "1,234.5");
    assert.equal(f.fmtBytes("en", 4_831_838_208), "4.5 GB");
    assert.equal(f.fmtBytes("en", 512), "512 bytes");
    assert.equal(f.fmtBytes("en", 1), "1 byte");
    assert.equal(f.fmtBytes("en", 1_500_000, { base: 1000 }), "1.5 MB");
    assert.equal(f.fmtList("en", ["a", "b", "c"]), "a, b and c");
    assert.equal(f.monthName("en", 0), "January");
    assert.equal(f.monthName("en", 13, "short"), "Feb");
    const now = Date.UTC(2026, 9, 11, 12);
    assert.equal(f.fmtRelative("en", now - 90_000, { now }), "1 minute ago");
    assert.equal(f.fmtRelative("en", now - 86_400_000, { now }), "yesterday");
    assert.equal(f.fmtRelative("en", now - 10_000, { now }), "now");
  });
});
