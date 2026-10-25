// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Settings › Wi-Fi's rules (src/components/settings/wifiFlow.ts).
// The web app has no test runner, so this loads the module through Vite's
// SSR loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/wifi-flow-test.mjs        (needs web/node_modules)
//
// The same table of cases as the phones' WifiFlow tests (iOS
// OffTheCloudTests/WifiFlowTests.swift, Android ui/settings/WifiFlowTest.kt),
// row for row by id (N names, P passwords, C what is sent, K cable-only
// confirm, L/R on load, J JoinWifi's answer, W a look while waiting, K-
// which failures keep the list open, U which listed networks can be
// chosen, T timing), and the device's rules (network.Join, the root
// runner's --selftest): change them together. Web-only checks follow.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
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
    cacheDir: join(tmpdir(), "otc-wifi-flow-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with type-only imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/settings/wifiFlow.ts");
});
after(() => server?.close());

const net = (ssid, extra = {}) => ({ ssid, signal: 50, secured: true, current: false, unsupported: false, ...extra });
const status = (extra = {}) => ({ managed: true, ssid: "Home", signal: 72, wired: false, lastJoin: undefined, ...extra });
const joinOf = (id, state, code = state, extra = {}) => ({ id, ssid: "Home", state, code, message: `${code} words`, restored: false, ...extra });
const ok = (payload) => ({ id: 1, error: false, errorMessage: "", errorCode: "", payload });
const wifiStatus = (s) => ok({ $case: "respWifiStatus", respWifiStatus: s });
const refused = (code, message, payload) => ({ id: 1, error: true, errorMessage: message, errorCode: code, payload });

// The table's names for the web's answers.
const nameOf = { name_needed: "nameNeeded", name_too_long: "nameTooLong", password_needed: "passwordNeeded", password_invalid: "passwordInvalid" };
const named = (p) => (p === null ? null : nameOf[p] ?? p);
const checkName = (ssid) => named(m.validateSsid(ssid));
const checkPassword = (pw, kind) => named(m.checkPassword(pw, kind));
const check = (ssid, pw, kind) => checkName(ssid) ?? checkPassword(pw, kind);
const outcomeOf = (join) => (join.state === "connected" ? ["connected", join.message] : ["failed", join.code, join.message]);
function onLoad(resp) {
  const v = m.judgeStatus(resp);
  return { show: "show", hide: "hide", unsupported: "hideUntilSignIn" }[v.kind];
}
function afterJoin(resp) {
  const v = m.judgeJoin(resp);
  switch (v.kind) {
    case "joining": return ["wait", v.id];
    case "done": return ["done", ...outcomeOf(v.join)];
    case "unknown": return ["done", "unknown"];
    case "unsupported": return ["hideUntilSignIn"];
    case "not_managed": return ["hide"];
    case "refused": return ["refused", v.message || null];
  }
}
function onPoll(resp, joinID) {
  const v = m.judgePoll(resp, joinID);
  switch (v.kind) {
    case "ignore": case "waiting": return ["keepWaiting"];
    case "done": return ["done", ...outcomeOf(v.join)];
    case "unknown": return ["done", "unknown"];
  }
}
const bridgeAck = (errorMsg) => ok({ $case: "respAck", respAck: { ok: false, errorMsg, code: "" } });
const errorOf = (code, message) => refused(code, message);

describe("the shared table: names (checkName)", () => {
  const rows = [
    ["N1", "", "nameNeeded"], ["N2", "a", null], ["N3", "a".repeat(32), null], ["N4", "a".repeat(33), "nameTooLong"],
    ["N5", "é".repeat(16), null], ["N6", "é".repeat(17), "nameTooLong"], ["N7", "😀".repeat(8), null],
    ["N8", "😀".repeat(9), "nameTooLong"], ["N9", " Home ", null], ["N10", "   ", null],
    ["N11", "Casa\tWifi", null], ["N12", "Bob's", null],
  ];
  for (const [id, ssid, want] of rows) test(id, () => assert.equal(checkName(ssid), want));
});

