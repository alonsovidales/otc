// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Whether this device is the primary instance (GetInstanceRole), for the
// Settings sections only it has: Tailscale, Update and Logs (Users asks
// again on its own timer). null while it is asked. With no answer - the
// socket down, the bridge answering for the device, a device that can't
// reach its storage - the section stays out and is asked again once the
// device answers again (useDeviceBack): Settings opened while the device
// was away used to leave it out until a reload. The device's own refusal
// is a no.
import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { isBridgeAnswer, isStorageUnavailable } from "../net/deviceStatus";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { useDeviceBack } from "./useDeviceBack";

export function useInstanceRole(): boolean | null {
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const back = useDeviceBack(() => void askRef.current());
  const ask = useCallback(async () => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
      });
      if (!isBridgeAnswer(resp) && !isStorageUnavailable(resp.errorCode)) {
        back.ok();
        setIsPrimary(resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary);
        return;
      }
    } catch {
      // No answer: as below.
    }
    setIsPrimary(false);
    back.failed();
  }, [back]);
  const askRef = useRef(ask);
  askRef.current = ask;
  useEffect(() => { void ask(); }, [ask]);
  return isPrimary;
}
