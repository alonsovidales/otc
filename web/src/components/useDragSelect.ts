// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #200: the press-and-drag that picks a run of items (dragSelect.ts
// has the rules), for Images, People and Files. The component keeps its
// selection and its clicks; this owns the press on its grid or list:
//
// - A mouse press on an item (left button, no modifier key) that moves more
//   than cDragThresholdPx is a drag; less is the click it always was.
// - A touch (or pen) held cLongPressMs on an item picks it - the long press
//   the grids always had - and moving on without lifting is a drag. Moving
//   first is a scroll, as before.
// - Only a press on an item itself starts anything (`indexAt` with `press`):
//   on a gap, past the end of a short row, on a month's title, a header,
//   Files' ".." or a face's name, count or "Add a name" it starts nothing
//   and the page scrolls as usual.
// - While dragging: the item under the pointer is looked for again at each
//   move and after each scroll (the wheel works mid-drag), text isn't
//   selected, the page doesn't scroll under the finger, and Escape puts the
//   selection back as it was before the drag. The page scrolls near the
//   window's top and bottom, but only once the pointer has moved more than
//   cDragThresholdPx since the drag began (a mouse drag has; a long press
//   held still near an edge never scrolls). The click a drag or a long
//   press ends with goes nowhere.
// - The item pressed going away mid-drag ends the drag with the selection
//   as it stands: later moves change nothing, and the release is still no
//   click. The item under the pointer going away leaves the selection as it
//   is until the pointer is over another item. A pick that goes from the
//   items (the view drops it) goes from what the drag started from too, so
//   the next move doesn't bring it back.
// - Something coming over the items (`enabled` false: the viewer, a dialog,
//   the date scrubber) or a new Files listing (a refresh, an upload
//   finishing, another folder: Files is disabled while it loads) ends the
//   drag the same way, with the selection as the view makes it (the
//   listing clears it); Shift+click doesn't go on from it.
// - Something the selection shows in the page's flow above the items (Files'
//   actions bar) would move them under the pointer: the page scrolls by as
//   much, so they stay put.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import type { DragEvent as ReactDragEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import {
  cLongPressMs, dragPointer, dragSelection, findItem, hitPoint, keptBase, movePointer, pastThreshold, pointerSpeed,
  relocate, sameSelection,
} from "./dragSelect";
import type { DragEnds, DragPointer } from "./dragSelect";
import "./dragSelect.css";

export type DragSelectOptions<T> = {
  // False while something lies over the items (the viewer, a dialog, the
  // date scrubber's grey tiles): no press starts, and a drag ends.
  enabled: boolean;
  // Everything shown, in display order.
  items: readonly T[];
  keyOf: (item: T) => string;
  // The item an element is part of, as an index into `items`, or null.
  // `press`: where a press may start (a control of its own may not be).
  indexAt: (el: Element, press: boolean) => number | null;
  // What can't be picked (Files' ".."): a press there starts nothing, and
  // a run over it leaves it out.
  skip?: (item: T) => boolean;
  // The selection, in its own order.
  selection: readonly T[];
  setSelection: (next: T[]) => void;
  // A drag (or long press) ended on this item (its index in `items`):
  // Shift+click goes on from there.
  onEnd?: (last: T, index: number) => void;
};

// (x, y): where the press began; (cx, cy): where the pointer is now, which
// is where a long press fires.
type Press = { pointerId: number; type: string; x: number; y: number; cx: number; cy: number; index: number; key: string; timer: number };
type Drag<T> = DragEnds & {
  pointerId: number;
  type: string;
  // The selection the drag started from, less the picks that went from
  // the items since (keptBase), and the items as the drag last saw them.
  base: readonly T[];
  items: readonly T[];
  pointer: DragPointer;
  applied: T[] | null;
  frame: number;
  lastTs: number;
  // Escape: the selection is back as it was, and nothing more happens
  // until the pointer is released.
  cancelled: boolean;
  // The item pressed went away, something came over the items, or Files'
  // listing is being read again: the selection stays as the view has it,
  // and nothing more happens until the pointer is released.
  over: boolean;
  // Over because something came over the items or a new listing is on its
  // way: Shift+click doesn't go on from where it ended.
  interrupted: boolean;
};

