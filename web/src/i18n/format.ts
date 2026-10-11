// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Numbers, sizes, dates and lists in a language, through Intl. Each takes
// the language's code - t.lang from useT(), so the component re-renders
// when it changes - and formats with the browser's own locale when its base
// language is that one (en-GB dates for an English browser in Britain), else
// with the language's canonical tag (match.ts formatLocale).

import { browserLanguages } from "./choice";
import { formatLocale } from "./match";
import { languages, sourceLanguage } from "./messages";
import { numberFormat } from "./runtime";

function localeOf(lang: string): string {
  const language = languages.find((l) => l.code === lang) ?? languages.find((l) => l.code === sourceLanguage)!;
  return formatLocale(language, browserLanguages());
}

// Intl objects by locale and options.
const cache = new Map<string, unknown>();
function cached<T>(kind: string, locale: string, options: object | undefined, make: (locale: string) => T): T {
  const key = `${kind}|${locale}|${options ? JSON.stringify(options) : ""}`;
  let v = cache.get(key) as T | undefined;
  if (v === undefined) {
    try {
      v = make(locale);
    } catch {
      // A locale Intl refuses: the source language's formats.
      v = make(sourceLanguage);
    }
    cache.set(key, v);
  }
  return v;
}

/** A number: fmtNumber(t.lang, 1234.5) -> "1.234,5" in German. */
export function fmtNumber(lang: string, value: number | bigint, options?: Intl.NumberFormatOptions): string {
  const locale = localeOf(lang);
  if (!options) return numberFormat(locale).format(value);
  return cached("n", locale, options, (l) => new Intl.NumberFormat(l, options)).format(value);
}

const cByteUnits = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte", "petabyte"] as const;

export interface BytesOptions {
  /** 1024 (the default, like the apps' file sizes) or 1000. */
  base?: 1000 | 1024;
  /** Most digits after the decimal separator (1). */
  fractionDigits?: number;
}

/** A size in bytes: fmtBytes(t.lang, 4_831_838_208) -> "4.5 GB", "4,5 Go" in French. */
export function fmtBytes(lang: string, bytes: number | bigint, options: BytesOptions = {}): string {
  const base = options.base ?? 1024;
  let n = Math.max(0, Number(bytes));
  let unit = 0;
  while (n >= base && unit < cByteUnits.length - 1) {
    n /= base;
    unit++;
  }
  const opts: Intl.NumberFormatOptions = {
    style: "unit",
    unit: cByteUnits[unit],
    // "512 bytes": the short form of a plain byte doesn't take a plural.
    unitDisplay: unit === 0 ? "long" : "short",
    maximumFractionDigits: unit === 0 ? 0 : options.fractionDigits ?? 1,
  };
  return cached("b", localeOf(lang), opts, (l) => new Intl.NumberFormat(l, opts)).format(n);
}

/** A date or time: fmtDate(t.lang, d, { dateStyle: "medium" }) (the default). */
export function fmtDate(lang: string, value: Date | number, options: Intl.DateTimeFormatOptions = { dateStyle: "medium" }): string {
  return cached("d", localeOf(lang), options, (l) => new Intl.DateTimeFormat(l, options)).format(value);
}

const cRelativeSteps: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

export interface RelativeOptions {
  /** "long" (the default: "5 minutes ago"), "short" or "narrow" ("5m ago"). */
  style?: Intl.RelativeTimeFormatStyle;
  /** The moment it is relative to (now). */
  now?: Date | number;
}

/**
 * How long ago (or from now) a moment is, in its largest whole unit: "now",
 * "5 minutes ago", "yesterday", "in 2 weeks".
 */
export function fmtRelative(lang: string, value: Date | number, options: RelativeOptions = {}): string {
  const opts: Intl.RelativeTimeFormatOptions = { numeric: "auto", style: options.style ?? "long" };
  const rtf = cached("r", localeOf(lang), opts, (l) => new Intl.RelativeTimeFormat(l, opts));
  const seconds = (Number(value) - Number(options.now ?? Date.now())) / 1000;
  const abs = Math.abs(seconds);
  for (const [unit, size] of cRelativeSteps) {
    if (abs >= size) return rtf.format(Math.sign(seconds) * Math.floor(abs / size), unit);
  }
  return rtf.format(0, "second");
}

/** A list: fmtList(t.lang, ["a", "b", "c"]) -> "a, b and c" ("a, b y c" in Spanish). */
export function fmtList(lang: string, items: readonly string[], type: Intl.ListFormatType = "conjunction"): string {
  const opts: Intl.ListFormatOptions = { type, style: "long" };
  return cached("l", localeOf(lang), opts, (l) => new Intl.ListFormat(l, opts)).format(items);
}

/**
 * A month's name on its own (January = 0): the form a heading or a picker
 * uses, which some languages write differently inside a date. Capitalised
 * as the language writes it ("enero" in Spanish).
 */
export function monthName(lang: string, month: number, width: "long" | "short" | "narrow" = "long"): string {
  const opts: Intl.DateTimeFormatOptions = { month: width, timeZone: "UTC" };
  const fmt = cached("m", localeOf(lang), opts, (l) => new Intl.DateTimeFormat(l, opts));
  return fmt.format(Date.UTC(2000, ((month % 12) + 12) % 12, 15));
}
