// SPDX-License-Identifier: AGPL-3.0-or-later

// Images: every photo and video on the device as a wall of square tiles,
// newest first under month titles - or what the top bar's search, a person
// or an open collection narrows it to (photoFilter.ts). Photos are picked
// Google Photos style: the circle on a tile (shown on hover), or a long
// press on a touch screen, then a tap anywhere on a tile. A bar over the
// top bar then posts them, puts them in a collection, shares or deletes
// them. A collection is an image group in the protocol (ImageGroup,
// groupId), so the code says group where it talks to the device.
import { memo, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, ReactNode, Ref, SyntheticEvent } from "react";
import { flushSync } from "react-dom";
import { useWS } from "../net/useWS";
import type { DeepPartial, File as MsgFile, ImageGroup, Person, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { fileSize } from "../net/fileSize";
import "./PhotoGallery.css";
import Spinner from "./Spinner";
import SharedGalleryShare from "./SharedGalleryShare";
import MediaViewer from "./MediaViewer";
import DateScrubber, { type ScrubBucket } from "./DateScrubber";
import { CollectionsIcon, ImagesIcon } from "./NavIcons";
import { useObjectURLs } from "./useObjectURLs";
import { usePageRetry } from "./usePageRetry";
import { clearSearch, isDateOrdered, isFiltered, leaveGroup, openGroup, updateOpenGroup, usePhotoFilter, type OpenGroup } from "./photoFilter";
import { dropGroupLocally, personLabel, reloadGroups, renameGroupLocally, useGroups, usePeople } from "./libraryStore";

// A search's first page asks for only this many photos (SearchPhotos.limit):
// a full page of ~30 thumbnails took 5-45 s over a slow home upload, and the
// grid stayed empty all that time. The pages after it, sent with the token,
// get the device's own size; a device before release 97 answers 30 anyway.
const cFirstPagePhotos = 12;
// While the date scrubber is dragged, a month that hasn't loaded shows this
// many grey tiles at most: enough to read as "a lot", not thousands of nodes.
const cMaxPlaceholders = 300;
// Touch: how long a press on a tile takes to select it, and how far the
// finger may move meanwhile before it counts as the start of a scroll.
const cLongPressMs = 450;
const cPressSlopPx = 8;
// Grey tiles shown while the first page is on its way.
const cSkeletonTiles = 24;
// No date buckets (yet), one array for every render.
const cNoBuckets: ScrubBucket[] = [];

// Issue #106: a search result's `content` is always a server-generated
// JPEG thumbnail (files_manager.GetThumbnail), for a video exactly as for a
// photo - tagging those bytes with the video's own mime made the browser
// refuse to show them at all.
const isVideoFile = (f: { mime?: string }) => (f.mime || "").startsWith("video/");

// A tile's thumbnail - always a JPEG, see isVideoFile. Module-level, so
// useObjectURLs gets the stable `make` it needs.
const thumbOf = (f: MsgFile) => {
  const bytes = f.content;
  if (!bytes || bytes.byteLength === 0) return "";
  return URL.createObjectURL(new Blob([bytes as BlobPart], { type: "image/jpeg" }));
};
// The duplicate check for a page (mapRef): the same file at the same place
// in a page that a restarted search sent again.
const fileKey = (f: MsgFile, idx: number) =>
  `${f.path || ""}#${f.hash || ""}#${f.mime || ""}#${fileSize(f)}#${idx}`;

// Whether a page that went on with a jump's search (by its token) came from
// a search the device started again: one that no longer holds the token
// (unused for five minutes, or a restart) searches again without the
// jump's cutoff, from the newest photo. A held token comes back as it was
// sent; the last page of either has none, and then the photos tell -
// newer than the cutoff, or already on the screen.
function lostCutoff(files: MsgFile[], token: string, sent: string, cutoff: Date, shown: Map<string, MsgFile>): boolean {
  if (token) return token !== sent;
  if (files.some((f) => f.created != null && f.created.getTime() > cutoff.getTime())) return true;
  const paths = new Set([...shown.values()].map((f) => f.path));
  return files.some((f) => paths.has(f.path));
}

// ---- requests --------------------------------------------------------------

type Payload = NonNullable<DeepPartial<ReqEnvelope>["payload"]>;

// A request names only the fields it sets; ws.ts completes the envelope
// (ReqEnvelope.fromPartial).
const ask = (payload: Payload): Promise<RespEnvelope> =>
  useWS.request((e) => { e.payload = payload as ReqEnvelope["payload"]; });

const acked = (r: RespEnvelope) => r.payload?.$case === "respAck" && r.payload.respAck.ok;

// ---- words -----------------------------------------------------------------

const cMonthNames = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const cDayFormat = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", year: "numeric" });
const cNumber = new Intl.NumberFormat("en");

/** "2024-03" for a date, in the viewer's time zone. */
const monthOf = (d?: Date): string | null =>
  d && !Number.isNaN(d.getTime()) ? `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}` : null;

const monthParts = (key: string) => {
  const [y, m] = key.split("-").map(Number);
  return { year: y, name: cMonthNames[(m || 1) - 1] };
};
/** "March 2024" */
const monthTitle = (key: string) => {
  const { year, name } = monthParts(key);
  return `${name} ${year}`;
};
/** "Mar 2024" */
const monthShort = (key: string) => {
  const { year, name } = monthParts(key);
  return `${name.slice(0, 3)} ${year}`;
};

/** The months the photos span, from date buckets (newest first): "Mar 2024",
 *  "Mar – Oct 2024" or "Mar 2024 – Oct 2026" ("2024 – 2026" in `years`,
 *  which fits a narrow header's line). */
function spanLabel(buckets: ScrubBucket[], years = false): string {
  if (!buckets.length) return "";
  const from = monthParts(buckets[buckets.length - 1].month);
  const to = monthParts(buckets[0].month);
  const short = (p: { name: string }) => p.name.slice(0, 3);
  if (from.year === to.year && from.name === to.name) return `${short(to)} ${to.year}`;
  if (from.year === to.year) return `${short(from)} – ${short(to)} ${to.year}`;
  if (years) return `${from.year} – ${to.year}`;
  return `${short(from)} ${from.year} – ${short(to)} ${to.year}`;
}

/** "1 item", "1,234 items" */
const count = (n: number, one: string, many = `${one}s`) => `${cNumber.format(n)} ${n === 1 ? one : many}`;

/** "3 photos", "1 video", "2 photos and 1 video" */
function whatLabel(files: MsgFile[]): string {
  const videos = files.filter(isVideoFile).length;
  const photos = files.length - videos;
  if (!videos) return count(photos, "photo");
  if (!photos) return count(videos, "video");
  return `${count(photos, "photo")} and ${count(videos, "video")}`;
}

// ---- icons (24px outlines, like NavIcons) -----------------------------------

type IconProps = { size?: number };

function Svg({ size = 24, children }: { size?: number; children: ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}

const CloseIcon = ({ size }: IconProps) => <Svg size={size}><path d="M6.5 6.5l11 11M17.5 6.5l-11 11" /></Svg>;
const CheckIcon = ({ size }: IconProps) => <Svg size={size}><path d="m5.5 12.5 4 4 9-9" /></Svg>;
const BackIcon = ({ size }: IconProps) => <Svg size={size}><path d="M19 12H5.5M11 6l-6 6 6 6" /></Svg>;
const PencilIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M4.5 19.5h3.75L18.6 9.15a2.65 2.65 0 0 0-3.75-3.75L4.5 15.75v3.75Z" /><path d="m13.5 6.75 3.75 3.75" /></Svg>
);
// A paper plane: send to the feed.
const PostIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M20.5 3.5 10.75 13.25" /><path d="M20.5 3.5 14.25 20.5l-3.5-7.25L3.5 9.75 20.5 3.5Z" /></Svg>
);
// The collection icon (a photo with another behind it) with a plus.
const AddToCollectionIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M7.5 4.5h10a3 3 0 0 1 3 3v10" /><rect x="3.5" y="7.5" width="13" height="13" rx="2" /><path d="M10 11v6M7 14h6" /></Svg>
);
const ShareIcon = ({ size }: IconProps) => (
  <Svg size={size}><circle cx="17.5" cy="5.5" r="2.5" /><circle cx="6.5" cy="12" r="2.5" /><circle cx="17.5" cy="18.5" r="2.5" /><path d="m8.7 10.7 6.6-3.9M8.7 13.3l6.6 3.9" /></Svg>
);
const TrashIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M4.5 7h15" /><path d="M9.5 7V5.5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1V7" /><path d="M6.5 7l.85 12.1a1.5 1.5 0 0 0 1.5 1.4h6.3a1.5 1.5 0 0 0 1.5-1.4L17.5 7" /><path d="M10 11v5.5M14 11v5.5" /></Svg>
);
// A globe: a page on the web.
const GalleryPageIcon = ({ size }: IconProps) => (
  <Svg size={size}><circle cx="12" cy="12" r="8.5" /><path d="M3.5 12h17" /><path d="M12 3.5c2.3 2.4 3.4 5.2 3.4 8.5s-1.1 6.1-3.4 8.5c-2.3-2.4-3.4-5.2-3.4-8.5s1.1-6.1 3.4-8.5Z" /></Svg>
);
const LinkIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M10.5 13.5a3.75 3.75 0 0 0 5.3 0l2.95-2.95a3.75 3.75 0 0 0-5.3-5.3l-1.2 1.2" /><path d="M13.5 10.5a3.75 3.75 0 0 0-5.3 0l-2.95 2.95a3.75 3.75 0 0 0 5.3 5.3l1.2-1.2" /></Svg>
);
const DownloadIcon = ({ size }: IconProps) => <Svg size={size}><path d="M12 4v11" /><path d="m7.5 10.5 4.5 4.5 4.5-4.5" /><path d="M5 19.5h14" /></Svg>;
const PlusIcon = ({ size }: IconProps) => <Svg size={size}><path d="M12 5.5v13M5.5 12h13" /></Svg>;
const ChevronLeftIcon = ({ size }: IconProps) => <Svg size={size}><path d="m14.5 6-6 6 6 6" /></Svg>;
const ChevronRightIcon = ({ size }: IconProps) => <Svg size={size}><path d="m9.5 6 6 6-6 6" /></Svg>;
const SearchIcon = ({ size }: IconProps) => <Svg size={size}><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 4.5 4.5" /></Svg>;
const OfflineIcon = ({ size }: IconProps) => (
  <Svg size={size}><path d="M7.2 18.5h9.3a4 4 0 0 0 .9-7.9 5.5 5.5 0 0 0-10.3-1.6 4.75 4.75 0 0 0 .1 9.5Z" /><path d="M4 4l16 16" /></Svg>
);
const FaceIcon = ({ size }: IconProps) => <Svg size={size}><circle cx="12" cy="9.5" r="3.5" /><path d="M5.5 19.5c1.2-3.2 3.6-4.8 6.5-4.8s5.3 1.6 6.5 4.8" /></Svg>;
// Filled, so it reads at 12px on any photo.
const PlayGlyph = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4.5v15l12.5-7.5L7 4.5Z" fill="currentColor" /></svg>
);

