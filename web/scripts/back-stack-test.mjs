// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Tests for Back on the web (src/components/backStack.ts): which history
// entries the app writes for its sections and layers, what Back and
// Forward do then, and that a layer closed by its own button never leaves
// a Back that does nothing. Against an in-memory history that behaves as
// a browser's: pushState drops the entries ahead, history.state is a copy,
// and a traversal's popstate comes a moment later. Loaded through Vite's
// SSR loader in Node, as the other scripts' tests are:
//
//   node web/scripts/back-stack-test.mjs        (needs web/node_modules)
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const WEB = resolve(dirname(fileURLToPath(import.meta.url)), "..");

let server;
let m;
before(async () => {
  const { createServer } = await import(join(WEB, "node_modules/vite/dist/node/index.js"));
  server = await createServer({
    configFile: false,
    root: WEB,
    cacheDir: join(tmpdir(), "otc-back-stack-test-vite"),
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true, hmr: false, ws: false },
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  m = await server.ssrLoadModule("/src/components/backStack.ts");
});
after(() => server?.close());

// A tab's history: entries before the page's are another site's (left
// counts the times Back went to them).
class Browser {
  constructor(state = null, url = "https://pit.off-the.cloud/") {
    this.entries = [{ state: "another site", url: "https://example.org/", foreign: true }, { state, url }];
    this.index = 1;
    this.left = 0;
    this.listeners = { popstate: [], pageshow: [] };
    const browser = this;
    this.history = {
      get state() { return structuredClone(browser.entries[browser.index].state); },
      pushState(data, _unused, url) {
        browser.entries.splice(browser.index + 1);
        browser.entries.push({ state: structuredClone(data), url: url ?? browser.url });
        browser.index++;
      },
      replaceState(data, _unused, url) {
        browser.entries[browser.index] = { state: structuredClone(data), url: url ?? browser.url };
      },
      go(delta) { browser.traverse(delta); },
      scrollRestoration: "auto",
    };
    this.window = {
      history: this.history,
      addEventListener: (type, fn) => this.listeners[type].push(fn),
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (t) => clearTimeout(t),
    };
  }
  get url() { return this.entries[this.index].url; }
  get here() { return this.entries[this.index].state?.otc; }
  // A traversal, as the browser runs it: its popstate a task later.
  traverse(delta) {
    setTimeout(() => {
      const to = this.index + delta;
      if (to < 0 || to >= this.entries.length) return;
      if (this.entries[to].foreign) { this.left++; this.index = to; return; }
      this.index = to;
      const state = structuredClone(this.entries[to].state);
      this.listeners.popstate.forEach((fn) => fn({ state }));
    }, 1);
  }
  back() { this.traverse(-1); }
  forward() { this.traverse(1); }
  // How many of the page's entries there are, the one it is on counted.
  get depth() { return this.index; }
}

const settle = (ms = 25) => new Promise((r) => setTimeout(r, ms));

// The app: a section shown, a stack, and layers with close() counters.
function app(browser, firstTab = "PhotoGallery", { doc = "doc1", show = (t) => t } = {}) {
  const stack = m.createBackStack(browser.window, doc, 200);
  const a = { stack, tab: firstTab, shown: [] };
  stack.follow((t) => { const s = show(t); a.tab = s; a.shown.push(s); return s; });
  stack.section(firstTab, false);
  a.go = (t) => { a.tab = t; stack.section(t, true); };
  a.open = (name, { busy = () => false } = {}) => {
    const layer = { name, closed: 0, open: true };
    layer.release = stack.layer(() => {
      if (busy()) return false;
      layer.closed++;
      layer.open = false;
      return true;
    });
    // Closed by its own button (×, Esc): it releases its entry.
    layer.shut = () => { layer.open = false; layer.release(); };
    return layer;
  };
  return a;
}

describe("sections", () => {
  test("each move is an entry; Back shows the one before, then leaves", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("AdminPannel");
    await settle();
    a.go("Social");
    await settle();
    assert.equal(b.depth, 3);
    assert.equal(b.here.tab, "Social");
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["AdminPannel"]);
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["AdminPannel", "PhotoGallery"]);
    b.back();
    await settle();
    assert.equal(b.left, 1);
  });

  test("the address is never changed", async () => {
    const b = new Browser(null, "https://pit.off-the.cloud/?download=x#frag");
    const a = app(b);
    a.go("Social");
    a.open("viewer");
    await settle();
    assert.ok(b.entries.slice(1).every((e) => e.url === "https://pit.off-the.cloud/?download=x#frag"));
  });

  test("a correction rewrites the entry it is on, no new one", async () => {
    const b = new Browser();
    const a = app(b, "Profile");
    a.stack.section("Social", false);
    await settle();
    assert.equal(b.depth, 1);
    assert.equal(b.here.tab, "Social");
  });

  test("a correction right after a move goes to the move's entry", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("People");
    a.stack.section("PhotoGallery", false); // face recognition is off
    await settle();
    // The move was to People, corrected to Images: the first entry keeps
    // Images too, and Back from the second changes nothing, so it goes on.
    assert.equal(b.entries[1].state.otc.tab, "PhotoGallery");
    assert.equal(b.here.tab, "PhotoGallery");
  });

  test("Forward shows the section again", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("Friends");
    await settle();
    b.back();
    await settle();
    b.forward();
    await settle();
    assert.deepEqual(a.shown, ["PhotoGallery", "Friends"]);
  });

  test("a section shown instead (signed out) that is the one showing: Back goes on", async () => {
    const b = new Browser();
    // Signed out, every section that needs a session shows Profile.
    const a = app(b, "PhotoGallery", { show: (t) => (t === "Social" ? t : "Profile") });
    a.go("AdminPannel");
    await settle();
    a.go("Social");
    await settle();
    a.stack.section("Profile", false); // the session ended
    await settle();
    b.back(); // to Files, shown as Profile: nothing changed, so on to Images, also Profile, so out
    await settle(60);
    assert.equal(b.left, 1);
  });
});

