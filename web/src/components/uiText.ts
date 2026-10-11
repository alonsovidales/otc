// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Helpers for the web app's text (docs/i18n.md, "Each platform").
//
// UiText: a text kept for later - in state, in a result handed back, in a
// note shown until the next try - is kept as a function that makes it in
// the language on screen when it is shown, never as the text itself, which
// would stay in the language it was made in (the apps' view models hold
// UiText the same way). The device's own words (an error reply's message,
// rendered in the language the request carried) are kept as they are.
//
//   const [note, setNote] = useState<UiText | null>(null);
//   setNote(() => say("web.x.saved"));              // one of ours
//   setNote(() => (t) => t("web.x.named", { name })); // one with arguments
//   setNote(() => asIs(resp.errorMessage));         // the device's words
//   {note && <p>{note(t)}</p>}                      // t from useT()
//
// (setState takes a function as an update, hence the extra () =>.)
//
// plainNumber/percentText: a number the page used to write by hand
// (toFixed, `${n}%`) is formatted for the language itself - its canonical
// tag, t.tag - rather than the browser's region, so English reads exactly
// as before in every browser ("4.6 GB" in an en-NL browser too, "Sep" in
// an en-GB one), and another language gets its own separators ("4,6 GB",
// "45 %"). A number inside a text goes in the key as an int or count
// argument instead: t() formats it.

import type { ArgsOf, MessageKey, TFunction } from "../i18n";

/** A text made when it is shown, in the language on screen then. */
export type UiText = (t: TFunction) => string;

/** The keys that take no arguments. */
export type PlainKey = { [K in MessageKey]: ArgsOf<K> extends [] ? K : never }[MessageKey];

/** One of our texts without arguments, as a UiText. */
export const say = (key: PlainKey): UiText => (t) => t(key);

/** Words that come already written (the device's or the relay's), shown as they are. */
export const asIs = (text: string): UiText => () => text;

const numberFormats = new Map<string, Intl.NumberFormat>();

/** A number in the language's own format (t.tag): plainNumber(t, 4.6, { maximumFractionDigits: 1 }). */
export function plainNumber(t: TFunction, value: number, options: Intl.NumberFormatOptions = {}): string {
  const key = `${t.tag}|${JSON.stringify(options)}`;
  let f = numberFormats.get(key);
  if (!f) {
    try {
      f = new Intl.NumberFormat(t.tag, options);
    } catch {
      // A tag Intl refuses: English's.
      f = new Intl.NumberFormat("en", options);
    }
    numberFormats.set(key, f);
  }
  // -0 (Math.round(-0.4)) reads "0", as it did in a template string.
  return f.format(value === 0 ? 0 : value);
}

/** A percentage, from 0 to 100: "45%" in English, "45 %" in French. */
export function percentText(t: TFunction, value: number, maxFractionDigits = 0): string {
  return plainNumber(t, value / 100, { style: "percent", maximumFractionDigits: maxFractionDigits });
}

/** A size with its unit, as the page wrote it by hand: "4.6 GB" (digits after the point, no grouping). */
export function unitText(t: TFunction, value: number, unit: "megabyte" | "gigabyte", digits: number): string {
  return plainNumber(t, value, {
    style: "unit",
    unit,
    unitDisplay: "short",
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
    useGrouping: false,
  });
}
