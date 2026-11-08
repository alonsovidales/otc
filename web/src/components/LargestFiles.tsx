// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Largest files (the owner, 2026-11-08: "so we can identify what
// is taking more space"): every file on the device, largest first, one row
// per content (a photo at three paths takes its space once), at its first
// path (ListLargestFiles; compress.ts has the rules). A screen of Settings'
// own, over its sections: Back - the browser's, or its own button - goes
// back to them (backStack.ts' layer). Pages of 50 as it scrolls, retried
// like a grid's page (usePageRetry). Each row: a photo's or a video's small
// thumbnail by path (GetThumbnails, as Files' grid asks), else the file's
// type icon; its name, its folder, its size and "in N places" for content
// at several paths. A photo or a video opens in the viewer, as Files opens
// it (Edit, Revert, Info, the heart and Delete; a deleted file leaves the
// list), anything else as a click on it in Files does (fileOpen.ts). A
// checkbox, or a long press on a touch screen, picks a row; the selection
// bar's Compress (CompressDialog.tsx) is on while a photo or a video is
// picked, and each file compressed takes its row's place - the list is
// read again once the job is over, as sizes and so the order changed.
// Its own chunk (lazyPart.ts), loaded when Settings opens it.
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { File as MsgFile, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { useT } from "../i18n";
import { useWS } from "../net/useWS";
import { fileSize } from "../net/fileSize";
import { refuseOnDemo } from "../net/demo";
import { useBackLayer } from "./backStack";
import { usePageRetry } from "./usePageRetry";
import { lazyPart } from "./lazyPart";
import Part from "./Part";
import Spinner from "./Spinner";
import FileTypeIcon from "./FileTypeIcon";
import BigFileDialog from "./BigFileDialog";
import { isTooBigForMemory } from "./bigFile";
import { openDocument } from "./fileOpen";
import { sizeText } from "./sizeText";
import { useToasts } from "./useToasts";
import ToastView from "./ToastView";
import { refusedText } from "./photoCopies";
import { shownAfter, type EditDone } from "./photoEdit";
import type { ViewerItem } from "./MediaViewer";
import { CompressIcon } from "./NavIcons";
import { asIs, type UiText } from "./uiText";
import {
  addPage, canCompress, compressedFile, folderOf, isMediaFile, jobOutcome, jobOver, largestAnswer, largestPayload, largestView, leafOf,
  pathKey, placesOf, reloadsAfter, wantsMore, type LargestLoad,
} from "./compress";
import { useCompressed, useCompressJob, type Compressed } from "./compressJob";
import "./PhotoGallery.css";
import "./LargestFiles.css";

// The viewer and the Compress dialog, loaded when first opened.
const MediaViewer = lazyPart(() => import("./MediaViewer"));
const CompressDialog = lazyPart(() => import("./CompressDialog"));

// Thumbnails are asked this many paths at a time, and at most this many
// are kept, those seen longest ago going first (as Files' grid).
const cThumbBatch = 24;
const cThumbKeep = 1000;
// A touch held this long picks its row (as Images' and Files').
const cLongPressMs = 450;
// A touch moved further than this is a scroll, not a press.
const cPressSlop = 10;

// Below this the selection bar's Compress is its icon alone, with a
// tooltip (PhotoGallery.css' .pg-bar-label).
const cCompactBar = "(max-width: 899px)";

// A size as Files writes it.
const sizeOf = (t: ReturnType<typeof useT>, f: MsgFile) => sizeText(t, fileSize(f), [1, 1, 1]);

const BackGlyph = () => (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M19 12H5.5M11 6l-6 6 6 6" />
  </svg>
);
const CloseGlyph = () => (
  <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
    <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
  </svg>
);

export default function LargestFiles({ onClose }: { onClose: () => void }) {
  const t = useT();
  const titleId = useId();
  // Back goes back to Settings' sections, as the screen's own button does.
  useBackLayer(true, onClose);
  const liveRef = useRef(true);
  useEffect(() => {
    liveRef.current = true;
    return () => { liveRef.current = false; };
  }, []);
  const { toast, showToast, dismissToast } = useToasts();

  // -------- the pages ----------------------------------------------------------
  const [rows, setRows] = useState<MsgFile[]>([]);
  const rowsRef = useRef(rows);
  useLayoutEffect(() => { rowsRef.current = rows; });
  const [load, setLoad] = useState<LargestLoad>({ kind: "loading" });
  const tokenRef = useRef("");
  // A read from the top (another page's answer then lands nowhere).
  const genRef = useRef(0);
  const { tick, failed: retryLater, reset: resetRetry, ready } = usePageRetry();

  // A page: the first (from the top, replacing the rows once it comes, so
  // a list read again doesn't flash empty), or the next after the token.
  const fetchPage = useCallback(async (first: boolean) => {
    if (first) {
      genRef.current++;
      tokenRef.current = "";
    }
    const gen = genRef.current;
    setLoad({ kind: "loading" });
    let resp: RespEnvelope | null = null;
    try {
      resp = await useWS.request((e: Partial<ReqEnvelope>) => { e.payload = largestPayload(first ? "" : tokenRef.current); });
    } catch {
      // No answer: as below.
    }
    if (!liveRef.current || gen !== genRef.current) return;
    const a = largestAnswer(resp);
    if (a.kind === "page") {
      resetRetry();
      tokenRef.current = a.token;
      setRows((prev) => addPage(first ? [] : prev, a.files));
      setLoad(a.token ? { kind: "more" } : { kind: "end" });
    } else if (a.kind === "old") {
      setLoad({ kind: "old" });
    } else {
      retryLater();
      setLoad({ kind: "failed", said: a.said });
    }
  }, [resetRetry, retryLater]);
  useEffect(() => { void fetchPage(true); }, [fetchPage]);
  const reload = useCallback(() => {
    resetRetry();
    void fetchPage(true);
  }, [fetchPage, resetRetry]);
  const retryNow = () => {
    resetRetry();
    void fetchPage(rowsRef.current.length === 0);
  };

  // The next page as the foot of the list nears the screen; one that
  // failed again once its wait is over (usePageRetry's tick).
  const footRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = footRef.current;
    const asks = wantsMore(load) || (load.kind === "failed" && ready());
    if (!el || !asks) return;
    const obs = new IntersectionObserver((entries) => {
      if (entries.some((en) => en.isIntersecting)) {
        obs.disconnect();
        void fetchPage(rowsRef.current.length === 0);
      }
    }, { rootMargin: "800px 0px" });
    obs.observe(el);
    return () => obs.disconnect();
  }, [load, tick, ready, fetchPage]);

  // -------- thumbnails (photos and videos) -------------------------------------
  const [thumbs, setThumbs] = useState<Record<string, string>>({});
  const thumbsRef = useRef(thumbs);
  useLayoutEffect(() => { thumbsRef.current = thumbs; });
  useEffect(() => () => { Object.values(thumbsRef.current).forEach((u) => u && URL.revokeObjectURL(u)); }, []);
  const asked = useRef(new Set<string>());
  const seen = useRef(new Map<string, number>());
  const seenTick = useRef(0);
  const queue = useRef<string[]>([]);
  const pumping = useRef(false);
  const pump = useCallback(async () => {
    if (pumping.current) return;
    pumping.current = true;
    try {
      while (queue.current.length) {
        const batch = queue.current.splice(0, cThumbBatch);
        try {
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: batch, smallThumbnails: true } };
          });
          if (!liveRef.current) return;
          const got: Record<string, string> = {};
          if (resp.payload?.$case === "respListOfFiles") {
            for (const f of resp.payload.respListOfFiles.files) {
              if (f.content?.length) got[f.path] = URL.createObjectURL(new Blob([f.content as BlobPart], { type: "image/jpeg" }));
            }
          }
          // A path without one keeps its type icon.
          for (const p of batch) if (!(p in got)) got[p] = "";
          setThumbs((prev) => ({ ...prev, ...got }));
        } catch {
          // Asked again when the row next comes in sight.
          for (const p of batch) asked.current.delete(p);
        }
      }
    } finally {
      pumping.current = false;
    }
  }, []);
  const listRef = useRef<HTMLUListElement>(null);
  useEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const askedNow = asked.current;
    const obs = new IntersectionObserver((entries) => {
      let added = false;
      for (const en of entries) {
        const p = (en.target as HTMLElement).dataset.thumb;
        if (!en.isIntersecting || !p) continue;
        seen.current.set(p, ++seenTick.current);
        if (askedNow.has(p)) continue;
        askedNow.add(p);
        queue.current.push(p);
        added = true;
      }
      if (added) void pump();
    }, { rootMargin: "600px 0px" });
    list.querySelectorAll<HTMLElement>("[data-thumb]").forEach((el) => obs.observe(el));
    // What is still queued is asked again by the rows the next observer
    // finds in sight.
    return () => {
      obs.disconnect();
      for (const p of queue.current) askedNow.delete(p);
      queue.current = [];
    };
  }, [rows, pump]);
  // At most cThumbKeep: the ones seen longest ago go, asked again when back.
  useEffect(() => {
    const kept = Object.entries(thumbs).filter(([, u]) => u);
    if (kept.length <= cThumbKeep) return;
    const out = kept.sort((a, b) => (seen.current.get(a[0]) ?? 0) - (seen.current.get(b[0]) ?? 0)).slice(0, kept.length - cThumbKeep);
    for (const [p, u] of out) {
      URL.revokeObjectURL(u);
      asked.current.delete(p);
      seen.current.delete(p);
    }
    setThumbs((prev) => {
      const next = { ...prev };
      for (const [p] of out) delete next[p];
      return next;
    });
  }, [thumbs]);
  // A row's thumbnail under another path (a PNG compressed into a .jpg:
  // the same picture), or gone with it.
  const moveThumb = useCallback((from: string, to: string | null) => {
    const u = thumbsRef.current[from];
    if (u === undefined || from === to) return;
    asked.current.delete(from);
    setThumbs((prev) => {
      const next = { ...prev };
      delete next[from];
      if (to) next[to] = u;
      return next;
    });
    if (to) asked.current.add(to);
    else if (u) URL.revokeObjectURL(u);
  }, []);
  // An edit's: the picture the viewer drew for it (the device makes its
  // own a little later), else none until the row asks again.
  const editedThumb = useCallback((from: string, to: string, blob: Blob | null) => {
    const old = [thumbsRef.current[from], from !== to ? thumbsRef.current[to] : undefined];
    const url = blob ? URL.createObjectURL(blob) : "";
    asked.current.delete(from);
    setThumbs((prev) => {
      const next = { ...prev };
      delete next[from];
      if (url) next[to] = url;
      else delete next[to];
      return next;
    });
    if (url) asked.current.add(to);
    else asked.current.delete(to);
    for (const u of old) if (u) URL.revokeObjectURL(u);
  }, []);

  // -------- the selection --------------------------------------------------------
  const [sel, setSel] = useState<ReadonlySet<string>>(new Set());
  const picked = useMemo(() => rows.filter((f) => sel.has(pathKey(f))), [rows, sel]);
  const toggle = (f: MsgFile) => setSel((prev) => {
    const next = new Set(prev);
    const k = pathKey(f);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    return next;
  });
  const clearSel = useCallback(() => setSel(new Set()), []);
  // A row that left the list (deleted, read again) is no longer picked.
  useEffect(() => {
    setSel((prev) => {
      if (!prev.size) return prev;
      const keys = new Set(rows.map(pathKey));
      const next = new Set([...prev].filter((k) => keys.has(k)));
      return next.size === prev.size ? prev : next;
    });
  }, [rows]);

  // A touch held still picks its row; once anything is picked a tap picks
  // or unpicks one, where a click opens it.
  const press = useRef<{ timer: number; x: number; y: number; done: boolean } | null>(null);
  const lastPointer = useRef("mouse");
  const cancelPress = () => {
    if (press.current) window.clearTimeout(press.current.timer);
    press.current = null;
  };
  useEffect(() => cancelPress, []);

  // -------- opening -------------------------------------------------------------
  const [viewer, setViewer] = useState<{ items: ViewerItem[]; index: number } | null>(null);
  // The viewer changed what the list shows (an edit, a copy kept elsewhere
  // after "Delete only this copy"): read again as it closes.
  const staleRef = useRef(false);
  const viewerOpener = useRef<HTMLElement | null>(null);
  const toItem = (f: MsgFile): ViewerItem => ({
    path: f.path, mime: f.mime, hash: f.hash, copies: f.copies, thumbURL: thumbs[f.path] || undefined,
    originalHash: f.originalHash || undefined, originalRemoved: f.originalRemoved || undefined,
  });
  const [bigAsk, setBigAsk] = useState<MsgFile | null>(null);
  const [opening, setOpening] = useState<string | null>(null);
  const openDoc = (f: MsgFile, bigOk = false) => {
    if (opening) return;
    if (!bigOk && isTooBigForMemory(fileSize(f))) { setBigAsk(f); return; }
    setOpening(f.path);
    const name = leafOf(f.path);
    void openDocument(f.path, name).then((failed) => {
      if (!liveRef.current) return;
      setOpening(null);
      if (failed) {
        const words = failed.failed;
        showToast({ text: words ? asIs(words) : (tt) => tt("web.files.open.failed", { name }), error: true });
      }
    });
  };
  const open = (f: MsgFile) => {
    if (!isMediaFile(f)) { openDoc(f); return; }
    // In the list's order, its photos and videos only.
    const media = rows.filter(isMediaFile);
    viewerOpener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setViewer({ items: media.map(toItem), index: Math.max(0, media.findIndex((x) => x.path === f.path)) });
  };
  const closeViewer = () => {
    setViewer(null);
    const el = viewerOpener.current;
    viewerOpener.current = null;
    if (el?.isConnected) el.focus({ preventScroll: true });
    if (staleRef.current) {
      staleRef.current = false;
      reload();
    }
  };
  // The viewer's thumbnails as they are now (rows come in sight meanwhile).
  const viewerItems = useMemo(
    () => viewer?.items.map((it) => ({ ...it, thumbURL: thumbs[it.path] || undefined })) ?? [],
    [viewer?.items, thumbs],
  );
  // Deleted from the viewer: it goes on to the next one, and the row
  // leaves. Only this copy of content held at several paths, the content
  // may still be listed at its next path: read again as the viewer closes.
  const onViewerDeleted = (it: ViewerItem) => {
    const v = viewer;
    if (v) {
      const rest = v.items.filter((x) => x.path !== it.path);
      if (!rest.length) {
        viewerOpener.current = listRef.current;
        closeViewer();
      } else setViewer({ items: rest, index: Math.min(v.index, rest.length - 1) });
    }
    if ((it.copies ?? 1) > 1) staleRef.current = true;
    setRows((prev) => prev.filter((f) => f.path !== it.path));
    moveThumb(it.path, null);
  };
  // An edit: the device's answer in the viewer and in the row's place
  // (a Replace's or a Revert's; a copy lies elsewhere), and the list is
  // read again as the viewer closes - its size, and so its place, changed.
  const onViewerEdited = (it: ViewerItem, done: EditDone) => {
    staleRef.current = true;
    if (done.kind === "copy") return;
    const f = done.file;
    const now: ViewerItem = { path: f.path, mime: f.mime, hash: f.hash, copies: it.copies, originalHash: f.originalHash || undefined, originalRemoved: f.originalRemoved || undefined };
    setViewer((v) => (v ? { ...v, items: v.items.map((x) => (x.path === it.path ? now : x)) } : v));
    setRows((prev) => prev.map((x) => (x.path === it.path ? shownAfter(x, done) : x)));
    editedThumb(it.path, f.path, done.thumb);
  };
  // Refused as "changed" (the path holds other content now): the viewer
  // closes, the device's words say so, and the list is read again.
  const onViewerChanged = (_it: ViewerItem, text: UiText) => {
    staleRef.current = true;
    closeViewer();
    showToast({ text, error: true });
  };
  const deleteBlocked = useCallback((it: ViewerItem): UiText | null => {
    const f = rowsRef.current.find((x) => x.path === it.path);
    return f?.uploadOnly ? (tt) => tt("web.files.list.upload_only_refused", { name: leafOf(it.path) }) : null;
  }, []);

  // -------- Compress -------------------------------------------------------------
  const [compressing, setCompressing] = useState<MsgFile[] | null>(null);
  const compressOn = canCompress(picked);
  const startCompress = () => {
    if (!compressOn || refuseOnDemo()) return;
    setCompressing(picked);
  };
  // Each file compressed takes its row's place: a new size, maybe a new
  // name (a PNG becomes .jpg), its thumbnail kept (the same picture).
  const putCompressed = (done: Compressed) => {
    const byPath = new Map(done.map((r) => [r.path, r]));
    if (!rowsRef.current.some((f) => byPath.has(f.path))) return;
    for (const r of done) if (r.file.path !== r.path) moveThumb(r.path, r.file.path);
    setRows((prev) => prev.map((f) => {
      const r = byPath.get(f.path);
      return r ? compressedFile(f, r) : f;
    }));
  };
  useCompressed(putCompressed);
  // Once the job is over, the list is read again from the top when it
  // compressed anything: sizes changed, and so did the order. Not for a job
  // that was over already when the screen opened.
  const followed = useCompressJob(false);
  const overSeen = useRef(followed && jobOver(followed.job, followed.gone) ? followed.job.id : "");
  useEffect(() => {
    if (!followed || !jobOver(followed.job, followed.gone) || overSeen.current === followed.job.id) return;
    overSeen.current = followed.job.id;
    if (reloadsAfter(jobOutcome(followed.job, followed.gone))) reload();
  }, [followed, reload]);

  // Escape lets go of the selection, unless something over the list takes it.
  const overlay = !!(viewer || compressing || bigAsk);
  useEffect(() => {
    if (!sel.size || overlay) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      e.preventDefault();
      clearSel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [sel.size, overlay, clearSel]);

  const view = largestView(rows.length, load);
  const selecting = sel.size > 0;
  const [compact, setCompact] = useState(() => window.matchMedia(cCompactBar).matches);
  useEffect(() => {
    const mq = window.matchMedia(cCompactBar);
    const on = () => setCompact(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  return (
    <section className="lf-root" aria-labelledby={titleId}>
      {selecting && (
        <div className="pg-selbar" role="toolbar" aria-labelledby={`${titleId}-count`}>
          <button type="button" className="pg-icon-btn" onClick={clearSel} aria-label={t("web.files.selection.clear")} data-tip={t("web.files.selection.clear")}>
            <CloseGlyph />
          </button>
          <span id={`${titleId}-count`} className="pg-selbar-count" aria-live="polite">{t("web.images.select.count", { count: sel.size })}</span>
          <div className="pg-selbar-actions">
            <button
              type="button"
              className="pg-bar-btn"
              onClick={startCompress}
              disabled={!compressOn}
              aria-label={t("web.files.compress.button")}
              data-tip={compact ? t("web.files.compress.button") : undefined}
            >
              <CompressIcon size={22} />
              <span className="pg-bar-label">{t("web.files.compress.button")}</span>
            </button>
          </div>
        </div>
      )}

      <div className="lf-head">
        <button type="button" className="pg-icon-btn" onClick={onClose} aria-label={t("common.back")} data-tip={t("common.back")}>
          <BackGlyph />
        </button>
        <h2 id={titleId} className="lf-title">{t("web.files.largest.title")}</h2>
      </div>
      <p className="lf-intro">{t("web.files.largest.intro")}</p>

      {view.kind === "loading" && <div className="lf-state"><Spinner label={t("common.loading")} /></div>}
      {view.kind === "old" && <p className="lf-state">{t("web.files.largest.needs_update")}</p>}
      {view.kind === "empty" && <p className="lf-state">{t("web.files.largest.empty")}</p>}
      {view.kind === "failed" && (
        <div className="lf-state" role="alert">
          <p>{view.said || t("web.files.largest.failed")}</p>
          <button type="button" className="pg-btn outline" onClick={retryNow}>{t("common.try_again")}</button>
        </div>
      )}

      {rows.length > 0 && view.kind !== "old" && (
        <ul ref={listRef} className={`lf-list${selecting ? " selecting" : ""}`} tabIndex={-1} aria-labelledby={titleId}>
          {rows.map((f) => {
            const k = pathKey(f);
            const media = isMediaFile(f);
            const thumb = media ? thumbs[f.path] : "";
            const places = placesOf(f);
            const name = leafOf(f.path);
            const on = sel.has(k);
            return (
              <li key={k} className={`lf-row${on ? " picked" : ""}`} data-thumb={media ? f.path : undefined}>
                <input
                  type="checkbox"
                  className="lf-check"
                  checked={on}
                  onChange={() => toggle(f)}
                  aria-label={t("web.files.list.select_item", { name })}
                />
                <button
                  type="button"
                  className="lf-open"
                  aria-busy={opening === f.path || undefined}
                  onPointerDown={(e) => {
                    lastPointer.current = e.pointerType;
                    cancelPress();
                    if (e.pointerType !== "touch") return;
                    const timer = window.setTimeout(() => {
                      if (press.current) press.current.done = true;
                      toggle(f);
                    }, cLongPressMs);
                    press.current = { timer, x: e.clientX, y: e.clientY, done: false };
                  }}
                  onPointerMove={(e) => {
                    const p = press.current;
                    if (p && !p.done && Math.hypot(e.clientX - p.x, e.clientY - p.y) > cPressSlop) cancelPress();
                  }}
                  onPointerCancel={cancelPress}
                  onContextMenu={(e) => { if (lastPointer.current === "touch") e.preventDefault(); }}
                  onClick={(e) => {
                    const held = press.current?.done;
                    cancelPress();
                    if (held) return;
                    // A key (detail 0) always opens.
                    if (selecting && e.detail !== 0 && lastPointer.current === "touch") { toggle(f); return; }
                    open(f);
                  }}
                >
                  <span className="lf-art" aria-hidden="true">
                    {thumb ? <img src={thumb} alt="" draggable={false} /> : <FileTypeIcon name={name} size={40} />}
                  </span>
                  <span className="lf-text">
                    <span className="lf-name" title={name}>{name}</span>
                    <span className="lf-where">
                      <span className="lf-folder" title={folderOf(f.path)}>{folderOf(f.path)}</span>
                      {places > 0 && <span className="lf-places">{t("web.files.largest.places", { count: places })}</span>}
                    </span>
                  </span>
                  <span className="lf-size">{opening === f.path ? <Spinner /> : sizeOf(t, f)}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}

      {/* The end of the list: the next page loads as this nears the
          screen. Always the same node, which the observer above watches. */}
      <div className="lf-foot" ref={footRef}>
        {view.kind === "rows" && view.foot === "loading" && <Spinner />}
        {view.kind === "rows" && view.foot === "failed" && (
          <span className="pg-foot-error">
            {load.kind === "failed" && load.said ? load.said : t("web.files.largest.failed")}
            <button type="button" className="pg-btn quiet small" onClick={retryNow}>{t("common.try_again")}</button>
          </span>
        )}
      </div>

      <ToastView toast={toast} onDismiss={dismissToast} />

      {bigAsk && (
        <BigFileDialog
          bytes={fileSize(bigAsk)}
          kind="save"
          onGo={() => { const f = bigAsk; setBigAsk(null); openDoc(f, true); }}
          onCancel={() => setBigAsk(null)}
        />
      )}

      {viewer && (
        <Part kind="layer" onClose={closeViewer}>
          <MediaViewer
            items={viewerItems}
            index={viewer.index}
            onIndexChange={(i) => setViewer((v) => (v ? { ...v, index: i } : v))}
            onClose={closeViewer}
            canFavourite
            onDeleted={onViewerDeleted}
            onDeleteChanged={(_it, said) => {
              staleRef.current = true;
              closeViewer();
              showToast({ text: (tt) => (said ? refusedText(tt, said) : tt("web.images.delete.changed", { count: 1 })), error: true });
            }}
            deleteBlocked={deleteBlocked}
            onEdited={onViewerEdited}
            onEditChanged={onViewerChanged}
          />
        </Part>
      )}

      {compressing && (
        <Part kind="layer" onClose={() => setCompressing(null)}>
          <CompressDialog picks={compressing} onClose={() => setCompressing(null)} onStarted={clearSel} />
        </Part>
      )}
    </section>
  );
}
