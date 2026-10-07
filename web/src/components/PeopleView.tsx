// SPDX-License-Identifier: AGPL-3.0-or-later

import { memo, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type {
  KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { useWS } from "../net/useWS";
import type { Person, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { reloadPeople, renamePersonLocally, usePeople } from "./libraryStore";
import type { Reload } from "./libraryStore";
import { forgetPerson, showPerson } from "./photoFilter";
import Spinner from "./Spinner";
import "./PeopleView.css";

// The People page: the faces the device found, named people first, each a
// circle that opens Images on that person. A person's "more" menu names or
// deletes them. Several are picked as photos are on Images - the circle on
// a face's corner, a long press on a touch screen, Shift for a run - and
// the bar that then lies over the top bar merges them into one (face
// matching split one person in several) or deletes them.

type Props = {
  // A person was opened (photoFilter.showPerson already called): show Images.
  onOpenPhotos: () => void;
};

type ReqPayload = NonNullable<ReqEnvelope["payload"]>;

type Confirm =
  | { kind: "merge" }
  // From a person's menu (one) or from the selection bar.
  | { kind: "delete"; ids: readonly string[]; from: "menu" | "bar" };

type FocusPart = "face" | "more";

const cSkeletonFaces = 14;
// How long the first listing may take before the page offers to ask again.
const cSlowMs = 10_000;
const cToastMs = 3200;
const cErrorToastMs = 6000;
// How long after a confirmation opens a press on it is ignored.
const cArmMs = 450;
// The least room kept between a dialog and the window's top or bottom
// (the CSS max-height leaves twice this).
const cDialogEdge = 16;
// people.name is a varchar(150).
const cMaxName = 100;
// A touch held this long picks a face, as on Images; moving further than
// the slop first is a scroll.
const cLongPressMs = 450;
const cPressSlopPx = 8;
// Deletions out at once: one request per person, a few at a time.
const cDeleteParallel = 3;

const counted = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const photosLabel = (n: number) => counted(n, "photo", "photos");
const tidy = (s: string) => s.replace(/\s+/g, " ").trim();
const fold = (s: string) => tidy(s).toLocaleLowerCase();
// "Ana", "Ana and Leo", "Ana, Leo and Rosa".
const andList = (xs: readonly string[]) =>
  xs.length < 2 ? (xs[0] ?? "") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;

// A request the device answers with an Ack: true once it is done.
async function ack(payload: ReqPayload): Promise<boolean> {
  const resp: RespEnvelope = await useWS.request((e) => { e.payload = payload; });
  return resp.payload?.$case === "respAck" && resp.payload.respAck.ok;
}

// A double click (or a double tap) on something its first click takes away
// sends the second click to whatever is under it then: the top bar's menu
// button under the selection bar's close, a face under a menu item, a face
// that opens Images once the selection is gone. For a moment after such a
// click, a second click (detail 2 or more) goes nowhere, and its press
// takes no focus (a name field just opened keeps it). Single clicks and
// the keyboard's (detail 0) are left alone. As on Images.
function swallowSecondClick(ms = 500) {
  const until = performance.now() + ms;
  const onClick = (e: MouseEvent) => {
    if (e.detail < 2 || performance.now() > until) return;
    e.preventDefault();
    e.stopPropagation();
  };
  const onPress = (e: MouseEvent) => {
    if (e.detail >= 2 && performance.now() <= until) e.preventDefault();
  };
  window.addEventListener("click", onClick, true);
  window.addEventListener("mousedown", onPress, true);
  window.setTimeout(() => {
    window.removeEventListener("click", onClick, true);
    window.removeEventListener("mousedown", onPress, true);
  }, ms);
}

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

// The different names among some people, each once (Ana and ana are one).
function namesOf(list: readonly Person[]): string[] {
  const seen = new Set<string>();
  const names: string[] = [];
  for (const p of list) {
    const n = tidy(p.name);
    if (n && !seen.has(fold(n))) {
      seen.add(fold(n));
      names.push(n);
    }
  }
  return names;
}

export default function PeopleView({ onOpenPhotos }: Props) {
  const { items, thumbs, loaded } = usePeople();
  const { failed, slow, retry } = useFreshList(loaded, reloadPeople);
  const rootRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const menuId = useId();
  // Below this the selection bar's actions are icons, each with a tooltip.
  const compact = useMedia("(max-width: 899px)");

  // Held in a ref so the tiles' callbacks stay the same across App's renders.
  const openPhotos = useRef(onOpenPhotos);
  useEffect(() => { openPhotos.current = onOpenPhotos; });

  const [menu, setMenu] = useState<{ id: string; anchor: HTMLButtonElement } | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  // The field's blur after Enter or Esc must not save a second time.
  const editingRef = useRef<string | null>(null);
  // Names on their way to the device, shown (dimmed) until it answers.
  const [saving, setSaving] = useState<ReadonlyMap<string, string>>(() => new Map());
  // Merged away or deleted: hidden at once, before the list comes again.
  const [gone, setGone] = useState<ReadonlySet<string>>(() => new Set());
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  // How far a deletion of several has got.
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [toast, setToast] = useState<{ text: string; at: number; error: boolean } | null>(null);

  // Named people first, each part in the order the device lists them.
  const people = useMemo(() => {
    const shown = items.filter((p) => !gone.has(p.id));
    return [...shown.filter((p) => p.name.trim() !== ""), ...shown.filter((p) => p.name.trim() === "")];
  }, [items, gone]);
  const menuPerson = menu ? people.find((p) => p.id === menu.id) ?? null : null;

  // Focus moves once the render that needs it is on screen: a button that
  // a selection hides, the face a dialog was about, or the title when that
  // person is gone.
  const pendingFocus = useRef<{ id: string; part: FocusPart } | null>(null);
  const focusLater = useCallback((id: string, part: FocusPart) => { pendingFocus.current = { id, part }; }, []);
  useEffect(() => {
    const f = pendingFocus.current;
    if (!f) return;
    pendingFocus.current = null;
    const el = rootRef.current?.querySelector<HTMLElement>(`[data-person-id="${CSS.escape(f.id)}"] .pv-${f.part}`);
    (el ?? titleRef.current)?.focus({ preventScroll: true });
  });

  const say = useCallback((text: string, error = false) => setToast({ text, at: Date.now(), error }), []);
  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), toast.error ? cErrorToastMs : cToastMs);
    return () => window.clearTimeout(t);
  }, [toast]);

  // ---- selecting ------------------------------------------------------------
  // Ids, in the order they were picked; the page shows them in its own.

  const [sel, setSel] = useState<readonly string[]>([]);
  // Where a Shift-click run starts: the person last picked or unpicked.
  const anchorRef = useRef<string | null>(null);
  // Started from a person's "Merge with others…": the bar says what to do.
  const [mergeHint, setMergeHint] = useState(false);
  const barRef = useRef<HTMLDivElement>(null);

  const selSet = useMemo(() => new Set(sel), [sel]);
  const selPeople = useMemo(() => people.filter((p) => selSet.has(p.id)), [people, selSet]);
  const selecting = selPeople.length > 0;

  // Someone merged or deleted elsewhere (another tab, a new listing) is no
  // longer picked.
  useEffect(() => {
    setSel((prev) => {
      const here = new Set(people.map((p) => p.id));
      const next = prev.filter((id) => here.has(id));
      return next.length === prev.length ? prev : next;
    });
  }, [people]);
  useEffect(() => { if (!selecting) setMergeHint(false); }, [selecting]);

  const clearSel = useCallback(() => {
    setSel([]);
    anchorRef.current = null;
  }, []);

  // Closed from the bar with the keyboard: the focus goes back to the face
  // last picked, not to the top of the page.
  const endSelection = useCallback(() => {
    const el = document.activeElement;
    const last = anchorRef.current;
    if (last && el instanceof HTMLElement && barRef.current?.contains(el)) focusLater(last, "face");
    clearSel();
  }, [clearSel, focusLater]);

  const toggle = (id: string, range: boolean) => {
    const anchor = anchorRef.current;
    anchorRef.current = id;
    if (range && anchor && anchor !== id) {
      const a = people.findIndex((p) => p.id === anchor);
      const b = people.findIndex((p) => p.id === id);
      if (a >= 0 && b >= 0) {
        const run = people.slice(Math.min(a, b), Math.max(a, b) + 1).map((p) => p.id);
        setSel((prev) => {
          const have = new Set(prev);
          return [...prev, ...run.filter((x) => !have.has(x))];
        });
        return;
      }
    }
    setSel((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  const pick = (id: string) => {
    anchorRef.current = id;
    setSel((prev) => (prev.includes(id) ? prev : [...prev, id]));
  };

  // Esc ends the selection - unless a dialog or the menu is what it closes.
  const somethingOnTop = confirm !== null || menu !== null;
  useEffect(() => {
    if (!selecting || somethingOnTop) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) endSelection();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selecting, somethingOnTop, endSelection]);

  // The selection bar lies over the top bar: what is under it is out of
  // reach of the keyboard too, as on Images.
  useEffect(() => {
    const bar = document.querySelector<HTMLElement>(".topbar");
    if (!selecting || !bar) return;
    bar.inert = true;
    return () => { bar.inert = false; };
  }, [selecting]);

  // ---- the faces: clicks, and the long press on touch -------------------------
  // One set of handlers on the grid, as on Images: the tiles stay plain
  // memoised markup and the press is the grid's.

  const pressRef = useRef<{ pointerId: number; x: number; y: number; timer: number; id: string } | null>(null);
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

  const firePress = (id: string) => {
    pressRef.current = null;
    pressFiredRef.current = true;
    pick(id);
    try { navigator.vibrate?.(10); } catch { /* not every device can */ }
  };

  // The person a press or click is about, unless it is on a control of
  // its own: the "more" button, "Add a name", the name field.
  const personAt = (target: EventTarget | null): string | null => {
    if (!(target instanceof Element) || target.closest(".pv-more, .pv-addname, .pv-name-input")) return null;
    return target.closest<HTMLElement>("[data-person-id]")?.dataset.personId ?? null;
  };

  const onGridPointerDown = (e: ReactPointerEvent<HTMLUListElement>) => {
    pointerTypeRef.current = e.pointerType;
    pressFiredRef.current = false;
    // A new touch: the click a long press leaves behind came before it.
    suppressClickUntilRef.current = 0;
    cancelPress();
    if (e.pointerType === "mouse" || !e.isPrimary) return;
    const id = personAt(e.target);
    if (!id) return;
    const timer = window.setTimeout(() => firePress(id), cLongPressMs);
    pressRef.current = { pointerId: e.pointerId, x: e.clientX, y: e.clientY, timer, id };
  };
  const onGridPointerMove = (e: ReactPointerEvent<HTMLUListElement>) => {
    const p = pressRef.current;
    if (p && e.pointerId === p.pointerId && Math.hypot(e.clientX - p.x, e.clientY - p.y) > cPressSlopPx) cancelPress();
  };
  // The click that ends a long press must not unpick the face straight
  // away (or open it). It follows the release at once, or not at all.
  const onGridPointerEnd = () => {
    cancelPress();
    if (pressFiredRef.current) {
      pressFiredRef.current = false;
      suppressClickUntilRef.current = performance.now() + 600;
    }
  };
  const onGridContextMenu = (e: ReactMouseEvent<HTMLUListElement>) => {
    if (pointerTypeRef.current === "mouse" || !personAt(e.target)) return;
    // Android answers a long press on a picture with its own menu: here
    // the press picks the face instead.
    e.preventDefault();
    const p = pressRef.current;
    if (p) {
      window.clearTimeout(p.timer);
      firePress(p.id);
    }
  };
  const onGridClick = (e: ReactMouseEvent<HTMLUListElement>) => {
    if (performance.now() < suppressClickUntilRef.current) return;
    const id = personAt(e.target);
    if (!id) return;
    const target = e.target as Element;
    // The circle picks; once anything is picked, so does the whole face.
    // A double click picks once: its second click would take it back, or
    // open Images when that was the last face picked.
    if (selecting || target.closest(".pv-check")) {
      swallowSecondClick();
      toggle(id, e.shiftKey);
      return;
    }
    if (!target.closest(".pv-face")) return;
    // Nor may a double click's second land on a photo in Images.
    swallowSecondClick();
    showPerson(id);
    openPhotos.current();
  };

  // ---- renaming -----------------------------------------------------------

  const startRename = useCallback((p: Person) => {
    setMenu(null);
    editingRef.current = p.id;
    setEditingId(p.id);
  }, []);

  const saveName = useCallback(async (p: Person, name: string) => {
    setSaving((m) => new Map(m).set(p.id, name));
    let ok = false;
    try {
      ok = await ack({ $case: "reqRenamePerson", reqRenamePerson: { id: p.id, name } });
    } catch {
      // No answer: reported below like a refusal.
    }
    if (ok) renamePersonLocally(p.id, name);
    else say("The name couldn't be saved. Try again.", true);
    setSaving((m) => {
      const next = new Map(m);
      next.delete(p.id);
      return next;
    });
  }, [say]);

  // typed is null for Esc. Leaving a field emptied by accident (a click
  // elsewhere) keeps the name; Enter on an empty field takes it off.
  const endRename = useCallback((p: Person, typed: string | null, fromBlur: boolean) => {
    if (editingRef.current !== p.id) return;
    editingRef.current = null;
    setEditingId(null);
    if (!fromBlur) focusLater(p.id, "face");
    if (typed === null) return;
    const name = tidy(typed);
    if (name === tidy(p.name) || (fromBlur && name === "")) return;
    void saveName(p, name);
  }, [focusLater, saveName]);

  // ---- the menu -------------------------------------------------------------

  const toggleMenu = useCallback((p: Person, anchor: HTMLButtonElement) => {
    setMenu((m) => (m?.id === p.id ? null : { id: p.id, anchor }));
  }, []);
  const closeMenu = useCallback(() => setMenu(null), []);

  // "Merge with others…": a selection with this person in it, and the bar
  // says what to pick next.
  // (The menu itself sees that a double click's second click doesn't pick
  // the face under the item.)
  const mergeWithOthers = (p: Person) => {
    setMenu(null);
    anchorRef.current = p.id;
    setSel([p.id]);
    setMergeHint(true);
    focusLater(p.id, "face");
  };

  // ---- merging and deleting -------------------------------------------------

  // The face a merge kept: the focus goes there once its dialog closes.
  const mergedIntoRef = useRef<string | null>(null);

  const merge = async (keep: Person, name: string) => {
    const sources = selPeople.map((p) => p.id).filter((id) => id !== keep.id);
    if (sources.length === 0) return;
    setBusy(true);
    setConfirmError(null);
    try {
      // The merged person keeps the kept face's name, so a name it lacks
      // (one only another of them has, or one typed here) is given to it
      // first: merged first and named after, a failed rename would lose a
      // name whose only owner was just merged away. This way round a
      // failure loses nothing - at worst the kept face is named and the
      // others are still there to merge again.
      if (name && name !== tidy(keep.name)) {
        if (!(await ack({ $case: "reqRenamePerson", reqRenamePerson: { id: keep.id, name } }))) {
          setConfirmError("The name couldn't be saved, so nothing was merged. Try again.");
          return;
        }
        renamePersonLocally(keep.id, name);
      }
      if (!(await ack({ $case: "reqMergePeople", reqMergePeople: { targetId: keep.id, sourceIds: sources } }))) {
        setConfirmError("They couldn't be merged. Try again.");
        return;
      }
      sources.forEach(forgetPerson);
      setGone((s) => {
        const next = new Set(s);
        sources.forEach((id) => next.add(id));
        return next;
      });
      mergedIntoRef.current = keep.id;
      setConfirm(null);
      clearSel();
      const n = sources.length + 1;
      say(name ? `Merged ${n} into ${name}` : `Merged ${n} faces`);
      void reloadPeople().catch(() => {});
    } catch {
      setConfirmError("Your device didn't answer. Try again.");
    } finally {
      setBusy(false);
    }
  };

  // One request per person, a few at a time. What couldn't be deleted
  // stays picked, to try again; when nothing could, the dialog stays open
  // and says so.
  const remove = async (ids: readonly string[]) => {
    if (ids.length === 0) return;
    setBusy(true);
    setConfirmError(null);
    setProgress({ done: 0, total: ids.length });
    const deleted = new Set<string>();
    let unanswered = 0;
    let next = 0;
    const worker = async () => {
      while (next < ids.length) {
        const id = ids[next++];
        try {
          if (await ack({ $case: "reqDeletePerson", reqDeletePerson: { id } })) deleted.add(id);
        } catch {
          unanswered++;
        }
        setProgress((p) => (p ? { ...p, done: p.done + 1 } : p));
      }
    };
    await Promise.all(Array.from({ length: Math.min(cDeleteParallel, ids.length) }, worker));
    setBusy(false);
    setProgress(null);
    if (deleted.size === 0) {
      setConfirmError(unanswered === ids.length
        ? "Your device didn't answer. Try again."
        : ids.length === 1 ? "This person couldn't be deleted. Try again." : "They couldn't be deleted. Try again.");
      return;
    }
    deleted.forEach(forgetPerson);
    setGone((s) => {
      const out = new Set(s);
      deleted.forEach((id) => out.add(id));
      return out;
    });
    setSel((prev) => prev.filter((id) => !deleted.has(id)));
    if (anchorRef.current && deleted.has(anchorRef.current)) anchorRef.current = null;
    setConfirm(null);
    const failedCount = ids.length - deleted.size;
    if (failedCount === 0) {
      say(ids.length === 1 ? "Person deleted" : `Deleted ${counted(ids.length, "person", "people")}`);
    } else {
      // "1 of 2 people couldn't be deleted and is still selected." Only a
      // deletion of several gets here: one person is all or nothing.
      const verb = failedCount === 1 ? "is" : "are";
      say(`${failedCount.toLocaleString()} of ${counted(ids.length, "person", "people")} couldn't be deleted and ${verb} still selected. Try again.`, true);
    }
    void reloadPeople().catch(() => {});
  };

  // ---- the page -------------------------------------------------------------

  let body: ReactNode;
  if (loaded && people.length === 0) {
    body = (
      <div className="pv-state">
        <span className="pv-state-art"><EmptyArt /></span>
        <h2 className="pv-state-title">No faces found yet</h2>
        <p className="pv-state-text">
          Face recognition runs on the device after photos are uploaded. You can switch it on in Settings.
        </p>
      </div>
    );
  } else if (loaded) {
    body = (
      <ul
        className="pv-grid"
        role="list"
        onClick={onGridClick}
        onPointerDown={onGridPointerDown}
        onPointerMove={onGridPointerMove}
        onPointerUp={onGridPointerEnd}
        onPointerCancel={onGridPointerEnd}
        onContextMenu={onGridContextMenu}
      >
        {people.map((p) => {
          const pendingName = saving.get(p.id);
          return (
            <PersonTile
              key={p.id}
              person={p}
              thumb={thumbs.get(p.id)}
              name={tidy(pendingName ?? p.name)}
              pending={pendingName !== undefined}
              editing={editingId === p.id}
              selecting={selecting}
              selected={selSet.has(p.id)}
              menuOpen={menu?.id === p.id}
              menuId={menuId}
              onMore={toggleMenu}
              onStartRename={startRename}
              onEndRename={endRename}
            />
          );
        })}
      </ul>
    );
  } else if (failed || slow) {
    body = (
      <div className="pv-state" role="status">
        <span className="pv-state-art"><ProblemIcon /></span>
        <h2 className="pv-state-title">{failed ? "Couldn't load people" : "Still waiting for your device"}</h2>
        <p className="pv-state-text">
          {failed ? "Check that your device is online, then try again." : "People are taking longer than usual to load."}
        </p>
        <button type="button" className="pv-btn pv-btn-primary pv-state-btn" onClick={retry}>Try again</button>
      </div>
    );
  } else {
    body = (
      <ul className="pv-grid pv-skeleton" aria-busy="true" aria-label="Loading people">
        {Array.from({ length: cSkeletonFaces }, (_, i) => (
          <li key={i} className="pv-person" aria-hidden="true">
            <span className="pv-face-wrap" />
            <span className="pv-label"><span className="pv-bar" /></span>
            <span className="pv-count"><span className="pv-bar" /></span>
          </li>
        ))}
      </ul>
    );
  }

  const canMerge = selPeople.length > 1;
  const deleting = confirm?.kind === "delete" ? confirm : null;
  const deletePeople = deleting ? people.filter((p) => deleting.ids.includes(p.id)) : [];
  // Who a question is about went away meanwhile (another tab merged or
  // deleted them): no question left, and Esc ends the selection again.
  const confirmMoot = (confirm?.kind === "merge" && !canMerge) || (deleting !== null && deletePeople.length === 0);
  useEffect(() => { if (confirmMoot && !busy) setConfirm(null); }, [confirmMoot, busy]);

  return (
    <div ref={rootRef} className={`pv-root${selecting ? " selecting" : ""}`}>
      {/* First, though it is drawn over the top bar: Tab reaches it before
          the faces, as it would the top bar it covers. pv-pickbar is what
          TopSearch.tsx looks for to close its panel under a bar. */}
      {selecting && (
        <div ref={barRef} className="pv-selbar pv-pickbar" role="toolbar" aria-label="Selected people">
          <button
            type="button"
            className="pv-icon-btn"
            onClick={() => { swallowSecondClick(); endSelection(); }}
            aria-label="Clear selection"
            data-tip="Clear selection"
          >
            <CloseIcon />
          </button>
          <div className="pv-selbar-text" aria-live="polite">
            <span className="pv-selbar-count">{selPeople.length.toLocaleString()} selected</span>
            {mergeHint && !canMerge && <span className="pv-selbar-hint">Select the faces of the same person</span>}
          </div>
          <div className="pv-selbar-actions">
            <BarButton
              compact={compact}
              label="Merge"
              icon={<MergeIcon size={22} />}
              unavailable={canMerge ? undefined : "Select 2 or more to merge"}
              onClick={() => {
                if (!canMerge) {
                  say("Select 2 or more faces to merge them into one.");
                  return;
                }
                mergedIntoRef.current = null;
                setConfirmError(null);
                setConfirm({ kind: "merge" });
              }}
            />
            <BarButton
              compact={compact}
              danger
              label="Delete"
              icon={<TrashIcon size={22} />}
              onClick={() => {
                setConfirmError(null);
                setConfirm({ kind: "delete", ids: selPeople.map((p) => p.id), from: "bar" });
              }}
            />
          </div>
        </div>
      )}

      <header className="pv-head">
        <h1 ref={titleRef} className="pv-title" tabIndex={-1}>People</h1>
        <p className="pv-sub">Faces found in your photos. Name someone to find them easily.</p>
      </header>
      {body}

      {menu && menuPerson && (
        <PersonMenu
          id={menuId}
          anchor={menu.anchor}
          list={people}
          named={tidy(menuPerson.name) !== ""}
          canMerge={people.length > 1}
          onRename={() => startRename(menuPerson)}
          onMerge={() => mergeWithOthers(menuPerson)}
          onDelete={() => {
            setMenu(null);
            setConfirmError(null);
            setConfirm({ kind: "delete", ids: [menuPerson.id], from: "menu" });
          }}
          onClose={closeMenu}
        />
      )}

      {/* Keyed by what they ask about: another question is a new dialog. */}
      {confirm?.kind === "merge" && selPeople.length > 1 && (
        <MergeDialog
          people={selPeople}
          thumbs={thumbs}
          busy={busy}
          error={confirmError}
          onCancel={() => setConfirm(null)}
          onConfirm={(keep, name) => void merge(keep, name)}
          onReturnFocus={() => {
            const id = mergedIntoRef.current ?? selPeople[0]?.id;
            if (id) focusLater(id, "face");
          }}
        />
      )}
      {deleting && deletePeople.length > 0 && (
        <ConfirmDialog
          key={`delete:${deleting.ids.join(",")}`}
          art={<FaceStack people={deletePeople} thumbs={thumbs} />}
          title={deletePeople.length === 1
            ? (tidy(deletePeople[0].name) ? `Delete ${tidy(deletePeople[0].name)}?` : "Delete this person?")
            : `Delete ${deletePeople.length.toLocaleString()} people?`}
          // What DeletePerson does: the person and every face matched to
          // them go, the photos stay, and a face in a photo added later is
          // found as someone new.
          text="Their photos stay; only the faces matched to them are forgotten. This can't be undone, and they'll come back as new people in photos added later."
          action="Delete"
          busy={busy}
          busyText={progress && progress.total > 1 ? `${Math.min(progress.done + 1, progress.total)} of ${progress.total}` : undefined}
          error={confirmError}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void remove(deletePeople.map((p) => p.id))}
          onReturnFocus={() => focusLater(deleting.ids[0], deleting.from === "menu" ? "more" : "face")}
        />
      )}

      <div className="pv-toast-host" aria-live="polite">
        {toast && <div key={toast.at} className={`pv-toast${toast.error ? " error" : ""}`}>{toast.text}</div>}
      </div>
    </div>
  );
}

