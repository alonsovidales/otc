// SPDX-License-Identifier: AGPL-3.0-or-later

// Places, the map in Images (PlacesView.tsx): whether the device has it,
// where the map was left, and when what it holds goes - the places and
// thumbnails themselves are placesData.ts', loaded with the map. Module
// state, so going to Images and back keeps it all: the map comes back as
// it was, with nothing asked again.
//
// The device is asked once per sign-in (watchPlaces, from App): a device
// that answers "unknown_payload" has no Places - no menu item, and the
// grid's area filter (photoFilter.ts) is never sent to it. Until it has
// answered, the answer this browser had last time stands in
// (otc_places), as faceRecognition.ts does for People.

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";

// ---- whether the device has Places --------------------------------------------

const cKey = "otc_places";

function remembered(): boolean | null {
  try {
    const v = localStorage.getItem(cKey);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

let supported: boolean | null = remembered();
const supportListeners = new Set<() => void>();
const subscribeSupport = (l: () => void) => {
  supportListeners.add(l);
  return () => { supportListeners.delete(l); };
};
const getSupport = () => supported;

/** true or false as the device said (or last said here); null: not known yet. */
export const usePlacesSupported = () => useSyncExternalStore(subscribeSupport, getSupport);

function setSupported(on: boolean) {
  try { localStorage.setItem(cKey, on ? "1" : "0"); } catch { /* private mode */ }
  if (on === supported) return;
  supported = on;
  supportListeners.forEach((l) => l());
}

const cRetryFirstMs = 1000;
const cRetryMaxMs = 30_000;

/** Asks the device while signed in whether it has Places: at once, then
 *  again after a failure (1 s doubling to 30 s) until it answers. Returns
 *  the stop. The question is the cheapest Places request there is: no
 *  tiles, the whole library's count and area. */
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
        e.payload = { $case: "reqGetPhotoPlaces", reqGetPhotoPlaces: { zoom: 0, tiles: [], tags: [], personIds: [], includeVideos: true, groupId: "" } };
      });
      if (stopped) return;
      if (resp.payload?.$case === "respPhotoPlaces") { setSupported(true); return; }
      if (resp.errorCode === "unknown_payload") { setSupported(false); return; }
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
