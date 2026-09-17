// SPDX-License-Identifier: AGPL-3.0-or-later

import type { TabKey } from "../components/TopTabs";

// Issue #53: a reload used to always drop the user back on the default tab
// (Profile, or Social right after sign-in) regardless of what they were
// actually looking at — this persists the current tab so a reload restores
// the exact view instead. Session-local to this browser (localStorage),
// same mechanism issue #46 already uses to persist the login itself.
const cTabStorageKey = "otc_last_tab";

const cValidTabs: readonly TabKey[] = ["Profile", "Social", "SignIn", "AdminPannel", "PhotoGallery", "Settings", "Notifications", "Friends"];

export function saveLastTab(tab: TabKey) {
  try {
    localStorage.setItem(cTabStorageKey, tab);
  } catch {
    // Storage can be unavailable (private browsing, quota) — reload just
    // won't restore the view in that case, not worth surfacing an error.
  }
}

export function loadLastTab(): TabKey | null {
  try {
    const stored = localStorage.getItem(cTabStorageKey);
    return (cValidTabs as string[]).includes(stored ?? "") ? (stored as TabKey) : null;
  } catch {
    return null;
  }
}

// Issue #53 follow-up: restoring the right *tab* isn't enough on its own —
// a Files or Photos tab that comes back to its default directory/search on
// every reload still doesn't feel like "the exact view you were in".

const cFilesPathStorageKey = "otc_files_path";

export function saveFilesPath(path: string) {
  try {
    localStorage.setItem(cFilesPathStorageKey, path);
  } catch {
    // See saveLastTab's catch — not worth surfacing.
  }
}

export function loadFilesPath(): string | null {
  try {
    return localStorage.getItem(cFilesPathStorageKey);
  } catch {
    return null;
  }
}

const cPhotoSearchStorageKey = "otc_photo_search_tags";

export function savePhotoSearchTags(tags: string[]) {
  try {
    localStorage.setItem(cPhotoSearchStorageKey, JSON.stringify(tags));
  } catch {
    // See saveLastTab's catch — not worth surfacing.
  }
}

export function loadPhotoSearchTags(): string[] {
  try {
    const stored = localStorage.getItem(cPhotoSearchStorageKey);
    if (!stored) return [];
    const parsed = JSON.parse(stored);
    return Array.isArray(parsed) && parsed.every(t => typeof t === "string") ? parsed : [];
  } catch {
    return [];
  }
}
