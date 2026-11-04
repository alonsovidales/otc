// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A collection's Rating on screen (rating.ts): the five stars and the
// comment box of a viewer (Images' MediaViewer in a collection with Rating
// on, and a shared gallery's own viewer for its visitors), the Rate button
// that opens them on a phone (useRatingSheet.ts), the badge on a rated
// photo's tile, and the Show and Sort by menus over a rated grid.
//
// The stars are toggle buttons in a group: the photo's own is pressed, a
// tap on another sets that many, a tap on the pressed one takes the stars
// away. One Tab stop for the five (the pressed one, else the first); the
// arrow keys, Home and End move between them - and only between them: the
// viewer's arrows don't page from there. Stars save at once, with the
// comment as it stands in the box; the comment saves with Save (or
// Ctrl/Cmd+Enter), and by itself when the viewer moves on or closes with
// text not saved yet.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useT } from "../i18n";
import {
  cMaxComment, cMaxStars, cleanComment, nextStars, parseShow, ratingShows, ratingSorts,
  type RatingRefusal, type RatingShow, type RatingSort, type RatingValue, type RatingView, type Ratings, type ShownRating,
} from "./rating";
import { showLabel, sortLabel } from "./ratingText";
import { SelectField, SortBy } from "./SortBy";
import "./Rating.css";

/** A star: an outline, or filled. */
export function StarGlyph({ filled = false, size = 20 }: { filled?: boolean; size?: number }) {
  return (
    <svg className={"rt-glyph" + (filled ? " filled" : "")} width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M12 3.6l2.55 5.17 5.7.83-4.13 4.02.98 5.68L12 16.62l-5.1 2.68.98-5.68L3.75 9.6l5.7-.83L12 3.6Z" strokeWidth="1.7" strokeLinejoin="round" />
    </svg>
  );
}

/** A speech bubble: the photo has a comment. */
export function CommentGlyph({ size = 12 }: { size?: number }) {
  return (
    <svg className="rt-glyph filled" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M5 4.5h14A2.5 2.5 0 0 1 21.5 7v8a2.5 2.5 0 0 1-2.5 2.5h-6.6L7.5 21v-3.5H5A2.5 2.5 0 0 1 2.5 15V7A2.5 2.5 0 0 1 5 4.5Z" />
    </svg>
  );
}

/** The five stars: `value` pressed (0: none), onPick with the star tapped. */
export function RatingStars({ value, onPick, autoFocus = false }: {
  value: number;
  onPick: (star: number) => void;
  /** Focus the group's stop as it shows (a sheet just opened). */
  autoFocus?: boolean;
}) {
  const t = useT();
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  // Under a mouse: the stars up to it light, to show what a click gives -
  // not after the click (taking the stars away would look undone), and
  // never for a finger, which leaves no pointer behind.
  const [hover, setHover] = useState(0);
  // The group's one Tab stop: the star with the focus, else the pressed one.
  const [focused, setFocused] = useState<number | null>(null);
  const stop = focused ?? (value > 0 ? value - 1 : 0);
  useEffect(() => {
    if (autoFocus) refs.current[value > 0 ? value - 1 : 0]?.focus({ preventScroll: true });
    // Only as it shows.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const at = refs.current.indexOf(document.activeElement as HTMLButtonElement);
    if (at < 0) return;
    let next: number;
    if (e.key === "ArrowRight" || e.key === "ArrowUp") next = Math.min(cMaxStars - 1, at + 1);
    else if (e.key === "ArrowLeft" || e.key === "ArrowDown") next = Math.max(0, at - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = cMaxStars - 1;
    else return;
    // The viewer's arrows page; here they move between the stars.
    e.preventDefault();
    e.stopPropagation();
    refs.current[next]?.focus();
  };
  const lit = hover || value;
  return (
    <div className="rt-stars" role="group" aria-label={t("web.images.rating.title")} onKeyDown={onKeyDown} onPointerLeave={() => setHover(0)}>
      {Array.from({ length: cMaxStars }, (_, i) => {
        const n = i + 1;
        const label = t("web.images.rating.stars", { count: n });
        return (
          <button
            key={n}
            ref={(el) => { refs.current[i] = el; }}
            type="button"
            className={"rt-star" + (n <= lit ? " lit" : "")}
            aria-pressed={value === n}
            aria-label={label}
            data-tip={label}
            tabIndex={i === stop ? 0 : -1}
            onFocus={() => setFocused(i)}
            onBlur={() => setFocused(null)}
            onPointerEnter={(e) => { if (e.pointerType === "mouse") setHover(n); }}
            onClick={() => {
              setHover(0);
              onPick(n);
            }}
          >
            <StarGlyph filled={n <= lit} size={22} />
          </button>
        );
      })}
    </div>
  );
}

/** One photo's stars and comment, in a viewer. */
export function RatingPanel({ ratingKey, ratings, listed, fromLinkNote = false, sheet = false, autoFocus = false, id, onRefused, beforeChange, dropped }: {
  /** The photo's key in `ratings` (its path, an item's index). */
  ratingKey: string;
  ratings: Ratings;
  /** What the listing says of it, until a change. */
  listed: ShownRating;
  /** Say when the last change came through the shared link (the owner's
   *  viewer; a gallery's visitors never see who else rated). */
  fromLinkNote?: boolean;
  /** Laid out as a phone's sheet: the stars and Save over the box. */
  sheet?: boolean;
  autoFocus?: boolean;
  id?: string;
  /** A change the device didn't take (it is back as it was). Called after
   *  the panel has gone too: a comment saved as the viewer moved on. */
  onRefused: (r: RatingRefusal) => void;
  /** Asked before any change; true stops it (a demo device). */
  beforeChange?: () => boolean;
  /** Whether the photo has left the collection meanwhile (deleted, Not
   *  this person): its unsaved comment goes with it, not saved as the
   *  panel goes - the device refused it, and a "rating refused" note
   *  followed a delete that worked. */
  dropped?: () => boolean;
}) {
  const t = useT();
  useSyncExternalStore(ratings.subscribe, ratings.version);
  const shown = ratings.get(ratingKey) ?? listed;
  const busy = ratings.busy(ratingKey);

  // The box's text. It follows what shows (a change the device turned
  // down, its own trimming) while it holds nothing new of its own.
  const [draft, setDraft] = useState(shown.comment);
  const seenRef = useRef(shown.comment);
  useEffect(() => {
    const was = seenRef.current;
    if (was === shown.comment) return;
    seenRef.current = shown.comment;
    setDraft((d) => {
      const c = cleanComment(d);
      return c === was || c === shown.comment ? shown.comment : d;
    });
  }, [shown.comment]);
  const clean = cleanComment(draft);
  const dirty = clean !== shown.comment;

  // The latest of everything, for a change that lands after a render and
  // for the save as the panel goes.
  const latest = useRef({ draft, shown, listed, ratingKey, onRefused, beforeChange, dropped });
  useLayoutEffect(() => { latest.current = { draft, shown, listed, ratingKey, onRefused, beforeChange, dropped }; });
  const commit = useCallback((want: RatingValue) => {
    const now = latest.current;
    if (now.beforeChange?.()) return;
    void ratings.set(now.ratingKey, want, now.listed).then((refused) => {
      if (refused) latest.current.onRefused(refused);
    });
  }, [ratings]);
  // Moving on to another photo, or closing the viewer, with text not saved
  // yet saves it, with the stars the photo has.
  useEffect(() => () => {
    const { draft: text, shown: now, dropped: gone } = latest.current;
    if (gone?.()) return;
    const c = cleanComment(text);
    if (c !== now.comment) commit({ stars: now.stars, comment: c });
  }, [commit]);

  const pick = (star: number) => commit({ stars: nextStars(shown.stars, star), comment: clean });
  const save = () => {
    if (dirty) commit({ stars: shown.stars, comment: clean });
  };

  return (
    <div id={id} className={"rt-panel" + (sheet ? " sheet" : "")} aria-busy={busy || undefined}>
      <div className="rt-head">
        <RatingStars value={shown.stars} onPick={pick} autoFocus={autoFocus} />
        {fromLinkNote && shown.fromLink && <p className="rt-note">{t("web.images.rating.from_link")}</p>}
      </div>
      <textarea
        className="rt-comment"
        value={draft}
        maxLength={cMaxComment}
        rows={2}
        placeholder={t("web.images.rating.comment")}
        aria-label={t("web.images.rating.comment")}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            save();
          }
        }}
      />
      <button type="button" className="rt-save" disabled={!dirty} onClick={save} aria-keyshortcuts="Control+Enter Meta+Enter">
        {t("common.save")}
      </button>
    </div>
  );
}

