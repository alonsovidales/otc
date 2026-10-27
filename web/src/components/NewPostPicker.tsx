// SPDX-License-Identifier: AGPL-3.0-or-later

// src/components/NewPostPicker.tsx
//
// Issue #32: a dedicated "compose a new post" screen opened from the
// Social feed's "+" button. Deliberately NOT the Photo Gallery reused with
// bits hidden — no delete/share/download/groups here, no full-screen
// viewer. Just: filter by tag, tap photos to pick them, write a caption,
// hit Publish.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import { uploadFile } from "../net/upload";
import { fileSize } from "../net/fileSize";
import type { RespEnvelope, File as MsgFile, TagsList } from "../proto/messages";
import VideoTrimmer from "./VideoTrimmer";
import { formatTimecode, type TrimRange } from "./videoTrim";
import { useObjectURLs } from "./useObjectURLs";
import { usePageRetry } from "./usePageRetry";
import "./NewPostPicker.css";
import { useT, type TFunction } from "../i18n";
import { asIs, percentText, say, type UiText } from "./uiText";

const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  if (u8.byteLength === 0) return "";
  return URL.createObjectURL(new Blob([u8], { type: mime }));
};
// A library tile's thumbnail - see the comment where the grid draws it.
const tileThumb = (f: MsgFile) => bytesToURL(f.content, "image/jpeg");
const fileKey = (f: MsgFile, idx?: number) =>
  `${f.path || ""}#${f.hash || ""}#${f.mime || ""}#${fileSize(f)}#${idx ?? -1}`;
// A search's first page is small so the grid paints quickly over a slow
// upload; the pages after it get the device's own size (see PhotoGallery).
const cFirstPagePhotos = 12;

type Props = {
  onCancel: () => void;
  onPosted: () => void;
};

// Issue #87: matches the iOS composer's own Phone/Synced source toggle -
// "server" is the existing tag-filtered library grid below, "local" lets
// the browser upload a file straight from this computer instead (iOS's
// own local-picker equivalent, since a browser has no persistent device
// photo library to browse the way iOS does).
type Source = "server" | "local";

