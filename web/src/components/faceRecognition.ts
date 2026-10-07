// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";
import { clearPeople } from "./photoFilter";

// Whether face recognition is on (Settings.face_recognition_enabled, off
// by default): People - the menu item, its page, the people in the top
// bar's search, a person in the Images search - is there only while it
// is. Read once after sign-in (watchFaceRecognition, from App) and set by
// Settings when its switch is turned, so People comes and goes at once.
//
// Until the device has answered, the answer this browser had last time
// stands in (otc_face_recognition), so a reload neither shows People and
// then takes it away nor the other way round. With no answer ever (a
// first sign-in here) it is null and People stays hidden until the device
// says: it is off on most devices, and People appearing is gentler than
// People vanishing.

const cKey = "otc_face_recognition";

function remembered(): boolean | null {
  try {
    const v = localStorage.getItem(cKey);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

let enabled: boolean | null = remembered();
const listeners = new Set<() => void>();

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}
const get = () => enabled;

/** true or false as the device said (or last said here); null: not known yet. */
export const useFaceRecognition = () => useSyncExternalStore(subscribe, get);

/** The device's answer: from Settings, or a change it acknowledged. */
export function setFaceRecognition(on: boolean) {
  try { localStorage.setItem(cKey, on ? "1" : "0"); } catch { /* private mode */ }
  // Turned off: nobody to search for any more.
  if (!on) clearPeople();
  if (on === enabled) return;
  enabled = on;
  listeners.forEach((l) => l());
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
        setFaceRecognition(!!resp.payload.respSettings.faceRecognitionEnabled);
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
