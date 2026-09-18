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

export type DeviceStatusCode = "device_unreachable" | "account_disabled";

export interface DeviceStatus {
  code: DeviceStatusCode;
  /** The device/bridge's own wording, meant to be shown as-is. */
  message: string;
}

// Matches the bridge's cCodeDeviceUnreachable/cCodeAccountDisabled. These
// are the stable tags; the text alongside them is free to change.
const cKnownCodes: DeviceStatusCode[] = ["device_unreachable", "account_disabled"];

let current: DeviceStatus | null = null;
const listeners = new Set<(s: DeviceStatus | null) => void>();

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
  current = next;
  listeners.forEach(fn => fn(current));
}

/**
 * Called for every response the socket decodes (see ws.ts). A reply
 * carrying one of the known codes puts the app into that state; any reply
 * that isn't an error clears it, which is what makes the app recover on
 * its own the moment the device answers again - no reload, no retry
 * button needed.
 */
export function noteResponse(code: string | undefined, message: string | undefined, isError: boolean) {
  if (code && (cKnownCodes as string[]).includes(code)) {
    publish({ code: code as DeviceStatusCode, message: message || "" });
    return;
  }
  if (!isError) publish(null);
}

/** Clears the state - used when a reconnect succeeds. */
export function clearDeviceStatus() {
  publish(null);
}
