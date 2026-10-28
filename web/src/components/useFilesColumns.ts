// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Files' list columns on the page (the rules are filesColumns.ts): the
// grid's columns are one CSS variable on the table (--fb-cols), which the
// header and every row use, and the columns hidden at this width are its
// data-hide. Both are written here, never through React state: a drag
// doesn't draw the rows again (a folder can hold 20,000), and the browser
// skips the blocks of rows off screen.
//
// The handles at the headings' right edges (role "separator") take a mouse,
// a finger or a pen (pointer capture: the drag goes on off the handle), the
// arrow keys, and a double-click or double tap to put the column back.
// The press that ends a drag isn't a click on a heading (no new order).
import { useCallback, useLayoutEffect, useRef } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import {
  cKeyStep, columnMetrics, columnsTemplate, edgeValues, fitColumns, loadColumnWidths, resetColumn, resizeColumn,
  saveColumnWidths, widthsAfter,
  type ColumnKey, type ColumnLayout, type ColumnMetrics, type ColumnWidths,
} from "./filesColumns";

/** The texts the defaults are measured from, in the page's language. */
export type ColumnTexts = { size: string; sizeShort: string; date: string; dateShort: string };

// A press on a handle: where it began and the layout then; `cancelled` by
// Escape, it waits for the release.
type Drag = { id: number; col: ColumnKey; x: number; start: ColumnLayout; moved: boolean; cancelled: boolean };

// Moves under this many pixels are a press, not a drag.
const cMoveSlop = 3;
// Two presses this close (ms) on one handle are a double-click.
const cDoublePressMs = 450;
// The click a drag ends with comes at once; one later than this (ms) is
// another (a key on a heading after a touch drag, which has no click).
const cClickAfterDragMs = 500;

