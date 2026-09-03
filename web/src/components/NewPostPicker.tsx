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

export default function NewPostPicker({ onCancel, onPosted }: Props) {
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
            reqSearchPhotos: { tags: chips, token: overrideToken ?? token ?? "" },
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
  const [sel, setSel] = useState<Set<string>>(new Set());
  const toggleSel = (p: string) => setSel(prev => {
    const n = new Set(prev);
    n.has(p) ? n.delete(p) : n.add(p);
    return n;
  });

  // -------- publish --------------------------------------------------------
  const [caption, setCaption] = useState("");
  const [publishing, setPublishing] = useState(false);

  const publish = async () => {
    if (!sel.size || publishing) return;
    setPublishing(true);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = {
          $case: "reqNewSocialPublication",
          reqNewSocialPublication: { text: caption, paths: Array.from(sel) },
        };
      });
      if (resp.payload?.$case === "respNewSocial" && resp.payload.respNewSocial.uuid) {
        onPosted();
      } else {
        alert("Could not publish the post");
      }
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
          const thumb = bytesToURL(f.content, f.mime || "image/jpeg");
          const checked = sel.has(f.path);
          return (
            <button
              key={key}
              className={`np-cell${checked ? " selected" : ""}`}
              onClick={() => toggleSel(f.path)}
              title={f.path}
            >
              <img src={thumb} alt={f.path} loading="lazy" />
              {checked && <span className="np-check">✓</span>}
            </button>
          );
        })}
        <div ref={sentinelRef} style={{ height: 1 }} />
      </div>

      <div className="np-composer">
        <input
          className="np-caption"
          value={caption}
          onChange={(e) => setCaption(e.target.value)}
          placeholder="Write a caption…"
        />
        <button
          className="np-publish"
          disabled={!sel.size || publishing}
          onClick={publish}
        >
          {publishing ? "Publishing…" : `Publish${sel.size ? ` (${sel.size})` : ""}`}
        </button>
      </div>
    </div>
  );
}
