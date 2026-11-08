// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Compress and Settings › Largest files (the owner, 2026-11-08: "so we can
// identify what is taking more space", then make it smaller). The device
// lists every file by size (ListLargestFiles), says what it could do for
// each photo or video picked and what it would weigh after
// (GetCompressOptions), and compresses them in the background, one at a
// time (CompressFiles, followed with GetCompressJob): a 4K video re-encoded
// at a lower resolution as HEVC, a large photo shrunk to about 12
// megapixels, a JPEG saved again at a lower quality, a PNG saved as JPEG.
// Every copy of the content is replaced and the original's bytes are
// deleted for good, its hash kept (File.original_removed): there is no
// Revert to original afterwards.
//
// This file holds the rules, pure - tested by web/scripts/compress-test.mjs,
// whose case ids (C: the dialog, J: the job, L: the list, R: the viewer) the
// phones' CompressTests.swift and CompressTest.kt share: change the three
// together. The Compress dialog is CompressDialog.tsx (opened from Images'
// selection bar and the Largest files list), the job's follower
// compressJob.ts, the list LargestFiles.tsx.
//
// The dialog asks about the photos and videos picked (documents picked in
// the list are left out), each once, the first 500. Its choices:
//   - Videos, shown only when an item offers a video preset: a radio of
//     the presets offered, in the order 1080p, 720p, HEVC, then "Leave as
//     they are". 1080p by default when anything offers it, else HEVC, else
//     720p. Each video gets the preset chosen when it offers it, else HEVC
//     when it offers that, else nothing.
//   - Photos: a switch per preset offered by anything, each on by default.
//     A photo gets the 12 MP shrink when its switch is on and it offers it,
//     else the JPEG resave (the same), else PNG to JPEG, else nothing.
//   - Under each choice, how many files it would compress, what they weigh
//     now and, estimated, after; for a video choice that is everything the
//     choice would do (its fallbacks to HEVC included), for a photo switch
//     the photos that would get that preset, the other switches as they are.
//   - The files the device can't make smaller, each with its reason in the
//     device's words; when none can be, the dialog says so instead.
//   - At its foot, what the choices made would free (each file's size less
//     its estimate), and that videos take a while when any is among them.
// A preset this app doesn't know (a newer device's) is left out, as
// messages.proto asks.

