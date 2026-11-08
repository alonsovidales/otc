// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Photo edit (rotate and crop, done by the device: EditImage, RevertImage
// and GetEditState in messages.proto): which photos the viewer
// (MediaViewer.tsx) offers Edit and Revert to original for, what the editor
// (PhotoEditor.tsx) and the viewer send, how they read the answers, and
// what the grid behind shows after - pure, tested by
// web/scripts/photo-edit-test.mjs. The crop frame's arithmetic is
// photoEditMath.ts. The phones' rules are the same (iOS and Android's
// photo editor): change them together.
//
// Edit is offered for a photo of the owner's own library the device can
// edit, by its type: JPEG, PNG, HEIC/HEIF (or a name ending in .heic/.heif,
// whatever type a device gave it), AVIF and WebP - never a video (a video's
// Edit is the cut: videoEdit.ts), a GIF, a camera raw, a TIFF or an SVG,
// which the device refuses as "not_editable".
// Revert to original is offered for a photo that is an edit
// (File.original_hash, from ListFiles and SearchPhotos answers) - never for
// one compressed on the device (File.original_removed), whose original is
// gone.
//
// Saving either replaces the photo with the edit (in place, or - every
// copy - at every path holding it) or saves it as "<name> (edited).jpg"
// next to it. Before Replace, and before Revert, GetEditState says how many
// paths hold the photo: above 1 the owner is asked whether to change them
// all or only this one. A device that can't say (an older one answering
// "unknown_payload", or no answer) counts as one path.

