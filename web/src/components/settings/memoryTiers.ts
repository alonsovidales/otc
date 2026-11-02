// SPDX-License-Identifier: AGPL-3.0-or-later

// What a device's memory allows (release 160; owner, 2026-11-01; since
// release 164, issue #209: "Opt-in on 4 GB"): one with less than 3 GB (a 2
// GB board) has no Image Tagging and takes no other users; one with less
// than 6 GB (a 4 GB board) may turn Image Tagging on, off until its user
// does, and says what it costs there. Its Settings say so
// (Settings.image_tagging_available, image_tagging_heavy) and so does its
// users list (RespUsers.can_add); turning tagging on or adding a user where
// it can't is refused with these codes and the device's words. Unset - an
// older device - is as before: available, nothing to say. Settings' Image
// Tagging section shows the switch off and disabled where it isn't
// available, with the heavy note under it where it costs more, Settings ›
// Users a note in place of Add User; existing users keep working. `node
// web/scripts/memory-tiers-test.mjs`; the phones follow the same rules.
import type { RespEnvelope, RespUsers, Settings } from "../../proto/messages";

export const cTaggingUnavailable = "tagging_unavailable";
export const cUsersUnavailable = "users_unavailable";

/** Image Tagging's switch as Settings shows it. */
export interface TaggingSwitch {
  on: boolean;
  available: boolean;
  /** The note under it: "On this device Image Tagging uses about 1–2 GB
   *  more memory…" (a device under 6 GB that can have it). */
  heavy: boolean;
}

/** From the device's Settings: off and unavailable where it can't have it,
 *  with what it costs where it is heavy. */
export function taggingSwitch(s: Pick<Settings, "imageTaggingEnabled" | "imageTaggingAvailable" | "imageTaggingHeavy">): TaggingSwitch {
  const available = s.imageTaggingAvailable !== false;
  return { on: available && !!s.imageTaggingEnabled, available, heavy: available && s.imageTaggingHeavy === true };
}

/** After a SetImageTaggingEnabled to `enabled`: what the device took, off
 *  and unavailable when it refused for its memory, else as it was. */
export function taggingAfterSet(
  prev: TaggingSwitch,
  enabled: boolean,
  resp: Pick<RespEnvelope, "payload" | "errorCode">,
): TaggingSwitch {
  if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) return { ...prev, on: enabled };
  if (resp.errorCode === cTaggingUnavailable) return { on: false, available: false, heavy: false };
  return prev;
}

/** Whether a users list says Add User is offered (unset: it is). */
export const canAddUsers = (r: Pick<RespUsers, "canAdd">): boolean => r.canAdd !== false;

/** Add User after a ListUsers or CreateUser answer: as a list says, not
 *  after a CreateUser refused for the device's memory, else as it was. */
export function canAddAfter(prev: boolean, resp: Pick<RespEnvelope, "payload" | "errorCode">): boolean {
  if (resp.payload?.$case === "respUsers") return canAddUsers(resp.payload.respUsers);
  if (resp.errorCode === cUsersUnavailable) return false;
  return prev;
}
