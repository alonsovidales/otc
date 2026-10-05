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
import "./SharedGalleryView.css";

const CHUNK = 4 << 20;
type Part = typeof GetSharedGalleryItem_Part[keyof typeof GetSharedGalleryItem_Part];
type FetchPart = (index: number, part: Part, onBytes?: (n: number, total: number) => void, shouldStop?: () => boolean) => Promise<Blob>;
// How many steps either side of the open item the viewer keeps already
// loaded photos and clips for, so stepping back shows them at once.
const cViewerKeep = 2;

function parseLink(): { uuid: string; secret: string } | null {
  const frag = window.location.hash.replace(/^#/, "");
  const dot = frag.indexOf(".");
  if (dot <= 0) return null;
  return { uuid: frag.slice(0, dot), secret: frag.slice(dot + 1) };
}

const fmtBytes = (n: number) =>
  n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${(n / (1 << 30)).toFixed(2)} GB`;

const isVideo = (it: SharedGalleryItem) => it.mime.startsWith("video/");

export default function SharedGalleryView() {
  const link = useMemo(parseLink, []);
  const [gallery, setGallery] = useState<SharedGallery | null>(null);
  const [error, setError] = useState<string | null>(link ? null : "This link is incomplete - copy the whole link and try again.");
  const [thumbs, setThumbs] = useState<Record<number, string>>({});
  const [open, setOpen] = useState<number | null>(null);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [dl, setDl] = useState<{ done: number; total: number; bytes: number } | null>(null);
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
      if (resp.payload?.$case !== "respFileChunk") throw new Error(resp.errorMessage || "This link doesn't exist or has expired.");
      const c = resp.payload.respFileChunk;
      total = Number(c.size);
      mime = c.mime || mime;
      if (c.data.length === 0 && offset < total) throw new Error("The device stopped sending the file.");
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
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqOpenSharedGallery", reqOpenSharedGallery: { uuid: link.uuid, secret: link.secret } };
        });
        if (cancelled) return;
        if (resp.payload?.$case !== "respSharedGallery") {
          setError(resp.errorMessage || "This link doesn't exist or has expired.");
          return;
        }
        const g = resp.payload.respSharedGallery;
        setGallery(g);
        document.title = g.description || "Shared gallery";
        // Thumbnails, four at a time, in order.
        let next = 0;
        const worker = async () => {
          while (!cancelled && next < g.items.length) {
            const i = next++;
            try {
              const b = await fetchPart(i, GetSharedGalleryItem_Part.THUMBNAIL, undefined, stop);
              if (!cancelled) addThumb(i, objectURL(b));
            } catch {
              // A gallery made before its files had thumbnails: the
              // screen-sized preview stands in; without one either, the
              // tile shows the file's name.
              if (g.items[i].hasPreview) {
                try {
                  const b = await fetchPart(i, GetSharedGalleryItem_Part.PREVIEW, undefined, stop);
                  if (!cancelled) addThumb(i, objectURL(b));
                } catch { /* the name, then */ }
              }
            }
          }
        };
        await Promise.all([worker(), worker(), worker(), worker()]);
        flush();
      } catch (err: any) {
        if (!cancelled) setError(err?.message || "Could not open this gallery.");
      }
    })();
    return () => {
      cancelled = true;
      if (flushTimer != null) clearTimeout(flushTimer);
    };
  }, [link, fetchPart, objectURL]);

  const download = async (indexes: number[]) => {
    if (!gallery || dl) return;
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
      setError(err?.message || "The download failed.");
    } finally {
      setDl(null);
    }
  };

  if (error) {
    return (
      <div className="sg-page">
        <div className="sg-empty"><h1>Shared gallery</h1><p>{error}</p></div>
      </div>
    );
  }
  if (!gallery) {
    return <div className="sg-page"><div className="sg-empty"><Spinner label="Opening the gallery…" /></div></div>;
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
          <h1>{gallery.description || "Shared gallery"}</h1>
          <p className="sg-meta">
            {items.length} {items.length === 1 ? "item" : "items"} · {fmtBytes(totalBytes)}
            {gallery.expires && <> · available until {gallery.expires.toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" })}</>}
          </p>
          {gallery.lowRes && <p className="sg-lowres">Low resolution: these are small copies, not the original photos.</p>}
        </div>
        <div className="sg-actions">
          {selecting ? (
            <>
              <button className="btn" onClick={() => setSelected(new Set(items.map(it => it.index)))}>Select all</button>
              <button className="btn" onClick={() => { setSelecting(false); setSelected(new Set()); }}>Cancel</button>
              <button className="btn primary" disabled={selected.size === 0 || !!dl}
                onClick={() => void download([...selected].sort((a, b) => a - b))}>Download {selected.size || ""}</button>
            </>
          ) : (
            <>
              <button className="btn" onClick={() => setSelecting(true)}>Select</button>
              <button className="btn primary" disabled={!!dl} onClick={() => void download(items.map(it => it.index))}>Download all</button>
            </>
          )}
        </div>
      </header>
      {dl && (
        <div className="sg-progress" role="status">
          Downloading {Math.min(dl.done + 1, dl.total)} of {dl.total} · {fmtBytes(dl.bytes)}
          <div className="sg-bar"><div style={{ width: `${(100 * dl.done) / dl.total}%` }} /></div>
        </div>
      )}
      <div className="sg-grid">
        {items.map(it => (
          <button key={it.index} className={`sg-tile${selected.has(it.index) ? " on" : ""}`}
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
    </div>
  );
}

function Viewer(props: {
  item: SharedGalleryItem; index: number; count: number; thumb?: string; link: { uuid: string; secret: string };
  fetchPart: FetchPart; cache: Map<number, string>;
  onClose: () => void; onMove: (d: number) => void; onDownload: () => void;
}) {
  const { item, index, count, thumb, link, fetchPart, cache, onClose, onMove, onDownload } = props;
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
    const load = async (part: Part) => {
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
        <span className="sg-viewer-name">{item.name} · {index + 1} of {count}</span>
        <button className="btn" onClick={onDownload}>Download</button>
        <button className="btn" onClick={onClose} aria-label="Close">✕</button>
      </div>
      <div className="sg-stage" onClick={e => e.stopPropagation()}>
        {count > 1 && <button className="sg-nav prev" onClick={() => onMove(-1)} aria-label="Previous">‹</button>}
        {failed ? <p className="sg-name">This file could not be shown here - download it instead.</p>
          : !src ? (
            // The thumbnail, blurred, while the photo itself arrives -
            // with how far along it is, so it never looks stuck.
            <div className="sg-loading">
              {thumb && <img src={thumb} alt="" className="sg-blur" />}
              <div className="sg-loading-label" role="status">
                <Spinner label={loaded.total > 0 ? `Loading… ${Math.round((100 * loaded.n) / loaded.total)}%` : "Loading…"} />
              </div>
            </div>
          )
          : isVideo(item) ? <video src={src} controls autoPlay playsInline />
          : <img src={src} alt={item.name} />}
        {count > 1 && <button className="sg-nav next" onClick={() => onMove(1)} aria-label="Next">›</button>}
      </div>
    </div>
  );
}
