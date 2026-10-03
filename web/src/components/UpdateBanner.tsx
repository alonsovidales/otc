// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #183: a critical update - one that breaks compatibility with the
// bridge or the apps if not installed, or a serious security fix - is
// shown above every page until it's installed. The device says so in its
// Status (update_alert), polled here every five minutes. The apps do the
// same; a major update only gets a notification.
import { useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, UpdateAlert } from "../proto/messages";
import "./UpdateBanner.css";

const cEveryMs = 5 * 60 * 1000;

export default function UpdateBanner({ onOpenSettings }: { onOpenSettings: () => void }) {
  const [alert, setAlert] = useState<UpdateAlert | null>(null);
  useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetStatus", reqGetStatus: {} };
        });
        if (alive && resp.payload?.$case === "respStatus") setAlert(resp.payload.respStatus.updateAlert ?? null);
      } catch { /* the next poll tries again */ }
    };
    void poll();
    const t = window.setInterval(() => void poll(), cEveryMs);
    return () => { alive = false; window.clearInterval(t); };
  }, []);
  if (alert?.level !== "critical") return null;
  return (
    <div className="ub-banner" role="alert">
      <span className="ub-icon" aria-hidden="true">⚠</span>
      <span className="ub-text"><strong>Critical update {alert.version} available.</strong> {alert.summary} Install it as soon as possible.</span>
      <button className="ub-btn" onClick={onOpenSettings}>Update</button>
    </div>
  );
}
