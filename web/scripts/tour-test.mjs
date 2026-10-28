// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for the first-run tour's rules (src/components/tourSteps.ts and
// tourSeen.ts): the steps and moving between them, the section each one
// shows and the controls it rings there, the seen flag that opens it by
// itself once, the holes the dimmed layer leaves around those controls,
// where the card goes so it covers none of them, and how far the page is
// scrolled to show one. The web app has no test runner, so this loads the
// modules through Vite's SSR loader in Node, as i18n-test.mjs does:
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
let m; // the modules under test, together
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-tour-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    // Modules with no imports but types: nothing to pre-bundle.
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = {
    ...(await server.ssrLoadModule("/src/components/tourSteps.ts")),
    ...(await server.ssrLoadModule("/src/components/tourSeen.ts")),
  };
});
after(() => server?.close());

// Every step: face recognition on, and the device's owner.
const all = ["welcome", "search", "dates", "select", "viewer", "people", "collections", "files", "upload", "social", "post", "friends", "users", "alerts"];
const withoutFaces = all.filter((s) => s !== "people");
const withoutUsers = all.filter((s) => s !== "users");
const neither = all.filter((s) => s !== "people" && s !== "users");

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

const box = (left, top, right, bottom) => ({ left, top, right, bottom });

describe("the steps", () => {
  test("fourteen with face recognition on, to the owner: People after the viewer, Users before the last", () => {
    assert.deepEqual(m.tourSteps(true, true), all);
    assert.equal(all.indexOf("users"), all.length - 2);
  });
  test("People only with it on, and not while it isn't known yet (hidden, as in the menu)", () => {
    assert.deepEqual(m.tourSteps(false, true), withoutFaces);
    assert.deepEqual(m.tourSteps(null, true), withoutFaces);
  });
  test("Users only to the device's owner: not to a user, nor while it isn't known yet", () => {
    assert.deepEqual(m.tourSteps(true, false), withoutUsers);
    assert.deepEqual(m.tourSteps(true, null), withoutUsers);
  });
  test("twelve with neither", () => {
    assert.deepEqual(m.tourSteps(false, false), neither);
    assert.deepEqual(m.tourSteps(null, null), neither);
    assert.equal(neither.length, 12);
  });
  test("each call is a new list", () => {
    const a = m.tourSteps(true, true);
    a.pop();
    assert.equal(m.tourSteps(true, true).length, 14);
  });
});

describe("moving between steps", () => {
  test("Next goes to the following step, none after the last", () => {
    assert.equal(m.stepAfter("welcome", all), "search");
    assert.equal(m.stepAfter("viewer", all), "people");
    assert.equal(m.stepAfter("files", all), "upload");
    assert.equal(m.stepAfter("friends", all), "users");
    assert.equal(m.stepAfter("users", all), "alerts");
    assert.equal(m.stepAfter("alerts", all), null);
  });
  test("Back goes to the one before, none before the first", () => {
    assert.equal(m.stepBefore("search", all), "welcome");
    assert.equal(m.stepBefore("collections", all), "people");
    assert.equal(m.stepBefore("welcome", all), null);
  });
  test("without People, the viewer and Collections are next to each other", () => {
    assert.equal(m.stepAfter("viewer", withoutFaces), "collections");
    assert.equal(m.stepBefore("collections", withoutFaces), "viewer");
  });
  test("without Users, Friends and the last step are next to each other", () => {
    assert.equal(m.stepAfter("friends", withoutUsers), "alerts");
    assert.equal(m.stepBefore("alerts", withoutUsers), "friends");
    assert.equal(m.stepBefore("alerts", all), "users");
  });
  test("from a step that went, they go on from where it was", () => {
    assert.equal(m.stepAfter("people", withoutFaces), "collections");
    assert.equal(m.stepBefore("people", withoutFaces), "viewer");
    assert.equal(m.stepAfter("users", withoutUsers), "alerts");
    assert.equal(m.stepBefore("users", withoutUsers), "friends");
  });
});

describe("where the tour stands", () => {
  test("a step that is there stays", () => {
    for (const steps of [all, withoutFaces, withoutUsers, neither]) for (const s of steps) assert.equal(m.stepPlace(s, steps), s);
  });
  test("People, once face recognition is turned off, moves on to the next step", () => {
    assert.equal(m.stepPlace("people", withoutFaces), "collections");
  });
  test("Users, where the owner isn't known (an unanswered question), moves on to the last step", () => {
    assert.equal(m.stepPlace("users", withoutUsers), "alerts");
  });
  test("a last step that went falls back to the one before, and an empty list to the first", () => {
    assert.equal(m.stepPlace("alerts", ["welcome", "search"]), "search");
    assert.equal(m.stepPlace("search", []), "welcome");
  });
});

