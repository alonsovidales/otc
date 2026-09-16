// SPDX-License-Identifier: AGPL-3.0-or-later

// src/components/PhotoGallery.tsx
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope, File as MsgFile, TagsList, FileExifInfo, Person } from "../proto/messages";
import { loadPhotoSearchTags, savePhotoSearchTags } from "../net/uiState";
import './PhotoGallery.css';

type Chip = string;
type Token = string | null;

// ---- helpers ---------------------------------------------------------------
const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  if (u8.byteLength === 0) return "";
  return URL.createObjectURL(new Blob([u8], { type: mime }));
};
const fileKey = (f: MsgFile, idx?: number) =>
  `${f.path || ""}#${f.hash || ""}#${f.mime || ""}#${f.size || 0}#${idx ?? -1}`;

// ===========================================================================

export default function PhotoGallery() {
  // -------- tags/typeahead --------------------------------------------------
  const [allTags, setAllTags] = useState<string[]>([]);
  // Issue #53 follow-up: a reload restores the Photos tab itself now, but
  // used to still always drop the applied tag search rather than
  // wherever the user had actually filtered to.
  const [chips, setChips] = useState<Chip[]>(() => loadPhotoSearchTags());
  const [input, setInput] = useState("");
  const [showSuggest, setShowSuggest] = useState(false);

  const suggestions = useMemo(() => {
    const q = input.trim().toLowerCase();
    if (!q) return [];
    return allTags.filter(t => t.toLowerCase().startsWith(q)).slice(0, 12);
  }, [allTags, input]);

  const addChip = (t: string) => {
    const tag = t.trim();
    if (!tag) return;
    setChips(prev => (prev.includes(tag) ? prev : [...prev, tag]));
    setInput("");
    setShowSuggest(false);
    // search will auto-trigger via chips effect
  };
  const removeChip = (t: string) => setChips(prev => prev.filter(x => x !== t));

  // -------- people filter (issue #52 follow-up: lives next to the tag
  // search bar, not a separate People screen) --------------------------------
  const [allPeople, setAllPeople] = useState<Person[]>([]);
  // Multiple people selected means AND, not OR - see dao.SearchMedia on
  // the backend: a photo must contain a face matched to *every* person
  // selected here, not just one of them.
  const [selectedPeople, setSelectedPeople] = useState<string[]>([]);
  const [editingPersonId, setEditingPersonId] = useState<string | null>(null);
  const [editingPersonName, setEditingPersonName] = useState("");
  const [confirmDeletePersonId, setConfirmDeletePersonId] = useState<string | null>(null);
  // Issue #74: merge two people the model split into separate identities
  // (different angle/lighting missed the same-person threshold). A second,
  // narrower "pick mode" layered on the person strip rather than reusing
  // selectedPeople - that state means "search filter", a different thing
  // people already select multiple of for AND-search, and conflating the
  // two would make clicking a person while merging also change the filter.
  const [mergeTargetId, setMergeTargetId] = useState<string | null>(null);
  const [pendingMerge, setPendingMerge] = useState<{ target: Person; source: Person } | null>(null);
  const personThumbURLs = useRef<Map<string, string>>(new Map());
  // Issue #76: the strip used to wrap onto as many rows as there were
  // people, pushing the photo grid further down the more faces got
  // recognized. Collapse it to one row by default and only reveal an
  // "expand" toggle when there's actually a second row hiding - measured
  // from the real DOM rather than guessed from a fixed people-per-row
  // count, since that count depends on the strip's own (responsive) width.
  const peopleStripRef = useRef<HTMLDivElement>(null);
  const [peopleExpanded, setPeopleExpanded] = useState(false);
  const [peopleOverflowing, setPeopleOverflowing] = useState(false);
  const [peopleRowHeight, setPeopleRowHeight] = useState<number | null>(null);

  const loadPeople = useCallback(async () => {
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = { $case: "reqListPeople", reqListPeople: {} };
    });
    if (resp.payload?.$case === "respPeople") {
      setAllPeople(resp.payload.respPeople.people ?? []);
    }
  }, []);

  const togglePerson = (id: string) => setSelectedPeople(prev =>
    prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id]
  );

  const startRenamePerson = (p: Person) => {
    setEditingPersonId(p.id);
    setEditingPersonName(p.name);
  };
  const commitRenamePerson = async (id: string) => {
    const name = editingPersonName.trim();
    setEditingPersonId(null);
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = { $case: "reqRenamePerson", reqRenamePerson: { id, name } };
    });
    if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
      setAllPeople(prev => prev.map(p => (p.id === id ? { ...p, name } : p)));
    }
  };
  const deletePerson = async (id: string) => {
    setConfirmDeletePersonId(null);
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = { $case: "reqDeletePerson", reqDeletePerson: { id } };
    });
    if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
      setAllPeople(prev => prev.filter(p => p.id !== id));
      setSelectedPeople(prev => prev.filter(x => x !== id));
    }
  };
  // Clicking a person's avatar while merge-picking is active merges instead
  // of toggling the search filter (see mergeTargetId's own doc comment) -
  // this is that branch, invoked from the thumb's onClick below.
  const pickMergeTarget = (p: Person) => {
    if (mergeTargetId === p.id) {
      setMergeTargetId(null); // clicked the target again - cancel picking
      return;
    }
    const target = allPeople.find(x => x.id === mergeTargetId);
    setMergeTargetId(null);
    if (target) setPendingMerge({ target, source: p });
  };
  const confirmMerge = async () => {
    if (!pendingMerge) return;
    const { target, source } = pendingMerge;
    setPendingMerge(null);
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = {
        $case: "reqMergePeople",
        reqMergePeople: { targetId: target.id, sourceIds: [source.id] },
      };
    });
    if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
      setSelectedPeople(prev => prev.filter(x => x !== source.id));
      await loadPeople(); // re-sort by the merged face count (issue #75) rather than patch counts by hand
    }
  };
  const personThumb = (p: Person) => {
    const cached = personThumbURLs.current.get(p.id);
    if (cached) return cached;
    const url = bytesToURL(p.coverThumbnail as unknown as Uint8Array, "image/jpeg");
    if (url) personThumbURLs.current.set(p.id, url);
    return url;
  };
  useEffect(() => () => { personThumbURLs.current.forEach(u => URL.revokeObjectURL(u)); }, []);

  // Re-measure whenever the people list changes and whenever the strip's
  // own width changes (window resize, sidebar toggle, etc.) - a
  // ResizeObserver rather than a one-shot effect because the wrap point
  // depends on layout, not just on how many people there are.
  useEffect(() => {
    const el = peopleStripRef.current;
    if (!el) return;
    const measure = () => {
      const firstItem = el.querySelector<HTMLElement>(".pg-person");
      if (!firstItem) { setPeopleOverflowing(false); return; }
      const rowHeight = firstItem.offsetHeight;
      setPeopleRowHeight(rowHeight);
      // scrollHeight is the full, un-clipped content height even while
      // overflow:hidden + max-height are actively clipping it - that's
      // exactly what lets this double as both the measurement and the
      // thing being collapsed.
      setPeopleOverflowing(el.scrollHeight > rowHeight + 2);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [allPeople]);

  // -------- data & paging ---------------------------------------------------
  const [items, setItems] = useState<MsgFile[]>([]);
  const mapRef = useRef<Map<string, MsgFile>>(new Map()); // dedupe
  const [token, setToken] = useState<Token>(null);
  const [loading, setLoading] = useState(false);
  const [endReached, setEndReached] = useState(false);

  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const observerRef = useRef<IntersectionObserver | null>(null);

  // -------- date scrubber (issue #77) ----------------------------------------
  // Per-month counts drive both the scrubber's year ticks and how many
  // placeholder squares to draw for a month that hasn't loaded yet. Only
  // meaningful against date order - a tag search sorts by relevance
  // (dao.SearchMedia switches to "order by score desc" whenever tags are
  // given), so the scrubber is hidden outright rather than showing ticks
  // against an order it doesn't actually reflect. A person filter alone is
  // fine - that keeps the default created-desc order.
  const [dateBuckets, setDateBuckets] = useState<{ month: string; count: number }[]>([]);
  // scrubFrac is the live drag position (0=newest/top, 1=oldest/bottom),
  // null whenever the user isn't actively dragging - drives the thumb/
  // tooltip only. placeholderCount is separate and outlives the drag: it
  // stays set (showing black squares in place of the real grid) from the
  // moment a drag starts until the jump-to-date fetch it triggers actually
  // resolves, so releasing the thumb doesn't flash an empty grid while the
  // real thumbnails are still in flight.
  const [scrubFrac, setScrubFrac] = useState<number | null>(null);
  const [placeholderCount, setPlaceholderCount] = useState<number | null>(null);
  const scrubTrackRef = useRef<HTMLDivElement | null>(null);
  const gridRef = useRef<HTMLDivElement | null>(null);

  const bucketIndex = useMemo(() => {
    let cum = 0;
    return dateBuckets.map(b => {
      const start = cum;
      cum += b.count;
      return { month: b.month, count: b.count, start, end: cum };
    });
  }, [dateBuckets]);
  const totalPhotos = bucketIndex.length ? bucketIndex[bucketIndex.length - 1].end : 0;
  const showScrubber = chips.length === 0 && bucketIndex.length > 0;

  // Needed to thin out year ticks that would otherwise overlap (see
  // yearTicks below) - the track's height only exists as a CSS percentage
  // until measured, and thinning has to happen in real pixels. The
  // scrubber div only exists once showScrubber is true, so this re-runs
  // when that flips rather than finding a null ref on first render
  // (before any buckets have loaded).
  const [trackHeight, setTrackHeight] = useState(0);
  useEffect(() => {
    const el = scrubTrackRef.current;
    if (!el) return;
    const measure = () => setTrackHeight(el.clientHeight);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [showScrubber]);

  // Ticks: one per year, positioned by cumulative photo count rather than
  // calendar-uniform spacing, so a drag fraction actually corresponds to
  // "how far into the library" that year sits (matches Google Photos'
  // own timeline, where a sparse year takes less track space than a busy
  // one). A run of sparse years can still land closer together than a
  // label is tall though - reproduced live as several years' labels
  // rendering stacked on top of each other, unreadable - so this also
  // drops any tick that would land within MIN_TICK_GAP_PX of the last
  // one actually kept, once the track's real height is known.
  const MIN_TICK_GAP_PX = 14;
  const yearTicks = useMemo(() => {
    if (!totalPhotos || !trackHeight) return [];
    const ticks: { year: string; pct: number }[] = [];
    let lastYear = "";
    let lastKeptPx = -Infinity;
    bucketIndex.forEach(b => {
      const year = b.month.slice(0, 4);
      if (year === lastYear) return;
      lastYear = year;
      const px = (b.start / totalPhotos) * trackHeight;
      if (px - lastKeptPx < MIN_TICK_GAP_PX) return;
      ticks.push({ year, pct: (b.start / totalPhotos) * 100 });
      lastKeptPx = px;
    });
    return ticks;
  }, [bucketIndex, totalPhotos, trackHeight]);

  const scrubTarget = useMemo(() => {
    if (scrubFrac == null || !totalPhotos) return null;
    const idx = scrubFrac * totalPhotos;
    return bucketIndex.find(b => idx >= b.start && idx < b.end) ?? bucketIndex[bucketIndex.length - 1] ?? null;
  }, [scrubFrac, bucketIndex, totalPhotos]);

  const MONTH_NAMES = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  const formatMonthLabel = (month: string) => {
    const [y, m] = month.split("-").map(Number);
    return `${MONTH_NAMES[(m || 1) - 1]} ${y}`;
  };

  const loadDateBuckets = useCallback(async () => {
    if (chips.length > 0) { setDateBuckets([]); return; }
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = {
        $case: "reqPhotoDateBuckets",
        reqPhotoDateBuckets: { tags: [], personIds: selectedPeople, includeVideos: false },
      };
    });
    if (resp.payload?.$case === "respPhotoDateBuckets") {
      setDateBuckets(resp.payload.respPhotoDateBuckets.buckets ?? []);
    }
  }, [chips.length, selectedPeople]);

  // -------- modal (hi-res) --------------------------------------------------
  const [openIdx, setOpenIdx] = useState<number | null>(null);
  const [hiURL, setHiURL] = useState<string | null>(null);

  // -------- "More info" panel (issue #41) ------------------------------------
  const [infoOpen, setInfoOpen] = useState(false);
  const [infoLoading, setInfoLoading] = useState(false);
  const [infoData, setInfoData] = useState<FileExifInfo | null>(null);

  // -------- requests --------------------------------------------------------
  const loadTags = useCallback(async () => {
    const resp = await useWS.request(e => {
      (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
    });
    if (resp.payload?.$case === "respTagsList") {
      const list = (resp.payload.respTagsList as TagsList).tags ?? [];
      setAllTags(list);
    }
  }, []);

  // Bumped every time chips/selectedPeople trigger a fresh search (see the
  // effect below) - fetchPage captures the value at call time and checks
  // it's unchanged before applying its response. Toggling a person filter
  // (or a tag) twice in quick succession fires two overlapping requests;
  // without this, whichever *response* happens to land last wins even if
  // it was for the *older* selection - reproduced live as: click a person
  // on then off quickly, the avatar shows selected/deselected correctly
  // but the grid shows the other request's (wrong) results, because that
  // one's reply simply arrived second.
  const searchGenRef = useRef(0);

  const fetchPage = useCallback(
    async (overrideToken?: Token, force = false, before?: Date) => {
      // force skips the loading/endReached guard: a deliberate fresh
      // search (chips just changed) always resets those to false right
      // before calling this, but that reset and this call happen in the
      // same tick - React hasn't re-rendered yet, so without `force` this
      // closure would still see whatever they were for the *previous*
      // search (e.g. endReached=true from having scrolled to the bottom
      // of it), silently no-op, and leave the old results on screen until
      // a full reload reset everything fresh.
      if (!force && (loading || endReached)) return;
      const myGen = searchGenRef.current;
      setLoading(true);
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = {
            $case: "reqSearchPhotos",
            reqSearchPhotos: {
              tags: chips,
              personIds: selectedPeople,
              token: overrideToken ?? token ?? "",
              // Issue #77: the date scrubber's "jump to date" - set only
              // by jumpToDate below, which also forces overrideToken/force
              // so this always starts a fresh, cutoff-filtered search.
              before,
            },
          };
        });
        // A newer search superseded this one while it was in flight -
        // discard rather than let a stale reply clobber current results.
        if (myGen !== searchGenRef.current) return;
        if (resp.payload?.$case !== "respListOfFiles") return;

        const lof = resp.payload.respListOfFiles!;
        const nextToken = lof.token || null;

        // dedupe via map
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
        if (added.length) setItems(prev => prev.concat(added));

        setToken(nextToken);
        setEndReached(!nextToken); // if no token back, we've reached the end
      } finally {
        // Only this request's own generation may clear loading - a stale
        // one finishing after a newer search started must not report
        // "done" for a fetch that isn't actually the current one.
        if (myGen === searchGenRef.current) setLoading(false);
      }
    },
    [chips, selectedPeople, token, loading, endReached]
  );

  // Issue #77: the date scrubber's "jump to date" - a reset exactly like
  // the chips/selectedPeople effect below does for a fresh filter, plus
  // the `before` cutoff that anchors the fresh search to the target
  // month's own last instant (so it starts at that month's newest photo
  // and reads backward from there, same as scrolling there normally
  // would). placeholderCount is left showing until this resolves (or is
  // superseded) - cleared here rather than by the caller so a jump that
  // gets superseded by a *newer* jump/filter change doesn't clear
  // placeholders that newer request is still relying on.
  const jumpToDate = useCallback(async (month: string) => {
    const [y, m] = month.split("-").map(Number);
    const before = new Date(y, m, 0, 23, 59, 59, 999); // last instant of `month`
    searchGenRef.current += 1;
    const myGen = searchGenRef.current;
    setItems([]);
    mapRef.current = new Map();
    setToken(null);
    setEndReached(false);
    // Scrolling to the top already happened in handleScrubMove, the
    // moment the drag/click started - see its own comment.
    try {
      await fetchPage("", true, before);
    } finally {
      if (myGen === searchGenRef.current) setPlaceholderCount(null);
    }
  }, [fetchPage]);

  const handleScrubMove = (clientY: number) => {
    const el = scrubTrackRef.current;
    if (!el || !totalPhotos) return;
    if (scrubFrac == null) {
      // First move of a new drag (a plain click/tap counts too, since
      // pointerdown itself calls this once) - scroll away immediately
      // rather than waiting for the jump to actually resolve, so the
      // current cards visibly start moving out of the way the moment you
      // touch the scrubber, the way Google Photos' own does.
      gridRef.current?.scrollIntoView({ block: "start" });
    }
    const rect = el.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
    setScrubFrac(frac);
    // No network call here at all - the placeholder count comes straight
    // out of the already-fetched bucket counts, which is the whole point:
    // dragging fast across years costs nothing but re-renders.
    const idx = frac * totalPhotos;
    const bucket = bucketIndex.find(b => idx >= b.start && idx < b.end) ?? bucketIndex[bucketIndex.length - 1];
    if (bucket) setPlaceholderCount(bucket.count);
  };
  const onScrubPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    handleScrubMove(e.clientY);
  };
  const onScrubPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (scrubFrac != null) handleScrubMove(e.clientY);
  };
  const onScrubPointerUp = () => {
    const target = scrubTarget; // capture before clearing scrubFrac below
    setScrubFrac(null);
    if (target) void jumpToDate(target.month);
    else setPlaceholderCount(null);
  };

  // open modal and fetch hi-res for current index
  const openAt = useCallback(
    async (idx: number) => {
      setOpenIdx(idx);
      setHiURL(null);
      setInfoOpen(false);
      setInfoData(null);
      setZoomScale(1);
      const f = items[idx];
      try {
        const resp = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetFile", reqGetFile: { path: f.path } };
        });
        if (resp.payload?.$case === "respFile") {
          const full = resp.payload.respFile!;
          setHiURL(bytesToURL(full.content, full.mime || "image/jpeg"));
        }
      } catch {
        // keep thumb
      }
    },
    [items]
  );

  // Issue #41: fetch and show a photo/video's camera/EXIF metadata,
  // computed live on the server from the file's own bytes.
  const openInfo = useCallback(async () => {
    if (openIdx == null) return;
    setInfoOpen(true);
    setInfoLoading(true);
    setInfoData(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetFileInfo", reqGetFileInfo: { path: items[openIdx].path } };
      });
      if (resp.payload?.$case === "respFileInfo") {
        setInfoData(resp.payload.respFileInfo);
      }
    } finally {
      setInfoLoading(false);
    }
  }, [openIdx, items]);
  const closeInfo = useCallback(() => { setInfoOpen(false); setInfoData(null); }, []);

  // -------- initial load ----------------------------------------------------
  // Just the autocomplete tag list - the photo list itself is fetched by
  // the chips effect below, which also fires on mount (with whatever tags
  // were persisted from a previous session, per issue #53) so fetching it
  // here too was pure duplicate work, not just on first load but racing
  // this effect's own fetchPage against the chips effect's.
  useEffect(() => {
    loadTags();
    loadPeople();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Fetch fresh whenever chips or the selected people change - including on
  // mount, for whichever tags (possibly none) were persisted. force=true:
  // see fetchPage's own comment for why a plain (non-forced) call here
  // could silently do nothing.
  useEffect(() => {
    // Invalidates any still-in-flight fetchPage from the *previous*
    // selection before this one's own request even goes out - see
    // searchGenRef's doc comment.
    searchGenRef.current += 1;
    (async () => {
      setItems([]);
      mapRef.current = new Map();
      setToken(null);
      setEndReached(false);
      await fetchPage("", true);
    })();
    savePhotoSearchTags(chips);
    // A filter change makes any scrub in progress meaningless (its target
    // bucket was computed against the *previous* filter's counts) - drop
    // it rather than leave stale placeholders or a thumb positioned
    // against numbers that no longer apply.
    setScrubFrac(null);
    setPlaceholderCount(null);
    loadDateBuckets();
  }, [chips, selectedPeople]); // eslint-disable-line react-hooks/exhaustive-deps

  // -------- infinite scroll: one call at a time -----------------------------
  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      (entries) => {
        const ent = entries[0];
        if (!ent?.isIntersecting) return;
        if (!loading && !endReached) fetchPage();
      },
      { root: null, rootMargin: "600px 0px 0px 0px" }
    );
    observerRef.current = obs;
    obs.observe(node);
    return () => {
      obs.disconnect();
      observerRef.current = null;
    };
  }, [fetchPage, loading, endReached]);

  // -------- selection bar (issue #48: ordered, not a Set — post order
  // matches selection order, and can be explicitly fixed up via moveSel
  // rather than only by deselecting/reselecting everything) ------------------
  const [selOrder, setSelOrder] = useState<string[]>([]);
  const toggleSel = (p: string) => setSelOrder(prev =>
    prev.includes(p) ? prev.filter(x => x !== p) : [...prev, p]
  );
  const moveSel = (p: string, offset: number) => setSelOrder(prev => {
    const idx = prev.indexOf(p);
    const newIdx = idx + offset;
    if (idx < 0 || newIdx < 0 || newIdx >= prev.length) return prev;
    const next = [...prev];
    [next[idx], next[newIdx]] = [next[newIdx], next[idx]];
    return next;
  });
  const selectedPaths = selOrder;

  const shareInSocial = async () => {
    if (!selectedPaths.length) return;
    const caption = window.prompt("Caption:") ?? "";
    const resp = await useWS.request(e => {
      (e as any).payload = {
        $case: "reqNewSocialPublication",
        reqNewSocialPublication: { text: caption, paths: selectedPaths },
      };
    });
    if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
      alert("Shared: " + resp.payload.respNewSocial.uuid);
      setSelOrder([]);
    } else {
      alert("Error publishing");
    }
  };

  // Issue #45: delete every selected photo/video.
  const deleteSelected = async () => {
    if (!selectedPaths.length) return;
    if (!window.confirm(`Delete ${selectedPaths.length} item${selectedPaths.length > 1 ? "s" : ""}? This cannot be undone.`)) return;

    const toDelete = new Set(selectedPaths);
    for (const path of selectedPaths) {
      await useWS.request(e => {
        (e as any).payload = { $case: "reqDelFile", reqDelFile: { path } };
      });
    }

    setItems(prev => prev.filter(f => !toDelete.has(f.path)));
    toDelete.forEach(p => mapRef.current.delete(p));
    setSelOrder([]);
    // The modal may be pointing at an item that no longer exists (or whose
    // index shifted) once the deleted items are filtered out of `items`.
    setOpenIdx(null);
  };

  const shareOrDownload = async (openAfter: boolean) => {
    if (!selectedPaths.length) return;
    const r1 = await useWS.request(e => {
      (e as any).payload = { $case: "reqShareFilesLink", reqShareFilesLink: { paths: selectedPaths } };
    });
    if (r1.payload?.$case !== "respShareLink") { alert("Could not create link"); return; }
    const link = r1.payload.respShareLink.link;
    if (openAfter) window.open(link, "_blank");
    else {
      await navigator.clipboard?.writeText?.(link);
      alert("Link copied");
    }
  };

  // -------- keyboard in modal ----------------------------------------------
  useEffect(() => {
    if (openIdx == null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpenIdx(null);
      if (e.key === "ArrowRight" && openIdx < items.length - 1) openAt(openIdx + 1);
      if (e.key === "ArrowLeft" && openIdx > 0) openAt(openIdx - 1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openIdx, items.length, openAt]);

  // -------- swipe in modal (issue #18) --------------------------------------
  const modalTouchStartX = useRef<number | null>(null);
  // Issue #36: pinch-to-zoom, matching the native apps — a genuine 2-finger
  // pinch on touch devices, and trackpad pinch (which browsers report as a
  // ctrlKey+wheel event, there's no native "pinch" DOM event) on desktop.
  const [zoomScale, setZoomScale] = useState(1);
  const pinchStartDist = useRef<number | null>(null);
  const pinchStartScale = useRef(1);

  const touchDistance = (t: React.TouchList) => {
    const dx = t[0].clientX - t[1].clientX;
    const dy = t[0].clientY - t[1].clientY;
    return Math.hypot(dx, dy);
  };

  const onModalTouchStart = (e: React.TouchEvent) => {
    if (e.touches.length === 2) {
      pinchStartDist.current = touchDistance(e.touches);
      pinchStartScale.current = zoomScale;
    } else {
      modalTouchStartX.current = e.touches[0].clientX;
    }
  };
  const onModalTouchMove = (e: React.TouchEvent) => {
    if (e.touches.length === 2 && pinchStartDist.current) {
      e.preventDefault();
      const scale = pinchStartScale.current * (touchDistance(e.touches) / pinchStartDist.current);
      setZoomScale(Math.min(4, Math.max(1, scale)));
    }
  };
  const onModalTouchEnd = (e: React.TouchEvent) => {
    if (pinchStartDist.current != null) {
      pinchStartDist.current = null;
      return; // was pinching, not swiping — don't also page through images
    }
    if (modalTouchStartX.current == null || openIdx == null || zoomScale !== 1) return;
    const dx = e.changedTouches[0].clientX - modalTouchStartX.current;
    if (dx < -30 && openIdx < items.length - 1) openAt(openIdx + 1);
    else if (dx > 30 && openIdx > 0) openAt(openIdx - 1);
    modalTouchStartX.current = null;
  };
  const onModalWheel = (e: React.WheelEvent) => {
    if (!e.ctrlKey) return; // plain scroll shouldn't zoom, only trackpad pinch
    e.preventDefault();
    setZoomScale(s => Math.min(4, Math.max(1, s - e.deltaY * 0.01)));
  };

  // -------- render ----------------------------------------------------------
  return (
    <div className="pg-root">
      {/* top search with chips and suggestions */}
      <div className="pg-search">
        <div className="pg-chipbar">
          {chips.map((c) => (
            <span key={`chip-${c}`} className="pg-chip">
              {c}
              <button className="pg-chip-x" onClick={() => removeChip(c)} aria-label={`Remove ${c}`}>×</button>
            </span>
          ))}
          <input
            value={input}
            onChange={(e) => { setInput(e.target.value); setShowSuggest(true); }}
            onFocus={() => setShowSuggest(true)}
            onBlur={() => setTimeout(() => setShowSuggest(false), 100)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                if (suggestions.length === 1) addChip(suggestions[0]);
                else if (input.trim()) addChip(input.trim());
              } else if (e.key === "Backspace" && !input && chips.length) {
                removeChip(chips[chips.length - 1]);
              }
            }}
            placeholder="Type a tag…"
          />
        </div>

        {showSuggest && suggestions.length > 0 && (
          <div className="pg-suggest">
            {suggestions.map(s => (
              <div key={`sug-${s}`} className="pg-suggest-item" onMouseDown={() => addChip(s)}>{s}</div>
            ))}
          </div>
        )}

        {/* Issue #52 follow-up: filter by person right here, alongside
            tags - combines with them on the same search (e.g. a person
            plus the "dogs" tag), and selecting more than one person means
            photos containing all of them, not just one. Nothing renders at
            all when there's no one to filter by yet - no explanatory
            hint needed. */}
        {allPeople.length > 0 && (
          <>
            {mergeTargetId && (
              <div className="pg-people-hint pg-merge-hint">
                Merging into <strong>{allPeople.find(x => x.id === mergeTargetId)?.name || "Unnamed"}</strong> —
                tap another person below to merge them in, or{" "}
                <button className="pg-link-btn" onClick={() => setMergeTargetId(null)}>cancel</button>.
              </div>
            )}
            <div
              className="pg-people-strip"
              ref={peopleStripRef}
              style={!peopleExpanded && peopleOverflowing && peopleRowHeight
                ? { maxHeight: peopleRowHeight, overflow: "hidden" }
                : undefined}
            >
              {allPeople.map(p => {
                const selected = selectedPeople.includes(p.id);
                const isMergeTarget = mergeTargetId === p.id;
                const thumb = personThumb(p);
                return (
                  <div key={p.id} className={`pg-person${selected ? " selected" : ""}${isMergeTarget ? " merge-target" : ""}`}>
                    <button
                      className="pg-person-thumb"
                      onClick={() => (mergeTargetId ? pickMergeTarget(p) : togglePerson(p.id))}
                      title={mergeTargetId ? (isMergeTarget ? "Cancel merge" : `Merge into ${allPeople.find(x => x.id === mergeTargetId)?.name || "Unnamed"}`) : (p.name || "Unnamed")}
                    >
                      {thumb ? <img src={thumb} alt={p.name || "Unnamed"} /> : <span className="pg-person-ph">🙂</span>}
                    </button>
                    {editingPersonId === p.id ? (
                      <input
                        className="pg-person-name-input"
                        autoFocus
                        placeholder="Name…"
                        value={editingPersonName}
                        onChange={e => setEditingPersonName(e.target.value)}
                        onBlur={() => void commitRenamePerson(p.id)}
                        onKeyDown={e => {
                          if (e.key === "Enter") void commitRenamePerson(p.id);
                          if (e.key === "Escape") setEditingPersonId(null);
                        }}
                      />
                    ) : (
                      <button className="pg-person-name" onClick={() => startRenamePerson(p)}>
                        {p.name || "Unnamed"}
                      </button>
                    )}
                    <div className="pg-person-actions">
                      <button
                        className="pg-person-merge"
                        title="Merge another person into this one"
                        onClick={() => setMergeTargetId(p.id)}
                      >
                        🔗
                      </button>
                      <button
                        className="pg-person-delete"
                        title="Delete this person"
                        onClick={() => setConfirmDeletePersonId(p.id)}
                      >
                        🗑️
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
            {peopleOverflowing && (
              <button
                className="pg-link-btn pg-people-toggle"
                onClick={() => setPeopleExpanded(v => !v)}
              >
                <span>{peopleExpanded ? "Show less" : `Show all (${allPeople.length})`}</span>
                <svg
                  className={`pg-people-toggle-chevron${peopleExpanded ? " expanded" : ""}`}
                  width="10" height="6" viewBox="0 0 10 6" aria-hidden="true"
                >
                  <path d="M1 1l4 4 4-4" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round" />
                </svg>
              </button>
            )}
          </>
        )}
      </div>

      {confirmDeletePersonId && (
        <div className="pg-modal" onClick={() => setConfirmDeletePersonId(null)}>
          <div className="pg-modal-inner pg-person-confirm" onClick={e => e.stopPropagation()}>
            <p>Delete this person? This removes every face matched to them — it can't be undone.</p>
            <div className="pg-modal-actions">
              <button onClick={() => setConfirmDeletePersonId(null)}>Cancel</button>
              <button className="pg-danger" onClick={() => void deletePerson(confirmDeletePersonId)}>Delete</button>
            </div>
          </div>
        </div>
      )}

      {pendingMerge && (
        <div className="pg-modal" onClick={() => setPendingMerge(null)}>
          <div className="pg-modal-inner pg-person-confirm" onClick={e => e.stopPropagation()}>
            <p>
              Merge <strong>{pendingMerge.source.name || "Unnamed"}</strong> into{" "}
              <strong>{pendingMerge.target.name || "Unnamed"}</strong>? Every photo of{" "}
              {pendingMerge.source.name || "Unnamed"} will show up under{" "}
              {pendingMerge.target.name || "Unnamed"} instead — this can't be undone.
            </p>
            <div className="pg-modal-actions">
              <button onClick={() => setPendingMerge(null)}>Cancel</button>
              <button className="pg-danger" onClick={() => void confirmMerge()}>Merge</button>
            </div>
          </div>
        </div>
      )}

      {/* grid - while the date scrubber has a target bucket (dragging, or
          the jump it triggered still in flight), placeholder squares
          stand in for the real grid rather than showing whatever was
          scrolled to before the jump started. Capped at 300 - a month
          with thousands of photos doesn't need that many real DOM nodes
          just to convey "this is a lot of squares". */}
      <div className="pg-grid" ref={gridRef}>
        {placeholderCount != null ? (
          Array.from({ length: Math.min(placeholderCount, 300) }, (_, i) => (
            <div key={`ph-${i}`} className="pg-cell pg-cell-placeholder" />
          ))
        ) : (
          <>
            {items.map((f, i) => {
              const key = fileKey(f, i); // unique key (fixes React warnings)
              const thumb = bytesToURL(f.content, f.mime || "image/jpeg");
              const selIdx = selOrder.indexOf(f.path);
              return (
                <div key={key} className="pg-cell">
                  <label className="pg-check">
                    <input type="checkbox" checked={selIdx >= 0} onChange={() => toggleSel(f.path)} />
                  </label>
                  {/* Issue #48: a numbered badge instead of just a checkmark
                      shows the post order directly in the grid. */}
                  {selIdx >= 0 && <span className="pg-order-badge">{selIdx + 1}</span>}
                  <button className="pg-thumb" title={f.path} onClick={() => openAt(i)}>
                    <img src={thumb} alt={f.path} loading="lazy" />
                  </button>
                </div>
              );
            })}
            <div ref={sentinelRef} style={{ height: 1 }} />
          </>
        )}
      </div>

      {/* Issue #77: Google-Photos-style date scrubber - year ticks always
          visible, a floating month/year tooltip only while dragging. Fixed
          to the viewport rather than sized to the grid's own (ever-
          growing, as pages load) content height, since it represents the
          whole library's timeline, not just what's currently mounted.
          (iOS hides its ticks until you touch the scrubber instead - web's
          own screen is roomier and this read fine always-on, so it stays
          as it was.) */}
      {showScrubber && (
        <div
          className="pg-scrubber"
          ref={scrubTrackRef}
          onPointerDown={onScrubPointerDown}
          onPointerMove={onScrubPointerMove}
          onPointerUp={onScrubPointerUp}
          onPointerCancel={onScrubPointerUp}
        >
          {yearTicks.map(t => (
            <span key={t.year} className="pg-scrubber-tick" style={{ top: `${t.pct}%` }}>{t.year}</span>
          ))}
          {scrubTarget && (
            <>
              <div className="pg-scrubber-thumb" style={{ top: `${(scrubFrac ?? 0) * 100}%` }} />
              <div className="pg-scrubber-tooltip" style={{ top: `${(scrubFrac ?? 0) * 100}%` }}>
                {formatMonthLabel(scrubTarget.month)}
              </div>
            </>
          )}
        </div>
      )}

      {/* Issue #48: the grid's own order isn't necessarily post order (it's
          whatever the search/feed returned) - this strip shows the actual
          order and lets it be fixed up directly. */}
      {selOrder.length > 0 && (
        <div className="pg-order-strip">
          <span className="pg-order-strip-label">Order in post:</span>
          {selOrder.map((path, idx) => {
            const item = items.find(it => it.path === path);
            const thumb = item ? bytesToURL(item.content, item.mime || "image/jpeg") : "";
            return (
              <div key={path} className="pg-order-thumb">
                <img src={thumb} alt={path} />
                <button className="pg-order-remove" title="Remove" onClick={() => toggleSel(path)}>×</button>
                <div className="pg-order-controls">
                  <button disabled={idx === 0} onClick={() => moveSel(path, -1)}>‹</button>
                  <span>{idx + 1}</span>
                  <button disabled={idx === selOrder.length - 1} onClick={() => moveSel(path, 1)}>›</button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {/* bottom actions */}
      {selOrder.length > 0 && (
        <div className="pg-actions">
          <button onClick={shareInSocial}>Share in social</button>
          <button onClick={() => alert("Create group (not implemented)")}>Create group</button>
          <button onClick={() => alert("Add to existing group (not implemented)")}>Add to group</button>
          <button onClick={() => shareOrDownload(false)}>Share link</button>
          <button onClick={() => shareOrDownload(true)}>Download as ZIP</button>
          <button className="pg-danger" onClick={() => void deleteSelected()}>Delete</button>
          <span className="pg-count">{selOrder.length} selected</span>
        </div>
      )}

      {/* modal */}
      {openIdx != null && (
        <div className="pg-modal" onClick={() => setOpenIdx(null)}>
          <div className="pg-modal-inner" onClick={(e) => e.stopPropagation()}>
            <button className="pg-close" onClick={() => setOpenIdx(null)}>×</button>
            {/* Issue #41: camera/EXIF metadata + location, on demand. */}
            <button className="pg-info-btn" title="More info" onClick={openInfo}>ⓘ</button>
            {openIdx > 0 && <button className="pg-nav left" onClick={() => openAt(openIdx - 1)}>‹</button>}
            {openIdx < items.length - 1 && <button className="pg-nav right" onClick={() => openAt(openIdx + 1)}>›</button>}
            <div
              className="pg-modal-imgwrap"
              onTouchStart={onModalTouchStart}
              onTouchMove={onModalTouchMove}
              onTouchEnd={onModalTouchEnd}
              onWheel={onModalWheel}
            >
              {(() => {
                const f = items[openIdx];
                const thumb = bytesToURL(f.content, f.mime || "image/jpeg");
                return (
                  <img
                    src={hiURL || thumb}
                    alt={f.path}
                    style={{ transform: `scale(${zoomScale})`, transition: pinchStartDist.current ? "none" : "transform 0.15s ease-out" }}
                  />
                );
              })()}
            </div>

            {infoOpen && (
              <div className="pg-info-panel" onClick={(e) => e.stopPropagation()}>
                <div className="pg-info-hdr">
                  <span>More info</span>
                  <button className="pg-info-close" onClick={closeInfo}>×</button>
                </div>
                {infoLoading ? (
                  <div className="pg-info-loading">Loading…</div>
                ) : !infoData ? (
                  <div className="pg-info-loading">No metadata found</div>
                ) : (
                  <div className="pg-info-body">
                    {(infoData.cameraMake || infoData.cameraModel) && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Camera</span>
                        <span>{[infoData.cameraMake, infoData.cameraModel].filter(Boolean).join(" ")}</span>
                      </div>
                    )}
                    {infoData.takenAt && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Taken</span>
                        <span>{infoData.takenAt.toLocaleString()}</span>
                      </div>
                    )}
                    {!!(infoData.width && infoData.height) && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Dimensions</span>
                        <span>{infoData.width} × {infoData.height}</span>
                      </div>
                    )}
                    {infoData.exposureTime && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Exposure</span>
                        <span>{infoData.exposureTime}</span>
                      </div>
                    )}
                    {infoData.fNumber && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Aperture</span>
                        <span>{infoData.fNumber}</span>
                      </div>
                    )}
                    {!!infoData.iso && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">ISO</span>
                        <span>{infoData.iso}</span>
                      </div>
                    )}
                    {infoData.focalLength && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Focal length</span>
                        <span>{infoData.focalLength}</span>
                      </div>
                    )}
                    {(infoData.city || infoData.country) && (
                      <div className="pg-info-row">
                        <span className="pg-info-label">Location</span>
                        <span>{[infoData.city, infoData.country].filter(Boolean).join(", ")}</span>
                      </div>
                    )}
                    {infoData.hasGps && (
                      <iframe
                        className="pg-info-map"
                        title="Photo location"
                        loading="lazy"
                        src={`https://www.openstreetmap.org/export/embed.html?bbox=${infoData.longitude - 0.02}%2C${infoData.latitude - 0.02}%2C${infoData.longitude + 0.02}%2C${infoData.latitude + 0.02}&layer=mapnik&marker=${infoData.latitude}%2C${infoData.longitude}`}
                      />
                    )}
                    {!infoData.cameraMake && !infoData.cameraModel && !infoData.hasGps && !infoData.exposureTime && (
                      <div className="pg-info-loading">No EXIF metadata in this file</div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}

      {/* styles */}
      <style>{`
      `}</style>
    </div>
  );
}

