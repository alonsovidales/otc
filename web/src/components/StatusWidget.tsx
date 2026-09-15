// SPDX-License-Identifier: AGPL-3.0-or-later

import React, { useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import { RaidState } from "../proto/messages";
import type { ReqEnvelope, RespEnvelope, Status as MsgStatus } from "../proto/messages";

type Props = {
  refreshMs?: number;       // default 2000
  className?: string;
};

function formatMB(n?: number | null) {
  if (n == null) return "-";
  return `${n.toLocaleString()} MB`;
}

function round(num: number) {
  return Math.max(0, Math.min(100, Math.round(num * 100) / 100));
}

function pct(used?: number | null, total?: number | null) {
  if (!used || !total || total <= 0) return 0;
  return round((used/total) * 100);
}

// Issue #65: a plain, always-visible read of the RAID's own health -
// "in sync"/"syncing"/"degraded" - rather than something the user has to
// infer from whether an error banner happens to be showing.
function raidStateLabel(status: MsgStatus): string {
  switch (status.raidState) {
    case RaidState.RaidNone: return "No RAID";
    case RaidState.RaidInSync: return "In sync";
    case RaidState.RaidSyncing: return `Syncing (${round(status.raidSyncPercent)}%)`;
    case RaidState.RaidDegraded: return "Degraded";
    default: return "Unknown";
  }
}

const StatusWidget: React.FC<Props> = ({ refreshMs = 2000, className }) => {
  const [status, setStatus] = useState<MsgStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const hasErrors = (status?.errors?.length || 0) > 0;

  // derive usage values
  const usedPct = useMemo(() => pct(status?.raidUsage, status?.raidSize), [status]);
  const freeMB = useMemo(
    () => (status ? Math.max(0, (status.raidSize || 0) - (status.raidUsage || 0)) : 0),
    [status]
  );

  async function fetchStatus() {
    if (!useWS.connected()) return;
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetStatus", reqGetStatus: {} };
      });
      if (resp.error) {
        setErr(resp.errorMessage ?? "Unknown error");
        setStatus(null);
        return;
      }
      const p = (resp as any).payload;
      if (p?.$case === "respStatus") {
        setStatus(p.respStatus as MsgStatus);
        setErr(null);
      } else {
        setErr("Unexpected response");
        setStatus(null);
      }
    } catch (ex: any) {
      setErr(ex?.message || String(ex));
      setStatus(null);
    }
  }

  useEffect(() => {
    fetchStatus();
    if (refreshMs > 0) {
      const t = setInterval(fetchStatus, refreshMs);
      return () => clearInterval(t);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [useWS.connected(), refreshMs]);

  return (
    <div className={`status-widget2 ${className ?? ""} ${hasErrors ? "has-errors" : ""}`}>
      {/* Collapsed face (270x70) */}
      <div className="sw2-face" title={useWS.connected() ? "Server status" : "Disconnected"}>
        {/* RAID usage bar - a single fill color, chosen by how full the
            bar actually is (not a fixed zoned gradient the fill happened
            to sit on top of), so "yellow"/"red" always means "getting
            full", not "used% for other reasons crossed some x position". */}
        <div className="sw2-bar-row">
          <div className="sw2-bar">
            <div
              className={`sw2-bar-used${usedPct >= 90 ? " crit" : usedPct >= 70 ? " warn" : ""}`}
              style={{ width: `${usedPct}%` }}
            />
            {/* Sits in a dark chip rather than plain text so it stays
                readable at any fill level - at low usedPct it's over the
                bare dark track, at high usedPct it's over the (light)
                fill color, and a fixed text color can't read well on both. */}
            <span className="sw2-bar-pct">{usedPct}%</span>
          </div>
          {hasErrors && <span className="sw2-alert" title="Needs attention">⚠️</span>}
        </div>
      </div>

      {/* Floating details (overlay; does NOT resize layout) */}
      <div className="sw2-pop">
        {status ? (
          <div className="sw2-grid">
            <div><strong>Free:</strong> {formatMB(freeMB)}</div>
            <div><strong>Disks:</strong> {status.disks || "-"}</div>
            <div><strong>RAID:</strong> {status.raidLevel ? `${status.raidLevel} — ${raidStateLabel(status)}` : raidStateLabel(status)}</div>
            <div><strong>Used:</strong> {formatMB(status.raidUsage)} / {formatMB(status.raidSize)}</div>
            <div><strong>Disk:</strong> {formatMB(status.diskUsage)} / {formatMB(status.diskSize)}</div>
            <div><strong>CPU:</strong> {status.cpuUsagePrc != null ? `${round(status.cpuUsagePrc)}%` : "-"}</div>
            <div><strong>Mem:</strong> {formatMB(status.memUsage)} / {formatMB(status.memSize)}</div>
            {/* Same bottom slot either way: every error listed if there are
                any, or a plain all-clear line if not - not a duplicate
                top-of-popover message repeating the first error. */}
            {hasErrors ? (
              <div className="sw2-errors">
                {status.errors!.map((e, i) => (
                  <div key={i}>• {e.Message || String(e.StatusErrorCode)}</div>
                ))}
              </div>
            ) : (
              <div className="sw2-summary ok">All systems nominal</div>
            )}
          </div>
        ) : (
          <div className="sw2-grid">{err ? err : "No data"}</div>
        )}
      </div>
    </div>
  );
};

export default StatusWidget;
