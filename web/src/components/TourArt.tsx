// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The first-run tour's drawings (Tour.tsx shows one in each step's card):
// a small moving mock of the controls the step teaches - the search typed
// into, the dates dragged, photos selected and the bar sliding in, Files
// switching views and sorting, a post liked, one composed and published,
// a friend added - so the tour teaches with nothing in the library yet.
// Plain React and CSS (TourArt.css): every label is the real control's
// own key, so it reads in the language the app shows; colours are the
// theme's variables; "photos" are gradient tiles, faces a plain outline,
// never a real person. Each scene is a few states a timer steps through
// (useScene), CSS moving between them; with reduced motion one still
// state shows instead. Decorative for screen readers: the step's text
// says it all. Loaded with the tour, never in the app's first chunk.

import type { CSSProperties, ReactNode } from "react";
import { Trans, fmtBytes, fmtDate, monthName, useT } from "../i18n";
import { FRIEND_TLD } from "../net/friendDomain";
import { AlertsIcon, CollectionsIcon, FilesIcon, FriendsIcon, ImagesIcon, MenuIcon, PeopleIcon, SettingsIcon, SocialIcon } from "./NavIcons";
import type { TourStepId } from "./tourSteps";
import { useCoarsePointer, useScene } from "./tourMotion";
import "./TourArt.css";

const cls = (...names: (string | false | null | undefined)[]) => names.filter(Boolean).join(" ");
const pct = (v: number) => `${v}%`;

// Outline icons drawn like NavIcons' (24px box, stroke in currentColor).
function Svg({ size = 14, stroke = 1.8, children }: { size?: number; stroke?: number; children: ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}
const GlassIcon = ({ size }: { size?: number }) => <Svg size={size}><circle cx="10.5" cy="10.5" r="6" /><path d="m15 15 5 5" /></Svg>;
const TagIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M3.5 12V5.5a2 2 0 0 1 2-2H12a2 2 0 0 1 1.4.6l6.9 6.9a2 2 0 0 1 0 2.8l-6.6 6.6a2 2 0 0 1-2.8 0l-6.9-6.9A2 2 0 0 1 3.5 12Z" /><circle cx="8.5" cy="8.5" r="1.4" /></Svg>
);
const PhotoIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><rect x="3.5" y="4.5" width="17" height="15" rx="2.5" /><circle cx="9" cy="10" r="1.6" /><path d="m4 17 4.5-4.5 3.5 3.5 2.5-2.5L20 18" /></Svg>
);
const DocIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M6.5 3.5h7l4 4v11a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2Z" /><path d="M13.5 3.5v4h4M8.5 12.5h7M8.5 16h5" /></Svg>
);
const FolderIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-10Z" /></Svg>
);
const DocSearchIcon = ({ size }: { size?: number }) => (
  <Svg size={size}>
    <path d="M10.5 20.5h-4a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2h7l4 4v3" />
    <path d="M13.5 3.5v4h4M8 11h4M8 14.5h2" />
    <circle cx="16" cy="16" r="3.2" />
    <path d="m18.4 18.4 2.6 2.6" />
  </Svg>
);
const CloseIcon = ({ size }: { size?: number }) => <Svg size={size}><path d="M6.5 6.5l11 11M17.5 6.5l-11 11" /></Svg>;
const CheckIcon = ({ size }: { size?: number }) => <Svg size={size} stroke={3}><path d="m5.5 12.5 4 4 9-9" /></Svg>;
const PostIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M20.5 3.5 10.75 13.25" /><path d="M20.5 3.5 14.25 20.5l-3.5-7.25L3.5 9.75 20.5 3.5Z" /></Svg>
);
const AddToCollectionIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M7.5 4.5h10a3 3 0 0 1 3 3v10" /><rect x="3.5" y="7.5" width="13" height="13" rx="2" /><path d="M10 11v6M7 14h6" /></Svg>
);
const ShareIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><circle cx="17.5" cy="5.5" r="2.5" /><circle cx="6.5" cy="12" r="2.5" /><circle cx="17.5" cy="18.5" r="2.5" /><path d="m8.7 10.7 6.6-3.9M8.7 13.3l6.6 3.9" /></Svg>
);
const TrashIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M4.5 7h15" /><path d="M9.5 7V5.5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1V7" /><path d="M6.5 7l.85 12.1a1.5 1.5 0 0 0 1.5 1.4h6.3a1.5 1.5 0 0 0 1.5-1.4L17.5 7" /><path d="M10 11v5.5M14 11v5.5" /></Svg>
);
const PencilIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M4.5 19.5h3.75L18.6 9.15a2.65 2.65 0 0 0-3.75-3.75L4.5 15.75v3.75Z" /><path d="m13.5 6.75 3.75 3.75" /></Svg>
);
const LockIcon = ({ size, locked }: { size?: number; locked: boolean }) => (
  <Svg size={size}>
    <rect x="5" y="11" width="14" height="9.5" rx="2" />
    <path d={locked ? "M8 11V8a4 4 0 0 1 8 0v3" : "M8 11V8a4 4 0 0 1 7.6-1.7"} />
  </Svg>
);
const HistoryIcon = ({ size }: { size?: number }) => (
  <Svg size={size}><path d="M4 12a8 8 0 1 0 2.34-5.66" /><path d="M4 4v5h5" /><path d="M12 8v4l3 2" /></Svg>
);
// The feed's heart: an outline, filled once liked.
const Heart = ({ on }: { on: boolean }) => (
  <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" className={cls("ta-heart", on && "on")}>
    <path d="M12 20.5s-7.5-4.6-9.2-9.3C1.6 7.8 3.9 4.5 7.3 4.5c2 0 3.4 1.1 4.7 2.8 1.3-1.7 2.7-2.8 4.7-2.8 3.4 0 5.7 3.3 4.5 6.7-1.7 4.7-9.2 9.3-9.2 9.3Z" />
  </svg>
);
// The three views of Files (FilesExplorer's own drawings).
const ListViewIcon = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true">
    <path d="M8 6h13M8 12h13M8 18h13" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    <circle cx="4" cy="6" r="1.5" fill="currentColor" /><circle cx="4" cy="12" r="1.5" fill="currentColor" /><circle cx="4" cy="18" r="1.5" fill="currentColor" />
  </svg>
);
const GridViewIcon = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true">
    <rect x="3" y="3" width="7.5" height="7.5" rx="1.5" fill="currentColor" /><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5" fill="currentColor" />
    <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5" fill="currentColor" /><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5" fill="currentColor" />
  </svg>
);
const StorageViewIcon = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" aria-hidden="true">
    <rect x="3" y="4" width="18" height="4" rx="1.5" fill="currentColor" /><rect x="3" y="10" width="12" height="4" rx="1.5" fill="currentColor" />
    <rect x="3" y="16" width="6" height="4" rx="1.5" fill="currentColor" />
  </svg>
);
const SortArrow = ({ down }: { down: boolean }) => (
  <Svg size={10} stroke={2.4}><path d={down ? "M12 5v14M6 13l6 6 6-6" : "M12 19V5M6 11l6-6 6 6"} /></Svg>
);