// ---- one person -------------------------------------------------------------

type TileProps = {
  person: Person;
  thumb: string | undefined;
  // What shows as the name: one being saved, else the person's own.
  name: string;
  pending: boolean;
  editing: boolean;
  // Anything picked (a click anywhere on a face picks it), and this one.
  selecting: boolean;
  selected: boolean;
  menuOpen: boolean;
  menuId: string;
  onMore: (p: Person, anchor: HTMLButtonElement) => void;
  onStartRename: (p: Person) => void;
  onEndRename: (p: Person, typed: string | null, fromBlur: boolean) => void;
};

// Memoised, with the page's callbacks kept stable: a menu, a toast, a pick
// or a name being saved re-renders one tile, not every face in the library.
// The face and its circle have no handlers of their own: the grid's take
// their clicks (see onGridClick).
const PersonTile = memo(function PersonTile(t: TileProps) {
  const { person, name, selecting, selected } = t;
  const who = name || "this person";
  const faceLabel = `${name || "Unnamed person"}, ${photosLabel(person.faceCount)}`;

  let label: ReactNode;
  if (t.editing) {
    label = (
      <NameEditor
        initial={name}
        named={name !== ""}
        onDone={(typed, fromBlur) => t.onEndRename(person, typed, fromBlur)}
      />
    );
  } else if (name) {
    label = <span className={`pv-name${t.pending ? " pending" : ""}`} title={name}>{name}</span>;
  } else if (selecting) {
    label = <span className="pv-name pv-unnamed">Unnamed</span>;
  } else {
    label = (
      <button type="button" className="pv-addname" onClick={() => t.onStartRename(person)}>
        Add a name
      </button>
    );
  }

  return (
    <li className={`pv-person${selected ? " selected" : ""}`} data-person-id={person.id}>
      <div className="pv-face-wrap">
        <button type="button" className="pv-face" aria-label={faceLabel} aria-pressed={selecting ? selected : undefined}>
          {t.thumb
            ? <img src={t.thumb} alt="" draggable={false} decoding="async" />
            : <span className="pv-face-ph"><PersonGlyph size={44} /></span>}
        </button>
        {/* Its own button for the keyboard; on a touch screen taps go to the
            face underneath (CSS), which picks it once anything is picked. */}
        <button
          type="button"
          className="pv-check"
          aria-pressed={selected}
          aria-label={`Select ${faceLabel}`}
          tabIndex={selecting ? -1 : 0}
        >
          <span className="pv-check-circle"><CheckIcon size={16} /></span>
        </button>
        {!selecting && (
          <button
            type="button"
            className="pv-more"
            onClick={(e) => t.onMore(person, e.currentTarget)}
            aria-label={`More options for ${who}`}
            data-tip="More options"
            aria-haspopup="menu"
            aria-expanded={t.menuOpen}
            aria-controls={t.menuOpen ? t.menuId : undefined}
          >
            <MoreIcon />
          </button>
        )}
      </div>
      <div className="pv-label">{label}</div>
      <span className="pv-count">{photosLabel(person.faceCount)}</span>
    </li>
  );
});

