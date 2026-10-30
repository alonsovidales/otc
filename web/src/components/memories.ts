// SPDX-License-Identifier: AGPL-3.0-or-later

// On this day (Memories): the device's choice of photos taken around today
// in earlier years, one memory per year (GetMemories), for the strip at the
// top of Images (MemoriesStrip.tsx) and its slideshow (MemoryViewer.tsx).
// Module state, so going to another section and back shows them at once.
//
// On unless the user turns it off in Settings (Settings.memories_enabled):
// GetMemories answers error_code "memories_off" then, and "unknown_payload"
// on a device before it - both hide the strip, as does a sign-in with
// nothing to show. The three periods are asked, day, week and month: the
// first with memories shows unless the user picks another, and one with
// none is greyed out; the device keeps each answer for the day, so asking
// again is cheap. A new day (the page left open past midnight), a hide,
// Settings' switch and - every 2 minutes - a device still measuring ask
// again; a new sign-in or a sign-out forgets everything - another user's
// library. The phones follow the same rules (MemoriesSupport).

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import { MemoryHideKind, MemoryPeriod, type File as MsgFile, type Memory, type RespEnvelope } from "../proto/messages";

export type Period = "day" | "week" | "month";
export const cPeriods: readonly Period[] = ["day", "week", "month"];
const cProtoPeriod: Record<Period, MemoryPeriod> = {
  day: MemoryPeriod.MemoryDay,
  week: MemoryPeriod.MemoryWeek,
  month: MemoryPeriod.MemoryMonth,
};

export type HideKind = "photo" | "day" | "memory" | "person";
const cProtoHide: Record<HideKind, MemoryHideKind> = {
  photo: MemoryHideKind.MemoryHidePhoto,
  day: MemoryHideKind.MemoryHideDay,
  memory: MemoryHideKind.MemoryHideMemory,
  person: MemoryHideKind.MemoryHidePerson,
};

/** A date as the device takes it, "YYYY-MM-DD", in this browser's time. */
export function localDay(d: Date = new Date()): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** "YYYY-MM-DD" (or "YYYY-MM") as a local date, at its first instant. */
export function dayDate(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, (m || 1) - 1, d || 1);
}

// ---- what is known ---------------------------------------------------------------

/** One period's answer: its memories, or that asking failed (asked again
 *  when the strip next shows). */
export type Answer = { memories: Memory[]; measuring: boolean } | "failed";

export type MemoriesState = {
  /** "unknown": nothing answered yet; "on"; "off": the user's switch;
   *  "none": a device before On this day. */
  readonly status: "unknown" | "on" | "off" | "none";
  /** The day the answers are for. */
  readonly date: string;
  /** The period shown: picked, or the first with memories. Null until the
   *  device has answered for it. */
  readonly period: Period | null;
  readonly answers: Readonly<Partial<Record<Period, Answer>>>;
  /** How many things the user has hidden (the last answer's). */
  readonly hidden: number;
};

const blank = (): MemoriesState => ({ status: "unknown", date: localDay(), period: null, answers: {}, hidden: 0 });

let state: MemoriesState = blank();
// The period the user picked this session, which a new day or a hide keeps.
let picked: Period | null = null;
// Bumped by a sign-in, a sign-out, a new day and every refresh: an answer
// for an older one lands nowhere.
let gen = 0;
const asking = new Map<Period, { gen: number; answer: Promise<Got | null> }>();

const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => { listeners.delete(l); };
};
const set = (next: Partial<MemoriesState>) => {
  state = { ...state, ...next };
  listeners.forEach((l) => l());
};
export const getMemories = () => state;
export const useMemories = () => useSyncExternalStore(subscribe, getMemories);

/** The memories a period has: none while unanswered or failed. */
export const memoriesOf = (s: MemoriesState, p: Period | null): Memory[] => {
  const a = p ? s.answers[p] : undefined;
  return a && a !== "failed" ? a.memories : [];
};

// ---- asking ------------------------------------------------------------------------

function timeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

type Got = { kind: "answer"; memories: Memory[]; measuring: boolean; hidden: number } | { kind: "off" } | { kind: "none" } | { kind: "failed" };

