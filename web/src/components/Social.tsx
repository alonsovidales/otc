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

// How many posts to fetch per page (issue #15): loading the whole feed
// up front is what made the page take ages to appear once there were many
// posts, so pull a small page and fetch more as the user scrolls near the
// end, mirroring the native iOS app's pagination.
const PAGE_SIZE = 4;

export default function Social() {
  // ---------------- Feed ----------------
  const [feed, setFeed] = useState<PbSocialPublication[]>([]);
  const [busyLikePub, setBusyLikePub] = useState<string | null>(null);
  const [busyLikeComm, setBusyLikeComm] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const endReachedRef = useRef(false);
  const loadingMoreRef = useRef(false);
  const feedRef = useRef<PbSocialPublication[]>([]);
  useEffect(() => { feedRef.current = feed; }, [feed]);

  const fetchPage = useCallback(async (total: number, excludeUuids: string[], replacing: boolean) => {
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetSocialPublications", reqGetSocialPublications: { total, excludeUuids } };
    });
    if (resp.payload?.$case !== "respSocialPublications") return;
    const sp: PbSocialPublications = resp.payload.respSocialPublications;
    if (replacing) {
      setFeed(sp.publications);
    } else {
      setFeed(prev => {
        const seen = new Set(prev.map(p => p.uuid));
        return [...prev, ...sp.publications.filter(p => !seen.has(p.uuid))];
      });
    }
    if (sp.publications.length < total) endReachedRef.current = true;
  }, []);

  const loadFeed = useCallback(async () => {
    endReachedRef.current = false;
    await fetchPage(PAGE_SIZE, [], true);
  }, [fetchPage]);

  // Re-fetches everything currently on screen (not just page 1), so a
  // like/comment action doesn't collapse the feed back down to one page.
  const refreshCurrentlyLoaded = useCallback(async () => {
    const count = Math.max(feedRef.current.length, PAGE_SIZE);
    await fetchPage(count, [], true);
  }, [fetchPage]);

  const loadMoreIfNeeded = useCallback(async (pubUuid: string) => {
    if (loadingMoreRef.current || endReachedRef.current) return;
    const cur = feedRef.current;
    const idx = cur.findIndex(p => p.uuid === pubUuid);
    if (idx === -1 || idx < cur.length - 2) return;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    try {
      await fetchPage(PAGE_SIZE, cur.map(p => p.uuid), false);
    } finally {
      loadingMoreRef.current = false;
      setLoadingMore(false);
    }
  }, [fetchPage]);

  useEffect(() => {
    (async () => {
      await loadFeed();
    })();
  }, [loadFeed]);

  const likePublication = useCallback(async (pub_uuid: string) => {
    setBusyLikePub(pub_uuid);
    try {
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikePublication", reqLikePublication: { pubUuid: pub_uuid } };
      });
      await refreshCurrentlyLoaded();
    } finally {
      setBusyLikePub(null);
    }
  }, [refreshCurrentlyLoaded]);

  const likeComment = useCallback(async (comment_uuid: string) => {
    setBusyLikeComm(comment_uuid);
    try {
      await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikeComment", reqLikeComment: { commentUuid: comment_uuid } };
      });
      await refreshCurrentlyLoaded();
    } finally {
      setBusyLikeComm(null);
    }
  }, [refreshCurrentlyLoaded]);

  const addComment = useCallback(async (pub_uuid: string, text: string, publisherName: string) => {
    if (!text.trim()) return;
    await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqNewSocialComment", reqNewSocialComment: {
        pubUuid: pub_uuid,
        comment: text.trim(),
        publisher: publisherName, // whatever identity you use
      }};
    });
    await refreshCurrentlyLoaded();
  }, [refreshCurrentlyLoaded]);

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
    const rootRef = useRef<HTMLElement | null>(null);

    // Trigger the next page fetch once this post (one of the last two
    // currently loaded) actually scrolls into view, mirroring the iOS
    // app's "trigger near the end of the list" pagination (issue #15).
    useEffect(() => {
      const el = rootRef.current;
      if (!el) return;
      const obs = new IntersectionObserver((entries) => {
        if (entries[0]?.isIntersecting) void loadMoreIfNeeded(p.uuid);
      }, { rootMargin: "600px" });
      obs.observe(el);
      return () => obs.disconnect();
    }, [p.uuid]);

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
      <article className="sv-post" ref={rootRef as React.RefObject<HTMLElement>}>
        <header className="sv-post-hdr">
          {profURL && <img src={profURL} className="sv-img-avatar" /> || <div className="sv-avatar">👤</div> }
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
            className={`sv-btn${p.liked ? " liked" : ""}`}
            onClick={() => likePublication(p.uuid)}
            disabled={busyLikePub === p.uuid}
            aria-label={p.liked ? "Unlike publication" : "Like publication"}
            aria-pressed={p.liked}
          >
            {p.liked ? "❤️" : "🤍"} {p.likes}
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
                className={`sv-btn tiny${c.liked ? " liked" : ""}`}
                onClick={() => likeComment(c.commentUuid)}
                disabled={busyLikeComm === c.commentUuid}
                aria-label={c.liked ? "Unlike comment" : "Like comment"}
                aria-pressed={c.liked}
              >
                {c.liked ? "❤️" : "🤍"} {c.likes}
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
        {loadingMore && <div className="sv-loading-more">Loading more…</div>}
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