// Types and pure functions only: the module loads in the test with nothing
// of the app around it (photoEdit.ts imports types and uiText alone).
import type { CompressJob, CompressOption, CompressResult, File as MsgFile, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { shownAfter } from "./photoEdit";

/** The video presets, in the order the dialog's radio lists them. */
export const cVideoPresets = ["video_1080p", "video_720p", "video_hevc"] as const;
/** The photo presets, strongest first: the order a photo takes them in. */
export const cPhotoPresets = ["photo_12mp", "jpeg_resave", "png_to_jpeg"] as const;
export type VideoPreset = (typeof cVideoPresets)[number];
export type PhotoPreset = (typeof cPhotoPresets)[number];
/** The videos' "Leave as they are". */
export const cVideoNone = "video_none";
export type VideoChoice = VideoPreset | typeof cVideoNone;
/** The most files GetCompressOptions takes at once: the dialog asks for
 *  the first this many (compressItems). */
export const cMaxCompress = 500;

const cVideoSet = new Set<string>(cVideoPresets);

const num = (v: bigint | number | undefined | null) => Number(v ?? 0);

/** A CompressOption as the dialog reads it (sizes are bigints in the
 *  generated code; the tests may write numbers). */
export type OptionLike = {
  path: string;
  hash: string;
  size: bigint | number;
  copies?: number;
  choices: readonly { preset: string; estimatedSize: bigint | number }[];
  reason: string;
  reasonCode?: string;
};

/** What the dialog's choices are set to: the videos' radio, and each photo
 *  switch (on unless set false). */
export type CompressPick = { video: VideoChoice; photos: Readonly<Partial<Record<PhotoPreset, boolean>>> };

// The presets this app knows that an item offers, with their estimates.
function offered(o: OptionLike): Map<string, number> {
  const out = new Map<string, number>();
  for (const c of o.choices) {
    if ((cVideoSet.has(c.preset) || (cPhotoPresets as readonly string[]).includes(c.preset)) && !out.has(c.preset)) {
      out.set(c.preset, num(c.estimatedSize));
    }
  }
  return out;
}

const offersVideo = (ps: Map<string, number>) => cVideoPresets.some((p) => ps.has(p));
const switchOn = (pick: CompressPick, p: PhotoPreset) => pick.photos[p] !== false;

/** C1: the videos' radio - the video presets anything offers, in order,
 *  then "Leave as they are"; none at all when no item offers one. */
export function videoChoices(options: readonly OptionLike[]): VideoChoice[] {
  const any = new Set<string>();
  for (const o of options) for (const p of offered(o).keys()) any.add(p);
  const shown: VideoChoice[] = cVideoPresets.filter((p) => any.has(p));
  return shown.length ? [...shown, cVideoNone] : [];
}

/** C2: the radio's default - 1080p when anything offers it, else HEVC
 *  when anything does, else 720p; null with no video choice at all. */
export function defaultVideo(options: readonly OptionLike[]): VideoPreset | null {
  const shown = videoChoices(options);
  if (!shown.length) return null;
  if (shown.includes("video_1080p")) return "video_1080p";
  if (shown.includes("video_hevc")) return "video_hevc";
  return "video_720p";
}

/** C3: a switch for each photo preset anything offers, in order. */
export function photoSwitches(options: readonly OptionLike[]): PhotoPreset[] {
  const any = new Set<string>();
  for (const o of options) for (const p of offered(o).keys()) any.add(p);
  return cPhotoPresets.filter((p) => any.has(p));
}

/** The choices as the dialog opens: the radio's default, every switch on. */
export function defaultPick(options: readonly OptionLike[]): CompressPick {
  return { video: defaultVideo(options) ?? cVideoNone, photos: {} };
}

/** C4: what one item gets under the choices made, or null for nothing - a
 *  video the preset chosen if it offers it, else HEVC if it offers that; a
 *  photo the first of its presets, strongest first, whose switch is on. */
export function presetFor(o: OptionLike, pick: CompressPick): string | null {
  const ps = offered(o);
  if (offersVideo(ps)) {
    if (pick.video === cVideoNone) return null;
    if (ps.has(pick.video)) return pick.video;
    return ps.has("video_hevc") ? "video_hevc" : null;
  }
  for (const p of cPhotoPresets) if (switchOn(pick, p) && ps.has(p)) return p;
  return null;
}

/** One file to compress, as CompressFiles takes it, with what it weighs
 *  now and its estimate after (for the sums). */
export type CompressTaskOf = { path: string; hash: string; preset: string; size: number; estimate: number };

/** C5: the tasks the choices make, in the order the items came. */
export function tasksOf(options: readonly OptionLike[], pick: CompressPick): CompressTaskOf[] {
  const out: CompressTaskOf[] = [];
  for (const o of options) {
    const preset = presetFor(o, pick);
    if (!preset) continue;
    out.push({ path: o.path, hash: o.hash, preset, size: num(o.size), estimate: offered(o).get(preset) ?? num(o.size) });
  }
  return out;
}

/** How many files a choice compresses, what they weigh now and after. */
export type Sums = { count: number; now: number; after: number };

const sumOf = (tasks: readonly CompressTaskOf[]): Sums =>
  tasks.reduce((s, t) => ({ count: s.count + 1, now: s.now + t.size, after: s.after + t.estimate }), { count: 0, now: 0, after: 0 });

/** C6: under a video choice, everything it would do if chosen: the videos
 *  that get it, and those that fall back to HEVC with it. */
export function videoRow(options: readonly OptionLike[], pick: CompressPick, choice: VideoPreset): Sums {
  const videos = options.filter((o) => offersVideo(offered(o)));
  return sumOf(tasksOf(videos, { ...pick, video: choice }));
}

/** C7: under a photo switch, the photos that would get its preset were it
 *  on, the other switches as they are. */
export function photoRow(options: readonly OptionLike[], pick: CompressPick, preset: PhotoPreset): Sums {
  const on: CompressPick = { ...pick, photos: { ...pick.photos, [preset]: true } };
  return sumOf(tasksOf(options, on).filter((t) => t.preset === preset));
}

/** C8: what the choices would free: each task's size less its estimate
 *  (never below nothing). */
export const savedOf = (tasks: readonly CompressTaskOf[]): number =>
  tasks.reduce((s, t) => s + Math.max(0, t.size - t.estimate), 0);

/** C9: whether a video is among the tasks (the "videos take a while"
 *  note). */
export const hasVideoTask = (tasks: readonly CompressTaskOf[]): boolean => tasks.some((t) => cVideoSet.has(t.preset));

/** A file the device can't make smaller: its name and why, in the
 *  device's words. */
export type NotOffered = { path: string; name: string; reason: string };

/** C10: the files nothing can be done for (no preset this app knows), in
 *  the order they came. */
export function notOffered(options: readonly OptionLike[]): NotOffered[] {
  return options.filter((o) => offered(o).size === 0).map((o) => ({ path: o.path, name: leafOf(o.path), reason: o.reason }));
}

/** Everything the dialog shows for its choices (C1-C10 together): the
 *  video radio with each choice's sums (none under "Leave as they are"),
 *  the photo switches with theirs, the files not offered, whether nothing
 *  can be done at all, the tasks, what they would free and whether a video
 *  is among them. */
export type CompressView = {
  videos: { choice: VideoChoice; sums: Sums | null }[];
  photos: { preset: PhotoPreset; on: boolean; sums: Sums }[];
  notOffered: NotOffered[];
  nothing: boolean;
  tasks: CompressTaskOf[];
  saved: number;
  slow: boolean;
};

export function compressView(options: readonly OptionLike[], pick: CompressPick): CompressView {
  const tasks = tasksOf(options, pick);
  const out = notOffered(options);
  return {
    videos: videoChoices(options).map((choice) => ({ choice, sums: choice === cVideoNone ? null : videoRow(options, pick, choice) })),
    photos: photoSwitches(options).map((preset) => ({ preset, on: switchOn(pick, preset), sums: photoRow(options, pick, preset) })),
    notOffered: out,
    nothing: out.length === options.length,
    tasks,
    saved: savedOf(tasks),
    slow: hasVideoTask(tasks),
  };
}

// ---- the requests and their answers -----------------------------------------

/** C13: what the dialog asks the device about, and its title counts: the
 *  photos and videos picked (documents picked in the Largest files list
 *  are left out - nothing could be done for them), each path once, the
 *  first cMaxCompress of them in the order picked. */
export function compressItems<F extends { path: string; hash?: string; mime?: string }>(picks: readonly F[]): F[] {
  const seen = new Set<string>();
  const out: F[] = [];
  for (const f of picks) {
    if (out.length >= cMaxCompress) break;
    if (!isMediaFile(f) || seen.has(f.path)) continue;
    seen.add(f.path);
    out.push(f);
  }
  return out;
}

/** GetCompressOptions' payload: the files picked, each with the content
 *  shown for it (a path holding other content by now is answered
 *  "changed", never compressed). */
export const optionsPayload = (items: readonly { path: string; hash?: string }[]): ReqEnvelope["payload"] =>
  ({ $case: "reqGetCompressOptions", reqGetCompressOptions: { items: items.map((i) => ({ path: i.path, hash: i.hash ?? "" })) } });

/** CompressFiles' payload: the tasks. */
export const compressPayload = (tasks: readonly CompressTaskOf[]): ReqEnvelope["payload"] =>
  ({ $case: "reqCompressFiles", reqCompressFiles: { tasks: tasks.map((t) => ({ path: t.path, hash: t.hash, preset: t.preset })) } });

/** GetCompressJob's payload: the job CompressFiles answered with. */
export const jobPayload = (id: string): ReqEnvelope["payload"] => ({ $case: "reqGetCompressJob", reqGetCompressJob: { id } });

type Answer = Pick<RespEnvelope, "error" | "errorCode" | "errorMessage" | "payload">;

// The device's own words for a refusal ("" when none came: the bridge's
// answer carries none any more, deviceStatus.ts).
const saidOf = (resp: Answer) => resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "");

