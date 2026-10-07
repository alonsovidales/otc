// SPDX-License-Identifier: AGPL-3.0-or-later

import { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import type { ComponentType } from "react";
import type { TabKey } from "./nav";
import { AlertsIcon, ChevronIcon, CollectionsIcon, FilesIcon, FriendsIcon, ImagesIcon, PeopleIcon, SettingsIcon, SocialIcon, StorageIcon } from "./NavIcons";
import { formatMB, pct, raidStateLabel, round, useDeviceStatus } from "./useDeviceStatus";
import { usePhotoFilter } from "./photoFilter";
import { RaidState } from "../proto/messages";
import type { Status } from "../proto/messages";
import "./Sidebar.css";

type Props = {
  tab: TabKey;
  notificationCount: number;
  // Beside the page: the whole menu ("full"), a rail of icons with short
  // labels ("rail"), or nothing ("none", a phone).
  layout: "full" | "rail" | "none";
  // The whole menu over the page, with a backdrop: the menu button on a
  // window too narrow for it beside the page. Closes after a pick.
  drawerOpen: boolean;
  onSelect: (tab: TabKey) => void;
  onCloseDrawer: () => void;
  // The rail's storage gauge: show the whole menu, where the details are.
  onExpand: () => void;
};

type Item = { key: TabKey; label: string; Icon: ComponentType<{ size?: number }> };
type Section = { heading?: string; items: Item[] };

// Grouped like a photo library: what is stored, what is shared, then the
// device.
const SECTIONS: Section[] = [
  {
    items: [
      { key: "PhotoGallery", label: "Images", Icon: ImagesIcon },
      { key: "People", label: "People", Icon: PeopleIcon },
      { key: "Collections", label: "Collections", Icon: CollectionsIcon },
      { key: "AdminPannel", label: "Files", Icon: FilesIcon },
    ],
  },
  {
    heading: "Sharing",
    items: [
      { key: "Social", label: "Social", Icon: SocialIcon },
      { key: "Friends", label: "Friends", Icon: FriendsIcon },
    ],
  },
  {
    heading: "Device",
    items: [
      { key: "Notifications", label: "Alerts", Icon: AlertsIcon },
      { key: "Settings", label: "Settings", Icon: SettingsIcon },
    ],
  },
];

// The storage gauge's tooltip (the rail's items show their labels) only
// where there is a pointer to hover with: on a touch screen a tap would
// leave it standing over the rail.
const hoverQuery = window.matchMedia("(hover: hover)");
const onHoverChange = (cb: () => void) => {
  hoverQuery.addEventListener("change", cb);
  return () => hoverQuery.removeEventListener("change", cb);
};
const canHover = () => hoverQuery.matches;

export default function Sidebar({ tab, notificationCount, layout, drawerOpen, onSelect, onCloseDrawer, onExpand }: Props) {
  // A collection open in Images is still the Collections section.
  const { group } = usePhotoFilter();
  const current: TabKey = tab === "PhotoGallery" && group ? "Collections" : tab;
  // One poll of the device's status for the menu beside the page and the
  // drawer both.
  const { status, err } = useDeviceStatus();
  const storage = summarize(status, err);
  const [details, setDetails] = useState(false);
  const tips = useSyncExternalStore(onHoverChange, canHover);

  const dockRef = useRef<HTMLElement>(null);
  const drawerRef = useRef<HTMLElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  // The rail's gauge asked for the storage details: the whole menu that
  // opens next shows them.
  const revealStorage = useRef(false);
  const closeRef = useRef(onCloseDrawer);
  useEffect(() => { closeRef.current = onCloseDrawer; });

  // Keyboard focus inside the drawer goes back to the menu button that
  // opened it, instead of being dropped on the page with the drawer.
  const closeDrawer = useCallback(() => {
    if (drawerRef.current?.contains(document.activeElement)) {
      document.querySelector<HTMLElement>(".tb-menu")?.focus({ preventScroll: true });
    }
    closeRef.current();
  }, []);

  const pickInDrawer = (key: TabKey) => {
    onSelect(key);
    closeDrawer();
  };

  const showStorage = () => {
    revealStorage.current = true;
    setDetails(true);
    onExpand();
  };

  // While the drawer is open, Escape, or a tap or keyboard focus anywhere
  // else (the search in the top bar), closes it. The menu button toggles it
  // by itself; the backdrop closes it on its own click, so that tap is spent
  // there and never lands on a photo under it.
  useEffect(() => {
    if (!drawerOpen) return;
    const elsewhere = (t: EventTarget | null) =>
      t instanceof Element && !drawerRef.current?.contains(t) && !t.closest(".tb-menu, .sb-backdrop");
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      e.preventDefault();
      closeDrawer();
    };
    const onAway = (e: Event) => { if (elsewhere(e.target)) closeRef.current(); };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onAway, true);
    document.addEventListener("focusin", onAway);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onAway, true);
      document.removeEventListener("focusin", onAway);
    };
  }, [drawerOpen, closeDrawer]);

  // The page under the backdrop doesn't scroll with the wheel either (a
  // touch is held off by its touch-action). Not a React handler: those are
  // passive and can't prevent it.
  useEffect(() => {
    const el = backdropRef.current;
    if (!el) return;
    const stop = (e: WheelEvent) => e.preventDefault();
    el.addEventListener("wheel", stop, { passive: false });
    return () => el.removeEventListener("wheel", stop);
  }, []);

  // An opened drawer takes the focus, on the open section, so the keyboard
  // carries on inside it. After the rail's gauge, the whole menu shows the
  // storage at its foot, scrolled into view.
  const wasOpen = useRef(false);
  useLayoutEffect(() => {
    const opened = drawerOpen && !wasOpen.current;
    wasOpen.current = drawerOpen;
    const nav = drawerOpen ? drawerRef.current : layout === "full" ? dockRef.current : null;
    if (!nav) return;
    if (revealStorage.current) {
      revealStorage.current = false;
      nav.scrollTop = nav.scrollHeight;
      nav.querySelector<HTMLElement>(".sb-storage-head")?.focus({ preventScroll: true });
    } else if (opened) {
      const target = nav.querySelector<HTMLElement>('[aria-current="page"]') ?? nav.querySelector<HTMLElement>("button");
      target?.focus({ preventScroll: true });
    }
  }, [drawerOpen, layout]);

  const full = (onPick: (key: TabKey) => void) => (
    <FullMenu
      current={current}
      unread={notificationCount}
      storage={storage}
      details={details}
      onToggleDetails={() => setDetails((v) => !v)}
      onPick={onPick}
    />
  );

  return (
    <>
      {layout !== "none" && (
        <nav ref={dockRef} className={`sidebar sb-dock sb-${layout}`} aria-label="Sections" inert={drawerOpen}>
          {layout === "rail" ? (
            <div className="sb-inner sb-inner-rail">
              <RailMenu
                current={current}
                unread={notificationCount}
                storage={storage}
                tips={tips}
                onPick={onSelect}
                onStorage={showStorage}
              />
            </div>
          ) : (
            <div className="sb-inner sb-inner-full">{full(onSelect)}</div>
          )}
        </nav>
      )}
      <div
        ref={backdropRef}
        className={`sb-backdrop${drawerOpen ? " open" : ""}`}
        onClick={closeDrawer}
        aria-hidden="true"
      />
      <nav
        ref={drawerRef}
        className={`sidebar sb-drawer${drawerOpen ? " open" : ""}`}
        aria-label="Sections"
        inert={!drawerOpen}
      >
        <div className="sb-inner sb-inner-full">{full(pickInDrawer)}</div>
      </nav>
    </>
  );
}

