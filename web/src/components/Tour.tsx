// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour (rules in tourSteps.ts; App.tsx opens it, loaded as a
// part of its own - lazyPart.ts - with its drawings, TourArt.tsx): a card
// over the dimmed page, one skill at a time - searching, jumping to a
// date, selecting, the viewer, people, collections, Files' views and
// sharing, liking and posting, adding friends - each with a small moving
// drawing of the real controls (their own labels, the theme's colours),
// so it teaches even an empty library. Each step shows its section (App
// moves there, onSection) and, where the step's control is on screen,
// the dimmed layer leaves a hole with an ember ring around it, the card
// beside it (a sheet at the foot on a phone, the control scrolled into
// the room above it). A click in a hole ends the tour where it is and goes
// on to that control; Skip, Done, Esc, Back or the dimmed layer end it
// and App shows the section it opened over. A native modal <dialog>: the
// page behind is inert, and the card draws above the top bar, banners,
// notices and tooltips. It sends nothing and opens nothing of its own.
// App keeps its step, so it comes back where it was after the
// device-unreachable screen.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Trans, useT } from "../i18n";
import type { TabKey } from "./nav";
import { useBackLayer } from "./backStack";
import TourArt from "./TourArt";
import { useReducedMotion } from "./tourMotion";
import {
  boxesOverlap, cCardMargin, cStepSection, cStepTargets, dimPath, holeRadius, holesFor, insideHole, placeCard, roundedBoxPath, scrollToShow,
  stepAfter, stepBefore, stepPlace, tourSteps, type Box, type CardPlace, type Hole, type TourStepId,
} from "./tourSteps";
import "./Tour.css";

type Props = {
  // Where the tour stands (App keeps it).
  step: TourStepId;
  // Face recognition on: the People step shows, and the search's text names people.
  faces: boolean | null;
  onStep: (step: TourStepId) => void;
  // The section the step shows; null: the one the tour opened over.
  onSection: (tab: TabKey | null) => void;
  // The tour ends: inside the event that ended it, so what App does then
  // counts as the person's doing. stay: a click went on to a control in a
  // hole, so the section shown stays; else App goes back to the one the
  // tour opened over.
  onClose: (stay: boolean) => void;
};

// A press that begins this soon after the tour opened answers nothing on
// the dimmed layer: the second half of the double click or double tap
// that opened it. The keyboard always counts.
const cArmMs = 450;
// What the keyboard goes through: the card's controls.
const cFocusable = "button, [href], input, textarea, select, [tabindex]:not([tabindex='-1'])";
// What a click in a hole goes on to: the control under the pointer.
const cControl = "button, a[href], input, select, textarea, label, summary, [role='button'], [role='menuitem']";
// Below this width the card is a sheet at the foot of the window (Tour.css).
const cSheetQuery = "(max-width: 599px)";
// How long after a step's control is first found the page is scrolled to
// it: the section's own "start at the top" (App) and its first layout
// come first.
const cScrollSettleMs = 160;
// Room kept around a control scrolled into view.
const cBandMargin = 12;

type TitleKey = `web.tour.${TourStepId}.title`;

// A label quoted in a step's text, from that label's own key, in bold.
const bold = (s: string) => <strong>{s}</strong>;

