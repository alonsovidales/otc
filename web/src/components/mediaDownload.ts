// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Download: saving a photo's or a video's original file, for every place
// that offers it - the library viewer (MediaViewer: Images, Files) and the
// Social feed (its pop-up and each post). The button and the failure note
// are DownloadButton.tsx.
//
// What is saved is the original - never the thumbnail on screen nor the
// JPEG a HEIC is shown as - under the name the caller gives.
//
// A video the device streams (ReqGetMediaURL answers a URL: the big ones)
// is saved by the browser straight from that URL, so it goes to disk as it
// arrives and never sits in this page's memory. The URL is on the page's
// own origin (the device at home, its subdomain through the bridge), which
// is what makes the link's download attribute count; ?download=1 has the
// device also answer it as an attachment (named after the file, when it
// has a name: a post's media has none, and the attribute names it) -
// devices before that, and the bridge, ignore it, and the attribute does
// it alone. A new link per click: the one the player got may be near its
// hour.
//
// Everything else - photos, and clips too short for the device to stream -
// is read by the caller (a library file in 4 MB ReadFile pieces, a post's
// media with GetPublicationMedia) and saved from a blob typed
// application/octet-stream: saved, never rendered (Files' security
// advisory: no HTML or SVG shown in the app's origin).
import { useEffect, useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { MediaRef } from "../net/media";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";

// How long the button stays busy once the browser has the download, so a
// double click doesn't start a second one.
const cSaveHoldMs = 2000;
// How long a blob handed to the browser is kept (FileSaver.js's figure):
// Safari on iOS reads it only once its sheet is answered.
const cBlobKeepMs = 40000;
const cReadPiece = 4 << 20;
// How long a failed download's note stays up.
const cSaveErrorMs = 8000;

/** How far one download has got: asking for it, reading its bytes (total
 *  0 while unknown), or handed to the browser. */
export type Saving = { phase: "start" } | { phase: "read"; done: number; total: number } | { phase: "handed" };

/** The note of the last download that failed. */
export type FailedDownload = { key: string; name: string; seq: number; page: boolean };

/** One download: what to save and how to get its bytes. */
export type DownloadJob = {
  /** One download at a time per key: a library path, a post's media (pubMediaKey). */
  key: string;
  /** The name it is saved under. */
  name: string;
  /** The device's media link to save from, for a video it may stream;
   *  null to always read the bytes. */
  stream: MediaRef | null;
  /** The original bytes, telling progress how far it got (total 0 while
   *  unknown). Null when the device stopped answering. */
  read: (progress: (done: number, total: number) => void) => Promise<BlobPart[] | null>;
  /** The name has no extension (its type said nothing): the one its
   *  bytes show is added once they are read. */
  extFromBytes?: boolean;
};

export const leafName = (path: string) => path.split("/").pop() || "download";

/** The store key of a post's media. */
export const pubMediaKey = (pubUuid: string, hash: string) => `pub:${pubUuid}/${hash}`;

/** The button's words for a download's state. */
export function savingLabel(s: Saving | undefined): string {
  if (!s) return "Download";
  if (s.phase === "start") return "Preparing…";
  if (s.phase === "handed") return "Started";
  const pct = savingPercent(s);
  return pct == null ? "Downloading…" : `Downloading ${pct}%`;
}
/** How much of it is read, when that is known. */
export function savingPercent(s: Saving | undefined): number | null {
  return s?.phase === "read" && s.total > 0 ? Math.floor((s.done / s.total) * 100) : null;
}
export const failedText = (name: string) => `Couldn't download ${name}. Check the connection to your device and try again.`;

// The downloads under way, by key, and the note of the last one that
// failed. Here rather than in a viewer: closing one unmounts it while its
// downloads go on, so a viewer opened again on the same file shows how far
// its download got and refuses a second, and a failure is told wherever
// the user is - in a viewer if one is open, over the page if not (page:
// until a viewer opens and shows it). Every viewer, the Social pop-up
// included, is a host (useDownloadHost).
type Downloads = {
  saving: Readonly<Record<string, Saving>>;
  failed: FailedDownload | null;
};
let downloads: Downloads = { saving: {}, failed: null };
// How many hosts are on screen (none: a failure's note goes over the page),
// and the keys of the items they show, each with how many show it.
let hostsOpen = 0;
const hostsShowing = new Map<string, number>();
const dlListeners = new Set<() => void>();
const subscribeDownloads = (l: () => void) => {
  dlListeners.add(l);
  return () => { dlListeners.delete(l); };
};
const notify = () => dlListeners.forEach((l) => l());
function setDownloads(next: Downloads) {
  downloads = next;
  syncPageNote();
  notify();
}
function showSaving(key: string, s: Saving | null) {
  const saving = { ...downloads.saving };
  if (s) saving[key] = s; else delete saving[key];
  setDownloads({ ...downloads, saving });
}
let failSeq = 0;
function showFailed(job: { key: string; name: string } | null) {
  if (!job) { setDownloads({ ...downloads, failed: null }); return; }
  const seq = ++failSeq;
  setDownloads({ ...downloads, failed: { key: job.key, name: job.name, seq, page: hostsOpen === 0 } });
  setTimeout(() => { if (downloads.failed?.seq === seq) showFailed(null); }, cSaveErrorMs);
}
/** Takes the failure note down. */
export const dismissFailed = () => showFailed(null);

/** This key's download, if one is under way: re-renders only when that
 *  one changes (a feed has a button per post). */
export function useDownloadState(key: string): Saving | undefined {
  return useSyncExternalStore(subscribeDownloads, () => downloads.saving[key]);
}
/** The failure note, for a host to show. */
export function useDownloadFailure(): FailedDownload | null {
  return useSyncExternalStore(subscribeDownloads, () => downloads.failed);
}
/** Whether an open viewer shows this key's item while its download runs:
 *  the viewer's own button tells screen readers about it, so another one
 *  for the same download (a post's, behind the Social pop-up) keeps
 *  quiet. A viewer showing something else leaves it to speak. Changes
 *  only for the key's own downloads. */
export function useDownloadToldByHost(key: string): boolean {
  return useSyncExternalStore(subscribeDownloads, () => !!downloads.saving[key] && hostsShowing.has(key));
}
/** Marks a viewer that shows failure notes as on screen while open: a note
 *  over the page moves into it, and one that comes while it is open stays
 *  in it. shown: the key of the item it shows (useDownloadToldByHost). */
export function useDownloadHost(open = true, shown = "") {
  useEffect(() => {
    if (!open) return;
    hostsOpen += 1;
    if (downloads.failed?.page) setDownloads({ ...downloads, failed: { ...downloads.failed, page: false } });
    else { syncPageNote(); notify(); }
    return () => { hostsOpen -= 1; syncPageNote(); notify(); };
  }, [open]);
  useEffect(() => {
    if (!open || !shown) return;
    hostsShowing.set(shown, (hostsShowing.get(shown) ?? 0) + 1);
    notify();
    return () => {
      const n = (hostsShowing.get(shown) ?? 1) - 1;
      if (n > 0) hostsShowing.set(shown, n); else hostsShowing.delete(shown);
      notify();
    };
  }, [open, shown]);
}

// The note over the page, while no host is open to show it: the viewer's
// own note, outside React (no host is mounted then).
let pageNote: HTMLElement | null = null;
function syncPageNote() {
  const f = hostsOpen === 0 && downloads.failed?.page ? downloads.failed : null;
  if (pageNote && pageNote.dataset.seq === String(f?.seq)) return;
  pageNote?.remove();
  pageNote = null;
  if (!f) return;
  const note = document.createElement("div");
  note.className = "pg-modal-toast pg-modal-toast-page";
  note.setAttribute("role", "alert");
  note.dataset.seq = String(f.seq);
  const text = document.createElement("span");
  text.textContent = failedText(f.name);
  const close = document.createElement("button");
  close.type = "button";
  close.className = "pg-modal-toast-close";
  close.setAttribute("aria-label", "Dismiss");
  close.textContent = "×";
  close.addEventListener("click", dismissFailed);
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

/** A library file's original bytes in pieces of at most 4 MB, as Files'
 *  readAll reads them, telling progress how far it got. Each piece looks
 *  the path up again, so a file replaced while it is read starts over
 *  once, for the new content. Null when the device stopped answering. */
export async function readFileInPieces(path: string, progress: (done: number, total: number) => void): Promise<Uint8Array[] | null> {
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

/** Saves job's file. One at a time per key: the store says so before
 *  anything is awaited, so a double click, or a click in a viewer opened
 *  again while the first goes on, doesn't start a second. */
export async function startDownload(job: DownloadJob) {
  const { key, name } = job;
  if (downloads.saving[key]) return;
  if (downloads.failed?.key === key) showFailed(null);
  showSaving(key, { phase: "start" });
  let handed = false;
  try {
    let url = "";
    if (job.stream) {
      const ref = job.stream;
      const req = "path" in ref ? { path: ref.path, pubUuid: "", hash: "" } : { path: "", pubUuid: ref.pubUuid, hash: ref.hash };
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqGetMediaUrl", reqGetMediaUrl: req };
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
      const parts = await job.read((done, total) => showSaving(key, { phase: "read", done, total }));
      if (!parts) throw new Error("no answer");
      const bytes = new Blob(parts, { type: "application/octet-stream" });
      const ext = job.extFromBytes ? extensionForBytes(new Uint8Array(await bytes.slice(0, 16).arrayBuffer())) : "";
      const blob = URL.createObjectURL(bytes);
      saveAs(blob, name + ext);
      setTimeout(() => URL.revokeObjectURL(blob), cBlobKeepMs);
    }
    handed = true;
  } catch {
    // the note below
  }
  if (handed) {
    showSaving(key, { phase: "handed" });
    setTimeout(() => showSaving(key, null), cSaveHoldMs);
  } else {
    showSaving(key, null);
    showFailed(job);
  }
}

// File name extensions for the types photos and videos are kept in, for
// a file that has no name of its own (a post's media): those the device's
// type detection (Go's mimetype) gives whose subtype isn't the extension,
// and the usual other names for them. ASF, as the device calls a WMV, is
// saved as .wmv, the name people know.
const cExtByMime: Record<string, string> = {
  "image/jpeg": "jpg", "image/jpg": "jpg", "image/pjpeg": "jpg", "image/png": "png", "image/vnd.mozilla.apng": "png",
  "image/gif": "gif", "image/webp": "webp", "image/heic": "heic", "image/heif": "heif", "image/heic-sequence": "heic",
  "image/heif-sequence": "heif", "image/avif": "avif", "image/tiff": "tif", "image/bmp": "bmp", "image/x-ms-bmp": "bmp",
  "image/x-adobe-dng": "dng", "image/dng": "dng", "image/svg+xml": "svg", "image/jpx": "jpf", "image/jxr": "jxr",
  "image/vnd.ms-photo": "jxr", "image/x-icon": "ico", "image/vnd.adobe.photoshop": "psd", "image/vnd.radiance": "hdr",
  "video/mp4": "mp4", "video/quicktime": "mov", "video/webm": "webm", "video/x-matroska": "mkv",
  "video/3gpp": "3gp", "video/3gp": "3gp", "video/3gpp2": "3g2", "video/x-msvideo": "avi", "video/msvideo": "avi",
  "video/mpeg": "mpg", "video/x-m4v": "m4v", "video/mp2t": "ts", "video/ogg": "ogv", "video/x-flv": "flv",
  "video/x-ms-asf": "wmv", "video/asf": "wmv", "video/x-ms-wmv": "wmv", "video/vnd.dvb.file": "dvb",
};
/** ".jpg" for image/jpeg and so on; the subtype itself when it reads like
 *  an extension; nothing when there is no type, or it is one like
 *  application/octet-stream (extensionForBytes is for those). */
export function extensionForMime(mime?: string): string {
  const m = (mime || "").split(";")[0].trim().toLowerCase();
  if (!m) return "";
  const known = cExtByMime[m];
  if (known) return "." + known;
  const sub = m.split("/")[1]?.replace(/^x-/, "") ?? "";
  return /^[a-z0-9]{1,5}$/.test(sub) ? "." + sub : "";
}

const ascii = (b: Uint8Array, at: number, n: number) => String.fromCharCode(...b.subarray(at, at + n));
/** The extension a photo's or a video's first bytes show (".jpg", ".mov"
 *  and so on), nothing when they are none it knows. */
export function extensionForBytes(b: Uint8Array): string {
  if (b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return ".jpg";
  if (ascii(b, 0, 8) === "\x89PNG\r\n\x1a\n") return ".png";
  if (ascii(b, 0, 4) === "GIF8") return ".gif";
  if (ascii(b, 0, 4) === "RIFF") return ({ WEBP: ".webp", "AVI ": ".avi" } as Record<string, string>)[ascii(b, 8, 4)] ?? "";
  if (ascii(b, 0, 4) === "II*\0" || ascii(b, 0, 4) === "MM\0*") return ".tif";
  if (ascii(b, 4, 4) === "ftyp") {
    const brand = ascii(b, 8, 4);
    if (/^he[iv][cxms]$/.test(brand)) return ".heic";
    if (brand === "mif1" || brand === "msf1") return ".heif";
    if (brand === "avif" || brand === "avis") return ".avif";
    if (brand === "qt  ") return ".mov";
    if (brand.startsWith("3g2")) return ".3g2";
    if (brand.startsWith("3gp")) return ".3gp";
    return ".mp4";
  }
  if (b[0] === 0x1a && b[1] === 0x45 && b[2] === 0xdf && b[3] === 0xa3) return ".mkv";
  if (b[0] === 0x30 && b[1] === 0x26 && b[2] === 0xb2 && b[3] === 0x75) return ".wmv";
  if (ascii(b, 0, 3) === "FLV") return ".flv";
  if (ascii(b, 0, 4) === "OggS") return ".ogv";
  if (b[0] === 0 && b[1] === 0 && b[2] === 1 && (b[3] === 0xba || b[3] === 0xb3)) return ".mpg";
  return "";
}
