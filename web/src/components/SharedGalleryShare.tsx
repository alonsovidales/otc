// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Issue #180: sharing a group of photos or a folder as a gallery. Asks the
// device what it would share (how many files, how much space the copy
// takes), lets the owner name it and pick how long it lasts, then follows
// the copy and shows the link - the only time it is ever shown, since the
// device keeps no copy of it.
import { useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { ReqEnvelope, RespEnvelope, SharedGallerySource, SharedGalleryPreview, SharedGalleryJob } from "../proto/messages";
import Spinner from "./Spinner";
import "./SharedGalleryShare.css";

const fmtBytes = (n: number) =>
  n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${(n / (1 << 30)).toFixed(2)} GB`;

const pad = (n: number) => String(n).padStart(2, "0");
const defaultDescription = () => {
  const d = new Date();
  return `Shared Media ${pad(d.getDate())}-${pad(d.getMonth() + 1)}-${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

const EXPIRY = [
  { hours: 24, label: "1 day" },
  { hours: 168, label: "7 days" },
  { hours: 720, label: "30 days" },
];

export default function SharedGalleryShare({ source, onClose }: { source: Partial<SharedGallerySource>; onClose: () => void }) {
  const src: SharedGallerySource = { paths: [], groupId: "", directory: "", ...source };
  const [preview, setPreview] = useState<SharedGalleryPreview | null>(null);
  const [description, setDescription] = useState(defaultDescription);
  const [ttl, setTtl] = useState(168);
  const [lowRes, setLowRes] = useState(false);
  const [job, setJob] = useState<SharedGalleryJob | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  useEffect(() => {
    (async () => {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqPreviewSharedGallery", reqPreviewSharedGallery: { source: src } };
      });
      if (!alive.current) return;
      if (resp.payload?.$case === "respSharedGalleryPreview") setPreview(resp.payload.respSharedGalleryPreview);
      else setError(resp.errorMessage || "Could not look at what to share.");
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const start = async () => {
    setError(null);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqCreateSharedGallery", reqCreateSharedGallery: { source: src, description: description.trim() || defaultDescription(), ttlHours: ttl, lowRes } };
    });
    if (resp.payload?.$case !== "respSharedGalleryJob") {
      setError(resp.errorMessage || "Could not start sharing.");
      return;
    }
    let j = resp.payload.respSharedGalleryJob;
    setJob(j);
    while (alive.current && !j.finished) {
      await new Promise(r => setTimeout(r, 700));
      const r: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetSharedGalleryJob", reqGetSharedGalleryJob: { jobId: j.jobId } };
      });
      if (r.payload?.$case !== "respSharedGalleryJob") {
        setError(r.errorMessage || "Lost track of the copy.");
        return;
      }
      j = r.payload.respSharedGalleryJob;
      if (alive.current) setJob(j);
    }
    if (j.error) setError(j.error);
  };

  const copy = async () => {
    if (!job?.link) return;
    try { await navigator.clipboard.writeText(job.link); setCopied(true); } catch { /* the field can be copied by hand */ }
  };

  const busy = !!job && !job.finished;
  const done = !!job?.finished && !job.error;
  const pct = job && Number(job.bytesTotal) > 0 ? (100 * Number(job.bytesDone)) / Number(job.bytesTotal) : 0;

  return (
    <div className="sgs-backdrop" onClick={busy ? undefined : onClose}>
      <div className="sgs-dialog" role="dialog" aria-label="Share as a gallery" onClick={e => e.stopPropagation()}>
        <h3>Share as a gallery</h3>
        {!preview && !error && <Spinner label="Looking at what to share…" />}
        {preview && !job && (
          <>
            <p className="sgs-count">
              <strong>{preview.files}</strong> {preview.files === 1 ? "photo or video" : "photos and videos"}, <strong>{fmtBytes(Number(preview.bytes))}</strong>
              {preview.skipped > 0 && <span className="sgs-dim"> · {preview.skipped} other {preview.skipped === 1 ? "file is" : "files are"} not included</span>}
            </p>
            <p className="sgs-dim">{lowRes
              ? <>Only small copies of the photos are shared (about 1000 pixels wide), re-encrypted with a key of their own that only the link carries; the originals never leave your device.{preview.videos > 0 && <> {preview.videos} {preview.videos === 1 ? "video is" : "videos are"} left out.</>}</>
              : <>They are copied and re-encrypted with a key of their own, which only the link carries. Anyone with the link can see them until it expires; the copy takes {fmtBytes(Number(preview.bytes))} on the device.</>}</p>
            <label className="sgs-check">
              <input type="checkbox" checked={lowRes} onChange={e => setLowRes(e.target.checked)} />
              <span><strong>Low resolution only</strong> - small copies of the photos, not the originals</span>
            </label>
            <label className="sgs-label" htmlFor="sgs-desc">Description</label>
            <input id="sgs-desc" className="sgs-input" value={description} onChange={e => setDescription(e.target.value)} maxLength={200} />
            <span className="sgs-label">Available for</span>
            <div className="sgs-seg" role="radiogroup" aria-label="Available for">
              {EXPIRY.map(x => (
                <button key={x.hours} role="radio" aria-checked={ttl === x.hours} className={ttl === x.hours ? "on" : ""} onClick={() => setTtl(x.hours)}>{x.label}</button>
              ))}
            </div>
            <div className="sgs-buttons">
              <button className="btn" onClick={onClose}>Cancel</button>
              <button className="btn primary" disabled={preview.files === 0 || (lowRes && preview.files === preview.videos)} onClick={() => void start()}>Share</button>
            </div>
          </>
        )}
        {busy && job && (
          <div role="status">
            <p>Copying {Math.min(job.done + 1, job.total)} of {job.total} · {fmtBytes(Number(job.bytesDone))} of {fmtBytes(Number(job.bytesTotal))}</p>
            <div className="sgs-bar"><div style={{ width: `${pct}%` }} /></div>
          </div>
        )}
        {done && job && (
          <>
            <p>Your gallery is ready. This is the only time the link is shown: copy it now - the device keeps no copy of it, so it can't be shown again.</p>
            <div className="sgs-link">
              <input className="sgs-input" readOnly value={job.link} onFocus={e => e.currentTarget.select()} aria-label="Gallery link" />
              <button className="btn primary" onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</button>
            </div>
            <div className="sgs-buttons">
              <a className="btn" href={job.link} target="_blank" rel="noreferrer">Open</a>
              <button className="btn" onClick={onClose}>Done</button>
            </div>
          </>
        )}
        {error && (
          <>
            <p className="sgs-error">{error}</p>
            <div className="sgs-buttons"><button className="btn" onClick={onClose}>Close</button></div>
          </>
        )}
      </div>
    </div>
  );
}
