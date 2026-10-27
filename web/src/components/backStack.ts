// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Back on the web (F12 of the 2026-10 review). The app has one address, and
// it never wrote a history entry, so the browser's Back - a phone's back
// gesture, the button, Alt+Left - left it, even with a photo open. Now a
// section the menu (or a link in a page) moves to is an entry, and so is
// each layer that opens over the page: the viewer, a dialog, the composer,
// the menu's drawer, the tour. Back closes the top layer, or else shows
// the section before. This file is the only one that writes the history:
// a layer says it is open with useBackLayer, App says which section shows
// with backSection.
//
// Every entry keeps the page's address (a #download link, /shared's
// fragment, a query): only its state differs, { otc: { doc, i, tab, layer } }
// - its place (i, counted from where this tab's app started), the section
// it shows, and the layer it is for (doc: the page load that opened it; a
// reload's layers are gone). The rules:
//   - Back past a layer's entry closes the layer, the top one first. One
//     that can't close now (a dialog at work) stays, and gets an entry
//     again.
//   - Back onto an entry of another section shows that section.
//   - A layer that closed by itself (its ×, Esc, its backdrop) leaves its
//     entry: the next entry written at that moment takes its place (a pick
//     in the drawer is one entry, the section's), else history steps back
//     off it - so Back never does nothing.
//   - A Back that changes nothing (the entry of a layer closed while
//     another was over it, or one from before a reload) goes on back.
//   - Forward onto a section's entry shows it; onto a layer's, nothing
//     opens again and history steps back off it.
// Not in the phone apps' web view (__OTC_CONFIG), whose Back is theirs.
// Rules tested by node web/scripts/back-stack-test.mjs.
import { useEffect, useLayoutEffect, useRef } from "react";

/** An entry's own state, under `otc` in history.state. */
export interface BackEntry {
  doc: string;
  i: number;
  tab?: string;
  layer?: string;
}

/** What the stack needs of window.history. */
export interface BackHistory {
  readonly state: unknown;
  pushState(data: unknown, unused: string): void;
  replaceState(data: unknown, unused: string, url?: string): void;
  go(delta: number): void;
  scrollRestoration?: string;
}

/** What the stack needs of window. */
export interface BackWindow {
  readonly history: BackHistory;
  addEventListener(type: "popstate" | "pageshow", fn: (e: { state?: unknown; persisted?: boolean }) => void): void;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
}

// How long a step of ours (history.go) has to arrive before the stack goes
// on without it: from history's first entry there is nowhere to go.
const cStepWaitMs = 3000;

type Layer = { key: string; close: () => boolean | void; entry: number | null };

/** The stack's part of a history state; null for an entry it didn't write. */
export function backEntryOf(state: unknown): BackEntry | null {
  const otc = (state as { otc?: Partial<BackEntry> } | null | undefined)?.otc;
  return otc && typeof otc.i === "number" && typeof otc.doc === "string" ? (otc as BackEntry) : null;
}

export type BackStack = ReturnType<typeof createBackStack>;

