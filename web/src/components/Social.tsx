import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import NewPostPicker from "./NewPostPicker";
import type {
  ReqEnvelope,
  RespEnvelope,
  SocialPublications as PbSocialPublications,
  SocialPublication as PbSocialPublication,
  Profile as PbProfile,
  //File as PbFile,
} from "../proto/messages";
import "./Social.css";

// When the post was published, shown in the feed. Relative for anything
// recent (the timescale people actually care about scrolling a feed),
// falling back to an absolute date once it's old enough that "3d ago" stops
// being more useful than just the date.
function formatPostDate(d?: Date): string {
  if (!d) return "";
  const diffMs = Date.now() - d.getTime();
  const mins = Math.floor(diffMs / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

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

  // Issue #17: flip the UI the instant the user taps/sends, instead of
  // waiting for the round-trip. The server's Ack for these three requests
  // carries no data beyond ok/error, so the optimistic guess *is* the new
  // truth on success — only a rejected/failed request needs to walk it back.

  const likePublication = useCallback(async (pub_uuid: string) => {
    const wasLiked = feedRef.current.find(p => p.uuid === pub_uuid)?.liked ?? false;
    setFeed(prev => prev.map(p => p.uuid === pub_uuid
      ? { ...p, liked: !wasLiked, likes: p.likes + (wasLiked ? -1 : 1) }
      : p));
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikePublication", reqLikePublication: { pubUuid: pub_uuid } };
      });
      if (resp.payload?.$case === "respAck" && !resp.payload.respAck.ok) {
        setFeed(prev => prev.map(p => p.uuid === pub_uuid
          ? { ...p, liked: wasLiked, likes: p.likes + (wasLiked ? 1 : -1) }
          : p));
      }
    } catch {
      setFeed(prev => prev.map(p => p.uuid === pub_uuid
        ? { ...p, liked: wasLiked, likes: p.likes + (wasLiked ? 1 : -1) }
        : p));
    }
  }, []);

  const likeComment = useCallback(async (comment_uuid: string) => {
    const wasLiked = feedRef.current
      .flatMap(p => p.comments)
      .find(c => c.commentUuid === comment_uuid)?.liked ?? false;
    const toggle = (liked: boolean) => setFeed(prev => prev.map(p => ({
      ...p,
      comments: p.comments.map(c => c.commentUuid === comment_uuid
        ? { ...c, liked, likes: c.likes + (liked === wasLiked ? 0 : (liked ? 1 : -1)) }
        : c),
    })));
    toggle(!wasLiked);
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqLikeComment", reqLikeComment: { commentUuid: comment_uuid } };
      });
      if (resp.payload?.$case === "respAck" && !resp.payload.respAck.ok) {
        toggle(wasLiked);
      }
    } catch {
      toggle(wasLiked);
    }
  }, []);

  const addComment = useCallback(async (pub_uuid: string, text: string, publisherName: string) => {
    const trimmed = text.trim();
    if (!trimmed) return;

    const tempUuid = `pending-${Math.random().toString(36).slice(2)}`;
    setFeed(prev => prev.map(p => p.uuid === pub_uuid
      ? { ...p, comments: [...p.comments, {
          pubUuid: pub_uuid, commentUuid: tempUuid, comment: trimmed,
          publisher: publisherName, likes: 0, liked: false, dateTime: undefined,
        }] }
      : p));

    const removeOptimistic = () => setFeed(prev => prev.map(p => p.uuid === pub_uuid
      ? { ...p, comments: p.comments.filter(c => c.commentUuid !== tempUuid) }
      : p));

    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqNewSocialComment", reqNewSocialComment: {
          pubUuid: pub_uuid,
          comment: trimmed,
          publisher: publisherName, // whatever identity you use
        }};
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        // Swap the placeholder for the server's real comment (uuid,
        // timestamp, anything anyone else posted meanwhile) quietly, now
        // that the user has already seen their comment appear.
        await refreshCurrentlyLoaded();
      } else {
        removeOptimistic();
      }
    } catch {
      removeOptimistic();
    }
  }, [refreshCurrentlyLoaded]);

  // Issue #34: delete one of your own posts.
  const deletePublication = useCallback(async (pub_uuid: string) => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqDelSocialPublication", reqDelSocialPublication: { pubUuid: pub_uuid } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setFeed(prev => prev.filter(p => p.uuid !== pub_uuid));
      }
    } catch { /* leave the post in place; user can retry */ }
  }, []);

  // Issue #35: delete a comment on one of your own posts (server enforces
  // the "own post" rule regardless of who wrote the comment).
  const deleteComment = useCallback(async (comment_uuid: string) => {
    try {
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = { $case: "reqDelSocialComment", reqDelSocialComment: { commentUuid: comment_uuid } };
      });
      if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
        setFeed(prev => prev.map(p => ({ ...p, comments: p.comments.filter(c => c.commentUuid !== comment_uuid) })));
      }
    } catch { /* leave the comment in place; user can retry */ }
  }, []);

  // ---------------- New post picker (issue #32) ----------------
  // "+" button opens the photo gallery (tag search included) so the user
  // can pick photos and post them, without leaving the social tab.
  const [pickerOpen, setPickerOpen] = useState(false);

  // ---------------- Likers modal (issue #29) ----------------
  const [likersOpen, setLikersOpen] = useState(false);
  const [likers, setLikers] = useState<PbProfile[] | null>(null);

  const showPublicationLikers = useCallback(async (pub_uuid: string) => {
    setLikersOpen(true);
    setLikers(null);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetPublicationLikers", reqGetPublicationLikers: { pubUuid: pub_uuid } };
    });
    setLikers(resp.payload?.$case === "respLikers" ? resp.payload.respLikers.likers : []);
  }, []);

  const showCommentLikers = useCallback(async (comment_uuid: string) => {
    setLikersOpen(true);
    setLikers(null);
    const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
      (e as any).payload = { $case: "reqGetCommentLikers", reqGetCommentLikers: { commentUuid: comment_uuid } };
    });
    setLikers(resp.payload?.$case === "respLikers" ? resp.payload.respLikers.likers : []);
  }, []);

  const closeLikers = useCallback(() => { setLikersOpen(false); setLikers(null); }, []);

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

  // basic swipe, shared by post images and the full-screen modal
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
  const modalSwipe = useSwipe();

  // Issue #20: clicking the left/right portion of an image pages through
  // it, like swiping — no separate nav buttons. `onNav` gets called with
  // "left"/"right" for anything left/right of center.
  const navByClickX = (e: React.MouseEvent<HTMLElement>, onLeft: () => void, onRight: () => void) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const x = e.clientX - rect.left;
    if (x < rect.width / 2) onLeft(); else onRight();
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

    // Issue #27: with more than one image in a post, fix the media area's
    // height to the tallest of them (capped at 3/4 of the viewport) instead
    // of letting it resize per-image — swiping between mixed aspect ratios
    // used to make the whole card jump taller/shorter on every image.
    // `null` for a single-image post, which keeps its own natural height.
    const [carouselHeight, setCarouselHeight] = useState<number | null>(null);
    useEffect(() => {
      if (p.files.length <= 1) { setCarouselHeight(null); return; }
      let cancelled = false;
      const width = rootRef.current?.getBoundingClientRect().width || window.innerWidth;
      const urls = p.files
        .map(f => bytesToURL(f.content as unknown as Uint8Array, f.mime))
        .filter((u): u is string => !!u);
      Promise.all(urls.map(u => new Promise<number>((resolve) => {
        const img = new Image();
        img.onload = () => resolve(img.naturalWidth > 0 ? width * (img.naturalHeight / img.naturalWidth) : 0);
        img.onerror = () => resolve(0);
        img.src = u;
      }))).then((heights) => {
        urls.forEach(URL.revokeObjectURL);
        if (cancelled) return;
        const tallest = Math.max(0, ...heights);
        if (tallest > 0) setCarouselHeight(Math.min(tallest, window.innerHeight * 0.75));
      });
      return () => { cancelled = true; };
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [p.uuid]);

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
          <div className="sv-pub-meta">
            <div className="sv-publisher">{p.publisher?.name || "User"}</div>
            {p.dateTime && <div className="sv-post-date">{formatPostDate(p.dateTime)}</div>}
          </div>
          {/* Issue #34: delete one of your own posts. */}
          {p.own && (
            <button
              className="sv-post-delete"
              title="Delete post"
              onClick={() => { if (window.confirm("Delete this post?")) void deletePublication(p.uuid); }}
            >
              🗑️
            </button>
          )}
        </header>

        <div className="sv-media"
             style={carouselHeight ? { height: carouselHeight } : undefined}
             onTouchStart={swipe.onTouchStart}
             onTouchEnd={(e)=>swipe.onTouchEnd(e, goRight, goLeft)}>
          {lowURL ? (
            <img
              src={lowURL}
              alt={current.path}
              style={carouselHeight ? { height: "100%", width: "100%", objectFit: "contain" } : undefined}
              onClick={(e) => {
                // Issue #20: click the left/right quarter of a multi-image
                // post to page through it (no visible buttons) — the
                // middle half still opens the full-screen viewer.
                if (p.files.length > 1) {
                  const rect = e.currentTarget.getBoundingClientRect();
                  const x = e.clientX - rect.left;
                  if (x < rect.width * 0.25) { goLeft(); return; }
                  if (x > rect.width * 0.75) { goRight(); return; }
                }
                openViewer(p, idx);
              }}
            />
          ) : (
            <div className="sv-media-ph">🖼️</div>
          )}
        </div>

        <div className="sv-caption">{p.text}</div>

        <div className="sv-actions">
          <button
            className={`sv-btn${p.liked ? " liked" : ""}`}
            onClick={() => likePublication(p.uuid)}
            aria-label={p.liked ? "Unlike publication" : "Like publication"}
            aria-pressed={p.liked}
          >
            {p.liked ? "❤️" : "🤍"}
          </button>
          <button className="sv-btn" onClick={() => alert("Share (not implemented)")}>↗︎ Share</button>
        </div>
        {/* Issue #29: tap the count (separate from the heart toggle above) */}
        {p.likes > 0 && (
          <button className="sv-likes-link" onClick={() => showPublicationLikers(p.uuid)}>
            {p.likes} like{p.likes === 1 ? "" : "s"}
          </button>
        )}

        {/* Comments */}
        <div className="sv-comments">
          {p.comments?.map(c => (
            <div className="sv-comment" key={c.commentUuid}>
              <div className="sv-cmeta">
                <span className="sv-cname">{c.publisher || "User"}:</span>
                <span className="sv-ctext">{c.comment}</span>
              </div>
              {c.likes > 0 && (
                <button className="sv-likes-link tiny" onClick={() => showCommentLikers(c.commentUuid)}>
                  {c.likes}
                </button>
              )}
              <button
                className={`sv-btn tiny${c.liked ? " liked" : ""}`}
                onClick={() => likeComment(c.commentUuid)}
                aria-label={c.liked ? "Unlike comment" : "Like comment"}
                aria-pressed={c.liked}
              >
                {c.liked ? "❤️" : "🤍"}
              </button>
              {/* Issue #35: on your own post, any comment can be deleted —
                  not just ones you wrote. */}
              {p.own && (
                <button
                  className="sv-btn tiny"
                  title="Delete comment"
                  onClick={() => { if (window.confirm("Delete this comment?")) void deleteComment(c.commentUuid); }}
                >
                  🗑️
                </button>
              )}
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

      {/* Issue #32: new post — a dedicated picker (tag filter + tap to
          select + single Publish action), not the full photo gallery. */}
      <button className="sv-fab" onClick={() => setPickerOpen(true)} aria-label="New post">+</button>
      {pickerOpen && (
        <NewPostPicker
          onCancel={() => setPickerOpen(false)}
          onPosted={() => { setPickerOpen(false); void loadFeed(); }}
        />
      )}

      {/* Image modal */}
      {viewerOpen && (
        <div className="sv-modal" onClick={closeViewer}>
          <div className="sv-modal-body" onClick={(e) => e.stopPropagation()}>
            <button className="sv-close" onClick={closeViewer}>✕</button>
            {viewerURL ? (
              <div
                className="sv-full-wrap"
                onTouchStart={modalSwipe.onTouchStart}
                onTouchEnd={(e) => modalSwipe.onTouchEnd(e, nextImg, prevImg)}
                onClick={(e) => {
                  // Issue #20: click the left/right half to page through,
                  // like swiping — no visible nav buttons.
                  if ((viewerPub?.files.length ?? 0) > 1) navByClickX(e, prevImg, nextImg);
                }}
              >
                <img className="sv-full" src={viewerURL} alt="full" />
                {viewerLoading && <div className="sv-loading">Loading…</div>}
              </div>
            ) : (
              <div className="sv-loading">Loading…</div>
            )}
          </div>
        </div>
      )}

      {/* Likers modal (issue #29) */}
      {likersOpen && (
        <div className="sv-modal" onClick={closeLikers}>
          <div className="sv-modal-body sv-likers-body" onClick={(e) => e.stopPropagation()}>
            <button className="sv-close" onClick={closeLikers}>✕</button>
            <h3 className="sv-likers-title">Likes</h3>
            {likers == null ? (
              <div className="sv-loading-more">Loading…</div>
            ) : likers.length === 0 ? (
              <div className="sv-loading-more">No likes yet</div>
            ) : (
              <ul className="sv-likers-list">
                {likers.map((l, i) => {
                  const avatarURL = bytesToURL(l.image as unknown as Uint8Array, "image/jpeg");
                  return (
                    <li key={`${l.domain}-${i}`}>
                      {avatarURL ? <img src={avatarURL} className="sv-img-avatar" /> : <div className="sv-avatar">👤</div>}
                      <span>{l.name || l.domain}</span>
                    </li>
                  );
                })}
              </ul>
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
