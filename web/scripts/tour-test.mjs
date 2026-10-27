// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the first-run tour's rules (src/components/tourSteps.ts): the
// steps and moving between them, the seen flag that opens it by itself
// once, and the hole the dimmed layer leaves around a step's control. The
// web app has no test runner, so this loads the module through Vite's SSR
// loader in Node, as i18n-test.mjs does:
//
//   node web/scripts/tour-test.mjs        (needs web/node_modules)
//
// The phones' TourTests.swift and TourTest.kt test their own steps by the
// same rules (steps, after, before, place).
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m; // the module under test
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-tour-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // One module with no imports: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/tourSteps.ts");
});
after(() => server?.close());

const withFaces = ["welcome", "menu", "images", "people", "collections", "files", "social", "post", "friends", "alerts", "done"];
const withoutFaces = withFaces.filter((s) => s !== "people");

// A localStorage stand-in: a Map, with getItem/setItem that can be made to throw.
function storage(initial = {}, { getThrows = false, setThrows = false } = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    getItem(k) {
      if (getThrows) throw new Error("SecurityError");
      return map.has(k) ? map.get(k) : null;
    },
    setItem(k, v) {
      if (setThrows) throw new Error("QuotaExceededError");
      map.set(k, String(v));
    },
  };
}

describe("the steps", () => {
  test("eleven with face recognition on, People fourth", () => {
    assert.deepEqual(m.tourSteps(true), withFaces);
  });
  test("ten with it off, and while it isn't known yet (People hidden, as in the menu)", () => {
    assert.deepEqual(m.tourSteps(false), withoutFaces);
    assert.deepEqual(m.tourSteps(null), withoutFaces);
  });
  test("each call is a new list", () => {
    const a = m.tourSteps(true);
    a.pop();
    assert.equal(m.tourSteps(true).length, 11);
  });
});

describe("moving between steps", () => {
  const all = withFaces;
  test("Next goes to the following step, none after the last", () => {
    assert.equal(m.stepAfter("welcome", all), "menu");
    assert.equal(m.stepAfter("images", all), "people");
    assert.equal(m.stepAfter("alerts", all), "done");
    assert.equal(m.stepAfter("done", all), null);
  });
  test("Back goes to the one before, none before the first", () => {
    assert.equal(m.stepBefore("menu", all), "welcome");
    assert.equal(m.stepBefore("collections", all), "people");
    assert.equal(m.stepBefore("welcome", all), null);
  });
  test("without People, Images and Collections are next to each other", () => {
    assert.equal(m.stepAfter("images", withoutFaces), "collections");
    assert.equal(m.stepBefore("collections", withoutFaces), "images");
  });
  test("from a step that went, they go on from where it was", () => {
    assert.equal(m.stepAfter("people", withoutFaces), "collections");
    assert.equal(m.stepBefore("people", withoutFaces), "images");
  });
});

describe("where the tour stands", () => {
  test("a step that is there stays", () => {
    for (const s of withFaces) assert.equal(m.stepPlace(s, withFaces), s);
    for (const s of withoutFaces) assert.equal(m.stepPlace(s, withoutFaces), s);
  });
  test("People, once face recognition is turned off, moves on to the next step", () => {
    assert.equal(m.stepPlace("people", withoutFaces), "collections");
  });
  test("a last step that went falls back to the one before, and an empty list to the first", () => {
    assert.equal(m.stepPlace("done", ["welcome", "menu"]), "menu");
    assert.equal(m.stepPlace("menu", []), "welcome");
  });
});

describe("opening by itself (the seen flag)", () => {
  test("the key and the version", () => {
    assert.equal(m.cTourKey, "otc_tour_seen");
    assert.equal(m.cTourVersion, 1);
  });
  test("a browser that never saw it: it opens, and is marked seen before it shows", () => {
    const s = storage();
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(s.map.get("otc_tour_seen"), "1");
  });
  test("once only: a reload, or signing out and in again, doesn't open it", () => {
    const s = storage();
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(m.claimAutoTour(() => s), false);
    assert.equal(m.claimAutoTour(() => s), false);
  });
  test("seen this version, or a newer one: it doesn't open", () => {
    assert.equal(m.claimAutoTour(() => storage({ otc_tour_seen: "1" })), false);
    assert.equal(m.claimAutoTour(() => storage({ otc_tour_seen: "2" })), false);
  });
  test("an older version or a value that isn't one: it opens again, and the flag is rewritten", () => {
    for (const old of ["0", "", "yes", "-3"]) {
      const s = storage({ otc_tour_seen: old });
      assert.equal(m.claimAutoTour(() => s), true, `stored ${JSON.stringify(old)}`);
      assert.equal(s.map.get("otc_tour_seen"), "1");
    }
  });
  test("storage that throws, or none: no tour by itself (rather than one at every load)", () => {
    assert.equal(m.claimAutoTour(() => storage({}, { getThrows: true })), false);
    const full = storage({}, { setThrows: true });
    assert.equal(m.claimAutoTour(() => full), false);
    assert.equal(full.map.size, 0);
    assert.equal(m.claimAutoTour(() => { throw new Error("SecurityError"); }), false);
    assert.equal(m.claimAutoTour(() => null), false);
    assert.equal(m.claimAutoTour(() => undefined), false);
  });
  test("other keys are left alone", () => {
    const s = storage({ otc_menu_open: "0", otc_language: "de" });
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(s.map.get("otc_menu_open"), "0");
    assert.equal(s.map.get("otc_language"), "de");
  });
});

