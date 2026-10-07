// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import { useWS } from "./net/useWS";
import SignIn from "./views/SignIn";
import FilesExplorer from "./components/FilesExplorer";
import Social from "./components/Social";
import PhotoGallery from "./components/PhotoGallery";
import SettingsForm from "./components/SettingsForm";
import ProfileCard from "./components/ProfileCard";
import FriendshipsManager from "./components/FriendshipsManager";
import type { TabKey } from "./components/nav";
import Sidebar from "./components/Sidebar";
import TopSearch from "./components/TopSearch";
import PeopleView from "./components/PeopleView";
import CollectionsView from "./components/CollectionsView";
import { showAll } from "./components/photoFilter";
import { MenuIcon } from "./components/NavIcons";
import NotificationsPage, { useNotificationCount } from "./components/NotificationsPage";
import type { ReqEnvelope, RespEnvelope } from "./proto/messages";
import { useSearchParams } from "react-router-dom";
import { getDeviceSetupInfo, loadPersistedToken } from "./net/pwCrypto";
import { promptForPushIfNeverAsked } from "./net/webPush";
import DeviceUnreachable from "./components/DeviceUnreachable";
import Spinner from "./components/Spinner";
import { getDeviceStatus, subscribeDeviceStatus } from "./net/deviceStatus";
import UpdateBanner from "./components/UpdateBanner";
import type { DeviceStatus } from "./net/deviceStatus";
import { loadLastTab, saveLastTab } from "./net/uiState";
import SharedGalleryView from "./components/SharedGalleryView";

declare global { interface Window { __OTC_CONFIG?: { endpoint: string; password: string; deviceId: string; }; } }

// The pages that lay out their own top (their headers' spacing, sticky bars).
const cOwnTopTabs: TabKey[] = ["PhotoGallery", "People", "Collections"];