describe("the shared table: passwords (checkPassword)", () => {
  const rows = [
    ["P1", "", "secured", "passwordNeeded"], ["P2", "", "open", null], ["P3", "", "other", null],
    ["P4", "abc", "open", null], ["P5", "a".repeat(7), "secured", "passwordInvalid"], ["P6", "a".repeat(8), "secured", null],
    ["P7", "a".repeat(63), "secured", null], ["P8", "a".repeat(64), "secured", null], ["P9", "0".repeat(64), "secured", null],
    ["P10", "aF".repeat(32), "secured", null], ["P11", "a".repeat(63) + "g", "secured", "passwordInvalid"],
    ["P12", "a".repeat(65), "secured", "passwordInvalid"], ["P13", "contraseña", "secured", null],
    ["P14", "ñ".repeat(31), "secured", null], ["P15", "ñ".repeat(32), "secured", "passwordInvalid"],
    ["P16", "€€€", "secured", null], ["P17", "€€", "secured", "passwordInvalid"], ["P18", "pass\tword", "secured", "passwordInvalid"],
    ["P19", "password\u007f", "secured", "passwordInvalid"], ["P20", "pass word", "secured", null],
    ["P21", "０".repeat(64), "secured", "passwordInvalid"], ["P22", "1234567", "other", "passwordInvalid"],
    ["P23", "éé", "secured", "passwordInvalid"], ["P24", "ééé", "secured", null],
  ];
  for (const [id, pw, kind, want] of rows) test(id, () => assert.equal(checkPassword(pw, kind), want));
});

describe("the shared table: check, and what is sent", () => {
  const rows = [
    ["C1", "", "", "other", "nameNeeded"], ["C2", "", "short", "other", "nameNeeded"],
    ["C3", "a".repeat(33), "", "secured", "nameTooLong"], ["C4", "Home", "", "secured", "passwordNeeded"],
    ["C5", "Home", "secret12", "secured", null, "secret12", false], ["C6", "Cafe", "typed", "open", null, "", false],
    ["C7", "Hidden", "", "other", null, "", true], ["C8", "Hidden", "secret12", "other", null, "secret12", true],
  ];
  for (const [id, ssid, pw, kind, want, sent, hidden] of rows) {
    test(id, () => {
      assert.equal(check(ssid, pw, kind), want);
      if (want === null) {
        assert.equal(m.sentPassword(pw, kind), sent);
        assert.equal(m.hiddenFor(kind), hidden);
      }
    });
  }
  test("a chosen row's kind", () => {
    assert.equal(m.kindOf({ secured: true, hidden: false }), "secured");
    assert.equal(m.kindOf({ secured: false, hidden: false }), "open");
    assert.equal(m.kindOf({ secured: false, hidden: true }), "other");
  });
});

describe("the shared table: the cable-only confirmation (cableOnly)", () => {
  const rows = [["K1", true, "", true], ["K2", true, "Home", false], ["K3", false, "", false], ["K4", false, "Home", false]];
  for (const [id, wired, ssid, cable] of rows) {
    test(id, () => assert.equal(m.confirmKey({ wired, ssid }), cable ? "web.settings.wifi.confirm_cable" : "web.settings.wifi.confirm"));
  }
});

describe("the shared table: on load (onLoad, resumeID)", () => {
  const rows = [
    ["L1", wifiStatus(status()), "show"], ["L2", wifiStatus(status({ managed: false })), "hide"],
    ["L3", errorOf("unknown_payload", "Unknown request"), "hideUntilSignIn"], ["L4", errorOf("", "unknown payload"), "hideUntilSignIn"],
    ["L5", errorOf("demo", "M"), "hide"], ["L6", errorOf("", "not available on this instance"), "hide"],
    ["L7", { ...bridgeAck("Device unreachable"), error: true }, "hide"],
  ];
  for (const [id, resp, want] of rows) test(id, () => assert.equal(onLoad(resp), want));
  const r = [["R1", undefined, null], ["R2", joinOf("a1", "joining"), "a1"], ["R3", joinOf("a1", "connected"), null], ["R4", joinOf("a1", "failed"), null]];
  for (const [id, lastJoin, want] of r) test(id, () => assert.equal(m.resumeID(status({ lastJoin })), want));
});