// The name field under a face. Its text lives here, so typing re-renders
// the field alone.
function NameEditor({ initial, named, onDone }: {
  initial: string;
  named: boolean;
  onDone: (typed: string | null, fromBlur: boolean) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <input
      className="pv-name-input"
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onFocus={(e) => e.currentTarget.select()}
      onBlur={(e) => onDone(e.currentTarget.value, true)}
      onKeyDown={(e) => {
        // Enter also ends an input method's composition: not a save.
        if (e.key === "Enter" && !e.nativeEvent.isComposing) {
          e.preventDefault();
          onDone(e.currentTarget.value, false);
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          onDone(null, false);
        }
      }}
      autoFocus
      maxLength={cMaxName}
      placeholder="Name"
      aria-label={named ? "New name" : "Name this person"}
      autoComplete="off"
      autoCapitalize="words"
      spellCheck={false}
      enterKeyHint="done"
    />
  );
}

// ---- the selection bar ------------------------------------------------------

// An action of the selection bar: icon and label, or the icon alone with a
// tooltip below 900px. One that can't be used yet stays where it is, dimmed,
// and says why - in its tooltip, to a screen reader, and when pressed.
function BarButton({ compact, label, icon, onClick, danger = false, unavailable }: {
  compact: boolean;
  label: string;
  icon: ReactNode;
  onClick: () => void;
  danger?: boolean;
  unavailable?: string;
}) {
  return (
    <button
      type="button"
      className={`pv-bar-btn${danger ? " danger" : ""}`}
      onClick={onClick}
      aria-disabled={unavailable ? true : undefined}
      aria-label={unavailable ? `${label} (${unavailable.toLowerCase()})` : label}
      data-tip={unavailable ?? (compact ? label : undefined)}
    >
      {icon}
      <span className="pv-bar-label">{label}</span>
    </button>
  );
}

