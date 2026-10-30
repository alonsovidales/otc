// SPDX-License-Identifier: AGPL-3.0-or-later

// Smart search (beta): finding photos by describing them ("kids in the
// snow", "un perro en la playa"), on the device's own model. On by default
// since release 152 (off by default in 151), each user turning it off or on
// again in Settings (Settings.smart_search_enabled: while it is on the
// device describes each photo and keeps that encrypted; turning it off
// deletes it). A device before it doesn't say, and then there is neither
// the switch nor the free-text search (SearchPhotos.text: an older device
// would ignore it and answer the whole library).
//
// The model (about 400 MB) is on the device only once it was asked for:
// from release 152 the device downloads it when Smart search is first
// turned on (Settings.smart_search_model; a device before that doesn't
// say, and had it or refuses: taken as ready). Until it is ready the search
// doesn't offer the description, and Settings shows the download. (How far
// the device is in describing the photos is Settings' Background work,
// from Status.background_tasks.)
//
// The device is asked once per sign-in (watchSmartSearch, from App);
// Settings' switch and a search told "smart_search_off" (turned off on
// another app) set it at once. Until the device has answered, the answer
// this browser had last time stands in (otc_smart_search), as places.ts
// does. While it isn't on, the search has no free text (photoFilter.ts).
// While it is on and the model is downloading, Settings asks the device
// again every 3 s (followSmartSearch); elsewhere the state is what the last
// answer said (sign-in, Settings, a refused search), and the search field
// asks once as it gets the focus while the model isn't there
// (smartSearchFocused, at most every 5 s), as on the phones.

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope, Settings, SmartSearchModel } from "../proto/messages";
import { clearText } from "./photoFilter";

const cKey = "otc_smart_search";

function remembered(): boolean | null {
  try {
    const v = localStorage.getItem(cKey);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

// ---- the model's state, and what follows from it (pure) ----------------------

/** Where Smart search's model is, as the device last said. */
export type SmartModel = { ready: boolean; downloading: boolean; percent: number; problem: string };

/** A device that doesn't say (before release 152), or not asked yet. */
export const cModelReady: SmartModel = Object.freeze({ ready: true, downloading: false, percent: 100, problem: "" });

/** The device's Settings.smart_search_model; absent = ready. */
export function modelFrom(m: SmartSearchModel | undefined): SmartModel {
  if (!m) return cModelReady;
  const percent = Number.isFinite(m.percent) ? Math.max(0, Math.min(100, Math.round(m.percent))) : 0;
  return { ready: m.ready, downloading: !m.ready && m.downloading, percent, problem: m.ready ? "" : (m.problem ?? "").trim() };
}

const sameModel = (a: SmartModel, b: SmartModel) =>
  a.ready === b.ready && a.downloading === b.downloading && a.percent === b.percent && a.problem === b.problem;

/** Whether the search offers "Search photos for …" (and Enter takes it):
 *  on, with the model there. */
export const offersSmartSearch = (isOn: boolean | null, m: SmartModel) => isOn === true && m.ready;

/** What Settings' section says under its hint:
 *  - ready: nothing (the model is there, or the device doesn't say);
 *  - note: off, without the model - turning it on downloads it (Smart
 *    search is on by default since release 152, so this is only said
 *    while it is off);
 *  - idle: on, the model neither there nor on its way yet - nothing;
 *  - downloading: its percent, or the device's words while a try failed
 *    (it tries again by itself). */
export type ModelView =
  | { kind: "ready" }
  | { kind: "note" }
  | { kind: "idle" }
  | { kind: "downloading"; percent: number; problem: string };

export function modelView(isOn: boolean | null, m: SmartModel): ModelView {
  if (m.ready) return { kind: "ready" };
  if (m.downloading) return { kind: "downloading", percent: m.percent, problem: m.problem };
  return isOn === true ? { kind: "idle" } : { kind: "note" };
}

/** What a screen reader is told as the section changes - only when the
 *  download starts (or starts again after a problem), runs into a problem
 *  and is done (as 100%), never at every percent; null: nothing new to
 *  say. `before` is null for the first view, which isn't announced. */
export type Announcement = { kind: "progress"; percent: number } | { kind: "problem"; text: string };

export function announcement(before: ModelView | null, now: ModelView): Announcement | null {
  if (!before) return null;
  if (now.kind === "downloading") {
    if (now.problem) return before.kind === "downloading" && before.problem === now.problem ? null : { kind: "problem", text: now.problem };
    return before.kind === "downloading" && !before.problem ? null : { kind: "progress", percent: now.percent };
  }
  if (now.kind === "ready" && before.kind === "downloading") return { kind: "progress", percent: 100 };
  return null;
}

// ---- the store ------------------------------------------------------------------

let on: boolean | null = remembered();
let model: SmartModel = cModelReady;
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => { listeners.delete(l); };
};
const changed = () => {
  listeners.forEach((l) => l());
  follow();
};
/** true while on; false while off or on a device before it; null: not
 *  known yet. */
export const getSmartSearchOn = () => on;
export const useSmartSearchOn = () => useSyncExternalStore(subscribe, getSmartSearchOn);
export const getSmartSearchModel = () => model;
export const useSmartSearchModel = () => useSyncExternalStore(subscribe, getSmartSearchModel);
/** Whether the search offers the description now (offersSmartSearch). */
export const useSmartSearchOffered = () =>
  useSyncExternalStore(subscribe, () => offersSmartSearch(on, model));

function setOn(next: boolean) {
  try { localStorage.setItem(cKey, next ? "1" : "0"); } catch { /* private mode */ }
  if (!next) clearText();
  if (next === on) return;
  on = next;
  changed();
}

// Bumped by every change made here (a switch the device acknowledged, a
// search told "smart_search_off"): a Settings answer to a question asked
// before it is older than what the app knows, and is dropped.
let changes = 0;

/** A change the device acknowledged (Settings' switch), or a search it
 *  told "smart_search_off". Not on: the free text goes. */
export function setSmartSearchOn(next: boolean) {
  changes++;
  setOn(next);
}

function setModel(next: SmartModel) {
  if (sameModel(next, model)) return;
  model = next;
  changed();
}

/** A Settings answer's switch and model (absent: a device before them). */
export function applySmartSearchSettings(s: Settings) {
  setOn(s.smartSearchEnabled === true);
  setModel(modelFrom(s.smartSearchModel));
}

// A sign-in's answers only: one that comes after a sign-out or another
// sign-in is dropped.
let generation = 0;
// The question on its way, and the changes made before it was asked.
let asking: { answer: Promise<boolean>; changes: number } | null = null;

/** Asks the device's Settings once and applies Smart search's parts; a
 *  caller while a question is on its way shares its answer, unless the
 *  app changed something since it was asked. true once applied. */
export function refreshSmartSearch(): Promise<boolean> {
  if (asking && asking.changes === changes) return asking.answer;
  const gen = generation;
  const seen = changes;
  const answer = (async () => {
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqGetSettings", reqGetSettings: {} };
      });
      if (gen !== generation || seen !== changes || resp.payload?.$case !== "respSettings") return false;
      applySmartSearchSettings(resp.payload.respSettings);
      return true;
    } catch {
      return false;
    }
  })();
  const mine = { answer, changes: seen };
  asking = mine;
  void answer.finally(() => {
    if (asking === mine) asking = null;
    follow();
  });
  return answer;
}

