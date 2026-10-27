// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #180: a shared gallery, opened by anyone with its link
// (https://<device>/shared#<uuid>.<secret>). The secret stays in the
// fragment, which browsers never send anywhere: it goes to the device
// only inside the requests, over the encrypted websocket, and the device
// keeps no copy of it. Thumbnails first, a viewer for photos (the
// browser-ready preview) and videos (streamed by range), and downloads of
// the originals - all of them, or a selection.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, SharedGallery, SharedGalleryItem } from "../proto/messages";
import { GetSharedGalleryItem_Part } from "../proto/messages";
import Spinner from "./Spinner";
import LanguagePicker from "./LanguagePicker";
import "./SharedGalleryView.css";
import { fmtDate, useT, type TFunction } from "../i18n";
import { asIs, percentText, say, type PlainKey, type UiText } from "./uiText";
import { sizeText } from "./sizeText";
import { isTooBigForMemory } from "./bigFile";
import { lazyPart } from "./lazyPart";
import Part from "./Part";
import { useBackLayer } from "./backStack";

// The question before a big download (F11 of the 2026-10 review), loaded
// only when one is asked: this page loads what it shows.
const BigFileDialog = lazyPart(() => import("./BigFileDialog"));

const CHUNK = 4 << 20;
type ItemPart = typeof GetSharedGalleryItem_Part[keyof typeof GetSharedGalleryItem_Part];
type FetchPart = (index: number, part: ItemPart, onBytes?: (n: number, total: number) => void, shouldStop?: () => boolean) => Promise<Blob>;
// How many steps either side of the open item the viewer keeps already
// loaded photos and clips for, so stepping back shows them at once.
const cViewerKeep = 2;

