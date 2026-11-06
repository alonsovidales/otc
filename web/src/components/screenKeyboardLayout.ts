// SPDX-License-Identifier: AGPL-3.0-or-later

// The on-screen keyboard's keys (ScreenKeyboard.tsx): its layers in each
// language, the accented letters a long press offers, and what a field
// asks for. Plain data and functions, tested in Node
// (web/scripts/screen-keyboard-test.mjs).
//
// The letters follow the language on screen, as a phone's keyboard does:
// QWERTY (English, Dutch, Italian; Spanish with ñ, Portuguese with ç),
// AZERTY (French), QWERTZ with ü ö ä (German). Every row of a layer adds
// up to the same width (cLayerUnits), so the keys line up.

import type { LanguageCode } from "../i18n";

/** A keyboard's page: the letters, two of symbols, a phone's number pad. */
export type Layer = "letters" | "symbols" | "more" | "digits";

/** One key; w is its width in key units (1 when left out). */
export type KeySpec =
  | { k: "char"; ch: string; w?: number }
  | { k: "shift"; w?: number }
  | { k: "back"; w?: number }
  | { k: "enter"; w?: number }
  | { k: "space"; w?: number }
  | { k: "layer"; to: Layer; w?: number }
  | { k: "hide"; w?: number }
  | { k: "gap"; w: number };

/** What a field takes: its keys, its Enter and whether it starts a sentence. */
export type FieldKind = "text" | "search" | "email" | "url" | "password" | "number" | "tel";

export interface FieldInfo {
  kind: FieldKind;
  // A textarea or an editable element: Enter is a new line.
  multiline: boolean;
  // enterkeyhint, lower-case ("" when left out).
  enterHint: string;
  // autocapitalize, lower-case ("" when left out).
  capitalize: string;
}

/** What fieldOf reads off an element (so it can be tested without one). */
export interface FieldAttrs {
  tag: string;
  type: string;
  inputMode: string;
  enterHint: string;
  capitalize: string;
  contentEditable: boolean;
  readOnly: boolean;
  disabled: boolean;
}

const cTextTypes: Record<string, FieldKind> = {
  text: "text",
  search: "search",
  email: "email",
  url: "url",
  password: "password",
  number: "number",
  tel: "tel",
};

/** The field an element is, or null when the keyboard has nothing to type
 *  into (a checkbox, a date, a read-only field, inputmode=none). */
export function fieldFrom(a: FieldAttrs): FieldInfo | null {
  if (a.readOnly || a.disabled) return null;
  const inputMode = a.inputMode.toLowerCase();
  if (inputMode === "none") return null;
  const tag = a.tag.toLowerCase();
  let kind: FieldKind;
  let multiline = false;
  if (tag === "textarea") {
    kind = "text";
    multiline = true;
  } else if (tag === "input") {
    const typed = cTextTypes[(a.type || "text").toLowerCase()];
    if (!typed) return null;
    kind = typed;
  } else if (a.contentEditable) {
    kind = "text";
    multiline = true;
  } else {
    return null;
  }
  if (kind !== "password") {
    if (inputMode === "numeric" || inputMode === "decimal") kind = "number";
    else if (inputMode === "tel") kind = "tel";
    else if (inputMode === "email") kind = "email";
    else if (inputMode === "url") kind = "url";
    else if (inputMode === "search") kind = "search";
  }
  return { kind, multiline, enterHint: a.enterHint.toLowerCase(), capitalize: a.capitalize.toLowerCase() };
}

/** The layer a field opens on. */
export function firstLayer(field: FieldInfo): Layer {
  return field.kind === "number" || field.kind === "tel" ? "digits" : "letters";
}

/** Whether the next letter starts in capitals, before being the text
 *  before the cursor: at the start of a sentence in text a person writes
 *  (a caption, a comment, a post), as a phone does. A one-line field only
 *  when it says so (autocapitalize sentences, words or on): most of this
 *  app's are names, codes and ports, where a capital would be wrong. Never
 *  in a password, an address, a search or with autocapitalize none/off. */
export function startsCapital(field: FieldInfo, before: string): boolean {
  if (field.kind !== "text") return false;
  const cap = field.capitalize;
  if (cap === "none" || cap === "off") return false;
  if (cap === "characters") return true;
  if (!field.multiline && cap !== "sentences" && cap !== "words" && cap !== "on") return false;
  if (before.trim() === "") return true;
  if (cap === "words" && /\s$/.test(before)) return true;
  return /[.!?¡¿…]\s+$/.test(before) || /\n\s*$/.test(before);
}

// Every layer's rows add up to these units (German's letters are 11 wide).
export const cLayerUnits: Record<Layer, number> = { letters: 10, symbols: 10, more: 10, digits: 4 };

function chars(s: string): KeySpec[] {
  return Array.from(s, (ch) => ({ k: "char", ch }) as KeySpec);
}