async function ask(period: Period, date: string): Promise<Got> {
  try {
    const resp: RespEnvelope = await useWS.request((e) => {
      e.payload = {
        $case: "reqGetMemories",
        reqGetMemories: {
          date,
          period: cProtoPeriod[period],
          timeZone: timeZone(),
          utcOffsetMinutes: -new Date().getTimezoneOffset(),
        },
      };
    });
    if (resp.payload?.$case === "respMemories") {
      const m = resp.payload.respMemories;
      return { kind: "answer", memories: m.memories, measuring: m.measuring, hidden: m.hiddenCount };
    }
    if (resp.errorCode === "memories_off") return { kind: "off" };
    if (resp.errorCode === "unknown_payload") return { kind: "none" };
  } catch {
    // No answer: asked again when the strip next shows.
  }
  return { kind: "failed" };
}

// One question per period at a time: a second caller of the same
// generation waits for the answer already on its way. Null: the answer
// came for an older generation (a sign-in, a new day, a refresh since)
// and was dropped.
function askPeriod(period: Period): Promise<Got | null> {
  const cur = asking.get(period);
  if (cur && cur.gen === gen) return cur.answer;
  const g = gen;
  const date = state.date;
  const answer = ask(period, date).then((got) => {
    if (g !== gen) return null;
    if (got.kind === "off") set({ status: "off", answers: {}, period: null });
    else if (got.kind === "none") set({ status: "none", answers: {}, period: null });
    else if (got.kind === "failed") set({ answers: { ...state.answers, [period]: "failed" } });
    else set({ status: "on", hidden: got.hidden, answers: { ...state.answers, [period]: { memories: got.memories, measuring: got.measuring } } });
    return got;
  }).finally(() => {
    if (asking.get(period)?.answer === answer) asking.delete(period);
  });
  asking.set(period, { gen: g, answer });
  return answer;
}

/** Whether the device answered a period, with nothing in it: the choice
 *  is greyed out then. */
export const isEmpty = (s: MemoriesState, p: Period): boolean => {
  const a = s.answers[p];
  return !!a && a !== "failed" && a.memories.length === 0;
};

// The period shown: the one picked while it has memories, else the first
// of day, week and month that has some - only once every period before it
// has answered, so a later period never shows first for a moment.
function choose() {
  const has = (p: Period) => memoriesOf(state, p).length > 0;
  let next: Period | null = null;
  if (picked && has(picked)) next = picked;
  else {
    for (const p of cPeriods) {
      if (has(p)) { next = p; break; }
      if (!isEmpty(state, p)) break; // not answered yet
    }
  }
  if (next !== state.period) set({ period: next });
}

/** Asks every period not yet answered today - the picked one first, then
 *  day, week and month - choosing the one shown as answers come (the rest
 *  only say which choices are greyed out). A new day starts again. */
export async function loadMemories(): Promise<void> {
  const today = localDay();
  if (today !== state.date) {
    gen++;
    set({ date: today, answers: {}, period: null });
  }
  if (state.status === "off" || state.status === "none") return;
  const g = gen;
  const order = picked ? [picked, ...cPeriods.filter((p) => p !== picked)] : cPeriods;
  for (const p of order) {
    const a = state.answers[p];
    if (!a || a === "failed") {
      const got = await askPeriod(p);
      if (g !== gen) return;
      if (got && (got.kind === "off" || got.kind === "none")) return;
    }
    choose();
  }
}

/** The user picked a period (one with memories: an empty one is greyed out). */
export function choosePeriod(p: Period) {
  if (isEmpty(state, p)) return;
  picked = p;
  choose();
  if (!state.answers[p] || state.answers[p] === "failed") void loadMemories();
}

/** Asks every period again (after a hide, while the device is still
 *  measuring), the one shown first; the old answers stay on screen until
 *  the new ones come. */
export async function refreshMemories(): Promise<void> {
  gen++;
  const g = gen;
  const order = state.period ? [state.period, ...cPeriods.filter((p) => p !== state.period)] : cPeriods;
  for (const p of order) {
    const got = await askPeriod(p);
    if (g !== gen) return;
    if (got && (got.kind === "off" || got.kind === "none")) return;
    choose();
  }
}

/** The device is still measuring photos it held before (just updated, or
 *  just turned on): the choice may get better, so the strip asks again. */