// A "photo": a gradient of the theme's colours with a sun and a hill -
// never a picture of anyone.
function Tile({ hue, className, style, children }: { hue: number; className?: string; style?: CSSProperties; children?: ReactNode }) {
  return <span className={cls("ta-tile", `ta-h${((hue % 6) + 6) % 6}`, className)} style={style}>{children}</span>;
}

// A face as People draws an unknown one: a plain outline in a circle.
function Face({ hue, className, children }: { hue: number; className?: string; children?: ReactNode }) {
  return (
    <span className={cls("ta-face", `ta-h${hue % 6}`, className)}>
      <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="9.5" r="3.6" /><path d="M5 20c1.2-3.6 3.9-5.3 7-5.3s5.8 1.7 7 5.3" /></svg>
      {children}
    </span>
  );
}

// Text in place of a label not worth reading (a file's name, a date).
const Bar = ({ w, className }: { w: number; className?: string }) => <span className={cls("ta-bar", className)} style={{ width: pct(w) }} />;

// The mouse (or, on a touch screen, a finger) doing what the step says.
// x and y: a share of the drawing (a number, %) or any CSS length (a calc
// from the edge a right-aligned button keeps, whatever its label's length).
type At = number | string;
const len = (v: At) => (typeof v === "number" ? pct(v) : v);
function Pointer({ x, y, down = false, hidden = false }: { x: At; y: At; down?: boolean; hidden?: boolean }) {
  const touch = useCoarsePointer();
  return (
    <span className={cls("ta-pointer", touch && "touch", down && "down", hidden && "gone")} style={{ left: len(x), top: len(y) }}>
      {touch ? <span className="ta-finger" /> : (
        <svg width="16" height="18" viewBox="0 0 16 18" aria-hidden="true">
          <path d="M1.5 1.5v13.2l3.4-3.1 2.4 5.3 2.4-1.1-2.4-5.2h4.6Z" />
        </svg>
      )}
    </span>
  );
}

// Text typed in: shown a character at a time from when `on` turns true.
function Typed({ text, on }: { text: string; on: boolean }) {
  const n = Math.max(1, Array.from(text).length);
  return <span className={cls("ta-typed", on && "on")} style={{ "--ta-n": n } as CSSProperties}>{text}</span>;
}

// A made-up device name from a made-up first name: what Friends takes
// (letters, digits and dashes).
const deviceName = (name: string) =>
  name.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase().replace(/[^a-z0-9-]+/g, "") || "sam"; // i18n-ignore: a fallback device name

// The first part of a word, as typed so far.
const prefixOf = (word: string) => {
  const chars = Array.from(word);
  return chars.slice(0, Math.min(chars.length, Math.max(2, Math.ceil(chars.length / 2)))).join("");
};

// ---- welcome: the menu, item by item ------------------------------------

type Section = { key: string; Icon: (p: { size?: number }) => ReactNode; label: string; kind: "tiles" | "faces" | "cards" | "rows" | "post" | "friends" | "alerts" | "settings" };

const cWelcomeStep = 900;

function WelcomeArt({ faces }: { faces: boolean }) {
  const t = useT();
  const sections: Section[] = [
    { key: "i", Icon: ImagesIcon, label: t("web.shell.menu.images"), kind: "tiles" },
    ...(faces ? [{ key: "p", Icon: PeopleIcon, label: t("web.shell.menu.people"), kind: "faces" } as Section] : []),
    { key: "c", Icon: CollectionsIcon, label: t("web.shell.menu.collections"), kind: "cards" },
    { key: "f", Icon: FilesIcon, label: t("web.shell.menu.files"), kind: "rows" },
    { key: "s", Icon: SocialIcon, label: t("web.shell.menu.social"), kind: "post" },
    { key: "r", Icon: FriendsIcon, label: t("web.shell.menu.friends"), kind: "friends" },
    { key: "a", Icon: AlertsIcon, label: t("web.shell.menu.alerts"), kind: "alerts" },
    { key: "g", Icon: SettingsIcon, label: t("web.shell.menu.settings"), kind: "settings" },
  ];
  const at = useScene(faces ? cWelcomeTimesFaces : cWelcomeTimes, 0);
  const on = sections[at] ?? sections[0];
  const rowH = 100 / (sections.length + 0.6);
  return (
    <>
      <div className="ta-bar-top">
        <span className="ta-menu-btn"><MenuIcon size={12} /></span>
        <span className="ta-logo" />
        <span className="ta-search-pill"><GlassIcon size={10} /></span>
        <span className="ta-avatar" />
      </div>
      <div className="ta-menu">
        {sections.map((s, i) => (
          <span key={s.key} className={cls("ta-menu-item", i === at && "on")} style={{ top: pct(4 + i * rowH), height: pct(rowH) }}>
            <s.Icon size={11} />
            <span className="ta-ellip">{s.label}</span>
          </span>
        ))}
      </div>
      <div className="ta-page">
        <SectionSketch key={on.key} kind={on.kind} />
      </div>
      <Pointer x={20} y={16 + (4 + at * rowH + rowH / 2) * 0.84} />
    </>
  );
}
const cWelcomeTimes = Array.from({ length: 7 }, () => cWelcomeStep);
const cWelcomeTimesFaces = Array.from({ length: 8 }, () => cWelcomeStep);