function parseLink(): { uuid: string; secret: string } | null {
  const frag = window.location.hash.replace(/^#/, "");
  const dot = frag.indexOf(".");
  if (dot <= 0) return null;
  return { uuid: frag.slice(0, dot), secret: frag.slice(dot + 1) };
}

// Sizes: whole KB, MB with one digit, GB with two.
const fmtBytes = (t: TFunction, n: number) => sizeText(t, n, [0, 1, 2]);

// A request that failed, with what the visitor is told: the device's own
// words (in the language the request carried) or ours. The page is for
// anyone with the link, signed in or not: it follows the browser's
// language, as the rest of the app does with no choice stored, or the one
// picked in its language picker (this browser's choice for the device's
// address, as on the signed-out top bar).
class GalleryFailure extends Error {
  readonly text: UiText;
  constructor(text: UiText) {
    super("shared gallery request failed"); // i18n-ignore: never shown, its text is
    this.text = text;
  }
}
const failureText = (err: unknown, fallback: PlainKey): UiText => {
  if (err instanceof GalleryFailure) return err.text;
  const message = (err as { message?: unknown } | null | undefined)?.message;
  return message ? asIs(String(message)) : say(fallback);
};

const isVideo = (it: SharedGalleryItem) => it.mime.startsWith("video/");

export default function SharedGalleryView() {
  const t = useT();
  const link = useMemo(parseLink, []);
  const [gallery, setGallery] = useState<SharedGallery | null>(null);
  const [error, setError] = useState<UiText | null>(() => (link ? null : say("web.files.gallery.incomplete")));
  const [thumbs, setThumbs] = useState<Record<number, string>>({});
  const [open, setOpen] = useState<number | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [dl, setDl] = useState<{ done: number; total: number; bytes: number } | null>(null);
  // Originals are put together in this page's memory before each is
  // saved: a big one asks first (bigFile.ts), with the biggest's size.
  const [bigAsk, setBigAsk] = useState<{ bytes: number; several: boolean; indexes: number[] } | null>(null);
  const urls = useRef<string[]>([]);
  // The viewer's loaded photos and clips by item index (blob URLs only,
  // never a stream URL, which expires) - see Viewer.
  const viewCache = useRef(new Map<number, string>());

  // One part of an item, assembled from 4 MB pieces. shouldStop ends it
  // between pieces: an item the viewer has moved past, or a closed page,
  // no longer has its whole file read and sent (each piece of a public
  // link holds 8 MB of the device's budget).
  const fetchPart: FetchPart = useCallback(async (index, part, onBytes, shouldStop) => {
    if (!link) throw new Error("no link");
    const pieces: Uint8Array[] = [];
    let offset = 0, total = -1, mime = "application/octet-stream";
    while (total < 0 || offset < total) {
      if (shouldStop?.()) throw new Error("cancelled");
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqGetSharedGalleryItem",
          reqGetSharedGalleryItem: { uuid: link.uuid, secret: link.secret, index, part, offset: BigInt(offset), length: CHUNK },
        };
      });
      if (resp.payload?.$case !== "respFileChunk") {
        const message = resp.errorMessage;
        throw new GalleryFailure(message ? asIs(message) : say("web.files.gallery.not_found"));
      }
      const c = resp.payload.respFileChunk;
      total = Number(c.size);
      mime = c.mime || mime;
      if (c.data.length === 0 && offset < total) throw new GalleryFailure(say("web.files.gallery.stopped"));
      pieces.push(c.data);
      offset += c.data.length;
      onBytes?.(c.data.length, total);
    }
    return new Blob(pieces as BlobPart[], { type: mime });
  }, [link]);

  const objectURL = useCallback((b: Blob) => {
    const u = URL.createObjectURL(b);
    urls.current.push(u);
    return u;
  }, []);
  useEffect(() => () => {
    urls.current.forEach(u => URL.revokeObjectURL(u));
    viewCache.current.forEach(u => URL.revokeObjectURL(u));
  }, []);

  useEffect(() => {
    if (!link) return;
    let cancelled = false;
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqOpenSharedGallery", reqOpenSharedGallery: { uuid: link.uuid, secret: link.secret } };
        });
        if (cancelled) return;
        if (resp.payload?.$case !== "respSharedGallery") {
          const message = resp.errorMessage;
          setError(() => (message ? asIs(message) : say("web.files.gallery.not_found")));
          return;
        }
        setGallery(resp.payload.respSharedGallery);
      } catch (err: any) {
        if (!cancelled) setError(() => failureText(err, "web.files.gallery.open_failed"));
      }
    })();
    return () => { cancelled = true; };
  }, [link]);

  // Thumbnails, four at a time, for the tiles on or near the screen (F14
  // of the 2026-10 review): every one of a big gallery used to be fetched
  // in order however little of it was looked at, each piece of a public
  // link holding the device's budget.
  const gridRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const grid = gridRef.current;
    if (!gallery || !grid) return;
    let cancelled = false;
    const stop = () => cancelled;
    // Thumbnails reach the grid in batches: one setThumbs per thumbnail
    // copied the map and re-rendered every tile each time, which grows
    // with the square of the gallery's size.
    let pending: Record<number, string> = {};
    let flushTimer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      if (flushTimer != null) clearTimeout(flushTimer);
      flushTimer = null;
      const batch = pending;
      pending = {};
      if (!cancelled && Object.keys(batch).length) setThumbs(t => ({ ...t, ...batch }));
    };
    const addThumb = (i: number, url: string) => {
      pending[i] = url;
      // A timer, not requestAnimationFrame, which stalls in a background tab.
      if (flushTimer == null) flushTimer = setTimeout(flush, 100);
    };
    const queue: number[] = [];
    let running = 0;
    const fetchThumb = async (i: number) => {
      try {
        const b = await fetchPart(i, GetSharedGalleryItem_Part.THUMBNAIL, undefined, stop);
        if (!cancelled) addThumb(i, objectURL(b));
      } catch {
        // A gallery made before its files had thumbnails: the
        // screen-sized preview stands in; without one either, the
        // tile shows the file's name.
        if (!cancelled && gallery.items.find((it) => it.index === i)?.hasPreview) {
          try {
            const b = await fetchPart(i, GetSharedGalleryItem_Part.PREVIEW, undefined, stop);
            if (!cancelled) addThumb(i, objectURL(b));
          } catch { /* the name, then */ }
        }
      }
    };
    const pump = () => {
      while (!cancelled && running < 4 && queue.length) {
        const i = queue.shift()!;
        running++;
        void fetchThumb(i).finally(() => { running--; pump(); });
      }
    };
    const obs = new IntersectionObserver((entries) => {
      for (const en of entries) {
        if (!en.isIntersecting) continue;
        obs.unobserve(en.target);
        const i = Number((en.target as HTMLElement).dataset.index);
        if (Number.isInteger(i)) queue.push(i);
      }
      pump();
    }, { rootMargin: "800px 0px" });
    grid.querySelectorAll<HTMLElement>("[data-index]").forEach((el) => obs.observe(el));
    return () => {
      cancelled = true;
      obs.disconnect();
      if (flushTimer != null) clearTimeout(flushTimer);
    };
  }, [gallery, fetchPart, objectURL]);

  // The tab's title: the gallery's description, or ours in the language
  // on screen.
  useEffect(() => {
    if (gallery) document.title = gallery.description || t("web.files.gallery.title");
  }, [gallery, t]);

  const download = async (indexes: number[], bigOk = false) => {
    if (!gallery || dl) return;
    const biggest = Math.max(0, ...indexes.map((i) => Number(gallery.items[i]?.size ?? 0)));
    if (!bigOk && isTooBigForMemory(biggest)) {
      setBigAsk({ bytes: biggest, several: indexes.length > 1, indexes });
      return;
    }
    setDl({ done: 0, total: indexes.length, bytes: 0 });
    try {
      for (const [k, i] of indexes.entries()) {
        const blob = await fetchPart(i, GetSharedGalleryItem_Part.ORIGINAL, n => setDl(d => d && { ...d, bytes: d.bytes + n }));
        const u = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = u;
        a.download = gallery.items[i].name;
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(u), 10_000);
        setDl(d => d && { ...d, done: k + 1 });
      }
    } catch (err: any) {
      setError(() => failureText(err, "web.files.gallery.download_failed"));
    } finally {
      setDl(null);
    }
  };

  if (error) {
    return (
      <div className="sg-page">
        <LanguagePicker className="sg-corner-lang" />
        <div className="sg-empty"><h1>{t("web.files.gallery.title")}</h1><p>{error(t)}</p></div>
      </div>
    );
  }
  if (!gallery) {
    return <div className="sg-page"><div className="sg-empty"><Spinner label={t("web.files.gallery.opening")} /></div></div>;
  }

  const items = gallery.items;
  const totalBytes = items.reduce((s, it) => s + Number(it.size), 0);
  const toggle = (i: number) => setSelected(s => {
    const n = new Set(s);
    if (n.has(i)) n.delete(i); else n.add(i);
    return n;
  });

  return (
    <div className="sg-page">
      <header className="sg-head">
        <div className="sg-title">
          <h1>{gallery.description || t("web.files.gallery.title")}</h1>
          <p className="sg-meta">
            {gallery.expires
              ? t("web.files.gallery.meta_until", {
                count: items.length,
                size: fmtBytes(t, totalBytes),
                date: fmtDate(t.lang, gallery.expires, { day: "numeric", month: "long", year: "numeric" }),
              })
              : t("web.files.gallery.meta", { count: items.length, size: fmtBytes(t, totalBytes) })}
          </p>
          {gallery.lowRes && <p className="sg-lowres">{t("web.files.gallery.low_res")}</p>}
        </div>
        <div className="sg-actions">
          {selecting ? (
            <>
              <button className="btn" onClick={() => setSelected(new Set(items.map(it => it.index)))}>{t("web.files.gallery.select_all")}</button>
              <button className="btn" onClick={() => { setSelecting(false); setSelected(new Set()); }}>{t("common.cancel")}</button>
              {/* With nothing selected (disabled) it read "Download ", space included. */}
              <button className="btn primary" disabled={selected.size === 0 || !!dl}
                onClick={() => void download([...selected].sort((a, b) => a - b))}>
                {selected.size ? t("web.files.gallery.download_count", { count: selected.size }) : <>{t("common.download")} </>}
              </button>
            </>
          ) : (
            <>
              <button className="btn" onClick={() => setSelecting(true)}>{t("common.select")}</button>
              <button className="btn primary" disabled={!!dl} onClick={() => void download(items.map(it => it.index))}>{t("web.files.gallery.download_all")}</button>
            </>
          )}
          <LanguagePicker className="sg-lang lpk-phone" />
        </div>
      </header>
      {dl && (
        <div className="sg-progress" role="status">
          {t("web.files.gallery.downloading", { current: Math.min(dl.done + 1, dl.total), total: dl.total, size: fmtBytes(t, dl.bytes) })}
          <div className="sg-bar"><div style={{ width: `${(100 * dl.done) / dl.total}%` }} /></div>
        </div>
      )}
      <div className="sg-grid" ref={gridRef}>
        {items.map(it => (
          <button key={it.index} data-index={it.index} className={`sg-tile${selected.has(it.index) ? " on" : ""}`}
            onClick={() => (selecting ? toggle(it.index) : setOpen(it.index))}
            aria-label={it.name} aria-pressed={selecting ? selected.has(it.index) : undefined}>
            {thumbs[it.index]
              ? <img src={thumbs[it.index]} alt="" loading="lazy" />
              : <span className="sg-name">{it.name}</span>}
            {isVideo(it) && <span className="sg-play" aria-hidden>▶</span>}
            {selecting && <span className="sg-check" aria-hidden>{selected.has(it.index) ? "✓" : ""}</span>}
          </button>
        ))}
      </div>
      {open !== null && (
        <Viewer item={items[open]} count={items.length} index={open} thumb={thumbs[open]}
          fetchPart={fetchPart} cache={viewCache.current} link={link!}
          onClose={() => setOpen(null)} onMove={d => setOpen(i => (i === null ? null : (i + d + items.length) % items.length))}
          onDownload={() => void download([open])} />
      )}
      {bigAsk && (
        <Part kind="layer" onClose={() => setBigAsk(null)}>
          <BigFileDialog
            bytes={bigAsk.bytes}
            kind={bigAsk.several ? "one_of" : "save"}
            onGo={() => { setBigAsk(null); void download(bigAsk.indexes, true); }}
            onCancel={() => setBigAsk(null)}
          />
        </Part>
      )}
    </div>
  );
}

