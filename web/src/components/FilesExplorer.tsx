// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { uploadFile } from "../net/upload";
import { fileSize } from "../net/fileSize";
import type {
  ReqEnvelope,
  RespEnvelope,
  ListOfFiles,
  File as PbFile,
} from "../proto/messages";
import { loadFilesPath, saveFilesPath } from "../net/uiState";
import { deviceCantSearchFiles, fold, foundParts, takeFilesRequest, useFilesRequest, type Span } from "./filesNav";
import SharedGalleryShare from "./SharedGalleryShare";
import FileTypeIcon from "./FileTypeIcon";
import MediaViewer, { type ViewerItem } from "./MediaViewer";
import { reloadAfterImagesChanged } from "./libraryStore";
import "./FilesExplorer.css";
import Spinner from "./Spinner";

type Props = {
  wsUrl?: string;            // defaults to VITE_WS_URL
  initialPath?: string;      // defaults to "/"
  requireAuth?: boolean;     // default true
};

const isDir = (f: PbFile) => f.mime === "inode/directory";
const isImg = (f: PbFile) => f.mime?.startsWith("image/");
// What the grid shows a thumbnail for (GetThumbnails answers these).
const isMedia = (f: PbFile) => !!f.mime && (f.mime.startsWith("image/") || f.mime.startsWith("video/")) || /\.heic$/i.test(f.path);
const isVideo = (f: PbFile) => !!f.mime?.startsWith("video/");

// The Files list or grid, remembered in this browser.
type ViewMode = "list" | "grid";
const cViewModeKey = "otc.files.viewMode";
function loadViewMode(): ViewMode {
  try { return localStorage.getItem(cViewModeKey) === "grid" ? "grid" : "list"; } catch { return "list"; }
}
// The grid asks for thumbnails this many paths at a time.
const cThumbBatch = 24;

// The top bar's "Search documents" (filesNav.searchInFiles): every file and
// folder whose path holds the text, as SearchFiles finds them - at most
// this many, the most it answers - listed over the folder, which stays as
// it was (path, list or grid, selection) for the way back.
const cSearchLimit = 50;
// An error says what went wrong, and whether trying again may help.
type Results = { text: string; state: "loading" | "done" | "error"; files: PbFile[]; error?: string; retry?: boolean };
// What a result is, read out with its name.
const kindWord = (f: PbFile) => (isDir(f) ? "folder" : isVideo(f) ? "video" : isMedia(f) ? "photo" : "file");

// Issue #192: a folder kept out of Images - its photos and videos are
// never tagged or searched for faces and Images leaves them out, while
// Files still shows them. The same words as the other apps (the iOS and
// Android OutOfImagesText).
const noImgHere = "photos and videos here aren't tagged, searched for faces or shown in Images.";
const NoImg = {
  show: "Show in Images",
  state: "Kept out of Images",
  stateInline: "kept out of Images",
  // The switch's name, the same pressed or not (aria-pressed says which):
  // "Keep Backups out of Images, pressed".
  keepNamed: (name: string) => `Keep ${name} out of Images`,
  keepTitle: (name: string) => `Keep “${name}” out of Images?`,
  keepMessage: "Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them.",
  showTitle: (name: string) => `Show “${name}” in Images?`,
  showMessage: "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background.",
  banner: `Kept out of Images - ${noImgHere}`,
  // In a folder only kept out because a folder above it is: which one,
  // and the banner's button shows that one (the only Show that works).
  bannerByParent: (parent: string) => `Inside ${parent}, which is kept out of Images - ${noImgHere}`,
  showNamed: (name: string) => `Show “${name}” in Images`,
  byParentTip: (parent: string) => `Inside ${parent}, which is kept out of Images`,
  tipOn: "Kept out of Images: photos and videos here aren't tagged, searched for faces or shown in Images. Click to show them in Images.",
  tipOff: "Keep out of Images: photos and videos here won't be tagged, searched for faces or shown in Images",
  // Read out once a change is done.
  keptSaid: (name: string) => `${name} kept out of Images`,
  shownSaid: (name: string) => `${name} shown in Images`,
};

// A picture (frame, hills, sun) struck through, drawn like the lock: a
// gap is masked out of the picture along the stroke so it reads at 14px.
function NoImgIcon({ size = 14 }: { size?: number }) {
  const mask = `fb-noimg-${useId().replace(/[^\w-]/g, "")}`;
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="24" height="24">
        <rect width="24" height="24" fill="#fff" stroke="none" />
        <path d="M3 3 21 21" stroke="#000" strokeWidth="5.5" />
      </mask>
      <g mask={`url(#${mask})`}>
        <rect x="3" y="4.5" width="18" height="15" rx="2.5" />
        <path d="m3.5 17 5.5-5.5 4 4 2.5-2.5 5 5" />
        <circle cx="15.5" cy="9" r="1.5" />
      </g>
      <path d="M3 3 21 21" />
    </svg>
  );
}

// A found name or folder with the part the text matched marked.
function Marked({ text, span }: { text: string; span?: Span }) {
  if (!span) return <>{text}</>;
  return <>{text.slice(0, span[0])}<mark className="fb-mark">{text.slice(span[0], span[1])}</mark>{text.slice(span[1])}</>;
}

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

// Security advisory (web app, HTML/SVG in the app's origin): a file opened
// in a tab is a blob: URL of this app's own origin, so an HTML or SVG file
// opened that way ran with full access to the app (its session token, the
// window that opened it). Only types that can't run script are opened;
// everything else is downloaded - never rendered.
const INLINE_EXT = /\.(pdf|txt|md|csv|log|json|jpe?g|png|gif|webp|bmp|heic|heif|avif|mp4|mov|m4v|webm|mkv|mp3|m4a|aac|wav|ogg|oga|flac)$/i;
const INLINE_MIME = /^(application\/pdf|text\/plain|text\/csv|text\/markdown|application\/json|image\/(jpeg|png|gif|webp|bmp|heic|heif|avif)|video\/[\w.+-]+|audio\/[\w.+-]+)(;.*)?$/i;
function canOpenInline(name: string) { return INLINE_EXT.test(name); }
function safeToOpen(mime: string) { return INLINE_MIME.test(mime || ""); }

