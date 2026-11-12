// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A photo's faces in the viewer's Info (owner, 2026-11-01, from a user: "see
// what face it has, especially on new images ... and be able to remove faces
// from there"), and the people added to a photo or video by hand (owner,
// 2026-11-09: "in the faces we will have an add button, after clicking we
// can just select one of the faces that are not already in the photo"): the
// rules MediaViewer.tsx's People block follows - pure, tested by
// web/scripts/photo-faces-test.mjs. Info asks GetPhotoFaces for the photo or
// video on screen while it is open; the answer's faces, left to right, then
// the people added by hand, are listed by name with their crop, a row opens
// that person's photos in Images (photoFilter.ts' showPersonPhotos), and a
// face's × is Not this person for that one photo (personFix.tsx's dialog,
// RemoveFromPerson with the answer's hash) - a by-hand row's takes them off
// at once, as there was no face to be wrong about. Where the device says it
// takes AddToPerson (PhotoFaces.adds_people), Add a person ends the list:
// People's people not in it already (personFix.tsx's AddToPersonDialog).
// Nothing shows while face recognition is off - and, since release 180,
// Pets too (a pet's face is one of the faces) - for a photo with no faces
// and no Add, or on a device from before (error_code "unknown_payload"; an
// older device answers a video with no faces, and never offers Add).
import { isBridgeAnswer } from "../net/deviceStatus";
import type { Person, PhotoFace, ReqEnvelope, RespEnvelope } from "../proto/messages";
import type { PhotoFilter } from "./photoFilter";

/** The request's payload: the faces of the photo (or video) at `path`. */
export const facesPayload = (path: string): ReqEnvelope["payload"] =>
  ({ $case: "reqGetPhotoFaces", reqGetPhotoFaces: { path } });

/** What an answer means for Info: the faces to list (none while face
 *  recognition is off), the content hash, and whether the device takes
 *  AddToPerson (never while face recognition is off); a device that
 *  doesn't know the request ("unknown", not asked again on this page); or
 *  nothing to show ("failed": refused, or not this answer). */
export type FacesAnswer =
  | { kind: "faces"; faces: PhotoFace[]; hash: string; addsPeople: boolean }
  | { kind: "unknown" }
  | { kind: "failed" };

export function facesAnswer(resp: Pick<RespEnvelope, "error" | "errorCode" | "payload">): FacesAnswer {
  if (resp.errorCode === "unknown_payload") return { kind: "unknown" };
  const p = resp.payload;
  if (resp.error || p?.$case !== "respPhotoFaces") return { kind: "failed" };
  const r = p.respPhotoFaces;
  const on = r.faceRecognition;
  return { kind: "faces", faces: on ? r.faces : [], hash: r.hash, addsPeople: on && r.addsPeople };
}

/** Whether Info asks for an item's faces: a photo or a video (its people
 *  added by hand; a device from before answers one with none), while
 *  People isn't known to be off (faceRecognition.ts' peopleOn: face
 *  recognition or Pets; null is not known yet, and the device's answer
 *  says) on a device that knows the request. */
export function asksFaces(item: object | undefined, peopleOn: boolean | null, unknown: boolean): boolean {
  return !!item && peopleOn !== false && !unknown;
}

/** Whether Info offers Add a person: the device said it takes AddToPerson
 *  (only while face recognition or Pets is on), People isn't known to be
 *  off here since, and the content's hash is known (removalHash). */
export const offersAdd = (addsPeople: boolean, peopleOn: boolean | null, hash: string): boolean =>
  addsPeople && peopleOn !== false && hash !== "";

/** Whether Info shows its People block: faces to list, or Add to offer. */
export const showsPeople = (faces: readonly PhotoFace[], add: boolean): boolean => faces.length > 0 || add;

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

/** A row's key: one person may have two faces in a photo (a by-hand row,
 *  one per person, has no box). */
export const faceKey = (f: Pick<PhotoFace, "personId" | "x" | "y" | "w" | "h">): string =>
  `${f.personId}@${f.x},${f.y},${f.w},${f.h}`;

/** The face's remove button to focus once a face went (keyboard use): the
 *  one now at the place it had, else the last; -1 when none is left. */
export const focusAfterRemoval = (removedAt: number, left: number): number =>
  left <= 0 ? -1 : Math.min(removedAt, left - 1);

/** Whom Add a person offers: People's people, in People's order, but for
 *  everyone in the photo or video already (a face of theirs, or added by
 *  hand). */
export function notInIt(people: readonly Person[], faces: readonly Pick<PhotoFace, "personId">[]): Person[] {
  const inIt = new Set(faces.map((f) => f.personId));
  return people.filter((p) => !inIt.has(p.id));
}

/** What Add a person's list says when it offers no one: People has no one
 *  yet ("none"), or everyone it has is in it already ("all"). */
export const addNone = (people: number): "none" | "all" => (people === 0 ? "none" : "all");

/** AddToPerson's payload: the person picked, put in this one content. */
export const addPayload = (personId: string, hash: string): ReqEnvelope["payload"] =>
  ({ $case: "reqAddToPerson", reqAddToPerson: { personId, hashes: [hash] } });

/** RemoveFromPerson's payload for a row added by hand: off them, to no one
 *  (no question first: nothing face recognition learns, and Add puts them
 *  back). */
export const byHandRemovalPayload = (personId: string, hash: string): ReqEnvelope["payload"] =>
  ({ $case: "reqRemoveFromPerson", reqRemoveFromPerson: { personId, hashes: [hash], toPersonId: "" } });

/** What the generic Ack to AddToPerson (or a by-hand row's
 *  RemoveFromPerson) means: done; refused in the device's words (in the
 *  request's language); the bridge's answer that the device isn't there
 *  ("unreachable": its words are taken out, deviceStatus.ts); or nothing
 *  usable ("failed": no words, or a device that doesn't know the request -
 *  its words name the request, not the person). */
export type AckOutcome = { kind: "done" } | { kind: "refused"; said: string } | { kind: "unreachable" } | { kind: "failed" };

export function ackOutcome(resp: RespEnvelope): AckOutcome {
  const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : undefined;
  if (!resp.error && ack?.ok) return { kind: "done" };
  if (isBridgeAnswer(resp)) return { kind: "unreachable" };
  if (resp.errorCode === "unknown_payload") return { kind: "failed" };
  const said = resp.errorMessage || ack?.errorMsg || "";
  return said ? { kind: "refused", said } : { kind: "failed" };
}

/** The row to focus once a person was added and the device answered
 *  again: theirs (the last of theirs: added by hand, they come after the
 *  faces); -1 when they aren't listed (the device left the content as it
 *  was) - the Add button then. */
export function addedAt(faces: readonly Pick<PhotoFace, "personId">[], personId: string): number {
  for (let i = faces.length - 1; i >= 0; i--) if (faces[i].personId === personId) return i;
  return -1;
}

/** Whether Images already shows exactly this person's photos, nothing
 *  narrowing them further: a face for them in Info then only closes the
 *  viewer, and the grid stays as it was. */
export const onlyPerson = (f: PhotoFilter, personId: string): boolean =>
  f.personIds.length === 1 && f.personIds[0] === personId && f.tags.length === 0 && f.group == null && f.area == null && f.text === "";
