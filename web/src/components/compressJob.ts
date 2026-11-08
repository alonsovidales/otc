// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The compress job (compress.ts), followed while something looks at it.
// CompressFiles answers with the job at once, and the device works through
// it in the background, one file at a time (Settings' Background work lists
// it too). The Compress dialog (CompressDialog.tsx) shows how far it is and,
// once it is over, what it came to: it asks every 2 s while it shows the
// job. The dialog can be closed and the job goes on; the page that opened
// it - Images, or the Largest files list - goes on asking every 10 s while
// it is open, only to put each compressed file in place of the one it
// showed (a new hash, a new size, maybe a new name), each once
// (compress.ts' pollDelay, newlyDone). Nothing is asked once the job is
// over, nor while nothing looks.
//
// One job at a time, as on the device: tasks sent while one runs join it
// (the same id, a larger total). The page's, not a component's: it outlives
// the dialog. The end of a sign-in forgets it (App's watchCompress), so the
// next one never follows another's job.
import { useEffect, useLayoutEffect, useRef, useSyncExternalStore } from "react";
import type { CompressJob, CompressResult, File as MsgFile, ReqEnvelope } from "../proto/messages";
import { useWS } from "../net/useWS";
import { jobOver, jobPayload, newlyDone, pollAnswer, pollDelay, resultKey } from "./compress";

/** The job followed, as last answered; `gone` once the device no longer
 *  knows it (it restarted mid-job, or another job replaced it). */
export type FollowedJob = { job: CompressJob; gone: boolean };

/** Done results, each with the file now at its path. */
export type Compressed = (CompressResult & { file: MsgFile })[];

let current: FollowedJob | null = null;
// How many dialogs show the job, and the pages putting its files in place.
let shown = 0;
const pages = new Set<(done: Compressed) => void>();
const listeners = new Set<() => void>();
// The done results already handed to the pages (compress.ts' resultKey).
const seen = new Set<string>();
let timer: ReturnType<typeof setTimeout> | null = null;
let asking = false;
// Bumped when the job is forgotten: an answer on its way then lands nowhere.
let generation = 0;

const emit = () => listeners.forEach((l) => l());

function schedule() {
  if (timer != null) clearTimeout(timer);
  timer = null;
  if (!current || asking) return;
  const delay = pollDelay(shown > 0, pages.size > 0, jobOver(current.job, current.gone));
  if (delay != null) timer = setTimeout(() => void ask(), delay);
}

async function ask() {
  timer = null;
  const was = current;
  if (!was) return;
  const gen = generation;
  asking = true;
  let answer;
  try {
    answer = pollAnswer(await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = jobPayload(was.job.id); }), was.job.id);
  } catch {
    // No answer: asked again at the next look.
    answer = pollAnswer(null, was.job.id);
  } finally {
    asking = false;
  }
  if (gen === generation && current?.job.id === was.job.id) {
    if (answer.kind === "job") take(answer.job);
    else if (answer.kind === "gone") {
      current = { job: current.job, gone: true };
      emit();
    }
  }
  schedule();
}

// The job as the device answered it: the dialog redraws, and each result
// newly done goes to the pages, once.
function take(job: CompressJob) {
  const fresh = newlyDone(job, seen);
  for (const r of fresh) seen.add(resultKey(r));
  current = { job, gone: false };
  emit();
  if (fresh.length) pages.forEach((p) => p(fresh));
}

/** CompressFiles answered with the job: followed from now on (another job
 *  than the one followed starts afresh). */
export function followJob(job: CompressJob) {
  if (current?.job.id !== job.id) seen.clear();
  take(job);
  schedule();
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => { listeners.delete(l); };
};
const get = () => current;

/** The dialog showing the job: it, as last answered, asked about every 2 s
 *  while `showing`. */
export function useCompressJob(showing: boolean): FollowedJob | null {
  useEffect(() => {
    if (!showing) return;
    shown++;
    schedule();
    return () => {
      shown--;
      schedule();
    };
  }, [showing]);
  return useSyncExternalStore(subscribe, get);
}

/** A page that started a compress (Images, the Largest files list): `put`
 *  gets each file compressed while the page is open, to show in place of
 *  the one it showed. */
export function useCompressed(put: (done: Compressed) => void) {
  const putRef = useRef(put);
  useLayoutEffect(() => { putRef.current = put; });
  useEffect(() => {
    const fn = (done: Compressed) => putRef.current(done);
    pages.add(fn);
    schedule();
    return () => {
      pages.delete(fn);
      schedule();
    };
  }, []);
}

/** App, for the length of a sign-in: its end forgets the job. */
export function watchCompress(): () => void {
  return () => {
    generation++;
    current = null;
    seen.clear();
    schedule();
    emit();
  };
}