describe("the section each step shows", () => {
  test("every step has one; the welcome stays where the tour opened", () => {
    assert.deepEqual(Object.keys(m.cStepSection).sort(), [...all].sort());
    assert.equal(m.cStepSection.welcome, null);
  });
  test("the search and the photo steps show Images; each section's steps show it", () => {
    for (const s of ["search", "dates", "select", "viewer"]) assert.equal(m.cStepSection[s], "PhotoGallery", s);
    assert.equal(m.cStepSection.people, "People");
    assert.equal(m.cStepSection.collections, "Collections");
    assert.equal(m.cStepSection.files, "AdminPannel");
    assert.equal(m.cStepSection.upload, "AdminPannel");
    assert.equal(m.cStepSection.social, "Social");
    assert.equal(m.cStepSection.post, "Social");
    assert.equal(m.cStepSection.friends, "Friends");
    assert.equal(m.cStepSection.users, "Settings");
  });
  test("never Alerts, which marks every alert read as it opens: the last step shows Settings", () => {
    assert.equal(m.cStepSection.alerts, "Settings");
    assert.ok(!Object.values(m.cStepSection).includes("Notifications"));
  });
});

describe("opening by itself (the seen flag)", () => {
  test("the key and the version: 2, the tour that shows how things work", () => {
    assert.equal(m.cTourKey, "otc_tour_seen");
    assert.equal(m.cTourVersion, 2);
  });
  test("a browser that never saw it: it opens, and is marked seen before it shows", () => {
    const s = storage();
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(s.map.get("otc_tour_seen"), "2");
  });
  test("a browser that saw the first tour sees this one once", () => {
    const s = storage({ otc_tour_seen: "1" });
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(s.map.get("otc_tour_seen"), "2");
    assert.equal(m.claimAutoTour(() => s), false);
  });
  test("once only: a reload, or signing out and in again, doesn't open it", () => {
    const s = storage();
    assert.equal(m.claimAutoTour(() => s), true);
    assert.equal(m.claimAutoTour(() => s), false);
    assert.equal(m.claimAutoTour(() => s), false);
  });
  test("seen this version, or a newer one: it doesn't open", () => {
    assert.equal(m.claimAutoTour(() => storage({ otc_tour_seen: "2" })), false);
    assert.equal(m.claimAutoTour(() => storage({ otc_tour_seen: "3" })), false);
  });
  test("an older version or a value that isn't one: it opens again, and the flag is rewritten", () => {
    for (const old of ["0", "1", "", "yes", "-3"]) {
      const s = storage({ otc_tour_seen: old });
      assert.equal(m.claimAutoTour(() => s), true, `stored ${JSON.stringify(old)}`);
      assert.equal(s.map.get("otc_tour_seen"), "2");
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
  test("every step has its entry, each with something to look for", () => {
    assert.deepEqual(Object.keys(m.cStepTargets).sort(), [...all].sort());
    for (const s of all) {
      assert.ok(m.cStepTargets[s].length > 0, s);
      for (const group of m.cStepTargets[s]) assert.ok(group.length > 0, s);
    }
  });
  test("the menu's items only in the menu beside the page, never the closed drawer", () => {
    for (const s of all) {
      for (const group of m.cStepTargets[s]) {
        for (const sel of group) if (/data-tour="(PhotoGallery|People|Collections|AdminPannel|Social|Friends|Notifications|Settings)"/.test(sel)) assert.match(sel, /^\.sb-dock /, `${s}: ${sel}`);
      }
    }
  });
  test("the welcome: the menu beside the page first, else the menu button", () => {
    assert.deepEqual(m.cStepTargets.welcome, [['.sb-dock[data-tour="menu"]'], ['[data-tour="menu-button"]']]);
  });
  test("the controls the steps teach: the search, the dates, a photo, Files' views, + New post, the friend's field, adding a user, Show the tour", () => {
    const all = (s) => m.cStepTargets[s].flat();
    assert.ok(all("search").includes(".ts-field"));
    assert.ok(all("dates").some((sel) => sel.includes(".ds-root")));
    assert.ok(all("select").some((sel) => sel.includes(".pg-tile")));
    assert.ok(m.cStepTargets.files.every((g) => g[0] === ".fb-viewmode"));
    assert.deepEqual(m.cStepTargets.post[0], ['[data-tour="new-post"]']);
    assert.ok(m.cStepTargets.friends.every((g) => g.includes(".add-friend")));
    // Settings › Users' name field and Add User, and the bridge box, once its list is in (UsersPanel).
    assert.deepEqual(m.cStepTargets.users, [[".up-list ~ .up-add-row", ".up-list ~ .up-bridge-opt"]]);
    assert.ok(m.cStepTargets.alerts.every((g) => g[0] === '[data-tour="show-tour"]'));
  });
});

describe("the holes", () => {
  test("one control: its box, 6px around", () => {
    assert.deepEqual(m.holeFor([box(100, 100, 140, 140)], 1280, 800), box(94, 94, 146, 146));
    assert.deepEqual(m.holesFor([box(100, 100, 140, 140)], 1280, 800), [box(94, 94, 146, 146)]);
  });
  test("holeFor: several together, one hole around them all", () => {
    assert.deepEqual(m.holeFor([box(12, 500, 244, 544), box(12, 546, 244, 590)], 1280, 800), box(6, 494, 250, 596));
  });
  test("holesFor: controls apart, a hole each, in their order", () => {
    assert.deepEqual(m.holesFor([box(400, 12, 900, 52), box(30, 300, 230, 340)], 1280, 800), [box(394, 6, 906, 58), box(24, 294, 236, 346)]);
  });
  test("holesFor: holes that would touch are one (even-odd would dim their overlap again)", () => {
    assert.deepEqual(m.holesFor([box(12, 500, 244, 544), box(12, 546, 244, 590)], 1280, 800), [box(6, 494, 250, 596)]);
    // A merge that reaches a third.
    assert.deepEqual(
      m.holesFor([box(100, 100, 150, 150), box(300, 100, 350, 150), box(140, 140, 310, 160)], 1280, 800),
      [box(94, 94, 356, 166)],
    );
  });
  test("kept inside the window, so a ring shows whole", () => {
    // The menu beside the page: from under the top bar to the window's foot.
    assert.deepEqual(m.holeFor([box(0, 64, 256, 800)], 1280, 800), box(2, 58, 262, 798));
    assert.deepEqual(m.holesFor([box(1226, 12, 1266, 52)], 1270, 800), [box(1220, 6, 1268, 58)]);
  });
  test("nothing on screen: no hole", () => {
    assert.equal(m.holeFor([], 1280, 800), null);
    assert.deepEqual(m.holesFor([], 1280, 800), []);
    assert.deepEqual(m.holesFor([box(10, 10, 10, 50)], 1280, 800), []); // no width (display: none)
    assert.deepEqual(m.holesFor([box(-80, 100, 0, 140)], 1280, 800), []); // left of the window
    assert.deepEqual(m.holesFor([box(100, 900, 140, 940)], 1280, 800), []); // below it
    assert.deepEqual(m.holesFor([box(100, 100, 140, 140)], 0, 0), []); // the dialog not laid out yet
  });
  test("one off screen and one on: only the one on screen", () => {
    assert.deepEqual(m.holesFor([box(100, 900, 140, 940), box(100, 100, 140, 140)], 1280, 800), [box(94, 94, 146, 146)]);
  });
  test("round around a round control, slightly rounded around the rest", () => {
    assert.equal(m.holeRadius(box(0, 0, 52, 52), true), 26);
    assert.equal(m.holeRadius(box(0, 0, 244, 56), true), 28);
    assert.equal(m.holeRadius(box(0, 0, 92, 68), false), 14);
    assert.equal(m.holeRadius(box(0, 0, 20, 20), false), 10);
  });
  test("a click inside any of them, edges included, goes on to the control", () => {
    const holes = [box(94, 94, 146, 146), box(400, 20, 500, 60)];
    assert.equal(m.insideHole(holes, 120, 120), true);
    assert.equal(m.insideHole(holes, 94, 146), true);
    assert.equal(m.insideHole(holes, 450, 40), true);
    assert.equal(m.insideHole(holes, 93, 120), false);
    assert.equal(m.insideHole(holes, 300, 40), false);
    assert.equal(m.insideHole([], 120, 120), false);
  });
});

describe("the dimmed layer's outline", () => {
  test("no hole: the whole window", () => {
    assert.equal(m.dimPath(1280, 800, []), "M0 0H1280V800H0Z");
  });
  test("holes: the window, then each hole (evenodd leaves them clear)", () => {
    const a = { box: box(94, 94, 146, 146), radius: 14 };
    const b = { box: box(400, 20, 500, 60), radius: 0 };
    const d = m.dimPath(1280, 800, [a, b]);
    assert.equal(d, "M0 0H1280V800H0Z" + m.roundedBoxPath(a.box, 14) + m.roundedBoxPath(b.box, 0));
    assert.equal(d.split("Z").length - 1, 3);
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

describe("where the card goes (600px and wider)", () => {
  const card = { width: 420, height: 460 };
  const view = { width: 1280, height: 800 };
  const cardBox = (p) => box(p.left, p.top, p.left + card.width, p.top + card.height);
  const inside = (p, v = view) => p.left >= 16 && p.top >= 16 && p.left + card.width <= v.width - 16 && p.top + card.height <= v.height - 16;
  test("nothing ringed: in the middle", () => {
    assert.deepEqual(m.placeCard([], card, view), { left: 430, top: 170, maxHeight: null });
  });
  test("the menu on the left: beside it, to the right, level with its middle", () => {
    const menu = box(2, 58, 262, 798);
    const p = m.placeCard([menu], card, view);
    assert.equal(p.left, 262 + m.cCardGap);
    assert.ok(inside(p));
    assert.equal(m.boxesOverlap(cardBox(p), menu), false);
  });
  test("the search in the top bar: below it", () => {
    const search = box(330, 6, 950, 58);
    const p = m.placeCard([search], card, view);
    assert.equal(p.top, 58 + m.cCardGap);
    assert.ok(inside(p));
  });
  test("the dates at the right: to their left", () => {
    const scrubber = box(1200, 70, 1266, 794);
    const p = m.placeCard([scrubber], card, view);
    assert.equal(p.left + card.width, 1200 - m.cCardGap);
    assert.ok(inside(p));
  });
  test("several controls: beside all of them, covering none", () => {
    const holes = [box(300, 90, 900, 180), box(300, 200, 900, 300)];
    const p = m.placeCard(holes, card, view);
    assert.ok(inside(p));
    for (const h of holes) assert.equal(m.boxesOverlap(cardBox(p), h), false);
  });
  test("just short of room below them: cut to it (its text scrolls), still covering none", () => {
    // Friends: the address and the field to add one, across the page.
    const holes = [box(312, 114, 1224, 224), box(329, 285, 1207, 339)];
    const p = m.placeCard(holes, { width: 420, height: 448 }, view);
    assert.equal(p.top, 339 + m.cCardGap);
    assert.equal(p.maxHeight, 800 - 16 - (339 + m.cCardGap));
    assert.ok(p.maxHeight >= m.cCardMinHeight);
  });
  test("never cut below the least height: a corner instead", () => {
    const holes = [box(100, 100, 1180, 500)];
    const p = m.placeCard(holes, { width: 420, height: 448 }, view);
    assert.equal(p.maxHeight, null);
  });
  test("no side has the room: the corner or middle that covers the least", () => {
    const big = box(200, 100, 1100, 700);
    const p = m.placeCard([big], card, view);
    assert.ok(inside(p));
    const covered = (q) => {
      const b = cardBox(q);
      return Math.max(0, Math.min(b.right, big.right) - Math.max(b.left, big.left)) * Math.max(0, Math.min(b.bottom, big.bottom) - Math.max(b.top, big.top));
    };
    for (const q of [{ left: 16, top: 16 }, { left: 844, top: 324 }, { left: 430, top: 170 }]) assert.ok(covered(p) <= covered(q));
  });
  test("a window too small for it: kept at the margin, top left", () => {
    const p = m.placeCard([box(10, 10, 50, 50)], card, { width: 400, height: 300 });
    assert.deepEqual(p, { left: 16, top: 16, maxHeight: null });
  });
});

describe("scrolling a control into view", () => {
  test("already in the band: no scroll", () => {
    assert.equal(m.scrollToShow(box(0, 200, 100, 240), 76, 788), 0);
  });
  test("below the band (or under a phone's sheet): its middle to the band's middle", () => {
    // Band 76..388 (above a sheet at 400): a 40px control at 900.
    assert.equal(m.scrollToShow(box(0, 900, 100, 940), 76, 388), 900 - (76 + (312 - 40) / 2));
  });
  test("above it: scrolled back up by as much", () => {
    assert.equal(m.scrollToShow(box(0, -300, 100, -260), 76, 788), -300 - (76 + (712 - 40) / 2));
  });
  test("taller than the band: its top to the band's top", () => {
    assert.equal(m.scrollToShow(box(0, 500, 100, 1500), 76, 788), 424);
  });
  test("no band at all: nothing", () => {
    assert.equal(m.scrollToShow(box(0, 900, 100, 940), 400, 300), 0);
  });
});