// What a section looks like, at a glance.
function SectionSketch({ kind }: { kind: Section["kind"] }) {
  switch (kind) {
    case "tiles":
      return <div className="ta-sk-grid">{[0, 1, 2, 3, 4, 5].map((i) => <Tile key={i} hue={i} />)}</div>;
    case "faces":
      return <div className="ta-sk-faces">{[0, 1, 2, 3].map((i) => <span key={i}><Face hue={i} /><Bar w={70} /></span>)}</div>;
    case "cards":
      return <div className="ta-sk-cards">{[1, 3].map((i) => <span key={i}><Tile hue={i} /><Bar w={60} /></span>)}</div>;
    case "rows":
      return (
        <div className="ta-sk-rows">
          {[70, 52, 64, 40].map((w, i) => <span key={i}>{i < 2 ? <FolderIcon size={11} /> : <DocIcon size={11} />}<Bar w={w} /></span>)}
        </div>
      );
    case "post":
      return <div className="ta-sk-post"><span className="ta-sk-who"><span className="ta-avatar" /><Bar w={40} /></span><Tile hue={2} /><Heart on={false} /></div>;
    case "friends":
      return <div className="ta-sk-rows">{[60, 48, 54].map((w, i) => <span key={i}><span className="ta-avatar" /><Bar w={w} /></span>)}</div>;
    case "alerts":
      return <div className="ta-sk-rows">{[74, 60, 68].map((w, i) => <span key={i}><span className="ta-dot" /><Bar w={w} /></span>)}</div>;
    case "settings":
      return <div className="ta-sk-rows">{[44, 58, 36, 50].map((w, i) => <span key={i}><Bar w={w} /><span className="ta-switch" /></span>)}</div>;
  }
}

// ---- search: typed, suggested, picked as a chip -------------------------

// rest, typing, suggestions, on "beach", pressed, the chip and fewer photos
const cSearchTimes = [700, 1000, 1000, 700, 250, 2000];

function SearchArt({ faces }: { faces: boolean }) {
  const t = useT();
  const at = useScene(cSearchTimes, 3);
  const word = t("web.tour.art.thing");
  const typed = prefixOf(word);
  const rest = word.slice(typed.length);
  const file = `${word}.jpg`; // i18n-ignore: a file's name
  const person = t("web.tour.art.person");
  const panel = at >= 2 && at <= 4;
  const chip = at === 5;
  // Six photos; with the chip only those with the thing (and the person).
  const kept = [0, 2, 3];
  return (
    <>
      <div className="ta-field">
        <span className="ta-glass"><GlassIcon size={12} /></span>
        {faces && <span className="ta-chip pic"><Face hue={4} /><span className="ta-ellip">{person}</span><CloseIcon size={9} /></span>}
        {chip && <span className="ta-chip new"><span className="ta-ellip">{word}</span><CloseIcon size={9} /></span>}
        {!chip && at >= 1 && <span className="ta-query"><Typed text={typed} on={at >= 1} /><span className="ta-caret" /></span>}
        {at === 0 && !faces && <span className="ta-placeholder ta-ellip">{t("web.shell.search.field")}</span>}
      </div>
      <div className="ta-sgrid">
        {[0, 1, 2, 3, 4, 5].map((i) => <Tile key={i} hue={i} className={cls(chip && !kept.includes(i) && "out")} />)}
      </div>
      <div className={cls("ta-panel", panel && "on")}>
        <span className="ta-head ta-ellip">{t("web.shell.search.things")}</span>
        <span className={cls("ta-row", at >= 3 && "hl")}>
          <TagIcon size={12} /><span className="ta-ellip"><mark>{typed}</mark>{rest}</span>
        </span>
        <span className="ta-head ta-ellip">{t("web.shell.search.files")}</span>
        <span className="ta-row">
          <PhotoIcon size={12} /><span className="ta-ellip"><mark>{typed}</mark>{file.slice(typed.length)}</span>
        </span>
        <span className="ta-row">
          <DocSearchIcon size={12} />
          <span className="ta-ellip"><Trans k="web.shell.search.documents" args={{ text: typed }} parts={{ mark: (s) => <mark>{s}</mark> }} /></span>
        </span>
      </div>
      <Pointer x={at >= 3 && at <= 4 ? 30 : 74} y={at >= 3 && at <= 4 ? 46 : 92} down={at === 4} hidden={at < 2} />
    </>
  );
}

// ---- dates: the scrubber dragged, the grid jumping ----------------------

// rest, to the column, pressed, dragged, dragged further, let go, the jump
const cDatesTimes = [800, 500, 350, 600, 700, 250, 1800];
const cMonthsShown = 12;
const cMonthStep = 3;

function DatesArt() {
  const t = useT();
  const at = useScene(cDatesTimes, 4);
  const now = new Date();
  const months = Array.from({ length: cMonthsShown }, (_, k) => {
    const d = new Date(now.getFullYear(), now.getMonth() - k * cMonthStep, 1);
    return { year: d.getFullYear(), month: d.getMonth() };
  });
  const title = (m: { year: number; month: number }) =>
    t("web.images.grid.month_title", { month: monthName(t.lang, m.month, "long"), year: String(m.year) });
  // Where the pointer is on the column (0 = top) and which month is there.
  const drag = [0.04, 0.04, 0.04, 0.34, 0.7, 0.7, 0.7][at];
  const under = Math.min(cMonthsShown - 1, Math.round(drag * (cMonthsShown - 1)));
  const jumped = at >= 6 ? under : 0;
  const years = months.map((m, k) => ({ ...m, k })).filter((m, k) => k === 0 || months[k - 1].year !== m.year);
  const y = 6 + drag * 86;
  return (
    <>
      <div className="ta-dgrid">
        <div className={cls("ta-dcol", at === 0 && "instant")} style={{ transform: `translateY(${-jumped * 70}px)` }}>
          {months.map((m, k) => (
            <div key={k} className="ta-dsec">
              <span className="ta-dtitle ta-ellip">{title(m)}</span>
              <span className="ta-drow">{[0, 1, 2, 3].map((i) => <Tile key={i} hue={k + i} />)}</span>
              <span className="ta-drow">{[0, 1, 2].map((i) => <Tile key={i} hue={k + i + 3} />)}</span>
            </div>
          ))}
        </div>
      </div>
      <div className={cls("ta-scrub", at >= 1 && at <= 5 && "hover")}>
        {years.map((m) => <span key={m.k} className="ta-year" style={{ top: pct(6 + (m.k / (cMonthsShown - 1)) * 86) }}>{m.year}</span>)}
        {at >= 2 && at <= 5 && <span className="ta-scrub-line" style={{ top: pct(y) }} />}
      </div>
      {at >= 2 && at <= 5 && (
        <span className="ta-bubble" style={{ top: pct(y) }}>
          {t("web.shell.scrubber.month", { month: monthName(t.lang, months[under].month, "short"), year: String(months[under].year) })}
        </span>
      )}
      <Pointer x={at === 0 ? 60 : 89} y={at === 0 ? 70 : y + 2} down={at >= 2 && at <= 4} />
    </>
  );
}

// ---- select: the circle, a drag across, the bar ------------------------

