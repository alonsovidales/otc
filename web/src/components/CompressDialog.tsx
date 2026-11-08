// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The Compress dialog (compress.ts has its rules), opened from Images'
// selection bar and from Settings › Largest files with the files picked -
// their photos and videos, each once, the first 500 (compressItems): the
// device first works out what it could do for each one and what it
// would weigh after (GetCompressOptions, "Working out the sizes…"), then
// the dialog offers the videos' choice and the photos' switches, each with
// how many files it compresses and their sizes now and after, the files it
// can't make smaller with the device's reasons, what the choices would
// free and that the originals are deleted for good. Compress sends the
// tasks (CompressFiles) and shows the job as the device works through it
// (compressJob.ts: asked every 2 s while it shows); it can be closed and
// the job goes on. Once over, how many were compressed and what that
// freed, and the files that couldn't be with the device's words. A demo
// device refuses Compress before this opens (the page's refuseOnDemo).
// Its own chunk (lazyPart.ts), loaded when Compress is first pressed.
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { CompressOption, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { useT, type TFunction } from "../i18n";
import { useWS } from "../net/useWS";
import { refuseOnDemo } from "../net/demo";
import { Modal } from "./ConfirmDialog";
import Spinner from "./Spinner";
import { sizeText } from "./sizeText";
import { asIs, say, type UiText } from "./uiText";
import {
  compressItems, compressPayload, compressView, defaultPick, jobOutcome, jobOver, jobProgress, optionsAnswer, optionsPayload, startAnswer,
  type CompressPick, type PhotoPreset, type Sums, type VideoChoice,
} from "./compress";
import { followJob, useCompressJob } from "./compressJob";
import "./CompressDialog.css";

// The words of each choice.
const cVideoLabel: Record<VideoChoice, "web.files.compress.video_1080p" | "web.files.compress.video_720p" | "web.files.compress.video_hevc" | "web.files.compress.video_none"> = {
  video_1080p: "web.files.compress.video_1080p",
  video_720p: "web.files.compress.video_720p",
  video_hevc: "web.files.compress.video_hevc",
  video_none: "web.files.compress.video_none",
};
const cPhotoLabel: Record<PhotoPreset, "web.files.compress.photo_12mp" | "web.files.compress.jpeg_resave" | "web.files.compress.png_to_jpeg"> = {
  photo_12mp: "web.files.compress.photo_12mp",
  jpeg_resave: "web.files.compress.jpeg_resave",
  png_to_jpeg: "web.files.compress.png_to_jpeg",
};

// A size as Files writes it.
const size = (t: TFunction, bytes: number) => sizeText(t, bytes, [1, 1, 1]);

// Under a choice: how many files, what they weigh now and about after.
// Nothing for a choice that would compress none (a switch whose photos all
// take a stronger one).
function RowText({ sums, id }: { sums: Sums | null; id?: string }) {
  const t = useT();
  if (!sums || sums.count === 0) return null;
  return (
    <span id={id} className="cz-row">
      {t("web.files.compress.row", { count: sums.count, now: size(t, sums.now), after: size(t, sums.after) })}
    </span>
  );
}

type Phase =
  | { kind: "estimating" }
  // A device older than Compress.
  | { kind: "old" }
  // The options didn't come: the device's words, or ours; Try again.
  | { kind: "failed"; text: UiText }
  | { kind: "choose"; options: CompressOption[] }
  // CompressFiles answered: the job (compressJob.ts).
  | { kind: "job" };

export default function CompressDialog({ picks, onClose, onStarted }: {
  /** The files picked, each with the content shown for it (File.hash)
   *  and its type: only photos and videos are asked about. */
  picks: readonly { path: string; hash: string; mime?: string }[];
  onClose: () => void;
  /** The job started (CompressFiles answered): the page lets go of its
   *  selection. */
  onStarted?: () => void;
}) {
  const t = useT();
  const titleId = useId();
  const radioName = useId();
  // What is asked about, as the dialog opened (the page's selection may go
  // meanwhile): the title counts it.
  const [asked] = useState(() => compressItems(picks).map((p) => ({ path: p.path, hash: p.hash })));
  const [phase, setPhase] = useState<Phase>({ kind: "estimating" });
  const [pick, setPick] = useState<CompressPick>({ video: "video_none", photos: {} });
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<UiText | null>(null);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  // GetCompressOptions: the device reads each file's header (seconds for
  // a few hundred), so this has the normal clock.
  const estimate = useCallback(async () => {
    setPhase({ kind: "estimating" });
    let resp: RespEnvelope | null = null;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = optionsPayload(asked); });
    } catch {
      // No answer: as below.
    }
    if (!mountedRef.current) return;
    const a = optionsAnswer(resp);
    if (a.kind === "options") {
      setPick(defaultPick(a.items));
      setPhase({ kind: "choose", options: a.items });
    } else if (a.kind === "old") setPhase({ kind: "old" });
    else setPhase({ kind: "failed", text: a.kind === "refused" ? asIs(a.said) : say("web.files.compress.options_failed") });
  }, [asked]);
  useEffect(() => { void estimate(); }, [estimate]);

  const options = phase.kind === "choose" ? phase.options : null;
  const view = useMemo(() => (options ? compressView(options, pick) : null), [options, pick]);

  // Compress: the tasks go, and the dialog shows the job from then on.
  const start = async () => {
    if (!view || !view.tasks.length || sending || refuseOnDemo()) return;
    setSending(true);
    setSendError(null);
    let resp: RespEnvelope | null = null;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = compressPayload(view.tasks); });
    } catch {
      // No answer: as below.
    }
    if (!mountedRef.current) return;
    setSending(false);
    const a = startAnswer(resp);
    if (a.kind === "job") {
      followJob(a.job);
      setPhase({ kind: "job" });
      onStarted?.();
    } else if (a.kind === "old") setPhase({ kind: "old" });
    else setSendError(() => (a.kind === "refused" ? asIs(a.said) : say("web.shell.connection.no_answer")));
  };

  const followed = useCompressJob(phase.kind === "job");

  // The dialog opens on "Working out the sizes…", with nothing to focus
  // but itself; once its buttons come (and once Compress has made way for
  // the job's Close) the focus goes to the first, Cancel or Close - unless
  // it was moved meanwhile.
  const titleRef = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    const card = titleRef.current?.closest<HTMLElement>(".pg-dlg");
    const at = document.activeElement;
    if (card && (at === card || at === document.body)) card.querySelector<HTMLElement>("[data-autofocus]")?.focus();
  }, [phase.kind]);

  return (
    <Modal labelledBy={titleId} busy={sending} wide className="cz-dlg" onClose={onClose}>
      <h2 ref={titleRef} id={titleId} className="pg-dlg-title">{t("web.files.compress.title", { count: asked.length })}</h2>

      {phase.kind === "estimating" && (
        <div className="cz-wait"><Spinner label={t("web.files.compress.estimating")} /></div>
      )}

      {phase.kind === "old" && (
        <>
          <p className="pg-dlg-text">{t("web.files.compress.needs_update")}</p>
          <div className="pg-dlg-actions">
            <button type="button" className="pg-btn quiet" onClick={onClose} data-autofocus>{t("common.close")}</button>
          </div>
        </>
      )}

      {phase.kind === "failed" && (
        <>
          <p className="pg-dlg-error" role="alert">{phase.text(t)}</p>
          <div className="pg-dlg-actions">
            <button type="button" className="pg-btn quiet" onClick={onClose} data-autofocus>{t("common.close")}</button>
            <button type="button" className="pg-btn primary" onClick={() => void estimate()}>{t("common.try_again")}</button>
          </div>
        </>
      )}

      {view && (
        <>
          {view.nothing && <p className="pg-dlg-text">{t("web.files.compress.nothing")}</p>}

          {/* The videos: one choice for them all. */}
          {view.videos.length > 0 && (
            <fieldset className="cz-group">
              <legend className="cz-head">{t("web.files.compress.videos")}</legend>
              {view.videos.map(({ choice, sums }) => {
                const rowId = `${radioName}-${choice}`;
                return (
                  <label key={choice} className="cz-choice">
                    <input
                      type="radio"
                      name={radioName}
                      checked={pick.video === choice}
                      onChange={() => setPick((p) => ({ ...p, video: choice }))}
                      aria-describedby={sums && sums.count > 0 ? rowId : undefined}
                      disabled={sending}
                    />
                    <span className="cz-choice-text">
                      <span className="cz-label">{t(cVideoLabel[choice])}</span>
                      <RowText sums={sums} id={rowId} />
                    </span>
                  </label>
                );
              })}
            </fieldset>
          )}

          {/* The photos: a switch for each way the device has. */}
          {view.photos.length > 0 && (
            <div className="cz-group">
              {view.photos.map(({ preset, on, sums }) => {
                const rowId = `${radioName}-${preset}`;
                return (
                  <label key={preset} className="cz-choice cz-switch">
                    <input
                      type="checkbox"
                      role="switch"
                      checked={on}
                      onChange={(e) => {
                        const value = e.currentTarget.checked;
                        setPick((p) => ({ ...p, photos: { ...p.photos, [preset]: value } }));
                      }}
                      aria-describedby={sums.count > 0 ? rowId : undefined}
                      disabled={sending}
                    />
                    <span className="cz-choice-text">
                      <span className="cz-label">{t(cPhotoLabel[preset])}</span>
                      <RowText sums={sums} id={rowId} />
                    </span>
                  </label>
                );
              })}
            </div>
          )}

          {/* What can't be made smaller, and why in the device's words. */}
          {view.notOffered.length > 0 && (
            <details className="cz-list">
              <summary>{t("web.files.compress.not_offered", { count: view.notOffered.length })}</summary>
              <ul>
                {view.notOffered.map((n) => (
                  <li key={n.path}>
                    <span className="cz-name" title={n.path}>{n.name}</span>
                    {n.reason && <span className="cz-why">{n.reason}</span>}
                  </li>
                ))}
              </ul>
            </details>
          )}

          {!view.nothing && (
            <div className="cz-foot">
              <p className="cz-total">{t("web.files.compress.total", { size: size(t, view.saved) })}</p>
              <p className="cz-warning">{t("web.files.compress.warning")}</p>
              {view.slow && <p className="cz-note">{t("web.files.compress.slow")}</p>}
            </div>
          )}

          {sendError && <p className="pg-dlg-error" role="alert">{sendError(t)}</p>}

          <div className="pg-dlg-actions">
            {view.nothing ? (
              <button type="button" className="pg-btn quiet" onClick={onClose} data-autofocus>{t("common.close")}</button>
            ) : (
              <>
                {/* Focus starts on Cancel: Enter right away keeps everything. */}
                <button type="button" className="pg-btn quiet" onClick={onClose} disabled={sending} data-autofocus>{t("common.cancel")}</button>
                <button
                  type="button"
                  className="pg-btn danger"
                  onClick={() => void start()}
                  disabled={sending || view.tasks.length === 0}
                  aria-busy={sending || undefined}
                >
                  {sending ? <Spinner label={t("web.files.compress.confirm")} /> : t("web.files.compress.confirm")}
                </button>
              </>
            )}
          </div>
        </>
      )}

      {phase.kind === "job" && <JobView followed={followed} onClose={onClose} />}
    </Modal>
  );
}

