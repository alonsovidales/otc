// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The full-screen photo and video viewer: the Images section's, and since
// it opens photos and videos from Files too, its own component, so both
// open them the same way - full-size image (pinch or trackpad zoom), a
// video streamed from the device, swiping or arrows between items, the
// Info panel (issue #41). The apps do the same with ImageModal.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { requestStreamURL, canStream } from "../net/media";
import type { RespEnvelope, FileExifInfo } from "../proto/messages";
import "./PhotoGallery.css";
import LowResBadge from "./LowResBadge";

// How long a video may show no sign of life before it's called stalled.
const cVideoStallMs = 30000;
// Steps closer together than this are a held arrow key or a burst of
// swipes: the full-size fetch waits this long and is skipped if the viewer
// has moved on by then.
const cRapidStepMs = 250;
// What Tab moves between inside the viewer.
const cFocusable = "button, [href], input, video[controls], iframe, [tabindex]:not([tabindex='-1'])";

/** One item: its path and mime, and its thumbnail (JPEG) if there is one. */
export type ViewerItem = { path: string; mime?: string; content?: Uint8Array | number[] | null; thumbURL?: string };

const isVideoFile = (f: { mime?: string }) => (f.mime || "").startsWith("video/");
const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content || !(content as ArrayLike<number>).length) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  return URL.createObjectURL(new Blob([u8 as BlobPart], { type: mime }));
};