// ---- clicks and tabs -----------------------------------------------------------

// A double click (or a double tap) on something its first click takes away
// sends the second click to whatever was under it: a photo, the menu
// button, a collection's Delete. For a moment after such a click, a second
// click (detail 2 or more) goes nowhere. Single clicks and the keyboard's
// (detail 0) are left alone.
function swallowSecondClick(ms = 500) {
  const until = performance.now() + ms;
  const onClick = (e: MouseEvent) => {
    if (e.detail < 2 || performance.now() > until) return;
    e.preventDefault();
    e.stopPropagation();
  };
  window.addEventListener("click", onClick, true);
  window.setTimeout(() => window.removeEventListener("click", onClick, true), ms);
}

// A tab for a ZIP still being made, opened at the click: a browser refuses
// one opened more than a few seconds after it, and making the archive can
// take longer. It goes to the link once there is one.
function openWaitingTab(): Window | null {
  const tab = window.open("", "_blank");
  if (!tab) return null;
  tab.opener = null;
  try {
    tab.document.title = "Preparing the ZIP…";
    tab.document.body.style.cssText =
      "margin:0;min-height:100vh;display:grid;place-items:center;background:#242424;color:#eaf2ef;font:16px system-ui,sans-serif";
    tab.document.body.textContent = "Preparing the ZIP… It downloads from here once it's ready.";
  } catch {
    // A blank tab still does the job.
  }
  return tab;
}

// ---- small hooks -------------------------------------------------------------

// A media query's current answer, kept up to date.
function useMedia(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const on = () => setMatches(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [query]);
  return matches;
}

// ===========================================================================

type PhotoGalleryProps = {
  // An open collection's "back" link: the Collections page.
  onShowCollections: () => void;
};

type DialogState =
  | { kind: "post" }
  | { kind: "collect" }
  | { kind: "delete" }
  | { kind: "deleteCollection" }
  // The share link, when the clipboard refused it (a browser that only
  // allows a copy right after a click, and making the link took longer).
  | { kind: "link"; link: string };

type Toast = {
  id: number;
  text: string;
  error?: boolean;
  // Stays until replaced (work in progress), with a spinner.
  busy?: boolean;
  action?: { label: string; run: () => void };
  // Stays until used or dismissed: its action is the only way to what it
  // offers.
  stay?: boolean;
};