// The letters' first three rows in each language.
function letterRows(lang: LanguageCode): KeySpec[][] {
  switch (lang) {
    case "fr":
      return [
        chars("azertyuiop"),
        chars("qsdfghjklm"),
        [{ k: "shift", w: 1.5 }, ...chars("wxcvbn'"), { k: "back", w: 1.5 }],
      ];
    case "de":
      return [
        chars("qwertzuiopü"),
        chars("asdfghjklöä"),
        [{ k: "shift", w: 2 }, ...chars("yxcvbnm"), { k: "back", w: 2 }],
      ];
    case "es":
      return [
        chars("qwertyuiop"),
        chars("asdfghjklñ"),
        [{ k: "shift", w: 1.5 }, ...chars("zxcvbnm"), { k: "back", w: 1.5 }],
      ];
    case "pt":
      return [
        chars("qwertyuiop"),
        chars("asdfghjklç"),
        [{ k: "shift", w: 1.5 }, ...chars("zxcvbnm"), { k: "back", w: 1.5 }],
      ];
    default:
      return [
        chars("qwertyuiop"),
        [{ k: "gap", w: 0.5 }, ...chars("asdfghjkl"), { k: "gap", w: 0.5 }],
        [{ k: "shift", w: 1.5 }, ...chars("zxcvbnm"), { k: "back", w: 1.5 }],
      ];
  }
}

/** The units a layer is wide in lang. */
export function layerUnits(lang: LanguageCode, layer: Layer): number {
  return layer === "letters" && lang === "de" ? 11 : cLayerUnits[layer];
}

// The bottom row: the other layer, a mark, the space, the full stop, Enter
// and the key that puts the keyboard away. On the letters, an address's
// mark is the one it needs (@, /) instead of the comma; the symbols have
// both already.
function bottomRow(units: number, layerKey: KeySpec, mark: string): KeySpec[] {
  const fixed: KeySpec[] = [layerKey, { k: "char", ch: mark }, { k: "char", ch: "." }, { k: "enter", w: 1.5 }, { k: "hide" }];
  const used = fixed.reduce((n, key) => n + (key.w ?? 1), 0);
  return [fixed[0], fixed[1], { k: "space", w: units - used }, fixed[2], fixed[3], fixed[4]];
}

function addressMark(field: FieldInfo): string {
  return field.kind === "email" ? "@" : field.kind === "url" ? "/" : ",";
}

/** The rows of layer in lang, for field. */
export function keyboardRows(lang: LanguageCode, layer: Layer, field: FieldInfo): KeySpec[][] {
  const units = layerUnits(lang, layer);
  switch (layer) {
    case "letters":
      return [...letterRows(lang), bottomRow(units, { k: "layer", to: "symbols", w: 1.5 }, addressMark(field))];
    case "symbols":
      return [
        chars("1234567890"),
        chars("@#€$%&-+()"),
        [{ k: "layer", to: "more", w: 1.5 }, ...chars("*\"':;!?"), { k: "back", w: 1.5 }],
        bottomRow(units, { k: "layer", to: "letters", w: 1.5 }, ","),
      ];
    case "more":
      return [
        chars("[]{}<>^~`|"),
        chars("_\\/=£¥§°•…"),
        [{ k: "layer", to: "symbols", w: 1.5 }, ...chars("¿¡«»×÷·"), { k: "back", w: 1.5 }],
        bottomRow(units, { k: "layer", to: "letters", w: 1.5 }, ","),
      ];
    case "digits": {
      const tel = field.kind === "tel";
      return [
        [...chars("123"), { k: "back" }],
        [...chars("456"), { k: "char", ch: tel ? "+" : "-" }],
        [...chars("789"), tel ? { k: "space" } : { k: "char", ch: "." }],
        [{ k: "hide" }, { k: "char", ch: "0", w: 2 }, { k: "enter" }],
      ];
    }
  }
}

// The accented letters of each language, its own first, in the order its
// writers reach for them.
const cOwnAccents: Partial<Record<LanguageCode, string>> = {
  es: "áéíóúüñ",
  pt: "áàâãéêíóôõúç",
  fr: "éèêëàâçùûüôîïœæÿ",
  it: "àèéìòóù",
  de: "äöüß",
  nl: "éëèïöüá",
};

// Every letter a long press can give, by its plain letter.
const cAccents: Record<string, string> = {
  a: "àáâäãåāæ",
  c: "çćč",
  e: "éèêëēęė",
  i: "íìîïī",
  l: "ł",
  n: "ñń",
  o: "óòôöõøōœ",
  s: "ßśš",
  u: "úùûüū",
  y: "ÿý",
  z: "žźż",
  // Marks too, as on a phone.
  "-": "–—",
  "'": "‘’",
  "\"": "“”„«»",
  ".": "…",
  "?": "¿",
  "!": "¡",
  "$": "€£¥¢",
  "0": "°",
};

// The plain letter an accented one is written from (æ and œ go under a
// and o, ß under s).
function baseOf(accented: string): string {
  for (const [base, list] of Object.entries(cAccents)) {
    if (list.includes(accented)) return base;
  }
  return accented;
}

/** What a long press on ch offers in lang: its own language's accents
 *  first, then the rest; [] when it has none. Lower-case; upperOf turns
 *  them with Shift. */
export function accentsOf(lang: LanguageCode, ch: string): string[] {
  const all = Array.from(cAccents[ch] ?? "");
  if (all.length === 0) return [];
  const own = Array.from(cOwnAccents[lang] ?? "").filter((a) => baseOf(a) === ch && all.includes(a));
  return [...own, ...all.filter((a) => !own.includes(a))];
}

/** ch as Shift types it in the language tagged tag. ß stays ß: its
 *  capital is two letters (SS). */
export function upperOf(ch: string, tag: string): string {
  if (ch === "ß") return ch;
  const up = ch.toLocaleUpperCase(tag);
  return Array.from(up).length === 1 ? up : ch;
}

/** Whether Shift changes ch (a letter, not a digit or a mark). */
export function hasCase(ch: string): boolean {
  return ch.toLowerCase() !== ch.toUpperCase();
}
