// SPDX-License-Identifier: AGPL-3.0-or-later

// What a device's memory allows (release 160; owner, 2026-11-01): one with
// less than 6 GB has no Image Tagging, one with less than 3 GB (a 2 GB
// board) takes no other users. Its Settings say so
// (Settings.image_tagging_available) and so does its users list
// (RespUsers.can_add); turning tagging on or adding a user there is refused
// with these codes and the device's words. Unset - an older device - is as
// before: available. Settings' Image Tagging section shows the switch off
// and disabled, Settings › Users a note in place of Add User; existing users
// keep working. `node web/scripts/memory-tiers-test.mjs`; the phones follow
// the same rules.
import type { RespEnvelope, RespUsers, Settings } from "../../proto/messages";

export const cTaggingUnavailable = "tagging_unavailable";
export const cUsersUnavailable = "users_unavailable";

/** Image Tagging's switch as Settings shows it. */
export interface TaggingSwitch {
  on: boolean;
  available: boolean;
}

/** From the device's Settings: off and unavailable where it can't have it. */
export function taggingSwitch(s: Pick<Settings, "imageTaggingEnabled" | "imageTaggingAvailable">): TaggingSwitch {
  const available = s.imageTaggingAvailable !== false;
  return { on: available && !!s.imageTaggingEnabled, available };
}

/** After a SetImageTaggingEnabled to `enabled`: what the device took, off
 *  and unavailable when it refused for its memory, else as it was. */
export function taggingAfterSet(
  prev: TaggingSwitch,
  enabled: boolean,
  resp: Pick<RespEnvelope, "payload" | "errorCode">,
): TaggingSwitch {
  if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) return { ...prev, on: enabled };
  if (resp.errorCode === cTaggingUnavailable) return { on: false, available: false };
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
