// SPDX-License-Identifier: AGPL-3.0-or-later

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { requestStreamURL, canStream } from "../net/media";
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
import LowResBadge from "./LowResBadge";
import Spinner from "./Spinner";
import { startDownload, pubMediaKey, extensionForMime, leafName, useDownloadState, useDownloadFailure, useDownloadHost, useDownloadToldByHost } from "./mediaDownload";
import { DownloadButton, DownloadNote } from "./DownloadButton";

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

// A post is memoised and re-renders only when its own data changes, so
// nothing else would move "just now" on to "5m ago". One shared tick a
// minute, running only while some post date is mounted, re-renders just
// the date labels.
const cPostDateTickMs = 60_000;
const postDateListeners = new Set<() => void>();
let postDateTimer: ReturnType<typeof setInterval> | null = null;
function subscribePostDateTick(fn: () => void): () => void {
  postDateListeners.add(fn);
  if (postDateTimer == null) {
    postDateTimer = setInterval(() => postDateListeners.forEach(l => l()), cPostDateTickMs);
  }
  return () => {
    postDateListeners.delete(fn);
    if (postDateListeners.size === 0 && postDateTimer != null) {
      clearInterval(postDateTimer);
      postDateTimer = null;
    }
  };
}
function PostDate({ d }: { d: Date }) {
  const [, setTick] = useState(0);
  useEffect(() => subscribePostDateTick(() => setTick(t => t + 1)), []);
  return <div className="sv-post-date">{formatPostDate(d)}</div>;
}

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

// A post's media box takes the shape of its first item, made no wider
// than Instagram's 1.91:1 so a panorama doesn't shrink to a strip. There
// is no lower bound any more: the box's height is capped by the room the
// window has for it (Social.css's --sv-media-room), so a tall photo or a
// 9:16 video gets the whole width it can have while still fitting on
// screen. The old 4:5 floor made a 9:16 video on a phone 70% of the
// width, with wide bars, to keep the caption on screen.
const cFeedMaxAspect = 1.91;

// Width / height of a JPEG, read from its frame header - no decoding, so
// the box has its final shape on the very first render and never jumps
// once an image has loaded. Thumbnails are the device's own JPEGs (Go's
// encoder, orientation already applied, no EXIF), so the frame header is
// the picture's shape. null when the bytes aren't a JPEG it can read.
function jpegAspect(bytes?: Uint8Array): number | null {
  if (!bytes || bytes.length < 4 || bytes[0] !== 0xff || bytes[1] !== 0xd8) return null;
  let i = 2;
  while (i + 8 < bytes.length) {
    if (bytes[i] !== 0xff) return null;
    const marker = bytes[i + 1];
    if (marker === 0xff) { i += 1; continue; } // fill byte
    if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd8)) { i += 2; continue; } // no length
    if (marker === 0xd9 || marker === 0xda) return null; // image data before any frame header
    // Start of frame: every SOFn but DHT (c4), JPG (c8) and DAC (cc).
    if (marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc) {
      const h = (bytes[i + 5] << 8) | bytes[i + 6];
      const w = (bytes[i + 7] << 8) | bytes[i + 8];
      return w > 0 && h > 0 ? w / h : null;
    }
    i += 2 + ((bytes[i + 2] << 8) | bytes[i + 3]);
  }
  return null;
}

function bytesToURL(bytes?: Uint8Array, mime = "application/octet-stream") {
  if (!bytes || bytes.length === 0) return null;
  const blob = new Blob([bytes], { type: mime });
  return URL.createObjectURL(blob);
}
const revokeAll = (urls: Set<string>) => {
  urls.forEach(u => URL.revokeObjectURL(u));
  urls.clear();
};

// ---- Download ---------------------------------------------------------------
// A post's photo or video is saved as the device keeps it for the post,
// from the pop-up and from the post itself (a video plays in the feed and
// never opens the pop-up). mediaDownload.ts does the saving: a video the
// device streams goes straight from its media link to disk, anything else
// comes down with GetPublicationMedia. The same for the owner's posts and
// friends' (their media is copied to this device when it syncs them).

