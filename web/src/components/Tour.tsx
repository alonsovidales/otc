// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour (rules in tourSteps.ts; App.tsx opens it): a card over
// the dimmed page, one step at a time - what each section is for, how to
// publish a post, how friends are added - with Skip on every step. A native
// modal <dialog> (showModal): the page behind is inert, and the card draws
// above the top bar, the banners, the notices and the tooltips. What a step
// says never depends on what the page shows, so a changed page can't break
// it; when the step's own control is on screen (a menu item, the top bar's
// menu button or "+ New post", the account picture) the dimmed layer
// leaves a hole with an ember ring around it. A click in the hole ends the
// tour and goes on to that control; anywhere else on the dimmed layer, Esc,
// Back and Skip end it too. It never navigates, sends nothing and opens
// nothing of its own. App keeps its step, so it comes back where it was
// after the device-unreachable screen.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ComponentType } from "react";
import logo from "../assets/off_the_cloud.png";
import { Trans, useT } from "../i18n";
import { FRIEND_TLD } from "../net/friendDomain";
import { AlertsIcon, CollectionsIcon, FilesIcon, FriendsIcon, ImagesIcon, MenuIcon, PeopleIcon, SettingsIcon, SocialIcon } from "./NavIcons";
import { useBackLayer } from "./backStack";
import {
  cStepTargets, dimPath, holeFor, holeRadius, insideHole, roundedBoxPath, stepAfter, stepBefore, stepPlace, tourSteps,
  type Box, type TourStepId,
} from "./tourSteps";
import "./Tour.css";

type Props = {
  // Where the tour stands (App keeps it).
  step: TourStepId;
  // Face recognition on: the People step shows.
  faces: boolean | null;
  onStep: (step: TourStepId) => void;
  // The tour ends (Skip, Done, Esc, the dimmed layer): inside the event
  // that ended it, so what App does then counts as the person's doing.
  onClose: () => void;
};

// A press that begins this soon after the tour opened answers nothing on
// the dimmed layer: the second half of the double click or double tap
// that opened it. The keyboard always counts.
const cArmMs = 450;
// What the keyboard goes through: the card's controls.
const cFocusable = "button, [href], input, textarea, select, [tabindex]:not([tabindex='-1'])";
// What a click in the hole goes on to: the control under the pointer.
const cControl = "button, a[href], input, select, textarea, label, summary, [role='button'], [role='menuitem']";

// Each step's title: the menu's own word for a section's step.
type TitleKey =
  | "web.tour.welcome.title" | "web.tour.menu.title" | "web.tour.post.title" | "web.tour.alerts.title" | "web.tour.done.title"
  | "web.shell.menu.images" | "web.shell.menu.people" | "web.shell.menu.collections" | "web.shell.menu.files"
  | "web.shell.menu.social" | "web.shell.menu.friends";
const cTitles: Record<TourStepId, TitleKey> = {
  welcome: "web.tour.welcome.title",
  menu: "web.tour.menu.title",
  images: "web.shell.menu.images",
  people: "web.shell.menu.people",
  collections: "web.shell.menu.collections",
  files: "web.shell.menu.files",
  social: "web.shell.menu.social",
  post: "web.tour.post.title",
  friends: "web.shell.menu.friends",
  alerts: "web.tour.alerts.title",
  done: "web.tour.done.title",
};

