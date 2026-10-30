// SPDX-License-Identifier: AGPL-3.0-or-later

// A memory's slideshow (On this day, memories.ts): its photos one after
// another, 4 s each, the next memory after the last one. A tap on the right
// goes on, on the left back; held down (or Space) it waits; the arrows step
// and Esc closes. "See all" lays its photos out as a grid that opens the
// shared viewer (MediaViewer). The menu hides the photo, the memory, the
// day or a person from the memories - the strip's note offers Undo.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode, SyntheticEvent } from "react";
import { fmtDate, fmtDateRange, useT } from "../i18n";
import type { TFunction } from "../i18n";
import { refuseOnDemo } from "../net/demo";
import type { File as MsgFile, Memory } from "../proto/messages";
import { useBackLayer } from "./backStack";
import { lazyPart } from "./lazyPart";
import { usePeople } from "./libraryStore";
import { dayDate, fetchThumbs, hideMemory, localDay, thumbFor, useThumbVersion, type HideKind } from "./memories";
import type { Hidden } from "./MemoriesStrip";
import Part from "./Part";
import "./MemoryViewer.css";

const MediaViewer = lazyPart(() => import("./MediaViewer"));

// How long a photo shows (the progress segment's animation: MemoryViewer.css).
const cPhotoMs = 4000;
// A press held this long pauses instead of stepping.
const cHoldMs = 250;
// A photo that hasn't loaded in this long shows what there is and goes on.
const cLoadWaitMs = 8000;
// The photos fetched big ahead of the one shown.
const cAhead = 2;

/** "3 years ago · 30 Oct 2023 · Lisbon": how long ago, its days, where. */
function memoryTitle(t: TFunction, m: Memory): { when: string; days: string } {
  const when = t("web.images.memories.years_ago", { count: m.yearsAgo });
  const start = dayDate(m.startDate);
  let days: string;
  if (m.key.startsWith("month:")) days = fmtDate(t.lang, start, { month: "long", year: "numeric" });
  else if (m.key.startsWith("week:") && m.endDate && m.endDate !== m.startDate) days = fmtDateRange(t.lang, start, dayDate(m.endDate));
  else days = fmtDate(t.lang, start);
  return { when, days };
}

// "Ana", "Ana and Leo", "Ana, Leo and Rosa" (PeopleView's rule).
function nameList(t: TFunction, xs: readonly string[]): string {
  if (xs.length < 2) return xs[0] ?? "";
  if (xs.length === 2) return t("web.images.people.names_two", { first: xs[0], second: xs[1] });
  return t("web.images.people.names_more", { first: xs[0], rest: nameList(t, xs.slice(1)) });
}

