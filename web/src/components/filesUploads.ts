// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Files' uploads (issue #86, FilesExplorer.tsx): every file dropped or
// picked with Upload is a row of the upload panel - waiting, read for its
// resume key, sending with how much the device holds, done, or failed with
// the device's words - in one queue, sent one at a time in the order they
// came. A drop while others are on their way joins it, and each row is its
// own by id. The panel goes cClearAfterMs after the last one is done when
// none failed; with failures it stays, with Try again, until closed.
//
// Module state, not Files': App shows one section at a time, so opening
// Images to watch the photos arrive unmounts Files - and a queue kept in
// the component went with it: the file on its way finished, the others
// were never sent, and nothing said so. Here the queue goes on whatever
// the page shows, and Files shows its rows again when it is opened. The
// end of a sign-in forgets it all (App's watchUploads: a session that
// ended, a sign-out reloads the page anyway), so nothing queued goes into
// the folders of whoever signs in next, nor shows them its names.
//
// Tested by web/scripts/files-uploads-test.mjs.
import { useSyncExternalStore } from "react";
import type { RespEnvelope } from "../proto/messages";
import { uploadFile } from "../net/upload";
import { isBridgeAnswer } from "../net/deviceStatus";
import { asIs, say, type UiText } from "./uiText";

export type UploadStatus = "waiting" | "reading" | "sending" | "done" | "failed";
export type UploadRow = { id: number; name: string; size: number; status: UploadStatus; sent: number; error?: UiText };

/** A file to send: where it goes (its full path) and the name its row shows
 *  ("Trip/a.jpg" for a dropped folder's). */
export type UploadItem = { file: File; path: string; name: string };

/** How one file is sent (net/upload.ts' uploadFile; the tests' stand-in):
 *  onProgress hears what the device holds, onReading that it is being read
 *  for its resume key. */
export type SendUpload = (path: string, file: File, onProgress: (sent: number) => void, onReading: () => void) => Promise<RespEnvelope>;

/** The clock the panel's going runs on (the tests' own). */
export type UploadTimers = { set: (fn: () => void, ms: number) => unknown; clear: (id: unknown) => void };

/** How long the panel stays once every file is done and none failed. */
export const cClearAfterMs = 2500;

const active = (u: UploadRow) => u.status === "waiting" || u.status === "reading" || u.status === "sending";

const realTimers: UploadTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