/** C11: GetCompressOptions' answer - the options; a device older than
 *  Compress ("unknown_payload": it needs an update); a refusal in the
 *  device's words; or nothing usable (no answer, the bridge's: try
 *  again). */
export type OptionsAnswer = { kind: "options"; items: CompressOption[] } | { kind: "old" } | { kind: "refused"; said: string } | { kind: "failed" };

export function optionsAnswer(resp: Answer | null): OptionsAnswer {
  if (!resp) return { kind: "failed" };
  const p = resp.payload;
  if (!resp.error && p?.$case === "respCompressOptions") return { kind: "options", items: p.respCompressOptions.items };
  if (resp.errorCode === "unknown_payload") return { kind: "old" };
  const said = saidOf(resp);
  return said ? { kind: "refused", said } : { kind: "failed" };
}

/** C12: CompressFiles' answer - the job (at once, whatever is still to
 *  do); an older device; a refusal in the device's words; or nothing. */
export type JobAnswer = { kind: "job"; job: CompressJob } | { kind: "old" } | { kind: "refused"; said: string } | { kind: "failed" };

export function startAnswer(resp: Answer | null): JobAnswer {
  if (!resp) return { kind: "failed" };
  const p = resp.payload;
  if (!resp.error && p?.$case === "respCompressJob") return { kind: "job", job: p.respCompressJob };
  if (resp.errorCode === "unknown_payload") return { kind: "old" };
  const said = saidOf(resp);
  return said ? { kind: "refused", said } : { kind: "failed" };
}

