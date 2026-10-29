// SPDX-License-Identifier: AGPL-3.0-or-later

// Places, the map in Images (PlacesView.tsx): whether it is on, where the
// map was left, and when what it holds goes - the places and thumbnails
// themselves are placesData.ts', loaded with the map. Module state, so
// going to Images and back keeps it all: the map comes back as it was,
// with nothing asked again.
//
// Places is off unless the user turns it on in Settings
// (Settings.places_enabled: the device keeps where each photo was taken,
// unencrypted, only while it is on; a device before Places doesn't say).
// The device is asked once per sign-in (watchPlaces, from App); Settings'
// switch and a map told "places_off" (turned off on another app) set it
// at once. While it isn't on there is no menu item, no map and no area
// filter (photoFilter.ts), as People without face recognition. Until the
// device has answered, the answer this browser had last time stands in
// (otc_places), as faceRecognition.ts does for People.

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";
import { leaveArea } from "./photoFilter";

// ---- whether Places is on ----------------------------------------------------

const cKey = "otc_places";

function remembered(): boolean | null {
  try {
    const v = localStorage.getItem(cKey);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

let on: boolean | null = remembered();
const onListeners = new Set<() => void>();
const subscribeOn = (l: () => void) => {
  onListeners.add(l);
  return () => { onListeners.delete(l); };
};
/** true or false as the device said (or last said here); null: not known
 *  yet. False too on a device before Places. */
export const getPlacesOn = () => on;
export const usePlacesOn = () => useSyncExternalStore(subscribeOn, getPlacesOn);

/** The device's answer: from Settings, a change it acknowledged, or a map
 *  it told "places_off". Turned off: the map's places and an open area go. */
export function setPlacesOn(next: boolean) {
  try { localStorage.setItem(cKey, next ? "1" : "0"); } catch { /* private mode */ }
  if (!next) {
    leaveArea();
    if (on) forgetPlaces();
  }
  if (next === on) return;
  on = next;
  onListeners.forEach((l) => l());
}

const cRetryFirstMs = 1000;
const cRetryMaxMs = 30_000;

/** Asks the device while signed in whether Places is on: at once, then
 *  again after a failure (1 s doubling to 30 s) until it answers. Returns
 *  the stop. */
export function watchPlaces(): () => void {
  // Another sign-in may be another user's library: nothing of the last
  // one's is kept, nor once signed out.
  forgetPlaces();
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
        // Absent: a device before Places.
        setPlacesOn(resp.payload.respSettings.placesEnabled === true);
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
    forgetPlaces();
  };
}

// ---- when what the map holds goes ----------------------------------------------

const dropListeners = new Set<(signedOut: boolean) => void>();

/** Told when the places go (placesData.ts): the library changed, or
 *  (signedOut) the session ended or another began, which takes the
 *  thumbnails too. */
export function onDropPlaces(l: (signedOut: boolean) => void): () => void {
  dropListeners.add(l);
  return () => { dropListeners.delete(l); };
}

/** The library changed (a delete or an upload in Images): every place is
 *  asked again when next shown. */
export function dropPlaces() {
  dropListeners.forEach((l) => l(false));
}

/** Every place, thumbnail and the map's last view gone. */
function forgetPlaces() {
  dropListeners.forEach((l) => l(true));
  camera = null;
}

// ---- where the map was -----------------------------------------------------

/** The map's view when it was left, under the filter it showed: back from
 *  Images (Back, or the menu) it opens there again. */
export type Camera = { filter: string; lng: number; lat: number; zoom: number };
let camera: Camera | null = null;
export const lastCamera = () => camera;
export const keepCamera = (c: Camera) => { camera = c; };
