// SPDX-License-Identifier: AGPL-3.0-or-later

import forge from "node-forge";
import { ReqEnvelope, RespEnvelope } from "../proto/messages";

// The bridge relays already-decrypted app payloads between a device and a
// browser/friend client, so anything sent as plaintext in the protobuf
// envelope is readable by the bridge operator. To keep the account
// password confidential end-to-end (see issue #2), the device generates an
// ephemeral RSA keypair per WebSocket connection and hands out the public
// half via GetPubKey/PubKey; the client encrypts the password with it
// (RSA-OAEP/SHA-256) before it ever leaves the browser.
//
// This uses node-forge rather than the browser's native crypto.subtle:
// SubtleCrypto is only available in a "secure context" (HTTPS, or
// localhost/127.0.0.1), which a brand-new device being reached over plain
// http://<lan-ip-or-hostname> during first-run setup (issue #38/#39) isn't
// — crypto.subtle is simply undefined there, which used to crash this
// whole flow. node-forge does the same RSA-OAEP/SHA-256 math in pure JS,
// so it works regardless of secure-context status.

type Requester = (build: (e: Partial<ReqEnvelope>) => void) => Promise<RespEnvelope>;

// Issue #46: a plain browser tab has nothing else keeping the user signed
// in across a reload — the mobile app already gets the same effect for
// free by re-sending the Keychain-stored password on every launch (see
// App.tsx's `mobile` auto-auth effect). This mirrors that for the browser
// by persisting *something* across a reload and replaying it the same way
// on mount.
//
// Issue #101: that something used to be the actual account password,
// sitting in localStorage in plain text — anything that could read this
// origin's storage (an XSS, a shared/synced browser profile, a forensic
// grab of the profile directory) got the real password outright, not just
// a way back into one browser tab. It's now a random opaque token minted
// by the device itself (ReqIssueSessionToken/ReqAuthWithToken in
// messages.proto) that's good for nothing but resuming a session that was
// already established with the real password — see session/tokens.go for
// the server-side store this redeems against.
const cSessionTokenStorageKey = "otc_session_token";

// The token doesn't get to sit in localStorage forever just because the
// tab does — matches the old cPersistedKeyTTLMs window, and, like that
// one, keeps getting refreshed to a fresh hour on every successful
// redemption (see useWS.ts's authWithToken), so a tab reloaded regularly
// effectively never has to fall back to the password prompt.
const cPersistedTokenTTLMs = 60 * 60 * 1000;

interface PersistedToken {
  token: string;
  expiresAtMs: number;
}

export function savePersistedToken(token: string, expiresAtMs: number) {
  try {
    const entry: PersistedToken = { token, expiresAtMs };
    localStorage.setItem(cSessionTokenStorageKey, JSON.stringify(entry));
  } catch {
    // Storage can be unavailable (private browsing, quota) — session just
    // won't survive a reload in that case, not worth surfacing an error for.
  }
}

export function loadPersistedToken(): string | null {
  try {
    const raw = localStorage.getItem(cSessionTokenStorageKey);
    if (!raw) {
      return null;
    }
    const entry = JSON.parse(raw) as Partial<PersistedToken>;
    if (typeof entry.token !== "string" || typeof entry.expiresAtMs !== "number") {
      localStorage.removeItem(cSessionTokenStorageKey);
      return null;
    }
    // The server is the real authority on expiry (see session/tokens.go) —
    // this only skips a doomed round trip for a token that's already stale,
    // and distrusts an expiry claiming to sit further out than any token
    // the server would ever actually issue (a tampered or corrupted entry),
    // since a client-side check can only ever make this stricter, never
    // extend anything.
    if (Date.now() > entry.expiresAtMs || entry.expiresAtMs - Date.now() > cPersistedTokenTTLMs) {
      localStorage.removeItem(cSessionTokenStorageKey);
      return null;
    }
    return entry.token;
  } catch {
    return null;
  }
}

export function clearPersistedToken() {
  try {
    localStorage.removeItem(cSessionTokenStorageKey);
  } catch {
    // Nothing to clean up if storage isn't available in the first place.
  }
}

/**
 * Issue #39: every client calls GetPubKey before Auth anyway, and the
 * server rides along a `is_new_device` flag on that same response (true
 * when no owner secret has been set yet) — so this is a free way to tell
 * a fresh device apart from a normal login before showing a sign-in form.
 */
export async function isNewDevice(request: Requester): Promise<boolean> {
  const resp = await request((e) => {
    (e as any).payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
  });

  if (resp.payload?.$case !== "respPubKey") {
    return false;
  }

  return resp.payload.respPubKey.isNewDevice;
}

/** Fetches this connection's public key and RSA-OAEP(SHA-256) encrypts `plaintext` with it. */
export async function encryptForConnection(request: Requester, plaintext: string): Promise<Uint8Array> {
  const resp = await request((e) => {
    (e as any).payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
  });

  if (resp.payload?.$case !== "respPubKey") {
    const msg = resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : undefined;
    throw new Error(msg || "Unable to fetch the connection's public key");
  }

  const der = resp.payload.respPubKey.publicKey;
  // The server hands out PKIX/SubjectPublicKeyInfo DER (x509.MarshalPKIXPublicKey) —
  // exactly what forge.pki.publicKeyFromAsn1 expects.
  const derBuffer = forge.util.createBuffer(forge.util.binary.raw.encode(der));
  const publicKey = forge.pki.publicKeyFromAsn1(forge.asn1.fromDer(derBuffer)) as forge.pki.rsa.PublicKey;

  // Matches the server's rsa.DecryptOAEP(sha256.New(), ...): SHA-256 for
  // both the OAEP hash and MGF1, no label.
  const encrypted = publicKey.encrypt(
    forge.util.binary.raw.encode(new TextEncoder().encode(plaintext)),
    "RSA-OAEP",
    { md: forge.md.sha256.create(), mgf1: { md: forge.md.sha256.create() } },
  );

  return forge.util.binary.raw.decode(encrypted);
}
