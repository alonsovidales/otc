// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour (rules in tourSteps.ts; App.tsx opens it, loaded as a
// part of its own - lazyPart.ts - with its drawings, TourArt.tsx): a card
// over the dimmed page, one skill at a time - searching, jumping to a
// date, selecting, the viewer, people, collections, Files' views and
// sharing, liking and posting, adding friends and (the device's owner
// only) users - each with a small moving drawing of the real controls
// (their own labels, the theme's colours), so it teaches even an empty
// library. Each step shows its section (App moves there, onSection)
// and, where the step's control is on screen, the dimmed layer leaves a
// hole with an ember ring around it, the card beside it (a sheet at the foot on a phone, the control scrolled into
// the room above it). A click in a hole ends the tour where it is and goes
// on to that control; Skip, Done, Esc, Back or the dimmed layer end it
// and App shows the section it opened over. A native modal <dialog>: the
// page behind is inert, and the card draws above the top bar, banners,
// notices and tooltips. It changes nothing and opens nothing of its own:
// it only asks whether this is the device's owner (useOwner).
// App keeps its step, so it comes back where it was after the
// device-unreachable screen.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Trans, useT } from "../i18n";
import { useWS } from "../net/useWS";
import type { TabKey } from "./nav";
import { useBackLayer } from "./backStack";
import TourArt from "./TourArt";
import { useReducedMotion } from "./tourMotion";
import {
  boxesOverlap, cStepSection, cStepTargets, dimPath, holeRadius, holesFor, insideHole, planCard, roundedBoxPath, scrollToShow,
  stepAfter, stepBefore, stepPlace, tourSteps, type Box, type CardPlace, type CardPlan, type Hole, type Size, type TourStepId,
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
// The longest the tour's own smooth scroll may take: after it, the card
// is placed for where the page is.
const cScrollMs = 1500;
// What sits still as the page scrolls.
const cFixedBars = ".topbar, .sb-dock"; // i18n-ignore: CSS selectors

// The card's drawing: as drawn, smaller, or left out (Tour.css: data-art)
// - smaller before the card's text would ever be cut.
type ArtSize = "full" | "compact" | "none";

type TitleKey = `web.tour.${TourStepId}.title`;

// Whether the signed-in person is the device's owner, who alone adds
// users: the primary instance's (Settings › Users is there only,
// UsersPanel; every other user has an instance of their own). Asked as
// the tour opens and, once answered, kept for the page - its instance
// is the page's own; null until then (the Users step and the count wait).
// For the owner, also whether the device is on the bridge, as UsersPanel
// asks: only then does Settings › Users offer an address of their own
// (Request bridge access); unanswered, it does.
let knownOwner: boolean | null = null;

function useOwner(): { owner: boolean | null; bridge: boolean } {
  const [owner, setOwner] = useState(knownOwner);
  const [bridge, setBridge] = useState(true);
  useEffect(() => {
    if (owner !== null) return;
    let live = true;
    // Unanswered: not the owner, this time (Settings › Users wouldn't show either).
    useWS.request((e) => { e.payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} }; }).then(
      (resp) => {
        if (resp.payload?.$case === "respInstanceRole") knownOwner = resp.payload.respInstanceRole.isPrimary;
        if (live) setOwner(knownOwner ?? false);
      },
      () => { if (live) setOwner(false); },
    );
    return () => { live = false; };
  }, [owner]);
  useEffect(() => {
    if (!owner) return;
    let live = true;
    useWS.request((e) => { e.payload = { $case: "reqGetBridgeAccess", reqGetBridgeAccess: {} }; }).then(
      (resp) => { if (live && resp.payload?.$case === "respBridgeAccess" && !resp.payload.respBridgeAccess.enabled) setBridge(false); },
      () => {},
    );
    return () => { live = false; };
  }, [owner]);
  return { owner, bridge };
}

// A label quoted in a step's text, from that label's own key, in bold.
const bold = (s: string) => <strong>{s}</strong>;