// ---- the "more" menu --------------------------------------------------------

function PersonMenu({ id, anchor, list, named, canMerge, onRename, onMerge, onDelete, onClose }: {
  id: string;
  anchor: HTMLButtonElement;
  // The people shown: a new list can move the button.
  list: readonly Person[];
  named: boolean;
  canMerge: boolean;
  onRename: () => void;
  onMerge: () => void;
  onDelete: () => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number; up: boolean } | null>(null);

  // Under the button with the right edges lined up, kept inside the
  // window, or above it when there is no room below. Measured before the
  // first paint, so it never shows in the wrong place, and again when a
  // new list may have moved the button.
  useLayoutEffect(() => {
    const m = ref.current;
    if (!m) return;
    const r = anchor.getBoundingClientRect();
    const w = m.offsetWidth;
    const h = m.offsetHeight;
    const left = Math.max(8, Math.min(r.right - w, document.documentElement.clientWidth - 8 - w));
    const up = r.bottom + 4 + h > window.innerHeight - 8 && r.top - 4 - h >= 8;
    const top = up ? r.top - 4 - h : r.bottom + 4;
    setPos((p) => (p && p.top === top && p.left === left && p.up === up ? p : { top, left, up }));
  }, [anchor, list]);

  // The first item takes the focus once, so the arrows and Esc work at once.
  const focused = useRef(false);
  useEffect(() => {
    if (!pos || focused.current) return;
    focused.current = true;
    ref.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus({ preventScroll: true });
  }, [pos]);

  // A tap anywhere else only closes it: the catcher under the menu takes
  // that tap, so it never also opens the face or the name field under it.
  // A scroll or a resize closes it too.
  useEffect(() => {
    window.addEventListener("scroll", onClose, true);
    window.addEventListener("resize", onClose);
    return () => {
      window.removeEventListener("scroll", onClose, true);
      window.removeEventListener("resize", onClose);
    };
  }, [onClose]);
  // However it goes (an item, a tap elsewhere), the second click of a
  // double click lands on what was under it - a face that would open
  // Images, or take the focus from the name field Rename just opened.
  useEffect(() => () => swallowSecondClick(), []);

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    const at = items.indexOf(document.activeElement as HTMLElement);
    const go = (i: number) => {
      e.preventDefault();
      items[(i + items.length) % items.length]?.focus();
    };
    switch (e.key) {
      case "ArrowDown": go(at + 1); break;
      case "ArrowUp": go(at - 1); break;
      case "Home": go(0); break;
      case "End": go(items.length - 1); break;
      case "Escape":
      case "Tab":
        e.preventDefault();
        e.stopPropagation();
        onClose();
        anchor.focus({ preventScroll: true });
        break;
    }
  };

  return createPortal(
    <>
      {/* A right or middle press brings no click: it closes on the press. */}
      <div
        className="pv-menu-catcher"
        aria-hidden="true"
        onClick={onClose}
        onPointerDown={(e) => { if (e.button !== 0) onClose(); }}
      />
      <div
        ref={ref}
        id={id}
        role="menu"
        aria-label="Person options"
        className={`pv-menu${pos?.up ? " up" : ""}`}
        style={pos ? { top: pos.top, left: pos.left } : { top: 0, left: 0, visibility: "hidden" }}
        onKeyDown={onKeyDown}
      >
        <button type="button" role="menuitem" tabIndex={-1} className="pv-menu-item" onClick={onRename}>
          <PencilIcon />{named ? "Rename" : "Add a name"}
        </button>
        {canMerge && (
          <button type="button" role="menuitem" tabIndex={-1} className="pv-menu-item" onClick={onMerge}>
            <MergeIcon />Merge with others…
          </button>
        )}
        <button type="button" role="menuitem" tabIndex={-1} className="pv-menu-item danger" onClick={onDelete}>
          <TrashIcon />Delete
        </button>
      </div>
    </>,
    document.body,
  );
}