// rest, over the first photo, its circle clicked, pressed on the second,
// dragged over the third and fourth, then each of the bar's buttons
const cSelectTimes = [600, 600, 450, 300, 300, 300, 900, 900, 900, 900, 500];
const cSelCols = [5, 28, 51, 74];

function SelectArt() {
  const t = useT();
  const at = useScene(cSelectTimes, 7);
  const picked = at >= 5 ? 4 : at >= 4 ? 3 : at >= 3 ? 2 : at >= 2 ? 1 : 0;
  const lit = at >= 6 && at <= 9 ? at - 6 : -1;
  const actions = [
    { label: t("web.images.select.post"), icon: <PostIcon size={13} /> },
    { label: t("web.images.select.collect"), icon: <AddToCollectionIcon size={13} /> },
    { label: t("common.share"), icon: <ShareIcon size={13} /> },
    { label: t("common.delete"), icon: <TrashIcon size={13} /> },
  ];
  // The bar's buttons sit at its right end, 24px apart.
  const barBtn = (k: number) => ({ x: `calc(100% - ${19 + (3 - k) * 24}px)`, y: 9 });
  const pointer: { x: At; y: At } = [
    { x: 46, y: 88 }, { x: 8, y: 31 }, { x: 8, y: 31 }, { x: 36, y: 44 }, { x: 58, y: 44 }, { x: 82, y: 44 },
    barBtn(0), barBtn(1), barBtn(2), barBtn(3), { x: 70, y: 84 },
  ][at];
  return (
    <>
      <div className={cls("ta-selbar", picked > 0 && "on")}>
        <CloseIcon size={11} />
        {/* Sliding away at the start again: the count it had. */}
        <span className="ta-ellip">{t("web.images.select.count", { count: picked || 4 })}</span>
        <span className="ta-sel-actions">
          {actions.map((a, i) => <span key={i} className={cls("ta-sel-btn", i === lit && "lit", i === 3 && "danger")}>{a.icon}</span>)}
        </span>
        {lit >= 0 && <span className="ta-tip ta-ellip" style={{ right: `${Math.max(4, 8 + (3 - lit) * 24 - 30)}px` }}>{actions[lit].label}</span>}
      </div>
      <div className="ta-pick-grid">
        {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => {
          const order = i < picked ? i + 1 : 0;
          return (
            <span key={i} className={cls("ta-pick", order > 0 && "on", (at === 1 && i === 0) || picked > 0 ? "show" : null)}
              style={{ left: pct(cSelCols[i % 4]), top: pct(i < 4 ? 26 : 62) }}>
              <Tile hue={i} />
              <span className="ta-circle">{order > 0 ? order : <CheckIcon size={8} />}</span>
            </span>
          );
        })}
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 2 || (at >= 3 && at <= 5)} />
    </>
  );
}

// ---- viewer: open, the next one, Info ---------------------------------

// the grid, opened, to the next arrow, next, to Info, Info pressed, Info open
const cViewerTimes = [700, 600, 600, 450, 600, 250, 2000];

function ViewerArt() {
  const t = useT();
  const at = useScene(cViewerTimes, 6);
  const open = at >= 1;
  const second = at >= 4;
  const infoOpen = at >= 6;
  const now = new Date();
  const taken = fmtDate(t.lang, new Date(now.getFullYear(), now.getMonth() - 2, 14, 17, 32), { dateStyle: "medium" });
  const next = { x: "calc(97% - 10px)", y: 58 };
  const info = { x: "calc(8px + 14px)", y: 9 };
  const pointer: { x: At; y: At } = [
    { x: 36, y: 50 }, { x: 36, y: 50 }, next, next, info, info, { x: 30, y: 84 },
  ][at];
  return (
    <>
      <div className="ta-vgrid">{[0, 1, 2, 3].map((i) => <Tile key={i} hue={i + 1} />)}</div>
      <div className={cls("ta-viewer", open && "on")}>
        <div className="ta-vhead">
          <span className={cls("ta-info-btn", at >= 5 && "lit")}><span className="ta-info-i">i</span><span className="ta-ellip">{t("web.images.viewer.info")}</span></span>
          <Bar w={22} />
          <CloseIcon size={11} />
        </div>
        <div className={cls("ta-vstrip", infoOpen && "narrow")}>
          <Tile hue={2} className={cls("ta-vphoto", second && "left")} />
          <Tile hue={3} className={cls("ta-vphoto", !second && "right")} />
        </div>
        <span className="ta-arrow l">‹</span>
        <span className={cls("ta-arrow r", at === 3 && "lit")}>›</span>
        <div className={cls("ta-info", infoOpen && "on")}>
          <span className="ta-info-title ta-ellip">{t("web.images.viewer.more_info")}</span>
          <span className="ta-info-row"><span className="ta-ellip">{t("web.images.viewer.taken")}</span><span className="ta-ellip">{taken}</span></span>
          <span className="ta-info-row"><span className="ta-ellip">{t("web.images.viewer.camera")}</span><Bar w={70} /></span>
          <span className="ta-info-row"><span className="ta-ellip">{t("web.images.viewer.location")}</span><Bar w={56} /></span>
          <span className="ta-map"><span className="ta-pin" /></span>
        </div>
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 3 || at === 5} />
    </>
  );
}

// ---- people: a name given, two faces merged -----------------------------

// rest, to Add a name, typing, named, a circle, another, the bar
const cPeopleTimes = [700, 500, 1200, 600, 450, 450, 1800];
const cFaceCols = [4, 28, 52, 76];

