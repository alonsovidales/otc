// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { t } from "../i18n/runtime";

// The bridge relays already-decrypted app payloads between a device and a
// browser/friend client, so anything sent as plaintext in the protobuf
// envelope is readable by the bridge operator. To keep the account
// password confidential end-to-end (see issue #2), the device generates an
// ephemeral RSA keypair per WebSocket connection and hands out the public
// half via GetPubKey/PubKey; the client encrypts the password with it
// (RSA-OAEP/SHA-256) before it ever leaves the browser.
//
// The browser's own crypto.subtle does it wherever the page is a "secure
// context" (HTTPS, or localhost/127.0.0.1) - through the bridge, always.
// A device reached over plain http://<lan-ip-or-hostname> (first-run
// setup, issue #38/#39, or a home network without its certificate) has no
// crypto.subtle at all, which used to crash this whole flow: there
// node-forge does the same RSA-OAEP/SHA-256 math in pure JS. It is loaded
// only then (a chunk of its own, 287 KB), never for the pages that don't
// need it.

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

export interface DeviceSetupInfo {
  /** No owner secret set yet - show first-run setup, not a sign-in form. */
  isNewDevice: boolean;
  /**
   * Issue #85: false on an additional user's own instance (issue #82), so
   * the wizard can skip the storage/WiFi steps - those configure the
   * shared physical machine, which is the primary's alone.
   */
  isPrimary: boolean;
  /**
   * The device runs from the container image: the server it runs on
   * looks after its disks and network, so the wizard skips those steps
   * too.
   */
  container: boolean;
}

// This connection's GetPubKey answer: the device's key for this socket,
// with its setup flags (below) riding along.
function requestPubKey(request: Requester): Promise<RespEnvelope> {
  return request((e) => {
    (e as any).payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
  });
}

/**
 * Issue #39: every client calls GetPubKey before Auth anyway, and the
 * server rides along the flags below on that same response — so this is a
 * free way to tell a fresh device apart from a normal login, and a child
 * instance apart from the primary, before anyone is authenticated (which
 * is exactly when the setup wizard has to decide what to show).
 *
 * null when the answer isn't the device's key: the bridge answering for a
 * device it can't reach, say. That says nothing about the device, and
 * taking it for "not new" would let a new device take a sign-in's password
 * as its own.
 */
export async function getDeviceSetupInfo(request: Requester): Promise<DeviceSetupInfo | null> {
  const resp = await requestPubKey(request);
  if (resp.payload?.$case !== "respPubKey") return null;
  return {
    isNewDevice: resp.payload.respPubKey.isNewDevice,
    isPrimary: resp.payload.respPubKey.isPrimary,
    container: resp.payload.respPubKey.container,
  };
}

/** Fetches this connection's public key and RSA-OAEP(SHA-256) encrypts `plaintext` with it. */
export async function encryptForConnection(request: Requester, plaintext: string): Promise<Uint8Array> {
  return encryptWithPubKey(await requestPubKey(request), plaintext);
}

/**
 * The same with a GetPubKey answer already in hand, for a caller that looks
 * at the answer's flags first (useWS.sendAuth). Rejects when the answer
 * isn't a key. `withForge` is for the tests: the plain-HTTP way.
 */
export async function encryptWithPubKey(resp: RespEnvelope, plaintext: string, withForge = false): Promise<Uint8Array> {
  if (resp.payload?.$case !== "respPubKey") {
    const msg = resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : undefined;
    throw new Error(msg || t("web.shell.connection.no_key"));
  }

  // The server hands out PKIX/SubjectPublicKeyInfo DER
  // (x509.MarshalPKIXPublicKey), and decrypts with rsa.DecryptOAEP(
  // sha256.New(), ...): SHA-256 for both the OAEP hash and MGF1, no label.
  const der = resp.payload.respPubKey.publicKey;
  const data = new TextEncoder().encode(plaintext);
  const subtle = !withForge && globalThis.isSecureContext !== false ? globalThis.crypto?.subtle : undefined;
  if (subtle) {
    try {
      const key = await subtle.importKey("spki", new Uint8Array(der), { name: "RSA-OAEP", hash: "SHA-256" }, false, ["encrypt"]);
      return new Uint8Array(await subtle.encrypt({ name: "RSA-OAEP" }, key, data));
    } catch (err) {
      // A key it can't use: WebCrypto's errors often have no words.
      throw new Error((err as Error | undefined)?.message || t("web.shell.connection.no_key"));
    }
  }

  const forge = (await import("node-forge")).default;
  const derBuffer = forge.util.createBuffer(forge.util.binary.raw.encode(der));
  const publicKey = forge.pki.publicKeyFromAsn1(forge.asn1.fromDer(derBuffer)) as import("node-forge").pki.rsa.PublicKey;
  const encrypted = publicKey.encrypt(
    forge.util.binary.raw.encode(data),
    "RSA-OAEP",
    { md: forge.md.sha256.create(), mgf1: { md: forge.md.sha256.create() } },
  );
  return forge.util.binary.raw.decode(encrypted);
}