export default function NewPostPicker({ onCancel, onPosted }: Props) {
  const t = useT();
  const [source, setSource] = useState<Source>("server");

  // -------- local files (issue #87) --------------------------------------
  const [localFiles, setLocalFiles] = useState<File[]>([]);
  const localUrlsRef = useRef<Map<File, string>>(new Map());
  const localUrlFor = (f: File) => {
    let url = localUrlsRef.current.get(f);
    if (!url) {
      url = URL.createObjectURL(f);
      localUrlsRef.current.set(f, url);
    }
    return url;
  };
  useEffect(() => () => { localUrlsRef.current.forEach(u => URL.revokeObjectURL(u)); }, []);

  const addLocalFiles = (files: FileList | File[]) => {
    const picked = Array.from(files).filter(f => f.type.startsWith("image/") || f.type.startsWith("video/"));
    setLocalFiles(prev => [...prev, ...picked]);
  };
  const removeLocalFile = (f: File) => {
    const url = localUrlsRef.current.get(f);
    if (url) { URL.revokeObjectURL(url); localUrlsRef.current.delete(f); }
    setLocalFiles(prev => prev.filter(x => x !== f));
  };

  // How each file from this computer is getting on (F20 of the 2026-10
  // review): its bytes the device has taken, and the path it landed at
  // once it has. A file that reached the device is never sent again - not
  // when some others failed and the person tries again, nor after a
  // publish the device turned down.
  type Sent = { status: "sending" | "done" | "failed"; sent: number; path?: string };
  const [sentFiles, setSentFiles] = useState<Map<File, Sent>>(new Map());
  const sentRef = useRef(sentFiles);
  const noteSent = (f: File, next: Sent) => {
    const m = new Map(sentRef.current);
    m.set(f, next);
    sentRef.current = m;
    setSentFiles(m);
  };
  // Some files didn't upload: nothing is posted until the person says
  // whether to post the rest or try those again.
  const [partial, setPartial] = useState(false);
  // While publishing: which file of how many is being sent.
  const [uploading, setUploading] = useState<{ current: number; total: number } | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  // Issue #150: the phone's camera, straight into the post. `capture`
  // opens the camera on phones and tablets; desktop browsers ignore it
  // (a plain file chooser), so the button is only offered on touch
  // screens.
  const cameraInputRef = useRef<HTMLInputElement | null>(null);
  const hasCamera = typeof window !== "undefined" && window.matchMedia?.("(pointer: coarse)").matches;
  const [dragOver, setDragOver] = useState(false);

  // -------- tag filter --------------------------------------------------
  const [allTags, setAllTags] = useState<string[]>([]);
  const [chips, setChips] = useState<string[]>([]);
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
  };
  const removeChip = (t: string) => setChips(prev => prev.filter(x => x !== t));

  // -------- data & paging ------------------------------------------------
  const [items, setItems] = useState<MsgFile[]>([]);
  const mapRef = useRef<Map<string, MsgFile>>(new Map());
  // The grid's own thumbnail URLs, one per loaded item and freed when a new
  // tag filter clears the grid. Separate from stripUrlFor below, which
  // keeps the picks' thumbnails across filter changes.
  const gridThumb = useObjectURLs(items, tileThumb);
  const [token, setToken] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [endReached, setEndReached] = useState(false);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  // The grid is its own scroll container here (unlike the Images tab,
  // where the page itself scrolls), so it - not the viewport - is what the
  // sentinel has to be measured against.
  const gridRef = useRef<HTMLDivElement | null>(null);
  // A failed page is asked for again after a pause, not at once forever.
  const { tick: retryTick, failed: pageFailed, reset: resetRetry, ready: retryReady } = usePageRetry();

  // Bumped when the grid starts over (a tag added or removed), so a page
  // still in flight for the old search lands nowhere, as in PhotoGallery
  // and the apps' composers.
  const searchGenRef = useRef(0);

  const fetchPage = useCallback(
    async (overrideToken?: string | null, force = false) => {
      // force: a new search starts even while the old one's page is in
      // flight (its loading flag would otherwise refuse it, and the grid
      // stayed empty for the new tags).
      if (!force && (loading || endReached)) return;
      const myGen = searchGenRef.current;
      const sendToken = overrideToken ?? token ?? "";
      setLoading(true);
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = {
            $case: "reqSearchPhotos",
            // includeVideos (issue #60): the composer offers videos
            // alongside photos, unlike the Photo Gallery's own search.
            // No token starts a search: a small first page.
            reqSearchPhotos: {
              tags: chips,
              token: sendToken,
              includeVideos: true,
              limit: sendToken ? 0 : cFirstPagePhotos,
              // The photos the grid holds: a page asked again (its answer
              // was lost) comes back as the same page instead of the one
              // after it (SearchPhotos.have).
              have: sendToken ? mapRef.current.size : 0,
              // The grid's and the order strip's tiles: the small
              // thumbnails (release 111). The post itself is made on the
              // device from the files.
              smallThumbnails: true,
            },
          };
        });
        if (myGen !== searchGenRef.current) return;
        if (resp.payload?.$case !== "respListOfFiles") { pageFailed(); return; }
        const lof = resp.payload.respListOfFiles!;
        const nextToken = lof.token || null;

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
        setEndReached(!nextToken);
        resetRetry();
      } catch (err) {
        // No connection, or the request failed outright: retried after a
        // pause (usePageRetry).
        console.warn("Composer library page failed:", err);
        if (myGen === searchGenRef.current) pageFailed();
      } finally {
        if (myGen === searchGenRef.current) setLoading(false);
      }
    },
    [chips, token, loading, endReached, pageFailed, resetRetry]
  );

  const loadTags = useCallback(async () => {
    const resp = await useWS.request(e => {
      (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
    });
    if (resp.payload?.$case === "respTagsList") {
      setAllTags((resp.payload.respTagsList as TagsList).tags ?? []);
    }
  }, []);

  // Only the tag list: the chips effect below also runs on mount and loads
  // the first page. Fetching it here too sent a second first page right
  // behind it, doubling what a slow upload carried before anything showed.
  useEffect(() => {
    void loadTags();
  }, [loadTags]);

  useEffect(() => {
    resetRetry();
    searchGenRef.current += 1;
    (async () => {
      setItems([]);
      mapRef.current = new Map();
      setToken(null);
      setEndReached(false);
      await fetchPage("", true);
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chips]);

  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      (entries) => {
        // After a failed page, not before its retry is due (retryTick
        // re-creates this observer when it is).
        if (entries[0]?.isIntersecting && !loading && !endReached && retryReady()) fetchPage();
      },
      // Bottom margin: the point is to start the next page while the
      // sentinel is still below the fold, so scrolling doesn't stall on
      // an empty grid.
      { root: gridRef.current, rootMargin: "0px 0px 600px 0px" }
    );
    obs.observe(node);
    return () => obs.disconnect();
  }, [fetchPage, loading, endReached, retryTick, retryReady]);

  // -------- selection (tap a tile, no separate checkbox/viewer) ---------
  // Issue #98: an ordered list, not a Set - the post's own order is the
  // order of the paths sent to reqNewSocialPublication (the device writes
  // them to social_publications_files.pos and reads them back `order by
  // pos`), so this is what actually decides how a post reads. A Set kept
  // insertion order too, but there was no way to *see* that order or fix
  // it up short of deselecting everything and starting again. Mirrors what
  // the iOS composer has had since issue #48 (selectedOrder/moveSelected).
  const [selected, setSelected] = useState<string[]>([]);
  // Keeps the MsgFile for everything currently selected, so the order
  // strip can still render a thumbnail for a pick whose tile has since
  // left `items` - changing the tag filter resets the grid but
  // deliberately keeps the selection.
  const selectedFilesRef = useRef<Map<string, MsgFile>>(new Map());

  const toggleSel = (f: MsgFile) => {
    const p = f.path;
    setSelected(prev => {
      if (prev.includes(p)) return prev.filter(x => x !== p);
      selectedFilesRef.current.set(p, f);
      return [...prev, p];
    });
  };

  // Issue #98: explicit reordering, so fixing one photo's position doesn't
  // mean redoing the whole selection.
  const moveSelected = (p: string, offset: number) => setSelected(prev => {
    const idx = prev.indexOf(p);
    const next = idx + offset;
    if (idx < 0 || next < 0 || next >= prev.length) return prev;
    const copy = [...prev];
    [copy[idx], copy[next]] = [copy[next], copy[idx]];
    return copy;
  });

  // Drag & drop reordering for the strip - the natural gesture with a
  // mouse, alongside the move buttons (which are what works on a touch
  // screen and with a keyboard).
  //
  // The index being dragged lives in a ref, not just state: the drop
  // handler needs the value the *dragstart* set, and reading that from
  // state means depending on a re-render having happened in between. A
  // real drag takes long enough that it always has, but that's a timing
  // assumption rather than a guarantee - a drop handled in the same tick
  // as its dragstart reads a stale null and silently does nothing. The
  // state copy alongside it exists only to drive the dragging style.
  const dragIdxRef = useRef<number | null>(null);
  const [dragIdx, setDragIdx] = useState<number | null>(null);
  const startDrag = (i: number) => { dragIdxRef.current = i; setDragIdx(i); };
  const endDrag = () => { dragIdxRef.current = null; setDragIdx(null); };
  const reorder = <T,>(list: T[], from: number, to: number): T[] => {
    if (from === to || from < 0 || from >= list.length || to < 0 || to >= list.length) return list;
    const copy = [...list];
    const [moved] = copy.splice(from, 1);
    copy.splice(to, 0, moved);
    return copy;
  };
  const dropOnIdx = (to: number) => {
    const from = dragIdxRef.current;
    if (from !== null) setSelected(prev => reorder(prev, from, to));
    endDrag();
  };

  const moveLocalFile = (from: number, offset: number) => setLocalFiles(prev => {
    const to = from + offset;
    if (to < 0 || to >= prev.length) return prev;
    const copy = [...prev];
    [copy[from], copy[to]] = [copy[to], copy[from]];
    return copy;
  });
  const dropLocalOnIdx = (to: number) => {
    const from = dragIdxRef.current;
    if (from !== null) setLocalFiles(prev => reorder(prev, from, to));
    endDrag();
  };

  // Thumbnails for the strip, cached per path: bytesToURL mints a fresh
  // object URL on every call, so building them inline during render would
  // leak one per render for as long as the composer is open.
  const stripUrlsRef = useRef<Map<string, string>>(new Map());
  const stripUrlFor = (f: MsgFile) => {
    let url = stripUrlsRef.current.get(f.path);
    if (!url) {
      url = bytesToURL(f.content, "image/jpeg");
      stripUrlsRef.current.set(f.path, url);
    }
    return url;
  };
  useEffect(() => () => {
    stripUrlsRef.current.forEach(u => { if (u) URL.revokeObjectURL(u); });
  }, []);

  // -------- trimming (issue #108) ---------------------------------------
  // Keyed by server path for library picks and by the File object itself
  // for local ones, because a local file has no path until it's uploaded
  // (which only happens at publish time) - both are resolved back to real
  // paths in publish() below.
  const [serverTrims, setServerTrims] = useState<Record<string, TrimRange>>({});
  const [localTrims, setLocalTrims] = useState<Map<File, TrimRange>>(new Map());
  // What the trimmer is currently open on: a source URL to preview plus
  // where to write the result back to.
  const [trimming, setTrimming] = useState<
    { kind: "server"; path: string; url: string } | { kind: "local"; file: File; url: string } | null
  >(null);
  const [trimLoading, setTrimLoading] = useState<string | null>(null);
  // Object URLs for library videos pulled down to be trimmed - cached so
  // reopening the trimmer on the same clip doesn't refetch it, revoked
  // together when the composer closes.
  const trimUrlsRef = useRef<Map<string, string>>(new Map());
  useEffect(() => () => {
    trimUrlsRef.current.forEach(u => URL.revokeObjectURL(u));
  }, []);

  // Trimming needs the real video, not the thumbnail the strip shows, so
  // a library pick has to be fetched in full first (same reqGetFile the
  // gallery's own viewer uses). That can be a few seconds on a large clip
  // - hence the per-item spinner rather than a button that looks inert.
  const openServerTrimmer = async (path: string) => {
    const cached = trimUrlsRef.current.get(path);
    if (cached) { setTrimming({ kind: "server", path, url: cached }); return; }
    setTrimLoading(path);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetFile", reqGetFile: { path } };
      });
      if (resp.payload?.$case === "respFile" && resp.payload.respFile) {
        const full = resp.payload.respFile;
        const url = bytesToURL(full.content, full.mime || "video/mp4");
        trimUrlsRef.current.set(path, url);
        setTrimming({ kind: "server", path, url });
      } else {
        const message = resp.errorMessage;
        setError(() => (message ? asIs(message) : say("web.social.composer.trim_load_failed")));
      }
    } catch (err: any) {
      setError(() => errorText(err));
    } finally {
      setTrimLoading(null);
    }
  };

  const applyTrim = (range: TrimRange | null) => {
    if (!trimming) return;
    if (trimming.kind === "server") {
      setServerTrims(prev => {
        const next = { ...prev };
        if (range) next[trimming.path] = range; else delete next[trimming.path];
        return next;
      });
    } else {
      setLocalTrims(prev => {
        const next = new Map(prev);
        if (range) next.set(trimming.file, range); else next.delete(trimming.file);
        return next;
      });
    }
    setTrimming(null);
  };

  // The strip's own label for a trimmed clip - "Trim" until there is one,
  // then the length that survives, so the strip shows the decision without
  // having to reopen the editor.
  const trimBadge = (range: TrimRange | undefined) =>
    range ? `✂ ${formatTimecode(range.end - range.start)}` : t("web.social.composer.trim");

  // -------- publish --------------------------------------------------------
  const [caption, setCaption] = useState("");
  const [publishing, setPublishing] = useState(false);
  // Issue #87 follow-up: this composer is opened via browser automation for
  // testing (and could just as easily be a real user), and a blocking
  // window.alert() here freezes the tab until someone manually dismisses
  // it - reproduced live as an unrecoverable "Publishing..." composer.
  // An inline banner reports the same errors without blocking anything.
  const [error, setError] = useState<UiText | null>(null);
  // A thrown error's own words (an empty one shows nothing, as before).
  const errorText = (err: unknown): UiText | null => {
    const m = (err as { message?: unknown } | null | undefined)?.message;
    const message = m != null ? String(m) : String(err);
    return message ? asIs(message) : null;
  };

  // Issue #87: uploads one local File straight to this device (the same
  // uploadFile the Files section's own drag & drop uses), returning
  // the server path it lands at so it can be included in the publish
  // call below - a post always references paths already on the server,
  // whether they got there just now or were already synced in.
  const uploadLocalFile = async (f: File, onProgress: (sent: number) => void): Promise<string | null> => {
    const path = `/web/${Date.now()}-${Math.random().toString(36).slice(2, 8)}-${f.name}`;
    const resp: RespEnvelope = await uploadFile(path, f, false, onProgress);
    // An upload answers with the stored RespFile (its path/hash/etc.),
    // not the generic Ack every mutation-with-nothing-to-say-back uses -
    // checking for respAck here always missed, silently treating every
    // successful upload as a failure (see FilesExplorer.tsx's own drag &
    // drop, which reuses this same RPC and never asserted the response
    // shape at all - reproduced live via issue #87's own testing).
    if (resp.payload?.$case === "respFile" && resp.payload.respFile) {
      return resp.payload.respFile.path || path;
    }
    return null;
  };

  // postAnyway: post the files that uploaded, leaving out the ones that
  // didn't (the person's answer to `partial`).
  const publish = async (postAnyway = false) => {
    const hasSelection = source === "local" ? localFiles.length > 0 : selected.length > 0;
    // Issue #96: a post always needs its own caption - the button below
    // already disables on this same check, this just guards the actual
    // publish call too rather than trusting the button's disabled state
    // alone (same defense-in-depth as the hasSelection check right above).
    if (!hasSelection || !caption.trim() || publishing) return;
    setPublishing(true);
    setError(null);
    setPartial(false);
    try {
      let paths: string[];
      // Index-aligned with localFiles (null where an upload failed) - kept
      // around past the filter below so a local file's trim can still find
      // the path its own upload landed at (see the trims block further
      // down).
      let uploadedPaths: (string | null)[] = [];
      if (source === "local") {
        // Issue #98: in the strip's own order, so the post reads the way
        // the composer showed it. One at a time: all at once read and
        // queued every selected file together, so a few phone videos
        // held gigabytes in the tab; the bytes share one socket anyway.
        // What already reached the device (an earlier try) isn't sent
        // again; with postAnyway, nothing more is sent.
        uploadedPaths = [];
        const toSend = localFiles.filter((f) => !sentRef.current.get(f)?.path);
        let n = 0;
        for (const f of localFiles) {
          const had = sentRef.current.get(f)?.path;
          if (had || postAnyway) { uploadedPaths.push(had ?? null); continue; }
          setUploading({ current: ++n, total: toSend.length });
          noteSent(f, { status: "sending", sent: 0 });
          const path = await uploadLocalFile(f, (sent) => noteSent(f, { status: "sending", sent })).catch(() => null);
          noteSent(f, path ? { status: "done", sent: f.size, path } : { status: "failed", sent: 0 });
          uploadedPaths.push(path);
        }
        setUploading(null);
        paths = uploadedPaths.filter((p): p is string => p !== null);
        if (paths.length === 0) {
          setError(() => say("web.social.composer.upload_none"));
          return;
        }
        // Some didn't make it: stop and ask (it used to warn, publish
        // the rest and close at once, the warning unseen).
        if (paths.length < localFiles.length && !postAnyway) {
          const failed = localFiles.length - paths.length;
          setError(() => (tt: TFunction) => tt("web.social.composer.upload_partial", { count: failed }));
          setPartial(true);
          return;
        }
      } else {
        paths = selected;
      }

      // Issue #108: the trims are keyed by whatever identified the file in
      // the composer; the device only knows paths, so resolve them here.
      // For local files that means pairing each upload back up with the
      // File it came from - uploadedPaths is index-aligned with localFiles
      // (uploaded in order) before the nulls are filtered out.
      let trims: { path: string; startSecs: number; endSecs: number }[];
      if (source === "local") {
        trims = localFiles.flatMap((f, i) => {
          const range = localTrims.get(f);
          const path = uploadedPaths[i];
          if (!range || !path) return [];
          return [{ path, startSecs: range.start, endSecs: range.end }];
        });
      } else {
        trims = Object.entries(serverTrims)
          .filter(([path]) => paths.includes(path))
          .map(([path, range]) => ({ path, startSecs: range.start, endSecs: range.end }));
      }

      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = {
          $case: "reqNewSocialPublication",
          reqNewSocialPublication: { text: caption, paths, trims },
        };
      });
      if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
        onPosted();
      } else {
        const message = resp.errorMessage;
        setError(() => (message ? asIs(message) : say("web.social.composer.publish_failed")));
      }
    } catch (err: any) {
      setError(() => errorText(err));
    } finally {
      setUploading(null);
      setPublishing(false);
    }
  };

  // How many photos and videos the post has so far.
  const picked = source === "local" ? localFiles.length : selected.length;

  return (
    <div className="np-root">
      <div className="np-header">
        <button className="np-close" onClick={onCancel} aria-label={t("common.cancel")}>✕</button>
        <div className="np-title">{t("web.social.composer.title")}</div>
        <div className="np-header-spacer" />
      </div>

      {/* Issue #87: matches the iOS composer's Phone/Synced toggle. */}
      <div className="np-source-tabs">
        <button
          className={`np-source-tab${source === "server" ? " active" : ""}`}
          onClick={() => setSource("server")}
        >
          {t("web.social.composer.library")}
        </button>
        <button
          className={`np-source-tab${source === "local" ? " active" : ""}`}
          onClick={() => setSource("local")}
        >
          {t("web.social.composer.this_computer")}
        </button>
      </div>

      {source === "server" ? (
        <>
          <div className="np-search">
            <div className="np-chipbar">
              {chips.map((c) => (
                <span key={`chip-${c}`} className="np-chip">
                  {c}
                  <button className="np-chip-x" onClick={() => removeChip(c)} aria-label={t("web.social.composer.remove_tag", { tag: c })}>×</button>
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
                placeholder={t("web.social.composer.tag_filter")}
              />
            </div>
            {showSuggest && suggestions.length > 0 && (
              <div className="np-suggest">
                {suggestions.map(s => (
                  <div key={`sug-${s}`} className="np-suggest-item" onMouseDown={() => addChip(s)}>{s}</div>
                ))}
              </div>
            )}
          </div>

          <div className="np-grid" ref={gridRef}>
            {items.map((f, i) => {
              const key = fileKey(f, i);
              // The tile's content is always a server-generated JPEG
              // thumbnail (see files_manager.GetThumbnail), for a video
              // file same as a photo — never pass the file's own mime
              // here, or a video tile's Blob gets tagged "video/mp4" over
              // genuinely-JPEG bytes and the browser refuses to render it
              // as an <img>.
              const thumb = gridThumb(f);
              const isVideo = (f.mime || "").startsWith("video/");
              // Issue #98: the badge is the tile's position in the post,
              // not a plain checkmark - so the order is visible from the
              // grid itself, same as the iOS composer.
              const position = selected.indexOf(f.path);
              const checked = position >= 0;
              return (
                <button
                  key={key}
                  className={`np-cell${checked ? " selected" : ""}`}
                  onClick={() => toggleSel(f)}
                  title={f.path}
                >
                  <img src={thumb} alt={f.path} loading="lazy" />
                  {isVideo && <span className="np-video-badge">▶</span>}
                  {checked && <span className="np-check">{position + 1}</span>}
                </button>
              );
            })}
            <div ref={sentinelRef} style={{ height: 1 }} />
          </div>
        </>
      ) : (
        <>
          <input
            ref={fileInputRef}
            type="file"
            accept="image/*,video/*"
            multiple
            style={{ display: "none" }}
            onChange={(e) => { if (e.target.files) addLocalFiles(e.target.files); e.target.value = ""; }}
          />
          <input
            ref={cameraInputRef}
            type="file"
            accept="image/*,video/*"
            capture="environment"
            style={{ display: "none" }}
            onChange={(e) => { if (e.target.files) addLocalFiles(e.target.files); e.target.value = ""; }}
          />
          {hasCamera && (
            <button type="button" className="np-camera" onClick={() => cameraInputRef.current?.click()}>
              📷 {t("web.social.composer.camera")}
            </button>
          )}
          <div
            className={`np-dropzone${dragOver ? " drag-over" : ""}`}
            onClick={() => fileInputRef.current?.click()}
            onDragOver={(e) => { e.preventDefault(); setDragOver(true); }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragOver(false);
              if (e.dataTransfer.files.length) addLocalFiles(e.dataTransfer.files);
            }}
          >
            {t("web.social.composer.drop")}
          </div>
          <div className="np-grid">
            {localFiles.map((f, i) => {
              const isVideo = f.type.startsWith("video/");
              return (
                <div key={`${f.name}-${f.lastModified}-${i}`} className="np-cell selected">
                  {isVideo ? (
                    <video src={localUrlFor(f)} muted />
                  ) : (
                    <img src={localUrlFor(f)} alt={f.name} />
                  )}
                  {isVideo && <span className="np-video-badge">▶</span>}
                  <span className="np-check">{i + 1}</span>
                  <button
                    className="np-remove"
                    onClick={() => removeLocalFile(f)}
                    aria-label={t("web.social.composer.remove_file", { name: f.name })}
                  >
                    ✕
                  </button>
                </div>
              );
            })}
          </div>
        </>
      )}

      {/* Issue #98: the grid's own layout (newest first, or whatever the
          tag filter turned up) isn't necessarily the order the photos
          should read in - this strip shows the actual post order and lets
          it be rearranged directly, rather than requiring a
          deselect-and-start-over. Same affordance the iOS composer has had
          since issue #48, plus drag & drop for a mouse. */}
      {(source === "server" ? selected.length > 0 : localFiles.length > 0) && (
        <div className="np-strip">
          <div className="np-strip-title">{t("web.social.composer.order")}</div>
          <div className="np-strip-items">
            {source === "server"
              ? selected.map((p, i) => {
                  const f = selectedFilesRef.current.get(p);
                  const isVideo = (f?.mime || "").startsWith("video/");
                  return (
                    <div
                      key={`sel-${p}`}
                      className={`np-strip-item${dragIdx === i ? " dragging" : ""}`}
                      draggable
                      onDragStart={() => startDrag(i)}
                      onDragOver={(e) => e.preventDefault()}
                      onDrop={() => dropOnIdx(i)}
                      onDragEnd={endDrag}
                      title={p}
                    >
                      {/* Issue #111: the whole tile opens the trimmer, not
                          just a pill in its corner - there is nothing else
                          to tap a selected video for, and the trimmer is
                          also where you can watch it. The badge is now
                          only an affordance and a place to show the
                          chosen length. */}
                      <div
                        className={`np-strip-thumb${isVideo ? " trimmable" : ""}`}
                        role={isVideo ? "button" : undefined}
                        tabIndex={isVideo ? 0 : undefined}
                        aria-label={isVideo ? t("web.social.composer.trim_item", { name: p }) : undefined}
                        onClick={() => { if (isVideo && trimLoading !== p) void openServerTrimmer(p); }}
                        onKeyDown={(e) => {
                          if (!isVideo || trimLoading === p) return;
                          if (e.key === "Enter" || e.key === " ") { e.preventDefault(); void openServerTrimmer(p); }
                        }}
                      >
                        {f && <img src={stripUrlFor(f)} alt={p} />}
                        <span className="np-strip-pos">{i + 1}</span>
                        {isVideo && <span className="np-strip-video">▶</span>}
                        {isVideo && (
                          <span className={`np-trim-btn${serverTrims[p] ? " trimmed" : ""}`} aria-hidden="true">
                            {trimLoading === p ? "…" : trimBadge(serverTrims[p])}
                          </span>
                        )}
                        <button
                          className="np-remove"
                          // Without this the click also reaches the tile
                          // above and opens the trimmer on a video that
                          // was just removed.
                          onClick={(e) => { e.stopPropagation(); if (f) toggleSel(f); }}
                          aria-label={t("web.social.composer.remove_from_post", { name: p })}
                        >
                          ✕
                        </button>
                      </div>
                      <div className="np-strip-moves">
                        <button
                          onClick={() => moveSelected(p, -1)}
                          disabled={i === 0}
                          aria-label={t("web.social.composer.move_earlier", { name: p })}
                        >
                          ‹
                        </button>
                        <button
                          onClick={() => moveSelected(p, 1)}
                          disabled={i === selected.length - 1}
                          aria-label={t("web.social.composer.move_later", { name: p })}
                        >
                          ›
                        </button>
                      </div>
                    </div>
                  );
                })
              : localFiles.map((f, i) => (
                  <div
                    key={`loc-${f.name}-${f.lastModified}-${i}`}
                    className={`np-strip-item${dragIdx === i ? " dragging" : ""}`}
                    draggable
                    onDragStart={() => startDrag(i)}
                    onDragOver={(e) => e.preventDefault()}
                    onDrop={() => dropLocalOnIdx(i)}
                    onDragEnd={endDrag}
                    title={f.name}
                  >
                    <div
                      className={`np-strip-thumb${f.type.startsWith("video/") ? " trimmable" : ""}`}
                      role={f.type.startsWith("video/") ? "button" : undefined}
                      tabIndex={f.type.startsWith("video/") ? 0 : undefined}
                      aria-label={f.type.startsWith("video/") ? t("web.social.composer.trim_item", { name: f.name }) : undefined}
                      onClick={() => {
                        if (f.type.startsWith("video/")) setTrimming({ kind: "local", file: f, url: localUrlFor(f) });
                      }}
                      onKeyDown={(e) => {
                        if (!f.type.startsWith("video/")) return;
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          setTrimming({ kind: "local", file: f, url: localUrlFor(f) });
                        }
                      }}
                    >
                      {f.type.startsWith("video/")
                        ? <video src={localUrlFor(f)} muted />
                        : <img src={localUrlFor(f)} alt={f.name} />}
                      <span className="np-strip-pos">{i + 1}</span>
                      <SentBadge t={t} sent={sentFiles.get(f)} size={f.size} />
                      {f.type.startsWith("video/") && <span className="np-strip-video">▶</span>}
                      {f.type.startsWith("video/") && (
                        <span className={`np-trim-btn${localTrims.get(f) ? " trimmed" : ""}`} aria-hidden="true">
                          {trimBadge(localTrims.get(f))}
                        </span>
                      )}
                      <button
                        className="np-remove"
                        onClick={(e) => { e.stopPropagation(); removeLocalFile(f); }}
                        aria-label={t("web.social.composer.remove_from_post", { name: f.name })}
                      >
                        ✕
                      </button>
                    </div>
                    <div className="np-strip-moves">
                      <button onClick={() => moveLocalFile(i, -1)} disabled={i === 0} aria-label={t("web.social.composer.move_earlier", { name: f.name })}>‹</button>
                      <button onClick={() => moveLocalFile(i, 1)} disabled={i === localFiles.length - 1} aria-label={t("web.social.composer.move_later", { name: f.name })}>›</button>
                    </div>
                  </div>
                ))}
          </div>
        </div>
      )}

      {trimming && (
        <VideoTrimmer
          src={trimming.url}
          value={
            trimming.kind === "server"
              ? serverTrims[trimming.path] ?? null
              : localTrims.get(trimming.file) ?? null
          }
          onApply={applyTrim}
          onCancel={() => setTrimming(null)}
        />
      )}

      {error && (
        <div className="np-error" role="alert">
          {error(t)}
          {/* Some files didn't upload: the person decides (F20). */}
          {partial && !publishing && (
            <div className="np-error-actions">
              <button type="button" className="np-error-btn" onClick={() => void publish(true)}>{t("web.social.composer.post_uploaded")}</button>
              <button type="button" className="np-error-btn" onClick={() => void publish()}>{t("common.try_again")}</button>
            </div>
          )}
        </div>
      )}

      <div className="np-composer">
        <input
          className="np-caption"
          value={caption}
          onChange={(e) => setCaption(e.target.value)}
          placeholder={t("web.social.composer.caption")}
        />
        <button
          className="np-publish"
          disabled={(source === "local" ? !localFiles.length : !selected.length) || !caption.trim() || publishing}
          onClick={() => void publish()}
        >
          {uploading
            ? t("web.social.composer.uploading", uploading)
            : publishing
            ? t("web.social.composer.publishing")
            : picked ? t("web.social.composer.publish_count", { count: picked }) : t("web.social.composer.publish")}
        </button>
      </div>
    </div>
  );
}

// How a file from this computer is getting on, over its thumbnail in the
// order strip: how much of it the device has taken, then a check mark, or
// an exclamation mark when it didn't upload.
function SentBadge({ t, sent, size }: {
  t: TFunction;
  sent?: { status: "sending" | "done" | "failed"; sent: number };
  size: number;
}) {
  if (!sent) return null;
  if (sent.status === "done") {
    return <span className="np-sent done" role="img" aria-label={t("web.social.composer.file_uploaded")}>✓</span>;
  }
  if (sent.status === "failed") {
    const label = t("web.social.composer.file_failed");
    return <span className="np-sent failed" role="img" aria-label={label} data-tip={label}>!</span>;
  }
  const share = size > 0 ? Math.min(100, Math.round((100 * sent.sent) / size)) : 0;
  return (
    <span className="np-sent sending">
      <span className="np-sent-bar"><span style={{ width: `${share}%` }} /></span>
      <span className="np-sent-pct">{percentText(t, share)}</span>
    </span>
  );
}