// Types, the frame's arithmetic and uiText's helpers only: the module loads
// in the test with nothing of the app around it.
import type { File as MsgFile, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { cropOf, type EditView } from "./photoEditMath";
import { asIs, say, type UiText } from "./uiText";

// The types the device edits (files_manager/edit_image.go's editKindOf).
const cEditableTypes = new Set(["image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp"]);
// A HEIC or HEIF photo by its name: a device or a sync may type one as
// something else (application/octet-stream).
const cHeifName = /\.hei[cf]$/i;

/** Whether the viewer offers Edit for a photo: by its type, or a HEIC or
 *  HEIF by its name - never a video. */
export function canEdit(f: { path: string; mime?: string }): boolean {
  const mime = (f.mime || "").split(";")[0].trim().toLowerCase();
  if (mime.startsWith("video/")) return false;
  return cEditableTypes.has(mime) || cHeifName.test(f.path);
}

/** Whether the viewer offers Revert to original: the photo is an edit,
 *  and not a compressed copy whose original the device deleted
 *  (File.original_removed: nothing to go back to - compress.ts). */
export const canRevert = (f: { originalHash?: string; originalRemoved?: boolean }): boolean => !!f.originalHash && !f.originalRemoved;

/** How an edit is saved: as a copy next to the photo, or in its place -
 *  `every`: at every path holding the photo (the copies question's "all"). */
export type EditHow = { asCopy: boolean; every: boolean };

/** EditImage's payload: the photo's path and the content it was shown
 *  with (so a path holding other content by now is refused as "changed"),
 *  the turns and the frame (none for the whole photo), and how to save. */
export function editPayload(item: { path: string; hash?: string }, v: EditView, how: EditHow): ReqEnvelope["payload"] {
  return {
    $case: "reqEditImage",
    reqEditImage: {
      path: item.path,
      hash: item.hash ?? "",
      quarterTurns: (((v.turns % 4) + 4) % 4),
      crop: cropOf(v.rect),
      asCopy: how.asCopy,
      // Every copy is a Replace's alone: a copy changes no path.
      everyCopy: !how.asCopy && how.every,
    },
  };
}

/** RevertImage's payload: the original back at the photo's path, or at
 *  every path holding the edit. */
export const revertPayload = (item: { path: string; hash?: string }, every: boolean): ReqEnvelope["payload"] =>
  ({ $case: "reqRevertImage", reqRevertImage: { path: item.path, hash: item.hash ?? "", everyCopy: every } });

/** GetEditState's payload, asked before Replace and before Revert. */
export const editStatePayload = (path: string): ReqEnvelope["payload"] =>
  ({ $case: "reqGetEditState", reqGetEditState: { path } });

/** How many paths hold the photo, from GetEditState's answer (null: none
 *  came): at least 1, and 1 for anything but the answer - an older device
 *  ("unknown_payload"), a refusal - which then asks nothing about copies. */
export function copiesFrom(resp: Pick<RespEnvelope, "error" | "payload"> | null): number {
  const p = resp?.payload;
  if (!resp || resp.error || p?.$case !== "respEditState") return 1;
  return Math.max(1, p.respEditState.copies || 1);
}

/** What the question before Replace or Revert asks: plainly, or - the
 *  photo held at `count` paths - all of them or only here. */
export type CopiesAsk = { kind: "plain" } | { kind: "copies"; count: number };
export const copiesAsk = (copies: number): CopiesAsk => (copies > 1 ? { kind: "copies", count: copies } : { kind: "plain" });

/** EditImage's or RevertImage's answer: the file now (the edit, the copy,
 *  or the original put back), or a refusal - `said`, the device's own
 *  words in the owner's language ("" when it gave none, or none came: the
 *  caller says it failed), and its error_code ("changed", "not_editable",
 *  "storage_full", "not_edited", "unknown_payload", "demo"...). */
export type EditAnswer = { kind: "file"; file: MsgFile } | { kind: "refused"; said: string; code: string };

export function editAnswer(resp: Pick<RespEnvelope, "error" | "errorCode" | "errorMessage" | "payload">): EditAnswer {
  const p = resp.payload;
  if (!resp.error && p?.$case === "respFile" && p.respFile.path) return { kind: "file", file: p.respFile };
  const ackSaid = p?.$case === "respAck" ? p.respAck.errorMsg : "";
  return { kind: "refused", said: resp.errorMessage || ackSaid || "", code: resp.errorCode || "" };
}

/** What a refusal of EditImage (or, `revert`, of RevertImage) comes to:
 *  its words - the device's, in the owner's language, else that it failed -
 *  and whether it closes the editor and the viewer. "changed" does, as the
 *  viewer's Delete does for it and the phones do: the path holds other
 *  content now, so trying again would only fail the same way - the page
 *  says the words and lists its photos again. Any other refusal stays in
 *  the question, and Save (or Revert) can be tried again. */
export function refusalOf(a: { said: string; code: string }, revert: boolean): { closes: boolean; text: UiText } {
  const text = a.said ? asIs(a.said) : say(revert ? "web.images.edit.revert_failed" : "web.images.edit.failed");
  return { closes: a.code === "changed", text };
}

/** A saved edit or a revert, as the viewer hands it to the page behind it.
 *  `kind`: the photo replaced by the edit, the edit saved as a copy, or the
 *  original put back. `file`: the device's answer (a new hash; a Replace's
 *  or a Revert's path can differ - a HEIC becomes IMG_1.JPG and back).
 *  `thumb`: a small JPEG of it for the tile until the device has made its
 *  own (the edit drawn by the editor, the original's thumbnail fetched),
 *  null when there is none. `copies`: how many paths held the photo before
 *  (GetEditState; 1 when unknown), and `every`: whether all of them
 *  changed - when not, the others still hold the photo as it was. */
export type EditDone = {
  kind: "replace" | "copy" | "revert";
  file: MsgFile;
  thumb: Blob | null;
  copies: number;
  every: boolean;
};

/** Whether the change left other paths holding what the photo was before
 *  (Replace or Revert "only here" of a photo held at several): a page that
 *  shows a photo once, by its content (Images), lists them again then. */
export const leftOthers = (d: Pick<EditDone, "kind" | "copies" | "every">): boolean =>
  d.kind !== "copy" && !d.every && d.copies > 1;

/** The file a page shows after the change, from the one it showed: the
 *  answer's path, hash, type, size, dates and original over the old ones,
 *  with what the answer doesn't carry kept (a Replace's or a Revert's
 *  rating and collection place go with the photo). How many paths hold
 *  it: the answer's when it says, else - all of them changed - as before,
 *  else unknown (a delete then asks nothing about copies). A copy keeps
 *  only the photo's date. */
export function shownAfter(shown: MsgFile, d: Pick<EditDone, "kind" | "file" | "every">): MsgFile {
  const a = d.file;
  const base: Partial<MsgFile> = d.kind === "copy" ? { created: shown.created } : shown;
  const copies = a.copies && a.copies > 0 ? a.copies : d.kind !== "copy" && d.every ? shown.copies : undefined;
  return {
    ...base,
    ...a,
    created: a.created ?? base.created,
    modified: a.modified ?? base.modified,
    content: undefined,
    copies,
    rating: d.kind === "copy" ? undefined : (a.rating ?? shown.rating),
  };
}

/** A path's last part: the copy's name in "Saved as {name}". */
export const leafOf = (path: string): string => path.split("/").pop() || path;
