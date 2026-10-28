// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The widths of Files' list columns (the rules; useFilesColumns.ts puts
// them on the page). The owner's report, 2026-10-28: on a Fold, folder
// names showed as "4d…" while Size, Created and Modified had wide columns
// of "—", and nothing could be resized.
//
// - Name takes what the other columns leave, and never less than room for
//   10 characters of the name with its row's icons (`nameMin`).
// - Size, Created and Modified have a width each: the one the person gave
//   it in this browser (localStorage, cColumnsStorageKey), else a default
//   that fits a long size or date in the page's language.
// - As the list narrows, Created goes, then Modified, once Name would keep
//   less than `nameRoom` with them at their defaults; Size always stays.
//   What shows is fitted to the room: columns wider than it allows give
//   way, the last first, down to their minimums. The list never scrolls
//   sideways (a scrolling box would unstick the column headings).
// - The handle at a heading's right edge resizes that column: wider takes
//   the room from the columns after it, nearest first, down to their
//   minimums; narrower gives it to the next column. A double-click puts the
//   column back (Name: every column), and the arrow keys move it by cKeyStep.

export type ColumnKey = "name" | "size" | "created" | "modified";
export type FixedColumn = Exclude<ColumnKey, "name">;

export const cColumns: readonly ColumnKey[] = ["name", "size", "created", "modified"];
// Hidden in this order as the list narrows.
const cHideOrder: readonly FixedColumn[] = ["created", "modified"];

export const cColumnsStorageKey = "otc_files_columns";
// What an arrow key moves a column's edge by.
export const cKeyStep = 16;
// Never kept wider than this (a stored value is checked against it).
const cMaxStored = 4000;

/** The widths given in this browser, in CSS pixels; a column left out has its default. */
export type ColumnWidths = Partial<Record<FixedColumn, number>>;

/** What the columns need, measured on the page in its language (columnMetrics). */
export type ColumnMetrics = {
  // The checkbox's column.
  check: number;
  // 10 characters of a name ("0000000000…"): a long name gives way to
  // its icons down to this (FilesExplorer.css .fb-name-long).
  nameText: number;
  // Name's minimum: those 10 characters, the row's icons and the cell's padding.
  nameMin: number;
  // What Name keeps before Created or Modified shows.
  nameRoom: number;
  min: Record<FixedColumn, number>;
  def: Record<FixedColumn, number>;
};

/** The columns shown, Name first, and the width of each (Name's is what the others leave). */
export type ColumnLayout = {
  shown: ColumnKey[];
  width: Record<ColumnKey, number>;
};

const isFixed = (k: ColumnKey): k is FixedColumn => k !== "name";
const fixedOf = (shown: readonly ColumnKey[]) => shown.filter(isFixed);
const sum = (ns: number[]) => ns.reduce((a, b) => a + b, 0);

// The checkbox's column, as FilesExplorer.css's fallback has it.
export const cCheckWidth = 26;
// After the name, at most: a gap, the lock and another gap (8 + 24 + 8),
// then the switch for Images (24) or a file's versions pill at its
// narrowest (35, its icon: FilesExplorer.css .fb-versions).
export const cNameTrail = 8 + 24 + 8 + 35;

/** Text widths measured on the page (useFilesColumns.ts), in CSS pixels. */
export type ColumnSamples = {
  // Left plus right padding of each column's cells.
  pad: Record<ColumnKey, number>;
  // "0000000000…" in a name's font: 10 characters and the ellipsis.
  name: number;
  // A long size ("1023.0 MB") and a short one ("1.5 KB").
  size: number;
  sizeShort: number;
  // A long date and time, and the date alone.
  date: number;
  dateShort: number;
};

/** The minimums and defaults from what the page measured. */
export function columnMetrics(s: ColumnSamples): ColumnMetrics {
  const up = (n: number) => Math.ceil(n);
  const nameMin = up(s.pad.name + s.name + cNameTrail + 2);
  return {
    check: cCheckWidth,
    nameText: up(s.name),
    nameMin,
    nameRoom: nameMin + up(s.name),
    min: {
      size: up(s.pad.size + s.sizeShort + 2),
      created: up(s.pad.created + s.dateShort + 4),
      modified: up(s.pad.modified + s.dateShort + 4),
    },
    def: {
      size: up(s.pad.size + s.size + 4),
      created: up(s.pad.created + s.date + 4),
      modified: up(s.pad.modified + s.date + 4),
    },
  };
}

/** The columns a list `room` wide shows: Created goes first, then Modified. */
export function shownColumns(room: number, m: ColumnMetrics): ColumnKey[] {
  let shown = [...cColumns];
  for (const c of cHideOrder) {
    if (m.check + m.nameRoom + sum(fixedOf(shown).map((k) => m.def[k])) <= room) break;
    shown = shown.filter((k) => k !== c);
  }
  return shown;
}