function PeopleArt() {
  const t = useT();
  const at = useScene(cPeopleTimes, 6);
  const friend = t("web.tour.art.friend");
  const picked = at >= 5 ? 2 : at >= 4 ? 1 : 0;
  const label = (i: number) => {
    if (i === 0) return <span className="ta-ellip">{t("web.tour.art.person")}</span>;
    if (i === 1 && at === 2) return <span className="ta-name-field"><Typed text={friend} on /><span className="ta-caret" /></span>;
    if (i === 1 && at >= 3) return <span className="ta-ellip">{friend}</span>;
    return <span className="ta-ellip ta-link">{t("web.images.people.add_name")}</span>;
  };
  const pointer: { x: At; y: At } = [
    { x: 60, y: 92 }, { x: 38, y: 62 }, { x: 38, y: 62 }, { x: 46, y: 92 }, { x: 57, y: 26 }, { x: 81, y: 26 }, { x: "calc(100% - 30px)", y: 9 },
  ][at];
  return (
    <>
      <div className={cls("ta-selbar", picked > 0 && "on")}>
        <CloseIcon size={11} />
        <span className="ta-ellip">{t("web.images.people.count", { count: picked || 2 })}</span>
        <span className="ta-sel-actions">
          <span className={cls("ta-sel-pill", picked >= 2 && "lit")}><span className="ta-ellip">{t("web.images.people.merge")}</span></span>
        </span>
      </div>
      {[0, 1, 2, 3].map((i) => {
        const order = i >= 2 && i - 2 < picked ? i - 1 : 0;
        return (
          <span key={i} className={cls("ta-person", order > 0 && "on", picked > 0 && i >= 2 && "show")} style={{ left: pct(cFaceCols[i]) }}>
            <Face hue={i + 1}><span className="ta-circle">{order > 0 ? order : <CheckIcon size={8} />}</span></Face>
            <span className="ta-person-label">{label(i)}</span>
            <span className="ta-person-count ta-ellip">{t("web.images.people.photos", { count: [12, 8, 5, 3][i] })}</span>
          </span>
        );
      })}
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 4 || at === 5} />
    </>
  );
}

// ---- collections: added to a new one, found here ------------------------

// selected, the button, its menu, the name typed, the page, the card
// opened, its header
const cCollectionsTimes = [700, 700, 700, 1100, 1300, 450, 1800];

function CollectionsArt() {
  const t = useT();
  const at = useScene(cCollectionsTimes, 4);
  const name = t("web.tour.art.collection");
  const page = at >= 4 ? (at >= 6 ? "open" : "list") : "images";
  const pointer: { x: At; y: At } = [
    { x: 50, y: 84 }, { x: "calc(100% - 67px)", y: 9 }, { x: 70, y: 36 }, { x: 70, y: 36 }, { x: 22, y: 60 }, { x: 22, y: 56 },
    { x: "calc(94% - 33px)", y: 14 },
  ][at];
  return (
    <>
      <div className={cls("ta-cpage", page === "images" && "on")}>
        <div className="ta-selbar on">
          <CloseIcon size={11} />
          <span className="ta-ellip">{t("web.images.select.count", { count: 3 })}</span>
          <span className="ta-sel-actions">
            <span className="ta-sel-btn"><PostIcon size={13} /></span>
            <span className={cls("ta-sel-btn", at >= 1 && "lit")}><AddToCollectionIcon size={13} /></span>
            <span className="ta-sel-btn"><ShareIcon size={13} /></span>
            <span className="ta-sel-btn danger"><TrashIcon size={13} /></span>
          </span>
          {at === 1 && <span className="ta-tip ta-ellip" style={{ right: "40px" }}>{t("web.images.select.collect")}</span>}
        </div>
        <div className="ta-cgrid">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <span key={i} className={cls("ta-pick show", i < 3 && "on")}>
              <Tile hue={i + 2} />
              <span className="ta-circle">{i < 3 ? i + 1 : <CheckIcon size={8} />}</span>
            </span>
          ))}
        </div>
        <div className={cls("ta-menu-pop", at >= 2 && at <= 3 && "on")}>
          <span className="ta-head ta-ellip">{t("web.images.collect.title")}</span>
          {at === 2 && <span className="ta-row hl"><span className="ta-plus">+</span><span className="ta-ellip">{t("web.images.collect.new")}</span></span>}
          {at === 3 && <span className="ta-name-field"><Typed text={name} on /><span className="ta-caret" /></span>}
        </div>
      </div>
      <div className={cls("ta-cpage", page === "list" && "on")}>
        <span className="ta-ptitle ta-ellip">{t("web.images.collections.title")}</span>
        <div className="ta-cards">
          <span className={cls("ta-card", at === 5 && "lit")}>
            <Tile hue={2} /><span className="ta-ellip">{name}</span>
            <small className="ta-ellip">{t("web.images.count.items", { count: 3 })}</small>
          </span>
          <span className="ta-card"><Tile hue={5} /><Bar w={60} /><Bar w={34} /></span>
          <span className="ta-card"><Tile hue={1} /><Bar w={50} /><Bar w={30} /></span>
        </div>
      </div>
      <div className={cls("ta-cpage", page === "open" && "on")}>
        <div className="ta-chead">
          <span className="ta-ctitle ta-ellip">{name}</span>
          <small className="ta-ellip">{t("web.images.count.items", { count: 3 })}</small>
          <span className="ta-cactions">
            <span className="ta-sel-btn"><PencilIcon size={12} /></span>
            <span className="ta-sel-btn lit"><ShareIcon size={12} /></span>
            <span className="ta-sel-btn danger"><TrashIcon size={12} /></span>
          </span>
          <span className="ta-tip ta-ellip" style={{ right: pct(4), top: "auto", bottom: "-18px" }}>{t("web.images.collection.share")}</span>
        </div>
        <div className="ta-cgrid low">{[0, 1, 2].map((i) => <Tile key={i} hue={i + 2} />)}</div>
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 2 || at === 5} />
    </>
  );
}

// ---- files: List, Grid, Storage, and sorting by a heading ---------------

// list, Grid pressed, grid, Storage pressed, storage, List pressed, list,
// Size pressed, sorted by size
const cFilesTimes = [900, 300, 1100, 300, 1300, 300, 700, 300, 1800];
type FileMode = "list" | "grid" | "storage";
const cFilesModes: readonly FileMode[] = ["list", "list", "grid", "grid", "storage", "storage", "list", "list", "list"];

const cFileRows = [
  { kind: "folder", w: 46, bytes: 2_400_000_000, share: 62 },
  { kind: "photo", w: 58, bytes: 4_300_000, share: 4 },
  { kind: "doc", w: 38, bytes: 128_000, share: 1 },
  { kind: "video", w: 52, bytes: 890_000_000, share: 33 },
] as const;

