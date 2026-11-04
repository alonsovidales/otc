// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #56: the bridge is the only thing that knows why a request
// couldn't be handed to a device - the device itself, by definition,
// isn't there to say anything. When it answers with one of the codes
// below (see Ack.code in proto/messages.proto), that verdict has to reach
// the UI from wherever in the app the request happened to be made, which
// is what this tiny store is for.
//
// Deliberately not React state or a context: the reports come from
// ws.ts's socket callbacks, which sit outside the component tree
// entirely, and every component that cares subscribes rather than having
// this threaded down through props from an owner none of them share.
//
// Issue #207: the device itself can say it is in no state to answer - it
// can't reach its storage disks - with the error_code storage_unavailable,
// on every request, GetPubKey first. That is the same kind of verdict (no
// answer about anything asked, a password included), shown on the same
// screen in words of its own, and held here too.

import type { RespEnvelope } from "../proto/messages";

export type DeviceStatusCode = "device_unreachable" | "account_disabled" | "storage_unavailable";

export interface DeviceStatus {
  code: DeviceStatusCode;
  /** The device's own wording (storage_unavailable), meant to be shown
   *  as-is; empty for the bridge's codes (withoutBridgeWords). */
  message: string;
}

// Matches the bridge's cCodeDeviceUnreachable/cCodeAccountDisabled. These
// are the stable tags; the text alongside them is free to change.
const cBridgeCodes: DeviceStatusCode[] = ["device_unreachable", "account_disabled"];

/** The device's own error_code while it can't reach its storage (#207). */
export const cStorageUnavailable = "storage_unavailable";

/**
 * Whether an Ack code is the bridge answering for the device (it could not
 * reach it, or the account is switched off) rather than the device's own
 * answer.
 */
export function isDeviceStatusCode(code: string | undefined): code is DeviceStatusCode {
  return !!code && (cBridgeCodes as string[]).includes(code);
}

/** Whether an answer's code (the envelope's error_code, or an Ack's) is
 *  the device saying it can't reach its storage (#207). */
export function isStorageUnavailable(code: string | undefined): boolean {
  return code === cStorageUnavailable;
}

/** How often the screen over the app asks whether the device answers
 *  again: every 5 s while the bridge can't reach it, every 30 s while it
 *  can't reach its storage (it starts again by itself once its disks are
 *  back), never for a switched-off account. A wake asks at once besides. */
export const cUnreachableRetryMs = 5_000;
export const cStorageRetryMs = 30_000;
export function recheckMs(code: DeviceStatusCode): number | null {
  if (code === "account_disabled") return null;
  return code === cStorageUnavailable ? cStorageRetryMs : cUnreachableRetryMs;
}

/** How long the app waits before the screen comes over it (App.tsx): a
 *  device the bridge couldn't reach is often back a moment later - a long
 *  request the bridge stopped waiting for, the device dialling in again
 *  after a blip - so its screen waits cCoverGraceMs, the device asked
 *  again meanwhile (cCoverLookMs), and a page whose device answers by then
 *  never sees it. A switched-off account and a device without its storage
 *  are a state, not a blip: at once. The app stays as it is under the
 *  screen either way (what is typed, a dialog, a post on its way). */
export const cCoverGraceMs = 4_000;
export const cCoverLookMs = 1_500;
export function coverAfterMs(code: DeviceStatusCode): number {
  return code === "device_unreachable" ? cCoverGraceMs : 0;
}

/** Whether an answer is the bridge's for the device rather than the
 *  device's own: the bridge couldn't hand the request over, or lost the
 *  device before its answer came (device_unreachable), or the account is
 *  switched off. Nothing about what was asked: a request that went may
 *  have been done, as when the socket closes before the answer. */
export function isBridgeAnswer(resp: RespEnvelope): boolean {
  return resp.payload?.$case === "respAck" && isDeviceStatusCode(resp.payload.respAck.code);
}

/** The bridge's answer for the device with its words taken out (the
 *  envelope's error_message and the Ack's error_msg), anything else as it
 *  came. They are the bridge's relay texts, English whatever the page's
 *  language, and they read as a refusal; its code stays, so
 *  isBridgeAnswer still tells it. ws.ts runs every answer through this as
 *  it arrives, so no part of the app can show them: each says what failed
 *  in its own words, or "The device did not answer". */
export function withoutBridgeWords(resp: RespEnvelope): RespEnvelope {
  if (!isBridgeAnswer(resp)) return resp;
  resp.errorMessage = "";
  if (resp.payload?.$case === "respAck") resp.payload.respAck.errorMsg = "";
  return resp;
}

let current: DeviceStatus | null = null;
const listeners = new Set<(s: DeviceStatus | null) => void>();
// How many times the device has answered again after a status: see
// deviceBackCount.
let backs = 0;

export function getDeviceStatus(): DeviceStatus | null {
  return current;
}

export function subscribeDeviceStatus(fn: (s: DeviceStatus | null) => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

function publish(next: DeviceStatus | null) {
  // Identical repeats are common (every request that fails while a device
  // is down reports the same thing) and re-rendering the whole app for
  // each one is pure waste.
  if (current?.code === next?.code && current?.message === next?.message) return;
  if (current && !next) backs++;
  current = next;
  listeners.forEach(fn => fn(current));
}

/** How many times the device has answered again (a status cleared) since
 *  the page loaded. A load that failed notes it, so a device back before
 *  anything listened still counts (whenDeviceBack). */
export function deviceBackCount(): number {
  return backs;
}

/** fn runs each time the device answers again after `since` (a
 *  deviceBackCount taken as a load failed) - at once when it already has.
 *  The unreachable screen lies over a page that stays mounted (App.tsx),
 *  so a load that failed while the device was away runs again through
 *  this (useDeviceBack). Returns the stop. */
export function whenDeviceBack(since: number, fn: () => void): () => void {
  const off = subscribeDeviceStatus((s) => { if (s === null) fn(); });
  if (backs !== since) fn();
  return off;
}

/**
 * Called for every response the socket decodes (see ws.ts). A reply
 * carrying one of the known codes puts the app into that state; any reply
 * that isn't an error clears it, which is what makes the app recover on
 * its own the moment the device answers again - no reload, no retry
 * button needed. The device's storage_unavailable goes with any answer
 * that isn't it, an error included: a device that says so answers
 * nothing else, so any other answer is the device back (a "not
 * authenticated" is then the sign-in's to deal with).
 */
export function noteResponse(code: string | undefined, message: string | undefined, isError: boolean) {
  if (isDeviceStatusCode(code) || isStorageUnavailable(code)) {
    publish({ code: code as DeviceStatusCode, message: message || "" });
    return;
  }
  if (!isError || current?.code === cStorageUnavailable) publish(null);
}

/** Clears the state - used when a reconnect succeeds. */
export function clearDeviceStatus() {
  publish(null);
}
