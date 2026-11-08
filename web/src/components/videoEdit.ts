// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Video edit (a cut by time, done by the device: EditVideo in
// messages.proto): which videos the viewer (MediaViewer.tsx) offers Edit
// for, what the cut screen (VideoEditor.tsx, on the composer's trimmer,
// VideoTrimmer.tsx) lets Save, what it sends, and which words Revert to
// original says for a video - pure, tested by
// web/scripts/video-edit-test.mjs. Everything else - the Save and copies
// questions, the answers, the refusals, what the page behind shows after,
// Revert itself - is the photo edit's (photoEdit.ts): a cut is an edit like
// a turn or a crop, with a hash of its own and the original kept. The
// phones' rules are the same (iOS and Android's video cut): change them
// together.
//
// Edit is offered for a video the device can cut, by its type: MP4, MOV,
// M4V, 3GP/3G2, MKV, WebM and AVI - or, for a file typed as nothing in
// particular (no type, application/octet-stream: an old upload), by its
// name. The device copies the streams into the container the video
// already is (no re-encoding), so a format it has no container for
// (MPEG-TS, WMV, FLV...) is refused as "not_editable" and never offered.
//
// The cut is the trimmer's two handles, in steps of a tenth of a second:
// Save is on for a real cut - not the whole video - that keeps at least
// half a second (less is refused as "too_short"). An end handle left at
// the video's end is sent as 0, "to the end": the slider's last step can
// fall short of a length that isn't a whole number of tenths.

// Types and uiText's types only: the module loads in the test with
// nothing of the app around it.
import type { ReqEnvelope } from "../proto/messages";
import type { EditHow } from "./photoEdit";
import type { PlainKey } from "./uiText";
import type { TrimRange } from "./videoTrim";

// The types the device cuts (files_manager/edit_video.go's cutFormats).
const cCutTypes = new Set([
  "video/mp4", "video/quicktime", "video/x-m4v", "video/3gpp", "video/3gpp2", "video/x-matroska", "video/webm", "video/x-msvideo",
]);
// The same by name, for a file typed as nothing in particular.
const cCutName = /\.(mp4|mov|m4v|3gp|3g2|mkv|webm|avi)$/i;
const cUntyped = new Set(["", "application/octet-stream"]);

// The trimmer's step: its handles move a tenth of a second at a time.
export const cCutStep = 0.1;
// The least a cut keeps: the trimmer's handles stay this far apart, and
// the device refuses less.
export const cMinCutSecs = 0.5;
// Room for the sums of tenths (0.1 * 3 is 0.30000000000000004).
const cSlack = 1e-3;

// A file's type without its parameters, in lower case.
const typeOf = (mime?: string) => (mime || "").split(";")[0].trim().toLowerCase();

/** Whether the viewer offers Edit (the cut) for a video: by its type, or
 *  - typed as nothing in particular - by its name. Never a photo, nor a
 *  video in a format the device can't cut. */
export function canCut(f: { path: string; mime?: string }): boolean {
  const mime = typeOf(f.mime);
  return cCutTypes.has(mime) || (cUntyped.has(mime) && cCutName.test(f.path));
}

/** The cut the handles choose, as the device takes it: from `startSecs`
 *  to `endSecs` (0: to the video's end), keeping `kept` seconds; `whole`
 *  when it keeps the whole video - nothing to save, and nothing that
 *  Cancel would lose. */
export type Cut = { startSecs: number; endSecs: number; kept: number; whole: boolean };

/** The cut of a video `duration` seconds long that the trimmer's range
 *  makes: a start at the slider's start is 0, and an end within a step of
 *  the video's end is its end. */
export function cutOf(range: TrimRange, duration: number): Cut {
  const start = range.start < cCutStep / 2 ? 0 : range.start;
  const toEnd = range.end <= 0 || range.end > duration - cCutStep + cSlack;
  const end = toEnd ? duration : range.end;
  return { startSecs: start, endSecs: toEnd ? 0 : end, kept: Math.max(0, end - start), whole: start === 0 && toEnd };
}

/** Whether the cut screen's Save is on: a real cut that keeps at least
 *  half a second. */
export const canSaveCut = (c: Cut): boolean => !c.whole && c.kept > cMinCutSecs - cSlack;

/** EditVideo's payload: the video's path and the content it was shown
 *  with (so a path holding other content by now is refused as
 *  "changed"), the cut, and how to save it - as EditImage's. */
export function cutPayload(item: { path: string; hash?: string }, c: Cut, how: EditHow): ReqEnvelope["payload"] {
  return {
    $case: "reqEditVideo",
    reqEditVideo: {
      path: item.path,
      hash: item.hash ?? "",
      startSecs: c.startSecs,
      endSecs: c.endSecs,
      asCopy: how.asCopy,
      // Every copy is a Replace's alone: a copy changes no path.
      everyCopy: !how.asCopy && how.every,
    },
  };
}

/** Whether a file is a video, for the words the edit's questions say: by
 *  its type, or as canCut finds one by its name. */
export const isVideoEdit = (f: { path: string; mime?: string }): boolean => typeOf(f.mime).startsWith("video/") || canCut(f);

/** What Revert to original's plain question says (one path holds the
 *  edit): a cut undone for a video, the edit removed for a photo. */
export const revertTextKey = (f: { path: string; mime?: string }): PlainKey =>
  isVideoEdit(f) ? "web.images.edit.video_revert_text" : "web.images.edit.revert_text";
