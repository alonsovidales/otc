// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import { RaidState } from "../proto/messages";
import type { ReqEnvelope, RespEnvelope, Status as MsgStatus } from "../proto/messages";

// A size the device reports in MB, readable: "4.6 GB", "984 GB", "820 MB" -
// the same as sizeText in the iOS and Android apps.
export function formatMB(n?: number | null) {
  if (n === undefined || n === null || Number.isNaN(n)) return "—";
  if (n >= 100_000) return `${Math.round(n / 1000)} GB`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)} GB`;
  return `${Math.round(n)} MB`;
}

export function round(num: number) {
  return Math.max(0, Math.min(100, Math.round(num * 100) / 100));
}

export function pct(used?: number | null, total?: number | null) {
  if (!used || !total || total <= 0) return 0;
  return round((used / total) * 100);
}

// Issue #65: the RAID's own health, always visible.
export function raidStateLabel(status: MsgStatus): string {
  switch (status.raidState) {
    case RaidState.RaidNone: return "No mirror";
    case RaidState.RaidInSync: return "Mirror in sync";
    case RaidState.RaidSyncing: return `Mirror syncing (${round(status.raidSyncPercent)}%)`;
    case RaidState.RaidDegraded: return "Mirror degraded";
    default: return "Mirror state unknown";
  }
}

/** The device's status (storage, mirror, CPU, memory, errors), polled
 *  while connected. */
export function useDeviceStatus(refreshMs = 5000) {
  const [status, setStatus] = useState<MsgStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let stop = false;
    async function fetchStatus() {
      if (!useWS.connected()) return;
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as { payload?: ReqEnvelope["payload"] }).payload = { $case: "reqGetStatus", reqGetStatus: {} };
        });
        if (stop) return;
        if (resp.error) {
          setErr(resp.errorMessage || "Could not read the device's status");
          return;
        }
        if (resp.payload?.$case === "respStatus") {
          setStatus(resp.payload.respStatus);
          setErr(null);
        }
      } catch (ex) {
        if (!stop) setErr(ex instanceof Error ? ex.message : String(ex));
      }
    }
    void fetchStatus();
    const t = setInterval(fetchStatus, refreshMs);
    return () => { stop = true; clearInterval(t); };
  }, [refreshMs]);

  return { status, err };
}
