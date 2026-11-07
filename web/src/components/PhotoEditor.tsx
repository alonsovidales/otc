// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The photo editor: rotate and crop, full screen over the viewer
// (MediaViewer.tsx opens it from its Edit, a lazy part of its own) on the
// picture the viewer already has - its full size, or its thumbnail until
// that comes. Rotate left and right turn it a quarter turn, the crop frame
// with it; the frame's corners and edges resize it and a drag inside moves
// it (pointer events: a mouse, a finger or a pen), the arrow keys move it
// while it has the focus, the outside is dimmed and a rule-of-thirds grid
// shows while it is dragged; Aspect holds a shape (the arithmetic is
// photoEditMath.ts). Save asks Replace photo or Save as copy: Replace asks
// the device first how many paths hold the photo (GetEditState) and, above
// one, whether to replace it in all of them or only here; then EditImage
// goes (photoEdit.ts), the dialog's button saying Saving… until the device
// answers. A refusal is said in the dialog in the device's words, and Save
// can be tried again - except "changed" (the photo's path holds other
// content now), which the viewer takes over as its Delete's: the editor
// and the viewer close, and the page lists its photos again. Cancel,
// Escape and Back ask first to discard what was
// changed. The device does the edit, on the photo itself: the picture here
// is only the preview, and a small JPEG drawn from it is the tile's
// thumbnail until the device has made its own. The phones' editors do the
// same.
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";
import { useT } from "../i18n";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { ConfirmDialog } from "./ConfirmDialog";
import { useBackLayer } from "./backStack";
import { keepTabInside } from "./focusTrap";
import type { ViewerItem } from "./MediaViewer";
import { copiesAsk, copiesFrom, editAnswer, editPayload, editStatePayload, refusalOf, type EditDone, type EditHow } from "./photoEdit";
import {
  aspectView, cAspects, cStartView, fitPicture, frameRatio, hasChanges, limitsFor, moveRect, resizeRect, rotateView, turnedSize,
  type Aspect, type EditView, type Handle, type Rect, type Size,
} from "./photoEditMath";
import { SelectField } from "./SortBy";
import Spinner from "./Spinner";
import { say, type UiText } from "./uiText";
import "./PhotoGallery.css";
import "./SortBy.css";
import "./PhotoEditor.css";

// Room left round the picture for the handles, which stand half outside
// the frame at the picture's edges.
const cPad = 28;
// An arrow key moves the frame by this share of the picture, ten times as
// far with Shift.
const cKeyStep = 0.01;
// The tile's thumbnail drawn from the edit: its shorter side, as the
// device's small thumbnails (release 111).
const cThumbSide = 400;
const cHandles: readonly Handle[] = ["n", "e", "s", "w", "nw", "ne", "se", "sw"];

// The menu's words for each shape.
const aspectKey = {
  free: "web.images.edit.aspect_free",
  original: "web.images.edit.aspect_original",
  square: "web.images.edit.aspect_square",
  "4:3": "web.images.edit.aspect_4_3",
  "16:9": "web.images.edit.aspect_16_9",
} as const;

// A quarter turn's arrow: anticlockwise, or mirrored for clockwise.
const TurnGlyph = ({ right }: { right?: boolean }) => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false" style={right ? { transform: "scaleX(-1)" } : undefined}>
    <path d="M7.7 17.3A7.5 7.5 0 1 0 7.7 6.7M7.7 2.7V6.7H11.7" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

// The edit drawn small, as a JPEG: the picture turned, the frame's part of
// it, its shorter side at most cThumbSide. null when the browser can't (a
// canvas it refuses, a picture not drawn yet).
async function drawEdit(img: HTMLImageElement, v: EditView): Promise<Blob | null> {
  try {
    const W = img.naturalWidth;
    const H = img.naturalHeight;
    if (!W || !H) return null;
    const pic = turnedSize({ w: W, h: H }, v.turns);
    const cw = v.rect.w * pic.w;
    const ch = v.rect.h * pic.h;
    const s = Math.min(1, cThumbSide / Math.max(1, Math.min(cw, ch)));
    const out = document.createElement("canvas");
    out.width = Math.max(1, Math.round(cw * s));
    out.height = Math.max(1, Math.round(ch * s));
    const ctx = out.getContext("2d");
    if (!ctx) return null;
    ctx.imageSmoothingQuality = "high";
    ctx.scale(s, s);
    ctx.translate(-v.rect.x * pic.w, -v.rect.y * pic.h);
    // The photo turned onto (0, 0)-(pic.w, pic.h): a clockwise turn takes
    // its top left corner to the top right, and so on.
    const turns = ((v.turns % 4) + 4) % 4;
    if (turns === 1) { ctx.translate(H, 0); ctx.rotate(Math.PI / 2); }
    if (turns === 2) { ctx.translate(W, H); ctx.rotate(Math.PI); }
    if (turns === 3) { ctx.translate(0, W); ctx.rotate(-Math.PI / 2); }
    ctx.drawImage(img, 0, 0, W, H);
    return await new Promise<Blob | null>((resolve) => out.toBlob(resolve, "image/jpeg", 0.85));
  } catch {
    return null;
  }
}