describe("the shared table: JoinWifi's answer (afterJoin)", () => {
  const rows = [
    ["J1", wifiStatus(status({ lastJoin: joinOf("a1", "joining") })), ["wait", "a1"]],
    ["J2", wifiStatus(status({ lastJoin: joinOf("a1", "failed", "busy", { message: "M" }) })), ["done", "failed", "busy", "M"]],
    ["J3", wifiStatus(status({ lastJoin: joinOf("a1", "connected", "connected", { message: "M" }) })), ["done", "connected", "M"]],
    ["J4", wifiStatus(status()), ["done", "unknown"]],
    ["J5", wifiStatus(status({ lastJoin: joinOf("", "joining") })), ["done", "unknown"]],
    ["J6", errorOf("unknown_payload", "Unknown request"), ["hideUntilSignIn"]],
    ["J7", errorOf("not_managed", "M"), ["hide"]],
    ["J8", errorOf("busy", "M"), ["refused", "M"]], ["J9", errorOf("bad_password", "M"), ["refused", "M"]],
    ["J10", errorOf("demo", "M"), ["refused", "M"]], ["J11", errorOf("", ""), ["refused", null]],
    ["J12", bridgeAck("M"), ["refused", "M"]],
    ["J13", bridgeAck(""), ["refused", null]],
  ];
  for (const [id, resp, want] of rows) test(id, () => assert.deepEqual(afterJoin(resp), want));
  test("J13, any other payload", () => assert.deepEqual(afterJoin(ok({ $case: "respWifiNetworks", respWifiNetworks: { networks: [] } })), ["refused", null]));
});

describe("the shared table: a look while waiting (onPoll, joinID a1)", () => {
  const rows = [
    ["W2", bridgeAck("Device unreachable"), ["keepWaiting"]],
    ["W3", errorOf("anything", "M"), ["keepWaiting"]],
    ["W4", wifiStatus(status({ managed: false })), ["keepWaiting"]],
    ["W5", wifiStatus(status({ lastJoin: joinOf("a1", "joining") })), ["keepWaiting"]],
    ["W6", wifiStatus(status({ lastJoin: joinOf("a1", "connected", "connected", { message: "Connected to Home." }) })), ["done", "connected", "Connected to Home."]],
    ["W7", wifiStatus(status({ lastJoin: joinOf("a1", "failed", "wrong_password", { message: "M" }) })), ["done", "failed", "wrong_password", "M"]],
    ["W8", wifiStatus(status({ lastJoin: joinOf("a1", "failed", "no_internet", { message: "" }) })), ["done", "failed", "no_internet", ""]],
    ["W9", wifiStatus(status()), ["done", "unknown"]],
    ["W10", wifiStatus(status({ lastJoin: joinOf("b2", "joining") })), ["done", "unknown"]],
    ["W11", wifiStatus(status({ lastJoin: joinOf("b2", "connected") })), ["done", "unknown"]],
    ["W12", wifiStatus(status({ lastJoin: joinOf("a1", "later") })), ["done", "unknown"]],
  ];
  for (const [id, resp, want] of rows) test(id, () => assert.deepEqual(onPoll(resp, "a1"), want));
});

describe("the shared table: which failures keep the list open (keepsPicker)", () => {
  for (const [id, code] of [["K-1", "wrong_password"], ["K-2", "auth_failed"], ["K-3", "not_found"], ["K-4", "password_required"], ["K-4b", "open_network"]]) {
    test(id, () => assert.equal(m.keepsPicker(code), true));
  }
  test("K-5", () => {
    for (const code of ["timed_out", "no_address", "no_internet", "join_failed", "interrupted", "unsupported_security", "bad_request",
      "no_wifi", "busy", "update_running", "too_many", "no_answer"]) {
      assert.equal(m.keepsPicker(code), false, code);
    }
  });
  test("K-6", () => {
    assert.equal(m.keepsPicker(""), false);
    assert.equal(m.keepsPicker("something_new"), false);
  });
});