function FilesArt() {
  const t = useT();
  const at = useScene(cFilesTimes, 8);
  const mode = cFilesModes[at];
  const sorted = at >= 8;
  // By name as listed; by size, largest first, once sorted.
  const order = sorted ? [0, 3, 1, 2] : [0, 1, 2, 3];
  const sortText = sorted ? t("web.files.sort.size_desc") : t("web.files.sort.name_asc");
  // The view buttons start the toolbar, 23px apart; Size is the last heading.
  const view = (k: number) => ({ x: `calc(5% + ${12 + k * 23}px)`, y: "18px" });
  const size = { x: 86, y: "44px" };
  const pointer: { x: At; y: At } = [
    view(1), view(1), view(2), view(2), view(0), view(0), size, size, { x: 60, y: 90 },
  ][at];
  const icon = (kind: string) => (kind === "folder" ? <FolderIcon size={11} /> : kind === "photo" ? <PhotoIcon size={11} /> : <DocIcon size={11} />);
  return (
    <>
      <div className="ta-ftool">
        <span className="ta-views">
          <span className={cls("ta-view", mode === "list" && "on", (at === 5) && "press")}><ListViewIcon /></span>
          <span className={cls("ta-view", mode === "grid" && "on", at === 1 && "press")}><GridViewIcon /></span>
          <span className={cls("ta-view", mode === "storage" && "on", at === 3 && "press")}><StorageViewIcon /></span>
        </span>
        <span className={cls("ta-sortpill", mode === "storage" && "gone")}><span className="ta-ellip">{sortText}</span></span>
      </div>
      <div className={cls("ta-fview", mode === "list" && "on")}>
        <span className="ta-fhead">
          <span className="ta-ellip">{t("web.files.column.name")}</span>
          <span className="ta-ellip">{t("web.files.column.modified")}</span>
          <span className={cls("ta-ellip", at === 7 && "press", sorted && "on")}>{t("web.files.column.size")}{sorted && <SortArrow down />}</span>
        </span>
        {cFileRows.map((r, i) => (
          <span key={i} className="ta-frow" style={{ top: pct(14 + order.indexOf(i) * 21) }}>
            <span className="ta-ficon">{icon(r.kind)}</span>
            <Bar w={r.w} />
            <Bar w={30} className="dim" />
            <span className="ta-ellip">{fmtBytes(t.lang, r.bytes)}</span>
          </span>
        ))}
      </div>
      <div className={cls("ta-fview", mode === "grid" && "on")}>
        <div className="ta-fgrid">
          {cFileRows.map((r, i) => (
            <span key={i}>
              {r.kind === "photo" || r.kind === "video" ? <Tile hue={i + 1} /> : <span className="ta-ftile">{icon(r.kind)}</span>}
              <Bar w={70} />
            </span>
          ))}
        </div>
      </div>
      <div className={cls("ta-fview", mode === "storage" && "on")}>
        <span className="ta-fhead">
          <span className="ta-ellip">{t("web.files.column.name")}</span>
          <span className="ta-ellip">{t("web.files.column.share")}</span>
          <span className="ta-ellip">{t("web.files.column.size")}</span>
        </span>
        {[0, 3, 1, 2].map((i, k) => (
          <span key={i} className="ta-frow" style={{ top: pct(14 + k * 21) }}>
            <span className="ta-ficon">{icon(cFileRows[i].kind)}</span>
            <Bar w={cFileRows[i].w} />
            <span className="ta-share"><span style={{ width: pct(Math.max(3, cFileRows[i].share)) }} /></span>
            <span className="ta-ellip">{fmtBytes(t.lang, cFileRows[i].bytes)}</span>
          </span>
        ))}
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 3 || at === 5 || at === 7} />
    </>
  );
}

// ---- upload: dropped in, shared, kept upload only -----------------------

// rest, a file dragged in, uploading, done, a tick, another and the bar,
// Share pressed, its toast, the lock, the versions
const cUploadTimes = [600, 1000, 1000, 500, 450, 450, 450, 1500, 600, 1700];

function UploadArt() {
  const t = useT();
  const at = useScene(cUploadTimes, 7);
  const added = at >= 2;
  const ticks = at >= 5 ? 2 : at >= 4 ? 1 : 0;
  const bar = ticks > 0 && at <= 7;
  const locked = at >= 8;
  const rows = [
    { key: "new", kind: "doc", w: 50, isNew: true },
    { key: "f", kind: "folder", w: 40 },
    { key: "p", kind: "photo", w: 56 },
    { key: "d", kind: "doc", w: 46 },
  ].filter((r) => !r.isNew || added);
  // A row's tick at its start; the folder's lock after its name.
  const tick = (row: number) => ({ x: "calc(4% + 11px)", y: 36 + row * 18 });
  const pointer: { x: At; y: At } = [
    { x: 88, y: 94 }, { x: 54, y: 52 }, { x: 70, y: 92 }, { x: 70, y: 92 }, tick(1), tick(2),
    tick(2), tick(2), { x: "calc(4% + 57px + 0.4 * (92% - 12px))", y: 54 }, { x: 70, y: 94 },
  ][at];
  return (
    <>
      <div className="ta-ftool">
        <span className="ta-views">
          <span className="ta-view on"><ListViewIcon /></span>
          <span className="ta-view"><GridViewIcon /></span>
          <span className="ta-view"><StorageViewIcon /></span>
        </span>
        <span className="ta-sortpill"><span className="ta-ellip">{t("web.files.sort.name_asc")}</span></span>
      </div>
      <div className={cls("ta-ulist", at === 1 && "drop")}>
        {rows.map((r, i) => {
          const checked = (i === 1 && ticks >= 1) || (i === 2 && ticks >= 2);
          return (
            <span key={r.key} className={cls("ta-urow", r.isNew && "new")} style={{ top: pct(28 + i * 18) }}>
              <span className={cls("ta-check", checked && "on")}>{checked && <CheckIcon size={8} />}</span>
              <span className="ta-ficon">{r.kind === "folder" ? <FolderIcon size={11} /> : r.kind === "photo" ? <PhotoIcon size={11} /> : <DocIcon size={11} />}</span>
              <Bar w={r.w} />
              {r.kind === "folder" && <span className={cls("ta-lock", locked && "on")}><LockIcon size={10} locked={locked} /></span>}
              {r.isNew && at === 2 && <span className="ta-progress"><span /></span>}
              {r.isNew && at >= 3 && <span className="ta-done ta-ellip">{t("web.files.upload.done")}</span>}
              {r.key === "d" && at >= 9 && (
                <span className="ta-versions ta-ellip"><HistoryIcon size={9} />{t("web.files.versions.count", { count: 2 })}</span>
              )}
            </span>
          );
        })}
      </div>
      <div className={cls("ta-actbar", bar && "on")}>
        <CloseIcon size={10} />
        <span className="ta-ellip"><Trans k="web.files.selection.selected" args={{ count: ticks || 2 }} parts={{ b: (s) => <strong>{s}</strong> }} /></span>
        <span className="ta-grow" />
        <span className="ta-btn danger ta-ellip">{t("common.delete")}</span>
        <span className={cls("ta-btn ta-ellip", at >= 6 && "lit")}>{t("common.share")}</span>
        <span className="ta-btn ta-ellip">{t("web.files.selection.download_zip")}</span>
      </div>
      <span className={cls("ta-toast ta-ellip", at === 7 && "on")}>{t("web.files.selection.link_copied")}</span>
      {locked && <span className="ta-tip ta-ellip" style={{ left: pct(30), top: pct(60), right: "auto" }}>{t("web.files.lock.on_tip_short")}</span>}
      <span className={cls("ta-dragged", at === 1 && "on")}><DocIcon size={18} /><span className="ta-plus-badge">+</span></span>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 4 || at === 5 || at === 8} hidden={at === 6 || at === 7} />
    </>
  );
}