/** J1: GetCompressJob's answer - the job; gone ("not_found": the device
 *  restarted since, or another job replaced it); or no answer (asked
 *  again at the next look). A job of another id is gone too. */
export type PollAnswer = { kind: "job"; job: CompressJob } | { kind: "gone" } | { kind: "failed" };

export function pollAnswer(resp: Answer | null, id: string): PollAnswer {
  if (!resp) return { kind: "failed" };
  const p = resp.payload;
  if (!resp.error && p?.$case === "respCompressJob") {
    return id && p.respCompressJob.id && p.respCompressJob.id !== id ? { kind: "gone" } : { kind: "job", job: p.respCompressJob };
  }
  return resp.errorCode === "not_found" ? { kind: "gone" } : { kind: "failed" };
}

// ---- following the job --------------------------------------------------------

/** How often the job is asked about while the dialog shows it, and while
 *  only a page that started it is open (to put each compressed file in
 *  place of the old one). */
export const cJobShownMs = 2000;
export const cJobWatchedMs = 10_000;

/** J2: when the job is asked about next - every 2 s while the dialog
 *  shows it, every 10 s while only the page behind follows it, never once
 *  it is over or nobody looks. */
export function pollDelay(shown: boolean, watched: boolean, over: boolean): number | null {
  if (over) return null;
  if (shown) return cJobShownMs;
  return watched ? cJobWatchedMs : null;
}

/** J3: how far the job is, for its bar: the tasks over, and a running
 *  video's percent of the next one. */
