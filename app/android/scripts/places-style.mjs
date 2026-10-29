// SPDX-License-Identifier: AGPL-3.0-or-later

// The Android map's styles (Places, PlacesMap.kt, and the viewer's info
// panel): OpenFreeMap's dark style made the app's as the web makes it at
// runtime (web/src/components/PlacesView.tsx's loadStyle - the same
// colours, the same layers dropped; change them together), and its
// Positron style made the light one the same way. Written to the app's
// assets, so the map starts with no style to fetch:
//
//   node app/android/scripts/places-style.mjs
//
// Run it again when OpenFreeMap's styles change (new layers, a new sprite)
// and commit the two files. The vector source keeps its TileJSON URL, so
// the tiles stay OpenFreeMap's current ones. Place names read
// name:__OTC_LANG__, which the app replaces with its language's code.

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const cHost = "https://tiles.openfreemap.org";
const cOut = resolve(dirname(fileURLToPath(import.meta.url)), "../app/src/main/assets/places");
export const cLangToken = "__OTC_LANG__";

// The web's (PlacesView.tsx cColours): OpenFreeMap's dark in the app's greys.
const cDark = {
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

const looks = [
  {
    file: "style-dark.json",
    style: "dark",
    colours: cDark,
    // Arrows and road numbers: noise under photos (the web's cDropLayers).
    drop: new Set(["road_oneway", "road_oneway_opposite", "highway_name_motorway"]),
    // Names in the app's language, without the towns' dots.
    named: (id) => id.startsWith("place_") || id === "water_name",
    nameColours: { "text-color": "#86948f", "text-halo-color": "rgba(20,22,21,0.85)" },
  },
  {
    file: "style-light.json",
    style: "positron",
    colours: {},
    // Road numbers and the airports' planes, as the dark style drops them.
    drop: new Set(["highway-shield-non-us", "highway-shield-us-interstate", "road_shield_us", "airport"]),
    named: (id) => id.startsWith("label_") || id.startsWith("water_name") || id === "waterway_line_label",
    nameColours: {},
  },
];

async function json(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`${url}: ${r.status}`);
  return r.json();
}

const name = ["coalesce", ["get", `name:${cLangToken}`], ["get", "name:en"], ["get", "name_en"], ["get", "name"]];

function made(style, look) {
  const used = new Set(style.layers.map((l) => l.source).filter(Boolean));
  for (const id of Object.keys(style.sources)) if (!used.has(id)) delete style.sources[id];
  style.layers = style.layers.filter((l) => !look.drop.has(l.id)).map((l) => {
    const paint = { ...l.paint, ...look.colours[l.id] };
    // A pattern from the sprite, which the map doesn't load.
    delete paint["fill-pattern"];
    delete paint["line-pattern"];
    if (!look.named(l.id)) return { ...l, paint };
    Object.assign(paint, look.nameColours);
    delete paint["icon-opacity"];
    const layout = { ...l.layout, "text-field": name };
    delete layout["icon-image"];
    delete layout["icon-size"];
    return { ...l, paint, layout };
  });
  // Nothing left draws from the sprite: none is fetched.
  const icons = style.layers.filter((l) => l.layout?.["icon-image"] || l.paint?.["fill-pattern"] || l.paint?.["line-pattern"]);
  if (icons.length) throw new Error(`${look.style}: layers still draw icons: ${icons.map((l) => l.id).join(", ")}`);
  delete style.sprite;
  for (const src of Object.values(style.sources)) {
    for (const u of [src.url, ...(src.tiles ?? [])].filter(Boolean)) {
      if (!u.startsWith(cHost + "/")) throw new Error(`${look.style}: a source outside OpenFreeMap: ${u}`);
    }
  }
  if (!style.glyphs?.startsWith(cHost + "/")) throw new Error(`${look.style}: glyphs outside OpenFreeMap`);
  return style;
}

mkdirSync(cOut, { recursive: true });
for (const look of looks) {
  const style = made(await json(`${cHost}/styles/${look.style}`), look);
  const out = resolve(cOut, look.file);
  writeFileSync(out, JSON.stringify(style) + "\n");
  console.log(`${look.file}: ${style.layers.length} layers`);
}
