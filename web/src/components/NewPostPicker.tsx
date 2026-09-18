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
import type { RespEnvelope, File as MsgFile, TagsList } from "../proto/messages";
import "./NewPostPicker.css";

const bytesToURL = (content?: Uint8Array | number[] | null, mime = "image/jpeg") => {
  if (!content) return "";
  const u8 = content instanceof Uint8Array ? content : new Uint8Array(content);
  if (u8.byteLength === 0) return "";
  return URL.createObjectURL(new Blob([u8], { type: mime }));
};
const fileKey = (f: MsgFile, idx?: number) =>
  `${f.path || ""}#${f.hash || ""}#${f.mime || ""}#${f.size || 0}#${idx ?? -1}`;

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
  const fileInputRef = useRef<HTMLInputElement | null>(null);
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
  const [token, setToken] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [endReached, setEndReached] = useState(false);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  const fetchPage = useCallback(
    async (overrideToken?: string | null) => {
      if (loading || endReached) return;
      setLoading(true);
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = {
            $case: "reqSearchPhotos",
            // includeVideos (issue #60): the composer offers videos
            // alongside photos, unlike the Photo Gallery's own search.
            reqSearchPhotos: { tags: chips, token: overrideToken ?? token ?? "", includeVideos: true },
          };
        });
        if (resp.payload?.$case !== "respListOfFiles") return;
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
      } finally {
        setLoading(false);
      }
    },
    [chips, token, loading, endReached]
  );

  const loadTags = useCallback(async () => {
    const resp = await useWS.request(e => {
      (e as any).payload = { $case: "reqGetTags", reqGetTags: {} };
    });
    if (resp.payload?.$case === "respTagsList") {
      setAllTags((resp.payload.respTagsList as TagsList).tags ?? []);
    }
  }, []);

  useEffect(() => {
    (async () => {
      await loadTags();
      await fetchPage("");
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    (async () => {
      setItems([]);
      mapRef.current = new Map();
      setToken(null);
      setEndReached(false);
      await fetchPage("");
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chips]);

  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const obs = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting && !loading && !endReached) fetchPage();
      },
      { root: null, rootMargin: "600px 0px 0px 0px" }
    );
    obs.observe(node);
    return () => obs.disconnect();
  }, [fetchPage, loading, endReached]);

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

  // -------- publish --------------------------------------------------------
  const [caption, setCaption] = useState("");
  const [publishing, setPublishing] = useState(false);
  // Issue #87 follow-up: this composer is opened via browser automation for
  // testing (and could just as easily be a real user), and a blocking
  // window.alert() here freezes the tab until someone manually dismisses
  // it - reproduced live as an unrecoverable "Publishing..." composer.
  // An inline banner reports the same errors without blocking anything.
  const [error, setError] = useState<string | null>(null);

  // Issue #87: uploads one local File straight to this device (same
  // reqUploadFile the Files section's own drag & drop uses), returning
  // the server path it lands at so it can be included in the publish
  // call below - a post always references paths already on the server,
  // whether they got there just now or were already synced in.
  const uploadLocalFile = async (f: File): Promise<string | null> => {
    const ab = await f.arrayBuffer();
    const path = `/web/${Date.now()}-${Math.random().toString(36).slice(2, 8)}-${f.name}`;
    const resp: RespEnvelope = await useWS.request(e => {
      (e as any).payload = {
        $case: "reqUploadFile",
        reqUploadFile: { path, content: new Uint8Array(ab), forceOverride: false },
      };
    });
    // ReqUploadFile answers with the stored RespFile (its path/hash/etc.),
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

  const publish = async () => {
    const hasSelection = source === "local" ? localFiles.length > 0 : selected.length > 0;
    // Issue #96: a post always needs its own caption - the button below
    // already disables on this same check, this just guards the actual
    // publish call too rather than trusting the button's disabled state
    // alone (same defense-in-depth as the hasSelection check right above).
    if (!hasSelection || !caption.trim() || publishing) return;
    setPublishing(true);
    setError(null);
    try {
      let paths: string[];
      if (source === "local") {
        // Issue #98: Promise.all resolves in the order it was given, so
        // the uploads land in the strip's own order - the post reads the
        // way the composer showed it.
        const uploaded = await Promise.all(
          localFiles.map(f => uploadLocalFile(f).catch(() => null))
        );
        paths = uploaded.filter((p): p is string => p !== null);
        if (paths.length === 0) {
          setError("Could not upload any of the selected files.");
          return;
        }
        if (paths.length < localFiles.length) {
          setError(`${localFiles.length - paths.length} file(s) failed to upload - publishing the rest.`);
        }
      } else {
        paths = selected;
      }

      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = {
          $case: "reqNewSocialPublication",
          reqNewSocialPublication: { text: caption, paths },
        };
      });
      if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
        onPosted();
      } else {
        setError(resp.errorMessage || "Could not publish the post.");
      }
    } catch (err: any) {
      setError(err?.message ?? String(err));
    } finally {
      setPublishing(false);
    }
  };

  return (
    <div className="np-root">
      <div className="np-header">
        <button className="np-close" onClick={onCancel} aria-label="Cancel">✕</button>
        <div className="np-title">New post</div>
        <div className="np-header-spacer" />
      </div>

      {/* Issue #87: matches the iOS composer's Phone/Synced toggle. */}
      <div className="np-source-tabs">
        <button
          className={`np-source-tab${source === "server" ? " active" : ""}`}
          onClick={() => setSource("server")}
        >
          Library
        </button>
        <button
          className={`np-source-tab${source === "local" ? " active" : ""}`}
          onClick={() => setSource("local")}
        >
          This Computer
        </button>
      </div>

      {source === "server" ? (
        <>
          <div className="np-search">
            <div className="np-chipbar">
              {chips.map((c) => (
                <span key={`chip-${c}`} className="np-chip">
                  {c}
                  <button className="np-chip-x" onClick={() => removeChip(c)} aria-label={`Remove ${c}`}>×</button>
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
                placeholder="Filter by tag…"
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

          <div className="np-grid">
            {items.map((f, i) => {
              const key = fileKey(f, i);
              // The tile's content is always a server-generated JPEG
              // thumbnail (see files_manager.GetThumbnail), for a video
              // file same as a photo — never pass the file's own mime
              // here, or a video tile's Blob gets tagged "video/mp4" over
              // genuinely-JPEG bytes and the browser refuses to render it
              // as an <img>.
              const thumb = bytesToURL(f.content, "image/jpeg");
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
            Drag photos or videos here, or click to choose files from this computer.
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
                    aria-label={`Remove ${f.name}`}
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
          <div className="np-strip-title">Order in post — drag, or use ‹ ›</div>
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
                      <div className="np-strip-thumb">
                        {f && <img src={stripUrlFor(f)} alt={p} />}
                        <span className="np-strip-pos">{i + 1}</span>
                        {isVideo && <span className="np-strip-video">▶</span>}
                        <button
                          className="np-remove"
                          onClick={() => f && toggleSel(f)}
                          aria-label={`Remove ${p} from the post`}
                        >
                          ✕
                        </button>
                      </div>
                      <div className="np-strip-moves">
                        <button
                          onClick={() => moveSelected(p, -1)}
                          disabled={i === 0}
                          aria-label={`Move ${p} earlier`}
                        >
                          ‹
                        </button>
                        <button
                          onClick={() => moveSelected(p, 1)}
                          disabled={i === selected.length - 1}
                          aria-label={`Move ${p} later`}
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
                    <div className="np-strip-thumb">
                      {f.type.startsWith("video/")
                        ? <video src={localUrlFor(f)} muted />
                        : <img src={localUrlFor(f)} alt={f.name} />}
                      <span className="np-strip-pos">{i + 1}</span>
                      {f.type.startsWith("video/") && <span className="np-strip-video">▶</span>}
                      <button
                        className="np-remove"
                        onClick={() => removeLocalFile(f)}
                        aria-label={`Remove ${f.name} from the post`}
                      >
                        ✕
                      </button>
                    </div>
                    <div className="np-strip-moves">
                      <button onClick={() => moveLocalFile(i, -1)} disabled={i === 0} aria-label={`Move ${f.name} earlier`}>‹</button>
                      <button onClick={() => moveLocalFile(i, 1)} disabled={i === localFiles.length - 1} aria-label={`Move ${f.name} later`}>›</button>
                    </div>
                  </div>
                ))}
          </div>
        </div>
      )}

      {error && <div className="np-error">{error}</div>}

      <div className="np-composer">
        <input
          className="np-caption"
          value={caption}
          onChange={(e) => setCaption(e.target.value)}
          placeholder="Write a caption…"
        />
        <button
          className="np-publish"
          disabled={(source === "local" ? !localFiles.length : !selected.length) || !caption.trim() || publishing}
          onClick={publish}
        >
          {publishing
            ? "Publishing…"
            : `Publish${(source === "local" ? localFiles.length : selected.length) ? ` (${source === "local" ? localFiles.length : selected.length})` : ""}`}
        </button>
      </div>
    </div>
  );
}