describe("layers", () => {
  test("Back closes the top layer, then the one under it, then the section", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("AdminPannel");
    await settle();
    const viewer = a.open("viewer");
    await settle();
    const dialog = a.open("dialog");
    await settle();
    assert.equal(b.depth, 4);
    b.back();
    await settle();
    assert.equal(dialog.closed, 1);
    assert.equal(viewer.closed, 0);
    b.back();
    await settle();
    assert.equal(viewer.closed, 1);
    assert.deepEqual(a.shown, []);
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["PhotoGallery"]);
  });

  test("a layer closed by its own button takes its entry with it", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("Social");
    await settle();
    const viewer = a.open("viewer");
    await settle();
    viewer.shut();
    await settle();
    assert.equal(b.depth, 2);
    assert.equal(b.here.tab, "Social");
    assert.equal(b.here.layer, undefined);
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["PhotoGallery"]);
  });

  test("a pick in the drawer is one entry, the section's", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    const drawer = a.open("drawer");
    await settle();
    // The same click: the section changes and the drawer closes.
    a.go("Friends");
    drawer.shut();
    await settle();
    assert.equal(b.depth, 2);
    assert.equal(b.here.tab, "Friends");
    assert.equal(b.here.layer, undefined);
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["PhotoGallery"]);
  });

  test("a dialog that closes as another opens: one entry, the new one's", async () => {
    const b = new Browser();
    const a = app(b);
    const confirm = a.open("confirm");
    await settle();
    confirm.shut();
    const link = a.open("link");
    await settle();
    assert.equal(b.depth, 2);
    b.back();
    await settle();
    assert.equal(link.closed, 1);
    b.back();
    await settle();
    assert.equal(b.left, 1);
  });

  test("a layer at work stays, and Back closes it once it can", async () => {
    const b = new Browser();
    const a = app(b);
    let busy = true;
    const dialog = a.open("dialog", { busy: () => busy });
    await settle();
    b.back();
    await settle();
    assert.equal(dialog.closed, 0);
    assert.equal(b.depth, 2); // its entry again
    busy = false;
    b.back();
    await settle();
    assert.equal(dialog.closed, 1);
    assert.equal(b.depth, 1);
  });

  test("a layer under another closing first: Back closes the top and skips its entry", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("Social");
    await settle();
    const lower = a.open("lower");
    await settle();
    const upper = a.open("upper");
    await settle();
    lower.shut();
    await settle();
    assert.equal(b.depth, 4); // the lower one's entry waits under the upper
    b.back();
    await settle(60);
    assert.equal(upper.closed, 1);
    assert.equal(b.depth, 2);
    assert.equal(b.here.tab, "Social");
    b.back();
    await settle();
    assert.deepEqual(a.shown, ["PhotoGallery"]);
  });

  test("two layers closing together leave both entries", async () => {
    const b = new Browser();
    const a = app(b);
    const viewer = a.open("viewer");
    await settle();
    const dialog = a.open("dialog");
    await settle();
    dialog.shut();
    viewer.shut();
    await settle(60);
    assert.equal(b.depth, 1);
  });

  test("Forward onto a closed layer's entry steps back off it", async () => {
    const b = new Browser();
    const a = app(b);
    const viewer = a.open("viewer");
    await settle();
    viewer.shut();
    await settle();
    assert.equal(b.depth, 1);
    b.forward();
    await settle(60);
    assert.equal(b.depth, 1);
    assert.equal(viewer.closed, 0);
  });

  test("a layer opened and closed at once writes nothing", async () => {
    const b = new Browser();
    const a = app(b);
    a.open("flash").shut();
    await settle();
    assert.equal(b.depth, 1);
    assert.equal(b.entries.length, 2);
  });

  test("Back over several entries at once closes every layer above", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    const viewer = a.open("viewer");
    await settle();
    const dialog = a.open("dialog");
    await settle();
    b.traverse(-2);
    await settle();
    assert.equal(dialog.closed, 1);
    assert.equal(viewer.closed, 1);
    assert.equal(b.depth, 1);
  });
});

