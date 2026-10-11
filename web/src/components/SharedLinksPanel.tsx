// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #180: Settings > Shared Links - every share link (galleries and zip
// downloads): when it was made, until when it works, how often it was
// opened, and the space its copy takes; deleting one deletes its copy. The
// links themselves aren't here: the device never keeps them.
import { useCallback, useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, SharedLinkInfo } from "../proto/messages";
import "./SharedGalleryShare.css";
import { fmtDate, useT, type TFunction } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import { sizeText } from "./sizeText";

// Sizes: whole KB, MB with one digit, GB with two. Dates in the page's
// language (the browser's own format when it is that language).
const fmtBytes = (t: TFunction, n: number) => sizeText(t, n, [0, 1, 2]);
const fmtWhen = (t: TFunction, d?: Date) => (d ? fmtDate(t.lang, d, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }) : "");

export default function SharedLinksPanel() {
  const t = useT();
  const [links, setLinks] = useState<SharedLinkInfo[] | null>(null);
  const [error, setError] = useState<UiText | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const load = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqListSharedLinks", reqListSharedLinks: {} };
    });
    if (resp.payload?.$case === "respSharedLinks") { setLinks(resp.payload.respSharedLinks.links); setError(null); }
    else {
      const message = resp.errorMessage;
      setError(() => (message ? asIs(message) : say("web.files.links.load_failed")));
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const del = async (l: SharedLinkInfo) => {
    if (!window.confirm(t("web.files.links.confirm_delete"))) return;
    setDeleting(l.uuid);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqDeleteSharedLink", reqDeleteSharedLink: { uuid: l.uuid } };
    });
    setDeleting(null);
    if (resp.error) {
      const message = resp.errorMessage;
      setError(() => (message ? asIs(message) : say("web.files.links.delete_failed")));
      return;
    }
    void load();
  };

  const total = (links ?? []).reduce((s, l) => s + Number(l.bytes), 0);
  return (
    <section className="sf-section">
      <h3>{t("web.files.links.title")}</h3>
      <p className="sf-hint">{t("web.files.links.hint")}{links && links.length > 0 && <> {t("web.files.links.total", { size: fmtBytes(t, total) })}</>}</p>
      {error && <p className="sf-note error">{error(t)}</p>}
      {links === null && !error && <p className="sf-hint">{t("common.loading")}</p>}
      {links && links.length === 0 && <p className="sf-hint">{t("web.files.links.none")}</p>}
      {links && links.length > 0 && (
        <ul className="sl-list">
          {links.map(l => (
            <li key={l.uuid} className="sl-row">
              <div className="sl-main">
                <strong>{l.description || (l.kind === "archive" ? t("web.files.links.zip") : t("web.files.links.gallery"))}</strong>
                <span className="sl-dim">
                  {l.files > 0
                    ? t("web.files.links.details_files", { count: l.files, size: fmtBytes(t, Number(l.bytes)), made: fmtWhen(t, l.created), until: fmtWhen(t, l.expires) })
                    : t("web.files.links.details", { size: fmtBytes(t, Number(l.bytes)), made: fmtWhen(t, l.created), until: fmtWhen(t, l.expires) })}
                </span>
                <span className="sl-dim">{l.opens > 0 ? t("web.files.links.opened", { count: l.opens, date: fmtWhen(t, l.lastOpened) }) : t("web.files.links.never_opened")}</span>
              </div>
              <button className="sf-btn small danger" disabled={deleting === l.uuid} onClick={() => void del(l)}>{deleting === l.uuid ? t("common.deleting") : t("common.delete")}</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
