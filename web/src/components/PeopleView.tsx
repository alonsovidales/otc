// SPDX-License-Identifier: AGPL-3.0-or-later

import { memo, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import { useWS } from "../net/useWS";
import type { Person, ReqEnvelope, RespEnvelope } from "../proto/messages";
import { reloadPeople, renamePersonLocally, usePeople } from "./libraryStore";
import { forgetPerson, showPerson } from "./photoFilter";
import Spinner from "./Spinner";
import "./PeopleView.css";

// The People page: the faces the device found, named people first, each a
// circle that opens Images on that person. A person's "more" menu names
// them, merges them into someone else (face matching split one person in
// two) or deletes them.

type Props = {
  // A person was opened (photoFilter.showPerson already called): show Images.
  onOpenPhotos: () => void;
};

type ReqPayload = NonNullable<ReqEnvelope["payload"]>;

type Confirm =
  | { kind: "merge"; source: Person; target: Person }
  | { kind: "delete"; person: Person };

type FocusPart = "face" | "more";

const cSkeletonFaces = 14;
// How long the first listing may take before the page offers to ask again.
const cSlowMs = 10_000;
const cToastMs = 3200;
// people.name is a varchar(150).
const cMaxName = 100;

const photosLabel = (n: number) => `${n.toLocaleString()} ${n === 1 ? "photo" : "photos"}`;
const tidy = (s: string) => s.replace(/\s+/g, " ").trim();

// A request the device answers with an Ack: true once it is done.
async function ack(payload: ReqPayload): Promise<boolean> {
  const resp: RespEnvelope = await useWS.request((e) => { e.payload = payload; });
  return resp.payload?.$case === "respAck" && resp.payload.respAck.ok;
}

export default function PeopleView({ onOpenPhotos }: Props) {
  const { items, thumbs, loaded } = usePeople();
  const { failed, slow, retry } = useFreshList(loaded, reloadPeople);
  const rootRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const menuId = useId();

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
  const [mergeFromId, setMergeFromId] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  const [toast, setToast] = useState<{ text: string; at: number } | null>(null);

  // Named people first, each part in the order the device lists them.
  const people = useMemo(() => {
    const shown = items.filter((p) => !gone.has(p.id));
    return [...shown.filter((p) => p.name.trim() !== ""), ...shown.filter((p) => p.name.trim() === "")];
  }, [items, gone]);
  const mergeFrom = mergeFromId ? people.find((p) => p.id === mergeFromId) ?? null : null;
  const menuPerson = menu ? people.find((p) => p.id === menu.id) ?? null : null;

  // Focus moves once the render that needs it is on screen: a button that
  // pick mode hides, the face a dialog was about, or the title when that
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

  const say = useCallback((text: string) => setToast({ text, at: Date.now() }), []);
  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), cToastMs);
    return () => window.clearTimeout(t);
  }, [toast]);

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
    else say("The name couldn't be saved. Try again.");
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

  // ---- merging and deleting -------------------------------------------------

  const startMerge = (p: Person) => {
    setMenu(null);
    setMergeFromId(p.id);
  };

  const cancelMerge = useCallback(() => {
    if (mergeFromId) focusLater(mergeFromId, "more");
    setMergeFromId(null);
  }, [mergeFromId, focusLater]);

  // Esc leaves pick mode (a dialog handles its own).
  useEffect(() => {
    if (!mergeFromId || confirm) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") cancelMerge(); };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [mergeFromId, confirm, cancelMerge]);

  const pickCancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (mergeFromId) pickCancelRef.current?.focus({ preventScroll: true });
  }, [mergeFromId]);

  const onFace = useCallback((p: Person) => {
    if (!mergeFromId) {
      showPerson(p.id);
      openPhotos.current();
      return;
    }
    if (p.id === mergeFromId) { cancelMerge(); return; }
    const source = items.find((x) => x.id === mergeFromId);
    if (!source) { setMergeFromId(null); return; }
    setConfirmError(null);
    setConfirm({ kind: "merge", source, target: p });
  }, [mergeFromId, items, cancelMerge]);

  const toggleMenu = useCallback((p: Person, anchor: HTMLButtonElement) => {
    setMenu((m) => (m?.id === p.id ? null : { id: p.id, anchor }));
  }, []);
  const closeMenu = useCallback(() => setMenu(null), []);

  // The list comes again in the background: the dialog closes as soon as
  // the device has done it, and the person is hidden meanwhile.
  const afterRemoval = (id: string) => {
    forgetPerson(id);
    setGone((s) => new Set(s).add(id));
    setConfirm(null);
  };

  const merge = async (source: Person, target: Person) => {
    setBusy(true);
    setConfirmError(null);
    try {
      if (!(await ack({ $case: "reqMergePeople", reqMergePeople: { targetId: target.id, sourceIds: [source.id] } }))) {
        setConfirmError("They couldn't be merged. Try again.");
        return;
      }
      afterRemoval(source.id);
      setMergeFromId(null);
      const kept = tidy(target.name) || tidy(source.name);
      say(kept ? `Merged into ${kept}` : "Merged");
      void (async () => {
        // The merged person keeps the target's name, so a name only the
        // source had would be lost: it moves over.
        if (!tidy(target.name) && kept) {
          const ok = await ack({ $case: "reqRenamePerson", reqRenamePerson: { id: target.id, name: kept } }).catch(() => false);
          if (ok) renamePersonLocally(target.id, kept);
        }
        await reloadPeople().catch(() => {});
      })();
    } catch {
      setConfirmError("Your device didn't answer. Try again.");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (p: Person) => {
    setBusy(true);
    setConfirmError(null);
    try {
      if (!(await ack({ $case: "reqDeletePerson", reqDeletePerson: { id: p.id } }))) {
        setConfirmError("This person couldn't be deleted. Try again.");
        return;
      }
      afterRemoval(p.id);
      say("Person deleted");
      void reloadPeople().catch(() => {});
    } catch {
      setConfirmError("Your device didn't answer. Try again.");
    } finally {
      setBusy(false);
    }
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
      <ul className="pv-grid" role="list">
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
              picking={mergeFrom !== null}
              isSource={mergeFrom?.id === p.id}
              menuOpen={menu?.id === p.id}
              menuId={menuId}
              onFace={onFace}
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

  const fromName = mergeFrom ? tidy(mergeFrom.name) : "";

  return (
    <div ref={rootRef} className={`pv-root${mergeFrom ? " picking" : ""}`}>
      {/* Picking whom to merge into: a mode, so it takes the top bar's
          place like a selection does, and nothing on the page moves. */}
      {mergeFrom && (
        <div className="pv-pickbar">
          <Avatar src={thumbs.get(mergeFrom.id)} className="pv-avatar-sm" />
          <p className="pv-pickbar-text" role="status">
            Pick who {fromName ? <strong>{fromName}</strong> : "this person"} should be merged into
          </p>
          <button ref={pickCancelRef} type="button" className="pv-btn pv-btn-quiet" onClick={cancelMerge}>
            Cancel
          </button>
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
          onMerge={() => startMerge(menuPerson)}
          onDelete={() => {
            setMenu(null);
            setConfirmError(null);
            setConfirm({ kind: "delete", person: menuPerson });
          }}
          onClose={closeMenu}
        />
      )}

      {/* Keyed by what they ask about: another question is a new dialog. */}
      {confirm?.kind === "merge" && (
        <MergeDialog
          key={`merge:${confirm.source.id}:${confirm.target.id}`}
          source={confirm.source}
          target={confirm.target}
          sourceThumb={thumbs.get(confirm.source.id)}
          targetThumb={thumbs.get(confirm.target.id)}
          busy={busy}
          error={confirmError}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void merge(confirm.source, confirm.target)}
          onReturnFocus={() => focusLater(confirm.target.id, "face")}
        />
      )}
      {confirm?.kind === "delete" && (
        <ConfirmDialog
          key={`delete:${confirm.person.id}`}
          art={<Avatar src={thumbs.get(confirm.person.id)} className="pv-avatar-lg" />}
          title="Delete this person?"
          text="This removes every face matched to them — it can't be undone. The photos themselves are kept."
          action="Delete"
          busy={busy}
          error={confirmError}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void remove(confirm.person)}
          onReturnFocus={() => focusLater(confirm.person.id, "more")}
        />
      )}

      <div className="pv-toast-host" aria-live="polite">
        {toast && <div key={toast.at} className="pv-toast">{toast.text}</div>}
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
  // Choosing whom to merge someone into, and whether this is that someone.
  picking: boolean;
  isSource: boolean;
  menuOpen: boolean;
  menuId: string;
  onFace: (p: Person) => void;
  onMore: (p: Person, anchor: HTMLButtonElement) => void;
  onStartRename: (p: Person) => void;
  onEndRename: (p: Person, typed: string | null, fromBlur: boolean) => void;
};

// Memoised, with the page's callbacks kept stable: a menu, a toast or a
// name being saved re-renders one tile, not every face in the library.
const PersonTile = memo(function PersonTile(t: TileProps) {
  const { person, name, picking, isSource } = t;
  const who = name || "this person";
  const count = photosLabel(person.faceCount);
  const faceLabel = !picking
    ? `${name || "Unnamed person"}, ${count}`
    : isSource ? `Stop merging ${who}` : `Merge into ${who}`;

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
  } else if (picking) {
    label = <span className="pv-name pv-unnamed">Unnamed</span>;
  } else {
    label = (
      <button type="button" className="pv-addname" onClick={() => t.onStartRename(person)}>
        Add a name
      </button>
    );
  }

  return (
    <li className={`pv-person${isSource ? " is-source" : ""}`} data-person-id={person.id}>
      <div className="pv-face-wrap">
        <button
          type="button"
          className="pv-face"
          onClick={() => t.onFace(person)}
          aria-label={faceLabel}
          data-tip={picking ? (isSource ? "Stop merging" : "Merge into this person") : undefined}
        >
          {t.thumb
            ? <img src={t.thumb} alt="" draggable={false} decoding="async" />
            : <span className="pv-face-ph"><PersonGlyph size={44} /></span>}
        </button>
        {!picking && (
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
      <span className="pv-count">{count}</span>
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

  // A press anywhere else, a scroll or a resize closes it. The button's
  // own press is left to the button, which toggles it.
  useEffect(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor.contains(t)) return;
      onClose();
    };
    document.addEventListener("pointerdown", down, true);
    window.addEventListener("scroll", onClose, true);
    window.addEventListener("resize", onClose);
    return () => {
      document.removeEventListener("pointerdown", down, true);
      window.removeEventListener("scroll", onClose, true);
      window.removeEventListener("resize", onClose);
    };
  }, [anchor, onClose]);

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
          <MergeIcon />Merge with…
        </button>
      )}
      <button type="button" role="menuitem" tabIndex={-1} className="pv-menu-item danger" onClick={onDelete}>
        <TrashIcon />Delete
      </button>
    </div>,
    document.body,
  );
}

