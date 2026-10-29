// SPDX-License-Identifier: AGPL-3.0-or-later

// Places: the photos on a map, where they were taken. The device groups
// them (GetPhotoPlaces, places.ts) for the tiles on screen at the zoom
// shown, and each group is a pin: its newest photo's thumbnail, with how
// many photos it holds. Zooming in splits the groups. A pin of one photo
// opens it in the viewer; a group flies to its photos while they are
// spread over more than a few metres, and otherwise opens them in Images
// (photoFilter.showArea, with a chip in the top bar's search). The map
// shows what Images would: the same tags, people and collection.
//
// The map is MapLibre with OpenFreeMap's dark style, coloured as the app,
// its labels in the app's language. Everything it fetches goes through
// mapFetch.ts, so the map service never learns the device's address.
// Only this part loads MapLibre (lazyPart, App.tsx), and what the map
// holds (placesData.ts). The arithmetic is placesMath.ts; the phones
// follow the same rules.

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AttributionControl, Map as MapLibre, Marker, addProtocol, setWorkerUrl } from "maplibre-gl";
import type { MapOptions } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?url";
import { fmtDate, fmtNumber, useT, type TFunction } from "../i18n";
import type { File as MsgFile, PhotoPlace } from "../proto/messages";
import { ErrorBoundary } from "./ErrorBoundary";
import { lazyPart } from "./lazyPart";
import Part from "./Part";
import Spinner from "./Spinner";
import type { ViewerItem } from "./MediaViewer";
import { cMapHost, mapFetch, mapFetchJSON } from "./mapFetch";
import { isFiltered, leaveArea, showArea, usePhotoFilter } from "./photoFilter";
import { keepCamera, lastCamera } from "./places";
import { askLocated, askTiles, cKeepReadingMs, filterKeyOf, locatedOf, onPlaces, onThumbs, placesOf, thumbOf, wantThumbs, type PlaceFilter } from "./placesData";
import { areaSpanMetres, deviceZoom, fitCorners, mergePins, tileX, tileY, tilesFor, wrapX, type Area, type Pin } from "./placesMath";
import "./PlacesView.css";

const MediaViewer = lazyPart(() => import("./MediaViewer"));

// MapLibre's worker, from the page's own address (Vite copies it to the
// build): index.html's worker-src 'self' holds.
setWorkerUrl(workerUrl);

// The map's resources by a scheme of its own, fetched by mapFetch.
const cScheme = "otcmap";
let protocolAdded = false;
function addMapProtocol() {
  if (protocolAdded) return;
  protocolAdded = true;
  addProtocol(cScheme, async (params, ctl) => {
    const url = `${cMapHost}/${params.url.slice(cScheme.length + 3)}`;
    const f = await mapFetch(url, ctl.signal);
    const expiry = { cacheControl: f.cacheControl, expires: f.expires };
    if (params.type === "json") return { data: JSON.parse(new TextDecoder().decode(f.data)), ...expiry };
    if (params.type === "string") return { data: new TextDecoder().decode(f.data), ...expiry };
    return { data: f.data, ...expiry };
  });
}
const viaScheme = (url: string) => (url.startsWith(cMapHost + "/") ? `${cScheme}://${url.slice(cMapHost.length + 1)}` : url);

// Pins this close on screen (px) are one: a pin is 48 px. The phones' 54 pt.
const cPinGap = 54;
// Below this zoom (MapLibre's) a group of photos spread over more than
// cSpreadMetres flies in; past it, or closer together, it opens in Images
// (the phones' rule).
const cFlyBelowZoom = 16;
const cSpreadMetres = 25;
// The view of one spot, or of photos close together: a city.
const cFrameMaxZoom = 11;
const cMaxZoom = 18;
// Camera changes come to rest this long before the device is asked.
const cSettleMs = 200;
// And this long more before the next zoom is fetched ahead.
const cPrefetchMs = 600;

// ---- the map's look ------------------------------------------------------------

type StyleJSON = {
  version: number;
  sources: Record<string, { type: string; url?: string; tiles?: string[]; [k: string]: unknown }>;
  layers: { id: string; type: string; source?: string; layout?: Record<string, unknown>; paint?: Record<string, unknown> }[];
  sprite?: string;
  glyphs?: string;
  [k: string]: unknown;
};
type TileJSON = { tiles: string[]; minzoom?: number; maxzoom?: number; bounds?: number[]; attribution?: string };

