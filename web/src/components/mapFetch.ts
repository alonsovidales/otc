// SPDX-License-Identifier: AGPL-3.0-or-later

// What Places' map (PlacesView.tsx) fetches from its map service - the
// style, tiles, fonts and icons of OpenFreeMap - goes through a worker of
// its own made from a data: URL. Such a worker has an opaque origin, so its
// requests carry "Origin: null" and, sent with referrerPolicy no-referrer,
// no Referer: the map service learns which tiles are looked at, never the
// device's address (a page's own fetch to another site always names its
// origin). OpenFreeMap answers every origin (Access-Control-Allow-Origin
// *). A browser that can't start the worker fetches from the page, still
// without a Referer. Only the map service's host is ever fetched
// (index.html's connect-src allows only it besides the device).

export const cMapHost = "https://tiles.openfreemap.org";

type Answer = { id: number; status?: number; error?: string; buf?: ArrayBuffer; cacheControl?: string | null; expires?: string | null };
export type Fetched = { data: ArrayBuffer; cacheControl: string | null; expires: string | null };

const cWorkerSource = `
const live = new Map();
self.onmessage = async (e) => {
  const { id, url, abort } = e.data;
  if (abort) { live.get(id)?.abort(); return; }
  const ctl = new AbortController();
  live.set(id, ctl);
  try {
    const r = await fetch(url, { referrerPolicy: "no-referrer", credentials: "omit", signal: ctl.signal });
    if (!r.ok) { self.postMessage({ id, status: r.status }); return; }
    const buf = await r.arrayBuffer();
    self.postMessage({ id, buf, cacheControl: r.headers.get("cache-control"), expires: r.headers.get("expires") }, [buf]);
  } catch (err) {
    self.postMessage({ id, error: String(err) });
  } finally {
    live.delete(id);
  }
};`;

let worker: Worker | null | undefined;
let nextId = 1;
const waiting = new Map<number, { resolve: (f: Fetched) => void; reject: (e: Error) => void }>();

function proxy(): Worker | null {
  if (worker !== undefined) return worker;
  try {
    worker = new Worker("data:text/javascript;charset=utf-8," + encodeURIComponent(cWorkerSource));
    worker.onmessage = (e: MessageEvent<Answer>) => {
      const a = e.data;
      const w = waiting.get(a.id);
      if (!w) return;
      waiting.delete(a.id);
      if (a.buf) w.resolve({ data: a.buf, cacheControl: a.cacheControl ?? null, expires: a.expires ?? null });
      else w.reject(new Error(a.error ?? `HTTP ${a.status}`));
    };
    // A worker that can't run (refused by the browser after starting):
    // what waits fails, and the page fetches from now on.
    worker.onerror = () => {
      console.warn("The map's fetch worker didn't start: fetching from the page.");
      worker = null;
      for (const [, w] of waiting) w.reject(new Error("map fetch worker failed")); // i18n-ignore: an error MapLibre logs, never shown
      waiting.clear();
    };
  } catch (err) {
    console.warn("The map's fetch worker didn't start: fetching from the page.", err);
    worker = null;
  }
  return worker;
}

/** A map resource, by its full https URL on the map service's host. */
export function mapFetch(url: string, signal?: AbortSignal): Promise<Fetched> {
  if (!url.startsWith(cMapHost + "/")) return Promise.reject(new Error("not the map service")); // i18n-ignore: an error MapLibre logs, never shown
  const w = proxy();
  if (!w) {
    return fetch(url, { referrerPolicy: "no-referrer", credentials: "omit", signal }).then(async (r) => {
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      return { data: await r.arrayBuffer(), cacheControl: r.headers.get("cache-control"), expires: r.headers.get("expires") };
    });
  }
  const id = nextId++;
  return new Promise<Fetched>((resolve, reject) => {
    waiting.set(id, { resolve, reject });
    signal?.addEventListener("abort", () => {
      if (!waiting.delete(id)) return;
      w.postMessage({ id, abort: true });
      reject(new DOMException("aborted", "AbortError"));
    }, { once: true });
    w.postMessage({ id, url });
  });
}

/** A JSON document from the map service. */
export async function mapFetchJSON<T>(url: string): Promise<T> {
  const f = await mapFetch(url);
  return JSON.parse(new TextDecoder().decode(f.data)) as T;
}