export default function MediaViewer({ items, index, onIndexChange, onClose }: {
  items: ViewerItem[];
  index: number;
  onIndexChange: (i: number) => void;
  onClose: () => void;
}) {
  const [hiURL, setHiURL] = useState<string | null>(null);
  // A full-size photo (or a small video fetched whole) is a blob of
  // several MB: freed after the commit that took it off screen - the next
  // item, or closing. A streamed /media URL owns nothing to free.
  useEffect(() => () => { if (hiURL?.startsWith("blob:")) URL.revokeObjectURL(hiURL); }, [hiURL]);
  // Issue #106: why the opened video isn't playing, when it isn't. "codec"
  // means the browser said so (an HEVC recording some browsers can't
  // decode); "stalled" means nothing arrived for cVideoStallMs - a
  // transfer problem, not a codec one, and never reported as one.
  const [videoProblem, setVideoProblem] = useState<null | "codec" | "stalled">(null);
  const [infoOpen, setInfoOpen] = useState(false);
  const [infoLoading, setInfoLoading] = useState(false);
  const [infoData, setInfoData] = useState<FileExifInfo | null>(null);
  const [zoomScale, setZoomScale] = useState(1);
  // Whether the full-size fetch is still going (for the Low res badge).
  const [hiLoading, setHiLoading] = useState(false);
  const imgRef = useRef<HTMLImageElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);

  // The keyboard's focus moves into the viewer, so Space or Enter can't
  // press what is behind it (the photo it was opened from), and goes back
  // there when it closes - after keyboard use only, as dialogs do.
  useEffect(() => {
    const el = document.activeElement;
    const back = el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
    if (!boxRef.current?.contains(el)) boxRef.current?.focus({ preventScroll: true });
    return () => { if (back?.isConnected) back.focus({ preventScroll: true }); };
  }, []);

  // Which item the viewer is on: every change of item (and closing) moves
  // it on, and a full-size image, stream URL or info that arrives for an
  // earlier one is dropped - stepping on before one had loaded used to
  // show the next one and then the previous one's image over it.
  const viewGenRef = useRef(0);
  useEffect(() => () => { viewGenRef.current += 1; }, []);
  // When the previous item was opened (see cRapidStepMs).
  const lastStepRef = useRef(0);

  const item = items[index];

  // A caller that passes no thumbURL gets one made here from the item's
  // bytes: once per item, freed with it, rather than one per render (and
  // every pinch or ctrl+wheel step of a zoom is a render). A layout effect,
  // so no frame is painted without it. A caller's own thumbURL is the
  // caller's to free.
  const [ownThumb, setOwnThumb] = useState("");
  const itemThumbURL = item?.thumbURL;
  const itemContent = item?.content;
  useLayoutEffect(() => {
    if (itemThumbURL || !itemContent) { setOwnThumb(""); return; }
    const u = bytesToURL(itemContent);
    setOwnThumb(u);
    return () => { if (u) URL.revokeObjectURL(u); };
  }, [itemThumbURL, itemContent]);

  useEffect(() => {
    const gen = ++viewGenRef.current;
    const current = () => gen === viewGenRef.current;
    setHiURL(null);
    setVideoProblem(null);
    setInfoOpen(false);
    setInfoData(null);
    setZoomScale(1);
    if (!item) return;
    setHiLoading(true);
    const fetchFull = async () => {
      try {
        // Issue #110: a video streams from a URL; the device declines
        // small clips, which fall through to the whole-file fetch.
        if (canStream(item.mime)) {
          const streamURL = await requestStreamURL({ path: item.path });
          if (!current()) return;
          if (streamURL) { setHiURL(streamURL); return; }
        }
        const resp = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: item.path } };
        });
        if (!current()) return;
        if (resp.payload?.$case === "respFile") {
          const full = resp.payload.respFile!;
          setHiURL(bytesToURL(full.content, full.mime || "image/jpeg"));
        }
      } catch {
        // the thumbnail stays
      } finally {
        if (current()) setHiLoading(false);
      }
    };
    // A held arrow key steps about 30 times a second, and every full-size
    // GetFile it sent ran to the end on the device (a HEIC decode and a
    // share of its memory budget each) only to be dropped here, while the
    // photo the user stopped on queued behind them - a request already
    // sent can't be called back. The first open and a deliberate step
    // still fetch at once; of a burst, only its first step and the item
    // it ends on fetch.
    const now = Date.now();
    const rapid = now - lastStepRef.current < cRapidStepMs;
    lastStepRef.current = now;
    if (!rapid) { void fetchFull(); return; }
    const timer = setTimeout(() => void fetchFull(), cRapidStepMs);
    return () => clearTimeout(timer);
  }, [item?.path, item?.mime]); // eslint-disable-line react-hooks/exhaustive-deps

  // Issue #41: camera/EXIF metadata, computed on the device from the file.
  const openInfo = useCallback(async () => {
    if (!item) return;
    const gen = viewGenRef.current;
    setInfoOpen(true);
    setInfoLoading(true);
    setInfoData(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetFileInfo", reqGetFileInfo: { path: item.path } };
      });
      if (gen !== viewGenRef.current) return;
      if (resp.payload?.$case === "respFileInfo") setInfoData(resp.payload.respFileInfo);
    } finally {
      if (gen === viewGenRef.current) setInfoLoading(false);
    }
  }, [item]);
  const closeInfo = useCallback(() => { setInfoOpen(false); setInfoData(null); }, []);

  // Keyboard: Escape closes, arrows page, Tab stays in the viewer.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight" && index < items.length - 1) onIndexChange(index + 1);
      if (e.key === "ArrowLeft" && index > 0) onIndexChange(index - 1);
      const box = boxRef.current;
      if (!box) return;
      // Space on the viewer itself would scroll the page behind it.
      if (e.key === " " && e.target === box) e.preventDefault();
      if (e.key !== "Tab") return;
      const all = [...box.querySelectorAll<HTMLElement>(cFocusable)].filter((el) => el.offsetParent !== null);
      const active = document.activeElement;
      if (!all.length) { e.preventDefault(); box.focus(); return; }
      const first = all[0];
      const last = all[all.length - 1];
      if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (e.shiftKey && (active === first || active === box)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, items.length, onIndexChange, onClose]);

  // Swipe to page (issue #18) and pinch to zoom (issue #36): a two-finger
  // pinch on touch devices, ctrl+wheel (a trackpad pinch) on desktop.
  const modalTouchStartX = useRef<number | null>(null);
  const pinchStartDist = useRef<number | null>(null);
  const pinchStartScale = useRef(1);
  const touchDistance = (t: React.TouchList) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
  const onModalTouchStart = (e: React.TouchEvent) => {
    if (e.touches.length === 2) {
      pinchStartDist.current = touchDistance(e.touches);
      pinchStartScale.current = zoomScale;
    } else {
      modalTouchStartX.current = e.touches[0].clientX;
    }
  };
  const onModalTouchMove = (e: React.TouchEvent) => {
    if (e.touches.length === 2 && pinchStartDist.current) {
      e.preventDefault();
      const scale = pinchStartScale.current * (touchDistance(e.touches) / pinchStartDist.current);
      setZoomScale(Math.min(4, Math.max(1, scale)));
    }
  };
  const onModalTouchEnd = (e: React.TouchEvent) => {
    if (pinchStartDist.current != null) { pinchStartDist.current = null; return; }
    if (modalTouchStartX.current == null || zoomScale !== 1) return;
    const dx = e.changedTouches[0].clientX - modalTouchStartX.current;
    if (dx < -30 && index < items.length - 1) onIndexChange(index + 1);
    else if (dx > 30 && index > 0) onIndexChange(index - 1);
    modalTouchStartX.current = null;
  };
  const onModalWheel = (e: React.WheelEvent) => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    setZoomScale(s => Math.min(4, Math.max(1, s - e.deltaY * 0.01)));
  };

  if (!item) return null;
  const name = items[index].path.split("/").pop();
  return (
    <div className="pg-modal" onClick={onClose}>
      <div
        ref={boxRef}
        className="pg-modal-inner"
        role="dialog"
        aria-modal="true"
        aria-label={name}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Issue #130: a header row of its own, like the mobile
            viewers, rather than two glyphs floated over the picture's
            top-right corner - those vanished against a bright sky and,
            on a phone-width browser, sat past the right edge of the
            screen entirely, which is why "the web can't show the
            metadata": the button that opens it was never in view. */}
        <div className="pg-modal-hdr">
          {/* Issue #41: camera/EXIF metadata + location, on demand. */}
          <button className={"pg-info-btn" + (infoOpen ? " on" : "")} title="More info" onClick={infoOpen ? closeInfo : openInfo}>
            <span className="pg-info-glyph">i</span> Info
          </button>
          <span className="pg-modal-title">{name}</span>
          <button className="pg-close" title="Close" onClick={onClose}>×</button>
        </div>
        <div
          className="pg-modal-imgwrap"
          onTouchStart={onModalTouchStart}
          onTouchMove={onModalTouchMove}
          onTouchEnd={onModalTouchEnd}
          onWheel={onModalWheel}
        >
          {(() => {
            const f = items[index];
            const thumb = f.thumbURL || ownThumb; // always a JPEG thumbnail
            // Issue #106: a video opens as something you can actually
            // play. Until the full file arrives (hiURL), its own
            // thumbnail stands in - the same still the grid shows -
            // rather than an empty black box.
            if (isVideoFile(f)) {
              if (!hiURL) return <img src={thumb} alt={f.path} />;
              if (videoProblem) {
                return (
                  <div className="pg-video-unplayable">
                    <img src={thumb} alt={f.path} />
                    {videoProblem === "codec" ? (
                      <>
                        <p>This browser can't play this video.</p>
                        <p className="pg-video-unplayable-hint">
                          It may be recorded in HEVC, which not every browser can decode -
                          Safari handles it, and recent Chrome does on hardware that
                          supports it.
                        </p>
                      </>
                    ) : (
                      <>
                        <p>This video is taking too long to load.</p>
                        <p className="pg-video-unplayable-hint">
                          Nothing has arrived from your device for a while - it may be
                          busy or hard to reach right now. Trying again usually works.
                        </p>
                      </>
                    )}
                    <a className="pg-video-download" href={hiURL} download={f.path.split("/").pop()}>
                      Download it
                    </a>
                  </div>
                );
              }
              return (
                <video
                  src={hiURL}
                  controls
                  autoPlay
                  playsInline
                  // The browser is the only thing that actually knows
                  // why a video won't play, so ask it rather than
                  // inferring: only a decode failure or an outright
                  // rejection of the source is a codec problem.
                  // Anything else (a network error, a slow transfer)
                  // is reported as what it is.
                  onError={(e) => {
                    const code = (e.currentTarget as HTMLVideoElement).error?.code;
                    setVideoProblem(
                      code === MediaError.MEDIA_ERR_DECODE ||
                      code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
                        ? "codec"
                        : "stalled"
                    );
                  }}
                  onLoadedMetadata={(e) => {
                    if ((e.currentTarget as HTMLVideoElement).videoWidth === 0) setVideoProblem("codec");
                  }}
                  ref={(el) => {
                    if (!el) return;
                    // A stall is "no sign of life for a while", not
                    // "not finished yet" - so every event that proves
                    // something is still happening pushes the
                    // deadline back, and a video that streams in
                    // slowly is left alone to do it.
                    let timer: ReturnType<typeof setTimeout>;
                    const giveUp = () => { if (el.readyState === 0) setVideoProblem("stalled"); };
                    const arm = () => {
                      clearTimeout(timer);
                      timer = setTimeout(giveUp, cVideoStallMs);
                    };
                    arm();
                    for (const ev of ["progress", "loadedmetadata", "loadeddata", "canplay", "playing"]) {
                      el.addEventListener(ev, arm);
                    }
                    el.addEventListener("loadedmetadata", () => clearTimeout(timer), { once: true });
                  }}
                />
              );
            }
            return (
              <>
                <img
                  ref={imgRef}
                  src={hiURL || thumb}
                  alt={f.path}
                  style={{ transform: `scale(${zoomScale})`, transition: pinchStartDist.current ? "none" : "transform 0.15s ease-out" }}
                />
                {!hiURL && thumb && <LowResBadge imgRef={imgRef} loading={hiLoading} />}
              </>
            );
          })()}
        </div>
        {/* After the picture, which would paint over them, and before the
            Info panel, which covers them. */}
        {index > 0 && <button className="pg-nav left" onClick={() => onIndexChange(index - 1)} aria-label="Previous">‹</button>}
        {index < items.length - 1 && <button className="pg-nav right" onClick={() => onIndexChange(index + 1)} aria-label="Next">›</button>}

        {infoOpen && (
          <div className="pg-info-panel" onClick={(e) => e.stopPropagation()}>
            <div className="pg-info-hdr">
              <span>More info</span>
              <button className="pg-info-close" onClick={closeInfo}>×</button>
            </div>
            {infoLoading ? (
              <div className="pg-info-loading">Loading…</div>
            ) : !infoData ? (
              <div className="pg-info-loading">No metadata found</div>
            ) : (
              <div className="pg-info-body">
                {(infoData.cameraMake || infoData.cameraModel) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Camera</span>
                    <span>{[infoData.cameraMake, infoData.cameraModel].filter(Boolean).join(" ")}</span>
                  </div>
                )}
                {infoData.takenAt && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Taken</span>
                    <span>{infoData.takenAt.toLocaleString()}</span>
                  </div>
                )}
                {!!(infoData.width && infoData.height) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Dimensions</span>
                    <span>{infoData.width} × {infoData.height}</span>
                  </div>
                )}
                {infoData.exposureTime && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Exposure</span>
                    <span>{infoData.exposureTime}</span>
                  </div>
                )}
                {infoData.fNumber && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Aperture</span>
                    <span>{infoData.fNumber}</span>
                  </div>
                )}
                {!!infoData.iso && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">ISO</span>
                    <span>{infoData.iso}</span>
                  </div>
                )}
                {infoData.focalLength && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Focal length</span>
                    <span>{infoData.focalLength}</span>
                  </div>
                )}
                {(infoData.city || infoData.country) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">Location</span>
                    <span>{[infoData.city, infoData.country].filter(Boolean).join(", ")}</span>
                  </div>
                )}
                {infoData.hasGps && (
                  <iframe
                    className="pg-info-map"
                    title="Photo location"
                    loading="lazy"
                    src={`https://www.openstreetmap.org/export/embed.html?bbox=${infoData.longitude - 0.02}%2C${infoData.latitude - 0.02}%2C${infoData.longitude + 0.02}%2C${infoData.latitude + 0.02}&layer=mapnik&marker=${infoData.latitude}%2C${infoData.longitude}`}
                  />
                )}
                {!infoData.cameraMake && !infoData.cameraModel && !infoData.hasGps && !infoData.exposureTime && (
                  <div className="pg-info-loading">No EXIF metadata in this file</div>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
