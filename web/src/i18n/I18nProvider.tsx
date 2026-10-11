// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { I18nContext } from "./context";
import { i18nSnapshot, subscribeI18n } from "./runtime";
// Sends this browser's changes to the device (SetLanguage).
import "./deviceSync";

// How long the first paint waits for a language other than English to load
// before showing English: it switches as soon as its texts arrive.
const cFirstPaintWaitMs = 3000;

/**
 * The root of the app (main.tsx): every component under it follows the
 * language as it changes, without a reload.
 */
export default function I18nProvider({ children }: { children: ReactNode }) {
  const snapshot = useSyncExternalStore(subscribeI18n, i18nSnapshot);
  const [waited, setWaited] = useState(false);
  useEffect(() => {
    if (snapshot.ready) return;
    const timer = window.setTimeout(() => setWaited(true), cFirstPaintWaitMs);
    return () => window.clearTimeout(timer);
  }, [snapshot.ready]);
  // A page whose language isn't English yet would flash English first.
  if (!snapshot.ready && !waited) return null;
  return <I18nContext.Provider value={snapshot}>{children}</I18nContext.Provider>;
}
