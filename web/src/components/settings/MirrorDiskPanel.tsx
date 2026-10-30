// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Settings › Storage: a disk plugged in while the mirror lacks one, which
// the device didn't add by itself because something is on it - most new
// disks and cards come formatted (Status.mirror_candidates). Its owner may
// have it erased and added: the confirmation says plainly that everything
// on that disk is erased and that it becomes the mirror's second disk. The
// device's root watcher checks everything again before it erases anything;
// then the mirror copies every file onto the disk, its progress shown here
// as under the left menu's storage bar.
//
// Shown only while there is something to say: a disk offered, or one this
// page asked for, until the copy onto it ends. The device offers disks to
// the owner of the primary instance alone, never in a container nor on a
// demo device (where the button would show the demo's notice first). Two
// disks that look the same - one name, one size - can't be chosen here:
// the owner couldn't tell which one would be erased.

import { useEffect, useState } from "react";
import { useWS } from "../../net/useWS";
import { refuseOnDemo } from "../../net/demo";
import { RaidState, type MirrorDiskCandidate, type RespEnvelope } from "../../proto/messages";
import { fmtBytes, useT, type TFunction } from "../../i18n";
import { ConfirmDialog } from "../ConfirmDialog";
import Spinner from "../Spinner";
import { raidStateLabel, useDeviceStatus } from "../useDeviceStatus";
import { asIs, say, type UiText } from "../uiText";
import "../UpdatePanel.css";
import "./MirrorDiskPanel.css";

// The device answers at once and its watcher acts within seconds: a disk
// asked for that is neither being copied onto nor in a whole mirror after
// this long wasn't added (an Alert says why).
const cNotAddedAfterMs = 20_000;

type Disk = Pick<MirrorDiskCandidate, "id" | "model" | "size">;

// The disk this page asked for, until the copy onto it ends or it turns
// out it wasn't added: at module level, so Settings closed and opened
// again still shows how it goes. Its name is made when shown.
type Asked = Disk & { at: number; copying: boolean };
let asked: Asked | null = null;

// Keeps next as the disk asked for, here and in the page's state.
function remember(next: Asked | null, setMine: (a: Asked | null) => void) {
  asked = next;
  setMine(next);
}

// A disk as the owner knows it: what it calls itself and its size, in
// decimal units as on its label ("Generic Card Reader · 512.1 GB").
function diskName(t: TFunction, d: Disk): string {
  const size = fmtBytes(t.lang, d.size, { base: 1000, fractionDigits: 1 });
  return d.model ? `${d.model} · ${size}` : size;
}

export default function MirrorDiskPanel() {
  const t = useT();
  const [mine, setMine] = useState<Asked | null>(asked);
  const [notAdded, setNotAdded] = useState<Disk | null>(null);
  const [confirming, setConfirming] = useState<Disk | null>(null);
  // Looked at more often while the device takes it up. This is Settings'
  // Status poll: Background work reads what it brings too.
  const { status } = useDeviceStatus(mine && !mine.copying ? 2000 : 5000);
  const offered = status?.mirrorCandidates ?? [];

  // How the disk asked for is going, from each Status.
  useEffect(() => {
    const a = asked;
    if (!a || !status) return;
    if (status.raidState === RaidState.RaidSyncing) {
      if (!a.copying) remember({ ...a, copying: true }, setMine);
      return;
    }
    // A whole mirror: added and copied. A copy that stopped: the mirror's
    // own Alert says why.
    if (status.raidState === RaidState.RaidInSync || a.copying) {
      remember(null, setMine);
      return;
    }
    if (Date.now() - a.at >= cNotAddedAfterMs) {
      setNotAdded({ id: a.id, model: a.model, size: a.size });
      remember(null, setMine);
    }
  }, [status]);

  if (!mine && !notAdded && offered.length === 0) return null;

  const lookAlike = (d: Disk) => offered.some((o) => o.id !== d.id && o.model === d.model && o.size === d.size);

  const use = async (d: Disk): Promise<UiText | null> => {
    try {
      const resp: RespEnvelope = await useWS.request((e) => {
        e.payload = { $case: "reqUseMirrorDisk", reqUseMirrorDisk: { disk: d.id } };
      });
      const ack = resp.payload?.$case === "respAck" ? resp.payload.respAck : null;
      if (!resp.error && ack?.ok) {
        setNotAdded(null);
        remember({ id: d.id, model: d.model, size: d.size, at: Date.now(), copying: false }, setMine);
        setConfirming(null);
        return null;
      }
      const words = resp.errorMessage || ack?.errorMsg;
      return words ? asIs(words) : say("web.settings.storage.failed");
    } catch (e) {
      return asIs(e instanceof Error ? e.message : String(e));
    }
  };

  const percent = status?.raidSyncPercent ?? 0;
  return (
    <section className="sf-section md-panel" aria-labelledby="md-title">
      <h3 id="md-title">{t("web.settings.storage.title")}</h3>
      {mine && !mine.copying && (
        <div className="up-line"><Spinner label={t("web.settings.storage.adding", { disk: diskName(t, mine) })} /></div>
      )}
      {mine?.copying && status && (
        <div className="sf-progress md-copy">
          <p className="md-text">{t("web.settings.storage.copying", { disk: diskName(t, mine) })}</p>
          <div className="sf-progress-track" role="progressbar" aria-labelledby="md-copy-state"
            aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent)}>
            <div className="sf-progress-fill" style={{ width: `${Math.max(0, Math.min(100, percent))}%` }} />
          </div>
          <div id="md-copy-state" className="sf-hint md-state">{raidStateLabel(t, status)}</div>
        </div>
      )}
      {notAdded && (
        <p className="sf-note error md-text" role="status">{t("web.settings.storage.not_added", { disk: diskName(t, notAdded) })}</p>
      )}
      {!mine && offered.map((d) => (
        <div key={d.id} className="md-disk">
          <p className="md-text">{t("web.settings.storage.new_disk", { disk: diskName(t, d) })}</p>
          {lookAlike(d) ? (
            <p className="sf-hint md-text">{t("web.settings.storage.look_alike")}</p>
          ) : (
            <button type="button" className="sf-btn sf-danger" onClick={() => { if (!refuseOnDemo()) setConfirming(d); }}>
              {t("web.settings.storage.use")}
            </button>
          )}
        </div>
      ))}
      {confirming && (
        <ConfirmDialog
          title={t("web.settings.storage.confirm_title")}
          text={t("web.settings.storage.confirm_text", { disk: diskName(t, confirming) })}
          confirm={t("web.settings.storage.confirm")}
          busyText={t("web.settings.general.starting")}
          onConfirm={() => use(confirming)}
          onCancel={() => setConfirming(null)}
        />
      )}
    </section>
  );
}
