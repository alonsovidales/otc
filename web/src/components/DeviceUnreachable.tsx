// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #56: a domain that's registered on the bridge but whose device
// has no live connection used to surface as "Unable to fetch the
// connection's public key" - the bridge's real explanation was dropped on
// the way (see deviceStatus.ts and the bridge's own default-case handler).
// This is what gets shown instead, with the same unplugged-cloud icon the
// bridge's own unavailable.html uses (issue #97) so the two halves of the
// same situation - device down before the app loads, device down while
// it's open - look like the same thing rather than two unrelated errors.
import { useEffect } from "react";
import { useWS } from "../net/useWS";
import type { DeviceStatus } from "../net/deviceStatus";
import "./DeviceUnreachable.css";

// How often to check whether the device is answering again. Cheap enough
// to be frequent (one GetPubKey, no auth, no device work beyond generating
// a keypair) and slow enough not to hammer the bridge with one client's
// reconnect attempts.
const cRetryMs = 5000;

export default function DeviceUnreachable({ status }: { status: DeviceStatus }) {
  const disabled = status.code === "account_disabled";

  // This screen replaces the whole app (see App.tsx), which means every
  // component that would normally be polling is unmounted behind it - so
  // without this, nothing would ever issue the request whose success is
  // what clears this screen, and it would sit here until a manual reload
  // even after the device came back. Caught exactly that way in testing:
  // the device returned, and the page kept insisting it hadn't.
  //
  // Any successful response clears the state on its own (see
  // deviceStatus.noteResponse), so this only has to make a request, not
  // interpret one. Not polled for a disabled account: that state lasts
  // until the owner does something about it, and retrying every few
  // seconds would be asking a question already answered.
  useEffect(() => {
    if (disabled) return;
    const id = setInterval(() => {
      useWS.request((e) => {
        (e as any).payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
      }).catch(() => {
        // Still down - the socket may not even be connectable yet. The
        // next tick tries again; there is nothing useful to report here
        // that this screen isn't already saying.
      });
    }, cRetryMs);
    return () => clearInterval(id);
  }, [disabled]);
  return (
    <div className="du-root">
      <div className="du-card">
        {disabled ? (
          <svg className="du-icon" width="56" height="56" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="1.5" />
            <path d="M5.6 5.6l12.8 12.8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        ) : (
          <svg className="du-icon" width="56" height="56" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path
              d="M17.5 18H7a4.5 4.5 0 0 1-.6-8.96 5.5 5.5 0 0 1 10.53 1.48A3.75 3.75 0 0 1 17.5 18Z"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinejoin="round"
            />
            <path d="M3 3l18 18" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        )}
        <h2>{disabled ? "This account has been disabled" : "This device isn't available right now"}</h2>
        {/* The device/bridge's own wording, shown as-is - it's the half of
            this that can actually say something specific. */}
        <p>{status.message}</p>
        {!disabled && (
          <p className="du-note">
            Nothing has been lost — everything stays on the device itself. This page keeps
            checking, and picks up on its own as soon as it's back.
          </p>
        )}
      </div>
    </div>
  );
}
