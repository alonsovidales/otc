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
import { useEffect, useRef, useState } from "react";
import { Trans, useT } from "../i18n";
import { say, type UiText } from "./uiText";
import { formatTimecode, type TrimRange } from "./videoTrim";
import "./VideoTrimmer.css";

export type { TrimRange };

// Below this there's nothing left to watch, and ffmpeg's own output for a
// sub-frame cut isn't something worth publishing.
const cMinTrimSecs = 0.5;

// How long to wait for the browser to report the clip's length before
// giving up on it (see loadError below).
const cReadTimeoutMs = 20000;

type Props = {
  src: string;
  // The trim already applied to this file, if it's being edited again.
  value: TrimRange | null;
  onApply: (range: TrimRange | null) => void;
  onCancel: () => void;
};

export default function VideoTrimmer({ src, value, onApply, onCancel }: Props) {
  const t = useT();
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

  useEffect(() => {
    if (duration !== null) return;
    const timer = setTimeout(
      () => setLoadError(() => say("web.images.trim.unreadable")),
      cReadTimeoutMs
    );
    return () => clearTimeout(timer);
  }, [duration]);

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

  return (
    <div className="vt-backdrop" role="dialog" aria-modal="true" aria-label={t("web.images.trim.title")}>
      <div className="vt-panel">
        <div className="vt-header">
          <div className="vt-title">{t("web.images.trim.title")}</div>
          <button className="vt-close" onClick={onCancel} aria-label={t("common.cancel")}>✕</button>
        </div>

        <video
          ref={videoRef}
          className="vt-video"
          src={src}
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

            <div className="vt-actions">
              <button className="vt-btn" onClick={playCut}>▶ {t("web.images.trim.play")}</button>
              <div className="vt-actions-spacer" />
              {value && (
                <button className="vt-btn" onClick={() => onApply(null)}>
                  {t("web.images.trim.remove")}
                </button>
              )}
              <button className="vt-btn" onClick={onCancel}>{t("common.cancel")}</button>
              <button
                className="vt-btn vt-btn-primary"
                disabled={isWholeClip || cutLength < cMinTrimSecs}
                onClick={() => onApply({ start, end: effectiveEnd })}
              >
                {t("common.apply")}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
