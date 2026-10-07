// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import type { TabKey } from "./nav";
import { AlertsIcon, FilesIcon, FriendsIcon, GroupsIcon, ImagesIcon, SettingsIcon, SocialIcon, StorageIcon } from "./NavIcons";
import { formatMB, pct, raidStateLabel, round, useDeviceStatus } from "./useDeviceStatus";
import { RaidState } from "../proto/messages";
import "./Sidebar.css";

type Props = {
  tab: TabKey;
  notificationCount: number;
  // Beside the page: the whole menu ("full"), its icons only ("rail"), or
  // nothing ("none", a phone).
  layout: "full" | "rail" | "none";
  // The whole menu over the page, with a backdrop: a narrow window's menu
  // button. Closes after a pick.
  drawerOpen: boolean;
  onSelect: (tab: TabKey) => void;
  onCloseDrawer: () => void;
  // The rail's storage meter: show the whole menu, where the details are.
  onExpand: () => void;
};

type Item = { key: TabKey; label: string; icon: React.ReactNode };

// Grouped like a photo library: what is stored, then the people, then the
// device. Names as in the iOS and Android apps.
const LIBRARY: Item[] = [
  { key: "PhotoGallery", label: "Images", icon: <ImagesIcon /> },
  { key: "Groups", label: "Groups", icon: <GroupsIcon /> },
  { key: "AdminPannel", label: "Files", icon: <FilesIcon /> },
];
const PEOPLE: Item[] = [
  { key: "Social", label: "Social", icon: <SocialIcon /> },
  { key: "Friends", label: "Friends", icon: <FriendsIcon /> },
];
const DEVICE: Item[] = [
  { key: "Notifications", label: "Alerts", icon: <AlertsIcon /> },
  { key: "Settings", label: "Settings", icon: <SettingsIcon /> },
];

export default function Sidebar({ tab, notificationCount, layout, drawerOpen, onSelect, onCloseDrawer }: Props) {
  // STUB wiring until the rail lands.
  const overlay = drawerOpen;
  const open = drawerOpen || layout !== "none";
  const onClose = onCloseDrawer;
  const isActive = (key: Item["key"]) => key === tab;

  const pick = (key: Item["key"]) => {
    onSelect(key);
    if (overlay) onClose();
  };

  const row = (it: Item) => (
    <button
      key={it.key}
      className={`sb-item${isActive(it.key) ? " active" : ""}`}
      onClick={() => pick(it.key)}
      aria-current={isActive(it.key) ? "page" : undefined}
    >
      <span className="sb-icon">{it.icon}</span>
      <span className="sb-label">{it.label}</span>
      {it.key === "Notifications" && notificationCount > 0 && (
        <span className="sb-badge" aria-label={`${notificationCount} unread`}>{notificationCount > 99 ? "99+" : notificationCount}</span>
      )}
    </button>
  );

  return (
    <>
      {overlay && open && <div className="sb-backdrop" onClick={onClose} />}
      <nav className={`sidebar${open ? " open" : ""}${overlay ? " overlay" : ""}`} aria-label="Sections" aria-hidden={!open}>
        <div className="sb-group">{LIBRARY.map(row)}</div>
        <div className="sb-heading">People</div>
        <div className="sb-group">{PEOPLE.map(row)}</div>
        <div className="sb-heading">Device</div>
        <div className="sb-group">{DEVICE.map(row)}</div>
        <div className="sb-fill" />
        <SidebarStorage />
        <div className="sb-legal">
          <a href="https://off-the.cloud/privacy" target="_blank" rel="noreferrer">Privacy</a>
          <span aria-hidden="true">·</span>
          <a href="https://off-the.cloud/terms" target="_blank" rel="noreferrer">Terms</a>
        </div>
      </nav>
    </>
  );
}

// Storage at the foot of the menu: how full, and the mirror's health; the
// rest of the device's status (disks, CPU, memory, errors) on a click.
function SidebarStorage() {
  const { status, err } = useDeviceStatus();
  const [details, setDetails] = useState(false);
  const usedPct = pct(status?.raidUsage, status?.raidSize);
  const errors = status?.errors ?? [];
  const degraded = status?.raidState === RaidState.RaidDegraded;
  return (
    <div className="sb-storage">
      <button className={`sb-item sb-storage-head${details ? " active" : ""}`} onClick={() => setDetails((v) => !v)} aria-expanded={details}>
        <span className="sb-icon"><StorageIcon /></span>
        <span className="sb-label">Storage</span>
        {(errors.length > 0 || degraded) && <span className="sb-dot" aria-label="Needs attention" />}
      </button>
      <div className="sb-meter" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={usedPct}>
        <div className={`sb-meter-fill${usedPct >= 90 ? " crit" : usedPct >= 70 ? " warn" : ""}`} style={{ width: `${usedPct}%` }} />
      </div>
      <div className="sb-storage-text">
        {status ? `${formatMB(status.raidUsage)} of ${formatMB(status.raidSize)} used` : err ? "Storage unavailable right now" : "Reading…"}
      </div>
      {status && <div className={`sb-storage-text${degraded ? " bad" : ""}`}>{raidStateLabel(status)}</div>}
      {details && status && (
        <div className="sb-details">
          <div><span>Disks</span><span>{status.disks || "—"}</span></div>
          <div><span>Free</span><span>{formatMB(Math.max(0, (status.raidSize || 0) - (status.raidUsage || 0)))}</span></div>
          <div><span>System card</span><span>{formatMB(status.diskUsage)} of {formatMB(status.diskSize)}</span></div>
          <div><span>CPU</span><span>{status.cpuUsagePrc != null ? `${round(status.cpuUsagePrc)}%` : "—"}</span></div>
          <div><span>Memory</span><span>{formatMB(status.memUsage)} of {formatMB(status.memSize)}</span></div>
          {errors.map((e, i) => <div key={i} className="sb-error">{e.Message || String(e.StatusErrorCode)}</div>)}
        </div>
      )}
    </div>
  );
}
