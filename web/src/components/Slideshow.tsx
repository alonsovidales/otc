// SPDX-License-Identifier: AGPL-3.0-or-later

// A slideshow of a collection's photos, or of the whole library's
// (slideshowState.ts opens it; the wall screen is what it is for): full
// screen, black, a photo every 5 s in a random order (slideshowRules.ts: the
// device's rounds), each crossfading into the next while it moves slowly
// (Ken Burns), shown whole over a blurred copy of itself that fills the
// rest of the screen. Videos are left out; it goes on for ever.
//
// A photo goes on screen only once it is loaded and decoded
// (slideshowLoad.ts): the next one is loaded while one shows, and when
// its 5 s are up before it is ready the photo on screen stays until it
// is. One that can't be shown is skipped. The photo before, the one on
// screen and the next are kept; the rest are let go. The device away
// (no answer, the bridge's) keeps the show on what it has, asking again
// as it comes back - App keeps its unreachable screen off it meanwhile.
//
// The controls (the place in the round, Close, Previous, Play/Pause,
// Next) hide while it plays and come back for 3 s with a tap, a click or
// the mouse moving, and stay while it is paused. Keys: Space plays or
// pauses, the arrows step, Esc closes - as does leaving full screen, and
// Back. A swipe steps too. The screen is kept awake (Wake Lock) while it
// shows.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, PointerEvent as ReactPointerEvent } from "react";
import { createPortal } from "react-dom";
import { useT } from "../i18n";
import { subscribeDeviceStatus } from "../net/deviceStatus";
import { useBackLayer } from "./backStack";
import { keepTabInside } from "./focusTrap";
import {
  cFadeMs, cMoveMs, cPageSize, cPhotoMs, createRounds, kenBurnsOf, swipeOf,
  type FetchPage, type Rounds, type ShowEntry,
} from "./slideshowRules";
import { askDevice, loadSlide, SlideAway, SlideGone, type Slide } from "./slideshowLoad";
import type { SlideshowOpen } from "./slideshowState";
import Spinner from "./Spinner";
import "./Slideshow.css";

// How long the controls stay after a tap, a click or the mouse.
const cChromeMs = 3000;
// The waits after a photo or a page that got no answer.
const cAwayFirstMs = 2000;
const cAwayMaxMs = 30_000;

type Status = "starting" | "showing" | "empty" | "away" | "failed";

interface Layer { entry: ShowEntry; slide: Slide; n: number }

