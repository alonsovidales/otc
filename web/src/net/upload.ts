// SPDX-License-Identifier: AGPL-3.0-or-later

// A browser upload in pieces of at most 4 MB (BeginUpload, UploadChunk,
// FinishUpload), as the apps already send them. A whole-file ReqUploadFile
// made the device hold the file two to three times over while reading and
// decoding the one message - about 1.7 GB for a 700 MB video - against a
// memory budget that counted it once. Reading a piece at a time also spares
// the browser loading the whole file.
//
// Resumable (issue #205, as the apps' ChunkedUpload): a file of more than
// one piece is hashed first (sha256.ts, a piece at a time: crypto.subtle
// would need the whole file in memory, and a plain-HTTP page has none) and
// BeginUpload carries the hash, which with the path and size is the
// device's resume key. A piece that fails on the way - the socket gone, no
// answer in time, the bridge answering for a device it lost, an answer the
// device didn't take - is begun again with the same key after a pause
// (cResumePauses), and the upload goes on from what the device holds
// (UploadStarted.received): a Wi-Fi blip in a 2 GB upload costs the piece
// on its way, not the gigabytes before it. At most cMaxResumes in a row
// without the device getting further than it ever had (a device before
// release 166 answers 0, so it must pass that point again first); then the
// error is the caller's. The first BeginUpload, storage_full and
// FinishUpload are never resumed. A file of one piece goes without a key,
// as before: sending it again costs no more than resuming it.
//
// Tested by web/scripts/transfer-test.mjs against a stand-in device.
import { useWS } from "./useWS";
import { Sha256 } from "./sha256";
import { isBridgeAnswer } from "./deviceStatus";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";

// The most the device takes in one piece (files_manager.MaxChunk).
export const CHUNK = 4 << 20;

/** The pauses before each try at resuming; past the last, the last again. */
export const cResumePauses = [2_000, 4_000, 8_000, 16_000, 30_000, 30_000];
/** Tries at resuming in a row, without the device getting further than it
 *  ever had, before the upload fails. */
export const cMaxResumes = cResumePauses.length;

/** What an upload needs from outside: the socket and a clock (the tests'
 *  own stand-ins). */
export type UploadDeps = {
  send: (payload: ReqEnvelope["payload"]) => Promise<RespEnvelope>;
  sleep: (ms: number) => Promise<void>;
};

const live: UploadDeps = {
  send: (payload) => useWS.request((e: Partial<ReqEnvelope>) => { e.payload = payload; }),
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
};

export type UploadOptions = {
  forceOverride?: boolean;
  /** How many bytes the device holds, after each piece (and from where a
   *  resumed upload goes on). */
  onProgress?: (sent: number) => void;
  /** How much of the file has been read for its hash, while it is. */
  onHashing?: (read: number) => void;
  /** Its hash already worked out (contentKey), for a file sent again under
   *  another name. */
  key?: string;
};

/** The SHA-256 of a file (lowercase hex), read a piece at a time. */
export async function contentKey(file: Blob, onRead?: (read: number) => void): Promise<string> {
  const h = new Sha256();
  for (let off = 0; off < file.size; off += CHUNK) {
    h.update(new Uint8Array(await file.slice(off, off + CHUNK).arrayBuffer()));
    onRead?.(Math.min(off + CHUNK, file.size));
  }
  return h.hex();
}

// uploadFile stores file at path. It resolves with what ReqUploadFile
// answered: the stored respFile, or the device's error reply (from
// whichever step failed). Like ReqUploadFile it sends no dates or cloud id.
// onProgress hears how many bytes the device has taken, after each piece.
export function uploadFile(path: string, file: File, forceOverride = false, onProgress?: (sent: number) => void, onHashing?: (read: number) => void): Promise<RespEnvelope> {
  return uploadWith(live, path, file, { forceOverride, onProgress, onHashing });
}

