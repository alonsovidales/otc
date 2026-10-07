// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The search in the top bar, as in Google Photos. The field holds what
// Images is narrowed to - an open collection, people, tags - as chips.
// While it is in use a panel under it offers people and things to pick:
// with nothing typed, faces and some tags to browse; while typing, the
// people and tags that match. The search itself is photoFilter's, shared
// with the pages; picking something shows Images.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Person } from "../proto/messages";
import { personLabel, reloadPeople, reloadTags, usePeople, useTags } from "./libraryStore";
import { addTag, clearSearch, leaveGroup, removeTag, togglePerson, usePhotoFilter } from "./photoFilter";
import { CollectionsIcon } from "./NavIcons";
import "./TopSearch.css";

type Props = {
  // A filter was picked from anywhere: show Images.
  onShowPhotos: () => void;
  // "All people" in the suggestions: the People page.
  onShowPeople: () => void;
};

// [start, end) of the part of a label that matches what was typed.
type Span = [number, number];

// What the arrow keys and Enter go through in the panel, in the order
// shown. "text" is the typed word itself, offered when nothing matches.
type Option =
  | { kind: "person"; key: string; person: Person; span?: Span }
  | { kind: "allPeople"; key: string }
  | { kind: "tag"; key: string; tag: string; span?: Span }
  | { kind: "text"; key: string; text: string };

// The faces grid: as many columns of at least FACE_MIN as fit, two rows
// at most, the last place "All people".
const FACE_MIN = 72;
const FACE_GAP = 4;
const BROWSE_TAGS = 16;
const MATCH_PEOPLE = 5;
const MATCH_TAGS = 12;
// The highlight moved up past the first option: Enter then takes the
// typed text as it is.
const NONE = "";

// Text compared without case or accents, so "jose" finds "José". `from`
// maps each of its characters back to the original, to bold the match.
type Folded = { text: string; from: number[] };

function fold(s: string): Folded {
  let text = "";
  const from: number[] = [];
  let at = 0;
  for (const ch of s) {
    const f = ch.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
    for (let k = 0; k < f.length; k++) from.push(at);
    text += f;
    at += ch.length;
  }
  return { text, from };
}

// The entries matching the folded query: those starting with it first,
// then those with a word starting with it, then any containing it; in
// their own order within each.
function ranked<T extends { label: string; f: Folded }>(items: T[], q: string) {
  const hits: { item: T; rank: number; span: Span }[] = [];
  for (const item of items) {
    const { text, from } = item.f;
    let rank = -1;
    let at = -1;
    for (let j = text.indexOf(q); j !== -1; j = text.indexOf(q, j + 1)) {
      const r = j === 0 ? 0 : /[\p{L}\p{N}]/u.test(text[j - 1]) ? 2 : 1;
      if (rank < 0 || r < rank) { rank = r; at = j; }
      if (r < 2) break;
    }
    if (rank < 0) continue;
    const last = from[at + q.length - 1];
    const end = last + ((item.label.codePointAt(last) ?? 0) > 0xffff ? 2 : 1);
    hits.push({ item, rank, span: [from[at], end] });
  }
  return hits.sort((a, b) => a.rank - b.rank);
}

// New photos bring new tags and faces. The lists are fetched again when a
// search ends, if the last time was a while ago: current for the next
// one, and nothing moves under the pointer while picking.
const REFRESH_MS = 5 * 60_000;
let fetchedAt = Date.now();
function refreshIfStale() {
  if (Date.now() - fetchedAt < REFRESH_MS) return;
  fetchedAt = Date.now();
  void reloadTags().catch(() => {});
  void reloadPeople().catch(() => {});
}

const cls = (...names: (string | false)[]) => names.filter(Boolean).join(" ");

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
const BackIcon = () => <Icon><path d="M19 12H5m6-6-6 6 6 6" /></Icon>;
const CloseIcon = ({ size = 20 }: { size?: number }) => <Icon size={size} stroke={1.8}><path d="m6.5 6.5 11 11m0-11-11 11" /></Icon>;
const TagIcon = () => (
  <Icon><path d="M3.5 12V5.5a2 2 0 0 1 2-2H12a2 2 0 0 1 1.4.6l6.9 6.9a2 2 0 0 1 0 2.8l-6.6 6.6a2 2 0 0 1-2.8 0l-6.9-6.9A2 2 0 0 1 3.5 12Z" /><circle cx="8.5" cy="8.5" r="1.4" /></Icon>
);
const FaceIcon = ({ size = 20 }: { size?: number }) => (
  <Icon size={size}><circle cx="12" cy="9.5" r="3.5" /><path d="M5.5 19.5c1.1-3.2 3.6-4.8 6.5-4.8s5.4 1.6 6.5 4.8" /></Icon>
);
const ArrowIcon = () => <Icon><path d="M5 12h14m-6-6 6 6-6 6" /></Icon>;
const CheckIcon = ({ size = 12 }: { size?: number }) => <Icon size={size} stroke={3}><path d="m5 12.5 4.5 4.5L19 7.5" /></Icon>;

