// SPDX-License-Identifier: AGPL-3.0-or-later

import { useContext, useSyncExternalStore } from "react";
import { languageChoice, type ChoiceSnapshot } from "./choice";
import { I18nContext } from "./context";
import type { I18nSnapshot, TFunction } from "./runtime";

/**
 * t() in the language on screen; the component re-renders when it changes.
 * t.lang is what the formatters take: fmtDate(t.lang, when).
 */
export function useT(): TFunction {
  return useContext(I18nContext).t;
}

/** The language on screen: its code, tag and formatting locale, and t. */
export function useI18n(): I18nSnapshot {
  return useContext(I18nContext);
}

/** The stored choice and what it resolves to (the Language panel). */
export function useLanguage(): ChoiceSnapshot {
  return useSyncExternalStore(languageChoice.subscribe, languageChoice.snapshot);
}
