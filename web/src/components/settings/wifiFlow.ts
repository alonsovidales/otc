// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Wi-Fi's rules (WifiPanel.tsx): what the device's owner may
// send, what an answer means, and how long to wait. React-free and
// socket-free, so node tests them (web/scripts/wifi-flow-test.mjs).
//
// The same rules as the phones' WifiFlow (iOS WifiSection.swift, Android
// ui/settings/WifiFlow.kt), tested from the same table of cases (their row
// ids are in web/scripts/wifi-flow-test.mjs), and the device's
// (network.Join, and the root runner
// scripts/device-runner/otc-wifi-join-runner.py): change them together.

import type { RespEnvelope, WifiJoin, WifiNetwork, WifiStatus } from "../../proto/messages";

// After JoinWifi the device switches networks, so its connection may drop
// for about a minute: the panel asks GetWifiStatus every cPollMs, giving up
// on one look after cLookTimeoutMs. Past cSlowAfterMs it says the device
// hasn't answered yet, and looks every cSlowPollMs until it does or the
// page goes.
export const cPollMs = 3000;
export const cLookTimeoutMs = 10_000;
export const cSlowAfterMs = 240_000;
export const cSlowPollMs = 10_000;

// A Wi-Fi network's name is 1-32 bytes; a password 8-63 bytes, or exactly
// 64 hexadecimal digits (a raw key). Both in UTF-8, without control
// characters.
export const cMaxNameBytes = 32;
export const cMinPasswordBytes = 8;
export const cMaxPasswordBytes = 63;

const hexKey = /^[0-9a-fA-F]{64}$/;

/** The length of a text in UTF-8, as the device counts it. */
export function utf8Bytes(s: string): number {
  return new TextEncoder().encode(s).length;
}

// A text JavaScript holds can have half a surrogate pair, which has no
// UTF-8 form: the device would see U+FFFD instead.
function wellFormed(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      const next = s.charCodeAt(i + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      i++;
    } else if (c >= 0xdc00 && c <= 0xdfff) {
      return false;
    }
  }
  return true;
}

// Below U+0020, or U+007F.
function hasControl(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return true;
  }
  return false;
}

/**
 * What is wrong with a network's name: web.settings.wifi.<problem>, or
 * name_invalid - half a surrogate pair, which has no UTF-8 form at all (a
 * field never holds one in practice).
 */
export type NameProblem = "name_needed" | "name_too_long" | "name_invalid";

/**
 * A network's name as typed (Other Network…) or listed: 1-32 bytes of
 * UTF-8. Never trimmed or changed: spaces are part of a name. Control
 * characters are left to the device, which refuses them (bad_name, in its
 * own words) - the phones' rule (table row N11).
 */
export function validateSsid(ssid: string): NameProblem | null {
  if (ssid === "") return "name_needed";
  if (!wellFormed(ssid)) return "name_invalid";
  if (utf8Bytes(ssid) > cMaxNameBytes) return "name_too_long";
  return null;
}

/** What is wrong with a password: web.settings.wifi.<problem>. */
export type PasswordProblem = "password_needed" | "password_invalid";

/**
 * A password for a network: required for a listed network that has one;
 * optional for one typed by hand (empty: an open network). Never trimmed.
 */
export function validatePassword(password: string, required: boolean): PasswordProblem | null {
  if (password === "") return required ? "password_needed" : null;
  if (hexKey.test(password)) return null;
  if (!wellFormed(password) || hasControl(password)) return "password_invalid";
  const n = utf8Bytes(password);
  return n >= cMinPasswordBytes && n <= cMaxPasswordBytes ? null : "password_invalid";
}

/**
 * Which kind of network is chosen: a listed one with a password, a listed
 * open one (no field, nothing sent), or Other Network… (a typed name and an
 * optional password - empty for an open one).
 */
export type Kind = "secured" | "open" | "other";

export function kindOf(c: { secured: boolean; hidden: boolean }): Kind {
  return c.hidden ? "other" : c.secured ? "secured" : "open";
}

/** The password check for a kind: an open row's password is never looked at. */
export function checkPassword(password: string, kind: Kind): PasswordProblem | null {
  return kind === "open" ? null : validatePassword(password, kind === "secured");
}

/** What gets sealed and sent: nothing for an open row. */
export function sentPassword(password: string, kind: Kind): string {
  return kind === "open" ? "" : password;
}

/** JoinWifi.hidden: only a typed name may be missing from the scan. */
export function hiddenFor(kind: Kind): boolean {
  return kind === "other";
}

/** A change still running when the section loads (a reload while waiting): its id. */
export function resumeID(status: WifiStatus): string | null {
  const join = status.lastJoin;
  return join && join.state === "joining" && join.id ? join.id : null;
}

/** The networks worth listing: every one with a name. */
export function listedNetworks(networks: WifiNetwork[]): WifiNetwork[] {
  return networks.filter((n) => n.ssid !== "");
}

/**
 * Whether a listed network can be chosen: not one the device says it can't
 * join (enterprise, WEP, OWE), nor one whose name it wouldn't take (control
 * characters, over 32 bytes). Shown greyed out as "Not supported".
 */
export function joinable(n: WifiNetwork): boolean {
  return !n.unsupported && validateSsid(n.ssid) === null && !hasControl(n.ssid);
}

/**
 * Which confirmation Connect asks: a device on its cable and no Wi-Fi
 * stays reachable through the cable while it joins; one on Wi-Fi leaves it.
 */
export function confirmKey(now: Pick<WifiStatus, "ssid" | "wired">): "web.settings.wifi.confirm" | "web.settings.wifi.confirm_cable" {
  return now.wired && now.ssid === "" ? "web.settings.wifi.confirm_cable" : "web.settings.wifi.confirm";
}

