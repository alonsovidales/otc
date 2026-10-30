// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "Sort by" and a list of orders, over the People and Collections pages
// (librarySort.ts): a native select, as Files' Sort by is - the keyboard,
// screen readers and a phone's own picker come with it.
import { useT } from "../i18n";
import "./SortBy.css";

export function SortBy<V extends string>({ value, options, onChange }: {
  value: V;
  options: readonly { value: V; label: string }[];
  onChange: (value: V) => void;
}) {
  const t = useT();
  return (
    <label className="sortby">
      <span className="sortby-label">{t("web.images.sort_by")}</span>
      <select value={value} onChange={(e) => onChange(e.target.value as V)}>
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
    </label>
  );
}
