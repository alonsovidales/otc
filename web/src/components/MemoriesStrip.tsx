// SPDX-License-Identifier: AGPL-3.0-or-later

// On this day at the top of Images (memories.ts has the rules): the period's
// title, the Day / Week / Month choice (a period with nothing greyed out)
// and one card per earlier year - its cover, how long ago, where. A card
// opens the slideshow (MemoryViewer.tsx, loaded when first opened). Nothing
// shows until the device has memories in some period, nor while it is off
// or the device predates it; what the slideshow hides comes back with Undo
// in a note at the foot of the page.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { SyntheticEvent } from "react";
import { useT } from "../i18n";
import type { Memory } from "../proto/messages";
import { lazyPart } from "./lazyPart";
import {
  cPeriods,
  choosePeriod,
  fetchThumbs,
  hideMemory,
  isEmpty,
  isMeasuring,
  loadMemories,
  localDay,
  memoriesOf,
  refreshMemories,
  thumbFor,
  useMemories,
  useThumbVersion,
  type HideKind,
  type Period,
} from "./memories";
import Part from "./Part";
import ToastView from "./ToastView";
import { asIs, say, type PlainKey } from "./uiText";
import { useToasts } from "./useToasts";
import "./MemoriesStrip.css";

const MemoryViewer = lazyPart(() => import("./MemoryViewer"));

const cTitle: Record<Period, PlainKey> = {
  day: "web.images.memories.title_day",
  week: "web.images.memories.title_week",
  month: "web.images.memories.title_month",
};
const cChoice: Record<Period, PlainKey> = {
  day: "web.images.memories.day",
  week: "web.images.memories.week",
  month: "web.images.memories.month",
};
// While the device is still measuring the photos it held before, the
// choice is asked again this often.
const cMeasuringMs = 2 * 60_000;

/** What the slideshow hid, for its Undo. */
export type Hidden = { kind: HideKind; value: string };

const markLoaded = (e: SyntheticEvent<HTMLImageElement>) => { e.currentTarget.dataset.loaded = "1"; };

export default function MemoriesStrip() {
  const t = useT();
  const s = useMemories();
  useThumbVersion();
  const { toast, showToast, dismissToast } = useToasts();
  // The slideshow: its memories as opened, and paused when opened from the
  // keyboard (a screen reader's way in), as the phones start it paused for
  // VoiceOver and TalkBack.
  const [open, setOpen] = useState<{ memories: Memory[]; index: number; paused: boolean } | null>(null);

  // Asked when shown, and again on a new day: the page left open past
  // midnight, or a tab brought back the next morning.
  useEffect(() => {
    void loadMemories();
    let timer: ReturnType<typeof setTimeout>;
    const atMidnight = () => {
      const now = new Date();
      const next = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 0, 0, 5);
      timer = setTimeout(() => { void loadMemories(); atMidnight(); }, next.getTime() - now.getTime());
    };
    atMidnight();
    const onShow = () => { if (document.visibilityState === "visible" && localDay() !== s.date) void loadMemories(); };
    document.addEventListener("visibilitychange", onShow);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onShow);
    };
  }, [s.date]);

  // Still measuring: asked again every 2 minutes until it says it is done.
  const measuring = isMeasuring(s);
  useEffect(() => {
    if (!measuring) return;
    const timer = setInterval(() => void refreshMemories(), cMeasuringMs);
    return () => clearInterval(timer);
  }, [measuring]);

  const period = s.period;
  const memories = memoriesOf(s, period);
  const shown = s.status === "on" && period !== null && memories.length > 0;

  // The covers, small, once the period's memories are known.
  const coverKey = memories.map((m) => m.cover?.path ?? "").filter(Boolean).join("\n");
  useEffect(() => {
    if (coverKey) void fetchThumbs(coverKey.split("\n"), true);
  }, [coverKey]);

  // Coming in above a grid already scrolled (the device answered late), the
  // strip pushes nothing on screen out of place: the page scrolls by its
  // height.
  const rootRef = useRef<HTMLElement>(null);
  const wasShown = useRef(false);
  useLayoutEffect(() => {
    if (shown && !wasShown.current && rootRef.current && window.scrollY > 0) {
      window.scrollBy(0, rootRef.current.getBoundingClientRect().height);
    }
    wasShown.current = shown;
  }, [shown]);

  const refused = useCallback((said: string) => {
    showToast({ text: said ? asIs(said) : say("web.images.memories.hide_failed"), error: true });
  }, [showToast]);
  const onHidden = useCallback((h: Hidden) => {
    showToast({
      text: say("web.images.memories.hidden"),
      action: {
        label: say("web.images.memories.undo"),
        run: () => { void hideMemory(h.kind, h.value, true).then((said) => { if (said !== null) refused(said); }); },
      },
    });
  }, [showToast, refused]);

  return (
    <>
      {shown && (
        <section className="mem-strip" ref={rootRef} aria-labelledby="mem-strip-title">
          <div className="mem-head">
            <h2 className="mem-title" id="mem-strip-title">{t(cTitle[period])}</h2>
            <div className="mem-periods" role="group" aria-label={t("web.images.memories.period")}>
              {cPeriods.map((p) => (
                <button
                  key={p}
                  type="button"
                  className="mem-period"
                  aria-pressed={p === period}
                  disabled={isEmpty(s, p)}
                  onClick={() => choosePeriod(p)}
                >
                  {t(cChoice[p])}
                </button>
              ))}
            </div>
          </div>
          <ul className="mem-cards">
            {memories.map((m, i) => {
              const cover = m.cover ? thumbFor(m.cover.path, true) : undefined;
              return (
                <li key={m.key}>
                  <button
                    type="button"
                    className="mem-card"
                    // detail 0: pressed with the keyboard, not clicked.
                    onClick={(e) => setOpen({ memories, index: i, paused: e.detail === 0 })}
                  >
                    {cover && <img src={cover} alt="" draggable={false} onLoad={markLoaded} />}
                    <span className="mem-card-text">
                      <span className="mem-card-when">{t("web.images.memories.years_ago", { count: m.yearsAgo })}</span>
                      {m.place && <span className="mem-card-place">{m.place}</span>}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      )}
      {/* Outside the strip: hiding a photo keeps the slideshow open even
          when the strip has nothing left to show. */}
      {open && (
        <Part kind="layer" onClose={() => setOpen(null)}>
          <MemoryViewer
            memories={open.memories}
            index={open.index}
            startPaused={open.paused}
            onClose={() => setOpen(null)}
            onHidden={onHidden}
            onHideFailed={refused}
          />
        </Part>
      )}
      <ToastView toast={toast} onDismiss={dismissToast} />
    </>
  );
}
