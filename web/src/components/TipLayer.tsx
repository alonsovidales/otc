// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import "./TipLayer.css";

type Tip = { text: string; x: number; y: number; below: boolean };

/**
 * Tooltips that show at once: any element with a `data-tip` attribute gets
 * one on hover or keyboard focus. The browser's own `title` tooltip waits
 * about a second, too long for an icon whose meaning isn't obvious (the
 * upload-only lock). Drawn fixed in a portal, so a container's
 * `overflow: hidden` (the files table) can't clip it. Mounted once, in
 * main.tsx. Give the element an aria-label too: `data-tip` isn't read out.
 */
export default function TipLayer() {
  const [tip, setTip] = useState<Tip | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const [shift, setShift] = useState(0);

  useEffect(() => {
    let current: Element | null = null;
    // The text can change while the pointer stays on it (the lock after a
    // click): follow the attribute.
    const watch = new MutationObserver(() => { if (current) show(current); });
    const show = (el: Element) => {
      const text = el.getAttribute("data-tip");
      if (!text) { hide(); return; }
      if (el !== current) {
        watch.disconnect();
        watch.observe(el, { attributes: true, attributeFilter: ["data-tip"] });
      }
      current = el;
      const r = el.getBoundingClientRect();
      const below = r.top < 64;
      setTip({ text, x: r.left + r.width / 2, y: below ? r.bottom + 8 : r.top - 8, below });
    };
    const hide = () => {
      watch.disconnect();
      current = null;
      setTip(null);
    };
    const tipAt = (t: EventTarget | null) => (t instanceof Element ? t.closest("[data-tip]") : null);
    const over = (e: MouseEvent) => {
      const el = tipAt(e.target);
      if (el && el !== current) show(el);
      else if (!el && current) hide();
    };
    const out = (e: MouseEvent) => { if (!e.relatedTarget) hide(); };
    const focus = (e: FocusEvent) => { const el = tipAt(e.target); if (el) show(el); };
    document.addEventListener("mouseover", over);
    document.addEventListener("mouseout", out);
    document.addEventListener("focusin", focus);
    document.addEventListener("focusout", hide);
    window.addEventListener("scroll", hide, true);
    window.addEventListener("resize", hide);
    return () => {
      watch.disconnect();
      document.removeEventListener("mouseover", over);
      document.removeEventListener("mouseout", out);
      document.removeEventListener("focusin", focus);
      document.removeEventListener("focusout", hide);
      window.removeEventListener("scroll", hide, true);
      window.removeEventListener("resize", hide);
    };
  }, []);

  // Kept inside the window: centred on the element unless that would run
  // past an edge.
  useLayoutEffect(() => {
    if (!tip || !box.current) return;
    const w = box.current.offsetWidth;
    const left = tip.x - w / 2;
    const max = window.innerWidth - 8 - w;
    setShift(left < 8 ? 8 - left : left > max ? max - left : 0);
  }, [tip]);

  if (!tip) return null;
  return createPortal(
    <div
      ref={box}
      className={`tip-layer${tip.below ? " below" : ""}`}
      style={{ left: tip.x + shift, top: tip.y }}
      role="tooltip"
    >
      {tip.text}
    </div>,
    document.body,
  );
}
