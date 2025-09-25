import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, File as PbFile } from "../proto/messages";
import "./PhotoGallery.css";

const THUMB = 120;

function bytesToURL(bytes: Uint8Array, mime?: string) {
  const blob = new Blob([bytes], { type: mime || "application/octet-stream" });
  return URL.createObjectURL(blob);
}

export default function PhotoGallery() {
  // Tags
  const [allTags, setAllTags] = useState<string[]>([]);
  const [input, setInput] = useState("");
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [showSuggest, setShowSuggest] = useState(false);

  // Results
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [files, setFiles] = useState<PbFile[]>([]);
  const [thumbURLs, setThumbURLs] = useState<Record<string, string>>({}); // path -> objectURL

  // Selection
  const [selected, setSelected] = useState<Set<string>>(new Set());

  // Viewer
  const [viewerOpen, setViewerOpen] = useState(false);
  const [viewerIdx, setViewerIdx] = useState<number>(0);
  const [viewerURL, setViewerURL] = useState<string | null>(null);
  const [viewerLoading, setViewerLoading] = useState(false);

  const inputRef = useRef<HTMLInputElement | null>(null);

  // ---------- Fetch all tags once ----------
  useEffect(() => {
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
        });

        if (resp.payload?.$case === "respTagsList") {
          const tags = resp.payload.respTagsList.tags ?? [];
          setAllTags([...tags].sort((a, b) => a.localeCompare(b)));
        } else {
          setAllTags([]); // fallback empty
        }
      } catch {
        setAllTags([]); // offline or no tags endpoint
      }
    })();
  }, [useWS.request]);

  // ---------- Autocomplete ----------
  const suggestions = useMemo(() => {
    const q = input.trim().toLowerCase();
    if (!q) return [];
    return allTags
      .filter(t => t.toLowerCase().startsWith(q) && !selectedTags.includes(t))
      .slice(0, 8);
  }, [input, allTags, selectedTags]);

  const addTag = useCallback((tag: string) => {
    if (!tag) return;
    setSelectedTags(prev => (prev.includes(tag) ? prev : [...prev, tag]));
    setInput("");
    setShowSuggest(false);
    inputRef.current?.focus();
  }, []);

  const removeTag = useCallback((tag: string) => {
    setSelectedTags(prev => prev.filter(t => t !== tag));
  }, []);

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      if (suggestions.length) addTag(suggestions[0]);
      else if (input.trim()) addTag(input.trim());
    }
    if (e.key === "Backspace" && !input && selectedTags.length) {
      removeTag(selectedTags[selectedTags.length - 1]);
    }
  };

  // ---------- Search ----------
  const clearThumbs = useCallback(() => {
    Object.values(thumbURLs).forEach(URL.revokeObjectURL);
    setThumbURLs({});
  }, [thumbURLs]);

  const shareSocial = async (selection: Set<string>) => {
    let socialText = prompt('Text:');

    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqNewSocialPublication", reqNewSocialPublication: { text: socialText, paths: [...selection] } };
    });

    if (resp.payload?.$case === "respNewSocial") {
      alert("Shared!: " + resp.payload.respNewSocial.uuid);
    }
  };

  const shareLink = async (selection: Set<string>) => {
    console.log('Share', selection);

    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqShareFilesLink", reqShareFilesLink: { paths: [...selection] } };
    });

    if (resp.payload?.$case === "respShareLink" && resp.payload.respShareLink.link) {
      alert(resp.payload.respShareLink.link);
    }
  };

  const doSearch = useCallback(async () => {
    setError(null);
    setLoading(true);
    setSelected(new Set());
    clearThumbs();
    try {
      const tags = selectedTags; // per proto: repeated string tags
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqSearchPhotos", reqSearchPhotos: { tags } };
      });

      if (resp.payload?.$case === "respListOfFiles") {
        const list = resp.payload.respListOfFiles;
        setFiles(list.files);

        const map: Record<string, string> = {};
        for (const f of list.files) {
          if (f.content && f.content.length) {
            map[f.path] = bytesToURL(f.content as Uint8Array, f.mime);
          }
        }
        setThumbURLs(map);
      } else if (resp.payload?.$case === "respAck") {
        setError(resp.payload.respAck.errorMsg || "Unexpected ACK");
      } else {
        setError("Unexpected response.");
      }
    } catch (err: any) {
      setError(err?.message ?? String(err));
    } finally {
      setLoading(false);
    }
  }, [useWS.request, selectedTags, clearThumbs]);

  // ---------- Grid selection ----------
  const toggleSelect = useCallback((path: string, checked: boolean) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (checked) next.add(path);
      else next.delete(path);
      return next;
    });
  }, []);

  // ---------- Viewer ----------
  const openViewerAt = useCallback(async (idx: number) => {
  if (idx < 0 || idx >= files.length) return;
  setViewerIdx(idx);
  setViewerOpen(true);

  const f = files[idx];

  // 1) show thumbnail immediately if present
  const thumb = thumbURLs[f.path] || null;
  setViewerURL(thumb);

  // 2) fetch full res and swap in
  setViewerLoading(true);
  try {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: f.path } };
    });

    if (resp.payload?.$case === "respFile" && resp.payload.respFile.content) {
      const url = bytesToURL(
        resp.payload.respFile.content as Uint8Array,
        resp.payload.respFile.mime
      );
      setViewerURL(prev => {
        if (prev && prev !== thumb) URL.revokeObjectURL(prev);
        return url;
      });
    }
  } catch {
    // keep thumb on error
  } finally {
    setViewerLoading(false);
  }
}, [files, thumbURLs, useWS.request]);

  const closeViewer = useCallback(() => {
    setViewerOpen(false);
    setViewerLoading(false);
    if (viewerURL) URL.revokeObjectURL(viewerURL);
    setViewerURL(null);
  }, [viewerURL]);

  const next = useCallback(() => {
    if (!files.length) return;
    void openViewerAt((viewerIdx + 1) % files.length);
  }, [viewerIdx, files.length, openViewerAt]);

  const prev = useCallback(() => {
    if (!files.length) return;
    void openViewerAt((viewerIdx - 1 + files.length) % files.length);
  }, [viewerIdx, files.length, openViewerAt]);

  useEffect(() => {
  if (!viewerOpen) return;
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "ArrowRight") { e.preventDefault(); next(); }
    else if (e.key === "ArrowLeft") { e.preventDefault(); prev(); }
    else if (e.key === "Escape") { e.preventDefault(); closeViewer(); }
  };
  window.addEventListener("keydown", onKey);
  return () => window.removeEventListener("keydown", onKey);
}, [viewerOpen, next, prev, closeViewer]);
 
  const downloadCurrent = useCallback(() => {
    if (!viewerURL) return;
    const a = document.createElement("a");
    const name = files[viewerIdx]?.path?.split("/").pop() || "photo";
    a.href = viewerURL;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
  }, [viewerURL, viewerIdx, files]);

  // ---------- Render ----------
  return (
    <div className="pg2-wrap">
      {/* Tag input with chips */}
      <div className="pg2-search">
        <div className="pg2-chips">
          {selectedTags.map(tag => (
            <span key={tag} className="pg2-chip">
              {tag}
              <button className="pg2-chip-x" onClick={() => removeTag(tag)} style={{ padding: 0 }} aria-label={`Remove ${tag}`}>×</button>
            </span>
          ))}
          <input
            ref={inputRef}
            className="pg2-input"
            placeholder={selectedTags.length ? "Add another tag…" : "Search by tags…"}
            value={input}
            onChange={(e) => { setInput(e.target.value); setShowSuggest(true); }}
            onFocus={() => setShowSuggest(!!input)}
            onKeyDown={onKeyDown}
          />
        </div>
        <button className="pg2-btn" onClick={() => void doSearch()} disabled={loading}>
          🔎 Search
        </button>
      </div>

      {/* Suggestions dropdown */}
      {showSuggest && suggestions.length > 0 && (
        <div className="pg2-suggest" onMouseDown={(e) => e.preventDefault()}>
          {suggestions.map(s => (
            <button key={s} className="pg2-suggest-item" onClick={() => addTag(s)}>
              {s}
            </button>
          ))}
        </div>
      )}

      {/* Body */}
      {loading ? (
        <div className="pg2-loading">Searching…</div>
      ) : error ? (
        <div className="pg2-error">{error}</div>
      ) : (
        <div className="pg2-grid">
          {files.map((f, i) => (
            <div key={f.path} className="pg2-tile" style={{ width: THUMB, height: THUMB }}>
              <button className="pg2-image-btn" title={f.path} onClick={() => void openViewerAt(i)}>
                {thumbURLs[f.path] ? (
                  <img src={thumbURLs[f.path]} alt={f.path} />
                ) : (
                  <div className="pg2-ph"><span>🖼️</span></div>
                )}
              </button>
              <label className="pg2-check">
                <input
                  type="checkbox"
                  checked={selected.has(f.path)}
                  onChange={(e) => toggleSelect(f.path, e.target.checked)}
                />
                <span />
              </label>
            </div>
          ))}
        </div>
      )}

      {/* Floating bottom bar */}
      {selected.size > 0 && (
        <div className="pg2-bottom">
          <div className="pg2-bottom-inner">
            <div className="pg2-count">{selected.size} selected</div>
            <div className="pg2-actions">
              <button onClick={() => shareSocial(selected)}>Share In Social</button>
              <button onClick={() => shareLink(selected)}>Share with link</button>
              <button onClick={() => alert("Create group (not implemented)")}>Create group</button>
              <button onClick={() => alert("Add to group (not implemented)")}>Add to group</button>
            </div>
          </div>
        </div>
      )}

      {/* Viewer modal */}
      {viewerOpen && (
        <div className="pg2-modal" onClick={closeViewer}>
          <div className="pg2-modal-body" onClick={(e) => e.stopPropagation()}>
            <button className="pg2-close" onClick={closeViewer} title="Close">✕</button>
            <button className="pg2-dl" onClick={downloadCurrent} title="Download">⬇︎</button>
            <button className="pg2-nav left"  onClick={prev} title="Previous">‹</button>
            <button className="pg2-nav right" onClick={next} title="Next">›</button>
            {viewerURL ? (
  <div className="pg2-full-wrap">
    <img className="pg2-full" src={viewerURL} alt={files[viewerIdx]?.path ?? "photo"} />
    {viewerLoading && <div className="pg2-full-spinner">Loading…</div>}
  </div>
) : viewerLoading ? (
  <div className="pg2-loading light">Loading…</div>
) : (
  <div className="pg2-error light">Failed to load image</div>
)}

          </div>
        </div>
      )}
    </div>
  );
}