export function createUploads(send: SendUpload, timers: UploadTimers = realTimers) {
  // gen: the sign-in the job was queued under (forget starts another).
  type Job = { id: number; file: File; path: string; gen: number };
  let rows: UploadRow[] = [];
  let queue: Job[] = [];
  // The rows' files, kept for Try again until their row goes.
  const files = new Map<number, Job>();
  let pumping = false;
  let seq = 0;
  let gen = 0;
  let clearTimer: unknown = null;
  const listeners = new Set<() => void>();
  const landedListeners = new Set<(paths: string[]) => void>();

  const setRows = (next: UploadRow[]) => {
    rows = next;
    listeners.forEach((l) => l());
  };
  // A row that went (forgotten, closed) is left alone.
  const setRow = (id: number, change: Partial<UploadRow>) => {
    if (!rows.some((u) => u.id === id)) return;
    setRows(rows.map((u) => (u.id === id ? { ...u, ...change } : u)));
  };
  const stopClearing = () => {
    if (clearTimer !== null) timers.clear(clearTimer);
    clearTimer = null;
  };

  // One at a time, in the queue's order. Once it is empty, whoever listens
  // (Files, when it shows) hears where files landed, and the panel goes in
  // a moment if nothing failed.
  const pump = async () => {
    if (pumping) return;
    pumping = true;
    let landed: string[] = [];
    try {
      for (;;) {
        const job = queue.shift();
        if (!job) break;
        setRow(job.id, { status: "sending", sent: 0, error: undefined });
        try {
          // The device refuses with a reply, not a dropped request: a file
          // edited and dropped onto its own name ("Duplicated file"), or a
          // failed disk write. The listing still shows the old file by that
          // name, so a "Done" here would say the new content was kept.
          const resp = await send(job.path, job.file,
            (sent) => setRow(job.id, { status: "sending", sent }),
            () => setRow(job.id, { status: "reading" }));
          if (resp.error || resp.payload?.$case !== "respFile") {
            const words = isBridgeAnswer(resp) ? "" : resp.errorMessage;
            setRow(job.id, { status: "failed", error: words ? asIs(words) : say("web.shell.connection.no_answer") });
            console.error("Upload failed for", job.path, resp.errorMessage);
          } else {
            setRow(job.id, { status: "done", sent: job.file.size });
            files.delete(job.id);
            if (job.gen === gen) landed.push(job.path);
          }
        } catch (err) {
          const words = err instanceof Error ? err.message : "";
          setRow(job.id, { status: "failed", error: words ? asIs(words) : say("web.shell.connection.no_answer") });
          console.error("Upload failed for", job.path, err);
        }
        if (queue.length === 0 && landed.length) {
          const paths = landed;
          landed = [];
          landedListeners.forEach((l) => l(paths));
        }
      }
    } finally {
      pumping = false;
    }
    // Nothing failed: the panel goes in a moment. Failures stay until the
    // panel is closed (or tried again).
    if (rows.length && rows.every((u) => u.status === "done")) {
      stopClearing();
      clearTimer = timers.set(() => {
        clearTimer = null;
        if (!rows.every((u) => u.status === "done")) return;
        for (const u of rows) files.delete(u.id);
        setRows([]);
      }, cClearAfterMs);
    }
  };

  return {
    /** The panel's rows, in the order they came. */
    rows: () => rows,
    subscribe(l: () => void): () => void {
      listeners.add(l);
      return () => { listeners.delete(l); };
    },
    /** fn hears the paths that landed each time the queue empties. */
    onLanded(fn: (paths: string[]) => void): () => void {
      landedListeners.add(fn);
      return () => { landedListeners.delete(fn); };
    },
    /** Files to send: they join the queue. A panel whose files were all
     *  done starts afresh. */
    add(items: UploadItem[]) {
      if (!items.length) return;
      stopClearing();
      const jobs = items.map(({ file, path }) => ({ id: ++seq, file, path, gen }));
      const keep = rows.some((u) => u.status !== "done") ? rows : [];
      if (!keep.length) for (const u of rows) files.delete(u.id);
      for (const j of jobs) files.set(j.id, j);
      setRows([...keep, ...jobs.map((j, i) => ({ id: j.id, name: items[i].name, size: j.file.size, status: "waiting" as const, sent: 0 }))]);
      queue.push(...jobs);
      void pump();
    },
    /** The panel's Try again: the failed ones join the queue once more. */
    retry() {
      const again = rows.filter((u) => u.status === "failed").map((u) => files.get(u.id)).filter((j): j is Job => !!j);
      if (!again.length) return;
      stopClearing();
      const ids = new Set(again.map((j) => j.id));
      setRows(rows.map((u) => (ids.has(u.id) ? { ...u, status: "waiting", sent: 0, error: undefined } : u)));
      queue.push(...again);
      void pump();
    },
    /** The panel's ×: rows still to send stay until they are done. */
    close() {
      stopClearing();
      const left = rows.filter(active);
      for (const u of rows) if (!left.includes(u)) files.delete(u.id);
      setRows(left);
    },
    /** The sign-in ended: nothing more is sent and every row goes. The file
     *  on its way finishes or fails on its own; its answer changes nothing. */
    forget() {
      gen++;
      queue = [];
      files.clear();
      stopClearing();
      if (rows.length) setRows([]);
    },
  };
}

export type Uploads = ReturnType<typeof createUploads>;

/** The page's queue. */
export const filesUploads = createUploads((path, file, onProgress, onReading) => uploadFile(path, file, false, onProgress, onReading));

/** Files' panel rows. */
export function useUploadRows(): UploadRow[] {
  return useSyncExternalStore(filesUploads.subscribe, filesUploads.rows);
}

/** From App while signed in: the stop forgets the queue (see above). */
export function watchUploads(): () => void {
  return () => filesUploads.forget();
}