function Icon({ children, size = 24 }: { children: ReactNode; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}
const CloseIcon = () => <Icon><path d="M6.5 6.5l11 11M17.5 6.5l-11 11" /></Icon>;
const MoreIcon = () => <Icon><circle cx="12" cy="5.5" r="1.2" /><circle cx="12" cy="12" r="1.2" /><circle cx="12" cy="18.5" r="1.2" /></Icon>;
const PauseIcon = () => <Icon><path d="M9 6v12M15 6v12" /></Icon>;
const PlayIcon = () => <Icon><path d="M8 5.5v13l10.5-6.5z" /></Icon>;
const BackIcon = () => <Icon><path d="M19 12H5.5M11 6l-6 6 6 6" /></Icon>;
const PrevIcon = () => <Icon><path d="m14.5 6-6 6 6 6" /></Icon>;
const NextIcon = () => <Icon><path d="m9.5 6 6 6-6 6" /></Icon>;
const GridIcon = () => <Icon size={20}><rect x="4" y="4" width="6.5" height="6.5" rx="1.2" /><rect x="13.5" y="4" width="6.5" height="6.5" rx="1.2" /><rect x="4" y="13.5" width="6.5" height="6.5" rx="1.2" /><rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.2" /></Icon>;

const markLoaded = (e: SyntheticEvent<HTMLImageElement>) => { e.currentTarget.dataset.loaded = "1"; };

export default function MemoryViewer({ memories, index, startPaused = false, onClose, onHidden, onHideFailed }: {
  memories: Memory[];
  index: number;
  /** Opened from the keyboard: waits for Play (a screen reader reads first). */
  startPaused?: boolean;
  onClose: () => void;
  /** Something was hidden: the strip's note, with Undo. */
  onHidden: (h: Hidden) => void;
  /** The device refused a hide: its words, or "" for none. */
  onHideFailed: (said: string) => void;
}) {
  const t = useT();
  useThumbVersion();
  // The memories as opened: a hide changes the strip's, not these.
  const [mems, setMems] = useState(memories);
  const [mi, setMi] = useState(Math.min(index, memories.length - 1));
  const [fi, setFi] = useState(0);
  const [paused, setPaused] = useState(startPaused);
  const [holding, setHolding] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [allOpen, setAllOpen] = useState(false);
  const [viewing, setViewing] = useState<number | null>(null);
  const [ended, setEnded] = useState(false);
  const [loadedKey, setLoadedKey] = useState("");
  const [waitedKey, setWaitedKey] = useState("");

  const memory = mems[mi];
  const files = memory?.files ?? [];
  const file: MsgFile | undefined = files[fi];
  const stepKey = `${mi}|${fi}|${file?.path ?? ""}`;

  useBackLayer(true, onClose);
  useBackLayer(allOpen, () => setAllOpen(false));

  // The memory's photos small (the grid, and the picture while the big one
  // comes), and big for the one shown and the next ones.
  const smallKey = files.map((f) => f.path).join("\n");
  useEffect(() => {
    if (smallKey) void fetchThumbs(smallKey.split("\n"), true);
  }, [smallKey]);
  const aheadKey = files.slice(fi, fi + 1 + cAhead).map((f) => f.path).join("\n");
  useEffect(() => {
    if (aheadKey) void fetchThumbs(aheadKey.split("\n"), false);
  }, [aheadKey]);

  const big = file ? thumbFor(file.path, false) : undefined;
  const small = file ? thumbFor(file.path, true) : undefined;
  // Ready to time: the big picture drawn, none to wait for, or waited long
  // enough.
  const ready = loadedKey === stepKey || big === "" || waitedKey === stepKey;
  useEffect(() => {
    if (ready) return;
    const timer = setTimeout(() => setWaitedKey(stepKey), cLoadWaitMs);
    return () => clearTimeout(timer);
  }, [ready, stepKey]);

  // What the user asked for (the button), and whether the clock runs now.
  const playing = !paused && !ended;
  const running = playing && !holding && !menuOpen && !allOpen && viewing === null && ready;

  const next = useCallback(() => {
    setEnded(false);
    if (fi < files.length - 1) setFi(fi + 1);
    else if (mi < mems.length - 1) { setMi(mi + 1); setFi(0); }
    else setEnded(true);
  }, [fi, files.length, mi, mems.length]);
  const prev = useCallback(() => {
    setEnded(false);
    if (fi > 0) setFi(fi - 1);
    else if (mi > 0) { setMi(mi - 1); setFi(0); }
  }, [fi, mi]);
  const togglePause = useCallback(() => {
    if (ended) { setEnded(false); setMi(0); setFi(0); setPaused(false); return; }
    setPaused((p) => !p);
  }, [ended]);

  // The keyboard, while nothing is over the slideshow: on the document, so
  // the keys work wherever the focus went (the note's Undo, a click on the
  // page). Tab stays inside it (onKeyDown below).
  const rootRef = useRef<HTMLDivElement>(null);
  const keys = useRef<(e: KeyboardEvent) => void>(() => {});
  useLayoutEffect(() => {
    keys.current = (e: KeyboardEvent) => {
      if (viewing !== null || e.defaultPrevented) return;
      if (e.key === "Escape") {
        e.preventDefault();
        if (menuOpen) setMenuOpen(false);
        else if (allOpen) setAllOpen(false);
        else onClose();
        return;
      }
      if (menuOpen || allOpen) return;
      if (e.key === "ArrowRight") { e.preventDefault(); next(); }
      else if (e.key === "ArrowLeft") { e.preventDefault(); prev(); }
      else if (e.key === " " && !(e.target instanceof HTMLButtonElement)) { e.preventDefault(); togglePause(); }
    };
  });
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => keys.current(e);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === "Tab" && viewing === null) trapTab(e, rootRef.current);
  };
  // The focus starts on the slideshow and goes back where it was. Closing
  // the viewer over See all, or See all, brings it back to what opened
  // them, so the keys reach the slideshow again.
  useLayoutEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    rootRef.current?.focus();
    return () => { before?.focus?.(); };
  }, []);
  const lastViewed = useRef<number | null>(null);
  useEffect(() => {
    if (viewing !== null) { lastViewed.current = viewing; return; }
    if (lastViewed.current === null) return;
    const tile = rootRef.current?.querySelectorAll<HTMLElement>(".mem-all-tile")[lastViewed.current];
    lastViewed.current = null;
    (tile ?? rootRef.current)?.focus();
  }, [viewing]);
  const wasAll = useRef(false);
  useEffect(() => {
    if (wasAll.current && !allOpen) rootRef.current?.querySelector<HTMLElement>(".mem-see-all")?.focus();
    wasAll.current = allOpen;
  }, [allOpen]);

  // A press: a tap steps (left third back, else on), held it waits.
  const press = useRef<{ timer: ReturnType<typeof setTimeout>; held: boolean; x: number } | null>(null);
  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    const p = { held: false, x: e.clientX, timer: setTimeout(() => { p.held = true; setHolding(true); }, cHoldMs) };
    press.current = p;
  };
  const endPress = (e: ReactPointerEvent<HTMLDivElement>, tap: boolean) => {
    const p = press.current;
    press.current = null;
    if (!p) return;
    clearTimeout(p.timer);
    if (p.held) { setHolding(false); return; }
    if (!tap) return;
    const r = e.currentTarget.getBoundingClientRect();
    if (p.x - r.left < r.width / 3) prev();
    else next();
  };

  // Hiding.
  const hide = async (kind: HideKind, value: string) => {
    setMenuOpen(false);
    if (refuseOnDemo()) return;
    const said = await hideMemory(kind, value);
    if (said !== null) { onHideFailed(said); return; }
    onHidden({ kind, value });
    if (kind !== "photo") { onClose(); return; }
    // The photo leaves this slideshow; the memory with it if it was its last.
    const left = files.filter((f) => f.hash !== value);
    if (left.length > 0) {
      setMems(mems.map((m, i) => (i === mi ? { ...m, files: left } : m)));
      setFi(Math.min(fi, left.length - 1));
      return;
    }
    const rest = mems.filter((_, i) => i !== mi);
    if (rest.length === 0) { onClose(); return; }
    setMems(rest);
    setMi(Math.min(mi, rest.length - 1));
    setFi(0);
  };

  const viewerItems = useMemo(
    () => files.map((f) => ({ path: f.path, mime: f.mime, hash: f.hash, copies: f.copies, thumbURL: thumbFor(f.path, true) || thumbFor(f.path, false) || undefined })),
    // thumbFor reads the module's cache; the version re-renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [files, viewing],
  );

  if (!memory || !file) return null;
  const { when, days } = memoryTitle(t, memory);
  const day = file.created ? localDay(file.created) : memory.startDate;
  const picture = big || small;

  return (
    <div
      className="mem-viewer"
      ref={rootRef}
      role="dialog"
      aria-modal="true"
      aria-label={`${when} · ${days}`}
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      <div className="mem-progress" aria-hidden="true">
        {files.map((f, i) => (
          <span key={f.path + i} className="mem-seg">
            <span
              key={i === fi ? stepKey : undefined}
              className={`mem-seg-fill${i < fi || (i === fi && ended) ? " done" : i === fi ? " now" : ""}`}
              style={i === fi ? { animationDuration: `${cPhotoMs}ms`, animationPlayState: running ? "running" : "paused" } : undefined}
              onAnimationEnd={i === fi ? next : undefined}
            />
          </span>
        ))}
      </div>

      <div className="mem-bar">
        <div className="mem-heading">
          <p className="mem-when">
            <span className="mem-when-years">{when}</span>
            <span className="mem-when-rest"> · {days}{memory.place ? ` · ${memory.place}` : ""}</span>
          </p>
          {memory.people.length > 0 && (
            <p className="mem-people">{t("web.images.memories.with", { names: nameList(t, memory.people) })}</p>
          )}
        </div>
        <div className="mem-actions">
          <button
            type="button"
            className="mem-icon-btn"
            onClick={togglePause}
            aria-label={playing ? t("common.pause") : t("common.resume")}
            data-tip={playing ? t("common.pause") : t("common.resume")}
          >
            {playing ? <PauseIcon /> : <PlayIcon />}
          </button>
          <div className="mem-menu-wrap">
            <button
              type="button"
              className="mem-icon-btn"
              onClick={() => setMenuOpen((o) => !o)}
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              aria-label={t("web.images.memories.options")}
              data-tip={t("web.images.memories.options")}
            >
              <MoreIcon />
            </button>
            {menuOpen && (
              <>
                <div className="mem-menu-catcher" onClick={() => setMenuOpen(false)} />
                <div className="mem-menu" role="menu">
                  <button type="button" role="menuitem" autoFocus onClick={() => void hide("photo", file.hash)}>
                    {t("web.images.memories.hide_photo")}
                  </button>
                  <button type="button" role="menuitem" onClick={() => void hide("memory", memory.key)}>
                    {t("web.images.memories.hide_memory")}
                  </button>
                  <button type="button" role="menuitem" onClick={() => void hide("day", day)}>
                    {t("web.images.memories.hide_day")}
                  </button>
                  {memory.people.length > 0 && <PersonItems names={memory.people} onHide={(id) => void hide("person", id)} />}
                </div>
              </>
            )}
          </div>
          <button type="button" className="mem-icon-btn" onClick={onClose} aria-label={t("common.close")} data-tip={t("common.close")}>
            <CloseIcon />
          </button>
        </div>
      </div>

      <div
        className="mem-stage"
        onPointerDown={onPointerDown}
        onPointerUp={(e) => endPress(e, true)}
        onPointerCancel={(e) => endPress(e, false)}
        onPointerLeave={(e) => endPress(e, false)}
        onContextMenu={(e) => e.preventDefault()}
      >
        {small && big !== small && <img className="mem-backdrop" src={small} alt="" draggable={false} />}
        {picture && (
          <img
            key={stepKey}
            className="mem-photo"
            src={picture}
            alt={t("web.images.memories.photo_position", { index: fi + 1, count: files.length })}
            draggable={false}
            onLoad={(e) => { markLoaded(e); if (picture === big) setLoadedKey(stepKey); }}
          />
        )}
        <button type="button" className="mem-nav prev" onClick={prev} onPointerDown={(e) => e.stopPropagation()}
          aria-label={t("web.images.viewer.previous")} disabled={mi === 0 && fi === 0}>
          <PrevIcon />
        </button>
        <button type="button" className="mem-nav next" onClick={next} onPointerDown={(e) => e.stopPropagation()}
          aria-label={t("web.images.viewer.next")}>
          <NextIcon />
        </button>
      </div>

      <div className="mem-foot">
        <button type="button" className="mem-see-all" onClick={() => setAllOpen(true)}>
          <GridIcon /> {t("web.images.memories.see_all")}
        </button>
      </div>

      {allOpen && (
        <div className="mem-all">
          <div className="mem-all-head">
            <button type="button" className="mem-icon-btn" onClick={() => setAllOpen(false)} aria-label={t("common.back")} data-tip={t("common.back")}>
              <BackIcon />
            </button>
            <p className="mem-when">
              <span className="mem-when-years">{when}</span>
              <span className="mem-when-rest"> · {days}{memory.place ? ` · ${memory.place}` : ""}</span>
            </p>
          </div>
          <ul className="mem-all-grid">
            {files.map((f, i) => {
              const src = thumbFor(f.path, true) || thumbFor(f.path, false);
              return (
                <li key={f.path + i}>
                  <button
                    type="button"
                    className="mem-all-tile"
                    onClick={() => setViewing(i)}
                    aria-label={t("web.images.memories.photo_position", { index: i + 1, count: files.length })}
                  >
                    {src && <img src={src} alt="" draggable={false} loading="lazy" onLoad={markLoaded} />}
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {viewing !== null && viewing < files.length && (
        <Part kind="layer" onClose={() => setViewing(null)}>
          <MediaViewer
            items={viewerItems}
            index={viewing}
            onIndexChange={setViewing}
            onClose={() => setViewing(null)}
          />
        </Part>
      )}
    </div>
  );
}

// "Don't show <name>" for the named people in the memory. The device says
// their names, so a name is offered only when exactly one person in People
// has it (as the phones do): two people of one name could be the wrong one.
function PersonItems({ names, onHide }: { names: readonly string[]; onHide: (id: string) => void }) {
  const t = useT();
  const people = usePeople();
  const fold = (s: string) => s.replace(/\s+/g, " ").trim().toLocaleLowerCase();
  return (
    <>
      {names.map((name) => {
        const same = people.items.filter((x) => fold(x.name) === fold(name));
        if (same.length !== 1) return null;
        const p = same[0];
        return (
          <button key={p.id} type="button" role="menuitem" onClick={() => onHide(p.id)}>
            {t("web.images.memories.hide_person", { name })}
          </button>
        );
      })}
    </>
  );
}

// Tab and Shift+Tab stay inside the slideshow.
function trapTab(e: ReactKeyboardEvent, root: HTMLElement | null) {
  if (!root) return;
  const stops = Array.from(root.querySelectorAll<HTMLElement>("button:not([disabled]), [href], [tabindex]:not([tabindex='-1'])"))
    .filter((el) => el.offsetParent !== null);
  if (stops.length === 0) return;
  const first = stops[0];
  const last = stops[stops.length - 1];
  if (e.shiftKey && (document.activeElement === first || document.activeElement === root)) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
}