describe("what each step rings", () => {
  test("every step has its entry; the welcome rings nothing", () => {
    assert.deepEqual(Object.keys(m.cStepTargets).sort(), [...withFaces].sort());
    assert.deepEqual(m.cStepTargets.welcome, []);
    for (const s of withFaces.filter((x) => x !== "welcome")) assert.ok(m.cStepTargets[s].length > 0, s);
  });
  test("the menu's items only in the menu beside the page, never the closed drawer", () => {
    for (const s of ["images", "people", "collections", "files", "social", "friends", "alerts"]) {
      for (const group of m.cStepTargets[s]) for (const sel of group) assert.match(sel, /^\.sb-dock /, `${s}: ${sel}`);
    }
  });
  test("the menu step: the menu beside the page first, else the menu button", () => {
    assert.deepEqual(m.cStepTargets.menu, [['.sb-dock[data-tour="menu"]'], ['[data-tour="menu-button"]']]);
  });
  test("Alerts and Settings together; the post and the last step in the top bar", () => {
    assert.equal(m.cStepTargets.alerts[0].length, 2);
    assert.deepEqual(m.cStepTargets.post, [['[data-tour="new-post"]']]);
    assert.deepEqual(m.cStepTargets.done, [['[data-tour="account"]']]);
  });
});

describe("the hole", () => {
  const box = (left, top, right, bottom) => ({ left, top, right, bottom });
  test("a control's box, 6px around", () => {
    assert.deepEqual(m.holeFor([box(100, 100, 140, 140)], 1280, 800), box(94, 94, 146, 146));
  });
  test("several together: one hole around them all", () => {
    assert.deepEqual(m.holeFor([box(12, 500, 244, 544), box(12, 546, 244, 590)], 1280, 800), box(6, 494, 250, 596));
  });
  test("kept inside the window, so its ring shows whole", () => {
    // The menu beside the page: from under the top bar to the window's foot.
    assert.deepEqual(m.holeFor([box(0, 64, 256, 800)], 1280, 800), box(2, 58, 262, 798));
    // The account picture at the top right.
    assert.deepEqual(m.holeFor([box(1226, 12, 1266, 52)], 1270, 800), box(1220, 6, 1268, 58));
  });
  test("nothing on screen: no hole", () => {
    assert.equal(m.holeFor([], 1280, 800), null);
    assert.equal(m.holeFor([box(10, 10, 10, 50)], 1280, 800), null); // no width (display: none)
    assert.equal(m.holeFor([box(-80, 100, 0, 140)], 1280, 800), null); // left of the window
    assert.equal(m.holeFor([box(100, 900, 140, 940)], 1280, 800), null); // below it
    assert.equal(m.holeFor([box(100, 100, 140, 140)], 0, 0), null); // the dialog not laid out yet
  });
  test("one off screen and one on: only the one on screen", () => {
    assert.deepEqual(m.holeFor([box(100, 900, 140, 940), box(100, 100, 140, 140)], 1280, 800), box(94, 94, 146, 146));
  });
  test("round around a round control, slightly rounded around the rest", () => {
    assert.equal(m.holeRadius(box(0, 0, 52, 52), true), 26);
    assert.equal(m.holeRadius(box(0, 0, 244, 56), true), 28);
    assert.equal(m.holeRadius(box(0, 0, 92, 68), false), 14);
    assert.equal(m.holeRadius(box(0, 0, 20, 20), false), 10);
  });
  test("a click inside it, edges included, goes on to the control", () => {
    const h = box(94, 94, 146, 146);
    assert.equal(m.insideHole(h, 120, 120), true);
    assert.equal(m.insideHole(h, 94, 146), true);
    assert.equal(m.insideHole(h, 93, 120), false);
    assert.equal(m.insideHole(h, 120, 147), false);
    assert.equal(m.insideHole(null, 120, 120), false);
  });
});

describe("the dimmed layer's outline", () => {
  test("no hole: the whole window", () => {
    assert.equal(m.dimPath(1280, 800, null, 0), "M0 0H1280V800H0Z");
  });
  test("a hole: the window, then the hole (evenodd leaves it clear)", () => {
    const d = m.dimPath(1280, 800, { left: 94, top: 94, right: 146, bottom: 146 }, 14);
    assert.ok(d.startsWith("M0 0H1280V800H0Z"));
    assert.equal(d.split("Z").length - 1, 2);
    assert.equal(d.slice("M0 0H1280V800H0Z".length), m.roundedBoxPath({ left: 94, top: 94, right: 146, bottom: 146 }, 14));
  });
  test("a rounded box: four arcs; a square one, none", () => {
    const rounded = m.roundedBoxPath({ left: 10, top: 20, right: 110, bottom: 70 }, 14);
    assert.equal(rounded, "M24 20H96A14 14 0 0 1 110 34V56A14 14 0 0 1 96 70H24A14 14 0 0 1 10 56V34A14 14 0 0 1 24 20Z");
    assert.equal(m.roundedBoxPath({ left: 10, top: 20, right: 110, bottom: 70 }, 0), "M10 20H110V70H10Z");
  });
  test("the radius never passes half the box", () => {
    assert.equal(
      m.roundedBoxPath({ left: 0, top: 0, right: 40, bottom: 20 }, 99),
      m.roundedBoxPath({ left: 0, top: 0, right: 40, bottom: 20 }, 10),
    );
  });
});
