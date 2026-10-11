// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The texts (docs/i18n.md, "Rendering contract"): English is in the main
// bundle, every other language is its own chunk, loaded when it is first
// shown. The language on screen switches only once its texts are there, so
// a change never shows keys or a mix; <html lang> follows it.
//
// A text is split into literal, placeholder and tag tokens first, and the
// arguments are inserted as plain text in one pass (a replacer function):
// an argument is never scanned again, for placeholders, "$&" or tags. Never
// render a text as markup: rich keys go through <Trans>, which renders each
// tag's span with a function from code.
//
// React-free: the provider (I18nProvider.tsx) subscribes to this store.

import { languageChoice } from "./choice";
import { formatLocale } from "./match";
import {
  languages,
  sourceLanguage,
  type ArgsOf,
  type Language,
  type LanguageCode,
  type MessageArgs,
  type MessageKey,
  type Messages,
  type PluralMessage,
  type RichKey,
} from "./messages";

type Entry = string | PluralMessage;
type Catalog = ReadonlyMap<string, Entry>;
type Args = Readonly<Record<string, unknown>>;

/** A lookup in the language on screen: t("common.save"), t("x.y", { count: 2 }). */
export interface TFunction {
  <K extends MessageKey>(key: K, ...args: ArgsOf<K>): string;
  /** The language's code, for the formatters (fmtDate(t.lang, ...)). */
  readonly lang: LanguageCode;
  /** Its BCP 47 tag (plural rules, <html lang>). */
  readonly tag: string;
  /** The locale formatters use (match.ts formatLocale). */
  readonly locale: string;
}

/** The language on screen; a new object whenever it changes. */
export interface I18nSnapshot {
  readonly lang: LanguageCode;
  readonly tag: string;
  readonly locale: string;
  readonly t: TFunction;
  /** False until the language chosen at load is on screen (or gave up). */
  readonly ready: boolean;
}

/** A piece of a rich text: plain, or the span of a tag. */
export interface RichSegment {
  readonly text: string;
  readonly tag?: string;
}

// The generated locale files (one per language and prefix, i18n/README.md).
const englishFiles = import.meta.glob<Messages>("./locales/en/*.json", { eager: true, import: "default" });
const otherFiles = import.meta.glob<Messages>(["./locales/*/*.json", "!./locales/en/*.json"], { import: "default" });

// Merged into a Map: a key is only ever looked up, never an object's own
// property, so nothing a file holds can reach a prototype. The draft
// marker ("//") and anything that isn't a text are left out.
function catalogOf(files: Messages[]): Catalog {
  const out = new Map<string, Entry>();
  for (const file of files) {
    for (const [key, value] of Object.entries(file)) {
      if (key === "//") continue;
      if (typeof value === "string") out.set(key, value);
      else if (value && typeof value.one === "string" && typeof value.other === "string" && typeof value.arg === "string") out.set(key, value);
    }
  }
  return out;
}

const filesIn = (code: string) => (path: string) => path.startsWith(`./locales/${code}/`);

const english: Catalog = catalogOf(Object.keys(englishFiles).sort().map((p) => englishFiles[p]));
const loaded = new Map<string, Catalog>([[sourceLanguage, english]]);
const loading = new Map<string, Promise<Catalog>>();

function load(code: string): Promise<Catalog> {
  const have = loaded.get(code);
  if (have) return Promise.resolve(have);
  let p = loading.get(code);
  if (!p) {
    const paths = Object.keys(otherFiles).filter(filesIn(code)).sort();
    p = Promise.all(paths.map((path) => otherFiles[path]()))
      .then((files) => {
        const catalog = catalogOf(files);
        loaded.set(code, catalog);
        return catalog;
      })
      .finally(() => loading.delete(code));
    loading.set(code, p);
  }
  return p;
}

// Intl objects are not free to make; one per locale.
const pluralRulesCache = new Map<string, Intl.PluralRules>();
const numberFormatCache = new Map<string, Intl.NumberFormat>();

function pluralRules(tag: string): Intl.PluralRules {
  let r = pluralRulesCache.get(tag);
  if (!r) {
    try {
      r = new Intl.PluralRules(tag);
    } catch {
      r = new Intl.PluralRules(sourceLanguage);
    }
    pluralRulesCache.set(tag, r);
  }
  return r;
}

/** A number as text in a locale (cached). */
export function numberFormat(locale: string): Intl.NumberFormat {
  let f = numberFormatCache.get(locale);
  if (!f) {
    try {
      f = new Intl.NumberFormat(locale);
    } catch {
      f = new Intl.NumberFormat(sourceLanguage);
    }
    numberFormatCache.set(locale, f);
  }
  return f;
}

const cPlaceholderRe = /\{([a-z][A-Za-z0-9]*)\}/g;
const cTagRe = /<(\/?)([a-z][a-z0-9]*)>/g;

// One pass over a literal: each placeholder becomes its argument as plain
// text (a number formatted for the locale). The replacer's return value is
// never interpreted, so "$&" stays "$&", and an argument holding "{name}"
// or "<b>" stays exactly that.
function substitute(text: string, args: Args | undefined, locale: string): string {
  if (!args) return text;
  return text.replace(cPlaceholderRe, (whole, name: string) => {
    if (!Object.prototype.hasOwnProperty.call(args, name)) return whole;
    const v = args[name];
    if (typeof v === "number" || typeof v === "bigint") return numberFormat(locale).format(v);
    return String(v ?? "");
  });
}

interface Lang {
  readonly code: LanguageCode;
  readonly tag: string;
  readonly locale: string;
  readonly catalog: Catalog;
}

