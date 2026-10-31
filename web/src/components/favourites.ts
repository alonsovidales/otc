// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Favourites in the viewer (MediaViewer's heart; owner, 2026-10-31): what
// the heart shows for each path, kept for the page's session so stepping
// to a photo already asked about shows its heart at once. The viewer asks
// GetFavourites for the photo on screen and its neighbours in one request
// (ask); a press shows the new state at once and sends SetFavourite
// (toggle), and a refusal puts it back. useFavourites.ts makes the page's
// one on the socket.
//
// - A path's answer is only known once the device gave it: until then
//   get() is undefined and the viewer draws no heart, never an empty one
//   that then fills.
// - Membership is by photo: every copy of a favourite is one. A press is
//   shown on every path known to hold the same content (by the hash the
//   viewer had for it).
// - One SetFavourite at a time per path: a press while one is out waits
//   for its answer, and then only what the heart shows by then goes, if
//   the device doesn't hold it already - so two quick presses can't reach
//   the device in the wrong order. A refused change goes back to the
//   device's last word, unless the heart was pressed again meanwhile.
// - An answer to a question asked before a press never undoes the press.
// - stale(): what is kept may have changed elsewhere (another app, another
//   tab): it still shows, and is asked again the next time it is wanted.
//   forget(): it may be wrong (Add to or Remove from collection on
//   Favourites here): nothing is shown until asked again.
// - A device that answers unknown_payload has no Favourites: no heart for
//   the page's life (the web always has one that does - the device serves
//   it - but the phones' rule costs nothing).
//
// No imports but types, so `node web/scripts/favourites-test.mjs` loads it
// alone.

import type { ReqEnvelope, RespEnvelope } from "../proto/messages";

/** An item as the viewer has it: its path, and its content hash if known. */
export type FavouriteItem = { path: string; hash?: string };

type Payload = Extract<NonNullable<ReqEnvelope["payload"]>, { $case: "reqGetFavourites" | "reqSetFavourite" }>;
/** Sends one request and gives the device's answer; throws without one. */
export type FavouritesSend = (payload: Payload) => Promise<Pick<RespEnvelope, "errorCode" | "errorMessage" | "payload">>;

/** How asking went: "failed" is to be asked again later (no answer, an
 *  error reply); "unsupported" means the device has no Favourites. */
export type AskOutcome = "ok" | "failed" | "unsupported";

/** How a press ended: null when the device holds what the heart shows (or
 *  nothing had to be sent), else the device's words - "" when it gave none
 *  or didn't answer - and the heart is back as it was. */
export type PressOutcome = string | null;

export type Favourites = {
  /** Whether the photo at this path is a favourite; undefined until known. */
  get(path: string): boolean | undefined;
  /** Known to be out of Favourites since the last stale(): its heart was
   *  emptied, or the device said so since. */
  knownOut(path: string): boolean;
  /** The device has no Favourites (unknown_payload). */
  unsupported(): boolean;
  /** Asks, in one request, about the items whose answer isn't known (or
   *  was kept from before the last stale()), and waits for those already
   *  asked about. */
  ask(items: readonly FavouriteItem[]): Promise<AskOutcome>;
  /** The heart pressed for this item: shown at once, then sent. */
  toggle(item: FavouriteItem): Promise<PressOutcome>;
  /** Kept answers may be old: shown, and asked again when next wanted. */
  stale(): void;
  /** Kept answers may be wrong: forgotten, and any answer on its way. */
  forget(): void;
  subscribe(listener: () => void): () => void;
};

type Entry = {
  on: boolean;
  // Known since the last stale(): not to be asked again.
  fresh: boolean;
};