// OpenFreeMap's dark style in the app's own greys (index.css), calm under
// the pins.
const cColours: Record<string, Record<string, unknown>> = {
  background: { "background-color": "#1c1e1d" },
  water: { "fill-color": "#121918" },
  waterway: { "line-color": "#121918" },
  landcover_ice_shelf: { "fill-color": "#202322" },
  landcover_glacier: { "fill-color": "#202322" },
  landuse_residential: { "fill-color": "#232625" },
  landcover_wood: { "fill-color": "#1f2523" },
  landuse_park: { "fill-color": "#1f2523" },
  building: { "fill-color": "#191b1a", "fill-outline-color": "#252827" },
  highway_path: { "line-color": "#272a29" },
  highway_minor: { "line-color": "#2a2d2c" },
  highway_major_casing: { "line-color": "rgba(78,86,84,0.55)" },
  highway_major_inner: { "line-color": "#2b2f2e" },
  highway_major_subtle: { "line-color": "#2c302f" },
  highway_motorway_casing: { "line-color": "rgba(78,86,84,0.55)" },
  highway_motorway_inner: { "line-color": "#2e3231" },
  highway_motorway_subtle: { "line-color": "#2a2d2c" },
  boundary_state: { "line-color": "#323735" },
  "boundary_country_z0-4": { "line-color": "#3c4341" },
  "boundary_country_z5-": { "line-color": "#3c4341" },
  water_name: { "text-color": "#4d5f5c", "text-halo-color": "rgba(18,25,24,0.8)" },
  highway_name_other: { "text-color": "#5c6563", "text-halo-color": "#1c1e1d" },
};
// Arrows and road numbers: noise under photos.
const cDropLayers = new Set(["road_oneway", "road_oneway_opposite", "highway_name_motorway"]);

/** OpenFreeMap's dark style, made ours: every URL through mapFetch (the
 *  vector source's TileJSON read here, so its tiles go that way too), the
 *  colours above, and place names in `lang` where the map has them. */
async function loadStyle(lang: string): Promise<StyleJSON> {
  const style = await mapFetchJSON<StyleJSON>(`${cMapHost}/styles/dark`);
  const used = new Set(style.layers.map((l) => l.source).filter(Boolean));
  for (const [id, src] of Object.entries(style.sources)) {
    if (!used.has(id)) { delete style.sources[id]; continue; }
    if (src.url) {
      const tj = await mapFetchJSON<TileJSON>(src.url);
      style.sources[id] = { type: src.type, tiles: tj.tiles.map(viaScheme), minzoom: tj.minzoom, maxzoom: tj.maxzoom, bounds: tj.bounds, attribution: tj.attribution };
    } else if (src.tiles) {
      src.tiles = src.tiles.map(viaScheme);
    }
  }
  if (style.sprite) style.sprite = viaScheme(style.sprite);
  if (style.glyphs) style.glyphs = viaScheme(style.glyphs);
  const name = ["coalesce", ["get", `name:${lang}`], ["get", "name:en"], ["get", "name_en"], ["get", "name"]];
  style.layers = style.layers.filter((l) => !cDropLayers.has(l.id)).map((l) => {
    const paint: Record<string, unknown> = { ...l.paint, ...cColours[l.id] };
    // A pattern the style names but its sprite doesn't have.
    delete paint["fill-pattern"];
    if (!l.id.startsWith("place_") && l.id !== "water_name") return { ...l, paint };
    // Names only, in the app's language: the towns' dots (an icon its
    // sprite lacks too) would sit under the pins.
    Object.assign(paint, { "text-color": "#86948f", "text-halo-color": "rgba(20,22,21,0.85)" });
    delete paint["icon-opacity"];
    const layout: Record<string, unknown> = { ...l.layout, "text-field": name };
    delete layout["icon-image"];
    delete layout["icon-size"];
    return { ...l, paint, layout };
  });
  return style;
}

// ---- the page -----------------------------------------------------------------

export default function PlacesView(props: { onOpenPhotos: () => void }) {
  return <ErrorBoundary where="tab"><PlacesMap {...props} /></ErrorBoundary>;
}

type Shown = { marker: Marker; btn: HTMLButtonElement; img: HTMLImageElement; badge: HTMLSpanElement; pin: Pin<MsgFile> };
type MapProblem = "webgl" | "style" | null;

const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

