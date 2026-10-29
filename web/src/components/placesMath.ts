// SPDX-License-Identifier: AGPL-3.0-or-later

// Places' arithmetic (the map in Images, PlacesView.tsx): which tiles the
// device is asked for (GetPhotoPlaces, numbered as every web map numbers
// its tiles: PlaceTile in messages.proto), at which of its zooms, and how
// the places that come back become the pins on screen. No imports, so
// web/scripts/places-test.mjs tests it in Node. The phones follow the same
// rules: change them together.

/** The device's tiles are 256 px; MapLibre's zoom counts 512 px tiles. */
export const cZoomOffset = 1;
/** GetPhotoPlaces takes zoom 0 to 20. */
export const cMaxDeviceZoom = 20;
/** And at most 64 tiles per request. */
export const cMaxTilesPerAsk = 64;
/** Square Web Mercator's edge. */
export const cMaxLat = 85.0511287798;

export type Area = { south: number; west: number; north: number; east: number };
export type Tile = { x: number; y: number };

/** The device's zoom for a MapLibre zoom: its 64 px cells are then 64 to
 *  128 px on screen, never smaller than a pin. */
export function deviceZoom(mapZoom: number): number {
  return Math.max(0, Math.min(cMaxDeviceZoom, Math.floor(mapZoom + cZoomOffset)));
}

/** The tile column of a longitude at zoom z, unwrapped: a longitude past
 *  ±180 (a map shown across the antimeridian) gives a column outside 0 to
 *  2^z - 1, which the device takes modulo 2^z. */
export function tileX(lon: number, z: number): number {
  return Math.floor(((lon + 180) / 360) * 2 ** z);
}

/** The tile row of a latitude at zoom z, clamped to the map's edge. */
export function tileY(lat: number, z: number): number {
  const n = 2 ** z;
  const l = (Math.max(-cMaxLat, Math.min(cMaxLat, lat)) * Math.PI) / 180;
  const y = Math.floor(((1 - Math.asinh(Math.tan(l)) / Math.PI) / 2) * n);
  return Math.max(0, Math.min(n - 1, y));
}

/** x within 0 to 2^z - 1, as the device answers it. */
export const wrapX = (x: number, z: number) => ((x % 2 ** z) + 2 ** z) % 2 ** z;

/** The tiles covering a map's bounds (west and east as the map gives them,
 *  maybe beyond ±180) plus `margin` tiles around, each once: never more
 *  columns than the world has. */
export function tilesFor(b: Area, z: number, margin: number): Tile[] {
  const n = 2 ** z;
  let x0 = tileX(b.west, z) - margin;
  let x1 = tileX(b.east, z) + margin;
  if (x1 - x0 + 1 > n) {
    x0 = 0;
    x1 = n - 1;
  }
  const y0 = Math.max(0, tileY(b.north, z) - margin);
  const y1 = Math.min(n - 1, tileY(b.south, z) + margin);
  const seen = new Set<number>();
  const out: Tile[] = [];
  // Centre first: the batches the device answers first are what is on screen.
  const cx = (x0 + x1 + 1) / 2;
  const cy = (y0 + y1 + 1) / 2;
  for (let y = y0; y <= y1; y++) {
    for (let x = x0; x <= x1; x++) {
      const k = wrapX(x, z);
      if (seen.has(k * n + y)) continue;
      seen.add(k * n + y);
      out.push({ x: k, y });
    }
  }
  out.sort((a, b2) => dist(a, cx, cy, z) - dist(b2, cx, cy, z));
  return out;
}

function dist(t: Tile, cx: number, cy: number, z: number) {
  const n = 2 ** z;
  // Columns compare around the world: the nearest way.
  let dx = Math.abs(t.x + 0.5 - wrapX(cx, z));
  dx = Math.min(dx, n - dx);
  const dy = t.y + 0.5 - cy;
  return dx * dx + dy * dy;
}

/** The request batches for `tiles`: at most cMaxTilesPerAsk each. */
export function batches<T>(tiles: T[], size = cMaxTilesPerAsk): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < tiles.length; i += size) out.push(tiles.slice(i, i + size));
  return out;
}

/** A place's cache key under a filter: filter|zoom|x|y. */
export const tileKey = (filter: string, z: number, t: Tile) => `${filter}|${z}|${t.x}|${t.y}`;

/** The smallest area holding both (neither across the antimeridian). */
export function union(a: Area, b: Area): Area {
  return {
    south: Math.min(a.south, b.south),
    west: Math.min(a.west, b.west),
    north: Math.max(a.north, b.north),
    east: Math.max(a.east, b.east),
  };
}

