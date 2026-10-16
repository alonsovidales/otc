// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The full-screen photo and video viewer: the Images section's, and since
// it opens photos and videos from Files too, its own component, so both
// open them the same way - full-size image (pinch or trackpad zoom), a
// video streamed from the device, swiping or arrows between items, the
// Info panel (issue #41), Download, and Delete where the caller allows it
// (issue #199). The apps do the same with ImageModal.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { dateTimeFields, fmtDate, useT } from "../i18n";
import { useWS } from "../net/useWS";
import { requestStreamURL, canStream } from "../net/media";
import type { ReqEnvelope, RespEnvelope, FileExifInfo } from "../proto/messages";
import "./PhotoGallery.css";
import LowResBadge from "./LowResBadge";
import { usePageRetry } from "./usePageRetry";
import { startDownload, readFileInPieces, leafName, useDownloadState, useDownloadFailure, useDownloadHost } from "./mediaDownload";
import { DownloadButton, DownloadNote } from "./DownloadButton";
import { ConfirmDialog } from "./ConfirmDialog";
import type { UiText } from "./uiText";
import { copiesChoice, delFileFields, deleteOutcome, refusedText } from "./photoCopies";
import { useDemo } from "./useDemo";

// How long a video may show no sign of life before it's called stalled.
const cVideoStallMs = 30000;
// Steps closer together than this are a held arrow key or a burst of
// swipes: the full-size fetch waits this long and is skipped if the viewer
// has moved on by then.
const cRapidStepMs = 250;
// What Tab moves between inside the viewer. A video's controls and the
// map's links are stops of their own that can't be focused from here, and
// while one has focus the video or the map is the active element.
const cFocusable = "button, [href], input, video[controls], iframe, [tabindex]:not([tabindex='-1'])";
// Whether el has stops of its own (above).
const hasInnerStops = (el: Element) => el instanceof HTMLVideoElement || el instanceof HTMLIFrameElement;

/** One item: its path and mime, and its thumbnail (JPEG) if there is one.
 *  Issue #201: the content hash it was shown with (sent with a delete, so
 *  a path holding other content by then is refused, not deleted), and for
 *  Images how many paths hold that content (File.copies; unset where the
 *  device didn't say). */
export type ViewerItem = {
  path: string;
  mime?: string;
  content?: Uint8Array | number[] | null;
  thumbURL?: string;
  hash?: string;
  copies?: number;
};

const isVideoFile = (f: { mime?: string }) => (f.mime || "").startsWith("video/");
const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content || !(content as ArrayLike<number>).length) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  return URL.createObjectURL(new Blob([u8 as BlobPart], { type: mime }));
};

// Issue #199: deletes one file on the device (DelFile): no error once it
// is gone, else what the confirmation says instead - the device keeps
// everything in an upload-only folder (error_code "upload_only"), and any
// other refusal or no answer is a failure to try again. Issue #201: with
// the hash it was shown with, and every copy of it when `every` (Images,
// for an item with copies); "every copy" refused for a copy in an
// upload-only folder ("copy_upload_only") and a path that holds other
// content now ("changed") are said in the device's own words, in "Delete
// failed: …" (refusedText) as the phones say them.
async function deleteOnDevice(it: ViewerItem, every: boolean): Promise<{ error: UiText | null; changed: string | null }> {
  try {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      e.payload = { $case: "reqDelFile", reqDelFile: delFileFields(it, every) };
    });
    const outcome = deleteOutcome(resp.payload?.$case === "respAck" && resp.payload.respAck.ok, resp.errorCode);
    if (outcome === "deleted") return { error: null, changed: null };
    if (outcome === "upload_only") return { error: (t) => t("web.images.delete.upload_only", { count: 1 }), changed: null };
    const said = resp.errorMessage;
    if (outcome === "changed") {
      return { error: (t) => (said ? refusedText(t, said) : t("web.images.delete.changed", { count: 1 })), changed: said };
    }
    if (outcome === "copy_upload_only" && said) return { error: (t) => refusedText(t, said), changed: null };
  } catch {
    // No connection: as a refusal.
  }
  return { error: (t) => t("web.images.delete.failed", { count: 1 }), changed: null };
}

