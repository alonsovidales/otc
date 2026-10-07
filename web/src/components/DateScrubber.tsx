// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { CSSProperties } from "react";
import "./DateScrubber.css";

// The date scrubber beside Images, as in Google Photos: the whole timeline
// under the current search, to jump through by date. A month sits as far
// down the track as the share of photos newer than it, so a busy year takes
// more of the track than a quiet one and a drag moves through the photos at
// an even pace. With a mouse or trackpad it is a column the grid leaves room
// for; with a finger it is a handle that shows while the page scrolls, and
// only the handle takes touches, so the page scrolls as usual everywhere else.

export type ScrubBucket = { month: string; count: number };

type Props = {
  // The whole timeline under the current filter, newest first ("YYYY-MM").
  buckets: ScrubBucket[];
  // The month of the photos at the top of the screen now, for the marker.
  currentMonth: string | null;
  // While dragging: the month under the pointer; null when the drag is
  // cancelled without a jump.
  onPreview: (bucket: ScrubBucket | null) => void;
  // Released on a month: show the photos from there.
  onJump: (month: string) => void;
};

const cFineQuery = "(hover: hover) and (pointer: fine)";
// A finger has to move the handle this far before it scrubs: a touch that
// stays put is a tap, and a tap on the handle does nothing.
const cEngagePx = 6;
// How long the handle stays after the page stops scrolling.
const cIdleMs = 1500;
// Labels closer than this would touch; under a finger they get more room,
// since they are read at a glance while dragging.
const cLabelGapFine = 16;
const cLabelGapTouch = 20;
// Month names (on a timeline of a year or two) keep further apart than
// years, so they read as landmarks rather than a list.
const cMonthGapExtra = 16;
// The track's ends are inset so nothing is cut in half there: a label (and
// the focus ring) with a mouse, the handle (64px tall) with a finger.
const cInsetFine = 12;
const cInsetTouch = 32;
// Half the mouse bubble's height (DateScrubber.css), to keep it on the track.
const cBubbleHalfFine = 16;
const cMonthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// A bucket and where its photos fall in the timeline (0 = the newest).
type Span = { month: string; start: number; end: number; index: number };
type Label = { key: string; text: string; y: number; year: boolean };
// The scrub on screen: `y` is its place on the track. `on` turns false when
// it ends, so the touch bubble can fade out still showing its month.
type Scrub = { y: number; index: number; on: boolean };
// What the event handlers share between events; state lags a render behind.
// `keys`: a scrub made with keys was going on when the pointer came down,
// and a touch that never moves gives it back.
type PointerGesture = { by: "pointer"; pointerId: number; startY: number; startCenter: number; engaged: boolean; keys: boolean };
type Gesture = PointerGesture | { by: "keys" };

// The main pointer can change (a tablet gains a mouse), so it is watched.
let fineList: MediaQueryList | null = null;
const fineMedia = () => (fineList ??= window.matchMedia(cFineQuery));
const subscribePointer = (onChange: () => void) => {
  const mq = fineMedia();
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
};
const isFinePointer = () => fineMedia().matches;

function monthName(month: string): string | null {
  const m = Number(month.slice(5, 7));
  return m >= 1 && m <= 12 ? cMonthNames[m - 1] : null;
}

// "Mar 2024". A photo without a date is grouped under a zero month.
function monthLabel(month: string): string {
  const name = monthName(month);
  const year = month.slice(0, 4);
  return name && Number(year) > 0 ? `${name} ${year}` : "No date";
}

function spansOf(buckets: ScrubBucket[]): { spans: Span[]; total: number } {
  let total = 0;
  const spans = buckets.map((b, index): Span => {
    const start = total;
    total += Math.max(0, b.count);
    return { month: b.month, start, end: total, index };
  });
  return { spans, total };
}

// The span holding the photo `at` places from the newest.
function spanAt(spans: Span[], at: number): Span {
  let lo = 0;
  let hi = spans.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (spans[mid].end <= at) lo = mid + 1;
    else hi = mid;
  }
  return spans[lo];
}