// ---- dialogs ----------------------------------------------------------------

// The face to keep is picked here. Preselected when the choice is plain:
// the one named person (the one of them with the most photos, when several
// share that name), or, when nobody has a name, the face with the most
// photos. People with different names need a pick: only one name can stay.
function firstKeep(list: readonly Person[]): string | null {
  const named = list.filter((p) => tidy(p.name));
  if (namesOf(named).length > 1) return null;
  const from = named.length ? named : list;
  return from.reduce((a, b) => (b.faceCount > a.faceCount ? b : a)).id;
}

function MergeDialog({ people, thumbs, busy, error, onCancel, onConfirm, onReturnFocus }: {
  // Two or more, in the page's order.
  people: readonly Person[];
  thumbs: ReadonlyMap<string, string>;
  busy: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: (keep: Person, name: string) => void;
  onReturnFocus: () => void;
}) {
  const [keepId, setKeepId] = useState<string | null>(() => firstKeep(people));
  const [typed, setTyped] = useState("");
  const groupRef = useRef<HTMLDivElement>(null);
  // Many faces scroll inside the group: a fade at an edge with more
  // behind it says so (1 = above, 2 = below).
  const [more, setMore] = useState(0);
  const measure = useCallback(() => {
    const el = groupRef.current;
    if (!el) return;
    const next = (el.scrollTop > 1 ? 1 : 0) | (el.scrollHeight - el.clientHeight - el.scrollTop > 1 ? 2 : 0);
    setMore((m) => (m === next ? m : next));
  }, []);
  useLayoutEffect(() => {
    const el = groupRef.current;
    if (!el) return;
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [measure]);
  const fieldId = useId();
  const n = people.length;
  const names = namesOf(people);
  const keep = people.find((p) => p.id === keepId) ?? null;
  // The name the merged person gets: the kept face's own, or the only name
  // among them; failing both, what is typed in the field.
  const carried = keep ? tidy(keep.name) || (names.length === 1 ? names[0] : "") : "";
  const askName = keep !== null && carried === "";
  const name = carried || tidy(typed);
  const dropped = keep ? names.filter((x) => fold(x) !== fold(name)) : [];

  // A pick can bring in the name field, and when the dialog is already as
  // tall as the window allows the faces give up the room it takes (CSS):
  // the face picked stays in view, clear of the fade at the edge (40px,
  // PeopleView.css).
  useLayoutEffect(() => {
    const g = groupRef.current;
    const el = keepId ? g?.querySelector<HTMLElement>(`[data-keep="${CSS.escape(keepId)}"]`) : null;
    if (!g || !el) return;
    const box = g.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    if (r.bottom > box.bottom) g.scrollTop += r.bottom - box.bottom + 40;
    else if (r.top < box.top) g.scrollTop -= box.top - r.top + 40;
  }, [keepId, askName]);

  // While the device merges (busy), the face and the name it was asked
  // for stay as they are - picks, arrows and typing do nothing - or the
  // dialog would describe a merge other than the one under way.
  const go = () => {
    if (busy) return;
    if (keep) onConfirm(keep, name);
    else groupRef.current?.querySelector<HTMLElement>('[role="radio"]')?.focus();
  };

  // A radio group: one stop for Tab, the arrows move the choice.
  const onGroupKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (!step) return;
    e.preventDefault();
    if (busy) return;
    const at = people.findIndex((p) => p.id === keepId);
    const next = people[(Math.max(at, step > 0 ? -1 : 0) + step + n) % n];
    setKeepId(next.id);
    groupRef.current?.querySelector<HTMLElement>(`[data-keep="${CSS.escape(next.id)}"]`)?.focus();
  };

  let note: ReactNode = null;
  if (!keep) {
    note = <>They have different names, and only one can stay. Pick the face to keep.</>;
  } else if (name && !askName) {
    note = <>The merged person will be called <strong>{name}</strong>.</>;
  }
  const droppedNote = dropped.length > 0 && (
    <> {dropped.length === 1 ? "The name" : "The names"} {andList(dropped)} will be dropped.</>
  );

  return (
    <ConfirmDialog
      wide
      title={`Merge ${n.toLocaleString()} people into one?`}
      text="Pick the face to keep: it keeps its name and picture, and gets the photos of the others. This can't be undone."
      action="Merge"
      tone="primary"
      ready={keep !== null}
      busy={busy}
      error={error}
      onCancel={onCancel}
      onConfirm={go}
      onReturnFocus={onReturnFocus}
    >
      <div
        ref={groupRef}
        className={`pv-keep${more & 1 ? " more-above" : ""}${more & 2 ? " more-below" : ""}`}
        role="radiogroup"
        aria-label="Face to keep"
        aria-disabled={busy || undefined}
        onKeyDown={onGroupKey}
        onScroll={measure}
      >
        {people.map((p, i) => {
          const chosen = p.id === keepId;
          const label = tidy(p.name);
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={chosen}
              tabIndex={chosen || (keepId === null && i === 0) ? 0 : -1}
              className={`pv-keep-opt${chosen ? " chosen" : ""}`}
              data-keep={p.id}
              onClick={() => { if (!busy) setKeepId(p.id); }}
            >
              <span className="pv-keep-face">
                <Avatar src={thumbs.get(p.id)} className="pv-avatar-keep" />
                {chosen && <span className="pv-keep-badge" aria-hidden="true"><CheckIcon size={12} />Keep</span>}
              </span>
              <span className={`pv-keep-name${label ? "" : " pv-unnamed"}`}>{label || "Unnamed"}</span>
              <span className="pv-keep-count">{photosLabel(p.faceCount)}</span>
            </button>
          );
        })}
      </div>
      {(note || droppedNote) && <p className="pv-dialog-note" aria-live="polite">{note}{droppedNote}</p>}
      {askName && (
        <div className="pv-field">
          <label htmlFor={fieldId}>Name <span>(optional)</span></label>
          <input
            id={fieldId}
            value={typed}
            readOnly={busy}
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              // Enter also ends an input method's composition: not a merge.
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault();
                go();
              }
            }}
            maxLength={cMaxName}
            placeholder="Add a name"
            autoComplete="off"
            autoCapitalize="words"
            spellCheck={false}
            enterKeyHint="done"
          />
        </div>
      )}
    </ConfirmDialog>
  );
}