// ---- social: liked, commented ------------------------------------------

// rest, the heart, liked, to the field, typing, Post, the comment
const cSocialTimes = [800, 300, 700, 500, 1100, 300, 2000];

function SocialArt() {
  const t = useT();
  const at = useScene(cSocialTimes, 6);
  const liked = at >= 1;
  const comment = t("web.tour.art.comment");
  // From the foot: the field, and the heart above it (and above the comment once there).
  const heart = { x: "calc(10% + 8px)", y: "calc(100% - 39px)" };
  const field = { x: 40, y: "calc(100% - 16px)" };
  const pointer: { x: At; y: At } = [
    { x: 64, y: 40 }, heart, heart, field, field, { x: "calc(90% - 12px)", y: "calc(100% - 16px)" }, { x: 64, y: 40 },
  ][at];
  return (
    <div className="ta-post">
      <span className="ta-post-who">
        <span className="ta-avatar" />
        <strong className="ta-ellip">{t("web.tour.art.friend")}</strong>
        <small className="ta-ellip">{t("web.social.time.hours_ago", { hours: 2 })}</small>
      </span>
      <span className="ta-post-photo"><Tile hue={3} /><Tile hue={0} /></span>
      <span className="ta-post-actions">
        <span className={cls("ta-like", at === 1 && "pop")}><Heart on={liked} /></span>
        <span className="ta-likes ta-ellip">{t("web.social.post.likes", { count: liked ? 4 : 3 })}</span>
      </span>
      {at >= 6 && (
        <span className="ta-comment">
          <strong className="ta-ellip">{t("web.social.comment.author", { name: t("web.social.comment.me") })}</strong>
          <span className="ta-ellip">{comment}</span>
        </span>
      )}
      <span className="ta-newcomment">
        <span className="ta-input ta-ellip">
          {at >= 4 && at <= 5 ? <><Typed text={comment} on /><span className="ta-caret" /></> : <span className="ta-placeholder">{t("web.social.comment.placeholder")}</span>}
        </span>
        <span className={cls("ta-btn small ta-ellip", at >= 4 && at <= 5 && "ready", at === 5 && "lit")}>{t("web.social.comment.post")}</span>
      </span>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 5} />
    </div>
  );
}

// ---- post: composed and published --------------------------------------

// the feed, + New post pressed, the composer, a photo, another, the
// caption, Publish pressed, the new post
const cPostTimes = [700, 300, 700, 450, 450, 1200, 400, 2000];

function PostArt() {
  const t = useT();
  const at = useScene(cPostTimes, 5);
  const composing = at >= 2 && at <= 6;
  const picks = at >= 4 ? 2 : at >= 3 ? 1 : 0;
  const caption = t("web.tour.art.caption");
  const tile = (k: number) => ({ x: `calc(6% + 8px + ${0.1 + k * 0.2} * (88% - 16px))`, y: 56 });
  const newPost = { x: "calc(100% - 48px)", y: 8 };
  const pointer: { x: At; y: At } = [
    { x: 60, y: 60 }, newPost, tile(0), tile(0), tile(2), { x: 34, y: "calc(100% - 20px)" },
    { x: "calc(94% - 20px)", y: "calc(100% - 20px)" }, { x: 60, y: 92 },
  ][at];
  return (
    <>
      <div className="ta-bar-top">
        <span className="ta-menu-btn"><MenuIcon size={12} /></span>
        <span className="ta-logo" />
        <span className="ta-search-pill"><GlassIcon size={10} /></span>
        <span className={cls("ta-newpost ta-ellip", at === 1 && "press")}>+ {t("web.shell.top.new_post")}</span>
        <span className="ta-avatar" />
      </div>
      <div className="ta-feed">
        {at >= 7 && (
          <span className="ta-post-mini new">
            <span className="ta-post-who"><span className="ta-avatar" /><strong className="ta-ellip">{t("web.tour.art.person")}</strong><small className="ta-ellip">{t("web.social.time.just_now")}</small></span>
            <span className="ta-ellip ta-cap">{caption}</span>
            <span className="ta-post-photo"><Tile hue={1} /><Tile hue={3} /></span>
          </span>
        )}
        <span className="ta-post-mini">
          <span className="ta-post-who"><span className="ta-avatar" /><Bar w={30} /></span>
          <span className="ta-post-photo"><Tile hue={4} /></span>
        </span>
      </div>
      <div className={cls("ta-composer", composing && "on")}>
        <span className="ta-comp-head"><strong className="ta-ellip">{t("web.social.composer.title")}</strong><CloseIcon size={10} /></span>
        <span className="ta-tabs">
          <span className="on ta-ellip">{t("web.social.composer.library")}</span>
          <span className="ta-ellip">{t("web.social.composer.this_computer")}</span>
        </span>
        <span className="ta-comp-grid">
          {[1, 2, 3, 4, 5].map((h, i) => {
            const order = i === 0 && picks >= 1 ? 1 : i === 2 && picks >= 2 ? 2 : 0;
            return <span key={i} className={cls("ta-pick show", order > 0 && "on")}><Tile hue={h} /><span className="ta-circle">{order || <CheckIcon size={8} />}</span></span>;
          })}
        </span>
        <span className="ta-comp-foot">
          <span className="ta-input ta-ellip">
            {at >= 5 ? <><Typed text={caption} on={at >= 5} /><span className="ta-caret" /></> : <span className="ta-placeholder">{t("web.social.composer.caption")}</span>}
          </span>
          <span className={cls("ta-btn primary ta-ellip", at >= 5 && "ready", at === 6 && "lit")}>
            {picks ? t("web.social.composer.publish_count", { count: picks }) : t("web.social.composer.publish")}
          </span>
        </span>
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 3 || at === 4 || at === 6} />
    </>
  );
}

// ---- friends: the address copied, a request sent, one accepted ----------

