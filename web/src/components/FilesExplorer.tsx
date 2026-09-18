// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type {
  ReqEnvelope,
  RespEnvelope,
  ListOfFiles,
  File as PbFile,
} from "../proto/messages";
import { loadFilesPath, saveFilesPath } from "../net/uiState";
import "./FilesExplorer.css";
import Spinner from "./Spinner";

type Props = {
  wsUrl?: string;            // defaults to VITE_WS_URL
  initialPath?: string;      // defaults to "/"
  requireAuth?: boolean;     // default true
};

const isDir = (f: PbFile) => f.mime === "inode/directory";
const isImg = (f: PbFile) => f.mime?.startsWith("image/");

const fmtBytes = (n?: number) =>
  typeof n === "number"
    ? (n >= 1<<30 ? (n/(1<<30)).toFixed(1)+" GB"
      : n >= 1<<20 ? (n/(1<<20)).toFixed(1)+" MB"
      : n >= 1<<10 ? (n/(1<<10)).toFixed(1)+" KB"
      : `${n} B`)
    : "—";

/*function tsToDate(ts: any): Date | undefined {
  if (!ts) return;
  const sec = Number((ts.seconds as any) ?? 0);
  const ns  = Number((ts.nanos as any) ?? 0);
  if (Number.isNaN(sec)) return;
  return new Date(sec * 1000 + Math.floor(ns / 1e6));
}*/

function bytesToURL(bytes: Uint8Array, mime = "application/octet-stream") {
  return URL.createObjectURL(new Blob([bytes], { type: mime }));
}

function joinPath(base: string, leaf: string) {
  const b = base.endsWith("/") ? base.slice(0, -1) : base;
  const l = leaf.startsWith("/") ? leaf.slice(1) : leaf;
  return `${b}/${l}`;
}
function dirname(p: string) {
  const clean = p.endsWith("/") && p !== "/" ? p.slice(0, -1) : p;
  const idx = clean.lastIndexOf("/");
  if (idx <= 0) return "/";
  return clean.slice(0, idx);
}
function normPath(p: string) {
  let s = p.trim();
  if (!s.startsWith("/")) s = "/" + s;
  if (!s.endsWith("/")) s = s + "/";
  return s || "/";
}
function leafName(full: string) {
  const parts = full.split("/");
  return parts[parts.length - 1] || full;
}

