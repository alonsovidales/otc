// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #108: pick the piece of a video that actually goes into a post.
//
// The cut itself happens on the device (ffmpeg, at publish time - see
// social.NewPublication); this screen only ever produces a start/end pair.
// That's deliberate: a browser can't re-encode a video without dragging in
// a WASM ffmpeg build, and the device is already re-encoding social videos
// anyway, so sending two numbers instead of a freshly encoded file keeps
// the upload identical to what it was and the cut frame-accurate.
//
// Two sliders rather than a pair of custom drag handles on one track: they
// work with a keyboard and on a touch screen for free, and each one can
// scrub the preview to its own position as it moves, which is the part
// that actually makes trimming feel accurate.
//
// The viewer's Edit on a video (VideoEditor.tsx) is this same screen with
// the `cut` option: the cut is saved on the device then (EditVideo,
// videoEdit.ts' rules), so the bar has Save - on for a real cut of at
// least half a second - in place of Apply and Remove trim, the small print
// under the slider says the cut may start a little early, and Cancel (its
// ✕, Escape, Back) with the range changed lets the caller ask first, as
// the photo editor does. The caller's questions are its children, drawn
// over the panel; while one shows, the keys are its own. It also takes
// the keyboard's focus as it opens and keeps Tab inside, as the photo
// editor does over the viewer. The composer's trim is as it always was.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Trans, useT } from "../i18n";
import { keepTabInside } from "./focusTrap";
import { say, type UiText } from "./uiText";
import { canSaveCut, cutOf, type Cut } from "./videoEdit";
import { formatTimecode, type TrimRange } from "./videoTrim";
import { useBackLayer } from "./backStack";
import "./VideoTrimmer.css";

export type { TrimRange };

/** The viewer's cut (VideoEditor.tsx), in place of the composer's
 *  `value` and `onApply`. */
export type TrimCut = {
  /** Save, with the cut the handles chose (videoEdit.ts). */
  onSave: (cut: Cut) => void;
  /** Cancel - its ✕, Escape or Back - with the range changed: the
   *  caller asks first. With nothing changed it is onCancel. */
  onDiscard: () => void;
  /** A question of the caller's shows over the panel: the keys are
   *  the question's. */
  asking: boolean;
};

// Below this there's nothing left to watch, and ffmpeg's own output for a
// sub-frame cut isn't something worth publishing.
const cMinTrimSecs = 0.5;

// How long to wait for the browser to report the clip's length before
// giving up on it (see loadError below).
const cReadTimeoutMs = 20000;

type Props = {
  src: string;
  onCancel: () => void;
  // The cut's questions (VideoEditor.tsx), over the panel.
  children?: ReactNode;
} & (
  | {
      // The trim already applied to this file, if it's being edited again.
      value: TrimRange | null;
      onApply: (range: TrimRange | null) => void;
      cut?: undefined;
    }
  | { cut: TrimCut; value?: undefined; onApply?: undefined }
);