function PlacesMap({ onOpenPhotos }: { onOpenPhotos: () => void }) {
  const t = useT();
  const filter = usePhotoFilter();
  const { tags, personIds } = filter;
  const groupId = filter.group?.id ?? "";
  const pf: PlaceFilter = useMemo(() => ({ tags, personIds, groupId }), [tags, personIds, groupId]);
  const fk = filterKeyOf(pf);
  const filtered = isFiltered({ ...filter, area: null });

  // The map chooses the area: one left from Images (Back) goes.
  useEffect(() => { leaveArea(); }, []);

  // The map ends at the window's foot. A banner under the top bar (the
  // demo's, an update's) pushes it down, so its height is what is left
  // below where it starts.
  const rootRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = rootRef.current;
    const body = el?.closest(".app-body");
    if (!el || !body) return;
    const fit = () => el.style.setProperty("--pl-top", `${el.getBoundingClientRect().top + window.scrollY}px`);
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(body);
    window.addEventListener("resize", fit);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", fit);
    };
  }, []);

  const boxRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MapLibre | null>(null);
  const shownRef = useRef(new Map<string, Shown>());
  const [problem, setProblem] = useState<MapProblem>(null);
  const [attempt, setAttempt] = useState(0);
  const [ready, setReady] = useState(false);
  // The whole library's located count under the filter (null: not known yet).
  const [total, setTotal] = useState<number | null>(() => locatedOf(fk)?.total ?? null);
  // The device is still reading where the photos were taken (Places just
  // turned on): the map says so, and asks again as it fills in.
  const [reading, setReading] = useState(() => locatedOf(fk)?.reading ?? false);
  const [placesFailed, setPlacesFailed] = useState(false);
  const [viewing, setViewing] = useState<ViewerItem | null>(null);

  // What the latest render knows, for the map's own event handlers.
  const live = useRef({ pf, fk, t });
  live.current = { pf, fk, t };

  // ---- the pins ----
  const labelOf = useCallback((pin: Pin<MsgFile>, tt: TFunction) => {
    if (pin.count > 1 || !pin.cover) return tt("web.images.places.pin", { count: pin.count });
    const when = pin.cover.created ? fmtDate(tt.lang, pin.cover.created) : "";
    const video = (pin.cover.mime || "").startsWith("video/");
    if (!when) return tt("web.images.places.pin", { count: 1 });
    return video ? tt("web.images.places.pin_video", { date: when }) : tt("web.images.places.pin_photo", { date: when });
  }, []);

  const paintPin = useCallback((s: Shown, tt: TFunction) => {
    const { pin } = s;
    s.btn.setAttribute("aria-label", labelOf(pin, tt));
    s.btn.toggleAttribute("data-single", pin.count === 1);
    s.btn.toggleAttribute("data-video", pin.count === 1 && (pin.cover?.mime || "").startsWith("video/"));
    s.badge.textContent = pin.count >= 1000
      ? fmtNumber(tt.lang, pin.count, { notation: "compact", maximumFractionDigits: 1 })
      : fmtNumber(tt.lang, pin.count);
    const path = pin.cover?.path ?? "";
    const url = path ? thumbOf(path) : undefined;
    if (url && s.img.dataset.src !== url) {
      s.img.dataset.src = url;
      s.img.classList.remove("is-loaded");
      s.img.onload = () => s.img.classList.add("is-loaded");
      s.img.src = url;
    }
  }, [labelOf]);

  const openPin = useRef<(key: string) => void>(() => {});

  // The places of the tiles on screen at the zoom shown, as pins: what is
  // known at once, the rest as it comes. Until this zoom's first answer the
  // pins of the last one stay.
  const draw = useCallback(() => {
    const map = mapRef.current;
    if (!map) return;
    const { fk: key, t: tt } = live.current;
    const mz = map.getZoom();
    const z = deviceZoom(mz);
    const b = map.getBounds();
    const want = tilesFor({ west: b.getWest(), east: b.getEast(), south: b.getSouth(), north: b.getNorth() }, z, 1);
    const places: PhotoPlace[] = [];
    let known = 0;
    for (const tile of want) {
      const ps = placesOf(key, z, tile);
      if (!ps) continue;
      known++;
      places.push(...ps);
    }
    const shown = shownRef.current;
    if (!known && shown.size) return;
    // Each place where it is nearest the middle of the map, of the world's
    // copies side by side.
    const mid = map.getCenter().lng;
    const near = (lon: number) => lon + 360 * Math.round((mid - lon) / 360);
    const pins = mergePins(
      places.map((p) => ({
        key: `${z}:${wrapX(tileX(p.longitude, z + 2), z + 2)}:${tileY(p.latitude, z + 2)}`,
        latitude: p.latitude,
        longitude: p.longitude,
        count: p.count,
        area: (p.area ?? { south: p.latitude, west: p.longitude, north: p.latitude, east: p.longitude }) as Area,
        cover: p.cover,
        coverTime: p.cover?.created?.getTime() ?? 0,
      })),
      (p) => map.project([near(p.longitude), p.latitude]),
      cPinGap,
    );
    const next = new Set(pins.map((p) => p.key));
    for (const [k, s] of shown) {
      if (next.has(k)) continue;
      s.marker.remove();
      shown.delete(k);
    }
    for (const pin of pins) {
      let s = shown.get(pin.key);
      if (!s) {
        const root = document.createElement("div");
        root.className = "pl-pin";
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "pl-pin-btn";
        const img = document.createElement("img");
        img.alt = "";
        img.draggable = false;
        img.decoding = "async";
        const badge = document.createElement("span");
        badge.className = "pl-pin-count";
        badge.setAttribute("aria-hidden", "true");
        btn.append(img, badge);
        root.append(btn);
        const k = pin.key;
        btn.addEventListener("click", (e) => { e.stopPropagation(); openPin.current(k); });
        const marker = new Marker({ element: root, anchor: "center" }).setLngLat([pin.longitude, pin.latitude]).addTo(map);
        s = { marker, btn, img, badge, pin };
        shown.set(k, s);
      } else {
        // What joins a pin can move it to another of its places.
        if (s.pin.latitude !== pin.latitude || s.pin.longitude !== pin.longitude) s.marker.setLngLat([pin.longitude, pin.latitude]);
        s.pin = pin;
      }
      paintPin(s, tt);
    }
    // The thumbnails of the pins on screen, nearest the middle first.
    const w = map.getContainer().clientWidth;
    const h = map.getContainer().clientHeight;
    const onScreen = pins
      .filter((p) => p.cover?.path && p.x > -cPinGap && p.y > -cPinGap && p.x < w + cPinGap && p.y < h + cPinGap)
      .sort((a, c) => Math.hypot(a.x - w / 2, a.y - h / 2) - Math.hypot(c.x - w / 2, c.y - h / 2));
    wantThumbs(onScreen.map((p) => p.cover!.path));
  }, [paintPin]);

  // A pin pressed: one photo opens; a group flies in, or opens in Images.
  openPin.current = (key: string) => {
    const map = mapRef.current;
    const s = shownRef.current.get(key);
    if (!map || !s) return;
    const { pin } = s;
    if (pin.count === 1 && pin.cover) {
      const c = pin.cover;
      setViewing({ path: c.path, mime: c.mime, hash: c.hash, thumbURL: thumbOf(c.path) || undefined });
      return;
    }
    if (map.getZoom() < cFlyBelowZoom && areaSpanMetres(pin.area) > cSpreadMetres) {
      // Fitted to its photos, and at least twice as close.
      const cam = map.cameraForBounds(fitCorners(pin.area), { padding: 96, maxZoom: cMaxZoom - 1 });
      const zoom = Math.min(cMaxZoom - 1, Math.max(cam?.zoom ?? 0, map.getZoom() + 1));
      map.easeTo({ center: cam?.center ?? map.getCenter(), zoom, duration: reducedMotion() ? 0 : 700 });
      return;
    }
    keepView();
    showArea(pin.area);
    onOpenPhotos();
  };

  const keepView = useCallback(() => {
    const map = mapRef.current;
    if (!map) return;
    const c = map.getCenter();
    keepCamera({ filter: live.current.fk, lng: c.lng, lat: c.lat, zoom: map.getZoom() });
  }, []);

  // ---- asking the device ----
  const settleTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const prefetchTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const retryTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const fails = useRef(0);
  const ask = useCallback(async () => {
    const map = mapRef.current;
    if (!map) return;
    clearTimeout(retryTimer.current);
    const { pf: f } = live.current;
    const z = deviceZoom(map.getZoom());
    const b = map.getBounds();
    const view = { west: b.getWest(), east: b.getEast(), south: b.getSouth(), north: b.getNorth() };
    const ok = await askTiles(f, z, tilesFor(view, z, 1));
    if (mapRef.current !== map) return;
    if (!ok) {
      setPlacesFailed(true);
      retryTimer.current = setTimeout(() => void ask(), Math.min(2000 * 2 ** fails.current++, 30_000));
      return;
    }
    fails.current = 0;
    setPlacesFailed(false);
    // The next zoom's, ahead, while the map rests.
    clearTimeout(prefetchTimer.current);
    prefetchTimer.current = setTimeout(() => {
      if (mapRef.current !== map || z >= 20) return;
      const ahead = tilesFor(view, z + 1, 0);
      if (ahead.length <= 64) void askTiles(live.current.pf, z + 1, ahead);
    }, cPrefetchMs);
  }, []);

  const settle = useCallback(() => {
    clearTimeout(settleTimer.current);
    clearTimeout(prefetchTimer.current);
    settleTimer.current = setTimeout(() => void ask(), cSettleMs);
  }, [ask]);

  // ---- the map itself, again when the language changes ----
  useEffect(() => {
    const box = boxRef.current;
    const shown = shownRef.current;
    if (!box) return;
    let gone = false;
    let map: MapLibre | null = null;
    setProblem(null);
    setReady(false);
    addMapProtocol();
    (async () => {
      let style: StyleJSON;
      try {
        style = await loadStyle(t.lang);
      } catch (err) {
        console.warn("The map's style didn't load:", err);
        if (!gone) setProblem("style");
        return;
      }
      if (gone) return;
      const cam = lastCamera();
      const here = cam && cam.filter === live.current.fk ? cam : null;
      try {
        map = new MapLibre({
          container: box,
          style: style as unknown as MapOptions["style"],
          center: here ? [here.lng, here.lat] : [10, 30],
          zoom: here ? here.zoom : 1,
          minZoom: 0,
          maxZoom: cMaxZoom,
          maxPitch: 0,
          dragRotate: false,
          pitchWithRotate: false,
          touchPitch: false,
          attributionControl: false,
          fadeDuration: 150,
          locale: { "Map.Title": t("web.images.places.map") },
        });
      } catch (err) {
        console.warn("The map couldn't start:", err);
        setProblem("webgl");
        return;
      }
      map.touchZoomRotate.disableRotation();
      map.keyboard.disableRotation();
      // The credits OpenFreeMap and OpenStreetMap ask for, written out
      // (compact, they hide behind an icon MapLibre draws from a data: URL,
      // which img-src doesn't allow; the zoom buttons are ours for that).
      map.addControl(new AttributionControl({ compact: false }), "bottom-left");
      mapRef.current = map;
      // Never more than one world across: further out its copies side by
      // side would show each place once, on one of them.
      const oneWorld = () => map?.setMinZoom(Math.max(0, Math.log2(box.clientWidth / 512)));
      oneWorld();
      map.on("resize", oneWorld);
      map.on("moveend", () => { draw(); settle(); keepView(); });
      map.on("load", () => {
        if (gone || !map) return;
        setReady(true);
        if (here) { draw(); settle(); return; }
        void frame(map);
      });
    })();
    return () => {
      gone = true;
      clearTimeout(settleTimer.current);
      clearTimeout(prefetchTimer.current);
      clearTimeout(retryTimer.current);
      if (map) {
        keepView();
        for (const s of shown.values()) s.marker.remove();
        shown.clear();
        map.remove();
      }
      mapRef.current = null;
    };
  }, [t.lang, attempt]); // eslint-disable-line react-hooks/exhaustive-deps

  // Frames every located photo under the filter (a spot alone: a city),
  // or the world when there is none.
  const frame = useCallback(async (map: MapLibre) => {
    const f = live.current.pf;
    let loc = await askLocated(f);
    for (let n = 0; !loc && mapRef.current === map && n < 30; n++) {
      setPlacesFailed(true);
      await new Promise((r) => setTimeout(r, Math.min(2000 * 2 ** n, 30_000)));
      if (filterKeyOf(live.current.pf) !== filterKeyOf(f)) return;
      loc = await askLocated(f);
    }
    if (mapRef.current !== map || !loc) return;
    setPlacesFailed(false);
    setTotal(loc.total);
    setReading(loc.reading);
    const padding = { top: 96, bottom: 64, left: 64, right: 64 };
    const cam = loc.area ? map.cameraForBounds(fitCorners(loc.area), { padding, maxZoom: cFrameMaxZoom }) : undefined;
    if (cam && (cam.zoom ?? 0) > map.getMinZoom() + 0.25) {
      map.jumpTo({ center: cam.center, zoom: cam.zoom });
    } else {
      // Photos all over the world: the world, its middle where most of
      // them are (the biggest place of zoom 0, which is at a photo), not
      // across an ocean between them.
      const world = placesOf(filterKeyOf(f), 0, { x: 0, y: 0 }) ?? [];
      const most = world.reduce<PhotoPlace | null>((a, p) => (!a || p.count > a.count ? p : a), null);
      map.jumpTo({ center: [most?.longitude ?? 10, 25], zoom: map.getMinZoom() });
    }
    draw();
    settle();
  }, [draw, settle]);

  // Another filter (the top bar's search): its pins, framed afresh.
  const firstFilter = useRef(fk);
  useEffect(() => {
    if (firstFilter.current === fk) return;
    firstFilter.current = fk;
    const map = mapRef.current;
    for (const s of shownRef.current.values()) s.marker.remove();
    shownRef.current.clear();
    setTotal(locatedOf(fk)?.total ?? null);
    setReading(locatedOf(fk)?.reading ?? false);
    if (map && ready) void frame(map);
  }, [fk, ready, frame]);

  // Places and thumbnails as they come.
  useEffect(() => onPlaces(() => {
    draw();
    const loc = locatedOf(live.current.fk);
    if (loc) {
      setTotal(loc.total);
      setReading(loc.reading);
    }
  }), [draw]);
  // While the device reads, what is on screen is asked again as its
  // answers age (cKeepReadingMs), the count and area with it.
  useEffect(() => {
    if (!reading || !ready) return;
    const timer = setInterval(() => {
      void askLocated(live.current.pf);
      void ask();
    }, cKeepReadingMs);
    return () => clearInterval(timer);
  }, [reading, ready, ask]);
  useEffect(() => onThumbs(() => {
    for (const s of shownRef.current.values()) paintPin(s, live.current.t);
  }), [paintPin]);
  // The labels in another language.
  useEffect(() => {
    for (const s of shownRef.current.values()) paintPin(s, t);
  }, [t, paintPin]);

  const zoomBy = (d: 1 | -1) => {
    const map = mapRef.current;
    if (!map) return;
    if (d > 0) map.zoomIn({ animate: !reducedMotion() });
    else map.zoomOut({ animate: !reducedMotion() });
  };

  const note = problem === "webgl"
    ? { title: t("web.images.places.no_map"), text: t("web.images.places.no_map_text"), retry: false }
    : problem === "style"
      ? { title: t("web.images.places.map_failed"), text: t("web.images.places.map_failed_text"), retry: true }
      : total === 0 && !reading
        ? { title: filtered ? t("web.images.places.empty_filtered") : t("web.images.places.empty"), text: t("web.images.places.empty_text"), retry: false }
        : null;

  return (
    <div ref={rootRef} className="pl-root">
      <div ref={boxRef} className="pl-map" />
      <div className="pl-head">
        <h1 className="pl-title">{t("web.shell.menu.places")}</h1>
        <p className="pl-sub" aria-live="polite">
          {placesFailed
            ? t("web.images.places.failed")
            : total == null
              ? (problem ? "" : <Spinner label={t("web.images.places.loading")} />)
              : total > 0 && t("web.images.places.located", { count: total })}
        </p>
        {reading && !placesFailed && <p className="pl-sub pl-reading">{t("web.images.places.reading")}</p>}
      </div>
      {ready && (
        <div className="pl-zoom">
          <button type="button" className="pl-zoom-btn" aria-label={t("web.images.places.zoom_in")} data-tip={t("web.images.places.zoom_in")} onClick={() => zoomBy(1)}>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
          </button>
          <button type="button" className="pl-zoom-btn" aria-label={t("web.images.places.zoom_out")} data-tip={t("web.images.places.zoom_out")} onClick={() => zoomBy(-1)}>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><path d="M5 12h14" /></svg>
          </button>
        </div>
      )}
      {note && (
        <div className="pl-note" role="status">
          <p className="pl-note-title">{note.title}</p>
          <p className="pl-note-text">{note.text}</p>
          {note.retry && (
            <button type="button" className="pl-btn" onClick={() => setAttempt((n) => n + 1)}>{t("common.try_again")}</button>
          )}
        </div>
      )}
      {viewing && (
        <Part kind="layer" onClose={() => setViewing(null)}>
          <MediaViewer items={[viewing]} index={0} onIndexChange={() => {}} onClose={() => setViewing(null)} />
        </Part>
      )}
    </div>
  );
}