export default function PhotoGallery({ onShowCollections }: PhotoGalleryProps) {
  const filter = usePhotoFilter();
  const { tags, personIds, group } = filter;
  const groupId = group?.id ?? "";
  const dateOrdered = isDateOrdered(filter);
  // Below this the selection bar shows icons only, each with a tooltip.
  const compact = useMedia("(max-width: 899px)");

  // -------- data & paging ---------------------------------------------------
  const [items, setItems] = useState<MsgFile[]>([]);
  const mapRef = useRef<Map<string, MsgFile>>(new Map()); // dedupe, and `have`
  // One object URL per loaded item, freed once it leaves `items` (a new
  // search, a jump to a date, a delete) - see useObjectURLs.
  const thumbFor = useObjectURLs(items, thumbOf);
  const [token, setToken] = useState<string | null>(null);
  // True from the start: the filter effect below starts a search on mount,
  // so the very first paint shows grey tiles rather than an empty page.
  const [loading, setLoading] = useState(true);
  const [endReached, setEndReached] = useState(false);
  // The last page failed and waits for its retry (usePageRetry).
  const [pageError, setPageError] = useState(false);
  const footRef = useRef<HTMLDivElement | null>(null);
  const gridRef = useRef<HTMLDivElement | null>(null);

  // Bumped by every search that replaces the grid (a filter change, a jump
  // to a date). fetchPage checks it before applying a reply: toggling a
  // filter twice quickly fires two overlapping searches, and without this
  // whichever reply happened to land last won, even the older one's.
  const searchGenRef = useRef(0);
  // A jump's cutoff, for the search it started: if its first page fails,
  // the retry must start at that month again, not at the newest photo.
  const beforeRef = useRef<Date | undefined>(undefined);
  const { tick: retryTick, failed: retryLater, reset: resetRetry, ready: retryReady } = usePageRetry();
  // Where a shift-click range of the selection starts (an index in items).
  const anchorRef = useRef<number | null>(null);

  const fetchPage = useCallback(
    async (overrideToken?: string, force = false, before?: Date) => {
      // force skips the loading/endReached guard: a fresh search resets
      // those right before calling this, in the same tick, so this closure
      // still sees the previous search's values (endReached after scrolling
      // to its end) and would otherwise do nothing.
      if (!force && (loading || endReached)) return;
      const myGen = searchGenRef.current;
      const sendToken = overrideToken ?? token ?? "";
      const failed = () => {
        retryLater();
        setPageError(true);
      };
      setLoading(true);
      try {
        const have = mapRef.current.size;
        const page = (tok: string, cutoff: Date | undefined) => ask({
          $case: "reqSearchPhotos",
          reqSearchPhotos: {
            tags,
            personIds,
            // Issue #115: inside a collection, only its members.
            groupId,
            // Issue #106: videos belong in Images too.
            includeVideos: true,
            token: tok,
            // Without a token this starts a search (a filter, a jump, or
            // the retry of a first page): a small page, see
            // cFirstPagePhotos. With one, 0: the device's own size.
            limit: tok ? 0 : cFirstPagePhotos,
            // Lets the device resume where this grid is if it no longer
            // holds the token (see SearchPhotos.have), instead of starting
            // over with photos already on screen.
            have,
            // Issue #77: the date scrubber's jump.
            before: cutoff,
            // The tiles' small thumbnails (release 111; older devices
            // send big ones). The viewer shows them only until the full
            // size arrives (MediaViewer).
            smallThumbnails: true,
          },
        });
        // The cutoff goes only on the request that starts the search; the
        // token carries the place after.
        let resp = await page(sendToken, sendToken ? undefined : (before ?? beforeRef.current));
        // A newer search took over while this one was in flight.
        if (myGen !== searchGenRef.current) return;
        // After a jump, a page from a search the device started again
        // (lostCutoff) is from the wrong end of the library. Asked once
        // more with the cutoff, the device runs the jump's search, skipping
        // the `have` photos the grid holds, whatever the token.
        const cutoff = beforeRef.current;
        if (sendToken && cutoff && resp.payload?.$case === "respListOfFiles") {
          const lof = resp.payload.respListOfFiles;
          if (lostCutoff(lof.files ?? [], lof.token, sendToken, cutoff, mapRef.current)) {
            resp = await page(sendToken, cutoff);
            if (myGen !== searchGenRef.current) return;
          }
        }
        if (resp.payload?.$case !== "respListOfFiles") { failed(); return; }

        const lof = resp.payload.respListOfFiles;
        const map = new Map(mapRef.current);
        const added: MsgFile[] = [];
        (lof.files ?? []).forEach((f, i) => {
          const k = fileKey(f, i);
          if (!map.has(k)) {
            map.set(k, f);
            added.push(f);
          }
        });
        mapRef.current = map;
        if (added.length) setItems((prev) => prev.concat(added));
        const nextToken = lof.token || null;
        setToken(nextToken);
        setEndReached(!nextToken);
        setPageError(false);
        resetRetry();
      } catch (err) {
        // No connection, or the request failed outright: asked again after
        // a pause (usePageRetry). A superseded search's failure is no one's.
        console.warn("Photo search page failed:", err);
        if (myGen === searchGenRef.current) failed();
      } finally {
        // Only the current search may say it is done.
        if (myGen === searchGenRef.current) setLoading(false);
      }
    },
    [tags, personIds, groupId, token, loading, endReached, retryLater, resetRetry]
  );

  // -------- date buckets and the scrubber (issue #77) -------------------------
  // Photo counts per month, for the scrubber's track, the headers' count and
  // span, and how many grey tiles stand in for a month that hasn't loaded.
  // Only meaningful in date order: a tag search is sorted by relevance
  // (isDateOrdered). Kept with what they count - the people and the open
  // collection; tags never change them - and shown only for that: after a
  // filter change the previous list is still here until the new one comes,
  // and it isn't this filter's.
  const [buckets, setBuckets] = useState<{ key: string; list: ScrubBucket[] } | null>(null);
  const bucketKey = dateOrdered ? `${personIds.join(",")}|${groupId}` : null;
  const bucketsFresh = buckets != null && buckets.key === bucketKey;
  const dateBuckets = bucketsFresh ? buckets.list : cNoBuckets;
  // The month at the end of the grid is laid out at the size it will have.
  const monthCounts = useMemo(
    () => (bucketsFresh ? new Map(dateBuckets.map((b) => [b.month, b.count])) : null),
    [bucketsFresh, dateBuckets]
  );
  const bucketsRef = useRef<ScrubBucket[]>([]);
  useEffect(() => { bucketsRef.current = dateBuckets; }, [dateBuckets]);
  const bucketGenRef = useRef(0);
  // A load that failed is asked again like a page is (usePageRetry).
  const { tick: bucketRetryTick, failed: bucketsLater, reset: resetBucketRetry } = usePageRetry();
  const loadDateBuckets = useCallback(async () => {
    const gen = ++bucketGenRef.current;
    if (bucketKey == null) { setBuckets(null); return; }
    try {
      const resp = await ask({
        $case: "reqPhotoDateBuckets",
        reqPhotoDateBuckets: { tags: [], personIds, groupId, includeVideos: true },
      });
      if (gen !== bucketGenRef.current) return;
      if (resp.payload?.$case === "respPhotoDateBuckets") {
        setBuckets({ key: bucketKey, list: resp.payload.respPhotoDateBuckets.buckets ?? [] });
        resetBucketRetry();
        return;
      }
      // A device without them: no scrubber, and nothing to ask again.
      if (resp.errorCode === "unknown_payload") {
        setBuckets({ key: bucketKey, list: [] });
        return;
      }
    } catch {
      if (gen !== bucketGenRef.current) return;
    }
    bucketsLater();
  }, [bucketKey, personIds, groupId, resetBucketRetry, bucketsLater]);
  const loadBucketsRef = useRef(loadDateBuckets);
  useLayoutEffect(() => { loadBucketsRef.current = loadDateBuckets; });
  useEffect(() => {
    if (bucketRetryTick) void loadBucketsRef.current();
  }, [bucketRetryTick]);

  // Grey tiles in place of the grid while the scrubber is dragged and until
  // the jump it ends with has its photos, so releasing it doesn't flash
  // whatever was on screen before.
  const [placeholderCount, setPlaceholderCount] = useState<number | null>(null);
  // Their month's title, so the photos land where the grey tiles were.
  const [placeholderMonth, setPlaceholderMonth] = useState<string | null>(null);
  const previewing = placeholderCount != null;
  const scrubbingRef = useRef(false);
  // The search a jump on its way started (0 when none) and the grey tiles
  // it stands behind: a scrub cancelled meanwhile goes back to them, while
  // that is still the current search - one a filter change overtook holds
  // nothing up.
  const jumpGenRef = useRef(0);
  const jumpTilesRef = useRef<{ count: number; month: string } | null>(null);
  // Where the page was when a scrub began, under which filter: its first
  // preview scrolls to the top, and a scrub cancelled without a jump goes
  // back there (restoreRef, once the photos are back).
  const filterKey = `${tags.join("\n")}|${personIds.join(",")}|${groupId}`;
  const filterKeyRef = useRef(filterKey);
  useLayoutEffect(() => { filterKeyRef.current = filterKey; });
  const scrubFromRef = useRef<{ y: number; filter: string } | null>(null);
  const restoreRef = useRef<{ y: number; filter: string } | null>(null);

  // The first photo below the top bar and where it was after the user's
  // last scroll, to keep it there when the width changes (see the tile
  // size below); restoredYRef is where that put the page.
  const topTileRef = useRef<{ idx: number; top: number; y: number } | null>(null);
  const restoredYRef = useRef<number | null>(null);
  const measureRef = useRef(() => {});

  // The scrubber's jump: a fresh search like a filter change, anchored at
  // the last instant of `month`, so it starts at that month's newest photo
  // and reads back from there as scrolling there would. The placeholders
  // are cleared here, once this search is current and done: a jump
  // superseded by a newer one leaves them to that one.
  const jumpToDate = useCallback(async (month: string) => {
    const [y, m] = month.split("-").map(Number);
    const before = new Date(y, m, 0, 23, 59, 59, 999);
    searchGenRef.current += 1;
    const myGen = searchGenRef.current;
    jumpGenRef.current = myGen;
    beforeRef.current = before;
    resetRetry();
    setItems([]);
    mapRef.current = new Map();
    setToken(null);
    setEndReached(false);
    setPageError(false);
    anchorRef.current = null;
    topTileRef.current = null;
    try {
      await fetchPage("", true, before);
    } finally {
      if (jumpGenRef.current === myGen) jumpGenRef.current = 0;
      // A scrub begun meanwhile keeps its own grey tiles: its jump, or its
      // cancel, clears them.
      if (myGen === searchGenRef.current && !scrubbingRef.current) setPlaceholderCount(null);
    }
  }, [fetchPage, resetRetry]);
  const jumpRef = useRef(jumpToDate);
  useLayoutEffect(() => { jumpRef.current = jumpToDate; });

  // Stable, so the scrubber can keep them in its own effects.
  const onScrubPreview = useCallback((bucket: ScrubBucket | null) => {
    if (!bucket) {
      scrubbingRef.current = false;
      const from = scrubFromRef.current;
      scrubFromRef.current = null;
      // A jump of this search on its way: its grey tiles, as they were.
      const jumpTiles = jumpTilesRef.current;
      if (jumpGenRef.current !== 0 && jumpGenRef.current === searchGenRef.current && jumpTiles) {
        setPlaceholderCount(jumpTiles.count);
        setPlaceholderMonth(jumpTiles.month);
        return;
      }
      setPlaceholderCount(null);
      restoreRef.current = from;
      return;
    }
    if (!scrubbingRef.current) {
      scrubbingRef.current = true;
      scrubFromRef.current = { y: window.scrollY, filter: filterKeyRef.current };
      // Out of the way at once, as Google Photos' own does: the grid's top
      // comes up under the bar while the finger is still moving.
      gridRef.current?.scrollIntoView({ block: "start" });
    }
    // No request here: the count comes from the buckets already loaded, so
    // dragging across years costs nothing but renders.
    setPlaceholderCount(Math.min(bucket.count, cMaxPlaceholders));
    setPlaceholderMonth(bucket.month);
  }, []);

  const onScrubJump = useCallback((month: string) => {
    if (!scrubbingRef.current) gridRef.current?.scrollIntoView({ block: "start" });
    scrubbingRef.current = false;
    scrubFromRef.current = null;
    const bucket = bucketsRef.current.find((b) => b.month === month);
    const count = Math.min(bucket?.count ?? cFirstPagePhotos, cMaxPlaceholders);
    jumpTilesRef.current = { count, month };
    setPlaceholderCount(count);
    setPlaceholderMonth(month);
    void jumpRef.current(month);
  }, []);

  // A cancelled scrub: back where it began, once the photos are.
  useLayoutEffect(() => {
    const from = restoreRef.current;
    if (previewing || !from) return;
    restoreRef.current = null;
    if (from.filter !== filterKey) return;
    window.scrollTo(0, from.y);
    measureRef.current();
  }, [previewing, filterKey]);

  // The month at the top of the screen, for the scrubber's "you are here",
  // and the first photo there (topTileRef).
  const [currentMonth, setCurrentMonth] = useState<string | null>(null);
  useEffect(() => {
    const barHeight = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--topbar-h")) || 64;
    // The first of `kids` still below the top bar. Months run in document
    // order, and the ones sharing a row (each a single row of tiles) end
    // level, so their bottoms only grow, as a month's rows do: a binary
    // search finds it in a dozen reads, however long the grid.
    const firstBelowBar = (kids: HTMLCollection) => {
      let lo = 0;
      let hi = kids.length - 1;
      while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (kids[mid].getBoundingClientRect().bottom > barHeight) hi = mid;
        else lo = mid + 1;
      }
      return kids[lo] as HTMLElement;
    };
    let frame = 0;
    const measure = () => {
      frame = 0;
      const kids = gridRef.current?.children;
      if (!kids || kids.length === 0) return;
      const sec = firstBelowBar(kids);
      const month = sec.dataset.month;
      if (dateOrdered && month) setCurrentMonth(month);
      // Not the scroll that kept the photos in place: its layout may be
      // half way through the menu's transition.
      if (window.scrollY === restoredYRef.current) return;
      restoredYRef.current = null;
      if (!sec.children.length) return;
      // Past the month's title, if that is what is there.
      const at = firstBelowBar(sec.children);
      const tile = at.dataset.idx != null ? at : at.nextElementSibling;
      if (tile instanceof HTMLElement && tile.dataset.idx != null) {
        topTileRef.current = { idx: Number(tile.dataset.idx), top: tile.getBoundingClientRect().top, y: window.scrollY };
      }
    };
    measureRef.current = measure;
    const onScroll = () => { if (!frame) frame = requestAnimationFrame(measure); };
    measure();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      window.removeEventListener("scroll", onScroll);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [dateOrdered, items]);

  // -------- one tile size for every month -------------------------------------
  // Months short of a row sit side by side, so the tile size is the page's,
  // not each month's: as many columns as fit at the smallest size the CSS
  // allows (--pg-tile-min, or --pg-cols-fixed on a phone), sharing the width
  // exactly, so a full row still reaches both edges. Set as --pg-tile, which
  // every month's grid is laid out from (.pg-sec) - a resize renders nothing.
  // Measured on a ruler as wide as the grid's content and never taller: the
  // grid's own height changes with the size it is given.
  const rootRef = useRef<HTMLDivElement | null>(null);
  const rulerRef = useRef<HTMLDivElement | null>(null);
  useLayoutEffect(() => {
    const root = rootRef.current;
    const ruler = rulerRef.current;
    if (!root || !ruler) return;
    const ro = new ResizeObserver((entries) => {
      const width = entries[entries.length - 1].contentRect.width;
      if (width <= 0) return;
      const css = getComputedStyle(ruler);
      const gap = parseFloat(css.getPropertyValue("--pg-gap")) || 0;
      const fixed = parseInt(css.getPropertyValue("--pg-cols-fixed"), 10);
      const min = parseFloat(css.getPropertyValue("--pg-tile-min")) || 150;
      const cols = fixed > 0 ? fixed : Math.max(1, Math.floor((width + gap) / (min + gap)));
      root.style.setProperty("--pg-tile", `${(width - (cols - 1) * gap) / cols}px`);
      // Every row moves when the tiles change size (the menu switched, the
      // window resized, a phone turned), and the browser's own scroll
      // anchoring stands aside when a width changes: the photo that was
      // first below the bar goes back where it was, before this is painted.
      const top = topTileRef.current;
      const tile = top && top.y > 0 ? gridRef.current?.querySelector<HTMLElement>(`.pg-tile[data-idx="${top.idx}"]`) : null;
      if (top && tile) {
        const d = tile.getBoundingClientRect().top - top.top;
        if (Math.abs(d) > 0.5) {
          window.scrollBy(0, d);
          restoredYRef.current = window.scrollY;
        }
      }
      measureRef.current();
    });
    ro.observe(ruler);
    return () => ro.disconnect();
  }, []);

  // -------- a new search whenever the filter changes ------------------------
  // Tags, people and the open collection, and only those: renaming the open
  // collection (updateOpenGroup) keeps its id and the photos on screen.
  // Runs on mount too.
  useEffect(() => {
    searchGenRef.current += 1;
    beforeRef.current = undefined;
    resetRetry();
    setItems([]);
    mapRef.current = new Map();
    setToken(null);
    setEndReached(false);
    setPageError(false);
    anchorRef.current = null;
    topTileRef.current = null;
    void fetchPage("", true);
    // A scrub in progress was measured against the previous filter's months.
    scrubbingRef.current = false;
    scrubFromRef.current = null;
    setPlaceholderCount(null);
    resetBucketRetry();
    void loadDateBuckets();
    window.scrollTo(0, 0);
  }, [tags, personIds, groupId]); // eslint-disable-line react-hooks/exhaustive-deps

  // -------- infinite scroll: one page at a time ------------------------------
  // Not under the scrubber's grey tiles: those stand in for another month,
  // and a page now would be of the photos being left.
  useEffect(() => {
    const node = footRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) return;
        // After a failed page, not before its retry is due (retryTick
        // re-creates this observer when it is).
        if (!loading && !endReached && !previewing && retryReady()) void fetchPage();
      },
      // The next page starts while the end is still well below the fold.
      { root: null, rootMargin: "0px 0px 600px 0px" }
    );
    obs.observe(node);
    return () => obs.disconnect();
  }, [fetchPage, loading, endReached, previewing, retryTick, retryReady]);

  // "Try again" after a failure, without waiting for the retry's pause.
  const retryNow = () => {
    resetRetry();
    setPageError(false);
    void fetchPage(items.length ? undefined : "", true);
  };

  // -------- the viewer --------------------------------------------------------
  const [openIdx, setOpenIdx] = useState<number | null>(null);
  const openIdxRef = useRef<number | null>(null);
  const openAt = useCallback((i: number) => {
    openIdxRef.current = i;
    setOpenIdx(i);
  }, []);
  // Back to the tile that was showing last, keyboard focus included.
  const closeViewer = useCallback(() => {
    const i = openIdxRef.current;
    openIdxRef.current = null;
    setOpenIdx(null);
    if (i == null) return;
    requestAnimationFrame(() => {
      const el = gridRef.current?.querySelector<HTMLElement>(`.pg-tile[data-idx="${i}"] .pg-tile-open`);
      if (!el) return;
      el.focus({ preventScroll: true });
      // The tile, whose scroll margin keeps it clear of the top bar.
      el.closest(".pg-tile")?.scrollIntoView({ block: "nearest" });
    });
  }, []);
  // The viewer shows the grid's own thumbnail while the full size loads.
  const viewerOpen = openIdx != null;
  const viewerItems = useMemo(
    () => (viewerOpen ? items.map((f) => ({ path: f.path, mime: f.mime, thumbURL: thumbFor(f) })) : []),
    [viewerOpen, items, thumbFor]
  );

  // -------- selection (issue #48: ordered - a post shows its photos in the
  // order they were picked, and the post dialog can change it) ---------------
  const [sel, setSel] = useState<MsgFile[]>([]);
  const selecting = sel.length > 0;
  const selNo = useMemo(() => new Map(sel.map((f, i) => [f.path, i + 1])), [sel]);
  // The post dialog's thumbnails: a picked photo may have left the grid
  // since (a jump to another date), so the selection keeps its own.
  const selThumbFor = useObjectURLs(sel, thumbOf);

  const clearSel = useCallback(() => {
    setSel([]);
    anchorRef.current = null;
  }, []);

  const toggleAt = (idx: number, range: boolean) => {
    const f = items[idx];
    if (!f) return;
    const anchor = anchorRef.current;
    anchorRef.current = idx;
    if (range && anchor != null && anchor !== idx && items[anchor]) {
      const span = anchor < idx ? items.slice(anchor, idx + 1) : items.slice(idx, anchor + 1);
      setSel((prev) => {
        const have = new Set(prev.map((x) => x.path));
        return prev.concat(span.filter((x) => !have.has(x.path)));
      });
      return;
    }
    setSel((prev) => (prev.some((x) => x.path === f.path) ? prev.filter((x) => x.path !== f.path) : [...prev, f]));
  };

  const selectFile = (f: MsgFile, idx: number) => {
    anchorRef.current = idx;
    setSel((prev) => (prev.some((x) => x.path === f.path) ? prev : [...prev, f]));
  };

  const moveSel = useCallback((path: string, offset: number) => setSel((prev) => {
    const i = prev.findIndex((x) => x.path === path);
    const j = i + offset;
    if (i < 0 || j < 0 || j >= prev.length) return prev;
    const next = [...prev];
    [next[i], next[j]] = [next[j], next[i]];
    return next;
  }), []);
  const unselect = useCallback((path: string) => setSel((prev) => prev.filter((x) => x.path !== path)), []);

  // -------- tiles: clicks, and the long press on touch ------------------------
  // One set of handlers on the grid rather than on every tile: tiles stay
  // plain memoized markup, and the press state is the grid's.
  const pressRef = useRef<{ pointerId: number; x: number; y: number; timer: number; file: MsgFile; idx: number } | null>(null);
  const pressFiredRef = useRef(false);
  const suppressClickUntilRef = useRef(0);
  const pointerTypeRef = useRef("mouse");

  const cancelPress = () => {
    const p = pressRef.current;
    if (p) window.clearTimeout(p.timer);
    pressRef.current = null;
  };
  useEffect(() => () => {
    const p = pressRef.current;
    if (p) window.clearTimeout(p.timer);
  }, []);

  const firePress = (f: MsgFile, idx: number) => {
    pressRef.current = null;
    pressFiredRef.current = true;
    selectFile(f, idx);
    try { navigator.vibrate?.(10); } catch { /* not every device can */ }
  };

  const tileIndexOf = (target: EventTarget | null): number | null => {
    if (!(target instanceof Element)) return null;
    const tile = target.closest<HTMLElement>(".pg-tile[data-idx]");
    if (!tile) return null;
    const i = Number(tile.dataset.idx);
    return Number.isInteger(i) ? i : null;
  };

  const onGridPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    pointerTypeRef.current = e.pointerType;
    pressFiredRef.current = false;
    // A new touch: the click a long press leaves behind came before it, so
    // a quick tap on the next photo is a tap.
    suppressClickUntilRef.current = 0;
    cancelPress();
    if (e.pointerType === "mouse" || !e.isPrimary) return;
    const idx = tileIndexOf(e.target);
    const f = idx == null ? undefined : items[idx];
    if (idx == null || !f) return;
    const timer = window.setTimeout(() => firePress(f, idx), cLongPressMs);
    pressRef.current = { pointerId: e.pointerId, x: e.clientX, y: e.clientY, timer, file: f, idx };
  };
  const onGridPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const p = pressRef.current;
    if (p && e.pointerId === p.pointerId && Math.hypot(e.clientX - p.x, e.clientY - p.y) > cPressSlopPx) cancelPress();
  };
  // The click that ends a long press must not toggle the photo straight
  // back (or open it). It follows the release at once, or not at all.
  const onGridPointerEnd = () => {
    cancelPress();
    if (pressFiredRef.current) {
      pressFiredRef.current = false;
      suppressClickUntilRef.current = performance.now() + 600;
    }
  };
  const onGridContextMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    if (pointerTypeRef.current === "mouse") return;
    const idx = tileIndexOf(e.target);
    if (idx == null) return;
    // Android answers a long press on a picture with its own menu (save,
    // open in a new tab): here the press selects the photo instead.
    e.preventDefault();
    const p = pressRef.current;
    if (p) {
      window.clearTimeout(p.timer);
      firePress(p.file, p.idx);
    }
  };
  const onGridClick = (e: ReactMouseEvent<HTMLDivElement>) => {
    if (performance.now() < suppressClickUntilRef.current) return;
    const idx = tileIndexOf(e.target);
    if (idx == null) return;
    const onCircle = (e.target as Element).closest(".pg-tile-check") != null;
    if (onCircle || selecting) {
      toggleAt(idx, e.shiftKey);
      return;
    }
    // A double click's second click would land on the viewer's arrows.
    swallowSecondClick();
    openAt(idx);
  };

  // -------- toasts ------------------------------------------------------------
  const [toast, setToast] = useState<Toast | null>(null);
  const toastIdRef = useRef(0);
  const showToast = useCallback((t: Omit<Toast, "id">) => {
    const id = ++toastIdRef.current;
    setToast({ ...t, id });
    return id;
  }, []);
  const dismissToast = useCallback((id: number) => setToast((cur) => (cur?.id === id ? null : cur)), []);
  useEffect(() => {
    if (!toast || toast.busy || toast.stay) return;
    const t = window.setTimeout(() => dismissToast(toast.id), toast.action ? 6000 : toast.error ? 5000 : 3000);
    return () => window.clearTimeout(t);
  }, [toast, dismissToast]);
  const toastError = useCallback((text: string) => { showToast({ text, error: true }); }, [showToast]);

  // -------- dialogs, menus and sharing -----------------------------------------
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const closeDialog = useCallback(() => setDialog(null), []);
  const [shareOpen, setShareOpen] = useState(false);
  const shareBtnRef = useRef<HTMLButtonElement>(null);
  const closeShare = useCallback((refocus: boolean) => {
    setShareOpen(false);
    if (refocus) shareBtnRef.current?.focus();
  }, []);
  // From the selection bar: an open share menu gives way to the dialog.
  const openDialog = (d: DialogState) => {
    setShareOpen(false);
    setDialog(d);
  };
  // Issue #180: what is being shared as a gallery page - the open
  // collection, or the selected photos.
  const [sharing, setSharing] = useState<{ groupId?: string; paths?: string[] } | null>(null);
  // Which share link is being made: the device reads every file and builds
  // an archive first, which takes seconds, and a second click would start
  // a second archive (issue #104).
  const [preparing, setPreparing] = useState<null | "link" | "zip">(null);

  const makeLink = async (kind: "link" | "zip") => {
    if (preparing || !sel.length) return;
    swallowSecondClick();
    const paths = sel.map((f) => f.path);
    closeShare(false);
    setPreparing(kind);
    const busyId = showToast({ text: kind === "zip" ? "Preparing the ZIP…" : "Preparing the link…", busy: true });
    // The link's own page downloads the archive, in a tab opened now.
    const tab = kind === "zip" ? openWaitingTab() : null;
    try {
      const resp = await ask({ $case: "reqShareFilesLink", reqShareFilesLink: { paths } });
      const link = resp.payload?.$case === "respShareLink" ? resp.payload.respShareLink.link : "";
      if (!link) {
        tab?.close();
        showToast({ text: kind === "zip" ? "Couldn't make the ZIP. Try again." : "Couldn't make the link. Try again.", error: true });
        return;
      }
      if (kind === "zip") {
        if (tab && !tab.closed) {
          tab.location.href = link;
          dismissToast(busyId);
        } else if (window.open(link, "_blank")) {
          dismissToast(busyId);
        } else {
          // Popups refused, or the tab closed meanwhile: a button opens it.
          showToast({ text: "Your ZIP is ready", stay: true, action: { label: "Download", run: () => { window.open(link, "_blank"); } } });
        }
        return;
      }
      try {
        await navigator.clipboard.writeText(link);
        showToast({ text: "Link copied" });
      } catch {
        dismissToast(busyId);
        setDialog({ kind: "link", link });
      }
    } catch {
      tab?.close();
      showToast({ text: "Couldn't reach your device. Try again.", error: true });
    } finally {
      setPreparing(null);
    }
  };

  // The focus goes back to the Share button when the menu was used with
  // the keyboard: its item goes away, and the dialog gives it back there.
  const shareAsGallery = () => {
    const el = document.activeElement;
    closeShare(el instanceof HTMLElement && el.matches(":focus-visible"));
    setSharing({ paths: sel.map((f) => f.path) });
  };

  // Issue #45: delete every selected photo and video, one request each.
  const [deleting, setDeleting] = useState<{ done: number; total: number } | null>(null);
  const deleteSelected = async (): Promise<string | null> => {
    const files = sel;
    if (!files.length) return null;
    setDeleting({ done: 0, total: files.length });
    const gone = new Set<string>();
    let uploadOnly = 0;
    let failed = 0;
    for (const f of files) {
      try {
        const resp = await ask({ $case: "reqDelFile", reqDelFile: { path: f.path } });
        if (acked(resp)) gone.add(f.path);
        else if (resp.errorCode === "upload_only") uploadOnly += 1;
        else failed += 1;
      } catch {
        failed += 1;
      }
      setDeleting((d) => (d ? { ...d, done: d.done + 1 } : d));
    }
    setDeleting(null);
    if (gone.size) {
      setItems((prev) => prev.filter((f) => !gone.has(f.path)));
      // `have` must count what is on screen, or a search the device
      // resumes would skip as many photos as were deleted.
      for (const [k, f] of mapRef.current) if (gone.has(f.path)) mapRef.current.delete(k);
      // The viewer could point at a photo that's gone or has moved.
      openIdxRef.current = null;
      setOpenIdx(null);
      void loadDateBuckets();
      // The open collection's count (its header follows the list).
      if (group) void reloadGroups().catch(() => {});
    }
    // What couldn't be deleted stays selected, to try again or keep.
    setSel((prev) => prev.filter((f) => !gone.has(f.path)));
    anchorRef.current = null;
    setDialog(null);
    const kept = uploadOnly + failed;
    if (!kept) {
      showToast({ text: `Deleted ${whatLabel(files)}` });
    } else if (kept === uploadOnly) {
      showToast({ text: `${count(kept, "item")} ${kept === 1 ? "is" : "are"} in an upload-only folder and can't be deleted`, error: true });
    } else {
      showToast({ text: `${count(kept, "item")} couldn't be deleted. Try again.`, error: true });
    }
    return null;
  };

  const deleteCollection = async (): Promise<string | null> => {
    if (!group) return null;
    try {
      const resp = await ask({ $case: "reqDeleteImageGroup", reqDeleteImageGroup: { id: group.id } });
      if (!acked(resp)) return "Couldn't delete the collection. Try again.";
    } catch {
      return "Couldn't reach your device. Try again.";
    }
    // One render for all of it: after the await these would otherwise be
    // two (the filter's store and App's tab), and the first would search
    // the whole library on the way out.
    flushSync(() => {
      setDialog(null);
      dropGroupLocally(group.id);
      leaveGroup();
      onShowCollections();
    });
    return null;
  };

  const onPosted = () => {
    setDialog(null);
    clearSel();
    showToast({ text: "Posted to Social" });
  };

  const onCollected = (g: OpenGroup) => {
    setDialog(null);
    clearSel();
    showToast({ text: `Added to “${g.name}”`, action: { label: "View", run: () => openGroup(g) } });
  };

  // Escape clears the selection - unless something on top of the grid
  // (the viewer, a dialog, the share menu) is what it should close.
  const somethingOnTop = openIdx != null || dialog != null || shareOpen || sharing != null;
  useEffect(() => {
    if (!selecting || somethingOnTop) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) clearSel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selecting, somethingOnTop, clearSel]);

  // The share menu belongs to the selection bar.
  useEffect(() => { if (!selecting) setShareOpen(false); }, [selecting]);

  // The selection bar lies over the top bar: what is under it is out of
  // reach of the keyboard too (TopSearch closes its own search).
  useEffect(() => {
    const bar = document.querySelector<HTMLElement>(".topbar");
    if (!selecting || !bar) return;
    bar.inert = true;
    return () => { bar.inert = false; };
  }, [selecting]);

  // -------- render ------------------------------------------------------------
  // In date order, a section per month: its title over its own tiles (a
  // photo without a date stays with the month before it). By relevance,
  // one section without a title.
  const gridChildren = useMemo(() => {
    const keys = new Set<string>();
    // A restarted search can send a photo twice: React keys stay unique.
    const unique = (k: string) => {
      let key = k;
      for (let n = 2; keys.has(key); n++) key = `${k}~${n}`;
      keys.add(key);
      return key;
    };
    const runs: { month: string | null; start: number; end: number }[] = [];
    items.forEach((f, i) => {
      const m = dateOrdered ? monthOf(f.created) : null;
      const run = runs[runs.length - 1];
      if (run && (!m || m === run.month)) run.end = i + 1;
      else runs.push({ month: m, start: i, end: i + 1 });
    });
    return runs.map(({ month, start, end }, r) => {
      const tiles = items.slice(start, end).map((f, k) => (
        <Tile
          key={unique(`${f.path}#${f.hash}`)}
          file={f}
          index={start + k}
          thumb={thumbFor(f)}
          selNo={selNo.get(f.path) ?? 0}
          selecting={selecting}
        />
      ));
      if (!dateOrdered) return <div key="all" className="pg-sec" style={sectionStyle(tiles.length)}>{tiles}</div>;
      // The last month may have more photos on the way: it takes the room
      // they will need now, so the next page fills it in instead of moving
      // it off a row it shared.
      const room = r === runs.length - 1 && !endReached && month
        ? Math.max(tiles.length, monthCounts?.get(month) ?? 0)
        : tiles.length;
      return (
        <section key={unique(`month-${month ?? ""}`)} className="pg-sec" data-month={month ?? undefined} style={sectionStyle(room)}>
          <MonthTitle month={month} oneTile={room === 1} />
          {tiles}
        </section>
      );
    });
  }, [items, dateOrdered, endReached, monthCounts, thumbFor, selNo, selecting]);

  const bucketTotal = dateBuckets.reduce((sum, b) => sum + b.count, 0);
  const showScrubber = dateOrdered && dateBuckets.length > 0;
  // Once the first page has failed, its error stays up while the retries
  // run (the button shows them), rather than flipping back to grey tiles.
  const firstLoad = items.length === 0 && !pageError && (loading || !endReached);

  let body: ReactNode;
  if (previewing) {
    body = (
      <div className="pg-grid" ref={gridRef} aria-busy="true" aria-label="Loading photos">
        <div className="pg-sec" style={sectionStyle(placeholderCount)}>
          <MonthTitle month={placeholderMonth} oneTile={placeholderCount === 1} />
          {Array.from({ length: placeholderCount }, (_, i) => <div key={i} className="pg-tile pg-tile-skel" />)}
        </div>
      </div>
    );
  } else if (firstLoad) {
    body = (
      <div className="pg-grid" aria-busy="true" aria-label="Loading photos">
        <div className="pg-sec" style={sectionStyle(cSkeletonTiles)}>
          {dateOrdered && <div className="pg-month pg-month-skel"><span /></div>}
          {Array.from({ length: cSkeletonTiles }, (_, i) => <div key={i} className="pg-tile pg-tile-skel" />)}
        </div>
      </div>
    );
  } else if (items.length === 0 && pageError) {
    body = (
      <Empty icon={<OfflineIcon size={32} />} title="Can't reach your device" text="Your photos will appear as soon as it answers.">
        <button type="button" className="pg-btn outline" onClick={retryNow} disabled={loading}>
          {loading ? <Spinner label="Trying…" /> : "Try again"}
        </button>
      </Empty>
    );
  } else if (items.length === 0) {
    if (group && !tags.length && !personIds.length) {
      body = (
        <Empty icon={<CollectionsIcon size={32} />} title="This collection is empty" text="Select photos in Images, then choose Add to collection.">
          <button type="button" className="pg-btn outline" onClick={() => leaveGroup()}>Go to Images</button>
        </Empty>
      );
    } else if (isFiltered(filter)) {
      body = (
        <Empty icon={<SearchIcon size={32} />} title="No photos match" text={group ? `Nothing in “${group.name}” matches this search.` : "Try other words, or fewer of them."}>
          <button type="button" className="pg-btn outline" onClick={() => clearSearch()}>Clear search</button>
        </Empty>
      );
    } else {
      body = <Empty icon={<ImagesIcon size={32} />} title="No photos yet" text="Photos from the phone and computer apps appear here." />;
    }
  } else {
    body = (
      <div
        className="pg-grid"
        ref={gridRef}
        onClick={onGridClick}
        onPointerDown={onGridPointerDown}
        onPointerMove={onGridPointerMove}
        onPointerUp={onGridPointerEnd}
        onPointerCancel={onGridPointerEnd}
        onContextMenu={onGridContextMenu}
      >
        {gridChildren}
      </div>
    );
  }

  // The room for the scrubber is kept from the start of a date-ordered
  // search, so its arrival doesn't reflow the grid - and dropped once the
  // device says there are no months to show at all.
  const keepScrubberRoom = dateOrdered && (!bucketsFresh || dateBuckets.length > 0);
  const rootClass = `pg-root${keepScrubberRoom ? " with-scrubber" : ""}${selecting ? " selecting" : ""}`;

  return (
    <div className={rootClass} ref={rootRef}>
      {/* First, though it is drawn over the top bar: Tab reaches it before
          the photos, as it would the top bar it covers. */}
      {selecting && (
        <div className="pg-selbar" role="toolbar" aria-label="Selected photos">
          <button
            type="button"
            className="pg-icon-btn"
            onClick={() => { swallowSecondClick(); clearSel(); }}
            aria-label="Clear selection"
            data-tip="Clear selection"
          >
            <CloseIcon />
          </button>
          <span className="pg-selbar-count" aria-live="polite">{cNumber.format(sel.length)} selected</span>
          <div className="pg-selbar-actions">
            <BarButton compact={compact} label="Social Post" icon={<PostIcon size={22} />} onClick={() => openDialog({ kind: "post" })} />
            <BarButton compact={compact} label="Add to Collection" icon={<AddToCollectionIcon size={22} />} onClick={() => openDialog({ kind: "collect" })} />
            <div className="pg-share-anchor">
              <BarButton
                ref={shareBtnRef}
                compact={compact}
                label="Share"
                icon={preparing ? <Spinner /> : <ShareIcon size={22} />}
                onClick={() => setShareOpen((v) => !v)}
                disabled={!!preparing}
                menu={shareOpen}
              />
              {shareOpen && (
                <ShareMenu
                  onGallery={shareAsGallery}
                  onCopyLink={() => void makeLink("link")}
                  onZip={() => void makeLink("zip")}
                  onClose={closeShare}
                />
              )}
            </div>
            <BarButton compact={compact} danger label="Delete" icon={<TrashIcon size={22} />} onClick={() => openDialog({ kind: "delete" })} />
          </div>
        </div>
      )}

      {group && (
        <CollectionHeader
          group={group}
          // Below 900px, as in the selection bar, the actions are icons and
          // the span is years only: the name and its line keep the room.
          span={bucketsFresh ? spanLabel(dateBuckets, compact) : ""}
          compact={compact}
          onBack={() => {
            swallowSecondClick();
            // Leaving it, not just looking away: the search in the top bar
            // is the whole library again.
            leaveGroup();
            onShowCollections();
          }}
          onShare={() => setSharing({ groupId: group.id })}
          onDelete={() => setDialog({ kind: "deleteCollection" })}
          onError={toastError}
        />
      )}
      {!group && personIds.length > 0 && tags.length === 0 && (
        <PersonHeader personIds={personIds} total={bucketsFresh ? bucketTotal : null} />
      )}

      {/* What the tile size is measured on (see rulerRef). */}
      <div className="pg-ruler" ref={rulerRef} aria-hidden="true" />
      {body}

      {/* The end of the grid: the next page loads when this nears the
          screen. Always the same node, which the observer above watches. */}
      <div className="pg-foot" ref={footRef}>
        {items.length > 0 && !previewing && (loading
          ? <Spinner />
          : pageError && (
            <span className="pg-foot-error">
              Couldn't load more photos.
              <button type="button" className="pg-btn quiet small" onClick={retryNow}>Try again</button>
            </span>
          ))}
      </div>

      {dialog?.kind === "post" && (
        <PostDialog files={sel} thumbFor={selThumbFor} onMove={moveSel} onRemove={unselect} onCancel={closeDialog} onPosted={onPosted} />
      )}
      {dialog?.kind === "collect" && (
        <CollectDialog files={sel} exclude={groupId} onCancel={closeDialog} onDone={onCollected} />
      )}
      {dialog?.kind === "delete" && (
        <ConfirmDialog
          title={`Delete ${whatLabel(sel)}?`}
          text={group
            ? "They're deleted from your device, not just from this collection. This can't be undone."
            : "They're deleted from your device for good. This can't be undone."}
          confirm="Delete"
          busyText={deleting ? `Deleting ${Math.min(deleting.done + 1, deleting.total)} of ${deleting.total}…` : "Deleting…"}
          onConfirm={deleteSelected}
          onCancel={closeDialog}
        />
      )}
      {dialog?.kind === "deleteCollection" && group && (
        <ConfirmDialog
          title="Delete collection?"
          text={<>Delete the collection “{group.name}”? The photos themselves are kept.</>}
          confirm="Delete"
          busyText="Deleting…"
          onConfirm={deleteCollection}
          onCancel={closeDialog}
        />
      )}
      {dialog?.kind === "link" && <LinkDialog link={dialog.link} onClose={closeDialog} />}

      <div className="pg-toast-wrap" aria-live="polite">
        {toast && (
          <div key={toast.id} className={`pg-toast${toast.error ? " error" : ""}${toast.action ? " has-action" : ""}`}>
            {toast.busy && <Spinner />}
            <span className="pg-toast-text">{toast.text}</span>
            {toast.action && (
              <button
                type="button"
                className="pg-toast-action"
                onClick={() => { swallowSecondClick(); toast.action?.run(); dismissToast(toast.id); }}
              >
                {toast.action.label}
              </button>
            )}
            {toast.stay && (
              <button type="button" className="pg-icon-btn pg-toast-close" onClick={() => dismissToast(toast.id)} aria-label="Dismiss">
                <CloseIcon size={18} />
              </button>
            )}
          </div>
        )}
      </div>

      {/* The shared viewer (MediaViewer.tsx), also used by Files. */}
      {openIdx != null && openIdx < items.length && (
        <MediaViewer items={viewerItems} index={openIdx} onIndexChange={openAt} onClose={closeViewer} />
      )}

      {sharing && <SharedGalleryShare source={sharing} onClose={() => setSharing(null)} />}

      {showScrubber && (
        <DateScrubber buckets={dateBuckets} currentMonth={currentMonth} onPreview={onScrubPreview} onJump={onScrubJump} />
      )}
    </div>
  );
}