describe("the shared table: which listed networks can be chosen (joinable)", () => {
  const rows = [
    ["U1", net("Corp", { unsupported: true }), false], ["U2", net("Casa\tWifi"), false], ["U3", net("a".repeat(33)), false],
    ["U4", net("Home"), true], ["U5", net("Cafe", { secured: false }), true],
  ];
  for (const [id, n, want] of rows) test(id, () => assert.equal(m.joinable(n), want));
});

describe("the shared table: timing", () => {
  test("T1", () => assert.equal(m.nextPhase(0), "waiting"));
  test("T2", () => assert.equal(m.nextPhase(239_900), "waiting"));
  test("T3", () => assert.equal(m.nextPhase(240_000), "slow"));
  test("T4", () => assert.equal(m.nextPhase(600_000), "slow"));
  test("T5", () => assert.equal(m.pollDelay(0), 3000));
  test("T6", () => assert.equal(m.pollDelay(240_000), 10_000));
  test("T7", () => assert.equal(m.cLookTimeoutMs, 10_000));
});

describe("web only", () => {
  test("half a surrogate pair has no UTF-8 form: refused before sending", () => {
    assert.equal(m.validateSsid("Home\ud83d"), "name_invalid");
    assert.equal(m.validateSsid("\ude00Home"), "name_invalid");
    assert.equal(m.validatePassword("password\ud83d", true), "password_invalid");
  });
  test("utf8Bytes counts as the device does", () => {
    assert.equal(m.utf8Bytes("abc"), 3);
    assert.equal(m.utf8Bytes("é"), 2);
    assert.equal(m.utf8Bytes("€"), 3);
    assert.equal(m.utf8Bytes("📶"), 4);
  });
  test("a network without a name isn't listed", () => {
    assert.deepEqual(m.listedNetworks([net(""), net("Home")]).map((n) => n.ssid), ["Home"]);
  });
  test("signal as bars", () => {
    assert.equal(m.signalBars(0), 1);
    assert.equal(m.signalBars(24), 1);
    assert.equal(m.signalBars(25), 2);
    assert.equal(m.signalBars(50), 3);
    assert.equal(m.signalBars(74), 3);
    assert.equal(m.signalBars(75), 4);
    assert.equal(m.signalBars(100), 4);
  });
  test("an answer's words: its error message, else the Ack's", () => {
    assert.equal(m.words(errorOf("x", "M")), "M");
    assert.equal(m.words(bridgeAck("A")), "A");
    assert.equal(m.words(ok({ $case: "respAck", respAck: { ok: true, errorMsg: "", code: "" } })), "");
    // ws.ts's own demo refusal carries its words in the Ack too.
    const demo = refused("demo", "", { $case: "respAck", respAck: { ok: false, errorMsg: "Changes are off", code: "demo" } });
    assert.deepEqual(afterJoin(demo), ["refused", "Changes are off"]);
  });
  test("every look updates the line at the top; isFinished", () => {
    const v = m.judgePoll(wifiStatus(status({ ssid: "New", lastJoin: joinOf("a1", "joining") })), "a1");
    assert.equal(v.status.ssid, "New");
    assert.equal(m.isFinished(wifiStatus(status({ lastJoin: joinOf("a1", "joining") })), "a1"), false);
    assert.equal(m.isFinished(wifiStatus(status({ lastJoin: joinOf("a1", "failed", "wrong_password") })), "a1"), true);
    assert.equal(m.isFinished(wifiStatus(status()), "a1"), false);
  });
  test("the constants", () => {
    assert.equal(m.cPollMs, 3000);
    assert.equal(m.cSlowAfterMs, 240_000);
    assert.equal(m.cSlowPollMs, 10_000);
  });
});