/** About how many metres an area is across, corner to corner. */
export function areaSpanMetres(a: Area): number {
  const r = 6371000;
  const lat = ((a.south + a.north) / 2) * (Math.PI / 180);
  const dy = ((a.north - a.south) * Math.PI) / 180;
  const dx = (((a.east - a.west) * Math.PI) / 180) * Math.cos(lat);
  return r * Math.hypot(dx, dy);
}

/** A place as a pin needs it. */
export type PinPlace<C> = {
  latitude: number;
  longitude: number;
  count: number;
  area: Area;
  cover: C | undefined;
  /** When the cover was taken (its sort date), to keep the newest. */
  coverTime: number;
};

/** A pin: one place, or neighbours too close on screen to tell apart. */
export type Pin<C> = PinPlace<C> & { key: string; x: number; y: number };

/** Places whose pins would overlap on screen (closer than `gap` px of the
 *  biggest of them) become one pin: their photos add up, its area grows to
 *  hold them all and the newest cover stands for them. It goes where one
 *  of them is - the one nearest their mean, weighted by their photos - so
 *  a pin always stands where a photo was taken (the device places each
 *  place at a photo too), never between two cities. `at` gives a place's
 *  spot on screen. A pin's key is its biggest place's, so a pin keeps its
 *  element while what joins it changes. Places on the
 *  two sides of the antimeridian join the short way round: the pin's area
 *  then has its west greater than its east, as SearchPhotos.area takes one
 *  across it. */
export function mergePins<C>(
  places: (PinPlace<C> & { key: string })[],
  at: (p: PinPlace<C>) => { x: number; y: number },
  gap: number,
): Pin<C>[] {
  const order = [...places].sort((a, b) => b.count - a.count || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  const pins: Pin<C>[] = [];
  // Each pin's places, where they are: the biggest first.
  type Member = { x: number; y: number; latitude: number; longitude: number; count: number };
  const members = new Map<Pin<C>, Member[]>();
  const cell = gap;
  const grid = new Map<string, Pin<C>[]>();
  const near = (x: number, y: number) => {
    const gx = Math.floor(x / cell);
    const gy = Math.floor(y / cell);
    const out: Pin<C>[] = [];
    for (let dy = -1; dy <= 1; dy++) {
      for (let dx = -1; dx <= 1; dx++) {
        const g = grid.get(`${gx + dx},${gy + dy}`);
        if (g) out.push(...g);
      }
    }
    return out;
  };
  for (const p of order) {
    const { x, y } = at(p);
    const hit = near(x, y).find((q) => Math.hypot(q.x - x, q.y - y) < gap);
    if (hit) {
      members.get(hit)!.push({ x, y, latitude: p.latitude, longitude: p.longitude, count: p.count });
      hit.count += p.count;
      const shift = -360 * Math.round((p.longitude - hit.longitude) / 360);
      hit.area = union(hit.area, { ...p.area, west: p.area.west + shift, east: p.area.east + shift });
      if (p.coverTime > hit.coverTime && p.cover !== undefined) {
        hit.cover = p.cover;
        hit.coverTime = p.coverTime;
      }
      continue;
    }
    const pin: Pin<C> = { ...p, x, y };
    pins.push(pin);
    members.set(pin, [{ x, y, latitude: p.latitude, longitude: p.longitude, count: p.count }]);
    pin.area = { ...pin.area };
    const gk = `${Math.floor(x / cell)},${Math.floor(y / cell)}`;
    const g = grid.get(gk);
    if (g) g.push(pin);
    else grid.set(gk, [pin]);
  }
  for (const pin of pins) {
    pin.area = wrapArea(pin.area);
    const ms = members.get(pin)!;
    if (ms.length < 2) continue;
    const n = ms.reduce((t, m) => t + m.count, 0);
    const mx = ms.reduce((t, m) => t + m.x * m.count, 0) / n;
    const my = ms.reduce((t, m) => t + m.y * m.count, 0) / n;
    let best = ms[0];
    for (const m of ms) if (Math.hypot(m.x - mx, m.y - my) < Math.hypot(best.x - mx, best.y - my)) best = m;
    Object.assign(pin, { x: best.x, y: best.y, latitude: best.latitude, longitude: best.longitude });
  }
  return pins;
}

/** An area whose west or east went past ±180 (joined across the
 *  antimeridian) back within it: west greater than east then. */
export function wrapArea(a: Area): Area {
  if (a.east - a.west >= 360) return { ...a, west: -180, east: 180 };
  if (a.east > 180) return { ...a, east: a.east - 360 };
  if (a.west < -180) return { ...a, west: a.west + 360 };
  return a;
}

/** fitBounds' corners for an area: one across the antimeridian (west
 *  greater than east, as located_area may be) goes on past 180. */
export function fitCorners(a: Area): [[number, number], [number, number]] {
  const east = a.west > a.east ? a.east + 360 : a.east;
  return [[a.west, a.south], [east, a.north]];
}