/** Whether an error answer is a device too old for the request (before the error codes too: the bare message). */
export function isUnknownPayload(resp: RespEnvelope): boolean {
  if (!resp.error) return false;
  if (resp.errorCode === "unknown_payload") return true;
  const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : undefined;
  return resp.errorCode === "" && /unknown payload/i.test(resp.errorMessage || ack?.errorMsg || "");
}

/** What GetWifiStatus's answer means for the section. */
export type StatusVerdict =
  | { kind: "show"; status: WifiStatus }
  // Not a device that changes its own Wi-Fi from Settings, or not its
  // owner, or an error: no section.
  | { kind: "hide" }
  // A device before release 140: no section, and not asked again until
  // the next sign-in.
  | { kind: "unsupported" };

export function judgeStatus(resp: RespEnvelope): StatusVerdict {
  if (resp.payload?.$case === "respWifiStatus") {
    const status = resp.payload.respWifiStatus;
    return status.managed ? { kind: "show", status } : { kind: "hide" };
  }
  if (isUnknownPayload(resp)) return { kind: "unsupported" };
  return { kind: "hide" };
}

/** What JoinWifi's answer means. */
export type JoinVerdict =
  // Taken: the device is switching; wait for this id to end.
  | { kind: "joining"; status: WifiStatus; id: string }
  // Ended already (a device that answers with the result).
  | { kind: "done"; status: WifiStatus; join: WifiJoin }
  // Taken, but with no change to wait for (no last_join, or no id): how it
  // goes can't be told.
  | { kind: "unknown"; status: WifiStatus }
  // A device before release 140: hide the section until the next sign-in.
  | { kind: "unsupported" }
  // The device can't change its Wi-Fi from Settings after all: ask again,
  // which hides the section.
  | { kind: "not_managed"; message: string }
  // Refused before anything changed (bad_name, bad_password, busy,
  // update_running, too_many, demo, ...): the device's words, or "" for
  // web.settings.wifi.failed.
  | { kind: "refused"; message: string };

export function judgeJoin(resp: RespEnvelope): JoinVerdict {
  if (resp.payload?.$case === "respWifiStatus") {
    const status = resp.payload.respWifiStatus;
    const join = status.lastJoin;
    if (!join) return { kind: "unknown", status };
    if (join.state === "joining") return join.id ? { kind: "joining", status, id: join.id } : { kind: "unknown", status };
    return finalState(join.state) ? { kind: "done", status, join } : { kind: "unknown", status };
  }
  if (isUnknownPayload(resp)) return { kind: "unsupported" };
  const message = words(resp);
  if (resp.error && resp.errorCode === "not_managed") return { kind: "not_managed", message };
  return { kind: "refused", message };
}

/** What a look at GetWifiStatus says about the change under way (joinID). */
export type PollVerdict =
  // Not the device's own status: a bridge answering for a device it can't
  // reach, a device that wants a sign-in first. Look again.
  | { kind: "ignore" }
  // The device answered: the change still runs.
  | { kind: "waiting"; status: WifiStatus }
  // The change ended: join.state is connected or failed, join.message the
  // device's words for it (with failed, where the device ended up).
  | { kind: "done"; status: WifiStatus; join: WifiJoin }
  // The device answered without this change (it restarted since, or
  // another change replaced it): how it went is lost.
  | { kind: "unknown"; status: WifiStatus };

// The states a change ends in; any other (a later build's) can't be told.
function finalState(state: string): boolean {
  return state === "connected" || state === "failed";
}

export function judgePoll(resp: RespEnvelope, joinID: string): PollVerdict {
  if (resp.error || resp.payload?.$case !== "respWifiStatus") return { kind: "ignore" };
  const status = resp.payload.respWifiStatus;
  // A device in the middle of a change may say for a moment that it
  // doesn't manage its Wi-Fi: not an answer about the change.
  if (!status.managed) return { kind: "ignore" };
  const join = status.lastJoin;
  if (!join || join.id !== joinID) return { kind: "unknown", status };
  if (join.state === "joining") return { kind: "waiting", status };
  return finalState(join.state) ? { kind: "done", status, join } : { kind: "unknown", status };
}

/** Whether a look says the change under way ended. */
export function isFinished(resp: RespEnvelope, joinID: string): boolean {
  return judgePoll(resp, joinID).kind === "done";
}

/**
 * Whether a failure keeps the list open with the network still chosen, for
 * another try at its password or name. auth_failed is the half of the old
 * wrong_password the device can't be sure of ("check the password");
 * open_network, a password typed for a network that has none.
 */
export function keepsPicker(code: string): boolean {
  return ["wrong_password", "auth_failed", "not_found", "password_required", "open_network"].includes(code);
}

/** An answer's words: its error message, else a bridge Ack's ("" for none). */
export function words(resp: RespEnvelope): string {
  const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : undefined;
  return resp.errorMessage || ack?.errorMsg || "";
}

/** Where a wait is, this long after the device took the change. */
export type WaitPhase = "waiting" | "slow";

export function nextPhase(elapsedMs: number): WaitPhase {
  return elapsedMs >= cSlowAfterMs ? "slow" : "waiting";
}

/** How long until the next look, this long after the device took the change. */
export function pollDelay(elapsedMs: number): number {
  return nextPhase(elapsedMs) === "slow" ? cSlowPollMs : cPollMs;
}

/** A signal (0-100) as 1 to 4 bars. */
export function signalBars(signal: number): 1 | 2 | 3 | 4 {
  if (signal >= 75) return 4;
  if (signal >= 50) return 3;
  if (signal >= 25) return 2;
  return 1;
}
