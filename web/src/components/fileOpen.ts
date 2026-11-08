// SPDX-License-Identifier: AGPL-3.0-or-later
//
// How a file that isn't a photo or a video opens, in Files (its rows, its
// versions, its search's results) and in Settings › Largest files: read
// whole in 4 MB ReadFile pieces (mediaDownload.ts' readPieces, as the sync
// clients read it), then shown in a tab opened inside the click when the
// browser can show its type without running anything (a PDF, a text -
// inlineTypes.ts; the device's own reading of the content decides, so a
// ".txt" that is really HTML is saved), else saved under its name. The
// file is held whole in memory: past bigFile.ts' limit the caller asks
// first (BigFileDialog).

import { canOpenInline, safeToOpen } from "./inlineTypes";
import { readPieces } from "./mediaDownload";

// How long a download's blob: URL is kept after its click: the browser may
// start reading it later than the click (Safari), and one revoked at once
// saved nothing (mediaDownload.ts keeps them as long).
const cBlobKeepMs = 40_000;

/** Saves a blob: URL under a name, and lets it go a while later. */
export function saveURL(url: string, name: string) {
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), cBlobKeepMs);
}

/** Saves bytes under a name - as application/octet-stream: saved, never
 *  rendered, whatever the file is. */
export function downloadBytes(parts: Uint8Array[], name: string) {
  saveURL(blobURL(parts, "application/octet-stream"), name);
}

export function blobURL(parts: Uint8Array[], type: string) {
  return URL.createObjectURL(new Blob(parts as BlobPart[], { type }));
}

/** Points a tab opened for the click at the file. The tab keeps using the
 *  URL after it loads (a reload, the PDF viewer's own Save), so it is
 *  released once the tab is closed rather than after a set time. */
export function showInTab(tab: Window, parts: Uint8Array[], mime: string) {
  const url = blobURL(parts, mime);
  tab.location.href = url;
  const t = window.setInterval(() => {
    if (tab.closed) { URL.revokeObjectURL(url); window.clearInterval(t); }
  }, 5000);
}

/** Opens the document at `path` (`name`: its last part) as a click on it
 *  in Files does - called inside the click, since its tab opens before
 *  anything is read (opened later, a browser blocks it as a popup). null
 *  once it is shown or saved, else why not: the device's words, "" when it
 *  gave none (no answer). */
export function openDocument(path: string, name: string): Promise<{ failed: string } | null> {
  const tab = canOpenInline(name) ? window.open("", "_blank") : null;
  // No handle back into the app for whatever the file links to (a link in
  // a PDF could otherwise point this tab at a fake sign-in page). Not
  // "noopener": that returns null, and the tab is still needed here.
  if (tab) tab.opener = null;
  return (async () => {
    try {
      const got = await readPieces(path);
      if ("failed" in got) {
        tab?.close();
        return { failed: got.failed };
      }
      if (tab && safeToOpen(got.mime)) showInTab(tab, got.parts, got.mime);
      else {
        // Anything else - and a blocked popup - is saved.
        tab?.close();
        downloadBytes(got.parts, name);
      }
      return null;
    } catch {
      tab?.close();
      return { failed: "" };
    }
  })();
}
