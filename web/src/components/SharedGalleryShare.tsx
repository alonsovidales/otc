// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #180: sharing a group of photos or a folder as a gallery. Asks the
// device what it would share (how many files, how much space the copy
// takes), lets the owner name it and pick how long it lasts, then follows
// the copy and shows the link - the only time it is ever shown, since the
// device keeps no copy of it.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, SharedGallerySource, SharedGalleryPreview, SharedGalleryJob } from "../proto/messages";
import Spinner from "./Spinner";
import { useBackLayer } from "./backStack";
import "./SharedGalleryShare.css";
import { Trans, useT, type TFunction } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import { sizeText } from "./sizeText";

// Sizes: whole KB, MB with one digit, GB with two.
const fmtBytes = (t: TFunction, n: number) => sizeText(t, n, [0, 1, 2]);

// The description a gallery gets until the owner writes one: "Shared
// Media 07-10-2026 14:05" (the phones write the same date).
const pad = (n: number) => String(n).padStart(2, "0");
const defaultDescription = (t: TFunction) => {
  const d = new Date();
  return t("web.files.share.default_description", {
    date: `${pad(d.getDate())}-${pad(d.getMonth() + 1)}-${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`,
  });
};

// A poll that fails (the socket dropped, the device or bridge is
// reconnecting) is retried with a growing delay, about two minutes in all,
// before the dialog gives up on the copy.
const MAX_POLL_FAILS = 12;

// How long the link works: 1, 7 or 30 days.
const EXPIRY_HOURS = [24, 168, 720];

const FOCUSABLE = "button, [href], input, [tabindex]:not([tabindex='-1'])";

