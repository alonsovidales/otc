// SPDX-License-Identifier: AGPL-3.0-or-later

import React, { useCallback, useMemo } from "react";
import "./TopTabs.css";

// "Profile" still routes an anonymous visitor to the read-only ProfileCard
// (App.tsx's default landing tab when signed out) - it just isn't one of
// the authenticated owner's own tabs below any more (issue #84: the
// editable form moved into Settings). "Friends" is deliberately not in
// ALL_TABS either - it's reached from a button in the Social header
// instead (see App.tsx), not a top-level tab of its own.
export type TabKey = "Profile" | "Social" | "SignIn" | "AdminPannel" | "PhotoGallery" | "Settings" | "Notifications" | "Friends";

export type TopTabsProps = {
  value: TabKey;                    // currently selected tab
  onChange: (next: TabKey) => void; // notify parent
  className?: string;
  // Issue #78 follow-up: notifications became a section/tab of its own
  // (was a standalone header bell+dropdown) - the unread count now shows
  // as a badge on this tab instead, same as the bell's badge used to.
  notificationCount?: number;
};

// Issue #84: "Profile" removed from here - its editable form lives at the
// top of Settings now, and Friendships (what this tab used to show once
// signed in) is reached from the Social header's own button instead.
const ALL_TABS: { key: TabKey; label: string }[] = [
  { key: "Social",   label: "Social" },
  { key: "AdminPannel",    label: "Files" },
  { key: "PhotoGallery",   label: "Images" },
  { key: "Settings", label: "Settings" },
  // Rightmost, per issue #78 follow-up ("in the web app at the right").
  { key: "Notifications", label: "Notifications" },
];

export default function TopTabs({ value, onChange, className, notificationCount = 0 }: TopTabsProps) {
  const idx = useMemo(() => ALL_TABS.findIndex(t => t.key === value), [value]);

  const onKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight" && e.key !== "Social" && e.key !== "End") return;
    e.preventDefault();
    const max = ALL_TABS.length - 1;
    let nextIdx = idx;
    if (e.key === "ArrowLeft")  nextIdx = idx <= 0 ? max : idx - 1;
    if (e.key === "ArrowRight") nextIdx = idx >= max ? 0 : idx + 1;
    if (e.key === "Social")       nextIdx = 0;
    if (e.key === "End")        nextIdx = max;
    onChange(ALL_TABS[nextIdx].key);
  }, [idx, onChange]);

  return (
    <div
      className={`top-tabs ${className ?? ""}`}
      role="tablist"
      aria-label="Primary navigation"
      onKeyDown={onKeyDown}
    >
      {ALL_TABS.map((t) => {
        const selected = t.key === value;
        return (
          <button
            key={t.key}
            role="tab"
            aria-selected={selected}
            tabIndex={selected ? 0 : -1}
            className={`top-tab ${selected ? "is-active" : ""}`}
            onClick={() => onChange(t.key)}
            aria-label={t.key === "Notifications" ? "Notifications" : undefined}
          >
            {/* Issue #78 follow-up: "keep the bell in the web app" - a
                bell glyph instead of a text label for this one tab, same
                shape as the standalone header bell it replaced. */}
            {t.key === "Notifications" ? (
              <svg width="17" height="17" viewBox="0 0 24 24" aria-hidden="true">
                <path
                  d="M12 3a5 5 0 0 0-5 5v2.7c0 1.15-.45 2.25-1.26 3.06L4.5 15h15l-1.24-1.24A4.33 4.33 0 0 1 17 10.7V8a5 5 0 0 0-5-5Z"
                  stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"
                />
                <path d="M9.5 18a2.5 2.5 0 0 0 5 0" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" />
              </svg>
            ) : t.label}
            {t.key === "Notifications" && notificationCount > 0 && (
              <span className="top-tab-badge">{notificationCount > 99 ? "99+" : notificationCount}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