// The template of a key in a language, its plural form picked: the
// language's own plural rules on its text, English rules on the English
// filled in for a key its file lacks (never the case for generated files).
function template(lang: Lang, key: string, args: Args | undefined): string {
  let entry = lang.catalog.get(key);
  let rulesTag = lang.tag;
  if (entry === undefined) {
    entry = english.get(key);
    rulesTag = sourceLanguage;
  }
  if (entry === undefined) {
    // The key only; never its arguments (docs/i18n.md: logs stay free of them).
    console.error("i18n: missing key", key);
    return key;
  }
  if (typeof entry === "string") return entry;
  const n = args?.[entry.arg];
  if (typeof n !== "number" && typeof n !== "bigint") return entry.other;
  return pluralRules(rulesTag).select(Number(n)) === "one" ? entry.one : entry.other;
}

/** A plain key's text, in lang. */
function translate(lang: Lang, key: string, args: Args | undefined): string {
  return substitute(template(lang, key, args), args, lang.locale);
}

/**
 * A rich key's text in pieces: the tags of the template are found first,
 * then each piece's placeholders are filled in, so no argument can open a
 * tag. A malformed tag (never in a checked catalog) is kept as text.
 */
function richPieces(lang: Lang, key: string, args: Args | undefined): RichSegment[] {
  const tpl = template(lang, key, args);
  const out: RichSegment[] = [];
  let open: string | null = null;
  let buf = "";
  let last = 0;
  const flush = (tag?: string) => {
    if (buf !== "") out.push(tag ? { text: substitute(buf, args, lang.locale), tag } : { text: substitute(buf, args, lang.locale) });
    buf = "";
  };
  for (const m of tpl.matchAll(cTagRe)) {
    buf += tpl.slice(last, m.index);
    last = m.index + m[0].length;
    const closing = m[1] === "/";
    const name = m[2];
    if (!closing && open === null) {
      flush();
      open = name;
    } else if (closing && open === name) {
      flush(name);
      open = null;
    } else {
      buf += m[0];
    }
  }
  buf += tpl.slice(last);
  flush();
  return out;
}

function languageOf(code: string): Language {
  return languages.find((l) => l.code === code) ?? languages[0];
}

function makeSnapshot(language: Language, catalog: Catalog, system: readonly string[], ready: boolean): I18nSnapshot {
  const lang: Lang = { code: language.code, tag: language.tag, locale: formatLocale(language, system), catalog };
  const t = Object.assign(
    <K extends MessageKey>(key: K, ...args: ArgsOf<K>): string => translate(lang, key, args[0] as Args | undefined),
    { lang: lang.code, tag: lang.tag, locale: lang.locale },
  ) as TFunction;
  return { lang: lang.code, tag: lang.tag, locale: lang.locale, t, ready };
}

let current: I18nSnapshot = makeSnapshot(languageOf(sourceLanguage), english, languageChoice.snapshot().system, false);
let wanted: string = "";
let wantedSystem = "";
const listeners = new Set<() => void>();

function show(next: I18nSnapshot) {
  current = next;
  try {
    document.documentElement.lang = next.tag;
  } catch {
    // No document (a test run outside a browser).
  }
  listeners.forEach((fn) => {
    try {
      fn();
    } catch (err) {
      console.error("i18n: a listener failed:", err);
    }
  });
}

// Puts the effective language on screen once its texts are loaded. Of two
// changes in a row only the last one shows.
function follow() {
  const choice = languageChoice.snapshot();
  const code = choice.effective;
  const systemKey = choice.system.join();
  if (code === wanted && systemKey === wantedSystem) return;
  wanted = code;
  wantedSystem = systemKey;
  const language = languageOf(code);
  const have = loaded.get(language.code);
  if (have) {
    show(makeSnapshot(language, have, choice.system, true));
    return;
  }
  load(language.code).then(
    (catalog) => {
      if (wanted === code) show(makeSnapshot(language, catalog, languageChoice.snapshot().system, true));
    },
    (err) => {
      // A chunk that didn't load (offline, a release replaced it): English
      // until the next change asks again.
      console.error("i18n: could not load language", code, err);
      if (wanted !== code) return;
      wanted = "";
      show(makeSnapshot(languageOf(sourceLanguage), english, languageChoice.snapshot().system, true));
    },
  );
}

languageChoice.subscribe(follow);
follow();

/** The language on screen (useSyncExternalStore). */
export function i18nSnapshot(): I18nSnapshot {
  return current;
}

export function subscribeI18n(fn: () => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

/**
 * A text in the language on screen, for code outside components (a toast,
 * a thrown error's message). Components use useT(), which also re-renders
 * them when the language changes. Never at module level: the language
 * isn't known there yet, and it changes.
 */
export function t<K extends MessageKey>(key: K, ...args: ArgsOf<K>): string {
  return current.t(key, ...args);
}

/** A rich key's pieces in the snapshot's language (<Trans>). */
export function richSegments<K extends RichKey>(snapshot: I18nSnapshot, key: K, args: MessageArgs[K] | undefined): RichSegment[] {
  const catalog = loaded.get(snapshot.lang) ?? english;
  return richPieces({ code: snapshot.lang, tag: snapshot.tag, locale: snapshot.locale, catalog }, key, args as Args | undefined);
}

/** For scripts/i18n-test.mjs: one text from a given catalog, as the runtime renders it. */
export function renderForTest(language: Language, locale: string, messages: Messages, key: string, args?: Args): { plain: string; rich: RichSegment[] } {
  const lang: Lang = { code: language.code, tag: language.tag, locale, catalog: catalogOf([messages]) };
  return { plain: translate(lang, key, args), rich: richPieces(lang, key, args) };
}