function downloadBytes(parts: Uint8Array[], name: string) {
  // octet-stream: saved, never rendered, whatever the file is.
  const url = blobURL(parts, "application/octet-stream");
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function bytesToURL(bytes: Uint8Array, mime = "application/octet-stream") {
  return blobURL([bytes], mime);
}

function blobURL(parts: Uint8Array[], type: string) {
  return URL.createObjectURL(new Blob(parts as BlobPart[], { type }));
}

// Points a tab opened for the click at the file. The tab keeps using the
// URL after it loads (a reload, the PDF viewer's own Save), so it is
// released once the tab is closed rather than after a set time.
function showInTab(tab: Window, parts: Uint8Array[], mime: string) {
  const url = blobURL(parts, mime);
  tab.location.href = url;
  const t = window.setInterval(() => {
    if (tab.closed) { URL.revokeObjectURL(url); window.clearInterval(t); }
  }, 5000);
}

// A file in pieces of at most 4 MB (ReadFile), as the sync clients read
// it. GetFile answers with the whole file in one message: the device holds
// about three times the file in memory while every other download waits
// behind it, and past the bridge's message limit the download just fails.
// Its original bytes and mime, or null if the device stopped answering.
//
// Each piece looks the path up again, so a file replaced while it is read
// (a sync client overriding it, a new version in an upload-only folder)
// would join the old content's start to the new one's end. A piece of
// other content starts the read over once, for the new content, as the
// whole-file GetFile always gave one version.
const cReadChunk = 4 << 20;
const cChanged = Symbol("changed");
async function readAll(path: string, hash = ""): Promise<{ parts: Uint8Array[]; mime: string } | null> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const got = await readOnce(path, hash);
    if (got !== cChanged) return got;
  }
  return null;
}
async function readOnce(path: string, hash: string): Promise<{ parts: Uint8Array[]; mime: string } | null | typeof cChanged> {
  const parts: Uint8Array[] = [];
  let mime = "";
  let content = "";
  let offset = 0;
  let total = -1;
  while (total < 0 || offset < total) {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      e.payload = { $case: "reqReadFile", reqReadFile: { path, hash, offset: BigInt(offset), length: cReadChunk } };
    });
    if (resp.payload?.$case !== "respFileChunk") return null;
    const chunk = resp.payload.respFileChunk;
    if (total < 0) {
      mime = chunk.mime;
      content = chunk.hash;
    } else if (chunk.hash !== content || Number(chunk.size) !== total) {
      return cChanged;
    }
    total = Number(chunk.size);
    if (chunk.data.length === 0 && offset < total) return null;
    parts.push(chunk.data);
    offset += chunk.data.length;
  }
  return { parts, mime };
}

