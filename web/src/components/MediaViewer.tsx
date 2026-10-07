// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The full-screen photo and video viewer: the Images section's, and since
// it opens photos and videos from Files too, its own component, so both
// open them the same way - full-size image (pinch or trackpad zoom), a
// video streamed from the device, swiping or arrows between items, the
// Info panel (issue #41), Download. The apps do the same with ImageModal.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import { requestStreamURL, canStream } from "../net/media";
import type { ReqEnvelope, RespEnvelope, FileExifInfo } from "../proto/messages";
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

// ---- Download ---------------------------------------------------------------
// The Download button saves the original file - never the thumbnail on
// screen nor the JPEG a HEIC is shown as - under its own name.
//
// A video the device streams (ReqGetMediaURL answers a URL: the big ones)
// is saved by the browser straight from that URL, so it goes to disk as it
// arrives and never sits in this page's memory. The URL is on the page's
// own origin (the device at home, its subdomain through the bridge), which
// is what makes the link's download attribute count; ?download=1 has the
// device also answer it as an attachment named after the file - devices
// before that, and the bridge, ignore it, and the attribute does it alone.
// A new link per click: the one the player got may be near its hour.
//
// Everything else - photos, and clips too short for the device to stream -
// is read in 4 MB pieces (ReadFile) and saved from a blob typed
// application/octet-stream: saved, never rendered (Files' security
// advisory: no HTML or SVG shown in the app's origin).

// How long the button stays busy once the browser has the download, so a
// double click doesn't start a second one.
const cSaveHoldMs = 2000;
// How long a blob handed to the browser is kept (FileSaver.js's figure):
// Safari on iOS reads it only once its sheet is answered.
const cBlobKeepMs = 40000;
const cReadPiece = 4 << 20;
// How long a failed download's note stays up.
const cSaveErrorMs = 8000;

type Saving = { phase: "start" } | { phase: "read"; done: number; total: number } | { phase: "handed" };

const leafName = (path: string) => path.split("/").pop() || "download";

// The downloads under way, by path, and the note of the last one that
// failed. Here rather than in a viewer: closing one unmounts it while its
// downloads go on, so a viewer opened again on the same file shows how far
// its download got and refuses a second, and a failure is told wherever
// the user is - in the viewer if one is open, over the page if not (page:
// until a viewer opens and shows it).
type Downloads = {
  saving: Readonly<Record<string, Saving>>;
  failed: { path: string; seq: number; page: boolean } | null;
};
let downloads: Downloads = { saving: {}, failed: null };
// How many viewers are on screen (none: a failure's note goes over the page).
let viewersOpen = 0;
const dlListeners = new Set<() => void>();
const subscribeDownloads = (l: () => void) => {
  dlListeners.add(l);
  return () => { dlListeners.delete(l); };
};
const getDownloads = () => downloads;
function setDownloads(next: Downloads) {
  downloads = next;
  syncPageNote();
  dlListeners.forEach((l) => l());
}
function showSaving(path: string, s: Saving | null) {
  const saving = { ...downloads.saving };
  if (s) saving[path] = s; else delete saving[path];
  setDownloads({ ...downloads, saving });
}
let failSeq = 0;
function showFailed(path: string | null) {
  if (!path) { setDownloads({ ...downloads, failed: null }); return; }
  const seq = ++failSeq;
  setDownloads({ ...downloads, failed: { path, seq, page: viewersOpen === 0 } });
  setTimeout(() => { if (downloads.failed?.seq === seq) showFailed(null); }, cSaveErrorMs);
}
const failedText = (path: string) => `Couldn't download ${leafName(path)}. Check the connection to your device and try again.`;

// The note over the page, while no viewer is open to show it: the viewer's
// own note, outside React (nothing of this file is mounted then).
let pageNote: HTMLElement | null = null;
function syncPageNote() {
  const f = viewersOpen === 0 && downloads.failed?.page ? downloads.failed : null;
  if (pageNote && pageNote.dataset.seq === String(f?.seq)) return;
  pageNote?.remove();
  pageNote = null;
  if (!f) return;
  const note = document.createElement("div");
  note.className = "pg-modal-toast pg-modal-toast-page";
  note.setAttribute("role", "alert");
  note.dataset.seq = String(f.seq);
  const text = document.createElement("span");
  text.textContent = failedText(f.path);
  const close = document.createElement("button");
  close.type = "button";
  close.className = "pg-modal-toast-close";
  close.setAttribute("aria-label", "Dismiss");
  close.textContent = "×";
  close.addEventListener("click", () => showFailed(null));
  note.append(text, close);
  document.body.appendChild(note);
  pageNote = note;
}

// A /media/<token> URL made absolute as net/media.ts does: against the
// page, or the device a native app's copy of this page talks to.
function absoluteMediaURL(url: string): URL {
  let base = location.href;
  const endpoint = window.__OTC_CONFIG?.endpoint;
  if (endpoint) {
    try {
      const u = new URL(endpoint);
      u.protocol = u.protocol === "wss:" ? "https:" : "http:";
      base = u.origin;
    } catch { /* the page's own */ }
  }
  return new URL(url, base);
}