/** The history of one page load (`doc` tells its layers from a reload's). */
export function createBackStack(win: BackWindow, doc: string, stepWaitMs = cStepWaitMs) {
  let started = false;
  // The entry history is on.
  let at = 0;
  // The section the page shows (App's); undefined where there are none (/shared).
  let tab: string | undefined;
  // Open layers, in the order they opened; entry null until it has one.
  const layers: Layer[] = [];
  // A section moved to, its entry not written yet.
  let pushTab = false;
  // A history.go of ours, until its popstate: tidy when it steps off a
  // dead entry (what the page shows stays), else a Back going on.
  let step: { tidy: boolean; timer: unknown } | null = null;
  let settleTimer: unknown = null;
  let nextKey = 0;
  let onSection: ((tab: string) => string) | null = null;

  const write = (push: boolean, e: Omit<BackEntry, "doc">) => {
    const cur = win.history.state;
    // Anything else in the entry's state stays (a replace); a new entry
    // has only ours.
    const keep = !push && cur && typeof cur === "object" ? cur : {};
    const data = { ...keep, otc: { doc, ...e } };
    if (push) win.history.pushState(data, "");
    else win.history.replaceState(data, "");
  };

  const start = () => {
    if (started) return;
    started = true;
    const cur = backEntryOf(win.history.state);
    at = cur?.i ?? 0;
    // The entry the page loaded on shows its section and no layer (a
    // reload with the viewer open left the viewer's entry).
    write(false, { i: at, tab: tab ?? cur?.tab });
    // Each section starts at its top (App), and a layer closing must not
    // scroll the page back to where it stood when it opened.
    try { if (win.history.scrollRestoration) win.history.scrollRestoration = "manual"; } catch { /* read-only */ }
    win.addEventListener("popstate", (e) => onPop(e.state));
    // Back from the page cache: a step that took the page away isn't coming.
    win.addEventListener("pageshow", (e) => {
      if (!e.persisted || !step) return;
      win.clearTimeout(step.timer);
      step = null;
      schedule();
    });
  };

  const schedule = () => {
    if (settleTimer === null) settleTimer = win.setTimeout(settle, 0);
  };

  const go = (delta: number, tidy: boolean) => {
    const timer = win.setTimeout(() => {
      step = null;
      // History didn't move. A dead entry it was to leave shows what the
      // page shows from now on, rather than being tried again and again.
      if (tidy) write(false, { i: at, tab });
      schedule();
    }, stepWaitMs);
    step = { tidy, timer };
    win.history.go(delta);
  };

  // The entry history is on belongs to a layer that is no longer open.
  const onDeadEntry = () => {
    const key = backEntryOf(win.history.state)?.layer;
    return !!key && !layers.some((l) => l.key === key && l.entry === at);
  };

  // Writes what waits for an entry - a section moved to, then each new
  // layer in the order they opened - the first one over a dead entry in its
  // place, and steps off a dead entry nothing took. Run once whatever
  // opened, closed or moved at the same moment has (a click closing the
  // drawer and changing the section is one entry).
  const settle = () => {
    settleTimer = null;
    if (step) return; // again after its popstate
    let dead = onDeadEntry();
    const put = (layer?: Layer) => {
      if (dead) write(false, { i: at, tab, layer: layer?.key });
      else write(true, { i: ++at, tab, layer: layer?.key });
      dead = false;
      if (layer) layer.entry = at;
    };
    if (pushTab) {
      pushTab = false;
      put();
    }
    for (const l of layers) if (l.entry === null) put(l);
    if (dead) go(-1, true);
  };

  const onPop = (state: unknown) => {
    const ours = step;
    if (step) {
      win.clearTimeout(step.timer);
      step = null;
    }
    const e = backEntryOf(state);
    if (!e) {
      // An entry the app didn't write (a #download link pasted into the
      // address bar): on top of the one it was on.
      at += 1;
      write(false, { i: at, tab });
      schedule();
      return;
    }
    const back = e.i < at;
    at = e.i;
    if (ours?.tidy) {
      // Stepped off a dead entry: this one shows what the page shows.
      write(false, { i: at, tab, layer: e.layer });
      schedule();
      return;
    }
    let changed = false;
    let kept = false;
    for (let k = layers.length - 1; k >= 0; k--) {
      const l = layers[k];
      if (l.entry === null || l.entry <= at) continue;
      if (l.close() === false) {
        l.entry = null;
        kept = true;
      } else {
        layers.splice(k, 1);
        changed = true;
      }
    }
    if (e.tab && e.tab !== tab && onSection) {
      // App may show another (a section that needs a session, signed
      // out): that one is what the entry says from now on.
      const shown = onSection(e.tab);
      if (shown !== tab) changed = true;
      tab = shown;
      if (shown !== e.tab) write(false, { i: at, tab, layer: e.layer });
    }
    if (back && !changed && !kept) {
      go(-1, false);
      return;
    }
    schedule();
  };

  return {
    /** A layer opened; close() is Back's (false: it stays). Returns its release. */
    layer(close: () => boolean | void): () => void {
      start();
      const key = `${doc}:${++nextKey}`;
      layers.push({ key, close, entry: null });
      schedule();
      return () => {
        const k = layers.findIndex((l) => l.key === key);
        if (k < 0) return; // Back closed it
        layers.splice(k, 1);
        schedule();
      };
    },
    /** The section shown: moved to (push, an entry of its own) or corrected
     *  (the entry history is on says it). */
    section(next: string, push: boolean) {
      start();
      if (next === tab) return;
      tab = next;
      if (push) {
        pushTab = true;
        schedule();
        return;
      }
      // The entry on its way says it, or the one a step lands on.
      if (pushTab || step) return;
      const cur = backEntryOf(win.history.state);
      write(false, { i: at, tab, layer: cur?.layer });
    },
    /** fn shows the section Back went to and returns the one it shows. */
    follow(fn: (tab: string) => string): () => void {
      onSection = fn;
      return () => { if (onSection === fn) onSection = null; };
    },
    /** The page's address changes, not its entry (BridgePanel drops a code). */
    replaceAddress(url: string) {
      win.history.replaceState(win.history.state, "", url);
    },
  };
}

// The page's stack: none in the phone apps' web view, nor outside a browser.
const pageStack: BackStack | null =
  typeof window !== "undefined" && window.history && !window.__OTC_CONFIG
    ? createBackStack(window as unknown as BackWindow, Math.random().toString(36).slice(2, 10))
    : null;

/** App: the section shown - push for a move to it, else a correction. */
export function backSection(tab: string, push: boolean) {
  pageStack?.section(tab, push);
}

/** App: fn shows the section Back went to and returns the one it shows. */
export function followBackSection(fn: (tab: string) => string): () => void {
  return pageStack?.follow(fn) ?? (() => {});
}

/** The address changes, the history entry stays (and keeps its state). */
export function replaceAddress(url: string) {
  if (pageStack) pageStack.replaceAddress(url);
  else window.history.replaceState(window.history.state, "", url);
}

/** While `open`, Back closes this layer with `close` (false: it stays). */
export function useBackLayer(open: boolean, close: () => boolean | void) {
  const closeRef = useRef(close);
  useLayoutEffect(() => { closeRef.current = close; });
  useEffect(() => {
    if (!open || !pageStack) return;
    return pageStack.layer(() => closeRef.current());
  }, [open]);
}