function Viewer(props: {
  item: SharedGalleryItem; index: number; count: number; thumb?: string; link: { uuid: string; secret: string };
  fetchPart: FetchPart; cache: Map<number, string>;
  onClose: () => void; onMove: (d: number) => void; onDownload: () => void;
}) {
  const { item, index, count, thumb, link, fetchPart, cache, onClose, onMove, onDownload } = props;
  const t = useT();
  // Back closes it (backStack.ts): on a phone, Back used to leave the
  // gallery for wherever its link was opened from.
  useBackLayer(true, onClose);
  const [src, setSrc] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState<{ n: number; total: number }>({ n: 0, total: 0 });

  useEffect(() => {
    let cancelled = false;
    setSrc(null);
    setFailed(false);
    setLoaded({ n: 0, total: 0 });
    const progress = (n: number, total: number) => { if (!cancelled) setLoaded(l => ({ n: l.n + n, total })); };
    // Every photo and whole clip opened used to stay in memory until the
    // page closed, and a revisit downloaded it again. Now one is kept per
    // item while the viewer is within cViewerKeep steps of it, and freed
    // once it is further - never the item on screen or its neighbours.
    const show = (u: string) => {
      setSrc(u);
      cache.forEach((url, k) => {
        const d = Math.abs(k - index);
        if (Math.min(d, count - d) <= cViewerKeep) return;
        URL.revokeObjectURL(url);
        cache.delete(k);
      });
    };
    const load = async (part: ItemPart) => {
      const hit = cache.get(index);
      if (hit) { show(hit); return; }
      const b = await fetchPart(index, part, progress, () => cancelled);
      if (cancelled) return;
      const u = URL.createObjectURL(b);
      cache.set(index, u);
      show(u);
    };
    (async () => {
      try {
        if (isVideo(item)) {
          // Streamed by range when it's worth it; small clips come whole.
          // A stream URL expires, so it is asked for afresh every time.
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            (e as any).payload = { $case: "reqGetSharedGalleryStream", reqGetSharedGalleryStream: { uuid: link.uuid, secret: link.secret, index } };
          });
          if (resp.payload?.$case === "respMediaUrl" && resp.payload.respMediaUrl.url) {
            if (!cancelled) setSrc(resp.payload.respMediaUrl.url);
            return;
          }
          if (cancelled) return;
          await load(GetSharedGalleryItem_Part.ORIGINAL);
          return;
        }
        await load(GetSharedGalleryItem_Part.PREVIEW);
      } catch {
        if (!cancelled) setFailed(true);
      }
    })();
    return () => { cancelled = true; };
  }, [item, index, count, link, fetchPart, cache]);

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight") onMove(1);
      if (e.key === "ArrowLeft") onMove(-1);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [onClose, onMove]);

  return (
    <div className="sg-viewer" role="dialog" aria-label={item.name} onClick={onClose}>
      <div className="sg-viewer-bar" onClick={e => e.stopPropagation()}>
        <span className="sg-viewer-name">{t("web.files.gallery.position", { name: item.name, index: index + 1, total: count })}</span>
        <button className="btn" onClick={onDownload}>{t("common.download")}</button>
        <button className="btn" onClick={onClose} aria-label={t("common.close")}>✕</button>
      </div>
      <div className="sg-stage" onClick={e => e.stopPropagation()}>
        {count > 1 && <button className="sg-nav prev" onClick={() => onMove(-1)} aria-label={t("web.files.gallery.previous")}>‹</button>}
        {failed ? <p className="sg-name">{t("web.files.gallery.cant_show")}</p>
          : !src ? (
            // The thumbnail, blurred, while the photo itself arrives -
            // with how far along it is, so it never looks stuck.
            <div className="sg-loading">
              {thumb && <img src={thumb} alt="" className="sg-blur" />}
              <div className="sg-loading-label" role="status">
                <Spinner label={loaded.total > 0
                  ? t("web.files.gallery.loading_percent", { percent: percentText(t, Math.round((100 * loaded.n) / loaded.total)) })
                  : t("common.loading")} />
              </div>
            </div>
          )
          : isVideo(item) ? <video src={src} controls autoPlay playsInline />
          : <img src={src} alt={item.name} />}
        {count > 1 && <button className="sg-nav next" onClick={() => onMove(1)} aria-label={t("web.files.gallery.next")}>›</button>}
      </div>
    </div>
  );
}