// ---- dialogs ----------------------------------------------------------------

function MergeDialog({ source, target, sourceThumb, targetThumb, ...rest }: {
  source: Person;
  target: Person;
  sourceThumb: string | undefined;
  targetThumb: string | undefined;
  busy: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
  onReturnFocus: () => void;
}) {
  const a = tidy(source.name);
  const b = tidy(target.name);
  // Most faces have no name yet: then the two pictures say who is who.
  let title: string;
  let text: string;
  if (a && b) {
    title = `Merge ${a} into ${b}?`;
    text = `Every photo of ${a} will show under ${b}. This can't be undone.`;
  } else if (b) {
    title = `Merge this person into ${b}?`;
    text = `Every photo of the person on the left will show under ${b}. This can't be undone.`;
  } else if (a) {
    title = "Merge these two people?";
    text = `They will become one person, ${a}, with every photo of both. This can't be undone.`;
  } else {
    title = "Merge these two people?";
    text = "Every photo of the person on the left will show under the person on the right. This can't be undone.";
  }
  return (
    <ConfirmDialog
      art={
        <div className="pv-dialog-faces" aria-hidden="true">
          <span className="pv-dialog-person">
            <Avatar src={sourceThumb} className="pv-avatar-lg" />
            <span>{a || "Unnamed"}</span>
          </span>
          <span className="pv-dialog-arrow"><ArrowIcon /></span>
          <span className="pv-dialog-person">
            <Avatar src={targetThumb} className="pv-avatar-lg" />
            <span>{b || "Unnamed"}</span>
          </span>
        </div>
      }
      title={title}
      text={text}
      action="Merge"
      {...rest}
    />
  );
}