// Hands href to the browser to save as name.
function saveAs(href: string, name: string) {
  const a = document.createElement("a");
  a.href = href;
  a.download = name;
  // The download attribute counts on the page's own origin only; another
  // origin's file would be opened instead - in a tab of its own, never
  // over the app.
  if (new URL(href, location.href).origin !== location.origin) {
    a.target = "_blank";
    a.rel = "noopener";
  }
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// The original bytes in pieces of at most 4 MB, as Files' readAll reads
// them, telling progress how far it got. Each piece looks the path up
// again, so a file replaced while it is read starts over once, for the
// new content. Null when the device stopped answering.
async function readOriginal(path: string, progress: (done: number, total: number) => void): Promise<Uint8Array[] | null> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const parts: Uint8Array[] = [];
    let content = "";
    let offset = 0;
    let total = -1;
    let changed = false;
    while (total < 0 || offset < total) {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqReadFile", reqReadFile: { path, hash: "", offset: BigInt(offset), length: cReadPiece } };
      });
      if (resp.payload?.$case !== "respFileChunk") return null;
      const chunk = resp.payload.respFileChunk;
      if (total >= 0 && (chunk.hash !== content || Number(chunk.size) !== total)) { changed = true; break; }
      content = chunk.hash;
      total = Number(chunk.size);
      if (chunk.data.length === 0 && offset < total) return null;
      parts.push(chunk.data);
      offset += chunk.data.length;
      progress(offset, total);
    }
    if (!changed) return parts;
  }
  return null;
}

// Saves the item's original file. One at a time per path: the store says
// so before anything is awaited, so a double click, or a click in a viewer
// opened again while the first goes on, doesn't start a second.
async function startDownload(it: ViewerItem) {
  const path = it.path;
  if (downloads.saving[path]) return;
  const name = leafName(path);
  if (downloads.failed?.path === path) showFailed(null);
  showSaving(path, { phase: "start" });
  let handed = false;
  try {
    let url = "";
    if (canStream(it.mime)) {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqGetMediaUrl", reqGetMediaUrl: { path, pubUuid: "", hash: "" } };
      });
      if (resp.payload?.$case === "respMediaUrl") url = resp.payload.respMediaUrl.url;
      // A failure here is a failure: falling back would read a long
      // video whole into memory. Only a device too old to stream at all
      // reads it in pieces.
      else if (resp.errorCode !== "unknown_payload") throw new Error(resp.errorMessage);
    }
    if (url) {
      const link = absoluteMediaURL(url);
      link.searchParams.set("download", "1");
      saveAs(link.href, name);
    } else {
      const parts = await readOriginal(path, (done, total) => showSaving(path, { phase: "read", done, total }));
      if (!parts) throw new Error("no answer");
      const blob = URL.createObjectURL(new Blob(parts as BlobPart[], { type: "application/octet-stream" }));
      saveAs(blob, name);
      setTimeout(() => URL.revokeObjectURL(blob), cBlobKeepMs);
    }
    handed = true;
  } catch {
    // the note below
  }
  if (handed) {
    showSaving(path, { phase: "handed" });
    setTimeout(() => showSaving(path, null), cSaveHoldMs);
  } else {
    showSaving(path, null);
    showFailed(path);
  }
}

const DownloadGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19.5h14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const DoneGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path d="M5 12.5 9.5 17 19 7.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

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

  // Download (see the top of the file), in the module's store: by path, so
  // the button shows the item on screen, and kept when the viewer closes.
  const { saving, failed: saveError } = useSyncExternalStore(subscribeDownloads, getDownloads);
  useEffect(() => {
    viewersOpen += 1;
    // A note over the page moves in here, and stays with the viewers.
    if (downloads.failed?.page) setDownloads({ ...downloads, failed: { ...downloads.failed, page: false } });
    else syncPageNote();
    return () => { viewersOpen -= 1; syncPageNote(); };
  }, []);
  const download = useCallback(() => {
    const it = items[index];
    if (it) void startDownload(it);
  }, [items, index]);

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
  const save = saving[item.path];
  const savePct = save?.phase === "read" && save.total > 0 ? Math.floor((save.done / save.total) * 100) : null;
  const saveLabel = !save ? "Download"
    : save.phase === "start" ? "Preparing…"
    : save.phase === "read" ? `Downloading ${savePct ?? 0}%`
    : "Started";
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
          {/* The original file, under its own name. Busy rather than
              disabled while it starts, so the keyboard's focus stays. */}
          <button
            type="button"
            className={"pg-modal-dl" + (save ? " busy" : "")}
            title={save ? saveLabel : "Download the original"}
            aria-label={saveLabel}
            aria-disabled={save ? true : undefined}
            onClick={download}
          >
            <span className="pg-modal-dl-glyph">
              {!save ? <DownloadGlyph /> : save.phase === "handed" ? <DoneGlyph /> : <span className="pg-modal-dl-spin" />}
            </span>
            <span className="pg-modal-dl-text">{saveLabel}</span>
            {savePct != null && <span className="pg-modal-dl-pct" aria-hidden="true">{savePct}%</span>}
          </button>
          <span className="pg-modal-sr" role="status">
            {save ? (save.phase === "handed" ? `${name}: download started` : `Downloading ${name}`) : ""}
          </span>
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

        {saveError && (
          <div className="pg-modal-toast" role="alert" key={saveError.seq}>
            <span>{failedText(saveError.path)}</span>
            <button type="button" className="pg-modal-toast-close" aria-label="Dismiss" onClick={() => showFailed(null)}>×</button>
          </div>
        )}
      </div>
    </div>
  );
}