// The job: how far it is while the device works through it, and once
// over what it came to. Its words are one live region, so a screen
// reader hears each file done and the end.
function JobView({ followed, onClose }: { followed: ReturnType<typeof useCompressJob>; onClose: () => void }) {
  const t = useT();
  const labelId = useId();
  if (!followed) return null;
  const { job, gone } = followed;
  const over = jobOver(job, gone);
  const progress = jobProgress(job);
  const outcome = over ? jobOutcome(job, gone) : null;
  return (
    <>
      <div className="cz-job" role="status">
        {!outcome ? (
          <>
            <p id={labelId} className="cz-progress-text">{t("web.files.compress.progress", { done: progress.done, total: progress.total })}</p>
            <div
              className="cz-track"
              role="progressbar"
              aria-labelledby={labelId}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(progress.fraction * 100)}
            >
              <div className="cz-fill" style={{ width: `${progress.fraction * 100}%` }} />
            </div>
          </>
        ) : (
          <>
            {(outcome.compressed > 0 || outcome.failed.length === 0) && (
              <p className="cz-done">{t("web.files.compress.done", { count: outcome.compressed, size: size(t, outcome.saved) })}</p>
            )}
            {outcome.failed.length > 0 && (
              <details className="cz-list">
                <summary>{t("web.files.compress.failed", { count: outcome.failed.length })}</summary>
                <ul>
                  {outcome.failed.map((f) => (
                    <li key={f.path}>
                      <span className="cz-name" title={f.path}>{f.name}</span>
                      {f.message && <span className="cz-why">{f.message}</span>}
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </>
        )}
      </div>
      <div className="pg-dlg-actions">
        {/* Closed while it runs, the job goes on (Settings' Background
            work shows it). */}
        <button type="button" className={`pg-btn ${outcome ? "primary" : "quiet"}`} onClick={onClose} data-autofocus>
          {outcome ? t("common.done") : t("common.close")}
        </button>
      </div>
    </>
  );
}