// A confirmation over a dimmed page: a native modal dialog, so the page
// behind can't be reached and Esc closes it. Cancel takes the focus - the
// safe answer - and it goes back to the person when the dialog closes.
function ConfirmDialog({ art, title, text, action, busy, error, onCancel, onConfirm, onReturnFocus }: {
  art?: ReactNode;
  title: string;
  text: string;
  action: string;
  busy: boolean;
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
  // A press that began inside the card and ended on the backdrop (a text
  // selection dragged out) is no click on the backdrop.
  const downOnBackdrop = useRef(false);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (!d.open) d.showModal();
    cancelRef.current?.focus();
    return () => {
      if (d.open) d.close();
      returnFocus.current();
    };
  }, []);

  const dismiss = () => { if (!busy) onCancel(); };

  return (
    <dialog
      ref={ref}
      className="pv-dialog"
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
      onPointerDown={(e) => { downOnBackdrop.current = e.target === e.currentTarget; }}
      onClick={(e) => { if (downOnBackdrop.current && e.target === e.currentTarget) dismiss(); }}
    >
      <div className="pv-dialog-card">
        {art}
        <h2 id={titleId} className="pv-dialog-title">{title}</h2>
        <p id={textId} className="pv-dialog-text">{text}</p>
        {error && <p className="pv-dialog-error" role="alert">{error}</p>}
        <div className="pv-dialog-actions">
          <button ref={cancelRef} type="button" className="pv-btn pv-btn-quiet" onClick={dismiss} aria-disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            className="pv-btn pv-btn-danger"
            onClick={() => { if (!busy) onConfirm(); }}
            aria-disabled={busy}
            aria-label={busy ? `${action}…` : undefined}
          >
            {busy ? <Spinner /> : action}
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

// The list is asked for again on every visit (new faces are found as
// photos arrive), and what the store already holds shows meanwhile. When
// nothing is loaded yet the store's own hook is listing it already, and
// asking again would list it twice. A listing that failed, or a first one
// that is slow, gets a "Try again".
function useFreshList(loaded: boolean, reload: () => Promise<void>) {
  const [asked, setAsked] = useState<"no" | "running" | "done">("no");
  const [slow, setSlow] = useState(false);
  const retry = useCallback(() => {
    setAsked("running");
    setSlow(false);
    reload().catch(() => {}).finally(() => setAsked("done"));
  }, [reload]);
  const loadedAtMount = useRef(loaded);
  useEffect(() => { if (loadedAtMount.current) retry(); }, [retry]);
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

const MoreIcon = () => (
  <Icon><g fill="currentColor" stroke="none"><circle cx="6" cy="12" r="1.6" /><circle cx="12" cy="12" r="1.6" /><circle cx="18" cy="12" r="1.6" /></g></Icon>
);
const PencilIcon = () => (
  <Icon><path d="M4 20h4L19 9a2.83 2.83 0 0 0-4-4L4 16v4Z" /><path d="m13.5 6.5 4 4" /></Icon>
);
// Two lines joining into one.
const MergeIcon = () => (
  <Icon><path d="M6 20v-3.5a4 4 0 0 1 1.17-2.83L12 9" /><path d="M18 20v-3.5a4 4 0 0 0-1.17-2.83L12 9V4" /><path d="m8.5 7.5 3.5-3.5 3.5 3.5" /></Icon>
);
const TrashIcon = () => (
  <Icon><path d="M4.5 7h15" /><path d="M9.5 7V5.5A1.5 1.5 0 0 1 11 4h2a1.5 1.5 0 0 1 1.5 1.5V7" /><path d="m6.5 7 .8 11.2A2 2 0 0 0 9.3 20h5.4a2 2 0 0 0 2-1.8L17.5 7" /><path d="M10 11v5M14 11v5" /></Icon>
);
const ArrowIcon = () => (
  <Icon size={24}><path d="M5 12h14M13 6l6 6-6 6" /></Icon>
);
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
