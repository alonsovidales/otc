// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The search in the top bar, as in Google Photos. The field holds what
// Images is narrowed to - an open collection, people, tags - as chips.
// While something is typed a panel under it offers, in this order, the
// things (tags) that match, the named people that match (while face
// recognition is on), and the files and folders whose path matches
// (SearchFiles, asked of the device as the typing pauses), with a row to
// search the documents for the word: all the files and folders it finds
// listed in Files (filesNav.searchInFiles). That row comes first, and is
// what Enter takes, when no tag or person matches; otherwise it follows
// the files. A device without SearchFiles has neither. Picking a tag or a
// person adds it to photoFilter's search, shared with the pages, and
// shows Images; picking a folder shows it in Files, and a file opens there
// as a click on it would. Enter takes a file or a folder only when the
// arrow keys or the mouse went to it. With nothing typed there is no panel.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { File as PbFile, Person, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { useWS } from "../net/useWS";
import { personLabel, reloadPeople, reloadTags, usePeople, useTags } from "./libraryStore";
import { addTag, clearSearch, leaveGroup, removeTag, togglePerson, usePhotoFilter } from "./photoFilter";
import { useFaceRecognition } from "./faceRecognition";
import {
  asFolder, deviceCantSearchFiles, fold, foundParts, matchIn, parentFolder, searchInFiles, showInFiles, useNoFileSearch,
  type Folded, type Span,
} from "./filesNav";
import { CollectionsIcon } from "./NavIcons";
import "./TopSearch.css";

type Props = {
  // A tag or a person was picked: show Images.
  onShowPhotos: () => void;
  // A folder or a file was picked (filesNav.showInFiles): show Files.
  onShowFiles: () => void;
};

type FileKind = "folder" | "photo" | "video" | "doc";

// What the arrow keys and Enter go through in the panel, in the order
// shown. "docs" is the typed word itself, searched for in every file's
// path: the row before the files when no tag or person matches, after
// them otherwise.
type Option =
  | { kind: "tag"; key: string; tag: string; span?: Span }
  | { kind: "person"; key: string; person: Person; span?: Span }
  | { kind: "docs"; key: string; text: string }
  | { kind: "file"; key: string; file: PbFile; type: FileKind; name: string; dir: string; nameSpan?: Span; dirSpan?: Span };

const MATCH_TAGS = 5;
const MATCH_PEOPLE = 5;
const MATCH_FILES = 8;
// The files are asked for once the typing pauses this long.
const FILES_DELAY_MS = 150;
// The highlight moved up past the first option: Enter then takes the
// typed text as it is.
const NONE = "";

// The entries matching the folded query: those starting with it first,
// then those with a word starting with it, then any containing it; in
// their own order within each.
function ranked<T extends { label: string; f: Folded }>(items: T[], q: string) {
  const hits: { item: T; rank: number; span: Span }[] = [];
  for (const item of items) {
    const m = matchIn(item.label, item.f, q);
    if (m) hits.push({ item, ...m });
  }
  return hits.sort((a, b) => a.rank - b.rank);
}

function fileKind(f: PbFile): FileKind {
  const mime = f.mime || "";
  if (mime === "inode/directory") return "folder";
  if (mime.startsWith("video/")) return "video";
  if (mime.startsWith("image/") || /\.(heic|heif)$/i.test(f.path)) return "photo";
  return "doc";
}
const KIND_WORD: Record<FileKind, string> = { folder: "Folder", photo: "Photo", video: "Video", doc: "File" };

const photosLabel = (n: number) => `${n.toLocaleString()} ${n === 1 ? "photo" : "photos"}`;

// New photos bring new tags and faces. The lists are fetched again when a
// search ends, if the last time was a while ago: current for the next
// one, and nothing moves under the pointer while picking.
const REFRESH_MS = 5 * 60_000;
let fetchedAt = Date.now();
function refreshIfStale(faces: boolean) {
  if (Date.now() - fetchedAt < REFRESH_MS) return;
  fetchedAt = Date.now();
  void reloadTags().catch(() => {});
  if (faces) void reloadPeople().catch(() => {});
}

