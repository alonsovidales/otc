// SPDX-License-Identifier: AGPL-3.0-or-later

// A browser upload in pieces of at most 4 MB (BeginUpload, UploadChunk,
// FinishUpload), as the apps already send them. A whole-file ReqUploadFile
// made the device hold the file two to three times over while reading and
// decoding the one message - about 1.7 GB for a 700 MB video - against a
// memory budget that counted it once. Reading a piece at a time also spares
// the browser loading the whole file.
import { useWS } from "./useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";

// The most the device takes in one piece (files_manager.MaxChunk).
const CHUNK = 4 << 20;

const send = (payload: ReqEnvelope["payload"]): Promise<RespEnvelope> =>
  useWS.request((e: Partial<ReqEnvelope>) => { e.payload = payload; });

// uploadFile stores file at path. It resolves with what ReqUploadFile
// answered: the stored respFile, or the device's error reply (from
// whichever step failed). Like ReqUploadFile it sends no dates or cloud id,
// and no hash: crypto.subtle is missing on a plain-HTTP LAN origin, and the
// device checks every piece's offset and the total size.
export async function uploadFile(path: string, file: File, forceOverride = false): Promise<RespEnvelope> {
  const begin = await send({
    $case: "reqBeginUpload",
    reqBeginUpload: { path, size: BigInt(file.size), forceOverride, cloudId: "" },
  });
  if (begin.error || begin.payload?.$case !== "respUploadStarted") return begin;
  const uploadId = begin.payload.respUploadStarted.uploadId;

  // Strictly one piece at a time: the device handles a connection's
  // requests concurrently, so pieces sent together could arrive out of order.
  for (let off = 0; off < file.size; off += CHUNK) {
    const data = new Uint8Array(await file.slice(off, off + CHUNK).arrayBuffer());
    const resp = await send({
      $case: "reqUploadChunk",
      reqUploadChunk: { uploadId, offset: BigInt(off), data },
    });
    if (resp.error) return resp;
    if (resp.payload?.$case !== "respUploadProgress" ||
        resp.payload.respUploadProgress.received !== BigInt(off + data.length)) {
      throw new Error(`the device stopped taking ${file.name} at ${off} bytes`);
    }
  }

  return send({ $case: "reqFinishUpload", reqFinishUpload: { uploadId, sha256: "" } });
}
