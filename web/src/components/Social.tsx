// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { requestStreamURL } from "../net/media";
import NewPostPicker from "./NewPostPicker";
import type {
  ReqEnvelope,
  RespEnvelope,
  SocialPublications as PbSocialPublications,
  SocialPublication as PbSocialPublication,
  Profile as PbProfile,
  File as PbFile,
} from "../proto/messages";
import "./Social.css";
import Spinner from "./Spinner";

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

// Instagram's feed range: nothing wider than 1.91:1, nothing taller than
// 4:5. A post's media keeps its own shape between those two.
// Issue #114: whether feed videos are muted, shared by every post - once
// someone unmutes, the next video they scroll to stays unmuted rather
// than making them tap again on every post. Module-level and read
// through a subscription so a toggle in one card reaches the rest.
let feedMuted = true;
const feedMutedListeners = new Set<(muted: boolean) => void>();
const setFeedMuted = (muted: boolean) => {
  feedMuted = muted;
  feedMutedListeners.forEach(fn => fn(muted));
};
function useFeedMuted(): [boolean, (muted: boolean) => void] {
  const [muted, setLocal] = useState(feedMuted);
  useEffect(() => {
    feedMutedListeners.add(setLocal);
    return () => { feedMutedListeners.delete(setLocal); };
  }, []);
  return [muted, setFeedMuted];
}

const cFeedMinAspect = 4 / 5;
const cFeedMaxAspect = 1.91;

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