// What the pop-up opens with: Tab moves between these inside it. A video's
// controls are stops of their own that can't be focused from here, and
// while one has focus the video is the active element.
const cFocusable = "button, [href], input, video[controls], [tabindex]:not([tabindex='-1'])";

// A file name without what file systems refuse (/ \ : * ? " < > |, control
// characters) or a leading dot, its spaces collapsed.
function cleanFileName(s: string): string {
  return [...s].map(c => (c < " " || c === "\x7f" || '/\\:*?"<>|'.includes(c) ? " " : c)).join("")
    .replace(/\s+/g, " ").trim().replace(/^\.+/, "").trim();
}
const pad2 = (n: number) => String(n).padStart(2, "0");

// A name the post's item carries itself (none do today), with its
// extension; "" when it has none.
function ownMediaName(f: PbFile | undefined): string {
  const own = f?.path ? cleanFileName(leafName(f.path)) : "";
  return own && own !== f?.hash && /\.[A-Za-z0-9]{1,5}$/.test(own) ? own : "";
}

// What a post's photo or video is saved as. A post's media has no name of
// its own (the device keeps its hash and type), so it is named after the
// post: "Ana 2026-10-07.jpg", or "Ana 2026-10-07 2.mp4" for the second of
// several - a name the post does carry wins. The publisher's name is cut
// by characters, not UTF-16 units, so an emoji isn't cut in half.
function postMediaName(p: PbSocialPublication, index: number): string {
  const f = p.files[index];
  const own = ownMediaName(f);
  if (own) return own;
  const who = Array.from(cleanFileName(p.publisher?.name || "")).slice(0, 60).join("").trim()
    || cleanFileName(p.publisher?.domain || "") || "Post";
  const d = p.dateTime;
  const date = d ? `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}` : "";
  const num = p.files.length > 1 ? String(index + 1) : "";
  return [who, date, num].filter(Boolean).join(" ") + extensionForMime(f?.mime);
}

// Saves a post's item. cached: its bytes, when the pop-up already has
// them whole (a full-size photo it is showing, or a video it fetched
// whole because the device didn't stream it), so they aren't sent again
// and no media link is asked for.
function downloadPostMedia(p: PbSocialPublication, index: number, cached?: Blob | null) {
  const f = p.files[index];
  if (!f) return;
  const pubUuid = p.uuid, hash = f.hash;
  void startDownload({
    key: pubMediaKey(pubUuid, hash),
    name: postMediaName(p, index),
    stream: !cached && canStream(f.mime) ? { pubUuid, hash } : null,
    // A type that names no extension (application/octet-stream): the
    // bytes tell it.
    extFromBytes: !ownMediaName(f) && !extensionForMime(f.mime),
    read: async (progress) => {
      if (cached) return [cached];
      // One answer, the whole file: no pieces to count.
      progress(0, 0);
      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqGetPublicationMedia", reqGetPublicationMedia: { pubUuid, hash } };
      });
      if (resp.payload?.$case !== "respFile" || resp.payload.respFile.content == null) return null;
      return [resp.payload.respFile.content as BlobPart];
    },
  });
}

