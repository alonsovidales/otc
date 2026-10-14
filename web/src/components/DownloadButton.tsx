// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The Download button and the note of a download that failed, for the
// library viewer (MediaViewer) and the Social feed; what they do is
// mediaDownload.ts. Styled in PhotoGallery.css (.pg-modal-dl*, the
// viewers' header pill; .pg-modal-toast*) and Social.css (.sv-dl*, a
// post's own button).
import { savingLabel, savingPercent, failedText, dismissFailed } from "./mediaDownload";
import type { Saving, FailedDownload } from "./mediaDownload";
import { useT } from "../i18n";
import { percentText } from "./uiText";
import "./PhotoGallery.css";

const DownloadGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19.5h14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
const DoneGlyph = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
    <path d="M5 12.5 9.5 17 19 7.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

/** The button, with its progress, and what screen readers hear of it.
 *  Busy rather than disabled while it runs, so the keyboard's focus
 *  stays. `cls` names its parts: "pg-modal-dl" is the viewers' header
 *  pill (its label only on a wide screen), "sv-dl" a post's own. */
export function DownloadButton({ save, onClick, what, cls = "pg-modal-dl", className = "", title, label, announce = true }: {
  /** The download of the item it saves, if one is under way. */
  save: Saving | undefined;
  onClick: () => void;
  /** The file's name, for screen readers. */
  what: string;
  cls?: string;
  /** More classes for the button itself. */
  className?: string;
  /** The tooltip (TipLayer's, at once; also the screen readers'
   *  description) and accessible name while idle ("Download the original",
   *  "Download"), made by the caller in the language on screen. */
  title?: string;
  label?: string;
  /** Whether its status is told to screen readers (false while another
   *  button for the same download, in an open viewer, tells it). */
  announce?: boolean;
}) {
  const t = useT();
  const text = savingLabel(t, save);
  const pct = savingPercent(save);
  return (
    <>
      <button
        type="button"
        className={cls + (className ? " " + className : "") + (save ? " busy" : "")}
        data-tip={save ? text : title ?? t("web.shell.download.original")}
        aria-label={save ? text : label ?? t("common.download")}
        aria-description={save ? undefined : title ?? t("web.shell.download.original")}
        aria-disabled={save ? true : undefined}
        onClick={onClick}
      >
        <span className={cls + "-glyph"}>
          {!save ? <DownloadGlyph /> : save.phase === "handed" ? <DoneGlyph /> : <span className="pg-modal-dl-spin" />}
        </span>
        <span className={cls + "-text"}>{text}</span>
        {pct != null && <span className={cls + "-pct"} aria-hidden="true">{percentText(t, pct)}</span>}
      </button>
      <span className="pg-modal-sr" role="status">
        {save && announce
          ? (save.phase === "handed" ? t("web.shell.download.started_sr", { name: what }) : t("web.shell.download.downloading_sr", { name: what }))
          : ""}
      </span>
    </>
  );
}

/** A failed download's note, inside a viewer (useDownloadHost); over the
 *  page there is mediaDownload's own. */
export function DownloadNote({ failed }: { failed: FailedDownload | null }) {
  const t = useT();
  if (!failed) return null;
  return (
    <div className="pg-modal-toast" role="alert" key={failed.seq}>
      <span>{failedText(failed.name)}</span>
      <button type="button" className="pg-modal-toast-close" aria-label={t("web.shell.download.dismiss")} onClick={dismissFailed}>×</button>
    </div>
  );
}
