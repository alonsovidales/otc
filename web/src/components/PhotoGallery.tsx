// src/components/PhotoGallery.tsx
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope, File as MsgFile, TagsList, FileExifInfo } from "../proto/messages";
import './PhotoGallery.css';

type Chip = string;
type Token = string | null;

// ---- helpers ---------------------------------------------------------------
const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  if (u8.byteLength === 0) return "";
  return URL.createObjectURL(new Blob([u8], { type: mime }));
};
const fileKey = (f: MsgFile, idx?: number) =>
  `${f.path || ""}#${f.hash || ""}#${f.mime || ""}#${f.size || 0}#${idx ?? -1}`;

// ===========================================================================

export default function PhotoGallery() {
  // -------- tags/typeahead --------------------------------------------------
  const [allTags, setAllTags] = useState<string[]>([]);
  const [chips, setChips] = useState<Chip[]>([]);
  const [input, setInput] = useState("");
  const [showSuggest, setShowSuggest] = useState(false);

  const suggestions = useMemo(() => {
    const q = input.trim().toLowerCase();
    if (!q) return [];
    return allTags.filter(t => t.toLowerCase().startsWith(q)).slice(0, 12);
  }, [allTags, input]);

  const addChip = (t: string) => {
    const tag = t.trim();
    if (!tag) return;
    setChips(prev => (prev.includes(tag) ? prev : [...prev, tag]));
    setInput("");
    setShowSuggest(false);
    // search will auto-trigger via chips effect
  };
  const removeChip = (t: string) => setChips(prev => prev.filter(x => x !== t));

  // -------- data & paging ---------------------------------------------------
  const [items, setItems] = useState<MsgFile[]>([]);
  const mapRef = useRef<Map<string, MsgFile>>(new Map()); // dedupe
  const [token, setToken] = useState<Token>(null);
  const [loading, setLoading] = useState(false);
  const [endReached, setEndReached] = useState(false);

  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const observerRef = useRef<IntersectionObserver | null>(null);

  // -------- modal (hi-res) --------------------------------------------------
  const [openIdx, setOpenIdx] = useState<number | null>(null);
  const [hiURL, setHiURL] = useState<string | null>(null);

  // -------- "More info" panel (issue #41) ------------------------------------
  const [infoOpen, setInfoOpen] = useState(false);
  const [infoLoading, setInfoLoading] = useState(false);
  const [infoData, setInfoData] = useState<FileExifInfo | null>(null);

  // -------- requests --------------------------------------------------------
  const loadTags = useCallback(async () => {
    const resp = await useWS.request(e => {
      (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
    });
    if (resp.payload?.$case === "respTagsList") {
      const list = (resp.payload.respTagsList as TagsList).tags ?? [];
      setAllTags(list);
    }
  }, []);

  const fetchPage = useCallback(
    async (overrideToken?: Token) => {
      if (loading || endReached) return;
      setLoading(true);
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = {
            $case: "reqSearchPhotos",
            reqSearchPhotos: {
              tags: chips,
              token: overrideToken ?? token ?? "",
            },
          };
        });
        if (resp.payload?.$case !== "respListOfFiles") return;

        const lof = resp.payload.respListOfFiles!;
        const nextToken = lof.token || null;

        // dedupe via map
        const map = new Map(mapRef.current);
        const added: MsgFile[] = [];
        (lof.files ?? []).forEach((f, i) => {
          const k = fileKey(f, i);
          if (!map.has(k)) {
            map.set(k, f);
            added.push(f);
          }
        });
        mapRef.current = map;
        if (added.length) setItems(prev => prev.concat(added));

        setToken(nextToken);
        setEndReached(!nextToken); // if no token back, we've reached the end
      } finally {
        setLoading(false);
      }
    },
    [chips, token, loading, endReached]
  );

  // open modal and fetch hi-res for current index
  const openAt = useCallback(
    async (idx: number) => {
      setOpenIdx(idx);
      setHiURL(null);
      setInfoOpen(false);
      setInfoData(null);
      setZoomScale(1);
      const f = items[idx];
      try {
        const resp = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: f.path } };
        });
        if (resp.payload?.$case === "respFile") {
          const full = resp.payload.respFile!;
          setHiURL(bytesToURL(full.content, full.mime || "image/jpeg"));
        }
      } catch {
        // keep thumb
      }
    },
    [items]
  );

  // Issue #41: fetch and show a photo/video's camera/EXIF metadata,
  // computed live on the server from the file's own bytes.
  const openInfo = useCallback(async () => {
    if (openIdx == null) return;
    setInfoOpen(true);
    setInfoLoading(true);
    setInfoData(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetFileInfo", reqGetFileInfo: { path: items[openIdx].path } };
      });
      if (resp.payload?.$case === "respFileInfo") {
        setInfoData(resp.payload.respFileInfo);
      }
    } finally {
      setInfoLoading(false);
    }
  }, [openIdx, items]);
  const closeInfo = useCallback(() => { setInfoOpen(false); setInfoData(null); }, []);

  // -------- initial load ----------------------------------------------------
  useEffect(() => {
    (async () => {
      await loadTags();
      // initial list (no tags)
      setItems([]);
      mapRef.current = new Map();
      setToken(null);
      setEndReached(false);
      await fetchPage("");
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // re-run search when chips change (fresh paging)
  useEffect(() => {
    (async () => {
      setItems([]);
      mapRef.current = new Map();
      setToken(null);
      setEndReached(false);
      await fetchPage("");
    })();
  }, [chips]); // eslint-disable-line react-hooks/exhaustive-deps

  // -------- infinite scroll: one call at a time -----------------------------
  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      (entries) => {
        const ent = entries[0];
        if (!ent?.isIntersecting) return;
        if (!loading && !endReached) fetchPage();
      },
      { root: null, rootMargin: "600px 0px 0px 0px" }
    );
    observerRef.current = obs;
    obs.observe(node);
    return () => {
      obs.disconnect();
      observerRef.current = null;
    };
  }, [fetchPage, loading, endReached]);

  // -------- selection bar ---------------------------------------------------
  const [sel, setSel] = useState<Set<string>>(new Set());
  const toggleSel = (p: string) => setSel(prev => {
    const n = new Set(prev);
    n.has(p) ? n.delete(p) : n.add(p);
    return n;
  });
  const selectedPaths = useMemo(() => Array.from(sel), [sel]);

  const shareInSocial = async () => {
    if (!selectedPaths.length) return;
    const caption = window.prompt("Caption:") ?? "";
    const resp = await useWS.request(e => {
      (e as any).payload = {
        $case: "reqNewSocialPublication",
        reqNewSocialPublication: { text: caption, paths: selectedPaths },
      };
    });
    if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
      alert("Shared: " + resp.payload.respNewSocial.uuid);
      setSel(new Set());
    } else {
      alert("Error publishing");
    }
  };

  const shareOrDownload = async (openAfter: boolean) => {
    if (!selectedPaths.length) return;
    const r1 = await useWS.request(e => {
      (e as any).payload = { $case: "reqShareFilesLink", reqShareFilesLink: { paths: selectedPaths } };
    });
    if (r1.payload?.$case !== "respShareLink") { alert("Could not create link"); return; }
    const link = r1.payload.respShareLink.link;
    if (openAfter) window.open(link, "_blank");
    else {
      await navigator.clipboard?.writeText?.(link);
      alert("Link copied");
    }
  };

  // -------- keyboard in modal ----------------------------------------------
  useEffect(() => {
    if (openIdx == null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpenIdx(null);
      if (e.key === "ArrowRight" && openIdx < items.length - 1) openAt(openIdx + 1);
      if (e.key === "ArrowLeft" && openIdx > 0) openAt(openIdx - 1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openIdx, items.length, openAt]);

  // -------- swipe in modal (issue #18) --------------------------------------
  const modalTouchStartX = useRef<number | null>(null);
  // Issue #36: pinch-to-zoom, matching the native apps — a genuine 2-finger
  // pinch on touch devices, and trackpad pinch (which browsers report as a
  // ctrlKey+wheel event, there's no native "pinch" DOM event) on desktop.
  const [zoomScale, setZoomScale] = useState(1);
  const pinchStartDist = useRef<number | null>(null);
  const pinchStartScale = useRef(1);

  const touchDistance = (t: React.TouchList) => {
    const dx = t[0].clientX - t[1].clientX;
    const dy = t[0].clientY - t[1].clientY;
    return Math.hypot(dx, dy);
  };

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
    if (pinchStartDist.current != null) {
      pinchStartDist.current = null;
      return; // was pinching, not swiping — don't also page through images
    }
    if (modalTouchStartX.current == null || openIdx == null || zoomScale !== 1) return;
    const dx = e.changedTouches[0].clientX - modalTouchStartX.current;
    if (dx < -30 && openIdx < items.length - 1) openAt(openIdx + 1);
    else if (dx > 30 && openIdx > 0) openAt(openIdx - 1);
    modalTouchStartX.current = null;
  };
  const onModalWheel = (e: React.WheelEvent) => {
    if (!e.ctrlKey) return; // plain scroll shouldn't zoom, only trackpad pinch
    e.preventDefault();
    setZoomScale(s => Math.min(4, Math.max(1, s - e.deltaY * 0.01)));
  };

  // -------- render ----------------------------------------------------------
  return (
    <div className="pg-root">
      {/* top search with chips and suggestions */}
      <div className="pg-search">
        <div className="pg-chipbar">
          {chips.map((c) => (
            <span key={`chip-${c}`} className="pg-chip">
              {c}
              <button className="pg-chip-x" onClick={() => removeChip(c)} aria-label={`Remove ${c}`}>×</button>
            </span>
          ))}
          <input
            value={input}
            onChange={(e) => { setInput(e.target.value); setShowSuggest(true); }}
            onFocus={() => setShowSuggest(true)}
            onBlur={() => setTimeout(() => setShowSuggest(false), 100)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                if (suggestions.length === 1) addChip(suggestions[0]);
                else if (input.trim()) addChip(input.trim());
              } else if (e.key === "Backspace" && !input && chips.length) {
                removeChip(chips[chips.length - 1]);
              }
            }}
            placeholder="Type a tag…"
          />
        </div>

        {showSuggest && suggestions.length > 0 && (
          <div className="pg-suggest">
            {suggestions.map(s => (
              <div key={`sug-${s}`} className="pg-suggest-item" onMouseDown={() => addChip(s)}>{s}</div>
            ))}
          </div>
        )}
      </div>

      {/* grid */}
      <div className="pg-grid">
        {items.map((f, i) => {
          const key = fileKey(f, i); // unique key (fixes React warnings)
          const thumb = bytesToURL(f.content, f.mime || "image/jpeg");
          const checked = sel.has(f.path);
          return (
            <div key={key} className="pg-cell">
              <label className="pg-check">
                <input type="checkbox" checked={!!checked} onChange={() => toggleSel(f.path)} />
              </label>
              <button className="pg-thumb" title={f.path} onClick={() => openAt(i)}>
                <img src={thumb} alt={f.path} loading="lazy" />
              </button>
            </div>
          );
        })}
        <div ref={sentinelRef} style={{ height: 1 }} />
      </div>

      {/* bottom actions */}
      {sel.size > 0 && (
        <div className="pg-actions">
          <button onClick={shareInSocial}>Share in social</button>
          <button onClick={() => alert("Create group (not implemented)")}>Create group</button>
          <button onClick={() => alert("Add to existing group (not implemented)")}>Add to group</button>
          <button onClick={() => shareOrDownload(false)}>Share link</button>
          <button onClick={() => shareOrDownload(true)}>Download as ZIP</button>
          <span className="pg-count">{sel.size} selected</span>
        </div>
      )}

      {/* modal */}
      {openIdx != null && (
        <div className="pg-modal" onClick={() => setOpenIdx(null)}>
          <div className="pg-modal-inner" onClick={(e) => e.stopPropagation()}>
            <button className="pg-close" onClick={() => setOpenIdx(null)}>×</button>
            {/* Issue #41: camera/EXIF metadata + location, on demand. */}
            <button className="pg-info-btn" title="More info" onClick={openInfo}>ⓘ</button>
            {openIdx > 0 && <button className="pg-nav left" onClick={() => openAt(openIdx - 1)}>‹</button>}
            {openIdx < items.length - 1 && <button className="pg-nav right" onClick={() => openAt(openIdx + 1)}>›</button>}
            <div
              className="pg-modal-imgwrap"
              onTouchStart={onModalTouchStart}
              onTouchMove={onModalTouchMove}
              onTouchEnd={onModalTouchEnd}
              onWheel={onModalWheel}
            >
              {(() => {
                const f = items[openIdx];
                const thumb = bytesToURL(f.content, f.mime || "image/jpeg");
                return (
                  <img
                    src={hiURL || thumb}
                    alt={f.path}
                    style={{ transform: `scale(${zoomScale})`, transition: pinchStartDist.current ? "none" : "transform 0.15s ease-out" }}
                  />
                );
              })()}
            </div>

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
      )}

      {/* styles */}
      <style>{`
      `}</style>
    </div>
  );
}

