// SPDX-License-Identifier: AGPL-3.0-or-later

// Smart search (beta): finding photos by describing them ("kids in the
// snow", "un perro en la playa"), on the device's own model. Off unless the
// user turns it on in Settings (Settings.smart_search_enabled: while it is
// on the device describes each photo and keeps that encrypted; turning it
// off deletes it). A device before it doesn't say, and then there is
// neither the switch nor the free-text search (SearchPhotos.text: an older
// device would ignore it and answer the whole library).
//
// The device is asked once per sign-in (watchSmartSearch, from App);
// Settings' switch and a search told "smart_search_off" (turned off on
// another app) set it at once. Until the device has answered, the answer
// this browser had last time stands in (otc_smart_search), as places.ts
// does. While it isn't on, the search has no free text (photoFilter.ts).

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";
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

let on: boolean | null = remembered();
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => { listeners.delete(l); };
};
/** true while on; false while off or on a device before it; null: not
 *  known yet. */
export const getSmartSearchOn = () => on;
export const useSmartSearchOn = () => useSyncExternalStore(subscribe, getSmartSearchOn);

/** The device's answer: from Settings, a change it acknowledged, or a
 *  search it told "smart_search_off". Not on: the free text goes. */
export function setSmartSearchOn(next: boolean) {
  try { localStorage.setItem(cKey, next ? "1" : "0"); } catch { /* private mode */ }
  if (!next) clearText();
  if (next === on) return;
  on = next;
  listeners.forEach((l) => l());
}

const cRetryFirstMs = 1000;
const cRetryMaxMs = 30_000;

/** Asks the device while signed in whether Smart search is on: at once,
 *  then again after a failure (1 s doubling to 30 s) until it answers.
 *  Returns the stop. */
export function watchSmartSearch(): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let fails = 0;
  const ask = async () => {
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqGetSettings", reqGetSettings: {} };
      });
      if (stopped) return;
      if (resp.payload?.$case === "respSettings") {
        // Absent: a device before Smart search.
        setSmartSearchOn(resp.payload.respSettings.smartSearchEnabled === true);
        return;
      }
    } catch {
      if (stopped) return;
    }
    timer = setTimeout(() => void ask(), Math.min(cRetryFirstMs * 2 ** fails++, cRetryMaxMs));
  };
  void ask();
  return () => {
    stopped = true;
    clearTimeout(timer);
    // Another sign-in may be another user's library, with its own switch.
    clearText();
  };
}
