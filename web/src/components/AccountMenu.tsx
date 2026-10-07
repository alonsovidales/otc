// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The account button at the right end of the top bar, as in Google Photos:
// the owner's profile picture (the profile this device keeps, as Settings
// shows it), which opens a small menu under it - the profile's name and
// this device's address, then Settings and Sign out (net/signOut.ts, the
// same as Settings' button).

import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useWS } from "../net/useWS";
import { signOut } from "../net/signOut";
import type { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { SettingsIcon } from "./NavIcons";
import Spinner from "./Spinner";
import "./AccountMenu.css";

type Props = {
  onOpenSettings: () => void;
  // Fetched again whenever this changes: App passes whether Settings,
  // where the profile is edited, is open.
  refreshKey?: unknown;
  // A tooltip on hover, where there is a pointer to hover with.
  tip: boolean;
};

// null while the first answer is awaited.
type Profile = { name: string; url: string | null } | null;

// A double click (or a double tap) on something its first click takes away
// sends the second click to whatever was under it: Settings' page under
// its item, the photo under the press that closed the menu. For a moment
// after such a click, a second click (detail 2 or more) goes nowhere and
// its press takes no focus. As on Images and People.
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

const sameBytes = (a: Uint8Array | null, b: Uint8Array | null) =>
  a === b || (!!a && !!b && a.length === b.length && a.every((v, i) => v === b[i]));

// The profile's name and picture. The picture's URL is made when an answer
// brings different bytes, never in a render, and the one it replaces is
// revoked then; the last one goes when the button does.
function useProfile(refreshKey: unknown): Profile {
  const [profile, setProfile] = useState<Profile>(null);
  const shown = useRef<{ bytes: Uint8Array | null; url: string | null }>({ bytes: null, url: null });

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          e.payload = { $case: "reqGetProfile", reqGetProfile: {} };
        });
        if (cancelled) return;
        if (resp.payload?.$case !== "respProfile") {
          // No profile to show: the glyph, and whatever was shown stays.
          setProfile((p) => p ?? { name: "", url: null });
          return;
        }
        const p = resp.payload.respProfile;
        const bytes = p.image && p.image.length > 0 ? p.image : null;
        const cur = shown.current;
        if (!sameBytes(bytes, cur.bytes)) {
          if (cur.url) URL.revokeObjectURL(cur.url);
          shown.current = { bytes, url: bytes ? URL.createObjectURL(new Blob([bytes as BlobPart])) : null };
        }
        setProfile({ name: (p.name ?? "").trim(), url: shown.current.url });
      } catch {
        if (!cancelled) setProfile((p) => p ?? { name: "", url: null });
      }
    })();
    return () => { cancelled = true; };
  }, [refreshKey]);

  useEffect(() => () => {
    if (shown.current.url) URL.revokeObjectURL(shown.current.url);
    shown.current = { bytes: null, url: null };
  }, []);

  return profile;
}

// The name's first letter, whole even when it is two UTF-16 units.
const initialOf = (name: string) => (Array.from(name)[0] ?? "").toLocaleUpperCase();

