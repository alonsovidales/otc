// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings > Language (docs/i18n.md, "The stored choice"): the language of
// every app, stored on the device for this user. Automatic follows each
// app's own system language - here, the browser's. Hidden while English is
// the only language this build has.

import { useState } from "react";
import { chooseLanguage, languages, useLanguage, useT, type ChooseOutcome } from "../../i18n";
import "./LanguagePanel.css";

// What the panel says about the last change made in it.
type Note = "web.settings.language.too_old" | "web.settings.language.changed" | "web.settings.language.not_saved";

export default function LanguagePanel() {
  const t = useT();
  const lang = useLanguage();
  const [last, setLast] = useState<{ choice: string; outcome: ChooseOutcome } | null>(null);

  if (languages.length < 2) return null;

  const pick = (choice: string) => {
    setLast(null);
    void chooseLanguage(choice).then((outcome) => setLast({ choice, outcome }));
  };

  // Names are each language's own, never translated.
  const nameOf = (code: string) => languages.find((l) => l.code === code)?.name ?? code;
  // A code from a newer app that this build doesn't have: shown in English,
  // kept as the choice, listed by its code.
  const unknown = lang.choice !== "" && !languages.some((l) => l.code === lang.choice);

  let note: Note | null = null;
  if (lang.tooOld) note = "web.settings.language.too_old";
  else if (last?.outcome === "changed") note = "web.settings.language.changed";
  // Only while that change is still waiting: one seen since (the device
  // adopted it, or another app's) settles it.
  else if (last?.outcome === "failed" && lang.pending && last.choice === lang.choice) note = "web.settings.language.not_saved";

  return (
    <section className="sf-section lp-section">
      <h3 id="lp-title">{t("web.settings.language.title")}</h3>
      <p className="sf-hint">{t("web.settings.language.hint")}</p>
      <select
        className="sf-input lp-select"
        aria-labelledby="lp-title"
        aria-describedby={note ? "lp-note" : undefined}
        value={lang.choice}
        onChange={(e) => pick(e.target.value)}
      >
        <option value="">{t("web.settings.language.automatic", { language: nameOf(lang.automatic) })}</option>
        {languages.map((l) => (
          <option key={l.code} value={l.code} lang={l.tag}>{l.name}</option>
        ))}
        {unknown && <option value={lang.choice}>{lang.choice}</option>}
      </select>
      {note && (
        <div id="lp-note" role="status" className={`sf-note lp-note${note === "web.settings.language.not_saved" ? " error" : ""}`}>
          {t(note)}
        </div>
      )}
    </section>
  );
}
