// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Background work: what the device works through by itself
// (backgroundWork.ts has the rules), shown only while there is something,
// right after Storage (the phones put it after their Status section). No
// timer of its own, as on the phones: it reads the Status that Settings'
// own 5 s poll (MirrorDiskPanel's useDeviceStatus) and the menu's storage
// poll already bring (useDeviceStatus' latest).

import { useId, useMemo } from "react";
import { useT } from "../../i18n";
import { useLatestStatus } from "../useDeviceStatus";
import { workRows, type WorkRow } from "./backgroundWork";
import "./BackgroundWorkPanel.css";

export default function BackgroundWorkPanel() {
  const t = useT();
  const latest = useLatestStatus();
  const rows = useMemo(() => workRows(latest?.status.backgroundTasks ?? []), [latest]);
  if (rows.length === 0) return null;
  return (
    <section className="sf-section bw-panel" aria-labelledby="bw-title">
      <h3 id="bw-title">{t("web.settings.background.title")}</h3>
      <ul className="bw-list">
        {rows.map((row) => <WorkRowView key={row.key} row={row} />)}
      </ul>
      <p className="sf-hint bw-footer">{t("web.settings.background.footer")}</p>
    </section>
  );
}

function WorkRowView({ row }: { row: WorkRow }) {
  const t = useT();
  const id = useId();
  const counted = row.total > 0 ? t("web.settings.background.count", { done: row.done, total: row.total }) : "";
  return (
    <li className="bw-row">
      <div className="bw-line">
        <span id={id} className="bw-text">{row.text}</span>
        {counted && <span className="bw-count">{counted}</span>}
      </div>
      {/* Without a total the bar is indeterminate: no value at all. */}
      <div
        className="sf-progress-track bw-track"
        role="progressbar"
        aria-labelledby={id}
        {...(row.percent !== null
          ? { "aria-valuemin": 0, "aria-valuemax": row.total, "aria-valuenow": row.done, "aria-valuetext": counted }
          : {})}
      >
        {row.percent !== null
          ? <div className="sf-progress-fill" style={{ width: `${row.percent}%` }} />
          : <div className="sf-progress-fill bw-indeterminate" />}
      </div>
    </li>
  );
}
