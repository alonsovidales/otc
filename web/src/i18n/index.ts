// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The web app's localization (docs/i18n.md). In a component:
//
//   const t = useT();
//   t("common.save"); t("web.photos.count", { count: n });
//   fmtDate(t.lang, when);
//   <Trans k="web.x.policy" parts={{ link: (s) => <a href={url}>{s}</a> }} />
//
// Outside one, t() reads the language on screen at the time of the call.
// Never keep a translated text at module level or in state: it would stay
// in the language it was made in.
//
// net/ws.ts imports choice.ts directly, never this file (which reaches
// net/useWS through the provider).

import { languageChoice, type ChooseOutcome } from "./choice";

export { default as I18nProvider } from "./I18nProvider";
export { default as Trans, type RichParts, type TransProps } from "./Trans";
export { useI18n, useLanguage, useT } from "./hooks";
export { t, type I18nSnapshot, type TFunction } from "./runtime";
export { fmtBytes, fmtDate, fmtList, fmtNumber, fmtRelative, monthName, type BytesOptions, type RelativeOptions } from "./format";
export { type ChoiceSnapshot, type ChooseOutcome } from "./choice";
export { languages, type ArgsOf, type Language, type LanguageCode, type MessageKey, type RichKey } from "./messages";

/** Picks the language ("" for Automatic): shown at once, then stored on the device. */
export function chooseLanguage(choice: string): Promise<ChooseOutcome> {
  return languageChoice.choose(choice);
}