// rest, Copy, copied, the name typed, Send pressed, sent, down to the
// requests, Accept pressed, accepted
const cFriendsTimes = [700, 300, 700, 1100, 300, 1100, 600, 300, 1700];

function FriendsArt() {
  const t = useT();
  const at = useScene(cFriendsTimes, 3);
  const me = deviceName(t("web.tour.art.person"));
  const friend = t("web.tour.art.friend");
  const down = at >= 6;
  // The buttons keep the cards' right edge.
  const right = "calc(94% - 18px)";
  const pointer: { x: At; y: At } = [
    { x: 60, y: 90 }, { x: right, y: "22px" }, { x: right, y: "22px" }, { x: 40, y: "78px" }, { x: right, y: "78px" }, { x: 60, y: 90 },
    { x: 60, y: 90 }, { x: right, y: "72px" }, { x: 60, y: 92 },
  ][at];
  return (
    <>
      <div className={cls("ta-fpage", down && "down")}>
        <span className="ta-fcard addr">
          <span className="ta-label ta-ellip">{t("web.social.friends.your_address")}</span>
          <code className="ta-ellip">{me}.{FRIEND_TLD}</code>
          <span className={cls("ta-btn small ta-ellip", at === 1 && "lit")}>{at >= 1 && at <= 2 ? t("web.social.friends.copied") : t("web.social.friends.copy")}</span>
        </span>
        <span className="ta-fcard add">
          <strong className="ta-ellip">{t("web.social.friends.add_title")}</strong>
          <span className="ta-addrow">
            <span className="ta-input ta-ellip">
              {at >= 3 ? <Typed text={deviceName(friend)} on /> : <span className="ta-placeholder">{t("web.social.friends.name_placeholder")}</span>}
              <span className="ta-suffix">.{FRIEND_TLD}</span>
            </span>
            <span className={cls("ta-btn primary ta-ellip", at >= 3 && "ready", at === 4 && "lit")}>{t("web.social.friends.send")}</span>
          </span>
        </span>
        <span className="ta-fcard wait">
          <strong className="ta-ellip">{t("web.social.friends.waiting_title")}</strong>
          <span className="ta-addrow">
            <span className="ta-avatar" />
            <span className="ta-ellip ta-grow">{friend}</span>
            <span className={cls("ta-status ta-ellip", at >= 8 && "ok")}>
              {at >= 8 ? t("web.social.friends.status_accepted") : t("web.social.friends.status_pending")}
            </span>
            {at < 8 && <span className={cls("ta-btn primary small ta-ellip", at === 7 && "lit")}>{t("common.accept")}</span>}
          </span>
        </span>
      </div>
      <span className={cls("ta-toast ta-ellip", at === 5 && "on")}>{t("web.social.friends.request_sent")}</span>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 4 || at === 7} />
    </>
  );
}

// ---- alerts and settings ------------------------------------------------

// to Alerts, its list, read, to Settings, Settings, Show the tour
const cAlertsTimes = [700, 400, 1700, 600, 400, 2000];

function AlertsArt() {
  const t = useT();
  const at = useScene(cAlertsTimes, 5);
  const page = at >= 4 ? "settings" : at >= 1 ? "alerts" : "none";
  const friend = t("web.tour.art.friend");
  const b = { b: (s: string) => <strong>{s}</strong> };
  const pointer: { x: At; y: At } = [
    { x: 16, y: 28 }, { x: 16, y: 28 }, { x: 64, y: 92 }, { x: 16, y: 56 }, { x: 16, y: 56 }, { x: "calc(37% + 30px)", y: "calc(100% - 34px)" },
  ][at];
  return (
    <>
      <div className="ta-amenu">
        <span className={cls("ta-menu-item", page === "alerts" && "on")} style={{ top: pct(18), height: pct(20) }}>
          <AlertsIcon size={11} /><span className="ta-ellip">{t("web.shell.menu.alerts")}</span>
          {page === "none" && <span className="ta-badge">3</span>}
        </span>
        <span className={cls("ta-menu-item", page === "settings" && "on")} style={{ top: pct(46), height: pct(20) }}>
          <SettingsIcon size={11} /><span className="ta-ellip">{t("web.shell.menu.settings")}</span>
        </span>
      </div>
      <div className={cls("ta-apage", page === "alerts" && "on")}>
        <span className="ta-arow"><span className="ta-avatar" /><span className="ta-ellip"><Trans k="web.social.alerts.liked_post" args={{ name: friend }} parts={b} /></span><Tile hue={2} /></span>
        <span className="ta-arow"><span className="ta-avatar" /><span className="ta-ellip"><Trans k="web.social.alerts.commented" args={{ name: friend }} parts={b} /></span><Tile hue={4} /></span>
        <span className="ta-arow"><span className="ta-avatar" /><span className="ta-ellip"><Trans k="web.social.alerts.friend_request" args={{ name: friend }} parts={b} /></span><span className="ta-btn primary small ta-ellip">{t("common.accept")}</span></span>
      </div>
      <div className={cls("ta-apage", page === "settings" && "on")}>
        <span className="ta-sec"><strong className="ta-ellip">{t("web.settings.profile.title")}</strong><span className="ta-sec-row"><span className="ta-avatar" /><Bar w={40} /></span></span>
        <span className="ta-sec"><strong className="ta-ellip">{t("web.settings.language.title")}</strong><span className="ta-sec-row"><Bar w={30} /></span></span>
        <span className="ta-sec"><strong className="ta-ellip">{t("web.tour.settings.title")}</strong><span className={cls("ta-btn ta-ellip", at >= 5 && "lit")}>{t("web.tour.show")}</span></span>
      </div>
      <Pointer x={pointer.x} y={pointer.y} down={at === 1 || at === 4} />
    </>
  );
}

const cScenes: Record<TourStepId, (p: { faces: boolean }) => ReactNode> = {
  welcome: WelcomeArt,
  search: SearchArt,
  dates: DatesArt,
  select: SelectArt,
  viewer: ViewerArt,
  people: PeopleArt,
  collections: CollectionsArt,
  files: FilesArt,
  upload: UploadArt,
  social: SocialArt,
  post: PostArt,
  friends: FriendsArt,
  alerts: AlertsArt,
};

/** The step's drawing: what the step teaches, moving (still with reduced motion). */
export default function TourArt({ step, faces }: { step: TourStepId; faces: boolean }) {
  const Scene = cScenes[step];
  return (
    <div className={cls("ta-stage", `ta-scene-${step}`)} aria-hidden="true">
      <Scene faces={faces} />
    </div>
  );
}