// Viewer steps closer together than this are a held arrow key: the
// full-size fetch waits this long and is skipped if the viewer has moved
// on by then (MediaViewer does the same).
const cRapidStepMs = 250;

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
  // Whether the first page has answered - an empty feed before that is
  // "still loading", not "nothing here", and must not show the invitation.
  const [loaded, setLoaded] = useState(false);
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
    setLoaded(true);
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
        // Its header just under the top bar (.sv-post's scroll-margin),
        // which is where its media is sized to fit whole. Centred, a
        // post taller than the window had the top of its photo under
        // the bar.
        document.getElementById(`post-${openPubUuid}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
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
          publisher: publisherName, likes: 0, liked: false, dateTime: undefined, own: true,
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
  // the "own post" rule regardless of who wrote the comment). Issue #174:
  // also your own comment on a friend's post - the friend's device applies
  // the deletion because the comment is yours.
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
  // One avatar URL per liker while the list is up, freed with it - not a
  // new, never-freed one for every liker on every render of Social.
  const likerURLs = useMemo(
    () => (likers ?? []).map(l => bytesToURL(l.image as unknown as Uint8Array, "image/jpeg")),
    [likers]
  );
  useEffect(() => () => likerURLs.forEach(u => { if (u) URL.revokeObjectURL(u); }), [likerURLs]);

  // ---------------- Image viewer (modal) ----------------
  const [viewerOpen, setViewerOpen] = useState(false);
  const [viewerPub, setViewerPub] = useState<PbSocialPublication | null>(null);
  const [viewerIdx, setViewerIdx] = useState(0);
  const [viewerURL, setViewerURL] = useState<string | null>(null);
  const [viewerLoading, setViewerLoading] = useState(false);
  // Whether the photo shown is its full-size original yet (Low res badge).
  const [viewerHiRes, setViewerHiRes] = useState(false);
  const viewerImgRef = useRef<HTMLImageElement>(null);
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
  // Which item the viewer is on: every step (and closing) moves it on, so
  // a full-size reply for an item already paged past is dropped. The
  // device answers out of order, and one used to land under the next
  // item's dot, marked hi-res, or clear its "Loading" early.
  const viewerGenRef = useRef(0);
  // The blobs the viewer is showing (thumbnail or poster, full size),
  // freed together when it steps to another item or closes - paging
  // used to drop each full-size photo or clip without revoking it.
  // Stream URLs own nothing and never go in here.
  const viewerBlobsRef = useRef<Set<string>>(new Set());
  // When the previous item was opened (see cRapidStepMs).
  const lastViewerStepRef = useRef(0);
  // The full-size bytes of the item on screen, once they are in (a photo,
  // or a clip too short to stream), so Download saves them without
  // fetching them again. Let go with the item's blobs.
  const viewerFullRef = useRef<{ key: string; blob: Blob } | null>(null);
  useEffect(() => {
    const blobs = viewerBlobsRef.current;
    return () => { viewerGenRef.current += 1; revokeAll(blobs); viewerFullRef.current = null; };
  }, []);

  const openViewer = useCallback(async (pub: PbSocialPublication, index: number) => {
    const gen = ++viewerGenRef.current;
    const current = () => gen === viewerGenRef.current;
    // The previous item's blobs leave the screen with this update.
    revokeAll(viewerBlobsRef.current);
    viewerFullRef.current = null;
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
    if (thumb) viewerBlobsRef.current.add(thumb);
    if (isVideo) {
      setViewerPosterURL(thumb);
      setViewerVideoURL(null);
      setViewerURL(null);
    } else {
      setViewerURL(thumb);
      setViewerPosterURL(null);
      setViewerVideoURL(null);
    }
    setViewerHiRes(false);

    // then fetch the full file - the actual video bytes for a video, or
    // the hi-res original for an image
    setViewerLoading(true);
    // A post's media wraps around, so a held arrow key fetched the same
    // originals over and over: a step within cRapidStepMs of the last
    // waits, and fetches only if the viewer is still on it.
    const now = Date.now();
    const rapid = now - lastViewerStepRef.current < cRapidStepMs;
    lastViewerStepRef.current = now;
    if (rapid) {
      await new Promise(r => setTimeout(r, cRapidStepMs));
      if (!current()) return;
    }
    try {
      // Issue #107: same correction as playInline - a publication's files
      // are addressed by hash, never by path.
      // Issue #110: same as the inline player - a video streams from a
      // URL, everything else (and anything the device declines) comes
      // down whole below.
      if (isVideo) {
        const streamURL = await requestStreamURL({ pubUuid: pub.uuid, hash: f.hash });
        if (!current()) return;
        if (streamURL) {
          setViewerVideoURL(streamURL);
          return;
        }
      }

      const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
        (e as any).payload = {
          $case: "reqGetPublicationMedia",
          reqGetPublicationMedia: { pubUuid: pub.uuid, hash: f.hash },
        };
      });
      if (!current()) return;
      if (resp.payload?.$case === "respFile" && resp.payload.respFile.content?.length) {
        const blob = new Blob([resp.payload.respFile.content as BlobPart], { type: resp.payload.respFile.mime || "application/octet-stream" });
        const full = URL.createObjectURL(blob);
        viewerBlobsRef.current.add(full);
        viewerFullRef.current = { key: pubMediaKey(pub.uuid, f.hash), blob };
        if (isVideo) {
          setViewerVideoURL(full);
        } else {
          setViewerURL(full);
          setViewerHiRes(true);
        }
      }
    } finally {
      if (current()) setViewerLoading(false);
    }
  }, []);

  const closeViewer = useCallback(() => {
    viewerGenRef.current += 1;
    revokeAll(viewerBlobsRef.current);
    viewerFullRef.current = null;
    setViewerOpen(false);
    setViewerLoading(false);
    setViewerURL(null);
    setViewerPosterURL(null);
    setViewerVideoURL(null);
    setViewerIsVideo(false);
    setViewerPub(null);
  }, []);

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

  // Download, from the pop-up: the item on screen, saved from the bytes
  // it already has when it has them. Its progress is mediaDownload's, by
  // post and file, so the post's own button shows the same download.
  const viewerFile = viewerPub?.files[viewerIdx];
  const viewerKey = viewerPub && viewerFile ? pubMediaKey(viewerPub.uuid, viewerFile.hash) : "";
  const viewerSave = useDownloadState(viewerKey);
  const downloadFailed = useDownloadFailure();
  // A failure's note shows in the pop-up while it is open, over the page
  // otherwise (as the library viewer's does); and the item it shows has
  // its download told here, not by its post behind.
  useDownloadHost(viewerOpen, viewerKey);
  const downloadViewerItem = useCallback(() => {
    if (!viewerPub) return;
    const key = pubMediaKey(viewerPub.uuid, viewerPub.files[viewerIdx]?.hash ?? "");
    downloadPostMedia(viewerPub, viewerIdx, viewerFullRef.current?.key === key ? viewerFullRef.current.blob : null);
  }, [viewerPub, viewerIdx]);

  // The keyboard's focus moves into the pop-up, so Space or Enter can't
  // press what is behind it, and goes back once it closes - after
  // keyboard use only, as the library viewer does.
  const viewerBoxRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!viewerOpen) return;
    const el = document.activeElement;
    const back = el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
    if (!viewerBoxRef.current?.contains(el)) viewerBoxRef.current?.focus({ preventScroll: true });
    return () => { if (back?.isConnected) back.focus({ preventScroll: true }); };
  }, [viewerOpen]);

  // The pop-up's Tab stops, in order (cFocusable), without the guard.
  const viewerGuardRef = useRef<HTMLSpanElement>(null);
  const viewerStops = useCallback(() => {
    const box = viewerBoxRef.current;
    if (!box) return [];
    return [...box.querySelectorAll<HTMLElement>(cFocusable)].filter((el) => el.offsetParent !== null && el !== viewerGuardRef.current);
  }, []);
  // The guard, the pop-up's last stop: Tab on from a video's last control
  // (which can't be told from its others) lands on it, and it hands focus
  // round to the first stop - unless it is only where a Shift+Tab onto the
  // video starts from (guardPassRef).
  const guardPassRef = useRef(false);
  const onViewerGuardFocus = useCallback(() => {
    if (guardPassRef.current) { guardPassRef.current = false; return; }
    viewerStops()[0]?.focus();
  }, [viewerStops]);
  // Whether the keyboard, not a pointer, moved focus last: arrow keys on a
  // video reached with Tab are the video's own (they seek), not paging. A
  // click on the video focuses it too, and arrows page from there as
  // before.
  const viewerKeysRef = useRef(false);

  // keyboard when modal open: arrows page, Escape closes, Tab stays in it.
  useEffect(() => {
    if (!viewerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      const box = viewerBoxRef.current;
      const active = document.activeElement;
      const inVideo = viewerKeysRef.current && active instanceof HTMLVideoElement && !!box?.contains(active);
      if (e.key === "ArrowRight" && !inVideo) { e.preventDefault(); nextImg(); }
      else if (e.key === "ArrowLeft" && !inVideo) { e.preventDefault(); prevImg(); }
      else if (e.key === "Escape") { e.preventDefault(); closeViewer(); }
      if (!box) return;
      // Space on the pop-up itself would scroll the feed behind it.
      if (e.key === " " && e.target === box) e.preventDefault();
      if (e.key !== "Tab") return;
      viewerKeysRef.current = true;
      const all = viewerStops();
      if (!all.length) { e.preventDefault(); box.focus(); return; }
      const first = all[0];
      const last = all[all.length - 1];
      // A video last: Tab from it is the browser's, through its controls
      // and on to the guard; Shift+Tab onto it goes back from the guard,
      // to its last control.
      const guard = last instanceof HTMLVideoElement ? viewerGuardRef.current : null;
      if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (e.shiftKey && (active === first || active === box)) {
        if (guard) { guardPassRef.current = true; guard.focus(); }
        else { e.preventDefault(); last.focus(); }
      }
      else if (!e.shiftKey && active === last && !guard) { e.preventDefault(); first.focus(); }
    };
    const onPointer = () => { viewerKeysRef.current = false; };
    window.addEventListener("keydown", onKey);
    window.addEventListener("pointerdown", onPointer, true);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("pointerdown", onPointer, true);
    };
  }, [viewerOpen, nextImg, prevImg, closeViewer, viewerStops]);

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

  // The feed's top, from the top of the page (Social.css's --sv-feed-top):
  // what the first post's media leaves room for, so it is whole on screen
  // at load even with the critical-update banner above the page. Read again
  // whenever anything the feed sits in changes size - the banner coming or
  // going, or wrapping differently at a new width, resizes .app-body. A
  // frame later, not in the observer's callback: the new value resizes the
  // first post and with it every element observed here, which inside the
  // callback would be a ResizeObserver loop.
  const feedElRef = useRef<HTMLDivElement | null>(null);
  useLayoutEffect(() => {
    const el = feedElRef.current;
    if (!el) return;
    let last = "", raf = 0;
    const measure = () => {
      const top = `${el.getBoundingClientRect().top + window.scrollY}px`;
      if (top !== last) el.style.setProperty("--sv-feed-top", (last = top));
    };
    measure();
    const ro = new ResizeObserver(() => { cancelAnimationFrame(raf); raf = requestAnimationFrame(measure); });
    for (let a = el.parentElement; a; a = a.parentElement) ro.observe(a);
    return () => { ro.disconnect(); cancelAnimationFrame(raf); };
  }, []);

  // Every callback here is stable, so this is too - see Post.
  const postActions = useMemo<PostActions>(() => ({
    loadMoreIfNeeded, likePublication, likeComment, addComment, deletePublication,
    deleteComment, showPublicationLikers, showCommentLikers, openViewer,
  }), [
    loadMoreIfNeeded, likePublication, likeComment, addComment, deletePublication,
    deleteComment, showPublicationLikers, showCommentLikers, openViewer,
  ]);

  return (
    <div className="sv-wrap">
      {/* Right: feed */}
      <div className="sv-feed" ref={feedElRef}>
        {/* Issue #79: an empty timeline invites the first post instead of
            being blank. Only for the owner - a visitor with nothing to see
            gets nothing to do about it either. */}
        {authenticated && loaded && feed.length === 0 && (
          <div className="sv-empty">
            <h2>No social posts</h2>
            <p>Share a photo or a video with your friends — it goes from your device to theirs, with no cloud in between.</p>
            <button className="sv-empty-new" onClick={() => setPickerOpen(true)} aria-label="New post">
              {/* Drawn, not typed: a text "+" sits on the font's baseline,
                  visibly below centre in a circle this size. */}
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 5v14M5 12h14" stroke="currentColor" strokeWidth="3" strokeLinecap="round" fill="none" />
              </svg>
            </button>
          </div>
        )}
        {feed.map((p, i) => (
          <Post
            key={p.uuid}
            p={p}
            highlighted={p.uuid === highlightPub}
            highlightComment={highlightComment}
            armPagination={i >= feed.length - 2 && !loadingMore}
            actions={postActions}
          />
        ))}
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
          <div
            ref={viewerBoxRef}
            className="sv-modal-body sv-viewer-body"
            role="dialog"
            aria-modal="true"
            aria-label={`${viewerIsVideo ? "Video" : "Photo"} by ${viewerPub?.publisher?.name || "User"}`
              + ((viewerPub?.files.length ?? 0) > 1 ? `, ${viewerIdx + 1} of ${viewerPub!.files.length}` : "")}
            tabIndex={-1}
            onClick={(e) => e.stopPropagation()}
          >
            {/* A header row of its own, as the library viewer has (issue
                #130): Close used to float over the picture's corner,
                where the picture covered it. */}
            <div className="sv-modal-hdr">
              <span className="sv-modal-title">
                {viewerPub?.publisher?.name || "User"}
                {viewerPub?.dateTime && <span className="sv-modal-date"> · {formatPostDate(viewerPub.dateTime)}</span>}
              </span>
              {viewerPub && viewerFile && (
                // The post's copy (a HEIC as the JPEG it was posted as, a
                // long video as cut and shrunk for the post), not the
                // library's original.
                <DownloadButton
                  save={viewerSave}
                  onClick={downloadViewerItem}
                  what={postMediaName(viewerPub, viewerIdx)}
                  title={`Download this ${(viewerFile.mime || "").startsWith("video/") ? "video" : "photo"}`}
                />
              )}
              <button type="button" className="sv-close" title="Close" aria-label="Close" onClick={closeViewer}>✕</button>
            </div>
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
                <img className="sv-full" ref={viewerImgRef} src={viewerURL} alt="full" />
                {!viewerHiRes && <LowResBadge imgRef={viewerImgRef} loading={viewerLoading} />}
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
            <DownloadNote failed={downloadFailed} />
            {/* Last of all: see onViewerGuardFocus. */}
            <span ref={viewerGuardRef} className="pg-modal-sr" tabIndex={0} onFocus={onViewerGuardFocus} />
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
                  const avatarURL = likerURLs[i];
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

// One post of the feed. Its own component at module level: declared inside
// Social it was a new component type on every Social render, so a like, a
// page load, a highlight or any App re-render unmounted and remounted
// every post - a comment being typed was wiped, a carousel jumped back to
// its first image, and a playing video restarted and was downloaded again.
// Memoised: with stable actions, a post re-renders only when its own data,
// highlight or pagination role changes.
type PostActions = {
  loadMoreIfNeeded: (pubUuid: string) => Promise<void>;
  likePublication: (pubUuid: string) => Promise<void>;
  likeComment: (commentUuid: string) => Promise<void>;
  addComment: (pubUuid: string, text: string, publisherName: string) => Promise<void>;
  deletePublication: (pubUuid: string) => Promise<void>;
  deleteComment: (commentUuid: string) => Promise<void>;
  showPublicationLikers: (pubUuid: string) => Promise<void>;
  showCommentLikers: (commentUuid: string) => Promise<void>;
  openViewer: (pub: PbSocialPublication, index: number) => Promise<void>;
};

const Post = memo(function Post({ p, highlighted, highlightComment, armPagination, actions }: {
  p: PbSocialPublication;
  // Issue #78: the post (and comment) a tapped notification opened.
  highlighted: boolean;
  highlightComment: string | null;
  // One of the last two posts while no page is loading (see below).
  armPagination: boolean;
  actions: PostActions;
}) {
  const {
    loadMoreIfNeeded, likePublication, likeComment, addComment, deletePublication,
    deleteComment, showPublicationLikers, showCommentLikers, openViewer,
  } = actions;
  const [idx, setIdx] = useState(0);
  const rootRef = useRef<HTMLElement | null>(null);

  // Trigger the next page fetch once this post (one of the last two
  // currently loaded) actually scrolls into view, mirroring the iOS
  // app's "trigger near the end of the list" pagination (issue #15).
  // Re-armed each time a page finishes loading: a new observer reports
  // at once, so a page that failed or brought nothing new is asked for
  // again while the end of the feed is still in view.
  useEffect(() => {
    if (!armPagination) return;
    const el = rootRef.current;
    if (!el) return;
    const obs = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) void loadMoreIfNeeded(p.uuid);
    }, { rootMargin: "600px" });
    obs.observe(el);
    return () => obs.disconnect();
  }, [p.uuid, armPagination, loadMoreIfNeeded]);

  const goLeft = () => setIdx(i => (i - 1 + p.files.length) % p.files.length);
  const goRight = () => setIdx(i => (i + 1) % p.files.length);

  // Issue #112 fix-up: the box takes this post's own shape (no wider
  // than 1.91:1, see cFeedMaxAspect) - hardcoding 4:5 for every post
  // cropped every landscape photo in the feed into a tall portrait slot.
  // One ratio for the whole post, the first item's, so swiping a
  // carousel can't resize the card. How tall it may get is CSS's job
  // (.sv-media's max-height: never taller than the window has room for).
  //
  // Every item's own shape is read from its thumbnail's bytes up front,
  // so the box is right from the first render. The current item's shape
  // also places the controls drawn over it (.sv-frame) and sizes the
  // player exactly where its poster was.
  const ratios = useMemo(
    () => p.files.map(f => jpegAspect(f.content as unknown as Uint8Array)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [p.uuid]
  );
  // Only for a first thumbnail the header couldn't be read from: measured
  // by decoding it (a local blob, so quickly), 4:5 until then.
  const [measuredAspect, setMeasuredAspect] = useState<number | null>(null);
  const firstUnreadable = p.files.length > 0 && ratios[0] == null;
  useEffect(() => {
    if (!firstUnreadable) return;
    const url = bytesToURL(p.files[0].content as unknown as Uint8Array, "image/jpeg");
    if (!url) return;
    let cancelled = false;
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      if (cancelled || !img.naturalWidth || !img.naturalHeight) return;
      setMeasuredAspect(img.naturalWidth / img.naturalHeight);
    };
    img.onerror = () => URL.revokeObjectURL(url);
    img.src = url;
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.uuid, firstUnreadable]);
  const firstAspect = ratios[0] ?? measuredAspect;
  const boxAspect = firstAspect ? Math.min(firstAspect, cFeedMaxAspect) : null;

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
  // Download, for the item on screen: a video plays right here and never
  // opens the pop-up, so this is where it is saved from (photos too, as
  // in the pop-up). Re-renders this post only when its own download moves.
  const dlKey = pubMediaKey(p.uuid, current.hash);
  const dlSave = useDownloadState(dlKey);
  const dlToldByPopup = useDownloadToldByHost(dlKey);
  const dlWhat = `${isVideo ? "video" : "photo"}${p.files.length > 1 ? ` ${idx + 1} of ${p.files.length}` : ""}`;
  // Unknown, the frame is the whole box (see .sv-frame).
  const currentAspect = ratios[idx] ?? (idx === 0 ? measuredAspect : null);
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
  // Shows the replay button. Set when the clip runs out, cleared by the
  // element's own play event - which covers replaying it and scrolling
  // back onto it alike, since play() on a finished video seeks to the
  // start by itself.
  const [ended, setEnded] = useState(false);
  const [muted, setMuted] = useFeedMuted();
  const mediaRef = useRef<HTMLDivElement | null>(null);
  const videoElRef = useRef<HTMLVideoElement | null>(null);
  // Whatever object URL is on screen has to outlive the fetch that
  // replaced it, hence revoking the previous one rather than the current.
  // The one still showing is freed on unmount, read through a ref: a
  // cleanup set up on mount only ever saw that render's null, and leaked
  // every clip fetched whole.
  const inlineURLRef = useRef<string | null>(null);
  useEffect(() => { inlineURLRef.current = inlineVideo?.url ?? null; }, [inlineVideo?.url]);
  useEffect(() => () => {
    const u = inlineURLRef.current;
    if (u?.startsWith("blob:")) URL.revokeObjectURL(u);
  }, []);

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


  // Made again only when a refresh brings new image bytes (a like or a
  // comment keeps the same publisher object), not on every carousel step,
  // and freed when replaced.
  const publisherImage = p.publisher?.image;
  const profURL = useMemo(
    () => bytesToURL(publisherImage as unknown as Uint8Array, "image/jpeg"),
    [publisherImage]
  );
  useEffect(() => () => { if (profURL) URL.revokeObjectURL(profURL); }, [profURL]);

  useEffect(() => () => { if (lowURL) URL.revokeObjectURL(lowURL); }, [lowURL]);

  return (
    <article
      id={`post-${p.uuid}`}
      className={`sv-post${highlighted ? " sv-highlight" : ""}`}
      ref={rootRef as React.RefObject<HTMLElement>}
    >
      <header className="sv-post-hdr">
        {profURL && <img src={profURL} className="sv-img-avatar" /> || <div className="sv-avatar">👤</div> }
        <div className="sv-pub-meta">
          <div className="sv-publisher">{p.publisher?.name || "User"}</div>
          {p.dateTime && <PostDate d={p.dateTime} />}
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

      {/* The box's shape is the post's (boxAspect); its height cap and
          the frame of the current item (--sv-r) are Social.css's. The
          poster and the player are both sized from that one ratio,
          which is what keeps them identical (issue #112). */}
      <div className="sv-media"
           ref={mediaRef}
           style={{
             ...(boxAspect ? { aspectRatio: String(boxAspect) } : {}),
             ...(currentAspect ? { "--sv-r": String(currentAspect) } : {}),
           } as React.CSSProperties}
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
            onEnded={() => setEnded(true)}
            onPlay={() => setEnded(false)}
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
                className="sv-slide"
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
        {isVideo && ended && (
          <button
            className="sv-replay"
            onClick={e => {
              e.stopPropagation();
              const el = videoElRef.current;
              if (!el) return;
              el.currentTime = 0;
              void el.play().catch(() => {});
            }}
            aria-label="Replay video"
          >
            ↺
          </button>
        )}
        {isVideo && (
          // The speaker sits on the picture's corner, not out in the
          // bars beside a tall video: .sv-frame is the rectangle the
          // current item is actually drawn in.
          <div className="sv-frame">
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
          </div>
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
        <DownloadButton
          cls="sv-dl"
          className="sv-btn"
          save={dlSave}
          onClick={() => downloadPostMedia(p, idx)}
          what={postMediaName(p, idx)}
          title={`Download this ${dlWhat}`}
          label={`Download ${dlWhat}`}
          announce={!dlToldByPopup}
        />
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
                not just ones you wrote. Issue #174: and your own comment
                anywhere (not the optimistic placeholder, which has no
                real uuid yet). */}
            {(p.own || c.own) && !c.commentUuid.startsWith("pending-") && (
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
});


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
