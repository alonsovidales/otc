// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { File as MsgFile, ImageGroup, RespEnvelope } from "../proto/messages";
import "./DevicePhotoPicker.css";

// Issue #178: the profile picture can come from the photos on the device
// too, not only from this computer - its groups as a row of chips, the
// library as a grid. The picked photo is fetched full size (GetFile, which
// hands a HEIC back as JPEG) and goes through the same crop as a local one.

type Props = {
  onCancel: () => void;
  onPicked: (file: File) => void;
};

// A search result's content is always the device's JPEG thumbnail.
const thumbURL = (f: MsgFile) => {
  const b = f.content as unknown as Uint8Array | undefined;
  return b && b.byteLength ? URL.createObjectURL(new Blob([b], { type: "image/jpeg" })) : "";
};
// A search's first page is small so the grid paints quickly over a slow
// upload; the pages after it get the device's own size (see PhotoGallery).
const cFirstPagePhotos = 12;

export default function DevicePhotoPicker({ onCancel, onPicked }: Props) {
  const [groups, setGroups] = useState<ImageGroup[]>([]);
  const [group, setGroup] = useState("");
  const [items, setItems] = useState<{ f: MsgFile; url: string }[]>([]);
  const [token, setToken] = useState("");
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);
  // In a ref, not only in state: the observer and the group change can
  // both ask for a page in the same tick, and a state flag read from a
  // closure is stale by then - the same page went out three times.
  const busy = useRef(false);
  // Every thumbnail URL made, revoked on close. Revoking the previous
  // render's URLs whenever `items` changed (as this did) blanked every
  // tile already shown each time a new page arrived.
  const urls = useRef<string[]>([]);
  const [opening, setOpening] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const gen = useRef(0);
  const gridRef = useRef<HTMLDivElement | null>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => () => urls.current.forEach(u => URL.revokeObjectURL(u)), []);

  useEffect(() => {
    void (async () => {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqListImageGroups", reqListImageGroups: {} };
      });
      if (resp.payload?.$case === "respImageGroups") setGroups(resp.payload.respImageGroups.groups ?? []);
    })();
  }, []);

  const fetchPage = useCallback(async (fresh: boolean) => {
    if (!fresh && (busy.current || done)) return;
    const my = fresh ? ++gen.current : gen.current;
    // No token (a new group, or a first page that failed) starts a search.
    const sendToken = fresh ? "" : token;
    busy.current = true;
    setLoading(true);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = {
          $case: "reqSearchPhotos",
          reqSearchPhotos: {
            tags: [], personIds: [], groupId: group, includeVideos: false, token: sendToken,
            limit: sendToken ? 0 : cFirstPagePhotos,
          },
        };
      });
      if (my !== gen.current || resp.payload?.$case !== "respListOfFiles") return;
      const lof = resp.payload.respListOfFiles;
      const added = (lof.files ?? []).map(f => ({ f, url: thumbURL(f) }));
      added.forEach(a => a.url && urls.current.push(a.url));
      setItems(prev => (fresh ? added : prev.concat(added)));
      setToken(lof.token || "");
      setDone(!lof.token);
    } finally {
      if (my === gen.current) {
        busy.current = false;
        setLoading(false);
      }
    }
  }, [group, token, done]);

  // A new group starts the grid over.
  useEffect(() => {
    setItems([]);
    setDone(false);
    void fetchPage(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [group]);

  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      entries => { if (entries[0]?.isIntersecting) void fetchPage(false); },
      { root: gridRef.current, rootMargin: "0px 0px 400px 0px" },
    );
    obs.observe(node);
    return () => obs.disconnect();
  }, [fetchPage]);

  // A page that doesn't fill the grid leaves nothing to scroll, so the
  // sentinel never "comes into view" again and loading stopped there (a
  // device answers 5-30 photos a page, and the first asks for only 12):
  // keep going until it is full.
  useEffect(() => {
    const g = gridRef.current;
    if (!g || loading || done || items.length === 0) return;
    if (g.scrollHeight <= g.clientHeight + 400) void fetchPage(false);
  }, [items, loading, done, fetchPage]);

  const pick = async (f: MsgFile) => {
    setOpening(f.path);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: f.path } };
      });
      if (resp.payload?.$case !== "respFile") throw new Error(resp.errorMessage || "no answer");
      const full = resp.payload.respFile;
      const bytes = full.content as unknown as Uint8Array;
      onPicked(new File([bytes], f.path.split("/").pop() || "photo.jpg", { type: full.mime || "image/jpeg" }));
    } catch (e: any) {
      setError(`That photo could not be fetched from the device (${e?.message ?? e}).`);
    } finally {
      setOpening(null);
    }
  };

  return (
    <div className="dpp-backdrop" role="dialog" aria-modal="true" aria-label="Choose a photo from your device">
      <div className="dpp-dialog">
        <div className="dpp-head">
          <h3>Choose a photo</h3>
          <button className="dpp-cancel" onClick={onCancel}>Cancel</button>
        </div>
        <div className="dpp-chips">
          <button className={`dpp-chip${group === "" ? " on" : ""}`} onClick={() => setGroup("")}>All photos</button>
          {groups.map(g => (
            <button key={g.id} className={`dpp-chip${group === g.id ? " on" : ""}`} onClick={() => setGroup(g.id)}>
              {g.name} <span className="dpp-count">{g.fileCount}</span>
            </button>
          ))}
        </div>
        {error && <p className="dpp-error">{error}</p>}
        <div className="dpp-grid" ref={gridRef}>
          {items.map(({ f, url }) => (
            <button key={f.path} className="dpp-tile" onClick={() => void pick(f)} disabled={opening !== null} title={f.path}>
              {url && <img src={url} alt="" loading="lazy" />}
              {opening === f.path && <span className="dpp-spinner" aria-label="Opening" />}
            </button>
          ))}
          {!loading && items.length === 0 && <p className="dpp-empty">No photos here yet.</p>}
          <div ref={sentinelRef} className="dpp-sentinel" />
        </div>
      </div>
    </div>
  );
}
