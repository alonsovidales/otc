// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { ImageGroup, Person, RespEnvelope, TagsList } from "../proto/messages";

// The library's people, groups and tags, each loaded once and shared by
// the top bar's search, the People and Groups pages and the Images grid.
// Cover thumbnails become object URLs here, when a list arrives - never
// during a render (CLAUDE.md, web invariants) - and the previous list's
// are revoked when it is replaced.

type Listed<T> = {
  items: T[];
  // id -> blob: URL of the cover thumbnail; missing when there is none.
  thumbs: Map<string, string>;
  loaded: boolean;
};

const empty = <T,>(): Listed<T> => ({ items: [], thumbs: new Map(), loaded: false });

let people: Listed<Person> = empty<Person>();
let groups: Listed<ImageGroup> = empty<ImageGroup>();
let tags: string[] = [];
let tagsLoaded = false;

const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

function coverURLs<T extends { id: string; coverThumbnail?: Uint8Array }>(items: T[]) {
  const thumbs = new Map<string, string>();
  for (const it of items) {
    const bytes = it.coverThumbnail;
    if (bytes && bytes.byteLength > 0) {
      thumbs.set(it.id, URL.createObjectURL(new Blob([bytes as BlobPart], { type: "image/jpeg" })));
    }
  }
  return thumbs;
}

function revoke(thumbs: Map<string, string>) {
  thumbs.forEach((u) => URL.revokeObjectURL(u));
}

// One request at a time per list; a reload asked for meanwhile waits for
// it and then runs, so it sees what the first one may have missed.
function serial(run: () => Promise<void>) {
  let current: Promise<void> | null = null;
  let again = false;
  const go = (): Promise<void> => {
    if (current) { again = true; return current; }
    current = (async () => {
      try {
        do { again = false; await run(); } while (again);
      } finally { current = null; }
    })();
    return current;
  };
  return go;
}

export const reloadPeople = serial(async () => {
  const resp: RespEnvelope = await useWS.request((e) => {
    (e as any).payload = { $case: "reqListPeople", reqListPeople: {} };
  });
  if (resp.payload?.$case !== "respPeople") return;
  const items = resp.payload.respPeople.people ?? [];
  const old = people.thumbs;
  people = { items, thumbs: coverURLs(items), loaded: true };
  emit();
  revoke(old);
});

export const reloadGroups = serial(async () => {
  const resp: RespEnvelope = await useWS.request((e) => {
    (e as any).payload = { $case: "reqListImageGroups", reqListImageGroups: {} };
  });
  if (resp.payload?.$case !== "respImageGroups") return;
  // A group's cover is picked at random per listing, so every URL is new.
  const items = resp.payload.respImageGroups.groups ?? [];
  const old = groups.thumbs;
  groups = { items, thumbs: coverURLs(items), loaded: true };
  emit();
  revoke(old);
});

export const reloadTags = serial(async () => {
  const resp: RespEnvelope = await useWS.request((e) => {
    (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
  });
  if (resp.payload?.$case !== "respTagsList") return;
  tags = (resp.payload.respTagsList as TagsList).tags ?? [];
  tagsLoaded = true;
  emit();
});

const getPeople = () => people;
const getGroups = () => groups;
const getTags = () => tags;

// Each hook loads its list the first time anything shows it.
export function usePeople() {
  const v = useSyncExternalStore(subscribe, getPeople);
  useEffect(() => { if (!people.loaded) void reloadPeople().catch(() => {}); }, []);
  return v;
}

export function useGroups() {
  const v = useSyncExternalStore(subscribe, getGroups);
  useEffect(() => { if (!groups.loaded) void reloadGroups().catch(() => {}); }, []);
  return v;
}

export function useTags() {
  const v = useSyncExternalStore(subscribe, getTags);
  useEffect(() => { if (!tagsLoaded) void reloadTags().catch(() => {}); }, []);
  return v;
}

/** After a rename the device acknowledged: no need to list everyone again. */
export function renamePersonLocally(id: string, name: string) {
  people = { ...people, items: people.items.map((p) => (p.id === id ? { ...p, name } : p)) };
  emit();
}

export function renameGroupLocally(id: string, name: string) {
  groups = { ...groups, items: groups.items.map((g) => (g.id === id ? { ...g, name } : g)) };
  emit();
}

export function dropGroupLocally(id: string) {
  const thumb = groups.thumbs.get(id);
  const thumbs = new Map(groups.thumbs);
  thumbs.delete(id);
  groups = { ...groups, items: groups.items.filter((g) => g.id !== id), thumbs };
  emit();
  if (thumb) URL.revokeObjectURL(thumb);
}

/** A person's name as shown: their name, or "Unnamed". */
export const personLabel = (p: Pick<Person, "name">) => p.name.trim() || "Unnamed";