/** The layout for a list `room` wide with the widths given in this browser. */
export function fitColumns(room: number, given: ColumnWidths, m: ColumnMetrics): ColumnLayout {
  const shown = shownColumns(room, m);
  const width: Record<ColumnKey, number> = { name: 0, size: 0, created: 0, modified: 0 };
  const fixed = fixedOf(shown);
  for (const k of fixed) width[k] = Math.max(m.min[k], Math.round(given[k] ?? m.def[k]));
  // Name keeps its minimum: the widest give way, from the last column.
  let over = m.check + m.nameMin + sum(fixed.map((k) => width[k])) - room;
  for (let i = fixed.length - 1; i >= 0 && over > 0; i--) {
    const k = fixed[i];
    const give = Math.min(over, width[k] - m.min[k]);
    width[k] -= give;
    over -= give;
  }
  width.name = Math.max(0, room - m.check - sum(fixed.map((k) => width[k])));
  return { shown, width };
}

/** The grid's columns (the header and every row share it): Name takes the rest. */
export function columnsTemplate(l: ColumnLayout, m: ColumnMetrics): string {
  return [`${m.check}px`, "minmax(0, 1fr)", ...fixedOf(l.shown).map((k) => `${l.width[k]}px`)].join(" ");
}

const minOf = (k: ColumnKey, m: ColumnMetrics) => (k === "name" ? m.nameMin : m.min[k]);

/**
 * `col`'s right edge moved by dx pixels from where it was in `l`: wider
 * takes the room from the columns after it, nearest first, down to their
 * minimums; narrower, down to its own minimum, gives it to the next one.
 * The last column shown has no edge to move.
 */
export function resizeColumn(l: ColumnLayout, m: ColumnMetrics, col: ColumnKey, dx: number): ColumnLayout {
  const i = l.shown.indexOf(col);
  const step = Math.round(dx);
  if (i < 0 || i >= l.shown.length - 1 || step === 0) return l;
  const w = l.shown.map((k) => l.width[k]);
  const min = l.shown.map((k) => minOf(k, m));
  if (step > 0) {
    let need = step;
    for (let j = i + 1; j < w.length && need > 0; j++) {
      const take = Math.min(need, Math.max(0, w[j] - min[j]));
      w[j] -= take;
      w[i] += take;
      need -= take;
    }
  } else {
    const take = Math.min(-step, Math.max(0, w[i] - min[i]));
    w[i] -= take;
    w[i + 1] += take;
  }
  const width = { ...l.width };
  l.shown.forEach((k, j) => { width[k] = w[j]; });
  return { shown: l.shown, width };
}

/** The widths to keep after a resize from `before` to `after`: those it changed. */
export function widthsAfter(given: ColumnWidths, before: ColumnLayout, after: ColumnLayout): ColumnWidths {
  const out = { ...given };
  for (const k of fixedOf(after.shown)) if (after.width[k] !== before.width[k]) out[k] = after.width[k];
  return out;
}

/** A column put back as it was: Name puts every column back, as Name's own width is what they leave. */
export function resetColumn(given: ColumnWidths, col: ColumnKey): ColumnWidths {
  if (col === "name") return {};
  const out = { ...given };
  delete out[col];
  return out;
}

/** The handle's values for assistive technology: `col`'s width, its minimum and its most. */
export function edgeValues(l: ColumnLayout, m: ColumnMetrics, col: ColumnKey): { now: number; min: number; max: number } {
  const i = l.shown.indexOf(col);
  const now = Math.round(l.width[col]);
  let max = now;
  for (let j = i + 1; i >= 0 && j < l.shown.length; j++) max += Math.max(0, l.width[l.shown[j]] - minOf(l.shown[j], m));
  return { now, min: Math.min(now, minOf(col, m)), max: Math.round(max) };
}

/** Stored widths as read back: anything else than a sane number per column is left out. */
export function parseWidths(raw: string | null): ColumnWidths {
  if (!raw) return {};
  let v: unknown;
  try { v = JSON.parse(raw); } catch { return {}; }
  if (!v || typeof v !== "object" || Array.isArray(v)) return {};
  const out: ColumnWidths = {};
  for (const k of fixedOf(cColumns)) {
    const n = (v as Record<string, unknown>)[k];
    if (typeof n === "number" && Number.isFinite(n) && n > 0 && n <= cMaxStored) out[k] = Math.round(n);
  }
  return out;
}

export function loadColumnWidths(): ColumnWidths {
  try { return parseWidths(localStorage.getItem(cColumnsStorageKey)); } catch { return {}; }
}

export function saveColumnWidths(w: ColumnWidths) {
  try {
    if (Object.keys(w).length === 0) localStorage.removeItem(cColumnsStorageKey);
    else localStorage.setItem(cColumnsStorageKey, JSON.stringify(w));
  } catch { /* kept for this visit only */ }
}