// ---- tiles -----------------------------------------------------------------------

type TileProps = {
  file: MsgFile;
  index: number;
  thumb: string;
  // The place in the selection, from 1; 0 when not selected.
  selNo: number;
  // Anything selected: a click anywhere on a tile picks it.
  selecting: boolean;
};

// Thumbnails fade in once decoded instead of popping in.
const markLoaded = (e: SyntheticEvent<HTMLImageElement>) => { e.currentTarget.dataset.loaded = "1"; };

// Plain markup: the grid's own handlers deal with clicks and presses (see
// onGridClick), so a tile only renders again when what it shows changes.
const Tile = memo(function Tile({ file, index, thumb, selNo, selecting }: TileProps) {
  const video = isVideoFile(file);
  const selected = selNo > 0;
  const when = file.created && !Number.isNaN(file.created.getTime()) ? cDayFormat.format(file.created) : "";
  return (
    <div className={`pg-tile${selected ? " selected" : ""}`} data-idx={index}>
      <button
        type="button"
        className="pg-tile-open"
        aria-label={`${video ? "Video" : "Photo"}${when ? `, ${when}` : ""}`}
        aria-pressed={selecting ? selected : undefined}
      >
        {thumb
          ? <img className="pg-tile-img" src={thumb} alt="" loading="lazy" decoding="async" draggable={false} onLoad={markLoaded} />
          : <span className="pg-tile-missing"><ImagesIcon size={28} /></span>}
        {video && <span className="pg-tile-video"><PlayGlyph /></span>}
      </button>
      {/* Its own button for the keyboard; on a touch screen taps go to the
          tile underneath (CSS), which picks it once anything is picked. */}
      <button type="button" className="pg-tile-check" aria-pressed={selected} aria-label="Select" tabIndex={selecting ? -1 : 0}>
        <span className="pg-tile-circle">{selected ? selNo : <CheckIcon size={16} />}</span>
      </button>
    </div>
  );
});

