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
