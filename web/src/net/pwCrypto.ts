import { ReqEnvelope, RespEnvelope } from "../proto/messages";

// The bridge relays already-decrypted app payloads between a device and a
// browser/friend client, so anything sent as plaintext in the protobuf
// envelope is readable by the bridge operator. To keep the account
// password confidential end-to-end (see issue #2), the device generates an
// ephemeral RSA keypair per WebSocket connection and hands out the public
// half via GetPubKey/PubKey; the client encrypts the password with it
// (RSA-OAEP/SHA-256) before it ever leaves the browser.

type Requester = (build: (e: Partial<ReqEnvelope>) => void) => Promise<RespEnvelope>;

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
  const key = await crypto.subtle.importKey(
    "spki",
    der as BufferSource,
    { name: "RSA-OAEP", hash: "SHA-256" },
    false,
    ["encrypt"],
  );

  const encrypted = await crypto.subtle.encrypt(
    { name: "RSA-OAEP" },
    key,
    new TextEncoder().encode(plaintext),
  );

  return new Uint8Array(encrypted);
}