// What a drag holds: the handle (or the whole frame), the frame as it
// began, where the pointer was, and which pointer it is (a second finger
// does nothing).
type Drag = { handle: Handle | "move"; start: Rect; x: number; y: number; pointer: number };

// The question showing: Save's (Replace photo or Save as copy), the copies'
// after Replace, or Cancel's with changes.
type Ask = { kind: "save" } | { kind: "copies"; count: number } | { kind: "discard" };

export default function PhotoEditor({ item, src, onCancel, onSaved, onChanged }: {
  item: ViewerItem;
  /** The picture: the viewer's full size, else its thumbnail meanwhile. */
  src: string;
  /** Closed with nothing saved. */
  onCancel: () => void;
  /** The device saved the edit: in the photo's place, or as a copy. */
  onSaved: (done: EditDone) => void;
  /** The device refused it as "changed", in these words: the caller
   *  closes the editor (and the viewer). Without it the dialog says so. */
  onChanged?: (text: UiText) => void;
}) {
  const t = useT();
  const titleId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  // The photo's own size, upright as shown, once its picture is in.
  const [photo, setPhoto] = useState<Size | null>(null);
  const [view, setView] = useState<EditView>(cStartView);
  const [room, setRoom] = useState<Size>({ w: 0, h: 0 });
  const [dragging, setDragging] = useState(false);
  const [ask, setAsk] = useState<Ask | null>(null);
  const changed = hasChanges(view);
  const name = item.path.split("/").pop() ?? "";

  const mountedRef = useRef(true);
  useEffect(() => () => { mountedRef.current = false; }, []);

  // The keyboard's focus comes in, and goes back to Edit as it closes -
  // after keyboard use only, as the dialogs do.
  const [returnFocus] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });
  useEffect(() => {
    rootRef.current?.focus({ preventScroll: true });
    return () => { if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true }); };
  }, [returnFocus]);

  // The stage's room, as the window changes.
  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const measure = () => setRoom({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const obs = new ResizeObserver(measure);
    obs.observe(el);
    return () => obs.disconnect();
  }, []);

  // Where the turned picture lies on the stage, and its frame's limits.
  const pic = photo ? fitPicture(photo, view.turns, { w: Math.max(0, room.w - 2 * cPad), h: Math.max(0, room.h - 2 * cPad) }) : null;
  const shown = pic ? { x: pic.x + cPad, y: pic.y + cPad, w: pic.w, h: pic.h } : null;
  const limits = limitsFor(shown ?? { w: 0, h: 0 });
  const ratio = photo ? frameRatio(view.aspect, turnedSize(photo, view.turns)) : null;
  // The latest of them, for the pointer's moves.
  const live = useRef({ shown, limits, ratio });
  live.current = { shown, limits, ratio };

  // Cancel: straight away with nothing changed, else asked first.
  const cancel = useCallback(() => {
    if (changed) setAsk({ kind: "discard" });
    else onCancel();
  }, [changed, onCancel]);
  // Back is Cancel; a question shows over it has its own Back (Modal).
  useBackLayer(true, () => {
    if (!changed) { onCancel(); return; }
    setAsk({ kind: "discard" });
    return false;
  });

  // ---- dragging ----
  const dragRef = useRef<Drag | null>(null);
  const endDrag = () => {
    dragRef.current = null;
    setDragging(false);
  };
  const startDrag = (e: ReactPointerEvent<HTMLElement>, handle: Handle | "move") => {
    if (dragRef.current || (e.pointerType === "mouse" && e.button !== 0)) return;
    // The handle's own press, not the frame's move under it.
    e.stopPropagation();
    // No text selection or the picture's own drag.
    e.preventDefault();
    try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* it keeps the moves that reach it */ }
    dragRef.current = { handle, start: view.rect, x: e.clientX, y: e.clientY, pointer: e.pointerId };
    setDragging(true);
  };
  const onPointerMove = (e: ReactPointerEvent<HTMLElement>) => {
    const d = dragRef.current;
    const { shown: at, limits: lim, ratio: r } = live.current;
    if (!d || e.pointerId !== d.pointer || !at || !at.w || !at.h) return;
    const dx = (e.clientX - d.x) / at.w;
    const dy = (e.clientY - d.y) / at.h;
    const rect = d.handle === "move" ? moveRect(d.start, dx, dy) : resizeRect(d.start, d.handle, dx, dy, r, lim);
    setView((v) => ({ ...v, rect }));
  };
  const onPointerEnd = (e: ReactPointerEvent<HTMLElement>) => {
    if (dragRef.current?.pointer === e.pointerId) endDrag();
  };

  // The arrow keys move the frame while it has the focus.
  const onFrameKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? cKeyStep * 10 : cKeyStep;
    const by: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    const move = by[e.key];
    if (!move || e.altKey || e.ctrlKey || e.metaKey) return;
    e.preventDefault();
    setView((v) => ({ ...v, rect: moveRect(v.rect, move[0], move[1]) }));
  };

  // Escape: a drag goes back to where it began, else Cancel; Tab stays in
  // the editor. None of it while a question shows: its keys are its own.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (ask) return;
      const root = rootRef.current;
      if (!root) return;
      if (e.key === "Escape") {
        e.preventDefault();
        const d = dragRef.current;
        if (d) {
          setView((v) => ({ ...v, rect: d.start }));
          endDrag();
        } else cancel();
      } else if (e.key === "Tab") {
        keepTabInside(root, e);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [ask, cancel]);

  // ---- the controls ----
  const rotate = (dir: 1 | -1) => {
    if (photo) setView((v) => rotateView(v, dir, photo));
  };
  const chooseAspect = (a: Aspect) => {
    if (photo) setView((v) => aspectView(v, a, photo));
  };

  // ---- saving ----
  // How many paths hold the photo (GetEditState): 1 when the device can't
  // say, or nothing came.
  const askCopies = async (): Promise<number> => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = editStatePayload(item.path); });
      return copiesFrom(resp);
    } catch {
      return copiesFrom(null);
    }
  };
  // EditImage: done (the editor closes), or what to say in the dialog.
  const send = async (how: EditHow, copies: number): Promise<UiText | null> => {
    const v = view;
    let resp: RespEnvelope;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = editPayload(item, v, how); });
    } catch {
      return say("web.images.edit.failed");
    }
    const answer = editAnswer(resp);
    if (answer.kind === "refused") {
      const refused = refusalOf(answer, false);
      if (!refused.closes || !onChanged) return refused.text;
      onChanged(refused.text);
      return null;
    }
    const img = imgRef.current;
    const thumb = img ? await drawEdit(img, v) : null;
    if (mountedRef.current) onSaved({ kind: how.asCopy ? "copy" : "replace", file: answer.file, thumb, copies, every: how.every });
    return null;
  };
  const replace = async (): Promise<UiText | null> => {
    const copies = await askCopies();
    if (!mountedRef.current) return null;
    const q = copiesAsk(copies);
    if (q.kind === "copies") {
      setAsk({ kind: "copies", count: q.count });
      return null;
    }
    return send({ asCopy: false, every: false }, copies);
  };

  // The picture, turned in its box on the stage, and the frame over it.
  const turnedDeg = (((view.turns % 4) + 4) % 4) * 90;
  const imgBox = photo && shown ? (() => {
    const scale = shown.w / turnedSize(photo, view.turns).w;
    const w = photo.w * scale;
    const h = photo.h * scale;
    return { left: (shown.w - w) / 2, top: (shown.h - h) / 2, width: w, height: h };
  })() : null;
  const frame = shown ? { left: view.rect.x * shown.w, top: view.rect.y * shown.h, width: view.rect.w * shown.w, height: view.rect.h * shown.h } : null;

  return (
    <div
      ref={rootRef}
      className="pe-root"
      role="dialog"
      aria-modal="true"
      aria-label={t("web.images.edit.button")}
      aria-describedby={titleId}
      tabIndex={-1}
    >
      <div className="pe-bar">
        <button type="button" className="pg-btn quiet small" onClick={cancel}>{t("common.cancel")}</button>
        <span id={titleId} className="pe-title">{name}</span>
        <button type="button" className="pg-btn primary small" disabled={!changed} onClick={() => setAsk({ kind: "save" })}>{t("common.save")}</button>
      </div>

      <div ref={stageRef} className="pe-stage">
        {!photo && <div className="pe-wait"><Spinner /></div>}
        {/* Until its size is known the picture loads unseen. */}
        <div
          className="pe-pic"
          style={shown ? { left: shown.x, top: shown.y, width: shown.w, height: shown.h } : { visibility: "hidden" }}
        >
          <img
            ref={imgRef}
            className="pe-img"
            src={src || undefined}
            alt=""
            draggable={false}
            onLoad={(e) => {
              const el = e.currentTarget;
              if (el.naturalWidth && el.naturalHeight) setPhoto({ w: el.naturalWidth, h: el.naturalHeight });
            }}
            style={imgBox ? { ...imgBox, transform: `rotate(${turnedDeg}deg)` } : undefined}
          />
          {frame && (
            <>
              {/* The outside dimmed: the hole's shadow, clipped to the picture. */}
              <div className="pe-dim" aria-hidden="true">
                <div className="pe-hole" style={frame} />
              </div>
              <div
                className={"pe-frame" + (dragging ? " dragging" : "")}
                style={frame}
                tabIndex={0}
                // A name needs a role: a plain div may not carry one.
                role="group"
                aria-label={t("web.images.edit.frame")}
                onPointerDown={(e) => startDrag(e, "move")}
                onPointerMove={onPointerMove}
                onPointerUp={onPointerEnd}
                onPointerCancel={onPointerEnd}
                onLostPointerCapture={onPointerEnd}
                onKeyDown={onFrameKey}
              >
                {dragging && (
                  <span className="pe-grid" aria-hidden="true">
                    <span className="pe-grid-v" style={{ left: "33.333%" }} />
                    <span className="pe-grid-v" style={{ left: "66.667%" }} />
                    <span className="pe-grid-h" style={{ top: "33.333%" }} />
                    <span className="pe-grid-h" style={{ top: "66.667%" }} />
                  </span>
                )}
                {cHandles.map((h) => (
                  <span key={h} className={`pe-h pe-h-${h}`} aria-hidden="true" onPointerDown={(e) => startDrag(e, h)} />
                ))}
              </div>
            </>
          )}
        </div>
      </div>

      <div className="pe-tools">
        <button type="button" className="pe-tool" disabled={!photo} onClick={() => rotate(-1)}
          aria-label={t("web.images.edit.rotate_left")} data-tip={t("web.images.edit.rotate_left")}>
          <TurnGlyph />
          <span className="pe-tool-text" aria-hidden="true">{t("web.images.edit.rotate_left")}</span>
        </button>
        <button type="button" className="pe-tool" disabled={!photo} onClick={() => rotate(1)}
          aria-label={t("web.images.edit.rotate_right")} data-tip={t("web.images.edit.rotate_right")}>
          <TurnGlyph right />
          <span className="pe-tool-text" aria-hidden="true">{t("web.images.edit.rotate_right")}</span>
        </button>
        <label className="sortby pe-aspect">
          <span className="sortby-label">{t("web.images.edit.aspect")}</span>
          <SelectField value={view.aspect} disabled={!photo} onChange={(e) => chooseAspect(e.target.value as Aspect)}>
            {cAspects.map((a) => <option key={a} value={a}>{t(aspectKey[a])}</option>)}
          </SelectField>
        </label>
        {changed && (
          <button
            type="button"
            className="pg-btn quiet small pe-reset"
            onClick={() => {
              setView(cStartView);
              // It goes with what it undid: the editor keeps the focus.
              rootRef.current?.focus({ preventScroll: true });
            }}
          >
            {t("web.images.edit.reset")}
          </button>
        )}
      </div>

      {ask?.kind === "save" && (
        <ConfirmDialog
          title={t("web.images.edit.save_title")}
          text={t("web.images.edit.save_text")}
          confirm={t("web.images.edit.replace")}
          busyText={t("web.images.edit.saving")}
          tone="primary"
          onConfirm={replace}
          other={{ label: t("web.images.edit.as_copy"), onConfirm: () => send({ asCopy: true, every: false }, 1) }}
          onCancel={() => setAsk(null)}
        />
      )}
      {ask?.kind === "copies" && (
        <ConfirmDialog
          title={t("web.images.edit.save_title")}
          text={t("web.images.edit.copies_text", { count: ask.count })}
          confirm={t("web.images.edit.copies_all", { count: ask.count })}
          busyText={t("web.images.edit.saving")}
          tone="primary"
          onConfirm={() => send({ asCopy: false, every: true }, ask.count)}
          other={{ label: t("web.images.edit.copies_only_this"), onConfirm: () => send({ asCopy: false, every: false }, ask.count) }}
          onCancel={() => setAsk(null)}
        />
      )}
      {ask?.kind === "discard" && (
        <ConfirmDialog
          title={t("web.images.edit.discard_title")}
          confirm={t("web.images.edit.discard")}
          busyText={t("web.images.edit.discard")}
          onConfirm={async () => { onCancel(); return null; }}
          onCancel={() => setAsk(null)}
        />
      )}
    </div>
  );
}