/** uploadFile with the socket and clock given (tests). */
export async function uploadWith(deps: UploadDeps, path: string, file: Blob, opts: UploadOptions = {}): Promise<RespEnvelope> {
  const key = file.size > CHUNK ? opts.key ?? await contentKey(file, opts.onHashing) : "";
  const begin = () => deps.send({
    $case: "reqBeginUpload",
    reqBeginUpload: { path, size: BigInt(file.size), forceOverride: !!opts.forceOverride, cloudId: "", sha256: key },
  });
  const started = await begin();
  if (started.error || started.payload?.$case !== "respUploadStarted") return started;
  let uploadId = started.payload.respUploadStarted.uploadId;
  // What the device holds of this content already (an earlier try's).
  const held = (r: bigint) => Math.min(Math.max(Number(r), 0), file.size);
  let off = held(started.payload.respUploadStarted.received);
  let furthest = off;
  let tries = 0;
  if (off > 0) opts.onProgress?.(off);

  while (off < file.size) {
    // Strictly one piece at a time: the device handles a connection's
    // requests concurrently, so pieces sent together could arrive out of
    // order.
    let failure: unknown;
    try {
      const data = new Uint8Array(await file.slice(off, off + CHUNK).arrayBuffer());
      const resp = await deps.send({
        $case: "reqUploadChunk",
        reqUploadChunk: { uploadId, offset: BigInt(off), data },
      });
      if (resp.payload?.$case === "respUploadProgress" && resp.payload.respUploadProgress.received === BigInt(off + data.length)) {
        off += data.length;
        if (off > furthest) { furthest = off; tries = 0; }
        opts.onProgress?.(off);
        continue;
      }
      // The disk is full: no resume makes room. Without a key there is
      // nothing to resume with: the device's own answer, as before.
      if (resp.errorCode === "storage_full" || !key) {
        if (resp.error) return resp;
        throw new Error(`the device stopped taking the file at ${off} bytes`); // i18n-ignore: only logged, never shown
      }
      failure = resp;
    } catch (e) {
      if (!key) throw e;
      failure = e;
    }

    // Begun again with the same key: the device says how much it holds.
    for (;;) {
      if (tries >= cMaxResumes) {
        if (failure && typeof failure === "object" && "payload" in failure) return failure as RespEnvelope;
        throw failure;
      }
      await deps.sleep(cResumePauses[Math.min(tries, cResumePauses.length - 1)]);
      tries++;
      let again: RespEnvelope;
      try {
        again = await begin();
      } catch (e) {
        failure = e;
        continue;
      }
      if (again.payload?.$case === "respUploadStarted") {
        uploadId = again.payload.respUploadStarted.uploadId;
        off = held(again.payload.respUploadStarted.received);
        // The piece whose answer was lost may have reached it.
        if (off > furthest) { furthest = off; tries = 0; }
        opts.onProgress?.(off);
        break;
      }
      // The device turning it down (the disk full, too many uploads): its
      // answer. The bridge answering for it: another try.
      if (again.error && !isBridgeAnswer(again)) return again;
      failure = again;
    }
  }

  return deps.send({ $case: "reqFinishUpload", reqFinishUpload: { uploadId, sha256: key } });
}

// uploadAsNew stores file in dir (with its slash) under its own name, or
// "name (2).jpg" and on when the device holds other content at that path
// (duplicated_file): never over a file already there. It resolves as
// uploadFile does.
export async function uploadAsNew(dir: string, file: File, onProgress?: (sent: number) => void): Promise<RespEnvelope> {
  const dot = file.name.lastIndexOf(".");
  const base = dot > 0 ? file.name.slice(0, dot) : file.name;
  const ext = dot > 0 ? file.name.slice(dot) : "";
  // Hashed once, whatever name it ends up under.
  const key = file.size > CHUNK ? await contentKey(file) : "";
  for (let n = 1; ; n++) {
    const name = n === 1 ? file.name : `${base} (${n})${ext}`;
    const resp = await uploadWith(live, dir + name, file, { onProgress, key });
    if (resp.errorCode !== "duplicated_file" || n >= 20) return resp;
  }
}
