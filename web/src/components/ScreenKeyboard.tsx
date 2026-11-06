// SPDX-License-Identifier: AGPL-3.0-or-later

// The on-screen keyboard of screen mode (screenMode.ts): a touch screen
// plugged into the device has no keyboard, and the kiosk's browser has
// none of its own. It comes up at the bottom of the page whenever the
// focus is in a text field and goes when the focus leaves, or with its
// own key, and types into the field as the browser's editing does
// (screenKeyboardTyping.ts), so the app sees every key as it would a
// physical one.
//
// Keys never take the focus: a press is cancelled where the browser would
// move it (pointerdown, mousedown, touchstart), and a key acts on its
// release - a long press on a letter offers its accents instead, to slide
// to or tap - except Backspace, which acts at once and repeats while held.
// Shift is for the next letter; tapped twice, for all of them. The letters
// follow the language on screen (screenKeyboardLayout.ts).
//
// The page makes room: the body gets the keyboard's height at its foot
// (body.osk-open, --osk-h), and a field the keyboard would cover is
// scrolled up - or, in a dialog (fixed, so it doesn't scroll), the dialog
// is moved up by the difference until the keyboard goes.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { PointerEvent as ReactPointerEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import { languages, useT, type TFunction } from "../i18n";
import {
  accentsOf, firstLayer, hasCase, keyboardRows, startsCapital, upperOf,
  type FieldInfo, type KeySpec, type Layer,
} from "./screenKeyboardLayout";
import { deleteBack, focusedField, pressEnter, textBefore, typeText } from "./screenKeyboardTyping";
import "./ScreenKeyboard.css";

type Shift = "off" | "once" | "lock";
interface Target { el: HTMLElement; field: FieldInfo }
// The accents a long press opened: over which key, which one is lit, and
// whether they stay up for a tap (the finger left without sliding).
interface Accents { id: string; base: string; list: string[]; lit: number; x: number; y: number; sticky: boolean }

const cHoldMs = 450;
const cRepeatMs = 70;
const cDoubleTapMs = 350;
// Room kept between a field and the keyboard's top.
const cClearancePx = 16;

// What the layer keys show: the same on every phone, in every language.
const cLayerGlyph: Record<Layer, string> = {
  letters: "ABC", // i18n-ignore: keyboard key glyph
  symbols: "123", // i18n-ignore: keyboard key glyph
  more: "=\\<", // i18n-ignore: keyboard key glyph
  digits: "123", // i18n-ignore: keyboard key glyph
};

function layerName(t: TFunction, to: Layer): string {
  if (to === "letters") return t("web.shell.keyboard.letters");
  if (to === "more") return t("web.shell.keyboard.more_symbols");
  return t("web.shell.keyboard.symbols");
}

function enterName(t: TFunction, hint: string): string {
  switch (hint) {
    case "go": return t("web.shell.keyboard.go");
    case "search": return t("common.search");
    case "send": return t("common.send");
    case "next": return t("common.next");
    case "done": return t("common.done");
    default: return t("web.shell.keyboard.enter");
  }
}

// The nearest fixed box around el (a dialog, the top bar), which scrolling
// can't bring up.
function fixedAround(el: HTMLElement): HTMLElement | null {
  for (let n: HTMLElement | null = el; n && n !== document.body; n = n.parentElement) {
    if (getComputedStyle(n).position === "fixed") return n;
  }
  return null;
}