// The click a drag or a long press ends with (it follows the release at
// once, or not at all) goes nowhere; the next press clears the way.
function swallowClick() {
  const until = performance.now() + 600;
  const onClick = (e: MouseEvent) => {
    done();
    if (performance.now() > until) return;
    e.preventDefault();
    e.stopPropagation();
  };
  const done = () => {
    window.removeEventListener("click", onClick, true);
    window.removeEventListener("pointerdown", done, true);
    window.clearTimeout(timer);
  };
  const timer = window.setTimeout(done, 700);
  window.addEventListener("click", onClick, true);
  window.addEventListener("pointerdown", done, true);
}

const topBarHeight = () =>
  parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--topbar-h")) || 64;

export function useDragSelect<T>(options: DragSelectOptions<T>) {
  const opts = useRef(options);
  useLayoutEffect(() => { opts.current = options; });

  const container = useRef<HTMLElement | null>(null);
  const press = useRef<Press | null>(null);
  const drag = useRef<Drag<T> | null>(null);
  const lastType = useRef("mouse");
  // The page's own min-height before room was added below it to keep the
  // items in place (see keepPut), until nothing is picked.
  const room = useRef<{ was: string } | null>(null);

  // One set of listeners for the life of the component: they read the
  // press and the drag from the refs above.
  const [l] = useState(() => {
    const setMark = (value: "press" | "drag" | null) => {
      const el = container.current;
      if (!el) return;
      if (value) el.dataset.dragsel = value;
      else delete el.dataset.dragsel;
    };

    // The items moved by `delta` on the screen (something the selection
    // shows went in or out above them): the page scrolls by as much, so
    // they stay under the pointer. A page too short for that gets the room
    // it needs below.
    const keepPut = (delta: number) => {
      const doc = document.documentElement;
      const want = window.scrollY + delta;
      const max = doc.scrollHeight - window.innerHeight;
      if (want > max) {
        if (!room.current) room.current = { was: doc.style.minHeight };
        doc.style.minHeight = `${doc.scrollHeight + Math.ceil(want - max)}px`;
      }
      window.scrollTo(window.scrollX, Math.max(0, want));
    };

    // A new selection, on the screen before this returns, and the items
    // kept where they were: Files' actions bar comes with the first pick
    // and pushes them down. (The browser's own scroll anchoring may have
    // made up for it already: what counts is where they are now.)
    const commit = (next: T[]) => {
      const el = container.current;
      const before = el?.getBoundingClientRect().top ?? 0;
      flushSync(() => opts.current.setSelection(next));
      if (!el?.isConnected) return;
      const delta = el.getBoundingClientRect().top - before;
      if (Math.abs(delta) >= 0.5) keepPut(delta);
    };

    // The room added below the page goes once no drag is on and nothing is
    // picked: after a render, and at the release (a drag the listing ended
    // has nothing picked, and nothing renders after its release).
    const freeRoom = () => {
      if (room.current && !drag.current && opts.current.selection.length === 0) {
        document.documentElement.style.minHeight = room.current.was;
        room.current = null;
      }
    };

    // The item pressed is gone, or the items were taken away (see
    // interrupt): the drag is over with what the view has picked, waiting
    // for the release, whose click still goes nowhere.
    const finish = (d: Drag<T>) => {
      d.over = true;
      cancelAnimationFrame(d.frame);
    };

    // The drag's ends found again in the items as they are now; false once
    // the item pressed is gone (the drag is then over).
    const locate = (d: Drag<T>) => {
      const o = opts.current;
      const at = relocate(d, o.items, o.keyOf);
      if (!at) {
        finish(d);
        return false;
      }
      d.start = at.start;
      d.end = at.end;
      return true;
    };

    const apply = (d: Drag<T>) => {
      if (d.cancelled || d.over || !locate(d)) return;
      // The item last under the pointer is gone: the selection stays as it
      // is until the pointer is over another.
      if (d.end < 0) return;
      const o = opts.current;
      const next = dragSelection(d.base, o.items, o.keyOf, d.start, d.end, o.skip);
      if (d.applied && sameSelection(d.applied, next, o.keyOf)) return;
      d.applied = next;
      commit(next);
    };

    // The item under the pointer, looked for again: after a move, and
    // after the page scrolled under it.
    const hit = () => {
      const d = drag.current;
      const el = container.current;
      if (!d || d.cancelled || d.over || !el) return;
      const rect = el.getBoundingClientRect();
      const p = hitPoint(d.pointer.x, d.pointer.y, rect, topBarHeight(), window.innerHeight);
      const under = document.elementFromPoint(p.x, p.y);
      // Over a gap, a month's title or past the last item: the last item
      // the pointer was on still counts.
      const o = opts.current;
      const idx = under && el.contains(under) ? o.indexAt(under, false) : null;
      const item = idx == null ? undefined : o.items[idx];
      if (idx == null || idx === d.end || item === undefined) return;
      d.end = idx;
      d.endKey = o.keyOf(item);
      apply(d);
    };

    // Near the top or the bottom of the window the page scrolls, faster
    // towards the edge, once the pointer has moved since the drag began;
    // pages load on the way as they would.
    const step = (ts: number) => {
      const d = drag.current;
      if (!d || d.cancelled || d.over) return;
      const dt = d.lastTs ? Math.min(50, ts - d.lastTs) : 16;
      d.lastTs = ts;
      const speed = pointerSpeed(d.pointer, topBarHeight(), window.innerHeight);
      if (speed !== 0) {
        const before = window.scrollY;
        window.scrollBy(0, (speed * dt) / 1000);
        if (window.scrollY !== before) hit();
      }
      d.frame = requestAnimationFrame(step);
    };

    const onMove = (e: PointerEvent) => {
      const d = drag.current;
      if (d && e.pointerId === d.pointerId) {
        if (d.cancelled || d.over) return;
        movePointer(d.pointer, e.clientX, e.clientY);
        hit();
        return;
      }
      const p = press.current;
      if (!p || e.pointerId !== p.pointerId) return;
      if (!pastThreshold(e.clientX - p.x, e.clientY - p.y)) {
        p.cx = e.clientX;
        p.cy = e.clientY;
        return;
      }
      if (p.type === "mouse") {
        begin(p, e.clientX, e.clientY);
      } else {
        // Moving before the long press: a scroll.
        cancelPress();
      }
    };
    const onUp = (e: PointerEvent) => {
      const d = drag.current;
      if (d && e.pointerId === d.pointerId) {
        end(true);
        return;
      }
      const p = press.current;
      if (p && e.pointerId === p.pointerId) cancelPress();
    };
    const onKey = (e: KeyboardEvent) => {
      const d = drag.current;
      if (!d || e.key !== "Escape") return;
      // Not the Escape that clears the selection: this one only takes the
      // drag back.
      e.preventDefault();
      e.stopPropagation();
      if (d.cancelled || d.over) return;
      d.cancelled = true;
      cancelAnimationFrame(d.frame);
      commit(d.base.slice());
    };
    const onScroll = () => hit();
    const onSelectStart = (e: Event) => e.preventDefault();
    // A non-passive listener (passive ones can't stop a scroll), there for
    // as long as the container is: Safari and Chrome only let a touch's
    // moves be cancelled when one was in place as the touch began. It
    // cancels nothing but a touch drag's moves.
    const onTouchMove = (e: TouchEvent) => {
      const d = drag.current;
      if (d && d.type !== "mouse" && e.cancelable) e.preventDefault();
    };
    // The same listener on the element the touch began on, for as long as
    // it is held: a touch's moves keep going to that element even once it
    // has left the page (a new listing, a face that went), and the
    // container no longer hears them - the page would follow the finger.
    let touched: EventTarget | null = null;
    const watchTouch = (el: EventTarget | null) => {
      if (touched === el) return;
      touched?.removeEventListener("touchmove", onTouchMove as EventListener);
      touched = el;
      el?.addEventListener("touchmove", onTouchMove as EventListener, { passive: false });
    };

    const listen = (on: boolean) => {
      if (on) {
        window.addEventListener("pointermove", onMove, true);
        window.addEventListener("pointerup", onUp, true);
        window.addEventListener("pointercancel", onUp, true);
      } else {
        window.removeEventListener("pointermove", onMove, true);
        window.removeEventListener("pointerup", onUp, true);
        window.removeEventListener("pointercancel", onUp, true);
      }
    };
    const listenDrag = (on: boolean) => {
      if (on) {
        window.addEventListener("keydown", onKey, true);
        window.addEventListener("scroll", onScroll, { passive: true });
        window.addEventListener("selectstart", onSelectStart, true);
      } else {
        window.removeEventListener("keydown", onKey, true);
        window.removeEventListener("scroll", onScroll);
        window.removeEventListener("selectstart", onSelectStart, true);
      }
    };

    const cancelPress = () => {
      const p = press.current;
      if (!p) return;
      window.clearTimeout(p.timer);
      press.current = null;
      if (!drag.current) {
        setMark(null);
        listen(false);
        watchTouch(null);
      }
    };

    const begin = (p: Press, x: number, y: number) => {
      const o = opts.current;
      const el = container.current;
      // The item pressed may have gone while it was held.
      const start = findItem(o.items, o.keyOf, p.key, p.index);
      if (!el || !o.enabled || start < 0) {
        cancelPress();
        return;
      }
      window.clearTimeout(p.timer);
      press.current = null;
      const d: Drag<T> = {
        pointerId: p.pointerId, type: p.type, base: o.selection.slice(), items: o.items,
        startKey: p.key, start, endKey: p.key, end: start,
        pointer: dragPointer(x, y, p.type === "mouse"), applied: null, frame: 0, lastTs: 0,
        cancelled: false, over: false, interrupted: false,
      };
      drag.current = d;
      try { el.setPointerCapture(p.pointerId); } catch { /* the pointer is gone already */ }
      setMark("drag");
      window.getSelection()?.removeAllRanges();
      listenDrag(true);
      if (p.type !== "mouse") {
        try { navigator.vibrate?.(10); } catch { /* not every device can */ }
      }
      apply(d);
      // A mouse may be over another item already.
      if (p.type === "mouse") hit();
      d.frame = requestAnimationFrame(step);
    };

    // `click`: the click that follows goes nowhere. `anchor`: Shift+click
    // goes on from where the drag ended.
    const end = (click: boolean, anchor = true) => {
      const d = drag.current;
      if (!d) return;
      drag.current = null;
      cancelAnimationFrame(d.frame);
      listenDrag(false);
      listen(false);
      watchTouch(null);
      const el = container.current;
      try { if (el?.hasPointerCapture(d.pointerId)) el.releasePointerCapture(d.pointerId); } catch { /* released already */ }
      setMark(null);
      if (click) swallowClick();
      const o = opts.current;
      if (anchor && !d.cancelled && !d.interrupted && d.end >= 0) {
        const at = findItem(o.items, o.keyOf, d.endKey, d.end);
        if (at >= 0) o.onEnd?.(o.items[at], at);
      }
      freeRoom();
    };

    const startPress = (e: ReactPointerEvent<HTMLElement>) => {
      lastType.current = e.pointerType;
      cancelPress();
      const o = opts.current;
      if (!o.enabled || !e.isPrimary || drag.current || !(e.target instanceof Element)) return;
      const mouse = e.pointerType === "mouse";
      if (mouse && (e.button !== 0 || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey)) return;
      const index = o.indexAt(e.target, true);
      const item = index == null ? undefined : o.items[index];
      if (index == null || item === undefined || o.skip?.(item)) return;
      const p: Press = {
        pointerId: e.pointerId, type: e.pointerType, x: e.clientX, y: e.clientY, cx: e.clientX, cy: e.clientY,
        index, key: o.keyOf(item), timer: 0,
      };
      if (!mouse) {
        p.timer = window.setTimeout(() => {
          if (press.current === p) begin(p, p.cx, p.cy);
        }, cLongPressMs);
        // No text selection or picture callout while it is held.
        setMark("press");
        watchTouch(e.target);
      }
      press.current = p;
      listen(true);
    };

    // Android answers a long press on a picture or a word with its own
    // menu: here the press picks the item instead (at once, when the menu
    // comes first).
    const contextMenu = (e: ReactMouseEvent<HTMLElement>) => {
      if (lastType.current === "mouse") return;
      if (drag.current) {
        e.preventDefault();
        return;
      }
      if (!(e.target instanceof Element) || opts.current.indexAt(e.target, true) == null) return;
      e.preventDefault();
      const p = press.current;
      if (p && p.type !== "mouse") begin(p, p.cx, p.cy);
    };

    // Nothing in the items is dragged as a picture or a link would be (a
    // browser's own drag would take the pointer away).
    const dragStart = (e: ReactDragEvent<HTMLElement>) => {
      if (press.current || drag.current) e.preventDefault();
    };

    const attach = (el: HTMLElement | null) => {
      const old = container.current;
      if (old === el) return;
      if (old) {
        old.removeEventListener("touchmove", onTouchMove);
        // The items went away under the pointer.
        cancelPress();
        end(false, false);
        delete old.dataset.dragsel;
      }
      container.current = el;
      if (el) el.addEventListener("touchmove", onTouchMove, { passive: false });
    };

    // The component is going: everything stops at once.
    const stop = () => {
      cancelPress();
      end(false, false);
    };

    // Something came over the items (the viewer, a dialog, the date
    // scrubber's grey tiles), or Files is reading its listing again (a
    // refresh, an upload finishing, another folder): a press not yet a drag
    // ends, and a drag is over - later moves change nothing, the edges stop
    // scrolling, a touch still doesn't scroll the page, and the release is
    // still no click. The selection is what the view makes it (Files' new
    // listing clears it).
    const interrupt = () => {
      cancelPress();
      const d = drag.current;
      if (!d) return;
      d.interrupted = true;
      finish(d);
    };

    // The items changed (a page came, a face went): a pick gone from them
    // goes from the drag's base too, the drag's ends are found again by
    // key, and the drag is over once the item pressed is gone. The
    // selection waits for the next move.
    const itemsChanged = () => {
      const d = drag.current;
      if (!d || d.cancelled || d.over) return;
      const o = opts.current;
      d.base = keptBase(d.base, d.items, o.items, o.keyOf);
      d.items = o.items;
      locate(d);
    };

    return { startPress, contextMenu, dragStart, attach, stop, interrupt, itemsChanged, freeRoom };
  });

  useLayoutEffect(() => l.itemsChanged(), [options.items, l]);

  // No press or drag goes on once the items are covered or being listed
  // again; before the screen shows it, so no move lands in between.
  useLayoutEffect(() => {
    if (!options.enabled) l.interrupt();
  }, [options.enabled, l]);
  useEffect(() => () => l.stop(), [l]);

  // The room added below the page to keep the items in place goes once
  // nothing is picked.
  useLayoutEffect(() => l.freeRoom());
  useEffect(() => () => {
    if (room.current) document.documentElement.style.minHeight = room.current.was;
  }, []);

  const ref = useCallback((el: HTMLElement | null) => l.attach(el), [l]);
  return {
    // The items' container: its ref, and the handlers it takes.
    ref,
    onPointerDown: l.startPress,
    onContextMenu: l.contextMenu,
    onDragStart: l.dragStart,
    // How the last press was made ("mouse", "touch", "pen"): a tap on a
    // touch screen picks where a click opens.
    pointerType: () => lastType.current,
  };
}
