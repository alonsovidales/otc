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

const fmtBytes = (n: number) =>
  n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${(n / (1 << 30)).toFixed(2)} GB`;
const fmtDate = (d?: Date) => (d ? d.toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }) : "");

export default function SharedLinksPanel() {
  const [links, setLinks] = useState<SharedLinkInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const load = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqListSharedLinks", reqListSharedLinks: {} };
    });
    if (resp.payload?.$case === "respSharedLinks") { setLinks(resp.payload.respSharedLinks.links); setError(null); }
    else setError(resp.errorMessage || "Could not load the shared links.");
  }, []);
  useEffect(() => { void load(); }, [load]);

  const del = async (l: SharedLinkInfo) => {
    if (!window.confirm("Delete this link? The shared copies are deleted and the link stops working.")) return;
    setDeleting(l.uuid);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqDeleteSharedLink", reqDeleteSharedLink: { uuid: l.uuid } };
    });
    setDeleting(null);
    if (resp.error) { setError(resp.errorMessage || "Could not delete it."); return; }
    void load();
  };

  const total = (links ?? []).reduce((s, l) => s + Number(l.bytes), 0);
  return (
    <section className="sf-section">
      <h3>Shared Links</h3>
      <p className="sf-hint">Galleries and downloads you shared. The links themselves aren't kept on the device, so they can't be shown again - only removed.{links && links.length > 0 && <> Together they take {fmtBytes(total)}.</>}</p>
      {error && <p className="sf-note error">{error}</p>}
      {links === null && !error && <p className="sf-hint">Loading…</p>}
      {links && links.length === 0 && <p className="sf-hint">No shared links.</p>}
      {links && links.length > 0 && (
        <ul className="sl-list">
          {links.map(l => (
            <li key={l.uuid} className="sl-row">
              <div className="sl-main">
                <strong>{l.description || (l.kind === "archive" ? "Shared files (zip)" : "Shared gallery")}</strong>
                <span className="sl-dim">
                  {l.files > 0 && <>{l.files} {l.files === 1 ? "file" : "files"} · </>}{fmtBytes(Number(l.bytes))} · made {fmtDate(l.created)} · until {fmtDate(l.expires)}
                </span>
                <span className="sl-dim">{l.opens > 0 ? `Opened ${l.opens} ${l.opens === 1 ? "time" : "times"}, last ${fmtDate(l.lastOpened)}` : "Never opened"}</span>
              </div>
              <button className="sf-btn small danger" disabled={deleting === l.uuid} onClick={() => void del(l)}>{deleting === l.uuid ? "Deleting…" : "Delete"}</button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