/** A phone's way to the stars and comment: Rate, or the photo's stars. */
export function RateButton({ stars, open, controls, onClick }: {
  stars: number;
  open: boolean;
  /** The sheet's id. */
  controls: string;
  onClick: () => void;
}) {
  const t = useT();
  const word = t("web.images.rating.rate");
  return (
    <button
      type="button"
      className={"rt-rate" + (open ? " on" : "")}
      aria-expanded={open}
      aria-controls={controls}
      aria-label={stars > 0 ? t("web.images.rating.rate_stars", { count: stars }) : word}
      onClick={onClick}
    >
      <StarGlyph filled={stars > 0} size={16} />
      <span>{stars > 0 ? stars : word}</span>
    </button>
  );
}

/** A rated photo's tile: its stars ("★ 4") and a bubble for a comment;
 *  nothing for one without either. What it says is the tile's description
 *  (ratingDescription), so it is hidden here. */
export function RatingBadge({ stars, commented }: { stars: number; commented: boolean }) {
  if (stars <= 0 && !commented) return null;
  return (
    <span className="rt-badge" aria-hidden="true">
      {stars > 0 && (
        <span className="rt-badge-stars">
          <StarGlyph filled size={12} />
          {stars}
        </span>
      )}
      {commented && <CommentGlyph size={12} />}
    </span>
  );
}

/** Show and Sort by over a rated grid; Show away from All photos looks
 *  pressed, so a narrowed grid never passes for the whole collection. */
export function RatingTools({ view, onChange }: { view: RatingView; onChange: (view: RatingView) => void }) {
  const t = useT();
  return (
    <div className="rt-tools">
      <label className={"sortby rt-show" + (view.show !== "all" ? " active" : "")}>
        <span className="sortby-label">{t("web.images.rating.show")}</span>
        <SelectField value={view.show} onChange={(e) => onChange({ ...view, show: parseShow(e.target.value) })}>
          {ratingShows.map((s: RatingShow) => <option key={s} value={s}>{showLabel(t, s)}</option>)}
        </SelectField>
      </label>
      <SortBy<RatingSort>
        value={view.sort}
        options={ratingSorts.map((s) => ({ value: s, label: sortLabel(t, s) }))}
        onChange={(sort) => onChange({ ...view, sort })}
      />
    </div>
  );
}
