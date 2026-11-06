// SPDX-License-Identifier: AGPL-3.0-or-later

// Screen mode's layer (ScreenLayer.tsx), mounted by the entries next to
// the page: a chunk of its own, loaded only on the device's own screen
// (screenMode.ts), and never taking the page down with it - a chunk that
// didn't come leaves the page as it is (the kiosk's browser blocks
// downloads and file choosers by itself, and a reload brings it back).
import { Component, lazy, Suspense } from "react";
import type { ReactNode } from "react";
import { installScreenGuards, screenMode } from "./screenMode";

const ScreenLayer = lazy(() => import("./ScreenLayer"));

// Before the page's own first click: links, window.open, file choosers.
if (screenMode()) installScreenGuards();

class Quiet extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(err: unknown) {
    console.error("Screen mode's keyboard didn't load:", err);
  }

  render() {
    return this.state.failed ? null : this.props.children;
  }
}

/** The keyboard and the rest of screen mode, or nothing off it. */
export function ScreenPart() {
  if (!screenMode()) return null;
  return (
    <Quiet>
      <Suspense fallback={null}>
        <ScreenLayer />
      </Suspense>
    </Quiet>
  );
}
