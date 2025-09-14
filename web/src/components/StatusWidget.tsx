import React, { useEffect, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
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

  // tooltip text when collapsed
  const summary =
    err ? err :
    hasErrors ? (status!.errors![0].Message || "Error") :
    "All systems nominal";

  return (
    <div className={`status-widget2 ${className ?? ""} ${hasErrors ? "has-errors" : ""}`}>
      {/* Collapsed face (270x70) */}
      <div className="sw2-face" title={useWS.connected() ? "Server status" : "Disconnected"}>
        {/* RAID usage bar with tri-color palette */}
        <div className="sw2-bar">
          {/* background shows 0–60 green, 60–90 yellow, 90–100 red */}
          <div className="sw2-bar-bg" />
          {/* used overlay simply clips to used% */}
          <div className="sw2-bar-used" style={{ width: `${usedPct}%` }} />
        </div>
        <div className="sw2-bar-labels">
          <span>Used: {formatMB(status?.raidUsage)} ({usedPct}%)</span>
        </div>
      </div>

      {/* Floating details (overlay; does NOT resize layout) */}
      <div className="sw2-pop">
        {/* one-line summary / error */}
        <div className={`sw2-summary ${err || hasErrors ? "bad" : "ok"}`}>
          {summary}
        </div>
        {status ? (
          <div className="sw2-grid">
            <div><strong>Free:</strong> {formatMB(freeMB)}</div>
            <div><strong>Disks:</strong> {status.disks ?? "-"}</div>
            <div><strong>Local IP:</strong> {status.localIp ?? "-"}</div>
            <div><strong>RAID:</strong> {formatMB(status.raidUsage)} / {formatMB(status.raidSize)}</div>
            <div><strong>Disk:</strong> {formatMB(status.diskUsage)} / {formatMB(status.diskSize)}</div>
            <div><strong>CPU:</strong> {status.cpuUsagePrc != null ? `${round(status.cpuUsagePrc)}%` : "-"}</div>
            <div><strong>Mem:</strong> {formatMB(status.memUsage)} / {formatMB(status.memSize)}</div>
            {hasErrors && (
              <div className="sw2-errors">
                {status.errors!.map((e, i) => (
                  <div key={i}>• {e.Message || String(e.StatusErrorCode)}</div>
                ))}
              </div>
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