// A confirmation over a dimmed page: a native modal dialog, so the page
// behind can't be reached and Esc closes it. Cancel takes the focus - the
// safe answer. When it closes the focus goes back to the button that
// opened it, if that is still there (the selection bar), else wherever
// onReturnFocus says (the person it was about).
function ConfirmDialog({
  art, title, text, children, action, tone = "danger", ready = true, wide = false,
  busy, busyText, error, onCancel, onConfirm, onReturnFocus,
}: {
  art?: ReactNode;
  title: string;
  text: string;
  // More of the question, under the text: the merge's faces and name.
  children?: ReactNode;
  action: string;
  tone?: "danger" | "primary";
  // False while the question still needs an answer: the action is dimmed.
  ready?: boolean;
  wide?: boolean;
  busy: boolean;
  // Shown by the spinner while busy (how far it has got).
  busyText?: string;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
  onReturnFocus: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const textId = useId();
  const returnFocus = useRef(onReturnFocus);
  useEffect(() => { returnFocus.current = onReturnFocus; });
  // What had the focus when it opened, read before the dialog takes it.
  const [opener] = useState(() => document.activeElement);
  // A press that began inside the card and ended on the backdrop (a text
  // selection dragged out) is no click on the backdrop.
  const downOnBackdrop = useRef(false);
  // The second press of the double-click or double-tap that opened it can
  // land on a button or the backdrop, so a press that begins in its first
  // moments answers nothing, however long it is held; the keyboard always
  // does.
  const shownAt = useRef(0);
  const pressTooSoon = useRef(false);

  // The question scrolls in its body when the window is short; the buttons
  // under it never do. A line over them says there is more below.
  const bodyRef = useRef<HTMLDivElement>(null);
  const errorRef = useRef<HTMLParagraphElement>(null);
  const [moreBelow, setMoreBelow] = useState(false);
  // Where the card's top is: centred when it opens, then left there while
  // the question grows or shrinks (the merge's name field, its note, an
  // error), so the faces under the pointer don't move. It goes up only as
  // far as it must to stay in the window; a new window size centres it.
  const topRef = useRef<number | null>(null);
  const place = useCallback(() => {
    const d = ref.current;
    const body = bodyRef.current;
    if (!d?.open || !body) return;
    const h = d.offsetHeight;
    const room = window.innerHeight;
    const top = Math.round(Math.max(cDialogEdge, Math.min(topRef.current ?? (room - h) / 2, room - cDialogEdge - h)));
    if (top !== topRef.current) {
      topRef.current = top;
      d.style.marginTop = `${top}px`;
    }
    const below = body.scrollHeight - body.clientHeight - body.scrollTop > 1;
    setMoreBelow((m) => (m === below ? m : below));
  }, []);
  // After every render: what it holds may have changed size.
  useLayoutEffect(() => place());
  useEffect(() => {
    const onResize = () => {
      topRef.current = null;
      place();
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [place]);
  // An error shows under the question, right over the buttons: scrolled to
  // when the body has scrolled away from it.
  useEffect(() => {
    const el = errorRef.current;
    const body = bodyRef.current;
    if (!error || !el || !body) return;
    const past = el.getBoundingClientRect().bottom - body.getBoundingClientRect().bottom;
    if (past > 0) body.scrollTop += past;
  }, [error]);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (!d.open) d.showModal();
    shownAt.current = performance.now();
    place();
    cancelRef.current?.focus();
    return () => {
      // However it closes - Cancel, the backdrop, Esc, or the merge or the
      // deletion done - the second click of a double click or double tap
      // would land on the face that was under it: it goes nowhere.
      swallowSecondClick();
      if (d.open) d.close();
      if (opener instanceof HTMLElement && opener !== document.body && opener.isConnected) {
        opener.focus({ preventScroll: true });
      } else {
        returnFocus.current();
      }
    };
  }, [opener, place]);

  const dismiss = () => { if (!busy) onCancel(); };

  return (
    <dialog
      ref={ref}
      className={`pv-dialog${wide ? " wide" : ""}`}
      role="alertdialog"
      aria-labelledby={titleId}
      aria-describedby={textId}
      onCancel={(e) => { e.preventDefault(); dismiss(); }}
      // The browser can still close it itself (Chrome does on a second Esc).
      // While the device is at work it comes back, so its answer has
      // somewhere to show; otherwise that counts as Cancel.
      onClose={() => {
        const d = ref.current;
        if (busy && d?.isConnected) d.showModal();
        else onCancel();
      }}
      onPointerDown={(e) => {
        pressTooSoon.current = performance.now() - shownAt.current < cArmMs;
        downOnBackdrop.current = !pressTooSoon.current && e.target === e.currentTarget;
      }}
      // A keyboard's click has no press behind it (detail 0).
      onClickCapture={(e) => { if (pressTooSoon.current && e.detail !== 0) e.stopPropagation(); }}
      onClick={(e) => { if (downOnBackdrop.current && e.target === e.currentTarget) dismiss(); }}
    >
      <div className={`pv-dialog-card${moreBelow ? " more-below" : ""}`}>
        <div ref={bodyRef} className="pv-dialog-body" onScroll={place}>
          {art}
          <h2 id={titleId} className="pv-dialog-title">{title}</h2>
          <p id={textId} className="pv-dialog-text">{text}</p>
          {children}
          {error && <p ref={errorRef} className="pv-dialog-error" role="alert">{error}</p>}
        </div>
        <div className="pv-dialog-actions">
          <button ref={cancelRef} type="button" className="pv-btn pv-btn-quiet" onClick={dismiss} aria-disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            className={`pv-btn pv-btn-${tone}`}
            onClick={() => { if (!busy) onConfirm(); }}
            aria-disabled={busy || !ready}
            aria-label={busy ? `${action}…` : undefined}
          >
            {busy ? <Spinner label={busyText} /> : action}
          </button>
        </div>
      </div>
    </dialog>
  );
}

// ---- pieces -----------------------------------------------------------------

function Avatar({ src, className }: { src: string | undefined; className: string }) {
  return (
    <span className={`pv-avatar ${className}`}>
      {src ? <img src={src} alt="" draggable={false} /> : <PersonGlyph size={24} />}
    </span>
  );
}

// The faces a deletion is about: one large, or a few overlapping with how
// many more there are.
function FaceStack({ people, thumbs }: { people: readonly Person[]; thumbs: ReadonlyMap<string, string> }) {
  if (people.length === 1) return <Avatar src={thumbs.get(people[0].id)} className="pv-avatar-lg" />;
  const shown = people.slice(0, people.length > 4 ? 3 : 4);
  const more = people.length - shown.length;
  return (
    <span className="pv-stack" aria-hidden="true">
      {shown.map((p) => <Avatar key={p.id} src={thumbs.get(p.id)} className="pv-avatar-md" />)}
      {more > 0 && <span className="pv-avatar pv-avatar-md pv-stack-more">+{more.toLocaleString()}</span>}
    </span>
  );
}

// The list is asked for again on every visit (new faces are found as
// photos arrive), and what the store already holds shows meanwhile. When
// nothing is loaded yet the store's own hook is listing it already, and
// asking again would list it twice, so the page waits on that listing and
// its failure shows at once. A listing that failed, or a first one that is
// slow, gets a "Try again".
function useFreshList(loaded: boolean, reload: Reload) {
  const [asked, setAsked] = useState<"no" | "running" | "done">("no");
  const [slow, setSlow] = useState(false);
  const wait = useCallback((listing: () => Promise<void>) => {
    setAsked("running");
    setSlow(false);
    listing().catch(() => {}).finally(() => setAsked("done"));
  }, []);
  const retry = useCallback(() => wait(reload), [wait, reload]);
  const loadedAtMount = useRef(loaded);
  useEffect(() => { wait(loadedAtMount.current ? reload : reload.join); }, [wait, reload]);
  useEffect(() => {
    if (loaded || asked === "done") return;
    const t = window.setTimeout(() => setSlow(true), cSlowMs);
    return () => window.clearTimeout(t);
  }, [loaded, asked]);
  return { failed: !loaded && asked === "done", slow: !loaded && slow, retry };
}

// Icons in the style of NavIcons.tsx: 24px box, round 1.6 strokes.
function Icon({ size = 20, strokeWidth = 1.6, children }: { size?: number; strokeWidth?: number; children: ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={strokeWidth}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}

type IconProps = { size?: number };

const MoreIcon = () => (
  <Icon><g fill="currentColor" stroke="none"><circle cx="6" cy="12" r="1.6" /><circle cx="12" cy="12" r="1.6" /><circle cx="18" cy="12" r="1.6" /></g></Icon>
);
const PencilIcon = () => (
  <Icon><path d="M4 20h4L19 9a2.83 2.83 0 0 0-4-4L4 16v4Z" /><path d="m13.5 6.5 4 4" /></Icon>
);
// Two lines joining into one.
const MergeIcon = ({ size }: IconProps) => (
  <Icon size={size}><path d="M6 20v-3.5a4 4 0 0 1 1.17-2.83L12 9" /><path d="M18 20v-3.5a4 4 0 0 0-1.17-2.83L12 9V4" /><path d="m8.5 7.5 3.5-3.5 3.5 3.5" /></Icon>
);
const TrashIcon = ({ size }: IconProps) => (
  <Icon size={size}><path d="M4.5 7h15" /><path d="M9.5 7V5.5A1.5 1.5 0 0 1 11 4h2a1.5 1.5 0 0 1 1.5 1.5V7" /><path d="m6.5 7 .8 11.2A2 2 0 0 0 9.3 20h5.4a2 2 0 0 0 2-1.8L17.5 7" /><path d="M10 11v5M14 11v5" /></Icon>
);
const CloseIcon = () => <Icon size={24}><path d="M6.5 6.5l11 11M17.5 6.5l-11 11" /></Icon>;
// Heavier, to read at 12-16px on a filled circle.
const CheckIcon = ({ size }: IconProps) => <Icon size={size} strokeWidth={2.6}><path d="m5.5 12.5 4 4 9-9" /></Icon>;
// A head and shoulders, for a face without a picture. Thinner as it grows,
// so the stroke stays about as heavy as the icons'.
const PersonGlyph = ({ size }: { size: number }) => (
  <Icon size={size} strokeWidth={size > 30 ? 1.1 : 1.6}><circle cx="12" cy="9" r="3.5" /><path d="M5 20c.9-3.6 3.7-5.5 7-5.5s6.1 1.9 7 5.5" /></Icon>
);

function ProblemIcon() {
  return (
    <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.4"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7.5V13M12 16.5h.01" />
    </svg>
  );
}

// A face in a viewfinder, with a gold sparkle: faces the device finds.
function EmptyArt() {
  return (
    <svg width="80" height="80" viewBox="0 0 80 80" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14 26v-6a6 6 0 0 1 6-6h6M54 14h6a6 6 0 0 1 6 6v6M66 54v6a6 6 0 0 1-6 6h-6M26 66h-6a6 6 0 0 1-6-6v-6" />
      <circle className="pv-art-head" cx="40" cy="35" r="9" />
      <path d="M24 60c2.6-8 8.6-12.5 16-12.5S53.4 52 56 60" />
      <path className="pv-art-spark" d="M56 19l1.4 3.6L61 24l-3.6 1.4L56 29l-1.4-3.6L51 24l3.6-1.4Z" />
      <path className="pv-art-spark" d="M62 29.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9Z" />
    </svg>
  );
}