function StepText({ id, faces, bridge }: { id: TourStepId; faces: boolean | null; bridge: boolean }) {
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
    case "users":
      return (
        <p>
          <Trans
            k="web.tour.users.text"
            args={{ settings: t("web.shell.menu.settings"), users: t("web.settings.users.title"), add: t("web.settings.users.add") }}
            parts={b}
          />
          {/* The box is there only on the bridge (UsersPanel). */}
          {bridge && <>{" "}<Trans k="web.tour.users.bridge" args={{ bridge: t("web.settings.users.request_bridge") }} parts={b} /></>}
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

// The card's whole height with each size of its drawing, the largest
// first: as it is now, less its drawing as drawn now, plus each size's
// (TourArt.css: --ta-full, --ta-compact; the step's gap goes with it).
// And the tallest the card may be (Tour.css): any taller, its text scrolls.
function cardSizes(card: HTMLElement, body: HTMLElement | null): { arts: ArtSize[]; cards: Size[]; maxHeight: number } {
  const whole = card.offsetHeight + (body ? body.scrollHeight - body.clientHeight : 0);
  const stage = card.querySelector<HTMLElement>(".ta-stage");
  const style = stage ? getComputedStyle(stage) : null;
  const gap = stage?.parentElement ? Number.parseFloat(getComputedStyle(stage.parentElement).rowGap) || 0 : 0;
  const drawn = stage && style?.display !== "none" ? stage.offsetHeight + gap : 0;
  const full = style ? Number.parseFloat(style.getPropertyValue("--ta-full")) || 0 : 0;
  const compact = Math.min(full, style ? Number.parseFloat(style.getPropertyValue("--ta-compact")) || full : full);
  const arts: ArtSize[] = [];
  const cards: Size[] = [];
  for (const [art, h] of [["full", full], ["compact", compact], ["none", 0]] as const) {
    if ((art === "full" && h <= 0) || (art === "compact" && (h <= 0 || h >= full))) continue;
    arts.push(art);
    cards.push({ width: card.offsetWidth, height: whole - drawn + (h > 0 ? h + gap : 0) });
  }
  const maxHeight = Number.parseFloat(getComputedStyle(card).maxHeight);
  return { arts, cards, maxHeight: Number.isFinite(maxHeight) ? maxHeight : Number.POSITIVE_INFINITY };
}

// Room at the page's foot (Tour.css: html[data-tour-room]), so a control
// on a short page can still be scrolled up: how much there is, and set it
// (null: none).
function footRoom(): number {
  const root = document.documentElement;
  return root.hasAttribute("data-tour-room") ? Number.parseFloat(root.style.getPropertyValue("--tour-room")) || 0 : 0;
}
function setFootRoom(px: number | null) {
  const root = document.documentElement;
  if (px === null) {
    root.removeAttribute("data-tour-room");
    root.style.removeProperty("--tour-room");
    return;
  }
  root.style.setProperty("--tour-room", `${Math.ceil(px)}px`);
  root.setAttribute("data-tour-room", "");
}

// From 600px: where the card goes, with which drawing, after what scroll
// (tourSteps.planCard), found being the step's controls - the scroll only
// when it may (the step's is still to come, or under way), counting room
// the page could be given at its foot (as tall as the card): short, how
// much more room that scroll needs than there is.
function planFor(card: HTMLElement, body: HTMLElement | null, found: Element[], view: Size, mayScroll: boolean): CardPlan & { arts: ArtSize[]; short: number } {
  const { arts, cards, maxHeight } = cardSizes(card, body);
  const fixed: Box[] = [];
  const moving: Box[] = [];
  for (const el of found) (el.closest(cFixedBars) ? fixed : moving).push(boxOf(el));
  const barBottom = document.querySelector(".topbar")?.getBoundingClientRect().bottom ?? 64;
  const page = document.scrollingElement ?? document.documentElement;
  const down = Math.max(0, page.scrollHeight - window.innerHeight - window.scrollY);
  const more = Math.max(0, Math.ceil(cards[0]?.height ?? 0) + 2 * cBandMargin - footRoom());
  const scroll = mayScroll ? { up: window.scrollY, down: down + more } : { up: 0, down: 0 };
  const band = { top: barBottom + cBandMargin, bottom: view.height - cBandMargin };
  const plan = planCard({ fixed, moving, cards, maxHeight, view, band, scroll });
  return { ...plan, arts, short: Math.max(0, Math.ceil(plan.scroll - down)) };
}

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
  const { owner, bridge } = useOwner();
  const steps = useMemo(() => tourSteps(faces, owner), [faces, owner]);
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
  const [art, setArt] = useState<ArtSize>("full");
  // The step whose controls were scrolled to (or found in view): once a step.
  const scrolledFor = useRef<TourStepId | null>(null);
  const scrollTimer = useRef<number | null>(null);
  // The tour's own scroll under way: the step's, where it goes, until when at the latest.
  const scrolling = useRef<{ step: TourStepId; to: number; until: number } | null>(null);

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
    // scrolled to bring the controls above it, below) - a control under it
    // left out before the holes are merged, so one above it keeps its own.
    const sheetBox = asSheet ? boxOf(card) : null;
    const next = holesFor(sheetBox ? boxes.filter((b) => !boxesOverlap(b, sheetBox)) : boxes, width, height)
      .filter((box) => !sheetBox || !boxesOverlap(box, sheetBox))
      .map((box) => ({
        box,
        // Round around one round control (a pill), else slightly rounded.
        radius: holeRadius(box, found.length === 1 && isRound(found[0])),
      }));
    setView((v) => (v.width === width && v.height === height ? v : { width, height }));
    setHoles((h) => (sameHoles(h, next) ? h : next));
    setTargets((was) => (was.length === found.length && was.every((el, i) => el === found[i]) ? was : found));
    // The tour's own scroll: over once there, or after its time.
    const sc = scrolling.current;
    if (sc && (sc.step !== at || Math.abs(window.scrollY - sc.to) < 1 || performance.now() > sc.until)) scrolling.current = null;
    const mayScroll = scrolledFor.current !== at || scrolling.current !== null;
    if (asSheet) {
      // A sheet at the foot (Tour.css): the largest drawing that keeps its text whole.
      const { arts, cards, maxHeight } = cardSizes(card, bodyRef.current);
      const fit = cards.findIndex((c) => c.height <= maxHeight + 0.5);
      const want = arts[fit < 0 ? arts.length - 1 : fit] ?? "full";
      setArt((a) => (a === want ? a : want));
      setPlace(null);
    } else {
      // Beside the holes, whole: the page scrolled first, the drawing made smaller only then.
      const plan = planFor(card, bodyRef.current, found, { width, height }, mayScroll);
      const want = plan.arts[plan.art] ?? "full";
      setArt((a) => (a === want ? a : want));
      const p2 = plan.place;
      setPlace((p) => (p && Math.abs(p.left - p2.left) < 0.5 && Math.abs(p.top - p2.top) < 0.5 ? p : p2));
    }
    // The step's controls scrolled to, once a step, after the section
    // settles: from 600px as far as the card's plan says (where it fits
    // whole beside them); on a phone into the room above the sheet - all
    // of them when they fit, else the first.
    if (found.length && scrolledFor.current !== at && scrollTimer.current === null) {
      scrollTimer.current = window.setTimeout(() => {
        scrollTimer.current = null;
        const { step: now, sheet: s } = live.current;
        // A step passed by meanwhile: look again for the one shown now.
        if (now !== at) { measureRef.current(); return; }
        if (scrolledFor.current === at) return;
        const all = findTargets(at);
        scrolledFor.current = at;
        const behavior = noMotion ? "auto" : "smooth";
        const c = cardRef.current;
        const dd = dialogRef.current;
        if (!s) {
          if (!c || !dd || !all.length) return;
          const plan = planFor(c, bodyRef.current, all, { width: dd.clientWidth, height: dd.clientHeight }, true);
          const delta = Math.round(plan.scroll);
          // A short page: room at its foot first, for the scroll to reach.
          if (plan.short > 0) setFootRoom(footRoom() + plan.short + 1);
          if (Math.abs(delta) >= 1) {
            scrolling.current = { step: at, to: window.scrollY + delta, until: performance.now() + cScrollMs };
            window.scrollBy({ top: delta, behavior });
          }
          measureRef.current();
          return;
        }
        // Nothing in the top bar or the menu moves with the page.
        const moving = all.filter((el) => !el.closest(cFixedBars));
        if (!moving.length) return;
        const barBottom = document.querySelector(".topbar")?.getBoundingClientRect().bottom ?? 64;
        const cardTop = c?.getBoundingClientRect().top ?? window.innerHeight;
        const top = barBottom + cBandMargin;
        const bottom = cardTop - cBandMargin;
        // All of the step's controls when they fit in the room, else the first.
        const boxes2 = moving.map(boxOf);
        const together = { left: 0, right: 0, top: Math.min(...boxes2.map((b) => b.top)), bottom: Math.max(...boxes2.map((b) => b.bottom)) };
        const delta = scrollToShow(together.bottom - together.top <= bottom - top ? together : boxes2[0], top, bottom);
        if (Math.abs(delta) >= 1) window.scrollBy({ top: delta, behavior });
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
  // above it. From 600px only when a step's scroll needs it (the timer
  // above). Gone with the tour.
  useEffect(() => {
    const card = cardRef.current;
    if (!sheet || !card) return;
    const room = () => setFootRoom(card.getBoundingClientRect().height + 2 * cBandMargin);
    room();
    const watch = new ResizeObserver(room);
    watch.observe(card);
    return () => watch.disconnect();
  }, [sheet]);
  useEffect(() => () => setFootRoom(null), []);

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
  const cardStyle = place ? { left: `${place.left}px`, top: `${place.top}px` } : undefined;

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
      <div ref={cardRef} className={`tour-card${place ? " placed" : ""}`} style={cardStyle} data-art={art}>
        <div className="tour-head">
          {/* The count once the Users step is known to be there or not. */}
          <span className="tour-progress">{owner !== null && t("web.tour.progress", { step: index + 1, total: steps.length })}</span>
          <button type="button" className="tour-skip" onClick={end}>{t("web.tour.skip")}</button>
        </div>
        <div ref={bodyRef} className="tour-body">
          <div key={step} className="tour-step">
            <TourArt step={step} faces={faces === true} bridge={bridge} />
            <h2 id={titleId} className="tour-title">{title}</h2>
            <div ref={textRef} id={textId} className="tour-text"><StepText id={step} faces={faces} bridge={bridge} /></div>
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
