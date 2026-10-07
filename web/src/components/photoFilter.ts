// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSyncExternalStore } from "react";
import { loadPhotoSearchTags, savePhotoSearchTags } from "../net/uiState";

// What the Images grid shows: the whole library, or the photos with every
// tag and every person here (AND, see dao.SearchMedia), inside one
// collection when one is open. The top bar's search edits it; the People
// and Collections pages open Images on a person or a collection through
// it. Tags are kept across a reload (issue #53); people and the open
// collection are not, as before. A collection is an image group in the
// protocol (ImageGroup, groupId), so the code here says group.

export type OpenGroup = { id: string; name: string; fileCount: number };

export type PhotoFilter = {
  tags: string[];
  personIds: string[];
  group: OpenGroup | null;
};

let state: PhotoFilter = { tags: loadPhotoSearchTags(), personIds: [], group: null };
const listeners = new Set<() => void>();

// Arrays are replaced, never changed in place: the grid restarts its
// search when `tags` or `personIds` is a new array, and only then.
function set(next: PhotoFilter) {
  if (next.tags !== state.tags) savePhotoSearchTags(next.tags);
  state = next;
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

export const getPhotoFilter = () => state;
export const usePhotoFilter = () => useSyncExternalStore(subscribe, getPhotoFilter);

/** Anything narrowing the grid: a tag, a person or a group. */
export const isFiltered = (f: PhotoFilter) => f.tags.length > 0 || f.personIds.length > 0 || f.group != null;
/** A tag search is sorted by how well photos match, not by date: no date
 *  headers and no date scrubber then. */
export const isDateOrdered = (f: PhotoFilter) => f.tags.length === 0;

export function addTag(tag: string) {
  const t = tag.trim();
  if (!t || state.tags.includes(t)) return;
  set({ ...state, tags: [...state.tags, t] });
}

export function removeTag(tag: string) {
  if (!state.tags.includes(tag)) return;
  set({ ...state, tags: state.tags.filter((x) => x !== tag) });
}

export function togglePerson(id: string) {
  const personIds = state.personIds.includes(id)
    ? state.personIds.filter((x) => x !== id)
    : [...state.personIds, id];
  set({ ...state, personIds });
}

/** A person deleted, or merged into another: no longer a filter. */
export function forgetPerson(id: string) {
  if (!state.personIds.includes(id)) return;
  set({ ...state, personIds: state.personIds.filter((x) => x !== id) });
}

/** Face recognition turned off (faceRecognition.ts): nobody to search for. */
export function clearPeople() {
  if (!state.personIds.length) return;
  set({ ...state, personIds: [] });
}

/** One person's photos, from the People page: a new search. */
export function showPerson(id: string) {
  set({ tags: [], personIds: [id], group: null });
}

/** A collection's photos, from the Collections page: a new search. */
export function openGroup(g: OpenGroup) {
  set({ tags: [], personIds: [], group: g });
}

/** The open group renamed or grown: same search, fresh name and count. */
export function updateOpenGroup(g: OpenGroup) {
  if (state.group?.id !== g.id) return;
  set({ ...state, group: g });
}

export function leaveGroup() {
  if (!state.group) return;
  set({ ...state, group: null });
}

/** The search box's clear button: tags and people go, an open group stays. */
export function clearSearch() {
  if (!state.tags.length && !state.personIds.length) return;
  set({ ...state, tags: [], personIds: [] });
}

/** Images in the menu: the whole library. */
export function showAll() {
  if (!isFiltered(state)) return;
  set({ tags: [], personIds: [], group: null });
}
