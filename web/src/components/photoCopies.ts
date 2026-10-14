// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #201: deleting from Images a photo the device holds at several
// paths. Since #201 Images lists such a photo once, and every entry of a
// SearchPhotos answer says how many paths hold its content (File.copies,
// unset from devices before #201 and on a page whose count failed). Above
// 1, Delete asks whether to delete only the copy shown or every copy
// (DelFile.every_copy); every delete also says which content it showed
// (DelFile.hash), so a path synced over since is refused ("changed")
// instead of deleted. The rules here are pure (tested by
// web/scripts/photo-copies-test.mjs) and the same as the phones' (iOS
// PhotoGallery, Android PhotoGalleryViewModel): change them together.

/** What the delete confirmation asks. */
export type CopiesChoice =
  // Nothing about copies: the dialog as it was, one red Delete.
  | { kind: "plain" }
  // One photo with `count` copies: "Delete only this copy" or "Delete all
  // {count} copies".
  | { kind: "single"; count: number }
  // Several photos, `count` of them with copies: "Delete only these copies"
  // or "Delete every copy".
  | { kind: "several"; count: number };

type Copies = { copies?: number };

/** Whether "every copy" can be offered for (and sent with) this photo. */
export const hasCopies = (f: Copies): boolean => (f.copies ?? 0) > 1;

/** The question for deleting `files` (the viewer's one photo, or the
 *  selection). The count is the one the tiles carry, maybe stale: "all"
 *  deletes whatever copies exist then. */
export function copiesChoice(files: readonly Copies[]): CopiesChoice {
  if (files.length === 1) {
    const n = files[0].copies ?? 0;
    return n > 1 ? { kind: "single", count: n } : { kind: "plain" };
  }
  const count = files.filter(hasCopies).length;
  return count > 0 ? { kind: "several", count } : { kind: "plain" };
}

/** DelFile's fields for one photo: its path, the hash it was shown with
 *  (when it has one), and every_copy only when asked for and the photo has
 *  copies - never when `copies` is unset, as an older device would ignore
 *  it and delete only the path while the dialog said "all". */
export function delFileFields(f: { path: string; hash?: string; copies?: number }, every: boolean): { path: string; hash?: string; everyCopy?: boolean } {
  const out: { path: string; hash?: string; everyCopy?: boolean } = { path: f.path };
  if (f.hash) out.hash = f.hash;
  if (every && hasCopies(f)) out.everyCopy = true;
  return out;
}

/** How one DelFile went. upload_only: the path is in an upload-only
 *  folder; copy_upload_only: "every copy" refused because a copy is in
 *  one; changed: the path holds other content now. In all three nothing
 *  was deleted. */
export type DeleteOutcome = "deleted" | "upload_only" | "copy_upload_only" | "changed" | "failed";

/** The outcome of an answer: `ok` an Ack that says so, `code` its
 *  error_code. */
export function deleteOutcome(ok: boolean, code: string): DeleteOutcome {
  if (ok) return "deleted";
  if (code === "upload_only" || code === "copy_upload_only" || code === "changed") return code;
  return "failed";
}

/** What a delete of several says once done: "done", or why `count` of
 *  them are still there - one reason when they all share it, else
 *  "failed". `reload`: something changed on the device since the grid
 *  showed it, so the grid is loaded again. */
export function deleteSummary(outcomes: readonly DeleteOutcome[]): {
  kind: "done" | Exclude<DeleteOutcome, "deleted">;
  count: number;
  reload: boolean;
} {
  const kept = outcomes.filter((o) => o !== "deleted");
  const reload = kept.includes("changed");
  if (!kept.length) return { kind: "done", count: 0, reload };
  const first = kept[0] as Exclude<DeleteOutcome, "deleted">;
  return { kind: kept.every((o) => o === first) ? first : "failed", count: kept.length, reload };
}

/** What a delete says once done, the same on the phones: a single
 *  refusal ("changed" or "copy_upload_only", whatever the others did) in
 *  the device's own words (`said`, that refusal's error_message), which
 *  name the folder; two or more summed up as deleteSummary does (one
 *  reason when they share it, else "failed"), and so is a refusal the
 *  device gave no words for. */
export function deleteNotice(outcomes: readonly DeleteOutcome[], said: string):
  | { kind: "said"; text: string; count: 1; reload: boolean }
  | ReturnType<typeof deleteSummary> {
  const sum = deleteSummary(outcomes);
  if (sum.count === 1 && said && (sum.kind === "changed" || sum.kind === "copy_upload_only")) {
    return { kind: "said", text: said, count: 1, reload: sum.reload };
  }
  return sum;
}