const Icon = ({ d, filled = false, size = 30 }: { d: string; filled?: boolean; size?: number }) => (
  <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" focusable="false">
    <path d={d} fill={filled ? "currentColor" : "none"} stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const cPlay = "M8 5.5v13l10.5-6.5Z"; // i18n-ignore: SVG path data
const cPause = "M8.5 5.5v13m7-13v13"; // i18n-ignore: SVG path data
const cPrev = "m14.5 5.5-6.5 6.5 6.5 6.5"; // i18n-ignore: SVG path data
const cNext = "m9.5 5.5 6.5 6.5-6.5 6.5"; // i18n-ignore: SVG path data
const cClose = "M6.5 6.5l11 11M17.5 6.5l-11 11"; // i18n-ignore: SVG path data

export default function Slideshow({ show, onClose }: { show: SlideshowOpen; onClose: () => void }) {
  const t = useT();
  const root = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<Status>("starting");
  const [layers, setLayers] = useState<Layer[]>([]);
  const [paused, setPaused] = useState(false);
  const [chrome, setChrome] = useState(true);
  const [busy, setBusy] = useState(false);

  // The show as it went: every photo taken, in order, and the one on
  // screen (pos). Slides are kept for pos - 1 .. pos + 1 (and what is
  // still fading out).
  const entries = useRef<ShowEntry[]>([]);
  const pos = useRef(-1);
  const slides = useRef(new Map<ShowEntry, Slide>());
  const loading = useRef(new Map<ShowEntry, Promise<Slide | null>>());
  const rounds = useRef<Rounds | null>(null);
  // The photo asked for (the timer's next, a press), shown once ready.
  const wanted = useRef<number | null>(null);
  // The timer of the photo on screen, and how long it has shown.
  const timer = useRef<number | null>(null);
  const shownAt = useRef(0);
  const shownFor = useRef(0);
  const pausedRef = useRef(false);
  const layerN = useRef(0);
  const closed = useRef(false);
  const awayWait = useRef(cAwayFirstMs);
  const awayTimer = useRef<number | null>(null);
  const retry = useRef<() => void>(() => {});

  const fetchPage = useCallback<FetchPage>(async (seed, after) => {
    const resp = await askDevice((e) => {
      e.payload = {
        $case: "reqRandomPhotos",
        reqRandomPhotos: { groupId: show.groupId, seed, after, limit: cPageSize, smallThumbnails: true },
      };
    });
    if (resp.payload?.$case !== "respListOfFiles") throw new Error(resp.errorCode || "failed");
    const lof = resp.payload.respListOfFiles;
    return { files: lof.files ?? [], token: lof.token, total: lof.randomTotal };
  }, [show.groupId]);

  if (!rounds.current) rounds.current = createRounds(fetchPage);

  // Back closes it, as its × does.
  useBackLayer(true, onClose);

  const box = () => ({ w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio || 1 });

  // Lets go of every slide but those near pos and those on screen.
  const prune = useCallback((onScreen: Layer[]) => {
    const keep = new Set<ShowEntry>();
    for (let i = pos.current - 1; i <= pos.current + 1; i++) {
      const e = entries.current[i];
      if (e) keep.add(e);
    }
    for (const l of onScreen) keep.add(l.entry);
    for (const [e, s] of slides.current) {
      if (!keep.has(e)) {
        s.release();
        slides.current.delete(e);
      }
    }
  }, []);

  const clearTimer = () => {
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = null;
  };

  // The device away: asked again after a pause (2 s doubling to 30 s), or
  // at once when it answers again or the browser is back online.
  const waitForDevice = useCallback(() => {
    if (awayTimer.current !== null || closed.current) return;
    const wait = awayWait.current;
    awayWait.current = Math.min(cAwayMaxMs, wait * 2);
    awayTimer.current = window.setTimeout(() => {
      awayTimer.current = null;
      retry.current();
    }, wait);
  }, []);
  useEffect(() => {
    const back = () => {
      if (awayTimer.current === null) return;
      window.clearTimeout(awayTimer.current);
      awayTimer.current = null;
      retry.current();
    };
    const off = subscribeDeviceStatus((s) => { if (s === null) back(); });
    window.addEventListener("online", back);
    return () => { off(); window.removeEventListener("online", back); };
  }, []);

  // The entry at i, taking the next photo of the show when i is past the
  // end; null when there is nothing to show.
  const entryAt = useCallback(async (i: number): Promise<ShowEntry | null> => {
    while (entries.current.length <= i) {
      const next = await rounds.current!.take();
      if (!next) return null;
      entries.current.push(next);
    }
    rounds.current!.prefetch();
    return entries.current[i];
  }, []);

  // e's slide, loaded once; null when it can't be shown (taken out of the
  // show). Rejects with SlideAway.
  const slideOf = useCallback((e: ShowEntry): Promise<Slide | null> => {
    const kept = slides.current.get(e);
    if (kept) return Promise.resolve(kept);
    let p = loading.current.get(e);
    if (!p) {
      const b = box();
      p = loadSlide(e.file, b.w, b.h, b.dpr)
        .then((s) => {
          if (closed.current) { s.release(); return null; }
          slides.current.set(e, s);
          return s;
        })
        .catch((err: unknown) => {
          if (err instanceof SlideGone) return null;
          throw err;
        })
        .finally(() => { loading.current.delete(e); });
      loading.current.set(e, p);
    }
    return p;
  }, []);

  const schedule = useCallback(() => {
    clearTimer();
    if (pausedRef.current || closed.current) return;
    const left = Math.max(0, cPhotoMs - shownFor.current - (performance.now() - shownAt.current));
    timer.current = window.setTimeout(() => {
      timer.current = null;
      void goRef.current(pos.current + 1);
    }, left);
  }, []);

  // Puts entry i on screen (its slide is ready).
  const showAt = useCallback((i: number, slide: Slide) => {
    const entry = entries.current[i];
    pos.current = i;
    wanted.current = null;
    shownAt.current = performance.now();
    shownFor.current = 0;
    awayWait.current = cAwayFirstMs;
    setStatus("showing");
    setLayers((was) => {
      const next = [...was.slice(-1), { entry, slide, n: ++layerN.current }];
      prune(next);
      return next;
    });
    schedule();
    // The next one, ready before its turn; asked again once the device is
    // back when nothing came.
    const ahead = () => {
      const at = pos.current + 1;
      void entryAt(at).then((e) => (e ? slideOf(e) : null)).catch(() => {
        if (pos.current + 1 !== at || wanted.current !== null) return;
        retry.current = ahead;
        waitForDevice();
      });
    };
    ahead();
  }, [entryAt, prune, schedule, slideOf, waitForDevice]);

  // Goes to entry i: at once when it is ready, else once it is (the photo
  // on screen staying meanwhile). One that can't be shown is skipped.
  const go = useCallback(async (i: number) => {
    if (i < 0 || closed.current) return;
    wanted.current = i;
    setBusy(true);
    try {
      for (;;) {
        const entry = await entryAt(i);
        if (wanted.current !== i || closed.current) return;
        if (!entry) {
          if (pos.current < 0) setStatus("empty");
          return;
        }
        const slide = await slideOf(entry);
        if (wanted.current !== i || closed.current) return;
        if (slide) {
          showAt(i, slide);
          return;
        }
        // Skipped: the show goes on without it.
        entries.current.splice(i, 1);
        if (i <= pos.current) pos.current--;
      }
    } catch (err) {
      if (wanted.current !== i || closed.current) return;
      if (pos.current < 0) setStatus(err instanceof SlideAway ? "away" : "failed");
      retry.current = () => { void goRef.current(i); };
      waitForDevice();
    } finally {
      if (wanted.current === i || wanted.current === null) setBusy(false);
    }
  }, [entryAt, showAt, slideOf, waitForDevice]);
  const goRef = useRef(go);
  goRef.current = go;

  // The first photo, once.
  useEffect(() => {
    void go(0);
  }, [go]);

  // Everything let go as it closes.
  useEffect(() => () => {
    closed.current = true;
    clearTimer();
    if (awayTimer.current !== null) window.clearTimeout(awayTimer.current);
    for (const s of slides.current.values()) s.release();
    slides.current.clear();
  }, []);

  const togglePause = useCallback(() => {
    setPaused((was) => {
      const now = !was;
      pausedRef.current = now;
      if (now) {
        shownFor.current += performance.now() - shownAt.current;
        clearTimer();
      } else {
        shownAt.current = performance.now();
        if (pos.current >= 0) schedule();
      }
      return now;
    });
  }, [schedule]);

  const step = useCallback((by: 1 | -1) => {
    const base = wanted.current ?? pos.current;
    void go(Math.max(0, base + by));
  }, [go]);

  // The controls: back for 3 s at any sign of someone there; always while
  // paused.
  const chromeTimer = useRef<number | null>(null);
  const wake = useCallback(() => {
    setChrome(true);
    if (chromeTimer.current !== null) window.clearTimeout(chromeTimer.current);
    chromeTimer.current = window.setTimeout(() => {
      chromeTimer.current = null;
      setChrome(false);
    }, cChromeMs);
  }, []);
  useEffect(() => {
    wake();
    return () => { if (chromeTimer.current !== null) window.clearTimeout(chromeTimer.current); };
  }, [wake]);
  const chromeShown = chrome || paused || status !== "showing";

  // Keys, before the page under it sees them.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); onClose(); return; }
      if (e.key === "Tab" && root.current) { keepTabInside(root.current, e); wake(); return; }
      if (e.altKey || e.ctrlKey || e.metaKey) return;
      if (e.key === " " || e.key === "Spacebar") {
        // Space on a focused button presses it instead.
        if (e.target instanceof HTMLButtonElement) return;
        e.preventDefault(); e.stopPropagation(); togglePause(); wake(); return;
      }
      if (e.key === "ArrowRight") { e.preventDefault(); e.stopPropagation(); step(1); wake(); return; }
      if (e.key === "ArrowLeft") { e.preventDefault(); e.stopPropagation(); step(-1); wake(); return; }
      wake();
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [onClose, step, togglePause, wake]);

  // Full screen where the browser allows it (the kiosk already is), and
  // leaving it closes the show. The focus goes back where it was.
  useLayoutEffect(() => {
    const el = root.current;
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    el?.focus({ preventScroll: true });
    let entered = false;
    const onChange = () => {
      if (document.fullscreenElement === el) entered = true;
      else if (entered) onClose();
    };
    document.addEventListener("fullscreenchange", onChange);
    if (el && typeof el.requestFullscreen === "function" && !document.fullscreenElement) {
      el.requestFullscreen({ navigationUI: "hide" }).catch(() => { /* not allowed: the page is the screen */ });
    }
    return () => {
      document.removeEventListener("fullscreenchange", onChange);
      if (document.fullscreenElement === el) void document.exitFullscreen().catch(() => {});
      before?.focus({ preventScroll: true });
    };
  }, [onClose]);

  // The screen kept on, again each time the tab shows (the lock goes with
  // a hidden tab).
  useEffect(() => {
    type Lock = { released: boolean; release(): Promise<void> };
    const nav = navigator as Navigator & { wakeLock?: { request(kind: "screen"): Promise<Lock> } };
    let lock: Lock | null = null;
    let live = true;
    const ask = () => {
      if (!nav.wakeLock || document.visibilityState !== "visible" || (lock && !lock.released)) return;
      nav.wakeLock.request("screen").then((l) => {
        if (live) lock = l;
        else void l.release().catch(() => {});
      }).catch(() => { /* refused: the screen does as it does */ });
    };
    ask();
    document.addEventListener("visibilitychange", ask);
    return () => {
      live = false;
      document.removeEventListener("visibilitychange", ask);
      void lock?.release().catch(() => {});
    };
  }, []);

  // The wheel never scrolls the page under it.
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const stop = (e: WheelEvent) => e.preventDefault();
    el.addEventListener("wheel", stop, { passive: false });
    return () => el.removeEventListener("wheel", stop);
  }, []);

  // A tap shows the controls; a swipe steps.
  const press = useRef<{ x: number; y: number; at: number; id: number } | null>(null);
  const lastMove = useRef<{ x: number; y: number } | null>(null);
  const onPointerDown = (e: ReactPointerEvent) => {
    press.current = { x: e.clientX, y: e.clientY, at: performance.now(), id: e.pointerId };
  };
  const onPointerUp = (e: ReactPointerEvent) => {
    const p = press.current;
    press.current = null;
    if (!p || p.id !== e.pointerId) return;
    const swipe = e.pointerType !== "mouse" ? swipeOf(e.clientX - p.x, e.clientY - p.y, performance.now() - p.at) : null;
    if (swipe) step(swipe === "next" ? 1 : -1);
    wake();
  };
  const onPointerMove = (e: ReactPointerEvent) => {
    if (e.pointerType !== "mouse") return;
    const was = lastMove.current;
    lastMove.current = { x: e.clientX, y: e.clientY };
    if (!was || Math.abs(was.x - e.clientX) + Math.abs(was.y - e.clientY) > 4) wake();
  };

  const current = layers.length ? layers[layers.length - 1].entry : null;
  const label = show.groupId
    ? t("web.images.slideshow.label_collection", { name: show.name })
    : t("web.images.slideshow.label_library");
  const position = current
    ? current.total > 0
      ? t("web.images.slideshow.position", { index: current.index, count: Math.max(current.total, current.index) })
      : t("web.images.slideshow.position_unknown", { index: current.index })
    : "";

  return createPortal(
    <div
      ref={root}
      className={`ss${chromeShown ? " chrome-on" : ""}${paused ? " is-paused" : ""}`}
      role="dialog"
      aria-modal="true"
      aria-label={label}
      tabIndex={-1}
      data-pos={current ? entries.current.indexOf(current) : -1}
      data-round={current?.round ?? 0}
      onPointerDown={onPointerDown}
      onPointerUp={onPointerUp}
      onPointerMove={onPointerMove}
      onContextMenu={(e) => e.preventDefault()}
    >
      {layers.map((l, i) => {
        const kb = kenBurnsOf(l.n);
        const move = {
          "--kb-s0": String(kb.from.s), "--kb-x0": `${kb.from.x}%`, "--kb-y0": `${kb.from.y}%`,
          "--kb-s1": String(kb.to.s), "--kb-x1": `${kb.to.x}%`, "--kb-y1": `${kb.to.y}%`,
          "--kb-ms": `${cMoveMs}ms`, "--ss-fade-ms": `${cFadeMs}ms`,
        } as CSSProperties;
        const last = i === layers.length - 1;
        return (
          <div
            key={l.n}
            className={`ss-layer${last ? " is-in" : ""}`}
            style={move}
            aria-hidden={last ? undefined : true}
            onAnimationEnd={(e) => {
              if (!last || e.target !== e.currentTarget) return;
              // Faded in: what it covered goes.
              setLayers((was) => {
                const keep = was.slice(-1);
                prune(keep);
                return keep;
              });
            }}
          >
            <img className={`ss-back${l.slide.blurred ? "" : " needs-blur"}`} src={l.slide.backdrop} alt="" draggable={false} />
            <img
              className="ss-photo"
              src={l.slide.photo}
              alt={last ? t("web.images.slideshow.photo", { index: l.entry.index, count: Math.max(l.entry.total, l.entry.index) }) : ""}
              draggable={false}
            />
          </div>
        );
      })}

      {status !== "showing" && (
        <div className="ss-state" role="status">
          {(status === "starting" || status === "away") && <Spinner />}
          <p>
            {status === "starting" && t("web.images.slideshow.loading")}
            {status === "away" && t("web.images.slideshow.waiting")}
            {status === "empty" && t("web.images.slideshow.empty")}
            {status === "failed" && t("web.images.slideshow.failed")}
          </p>
          {status === "failed" && (
            <button type="button" className="ss-text-btn" onClick={() => { setStatus("starting"); void go(0); }}>
              {t("common.try_again")}
            </button>
          )}
        </div>
      )}

      <div className="ss-chrome" aria-hidden={chromeShown ? undefined : true}>
        <div className="ss-top">
          <span className="ss-pos">{position}</span>
          <button type="button" className="ss-btn ss-close" onClick={onClose} aria-label={t("web.images.slideshow.close")} tabIndex={chromeShown ? 0 : -1}>
            <Icon d={cClose} />
          </button>
        </div>
        {status === "showing" && (
          <div className="ss-bottom">
            <button type="button" className="ss-btn" onClick={() => { step(-1); wake(); }} aria-label={t("web.images.slideshow.previous")}
              disabled={!current || entries.current.indexOf(current) <= 0} tabIndex={chromeShown ? 0 : -1}>
              <Icon d={cPrev} />
            </button>
            <button type="button" className="ss-btn ss-play" onClick={() => { togglePause(); wake(); }}
              aria-label={paused ? t("web.images.slideshow.play") : t("web.images.slideshow.pause")} tabIndex={chromeShown ? 0 : -1}>
              <Icon d={paused ? cPlay : cPause} filled={paused} size={34} />
            </button>
            <button type="button" className="ss-btn" onClick={() => { step(1); wake(); }} aria-label={t("web.images.slideshow.next")}
              tabIndex={chromeShown ? 0 : -1}>
              {busy && wanted.current !== null && wanted.current > pos.current ? <Spinner /> : <Icon d={cNext} />}
            </button>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