type Storage = {
  status: Status | null;
  usedPct: number;
  // Fuller than comfortable: the meter turns ember, then red.
  level: "" | "warn" | "crit";
  degraded: boolean;
  attention: boolean;
  usedText: string;
  mirrorText: string;
};

function summarize(status: Status | null, err: string | null): Storage {
  const usedPct = pct(status?.raidUsage, status?.raidSize);
  const degraded = status?.raidState === RaidState.RaidDegraded;
  return {
    status,
    usedPct,
    level: usedPct >= 90 ? "crit" : usedPct >= 70 ? "warn" : "",
    degraded,
    attention: degraded || (status?.errors?.length ?? 0) > 0,
    usedText: status
      ? `${formatMB(status.raidUsage)} of ${formatMB(status.raidSize)} used`
      : err ? "Storage unavailable right now" : "Reading…",
    mirrorText: status ? raidStateLabel(status) : "",
  };
}

type MenuProps = {
  current: TabKey;
  unread: number;
  storage: Storage;
  onPick: (key: TabKey) => void;
};

// The whole menu: pill rows with an icon and a label, under headings.
function FullMenu({ current, unread, storage, details, onToggleDetails, onPick }: MenuProps & {
  details: boolean;
  onToggleDetails: () => void;
}) {
  return (
    <>
      {SECTIONS.map((s) => (
        <div key={s.heading ?? ""} className="sb-section" role={s.heading ? "group" : undefined} aria-label={s.heading}>
          {s.heading && <div className="sb-heading" aria-hidden="true">{s.heading}</div>}
          {s.items.map((it) => (
            <NavItem key={it.key} item={it} active={it.key === current} unread={unread} onPick={onPick} />
          ))}
        </div>
      ))}
      <div className="sb-fill" />
      <StoragePanel storage={storage} details={details} onToggle={onToggleDetails} />
      <div className="sb-legal">
        <a href="https://off-the.cloud/privacy" target="_blank" rel="noreferrer">Privacy</a>
        <span aria-hidden="true">·</span>
        <a href="https://off-the.cloud/terms" target="_blank" rel="noreferrer">Terms</a>
      </div>
    </>
  );
}