// What the device converts for display (HEIC to JPEG, see GetFile's
// isHeicFile) still comes through GetFile, converted, as it always has.
const isHeicName = (name: string) => /\.heic$/i.test(name);

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
  // The folder the last listing that came back was of, set with it (a new
  // object each time, a failed one too): a photo the search asked to open
  // waits for its own folder's listing.
  const [listed, setListed] = useState<{ path: string } | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [dragOver, setDragOver] = useState(false);

  // Issue #132: the versions pop-up - which file it is for and the older
  // versions the device listed (newest first), or null when closed.
  const [versionsOf, setVersionsOf] = useState<{ path: string; name: string; mime: string; versions: PbFile[] } | null>(null);
  const [versionsLoading, setVersionsLoading] = useState(false);

  // Issue #192: what the last listing said about keeping folders out of
  // Images - whether the device can (devices before release 108 leave it
  // false: no control, marker or banner then) and whether the folder it
  // listed is kept out, itself or by a folder above it - with that folder,
  // so the banner never shows over another folder's failed listing.
  // `cover`, for a folder kept out: the outermost kept-out folder at or
  // above it (with its slash), from ListOutOfImages - the one whose Show
  // the device takes, as no folder above it covers it. Undefined while
  // asked, null when that failed.
  type NoImgState = { path: string; supported: boolean; folderOut: boolean; cover?: string | null };
  const [noImg, setNoImg] = useState<NoImgState>({ path: "", supported: false, folderOut: false });
  // The folder whose change is on its way: every other change waits.
  const [noImgBusy, setNoImgBusy] = useState<string | null>(null);
  const noImgBusyRef = useRef<string | null>(null);
  // What a screen reader hears once a change is done.
  const [noImgSaid, setNoImgSaid] = useState("");
  // Where the focus goes back to once the folder is listed again after a
  // change: that folder's switch, or the banner's button - or the
  // folder's contents when it is gone. The tick runs the effect below.
  const noImgRefocus = useRef<{ path: string; banner: boolean } | null>(null);
  const [noImgRefocusTick, setNoImgRefocusTick] = useState(0);

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
  // Only the last listing asked for lands: another folder opened (or the
  // search sending Files elsewhere) while one was on its way would
  // otherwise be overwritten by the earlier folder's answer.
  const listSeq = useRef(0);
  // The folder being listed right now, if any.
  const listingNow = useRef<string | null>(null);
  // A photo or video the search picked, to open once its folder's listing
  // is in (see the request below).
  const mediaToOpen = useRef<{ dir: string; file: PbFile } | null>(null);
  const loadList = useCallback(async (p: string) => {
    const seq = ++listSeq.current;
    listingNow.current = p;
    // Files went to another folder before the picked photo's came in: the
    // photo is not opened, then or when that folder is shown again later.
    if (mediaToOpen.current && mediaToOpen.current.dir !== p) mediaToOpen.current = null;
    setLoading(true); setError(null);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        console.log('Listing path:', p);
        (e as any).payload = { $case: "reqListFiles", reqListFiles: { path: p } };
      });
      if (seq !== listSeq.current) return;
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
          uploadOnly: false,
          versions: 0,
          size64: 0n,
          outOfImages: false,
        };
        // Don’t add .. at root
        const files = p === "/" ? lof.files : [up, ...lof.files];

        setListing(files);
        setListed({ path: p });
        const folderOut = !!lof.outOfImagesSupported && !!lof.folderOutOfImages;
        setNoImg({ path: p, supported: !!lof.outOfImagesSupported, folderOut, cover: folderOut ? undefined : null });
        setSel({});
        // Issue #192: in a folder kept out, which folder keeps it out - for
        // the banner's Show and the subfolders' tips. Asked after the
        // listing shows, so a slow answer never holds it up.
        if (folderOut) {
          const key = p.endsWith("/") ? p : p + "/";
          const coverIs = (cover: string | null) => {
            if (seq === listSeq.current) setNoImg((n) => (n.path === p ? { ...n, cover } : n));
          };
          useWS.request((e: Partial<ReqEnvelope>) => {
            e.payload = { $case: "reqListOutOfImages", reqListOutOfImages: {} };
          }).then((r: RespEnvelope) => {
            const kept = r.payload?.$case === "respOutOfImagesFolders" ? r.payload.respOutOfImagesFolders.paths : [];
            // The outermost: nothing above it keeps it out, so its Show is
            // taken (one inside it would be refused).
            const above = kept.filter((f) => f.endsWith("/") && key.startsWith(f));
            coverIs(above.length ? above.reduce((a, b) => (b.length < a.length ? b : a)) : null);
          }, () => coverIs(null));
        }
      } else if (resp.error) {
        setError(resp.errorMessage || "Failed to list path");
        setListed({ path: p });
      } else {
        setError("Unexpected response");
        setListed({ path: p });
      }
    } catch (e: any) {
      if (seq !== listSeq.current) return;
      setError(e?.message ?? String(e));
      setListed({ path: p });
    } finally {
      if (seq === listSeq.current) {
        listingNow.current = null;
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => { void loadList(path); }, [path, loadList]);
  useEffect(() => { saveFilesPath(path); }, [path]);
  const pathRef = useRef(path);
  pathRef.current = path;

  // -------- drag & drop upload (2) ----------
  // Issue #86: each dropped file is tracked through its phases so the UI can
  // show an upload progress panel instead of a blank screen. The per-file
  // "sending" phase covers the whole upload (in 4 MB pieces, see
  // net/upload.ts), so it does not report byte-level progress - but seeing
  // which file is in flight and the overall "N/M done" is what was missing.
  const onDrop: React.DragEventHandler<HTMLDivElement> = async (ev) => {
    ev.preventDefault(); ev.stopPropagation(); setDragOver(false);
    const files = Array.from(ev.dataTransfer.files ?? []);
    if (!files.length) return;
    // Over a search's results: back to the folder they go to, to see them
    // arrive.
    if (resultsRef.current) closeResults();

    // Seed the tracker with one row per dropped file up front, so the panel
    // appears immediately even before the first read/send finishes.
    setUploads(files.map((f) => ({ name: f.name, size: f.size, status: "reading" as const })));
    clearUploadsAfter(4000);

    let failed = 0;
    for (let i = 0; i < files.length; i++) {
      const f = files[i];
      setUploads((prev) => prev.map((u, j) => (j === i ? { ...u, status: "sending" } : u)));
      try {
        // The device refuses with a reply, not a dropped request: a file
        // edited and dropped onto its own name ("Duplicated file"), or a
        // failed disk write. The listing still shows the old file by that
        // name, so a "Done" here would say the new content was kept. An
        // upload that throws (the socket closed, or the device stopped
        // taking pieces) lands in the catch below.
        const resp: RespEnvelope = await uploadFile(joinPath(path, f.name), f, false);
        if (resp.error || resp.payload?.$case !== "respFile") {
          failed++;
          setUploads((prev) => prev.map((u, j) => (j === i ? { ...u, status: "failed" } : u)));
          console.error("Upload failed for", f.name, resp.errorMessage);
          continue;
        }

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
    if (!isDir(f) && isMedia(f)) {
      openMedia(f);
      return;
    }
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
    // click, for a non-image - a tab opened later, after the fetch
    // below resolves, reads to the browser as unrelated to the
    // click that "caused" it, and gets popup-blocked. Filling in its
    // location once the content's actually in hand still shows the
    // browser's native viewer for anything it can render (PDFs chief among
    // them) instead of always forcing a download the way this used to,
    // unconditionally, for every non-image type.
    const opensInline = !isImg(f) && canOpenInline(leafName(f.path));
    const preopenedTab = opensInline ? window.open("", "_blank") : null;
    // No handle back into the app for whatever the file links to (a link
    // in a PDF could otherwise point this tab at a fake sign-in page).
    // Not "noopener": that returns null, and the tab is still needed here.
    if (preopenedTab) preopenedTab.opener = null;

    try {
      const fullPath = f.path.includes("/") ? f.path : joinPath(path, f.path);
      if (!isImg(f) && !isHeicName(f.path)) {
        const got = await readAll(fullPath);
        if (!got) {
          preopenedTab?.close();
          return;
        }
        // The device's own reading of the content decides, as below.
        if (preopenedTab && safeToOpen(got.mime)) {
          showInTab(preopenedTab, got.parts, got.mime);
        } else {
          preopenedTab?.close();
          downloadBytes(got.parts, leafName(f.path));
        }
        return;
      }
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: fullPath } };
      });
      if (resp.payload?.$case !== "respFile" || !resp.payload.respFile.content) {
        preopenedTab?.close();
        return;
      }
      const bytes = resp.payload.respFile.content as Uint8Array;
      const mime = resp.payload.respFile.mime;
      // The device's own reading of the content decides too: a ".txt"
      // that is really HTML is not opened.
      if (isImg(f) && safeToOpen(mime)) {
        setViewer({ name: leafName(f.path), url: bytesToURL(bytes, mime) });
      } else if (preopenedTab && safeToOpen(mime)) {
        showInTab(preopenedTab, [bytes], mime);
      } else {
        // Anything else - and a blocked popup - is downloaded.
        preopenedTab?.close();
        downloadBytes([bytes], leafName(f.path));
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

  // Issue #132: nothing under an upload-only folder can be deleted - the
  // device refuses with error_code "upload_only", and the button is greyed
  // out up front so the refusal is never a surprise.
  const selectedUploadOnly = useMemo(() => selected.some(f => f.uploadOnly), [selected]);
  // Issue #180: one folder selected can be shared as a gallery of its
  // photos and videos.
  const [sharingFolder, setSharingFolder] = useState<string | null>(null);
  const singleFolder = selected.length === 1 && isDir(selected[0]) ? selected[0].path : null;

  const delSelected = async () => {
    if (!selected.length || selectedUploadOnly) return;
    if (!window.confirm(`Delete ${selected.length} item${selected.length > 1 ? "s" : ""}? This cannot be undone.`)) return;
    for (const f of selected) {
      const full = f.path.includes("/") ? f.path : joinPath(path, f.path);
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqDelFile", reqDelFile: { path: full } };
      });
      if (resp.error && resp.errorCode === "upload_only") {
        alert(`${leafName(full)} is in an upload-only folder and cannot be deleted.`);
        break;
      }
    }
    await loadList(path);
  };

  // Issue #132: the lock next to a folder - flag it upload only, or clear
  // it. Everything under it (and its lock) follows on the next listing.
  const toggleUploadOnly = async (f: PbFile) => {
    const full = f.path.includes("/") ? f.path : joinPath(path, f.path);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqSetUploadOnly", reqSetUploadOnly: { path: full, uploadOnly: !f.uploadOnly } };
    });
    // Issue #186: a folder inside another upload-only one can't be
    // unlocked on its own; the device says which folder to unlock, as the
    // apps show it. Silently reloading left the lock on with no word.
    if (resp.error) alert(resp.errorMessage || "Could not update the folder.");
    await loadList(path);
  };

  // Issue #192: keep a folder (its full path) out of Images, or show it
  // there again (`out`: whether it is kept out now; `from`: the folder's
  // switch or the banner's button). Asked first either way: keeping it
  // out deletes the tags and faces found in its photos, and showing it
  // again searches them for faces when that is on. Only offered where the
  // device takes it - a folder inside another one kept out has no switch,
  // and the banner shows the outermost one - but should the device still
  // refuse (another app changed it meanwhile: error_code
  // "out_of_images_by_parent"), its message says why. Then the folder is
  // listed again, and the tags, people and collections too, for the next
  // visit to Images. Every other change waits until this one is done, and
  // the focus comes back to where it was.
  const toggleOutOfImages = async (full: string, out: boolean, from: "row" | "banner") => {
    if (noImgBusyRef.current) return;
    const name = leafName(full.replace(/\/+$/, ""));
    const ask = out ? `${NoImg.showTitle(name)}\n\n${NoImg.showMessage}` : `${NoImg.keepTitle(name)}\n\n${NoImg.keepMessage}`;
    if (!window.confirm(ask)) return;
    const listedAt = pathRef.current;
    const opener = document.activeElement;
    noImgBusyRef.current = full;
    setNoImgBusy(full);
    setNoImgSaid("");
    try {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqSetOutOfImages", reqSetOutOfImages: { path: full, outOfImages: !out } };
        });
        const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : null;
        if (resp.error || !ack?.ok) alert(resp.errorMessage || ack?.errorMsg || "Could not update the folder.");
        else {
          reloadAfterImagesChanged();
          setNoImgSaid(out ? NoImg.shownSaid(name) : NoImg.keptSaid(name));
        }
      } catch {
        alert("Could not update the folder.");
      }
      // Unless it moved on meanwhile (another folder, a field), the focus
      // goes back once the rows are there again: the listing replaces them.
      const active = document.activeElement;
      const refocus = pathRef.current === listedAt && (active === opener || !active || active === document.body);
      await loadList(pathRef.current);
      if (refocus && pathRef.current === listedAt) {
        noImgRefocus.current = { path: full, banner: from === "banner" };
        setNoImgRefocusTick((t) => t + 1);
      }
    } finally {
      noImgBusyRef.current = null;
      setNoImgBusy(null);
    }
  };
  useEffect(() => {
    const want = noImgRefocus.current;
    if (!want || loading) return;
    // The banner's button comes once ListOutOfImages has answered.
    if (want.banner && noImg.folderOut && noImg.cover === undefined) return;
    noImgRefocus.current = null;
    const view = folderViewRef.current;
    const target = want.banner
      ? document.querySelector<HTMLElement>(".fb-noimg-banner-show")
      : view?.querySelector<HTMLElement>(`button.fb-noimg[data-noimg="${CSS.escape(want.path)}"]`);
    // The banner went with the change: the folder's contents instead, as
    // when a search's results close.
    if (target) target.focus();
    else view?.focus({ preventScroll: true });
  }, [noImgRefocusTick, loading, noImg]);

  // Issue #132: the versions badge - list a file's older versions.
  const openVersions = async (f: PbFile) => {
    const full = f.path.includes("/") ? f.path : joinPath(path, f.path);
    setVersionsLoading(true);
    setVersionsOf({ path: full, name: leafName(full), mime: f.mime, versions: [] });
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqListFileVersions", reqListFileVersions: { path: full } };
      });
      if (resp.payload?.$case === "respFileVersions") {
        setVersionsOf({ path: full, name: leafName(full), mime: f.mime, versions: resp.payload.respFileVersions.versions });
      }
    } finally {
      setVersionsLoading(false);
    }
  };

  // A version downloads by its hash; the current one is the row itself.
  const downloadVersion = async (full: string, hash: string, name: string, mime: string) => {
    let url: string;
    if (isHeicName(name) || mime.toLowerCase() === "image/heic") {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: full, hash } };
      });
      if (resp.payload?.$case !== "respFile" || !resp.payload.respFile.content) return;
      url = bytesToURL(resp.payload.respFile.content as Uint8Array, resp.payload.respFile.mime);
    } else {
      const got = await readAll(full, hash);
      if (!got) return;
      url = blobURL(got.parts, got.mime);
    }
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);
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
    size: fileSize(f),
    created: f.created,
    modified: f.modified,
    uploadOnly: !!f.uploadOnly,
    outOfImages: !!f.outOfImages,
    versions: f.versions ?? 0,
    file: f,
  })), [listing]);

  // -------- grid view ----------
  const [viewMode, setViewMode] = useState<ViewMode>(loadViewMode);
  const switchView = (m: ViewMode) => {
    setViewMode(m);
    try { localStorage.setItem(cViewModeKey, m); } catch { /* remembered for this visit only */ }
  };
  // Thumbnails by full path ("" for one the device has none of), kept
  // while the page is open; object URLs are released on the way out.
  const [thumbs, setThumbs] = useState<Record<string, string>>({});
  const thumbsRef = useRef<Record<string, string>>({});
  thumbsRef.current = thumbs;
  const thumbsAsked = useRef<Set<string>>(new Set());
  useEffect(() => () => { Object.values(thumbsRef.current).forEach(u => u && URL.revokeObjectURL(u)); }, []);
  const fullPathOf = useCallback((f: PbFile) => (f.path.includes("/") ? f.path : joinPath(path, f.path)), [path]);

  // Only tiles on or near the screen ask for theirs, as the iOS app does:
  // a phone's whole library syncs into one folder, and asking for every
  // photo in it up front sent hundreds of requests and held every result.
  // One queue, one batch in flight at a time. What is still queued when
  // the listing changes (Refresh, an upload, another folder) is forgotten,
  // so the tiles of the next listing ask for it again rather than keep
  // their type icon until a reload.
  const gridRef = useRef<HTMLDivElement>(null);
  const thumbQueue = useRef<string[]>([]);
  const pumping = useRef(false);
  const pumpThumbs = useCallback(async () => {
    if (pumping.current) return;
    pumping.current = true;
    try {
      while (thumbQueue.current.length) {
        const batch = thumbQueue.current.splice(0, cThumbBatch);
        try {
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            // The tiles' small thumbnails (release 111; older devices send
            // big ones). The viewer shows them only until the full size
            // arrives, and asks for the big one itself where it would stay
            // (MediaViewer).
            e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: batch, smallThumbnails: true } };
          });
          const got: Record<string, string> = {};
          if (resp.payload?.$case === "respListOfFiles") {
            for (const f of resp.payload.respListOfFiles.files) {
              if (f.content?.length) got[f.path] = bytesToURL(f.content as Uint8Array, "image/jpeg");
            }
          }
          // A path the device didn't answer has no thumbnail: its type icon stays.
          for (const p of batch) if (!(p in got)) got[p] = "";
          setThumbs(t => ({ ...t, ...got }));
        } catch {
          batch.forEach(p => thumbsAsked.current.delete(p)); // tried again on the next visit
        }
      }
    } finally {
      pumping.current = false;
    }
  }, []);
  const wantThumbs = useCallback((paths: string[]) => {
    for (const p of paths) {
      if (thumbsAsked.current.has(p)) continue;
      thumbsAsked.current.add(p);
      thumbQueue.current.push(p);
    }
    void pumpThumbs();
  }, [pumpThumbs]);
  useEffect(() => {
    if (viewMode !== "grid" || loading || !gridRef.current) return;
    const asked = thumbsAsked.current;
    // The viewport as the root, 1000px ahead: an ancestor that scrolls
    // still clips, whichever element it is.
    const obs = new IntersectionObserver((entries) => {
      const paths: string[] = [];
      for (const en of entries) {
        if (!en.isIntersecting) continue;
        const el = en.target as HTMLElement;
        obs.unobserve(el);
        if (el.dataset.thumb) paths.push(el.dataset.thumb);
      }
      if (paths.length) wantThumbs(paths);
    }, { rootMargin: "1000px 0px" });
    gridRef.current.querySelectorAll<HTMLElement>("[data-thumb]").forEach(el => {
      if (!asked.has(el.dataset.thumb!)) obs.observe(el);
    });
    return () => {
      obs.disconnect();
      thumbQueue.current.forEach(p => asked.delete(p));
      thumbQueue.current = [];
    };
  }, [viewMode, loading, rows, wantThumbs]);

  // Photos and videos open in the Images section's own viewer
  // (MediaViewer), paging through this folder's photos and videos.
  const [mediaViewer, setMediaViewer] = useState<{ items: ViewerItem[]; index: number } | null>(null);
  // What had the focus when the viewer opened (the row clicked): it has it
  // again once the viewer closes, rather than the page.
  const viewerOpener = useRef<HTMLElement | null>(null);
  const showViewer = (v: { items: ViewerItem[]; index: number }) => {
    viewerOpener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setMediaViewer(v);
  };
  const closeViewer = () => {
    setMediaViewer(null);
    const el = viewerOpener.current;
    viewerOpener.current = null;
    if (el?.isConnected) el.focus({ preventScroll: true });
  };
  const openMedia = (f: PbFile) => {
    const media = listing.filter(x => !isDir(x) && isMedia(x));
    const items: ViewerItem[] = media.map(x => {
      const full = fullPathOf(x);
      return { path: full, mime: x.mime, thumbURL: thumbs[full] || undefined };
    });
    const index = Math.max(0, media.findIndex(x => x.path === f.path));
    showViewer({ items, index });
  };

  // -------- the top bar's "Search documents" ----------
  // The results show over the folder, which stays mounted but hidden, as
  // it was. Only the last search asked for lands. Leaving them goes back
  // to where the folder was scrolled to, and its contents take the focus.
  const [results, setResults] = useState<Results | null>(null);
  const resultsRef = useRef(results);
  resultsRef.current = results;
  const searchSeq = useRef(0);
  // Where the folder was scrolled to when the results opened over it.
  const scrollBack = useRef(0);
  // The focus goes to the results' header once they show, and back to
  // the folder's contents (scrolled to y) once they go.
  const focusResults = useRef(false);
  const afterResults = useRef<{ y: number; focus: boolean } | null>(null);
  const resultsHeadRef = useRef<HTMLHeadingElement>(null);
  const folderViewRef = useRef<HTMLDivElement>(null);
  const uid = useId();

  const startSearch = useCallback((text: string) => {
    const seq = ++searchSeq.current;
    if (!resultsRef.current) scrollBack.current = window.scrollY;
    afterResults.current = null;
    focusResults.current = true;
    setResults({ text, state: "loading", files: [] });
    const fail = (error: string, retry = true) => {
      if (seq === searchSeq.current) setResults({ text, state: "error", files: [], error, retry });
    };
    useWS.request((e: Partial<ReqEnvelope>) => {
      e.payload = { $case: "reqSearchFiles", reqSearchFiles: { query: text, limit: cSearchLimit } };
    }).then((resp: RespEnvelope) => {
      // The top bar stops offering the search on such a device.
      if (resp.errorCode === "unknown_payload") deviceCantSearchFiles();
      if (seq !== searchSeq.current) return;
      if (resp.payload?.$case === "respListOfFiles") {
        setResults({ text, state: "done", files: resp.payload.respListOfFiles.files ?? [] });
      } else if (resp.errorCode === "unknown_payload") {
        fail("This device can't search its files yet. Update it in Settings.", false);
      } else {
        fail(resp.errorMessage || "The device didn't answer the search.");
      }
    }, () => fail("The search didn't reach the device. Check the connection and try again."));
  }, []);

  // Back to the folder. `y`: where to scroll to - where it was, unless
  // another folder is opened from the results. `focus`: the folder's
  // contents take it, unless the way back was the menu, which keeps it.
  const closeResults = useCallback((y?: number, focus = true) => {
    if (!resultsRef.current) return;
    searchSeq.current++;
    focusResults.current = false;
    afterResults.current = { y: y ?? scrollBack.current, focus };
    setResults(null);
  }, []);

  useEffect(() => {
    if (results && focusResults.current) {
      focusResults.current = false;
      window.scrollTo(0, 0);
      resultsHeadRef.current?.focus({ preventScroll: true });
    } else if (!results && afterResults.current) {
      const { y, focus } = afterResults.current;
      afterResults.current = null;
      window.scrollTo(0, y);
      if (focus) folderViewRef.current?.focus({ preventScroll: true });
    }
  }, [results]);

  // The results' photos and videos by their thumbnails, asked for again
  // if a listing of the folder underneath dropped the queue.
  useEffect(() => {
    if (results?.state !== "done") return;
    const media = results.files.filter(f => !isDir(f) && isMedia(f)).map(f => f.path);
    if (media.length) wantThumbs(media);
  }, [results, loading, wantThumbs]);

  // Escape goes back to the folder - unless something lies over the
  // results (the viewer, a pop-up, the menu's drawer: theirs to close) or
  // the key is the top bar's or a field's. Seen before anything else,
  // while whatever it is still shows.
  const overlayOpen = useRef(false);
  overlayOpen.current = !!(mediaViewer || versionsOf || viewer || sharingFolder);
  const inResults = results !== null;
  useEffect(() => {
    if (!inResults) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing || overlayOpen.current) return;
      if (document.querySelector(".sb-drawer.open")) return;
      const t = e.target as Element | null;
      if (t?.closest?.(".topbar, input, textarea, select, [contenteditable='true'], [role='dialog']")) return;
      closeResults();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [inResults, closeResults]);

  // A found folder opens in Files; a photo or video in the viewer, paging
  // through the results' photos and videos; anything else as a click on it
  // in its folder would (a tab opened inside this click, or a download).
  const openFound = (f: PbFile) => {
    if (isDir(f)) {
      const dir = normPath(f.path);
      closeResults(0);
      if (pathInputRef.current) pathInputRef.current.value = dir;
      if (dir !== pathRef.current) setPath(dir);
      else if (listingNow.current !== dir) void loadList(dir);
      return;
    }
    if (isMedia(f)) {
      const media = (results?.files ?? []).filter(x => !isDir(x) && isMedia(x));
      showViewer({
        items: media.map(x => ({ path: x.path, mime: x.mime, thumbURL: thumbs[x.path] || undefined })),
        index: Math.max(0, media.findIndex(x => x.path === f.path)),
      });
      return;
    }
    void openEntry(f);
  };

  // The top bar's search sent Files here (filesNav.ts): to a folder, and
  // maybe to a file in it, opened as a click on it would; or to the files
  // and folders a text finds. A document opens at once, still inside the
  // pick's click or key press, so the tab it may open isn't blocked as a
  // popup; a photo or video waits for its folder's listing, to page
  // through the folder in the viewer.
  const request = useFilesRequest();
  const handledRequest = useRef<typeof request>(null);
  const openEntryRef = useRef(openEntry);
  openEntryRef.current = openEntry;
  useEffect(() => {
    if (!request || handledRequest.current === request) return;
    handledRequest.current = request;
    takeFilesRequest(request);
    if ("search" in request) {
      if (request.search) startSearch(request.search);
      return;
    }
    // Files picked in the menu (leaveFilesSearch, from App): back to the
    // folder. The focus stays with the menu, or goes back to its button as
    // the drawer closes.
    if ("leave" in request) {
      closeResults(undefined, false);
      return;
    }
    closeResults(0);
    const dir = normPath(request.dir);
    if (pathInputRef.current) pathInputRef.current.value = dir;
    // Already in that folder: listed again, unless that is under way (a
    // Files just opened for it), since the search may know newer files.
    if (dir !== pathRef.current) setPath(dir);
    else if (listingNow.current !== dir) void loadList(dir);
    const f = request.file;
    mediaToOpen.current = f && isMedia(f) ? { dir, file: f } : null;
    if (f && !isMedia(f)) void openEntryRef.current(f);
  }, [request, loadList, startSearch, closeResults]);
  useEffect(() => {
    const want = mediaToOpen.current;
    if (!want || listed?.path !== want.dir) return;
    mediaToOpen.current = null;
    const full = (x: PbFile) => (x.path.includes("/") ? x.path : joinPath(want.dir, x.path));
    const media = listing.filter(x => !isDir(x) && isMedia(x));
    const index = media.findIndex(x => full(x) === want.file.path);
    // Not in the listing (it failed, or the file went): the file alone.
    const items: ViewerItem[] = index >= 0
      ? media.map(x => ({ path: full(x), mime: x.mime, thumbURL: thumbsRef.current[full(x)] || undefined }))
      : [{ path: want.file.path, mime: want.file.mime }];
    viewerOpener.current = null;
    setMediaViewer({ items, index: Math.max(0, index) });
  }, [listed, listing]);

  // The thumbnails as they are now, not as they were when the viewer
  // opened: with tiles asking only near the screen, paging on reaches
  // items whose thumbnail lands after that. The grid used to have every
  // one by then, so the item in view and its neighbours are asked for.
  const viewerItems = useMemo(
    () => mediaViewer?.items.map(it => ({ ...it, thumbURL: thumbs[it.path] || undefined })) ?? [],
    [mediaViewer?.items, thumbs],
  );
  useEffect(() => {
    if (!mediaViewer || viewMode !== "grid") return;
    const i = mediaViewer.index;
    wantThumbs(mediaViewer.items.slice(Math.max(0, i - 2), i + 3).map(it => it.path));
  }, [mediaViewer, viewMode, wantThumbs]);

  // The results as shown: name and folder, with the part that matched.
  const found = useMemo(() => {
    if (!results) return [];
    const q = fold(results.text).text;
    return results.files.map((f, k) => ({
      k, file: f, isDir: isDir(f), media: !isDir(f) && isMedia(f), size: fileSize(f), ...foundParts(f.path, q),
    }));
  }, [results]);
  // The folder the results go back to.
  const folderLabel = path === "/" ? "Files" : leafName(path.endsWith("/") ? path.slice(0, -1) : path);
  const resultsStatus = !results ? ""
    : results.state === "loading" ? `Searching for ${results.text}`
    : results.state === "error" ? results.error ?? ""
    : found.length === 0 ? `No files match ${results.text}`
    : found.length === cSearchLimit ? `Showing the first ${cSearchLimit} results`
    : `${found.length} ${found.length === 1 ? "result" : "results"}`;

  // Issue #192: the folder's switch for Images, beside its lock. Its name
  // stays the same and aria-pressed says whether the folder is kept out
  // ("Keep Backups out of Images, pressed"); the tip says what a click
  // does. While a change is on its way every switch waits, and says so.
  const noImgShown = noImg.supported && noImg.path === path;
  // In a folder kept out, every subfolder is kept out by it (or a folder
  // above it), and the device would refuse its Show: a marker naming that
  // folder then, not a switch - the banner has the way back.
  const noImgCover = noImg.folderOut ? (noImg.cover ?? normPath(path)).replace(/\/+$/, "") : "";
  const noImgButton = (r: { name: string; file: PbFile; outOfImages: boolean }, tile = false) => {
    const tileClass = tile ? " fb-tile-noimg" : "";
    if (noImg.folderOut) {
      const tip = NoImg.byParentTip(noImgCover);
      return (
        <span className={`fb-noimg${tileClass} on static`} data-tip={tip} role="img" aria-label={tip}>
          <NoImgIcon />
        </span>
      );
    }
    const full = fullPathOf(r.file);
    return (
      <button
        type="button"
        className={`fb-noimg${tileClass}${r.outOfImages ? " on" : ""}`}
        onClick={() => void toggleOutOfImages(full, r.outOfImages, "row")}
        data-tip={r.outOfImages ? NoImg.tipOn : NoImg.tipOff}
        data-noimg={full}
        aria-label={NoImg.keepNamed(r.name)}
        aria-pressed={r.outOfImages}
        aria-disabled={noImgBusy !== null || undefined}
      >
        <NoImgIcon />
      </button>
    );
  };
  // The banner's button: the listed folder when it is kept out itself,
  // else the outermost folder keeping it out (the one Show the device
  // takes) - not known until ListOutOfImages answers (no button until
  // then); when that failed, the listed folder, whose refusal says which.
  const listedKey = normPath(path);
  const noImgShow = noImg.cover === undefined ? null : (noImg.cover ?? listedKey);
  const noImgShowByParent = noImgShow !== null && noImgShow !== listedKey;
  const noImgShowPath = noImgShow?.replace(/\/+$/, "") ?? "";

  const lockIcon = (locked: boolean) => (
    <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true">
      <rect x="5" y="11" width="14" height="10" rx="2" stroke="currentColor" strokeWidth="2" fill="none" />
      {locked
        ? <path d="M8 11V7a4 4 0 0 1 8 0v4" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" />
        : <path d="M8 11V7a4 4 0 0 1 7.5-2" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" />}
    </svg>
  );

  return (
    <div
      className={`fb-wrap ${dragOver ? "drag" : ""}`}
      onDrop={onDrop}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
    >
      {/* The folder's toolbar and contents stay as they are, hidden, while
          a search's results show. */}
      <div hidden={inResults}>
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
          {/* Issue #51: re-list this directory on demand. Files arrive from
              other clients (the phone's sync, the Mac app) while this page
              sits open, and nothing else re-reads the listing. */}
          <button
            className={`fb-refresh${loading ? " spinning" : ""}`}
            onClick={() => void loadList(path)}
            disabled={loading}
            aria-label="Refresh"
            title="Refresh"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
              <path d="M20 12a8 8 0 1 1-2.34-5.66" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" />
              <path d="M20 4v5h-5" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
          <div className="fb-viewmode" role="group" aria-label="View">
            <button className={viewMode === "list" ? "on" : ""} aria-pressed={viewMode === "list"} title="List" onClick={() => switchView("list")}>
              <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M8 6h13M8 12h13M8 18h13" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
                <circle cx="4" cy="6" r="1.5" fill="currentColor" /><circle cx="4" cy="12" r="1.5" fill="currentColor" /><circle cx="4" cy="18" r="1.5" fill="currentColor" />
              </svg>
            </button>
            <button className={viewMode === "grid" ? "on" : ""} aria-pressed={viewMode === "grid"} title="Grid" onClick={() => switchView("grid")}>
              <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true">
                <rect x="3" y="3" width="7.5" height="7.5" rx="1.5" fill="currentColor" /><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5" fill="currentColor" />
                <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5" fill="currentColor" /><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5" fill="currentColor" />
              </svg>
            </button>
          </div>
        </div>
      {selected.length > 0 && (
        <div className="fb-actions">
          <div className="fb-actions-inner">
            <div><strong>{selected.length}</strong> selected</div>
            <div className="grow" />
            <button className="btn danger" onClick={() => void delSelected()} disabled={selectedUploadOnly}
              title={selectedUploadOnly ? "The selection includes an upload-only folder's contents, which cannot be deleted" : undefined}>Delete</button>
            <button className="btn" onClick={() => void shareOrZip(false)} disabled={!!preparing}>
              {preparing === "share" ? <Spinner label="Preparing…" /> : "Share"}
            </button>
            <button className="btn" onClick={() => void shareOrZip(true)} disabled={!!preparing}>
              {preparing === "zip" ? <Spinner label="Preparing ZIP…" /> : "Download ZIP"}
            </button>
            {singleFolder && (
              <button className="btn" onClick={() => setSharingFolder(singleFolder)} title="Share this folder's photos and videos as a gallery">
                Share as gallery
              </button>
            )}
          </div>
        </div>
      )}

        <div className="fb-total">Total files: <strong>{rows.length}</strong></div>
      </div>

      {error && <div className="fb-error">{error}</div>}
      {/* Issue #192: in a folder kept out of Images (or inside one), what
          that means for its photos, and the way back. Its files carry no
          marker of their own: this says it for all of them. */}
      {noImgShown && noImg.folderOut && (
        <div className="fb-noimg-banner" role="note">
          <span className="fb-noimg-banner-note">
            <span className="fb-noimg-banner-icon"><NoImgIcon size={16} /></span>
            <span>{noImgShowByParent ? NoImg.bannerByParent(noImgShowPath) : NoImg.banner}</span>
          </span>
          {noImgShow !== null && (
            <button type="button" className="fb-noimg-banner-show" onClick={() => void toggleOutOfImages(noImgShowPath, true, "banner")}
              aria-disabled={noImgBusy !== null || undefined}>
              {noImgShowByParent ? NoImg.showNamed(leafName(noImgShowPath)) : NoImg.show}
            </button>
          )}
        </div>
      )}
      {/* Issue #192: a change for Images, once done. */}
      <span className="fb-sr" role="status" aria-live="polite">{noImgSaid}</span>
      </div>

      {results && (
        <section className="fb-results" aria-labelledby={`${uid}rt`}>
          <div className="fb-results-head">
            <button type="button" className="fb-results-back" onClick={() => closeResults()}>
              <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M19 12H5m6-6-6 6 6 6" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              <span className="fb-results-back-label">Back to {folderLabel}</span>
            </button>
            <h2 ref={resultsHeadRef} id={`${uid}rt`} className="fb-results-title" tabIndex={-1}>
              Files matching “{results.text}”
            </h2>
            {/* Its line kept while searching, so the list doesn't move.
                Read out by the live region below, not twice. */}
            <p className="fb-results-count" aria-hidden="true">
              {results.state === "done" && found.length > 0 &&
                (found.length === cSearchLimit ? `Showing the first ${cSearchLimit} results` : `${found.length} ${found.length === 1 ? "result" : "results"}`)}
            </p>
          </div>
          <span className="fb-sr" aria-live="polite">{resultsStatus}</span>
          <div className="fb-table fb-results-table" aria-busy={results.state === "loading"}>
            <div className="fb-head" aria-hidden="true">
              <div className="c c-name">Name</div>
              <div className="c c-size">Size</div>
              <div className="c c-modified">Modified</div>
            </div>
            {results.state === "loading" && (
              <div className="fb-results-note" aria-hidden="true"><Spinner /><span>Searching…</span></div>
            )}
            {results.state === "error" && (
              <div className="fb-results-note is-error">
                <span aria-hidden="true">{results.error}</span>
                {results.retry && <button type="button" className="btn" onClick={() => startSearch(results.text)}>Try again</button>}
              </div>
            )}
            {results.state === "done" && found.length === 0 && (
              <div className="fb-results-note" aria-hidden="true">No files match “{results.text}”.</div>
            )}
            {found.length > 0 && (
              <ul className="fb-body fb-results-list" aria-label={`Files matching ${results.text}`}>
                {found.map(r => {
                  const thumb = r.media ? thumbs[r.file.path] : undefined;
                  const meta = r.isDir ? undefined : `${uid}m${r.k}`;
                  const metaDate = r.file.modified ? `${uid}d${r.k}` : undefined;
                  return (
                    <li className="fb-row fb-res-row" key={r.file.path}>
                      <div className="c c-name">
                        <span className={`fb-res-art${r.isDir ? " is-folder" : ""}`} aria-hidden="true">
                          {r.isDir
                            ? <svg viewBox="0 0 64 52" width="26" height="22"><path d="M4 6a4 4 0 0 1 4-4h16l6 6h26a4 4 0 0 1 4 4v34a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4z" /></svg>
                            : thumb
                              ? <img src={thumb} alt="" />
                              : <FileTypeIcon name={r.name} size={32} />}
                          {thumb && isVideo(r.file) && (
                            <span className="fb-res-play"><svg viewBox="0 0 24 24" width="9" height="9"><path d="M8 5v14l11-7z" fill="#fff" /></svg></span>
                          )}
                        </span>
                        <button
                          type="button"
                          className="fb-res-open"
                          onClick={() => openFound(r.file)}
                          disabled={openingPath !== null}
                          aria-label={`${r.name}, ${kindWord(r.file)} in ${r.dir}${r.file.outOfImages ? `, ${NoImg.stateInline}` : ""}`}
                          aria-describedby={[meta, metaDate].filter(Boolean).join(" ") || undefined}
                        >
                          <span className="fb-res-nameline">
                            <span className="fb-res-name">
                              {openingPath === r.file.path ? <span className="fb-opening">Opening…</span> : <Marked text={r.name} span={r.nameSpan} />}
                            </span>
                            {/* Issue #192: a folder kept out of Images, or
                                anything inside one. Only a marker here: the
                                switch is on the folder's row in its own
                                listing. Read out with the row's name above. */}
                            {r.file.outOfImages && (
                              <span className="fb-res-noimg" data-tip={NoImg.state}><NoImgIcon /></span>
                            )}
                          </span>
                          {/* Long folders lose their start, not the end nearest the file. */}
                          <span className="fb-res-dir"><bdi dir="ltr"><Marked text={r.dir} span={r.dirSpan} /></bdi></span>
                        </button>
                      </div>
                      <div className="c c-size" id={meta}>{r.isDir ? "—" : fmtBytes(r.size)}</div>
                      <div className="c c-modified" id={metaDate}>{r.file.modified ? r.file.modified.toLocaleString() : "—"}</div>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </section>
      )}

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

      <div ref={folderViewRef} className="fb-contents" hidden={inResults} tabIndex={-1} role="region" aria-label={`Contents of ${path}`}>
      {viewMode === "grid" ? (
        <div className="fb-grid" ref={gridRef}>
          {loading && <div className="fb-grid-note">Loading…</div>}
          {!loading && rows.length === 0 && <div className="fb-grid-note">This folder is empty.</div>}
          {!loading && rows.map(r => {
            const full = r.name === ".." ? "" : fullPathOf(r.file);
            const thumb = !r.isDir ? thumbs[full] : undefined;
            return (
              <div className={`fb-tile${sel[r.k] ? " selected" : ""}`} key={r.k}>
                <button className="fb-tile-art" onClick={() => openEntry(r.file)} disabled={openingPath !== null} title={r.name}
                  data-thumb={!r.isDir && isMedia(r.file) ? full : undefined}>
                  {r.isDir
                    ? <svg className="fb-folder" viewBox="0 0 64 52" aria-hidden="true"><path d="M4 6a4 4 0 0 1 4-4h16l6 6h26a4 4 0 0 1 4 4v34a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4z" /></svg>
                    : thumb
                      ? <img src={thumb} alt="" loading="lazy" />
                      : <FileTypeIcon name={r.name} size={72} />}
                  {!r.isDir && thumb && isVideo(r.file) && (
                    <span className="fb-play" aria-label="Video"><svg viewBox="0 0 24 24" width="12" height="12"><path d="M8 5v14l11-7z" fill="#fff" /></svg></span>
                  )}
                  {openingPath === r.file.path && <span className="fb-tile-opening">Opening…</span>}
                </button>
                {r.name !== ".." && (
                  <input className="fb-tile-check" type="checkbox" checked={!!sel[r.k]} onChange={() => toggleOne(r.file)} aria-label={`Select ${r.name}`} />
                )}
                {/* Issue #192: left of the lock, which keeps its corner. */}
                {r.name !== ".." && r.isDir && noImgShown && noImgButton(r, true)}
                {r.name !== ".." && r.isDir && (
                  <button className={`fb-lock fb-tile-lock${r.uploadOnly ? " on" : ""}`} onClick={() => void toggleUploadOnly(r.file)}
                    data-tip={r.uploadOnly ? "Upload only. Click to clear." : "Make upload only"}
                    aria-label={r.uploadOnly ? "Clear upload only" : "Make upload only"} aria-pressed={r.uploadOnly}>
                    {lockIcon(r.uploadOnly)}
                  </button>
                )}
                {!r.isDir && r.versions > 0 && (
                  <button className="fb-tile-versions" onClick={() => void openVersions(r.file)} title="Older versions of this file">{r.versions}</button>
                )}
                <div className="fb-tile-name" title={r.name}>{r.name}</div>
              </div>
            );
          })}
        </div>
      ) : (
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
                {/* Issue #132: the lock on a folder flags it upload only;
                    on a file it just says the file is inside one. */}
                {r.name !== ".." && r.isDir && (
                  <button
                    className={`fb-lock${r.uploadOnly ? " on" : ""}`}
                    onClick={() => void toggleUploadOnly(r.file)}
                    data-tip={r.uploadOnly ? "Upload only: nothing in this folder can be deleted, and re-uploads keep the old version. Click to clear." : "Make upload only: nothing in this folder can be deleted, and re-uploads keep the old version"}
                    aria-label={r.uploadOnly ? "Clear upload only" : "Make upload only"}
                    aria-pressed={r.uploadOnly}
                  >
                    {lockIcon(r.uploadOnly)}
                  </button>
                )}
                {r.name !== ".." && r.isDir && noImgShown && noImgButton(r)}
                {!r.isDir && r.uploadOnly && (
                  <span className="fb-lock on static" data-tip="In an upload-only folder: cannot be deleted" role="img" aria-label="In an upload-only folder: cannot be deleted">{lockIcon(true)}</span>
                )}
                {!r.isDir && r.versions > 0 && (
                  <button className="fb-versions" onClick={() => void openVersions(r.file)} title="Older versions of this file">
                    <svg width="13" height="13" viewBox="0 0 24 24" aria-hidden="true">
                      <path d="M4 12a8 8 0 1 0 2.34-5.66" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" />
                      <path d="M4 4v5h5" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round" />
                      <path d="M12 8v4l3 2" stroke="currentColor" strokeWidth="2" fill="none" strokeLinecap="round" />
                    </svg>
                    {r.versions} {r.versions === 1 ? "version" : "versions"}
                  </button>
                )}
              </div>
              <div className="c c-size">{r.isDir ? "—" : fmtBytes(r.size)}</div>
              <div className="c c-created">{r.created ? r.created.toLocaleString() : "—"}</div>
              <div className="c c-modified">{r.modified ? r.modified.toLocaleString() : "—"}</div>
            </div>
          ))}
        </div>
      </div>

      )}

      <div className="fb-tip">Tip: Drag files here to upload to <code>{path}</code>.</div>
      </div>

      {mediaViewer && (
        <MediaViewer
          items={viewerItems}
          index={mediaViewer.index}
          onIndexChange={i => setMediaViewer(v => (v ? { ...v, index: i } : v))}
          onClose={closeViewer}
        />
      )}

      {/* Issue #132: the versions pop-up - every older version of the
          file with when it was replaced and its size; each downloads. */}
      {versionsOf && (
        <div className="fb-modal" onClick={() => setVersionsOf(null)}>
          <div className="fb-modal-body fb-versions-body" onClick={(e) => e.stopPropagation()}>
            <button className="fb-close" onClick={() => setVersionsOf(null)} aria-label="Close">✕</button>
            <h3 className="fb-versions-title">Versions of {versionsOf.name}</h3>
            <p className="fb-versions-hint">The file is in an upload-only folder, so each upload to this path kept the one before it.</p>
            {versionsLoading && <div className="fb-row">Loading…</div>}
            {!versionsLoading && (
              <ul className="fb-versions-list">
                <li className="fb-versions-item current">
                  <span className="fb-versions-when">Current</span>
                  <span className="fb-versions-size" />
                  <button className="btn" onClick={() => void downloadVersion(versionsOf.path, "", versionsOf.name, versionsOf.mime)}>Download</button>
                </li>
                {versionsOf.versions.map((v) => (
                  <li key={v.hash} className="fb-versions-item">
                    <span className="fb-versions-when">Replaced {v.modified ? v.modified.toLocaleString() : "—"}</span>
                    <span className="fb-versions-size">{fmtBytes(fileSize(v))}</span>
                    <button className="btn" onClick={() => void downloadVersion(versionsOf.path, v.hash, versionsOf.name, v.mime)}>Download</button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )}

      {viewer && (
        <div className="fb-modal" onClick={() => { URL.revokeObjectURL(viewer.url); setViewer(null); }}>
          <div className="fb-modal-body" onClick={(e)=>e.stopPropagation()}>
            <button className="fb-close" onClick={() => { URL.revokeObjectURL(viewer.url); setViewer(null); }}>✕</button>
            <img className="fb-full" src={viewer.url} alt={viewer.name} />
            <div className="fb-cap">{viewer.name}</div>
          </div>
        </div>
      )}
      {sharingFolder && <SharedGalleryShare source={{ directory: sharingFolder }} onClose={() => setSharingFolder(null)} />}
    </div>
  );
}