const Icon = ({ d, filled = false }: { d: string; filled?: boolean }) => (
  <svg viewBox="0 0 24 24" width="26" height="26" aria-hidden="true" focusable="false">
    <path d={d} fill={filled ? "currentColor" : "none"} stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const cShiftPath = "M12 4 4 12.5h4.5V19h7v-6.5H20Z"; // i18n-ignore: SVG path data
const cBackPath = "M9 5h11v14H9l-6-7Zm3.5 4.5 5 5m0-5-5 5"; // i18n-ignore: SVG path data
const cEnterPath = "M19 6v6.5H6m4-4-4 4 4 4"; // i18n-ignore: SVG path data
const cGoPath = "M5 12h14m-5-5 5 5-5 5"; // i18n-ignore: SVG path data
const cSearchPath = "M10.5 4a6.5 6.5 0 1 1 0 13 6.5 6.5 0 0 1 0-13Zm4.8 11.3L20 20"; // i18n-ignore: SVG path data
const cSendPath = "M4 12 20 4l-5 16-3-7Zm8 1 8-9"; // i18n-ignore: SVG path data
const cDonePath = "M5 12.5 9.5 17 19 7.5"; // i18n-ignore: SVG path data
const cHidePath = "M3.5 5h17v9h-17Zm3 3h1m3 0h1m3 0h1m3 0h1M7 11h10m-7 5 2 2.5 2-2.5"; // i18n-ignore: SVG path data

function enterIcon(hint: string): string {
  switch (hint) {
    case "go": case "next": return cGoPath;
    case "search": return cSearchPath;
    case "send": return cSendPath;
    case "done": return cDonePath;
    default: return cEnterPath;
  }
}

export default function ScreenKeyboard() {
  const t = useT();
  const [target, setTarget] = useState<Target | null>(null);
  const [layer, setLayer] = useState<Layer>("letters");
  const [shift, setShift] = useState<Shift>("off");
  const [accents, setAccents] = useState<Accents | null>(null);
  const [down, setDown] = useState<ReadonlySet<string>>(new Set());
  const rootRef = useRef<HTMLDivElement>(null);
  const alts = useRef<HTMLDivElement>(null);
  // The latest of each, for the timers and the document's listeners.
  const targetRef = useRef(target);
  const shiftRef = useRef(shift);
  const accentsRef = useRef(accents);
  targetRef.current = target;
  shiftRef.current = shift;
  accentsRef.current = accents;
  const timers = useRef(new Map<number, number>());
  const lastShift = useRef(0);
  // A dialog moved up out of the keyboard's way, and where it was.
  const lifted = useRef<{ el: HTMLElement; was: string } | null>(null);

  // Which field has the focus. A field taken off the page while focused
  // says nothing (no focusout), so this also looks now and then.
  useEffect(() => {
    const look = () => {
      const now = focusedField();
      setTarget((was) => {
        if (!now) return null;
        if (was && was.el === now.el && was.field.kind === now.field.kind && was.field.enterHint === now.field.enterHint) return was;
        return now;
      });
    };
    const later = () => { window.setTimeout(look, 0); };
    document.addEventListener("focusin", look);
    document.addEventListener("focusout", later);
    const poll = window.setInterval(look, 400);
    look();
    return () => {
      document.removeEventListener("focusin", look);
      document.removeEventListener("focusout", later);
      window.clearInterval(poll);
    };
  }, []);

  // A field opens on its own layer, in capitals where a sentence starts.
  useEffect(() => {
    setAccents(null);
    if (!target) return;
    setLayer(firstLayer(target.field));
    setShift(startsCapital(target.field, textBefore(target.el)) ? "once" : "off");
  }, [target]);

  const lower = useCallback(() => {
    const l = lifted.current;
    if (!l) return;
    l.el.style.translate = l.was;
    lifted.current = null;
  }, []);

  // The page's room for the keyboard, and the field in sight above it.
  useLayoutEffect(() => {
    const kb = rootRef.current;
    if (!target || !kb) return;
    const reveal = () => {
      const height = Math.ceil(kb.getBoundingClientRect().height);
      document.documentElement.style.setProperty("--osk-h", `${height}px`);
      document.body.classList.add("osk-open");
      lower();
      const limit = window.innerHeight - height - cClearancePx;
      let r = target.el.getBoundingClientRect();
      if (r.bottom <= limit) return;
      const box = fixedAround(target.el);
      if (!box) {
        target.el.scrollIntoView({ block: "center", inline: "nearest" });
        return;
      }
      r = target.el.getBoundingClientRect();
      const by = Math.min(r.bottom - limit, Math.max(0, r.top - cClearancePx));
      if (by <= 0) return;
      lifted.current = { el: box, was: box.style.translate };
      box.style.translate = `0 ${-by}px`;
    };
    reveal();
    const seen = new ResizeObserver(reveal);
    seen.observe(kb);
    window.addEventListener("resize", reveal);
    return () => {
      seen.disconnect();
      window.removeEventListener("resize", reveal);
      lower();
      document.body.classList.remove("osk-open");
      document.documentElement.style.removeProperty("--osk-h");
    };
  }, [target, lower]);

  // Touches never reach the page under the keys (React's own touchstart is
  // passive, and can't cancel one).
  useEffect(() => {
    const kb = rootRef.current;
    if (!kb) return;
    const stop = (e: TouchEvent) => { if (e.cancelable) e.preventDefault(); };
    kb.addEventListener("touchstart", stop, { passive: false });
    return () => kb.removeEventListener("touchstart", stop);
  }, [target]);

  // The accents in the window, however near its edge their key is.
  useLayoutEffect(() => {
    const box = alts.current;
    if (!accents || !box) return;
    const w = box.getBoundingClientRect().width;
    const left = Math.max(8, Math.min(window.innerWidth - w - 8, accents.x - w / 2));
    box.style.left = `${left}px`;
  }, [accents]);

  // Any other press puts the accents away.
  useEffect(() => {
    if (!accents?.sticky) return;
    const away = (e: PointerEvent) => {
      if (!(e.target instanceof Element) || !e.target.closest(".osk-alts")) setAccents(null);
    };
    document.addEventListener("pointerdown", away, true);
    return () => document.removeEventListener("pointerdown", away, true);
  }, [accents]);

  useEffect(() => () => {
    for (const id of timers.current.values()) window.clearTimeout(id);
  }, []);

  const rows = useMemo(
    () => (target ? keyboardRows(t.lang, layer, target.field) : []),
    [t.lang, layer, target],
  );

  if (!target) return null;
  const field = target.field;

  // What the next letter is, after one was typed: in capitals again only
  // where a sentence starts, unless Shift is locked.
  const after = () => {
    const now = targetRef.current;
    if (!now) return;
    setShift((s) => (s === "lock" ? "lock" : startsCapital(now.field, textBefore(now.el)) ? "once" : "off"));
  };

  const type = (ch: string) => {
    const now = targetRef.current;
    if (!now) return;
    const out = shiftRef.current !== "off" && hasCase(ch) ? upperOf(ch, t.tag) : ch;
    typeText(now.el, out);
    after();
  };

  const clearTimer = (pointer: number) => {
    const id = timers.current.get(pointer);
    if (id !== undefined) window.clearTimeout(id);
    timers.current.delete(pointer);
  };

  const press = (id: string, on: boolean) => {
    setDown((was) => {
      const next = new Set(was);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const repeatBack = (pointer: number, wait: number) => {
    timers.current.set(pointer, window.setTimeout(() => {
      const now = targetRef.current;
      if (!now) return;
      deleteBack(now.el);
      after();
      repeatBack(pointer, cRepeatMs);
    }, wait));
  };

  const onDown = (e: ReactPointerEvent<HTMLElement>, key: KeySpec, id: string) => {
    e.preventDefault();
    if (accentsRef.current?.sticky) setAccents(null);
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      // A pointer that is already gone.
    }
    press(id, true);
    clearTimer(e.pointerId);
    const now = targetRef.current;
    if (!now) return;
    if (key.k === "back") {
      deleteBack(now.el);
      after();
      repeatBack(e.pointerId, cHoldMs);
    } else if (key.k === "shift") {
      const at = performance.now();
      const quick = at - lastShift.current < cDoubleTapMs;
      lastShift.current = at;
      setShift((s) => (s === "lock" ? "off" : s === "once" && quick ? "lock" : s === "off" ? "once" : "off"));
    } else if (key.k === "char") {
      const list = accentsOf(t.lang, key.ch);
      if (list.length === 0) return;
      const r = e.currentTarget.getBoundingClientRect();
      timers.current.set(e.pointerId, window.setTimeout(() => {
        timers.current.delete(e.pointerId);
        setAccents({ id, base: key.ch, list, lit: 0, x: r.left + r.width / 2, y: r.top, sticky: false });
      }, cHoldMs));
    }
  };

  // The accent under a point, if the accents are up.
  const accentAt = (x: number, y: number): number | null => {
    const el = document.elementFromPoint(x, y);
    const alt = el instanceof Element ? el.closest<HTMLElement>("[data-alt]") : null;
    return alt ? Number(alt.dataset.alt) : null;
  };

  const onMove = (e: ReactPointerEvent<HTMLElement>, id: string) => {
    const open = accentsRef.current;
    if (!open || open.id !== id || open.sticky) return;
    const at = accentAt(e.clientX, e.clientY);
    if (at !== null && at !== open.lit) setAccents({ ...open, lit: at });
  };

  const onUp = (e: ReactPointerEvent<HTMLElement>, key: KeySpec, id: string) => {
    e.preventDefault();
    press(id, false);
    clearTimer(e.pointerId);
    const now = targetRef.current;
    if (!now) return;
    const open = accentsRef.current;
    if (open && open.id === id && !open.sticky) {
      const at = accentAt(e.clientX, e.clientY);
      if (at !== null) {
        setAccents(null);
        type(open.list[at]);
      } else {
        // Let go without sliding to one: they stay up, to tap.
        setAccents({ ...open, sticky: true });
      }
      return;
    }
    switch (key.k) {
      case "char":
        type(key.ch);
        break;
      case "space":
        typeText(now.el, " ");
        after();
        if (layer === "symbols" || layer === "more") setLayer("letters");
        break;
      case "enter":
        pressEnter(now.el, now.field);
        after();
        break;
      case "layer":
        setLayer(key.to);
        break;
      case "hide":
        setAccents(null);
        now.el.blur();
        setTarget(null);
        break;
      default:
        break;
    }
  };

  const onCancel = (e: ReactPointerEvent<HTMLElement>, id: string) => {
    press(id, false);
    clearTimer(e.pointerId);
  };

  const shifted = shift !== "off";
  const spaceName = languages.find((l) => l.code === t.lang)?.name ?? "";

  const keyView = (key: KeySpec, id: string) => {
    if (key.k === "gap") return <span key={id} className="osk-gap" style={{ flexGrow: key.w }} aria-hidden="true" />;
    let content: ReactNode = null;
    let name: string | undefined;
    let cls = "osk-key";
    let pressed: boolean | undefined;
    switch (key.k) {
      case "char":
        content = shifted && hasCase(key.ch) ? upperOf(key.ch, t.tag) : key.ch;
        break;
      case "shift":
        cls += " is-fn osk-shift" + (shift === "once" ? " is-on" : shift === "lock" ? " is-locked" : "");
        name = shift === "lock" ? t("web.shell.keyboard.caps_lock") : t("web.shell.keyboard.shift");
        pressed = shift !== "off";
        content = <Icon d={cShiftPath} filled={shift !== "off"} />;
        break;
      case "back":
        cls += " is-fn";
        name = t("web.shell.keyboard.backspace");
        content = <Icon d={cBackPath} />;
        break;
      case "enter":
        cls += " osk-enter";
        name = enterName(t, field.enterHint);
        content = <Icon d={enterIcon(field.enterHint)} />;
        break;
      case "space":
        cls += " osk-space";
        name = t("web.shell.keyboard.space");
        content = <span className="osk-space-name" aria-hidden="true">{layer === "digits" ? "" : spaceName}</span>;
        break;
      case "layer":
        cls += " is-fn osk-layer";
        name = layerName(t, key.to);
        content = cLayerGlyph[key.to];
        break;
      case "hide":
        cls += " is-fn";
        name = t("web.shell.keyboard.hide");
        content = <Icon d={cHidePath} />;
        break;
    }
    if (down.has(id)) cls += " is-down";
    return (
      <button
        key={id}
        type="button"
        tabIndex={-1}
        className={cls}
        style={{ flexGrow: key.w ?? 1 }}
        aria-label={name}
        aria-pressed={pressed}
        onPointerDown={(e) => onDown(e, key, id)}
        onPointerMove={(e) => onMove(e, id)}
        onPointerUp={(e) => onUp(e, key, id)}
        onPointerCancel={(e) => onCancel(e, id)}
      >
        {content}
      </button>
    );
  };

  return createPortal(
    <>
      <div
        ref={rootRef}
        className={`osk${layer === "digits" ? " is-digits" : ""}`}
        role="group"
        aria-label={t("web.shell.keyboard.label")}
        onPointerDown={(e) => e.preventDefault()}
        onMouseDown={(e) => e.preventDefault()}
        onContextMenu={(e) => e.preventDefault()}
      >
        <div className="osk-rows">
          {rows.map((row, r) => (
            <div key={`${layer}-${r}`} className="osk-row">
              {row.map((key, c) => keyView(key, `${layer}-${r}-${c}`))}
            </div>
          ))}
        </div>
      </div>
      {accents && (
        <div
          ref={alts}
          className="osk-alts"
          role="group"
          aria-label={t("web.shell.keyboard.accents")}
          style={{ top: accents.y }}
          onPointerDown={(e) => e.preventDefault()}
          onMouseDown={(e) => e.preventDefault()}
          onContextMenu={(e) => e.preventDefault()}
        >
          {accents.list.map((alt, i) => (
            <button
              key={alt}
              type="button"
              tabIndex={-1}
              data-alt={i}
              className={`osk-alt${i === accents.lit ? " is-lit" : ""}`}
              onPointerUp={(e) => {
                e.preventDefault();
                if (!accentsRef.current?.sticky) return;
                setAccents(null);
                type(alt);
              }}
            >
              {shifted && hasCase(alt) ? upperOf(alt, t.tag) : alt}
            </button>
          ))}
        </div>
      )}
    </>,
    document.body,
  );
}