const cls = (...names: (string | false)[]) => names.filter(Boolean).join(" ");

// Fingers, as in TopSearch.css: the keyboard is on the screen, and stays
// up over the photos for as long as the field has the focus.
const onScreenKeyboard = () => window.matchMedia("(pointer: coarse)").matches;

// The selection bars of Images and People, laid over the top bar.
const COVERS = ".pg-selbar, .pv-pickbar";

// Outline icons drawn like NavIcons': 24px box, stroke in currentColor.
function Icon({ size = 20, stroke = 1.6, children }: { size?: number; stroke?: number; children: React.ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}
const SearchIcon = () => <Icon><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></Icon>;
// A page with a magnifier over its corner: search the documents.
const DocSearchIcon = () => (
  <Icon>
    <path d="M10.5 20.5h-4a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2h7l4 4v3" />
    <path d="M13.5 3.5v4h4M8 11h4M8 14.5h2" />
    <circle cx="16" cy="16" r="3.2" />
    <path d="m18.4 18.4 2.6 2.6" />
  </Icon>
);
const BackIcon = () => <Icon><path d="M19 12H5m6-6-6 6 6 6" /></Icon>;
const CloseIcon = ({ size = 20 }: { size?: number }) => <Icon size={size} stroke={1.8}><path d="m6.5 6.5 11 11m0-11-11 11" /></Icon>;
const TagIcon = () => (
  <Icon><path d="M3.5 12V5.5a2 2 0 0 1 2-2H12a2 2 0 0 1 1.4.6l6.9 6.9a2 2 0 0 1 0 2.8l-6.6 6.6a2 2 0 0 1-2.8 0l-6.9-6.9A2 2 0 0 1 3.5 12Z" /><circle cx="8.5" cy="8.5" r="1.4" /></Icon>
);
const FaceIcon = ({ size = 20 }: { size?: number }) => (
  <Icon size={size}><circle cx="12" cy="9.5" r="3.5" /><path d="M5.5 19.5c1.1-3.2 3.6-4.8 6.5-4.8s5.4 1.6 6.5 4.8" /></Icon>
);
const CheckIcon = ({ size = 12 }: { size?: number }) => <Icon size={size} stroke={3}><path d="m5 12.5 4.5 4.5L19 7.5" /></Icon>;
const FILE_ICONS: Record<FileKind, React.ReactNode> = {
  folder: <Icon><path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-10Z" /></Icon>,
  photo: <Icon><rect x="3.5" y="4.5" width="17" height="15" rx="2.5" /><circle cx="9" cy="10" r="1.6" /><path d="m4 17 4.5-4.5 3.5 3.5 2.5-2.5L20 18" /></Icon>,
  video: <Icon><rect x="3.5" y="5.5" width="17" height="13" rx="2.5" /><path d="m10.5 9.5 4 2.5-4 2.5Z" /></Icon>,
  doc: <Icon><path d="M6.5 3.5h7l4 4v11a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2Z" /><path d="M13.5 3.5v4h4M8.5 12.5h7M8.5 16h5" /></Icon>,
};

// A label with the part that matched in bold.
function Marked({ text, span }: { text: string; span?: Span }) {
  if (!span) return <>{text}</>;
  return <>{text.slice(0, span[0])}<mark className="ts-mark">{text.slice(span[0], span[1])}</mark>{text.slice(span[1])}</>;
}

export default function TopSearch({ onShowPhotos, onShowFiles }: Props) {
  const filter = usePhotoFilter();
  const people = usePeople();
  const tags = useTags();
  const faces = useFaceRecognition() === true;

  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  // Where the arrow keys or the mouse took the highlight: an option's key,
  // or NONE. null: the best match for what is typed, or nothing.
  const [moved, setMoved] = useState<string | null>(null);
  // The ends of the chips row that hide chips: 1 the left, 2 the right.
  const [fade, setFade] = useState(0);
  // The files the device found for the last search it answered, with the
  // text it was asked for (as typed, and folded). None once the field is
  // emptied: a new search starts from nothing.
  const [found, setFound] = useState<{ typed: string; q: string; files: PbFile[] } | null>(null);

  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const chipsRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  // What pressed in the panel last: a mouse leaves the focus in the field.
  const pressedWith = useRef("mouse");
  const mouseAt = useRef({ x: NaN, y: NaN });
  // The focus handed back to the field after a chip's × was pressed with
  // the keyboard: no reason to open the panel.
  const quietFocus = useRef(false);
  // The window coming back gives the field that kept the focus a focus
  // event of its own: no reason to open the panel over the page either.
  const windowBack = useRef(false);
  // A selection bar lies over the top bar.
  const [covered, setCovered] = useState(false);
  // The last file search sent: an answer to an earlier one is dropped.
  const fileSeq = useRef(0);
  // The device doesn't know SearchFiles (older than this app), as the
  // panel or Files found out: not asked again, and no Files section nor a
  // row to search the documents.
  const noFileSearch = useNoFileSearch();

  const id = useId();
  const listId = `${id}list`;
  const optId = (i: number) => `${id}opt${i}`;

  const chipCount = (filter.group ? 1 : 0) + filter.personIds.length + filter.tags.length;
  const shownChips = useRef(chipCount);
  // Whether the chips row shows its end, the newest chips: set by its
  // scrolling, unknown until first measured.
  const chipsAtEnd = useRef<boolean | null>(null);

  const byId = useMemo(() => new Map(people.items.map((p) => [p.id, p])), [people.items]);
  const named = useMemo(
    () => people.items.filter((p) => p.name.trim()).map((p) => ({ person: p, label: p.name.trim(), f: fold(p.name.trim()) })),
    [people.items],
  );
  const tagIndex = useMemo(() => tags.map((t) => ({ label: t, f: fold(t) })), [tags]);

  const typed = query.trim();
  const q = fold(typed).text;
  // The panel shows only while something is typed.
  const shown = open && q !== "";

  // The files whose path holds what is typed, from the device. Emptying
  // the field forgets the last answer, and one still on its way.
  useEffect(() => {
    if (!typed) {
      fileSeq.current++;
      setFound(null);
      return;
    }
    if (noFileSearch) return;
    const t = setTimeout(() => {
      const seq = ++fileSeq.current;
      const answer = (files: PbFile[]) => setFound({ typed, q: fold(typed).text, files });
      useWS.request((e: Partial<ReqEnvelope>) => {
        e.payload = { $case: "reqSearchFiles", reqSearchFiles: { query: typed, limit: MATCH_FILES } };
      }).then((resp: RespEnvelope) => {
        if (resp.errorCode === "unknown_payload") deviceCantSearchFiles();
        if (seq !== fileSeq.current) return;
        if (resp.payload?.$case === "respListOfFiles") {
          answer(resp.payload.respListOfFiles.files ?? []);
          return;
        }
        answer([]);
      }, () => {
        if (seq === fileSeq.current) answer([]);
      });
    }, FILES_DELAY_MS);
    return () => clearTimeout(t);
  }, [typed, noFileSearch]);

  // The device's answer for the text as it is now, as the device gave it.
  // Until that comes, the answer for the text before stands in only while
  // the typing goes on from it (what is typed now holds it), kept to what
  // still matches - narrowing rather than emptying on every key. The
  // answer for some other text is never shown, nor taken.
  const fileHits = useMemo(() => {
    if (!q || !found) return [];
    const current = found.typed === typed;
    if (!current && !q.includes(found.q)) return [];
    const out: Extract<Option, { kind: "file" }>[] = [];
    for (const file of found.files) {
      if (!current && !fold(file.path).text.includes(q)) continue;
      out.push({ kind: "file", key: `f:${file.path}`, file, type: fileKind(file), ...foundParts(file.path, q) });
      if (out.length === MATCH_FILES) break;
    }
    return out;
  }, [found, q, typed]);

  const { options, best } = useMemo(() => {
    const out: Option[] = [];
    if (!q) return { options: out, best: null };
    // Tags already searched for are chips in the field, not suggestions.
    const inSearch = new Set(filter.tags.map((t) => fold(t).text));
    const free = tagIndex.filter((t) => !inSearch.has(t.f.text));
    // What Enter takes: an exact match, or else the first.
    let exact: string | null = null;
    for (const h of ranked(free, q).slice(0, MATCH_TAGS)) {
      const key = `t:${h.item.label}`;
      out.push({ kind: "tag", key, tag: h.item.label, span: h.span });
      if (!exact && h.item.f.text === q) exact = key;
    }
    // The people with the most photos among those whose name matches.
    if (faces) {
      const hits = ranked(named, q)
        .sort((a, b) => b.item.person.faceCount - a.item.person.faceCount || a.rank - b.rank)
        .slice(0, MATCH_PEOPLE);
      for (const h of hits) {
        const key = `p:${h.item.person.id}`;
        out.push({ kind: "person", key, person: h.item.person, span: h.span });
        if (!exact && h.item.f.text === q) exact = key;
      }
    }
    // The documents searched for the word as typed: first, and what Enter
    // takes, when nothing above matches it; below the files otherwise.
    // Files and folders are never what Enter takes by itself - not after
    // Escape hid them, nor because their answer came in before the key:
    // only once the arrows or the mouse went there.
    const docs: Option[] = noFileSearch ? [] : [{ kind: "docs", key: "docs", text: typed }];
    if (!out.length) out.push(...docs, ...fileHits);
    else out.push(...fileHits, ...docs);
    return { options: out, best: exact ?? out.find((o) => o.kind !== "file")?.key ?? null };
  }, [q, typed, filter.tags, faces, named, tagIndex, fileHits, noFileSearch]);

  const activeKey = moved === null || (moved !== NONE && !options.some((o) => o.key === moved)) ? best : moved;
  const active = activeKey ? options.findIndex((o) => o.key === activeKey) : -1;

  const close = useCallback(() => {
    setOpen(false);
    setMoved(null);
    refreshIfStale(faces);
  }, [faces]);

  // A press anywhere else ends the search. A finger's does only that while
  // the panel shows: the click it becomes doesn't also open the photo
  // under it, as the scrim sees to on a phone. A swipe never becomes a
  // click and still scrolls, and the top bar's own buttons answer at once.
  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      const at = e.target as Element;
      if (rootRef.current?.contains(at)) return;
      close();
      if (!shown || e.pointerType === "mouse" || at.closest?.(".topbar")) return;
      const eat = (c: MouseEvent) => {
        c.preventDefault();
        c.stopPropagation();
        disarm();
      };
      const disarm = () => {
        document.removeEventListener("click", eat, true);
        document.removeEventListener("pointerdown", disarm, true);
        document.removeEventListener("pointercancel", disarm, true);
      };
      document.addEventListener("click", eat, true);
      document.addEventListener("pointerdown", disarm, true);
      document.addEventListener("pointercancel", disarm, true);
    };
    document.addEventListener("pointerdown", down, true);
    return () => document.removeEventListener("pointerdown", down, true);
  }, [open, shown, close]);

  // The window's focus event comes just before the field's, in the same
  // task: the flag lasts until the next one.
  useEffect(() => {
    const back = () => {
      if (document.activeElement !== inputRef.current) return;
      windowBack.current = true;
      setTimeout(() => { windowBack.current = false; });
    };
    window.addEventListener("focus", back);
    return () => window.removeEventListener("focus", back);
  }, []);

  // While a selection bar covers the top bar, the search under it is
  // closed and out of the keyboard's reach too: nothing changes behind a
  // selection.
  useEffect(() => {
    let was = false;
    const check = () => {
      const now = document.querySelector(COVERS) !== null;
      if (now === was) return;
      was = now;
      setCovered(now);
      if (!now) return;
      close();
      const el = document.activeElement;
      if (el instanceof HTMLElement && rootRef.current?.contains(el)) el.blur();
    };
    const mo = new MutationObserver(check);
    mo.observe(document.body, { childList: true, subtree: true });
    check();
    return () => mo.disconnect();
  }, [close]);

  // Which ends of the chips row hide chips, to fade them out.
  const updateFade = useCallback(() => {
    const el = chipsRef.current;
    if (!el) return;
    const right = el.scrollWidth - el.clientWidth - el.scrollLeft;
    setFade((el.scrollLeft > 1 ? 1 : 0) | (right > 1 ? 2 : 0));
  }, []);
  const onChipsScroll = () => {
    const el = chipsRef.current;
    if (el) chipsAtEnd.current = el.scrollWidth - el.clientWidth - el.scrollLeft <= 1;
    updateFade();
  };

  // A chip added goes at the end of the row: scrolled to.
  useLayoutEffect(() => {
    const el = chipsRef.current;
    if (el && chipCount > shownChips.current) el.scrollLeft = el.scrollWidth;
    shownChips.current = chipCount;
  }, [chipCount]);
  // Chips come and go and their names load with any render.
  useLayoutEffect(() => updateFade());

  useEffect(() => {
    const el = chipsRef.current;
    if (!el) return;
    // The row narrows when the field takes the focus or the clear button
    // shows: the newest chips stay in sight if they were.
    const ro = new ResizeObserver(() => {
      if (chipsAtEnd.current === null) chipsAtEnd.current = el.scrollWidth - el.clientWidth - el.scrollLeft <= 1;
      else if (chipsAtEnd.current) el.scrollLeft = el.scrollWidth;
      updateFade();
    });
    ro.observe(el);
    // A mouse wheel over the chips scrolls them sideways, not the page.
    const wheel = (e: WheelEvent) => {
      if (el.scrollWidth <= el.clientWidth || Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
      e.preventDefault();
      el.scrollLeft += e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => {
      ro.disconnect();
      el.removeEventListener("wheel", wheel);
    };
  }, [updateFade]);

  // Another word typed: its matches from the top.
  useLayoutEffect(() => {
    if (panelRef.current) panelRef.current.scrollTop = 0;
  }, [q]);

  // Keeps the highlighted option in sight, scrolling the panel (only: the
  // page under it stays where it is).
  const reveal = (i: number) => {
    const panel = panelRef.current;
    const el = document.getElementById(optId(i));
    if (!panel || !el) return;
    if (i === 0) { panel.scrollTop = 0; return; }
    const p = panel.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    if (r.top < p.top + 8) panel.scrollTop -= p.top + 8 - r.top;
    else if (r.bottom > p.bottom - 8) panel.scrollTop += r.bottom - p.bottom + 8;
  };

  const moveTo = (i: number) => {
    setMoved(options[i].key);
    reveal(i);
  };

  // Down and up go through every section in turn. Up past the first
  // option leaves the highlight off it, so Enter takes the text as typed.
  const step = (dir: 1 | -1) => {
    if (!options.length) return;
    if (active < 0) {
      moveTo(dir > 0 ? 0 : options.length - 1);
      return;
    }
    const next = active + dir;
    if (next < 0) setMoved(NONE);
    else if (next < options.length) moveTo(next);
  };

  // Done: the panel closes. After a click or a tap, or the Search key of a
  // keyboard on the screen, the field lets go of the focus too, which puts
  // that keyboard away from the page.
  const finish = (how: "key" | "pointer") => {
    close();
    if (how === "pointer" || onScreenKeyboard()) inputRef.current?.blur();
  };

  const pick = (o: Option, how: "key" | "pointer") => {
    setQuery("");
    finish(how);
    switch (o.kind) {
      case "person":
        togglePerson(o.person.id);
        onShowPhotos();
        return;
      case "tag":
        addTag(o.tag);
        onShowPhotos();
        return;
      case "docs":
        // Every file and folder found, listed in Files - still inside the
        // click or the key, like a file opened from the panel.
        searchInFiles(o.text);
        onShowFiles();
        return;
      case "file":
        // A folder opens; a file opens in its folder, as a click there.
        if (o.type === "folder") showInFiles(asFolder(o.file.path));
        else showInFiles(parentFolder(o.file.path), o.file);
        onShowFiles();
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        e.preventDefault();
        if (shown) step(e.key === "ArrowDown" ? 1 : -1);
        else setOpen(true);
        break;
      case "Enter":
        e.preventDefault();
        // Words typed are searched for whether or not Escape hid the panel
        // (which forgets where the arrows went: a file is never taken then).
        // Up past the first option, the documents are searched for them as
        // typed. A device that can't do that leaves the panel showing that
        // nothing matches.
        if (typed && active >= 0) pick(options[active], "key");
        else if (typed && !noFileSearch) pick({ kind: "docs", key: "docs", text: typed }, "key");
        else if (typed) setOpen(true);
        else {
          finish("key");
          if (chipCount) onShowPhotos();
        }
        break;
      case "Escape":
        // The panel goes; with none showing, the field lets go too.
        close();
        if (!shown) inputRef.current?.blur();
        break;
      case "Backspace":
        // The last chip goes: the last tag, then the last person. Not on
        // a held key, which would empty the whole search at once.
        if (query || e.repeat) return;
        if (filter.tags.length) removeTag(filter.tags[filter.tags.length - 1]);
        else if (filter.personIds.length) togglePerson(filter.personIds[filter.personIds.length - 1]);
        return;
      default:
        return;
    }
    // Handled here, not by the page's own keys.
    e.stopPropagation();
  };

  // A press anywhere on the field types into it.
  const onFieldMouseDown = (e: React.MouseEvent) => {
    if ((e.target as Element).closest("button")) return;
    if (e.target !== inputRef.current) {
      e.preventDefault();
      inputRef.current?.focus();
    }
    setOpen(true);
  };

  // The chips' × and the clear button leave the focus where it is; pressed
  // with the keyboard, they hand it to the field rather than lose it with
  // the button, without opening the panel over the page.
  const keepFocus = (e: React.MouseEvent) => e.preventDefault();
  const refocus = (e: React.MouseEvent) => {
    if (e.detail !== 0) return;
    quietFocus.current = true;
    inputRef.current?.focus();
    quietFocus.current = false;
  };

  // Tabbing on to something else ends the search. A tap in the panel
  // moves the focus to the panel, and the search goes on; a press outside
  // is the pointerdown above.
  const onBlur = (e: React.FocusEvent) => {
    const to = e.relatedTarget;
    if (to && !e.currentTarget.contains(to as Node)) close();
  };

  const dismiss = () => {
    close();
    inputRef.current?.blur();
  };

  const clearable = query !== "" || filter.tags.length > 0 || filter.personIds.length > 0;
  const onClear = (e: React.MouseEvent) => {
    setQuery("");
    setMoved(null);
    clearSearch();
    refocus(e);
  };

  const onPanelPointerDown = (e: React.PointerEvent) => {
    pressedWith.current = e.pointerType;
  };
  // A click in the panel leaves the focus in the field, to type on; a tap
  // lets it go, so the keyboard stops covering the suggestions.
  const onPanelMouseDown = (e: React.MouseEvent) => {
    if (pressedWith.current === "mouse") e.preventDefault();
  };
  const onPanelPointerMove = (e: React.PointerEvent) => {
    // Only a real move: the panel scrolling under a still mouse must not
    // take the highlight from the arrow keys.
    if (e.pointerType !== "mouse" || (e.clientX === mouseAt.current.x && e.clientY === mouseAt.current.y)) return;
    mouseAt.current = { x: e.clientX, y: e.clientY };
    const el = (e.target as Element).closest<HTMLElement>("[data-i]");
    const o = el ? options[Number(el.dataset.i)] : undefined;
    if (o && o.key !== activeKey) setMoved(o.key);
  };

  const optionProps = (o: Option, i: number) => ({
    id: optId(i),
    role: "option",
    "aria-selected": i === active,
    "data-i": i,
    onClick: () => pick(o, "pointer"),
  });

  const renderOption = (o: Option, i: number) => {
    const hl = i === active;
    switch (o.kind) {
      case "tag":
        return (
          <div key={o.key} {...optionProps(o, i)} className={cls("ts-row", hl && "is-active")}>
            <span className="ts-row-icon"><TagIcon /></span>
            <span className="ts-row-label"><Marked text={o.tag} span={o.span} /></span>
          </div>
        );
      case "person": {
        const on = filter.personIds.includes(o.person.id);
        const thumb = people.thumbs.get(o.person.id);
        return (
          <div key={o.key} {...optionProps(o, i)} aria-checked={on} className={cls("ts-row", "is-two", on && "is-on", hl && "is-active")}>
            <span className="ts-row-pic">{thumb ? <img src={thumb} alt="" draggable={false} /> : <FaceIcon size={18} />}</span>
            <span className="ts-row-text">
              <span className="ts-row-label"><Marked text={personLabel(o.person)} span={o.span} /></span>
              <span className="ts-row-sub">{photosLabel(o.person.faceCount)}</span>
            </span>
            {on && <span className="ts-row-tick"><CheckIcon size={18} /></span>}
          </div>
        );
      }
      case "docs":
        return (
          <div key={o.key} {...optionProps(o, i)} className={cls("ts-row", hl && "is-active")}>
            <span className="ts-row-icon"><DocSearchIcon /></span>
            <span className="ts-row-label">Search documents for “<mark className="ts-mark">{o.text}</mark>”</span>
          </div>
        );
      case "file":
        return (
          <div
            key={o.key}
            {...optionProps(o, i)}
            aria-label={`${o.name}, ${KIND_WORD[o.type].toLowerCase()} in ${o.dir}`}
            className={cls("ts-row", "is-two", hl && "is-active")}
          >
            <span className={`ts-row-icon ts-file is-${o.type}`}>{FILE_ICONS[o.type]}</span>
            <span className="ts-row-text">
              <span className="ts-row-label"><Marked text={o.name} span={o.nameSpan} /></span>
              {/* Long folders lose their start, not the end nearest the file. */}
              <span className="ts-row-sub ts-path"><bdi dir="ltr"><Marked text={o.dir} span={o.dirSpan} /></bdi></span>
            </span>
          </div>
        );
    }
  };

  const section = (name: string, title: string, body: React.ReactNode) => (
    <div role="group" aria-labelledby={`${id}${name}`} className="ts-section">
      <div role="presentation" id={`${id}${name}`} className="ts-head">{title}</div>
      {body}
    </div>
  );

  const numbered = options.map((o, i) => [o, i] as const);
  const tagOpts = numbered.filter(([o]) => o.kind === "tag");
  const personOpts = numbered.filter(([o]) => o.kind === "person");
  const fileOpts = numbered.filter(([o]) => o.kind === "file");
  const docsOpt = numbered.find(([o]) => o.kind === "docs");
  // The row to search the documents: first when no tag or person matches.
  const docsFirst = !tagOpts.length && !personOpts.length;
  const docsRow = docsOpt && <div role="presentation" className="ts-section">{renderOption(docsOpt[0], docsOpt[1])}</div>;

  // What matches, not counting the row to search the documents.
  const matches = options.length - (docsOpt ? 1 : 0);
  const status = !shown ? "" : !matches ? "No matches" : `${matches} ${matches === 1 ? "suggestion" : "suggestions"}`;
  const alreadyIn = filter.tags.some((t) => fold(t).text === q);
  // The chips, read out with the field.
  const summary = [
    filter.group && `the collection ${filter.group.name}`,
    ...filter.personIds.map((pid) => { const p = byId.get(pid); return p ? personLabel(p) : "a person"; }),
    ...filter.tags,
  ].filter(Boolean).join(", ");

  return (
    <div ref={rootRef} className={cls("ts-root", shown && "is-open")} onBlur={onBlur} inert={covered}>
      <div className="ts-field" onMouseDown={onFieldMouseDown}>
        <span className="ts-glass" aria-hidden="true"><SearchIcon /></span>
        {shown && (
          <button type="button" className="ts-back" aria-label="Close search" onMouseDown={keepFocus} onClick={dismiss}>
            <BackIcon />
          </button>
        )}
        <div ref={chipsRef} className={cls("ts-chips", (fade & 1) > 0 && "fade-l", (fade & 2) > 0 && "fade-r")} onScroll={onChipsScroll}>
          {filter.group && (
            <span className="ts-chip has-icon">
              <span className="ts-chip-icon"><CollectionsIcon size={18} /></span>
              <span className="ts-chip-label">{filter.group.name}</span>
              <button
                type="button"
                className="ts-chip-x"
                aria-label={`Close the collection ${filter.group.name}`}
                onMouseDown={keepFocus}
                onClick={(e) => { leaveGroup(); refocus(e); }}
              >
                <CloseIcon size={16} />
              </button>
            </span>
          )}
          {filter.personIds.map((pid) => {
            const p = byId.get(pid);
            const thumb = people.thumbs.get(pid);
            const name = p?.name.trim() ?? "";
            return (
              <span key={`p:${pid}`} className="ts-chip has-pic">
                <span className="ts-chip-pic">{thumb ? <img src={thumb} alt="" draggable={false} /> : <FaceIcon size={16} />}</span>
                <span className={cls("ts-chip-label", !name && "is-unnamed")}>{p ? personLabel(p) : "Person"}</span>
                <button
                  type="button"
                  className="ts-chip-x"
                  aria-label={name ? `Remove ${name}` : "Remove this person"}
                  onMouseDown={keepFocus}
                  onClick={(e) => { togglePerson(pid); refocus(e); }}
                >
                  <CloseIcon size={16} />
                </button>
              </span>
            );
          })}
          {filter.tags.map((t) => (
            <span key={`t:${t}`} className="ts-chip">
              <span className="ts-chip-label">{t}</span>
              <button
                type="button"
                className="ts-chip-x"
                aria-label={`Remove ${t}`}
                onMouseDown={keepFocus}
                onClick={(e) => { removeTag(t); refocus(e); }}
              >
                <CloseIcon size={16} />
              </button>
            </span>
          ))}
        </div>
        <input
          ref={inputRef}
          className="ts-input"
          type="text"
          role="combobox"
          aria-label="Search photos and files"
          aria-expanded={shown}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={shown && active >= 0 ? optId(active) : undefined}
          aria-describedby={summary ? `${id}now` : undefined}
          placeholder={chipCount ? "" : "Search photos and files"}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setMoved(null);
            setOpen(true);
          }}
          onFocus={() => { if (!quietFocus.current && !windowBack.current) setOpen(true); }}
          onKeyDown={onKeyDown}
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="none"
          spellCheck={false}
          enterKeyHint="search"
        />
        {clearable && (
          <button type="button" className="ts-clear" aria-label="Clear search" data-tip="Clear search" onMouseDown={keepFocus} onClick={onClear}>
            <CloseIcon />
          </button>
        )}
      </div>
      {shown && (
        <>
          <div className="ts-scrim" aria-hidden="true" onClick={dismiss} />
          <div
            ref={panelRef}
            className="ts-panel"
            tabIndex={-1}
            onPointerDown={onPanelPointerDown}
            onMouseDown={onPanelMouseDown}
            onPointerMove={onPanelPointerMove}
          >
            <div role="listbox" id={listId} aria-label="Suggestions">
              {tagOpts.length > 0 && section("things", "Things", tagOpts.map(([o, i]) => renderOption(o, i)))}
              {personOpts.length > 0 && section("people", "People", personOpts.map(([o, i]) => renderOption(o, i)))}
              {docsFirst && docsRow}
              {fileOpts.length > 0 && section("files", "Files", fileOpts.map(([o, i]) => renderOption(o, i)))}
              {!docsFirst && docsRow}
            </div>
            {!options.length && <p className="ts-hint">{alreadyIn ? <>“{typed}” is already in the search.</> : <>Nothing matches “{typed}”.</>}</p>}
          </div>
        </>
      )}
      {summary && <span id={`${id}now`} className="ts-sr">In the search: {summary}</span>}
      <span className="ts-sr" aria-live="polite">{status}</span>
    </div>
  );
}