export default function FilesExplorer({
  initialPath = "/"
}: Props) {
  // Issue #53 follow-up: a reload restores the Files tab itself now, but
  // used to still always drop back to initialPath ("/") rather than
  // wherever the user had actually navigated to.
  const [path, setPath] = useState(() => loadFilesPath() ?? initialPath);
  const [listing, setListing] = useState<PbFile[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [dragOver, setDragOver] = useState(false);

  // Image viewer
  const [viewer, setViewer] = useState<{ name: string; url: string } | null>(null);

  // Issue #71: the only feedback a click on a file used to get was however
  // long GetFile's round trip took - nothing changed on screen in the
  // meantime, so it looked stuck, and clicking again (or on other rows,
  // thinking the first click missed) queued up that many concurrent opens,
  // each popping its own viewer/tab open once its own fetch happened to
  // land. Tracking which single path is in flight both drives a loading
  // state on that row and - via the guard in openEntry below - makes every
  // other click a no-op until it's done.
  const [openingPath, setOpeningPath] = useState<string | null>(null);

  // Issue #86: dropping files used to give ZERO visual feedback - the
  // per-file upload is a single WebSocket send (the whole file goes out as
  // one protobuf message) so there is no byte-level progress to report,
  // and for a batch it looked like nothing was happening. This tracks each
  // dropped file through its phases (reading -> sending -> done/failed) and
  // renders a progress panel: an overall bar plus one row per file, so the
  // user always sees what is being uploaded and when it finishes. The list
  // auto-clears a couple seconds after the last upload settles.
  type UploadItem = { name: string; size: number; status: "reading" | "sending" | "done" | "failed" };
  const [uploads, setUploads] = useState<UploadItem[]>([]);
  const uploadsTimer = useRef<number | null>(null);
  const clearUploadsAfter = (delay = 2500) => {
    if (uploadsTimer.current !== null) window.clearTimeout(uploadsTimer.current);
    uploadsTimer.current = window.setTimeout(() => setUploads([]), delay);
  };
  useEffect(() => () => { if (uploadsTimer.current !== null) window.clearTimeout(uploadsTimer.current); }, []);

  // -------- load list (1, 3, 4) ----------
  const loadList = useCallback(async (p: string) => {
    setLoading(true); setError(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        console.log('Listing path:', p);
        (e as any).payload = { $case: "reqListFiles", reqListFiles: { path: p } };
      });
      if (resp.payload?.$case === "respListOfFiles") {
        const lof: ListOfFiles = resp.payload.respListOfFiles;
        console.log('Files:', lof);

        // inject ".." entry as directory to go up (4)
        const up: PbFile = {
          hash: "",
          mime: "inode/directory",
          created: undefined as any,
          modified: undefined as any,
          path: "..",
          size: 0,
          content: undefined,
        };
        // Don’t add .. at root
        const files = p === "/" ? lof.files : [up, ...lof.files];

        setListing(files);
        setSel({});
      } else if (resp.error) {
        setError(resp.errorMessage || "Failed to list path");
      } else {
        setError("Unexpected response");
      }
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void loadList(path); }, [path, loadList]);
  useEffect(() => { saveFilesPath(path); }, [path]);

  // -------- drag & drop upload (2) ----------
  // Issue #86: each dropped file is tracked through its phases so the UI can
  // show an upload progress panel instead of a blank screen. The per-file
  // "sending" phase is a single WebSocket send (the whole file as one
  // protobuf message), so it cannot report byte-level progress - but seeing
  // which file is in flight and the overall "N/M done" is what was missing.
  const onDrop: React.DragEventHandler<HTMLDivElement> = async (ev) => {
    ev.preventDefault(); ev.stopPropagation(); setDragOver(false);
    const files = Array.from(ev.dataTransfer.files ?? []);
    if (!files.length) return;

    // Seed the tracker with one row per dropped file up front, so the panel
    // appears immediately even before the first read/send finishes.
    setUploads(files.map((f) => ({ name: f.name, size: f.size, status: "reading" as const })));
    clearUploadsAfter(4000);

    let failed = 0;
    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      setUploads((prev) => prev.map((u, j) => (j === i ? { ...u, status: "sending" } : u)));
      try {
        const ab = await f.arrayBuffer();
        const bytes = new Uint8Array(ab);

        await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = {
            $case: "reqUploadFile",
            reqUploadFile: {
              path: joinPath(path, f.name),
              content: bytes,
              forceOverride: false,
            },
          };
        });

        setUploads((prev) => prev.map((u, j) => (j === i ? { ...u, status: "done" } : u)));
      } catch (err) {
        failed++;
        setUploads((prev) => prev.map((u, j) => (j === i ? { ...u, status: "failed" } : u)));
        console.error("Upload failed for", f.name, err);
      }
    }

    await loadList(path);
    if (failed === 0) {
      clearUploadsAfter(1500);
    } else {
      clearUploadsAfter(6000);
    }
  };
  const onDragOver: React.DragEventHandler<HTMLDivElement> = (e) => { e.preventDefault(); setDragOver(true); };
  const onDragLeave: React.DragEventHandler<HTMLDivElement> = () => setDragOver(false);

  // -------- click entries (4, 5) ----------
  const openEntry = async (f: PbFile) => {
    if (isDir(f)) {
      let newPath = '';
      if (f.path === "..") {
        // issue #21: dirname() already returns "/" once we're back at the
        // root — blindly appending another "/" after it (as this used to)
        // turned ".." from the top directory into "//". normPath() only
        // adds a trailing slash when one isn't already there.
        newPath = normPath(dirname(path));
      } else {
        // directory name from row (server may return full path; we want the leaf)
        const name = f.path === ".." ? ".." : leafName(f.path);
        newPath = normPath(joinPath(path, name));
      }
      pathInputRef.current!.value = newPath;
      setPath(newPath);
      return;
    }

    // Issue #71: single-flight - a click while one open is already in
    // progress (this row or another) is ignored rather than queued, so it
    // can't pile up into several viewers/tabs popping open back to back
    // once each fetch happens to land.
    if (openingPath !== null) return;
    setOpeningPath(f.path);

    // Issue #72: open a blank tab synchronously, in the same tick as the
    // click, for a non-image - a tab opened later, after the GetFile
    // await below resolves, reads to the browser as unrelated to the
    // click that "caused" it, and gets popup-blocked. Filling in its
    // location once the content's actually in hand still shows the
    // browser's native viewer for anything it can render (PDFs chief among
    // them) instead of always forcing a download the way this used to,
    // unconditionally, for every non-image type.
    const preopenedTab = isImg(f) ? null : window.open("", "_blank");

    try {
      const fullPath = f.path.includes("/") ? f.path : joinPath(path, f.path);
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: fullPath } };
      });
      if (resp.payload?.$case !== "respFile" || !resp.payload.respFile.content) {
        preopenedTab?.close();
        return;
      }
      const url = bytesToURL(resp.payload.respFile.content as Uint8Array, resp.payload.respFile.mime);
      if (isImg(f)) {
        setViewer({ name: leafName(f.path), url });
      } else if (preopenedTab) {
        preopenedTab.location.href = url;
      } else {
        // Popup blocked (or the browser otherwise refused window.open) -
        // falling all the way back to a forced download beats losing the
        // file entirely.
        const a = document.createElement("a");
        a.href = url;
        a.download = leafName(f.path);
        document.body.appendChild(a); a.click(); a.remove();
        URL.revokeObjectURL(url);
      }
    } catch {
      preopenedTab?.close();
    } finally {
      setOpeningPath(null);
    }
  };

  // -------- selection + actions (6) ----------
  const rowKey = (f: PbFile) => f.path; // path is unique in a listing
  const toggleOne = (f: PbFile) => setSel(s => ({ ...s, [rowKey(f)]: !s[rowKey(f)] }));
  const allChecked = listing.length > 0 && listing.every(f => sel[rowKey(f)]);
  const toggleAll = () => {
    if (allChecked) setSel({});
    else {
      const next: Record<string, boolean> = {};
      listing.forEach(f => next[rowKey(f)] = true);
      setSel(next);
    }
  };
  const selected = useMemo(() => listing.filter(f => sel[rowKey(f)]), [listing, sel]);

  const delSelected = async () => {
    if (!selected.length) return;
    if (!window.confirm(`Delete ${selected.length} item${selected.length > 1 ? "s" : ""}? This cannot be undone.`)) return;
    for (const f of selected) {
      const full = f.path.includes("/") ? f.path : joinPath(path, f.path);
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqDelFile", reqDelFile: { path: full } };
      });
    }
    await loadList(path);
  };

  // Same reasoning as the Photo Gallery's own share actions: the device
  // reads every selected file and builds an archive before a link exists,
  // which is seconds of apparent nothing without an indicator.
  const [preparing, setPreparing] = useState<null | "share" | "zip">(null);

  const shareOrZip = async (openZip: boolean) => {
    if (!selected.length || preparing) return;
    setPreparing(openZip ? "zip" : "share");
    try {
      const paths = selected.map(f => f.path.includes("/") ? f.path : joinPath(path, f.path));
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqShareFilesLink", reqShareFilesLink: { paths } };
      });
      if (resp.payload?.$case === "respShareLink" && resp.payload.respShareLink.link) {
        const link = resp.payload.respShareLink.link;
        if (openZip) window.open(link, "_blank");
        else {
          try { await navigator.clipboard.writeText(link); alert("Share link copied to clipboard"); }
          catch { window.open(link, "_blank"); }
        }
      }
    } finally {
      setPreparing(null);
    }
  };

  // -------- Path editing (3) ----------
  const pathInputRef = useRef<HTMLInputElement>(null);
  const onPathKey: React.KeyboardEventHandler<HTMLInputElement> = (e) => {
    if (e.key === "Enter") {
      const v = normPath((e.target as HTMLInputElement).value);
      setPath(v);
    }
  };

  // ---- Issue #86: overall upload progress ----
  const uploadsDone = uploads.filter((u) => u.status === "done").length;
  const uploadsFailed = uploads.filter((u) => u.status === "failed").length;
  const uploadsActive = uploads.length > 0 && uploads.some((u) => u.status === "reading" || u.status === "sending");
  const uploadsPct = uploads.length ? Math.round((uploadsDone / uploads.length) * 100) : 0;

  // ---- rows prepared for display ----
  const rows = useMemo(() => listing.map((f) => ({
    k: rowKey(f),
    name: f.path === ".." ? ".." : leafName(f.path),
    isDir: isDir(f),
    size: f.size,
    created: f.created,
    modified: f.modified,
    file: f,
  })), [listing]);

  return (
    <div
      className={`fb-wrap ${dragOver ? "drag" : ""}`}
      onDrop={onDrop}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
    >
      <div className="fb-toolbar">
        <div className="fb-path">
          <span className="fb-label">Path:</span>
          <input
            ref={pathInputRef}
            defaultValue={path}
            className="fb-path-input"
            onKeyDown={onPathKey}
            onBlur={(e)=> (e.currentTarget.value && normPath(e.currentTarget.value) !== path) && setPath(normPath(e.currentTarget.value))}
          />
        </div>
      {selected.length > 0 && (
        <div className="fb-actions">
          <div className="fb-actions-inner">
            <div><strong>{selected.length}</strong> selected</div>
            <div className="grow" />
            <button className="btn danger" onClick={() => void delSelected()}>Delete</button>
            <button className="btn" onClick={() => void shareOrZip(false)} disabled={!!preparing}>
              {preparing === "share" ? <Spinner label="Preparing…" /> : "Share"}
            </button>
            <button className="btn" onClick={() => void shareOrZip(true)} disabled={!!preparing}>
              {preparing === "zip" ? <Spinner label="Preparing ZIP…" /> : "Download ZIP"}
            </button>
          </div>
        </div>
      )}

        <div className="fb-total">Total files: <strong>{rows.length}</strong></div>
      </div>

      {error && <div className="fb-error">{error}</div>}

      {/* Issue #86: upload progress panel - shown while any dropped file is
          still reading/sending, or briefly after the batch settles. */}
      {uploads.length > 0 && (
        <div className="fb-uploads">
          <div className="fb-uploads-head">
            <span className="fb-uploads-title">
              {uploadsActive
                ? `Uploading ${uploadsDone}/${uploads.length}…`
                : uploadsFailed
                  ? `Uploads finished (${uploadsDone} ok, ${uploadsFailed} failed)`
                  : `Uploaded ${uploadsDone} file${uploadsDone === 1 ? "" : "s"}`}
            </span>
            <span className="fb-uploads-pct">{uploadsPct}%</span>
          </div>
          <div className="fb-uploads-bar">
            <div className="fb-uploads-fill" style={{ width: `${uploadsPct}%` }} />
          </div>
          <ul className="fb-uploads-list">
            {uploads.map((u, i) => (
              <li key={i} className={`fb-uploads-item ${u.status}`}>
                <span className="fb-uploads-name">{u.name}</span>
                <span className="fb-uploads-size">{fmtBytes(u.size)}</span>
                <span className="fb-uploads-status">
                  {u.status === "reading" && "Reading…"}
                  {u.status === "sending" && <span className="fb-spinner" />} Uploading…
                  {u.status === "done" && "✓ Done"}
                  {u.status === "failed" && "✕ Failed"}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="fb-table">
        <div className="fb-head">
          <div className="c c-check"><input type="checkbox" checked={allChecked} onChange={toggleAll} /></div>
          <div className="c c-name">Name</div>
          <div className="c c-size">Size</div>
          <div className="c c-created">Created</div>
          <div className="c c-modified">Modified</div>
        </div>

        <div className="fb-body">
          {loading && <div className="fb-row">Loading…</div>}
          {!loading && rows.map(r => (
            <div className="fb-row" key={r.k}>
              <div className="c c-check">
                <input type="checkbox" checked={!!sel[r.k]} onChange={() => toggleOne(r.file)} />
              </div>
              <div className="c c-name">
                <button
                  className={`${r.isDir ? "link" : "file"}`}
                  title={r.name}
                  onClick={() => openEntry(r.file)}
                  disabled={openingPath !== null}
                >
                  {/* Issue #71: a spinner in place of the row's own label
                      while its GetFile round trip is in flight - the only
                      feedback a click used to get was however long that
                      took, which just looked stuck. */}
                  {openingPath === r.file.path ? <span className="fb-opening">Opening…</span> : r.name}
                </button>
              </div>
              <div className="c c-size">{r.isDir ? "—" : fmtBytes(r.size)}</div>
              <div className="c c-created">{r.created ? r.created.toLocaleString() : "—"}</div>
              <div className="c c-modified">{r.modified ? r.modified.toLocaleString() : "—"}</div>
            </div>
          ))}
        </div>
      </div>

      <div className="fb-tip">Tip: Drag files into the table to upload to <code>{path}</code>.</div>

      {viewer && (
        <div className="fb-modal" onClick={() => { URL.revokeObjectURL(viewer.url); setViewer(null); }}>
          <div className="fb-modal-body" onClick={(e)=>e.stopPropagation()}>
            <button className="fb-close" onClick={() => { URL.revokeObjectURL(viewer.url); setViewer(null); }}>✕</button>
            <img className="fb-full" src={viewer.url} alt={viewer.name} />
            <div className="fb-cap">{viewer.name}</div>
          </div>
        </div>
      )}
    </div>
  );
}

