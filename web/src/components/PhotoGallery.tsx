import React, { useCallback, useMemo, useState } from "react";
import { useWS } from "../net/useWS";
import type {
  ReqEnvelope,
  RespEnvelope,
  File as PbFile,
} from "../proto/messages";
import "./PhotoGallery.css";

const MAX_THUMB = 120;

function bytesToDataURL(bytes: Uint8Array, mime?: string) {
  const blob = new Blob([bytes], { type: mime || "application/octet-stream" });
  return URL.createObjectURL(blob);
}

export default function PhotoGallery() {
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [files, setFiles] = useState<PbFile[]>([]);
  const [thumbURLs, setThumbURLs] = useState<Record<string, string>>({}); // path -> objectURL

  const [selected, setSelected] = useState<Set<string>>(new Set());

  // Viewer state
  const [viewerOpen, setViewerOpen] = useState(false);
  const [viewerURL, setViewerURL] = useState<string | null>(null);
  const [viewerPath, setViewerPath] = useState<string | null>(null);
  const [viewerLoading, setViewerLoading] = useState(false);

  const clearThumbs = useCallback(() => {
    Object.values(thumbURLs).forEach((url) => URL.revokeObjectURL(url));
    setThumbURLs({});
  }, [thumbURLs]);

  const doSearch = useCallback(async () => {
    setError(null);
    setLoading(true);
    setSelected(new Set());
    setViewerOpen(false);
    setViewerURL(null);
    setViewerPath(null);
    clearThumbs();

    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqSearchPhotos",
          reqSearchPhotos: { text: query },
        };
      });

      if (resp.payload?.$case === "respListOfFiles") {
        const list = resp.payload.respListOfFiles;
        setFiles(list.files);

        // Build thumb URLs if content is present (thumbnail bytes)
        const next: Record<string, string> = {};
        for (const f of list.files) {
          if (f.content && f.content.length) {
            next[f.path] = bytesToDataURL(f.content as Uint8Array, f.mime);
          }
        }
        setThumbURLs(next);
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
  }, [query, useWS.request, clearThumbs]);

  const onTileClick = useCallback(async (file: PbFile) => {
    setViewerLoading(true);
    setViewerOpen(true);
    setViewerURL(null);
    setViewerPath(file.path);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: file.path } };
      });

      if (resp.payload?.$case === "respFile" && resp.payload.respFile.content) {
        const bytes = resp.payload.respFile.content as Uint8Array;
        const url = bytesToDataURL(bytes, resp.payload.respFile.mime);
        setViewerURL(url);
      } else {
        setViewerURL(null);
      }
    } catch (e) {
      setViewerURL(null);
    } finally {
      setViewerLoading(false);
    }
  }, [useWS.request ]);

  const toggleSelect = useCallback((path: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(path);
      else next.delete(path);
      return next;
    });
  }, []);

  const hasSelection = selected.size > 0;

  const onKeySubmit = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void doSearch();
    }
  };

  const downloadCurrent = () => {
    if (!viewerURL) return;
    const a = document.createElement("a");
    a.href = viewerURL;
    a.download = viewerPath?.split("/").pop() || "photo";
    document.body.appendChild(a);
    a.click();
    a.remove();
  };

  const grid = useMemo(
    () =>
      files.map((f) => (
        <div className="pg-tile" key={f.path} style={{ width: MAX_THUMB, height: MAX_THUMB }}>
          <button className="pg-image-btn" onClick={() => onTileClick(f)} title={f.path}>
            {thumbURLs[f.path] ? (
              <img src={thumbURLs[f.path]} alt={f.path} />
            ) : (
              <div className="pg-placeholder">
                <span className="pg-ph-icon">🖼️</span>
              </div>
            )}
          </button>
          <label className="pg-checkbox">
            <input
              type="checkbox"
              checked={selected.has(f.path)}
              onChange={(e) => toggleSelect(f.path, e.target.checked)}
            />
            <span />
          </label>
        </div>
      )),
    [files, thumbURLs, selected, onTileClick, toggleSelect]
  );

  return (
    <div className="pg-wrap">
      {/* Search bar */}
      <div className="pg-search">
        <input
          className="pg-input"
          type="text"
          placeholder="Search photos…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKeySubmit}
        />
        <button className="pg-btn" disabled={loading} onClick={() => void doSearch()}>
          🔎 Search
        </button>
      </div>

      {/* Body */}
      {loading ? (
        <div className="pg-loading">Searching…</div>
      ) : error ? (
        <div className="pg-error">{error}</div>
      ) : (
        <div className="pg-grid">{grid}</div>
      )}

      {/* Bottom bar when selection */}
      {hasSelection && (
        <div className="pg-bottom">
          <div className="pg-bottom-inner">
            <div className="pg-sel-count">{selected.size} selected</div>
            <div className="pg-actions">
              <button onClick={() => alert("Share to Instagram (not implemented)")}>Share Instagram</button>
              <button onClick={() => alert("Share with friends (not implemented)")}>Share with friends</button>
              <button onClick={() => alert("Create group (not implemented)")}>Create group</button>
              <button onClick={() => alert("Add to group (not implemented)")}>Add to group</button>
            </div>
          </div>
        </div>
      )}

      {/* Viewer modal */}
      {viewerOpen && (
        <div className="pg-modal" onClick={() => setViewerOpen(false)}>
          <div className="pg-modal-body" onClick={(e) => e.stopPropagation()}>
            <button className="pg-modal-close" onClick={() => setViewerOpen(false)} title="Close">✕</button>
            <button className="pg-modal-dl" onClick={downloadCurrent} title="Download">⬇︎</button>
            {viewerLoading ? (
              <div className="pg-loading">Loading photo…</div>
            ) : viewerURL ? (
              <img className="pg-full" src={viewerURL} alt={viewerPath ?? "photo"} />
            ) : (
              <div className="pg-error">Failed to load image</div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

