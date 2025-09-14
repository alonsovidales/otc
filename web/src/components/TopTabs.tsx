import React, { useCallback, useMemo } from "react";
import "./TopTabs.css";

export type TabKey = "Social" | "SignIn" | "AdminPannel" | "PhotoGallery" | "Settings";

export type TopTabsProps = {
  value: TabKey;                    // currently selected tab
  onChange: (next: TabKey) => void; // notify parent
  className?: string;
};

const ALL_TABS: { key: TabKey; label: string }[] = [
  { key: "Social",   label: "Social" },
  { key: "AdminPannel",    label: "Files Manager" },
  { key: "PhotoGallery",   label: "Images Search" },
  { key: "Settings", label: "Settings" },
];

export default function TopTabs({ value, onChange, className }: TopTabsProps) {
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
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