// The Delete button's trash can (Images' selection bar draws the same).
const TrashGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path
      d="M4.5 7h15M9.5 7V5.5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1V7M6.5 7l.85 12.1a1.5 1.5 0 0 0 1.5 1.4h6.3a1.5 1.5 0 0 0 1.5-1.4L17.5 7M10 11v5.5M14 11v5.5"
      fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"
    />
  </svg>
);

// Download: the original file, under its own name (mediaDownload.ts).
// Keyed by path, so a viewer opened again on the same file shows how far
// its download got.
function downloadItem(it: ViewerItem) {
  const path = it.path;
  void startDownload({
    key: path,
    name: leafName(path),
    stream: canStream(it.mime) ? { path } : null,
    read: async (progress) => (await readFileInPieces(path, progress)) as BlobPart[] | null,
  });
}

export default function MediaViewer({ items, index, onIndexChange, onClose, onDeleted, onDeleteChanged, deleteBlocked, inCollection = false }: {
  items: ViewerItem[];
  index: number;
  onIndexChange: (i: number) => void;
  onClose: () => void;
  /** Issue #199: with it, the header has Delete. Called once the device
   *  has deleted the item: the caller takes it out of `items` and, in the
   *  same update, moves `index` on (the item after it, the one before at
   *  the end) or closes the viewer when none is left. */
  onDeleted?: (item: ViewerItem) => void;
  /** Issue #201: the device refused to delete the item because its path
   *  holds other content now ("changed"; `said`: its words, "" if none).
   *  The caller closes the viewer, says so in those words and lists its
   *  photos or files again (Images and Files, as the phones do); without
   *  a caller that closes it, the dialog shows the words. */
  onDeleteChanged?: (item: ViewerItem, said: string) => void;
  /** Why the item can't be deleted (an upload-only folder), when the
   *  caller knows: Delete is greyed out, with this as its tooltip. */
  deleteBlocked?: (item: ViewerItem) => UiText | null;
  /** Opened inside a collection: the confirmation says the item goes
   *  from the device, not just from the collection. */
  inCollection?: boolean;
}) {
  const t = useT();
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
  // there when it closes - after keyboard use only, as dialogs do. Not
  // when the viewer closed because its last item was deleted: what opened
  // it was that item's tile or row, on its way out, and the caller puts
  // the focus somewhere that stays.
  const restoreFocusRef = useRef(true);
  useEffect(() => {
    const el = document.activeElement;
    const back = el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
    if (!boxRef.current?.contains(el)) boxRef.current?.focus({ preventScroll: true });
    return () => { if (restoreFocusRef.current && back?.isConnected) back.focus({ preventScroll: true }); };
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

  // The grids hand over their tiles' small thumbnails (release 111: 400
  // px on the shorter side), fine while the full size loads. Where the
  // thumbnail is what stays on screen - the full photo or video couldn't
  // be fetched, a photo the browser can't decode, a video it can't play -
  // the item's path is `stuck`, and the big thumbnail (its longest side
  // 1000 px, GetThumbnails without small_thumbnails) is asked for and
  // shown instead. The viewer's own URL, never the caller's: the two
  // sizes don't share a cache. Freed after the commit that replaced it.
  const [stuck, setStuck] = useState<string | null>(null);
  const [bigThumb, setBigThumb] = useState<{ path: string; url: string } | null>(null);
  useEffect(() => () => { if (bigThumb) URL.revokeObjectURL(bigThumb.url); }, [bigThumb]);
  const itemPath = item?.path;
  const thumbStays = itemPath != null && stuck === itemPath;
  // The full size usually fails because the connection did (a dropped
  // socket, the bridge not reaching the device), and then so does this
  // request. It is asked again like a grid's page (usePageRetry: 1 s
  // doubling to 10 s, at once when the device answers again or the
  // browser is back online) for as long as the viewer stays on the item,
  // so it never ends up with only the small tile scaled up.
  const { tick: bigRetryTick, failed: bigFailed, reset: bigReset } = usePageRetry();
  useEffect(() => {
    if (!thumbStays || itemPath == null) return;
    const gen = viewGenRef.current;
    let live = true;
    const mine = () => live && gen === viewGenRef.current;
    void (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: [itemPath], smallThumbnails: false } };
        });
        if (!mine()) return;
        if (resp.payload?.$case === "respListOfFiles") {
          bigReset();
          // A path the device left out has no thumbnail: nothing to retry.
          const f = resp.payload.respListOfFiles.files.find(x => x.path === itemPath);
          const url = bytesToURL(f?.content);
          if (url) setBigThumb({ path: itemPath, url });
        } else if (resp.errorCode === "unknown_payload") {
          // A device before release 80 (no GetThumbnails) sent big tiles
          // anyway: the grid's thumbnail stays.
          bigReset();
        } else {
          // The bridge answering for an unreachable device, a session
          // being restored.
          bigFailed();
        }
      } catch {
        // No connection.
        if (mine()) bigFailed();
      }
    })();
    return () => { live = false; };
  }, [thumbStays, itemPath, bigRetryTick, bigFailed, bigReset]);

  useEffect(() => {
    const gen = ++viewGenRef.current;
    const current = () => gen === viewGenRef.current;
    setHiURL(null);
    setVideoProblem(null);
    setStuck(null);
    setBigThumb(null);
    bigReset();
    setInfoOpen(false);
    setInfoData(null);
    setZoomScale(1);
    if (!item) return;
    setHiLoading(true);
    const fetchFull = async () => {
      let got = false;
      try {
        // Issue #110: a video streams from a URL; the device declines
        // small clips, which fall through to the whole-file fetch.
        if (canStream(item.mime)) {
          const streamURL = await requestStreamURL({ path: item.path });
          if (!current()) return;
          if (streamURL) { got = true; setHiURL(streamURL); return; }
        }
        const resp = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: item.path } };
        });
        if (!current()) return;
        if (resp.payload?.$case === "respFile") {
          const full = resp.payload.respFile!;
          const url = bytesToURL(full.content, full.mime || "image/jpeg");
          got = !!url;
          setHiURL(url || null);
        }
      } catch {
        // the thumbnail stays
      } finally {
        if (current()) {
          setHiLoading(false);
          if (!got) setStuck(item.path);
        }
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

  // Download (see downloadItem), in mediaDownload's store: by path, so
  // the button shows the item on screen, and kept when the viewer closes.
  // A failure's note shows here while a viewer is open (a note over the
  // page moves in), over the page once none is.
  const save = useDownloadState(items[index]?.path ?? "");
  const saveError = useDownloadFailure();
  useDownloadHost(true, items[index]?.path ?? "");
  const download = useCallback(() => {
    const it = items[index];
    if (it) downloadItem(it);
  }, [items, index]);

  // Issue #199: Delete (the button, or the Delete key) always asks first,
  // about the item on screen then. While the question shows, the keys are
  // its own (see onKey): Escape cancels it rather than closing the
  // viewer, Tab stays in it and arrows don't page under it. It is drawn
  // inside the viewer's box, so a click in it never reaches the backdrop
  // that closes the viewer.
  const [asking, setAsking] = useState<ViewerItem | null>(null);
  const blockedWhy = item && deleteBlocked ? deleteBlocked(item) : null;
  // Nothing is deleted on a demo device (net/demo.ts).
  const demo = useDemo();
  const canDelete = !!onDeleted && !demo && !!item && !blockedWhy;
  const askDelete = useCallback(() => {
    if (canDelete) setAsking(items[index] ?? null);
  }, [canDelete, items, index]);
  // The caller's latest callback and items, for an answer that comes
  // after a render.
  const onDeletedRef = useRef(onDeleted);
  const itemsRef = useRef(items);
  useLayoutEffect(() => {
    onDeletedRef.current = onDeleted;
    itemsRef.current = items;
  });
  const mountedRef = useRef(true);
  useEffect(() => () => { mountedRef.current = false; }, []);
  const onDeleteChangedRef = useRef(onDeleteChanged);
  useLayoutEffect(() => { onDeleteChangedRef.current = onDeleteChanged; });
  // `every`: issue #201's "Delete all {count} copies".
  const confirmDelete = async (every: boolean): Promise<UiText | null> => {
    const it = asking;
    if (!it) return null;
    const { error: err, changed } = await deleteOnDevice(it, every);
    if (changed != null && mountedRef.current) onDeleteChangedRef.current?.(it, changed);
    if (err || !mountedRef.current) return err;
    // One update with the caller's: the next item, or the viewer closed.
    if (itemsRef.current.every((x) => x.path === it.path)) restoreFocusRef.current = false;
    setAsking(null);
    onDeletedRef.current?.(it);
    return null;
  };
  // The question gone, the viewer has the focus again if it fell out of it
  // (a click on Delete leaves it on nothing once the dialog goes).
  const wasAskingRef = useRef(false);
  useEffect(() => {
    const was = wasAskingRef.current;
    wasAskingRef.current = asking != null;
    if (!was || asking) return;
    const box = boxRef.current;
    if (box && !box.contains(document.activeElement)) box.focus({ preventScroll: true });
  }, [asking]);

  // The viewer's Tab stops, in order (cFocusable), without the guard.
  const guardRef = useRef<HTMLSpanElement>(null);
  const stops = useCallback(() => {
    const box = boxRef.current;
    if (!box) return [];
    return [...box.querySelectorAll<HTMLElement>(cFocusable)].filter((el) => el.offsetParent !== null && el !== guardRef.current);
  }, []);
  // The guard, the viewer's last stop: Tab on from the last control of a
  // video or the map (which can't be told from their others) lands on it,
  // and it hands focus round to the first stop - unless it is only where a
  // Shift+Tab onto them starts from (guardPassRef).
  const guardPassRef = useRef(false);
  const onGuardFocus = useCallback(() => {
    if (guardPassRef.current) { guardPassRef.current = false; return; }
    stops()[0]?.focus();
  }, [stops]);
  // Whether the keyboard, not a pointer, moved focus last: arrow keys on a
  // video reached with Tab are the video's own (they seek), not paging. A
  // click on the video focuses it too, and arrows page from there as
  // before.
  const keysRef = useRef(false);
  useEffect(() => {
    const onPointer = () => { keysRef.current = false; };
    window.addEventListener("pointerdown", onPointer, true);
    return () => window.removeEventListener("pointerdown", onPointer, true);
  }, []);

  // Keyboard: Escape closes, arrows page, Delete (or the Mac's delete
  // key, Backspace) asks to delete, Tab stays in the viewer. None of it
  // while the delete question shows: its keys are its own.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (asking) return;
      const box = boxRef.current;
      const active = document.activeElement;
      const inVideo = keysRef.current && active instanceof HTMLVideoElement && !!box?.contains(active);
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight" && !inVideo && index < items.length - 1) onIndexChange(index + 1);
      if (e.key === "ArrowLeft" && !inVideo && index > 0) onIndexChange(index - 1);
      if ((e.key === "Delete" || e.key === "Backspace") && !inVideo && !e.ctrlKey && !e.altKey && !e.repeat) {
        const field = e.target instanceof Element && e.target.closest("input, textarea, select, [contenteditable='true']");
        if (!field && canDelete) {
          e.preventDefault();
          askDelete();
        }
      }
      if (!box) return;
      // Space on the viewer itself would scroll the page behind it.
      if (e.key === " " && e.target === box) e.preventDefault();
      if (e.key !== "Tab") return;
      keysRef.current = true;
      const all = stops();
      if (!all.length) { e.preventDefault(); box.focus(); return; }
      const first = all[0];
      const last = all[all.length - 1];
      // A video or the map last: Tab from it is the browser's, through
      // its stops and on to the guard; Shift+Tab onto it goes back from
      // the guard, to its last stop.
      const guard = hasInnerStops(last) ? guardRef.current : null;
      if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (e.shiftKey && (active === first || active === box)) {
        if (guard) { guardPassRef.current = true; guard.focus(); }
        else { e.preventDefault(); last.focus(); }
      }
      else if (!e.shiftKey && active === last && !guard) { e.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, items.length, onIndexChange, onClose, stops, asking, canDelete, askDelete]);

  // Swipe to page (issue #18) and pinch to zoom (issue #36): a two-finger
  // pinch on touch devices, ctrl+wheel (a trackpad pinch) on desktop.
  const modalTouchStartX = useRef<number | null>(null);
  const pinchStartDist = useRef<number | null>(null);
  const pinchStartScale = useRef(1);
  const touchDistance = (touches: React.TouchList) =>
    Math.hypot(touches[0].clientX - touches[1].clientX, touches[0].clientY - touches[1].clientY);
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
          <button className={"pg-info-btn" + (infoOpen ? " on" : "")} onClick={infoOpen ? closeInfo : openInfo}
            data-tip={t("web.images.viewer.more_info")} aria-description={t("web.images.viewer.more_info")}>
            <span className="pg-info-glyph">i</span> {t("web.images.viewer.info")}
          </button>
          <span className="pg-modal-title">{name}</span>
          {/* The original file, under its own name. */}
          <DownloadButton save={save} onClick={download} what={name ?? ""} />
          {/* Issue #199: where the caller allows it. Greyed out, with
              the reason as its tooltip, where the device would refuse;
              still a stop for the keyboard, so the reason can be read. */}
          {onDeleted && !demo && (
            <button
              type="button"
              className="pg-modal-del"
              data-tip={blockedWhy ? blockedWhy(t) : t("common.delete")}
              aria-label={t("common.delete")}
              aria-description={blockedWhy ? blockedWhy(t) : undefined}
              aria-disabled={blockedWhy ? true : undefined}
              onClick={askDelete}
            >
              <TrashGlyph />
            </button>
          )}
          <button className="pg-close" aria-label={t("common.close")} data-tip={t("common.close")} onClick={onClose}>×</button>
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
            // Always a JPEG thumbnail: the big one where it stays on
            // screen (see stuck), the grid's own until then.
            const thumb = (bigThumb?.path === f.path && bigThumb.url) || f.thumbURL || ownThumb;
            // What the browser couldn't play keeps the thumbnail (above).
            const videoFails = (why: "codec" | "stalled") => {
              setVideoProblem(why);
              setStuck(f.path);
            };
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
                        <p>{t("web.images.viewer.codec")}</p>
                        <p className="pg-video-unplayable-hint">{t("web.images.viewer.codec_hint")}</p>
                      </>
                    ) : (
                      <>
                        <p>{t("web.images.viewer.stalled")}</p>
                        <p className="pg-video-unplayable-hint">{t("web.images.viewer.stalled_hint")}</p>
                      </>
                    )}
                    <a className="pg-video-download" href={hiURL} download={f.path.split("/").pop()}>
                      {t("web.images.viewer.download_it")}
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
                    videoFails(
                      code === MediaError.MEDIA_ERR_DECODE ||
                      code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
                        ? "codec"
                        : "stalled"
                    );
                  }}
                  onLoadedMetadata={(e) => {
                    if ((e.currentTarget as HTMLVideoElement).videoWidth === 0) videoFails("codec");
                  }}
                  ref={(el) => {
                    if (!el) return;
                    // A stall is "no sign of life for a while", not
                    // "not finished yet" - so every event that proves
                    // something is still happening pushes the
                    // deadline back, and a video that streams in
                    // slowly is left alone to do it.
                    let timer: ReturnType<typeof setTimeout>;
                    const giveUp = () => { if (el.readyState === 0) videoFails("stalled"); };
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
            // A full size the browser can't decode (a format GetFile
            // doesn't convert, a HEIC whose conversion failed) gives way
            // to the thumbnail rather than a broken image.
            const full = stuck === f.path ? null : hiURL;
            return (
              <>
                <img
                  ref={imgRef}
                  src={full || thumb}
                  alt={f.path}
                  onError={full ? () => setStuck(f.path) : undefined}
                  style={{ transform: `scale(${zoomScale})`, transition: pinchStartDist.current ? "none" : "transform 0.15s ease-out" }} // i18n-ignore: a CSS transition
                />
                {!full && thumb && <LowResBadge imgRef={imgRef} loading={hiLoading} />}
              </>
            );
          })()}
        </div>
        {/* After the picture, which would paint over them, and before the
            Info panel, which covers them. */}
        {index > 0 && <button className="pg-nav left" onClick={() => onIndexChange(index - 1)} aria-label={t("web.images.viewer.previous")}>‹</button>}
        {index < items.length - 1 && <button className="pg-nav right" onClick={() => onIndexChange(index + 1)} aria-label={t("web.images.viewer.next")}>›</button>}

        {infoOpen && (
          <div className="pg-info-panel" onClick={(e) => e.stopPropagation()}>
            <div className="pg-info-hdr">
              <span>{t("web.images.viewer.more_info")}</span>
              <button className="pg-info-close" onClick={closeInfo}>×</button>
            </div>
            {infoLoading ? (
              <div className="pg-info-loading">{t("common.loading")}</div>
            ) : !infoData ? (
              <div className="pg-info-loading">{t("web.images.viewer.no_metadata")}</div>
            ) : (
              <div className="pg-info-body">
                {(infoData.cameraMake || infoData.cameraModel) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.camera")}</span>
                    <span>{[infoData.cameraMake, infoData.cameraModel].filter(Boolean).join(" ")}</span>
                  </div>
                )}
                {infoData.takenAt && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.taken")}</span>
                    <span>{fmtDate(t.lang, infoData.takenAt, dateTimeFields)}</span>
                  </div>
                )}
                {!!(infoData.width && infoData.height) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.dimensions")}</span>
                    <span>{infoData.width} × {infoData.height}</span>
                  </div>
                )}
                {infoData.exposureTime && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.exposure")}</span>
                    <span>{infoData.exposureTime}</span>
                  </div>
                )}
                {infoData.fNumber && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.aperture")}</span>
                    <span>{infoData.fNumber}</span>
                  </div>
                )}
                {!!infoData.iso && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.iso")}</span>
                    <span>{infoData.iso}</span>
                  </div>
                )}
                {infoData.focalLength && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.focal_length")}</span>
                    <span>{infoData.focalLength}</span>
                  </div>
                )}
                {(infoData.city || infoData.country) && (
                  <div className="pg-info-row">
                    <span className="pg-info-label">{t("web.images.viewer.location")}</span>
                    <span>{[infoData.city, infoData.country].filter(Boolean).join(", ")}</span>
                  </div>
                )}
                {infoData.hasGps && (
                  <iframe
                    className="pg-info-map"
                    title={t("web.images.viewer.map")}
                    loading="lazy"
                    src={`https://www.openstreetmap.org/export/embed.html?bbox=${infoData.longitude - 0.02}%2C${infoData.latitude - 0.02}%2C${infoData.longitude + 0.02}%2C${infoData.latitude + 0.02}&layer=mapnik&marker=${infoData.latitude}%2C${infoData.longitude}`}
                  />
                )}
                {!infoData.cameraMake && !infoData.cameraModel && !infoData.hasGps && !infoData.exposureTime && (
                  <div className="pg-info-loading">{t("web.images.viewer.no_exif")}</div>
                )}
              </div>
            )}
          </div>
        )}

        <DownloadNote failed={saveError} />
        {asking && (() => {
          // Issue #201: a photo the device holds at several paths asks
          // whether to delete only this copy or all of them.
          const copies = copiesChoice([asking]);
          return (
            <ConfirmDialog
              title={isVideoFile(asking) ? t("web.images.viewer.delete_video_title") : t("web.images.viewer.delete_photo_title")}
              text={copies.kind === "single" ? t("web.images.copies.single_text", { count: copies.count })
                : inCollection ? t("web.images.viewer.delete_text_collection") : t("web.images.viewer.delete_text")}
              confirm={copies.kind === "single" ? t("web.images.copies.all", { count: copies.count }) : t("common.delete")}
              busyText={t("common.deleting")}
              onConfirm={() => confirmDelete(copies.kind === "single")}
              other={copies.kind === "single" ? { label: t("web.images.copies.only_this"), onConfirm: () => confirmDelete(false) } : undefined}
              onCancel={() => setAsking(null)}
            />
          );
        })()}
        {/* Last of all: see onGuardFocus. */}
        <span ref={guardRef} className="pg-modal-sr" tabIndex={0} onFocus={onGuardFocus} />
      </div>
    </div>
  );
}