// "+" in a circle, drawn like NavIcons': a new post.
const PlusIcon = ({ size = 22 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 8.25v7.5M8.25 12h7.5" />
  </svg>
);

const cIcons: Partial<Record<TourStepId, ComponentType<{ size?: number }>>> = {
  menu: MenuIcon,
  images: ImagesIcon,
  people: PeopleIcon,
  collections: CollectionsIcon,
  files: FilesIcon,
  social: SocialIcon,
  post: PlusIcon,
  friends: FriendsIcon,
  done: SettingsIcon,
};

// The step's picture: the logo first, then its section's icon (Alerts and
// Settings side by side) in an ember-tinted circle.
function StepArt({ id }: { id: TourStepId }) {
  if (id === "welcome") return <img className="tour-logo" src={logo} alt="" draggable={false} />;
  if (id === "alerts") {
    return (
      <span className="tour-art wide" aria-hidden="true">
        <AlertsIcon size={40} />
        <SettingsIcon size={40} />
      </span>
    );
  }
  const Icon = cIcons[id];
  return <span className="tour-art" aria-hidden="true">{Icon && <Icon size={40} />}</span>;
}

// A label quoted in a step's text, from that label's own key, in bold.
const bold = (s: string) => <strong>{s}</strong>;

function StepText({ id }: { id: TourStepId }) {
  const t = useT();
  const b = { b: bold };
  switch (id) {
    case "welcome":
      return <p>{t("web.tour.welcome.text")}</p>;
    case "menu":
      return <p>{t("web.tour.menu.text")}</p>;
    case "images":
      return <p>{t("web.tour.images.text")}</p>;
    case "people":
      return <p><Trans k="web.tour.people.text" args={{ name: t("web.images.people.add_name") }} parts={b} /></p>;
    case "collections":
      return <p><Trans k="web.tour.collections.text" args={{ add: t("web.images.select.collect") }} parts={b} /></p>;
    case "files":
      return <p>{t("web.tour.files.text")}</p>;
    case "social":
      return <p>{t("web.tour.social.text")}</p>;
    case "post":
      return (
        <>
          <ol>
            <li><Trans k="web.tour.post.step1" args={{ newPost: t("web.shell.top.new_post") }} parts={b} /></li>
            <li>
              <Trans
                k="web.tour.post.step2"
                args={{ library: t("web.social.composer.library"), computer: t("web.social.composer.this_computer") }}
                parts={b}
              />
            </li>
            <li><Trans k="web.tour.post.step3" args={{ publish: t("web.social.composer.publish") }} parts={b} /></li>
          </ol>
          <p><Trans k="web.tour.post.also" args={{ post: t("web.images.select.post") }} parts={b} /></p>
        </>
      );
    case "friends":
      return (
        <>
          <p>
            <Trans
              k="web.tour.friends.add"
              args={{ suffix: FRIEND_TLD, heading: t("web.social.friends.add_title"), send: t("web.social.friends.send") }}
              parts={b}
            />
          </p>
          <p>
            <Trans k="web.tour.friends.accept" args={{ waiting: t("web.social.friends.waiting_title"), accept: t("common.accept") }} parts={b} />
          </p>
          <p>{t("web.tour.friends.own")}</p>
        </>
      );
    case "alerts":
      return (
        <p>
          <Trans k="web.tour.alerts.text" args={{ alerts: t("web.shell.menu.alerts"), settings: t("web.shell.menu.settings") }} parts={b} />
        </p>
      );
    case "done":
      return <p><Trans k="web.tour.done.text" args={{ show: t("web.tour.show") }} parts={b} /></p>;
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

// The step's controls on screen now: the first group of its targets
// that finds one (tourSteps.cStepTargets).
function findTargets(step: TourStepId): Element[] {
  for (const group of cStepTargets[step]) {
    const found = group.map((sel) => document.querySelector(sel)).filter((el): el is Element => !!el && shownOnScreen(el));
    if (found.length) return found;
  }
  return [];
}

const boxOf = (el: Element): Box => {
  const r = el.getBoundingClientRect();
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
};

const sameBox = (a: Box | null, b: Box | null) =>
  a === b || (!!a && !!b && a.left === b.left && a.top === b.top && a.right === b.right && a.bottom === b.bottom);

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

export default function Tour({ step: wanted, faces, onStep, onClose }: Props) {
  const t = useT();
  const steps = useMemo(() => tourSteps(faces), [faces]);
  const step = stepPlace(wanted, steps);
  const index = steps.indexOf(step);
  const last = index === steps.length - 1;
  // A step that went (People, as face recognition was turned off) moves
  // App's place on too, so it doesn't come back if People does.
  useEffect(() => { if (step !== wanted) onStep(step); }, [step, wanted, onStep]);

  const dialogRef = useRef<HTMLDialogElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const textRef = useRef<HTMLDivElement>(null);
  const nextRef = useRef<HTMLButtonElement>(null);
  const liveRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const textId = useId();

  // The latest of what the native listeners below use.
  const live = useRef({ step, steps, last, onStep, onClose });
  useLayoutEffect(() => { live.current = { step, steps, last, onStep, onClose }; });

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

  const end = useCallback(() => live.current.onClose(), []);
  // Back is Skip (backStack.ts).
  useBackLayer(true, end);
  const goNext = useCallback(() => {
    const { step: at, steps: all, last: atLast, onStep: move, onClose: close } = live.current;
    if (atLast) { close(); return; }
    const next = stepAfter(at, all);
    if (next) move(next);
  }, []);
  const goBack = useCallback(() => {
    const { step: at, steps: all, onStep: move } = live.current;
    const before = stepBefore(at, all);
    if (before) move(before);
  }, []);

  // Open: modal, the focus on the main button - before the first paint,
  // so the hole is measured in the open dialog. Unmounted (the tour ended,
  // the session did, or the unreachable screen came over everything):
  // closed, and the focus back where it belongs.
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
      t(cTitles[step]),
      textRef.current?.innerText ?? "",
    ].map((s) => s.replace(/\s+/g, " ").trim()).filter(Boolean);
    // Full stops between the pieces, as a sentence each.
    region.textContent = parts.map((s) => (/[.!?…:]$/.test(s) ? s : `${s}.`)).join(" ");
  }, [step, index, steps.length, t]);

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
  // finger. Only the card's text scrolls, when it is longer than the card.
  // Not React handlers: those are passive and can't prevent it.
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

  // The hole: measured after every render (the menu changing layout, People
  // coming, "+ New post" appearing with Social), on resize, and as the
  // controls or the window change size.
  const [view, setView] = useState({ width: 0, height: 0 });
  const [hole, setHole] = useState<{ box: Box; round: boolean } | null>(null);
  const [targets, setTargets] = useState<Element[]>([]);
  const measure = useCallback(() => {
    const d = dialogRef.current;
    if (!d) return;
    const width = d.clientWidth;
    const height = d.clientHeight;
    const found = findTargets(live.current.step);
    const box = holeFor(found.map(boxOf), width, height);
    const round = found.length === 1 && isRound(found[0]);
    setView((v) => (v.width === width && v.height === height ? v : { width, height }));
    setHole((h) => (sameBox(h?.box ?? null, box) && (!box || h?.round === round) ? h : box && { box, round }));
    setTargets((was) => (was.length === found.length && was.every((el, i) => el === found[i]) ? was : found));
  }, []);
  useLayoutEffect(() => { measure(); });
  useEffect(() => {
    const d = dialogRef.current;
    const watch = new ResizeObserver(() => measure());
    if (d) watch.observe(d);
    targets.forEach((el) => watch.observe(el));
    window.addEventListener("resize", measure);
    return () => {
      watch.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [measure, targets]);

  // A click in the hole ends the tour and goes on to the control under it,
  // as if the tour hadn't been there.
  const passOn = (x: number, y: number) => {
    const d = dialogRef.current;
    closing.current = true;
    keepFocus.current = true;
    if (d?.open) d.close();
    const control = document.elementFromPoint(x, y)?.closest<HTMLElement>(cControl) ?? null;
    live.current.onClose();
    if (!control || control.matches(":disabled")) return;
    control.focus({ preventScroll: true });
    control.click();
  };

  const radius = hole ? holeRadius(hole.box, hole.round) : 0;

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
        if (insideHole(hole?.box ?? null, e.clientX, e.clientY)) passOn(e.clientX, e.clientY);
        else end();
      }}
    >
      <svg className="tour-dim" width={view.width} height={view.height} aria-hidden="true">
        <path className="tour-scrim" fillRule="evenodd" d={dimPath(view.width, view.height, hole?.box ?? null, radius)} />
        {hole && <path key={step} className="tour-ring" d={roundedBoxPath(hole.box, radius)} />}
      </svg>
      <div ref={cardRef} className="tour-card">
        <div className="tour-head">
          <span className="tour-progress">{t("web.tour.progress", { step: index + 1, total: steps.length })}</span>
          <button type="button" className="tour-skip" onClick={end}>{t("web.tour.skip")}</button>
        </div>
        <div ref={bodyRef} className="tour-body">
          <div key={step} className="tour-step">
            <StepArt id={step} />
            <h2 id={titleId} className="tour-title">{t(cTitles[step])}</h2>
            <div ref={textRef} id={textId} className="tour-text"><StepText id={step} /></div>
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
