// SPDX-License-Identifier: AGPL-3.0-or-later

// What Places' map holds (PlacesView.tsx, and only it: this loads with the
// map): the places the device answered (GetPhotoPlaces), kept per filter
// and tile, and the covers' thumbnails. Module state, so the map comes back
// from Images as it was, with nothing asked again; places.ts says when it
// all goes (dropPlaces, a new sign-in).

import { useWS } from "../net/useWS";
import type { File as MsgFile, PhotoPlace, PlaceArea, RespEnvelope } from "../proto/messages";
import { onDropPlaces, setPlacesOn } from "./places";
import { batches, tileKey, type Tile } from "./placesMath";

// ---- the places, per filter and tile --------------------------------------------

/** What the map shows: Images' search without its area. */
export type PlaceFilter = { tags: string[]; personIds: string[]; groupId: string };

export const filterKeyOf = (f: PlaceFilter) => `${f.tags.join("\n")}|${f.personIds.join(",")}|${f.groupId}`;

/** An answer is used for this long, then asked again when shown (the
 *  phones' 5 minutes); one given while the device is still reading where
 *  the photos were taken (PhotoPlaces.reading) only for cKeepReadingMs,
 *  so the map fills in as it reads. */
const cKeepMs = 5 * 60_000;
export const cKeepReadingMs = 15_000;

type TileEntry = { at: number; places: PhotoPlace[]; reading: boolean };
type Located = { at: number; total: number; area: PlaceArea | null; reading: boolean };

const tiles = new Map<string, TileEntry>();
const located = new Map<string, Located>();
// What is on its way, so a tile is never asked twice at once.
const asking = new Set<string>();
// Bumped by dropPlaces: an answer to a request sent before is not kept.
let generation = 0;
const placeListeners = new Set<() => void>();

/** Told when places arrive or are dropped. */
export function onPlaces(l: () => void): () => void {
  placeListeners.add(l);
  return () => { placeListeners.delete(l); };
}
const told = () => placeListeners.forEach((l) => l());

// The library changed: every place is asked again when next shown. Signed
// out, or another sign-in: the thumbnails go too.
onDropPlaces((signedOut) => {
  generation += 1;
  tiles.clear();
  located.clear();
  if (signedOut) forgetThumbs();
  told();
});

const fresh = (e: { at: number; reading: boolean }) => Date.now() - e.at < (e.reading ? cKeepReadingMs : cKeepMs);

/** A tile's places as the device answered (maybe none), or null when it
 *  wasn't asked, or too long ago. */
export function placesOf(filter: string, z: number, t: Tile): PhotoPlace[] | null {
  const e = tiles.get(tileKey(filter, z, t));
  return e && fresh(e) ? e.places : null;
}

/** The whole library's located count and area under a filter, and
 *  whether the device is still reading positions, if known. */
export function locatedOf(filter: string): Located | null {
  const e = located.get(filter);
  return e && fresh(e) ? e : null;
}

/** Asks the device for the tiles of `want` it doesn't have (and isn't
 *  being asked), in requests of at most 64 tiles. Resolves true when every
 *  request was answered; the places are kept as they come. */
export async function askTiles(filter: PlaceFilter, z: number, want: Tile[]): Promise<boolean> {
  const fk = filterKeyOf(filter);
  const missing = want.filter((t) => {
    const k = tileKey(fk, z, t);
    return !asking.has(k) && placesOf(fk, z, t) === null;
  });
  if (!missing.length) return true;
  const results = await Promise.all(batches(missing).map((b) => askBatch(filter, fk, z, b)));
  return results.every(Boolean);
}

/** The located count and area under a filter (for framing the map). */
export async function askLocated(filter: PlaceFilter): Promise<Located | null> {
  const fk = filterKeyOf(filter);
  const known = locatedOf(fk);
  if (known) return known;
  // The world's one tile at zoom 0 comes with it.
  return (await askBatch(filter, fk, 0, [{ x: 0, y: 0 }])) ? locatedOf(fk) : null;
}

