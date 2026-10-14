// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #195: Files' Storage view - what takes the space in the open
// folder. The device sizes it only when asked (GetFolderUsage, answered by
// FolderUsage), which Files does only while this view shows; List and Grid
// never size folders. Each direct child comes with the bytes of every file
// under it and of their older versions, largest first in the device's own
// order (never sorted again here). The rules here are pure (tested by
// web/scripts/folder-usage-test.mjs) and the same as the phones' (iOS and
// Android FilesExplorerView): change them together.

/** A FolderUsageEntry as the web gets it (int64 fields are bigints). */
export type UsageEntry = { path: string; isDir: boolean; filesSize: bigint; fileCount: number; versionsSize: bigint };
export type Usage = { path: string; entries: UsageEntry[]; filesSize: bigint; fileCount: number; versionsSize: bigint };

/** A folder's path as Files and FolderUsage.path write it: "/a/b/". */
export const asFolderPath = (p: string) => {
  let s = p.trim();
  if (!s.startsWith("/")) s = `/${s}`;
  return s.endsWith("/") ? s : `${s}/`;
};

/** Whether an answer is for the folder shown: requests overlap when the
 *  folder changes, and an answer for another one is dropped. */
export const usageIsFor = (usage: { path: string }, shown: string) => asFolderPath(usage.path) === asFolderPath(shown);

/** Whether an answer is shown: it answers the latest request (`mine` is
 *  `latest` - an older one for the same folder, from before a delete or a
 *  refresh, can land last) and is for the folder shown. The phones check
 *  the same two. */
export const usageAnswerFits = (mine: number, latest: number, usage: { path: string }, shown: string) =>
  mine === latest && usageIsFor(usage, shown);

/** The key an entry and a listing's file are joined by: a file and a
 *  folder can share a name (made from two computers), so never the path
 *  alone. */
export const usageKey = (path: string, isDir: boolean) => `${isDir ? "d" : "f"}:${path.replace(/\/+$/, "") || "/"}`;

/** A row's share of the folder: none (the folder takes no space: no bar),
 *  under 1% but more than nothing ("under 1%"), or a whole percentage. */
export type Share = { kind: "none" } | { kind: "small" } | { kind: "percent"; value: number };

export function shareOf(total: number, folderTotal: number): Share {
  if (!(folderTotal > 0)) return { kind: "none" };
  const ratio = Math.max(0, total) / folderTotal;
  if (ratio > 0 && ratio < 0.01) return { kind: "small" };
  return { kind: "percent", value: Math.min(100, Math.round(ratio * 100)) };
}

/** Whether a row shows its share (a bar and a percentage): not when the
 *  folder takes no space at all - the size alone shows then. */
export const shareShown = (share: Share): boolean => share.kind !== "none";

/** The share a row's screen-reader label says: as shown, and 0% when
 *  none shows (the folder takes no space), so the label keeps its shape.
 *  The phones say the same. */
export const labelShare = (share: Share): Exclude<Share, { kind: "none" }> =>
  share.kind === "none" ? { kind: "percent", value: 0 } : share;

/** How wide a row's bar is drawn, in % of its track: its exact share, with
 *  a sliver for anything above nothing, so a tiny file still shows one. */
export function barWidth(total: number, folderTotal: number): number {
  if (!(folderTotal > 0) || !(total > 0)) return 0;
  return Math.max(0.5, Math.min(100, (total / folderTotal) * 100));
}

/** One row of the view: an entry of the answer, its whole size (files and
 *  older versions, the base of the percentages), and the listing's file
 *  for it when the listing has one (its icon, lock and kept-out mark; a
 *  child holding only older versions isn't listed). */
export type UsageRow<F> = {
  key: string;
  path: string;
  name: string;
  isDir: boolean;
  total: number;
  share: Share;
  bar: number;
  file: F | null;
};

/** The rows, in the answer's order, and the folder's own totals: its
 *  whole size (files and older versions, as the rows' share is of) and how
 *  much of that is older versions. `listing` is the folder's ListFiles
 *  entries and `fullPath` gives each one's full path. */
export function usageRows<F extends { mime?: string }>(
  usage: Usage,
  listing: readonly F[],
  fullPath: (f: F) => string,
): { rows: UsageRow<F>[]; total: number; versions: number; files: number } {
  const byKey = new Map<string, F>();
  for (const f of listing) byKey.set(usageKey(fullPath(f), f.mime === "inode/directory"), f);
  const total = Number(usage.filesSize) + Number(usage.versionsSize);
  const rows = usage.entries.map((e) => {
    const size = Number(e.filesSize) + Number(e.versionsSize);
    const key = usageKey(e.path, e.isDir);
    const clean = e.path.replace(/\/+$/, "");
    return {
      key,
      path: clean,
      name: clean.slice(clean.lastIndexOf("/") + 1) || clean,
      isDir: e.isDir,
      total: size,
      share: shareOf(size, total),
      bar: barWidth(size, total),
      file: byKey.get(key) ?? null,
    };
  });
  return { rows, total, versions: Number(usage.versionsSize), files: usage.fileCount };
}
