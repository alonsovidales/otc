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

// Only a type: the module still loads with no imports in the test.
import type { TFunction } from "../i18n";

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

/** How a delete of several went, summed up (deleteNotice words it):
 *  "done", or why `count` of them are still there - one reason when they
 *  all share it, else "failed". `reload`: something changed on the
 *  device since the grid showed it, so the grid is loaded again. */
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

/** One photo or video of a delete: how it went, and the device's own
 *  words for a refusal (its error_message, already in the page's
 *  language; "" when it gave none or no answer came). */
export type DeleteResult = { outcome: DeleteOutcome; said?: string };

/** One line of what a delete says once done: a refusal in the device's
 *  own words (shown in "Delete failed: {detail}",
 *  web.images.delete.failed_detail, as the phones' delete_failed), or
 *  `count` photos still there for one reason (web.images.delete.<kind>). */
export type DeleteLine =
  | { kind: "said"; text: string }
  | { kind: Exclude<DeleteOutcome, "deleted">; count: number };

/** What a delete says once done (no lines: all deleted), the same on the
 *  phones (iOS DeleteCopies.keptText, Android deleteRefusalNotes). One
 *  photo still there: a "changed" or "copy_upload_only" refusal in the
 *  device's words, which name the folder, else its reason counted. Two or
 *  more for one reason: that reason, counted. Two or more for different
 *  reasons: the phones' rule - how many changed, how many have a copy in
 *  an upload-only folder, then every other refusal in the device's words
 *  (each text once), and those it gave no words for counted by reason.
 *  `reload`: something changed on the device since the grid showed it,
 *  so the grid is loaded again. */
export function deleteNotice(results: readonly DeleteResult[]): { lines: DeleteLine[]; reload: boolean } {
  const kept = results.filter((r) => r.outcome !== "deleted");
  const sum = deleteSummary(kept.map((r) => r.outcome));
  const reload = sum.reload;
  if (sum.kind === "done") return { lines: [], reload };
  if (kept.length === 1) {
    const said = kept[0].said ?? "";
    if (said && (sum.kind === "changed" || sum.kind === "copy_upload_only")) return { lines: [{ kind: "said", text: said }], reload };
    return { lines: [{ kind: sum.kind, count: 1 }], reload };
  }
  if (sum.kind !== "failed" || kept.every((r) => r.outcome === "failed")) return { lines: [{ kind: sum.kind, count: sum.count }], reload };
  const lines: DeleteLine[] = [];
  const counted = (kind: "changed" | "copy_upload_only") => {
    const count = kept.filter((r) => r.outcome === kind).length;
    if (count) lines.push({ kind, count });
  };
  counted("changed");
  counted("copy_upload_only");
  for (const r of kept) {
    if (r.outcome === "changed" || r.outcome === "copy_upload_only") continue;
    const said = r.said ?? "";
    if (said) {
      if (!lines.some((l) => l.kind === "said" && l.text === said)) lines.push({ kind: "said", text: said });
      continue;
    }
    const kind = r.outcome as "upload_only" | "failed";
    const line = lines.find((l): l is { kind: "upload_only" | "failed"; count: number } => l.kind === kind);
    if (line) line.count += 1;
    else lines.push({ kind, count: 1 });
  }
  return { lines, reload };
}

/** The device's words for one refusal shown by itself: "Delete failed:
 *  {detail}" - they are a lowercase piece written to follow it. */
export const refusedText = (t: TFunction, said: string): string => t("web.images.delete.failed_detail", { detail: said });

/** One line's text. */
export function deleteLineText(t: TFunction, line: DeleteLine): string {
  if (line.kind === "said") return refusedText(t, line.text);
  const count = line.count;
  if (line.kind === "upload_only") return t("web.images.delete.upload_only", { count });
  if (line.kind === "copy_upload_only") return t("web.images.delete.copy_upload_only", { count });
  if (line.kind === "changed") return t("web.images.delete.changed", { count });
  return t("web.images.delete.failed", { count });
}

/** The lines' text, one paragraph each (the toast keeps the breaks). */
export const deleteNoticeText = (t: TFunction, lines: readonly DeleteLine[]): string => lines.map((l) => deleteLineText(t, l)).join("\n");
