import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type {
  ReqEnvelope,
  RespEnvelope,
  SocialPublications as PbSocialPublications,
  SocialPublication as PbSocialPublication,
  //File as PbFile,
} from "../proto/messages";
import "./Social.css";

function bytesToURL(bytes?: Uint8Array, mime = "application/octet-stream") {
  if (!bytes || bytes.length === 0) return null;
  const blob = new Blob([bytes], { type: mime });
  return URL.createObjectURL(blob);
}

export default function Social() {
  // ---------------- Feed ----------------
  const [feed, setFeed] = useState<PbSocialPublication[]>([]);
  const [busyLikePub, setBusyLikePub] = useState<string | null>(null);
  const [busyLikeComm, setBusyLikeComm] = useState<string | null>(null);

  const loadFeed = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetSocialPublications", reqGetSocialPublications: { total: 50 } };
    });
    if (resp.payload?.$case === "respSocialPublications") {
      const sp: PbSocialPublications = resp.payload.respSocialPublications;
      setFeed(sp.publications);
    }
  }, []);

  useEffect(() => {
    (async () => {
      await Promise.all([ loadFeed()]);
    })();
  }, [loadFeed]);

  const likePublication = useCallback(async (pub_uuid: string) => {
    setBusyLikePub(pub_uuid);
    try {
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikePublication", reqLikePublication: { pubUuid: pub_uuid } };
      });
      await loadFeed();
    } finally {
      setBusyLikePub(null);
    }
  }, [loadFeed]);

  const likeComment = useCallback(async (comment_uuid: string) => {
    setBusyLikeComm(comment_uuid);
    try {
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikeComment", reqLikeComment: { commentUuid: comment_uuid } };
      });
      await loadFeed();
    } finally {
      setBusyLikeComm(null);
    }
  }, [loadFeed]);

  const addComment = useCallback(async (pub_uuid: string, text: string, publisherName: string) => {
    if (!text.trim()) return;
    await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqNewSocialComment", reqNewSocialComment: {
        pubUuid: pub_uuid,
        comment: text.trim(),
        publisher: publisherName, // whatever identity you use
      }};
    });
    await loadFeed();
  }, [loadFeed]);

  // ---------------- Image viewer (modal) ----------------
  const [viewerOpen, setViewerOpen] = useState(false);
  const [viewerPub, setViewerPub] = useState<PbSocialPublication | null>(null);
  const [viewerIdx, setViewerIdx] = useState(0);
  const [viewerURL, setViewerURL] = useState<string | null>(null);
  const [viewerLoading, setViewerLoading] = useState(false);

  const openViewer = useCallback(async (pub: PbSocialPublication, index: number) => {
    setViewerPub(pub);
    setViewerIdx(index);
    setViewerOpen(true);

    const f = pub.files[index];
    // show low-res first
    const low = bytesToURL(f.content as unknown as Uint8Array, f.mime) || null;
    setViewerURL(low);

    // then fetch hi-res
    setViewerLoading(true);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: f.path } };
      });
      if (resp.payload?.$case === "respFile" && resp.payload.respFile.content) {
        const hi = bytesToURL(resp.payload.respFile.content as Uint8Array, resp.payload.respFile.mime);
        setViewerURL(prev => {
          if (prev && prev !== hi) URL.revokeObjectURL(prev);
          return hi;
        });
      }
    } finally {
      setViewerLoading(false);
    }
  }, []);

  const closeViewer = useCallback(() => {
    setViewerOpen(false);
    setViewerLoading(false);
    if (viewerURL) URL.revokeObjectURL(viewerURL);
    setViewerURL(null);
    setViewerPub(null);
  }, [viewerURL]);

  const nextImg = useCallback(() => {
    if (!viewerPub) return;
    const next = (viewerIdx + 1) % viewerPub.files.length;
    void openViewer(viewerPub, next);
  }, [viewerPub, viewerIdx, openViewer]);

  const prevImg = useCallback(() => {
    if (!viewerPub) return;
    const prev = (viewerIdx - 1 + viewerPub.files.length) % viewerPub.files.length;
    void openViewer(viewerPub, prev);
  }, [viewerPub, viewerIdx, openViewer]);

  // keyboard when modal open
  useEffect(() => {
    if (!viewerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowRight") { e.preventDefault(); nextImg(); }
      else if (e.key === "ArrowLeft") { e.preventDefault(); prevImg(); }
      else if (e.key === "Escape") { e.preventDefault(); closeViewer(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [viewerOpen, nextImg, prevImg, closeViewer]);

  // basic swipe for post images (not modal)
  const useSwipe = () => {
    const startX = useRef<number | null>(null);
    const onTouchStart = (e: React.TouchEvent) => { startX.current = e.touches[0].clientX; };
    const onTouchEnd = (e: React.TouchEvent, onLeft: () => void, onRight: () => void) => {
      if (startX.current == null) return;
      const dx = e.changedTouches[0].clientX - startX.current;
      if (dx < -30) onLeft();
      if (dx > 30) onRight();
      startX.current = null;
    };
    return { onTouchStart, onTouchEnd };
  };

  // --------- Render helpers ----------
  const Post: React.FC<{ p: PbSocialPublication }> = ({ p }) => {
    const [idx, setIdx] = useState(0);
    const swipe = useSwipe();

    const goLeft = () => setIdx(i => (i - 1 + p.files.length) % p.files.length);
    const goRight = () => setIdx(i => (i + 1) % p.files.length);

    const current = p.files[idx];
    const lowURL = useMemo(
      () => bytesToURL(current.content as unknown as Uint8Array, current.mime),
      // eslint-disable-next-line react-hooks/exhaustive-deps
      [p.uuid, idx]
    );
    const profURL = useMemo(
      () => bytesToURL(p.publisher?.image as unknown as Uint8Array, current.mime),
      [p.uuid, idx]
    );

    useEffect(() => () => { if (lowURL) URL.revokeObjectURL(lowURL); }, [lowURL]);

    return (
      <article className="sv-post">
        <header className="sv-post-hdr">
          {profURL && <img src={profURL} className="sv-img-avatar" /> || <div className="sv-avatar">"👤"</div> }
          <div className="sv-publisher">{p.publisher?.name || "User"}</div>
        </header>

        <div className="sv-media"
             onTouchStart={swipe.onTouchStart}
             onTouchEnd={(e)=>swipe.onTouchEnd(e, goRight, goLeft)}>
          {lowURL ? (
            <img
              src={lowURL}
              alt={current.path}
              onClick={() => openViewer(p, idx)}
            />
          ) : (
            <div className="sv-media-ph">🖼️</div>
          )}
          {p.files.length > 1 && (
            <>
              <button className="sv-nav left" onClick={goLeft}>‹</button>
              <button className="sv-nav right" onClick={goRight}>›</button>
            </>
          )}
        </div>

        <div className="sv-caption">{p.text}</div>

        <div className="sv-actions">
          <button
            className="sv-btn"
            onClick={() => likePublication(p.uuid)}
            disabled={busyLikePub === p.uuid}
            aria-label="Like publication"
          >
            ❤️ {p.likes}
          </button>
          <button className="sv-btn" onClick={() => alert("Share (not implemented)")}>↗︎ Share</button>
        </div>

        {/* Comments */}
        <div className="sv-comments">
          {p.comments?.map(c => (
            <div className="sv-comment" key={c.commentUuid}>
              <div className="sv-cmeta">
                <span className="sv-cname">{c.publisher || "User"}:</span>
                <span className="sv-ctext">{c.comment}</span>
              </div>
              <button
                className="sv-btn tiny"
                onClick={() => likeComment(c.commentUuid)}
                disabled={busyLikeComm === c.commentUuid}
                aria-label="Like comment"
              >
                ❤️ {c.likes}
              </button>
            </div>
          ))}
          <NewComment pubUuid={p.uuid} onSend={(txt) => addComment(p.uuid, txt, "me")} />
        </div>
      </article>
    );
  };

  return (
    <div className="sv-wrap">
      {/* Right: feed */}
      <div className="sv-feed">
        {feed.map(p => <Post key={p.uuid} p={p} />)}
      </div>

      {/* Image modal */}
      {viewerOpen && (
        <div className="sv-modal" onClick={closeViewer}>
          <div className="sv-modal-body" onClick={(e) => e.stopPropagation()}>
            <button className="sv-close" onClick={closeViewer}>✕</button>
            <button className="sv-nav left" onClick={prevImg}>‹</button>
            <button className="sv-nav right" onClick={nextImg}>›</button>
            {viewerURL ? (
              <div className="sv-full-wrap">
                <img className="sv-full" src={viewerURL} alt="full" />
                {viewerLoading && <div className="sv-loading">Loading…</div>}
              </div>
            ) : (
              <div className="sv-loading">Loading…</div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// -------- Small bits --------

function NewComment({ onSend }: { pubUuid: string; onSend: (t: string) => void }) {
  const [txt, setTxt] = useState("");
  return (
    <div className="sv-newcomment">
      <input
        className="sv-input"
        placeholder="Add a comment…"
        value={txt}
        onChange={(e) => setTxt(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && txt.trim()) { onSend(txt); setTxt(""); } }}
      />
      <button className="sv-btn tiny" disabled={!txt.trim()} onClick={() => { onSend(txt); setTxt(""); }}>
        Post
      </button>
    </div>
  );
}
