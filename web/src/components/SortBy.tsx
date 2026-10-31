// SPDX-License-Identifier: AGPL-3.0-or-later
//
// "Sort by" and a list of orders, over the People and Collections pages
// (librarySort.ts): a native select, as Files' Sort by is - the keyboard,
// screen readers and a phone's own picker come with it.
import type { SelectHTMLAttributes } from "react";
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
      <SelectField value={value} onChange={(e) => onChange(e.target.value as V)}>
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </SelectField>
    </label>
  );
}

// A native select with its own chevron instead of the browser's, which some
// browsers draw against the border: the Sort by lists here and in Files.
export function SelectField(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="select-field">
      <select {...props} />
      <svg className="select-field-chevron" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
        strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="m6 9 6 6 6-6" />
      </svg>
    </span>
  );
}