export default function VideoTrimmer({ src, value, onApply, onCancel, cut, children }: Props) {
  const t = useT();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [duration, setDuration] = useState<number | null>(null);
  // A browser that can't decode the clip doesn't necessarily fire `error`
  // on the element - some just sit at readyState 0 with no event at all
  // (hit exactly that while testing this). Without a deadline the panel
  // reads "Reading the clip…" forever and the only way out is Cancel,
  // which looks like the feature is broken rather than the video being
  // unplayable here.
  const [loadError, setLoadError] = useState<UiText | null>(null);
  const [start, setStart] = useState(value?.start ?? 0);
  const [end, setEnd] = useState(value?.end ?? 0);
  // Set while "Play cut" is running, so the timeupdate handler below knows
  // to stop at `end` instead of letting the rest of the clip play on.
  const playingCutRef = useRef(false);

  // The clip's length only exists once the browser has read its metadata,
  // and an end of 0 means "to the end" all the way through this feature
  // (see the proto's VideoTrim) - so that's what an untouched end stays,
  // and the slider shows the real duration in the meantime.
  const onLoadedMetadata = () => {
    const d = videoRef.current?.duration;
    if (!d || !isFinite(d)) return;
    setDuration(d);
    setEnd(prev => (prev > 0 ? Math.min(prev, d) : d));
  };
  // ...or before React listens: the viewer's cut renders this in its lazy
  // chunk's retry, which can pause before the commit, and on a video the
  // browser already holds (the viewer's own) the metadata, or the error,
  // came in that pause - an event React drops for an element not on the
  // page yet. Seen in Chrome: the clip read, "Reading the clip…" for good.
  // So it is also looked at once the element is on the page.
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.error) setLoadError(() => say("web.images.trim.unopenable"));
    else if (v.readyState >= HTMLMediaElement.HAVE_METADATA) onLoadedMetadata();
  }, [src]);

  // From the source it was given: the cut's comes from the viewer, which
  // may still be asking the device for it as the screen opens.
  useEffect(() => {
    if (duration !== null) return;
    const timer = setTimeout(
      () => setLoadError(() => say("web.images.trim.unreadable")),
      cReadTimeoutMs
    );
    return () => clearTimeout(timer);
  }, [duration, src]);

  const seek = (at: number) => {
    const v = videoRef.current;
    if (!v) return;
    playingCutRef.current = false;
    v.pause();
    v.currentTime = at;
  };

  const changeStart = (at: number) => {
    const max = (end || duration || 0) - cMinTrimSecs;
    const next = Math.max(0, Math.min(at, Math.max(0, max)));
    setStart(next);
    seek(next);
  };

  const changeEnd = (at: number) => {
    const next = Math.min(duration ?? at, Math.max(at, start + cMinTrimSecs));
    setEnd(next);
    seek(next);
  };

  const playCut = () => {
    const v = videoRef.current;
    if (!v) return;
    v.currentTime = start;
    playingCutRef.current = true;
    void v.play();
  };

  // Stopping the preview at the out point is what makes the chosen cut
  // legible - watching it run past the end tells you nothing about what
  // you actually selected.
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    const onTime = () => {
      if (!playingCutRef.current) return;
      const stopAt = end > 0 ? end : (duration ?? Infinity);
      if (v.currentTime >= stopAt) {
        v.pause();
        playingCutRef.current = false;
      }
    };
    v.addEventListener("timeupdate", onTime);
    return () => v.removeEventListener("timeupdate", onTime);
  }, [end, duration]);

  const effectiveEnd = end > 0 ? end : (duration ?? 0);
  const cutLength = Math.max(0, effectiveEnd - start);
  // A cut that keeps the whole clip is the same as no cut at all, and
  // sending it anyway would make the device re-encode the video to change
  // nothing about it (see shouldTrimForSocial on the Go side).
  const isWholeClip = duration !== null && start <= 0 && effectiveEnd >= duration - 0.05;

  // The viewer's cut, as the device takes it (videoEdit.ts): none until the
  // clip's length is known. A range that isn't the whole video is what
  // Cancel would lose.
  const chosen = cut && duration !== null ? cutOf({ start, end: effectiveEnd }, duration) : null;
  const changed = !!chosen && !chosen.whole;
  // Cancel: the composer's at once; the cut's asked first when changed.
  const cancel = () => {
    if (cut && changed) cut.onDiscard();
    else onCancel();
  };
  // Back is Cancel (backStack.ts); a question over the cut has its own
  // Back (Modal), and the one this asks keeps the screen.
  useBackLayer(true, () => {
    if (!cut || !changed) { onCancel(); return; }
    cut.onDiscard();
    return false;
  });
  const save = () => {
    if (!cut || !chosen || !canSaveCut(chosen)) return;
    // The question comes over a still picture, not the cut playing on.
    playingCutRef.current = false;
    videoRef.current?.pause();
    cut.onSave(chosen);
  };

  // The cut over the viewer: the keyboard's focus comes in, and goes back
  // to Edit as it closes - after keyboard use only, as the dialogs do;
  // Escape is Cancel and Tab stays inside, except while a question shows.
  const cutting = !!cut;
  const asking = !!cut?.asking;
  const cancelRef = useRef(cancel);
  useLayoutEffect(() => { cancelRef.current = cancel; });
  const [returnFocus] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });
  useEffect(() => {
    if (!cutting) return;
    rootRef.current?.focus({ preventScroll: true });
    return () => { if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true }); };
  }, [cutting, returnFocus]);
  useEffect(() => {
    if (!cutting || asking) return;
    const onKey = (e: KeyboardEvent) => {
      const root = rootRef.current;
      if (!root) return;
      if (e.key === "Escape") {
        e.preventDefault();
        cancelRef.current();
      } else if (e.key === "Tab") {
        keepTabInside(root, e);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [cutting, asking]);

  return (
    <div
      ref={rootRef}
      className="vt-backdrop"
      role="dialog"
      aria-modal="true"
      aria-label={t("web.images.trim.title")}
      tabIndex={cut ? -1 : undefined}
    >
      <div className="vt-panel">
        <div className="vt-header">
          <div className="vt-title">{t("web.images.trim.title")}</div>
          <button className="vt-close" onClick={cancel} aria-label={t("common.cancel")}>✕</button>
        </div>

        <video
          ref={videoRef}
          className="vt-video"
          src={src || undefined}
          onLoadedMetadata={onLoadedMetadata}
          onError={() => setLoadError(() => say("web.images.trim.unopenable"))}
          playsInline
          preload="metadata"
        />

        {loadError ? (
          <div className="vt-loading">
            <div>{loadError(t)}</div>
            <div className="vt-actions">
              <div className="vt-actions-spacer" />
              <button className="vt-btn" onClick={onCancel}>{t("common.close")}</button>
            </div>
          </div>
        ) : duration === null ? (
          <div className="vt-loading">{t("web.images.trim.reading")}</div>
        ) : (
          <>
            {/* One track, a handle at each end - the two values are
                read as a single span, which is what a trim actually is.
                Two overlaid range inputs rather than custom drag handles:
                keyboard and touch support come for free, and only the
                thumbs take pointer events so the one underneath stays
                reachable. */}
            <div className="vt-range-row">
              <span className="vt-time">{formatTimecode(start)}</span>
              <div className="vt-dual">
                <div className="vt-dual-rail" aria-hidden="true" />
                <div
                  className="vt-dual-sel"
                  aria-hidden="true"
                  style={{
                    left: `${(start / duration) * 100}%`,
                    width: `${(cutLength / duration) * 100}%`,
                  }}
                />
                <input
                  type="range"
                  className="vt-dual-input"
                  aria-label={t("web.images.trim.start")}
                  min={0}
                  max={duration}
                  step={0.1}
                  value={start}
                  onChange={e => changeStart(parseFloat(e.target.value))}
                />
                <input
                  type="range"
                  className="vt-dual-input"
                  aria-label={t("web.images.trim.end")}
                  min={0}
                  max={duration}
                  step={0.1}
                  value={effectiveEnd}
                  onChange={e => changeEnd(parseFloat(e.target.value))}
                />
              </div>
              <span className="vt-time">{formatTimecode(effectiveEnd)}</span>
            </div>

            <div className="vt-summary">
              <Trans
                k="web.images.trim.keeping"
                args={{ kept: formatTimecode(cutLength), total: formatTimecode(duration) }}
                parts={{ b: (s) => <strong>{s}</strong> }}
              />
            </div>
            {/* A copied stream starts at a keyframe (EditVideo). */}
            {cut && <p className="vt-note">{t("web.images.edit.video_cut_note")}</p>}

            <div className="vt-actions">
              <button className="vt-btn" onClick={playCut}>▶ {t("web.images.trim.play")}</button>
              <div className="vt-actions-spacer" />
              {value && (
                <button className="vt-btn" onClick={() => onApply?.(null)}>
                  {t("web.images.trim.remove")}
                </button>
              )}
              <button className="vt-btn" onClick={cancel}>{t("common.cancel")}</button>
              {cut ? (
                <button className="vt-btn vt-btn-primary" disabled={!chosen || !canSaveCut(chosen)} onClick={save}>
                  {t("common.save")}
                </button>
              ) : (
                <button
                  className="vt-btn vt-btn-primary"
                  disabled={isWholeClip || cutLength < cMinTrimSecs}
                  onClick={() => onApply?.({ start, end: effectiveEnd })}
                >
                  {t("common.apply")}
                </button>
              )}
            </div>
          </>
        )}
      </div>
      {children}
    </div>
  );
}