async function askBatch(filter: PlaceFilter, fk: string, z: number, batch: Tile[]): Promise<boolean> {
  const gen = generation;
  const keys = batch.map((t) => tileKey(fk, z, t));
  keys.forEach((k) => asking.add(k));
  try {
    const resp: RespEnvelope = await useWS.request((e) => {
      e.payload = {
        $case: "reqGetPhotoPlaces",
        reqGetPhotoPlaces: { zoom: z, tiles: batch, tags: filter.tags, personIds: filter.personIds, includeVideos: true, groupId: filter.groupId },
      };
    });
    // Turned off (on another app, or in Settings meanwhile): no map.
    if (resp.errorCode === "places_off") {
      setPlacesOn(false);
      return true;
    }
    if (resp.payload?.$case !== "respPhotoPlaces") return false;
    if (gen !== generation) return true;
    const answer = resp.payload.respPhotoPlaces;
    const now = Date.now();
    const reading = !!answer.reading;
    const byTile = new Map<string, PhotoPlace[]>(keys.map((k) => [k, []]));
    for (const p of answer.places) byTile.get(tileKey(fk, z, { x: p.x, y: p.y }))?.push(p);
    for (const [k, places] of byTile) tiles.set(k, { at: now, places, reading });
    located.set(fk, { at: now, total: answer.locatedTotal, area: answer.locatedTotal > 0 ? answer.locatedArea ?? null : null, reading });
    told();
    return true;
  } catch {
    return false;
  } finally {
    keys.forEach((k) => asking.delete(k));
  }
}

// ---- the covers' thumbnails --------------------------------------------------

/** The grids' batch of thumbnails (GetThumbnails takes 48). */
const cThumbBatch = 24;
/** Thumbnails kept, by path; past it the oldest not on screen go. */
const cThumbKeep = 600;

// A path's object URL, or "" when the device has no thumbnail for it.
const thumbs = new Map<string, string>();
const thumbAsked = new Set<string>();
let thumbQueue: string[] = [];
let pumping = false;
const thumbListeners = new Set<() => void>();

/** Told when thumbnails arrive. */
export function onThumbs(l: () => void): () => void {
  thumbListeners.add(l);
  return () => { thumbListeners.delete(l); };
}

/** A cover's thumbnail URL, "" for none, undefined while unknown. */
export const thumbOf = (path: string): string | undefined => thumbs.get(path);

/** The thumbnails the pins on screen need, nearest the middle first: they
 *  replace what was queued and not yet asked for (pins gone off screen). */
export function wantThumbs(paths: string[]) {
  thumbQueue = paths.filter((p) => !thumbs.has(p) && !thumbAsked.has(p));
  void pumpThumbs(new Set(paths));
}

async function pumpThumbs(onScreen: Set<string>) {
  if (pumping) return;
  pumping = true;
  try {
    while (thumbQueue.length) {
      const batch = thumbQueue.splice(0, cThumbBatch);
      batch.forEach((p) => thumbAsked.add(p));
      try {
        const resp: RespEnvelope = await useWS.request((e) => {
          e.payload = { $case: "reqGetThumbnails", reqGetThumbnails: { paths: batch, smallThumbnails: true } };
        });
        if (resp.payload?.$case !== "respListOfFiles") continue;
        const lof = resp.payload.respListOfFiles;
        const got = new Set<string>();
        for (const f of lof.files as MsgFile[]) {
          if (!f.content?.length) continue;
          thumbs.set(f.path, URL.createObjectURL(new Blob([f.content as BlobPart], { type: "image/jpeg" })));
          got.add(f.path);
        }
        // Cut short (the answer's size cap): the rest is asked again.
        const cut = lof.askAgainFrom > 0 ? lof.askAgainFrom : batch.length;
        batch.forEach((p, i) => {
          if (got.has(p)) return;
          if (i < cut) thumbs.set(p, "");
          else { thumbAsked.delete(p); thumbQueue.push(p); }
        });
        trimThumbs(onScreen);
        thumbListeners.forEach((l) => l());
      } catch {
        // Asked again when the pins next want them.
      } finally {
        batch.forEach((p) => thumbAsked.delete(p));
      }
    }
  } finally {
    pumping = false;
  }
}

function forgetThumbs() {
  for (const url of thumbs.values()) if (url) URL.revokeObjectURL(url);
  thumbs.clear();
  thumbQueue = [];
}

function trimThumbs(keep: Set<string>) {
  if (thumbs.size <= cThumbKeep) return;
  for (const [p, url] of thumbs) {
    if (thumbs.size <= cThumbKeep) break;
    if (keep.has(p)) continue;
    if (url) URL.revokeObjectURL(url);
    thumbs.delete(p);
  }
}