export const isMeasuring = (s: MemoriesState): boolean =>
  cPeriods.some((p) => { const a = s.answers[p]; return !!a && a !== "failed" && a.measuring; });

/** Settings' switch, as the device acknowledged it. */
export function setMemoriesOn(on: boolean) {
  gen++;
  if (on) {
    set({ status: "unknown", answers: {}, period: null });
    void loadMemories();
  } else {
    set({ status: "off", answers: {}, period: null });
  }
}

/** Hides something from the memories, or (show) shows it again. Resolves
 *  to null once done, else to the device's words (empty when it gave none). */
export async function hideMemory(kind: HideKind, value: string, show = false): Promise<string | null> {
  return send({ kind: cProtoHide[kind], value, show, showAll: false });
}

/** Shows everything hidden again. */
export async function showAllMemories(): Promise<string | null> {
  return send({ kind: MemoryHideKind.MemoryHidePhoto, value: "", show: false, showAll: true });
}

async function send(req: { kind: MemoryHideKind; value: string; show: boolean; showAll: boolean }): Promise<string | null> {
  try {
    const resp: RespEnvelope = await useWS.request((e) => {
      e.payload = { $case: "reqHideMemory", reqHideMemory: req };
    });
    if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
      void refreshMemories();
      return null;
    }
    return resp.errorMessage || (resp.payload?.$case === "respAck" ? resp.payload.respAck.errorMsg : "") || "";
  } catch (err) {
    return err instanceof Error ? err.message : "";
  }
}

// ---- thumbnails ----------------------------------------------------------------------

// By path: the strip's covers small, the slideshow's photos big. "" is a
// path the device has no thumbnail for.
const thumbs = { small: new Map<string, string>(), big: new Map<string, string>() };
const thumbAsked = new Set<string>();
let thumbVersion = 0;
const cThumbBatch = 24;

const subscribeThumbs = subscribe;
const getThumbVersion = () => thumbVersion;
/** Re-renders when a thumbnail arrives; read them with thumbFor. */
export const useThumbVersion = () => useSyncExternalStore(subscribeThumbs, getThumbVersion);
export const thumbFor = (path: string, small: boolean): string | undefined => (small ? thumbs.small : thumbs.big).get(path);

/** Fetches the thumbnails of these paths the page doesn't have yet. */
export async function fetchThumbs(paths: readonly string[], small: boolean): Promise<void> {
  const cache = small ? thumbs.small : thumbs.big;
  const key = (p: string) => `${small ? "s" : "b"}|${p}`;
  const need = [...new Set(paths)].filter((p) => p && !cache.has(p) && !thumbAsked.has(key(p)));
  const g = gen;
  for (let i = 0; i < need.length; i += cThumbBatch) {
    const batch = need.slice(i, i + cThumbBatch);
    batch.forEach((p) => thumbAsked.add(key(p)));
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: batch, smallThumbnails: small } };
      });
      if (resp.payload?.$case !== "respListOfFiles" || g !== gen) continue;
      const lof = resp.payload.respListOfFiles;
      const got = new Set<string>();
      for (const f of lof.files as MsgFile[]) {
        if (!f.content?.length) continue;
        cache.set(f.path, URL.createObjectURL(new Blob([f.content as BlobPart], { type: "image/jpeg" })));
        got.add(f.path);
      }
      // Cut short (the answer's size cap): the rest is asked next time.
      const cut = lof.askAgainFrom > 0 ? lof.askAgainFrom : batch.length;
      batch.forEach((p, j) => { if (!got.has(p) && j < cut) cache.set(p, ""); });
      thumbVersion++;
      listeners.forEach((l) => l());
    } catch {
      // Asked again when next wanted.
    } finally {
      batch.forEach((p) => thumbAsked.delete(key(p)));
    }
  }
}

// ---- the session -----------------------------------------------------------------------

function forget() {
  gen++;
  picked = null;
  for (const m of [thumbs.small, thumbs.big]) {
    m.forEach((u) => { if (u) URL.revokeObjectURL(u); });
    m.clear();
  }
  thumbVersion++;
  state = blank();
  listeners.forEach((l) => l());
}

/** From App while signed in: a new sign-in starts empty and asks at once
 *  (Images is where the app opens); the stop forgets it all. */
export function watchMemories(): () => void {
  forget();
  void loadMemories();
  return forget;
}
