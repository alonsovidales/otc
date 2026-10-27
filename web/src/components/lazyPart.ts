// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The parts of the app loaded only when first opened (F3 of the 2026-10
// review): Settings, People and Files, the composer and the viewer are
// chunks of their own, so a first load fetches the shell and what it
// shows. lazyPart is React.lazy whose failure says so: a PartFailed is a
// chunk that didn't come (no connection, or the device was updated since
// the page loaded and its new build has other chunks), which <Part>
// (Part.tsx), around each, shows as "This part of the app couldn't load"
// with Reload. React keeps a lazy part's failure: only a reload tries
// again.
import { lazy } from "react";
import type { ComponentType } from "react";

/** A part's chunk that didn't load. */
export class PartFailed extends Error {}

/** React.lazy for a part of the app: a failure to load is a PartFailed. */
export function lazyPart<P extends object>(load: () => Promise<{ default: ComponentType<P> }>) {
  return lazy(() => load().catch((err: unknown) => {
    console.error("A part of the app didn't load:", err);
    throw new PartFailed(err instanceof Error ? err.message : String(err));
  }));
}
