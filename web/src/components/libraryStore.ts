// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useSyncExternalStore } from "react";
import { useWS } from "../net/useWS";
import type { ImageGroup, Person, RespEnvelope, TagsList } from "../proto/messages";
import { forgetPerson, getPhotoFilter } from "./photoFilter";

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

/** Lists again; join() waits on the listing already out, or starts one. */
export type Reload = (() => Promise<void>) & { join: () => Promise<void> };

// One request at a time per list; a reload asked for meanwhile waits for
// it and then runs, so it sees what the first one may have missed - also
// when the first one failed, and then only the last one's outcome counts.
function serial(run: () => Promise<void>): Reload {
  let current: Promise<void> | null = null;
  let again = false;
  const go = (): Promise<void> => {
    if (current) { again = true; return current; }
    current = (async () => {
      try {
        for (;;) {
          again = false;
          try {
            await run();
          } catch (e) {
            if (!again) throw e;
          }
          if (!again) return;
        }
      } finally { current = null; }
    })();
    return current;
  };
  return Object.assign(go, { join: () => current ?? go() });
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
    // Covers are cards and list icons: the small thumbnails (release 111).
    (e as any).payload = { $case: "reqListImageGroups", reqListImageGroups: { smallThumbnails: true } };
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

/** Issue #192: a folder kept out of Images, or shown there again, changes
 *  what these lists hold - its photos' tags go (or come back as they are
 *  worked out again), an unnamed person left with no face goes, and the
 *  collections' counts and covers change - so all three are listed again
 *  (Images itself searches afresh each time it opens). A person the photo
 *  filter still names who has gone stops being a filter, as after
 *  deleting them in People. */
export function reloadAfterImagesChanged() {
  void reloadTags().catch(() => {});
  void reloadGroups().catch(() => {});
  void reloadPeople().then(() => {
    if (!people.loaded) return;
    const ids = new Set(people.items.map((p) => p.id));
    for (const id of getPhotoFilter().personIds) if (!ids.has(id)) forgetPerson(id);
  }, () => {});
}

const getPeople = () => people;
const getGroups = () => groups;
const getTags = () => tags;

const cRetryFirstMs = 1000;
const cRetryMaxMs = 10_000;

// A hook's effect for one list: it lists at once when nothing is loaded,
// and a list that has never loaded (a socket dropped right after sign-in,
// an error reply) is asked for again while anything still shows it - 1 s,
// doubling to 10 s, as usePageRetry does for the grids. The top bar's
// search shows people and tags for the whole session, so without this one
// failure left it empty until the next refresh.
function watch(reload: Reload, loaded: () => boolean) {
  let showing = 0;
  let fails = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const settle = () => {
    if (loaded()) { fails = 0; return; }
    if (showing === 0 || timer !== undefined) return;
    timer = setTimeout(() => { timer = undefined; load(); }, Math.min(cRetryFirstMs * 2 ** fails++, cRetryMaxMs));
  };
  // A listing already out answers for this one too.
  const load = () => { if (!loaded()) reload.join().then(settle, settle); };
  return () => {
    showing++;
    load();
    return () => {
      if (--showing > 0 || timer === undefined) return;
      clearTimeout(timer);
      timer = undefined;
      fails = 0;
    };
  };
}

const watchPeople = watch(reloadPeople, () => people.loaded);
const watchGroups = watch(reloadGroups, () => groups.loaded);
const watchTags = watch(reloadTags, () => tagsLoaded);

export function usePeople() {
  const v = useSyncExternalStore(subscribe, getPeople);
  useEffect(() => watchPeople(), []);
  return v;
}

export function useGroups() {
  const v = useSyncExternalStore(subscribe, getGroups);
  useEffect(() => watchGroups(), []);
  return v;
}

export function useTags() {
  const v = useSyncExternalStore(subscribe, getTags);
  useEffect(() => watchTags(), []);
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