// A label with the part that matched in bold.
function Marked({ text, span }: { text: string; span?: Span }) {
  if (!span) return <>{text}</>;
  return <>{text.slice(0, span[0])}<mark className="ts-mark">{text.slice(span[0], span[1])}</mark>{text.slice(span[1])}</>;
}

export default function TopSearch({ onShowPhotos, onShowPeople }: Props) {
  const filter = usePhotoFilter();
  const people = usePeople();
  const tags = useTags();

  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  // Where the arrow keys or the mouse took the highlight: an option's key,
  // or NONE. null: the best match for what is typed, or nothing.
  const [moved, setMoved] = useState<string | null>(null);
  // Faces per row in the panel, from its width.
  const [cols, setCols] = useState(6);
  // The ends of the chips row that hide chips: 1 the left, 2 the right.
  const [fade, setFade] = useState(0);

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

  const id = useId();
  const listId = `${id}list`;
  const optId = (i: number) => `${id}opt${i}`;

  const chipCount = (filter.group ? 1 : 0) + filter.personIds.length + filter.tags.length;
  const shownChips = useRef(chipCount);
  // Whether the chips row shows its end, the newest chips: set by its
  // scrolling, unknown until first measured.
  const chipsAtEnd = useRef<boolean | null>(null);

  const byId = useMemo(() => new Map(people.items.map((p) => [p.id, p])), [people.items]);
  // Named people first, then the unnamed ones, each in the device's order.
  const ordered = useMemo(
    () => [...people.items.filter((p) => p.name.trim()), ...people.items.filter((p) => !p.name.trim())],
    [people.items],
  );
  const named = useMemo(
    () => ordered.filter((p) => p.name.trim()).map((p) => ({ person: p, label: p.name.trim(), f: fold(p.name.trim()) })),
    [ordered],
  );
  const tagIndex = useMemo(() => tags.map((t) => ({ label: t, f: fold(t) })), [tags]);

  const typed = query.trim();
  const q = fold(typed).text;

  const { options, best } = useMemo(() => {
    const out: Option[] = [];
    // Tags already searched for are chips in the field, not suggestions.
    const inSearch = new Set(filter.tags.map((t) => fold(t).text));
    const free = tagIndex.filter((t) => !inSearch.has(t.f.text));
    if (!q) {
      if (people.loaded && ordered.length) {
        for (const p of ordered.slice(0, cols * 2 - 1)) out.push({ kind: "person", key: `p:${p.id}`, person: p });
        out.push({ kind: "allPeople", key: "all" });
      }
      for (const t of free.slice(0, BROWSE_TAGS)) out.push({ kind: "tag", key: `t:${t.label}`, tag: t.label });
      return { options: out, best: null };
    }
    // What Enter takes: an exact match, or else the first.
    let exact: string | null = null;
    for (const h of ranked(named, q).slice(0, MATCH_PEOPLE)) {
      const key = `p:${h.item.person.id}`;
      out.push({ kind: "person", key, person: h.item.person, span: h.span });
      if (!exact && h.item.f.text === q) exact = key;
    }
    for (const h of ranked(free, q).slice(0, MATCH_TAGS)) {
      const key = `t:${h.item.label}`;
      out.push({ kind: "tag", key, tag: h.item.label, span: h.span });
      if (!exact && h.item.f.text === q) exact = key;
    }
    if (!out.length && !inSearch.has(q)) out.push({ kind: "text", key: "text", text: typed });
    return { options: out, best: exact ?? out[0]?.key ?? null };
  }, [q, typed, filter.tags, people.loaded, ordered, named, tagIndex, cols]);

  const activeKey = moved === null || (moved !== NONE && !options.some((o) => o.key === moved)) ? best : moved;
  const active = activeKey ? options.findIndex((o) => o.key === activeKey) : -1;

  const close = useCallback(() => {
    setOpen(false);
    setMoved(null);
    refreshIfStale();
  }, []);

  // A press anywhere else ends the search.
  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close();
    };
    document.addEventListener("pointerdown", down, true);
    return () => document.removeEventListener("pointerdown", down, true);
  }, [open, close]);

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

  // As many columns of faces as the panel's width holds.
  const facesRef = useCallback((el: HTMLDivElement | null) => {
    if (!el) return;
    const measure = () => setCols(Math.max(3, Math.floor((el.clientWidth + FACE_GAP) / (FACE_MIN + FACE_GAP))));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

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

  // Up and down go to the nearest option in the row above or below, so
  // they work alike in the faces grid, the wrapped tag chips and a list.
  const moveRow = (dir: 1 | -1) => {
    const nodes = panelRef.current ? Array.from(panelRef.current.querySelectorAll<HTMLElement>("[data-i]")) : [];
    const cells = nodes.map((el) => ({ i: Number(el.dataset.i), r: el.getBoundingClientRect() }));
    const cur = cells.find((c) => c.i === active);
    if (!cur) {
      if (options.length) moveTo(dir > 0 ? 0 : options.length - 1);
      return;
    }
    const ahead = (c: { r: DOMRect }) => (c.r.top - cur.r.top) * dir;
    const next = cells.filter((c) => ahead(c) > 4);
    if (!next.length) {
      if (dir < 0) setMoved(NONE);
      return;
    }
    const row = Math.min(...next.map(ahead));
    const x = cur.r.left + cur.r.width / 2;
    const off = (c: { r: DOMRect }) => Math.abs(c.r.left + c.r.width / 2 - x);
    moveTo(next.filter((c) => ahead(c) <= row + 4).reduce((a, c) => (off(c) < off(a) ? c : a)).i);
  };

  // Done: the panel closes. After a click or a tap the field lets go of
  // the focus too, which puts a phone's keyboard away from the photos.
  const finish = (how: "key" | "pointer") => {
    close();
    if (how === "pointer") inputRef.current?.blur();
  };

  // A typed word the device knows, in its spelling ("Dog" is "dog").
  const knownSpelling = (text: string) => {
    const f = fold(text).text;
    return tagIndex.find((t) => t.f.text === f)?.label ?? text;
  };

  const pick = (o: Option, how: "key" | "pointer") => {
    switch (o.kind) {
      case "allPeople":
        finish(how);
        onShowPeople();
        return;
      case "person":
        // The panel stays open, to add more people.
        togglePerson(o.person.id);
        if (query) {
          setQuery("");
          setMoved(null);
        }
        onShowPhotos();
        return;
      case "tag":
      case "text":
        addTag(o.kind === "tag" ? o.tag : knownSpelling(o.text));
        setQuery("");
        finish(how);
        onShowPhotos();
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        e.preventDefault();
        if (open) moveRow(e.key === "ArrowDown" ? 1 : -1);
        else setOpen(true);
        break;
      case "ArrowLeft":
      case "ArrowRight":
        // Sideways through the faces and tag chips when nothing is typed;
        // otherwise the keys move the caret.
        if (!open || q || active < 0) return;
        e.preventDefault();
        moveTo(Math.min(options.length - 1, Math.max(0, active + (e.key === "ArrowRight" ? 1 : -1))));
        break;
      case "Enter":
        e.preventDefault();
        if (!open) setOpen(true);
        else if (active >= 0) pick(options[active], "key");
        else if (typed) pick({ kind: "text", key: "text", text: typed }, "key");
        else {
          finish("key");
          if (chipCount) onShowPhotos();
        }
        break;
      case "Escape":
        if (open) close();
        else inputRef.current?.blur();
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
  // lets it go, so the keyboard stops covering the faces.
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

  // Nothing typed: faces in a grid and tags as chips. Typing: a list.
  const renderOption = (o: Option, i: number) => {
    const hl = i === active;
    switch (o.kind) {
      case "person": {
        const on = filter.personIds.includes(o.person.id);
        const thumb = people.thumbs.get(o.person.id);
        if (q) {
          return (
            <div key={o.key} {...optionProps(o, i)} aria-checked={on} className={cls("ts-row", on && "is-on", hl && "is-active")}>
              <span className="ts-row-pic">{thumb ? <img src={thumb} alt="" draggable={false} /> : <FaceIcon size={18} />}</span>
              <span className="ts-row-label"><Marked text={personLabel(o.person)} span={o.span} /></span>
              {on && <span className="ts-row-tick"><CheckIcon size={18} /></span>}
            </div>
          );
        }
        return (
          <div key={o.key} {...optionProps(o, i)} aria-checked={on} className={cls("ts-face", on && "is-on", hl && "is-active")}>
            <span className="ts-face-pic">
              {thumb ? <img src={thumb} alt="" draggable={false} /> : <FaceIcon size={24} />}
              {on && <span className="ts-tick"><CheckIcon /></span>}
            </span>
            <span className={cls("ts-face-name", !o.person.name.trim() && "is-unnamed")}>{personLabel(o.person)}</span>
          </div>
        );
      }
      case "allPeople":
        return (
          <div key={o.key} {...optionProps(o, i)} className={cls("ts-face", "ts-face-all", hl && "is-active")}>
            <span className="ts-face-pic"><ArrowIcon /></span>
            <span className="ts-face-name">All people</span>
          </div>
        );
      case "tag":
        if (!q) return <div key={o.key} {...optionProps(o, i)} className={cls("ts-tagchip", hl && "is-active")}>{o.tag}</div>;
        return (
          <div key={o.key} {...optionProps(o, i)} className={cls("ts-row", hl && "is-active")}>
            <span className="ts-row-icon"><TagIcon /></span>
            <span className="ts-row-label"><Marked text={o.tag} span={o.span} /></span>
          </div>
        );
      case "text":
        return (
          <div key={o.key} {...optionProps(o, i)} className={cls("ts-row", hl && "is-active")}>
            <span className="ts-row-icon"><SearchIcon /></span>
            <span className="ts-row-label">Search for “<mark className="ts-mark">{o.text}</mark>”</span>
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
  const personOpts = numbered.filter(([o]) => o.kind === "person" || o.kind === "allPeople");
  const tagOpts = numbered.filter(([o]) => o.kind === "tag");
  const textOpt = numbered.find(([o]) => o.kind === "text");

  let hint: React.ReactNode = null;
  if (!q && people.loaded && !ordered.length && !tags.length) {
    hint = <>Type a word like <em>beach</em> or <em>dog</em>, or pick a person.</>;
  } else if (q && !options.length) {
    hint = <>“{typed}” is already in the search.</>;
  }

  const status = !open || !q || !options.length
    ? ""
    : textOpt ? "No matches" : `${options.length} ${options.length === 1 ? "suggestion" : "suggestions"}`;
  // The chips, read out with the field.
  const summary = [
    filter.group && `the collection ${filter.group.name}`,
    ...filter.personIds.map((pid) => { const p = byId.get(pid); return p ? personLabel(p) : "a person"; }),
    ...filter.tags,
  ].filter(Boolean).join(", ");

  return (
    <div ref={rootRef} className={cls("ts-root", open && "is-open")} onBlur={onBlur}>
      <div className="ts-field" onMouseDown={onFieldMouseDown}>
        <span className="ts-glass" aria-hidden="true"><SearchIcon /></span>
        {open && (
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
          aria-label="Search your photos"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={open && active >= 0 ? optId(active) : undefined}
          aria-describedby={summary ? `${id}now` : undefined}
          placeholder={chipCount ? "" : "Search your photos"}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setMoved(null);
            setOpen(true);
          }}
          onFocus={() => { if (!quietFocus.current) setOpen(true); }}
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
      {open && (
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
              {!q && (!people.loaded || personOpts.length > 0) && section("people", "People",
                <div role="presentation" ref={facesRef} className="ts-faces" style={{ "--ts-cols": cols } as React.CSSProperties}>
                  {people.loaded
                    ? personOpts.map(([o, i]) => renderOption(o, i))
                    : Array.from({ length: cols }, (_, k) => (
                      <div key={k} className="ts-face is-skel" aria-hidden="true">
                        <span className="ts-face-pic" />
                        <span className="ts-skel-line" />
                      </div>
                    ))}
                </div>,
              )}
              {!q && tagOpts.length > 0 && section("things", "Things",
                <div role="presentation" className="ts-tagchips">{tagOpts.map(([o, i]) => renderOption(o, i))}</div>,
              )}
              {q && personOpts.length > 0 && section("people", "People", personOpts.map(([o, i]) => renderOption(o, i)))}
              {q && tagOpts.length > 0 && section("things", "Things", tagOpts.map(([o, i]) => renderOption(o, i)))}
              {textOpt && renderOption(textOpt[0], textOpt[1])}
            </div>
            {hint && <p className="ts-hint">{hint}</p>}
          </div>
        </>
      )}
      {summary && <span id={`${id}now`} className="ts-sr">In the search: {summary}</span>}
      <span className="ts-sr" aria-live="polite">{status}</span>
    </div>
  );
}
