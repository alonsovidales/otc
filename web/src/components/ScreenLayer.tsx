// SPDX-License-Identifier: AGPL-3.0-or-later

// What screen mode (screenMode.ts) adds over the page: the on-screen
// keyboard, the note of a press this screen can't do (another site, a
// download, files to add: on a phone or a computer instead), and Back on a
// page of the device's other than the app (a shared gallery's), where the
// kiosk's browser has no button of its own. Loaded only in screen mode
// (screenPart.tsx).
import { useEffect, useState } from "react";
import { useT, type TFunction } from "../i18n";
import ScreenKeyboard from "./ScreenKeyboard";
import { subscribeScreenNotice, type ScreenNoticeKind } from "./screenMode";
import "./ScreenKeyboard.css";

const cNoticeMs = 4000;

function noticeText(t: TFunction, kind: ScreenNoticeKind): string {
  if (kind === "download") return t("web.shell.screen.download");
  if (kind === "upload") return t("web.shell.screen.upload");
  return t("web.shell.screen.elsewhere");
}

export default function ScreenLayer() {
  const t = useT();
  // The note, and a count so that the same note again shows anew.
  const [notice, setNotice] = useState<{ kind: ScreenNoticeKind; n: number } | null>(null);

  useEffect(() => subscribeScreenNotice((kind) => {
    setNotice((was) => ({ kind, n: (was?.n ?? 0) + 1 }));
  }), []);

  useEffect(() => {
    if (!notice) return;
    const id = window.setTimeout(() => setNotice(null), cNoticeMs);
    return () => window.clearTimeout(id);
  }, [notice]);

  const away = window.location.pathname !== "/" && window.history.length > 1;
  const text = notice ? noticeText(t, notice.kind) : "";

  return (
    <>
      <ScreenKeyboard />
      {/* Always there, so a screen reader hears each note as it comes. */}
      <div className="scr-live" role="status">{text}</div>
      {notice && <div key={notice.n} className="scr-notice" aria-hidden="true">{text}</div>}
      {away && (
        <button type="button" className="scr-back" onClick={() => window.history.back()}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true" focusable="false">
            <path d="M19 12H5m6-6-6 6 6 6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          {t("common.back")}
        </button>
      )}
    </>
  );
}
