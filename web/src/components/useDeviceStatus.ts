// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import { RaidState } from "../proto/messages";
import type { ReqEnvelope, RespEnvelope, Status as MsgStatus } from "../proto/messages";
import type { TFunction } from "../i18n";
import { percentText, unitText } from "./uiText";
import { pollDevice } from "./devicePoll";

// A size the device reports in MB, readable: "4.6 GB", "984 GB", "820 MB" -
// the same as sizeText in the iOS and Android apps - in the language's own
// number format ("4,6 GB" in German; uiText.ts).
export function formatMB(t: TFunction, n?: number | null) {
  if (n === undefined || n === null || Number.isNaN(n)) return "—";
  if (n >= 100_000) return unitText(t, Math.round(n / 1000), "gigabyte", 0);
  if (n >= 1000) return unitText(t, n / 1000, "gigabyte", 1);
  return unitText(t, Math.round(n), "megabyte", 0);
}

export function round(num: number) {
  return Math.max(0, Math.min(100, Math.round(num * 100) / 100));
}

export function pct(used?: number | null, total?: number | null) {
  if (!used || !total || total <= 0) return 0;
  return round((used / total) * 100);
}

// Issue #65: the RAID's own health, always visible.
export function raidStateLabel(t: TFunction, status: MsgStatus): string {
  switch (status.raidState) {
    case RaidState.RaidNone: return t("web.shell.storage.mirror_none");
    case RaidState.RaidInSync: return t("web.shell.storage.mirror_in_sync");
    case RaidState.RaidSyncing: return t("web.shell.storage.mirror_syncing", { percent: percentText(t, round(status.raidSyncPercent), 2) });
    case RaidState.RaidDegraded: return t("web.shell.storage.mirror_degraded");
    default: return t("web.shell.storage.mirror_unknown");
  }
}

/** The device's status (storage, mirror, CPU, memory, errors), polled
 *  (devicePoll.ts: a closed socket is opened again, a failure waits
 *  longer); `failed` while the last poll got no status. */
export function useDeviceStatus(refreshMs = 5000) {
  const [status, setStatus] = useState<MsgStatus | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let stop = false;
    const fetchStatus = async (): Promise<boolean> => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as { payload?: ReqEnvelope["payload"] }).payload = { $case: "reqGetStatus", reqGetStatus: {} };
        });
        if (stop) return true;
        if (resp.payload?.$case === "respStatus") {
          setStatus(resp.payload.respStatus);
          setFailed(false);
          return true;
        }
        setFailed(true);
        return false;
      } catch {
        if (!stop) setFailed(true);
        return false;
      }
    };
    const cancel = pollDevice(fetchStatus, refreshMs);
    return () => { stop = true; cancel(); };
  }, [refreshMs]);

  return { status, failed };
}