// The span of a month the gallery reports. A photo's date and the device's
// bucket for it can fall in different months near midnight (time zones), so
// a month without a bucket of its own takes the next older one, which starts
// where it would.
function spanOfMonth(spans: Span[], month: string): Span {
  return spans.find((s) => s.month <= month) ?? spans[spans.length - 1];
}

function shiftMonth(month: string, by: number): string {
  const y = Number(month.slice(0, 4));
  const m = Number(month.slice(5, 7));
  if (!(m >= 1 && m <= 12)) return month;
  const t = y * 12 + (m - 1) + by;
  return `${String(Math.floor(t / 12)).padStart(4, "0")}-${String((((t % 12) + 12) % 12) + 1).padStart(2, "0")}`;
}

// PageUp/PageDown: the nearest month at least a year newer (dir -1) or older
// (dir 1) than `from`, or the end of the timeline.
function yearAway(spans: Span[], from: number, dir: 1 | -1): number {
  const target = shiftMonth(spans[from].month, -12 * dir);
  if (dir === 1) {
    for (let k = from + 1; k < spans.length; k++) if (spans[k].month <= target) return k;
    return spans.length - 1;
  }
  for (let k = from - 1; k >= 0; k--) if (spans[k].month >= target) return k;
  return 0;
}

// A label where each year starts. Where they crowd, the year with more
// photos keeps its label: the stretch of track is mostly that year's. On a
// timeline of a year or two, month names fill in where they fit, or the
// track would be one label and a lot of nothing.
function layoutLabels(spans: Span[], total: number, height: number, inset: number, gap: number): Label[] {
  const travel = height - 2 * inset;
  if (travel <= 0 || total <= 0) return [];
  const yOf = (s: Span) => inset + (s.start / total) * travel;
  const years = new Map<string, { first: Span; count: number }>();
  for (const s of spans) {
    const year = s.month.slice(0, 4);
    const seen = years.get(year);
    if (seen) seen.count += s.end - s.start;
    else years.set(year, { first: s, count: s.end - s.start });
  }
  const placed: Label[] = [];
  // Heaviest first; the sort is stable, so the newer wins a tie.
  const place = (candidates: { label: Label; weight: number }[], room: number) => {
    candidates.sort((a, b) => b.weight - a.weight);
    for (const { label } of candidates) {
      if (placed.every((p) => Math.abs(p.y - label.y) >= room)) placed.push(label);
    }
  };
  const yearCandidates: { label: Label; weight: number }[] = [];
  for (const [year, { first, count }] of years) {
    if (!(Number(year) > 0) || count === 0) continue;
    yearCandidates.push({ label: { key: `y${year}`, text: year, y: yOf(first), year: true }, weight: count });
  }
  place(yearCandidates, gap);
  if (years.size <= 2) {
    const monthCandidates: { label: Label; weight: number }[] = [];
    for (const s of spans) {
      const name = monthName(s.month);
      // A year's first month is where the year's own label is.
      if (!name || s.end === s.start || years.get(s.month.slice(0, 4))?.first === s) continue;
      monthCandidates.push({ label: { key: `m${s.month}`, text: name, y: yOf(s), year: false }, weight: s.end - s.start });
    }
    place(monthCandidates, gap + cMonthGapExtra);
  }
  return placed.sort((a, b) => a.y - b.y);
}

// Places an element on the track (DateScrubber.css reads --y), on whole
// pixels so thin lines and text stay sharp.
const at = (y: number) => ({ "--y": `${Math.round(y)}px` }) as CSSProperties;

