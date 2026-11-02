// SPDX-License-Identifier: AGPL-3.0-or-later

// Settings > Shut Down Device (issue #202, release 164), in the Device
// version section (UpdatePanel): the owner of the primary instance sends
// ShutDownDevice; the device answers the generic Ack before anything
// happens, powers itself off a few seconds later and stays off until it is
// unplugged and plugged in again. A device before it answers
// unknown_payload ("needs an update"); any other refusal (an update or a
// Wi-Fi change running, just started, a supervised user's instance, Docker,
// the demo) comes in the device's words. Once it agreed, the app leaves it
// alone for a while (net/shutDownHold.ts). `node
// web/scripts/shut-down-test.mjs`; the phones follow the same rules.
import type { RespEnvelope } from "../../proto/messages";
import { asIs, say, type UiText } from "../uiText";

/** What the device said to ShutDownDevice: null when it agreed, else the
 *  words to show - "needs an update" for a device before it, the device's
 *  own for any other refusal, ours when it gave none. */
export function shutDownRefusal(resp: Pick<RespEnvelope, "payload" | "error" | "errorCode" | "errorMessage">): UiText | null {
  if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) return null;
  if (resp.error && resp.errorCode === "unknown_payload") return say("web.settings.shutdown.needs_update");
  if (resp.errorMessage) return asIs(resp.errorMessage);
  if (resp.payload?.$case === "respAck" && resp.payload.respAck.errorMsg) return asIs(resp.payload.respAck.errorMsg);
  return say("web.settings.shutdown.failed");
}