// How many tiles a section has room for: its width, up to a whole row, is
// made from it (.pg-sec).
const sectionStyle = (tiles: number) => ({ "--pg-n": tiles }) as CSSProperties;

// A month's title, one line. A month one tile wide has both names, and the
// CSS shows "Sep 2026" where "September 2026" doesn't fit (.pg-month.fit).
// Photos without a date at the top of the list get an empty line, so the
// tiles of months beside them stay level.
const MonthTitle = memo(function MonthTitle({ month, oneTile }: { month: string | null; oneTile: boolean }) {
  if (!month) return <div className="pg-month" aria-hidden="true" />;
  if (!oneTile) return <h2 className="pg-month">{monthTitle(month)}</h2>;
  return (
    <h2 className="pg-month fit">
      <span className="pg-month-long">{monthTitle(month)}</span>
      <span className="pg-month-short" aria-hidden="true">{monthShort(month)}</span>
    </h2>
  );
});

// ---- headers ---------------------------------------------------------------------

function CollectionHeader({ group, span, compact, onBack, onShare, onDelete, onError }: {
  group: OpenGroup;
  // The months its photos span, when known.
  span: string;
  // Below 900px the actions are icons.
  compact: boolean;
  onBack: () => void;
  onShare: () => void;
  onDelete: () => void;
  onError: (text: string) => void;
}) {
  const groups = useGroups();
  // The name and count as the device lists them: renamed or grown in
  // another tab or on another device, or after photos were deleted here.
  useEffect(() => {
    const g = groups.items.find((x) => x.id === group.id);
    if (g && (g.name !== group.name || g.fileCount !== group.fileCount)) {
      updateOpenGroup({ id: g.id, name: g.name, fileCount: g.fileCount });
    }
  }, [groups.items, group]);

  // Renaming in place: null when not editing.
  const [draft, setDraft] = useState<string | null>(null);
  // The new name, shown while the device saves it.
  const [saving, setSaving] = useState<string | null>(null);
  // Enter saves and the input then goes away, which can also blur it: one
  // save, not two.
  const editingRef = useRef(false);

  const start = () => {
    editingRef.current = true;
    setDraft(saving ?? group.name);
  };
  const cancel = () => {
    editingRef.current = false;
    setDraft(null);
  };
  const commit = async () => {
    if (!editingRef.current) return;
    editingRef.current = false;
    const name = (draft ?? "").trim();
    setDraft(null);
    if (!name || name === group.name) return;
    setSaving(name);
    try {
      const resp = await ask({ $case: "reqRenameImageGroup", reqRenameImageGroup: { id: group.id, name } });
      if (acked(resp)) {
        updateOpenGroup({ ...group, name });
        renameGroupLocally(group.id, name);
      } else {
        onError("Couldn't rename the collection. Try again.");
      }
    } catch {
      onError("Couldn't reach your device to rename the collection.");
    } finally {
      setSaving(null);
    }
  };

  const shown = saving ?? group.name;
  return (
    <header className="pg-head">
      <button type="button" className="pg-back" onClick={onBack}>
        <BackIcon size={20} />
        Collections
      </button>
      <div className="pg-head-row">
        <div className="pg-head-main">
          <h1 className="pg-title">
            {draft != null ? (
              <input
                className="pg-title-input"
                value={draft}
                autoFocus
                maxLength={120}
                aria-label="Collection name"
                onChange={(e) => setDraft(e.target.value)}
                onFocus={(e) => e.currentTarget.select()}
                onBlur={() => void commit()}
                onKeyDown={(e) => {
                  if (e.key === "Enter") { e.preventDefault(); void commit(); }
                  else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancel(); }
                }}
              />
            ) : (
              <>
                {/* Click the name to rename it; the pencil is the same for
                    the keyboard. */}
                <button type="button" className="pg-title-text" tabIndex={-1} onClick={start}>
                  <span className="pg-clamp">{shown}</span>
                </button>
                <button type="button" className="pg-icon-btn pg-title-edit" onClick={start} aria-label="Rename collection" data-tip="Rename">
                  <PencilIcon size={20} />
                </button>
              </>
            )}
          </h1>
          <p className="pg-sub">{count(group.fileCount, "item")}{span && <> · {span}</>}</p>
        </div>
        <div className="pg-head-actions">
          <button
            type="button"
            className={compact ? "pg-icon-btn" : "pg-btn outline"}
            onClick={onShare}
            aria-label="Share as gallery"
            data-tip={compact ? "Share as gallery" : undefined}
          >
            <GalleryPageIcon size={20} />
            {!compact && <span>Share as gallery</span>}
          </button>
          <button
            type="button"
            className={compact ? "pg-icon-btn danger" : "pg-btn outline danger"}
            onClick={onDelete}
            aria-label="Delete collection"
            data-tip={compact ? "Delete collection" : undefined}
          >
            <TrashIcon size={20} />
            {!compact && <span>Delete collection</span>}
          </button>
        </div>
      </div>
    </header>
  );
}