export function jobProgress(job: Pick<CompressJob, "done" | "total" | "results">): { done: number; total: number; fraction: number } {
  const total = Math.max(job.total, job.done, 0);
  const running = job.results.find((r) => r.state === "running");
  const part = running ? Math.min(100, Math.max(0, running.percent)) / 100 : 0;
  const fraction = total > 0 ? Math.min(1, (job.done + part) / total) : 0;
  return { done: Math.min(job.done, total), total, fraction };
}

/** A file the job couldn't compress (failed, or not smaller and left as
 *  it was): its name and why, in the device's words ("" when none). */
export type JobFailure = { path: string; name: string; message: string };

/** J4: what the job came to - how many files it compressed, what it freed,
 *  and the files it couldn't (failed, or skipped as no smaller: both left
 *  as they were). Gone (the device restarted mid-job), the tasks not over
 *  count as not compressed, with no words. */
export function jobOutcome(job: Pick<CompressJob, "saved" | "results">, gone = false): { compressed: number; saved: number; failed: JobFailure[] } {
  let compressed = 0;
  const failed: JobFailure[] = [];
  for (const r of job.results) {
    if (r.state === "done") compressed++;
    else if (r.state === "failed" || r.state === "skipped" || gone) failed.push({ path: r.path, name: leafOf(r.path), message: r.message });
  }
  return { compressed, saved: Math.max(0, num(job.saved)), failed };
}

/** Whether the job is over: the device says so, or it is gone. */
export const jobOver = (job: Pick<CompressJob, "finished">, gone: boolean): boolean => gone || job.finished;

/** J5: the results that are done, each with the file now at its path,
 *  not yet put in place (`seen`, by resultKey): a page puts each one in
 *  place once. */
export function newlyDone(job: Pick<CompressJob, "results">, seen: ReadonlySet<string>): (CompressResult & { file: MsgFile })[] {
  return job.results.filter((r): r is CompressResult & { file: MsgFile } => r.state === "done" && !!r.file && !seen.has(resultKey(r)));
}

/** A done result's key: its path and the content compressed into. */
export const resultKey = (r: Pick<CompressResult, "path" | "file">): string => `${r.path}\n${r.file?.hash ?? ""}`;

/** J6: the file a page shows after a compress, from the one it showed:
 *  the device's (a new hash, maybe a new name - a PNG becomes .jpg - its
 *  size, original_hash and original_removed) over it, as after an edit
 *  replacing every copy (photoEdit.ts' shownAfter). */
export const compressedFile = (shown: MsgFile, r: { file: MsgFile }): MsgFile =>
  shownAfter(shown, { kind: "replace", file: r.file, every: true });

// ---- Settings › Largest files -----------------------------------------------------

/** The list's page: 50 files. */
export const cLargestPage = 50;

/** L1: ListLargestFiles' payload - the first page ("") or the one after a
 *  token. */
export const largestPayload = (after: string): ReqEnvelope["payload"] =>
  ({ $case: "reqListLargestFiles", reqListLargestFiles: { after, limit: cLargestPage } });

/** L2: its answer - a page and the next one's token ("" at the end); a
 *  device older than the list ("unknown_payload"); or a failure, in the
 *  device's words when it said why ("" otherwise: try again). */
export type LargestAnswer = { kind: "page"; files: MsgFile[]; token: string } | { kind: "old" } | { kind: "failed"; said: string };

export function largestAnswer(resp: Answer | null): LargestAnswer {
  if (!resp) return { kind: "failed", said: "" };
  const p = resp.payload;
  if (!resp.error && p?.$case === "respListOfFiles") return { kind: "page", files: p.respListOfFiles.files, token: p.respListOfFiles.token };
  if (resp.errorCode === "unknown_payload") return { kind: "old" };
  return { kind: "failed", said: saidOf(resp) };
}

/** A row's key: the file's path, as the device writes it (one content a
 *  row, at its first path). */
export const pathKey = (f: { path: string }): string => f.path;

