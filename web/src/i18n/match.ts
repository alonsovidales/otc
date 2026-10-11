// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Matching languages (docs/i18n.md, "Languages"): pure functions over a
// language table, so they can be checked with any table (scripts/i18n-test.mjs)
// and not only the one this build ships.

import type { Language, LanguageCode } from "./messages";

// What the device stores and the wire carries: "" (Automatic) or a code.
const cCodeRe = /^[a-z]{2,3}$/;

/** Whether s is a language code as the device stores it (not ""). */
export function isLanguageCode(s: unknown): s is string {
  return typeof s === "string" && cCodeRe.test(s);
}

/** The base language of a BCP 47 tag: "pt-BR" -> "pt", "es_MX" -> "es". */
export function baseLanguage(tag: string): string {
  return tag.split(/[-_]/, 1)[0].toLowerCase();
}

// The languages a system language can resolve to: never the pseudo-locale,
// whose tag (en-XA) has another base than its code (qps).
function matchable(languages: readonly Language[]): readonly Language[] {
  return languages.filter((l) => baseLanguage(l.tag) === l.code);
}

/**
 * The first of the system's languages, in order, whose base language this
 * build ships: es-MX -> es, pt-BR -> pt, [ca-ES, es-ES] -> es. null when
 * none is (the caller falls back to English).
 */
export function matchLanguage(preferred: readonly string[], languages: readonly Language[]): LanguageCode | null {
  const candidates = matchable(languages);
  for (const tag of preferred) {
    if (typeof tag !== "string" || tag === "") continue;
    const base = baseLanguage(tag);
    const hit = candidates.find((l) => l.code === base);
    if (hit) return hit.code;
  }
  return null;
}

/**
 * The locale formatters use for a language (docs/i18n.md, "Formatting in
 * Automatic"): the system's own full locale when its base language is this
 * one (en-GB keeps day/month order, es-MX its separators), else the
 * language's canonical tag. Plural rules never use this: always the tag.
 */
export function formatLocale(language: Language, preferred: readonly string[]): string {
  const base = baseLanguage(language.tag);
  for (const tag of preferred) {
    if (typeof tag !== "string" || tag === "" || baseLanguage(tag) !== base) continue;
    try {
      // A tag the browser reports but Intl can't parse is skipped.
      return Intl.getCanonicalLocales(tag)[0] ?? language.tag;
    } catch {
      continue;
    }
  }
  return language.tag;
}