function App() {
  const cfg = window.__OTC_CONFIG!;
  // Anonymous visitors have no tab switcher at all (TopTabs only renders
  // once authenticated, below), so their landing tab has to be one that
  // actually works signed-out — Profile's ReqGetProfile is a non-auth
  // request, Social's feed isn't. A fresh sign-in moves to Social
  // explicitly (see handleSignedIn below); otherwise (issue #53) a reload
  // restores whatever tab was last open rather than always landing here.
  const [tab, setTab] = useState<TabKey>(() => loadLastTab() ?? "Profile");
  const [authenticated, setAuthenticated] = useState(false);
  const [sp] = useSearchParams();

  // Issue #78: a tapped notification names a post (and maybe a comment on
  // it) for Social to open/scroll to. Lives here rather than inside
  // Social itself since the bell that sets it is a header-level element,
  // outside any single tab's own view.
  const [openPubUuid, setOpenPubUuid] = useState<string | null>(null);
  const [openCommentUuid, setOpenCommentUuid] = useState<string | null>(null);
  const [notificationCount, clearNotificationCount] = useNotificationCount(authenticated);

  // Issue #32 follow-up: the Social feed's own "+" compose button moved up
  // into this shared header (next to the notifications bell) instead of
  // floating over the feed - Social registers its own opener here rather
  // than this component needing to know anything about the picker itself.
  const [openComposer, setOpenComposer] = useState<(() => void) | null>(null);

  // The left menu. A wide window shows it whole or as a rail of icons,
  // switched with the top bar's button and remembered. A medium one (a
  // tablet, an unfolded phone) always shows the rail, and the button lays
  // the whole menu over the page; a phone shows only that.
  const wide = useMediaQuery("(min-width: 1024px)");
  const narrow = useMediaQuery("(max-width: 599px)");
  // Tooltips only where there is a pointer to hover with: a tap would leave
  // one standing over what it opened.
  const canHover = useMediaQuery("(hover: hover)");
  const [menuPref, setMenuPref] = useState<boolean>(() => {
    try { return localStorage.getItem("otc_menu_open") !== "0"; } catch { return true; }
  });
  const [drawerOpen, setDrawerOpen] = useState(false);
  const menuLayout: "full" | "rail" | "none" = wide ? (menuPref ? "full" : "rail") : narrow ? "none" : "rail";
  // The page slides beside the menu only when someone switches it, not when
  // the menu first appears after signing in.
  const [menuSlides, setMenuSlides] = useState(false);
  const setMenuExpanded = (expanded: boolean) => {
    try { localStorage.setItem("otc_menu_open", expanded ? "1" : "0"); } catch { /* private mode */ }
    setMenuSlides(true);
    setMenuPref(expanded);
  };
  const toggleMenu = () => {
    if (!wide) { setDrawerOpen((v) => !v); return; }
    setMenuExpanded(!menuPref);
  };
  const menuShown = wide ? menuPref : drawerOpen;
  // A drawer left open while the window grew wide would otherwise come
  // back on the next narrowing.
  useEffect(() => { if (wide) setDrawerOpen(false); }, [wide]);

  // Issue #105: whether a session restore is actually in flight right now.
  // The authenticated-only views below used to show "Signing in…" purely
  // because `authenticated` was false, with no idea whether anything was
  // still trying - so once a stored token turned out to be expired (or the
  // device had restarted and dropped every token, see issue #101) the
  // placeholder simply stayed there forever, with no way forward and no
  // explanation. Starts true only when there's actually a token to redeem.
  const [restoringSession, setRestoringSession] = useState(() => !cfg && !!loadPersistedToken());

  // Issue #56: the bridge's verdict on whether this device is reachable at
  // all, reported from ws.ts's socket callbacks (outside the component
  // tree) via deviceStatus's little store.
  const [deviceStatus, setDeviceStatus] = useState<DeviceStatus | null>(getDeviceStatus());
  useEffect(() => subscribeDeviceStatus(setDeviceStatus), []);

  // The menu goes with the rest of the app when the session ends or the
  // device stops answering; a drawer left open would come back open over
  // the page.
  useEffect(() => { if (!authenticated || deviceStatus) setDrawerOpen(false); }, [authenticated, deviceStatus]);

  let protoWs = 'ws://';
  if (window.location.protocol === 'https:') {
    protoWs = 'wss://';
  }
  let endpoint = protoWs + window.location.host + '/ws';
  if (window.location.host.startsWith('localhost')) {
    // Development (npm run dev / preview): VITE_DEVICE=pit.off-the.cloud
    // picks the device; Cala otherwise.
    endpoint = 'wss://' + (import.meta.env.VITE_DEVICE || 'cala.off-the.cloud') + '/ws';
  }
  const mobile = !!cfg;

  if (mobile) {
    endpoint = cfg.endpoint;
  }
  useWS.init(endpoint, setAuthenticated);

  // Issue #105: a session that dies mid-use (the device restarting is the
  // ordinary cause - see session/tokens.go) flips this back to false from
  // useWS's own message listener. Land the viewer somewhere usable rather
  // than leaving them on a view that will now never load its data. Skips
  // the very first render, when nobody was signed in to begin with.
  const wasAuthenticated = useRef(false);
  useEffect(() => {
    if (wasAuthenticated.current && !authenticated) landIfSignedOut();
    wasAuthenticated.current = authenticated;
  }, [authenticated]);

  // Issue #53: keep localStorage's "last tab" in sync with whatever's
  // actually showing, so the *next* reload restores it. Another section
  // starts at its top, not wherever the last one was scrolled to.
  useEffect(() => {
    saveLastTab(tab);
    window.scrollTo(0, 0);
  }, [tab]);

  // Issue #105: the tabs that have nothing to show without a session. The
  // rest (Profile, Social, SignIn) render something meaningful signed out,
  // which is exactly why Profile is where a failed restore lands - it's
  // this device's public face, reachable with no session at all, and it
  // carries the Sign In button to try again from.
  // Issue #118: Social is in this list too. Its feed is an authenticated
  // request for a browser (only a friend's *device* reads it anonymously),
  // so signed out it rendered an empty timeline - a blank page with a
  // Sign In button in the corner - whenever a session token expired,
  // whether that was found out on reload or mid-session. The public
  // profile is what an unregistered visitor gets, and it is what an
  // expired session should get too.
  const cAuthOnlyTabs: TabKey[] = ["AdminPannel", "PhotoGallery", "People", "Collections", "Settings", "Notifications", "Friends", "Social"];
  const cLandingTab: TabKey = "Profile";

  // Sends the viewer somewhere usable when a session couldn't be restored,
  // rather than leaving them on a view that can only ever say "Signing in…".
  // Deliberately only moves *off* an authenticated-only tab: someone whose
  // token expired while reading a public profile shouldn't be bounced
  // anywhere at all.
  const landIfSignedOut = () => {
    setRestoringSession(false);
    setTab((current) => (cAuthOnlyTabs.includes(current) ? cLandingTab : current));
  };

  // Issue #43: nudge for browser push permission right after sign-in,
  // rather than requiring the user to go find "Enable Notifications" in
  // Settings — only for the plain browser (mobile already registers for
  // APNs natively, see OffTheCloudApp.swift's AppDelegate; this path has
  // no bearing on that one). promptForPushIfNeverAsked no-ops after the
  // first time it's ever asked, whichever way it went.
  const handleSignedIn = () => {
    setTab("Social");
    if (!mobile) promptForPushIfNeverAsked();
  };

  // Issue #38/#39: a device with no owner secret yet lands straight on
  // setup — nobody should have to know to go click "Sign In" first just
  // to see that a brand-new device needs configuring.
  useEffect(() => {
    (async () => {
      try {
        if ((await getDeviceSetupInfo(useWS.request)).isNewDevice) setTab("SignIn");
      } catch (e) {
        console.error("Could not check device state:", e);
      }
    })();
  }, []);

  if (mobile) {
    useEffect(() => {
      (async () => {
        try {
          const ok = await useWS.sendAuth(cfg.password);
          if (ok) setTab("Social");
        } catch (e) {
          console.error("Auto-auth from container failed:", e);
        }
      })();
    }, [useWS]);
  } else {
    // Issue #46: a plain browser tab has no native container replaying a
    // Keychain-stored password on every launch — reuse whatever this
    // origin persisted from the last successful sign-in instead, so a
    // reload doesn't drop back to the sign-in form. Issue #101: what's
    // persisted is a single-use session token the device issued, not the
    // password — redeeming it establishes this connection's session and
    // rotates a fresh token into storage (see useWS.authWithToken).
    useEffect(() => {
      const token = loadPersistedToken();
      if (!token) {
        // Nothing to restore: whatever tab was last open, if it needs a
        // session we have no way to get one without the password.
        landIfSignedOut();
        return;
      }
      (async () => {
        try {
          const ok = await useWS.authWithToken(token);
          // Issue #53: unlike a fresh manual sign-in (handleSignedIn,
          // which jumps to Social), this is a reload — `tab` was already
          // initialized from the last-open view, and clobbering that back
          // to Social on every reload is exactly the bug this issue is
          // about. Only step in if the restored tab is "SignIn" itself,
          // which isn't a sensible place to land now that this succeeded.
          if (ok && tab === "SignIn") setTab("Social");
          // Issue #105: an expired or already-spent token is the ordinary
          // end of a session, not an error state to sit in.
          if (!ok) landIfSignedOut();
        } catch (e) {
          console.error("Auto-auth from stored session failed:", e);
          landIfSignedOut();
        } finally {
          setRestoringSession(false);
        }
      })();
    }, []);
  }

  // Issue #104: a shared link (?download=<uuid>_<secret>) fetches the zip
  // and saves it. This used to run as a bare async IIFE in the render body
  // - so every re-render of App while that parameter was in the URL kicked
  // off *another* download, and App re-renders several times on any normal
  // load as auth, device status and the notification count each settle.
  // Selecting three photos and downloading them produced three copies of
  // the same zip.
  //
  // An effect keyed on the link runs it once per link instead, and the ref
  // makes that hold even when React deliberately double-invokes effects
  // (StrictMode in development), where a dependency array alone wouldn't.
  const downloadLink = sp.get("download");
  const startedDownloadRef = useRef<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const [downloadProgress, setDownloadProgress] = useState<number | null>(null);
  useEffect(() => {
    if (!downloadLink) return;
    if (startedDownloadRef.current === downloadLink) return;
    startedDownloadRef.current = downloadLink;

    (async () => {
      const parts = downloadLink.split("_").filter(Boolean);
      if (parts.length < 2) {
        setDownloadError("That download link is malformed.");
        return;
      }
      const [uuid, secret] = parts;
      try {
        // Issue #166: in parts of at most 4 MB, so the device never holds
        // the whole archive for one reply (it refuses to above 4 MB).
        const CHUNK = 4 << 20;
        const parts: Uint8Array[] = [];
        let offset = 0;
        let total = -1;
        while (total < 0 || offset < total) {
          const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
            (e as any).payload = {
              $case: "reqDownloadSharedLink",
              reqDownloadSharedLink: { uuid, secret, offset: BigInt(offset), length: CHUNK },
            };
          });
          if (resp.payload?.$case !== "respFileChunk") {
            setDownloadError(resp.errorMessage || "That link is no longer valid.");
            return;
          }
          const chunk = resp.payload.respFileChunk;
          total = Number(chunk.size);
          if (chunk.data.length === 0 && offset < total) {
            setDownloadError("The device stopped sending the file.");
            return;
          }
          parts.push(chunk.data);
          offset += chunk.data.length;
          if (total > 0) setDownloadProgress(offset / total);
        }
        if (total === 0) {
          setDownloadError("The device returned an empty file.");
          return;
        }

        const url = URL.createObjectURL(new Blob(parts as BlobPart[], { type: "application/zip" }));
        const a = document.createElement("a");
        a.href = url;
        a.download = "shared.zip";
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
      } catch (err: any) {
        // Reported inline rather than through alert(), which blocks the
        // whole tab until someone dismisses it - and this view has nothing
        // else to show, so the message is the page.
        setDownloadError(err?.message || "Could not download that link.");
      }
    })();
  }, [downloadLink]);

  // If this is a download, that's the whole page. The wait here is the
  // longest of the lot - the device builds the archive and sends it over
  // the relay before the browser sees a byte - so it gets a spinner
  // rather than a line of static text that could equally mean "stuck".
  // Issue #180: a shared gallery's page (/shared#<uuid>.<secret>) - for
  // anyone with the link, signed in or not; nothing else of the app shows.
  if (window.location.pathname === "/shared") {
    return <SharedGalleryView />;
  }

  if (!!downloadLink) {
    return (
      <div className="download-view">
        {downloadError
          ? <p className="sf-note error">{downloadError}</p>
          : <Spinner label={downloadProgress === null
              ? "Preparing your download…"
              : `Downloading… ${Math.round(downloadProgress * 100)}%`} />}
      </div>
    );
  }

  if (tab === "Settings") {
    (window as any).webkit?.messageHandlers?.native?.postMessage({
      action: "openSettings"
    });
  }

  // Issue #105: "Signing in…" only while something is genuinely in
  // flight. Once a restore has failed there's nothing left running, so
  // saying it again would be a lie that never resolves - and the viewer
  // is about to be moved to the landing tab anyway (landIfSignedOut),
  // making this the briefest of intermediate states rather than a
  // dead end.
  const signedOutPlaceholder = restoringSession
    ? <p>Signing in…</p>
    : <p className="sf-hint">Your session has ended — sign in again to continue.</p>;

  // Issue #56: nothing behind this is usable while the device is
  // unreachable - every view's data comes from it - so this stands in
  // front of the whole app rather than alongside it, and clears itself
  // the moment a request succeeds again (see deviceStatus.noteResponse).
  if (deviceStatus) {
    return <DeviceUnreachable status={deviceStatus} />;
  }

  return (
    <>
      <header className="topbar">
        {authenticated && (
          <button className="tb-menu" onClick={toggleMenu} aria-label={menuShown ? "Hide menu" : "Show menu"} aria-expanded={menuShown}>
            <MenuIcon size={24} />
          </button>
        )}
        {!mobile && (
          <div className={`tb-brand${authenticated ? " signed-in" : ""}`}>
            <img src={logo} className="tb-logo" alt="Off The Cloud" />
          </div>
        )}
        {/* Search the photos from any section, like Google Photos: picking
            a tag or a person shows Images. */}
        {authenticated
          ? <TopSearch onShowPhotos={() => setTab("PhotoGallery")} onShowPeople={() => setTab("People")} />
          : <div className="tb-fill" />}
        {/* The page's own action: a new post, on the feed. A phone shows
            only its "+". */}
        {authenticated && tab === "Social" && openComposer && (
          <button
            className="tb-action"
            onClick={() => openComposer()}
            aria-label="New post"
            data-tip={narrow && canHover ? "New post" : undefined}
          >
            <span aria-hidden="true">+</span><span className="tb-action-label"> New post</span>
          </button>
        )}
        {!authenticated &&
          <button className="top_sign_in" onClick={() => setTab("SignIn")}>
            Sign In
          </button>
        }
      </header>
      {authenticated && (
        <Sidebar
          tab={tab}
          notificationCount={notificationCount}
          layout={menuLayout}
          drawerOpen={drawerOpen}
          onSelect={(next) => {
            // Images in the menu is the whole library, as Photos is in
            // Google Photos: whatever was searched for is left.
            if (next === "PhotoGallery") showAll();
            setTab(next);
          }}
          onCloseDrawer={() => setDrawerOpen(false)}
          onExpand={() => (wide ? setMenuExpanded(true) : setDrawerOpen(true))}
        />
      )}
      <div className={`app-body${authenticated ? ` menu-${menuLayout}` : ""}${menuSlides ? " menu-slides" : ""}`}>
      {authenticated && <UpdateBanner onOpenSettings={() => setTab("Settings")} />}
      {/* Images, People and Collections space their own headers; every
          other page starts a gap below the top bar's line. */}
      <main className={cOwnTopTabs.includes(tab) ? undefined : "main-gap"}>
        {/* Issue #84: "Profile" is only ever the anonymous-visitor landing
            page now - the editable form moved into Settings, and
            Friendships (what this used to show once signed in) has its
            own tab below, reached from the Social header's button. */}
        {tab === "Profile" && !authenticated && <ProfileCard authenticated={authenticated} />}
        {tab === "Friends" && authenticated && <FriendshipsManager />}
        {tab === "Social" && (
          <Social
            authenticated={authenticated}
            openPubUuid={openPubUuid}
            openCommentUuid={openCommentUuid}
            onOpened={() => { setOpenPubUuid(null); setOpenCommentUuid(null); }}
            onRegisterOpenComposer={(open) => setOpenComposer(() => open)}
          />
        )}
        {tab === "SignIn" && <SignIn
          onAuth={async (key) => await useWS.sendAuth(key)}
          onDone={handleSignedIn}
        />}
        {/* Issue #53 follow-up: these three are authenticated-only views
            with no meaningful signed-out state (unlike Profile/Social
            above) — they used to mount unconditionally regardless of
            `authenticated`, which only ever mattered in practice once a
            reload could restore straight into one of them (see #53's tab
            persistence): the component would mount and fire its data
            request before the auto-auth from #46 had resolved, surfacing
            as a bare "not authenticated" error instead of just waiting. */}
        {tab === "AdminPannel" && (authenticated ? <FilesExplorer initialPath="/" /> : signedOutPlaceholder)}
        {tab === "PhotoGallery" && (authenticated ? <PhotoGallery onShowCollections={() => setTab("Collections")} /> : signedOutPlaceholder)}
        {tab === "People" && (authenticated ? <PeopleView onOpenPhotos={() => setTab("PhotoGallery")} /> : signedOutPlaceholder)}
        {tab === "Collections" && (authenticated ? <CollectionsView onOpenPhotos={() => setTab("PhotoGallery")} /> : signedOutPlaceholder)}
        {tab === "Settings" && (authenticated ? <SettingsForm /> : signedOutPlaceholder)}
        {tab === "Notifications" && (authenticated ? (
          <NotificationsPage
            onOpenPost={(pubUuid, commentUuid) => {
              setOpenPubUuid(pubUuid);
              setOpenCommentUuid(commentUuid);
              setTab("Social");
            }}
            onOpenFriendRequests={() => setTab("Friends")}
            onOpenSettings={() => setTab("Settings")}
            onAcknowledged={clearNotificationCount}
          />
        ) : signedOutPlaceholder)}
      </main>
      </div>
    </>
  )
}

// A media query's current answer, kept up to date.
function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const on = () => setMatches(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [query]);
  return matches;
}

export default App