export default function Social({ authenticated, openPubUuid, openCommentUuid, onOpened, onRegisterOpenComposer }: {
  authenticated: boolean;
  // Issue #78: set by a tapped notification to open/scroll to a specific
  // post (and, for a comment-related notification, that comment too).
  openPubUuid?: string | null;
  openCommentUuid?: string | null;
  onOpened?: () => void;
  // The "+" compose button lives in the shared top header now (so it can
  // sit next to the bell instead of floating over the feed) - App.tsx
  // owns that button but has no reason to know about pickerOpen, so this
  // just hands it a function to call instead.
  onRegisterOpenComposer?: (open: () => void) => void;
}) {
  // ---------------- Feed ----------------
  const [feed, setFeed] = useState<PbSocialPublication[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const endReachedRef = useRef(false);
  const loadingMoreRef = useRef(false);
  const feedRef = useRef<PbSocialPublication[]>([]);
  useEffect(() => { feedRef.current = feed; }, [feed]);

  // Issue #78: opening a post from a notification. Briefly highlighted
  // (highlightPub/highlightComment) rather than a persistent style, so it
  // reads as "here's what you tapped" without permanently marking the post.
  const [highlightPub, setHighlightPub] = useState<string | null>(null);
  const [highlightComment, setHighlightComment] = useState<string | null>(null);

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

  // Issue #78: a tapped notification names a post (and maybe a comment on
  // it) to open. Fetches it directly via reqGetPublication if it isn't
  // already among whatever page of the feed happens to be loaded, rather
  // than paging through everything since it, then scrolls to it.
  useEffect(() => {
    if (!openPubUuid) return;
    let cancelled = false;
    (async () => {
      if (!feedRef.current.some(p => p.uuid === openPubUuid)) {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqGetPublication", reqGetPublication: { pubUuid: openPubUuid } };
        });
        if (cancelled) return;
        if (resp.payload?.$case === "respPublication" && resp.payload.respPublication.publication) {
          const pub = resp.payload.respPublication.publication;
          setFeed(prev => prev.some(p => p.uuid === pub.uuid) ? prev : [pub, ...prev]);
        }
      }
      // Double rAF: give React a chance to actually commit the (possibly
      // just-added) post to the DOM before trying to scroll to it - a
      // single rAF after an awaited setFeed isn't reliably post-paint.
      requestAnimationFrame(() => requestAnimationFrame(() => {
        if (cancelled) return;
        document.getElementById(`post-${openPubUuid}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
        setHighlightPub(openPubUuid);
        if (openCommentUuid) {
          setHighlightComment(openCommentUuid);
          document.getElementById(`comment-${openCommentUuid}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
        }
        setTimeout(() => { setHighlightPub(null); setHighlightComment(null); }, 2500);
        onOpened?.();
      }));
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openPubUuid, openCommentUuid]);

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
  // "+" button (now in the shared header, see onRegisterOpenComposer)
  // opens the photo gallery (tag search included) so the user can pick
  // photos and post them, without leaving the social tab.
  const [pickerOpen, setPickerOpen] = useState(false);
  useEffect(() => {
    onRegisterOpenComposer?.(() => setPickerOpen(true));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
  // Issue #60: a video post needs two separate URLs, not one swapped
  // low->hi like an image - viewerPosterURL is the thumbnail (always a
  // JPEG, shown immediately), viewerVideoURL is the actual playable file,
  // set only once the hi-res GetFile below resolves. Rendering a <video>
  // against the JPEG poster URL (or an <img> against the video bytes)
  // would both silently fail, so these can't share viewerURL the way an
  // image's low/hi pair does.
  const [viewerIsVideo, setViewerIsVideo] = useState(false);
  const [viewerPosterURL, setViewerPosterURL] = useState<string | null>(null);
  const [viewerVideoURL, setViewerVideoURL] = useState<string | null>(null);

  const openViewer = useCallback(async (pub: PbSocialPublication, index: number) => {
    setViewerPub(pub);
    setViewerIdx(index);
    setViewerOpen(true);

    const f = pub.files[index];
    const isVideo = (f.mime || "").startsWith("video/");
    setViewerIsVideo(isVideo);

    // f.content is always a JPEG thumbnail (see the isVideo comment near
    // current/lowURL above) - shown immediately either as the low-res
    // image preview, or as a video's poster while its real bytes load.
    const thumb = bytesToURL(f.content as unknown as Uint8Array, "image/jpeg") || null;
    if (isVideo) {
      setViewerPosterURL(thumb);
      setViewerVideoURL(null);
      setViewerURL(null);
    } else {
      setViewerURL(thumb);
      setViewerPosterURL(null);
      setViewerVideoURL(null);
    }

    // then fetch the full file - the actual video bytes for a video, or
    // the hi-res original for an image
    setViewerLoading(true);
    try {
      // Issue #107: same correction as playInline - a publication's files
      // are addressed by hash, never by path.
      // Issue #110: same as the inline player - a video streams from a
      // URL, everything else (and anything the device declines) comes
      // down whole below.
      if (isVideo) {
        const streamURL = await requestStreamURL({ pubUuid: pub.uuid, hash: f.hash });
        if (streamURL) {
          setViewerVideoURL(prev => {
            if (prev && prev.startsWith("blob:") && prev !== streamURL) URL.revokeObjectURL(prev);
            return streamURL;
          });
          return;
        }
      }

      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqGetPublicationMedia",
          reqGetPublicationMedia: { pubUuid: pub.uuid, hash: f.hash },
        };
      });
      if (resp.payload?.$case === "respFile" && resp.payload.respFile.content) {
        const full = bytesToURL(resp.payload.respFile.content as Uint8Array, resp.payload.respFile.mime);
        if (isVideo) {
          setViewerVideoURL(prev => {
            if (prev && prev !== full) URL.revokeObjectURL(prev);
            return full;
          });
        } else {
          setViewerURL(prev => {
            if (prev && prev !== full) URL.revokeObjectURL(prev);
            return full;
          });
        }
      }
    } finally {
      setViewerLoading(false);
    }
  }, []);

  const closeViewer = useCallback(() => {
    setViewerOpen(false);
    setViewerLoading(false);
    if (viewerURL) URL.revokeObjectURL(viewerURL);
    if (viewerPosterURL) URL.revokeObjectURL(viewerPosterURL);
    if (viewerVideoURL) URL.revokeObjectURL(viewerVideoURL);
    setViewerURL(null);
    setViewerPosterURL(null);
    setViewerVideoURL(null);
    setViewerIsVideo(false);
    setViewerPub(null);
  }, [viewerURL, viewerPosterURL, viewerVideoURL]);

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

    // Issue #112: every post's media now sits in one fixed 4:5 box (see
    // .sv-media), so there is nothing per-post left to measure. This used
    // to load every image in a carousel just to find the tallest and pin
    // the card to it - work that is now done by a single CSS rule, and
    // which applies to single-media posts too rather than only carousels.

    // Issue #112 fix-up: the box takes this post's own shape, clamped to
    // the range Instagram allows (nothing wider than 1.91:1, nothing
    // taller than 4:5). Hardcoding 4:5 for every post cropped every
    // landscape photo in the feed into a tall portrait slot.
    //
    // Measured from the first thumbnail, which is a local blob and
    // therefore decodes immediately; one ratio for the whole post so
    // swiping a carousel can't resize the card.
    const [boxAspect, setBoxAspect] = useState<number | null>(null);
    useEffect(() => {
      const first = p.files[0];
      if (!first) return;
      const url = bytesToURL(first.content as unknown as Uint8Array, "image/jpeg");
      if (!url) return;
      let cancelled = false;
      const img = new Image();
      img.onload = () => {
        URL.revokeObjectURL(url);
        if (cancelled || !img.naturalWidth || !img.naturalHeight) return;
        const ratio = img.naturalWidth / img.naturalHeight;
        setBoxAspect(Math.min(Math.max(ratio, cFeedMinAspect), cFeedMaxAspect));
      };
      img.onerror = () => URL.revokeObjectURL(url);
      img.src = url;
      return () => { cancelled = true; };
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [p.uuid]);

    // Issue #109: a swipe moves the images with the finger and snaps when
    // it ends, instead of swapping only once the finger lifted - which
    // made a swipe feel like it had done nothing right up until it
    // suddenly had. Showing the next image arriving means every image in
    // the post has to be on screen, side by side, so they all need a URL.
    const stripURLs = useMemo(
      () => p.files.map(f => bytesToURL(f.content as unknown as Uint8Array, "image/jpeg")),
      // eslint-disable-next-line react-hooks/exhaustive-deps
      [p.uuid]
    );
    useEffect(
      () => () => { stripURLs.forEach(u => { if (u) URL.revokeObjectURL(u); }); },
      [stripURLs]
    );

    // How far the strip is dragged right now, in pixels. Zero whenever a
    // gesture isn't in progress, which is also what re-enables the snap
    // animation (a transition during the drag would lag the finger).
    const [dragDX, setDragDX] = useState(0);
    const dragStartX = useRef<number | null>(null);

    const onStripTouchStart = (e: React.TouchEvent) => {
      if (p.files.length < 2) return;
      dragStartX.current = e.touches[0].clientX;
    };
    const onStripTouchMove = (e: React.TouchEvent) => {
      if (dragStartX.current == null) return;
      let dx = e.touches[0].clientX - dragStartX.current;
      // Resistance at the two ends, so the first and last image can still
      // be pulled a little rather than feeling stuck.
      if ((idx === 0 && dx > 0) || (idx === p.files.length - 1 && dx < 0)) dx /= 3;
      setDragDX(dx);
    };
    const onStripTouchEnd = (e: React.TouchEvent) => {
      if (dragStartX.current == null) return;
      const dx = e.changedTouches[0].clientX - dragStartX.current;
      const width = (e.currentTarget as HTMLElement).getBoundingClientRect().width || 1;
      dragStartX.current = null;
      setDragDX(0);
      // A quarter of the width, rather than a fixed 30px: the same flick
      // should mean the same thing on a phone and on a desktop window.
      if (dx < -width / 4) goRight();
      else if (dx > width / 4) goLeft();
    };

    const current = p.files[idx];
    const isVideo = (current.mime || "").startsWith("video/");
    // current.content is always a server-generated JPEG thumbnail (see
    // files_manager.GetThumbnail), for a video file same as a photo -
    // never pass the file's own mime here, or a video post's Blob gets
    // tagged "video/mp4" over genuinely-JPEG bytes and the browser refuses
    // to render it as an <img> (issue #60).
    const lowURL = useMemo(
      () => bytesToURL(current.content as unknown as Uint8Array, "image/jpeg"),
      // eslint-disable-next-line react-hooks/exhaustive-deps
      [p.uuid, idx]
    );
    // Issue #107: a video in the feed plays where it is. It used to open
    // the full-screen viewer instead - a modal covering the whole timeline
    // to play something that was already on screen, which is not what
    // tapping play should do in a feed. Images still open the viewer:
    // wanting a photo bigger is a real thing to want, wanting a video
    // somewhere else is not.
    //
    // Keyed to this file (path), so paging a multi-file post to a
    // different video doesn't leave the previous one's bytes showing.
    const [inlineVideo, setInlineVideo] = useState<{ path: string; url: string } | null>(null);
    const [inlineLoading, setInlineLoading] = useState(false);
    const [muted, setMuted] = useFeedMuted();
    const mediaRef = useRef<HTMLDivElement | null>(null);
    const videoElRef = useRef<HTMLVideoElement | null>(null);
    // Whatever object URL is on screen has to outlive the fetch that
    // replaced it, hence revoking the previous one rather than the current.
    useEffect(() => () => { if (inlineVideo) URL.revokeObjectURL(inlineVideo.url); },
      // eslint-disable-next-line react-hooks/exhaustive-deps
      []);

    const playInline = async (f: PbFile) => {
      if (inlineLoading) return;
      if (inlineVideo?.path === f.hash) return; // already playing this one
      setInlineLoading(true);
      try {
        // Issue #110: stream it if the device offers a URL for it, so a
        // long clip starts playing immediately instead of after the whole
        // file has come down the socket. A small one it declines, and the
        // whole-file fetch below runs exactly as it did.
        const streamURL = await requestStreamURL({ pubUuid: p.uuid, hash: f.hash });
        if (streamURL) {
          setInlineVideo(prev => {
            // Only a blob URL owns memory that has to be handed back; a
            // streamed one is just an address.
            if (prev?.url.startsWith("blob:")) URL.revokeObjectURL(prev.url);
            return { path: f.hash, url: streamURL };
          });
          return;
        }
        // Issue #107: by hash, via the publication - a feed file has no
        // path at all (social_publications_files stores pos/uuid/hash/
        // mime/size), so the reqGetFile({path}) this used to send was
        // always asking for "", which is why a timeline video never
        // played on any platform.
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = {
            $case: "reqGetPublicationMedia",
            reqGetPublicationMedia: { pubUuid: p.uuid, hash: f.hash },
          };
        });
        if (resp.payload?.$case === "respFile" && resp.payload.respFile.content) {
          // The real file mime here, unlike the thumbnail above - these
          // are the actual video bytes.
          const url = bytesToURL(resp.payload.respFile.content as Uint8Array, resp.payload.respFile.mime);
          if (url) {
            setInlineVideo(prev => {
              if (prev?.url.startsWith("blob:")) URL.revokeObjectURL(prev.url);
              return { path: f.hash, url };
            });
          }
        }
      } finally {
        setInlineLoading(false);
      }
    };

    // React renders `muted` as a property but not as an attribute, and a
    // browser deciding whether to allow autoplay looks at the element
    // before React has necessarily applied it - so set it directly as
    // well, or the very first autoplay of a page load can be refused.
    useEffect(() => {
      if (videoElRef.current) videoElRef.current.muted = muted;
    }, [muted, inlineVideo?.url]);

    // Issue #114: a video starts when you scroll onto it and stops when
    // you leave, so the feed plays itself. 60% visible means "mostly on
    // screen", which is also what stops two videos playing at once -
    // only one post can be that visible at a time.
    useEffect(() => {
      const node = mediaRef.current;
      if (!node || !isVideo) return;
      const obs = new IntersectionObserver(
        entries => {
          const showing = entries[0]?.intersectionRatio ?? 0;
          if (showing >= 0.6) {
            // Already loaded: just resume. Otherwise fetch it, which
            // sets autoPlay on the element that replaces the poster.
            if (videoElRef.current) void videoElRef.current.play().catch(() => {});
            else void playInline(current);
          } else {
            videoElRef.current?.pause();
          }
        },
        { threshold: [0, 0.6, 1] }
      );
      obs.observe(node);
      return () => obs.disconnect();
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [isVideo, current.hash, inlineVideo?.url]);


    const profURL = useMemo(
      () => bytesToURL(p.publisher?.image as unknown as Uint8Array, "image/jpeg"),
      [p.uuid, idx]
    );

    useEffect(() => () => { if (lowURL) URL.revokeObjectURL(lowURL); }, [lowURL]);

    return (
      <article
        id={`post-${p.uuid}`}
        className={`sv-post${p.uuid === highlightPub ? " sv-highlight" : ""}`}
        ref={rootRef as React.RefObject<HTMLElement>}
      >
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

        {/* Issue #112: the box's shape comes from CSS now (a 4:5 feed
            slot), so there is no per-post height to set here - which is
            also what keeps the poster and the player identical. */}
        <div className="sv-media"
             ref={mediaRef}
             style={boxAspect ? { aspectRatio: String(boxAspect) } : undefined}
             onTouchStart={onStripTouchStart}
             onTouchMove={onStripTouchMove}
             onTouchEnd={onStripTouchEnd}>
          {isVideo && inlineVideo?.path === current.hash ? (
            // Issue #107: plays right here, sized exactly like the poster
            // it replaced so the card doesn't jump when it starts.
            <video
              ref={videoElRef}
              className="sv-inline-video"
              src={inlineVideo.url}
              poster={lowURL ?? undefined}
              controls
              autoPlay
              playsInline
              // Issue #114: muted is not a preference here, it is what
              // makes autoplay possible at all - every browser blocks
              // an unmuted video that starts on its own. The speaker
              // button below is how sound gets turned on, and doing it
              // from a real tap is what the browser requires.
              muted={muted}
            />
          ) : lowURL ? (
            <div
              className="sv-strip"
              style={{
                transform: `translateX(calc(${-idx * 100}% + ${dragDX}px))`,
                transition: dragDX === 0 ? "transform 0.25s ease-out" : "none",
              }}
            >
              {p.files.map((f, i) => (
                <img
                  key={`${f.hash}-${i}`}
                  className={`sv-slide${(f.mime || "").startsWith("video/") ? " is-video" : ""}`}
                  src={stripURLs[i] || lowURL}
                  alt={f.path}
                  onClick={(e) => {
                    // Issue #20: click the left/right quarter of a
                    // multi-image post to page through it (no visible
                    // buttons) — the middle half still opens the
                    // full-screen viewer, which is also where a video
                    // post's thumbnail (its poster, tapped here) actually
                    // starts playing (issue #60).
                    if (p.files.length > 1) {
                      const rect = e.currentTarget.getBoundingClientRect();
                      const x = e.clientX - rect.left;
                      if (x < rect.width * 0.25) { goLeft(); return; }
                      if (x > rect.width * 0.75) { goRight(); return; }
                    }
                    if ((f.mime || "").startsWith("video/")) { void playInline(f); return; }
                    openViewer(p, i);
                  }}
                />
              ))}
            </div>
          ) : (
            <div className="sv-media-ph">🖼️</div>
          )}
          {isVideo && inlineVideo?.path !== current.hash && (
            <div className="sv-video-badge" aria-hidden={!inlineLoading}>
              {inlineLoading ? <Spinner /> : "▶"}
            </div>
          )}
          {isVideo && (
            <button
              className="sv-mute"
              onClick={(e) => {
                e.stopPropagation();
                setMuted(!muted);
              }}
              aria-label={muted ? "Unmute video" : "Mute video"}
            >
              {muted ? "🔇" : "🔊"}
            </button>
          )}
          {/* Issue #68: the iOS app already shows a dot per image (current
              one solid, the rest dimmed) over a multi-image post - the web
              feed had the exact same swipe/tap paging (goLeft/goRight
              above) but nothing on screen showing there even *was* more
              than one image, let alone which one you were on. */}
          {p.files.length > 1 && (
            <div className="sv-dots" aria-hidden="true">
              {p.files.map((_, i) => (
                <span key={i} className={`sv-dot${i === idx ? " active" : ""}`} />
              ))}
            </div>
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
            <div
              id={`comment-${c.commentUuid}`}
              className={`sv-comment${c.commentUuid === highlightComment ? " sv-highlight" : ""}`}
              key={c.commentUuid}
            >
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
          select + single Publish action), not the full photo gallery. The
          "+" that opens it now lives in the shared header (see
          onRegisterOpenComposer above) rather than floating over the feed
          - signed-out visitors have nothing to post with, so App.tsx only
          ever wires that button up once authenticated. */}

      {authenticated && pickerOpen && (
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
            {viewerIsVideo ? (
              <div className="sv-full-wrap">
                {viewerVideoURL ? (
                  // Real bytes are in - hand every click straight to the
                  // native video controls, unlike the image case below
                  // which uses clicks for prev/next paging.
                  <video className="sv-full" src={viewerVideoURL} poster={viewerPosterURL ?? undefined} controls autoPlay />
                ) : viewerPosterURL ? (
                  <img className="sv-full" src={viewerPosterURL} alt="video loading" />
                ) : null}
                {viewerLoading && !viewerVideoURL && <div className="sv-loading">Loading…</div>}
                {(viewerPub?.files.length ?? 0) > 1 && (
                  <div className="sv-dots" aria-hidden="true">
                    {viewerPub!.files.map((_, i) => (
                      <span key={i} className={`sv-dot${i === viewerIdx ? " active" : ""}`} />
                    ))}
                  </div>
                )}
              </div>
            ) : viewerURL ? (
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
                {(viewerPub?.files.length ?? 0) > 1 && (
                  <div className="sv-dots" aria-hidden="true">
                    {viewerPub!.files.map((_, i) => (
                      <span key={i} className={`sv-dot${i === viewerIdx ? " active" : ""}`} />
                    ))}
                  </div>
                )}
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