// One person's photos (or several people's together), opened from the
// People page or the search.
function PersonHeader({ personIds, total }: { personIds: string[]; total: number | null }) {
  const people = usePeople();
  if (!people.loaded) {
    // The same height as the header it stands in for: no jump when it comes.
    return (
      <header className="pg-head" aria-hidden="true">
        <div className="pg-head-row">
          <div className="pg-faces"><span className="pg-face pg-face-blank" /></div>
          <div className="pg-head-main"><div className="pg-title-skel" /><div className="pg-sub-skel" /></div>
        </div>
      </header>
    );
  }
  const picked = personIds
    .map((id) => people.items.find((p) => p.id === id))
    .filter((p): p is Person => p != null);
  if (!picked.length) return null;
  const names = picked.map(personLabel);
  const title = names.length <= 2 ? names.join(" & ") : `${names.slice(0, 2).join(", ")} & ${count(names.length - 2, "other")}`;
  return (
    <header className="pg-head">
      <div className="pg-head-row">
        <div className="pg-faces" aria-hidden="true">
          {picked.slice(0, 3).map((p) => {
            const face = people.thumbs.get(p.id);
            return face
              ? <img key={p.id} className="pg-face" src={face} alt="" />
              : <span key={p.id} className="pg-face pg-face-blank"><FaceIcon size={28} /></span>;
          })}
        </div>
        <div className="pg-head-main">
          <h1 className="pg-title"><span className="pg-clamp">{title}</span></h1>
          {/* The line is there before the count is: nothing moves when it comes. */}
          <p className="pg-sub">{total != null ? count(total, "item") : "\u00a0"}</p>
        </div>
      </div>
    </header>
  );
}