function StepText({ id, faces }: { id: TourStepId; faces: boolean | null }) {
  const t = useT();
  const b = { b: bold };
  switch (id) {
    case "welcome":
      return <p>{t("web.tour.welcome.text")}</p>;
    case "search":
      return <p>{faces ? t("web.tour.search.text_faces") : t("web.tour.search.text")}</p>;
    case "dates":
      return <p>{t("web.tour.dates.text")}</p>;
    case "select":
      return (
        <p>
          <Trans
            k="web.tour.select.text"
            args={{ post: t("web.images.select.post"), collect: t("web.images.select.collect"), share: t("common.share"), remove: t("common.delete") }}
            parts={b}
          />
        </p>
      );
    case "viewer":
      return <p><Trans k="web.tour.viewer.text" args={{ info: t("web.images.viewer.info") }} parts={b} /></p>;
    case "people":
      return (
        <p>
          <Trans k="web.tour.people.text" args={{ name: t("web.images.people.add_name"), merge: t("web.images.people.merge") }} parts={b} />
        </p>
      );
    case "collections":
      return <p><Trans k="web.tour.collections.text" args={{ add: t("web.images.select.collect") }} parts={b} /></p>;
    case "files":
      return (
        <p>
          <Trans
            k="web.tour.files.text"
            args={{
              list: t("web.files.toolbar.list"), grid: t("web.files.toolbar.grid"), storage: t("web.files.toolbar.storage"),
              sort: t("web.files.sort.label"),
            }}
            parts={b}
          />
        </p>
      );
    case "upload":
      return (
        <p>
          <Trans k="web.tour.upload.text" args={{ share: t("common.share"), zip: t("web.files.selection.download_zip") }} parts={b} />
        </p>
      );
    case "social":
      return <p>{t("web.tour.social.text")}</p>;
    case "post":
      return (
        <p>
          <Trans
            k="web.tour.post.text"
            args={{
              newPost: t("web.shell.top.new_post"), library: t("web.social.composer.library"),
              computer: t("web.social.composer.this_computer"), publish: t("web.social.composer.publish"),
            }}
            parts={b}
          />
        </p>
      );
    case "friends":
      return (
        <p>
          <Trans
            k="web.tour.friends.text"
            args={{
              address: t("web.social.friends.your_address"), add: t("web.social.friends.add_title"), send: t("web.social.friends.send"),
              waiting: t("web.social.friends.waiting_title"), accept: t("common.accept"),
            }}
            parts={b}
          />
        </p>
      );
    case "alerts":
      return (
        <p>
          <Trans
            k="web.tour.alerts.text"
            args={{ alerts: t("web.shell.menu.alerts"), settings: t("web.shell.menu.settings"), show: t("web.tour.show") }}
            parts={b}
          />
        </p>
      );
  }
}

// Whether a control is drawn round (the account picture, a pill): its
// ring is round too.
function isRound(el: Element): boolean {
  const r = el.getBoundingClientRect();
  const v = getComputedStyle(el).borderTopLeftRadius;
  const px = Number.parseFloat(v);
  if (!Number.isFinite(px)) return false;
  return v.endsWith("%") ? px >= 50 : px >= Math.min(r.width, r.height) / 2 - 1;
}

const shownOnScreen = (el: Element) => el.getClientRects().length > 0 && getComputedStyle(el).visibility !== "hidden";

// The first element a selector matches that shows.
function firstShown(sel: string): Element | null {
  for (const el of document.querySelectorAll(sel)) if (shownOnScreen(el)) return el;
  return null;
}

// The step's controls on screen now: the first group of its targets
// that finds one (tourSteps.cStepTargets), in the group's order.
function findTargets(step: TourStepId): Element[] {
  for (const group of cStepTargets[step]) {
    const found = group.map(firstShown).filter((el): el is Element => !!el);
    if (found.length) return found;
  }
  return [];
}

const boxOf = (el: Element): Box => {
  const r = el.getBoundingClientRect();
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
};

const sameHoles = (a: readonly Hole[], b: readonly Hole[]) =>
  a.length === b.length && a.every((h, i) => {
    const g = b[i];
    return h.radius === g.radius && h.box.left === g.box.left && h.box.top === g.box.top && h.box.right === g.box.right && h.box.bottom === g.box.bottom;
  });

// The second click of a double click (or double tap) that ended the tour
// would land on whatever was under the dimmed layer: for a moment it goes
// nowhere and its press takes no focus. As in AccountMenu and PeopleView.
function swallowSecondClick(ms = 500) {
  const until = performance.now() + ms;
  const onClick = (e: MouseEvent) => {
    if (e.detail < 2 || performance.now() > until) return;
    e.preventDefault();
    e.stopPropagation();
  };
  const onPress = (e: MouseEvent) => {
    if (e.detail >= 2 && performance.now() <= until) e.preventDefault();
  };
  window.addEventListener("click", onClick, true);
  window.addEventListener("mousedown", onPress, true);
  window.setTimeout(() => {
    window.removeEventListener("click", onClick, true);
    window.removeEventListener("mousedown", onPress, true);
  }, ms);
}

