// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope, Settings } from "../proto/messages";
import { clearPeople } from "./photoFilter";

// Whether face recognition is on (Settings.face_recognition_enabled, off
// by default): People - the menu item, its page, the people in the top
// bar's search, a person in the Images search, Info's People block - is
// there only while it is, or while Pets is (below). Read once after
// sign-in (watchFaceRecognition, from App) and set by Settings when its
// switch is turned, so People comes and goes at once.
//
// Until the device has answered, the answer this browser had last time
// stands in (otc_face_recognition), so a reload neither shows People and
// then takes it away nor the other way round. With no answer ever (a
// first sign-in here) it is null and People stays hidden until the device
// says: it is off on most devices, and People appearing is gentler than
// People vanishing.
//
// Pets (release 180, Settings.pets_enabled, off by default) finds the dogs
// and cats in the photos, each a person in People: People is there while
// face recognition or Pets is on (usePeopleOn) - else a new device's pets
// would be found but out of reach. Pets' switch is read with face
// recognition's, remembered as it is (otc_pets) and set by Settings' Pets
// switch. A device before Pets doesn't say: People follows face
// recognition alone, as before.

const cKey = "otc_face_recognition";
const cPetsKey = "otc_pets";

function remembered(key: string): boolean | null {
  try {
    const v = localStorage.getItem(key);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

function remember(key: string, on: boolean) {
  try { localStorage.setItem(key, on ? "1" : "0"); } catch { /* private mode */ }
}

/** Whether People is there: face recognition or Pets on; null: not known
 *  yet. Pets not known (no answer yet from a device that has it) counts
 *  as off: People waits for the device rather than coming and going. */
export const peopleOn = (faces: boolean | null, pets: boolean | null): boolean | null =>
  faces === true || pets === true ? true : faces;

let enabled: boolean | null = remembered(cKey);
let pets: boolean | null = remembered(cPetsKey);
const listeners = new Set<() => void>();

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}
/** Face recognition's switch: true or false as the device said (or last
 *  said here); null: not known yet. */
export const getFaceRecognition = () => enabled;
export const useFaceRecognition = () => useSyncExternalStore(subscribe, getFaceRecognition);
/** Pets' switch, likewise (false on a device before Pets). */
export const getPetsOn = () => pets;
export const usePetsOn = () => useSyncExternalStore(subscribe, getPetsOn);
/** Whether People is there now (peopleOn). */
export const getPeopleOn = () => peopleOn(enabled, pets);
export const usePeopleOn = () => useSyncExternalStore(subscribe, getPeopleOn);

// The switches given, both at once (no moment with only one of them
// changed), each remembered for the next load. People gone: nobody to
// search for any more.
function apply(next: { faces?: boolean; pets?: boolean }) {
  let moved = false;
  if (next.faces !== undefined) {
    remember(cKey, next.faces);
    if (next.faces !== enabled) { enabled = next.faces; moved = true; }
  }
  if (next.pets !== undefined) {
    remember(cPetsKey, next.pets);
    if (next.pets !== pets) { pets = next.pets; moved = true; }
  }
  if (peopleOn(enabled, pets) === false) clearPeople();
  if (moved) listeners.forEach((l) => l());
}

/** Face recognition's switch, as the device acknowledged it (Settings). */
export function setFaceRecognition(on: boolean) {
  apply({ faces: on });
}

/** Pets' switch, as the device acknowledged it (Settings). */
export function setPetsOn(on: boolean) {
  apply({ pets: on });
}

/** A Settings answer's switches: face recognition's, and Pets' (absent on
 *  a device before Pets: off, so People follows face recognition alone). */
export function applyPeopleSettings(s: Settings) {
  apply({ faces: !!s.faceRecognitionEnabled, pets: s.petsEnabled === true });
}

const cRetryFirstMs = 1000;
const cRetryMaxMs = 10_000;

/** Asks the device while signed in: at once, then again after a failure
 *  (1 s doubling to 10 s) until it answers. Returns the stop. */
export function watchFaceRecognition(): () => void {
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
        applyPeopleSettings(resp.payload.respSettings);
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
  };
}