const PersonGlyph = ({ size }: { size: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="12" cy="9" r="3.6" />
    <path d="M5 19.5c1.2-3.4 3.8-5.1 7-5.1s5.8 1.7 7 5.1" />
  </svg>
);

const SignOutIcon = ({ size = 22 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M14 4.5H7a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h7" />
    <path d="M10.5 12H20m-3.5-3.5L20 12l-3.5 3.5" />
  </svg>
);

// The picture, else the name's initial, else a person: while the profile
// loads, when it has neither, and when its picture can't be shown.
function Avatar({ profile, big = false }: { profile: Profile; big?: boolean }) {
  const [broken, setBroken] = useState<string | null>(null);
  const cls = `am-avatar${big ? " big" : ""}`;
  if (profile?.url && profile.url !== broken) {
    return <img className={cls} src={profile.url} alt="" draggable={false} onError={() => setBroken(profile.url)} />;
  }
  const initial = profile ? initialOf(profile.name) : "";
  if (initial) return <span className={`${cls} is-initial`} aria-hidden="true">{initial}</span>;
  return <span className={`${cls} is-glyph`} aria-hidden="true"><PersonGlyph size={big ? 26 : 22} /></span>;
}

export default function AccountMenu({ onOpenSettings, refreshKey, tip }: Props) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  // Opened: fetched again too, so a name just changed elsewhere shows.
  const [opens, setOpens] = useState(0);
  // Saved in Settings (ProfileCard.tsx sends "otc-profile-changed"):
  // fetched again at once, not only as Settings closes.
  const [saves, setSaves] = useState(0);
  useEffect(() => {
    const onSaved = () => setSaves((n) => n + 1);
    window.addEventListener("otc-profile-changed", onSaved);
    return () => window.removeEventListener("otc-profile-changed", onSaved);
  }, []);
  const profile = useProfile(`${String(refreshKey)}:${opens}:${saves}`);

  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  // The item the focus goes to as the menu opens: the last one after the
  // up arrow on the button, else the first.
  const focusLast = useRef(false);
  const id = useId();
  const menuId = `${id}menu`;
  const headId = `${id}head`;
  const host = window.location.host;

  const items = () => Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);

  const show = (last: boolean) => {
    focusLast.current = last;
    setOpens((n) => n + 1);
    setOpen(true);
  };

  // Closed the way it opened: the focus goes back to the button.
  const close = useCallback(() => {
    setOpen(false);
    btnRef.current?.focus({ preventScroll: true });
  }, []);

  // Opened: the focus goes into the menu, so the arrows and Esc work at once.
  useEffect(() => {
    if (!open) return;
    const all = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    (focusLast.current ? all[all.length - 1] : all[0])?.focus({ preventScroll: true });
    // Esc wherever the focus fell (the menu's own keys don't come here).
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      e.preventDefault();
      if (!busyRef.current) close();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      // However it closes (an item, a press elsewhere), the second click
      // of a double click lands on what was under it.
      swallowSecondClick();
    };
  }, [open, close]);

  const onButtonKeyDown = (e: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    if (!open) show(e.key === "ArrowUp");
  };

  // The menu's keys are its own, never the page's (Images' Escape, its
  // selection keys).
  const onMenuKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    e.stopPropagation();
    const all = items();
    const at = all.indexOf(document.activeElement as HTMLElement);
    const go = (i: number) => {
      e.preventDefault();
      all[(i + all.length) % all.length]?.focus();
    };
    switch (e.key) {
      case "ArrowDown": go(at + 1); break;
      case "ArrowUp": go(at - 1); break;
      case "Home": go(0); break;
      case "End": go(all.length - 1); break;
      case "Escape":
      case "Tab":
        // Signing out goes on, and the menu stays to show it.
        e.preventDefault();
        if (!busy) close();
        break;
    }
  };

  const onSettings = () => {
    if (busy) return;
    close();
    onOpenSettings();
  };

  const onSignOut = () => {
    if (busy) return;
    busyRef.current = true;
    setBusy(true);
    // Reloads the page, signed out, once the device was told.
    void signOut();
  };

  return (
    <div className="tb-account">
      <button
        ref={btnRef}
        type="button"
        className="am-btn"
        aria-label="Account"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        data-tip={tip && !open ? "Account" : undefined}
        onClick={() => { if (open) { if (!busy) close(); } else show(false); }}
        onKeyDown={onButtonKeyDown}
      >
        <Avatar profile={profile} />
      </button>
      {open && (
        <>
          {/* Under the menu and over everything else, the rest of the top
              bar too: takes the press that closes it, so it never also
              opens what is under it. A right press (or a long press)
              closes it on its context menu, kept here so the browser's
              own never opens on what is under it. A middle press brings
              neither: it closes on the press. */}
          <div
            className="am-catcher"
            aria-hidden="true"
            onClick={() => { if (!busy) close(); }}
            onContextMenu={(e) => { e.preventDefault(); if (!busy) close(); }}
            onPointerDown={(e) => { if (e.button === 1 && !busy) close(); }}
          />
          {/* A press on the menu's header keeps the focus in the menu. */}
          <div className="am-pop" onMouseDown={(e) => { if (!(e.target as Element).closest("button")) e.preventDefault(); }}>
            <div className="am-head" id={headId}>
              <Avatar profile={profile} big />
              <span className="am-who">
                {profile?.name && <span className="am-name">{profile.name}</span>}
                <span className={profile?.name ? "am-host" : "am-name"}>{host}</span>
              </span>
            </div>
            <div
              ref={menuRef}
              id={menuId}
              role="menu"
              aria-labelledby={headId}
              className="am-menu"
              onKeyDown={onMenuKeyDown}
            >
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                className="am-item"
                aria-disabled={busy || undefined}
                onClick={onSettings}
              >
                <SettingsIcon size={22} />Settings
              </button>
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                className={`am-item${busy ? " is-busy" : ""}`}
                aria-disabled={busy || undefined}
                onClick={onSignOut}
              >
                {/* Signing out: the ring (hidden from screen readers) by
                    plain text, so the item, which keeps the focus, is
                    named by it. */}
                {busy ? <><Spinner />Signing out…</> : <><SignOutIcon />Sign out</>}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
