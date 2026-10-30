// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Background work (BackgroundWorkPanel.tsx): what the device is
// working through by itself for this library (Status.background_tasks,
// release 152) - new uploads' analysis, thumbnails, places, On this day's
// measures, Smart search's descriptions, photo dates... - one row each, in
// the device's words, with how far it is. Pure, so
// web/scripts/smart-search-test.mjs tests it; the phones follow the same
// rules.

import type { BackgroundTask } from "../../proto/messages";

/** One row: the device's words, done of total (total 0: not known yet, an
 *  indeterminate bar) and the bar's percent (null when indeterminate). */
export type WorkRow = { key: string; text: string; done: number; total: number; percent: number | null };

const count = (n: bigint | number | undefined) => {
  const v = Number(n ?? 0);
  return Number.isFinite(v) && v > 0 ? Math.floor(v) : 0;
};

/** The rows, in the device's order; keyed by kind (made unique), never
 *  shown. */
export function workRows(tasks: readonly BackgroundTask[]): WorkRow[] {
  const seen = new Set<string>();
  return tasks.map((task, i) => {
    let key = task.kind || `#${i}`;
    if (seen.has(key)) key = `${key}#${i}`;
    seen.add(key);
    const total = count(task.total);
    const done = total > 0 ? Math.min(count(task.done), total) : count(task.done);
    const percent = total > 0 ? Math.floor((done * 100) / total) : null;
    return { key, text: task.text.trim(), done, total, percent };
  });
}