export function useFilesColumns(texts: ColumnTexts) {
  const tableRef = useRef<HTMLDivElement | null>(null);
  const given = useRef<ColumnWidths>(loadColumnWidths());
  const metrics = useRef<ColumnMetrics | null>(null);
  const layout = useRef<ColumnLayout | null>(null);
  const drag = useRef<Drag | null>(null);
  const lastPress = useRef<{ col: ColumnKey; at: number } | null>(null);
  // A drag just ended: the click it ends with sorts nothing.
  const swallowClick = useRef(0);
  const textsRef = useRef(texts);
  textsRef.current = texts;

  // While a column is dragged, the resize cursor over the whole page: a
  // layer of its own over it (the handle keeps the pointer, captured). A
  // rule on the table's descendants would restyle all of its rows, twice.
  const shield = useRef<HTMLDivElement | null>(null);
  const setResizing = useCallback((on: boolean) => {
    if (on && !shield.current) {
      const el = document.createElement("div");
      el.className = "fb-col-shield";
      el.setAttribute("aria-hidden", "true");
      document.body.appendChild(el);
      shield.current = el;
    } else if (!on && shield.current) {
      shield.current.remove();
      shield.current = null;
    }
  }, []);

  // What the columns need, measured in the table's own fonts and padding.
  const measure = useCallback((table: HTMLElement): ColumnMetrics | null => {
    const head = table.querySelector(".fb-head");
    if (!head) return null;
    const pad = (k: ColumnKey) => {
      const cell = head.querySelector(`.c-${k}`);
      if (!cell) return 24;
      const cs = getComputedStyle(cell);
      return (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
    };
    const tx = textsRef.current;
    const probe = document.createElement("div");
    probe.className = "fb-col-probe";
    probe.setAttribute("aria-hidden", "true");
    const piece = (tag: string, cls: string, text: string) => {
      const el = document.createElement(tag);
      if (cls) el.className = cls;
      el.textContent = text;
      probe.appendChild(el);
      return el;
    };
    const name = piece("button", "file", "0000000000…");
    const size = piece("span", "", tx.size);
    const sizeShort = piece("span", "", tx.sizeShort);
    const date = piece("span", "", tx.date);
    const dateShort = piece("span", "", tx.dateShort);
    table.appendChild(probe);
    const w = (el: Element) => el.getBoundingClientRect().width;
    const m = columnMetrics({
      pad: { name: pad("name"), size: pad("size"), created: pad("created"), modified: pad("modified") },
      name: w(name), size: w(size), sizeShort: w(sizeShort), date: w(date), dateShort: w(dateShort),
    });
    probe.remove();
    return m;
  }, []);

  // The layout on the page: the grid's columns, the hidden ones, and each
  // handle's values (a handle shows on every column but the last).
  const apply = useCallback((l: ColumnLayout) => {
    const table = tableRef.current;
    const m = metrics.current;
    if (!table || !m) return;
    layout.current = l;
    const cols = columnsTemplate(l, m);
    if (table.style.getPropertyValue("--fb-cols") !== cols) table.style.setProperty("--fb-cols", cols);
    const nameText = `${m.nameText}px`;
    if (table.style.getPropertyValue("--fb-name-text") !== nameText) table.style.setProperty("--fb-name-text", nameText);
    const hide = (["created", "modified"] as const).filter((k) => !l.shown.includes(k)).join(" ");
    if ((table.getAttribute("data-hide") ?? "") !== hide) {
      if (hide) table.setAttribute("data-hide", hide); else table.removeAttribute("data-hide");
    }
    const last = l.shown[l.shown.length - 1];
    table.querySelectorAll<HTMLElement>(".fb-col-grip").forEach((grip) => {
      const col = grip.dataset.col as ColumnKey;
      const off = !l.shown.includes(col) || col === last;
      grip.hidden = off;
      if (off) return;
      const v = edgeValues(l, m, col);
      grip.setAttribute("aria-valuenow", String(v.now));
      grip.setAttribute("aria-valuemin", String(v.min));
      grip.setAttribute("aria-valuemax", String(v.max));
    });
  }, []);

  // Fitted again to the table's width (a new width, language or font).
  const refit = useCallback(() => {
    const table = tableRef.current;
    if (!table || drag.current) return;
    const room = table.clientWidth;
    // Hidden (a search's results over the folder): kept as it was.
    if (room <= 0) return;
    const m = measure(table);
    if (!m) return;
    metrics.current = m;
    apply(fitColumns(room, given.current, m));
  }, [apply, measure]);

  const watch = useRef<ResizeObserver | null>(null);
  const ref = useCallback((el: HTMLDivElement | null) => {
    watch.current?.disconnect();
    watch.current = null;
    tableRef.current = el;
    drag.current = null;
    setResizing(false);
    if (!el) return;
    refit();
    let width = el.clientWidth;
    watch.current = new ResizeObserver(() => {
      if (el.clientWidth === width) return;
      width = el.clientWidth;
      refit();
    });
    watch.current.observe(el);
  }, [refit, setResizing]);

  // The texts changed (another language): measured again.
  const key = `${texts.size}|${texts.sizeShort}|${texts.date}|${texts.dateShort}`;
  useLayoutEffect(() => { refit(); }, [key, refit]);

  const keep = (next: ColumnWidths) => {
    given.current = next;
    saveColumnWidths(next);
  };
  const reset = (col: ColumnKey) => {
    const table = tableRef.current;
    const m = metrics.current;
    if (!table || !m) return;
    keep(resetColumn(given.current, col));
    apply(fitColumns(table.clientWidth, given.current, m));
  };
  // The drag over: kept (a move), put back (cancelled), or a press, which
  // with the one before it on this handle is a double-click.
  const finish = (el: HTMLElement, id: number, how: "up" | "cancel") => {
    const d = drag.current;
    if (!d || d.id !== id) return;
    drag.current = null;
    setResizing(false);
    if (el.hasPointerCapture?.(id)) el.releasePointerCapture(id);
    if (d.cancelled) { swallowClick.current = performance.now(); return; }
    if (!d.moved) {
      if (how !== "up") return;
      const now = performance.now();
      const prev = lastPress.current;
      if (prev && prev.col === d.col && now - prev.at < cDoublePressMs) {
        lastPress.current = null;
        reset(d.col);
      } else {
        lastPress.current = { col: d.col, at: now };
      }
      return;
    }
    lastPress.current = null;
    swallowClick.current = performance.now();
    if (how === "cancel") { apply(d.start); return; }
    if (layout.current) keep(widthsAfter(given.current, d.start, layout.current));
  };

  const grip = (col: ColumnKey) => ({
    "data-col": col,
    role: "separator",
    "aria-orientation": "vertical" as const,
    tabIndex: 0,
    onPointerDown: (e: ReactPointerEvent<HTMLElement>) => {
      if (e.pointerType === "mouse" && e.button !== 0) return;
      if (!layout.current || drag.current) return;
      e.preventDefault();
      e.stopPropagation();
      e.currentTarget.setPointerCapture(e.pointerId);
      // Escape puts a drag back: the handle has the keys (not a ring, after a press).
      e.currentTarget.focus({ preventScroll: true });
      drag.current = { id: e.pointerId, col, x: e.clientX, start: layout.current, moved: false, cancelled: false };
    },
    onPointerMove: (e: ReactPointerEvent<HTMLElement>) => {
      const d = drag.current;
      const m = metrics.current;
      if (!d || d.id !== e.pointerId || d.cancelled || !m) return;
      const dx = e.clientX - d.x;
      if (!d.moved) {
        if (Math.abs(dx) < cMoveSlop) return;
        d.moved = true;
        setResizing(true);
      }
      apply(resizeColumn(d.start, m, d.col, dx));
    },
    onPointerUp: (e: ReactPointerEvent<HTMLElement>) => finish(e.currentTarget, e.pointerId, "up"),
    onPointerCancel: (e: ReactPointerEvent<HTMLElement>) => finish(e.currentTarget, e.pointerId, "cancel"),
    onLostPointerCapture: (e: ReactPointerEvent<HTMLElement>) => finish(e.currentTarget, e.pointerId, "up"),
    onClick: (e: ReactMouseEvent<HTMLElement>) => { e.preventDefault(); e.stopPropagation(); },
    onKeyDown: (e: ReactKeyboardEvent<HTMLElement>) => {
      const m = metrics.current;
      const l = layout.current;
      if (!m || !l) return;
      const d = drag.current;
      if (e.key === "Escape" && d) {
        // Put back at once; the drag ends when the pointer is let go.
        e.preventDefault();
        if (d.moved && !d.cancelled) {
          d.cancelled = true;
          setResizing(false);
          apply(d.start);
        }
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (drag.current) return;
      const next = resizeColumn(l, m, col, e.key === "ArrowRight" ? cKeyStep : -cKeyStep);
      if (next === l) return;
      keep(widthsAfter(given.current, l, next));
      apply(next);
    },
  });

  // On the header, in the capture phase: the click a drag ends with sorts
  // nothing, wherever it lands; the next press there is a click of its own.
  const head = {
    onClickCapture: (e: ReactMouseEvent) => {
      if (!swallowClick.current) return;
      const fresh = performance.now() - swallowClick.current < cClickAfterDragMs;
      swallowClick.current = 0;
      if (fresh) {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    onPointerDownCapture: () => { swallowClick.current = 0; },
  };

  return { ref, grip, head };
}