/** L3: a page added under the rows shown. Pages are read afresh, so a
 *  file deleted or added between them can bring one shown already - by
 *  its path, or its content at another path: it isn't shown twice. */
export function addPage<F extends { path: string; hash: string }>(shown: readonly F[], page: readonly F[]): F[] {
  const paths = new Set(shown.map(pathKey));
  const hashes = new Set(shown.map((f) => f.hash).filter(Boolean));
  const out = [...shown];
  for (const f of page) {
    if (paths.has(pathKey(f)) || (f.hash && hashes.has(f.hash))) continue;
    paths.add(pathKey(f));
    if (f.hash) hashes.add(f.hash);
    out.push(f);
  }
  return out;
}

/** What the list is doing: loading a page, at its end, or its last page
 *  failed (in the device's words when it said why). */
export type LargestLoad = { kind: "loading" } | { kind: "more" } | { kind: "end" } | { kind: "old" } | { kind: "failed"; said: string };

/** L4: what the screen shows - the first page on its way; an older
 *  device; the first page failed (Try again); no files at all; or the
 *  rows, with at their foot a spinner, nothing, or a failure with Try
 *  again. */
export type LargestView =
  | { kind: "loading" }
  | { kind: "old" }
  | { kind: "failed"; said: string }
  | { kind: "empty" }
  | { kind: "rows"; foot: "loading" | "failed" | "none" };

export function largestView(rows: number, load: LargestLoad): LargestView {
  if (load.kind === "old") return { kind: "old" };
  if (rows === 0) {
    if (load.kind === "loading" || load.kind === "more") return { kind: "loading" };
    if (load.kind === "failed") return { kind: "failed", said: load.said };
    return { kind: "empty" };
  }
  return { kind: "rows", foot: load.kind === "loading" ? "loading" : load.kind === "failed" ? "failed" : "none" };
}

/** L5: whether the list asks for its next page - it has one, and nothing
 *  is on its way. */
export const wantsMore = (load: LargestLoad): boolean => load.kind === "more";

/** L6: "in N places" under a row, for content at several paths: N, else
 *  0 (nothing said). */
export const placesOf = (f: { copies?: number }): number => (f.copies != null && f.copies > 1 ? f.copies : 0);

// A HEIC or HEIF photo by its name, whatever type it was given.
const cHeifName = /\.hei[cf]$/i;

/** L7: whether a file is a photo or a video: its row shows a thumbnail, it
 *  opens in the viewer, and picked it enables Compress. Anything else
 *  shows its type's icon and opens as Files opens it. */
export function isMediaFile(f: { path: string; mime?: string }): boolean {
  const mime = (f.mime || "").split(";")[0].trim().toLowerCase();
  return mime.startsWith("image/") || mime.startsWith("video/") || cHeifName.test(f.path);
}

/** L8: whether the selection bar's Compress is on: a photo or a video is
 *  among the picks. */
export const canCompress = (picks: readonly { path: string; mime?: string }[]): boolean => picks.some(isMediaFile);

/** L9: what the list does once a job is over: it is read again from the
 *  top (sizes changed, and so does the order), when anything was
 *  compressed. */
export const reloadsAfter = (outcome: { compressed: number }): boolean => outcome.compressed > 0;

/** A path's last part. */
export const leafOf = (path: string): string => {
  const clean = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  return clean.slice(clean.lastIndexOf("/") + 1) || clean;
};

/** L10: the folder a row is in, as it is shown under its name ("/a/b",
 *  "/" at the top). */
export function folderOf(path: string): string {
  const clean = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  const at = clean.lastIndexOf("/");
  return at <= 0 ? "/" : clean.slice(0, at);
}

// ---- the viewer ---------------------------------------------------------------------

/** R2: whether the viewer's Info says the photo or video was compressed
 *  and its original deleted (R1, no Revert to original for it, is
 *  photoEdit.ts' canRevert). */
export const compressedNote = (f: { originalRemoved?: boolean }): boolean => !!f.originalRemoved;