export function createFavourites(send: FavouritesSend): Favourites {
  // What the heart shows, by path.
  const shown = new Map<string, Entry>();
  // The device's last word, by path: what a refused change goes back to.
  const confirmed = new Map<string, boolean>();
  // The content hash the viewer had for a path, for its copies.
  const hashes = new Map<string, string>();
  // Presses so far, by path: an answer asked for before one is dropped.
  const presses = new Map<string, number>();
  // A GetFavourites under way, by each path it asks about.
  const asking = new Map<string, Promise<AskOutcome>>();
  // A path's SetFavourites, one at a time, until what it shows is sent.
  const sending = new Map<string, Promise<PressOutcome>>();
  // forget() starts a new one: what was on its way is dropped.
  let generation = 0;
  let noFavourites = false;
  const listeners = new Set<() => void>();
  const emit = () => listeners.forEach((l) => l());

  const note = (items: readonly FavouriteItem[]) => {
    for (const it of items) if (it.path && it.hash) hashes.set(it.path, it.hash);
  };
  // The path and every other one known to hold the same photo.
  const copiesOf = (path: string) => {
    const hash = hashes.get(path);
    const all = [path];
    if (hash) for (const [p, h] of hashes) if (h === hash && p !== path) all.push(p);
    return all;
  };
  const show = (path: string, on: boolean) => {
    for (const p of copiesOf(path)) {
      shown.set(p, { on, fresh: true });
      presses.set(p, (presses.get(p) ?? 0) + 1);
    }
  };

  const ask = async (items: readonly FavouriteItem[]): Promise<AskOutcome> => {
    if (noFavourites) return "unsupported";
    note(items);
    const waits: Promise<AskOutcome>[] = [];
    const paths: string[] = [];
    for (const { path } of items) {
      if (!path || paths.includes(path)) continue;
      const out = asking.get(path);
      if (out) { waits.push(out); continue; }
      // Its press decides; a question now could cross the change.
      if (sending.has(path) || shown.get(path)?.fresh) continue;
      paths.push(path);
    }
    if (paths.length) {
      const at = generation;
      const before = new Map(paths.map((p) => [p, presses.get(p) ?? 0]));
      const asked: Promise<AskOutcome> = (async (): Promise<AskOutcome> => {
        let reply;
        try {
          reply = await send({ $case: "reqGetFavourites", reqGetFavourites: { paths } });
        } catch {
          return "failed";
        }
        // Forgotten meanwhile: asked again.
        if (at !== generation) return "failed";
        if (reply.payload?.$case === "respFavourites") {
          const yes = new Set(reply.payload.respFavourites.paths);
          for (const p of paths) {
            if ((presses.get(p) ?? 0) !== before.get(p) || sending.has(p)) continue;
            shown.set(p, { on: yes.has(p), fresh: true });
            confirmed.set(p, yes.has(p));
          }
          emit();
          return "ok";
        }
        if (reply.errorCode === "unknown_payload") {
          noFavourites = true;
          emit();
          return "unsupported";
        }
        return "failed";
      })().finally(() => {
        for (const p of paths) if (asking.get(p) === asked) asking.delete(p);
      });
      for (const p of paths) asking.set(p, asked);
      waits.push(asked);
    }
    const outcomes = await Promise.all(waits);
    if (outcomes.includes("unsupported")) return "unsupported";
    return outcomes.includes("failed") ? "failed" : "ok";
  };

  // Sends what the path's heart shows until the device holds it. It leaves
  // `sending` in the step that decides it is done, so a press can't land
  // between that and its end and go unsent.
  const drain = (path: string): Promise<PressOutcome> => {
    const at = generation;
    const done = (out: PressOutcome) => {
      if (at === generation) sending.delete(path);
      return out;
    };
    const run = (async (): Promise<PressOutcome> => {
      // In `sending` before anything below runs.
      await undefined;
      for (;;) {
        const want = shown.get(path)?.on;
        if (at !== generation || want === undefined || want === confirmed.get(path)) return done(null);
        let said = "";
        try {
          const reply = await send({ $case: "reqSetFavourite", reqSetFavourite: { paths: [path], favourite: want } });
          if (at !== generation) return done(null);
          if (reply.payload?.$case === "respAck" && reply.payload.respAck.ok) {
            for (const p of copiesOf(path)) confirmed.set(p, want);
            continue;
          }
          if (reply.errorCode === "unknown_payload") noFavourites = true;
          said = reply.errorMessage || (reply.payload?.$case === "respAck" ? reply.payload.respAck.errorMsg : "") || "";
        } catch {
          // No answer: refused, without words.
          if (at !== generation) return done(null);
        }
        // Pressed again meanwhile: what it shows now decides.
        if (shown.get(path)?.on !== want) continue;
        const back = confirmed.get(path) ?? !want;
        for (const p of copiesOf(path)) shown.set(p, { on: back, fresh: true });
        emit();
        return done(said);
      }
    })();
    sending.set(path, run);
    return run;
  };

  return {
    get: (path) => shown.get(path)?.on,
    knownOut(path) {
      const e = shown.get(path);
      return !!e && e.fresh && !e.on;
    },
    unsupported: () => noFavourites,
    ask,
    toggle(item) {
      note([item]);
      const now = shown.get(item.path)?.on;
      if (now === undefined || noFavourites) return Promise.resolve(null);
      show(item.path, !now);
      emit();
      // One out already: it sends this too once answered.
      return sending.get(item.path) ?? drain(item.path);
    },
    stale() {
      for (const e of shown.values()) e.fresh = false;
    },
    forget() {
      generation++;
      shown.clear();
      confirmed.clear();
      hashes.clear();
      presses.clear();
      asking.clear();
      sending.clear();
      emit();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
  };
}
