// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSyncExternalStore } from "react";
import type { File as PbFile } from "../proto/messages";
import { saveFilesPath } from "../net/uiState";

// Files sent somewhere from outside it - the top bar's search: a folder
// to show and, maybe, a file in it to open as a click there would. The
// folder is saved as Files' remembered path, so a Files that mounts for
// it starts there; FilesExplorer takes the request (takeFilesRequest)
// whether it was open already or opens now, and opens the file.

export type FilesRequest = { dir: string; file?: PbFile };

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

/** The request not yet taken, if any. */
export const useFilesRequest = () => useSyncExternalStore(subscribe, get);

/** Files has it: it isn't done again when Files shows next time. */
export function takeFilesRequest(r: FilesRequest) {
  if (pending !== r) return;
  pending = null;
  emit();
}

/** The folder a path is in, as Files writes folders: "/a/b/". */
export function parentFolder(path: string) {
  const clean = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  const at = clean.lastIndexOf("/");
  return at <= 0 ? "/" : clean.slice(0, at + 1);
}

/** A folder's own path as Files writes it: "/a/b/". */
export const asFolder = (path: string) => (path.endsWith("/") ? path : `${path}/`);