// The rail: the same items as columns (icon in a pill, label under it), a
// hairline between the groups instead of headings, and storage as a gauge.
function RailMenu({ current, unread, storage, tips, onPick, onStorage }: MenuProps & {
  tips: boolean;
  onStorage: () => void;
}) {
  return (
    <>
      {SECTIONS.map((s, i) => (
        <Fragment key={s.heading ?? ""}>
          {i > 0 && <div className="sb-sep" aria-hidden="true" />}
          <div className="sb-section" role={s.heading ? "group" : undefined} aria-label={s.heading}>
            {s.items.map((it) => (
              <NavItem key={it.key} item={it} active={it.key === current} unread={unread} rail onPick={onPick} />
            ))}
          </div>
        </Fragment>
      ))}
      <div className="sb-fill" />
      <StorageGauge storage={storage} tips={tips} onOpen={onStorage} />
    </>
  );
}

function NavItem({ item: { key, label, Icon }, active, unread, rail = false, onPick }: {
  item: Item;
  active: boolean;
  unread: number;
  rail?: boolean;
  onPick: (key: TabKey) => void;
}) {
  const count = key === "Notifications" ? unread : 0;
  const badge = count > 0 && <span className="sb-badge" aria-hidden="true">{count > 99 ? "99+" : count}</span>;
  return (
    <button
      type="button"
      className={`sb-item${active ? " active" : ""}`}
      onClick={() => onPick(key)}
      aria-current={active ? "page" : undefined}
      aria-label={count > 0 ? `${label}, ${count} unread` : label}
    >
      <span className="sb-icon">
        <Icon size={rail ? 24 : 22} />
        {rail && badge}
      </span>
      <span className="sb-label">{label}</span>
      {!rail && badge}
    </button>
  );
}

// Storage at the foot of the whole menu: how full, and the mirror's
// health; the rest of the device's status (disks, CPU, memory, errors) on
// a click.
function StoragePanel({ storage: s, details, onToggle }: { storage: Storage; details: boolean; onToggle: () => void }) {
  const { status } = s;
  return (
    <div className="sb-storage">
      <button type="button" className="sb-item sb-storage-head" onClick={onToggle} aria-expanded={details}>
        <span className="sb-icon"><StorageIcon /></span>
        <span className="sb-label">Storage</span>
        {s.attention && <span className="sb-dot" role="img" aria-label="Needs attention" />}
        <span className="sb-chevron"><ChevronIcon size={18} /></span>
      </button>
      <div className="sb-meter" role="progressbar" aria-label="Storage used" aria-valuemin={0} aria-valuemax={100} aria-valuenow={s.usedPct}>
        <div className={`sb-meter-fill${s.level ? ` ${s.level}` : ""}`} style={{ width: `${s.usedPct}%` }} />
      </div>
      <div className="sb-storage-text">{s.usedText}</div>
      {/* Its line is kept while the status loads, so the block doesn't grow
          when it comes. */}
      <div className={`sb-storage-text${s.degraded ? " bad" : ""}`}>{s.mirrorText || " "}</div>
      {details && status && (
        <div className="sb-details">
          <div><span>Disks</span><span>{status.disks || "—"}</span></div>
          <div><span>Free</span><span>{formatMB(Math.max(0, (status.raidSize || 0) - (status.raidUsage || 0)))}</span></div>
          <div><span>System card</span><span>{formatMB(status.diskUsage)} of {formatMB(status.diskSize)}</span></div>
          <div><span>CPU</span><span>{status.cpuUsagePrc != null ? `${round(status.cpuUsagePrc)}%` : "—"}</span></div>
          <div><span>Memory</span><span>{formatMB(status.memUsage)} of {formatMB(status.memSize)}</span></div>
          {(status.errors ?? []).map((e, i) => <div key={i} className="sb-error">{e.Message || String(e.StatusErrorCode)}</div>)}
        </div>
      )}
    </div>
  );
}

// The rail's storage: a ring filling up as the disks do, the percentage
// under it. A click opens the whole menu at the details.
function StorageGauge({ storage: s, tips, onOpen }: { storage: Storage; tips: boolean; onOpen: () => void }) {
  const shown = s.status ? `${Math.round(s.usedPct)}%` : "–";
  const problems = (s.status?.errors?.length ?? 0) > 0 ? "Needs attention" : "";
  const tip = ["Storage", [s.usedText, s.mirrorText, problems].filter(Boolean).join(" · ")].join(": ");
  return (
    <button
      type="button"
      className={`sb-gauge${s.level ? ` ${s.level}` : ""}`}
      onClick={onOpen}
      aria-label={`Storage${s.status ? `, ${shown} used` : ""}${s.attention ? ", needs attention" : ""}. Show details`}
      data-tip={tips ? tip : undefined}
    >
      <span className="sb-ring">
        <svg viewBox="0 0 36 36" width="32" height="32" aria-hidden="true">
          <circle className="sb-ring-track" cx="18" cy="18" r="15.5" />
          {s.usedPct > 0 && (
            <circle
              className="sb-ring-value"
              cx="18"
              cy="18"
              r="15.5"
              pathLength={100}
              transform="rotate(-90 18 18)"
              style={{ strokeDasharray: `${s.usedPct} 100` }}
            />
          )}
        </svg>
        <span className="sb-ring-icon"><StorageIcon size={14} /></span>
        {s.attention && <span className="sb-dot" />}
      </span>
      <span className="sb-label">{shown}</span>
    </button>
  );
}