// Tab and Shift+Tab from the card's last control to its first and back,
// and into the card from wherever the focus fell (as ConfirmDialog's Modal).
function keepTabInside(box: HTMLElement, e: KeyboardEvent) {
  const all = [...box.querySelectorAll<HTMLElement>(cFocusable)]
    .filter((el) => !(el as HTMLButtonElement).disabled && el.getClientRects().length > 0);
  if (!all.length) return;
  const first = all[0];
  const last = all[all.length - 1];
  const active = document.activeElement;
  if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
  else if (e.shiftKey && active === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
}

// A media query's answer, kept up to date.
function useMedia(query: string): boolean {
  const [on, setOn] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const change = () => setOn(mq.matches);
    mq.addEventListener("change", change);
    return () => mq.removeEventListener("change", change);
  }, [query]);
  return on;
}

export default function Tour({ step: wanted, faces, onStep, onSection, onClose }: Props) {
  const t = useT();
  const steps = useMemo(() => tourSteps(faces), [faces]);
  const step = stepPlace(wanted, steps);
  const index = steps.indexOf(step);
  const last = index === steps.length - 1;
  const sheet = useMedia(cSheetQuery);
  const title = t(`web.tour.${step}.title` as TitleKey); // i18n-ignore: a catalog key
  const still = useReducedMotion();
  // A step that went (People, as face recognition was turned off) moves
  // App's place on too, so it doesn't come back if People does.
  useEffect(() => { if (step !== wanted) onStep(step); }, [step, wanted, onStep]);
  // The step's section shows behind it.
  useEffect(() => { onSection(cStepSection[step]); }, [step, onSection]);

  const dialogRef = useRef<HTMLDialogElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const textRef = useRef<HTMLDivElement>(null);
  const nextRef = useRef<HTMLButtonElement>(null);
  const liveRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const textId = useId();

  // The latest of what the native listeners below use.
  const live = useRef({ step, steps, last, sheet, still, onStep, onClose });
  useLayoutEffect(() => { live.current = { step, steps, last, sheet, still, onStep, onClose }; });

  // Closed by us (a step done, a click passed on, unmounted): the dialog's
  // own close event is then nothing to answer.
  const closing = useRef(false);
  // A click passed on to a control: the focus stays with it.
  const keepFocus = useRef(false);
  const shownAt = useRef(0);
  const downOnDim = useRef(false);

  // What had the keyboard's focus as the tour opened gets it back when it
  // ends (ConfirmDialog's Modal); else the account button. Read on the
  // first render, before the dialog takes the focus.
  const [opener] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });

  const end = useCallback(() => live.current.onClose(false), []);
  // Back is Skip (backStack.ts).
  useBackLayer(true, end);
  const goNext = useCallback(() => {
    const { step: at, steps: all, last: atLast, onStep: move, onClose: close } = live.current;
    if (atLast) { close(false); return; }
    const next = stepAfter(at, all);
    if (next) move(next);
  }, []);
  const goBack = useCallback(() => {
    const { step: at, steps: all, onStep: move } = live.current;
    const before = stepBefore(at, all);
    if (before) move(before);
  }, []);

  // Open: modal, the focus on the main button - before the first paint,
  // so the holes are measured in the open dialog. Unmounted (the tour
  // ended, the session did, or the unreachable screen came over
  // everything): closed, and the focus back where it belongs.
  useLayoutEffect(() => {
    const d = dialogRef.current;
    if (!d) return;
    closing.current = false;
    if (!d.open) d.showModal();
    shownAt.current = performance.now();
    nextRef.current?.focus({ preventScroll: true });
    return () => {
      swallowSecondClick();
      closing.current = true;
      if (d.open) d.close();
      if (keepFocus.current) return;
      const back = opener?.isConnected ? opener : document.querySelector<HTMLElement>('[data-tour="account"]');
      back?.focus({ preventScroll: true });
    };
  }, [opener]);

  // A step changed: the focus stays on the button pressed - or, when that
  // went (Back on the first step), moves to the main one - and the new
  // step is read out. Opening isn't: the dialog's name and text are.
  const announced = useRef(step);
  useLayoutEffect(() => {
    const d = dialogRef.current;
    if (!d?.open) return;
    const a = document.activeElement;
    if (!a || a === document.body || a === d || !d.contains(a)) nextRef.current?.focus({ preventScroll: true });
  }, [step]);
  useEffect(() => {
    if (announced.current === step) return;
    announced.current = step;
    const region = liveRef.current;
    if (!region) return;
    const parts = [
      t("web.tour.progress", { step: index + 1, total: steps.length }),
      title,
      textRef.current?.innerText ?? "",
    ].map((s) => s.replace(/\s+/g, " ").trim()).filter(Boolean);
    // Full stops between the pieces, as a sentence each.
    region.textContent = parts.map((s) => (/[.!?…:]$/.test(s) ? s : `${s}.`)).join(" ");
  }, [step, index, steps.length, t, title]);

  // Keys: every one stops here, so the page's own (Images' and Files'
  // shortcuts, their Escape) never act behind the tour. Arrows step; Tab
  // stays in the card; Esc is the dialog's cancel (below).
  useEffect(() => {
    const d = dialogRef.current;
    if (!d) return;
    const onKey = (e: KeyboardEvent) => {
      e.stopPropagation();
      if (e.key === "Tab") {
        if (cardRef.current) keepTabInside(cardRef.current, e);
        return;
      }
      if (e.isComposing || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.key === "ArrowRight") {
        e.preventDefault();
        if (!live.current.last) goNext();
      } else if (e.key === "ArrowLeft") {
        e.preventDefault();
        goBack();
      }
    };
    d.addEventListener("keydown", onKey);
    return () => d.removeEventListener("keydown", onKey);
  }, [goNext, goBack]);

  // The page under the tour doesn't scroll: not with the wheel, not with a
  // finger (only the tour scrolls it, to a step's control). Only the
  // card's text scrolls, when it is longer than the card. Not React
  // handlers: those are passive and can't prevent it.
  useEffect(() => {
    const d = dialogRef.current;
    if (!d) return;
    const inScrollingText = (target: EventTarget | null) => {
      const body = bodyRef.current;
      return !!body && target instanceof Node && body.contains(target) && body.scrollHeight > body.clientHeight + 1;
    };
    let textTouch = false;
    const onWheel = (e: WheelEvent) => { if (!inScrollingText(e.target)) e.preventDefault(); };
    const onTouchStart = (e: TouchEvent) => { textTouch = inScrollingText(e.target); };
    // One finger only: a pinch still zooms.
    const onTouchMove = (e: TouchEvent) => { if (!textTouch && e.touches.length === 1 && e.cancelable) e.preventDefault(); };
    d.addEventListener("wheel", onWheel, { passive: false });
    d.addEventListener("touchstart", onTouchStart, { passive: true });
    d.addEventListener("touchmove", onTouchMove, { passive: false });
    return () => {
      d.removeEventListener("wheel", onWheel);
      d.removeEventListener("touchstart", onTouchStart);
      d.removeEventListener("touchmove", onTouchMove);
    };
  }, []);

  // The holes and the card's place: measured after every render, on
  // resize and scroll, as the controls or the card change size, and as the
  // page behind changes (a section arriving, its photos loading) - at
  // most once a frame.
  const [view, setView] = useState({ width: 0, height: 0 });
  const [holes, setHoles] = useState<Hole[]>([]);
  const [targets, setTargets] = useState<Element[]>([]);
  const [place, setPlace] = useState<CardPlace | null>(null);
  // The step whose control was scrolled to (or found in view): once a step.
  const scrolledFor = useRef<TourStepId | null>(null);
  const scrollTimer = useRef<number | null>(null);
  const measure = useCallback(() => {
    const d = dialogRef.current;
    const card = cardRef.current;
    if (!d || !card) return;
    const { step: at, sheet: asSheet, still: noMotion } = live.current;
    const width = d.clientWidth;
    const height = d.clientHeight;
    const found = findTargets(at);
    const boxes = found.map(boxOf);
    // A phone's sheet covers what is under it: no ring there (the page is
    // scrolled to bring the controls above it, below).
    const sheetBox = asSheet ? boxOf(card) : null;
    const next = holesFor(boxes, width, height)
      .filter((box) => !sheetBox || !boxesOverlap(box, sheetBox))
      .map((box) => ({
        box,
        // Round around one round control (a pill), else slightly rounded.
        radius: holeRadius(box, found.length === 1 && isRound(found[0])),
      }));
    setView((v) => (v.width === width && v.height === height ? v : { width, height }));
    setHoles((h) => (sameHoles(h, next) ? h : next));
    setTargets((was) => (was.length === found.length && was.every((el, i) => el === found[i]) ? was : found));
    // The card: beside the holes from 600px, else a sheet (Tour.css). Its
    // whole height, as if its text weren't cut (nor scrolled), within what
    // the window lets it have.
    if (asSheet) setPlace(null);
    else {
      const body = bodyRef.current;
      const whole = card.offsetHeight + (body ? body.scrollHeight - body.clientHeight : 0);
      const size = { width: card.offsetWidth, height: Math.min(whole, height - 2 * cCardMargin) };
      const at2 = placeCard(next.map((h) => h.box), size, { width, height });
      setPlace((p) => (p && Math.abs(p.left - at2.left) < 0.5 && Math.abs(p.top - at2.top) < 0.5 && p.maxHeight === at2.maxHeight ? p : at2));
    }
    // The step's controls out of sight (below the fold, under a phone's
    // sheet): scrolled into the room left for them - all of them when they
    // fit, else the first - once a step, after the section settles.
    if (found.length && scrolledFor.current !== at && scrollTimer.current === null) {
      scrollTimer.current = window.setTimeout(() => {
        scrollTimer.current = null;
        const { step: now, sheet: s } = live.current;
        // A step passed by meanwhile: look again for the one shown now.
        if (now !== at) { measureRef.current(); return; }
        if (scrolledFor.current === at) return;
        const all = findTargets(at).filter((el) => !el.closest(".topbar, .sb-dock"));
        scrolledFor.current = at;
        // Nothing in the top bar or the menu moves with the page.
        if (!all.length) return;
        const barBottom = document.querySelector(".topbar")?.getBoundingClientRect().bottom ?? 64;
        const cardTop = s ? cardRef.current?.getBoundingClientRect().top ?? window.innerHeight : window.innerHeight;
        const top = barBottom + cBandMargin;
        const bottom = cardTop - cBandMargin;
        // All of the step's controls when they fit in the room, else the first.
        const boxes2 = all.map(boxOf);
        const together = { left: 0, right: 0, top: Math.min(...boxes2.map((b) => b.top)), bottom: Math.max(...boxes2.map((b) => b.bottom)) };
        const delta = scrollToShow(together.bottom - together.top <= bottom - top ? together : boxes2[0], top, bottom);
        if (Math.abs(delta) >= 1) window.scrollBy({ top: delta, behavior: noMotion ? "auto" : "smooth" });
      }, cScrollSettleMs);
    }
  }, []);
  const measureRef = useRef(measure);
  measureRef.current = measure;
  useLayoutEffect(() => { measure(); });
  useEffect(() => {
    let frame = 0;
    const soon = () => {
      if (frame) return;
      frame = window.requestAnimationFrame(() => { frame = 0; measure(); });
    };
    const d = dialogRef.current;
    const watch = new ResizeObserver(soon);
    if (d) watch.observe(d);
    if (cardRef.current) watch.observe(cardRef.current);
    targets.forEach((el) => watch.observe(el));
    // The page behind: what the tour's own card does (its drawings) is no
    // reason to look again.
    const page = new MutationObserver((records) => {
      if (records.some((r) => !d?.contains(r.target))) soon();
    });
    page.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["class", "hidden"] });
    window.addEventListener("resize", soon);
    window.addEventListener("scroll", soon, { passive: true });
    return () => {
      if (frame) window.cancelAnimationFrame(frame);
      watch.disconnect();
      page.disconnect();
      window.removeEventListener("resize", soon);
      window.removeEventListener("scroll", soon);
    };
  }, [measure, targets]);
  useEffect(() => () => { if (scrollTimer.current !== null) window.clearTimeout(scrollTimer.current); }, []);

  // A phone's sheet: the page gets room at its foot as tall as the sheet
  // (Tour.css), so a control on a short page can still be scrolled up
  // above it. Gone with the tour.
  useEffect(() => {
    const card = cardRef.current;
    if (!sheet || !card) return;
    const root = document.documentElement;
    const room = () => root.style.setProperty("--tour-room", `${Math.ceil(card.getBoundingClientRect().height) + 2 * cBandMargin}px`);
    room();
    root.setAttribute("data-tour-room", "");
    const watch = new ResizeObserver(room);
    watch.observe(card);
    return () => {
      watch.disconnect();
      root.removeAttribute("data-tour-room");
      root.style.removeProperty("--tour-room");
    };
  }, [sheet]);

  // A step's controls are marked while it shows (Tour.css: a photo's
  // circle shows, as it would under the mouse).
  useEffect(() => {
    targets.forEach((el) => el.setAttribute("data-tour-lit", ""));
    return () => targets.forEach((el) => el.removeAttribute("data-tour-lit"));
  }, [targets]);

  // A click in a hole ends the tour and goes on to the control under it,
  // as if the tour hadn't been there; the section stays.
  const passOn = (x: number, y: number) => {
    const d = dialogRef.current;
    closing.current = true;
    keepFocus.current = true;
    if (d?.open) d.close();
    const control = document.elementFromPoint(x, y)?.closest<HTMLElement>(cControl) ?? null;
    live.current.onClose(true);
    if (!control || control.matches(":disabled")) return;
    control.focus({ preventScroll: true });
    control.click();
  };

  const boxes = holes.map((h) => h.box);
  const cardStyle = place
    ? { left: `${place.left}px`, top: `${place.top}px`, ...(place.maxHeight !== null ? { maxHeight: `${place.maxHeight}px` } : {}) }
    : undefined;

  return (
    <dialog
      ref={dialogRef}
      className="tour"
      // Said again for the selector Files' Escape skips ([role=dialog]).
      role="dialog"
      aria-labelledby={titleId}
      aria-describedby={textId}
      onCancel={(e) => { e.preventDefault(); end(); }}
      // The browser can close it by itself (a second Esc): that is Skip.
      onClose={() => { if (!closing.current) end(); }}
      onPointerDown={(e) => {
        downOnDim.current = !cardRef.current?.contains(e.target as Node) && performance.now() - shownAt.current >= cArmMs;
      }}
      onClick={(e) => {
        const fromDim = downOnDim.current;
        downOnDim.current = false;
        if (!fromDim || cardRef.current?.contains(e.target as Node) || e.detail >= 2) return;
        if (insideHole(boxes, e.clientX, e.clientY)) passOn(e.clientX, e.clientY);
        else end();
      }}
    >
      <svg className="tour-dim" width={view.width} height={view.height} aria-hidden="true">
        <path className="tour-scrim" fillRule="evenodd" d={dimPath(view.width, view.height, holes)} />
        {holes.map((h, i) => <path key={`${step}-${i}`} className="tour-ring" d={roundedBoxPath(h.box, h.radius)} />)}
      </svg>
      <div ref={cardRef} className={`tour-card${place ? " placed" : ""}`} style={cardStyle}>
        <div className="tour-head">
          <span className="tour-progress">{t("web.tour.progress", { step: index + 1, total: steps.length })}</span>
          <button type="button" className="tour-skip" onClick={end}>{t("web.tour.skip")}</button>
        </div>
        <div ref={bodyRef} className="tour-body">
          <div key={step} className="tour-step">
            <TourArt step={step} faces={faces === true} />
            <h2 id={titleId} className="tour-title">{title}</h2>
            <div ref={textRef} id={textId} className="tour-text"><StepText id={step} faces={faces} /></div>
          </div>
        </div>
        <div className="tour-foot">
          {index > 0 && <button type="button" className="tour-back" onClick={goBack}>{t("common.back")}</button>}
          <button ref={nextRef} type="button" className="tour-next" onClick={goNext}>
            {last ? t("common.done") : t("common.next")}
          </button>
        </div>
        <div ref={liveRef} className="tour-sr" aria-live="polite" />
      </div>
    </dialog>
  );
}