// ---- the selection bar -------------------------------------------------------------

// An action of the selection bar: icon and label, or the icon alone with a
// tooltip on a narrow window.
function BarButton({ ref, compact, label, icon, onClick, danger = false, disabled = false, menu }: {
  ref?: Ref<HTMLButtonElement>;
  compact: boolean;
  label: string;
  icon: ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
  // Set for a button that opens a menu: whether it is open.
  menu?: boolean;
}) {
  return (
    <button
      ref={ref}
      type="button"
      className={`pg-bar-btn${danger ? " danger" : ""}`}
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      data-tip={compact ? label : undefined}
      aria-haspopup={menu === undefined ? undefined : "menu"}
      aria-expanded={menu}
    >
      {icon}
      <span className="pg-bar-label">{label}</span>
    </button>
  );
}

function ShareMenu({ onGallery, onCopyLink, onZip, onClose }: {
  onGallery: () => void;
  onCopyLink: () => void;
  onZip: () => void;
  // refocus: give the focus back to the Share button (Escape).
  onClose: (refocus: boolean) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      onClose(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Up and down move between the items, as in any menu.
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const all = [...(ref.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    if (!all.length) return;
    const at = all.indexOf(document.activeElement as HTMLElement);
    const next = e.key === "Home" ? 0
      : e.key === "End" ? all.length - 1
      : e.key === "ArrowDown" ? (at + 1) % all.length
      : (at - 1 + all.length) % all.length;
    all[next].focus();
  };

  return (
    <>
      {/* Catches the tap that closes the menu, so it doesn't also pick or
          open the photo under it (nor a double tap's second). Below the
          bar's own buttons. */}
      <div className="pg-menu-catcher" aria-hidden="true" onClick={() => { swallowSecondClick(); onClose(false); }} />
      <div ref={ref} className="pg-menu" role="menu" aria-label="Share" onKeyDown={onKeyDown}>
        <button type="button" role="menuitem" className="pg-menu-item" onClick={onGallery}>
          <GalleryPageIcon size={22} />
          <span className="pg-menu-text"><strong>Share as a gallery page</strong><small>A page anyone with the link can see</small></span>
        </button>
        <button type="button" role="menuitem" className="pg-menu-item" onClick={onCopyLink}>
          <LinkIcon size={22} />
          <span className="pg-menu-text"><strong>Copy link</strong><small>A link to download them</small></span>
        </button>
        <button type="button" role="menuitem" className="pg-menu-item" onClick={onZip}>
          <DownloadIcon size={22} />
          <span className="pg-menu-text"><strong>Download ZIP</strong><small>Save them on this device</small></span>
        </button>
      </div>
    </>
  );
}

// ---- dialogs ----------------------------------------------------------------------

const cFocusable = "button, [href], input, textarea, select, [tabindex]:not([tabindex='-1'])";

// Tab and Shift+Tab within `box`: from its last control to its first and
// back, and into it from wherever the focus fell (a control that was
// removed, or disabled while busy, leaves it on the page).
function keepTabInside(box: HTMLElement, e: KeyboardEvent) {
  const all = [...box.querySelectorAll<HTMLElement>(cFocusable)]
    .filter((el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null);
  if (!all.length) { e.preventDefault(); box.focus(); return; }
  const first = all[0];
  const last = all[all.length - 1];
  const active = document.activeElement;
  if (!box.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
  else if (e.shiftKey && (active === first || active === box)) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
}

// A small card over the dimmed page. Focus moves into it (to the element
// marked data-autofocus, else the card) and back when it closes; Tab stays
// inside; Escape or a click on the dimmed page cancels - except while busy.
// The keys are the document's, so they work wherever the focus fell.
function Modal({ labelledBy, busy = false, wide = false, onClose, children }: {
  labelledBy: string;
  busy?: boolean;
  wide?: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  // A press that starts inside the card (selecting text in a field) and
  // ends outside it is not a click on the page.
  const downOnBackdropRef = useRef(false);
  const busyRef = useRef(busy);
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    busyRef.current = busy;
    onCloseRef.current = onClose;
  });

  // What had the keyboard's focus before the dialog gets it back on close.
  // Read on the first render, before a field inside can take it
  // (autoFocus). After a tap or a click it stays where it falls: a button
  // focused only by a finger would come back with its tooltip showing.
  const [returnFocus] = useState(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el.matches(":focus-visible") ? el : null;
  });
  useEffect(() => {
    const card = cardRef.current;
    if (card && !card.contains(document.activeElement)) {
      (card.querySelector<HTMLElement>("[data-autofocus]") ?? card).focus();
    }
    return () => { if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true }); };
  }, [returnFocus]);
  // Busy no more (an error to read): the focus its disabled buttons lost.
  useEffect(() => {
    if (!busy && document.activeElement === document.body) cardRef.current?.focus();
  }, [busy]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const card = cardRef.current;
      if (!card) return;
      if (e.key === "Escape") {
        // A field's own Escape (the new collection's name) is not this.
        if (e.defaultPrevented) return;
        e.preventDefault();
        e.stopPropagation();
        if (!busyRef.current) onCloseRef.current();
      } else if (e.key === "Tab") {
        keepTabInside(card, e);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div
      className="pg-dlg-backdrop"
      onPointerDown={(e) => { downOnBackdropRef.current = e.target === e.currentTarget; }}
      onClick={(e) => {
        // Nor is a double click's second, when the first opened the dialog.
        const fromBackdrop = downOnBackdropRef.current && e.target === e.currentTarget && e.detail < 2;
        downOnBackdropRef.current = false;
        if (fromBackdrop && !busy) onClose();
      }}
    >
      <div ref={cardRef} className={`pg-dlg${wide ? " wide" : ""}`} role="dialog" aria-modal="true" aria-labelledby={labelledBy} tabIndex={-1}>
        {children}
      </div>
    </div>
  );
}

function ConfirmDialog({ title, text, confirm, busyText, onConfirm, onCancel }: {
  title: string;
  text: ReactNode;
  confirm: string;
  busyText: string;
  // Does the work: an error to show in the dialog, or null when done (the
  // caller closes the dialog then).
  onConfirm: () => Promise<string | null>;
  onCancel: () => void;
}) {
  const titleId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setBusy(true);
    setError(null);
    const err = await onConfirm();
    if (err) {
      setError(err);
      setBusy(false);
    }
  };
  return (
    <Modal labelledBy={titleId} busy={busy} onClose={onCancel}>
      <div>
        <h2 id={titleId} className="pg-dlg-title">{title}</h2>
        <p className="pg-dlg-text">{text}</p>
      </div>
      {error && <p className="pg-dlg-error" role="alert">{error}</p>}
      <div className="pg-dlg-actions">
        {/* Focus starts on Cancel: Enter right away keeps everything. */}
        <button type="button" className="pg-btn quiet" onClick={onCancel} disabled={busy} data-autofocus>Cancel</button>
        <button type="button" className="pg-btn danger" onClick={() => void run()} disabled={busy} aria-busy={busy}>
          {busy ? <Spinner label={busyText} /> : confirm}
        </button>
      </div>
    </Modal>
  );
}

// "New post": the selected photos in the order the post shows them, a
// caption, Publish.
function PostDialog({ files, thumbFor, onMove, onRemove, onCancel, onPosted }: {
  files: MsgFile[];
  thumbFor: (f: MsgFile) => string;
  onMove: (path: string, offset: number) => void;
  onRemove: (path: string) => void;
  onCancel: () => void;
  onPosted: () => void;
}) {
  const titleId = useId();
  const titleRef = useRef<HTMLHeadingElement>(null);
  const captionId = useId();
  const [caption, setCaption] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ text: string; detail?: string } | null>(null);
  const stripRef = useRef<HTMLOListElement>(null);
  // A moved photo keeps the focus on its arrow, wherever it went.
  const [focusAfterMove, setFocusAfterMove] = useState<{ path: string; dir: -1 | 1 } | null>(null);
  // A removed one hands it to its neighbour's remove button ("" when none
  // is left: to the dialog).
  const [focusAfterRemove, setFocusAfterRemove] = useState<string | null>(null);
  // A phone's keyboard would cover the photos: the caption waits for a tap there.
  const typeAtOnce = useMedia("(hover: hover) and (pointer: fine)");

  useLayoutEffect(() => {
    if (!focusAfterMove) return;
    const item = stripRef.current?.querySelector<HTMLElement>(`[data-path="${CSS.escape(focusAfterMove.path)}"]`);
    const btn = item?.querySelector<HTMLButtonElement>(`[data-dir="${focusAfterMove.dir}"]`);
    const other = item?.querySelector<HTMLButtonElement>(`[data-dir="${-focusAfterMove.dir}"]`);
    (btn && !btn.disabled ? btn : other)?.focus();
    item?.scrollIntoView({ block: "nearest", inline: "nearest" });
    setFocusAfterMove(null);
  }, [focusAfterMove]);
  useLayoutEffect(() => {
    if (focusAfterRemove == null) return;
    const btn = focusAfterRemove
      ? stripRef.current?.querySelector<HTMLElement>(`[data-path="${CSS.escape(focusAfterRemove)}"] .pg-strip-remove`)
      : null;
    (btn ?? titleRef.current?.closest<HTMLElement>("[role=dialog]"))?.focus();
    setFocusAfterRemove(null);
  }, [focusAfterRemove]);

  const publish = async () => {
    if (!files.length || busy) return;
    setBusy(true);
    setError(null);
    try {
      const resp = await ask({
        $case: "reqNewSocialPublication",
        reqNewSocialPublication: { text: caption.trim(), paths: files.map((f) => f.path) },
      });
      if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
        onPosted();
        return;
      }
      setError({ text: "Couldn't publish the post. Try again.", detail: resp.errorMessage || undefined });
    } catch {
      setError({ text: "Couldn't reach your device. Check the connection and try again." });
    }
    setBusy(false);
  };

  return (
    <Modal labelledBy={titleId} busy={busy} wide onClose={onCancel}>
      <h2 id={titleId} ref={titleRef} className="pg-dlg-title">New Social Post</h2>
      {files.length ? (
        <ol className="pg-strip" ref={stripRef} aria-label="Photos in the post, in order">
          {files.map((f, i) => {
            const thumb = thumbFor(f);
            const what = `${isVideoFile(f) ? "video" : "photo"} ${i + 1}`;
            return (
              <li key={f.path} className="pg-strip-item" data-path={f.path}>
                <div className="pg-strip-thumb">
                  {thumb ? <img src={thumb} alt="" draggable={false} /> : <ImagesIcon size={24} />}
                  <span className="pg-strip-no" aria-hidden="true">{i + 1}</span>
                  {isVideoFile(f) && <span className="pg-strip-video"><PlayGlyph /></span>}
                </div>
                <button
                  type="button"
                  className="pg-strip-remove"
                  onClick={() => {
                    setFocusAfterRemove(files[i + 1]?.path ?? files[i - 1]?.path ?? "");
                    onRemove(f.path);
                  }}
                  aria-label={`Remove ${what}`}
                  data-tip="Remove"
                  disabled={busy}
                >
                  <CloseIcon size={16} />
                </button>
                <div className="pg-strip-moves">
                  <button
                    type="button"
                    className="pg-strip-btn"
                    data-dir="-1"
                    disabled={busy || i === 0}
                    aria-label={`Move ${what} earlier`}
                    onClick={() => { onMove(f.path, -1); setFocusAfterMove({ path: f.path, dir: -1 }); }}
                  >
                    <ChevronLeftIcon size={20} />
                  </button>
                  <button
                    type="button"
                    className="pg-strip-btn"
                    data-dir="1"
                    disabled={busy || i === files.length - 1}
                    aria-label={`Move ${what} later`}
                    onClick={() => { onMove(f.path, 1); setFocusAfterMove({ path: f.path, dir: 1 }); }}
                  >
                    <ChevronRightIcon size={20} />
                  </button>
                </div>
              </li>
            );
          })}
        </ol>
      ) : (
        <p className="pg-dlg-text">No photos left in this post.</p>
      )}
      <div className="pg-field">
        <label className="pg-label" htmlFor={captionId}>Caption</label>
        <textarea
          id={captionId}
          className="pg-input pg-textarea"
          value={caption}
          onChange={(e) => setCaption(e.target.value)}
          placeholder="Say something about them (optional)"
          disabled={busy}
          data-autofocus={typeAtOnce ? "" : undefined}
          onKeyDown={(e) => {
            // Ctrl/Cmd+Enter publishes; Enter alone is a new line.
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); void publish(); }
          }}
        />
      </div>
      {error && (
        <p className="pg-dlg-error" role="alert">
          {error.text}
          {error.detail && <span className="pg-dlg-detail">{error.detail}</span>}
        </p>
      )}
      <div className="pg-dlg-actions">
        <button type="button" className="pg-btn quiet" onClick={onCancel} disabled={busy}>Cancel</button>
        <button type="button" className="pg-btn primary" onClick={() => void publish()} disabled={busy || !files.length} aria-busy={busy}>
          {busy ? <Spinner label="Publishing…" /> : "Publish"}
        </button>
      </div>
    </Modal>
  );
}