describe("reloads and foreign entries", () => {
  test("a reload with a layer's entry: the entry becomes its section's, and Back over the same section goes on", async () => {
    const b = new Browser();
    const a = app(b, "PhotoGallery");
    a.go("AdminPannel");
    await settle();
    a.open("viewer");
    await settle();
    // The page loads again on the viewer's entry, with another doc.
    const again = app(b, "AdminPannel", { doc: "doc2" });
    await settle();
    assert.equal(b.here.layer, undefined);
    assert.equal(b.here.tab, "AdminPannel");
    b.back(); // to Files' own entry - what shows already - so on to Images
    await settle(60);
    assert.deepEqual(again.shown, ["PhotoGallery"]);
  });

  test("an entry the app didn't write (a fragment) is taken as one on top", async () => {
    const b = new Browser();
    const a = app(b, "Social");
    const viewer = a.open("viewer");
    await settle();
    // A #download link pasted into the address bar: a new entry, no state.
    b.entries.splice(b.index + 1);
    b.entries.push({ state: null, url: "https://pit.off-the.cloud/#download=a_b" });
    b.index++;
    b.listeners.popstate.forEach((fn) => fn({ state: null }));
    await settle();
    assert.equal(b.here.i, 2);
    assert.equal(b.here.tab, "Social");
    b.back(); // onto the viewer's entry: nothing to close there, so on to the section's
    await settle(60);
    assert.equal(viewer.closed, 1);
    assert.equal(b.depth, 1);
  });

  test("another script's state in an entry stays when the stack rewrites it", async () => {
    const b = new Browser({ usr: null, key: "k1", idx: 0 });
    const a = app(b);
    a.stack.section("Social", false);
    assert.equal(b.entries[1].state.key, "k1");
    a.stack.replaceAddress("https://pit.off-the.cloud/?x=1");
    assert.equal(b.url, "https://pit.off-the.cloud/?x=1");
    assert.equal(b.here.tab, "Social");
  });

  test("history settles even when a step never arrives", async () => {
    const b = new Browser();
    const a = app(b);
    const viewer = a.open("viewer");
    await settle();
    b.traverse = () => {}; // a browser that ignores history.go
    viewer.shut();
    await settle(260);
    assert.equal(b.here.layer, undefined); // the dead entry now shows the page
    a.open("next");
    await settle();
    assert.equal(b.depth, 3);
  });
});
