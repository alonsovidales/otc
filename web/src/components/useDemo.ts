// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Whether this page's device is a demo device (net/demo.ts): what would
// change something is hidden while it is.
import { useSyncExternalStore } from "react";
import { isDemo, subscribeDemo } from "../net/demo";

export function useDemo(): boolean {
  return useSyncExternalStore(subscribeDemo, isDemo);
}