// "Add to collection": an existing collection, or a new one named here.
function CollectDialog({ files, exclude, onCancel, onDone }: {
  files: MsgFile[];
  // The open collection: its photos are in it already.
  exclude: string;
  onCancel: () => void;
  onDone: (g: OpenGroup) => void;
}) {
  const titleId = useId();
  const nameId = useId();
  const groups = useGroups();
  // Fresh counts (and anything made elsewhere) when the list was loaded
  // before; useGroups loads it the first time itself.
  const loadedAtStartRef = useRef(groups.loaded);
  useEffect(() => {
    if (loadedAtStartRef.current) void reloadGroups().catch(() => {});
  }, []);
  const list = groups.items.filter((g) => g.id !== exclude);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  // What is being saved: "new", or the id of a collection.
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const paths = files.map((f) => f.path);
  // With nothing to add to, naming a new one is the only thing to do.
  const showForm = creating || (groups.loaded && list.length === 0);
  // Escape in the name goes back to the list, and the focus to "New
  // collection…" (the field it was in is gone).
  const newRef = useRef<HTMLButtonElement>(null);
  const backToListRef = useRef(false);
  useLayoutEffect(() => {
    if (creating || !backToListRef.current) return;
    backToListRef.current = false;
    newRef.current?.focus();
  }, [creating]);

  const add = async (g: ImageGroup) => {
    setBusy(g.id);
    setError(null);
    try {
      const resp = await ask({ $case: "reqAddToImageGroup", reqAddToImageGroup: { groupId: g.id, paths } });
      if (acked(resp)) {
        void reloadGroups().catch(() => {});
        onDone({ id: g.id, name: g.name, fileCount: g.fileCount });
        return;
      }
      setError(`Couldn't add them to “${g.name}”. Try again.`);
    } catch {
      setError("Couldn't reach your device. Try again.");
    }
    setBusy(null);
  };

  const create = async () => {
    const trimmed = name.trim();
    if (!trimmed || busy) return;
    setBusy("new");
    setError(null);
    try {
      // The answer's cover goes unused (reloadGroups lists them again):
      // the small one costs least.
      const resp = await ask({ $case: "reqCreateImageGroup", reqCreateImageGroup: { name: trimmed, paths, smallThumbnails: true } });
      const g = resp.payload?.$case === "respImageGroup" ? resp.payload.respImageGroup.group : undefined;
      if (g) {
        void reloadGroups().catch(() => {});
        onDone({ id: g.id, name: g.name, fileCount: g.fileCount });
        return;
      }
      setError("Couldn't create the collection. Try again.");
    } catch {
      setError("Couldn't reach your device. Try again.");
    }
    setBusy(null);
  };

  return (
    <Modal labelledBy={titleId} busy={busy != null} onClose={onCancel}>
      <div>
        <h2 id={titleId} className="pg-dlg-title">Add to collection</h2>
        <p className="pg-dlg-text">{whatLabel(files)}</p>
      </div>
      {showForm ? (
        <form className="pg-field" onSubmit={(e) => { e.preventDefault(); void create(); }}>
          <label className="pg-label" htmlFor={nameId}>New collection</label>
          <div className="pg-inline">
            <input
              id={nameId}
              className="pg-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Name, like Summer 2026"
              maxLength={120}
              autoFocus
              disabled={busy != null}
              onKeyDown={(e) => {
                // Escape leaves the name, not the whole dialog - when
                // there is a list to go back to.
                if (e.key === "Escape" && list.length > 0) {
                  e.preventDefault();
                  e.stopPropagation();
                  backToListRef.current = true;
                  setCreating(false);
                }
              }}
            />
            <button type="submit" className="pg-btn primary" disabled={!name.trim() || busy != null} aria-busy={busy === "new"}>
              {busy === "new" ? <Spinner label="Creating…" /> : "Create"}
            </button>
          </div>
        </form>
      ) : (
        <button type="button" ref={newRef} className="pg-pick pg-pick-new" onClick={() => setCreating(true)} disabled={busy != null} data-autofocus>
          <span className="pg-pick-cover"><PlusIcon size={22} /></span>
          <span className="pg-pick-text"><span className="pg-pick-name">New collection…</span></span>
        </button>
      )}
      {!groups.loaded ? (
        <ul className="pg-pick-list" aria-hidden="true">
          {[0, 1, 2].map((i) => (
            <li key={i} className="pg-pick pg-pick-skel"><span className="pg-pick-cover" /><span className="pg-pick-text"><span /><span /></span></li>
          ))}
        </ul>
      ) : list.length > 0 && (
        <ul className="pg-pick-list" aria-label="Collections">
          {list.map((g) => {
            const cover = groups.thumbs.get(g.id);
            return (
              <li key={g.id}>
                <button type="button" className="pg-pick" onClick={() => void add(g)} disabled={busy != null}>
                  {cover
                    ? <img className="pg-pick-cover" src={cover} alt="" />
                    : <span className="pg-pick-cover"><CollectionsIcon size={22} /></span>}
                  <span className="pg-pick-text">
                    <span className="pg-pick-name">{g.name}</span>
                    <span className="pg-pick-count">{count(g.fileCount, "item")}</span>
                  </span>
                  {busy === g.id && <Spinner />}
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {error && <p className="pg-dlg-error" role="alert">{error}</p>}
      <div className="pg-dlg-actions">
        <button type="button" className="pg-btn quiet" onClick={onCancel} disabled={busy != null}>Cancel</button>
      </div>
    </Modal>
  );
}

// The share link, for copying by hand (see DialogState's "link").
function LinkDialog({ link, onClose }: { link: string; onClose: () => void }) {
  const titleId = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      // The field is selected: the system's own copy works.
    }
  };
  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div>
        <h2 id={titleId} className="pg-dlg-title">Your link is ready</h2>
        <p className="pg-dlg-text">Anyone with this link can download these files.</p>
      </div>
      <div className="pg-inline">
        <input className="pg-input" readOnly value={link} onFocus={(e) => e.currentTarget.select()} aria-label="Link" data-autofocus />
        <button type="button" className="pg-btn primary" onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</button>
      </div>
      <div className="pg-dlg-actions">
        <button type="button" className="pg-btn quiet" onClick={onClose}>Done</button>
      </div>
    </Modal>
  );
}

// ---- empty states -------------------------------------------------------------------

function Empty({ icon, title, text, children }: { icon: ReactNode; title: string; text: string; children?: ReactNode }) {
  return (
    <div className="pg-empty">
      <div className="pg-empty-art">{icon}</div>
      <h2 className="pg-empty-title">{title}</h2>
      <p className="pg-empty-text">{text}</p>
      {children}
    </div>
  );
}