export default function DateScrubber({ buckets, currentMonth, onPreview, onJump }: Props) {
  const fine = useSyncExternalStore(subscribePointer, isFinePointer);
  const { spans, total } = useMemo(() => spansOf(buckets), [buckets]);
  const live = total > 0;
  const inset = fine ? cInsetFine : cInsetTouch;

  // The track's height: positions are fractions of it, and labels are thinned
  // by the real distance between them.
  const trackRef = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const el = trackRef.current;
    if (!el) return;
    const measure = () => setHeight(el.clientHeight);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [live]);

  const labels = useMemo(
    () => layoutLabels(spans, total, height, inset, fine ? cLabelGapFine : cLabelGapTouch),
    [spans, total, height, inset, fine],
  );

  const [scrub, setScrub] = useState<Scrub | null>(null);
  const [hoverY, setHoverY] = useState<number | null>(null);
  // After a jump: its month, until the gallery reports another month than the
  // one it reported when the jump was made (`from`). The gallery may not
  // report anything new until the page scrolls, and the marker or handle
  // should not slide back to where the photos were before the jump.
  const [jumped, setJumped] = useState<{ month: string; from: string | null } | null>(null);
  // Touch: the page scrolled lately; a finger is on the handle.
  const [awake, setAwake] = useState(false);
  const [holding, setHolding] = useState(false);
  const gesture = useRef<Gesture | null>(null);
  // The bucket last sent to onPreview in this scrub; -1 before the first.
  const previewed = useRef(-1);
  const wake = useRef(() => {});
  const onPreviewRef = useRef(onPreview);
  useEffect(() => {
    onPreviewRef.current = onPreview;
  });

  // Touch: the handle shows while the page scrolls, and for a moment after.
  useEffect(() => {
    if (fine) return;
    let timer = 0;
    const onScroll = () => {
      setAwake(true);
      window.clearTimeout(timer);
      timer = window.setTimeout(() => setAwake(false), cIdleMs);
    };
    wake.current = onScroll;
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.clearTimeout(timer);
    };
  }, [fine]);

  // Another search brings another timeline, and a mouse plugged in or out
  // swaps the column for the handle: a scrub begun on the old one means
  // nothing now, and the gallery is told if it was showing its preview.
  const timeline = useMemo(() => buckets.map((b) => `${b.month}:${b.count}`).join(), [buckets]);
  const basis = `${fine ? "fine" : "touch"} ${timeline}`;
  useEffect(() => () => {
    gesture.current = null;
    if (previewed.current >= 0) {
      previewed.current = -1;
      onPreviewRef.current(null);
    }
  }, [basis]);
  const [shownBasis, setShownBasis] = useState(basis);
  if (shownBasis !== basis) {
    setShownBasis(basis);
    setScrub(null);
    setHoverY(null);
    setJumped(null);
    setHolding(false);
  }
  if (jumped && currentMonth != null && currentMonth !== jumped.from) setJumped(null);

  if (!live) return null;

  const n = spans.length;
  const travel = Math.max(1, height - 2 * inset);
  const clampY = (y: number) => Math.min(inset + travel, Math.max(inset, y));
  const yOf = (s: Span) => inset + (s.start / total) * travel;
  const spanAtY = (y: number) => spanAt(spans, ((clampY(y) - inset) / travel) * total);
  const here = jumped ? jumped.month : currentMonth;
  const hereSpan = here == null ? null : spanOfMonth(spans, here);
  const active = scrub && scrub.on && scrub.index < n ? scrub : null;
  // Touch: with nothing reported yet the photos start at the newest.
  const handleY = active ? active.y : hereSpan ? yOf(hereSpan) : inset;

  const trackY = (clientY: number) => clientY - (trackRef.current?.getBoundingClientRect().top ?? 0);

  // Puts the scrub on `s` and tells the gallery when its month changes.
  const show = (s: Span, y: number) => {
    setScrub({ y, index: s.index, on: true });
    if (previewed.current !== s.index) {
      previewed.current = s.index;
      onPreview(buckets[s.index]);
    }
  };
  const scrubTo = (y: number) => {
    const to = clampY(y);
    show(spanAtY(to), to);
  };

  // Ends the scrub: the photos jump to its month, or the gallery goes back
  // to what it showed before it.
  const finish = (jump: boolean) => {
    gesture.current = null;
    setHolding(false);
    setScrub((s) => (s ? { ...s, on: false } : s));
    const index = previewed.current;
    previewed.current = -1;
    if (index < 0) return;
    if (jump) {
      const month = buckets[index].month;
      setJumped({ month, from: currentMonth });
      onJump(month);
    } else {
      onPreview(null);
    }
  };

  const ours = (e: React.PointerEvent): PointerGesture | null => {
    const g = gesture.current;
    return g && g.by === "pointer" && g.pointerId === e.pointerId ? g : null;
  };

  // Mouse: the whole column; a press shows its month at once, as a click
  // anywhere on a scrollbar's track does.
  const onColumnDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!e.isPrimary || e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    gesture.current = { by: "pointer", pointerId: e.pointerId, startY: e.clientY, startCenter: 0, engaged: true, keys: false };
    setJumped(null);
    scrubTo(trackY(e.clientY));
  };
  const onColumnMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const y = trackY(e.clientY);
    if (ours(e)) scrubTo(y);
    else if (e.pointerType !== "touch") setHoverY(Math.round(clampY(y)));
  };
  const onColumnUp = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!ours(e)) return;
    // The hover line comes back where the button was let go, not where the
    // press started.
    if (e.pointerType !== "touch") setHoverY(Math.round(clampY(trackY(e.clientY))));
    finish(true);
  };
  const onColumnCancel = (e: React.PointerEvent<HTMLDivElement>) => {
    if (ours(e)) finish(false);
  };

  // Touch: the handle moves with the finger by as much as the finger moves,
  // wherever on the handle it was grabbed. It starts from where it is on
  // screen, which is not yet handleY while it glides to a new month.
  const onHandleDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!e.isPrimary || e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    const r = e.currentTarget.getBoundingClientRect();
    const startCenter = trackY(r.top + r.height / 2);
    const keys = gesture.current?.by === "keys";
    gesture.current = { by: "pointer", pointerId: e.pointerId, startY: e.clientY, startCenter, engaged: false, keys };
    setHolding(true);
  };
  const onHandleMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const g = ours(e);
    if (!g) return;
    const dy = e.clientY - g.startY;
    if (!g.engaged) {
      if (Math.abs(dy) < cEngagePx) return;
      g.engaged = true;
      setJumped(null);
    }
    scrubTo(g.startCenter + dy);
  };
  // A touch that never moved: nothing happens, and a scrub made with keys
  // carries on.
  const letGo = (g: PointerGesture) => {
    gesture.current = g.keys ? { by: "keys" } : null;
    setHolding(false);
  };
  const onHandleUp = (e: React.PointerEvent<HTMLDivElement>) => {
    const g = ours(e);
    if (!g) return;
    wake.current();
    if (g.engaged) finish(true);
    else letGo(g);
  };
  const onHandleCancel = (e: React.PointerEvent<HTMLDivElement>) => {
    const g = ours(e);
    if (!g) return;
    wake.current();
    if (g.engaged) finish(false);
    else letGo(g);
  };

  // Keys: arrows step a month (Up is newer), PageUp/PageDown a year,
  // Home/End the ends; Enter or Space jumps, Escape puts things back.
  const keyTarget = (key: string, from: number): number | null => {
    switch (key) {
      case "ArrowUp":
      case "ArrowRight":
        return from - 1;
      case "ArrowDown":
      case "ArrowLeft":
        return from + 1;
      case "PageUp":
        return yearAway(spans, from, -1);
      case "PageDown":
        return yearAway(spans, from, 1);
      case "Home":
        return 0;
      case "End":
        return n - 1;
      default:
        return null;
    }
  };
  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    // Alt+Left is the browser's Back, and so on.
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const g = gesture.current;
    if (e.key === "Escape" || e.key === "Enter" || e.key === " ") {
      const jump = e.key !== "Escape";
      if (!g || (jump && g.by !== "keys")) return;
      finish(jump);
    } else {
      // A drag in progress has the scrub.
      if (g?.by === "pointer") return;
      const from = g && previewed.current >= 0 ? previewed.current : hereSpan ? hereSpan.index : 0;
      const to = keyTarget(e.key, from);
      if (to == null) return;
      const s = spans[Math.max(0, Math.min(n - 1, to))];
      gesture.current = { by: "keys" };
      setJumped(null);
      show(s, yOf(s));
    }
    // Handled here: no page scroll, and Escape doesn't also clear the
    // gallery's selection.
    e.preventDefault();
    e.stopPropagation();
  };
  const onBlur = () => {
    if (gesture.current?.by === "keys") finish(false);
  };
  // A screen reader adjusts a slider with arrow keys and activates it with
  // a click: that jumps to the month the keys picked.
  const onClick = () => {
    if (gesture.current?.by === "keys") finish(true);
  };

  const valueIndex = active ? active.index : hereSpan ? hereSpan.index : 0;
  // Older is down the track; a slider's value grows upwards, so it counts
  // months from the oldest.
  const slider = {
    role: "slider",
    tabIndex: 0,
    "aria-label": "Jump to a date",
    "aria-orientation": "vertical" as const,
    "aria-valuemin": 0,
    "aria-valuemax": n - 1,
    "aria-valuenow": n - 1 - valueIndex,
    "aria-valuetext": monthLabel(spans[valueIndex].month),
    onKeyDown,
    onBlur,
    onClick,
  };

  const labelNodes = labels.map((l) => (
    <span key={l.key} className={l.year ? "ds-label" : "ds-label is-month"} style={at(l.y)}>
      {l.text}
    </span>
  ));

  if (fine) {
    const lineY = active ? active.y : hoverY;
    return (
      <div
        ref={trackRef}
        className={`ds-root ds-fine${active ? " is-scrubbing" : ""}`}
        {...slider}
        onPointerDown={onColumnDown}
        onPointerMove={onColumnMove}
        onPointerUp={onColumnUp}
        onPointerCancel={onColumnCancel}
        onLostPointerCapture={onColumnCancel}
        onPointerLeave={() => setHoverY(null)}
      >
        <div className="ds-labels" aria-hidden="true">{labelNodes}</div>
        {!active && hereSpan && <div className="ds-marker" style={at(yOf(hereSpan))} aria-hidden="true" />}
        {lineY != null && (
          <>
            <div className="ds-line" style={at(lineY)} aria-hidden="true" />
            <div
              className="ds-bubble"
              style={at(Math.min(height - cBubbleHalfFine, Math.max(cBubbleHalfFine, lineY)))}
              aria-hidden="true"
            >
              {monthLabel(spans[active ? active.index : spanAtY(lineY).index].month)}
            </div>
          </>
        )}
      </div>
    );
  }

  const bubble = scrub && scrub.index < n ? scrub : null;
  const shown = awake || holding || active != null;
  return (
    <div
      ref={trackRef}
      className={`ds-root ds-touch${shown ? " is-shown" : ""}${active ? " is-scrubbing" : ""}`}
    >
      <div className="ds-strip" aria-hidden="true" />
      <div className="ds-labels" aria-hidden="true">{labelNodes}</div>
      {bubble && (
        <div className="ds-bubble" style={at(bubble.y)} aria-hidden="true">
          {monthLabel(spans[bubble.index].month)}
        </div>
      )}
      <div
        className="ds-handle"
        style={at(handleY)}
        {...slider}
        onPointerDown={onHandleDown}
        onPointerMove={onHandleMove}
        onPointerUp={onHandleUp}
        onPointerCancel={onHandleCancel}
        onLostPointerCapture={onHandleCancel}
        onContextMenu={(e) => e.preventDefault()}
      >
        <span className="ds-pill">
          <ChevronsIcon />
        </span>
      </div>
    </div>
  );
}

// Up and down: the handle drags through the timeline either way.
function ChevronsIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="m8 10 4-4 4 4M8 14l4 4 4-4" />
    </svg>
  );
}
