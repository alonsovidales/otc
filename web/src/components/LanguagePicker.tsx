// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The language where Settings can't be reached (docs/i18n.md, "The stored
// choice"): the signed-out top bar, a new device's setup, the page saying
// the device can't be reached, a shared gallery. A globe and the language on
// screen by its own name - the globe alone where room is short - with the
// browser's own list over it, the same list as Settings > Language
// (LanguageOptions). A choice here is the one Settings makes
// (chooseLanguage): the page re-words at once and this browser keeps it;
// signed out it stays pending and reaches the device at the next sign-in,
// before anything the device says can replace it. Hidden while English is
// the only language this build has.

import { chooseLanguage, languages, useLanguage, useT, type ChoiceSnapshot } from "../i18n";
import "./LanguagePicker.css";

// Names are each language's own, never translated.
const nameOf = (code: string) => languages.find((l) => l.code === code)?.name ?? code;

/** Settings > Language's list, shared with the picker: Automatic, then each language. */
export function LanguageOptions({ lang }: { lang: ChoiceSnapshot }) {
  const t = useT();
  // A code from a newer app that this build doesn't have: shown in English,
  // kept as the choice, listed by its code.
  const unknown = lang.choice !== "" && !languages.some((l) => l.code === lang.choice);
  return (
    <>
      <option value="">{t("web.settings.language.automatic", { language: nameOf(lang.automatic) })}</option>
      {languages.map((l) => (
        <option key={l.code} value={l.code} lang={l.tag}>{l.name}</option>
      ))}
      {unknown && <option value={lang.choice}>{lang.choice}</option>}
    </>
  );
}

interface Props {
  // The globe alone (a phone's top bar).
  iconOnly?: boolean;
  className?: string;
}

export default function LanguagePicker({ iconOnly = false, className }: Props) {
  const t = useT();
  const lang = useLanguage();

  if (languages.length < 2) return null;

  const shown = languages.find((l) => l.code === lang.effective);
  return (
    <div className={`lpk${iconOnly ? " icon-only" : ""}${className ? ` ${className}` : ""}`}>
      <svg className="lpk-globe" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor"
        strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <circle cx="12" cy="12" r="9" />
        <path d="M3 12h18" />
        <path d="M12 3c2.4 2.5 3.7 5.5 3.7 9s-1.3 6.5-3.7 9c-2.4-2.5-3.7-5.5-3.7-9S9.6 5.5 12 3Z" />
      </svg>
      {!iconOnly && (
        <>
          <span className="lpk-name" lang={shown?.tag} aria-hidden="true">{shown?.name ?? lang.effective}</span>
          <svg className="lpk-chevron" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </>
      )}
      {/* The control itself, over the whole button and invisible: a click,
          a tap or the keyboard opens the system's own list, and a screen
          reader meets a plain list box named "Language" with its value. */}
      <select
        className="lpk-select"
        aria-label={t("web.settings.language.title")}
        value={lang.choice}
        onChange={(e) => void chooseLanguage(e.target.value)}
      >
        <LanguageOptions lang={lang} />
      </select>
    </div>
  );
}