// ---- following a download ----------------------------------------------------

export const cFollowMs = 3000;
let followers = 0;
let followTimer: ReturnType<typeof setTimeout> | undefined;

// While Settings shows the download (on, downloading), the next question
// is on its way, 3 s after the last answer.
function follow() {
  const want = followers > 0 && on === true && model.downloading;
  if (!want && followTimer !== undefined) {
    clearTimeout(followTimer);
    followTimer = undefined;
  }
  if (!want || followTimer !== undefined || asking) return;
  followTimer = setTimeout(() => {
    followTimer = undefined;
    void refreshSmartSearch();
  }, cFollowMs);
}

/** Settings shows the download: the device is asked every 3 s while Smart
 *  search is on and the model downloading - until it is ready, it is
 *  turned off or this stops (Settings left). Returns the stop. */
export function followSmartSearch(): () => void {
  followers++;
  follow();
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    followers--;
    follow();
  };
}

// ---- the search field --------------------------------------------------------------

export const cFocusAskMs = 5000;
let focusAskedAt = Number.NEGATIVE_INFINITY;

/** The top search field got the focus: while Smart search is on and its
 *  model isn't there, where it is is asked once (at most every 5 s), so
 *  the search offers descriptions once it is - whether or not Settings
 *  was watching. true when it asked. */
export function smartSearchFocused(now = Date.now()): boolean {
  if (on !== true || model.ready || now - focusAskedAt < cFocusAskMs) return false;
  focusAskedAt = now;
  void refreshSmartSearch();
  return true;
}

// ---- at sign-in ---------------------------------------------------------------

const cRetryFirstMs = 1000;
const cRetryMaxMs = 30_000;

/** Asks the device while signed in whether Smart search is on: at once,
 *  then again after a failure (1 s doubling to 30 s) until it answers.
 *  Returns the stop. */
export function watchSmartSearch(): () => void {
  const gen = ++generation;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let fails = 0;
  const ask = async () => {
    const done = await refreshSmartSearch();
    if (done || gen !== generation) return;
    timer = setTimeout(() => void ask(), Math.min(cRetryFirstMs * 2 ** fails++, cRetryMaxMs));
  };
  void ask();
  return () => {
    clearTimeout(timer);
    if (gen !== generation) return;
    generation++;
    // Another sign-in may be another user's library, with its own switch.
    clearText();
  };
}
