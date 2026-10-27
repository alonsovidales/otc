// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The dialogs over Images and the viewer: Modal, a small card over the
// dimmed page, and ConfirmDialog, a question with Cancel and a red button
// (Delete...), and maybe a second, outlined one before it (issue #201's
// "Delete only this copy"). Images' dialogs use them, and the photo viewer
// (MediaViewer) asks with ConfirmDialog before deleting what it shows.
// LinkDialog shows a share link to copy by hand (Images and Files). Styled
// in PhotoGallery.css (.pg-dlg*, .pg-btn).
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useT } from "../i18n";
import Spinner from "./Spinner";
import type { UiText } from "./uiText";
import "./PhotoGallery.css";

const cFocusable = "button, [href], input, textarea, select, [tabindex]:not([tabindex='-1'])";

// Tab and Shift+Tab within `box`: from its last control to its first and
// back, and into it from wherever the focus fell (a control that was
// removed, or disabled while busy, leaves it on the page).
function keepTabInside(box: HTMLElement, e: KeyboardEvent) {
  const all = [...box.querySelectorAll<HTMLElement>(cFocusable)]
    .filter((el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null);
  if (!all.length) { e.preventDefault(); box.focus(); return; }
  const first = all[0];
  const last = all[all.length - 1];
  const active = document.activeElement;
  if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
  else if (e.shiftKey && (active === first || active === box)) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
}

// A small card over the dimmed page. Focus moves into it (to the element
// marked data-autofocus, else the card) and back when it closes; Tab stays
// inside; Escape or a click on the dimmed page cancels - except while busy.
// The keys are the document's, so they work wherever the focus fell.
export function Modal({ labelledBy, busy = false, wide = false, onClose, children }: {
  labelledBy: string;
  busy?: boolean;
  wide?: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  // A press that starts inside the card (selecting text in a field) and
  // ends outside it is not a click on the page.
  const downOnBackdropRef = useRef(false);
  const busyRef = useRef(busy);
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    busyRef.current = busy;
    onCloseRef.current = onClose;
  });

  // What had the keyboard's focus before the dialog gets it back on close.
  // Read on the first render, before a field inside can take it
  // (autoFocus). After a tap or a click it stays where it falls: a button
  // focused only by a finger would come back with its tooltip showing.
  const [returnFocus] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });
  useEffect(() => {
    const card = cardRef.current;
    if (card && !card.contains(document.activeElement)) {
      (card.querySelector<HTMLElement>("[data-autofocus]") ?? card).focus();
    }
    return () => { if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true }); };
  }, [returnFocus]);
  // Busy no more (an error to read): the focus its disabled buttons lost.
  useEffect(() => {
    if (!busy && document.activeElement === document.body) cardRef.current?.focus();
  }, [busy]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const card = cardRef.current;
      if (!card) return;
      if (e.key === "Escape") {
        // A field's own Escape (the new collection's name) is not this.
        if (e.defaultPrevented) return;
        e.preventDefault();
        e.stopPropagation();
        if (!busyRef.current) onCloseRef.current();
      } else if (e.key === "Tab") {
        keepTabInside(card, e);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div
      className="pg-dlg-backdrop"
      onPointerDown={(e) => { downOnBackdropRef.current = e.target === e.currentTarget; }}
      onClick={(e) => {
        // Nor is a double click's second, when the first opened the dialog.
        const fromBackdrop = downOnBackdropRef.current && e.target === e.currentTarget && e.detail < 2;
        downOnBackdropRef.current = false;
        if (fromBackdrop && !busy) onClose();
      }}
    >
      <div ref={cardRef} className={`pg-dlg${wide ? " wide" : ""}`} role="dialog" aria-modal="true" aria-labelledby={labelledBy} tabIndex={-1}>
        {children}
      </div>
    </div>
  );
}

export function ConfirmDialog({ title, text, confirm, busyText, onConfirm, onCancel, other }: {
  title: string;
  text: ReactNode;
  confirm: string;
  busyText: string;
  // Does the work: an error to show in the dialog, or null when done (the
  // caller closes the dialog then).
  onConfirm: () => Promise<UiText | null>;
  onCancel: () => void;
  // A second way to go on, before the red button and outlined (issue
  // #201: "Delete only this copy" beside "Delete all 3 copies"). Its work
  // and its answer are as onConfirm's.
  other?: { label: string; onConfirm: () => Promise<UiText | null> };
}) {
  const t = useT();
  const titleId = useId();
  // Which button's work is running: its spinner, and every button waits.
  const [busy, setBusy] = useState<null | "confirm" | "other">(null);
  const [error, setError] = useState<UiText | null>(null);
  const run = async (which: "confirm" | "other", work: () => Promise<UiText | null>) => {
    setBusy(which);
    setError(null);
    const err = await work();
    if (err) {
      setError(() => err);
      setBusy(null);
    }
  };
  return (
    // Wider with a second way to go on, so its three buttons share a line.
    <Modal labelledBy={titleId} busy={busy != null} wide={!!other} onClose={onCancel}>
      <div>
        <h2 id={titleId} className="pg-dlg-title">{title}</h2>
        <p className="pg-dlg-text">{text}</p>
      </div>
      {error && <p className="pg-dlg-error" role="alert">{error(t)}</p>}
      <div className="pg-dlg-actions">
        {/* Focus starts on Cancel: Enter right away keeps everything. */}
        <button type="button" className="pg-btn quiet" onClick={onCancel} disabled={busy != null} data-autofocus>{t("common.cancel")}</button>
        {other && (
          <button type="button" className="pg-btn outline danger" onClick={() => void run("other", other.onConfirm)}
            disabled={busy != null} aria-busy={busy === "other"}>
            {busy === "other" ? <Spinner label={busyText} /> : other.label}
          </button>
        )}
        <button type="button" className="pg-btn danger" onClick={() => void run("confirm", onConfirm)} disabled={busy != null} aria-busy={busy === "confirm"}>
          {busy === "confirm" ? <Spinner label={busyText} /> : confirm}
        </button>
      </div>
    </Modal>
  );
}

// A share link, for copying by hand: the clipboard refused it (a browser
// that only allows a copy right after a click, and making the link took
// longer). Images and Files.
export function LinkDialog({ link, onClose }: { link: string; onClose: () => void }) {
  const t = useT();
  const titleId = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      // The field is selected: the system's own copy works.
    }
  };
  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div>
        <h2 id={titleId} className="pg-dlg-title">{t("web.images.share.link_title")}</h2>
        <p className="pg-dlg-text">{t("web.images.share.link_text")}</p>
      </div>
      <div className="pg-inline">
        <input className="pg-input" readOnly value={link} onFocus={(e) => e.currentTarget.select()} aria-label={t("web.images.share.link_field")} data-autofocus />
        <button type="button" className="pg-btn primary" onClick={() => void copy()}>
          {copied ? t("web.images.share.copy_done") : t("web.images.share.copy")}
        </button>
      </div>
      <div className="pg-dlg-actions">
        <button type="button" className="pg-btn quiet" onClick={onClose}>{t("common.done")}</button>
      </div>
    </Modal>
  );
}