export default function SharedGalleryShare({ source, onClose }: { source: Partial<SharedGallerySource>; onClose: () => void }) {
  const t = useT();
  const src: SharedGallerySource = { paths: [], groupId: "", directory: "", ...source };
  const [preview, setPreview] = useState<SharedGalleryPreview | null>(null);
  // A field's value, the owner's to change: made once, in the language on
  // screen when the dialog opened.
  const [description, setDescription] = useState(() => defaultDescription(t));
  const [ttl, setTtl] = useState(168);
  const [lowRes, setLowRes] = useState(false);
  const [job, setJob] = useState<SharedGalleryJob | null>(null);
  const [error, setError] = useState<UiText | null>(null);
  const [copied, setCopied] = useState(false);
  // The request that starts the copy is on its way.
  const [starting, setStarting] = useState(false);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  const dialogRef = useRef<HTMLDivElement>(null);
  const linkRef = useRef<HTMLInputElement>(null);
  // A press that starts in the dialog (selecting the link) and ends on the
  // dimmed page is not a click on the page.
  const downOnBackdrop = useRef(false);
  // What had the keyboard's focus gets it back on close; after a click or a
  // tap it stays where it falls.
  const [returnFocus] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });
  useEffect(() => {
    const box = dialogRef.current;
    if (box && !box.contains(document.activeElement)) box.focus();
    return () => { if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true }); };
  }, [returnFocus]);

  useEffect(() => {
    (async () => {
      let resp: RespEnvelope;
      try {
        resp = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqPreviewSharedGallery", reqPreviewSharedGallery: { source: src } };
        });
      } catch {
        if (alive.current) setError(() => say("web.files.share.preview_failed"));
        return;
      }
      if (!alive.current) return;
      if (resp.payload?.$case === "respSharedGalleryPreview") setPreview(resp.payload.respSharedGalleryPreview);
      else {
        const message = resp.errorMessage;
        setError(() => (message ? asIs(message) : say("web.files.share.preview_failed")));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const start = async () => {
    if (starting) return;
    setError(null);
    setStarting(true);
    let resp: RespEnvelope;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqCreateSharedGallery", reqCreateSharedGallery: { source: src, description: description.trim() || defaultDescription(t), ttlHours: ttl, lowRes } };
      });
    } catch {
      // Not retried: the device may have started the copy before the
      // socket dropped, and a second one would take the space again.
      if (alive.current) { setError(() => say("web.files.share.start_failed")); setStarting(false); }
      return;
    }
    if (!alive.current) return;
    setStarting(false);
    if (resp.payload?.$case !== "respSharedGalleryJob") {
      const message = resp.errorMessage;
      setError(() => (message ? asIs(message) : say("web.files.share.start_failed")));
      return;
    }
    let j = resp.payload.respSharedGalleryJob;
    setJob(j);
    // A poll that throws used to end here, leaving the dialog on
    // "Copying" with no way to close it while the device finished the
    // copy - and the link, shown only this once, was lost. The device
    // keeps a finished job for an hour, so the same job is asked again.
    let fails = 0;
    while (alive.current && !j.finished) {
      await new Promise(r => setTimeout(r, fails ? Math.min(1000 * 2 ** (fails - 1), 10000) : 700));
      if (!alive.current) return;
      let r: RespEnvelope;
      try {
        r = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetSharedGalleryJob", reqGetSharedGalleryJob: { jobId: j.jobId } };
        });
      } catch {
        if (++fails >= MAX_POLL_FAILS) {
          if (alive.current) setError(() => say("web.files.share.lost"));
          return;
        }
        continue;
      }
      fails = 0;
      if (r.payload?.$case !== "respSharedGalleryJob") {
        const message = r.errorMessage;
        if (alive.current) setError(() => (message ? asIs(message) : say("web.files.share.lost")));
        return;
      }
      j = r.payload.respSharedGalleryJob;
      if (alive.current) setJob(j);
    }
    // The device's own words for a copy that failed.
    const failure = j.error;
    if (alive.current && failure) setError(() => asIs(failure));
  };

  const copy = async () => {
    if (!job?.link) return;
    try { await navigator.clipboard.writeText(job.link); setCopied(true); } catch { /* the field can be copied by hand */ }
  };

  const busy = !!job && !job.finished;
  const done = !!job?.finished && !job.error;
  const pct = job && Number(job.bytesTotal) > 0 ? (100 * Number(job.bytesDone)) / Number(job.bytesTotal) : 0;
  // Once a copy is under way only Done or Close end the dialog: a stray
  // click or Escape would lose the link, which is shown this once. Before
  // it, and after an error, there is nothing to lose.
  const dismissable = !starting && (!job || error != null);

  // The link, selected, ready to copy.
  useLayoutEffect(() => { if (done) linkRef.current?.focus(); }, [done]);

  // The document's keys, so they work wherever the focus fell (the Share
  // button goes away once pressed): Escape, and Tab kept in the dialog.
  const dismissRef = useRef(dismissable);
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    dismissRef.current = dismissable;
    onCloseRef.current = onClose;
  });
  // Back closes it when Escape would (backStack.ts); a copy under way
  // stays, with its link to come.
  useBackLayer(true, () => {
    if (!dismissRef.current) return false;
    onCloseRef.current();
  });
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const box = dialogRef.current;
      if (!box) return;
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        if (dismissRef.current) onCloseRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const all = [...box.querySelectorAll<HTMLElement>(FOCUSABLE)]
        .filter(el => !(el as HTMLButtonElement).disabled && el.offsetParent !== null);
      const active = document.activeElement;
      if (!all.length) { e.preventDefault(); box.focus(); return; }
      const first = all[0];
      const last = all[all.length - 1];
      if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (e.shiftKey && (active === first || active === box)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div
      className="sgs-backdrop"
      onPointerDown={e => { downOnBackdrop.current = e.target === e.currentTarget; }}
      onClick={e => {
        // Not a double click's second either, when its first opened this.
        const fromBackdrop = downOnBackdrop.current && e.target === e.currentTarget && e.detail < 2;
        downOnBackdrop.current = false;
        if (fromBackdrop && dismissable) onClose();
      }}
    >
      <div ref={dialogRef} className="sgs-dialog" role="dialog" aria-modal="true" aria-label={t("web.files.share.title")} tabIndex={-1}>
        <h3>{t("web.files.share.title")}</h3>
        {!preview && !error && <Spinner label={t("web.files.share.looking")} />}
        {preview && !job && (
          <>
            <p className="sgs-count">
              <Trans k="web.files.share.summary" args={{ count: preview.files, size: fmtBytes(t, Number(preview.bytes)) }}
                parts={{ b: (s) => <strong>{s}</strong> }} />
              {preview.skipped > 0 && <span className="sgs-dim"> · {t("web.files.share.skipped", { count: preview.skipped })}</span>}
            </p>
            <p className="sgs-dim">{lowRes
              ? <>{t("web.files.share.low_res_note")}{preview.videos > 0 && <> {t("web.files.share.videos_left_out", { count: preview.videos })}</>}</>
              : t("web.files.share.note", { size: fmtBytes(t, Number(preview.bytes)) })}</p>
            <label className="sgs-check">
              <input type="checkbox" checked={lowRes} onChange={e => setLowRes(e.target.checked)} />
              <span><Trans k="web.files.share.low_res" parts={{ b: (s) => <strong>{s}</strong> }} /></span>
            </label>
            <label className="sgs-label" htmlFor="sgs-desc">{t("web.files.share.description")}</label>
            <input id="sgs-desc" className="sgs-input" value={description} onChange={e => setDescription(e.target.value)} maxLength={200} />
            <span className="sgs-label">{t("web.files.share.available_for")}</span>
            <div className="sgs-seg" role="radiogroup" aria-label={t("web.files.share.available_for")}>
              {EXPIRY_HOURS.map(hours => (
                <button key={hours} role="radio" aria-checked={ttl === hours} className={ttl === hours ? "on" : ""} onClick={() => setTtl(hours)}>
                  {t("web.files.share.days", { count: hours / 24 })}
                </button>
              ))}
            </div>
            <div className="sgs-buttons">
              <button className="btn" onClick={onClose}>{t("common.cancel")}</button>
              <button className="btn primary" disabled={starting || preview.files === 0 || (lowRes && preview.files === preview.videos)} onClick={() => void start()}>{t("common.share")}</button>
            </div>
          </>
        )}
        {busy && job && (
          <div role="status">
            <p>{t("web.files.share.copying", {
              current: Math.min(job.done + 1, job.total),
              total: job.total,
              done: fmtBytes(t, Number(job.bytesDone)),
              size: fmtBytes(t, Number(job.bytesTotal)),
            })}</p>
            <div className="sgs-bar"><div style={{ width: `${pct}%` }} /></div>
          </div>
        )}
        {done && job && (
          <>
            <p>{t("web.files.share.ready")}</p>
            <div className="sgs-link">
              <input ref={linkRef} className="sgs-input" readOnly value={job.link} onFocus={e => e.currentTarget.select()} aria-label={t("web.files.share.link")} />
              <button className="btn primary" onClick={() => void copy()}>{copied ? t("web.files.share.copied") : t("web.files.share.copy")}</button>
            </div>
            <div className="sgs-buttons">
              <a className="btn" href={job.link} target="_blank" rel="noreferrer">{t("common.open")}</a>
              <button className="btn" onClick={onClose}>{t("common.done")}</button>
            </div>
          </>
        )}
        {error && (
          <>
            <p className="sgs-error">{error(t)}</p>
            <div className="sgs-buttons"><button className="btn" onClick={onClose}>{t("common.close")}</button></div>
          </>
        )}
      </div>
    </div>
  );
}
