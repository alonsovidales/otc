// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSyncExternalStore } from "react";
import type { File as PbFile } from "../proto/messages";
import { saveFilesPath } from "../net/uiState";

// Files sent somewhere from outside it - the top bar's search: a folder
// to show and, maybe, a file in it to open as a click there would; or a
// text to search every file and folder for, shown as a list of results
// over the folder (which stays Files' remembered path). A folder is saved
// as Files' remembered path, so a Files that mounts for it starts there;
// FilesExplorer takes the request (takeFilesRequest) whether it was open
// already or opens now, and opens the file or runs the search.

export type FilesRequest = { dir: string; file?: PbFile } | { search: string } | { leave: true };

let pending: FilesRequest | null = null;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}
const get = () => pending;

/** A folder (a path ending in "/"), and a file in it to open. */
export function showInFiles(dir: string, file?: PbFile) {
  saveFilesPath(dir);
  pending = { dir, file };
  emit();
}

/** Every file and folder whose path holds the text, listed in Files. */
export function searchInFiles(text: string) {
  pending = { search: text.trim() };
  emit();
}

/** Files picked in the menu: whatever results show, back to the folder. */
export function leaveFilesSearch() {
  pending = { leave: true };
  emit();
}

/** The request not yet taken, if any. */
export const useFilesRequest = () => useSyncExternalStore(subscribe, get);

/** Files has it: it isn't done again when Files shows next time. */
export function takeFilesRequest(r: FilesRequest) {
  if (pending !== r) return;
  pending = null;
  emit();
}

// A device older than SearchFiles answers it "unknown_payload". Whichever
// asked first - the top bar's panel, or Files for "Search documents" -
// says so here, and the top bar offers neither files nor that row again
// while the page is open.
let noFileSearch = false;
const searchListeners = new Set<() => void>();
function subscribeSearch(l: () => void) {
  searchListeners.add(l);
  return () => { searchListeners.delete(l); };
}

/** The device answered SearchFiles "unknown_payload". */
export function deviceCantSearchFiles() {
  if (noFileSearch) return;
  noFileSearch = true;
  searchListeners.forEach((l) => l());
}

/** true once the device has said it can't search its files. */
export const useNoFileSearch = () => useSyncExternalStore(subscribeSearch, () => noFileSearch);

/** The folder a path is in, as Files writes folders: "/a/b/". */
export function parentFolder(path: string) {
  const clean = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  const at = clean.lastIndexOf("/");
  return at <= 0 ? "/" : clean.slice(0, at + 1);
}

/** A folder's own path as Files writes it: "/a/b/". */
export const asFolder = (path: string) => (path.endsWith("/") ? path : `${path}/`);

/** A found file's name, and the folder it is in as shown with it ("/a/b"). */
export function pathParts(path: string) {
  const clean = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  const name = clean.slice(clean.lastIndexOf("/") + 1) || clean;
  const dir = parentFolder(clean);
  return { name, dir: dir.length > 1 ? dir.slice(0, -1) : dir };
}

// ---- Finding the typed text in a label, to bold it ----

/** [start, end) of the part of a label that matches what was typed. */
export type Span = [number, number];

// Text compared without case or accents, so "jose" finds "José". `from`
// maps each of its characters back to the original, to bold the match.
// Each character goes to upper case and back, as the device's searchFold
// does: "ς" and "Σ" both become "σ", so "ΟΔΟΣ" finds "Οδός".
export type Folded = { text: string; from: number[] };

export function fold(s: string): Folded {
  let text = "";
  const from: number[] = [];
  let at = 0;
  for (const ch of s) {
    const f = ch.normalize("NFD").replace(/\p{M}/gu, "").toUpperCase().toLowerCase();
    for (let k = 0; k < f.length; k++) from.push(at);
    text += f;
    at += ch.length;
  }
  return { text, from };
}

/** Where the folded query best matches a label: rank 0 at its start, 1 at
 * the start of a word in it, 2 inside a word; with the part of the label
 * it covers. null when it isn't there. */
export function matchIn(label: string, f: Folded, q: string): { rank: number; span: Span } | null {
  const { text, from } = f;
  if (!q) return null;
  let rank = -1;
  let at = -1;
  for (let j = text.indexOf(q); j !== -1; j = text.indexOf(q, j + 1)) {
    const r = j === 0 ? 0 : /[\p{L}\p{N}]/u.test(text[j - 1]) ? 2 : 1;
    if (rank < 0 || r < rank) { rank = r; at = j; }
    if (r < 2) break;
  }
  if (rank < 0) return null;
  const last = from[at + q.length - 1];
  const end = last + ((label.codePointAt(last) ?? 0) > 0xffff ? 2 : 1);
  return { rank, span: [from[at], end] };
}

/** A found path's name and folder, with the part the text matched: in the
 * name when it is there, else in the folder. */
export function foundParts(path: string, q: string) {
  const { name, dir } = pathParts(path);
  const nameSpan = matchIn(name, fold(name), q)?.span;
  const dirSpan = nameSpan ? undefined : matchIn(dir, fold(dir), q)?.span;
  return { name, dir, nameSpan, dirSpan };
}
