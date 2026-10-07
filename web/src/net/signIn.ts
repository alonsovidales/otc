// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Signing this browser in with the device's password: the top bar's field
// (components/TopSignIn.tsx) and the first step of a new device's setup
// (views/SignIn.tsx), which sets the password the same way. useWS.sendAuth
// does the work - the session, the token kept for reloads (issue #101),
// the password replayed on a reconnect once the device has accepted it -
// and App's handleSignedIn lands the page once this says yes. What comes
// back on a no is said in words for the page.
//
// Both are typed by someone who is there: a password nothing answered is
// not kept to be sent again later (they press Enter again), and only setup
// may send one to a new device, which takes it as its own (issue #39).

import { NewDevice, useWS } from "./useWS";
import { getDeviceStatus } from "./deviceStatus";

export type SignInResult =
  | { ok: true }
  // "wrong": the device refused the password. "unreachable": nothing
  // answered (offline, the socket failed or closed, the bridge couldn't
  // reach the device). "other": the device's or the bridge's own words - a
  // lockout with its time (issue #117), a switched-off account (issue #93).
  | { ok: false; reason: "wrong" | "unreachable" | "other"; message: string }
  // The device has no password yet, so nothing was sent: its setup is
  // what to show (never for setUp).
  | { ok: false; reason: "new-device"; isPrimary: boolean; message: string };

// ws.ts's own errors: the socket could not open, or closed before the answer.
const cNoAnswer = /^WS (not connected|connection closed)/;

// setUp: the password is a new device's own, chosen in its setup.
export async function signInWithPassword(password: string, { setUp = false } = {}): Promise<SignInResult> {
  try {
    if (await useWS.sendAuth(password, { refuseNewDevice: !setUp, keepOnlyIfAccepted: true })) return { ok: true };
    return { ok: false, reason: "wrong", message: "Wrong password. Try again." };
  } catch (err) {
    if (err instanceof NewDevice) {
      return { ok: false, reason: "new-device", isPrimary: err.isPrimary, message: err.message };
    }
    if (!navigator.onLine) {
      return { ok: false, reason: "unreachable", message: "You're offline. Check your connection and try again." };
    }
    const message = err instanceof Error ? err.message : "";
    // The bridge answering for a device it couldn't reach says so before
    // the answer gets here (deviceStatus): the device never saw the
    // password.
    if (!message || cNoAnswer.test(message) || getDeviceStatus()?.code === "device_unreachable") {
      return { ok: false, reason: "unreachable", message: "Can't reach your device right now. Try again in a moment." };
    }
    return { ok: false, reason: "other", message };
  }
}
