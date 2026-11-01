// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A photo's faces in the viewer's Info (owner, 2026-11-01, from a user: "see
// what face it has, especially on new images ... and be able to remove faces
// from there"): the rules MediaViewer.tsx's People block follows - pure,
// tested by web/scripts/photo-faces-test.mjs. Info asks GetPhotoFaces for
// the photo on screen while it is open (never for a video); the answer's
// faces, left to right, are listed by name with their crop, a face opens
// that person's photos in Images (photoFilter.ts' showPersonPhotos), and its
// × is Not this person for that one photo (personFix.tsx's dialog,
// RemoveFromPerson with the answer's hash). Nothing shows while face
// recognition is off, for a photo with no faces, or on a device from before
// (error_code "unknown_payload").
import type { PhotoFace, ReqEnvelope, RespEnvelope } from "../proto/messages";
import type { PhotoFilter } from "./photoFilter";

/** The request's payload: the faces of the photo at `path`. */
export const facesPayload = (path: string): ReqEnvelope["payload"] =>
  ({ $case: "reqGetPhotoFaces", reqGetPhotoFaces: { path } });

/** What an answer means for Info: the faces to list (none while face
 *  recognition is off) and the photo's content hash; a device that doesn't
 *  know the request ("unknown", not asked again on this page); or nothing
 *  to show ("failed": refused, or not this answer). */
export type FacesAnswer =
  | { kind: "faces"; faces: PhotoFace[]; hash: string }
  | { kind: "unknown" }
  | { kind: "failed" };

export function facesAnswer(resp: Pick<RespEnvelope, "error" | "errorCode" | "payload">): FacesAnswer {
  if (resp.errorCode === "unknown_payload") return { kind: "unknown" };
  const p = resp.payload;
  if (resp.error || p?.$case !== "respPhotoFaces") return { kind: "failed" };
  const r = p.respPhotoFaces;
  return { kind: "faces", faces: r.faceRecognition ? r.faces : [], hash: r.hash };
}

/** Whether Info asks for an item's faces: a photo, not a video, while face
 *  recognition isn't known to be off (faceRecognition.ts: null is not known
 *  yet, and the device's answer says) on a device that knows the request. */
export function asksFaces(item: { mime?: string } | undefined, faceRecognition: boolean | null, unknown: boolean): boolean {
  return !!item && !(item.mime || "").startsWith("video/") && faceRecognition !== false && !unknown;
}

/** The faces once a person's were taken off the photo: all of theirs go
 *  (RemoveFromPerson takes both when one person has two in it). */
export const withoutPerson = (faces: readonly PhotoFace[], personId: string): PhotoFace[] =>
  faces.filter((f) => f.personId !== personId);

/** The content hash a face's × names: the answer's, else the item's; ""
 *  when neither is known - no × then. */
export const removalHash = (answerHash: string, itemHash?: string): string => answerHash || itemHash || "";

/** Whether taking a face off the photo takes the photo out of the grid the
 *  viewer was opened on: only when that grid is that person's photos (the
 *  viewer's own Not this person then: the viewer goes on to the next photo,
 *  as after a delete). */
export const leavesGrid = (gridPersonId: string | undefined, facePersonId: string): boolean =>
  !!gridPersonId && gridPersonId === facePersonId;

/** A row's key: one person may have two faces in a photo. */
export const faceKey = (f: Pick<PhotoFace, "personId" | "x" | "y" | "w" | "h">): string =>
  `${f.personId}@${f.x},${f.y},${f.w},${f.h}`;

/** The face's remove button to focus once a face went (keyboard use): the
 *  one now at the place it had, else the last; -1 when none is left. */
export const focusAfterRemoval = (removedAt: number, left: number): number =>
  left <= 0 ? -1 : Math.min(removedAt, left - 1);

/** Whether Images already shows exactly this person's photos, nothing
 *  narrowing them further: a face for them in Info then only closes the
 *  viewer, and the grid stays as it was. */
export const onlyPerson = (f: PhotoFilter, personId: string): boolean =>
  f.personIds.length === 1 && f.personIds[0] === personId && f.tags.length === 0 && f.group == null && f.area == null && f.text === "";
