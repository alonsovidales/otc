// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A person's face, chosen by the owner (SetPersonCover; owner, 2026-10-31):
// which photo Use this face takes, the picture of the owner's own the
// device takes, the request, and what its answer means - pure, tested by
// web/scripts/person-cover-test.mjs. libraryStore.ts' choosePersonCover
// sends it; Use this face is in Images' selection bar and the viewer, the
// rest in the menu of the face at the top of their photos (PhotoGallery.tsx's
// PersonFace). The phones offer the same (app.photos.person.cover_*).
import type { ReqEnvelope, RespEnvelope, SetPersonCover } from "../proto/messages";
import { asIs, say, type UiText } from "./uiText";

/** The face chosen: one of their photos' (its content hash), a picture of
 *  the owner's own (the crop's JPEG), or undefined - the device's choice
 *  again (Choose automatically). */
export type CoverChoice = SetPersonCover["cover"];

/** The photo whose face Use this face takes: the one photo picked (the
 *  selection bar's, the viewer's), by its content hash. Null - no button -
 *  for none or several, a video, or one without a hash. */
export function faceHash(items: readonly { mime?: string; hash?: string }[]): string | null {
  if (items.length !== 1) return null;
  const [f] = items;
  if ((f.mime || "").startsWith("video/") || !f.hash) return null;
  return f.hash;
}

/** The biggest picture of their own the device takes for a face. */
export const cCoverMaxBytes = 256 * 1024;

/** Whether the device takes this picture: a JPEG (it starts FF D8 FF) of
 *  at most cCoverMaxBytes. The crop (PhotoCropDialog.tsx) makes one far
 *  smaller; this is the check before it goes. */
export function coverImageFits(jpeg: Uint8Array): boolean {
  return jpeg.byteLength >= 3 && jpeg.byteLength <= cCoverMaxBytes && jpeg[0] === 0xff && jpeg[1] === 0xd8 && jpeg[2] === 0xff;
}

/** The request's payload. */
export const coverPayload = (personId: string, cover: CoverChoice): ReqEnvelope["payload"] =>
  ({ $case: "reqSetPersonCover", reqSetPersonCover: { personId, cover } });

/** What the device answered: null once it took the face, else the note -
 *  its own words when it gave some (already in the page's language), ours
 *  when it didn't. */
export function coverOutcome(resp: Pick<RespEnvelope, "error" | "errorMessage" | "payload">): UiText | null {
  const p = resp.payload;
  if (!resp.error && p?.$case === "respAck" && p.respAck.ok) return null;
  const said = resp.errorMessage || (p?.$case === "respAck" ? p.respAck.errorMsg : "");
  return said ? asIs(said) : say("web.images.person.cover_failed");
}
