// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import { useWS } from "./net/useWS";
import SignIn from "./views/SignIn";
import Social from "./components/Social";
import PhotoGallery from "./components/PhotoGallery";
import ProfileCard from "./components/ProfileCard";
import { useDownloadLink } from "./components/downloadLink";
import FriendshipsManager from "./components/FriendshipsManager";
import type { TabKey } from "./components/nav";
import Sidebar from "./components/Sidebar";
import TopSearch from "./components/TopSearch";
import CollectionsView from "./components/CollectionsView";
import { showAll } from "./components/photoFilter";
import { useFaceRecognition, watchFaceRecognition } from "./components/faceRecognition";
import { leaveFilesSearch } from "./components/filesNav";
import { MenuIcon } from "./components/NavIcons";
import NotificationsPage, { useNotificationCount } from "./components/NotificationsPage";
import type { ReqEnvelope, RespEnvelope } from "./proto/messages";
import { getDeviceSetupInfo, loadPersistedToken, type DeviceSetupInfo } from "./net/pwCrypto";
import { reregisterPush } from "./net/webPush";
import { useLanguage, useT } from "./i18n";
import { asIs, percentText, say, type UiText } from "./components/uiText";
import DeviceUnreachable from "./components/DeviceUnreachable";
import Spinner from "./components/Spinner";
import { getDeviceStatus, subscribeDeviceStatus } from "./net/deviceStatus";
import UpdateBanner from "./components/UpdateBanner";
import { DemoBanner, DemoRefusedNotice, DemoWelcome } from "./components/DemoBanner";
import { useDemo } from "./components/useDemo";
import DeviceUpdatedNotice from "./components/DeviceUpdatedNotice";
import type { DeviceStatus } from "./net/deviceStatus";
import { asTabKey, loadLastTab, saveLastTab } from "./net/uiState";
import AccountMenu from "./components/AccountMenu";
import TopSignIn, { type Interrupted } from "./components/TopSignIn";
import LanguagePicker from "./components/LanguagePicker";
import { pageEndpoint } from "./net/endpoint";
import Tour from "./components/Tour";
import { claimAutoTour, type TourStepId } from "./components/tourSteps";
import { backSection, followBackSection } from "./components/backStack";
import { restoreSession } from "./net/sessionRestore";
import ReconnectingBanner from "./components/ReconnectingBanner";
import BigFileDialog from "./components/BigFileDialog";
import { isTooBigForMemory } from "./components/bigFile";
import { lazyPart } from "./components/lazyPart";
import Part from "./components/Part";

// Loaded when first opened (F3 of the 2026-10 review; lazyPart.ts).
const FilesExplorer = lazyPart(() => import("./components/FilesExplorer"));
const SettingsForm = lazyPart(() => import("./components/SettingsForm"));
const PeopleView = lazyPart(() => import("./components/PeopleView"));
// main.tsx gives /shared its own entry (sharedEntry.tsx): this one is never
// needed by the app's.
const SharedGalleryView = lazyPart(() => import("./components/SharedGalleryView"));

declare global { interface Window { __OTC_CONFIG?: { endpoint: string; password: string; deviceId: string; }; } }

// The pages that lay out their own top (their headers' spacing, sticky bars).
const cOwnTopTabs: TabKey[] = ["PhotoGallery", "People", "Collections"];
// Where a signed-out visitor lands (see landIfSignedOut).
const cLandingTab: TabKey = "Profile";
// Issue #105: the tabs that have nothing to show without a session. The
// rest (Profile, SignIn - a new device's setup) render something
// meaningful signed out, which is exactly why Profile is where a failed
// restore lands - it's this device's public face, reachable with no
// session at all, under the top bar's password field to sign in again.
// Issue #118: Social is in this list too. Its feed is an authenticated
// request for a browser (only a friend's *device* reads it anonymously),
// so signed out it rendered an empty timeline - a blank page with a
// Sign In button in the corner - whenever a session token expired,
// whether that was found out on reload or mid-session. The public
// profile is what an unregistered visitor gets, and it is what an
// expired session should get too.
const cAuthOnlyTabs: TabKey[] = ["AdminPannel", "PhotoGallery", "People", "Collections", "Settings", "Notifications", "Friends", "Social"];

// Issue #38/#39: whether the device is new (no owner password yet), asked
// once per load - again only while the device hasn't said (null: it
// couldn't be reached, or the bridge answered for it).
let setupCheck: Promise<DeviceSetupInfo | null> | null = null;
function checkDeviceSetup(): Promise<DeviceSetupInfo | null> {
  setupCheck ??= getDeviceSetupInfo(useWS.request)
    .catch((e) => {
      console.error("Could not check device state:", e);
      return null;
    })
    .then((info) => {
      if (!info) setupCheck = null;
      return info;
    });
  return setupCheck;
}

function App() {
  const t = useT();
  const cfg = window.__OTC_CONFIG!;
  // Anonymous visitors have no tab switcher at all (TopTabs only renders
  // once authenticated, below), so their landing tab has to be one that
  // actually works signed-out — Profile's ReqGetProfile is a non-auth
  // request, Social's feed isn't. A fresh sign-in moves to Social
  // explicitly (see handleSignedIn below); otherwise (issue #53) a reload
  // restores whatever tab was last open rather than always landing here.
  const [tab, setTab] = useState<TabKey>(() => loadLastTab() ?? "Profile"); // i18n-ignore: a tab's key
  const [authenticated, setAuthenticated] = useState(false);
  // The page's query (an old ?download= link): read once, as nothing in
  // the app changes it.
  const sp = useMemo(() => new URLSearchParams(window.location.search), []);

  // Back and Forward (components/backStack.ts). A section moved to - from
  // the menu, the top bar, or a link in a page - is a history entry of its
  // own (goTo); a section App sets by itself (a sign-in landing on the
  // feed, a restore that failed, People without face recognition) rewrites
  // the entry it is on. Back to an entry shows its section, or what stands
  // for it now (sectionFor).
  const movedTo = useRef<TabKey | null>(null);
  const shownTab = useRef(tab);
  shownTab.current = tab;
  const goTo = useCallback((next: TabKey) => {
    if (next === shownTab.current) return;
    movedTo.current = next;
    setTab(next);
  }, []);
  useEffect(() => {
    backSection(tab, movedTo.current === tab);
    movedTo.current = null;
  }, [tab]);

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
  // The top bar's page action shows only its "+" (index.css).
  const actionIconOnly = useMediaQuery("(max-width: 699px)");
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

  // A new device's setup is up (the SignIn tab), from the device saying it
  // is new to setup's last step - the storage and WiFi steps come after its
  // password is set. See showSetup below.
  const [setupShown, setSetupShown] = useState(false);
  // A sign-in the unreachable screen cut off (TopSignIn): its password goes
  // back in the field when the device answers again, never sent by itself.
  const [signInInterrupted, setSignInInterrupted] = useState<Interrupted | null>(null);
  useEffect(() => { if (authenticated) setSignInInterrupted(null); }, [authenticated]);

  // Issue #56: the bridge's verdict on whether this device is reachable at
  // all, reported from ws.ts's socket callbacks (outside the component
  // tree) via deviceStatus's little store.
  const [deviceStatus, setDeviceStatus] = useState<DeviceStatus | null>(getDeviceStatus());
  useEffect(() => subscribeDeviceStatus(setDeviceStatus), []);

  // The menu goes with the rest of the app when the session ends or the
  // device stops answering; a drawer left open would come back open over
  // the page.
  useEffect(() => { if (!authenticated || deviceStatus) setDrawerOpen(false); }, [authenticated, deviceStatus]);

  // People is there only while face recognition is on (faceRecognition.ts):
  // the device is asked once signed in. People remembered as the last tab
  // waits for the answer (or the one remembered from last time) and gives
  // way to Images when it is off.
  const faces = useFaceRecognition();
  useEffect(() => (authenticated ? watchFaceRecognition() : undefined), [authenticated]);
  useEffect(() => { if (faces === false && tab === "People") setTab("PhotoGallery"); }, [faces, tab]);

  // The page's own host, but on the developer's Vite servers (npm run dev,
  // vite preview), where VITE_DEVICE=pit.off-the.cloud picks the device and
  // Cala is the default (net/endpoint.ts). The dev server by its mode, not
  // DEV: a build run with NODE_ENV=development is still a release's.
  const page = pageEndpoint(window.location, import.meta.env.MODE === "development", import.meta.env.VITE_DEVICE, !!cfg);
  let endpoint = page.endpoint;
  // The device itself served this page (not the phone apps, not Vite):
  // only then does a device update bring a new web app.
  const servedByDevice = page.servedByDevice;
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
  // A setup past its password (its storage and WiFi steps) has nothing
  // left to do once its session is gone: the top bar signs in again.
  const wasAuthenticated = useRef(false);
  useEffect(() => {
    if (wasAuthenticated.current && !authenticated) {
      setSetupShown(false);
      setTab((current) => (current === "SignIn" ? cLandingTab : current));
      landIfSignedOut();
    }
    wasAuthenticated.current = authenticated;
  }, [authenticated]);

  // Issue #53: keep localStorage's "last tab" in sync with whatever's
  // actually showing, so the *next* reload restores it. Another section
  // starts at its top, not wherever the last one was scrolled to.
  useEffect(() => {
    saveLastTab(tab);
    window.scrollTo(0, 0);
  }, [tab]);

  // Sends the viewer somewhere usable when a session couldn't be restored,
  // rather than leaving them on a view that can only ever say "Signing in…".
  // Deliberately only moves *off* an authenticated-only tab: someone whose
  // token expired while reading a public profile shouldn't be bounced
  // anywhere at all.
  const landIfSignedOut = () => {
    setRestoringSession(false);
    setTab((current) => (cAuthOnlyTabs.includes(current) ? cLandingTab : current));
  };

  // The first-run tour (Tour.tsx, rules in tourSteps.ts): its step is kept
  // here, so it comes back where it was after the unreachable screen. It
  // opens by itself the first time this browser shows the signed-in app -
  // however it signed in, people signed in before the tour existed too -
  // never over a new device's setup, inside the phone apps' web view, on a
  // shared gallery or a download (claimAutoTour marks it seen as it opens);
  // and again, from its first step, with Show the tour in the account menu
  // or Settings. autoTour says at once (before React renders) that the open
  // one came by itself.
  const [tour, setTour] = useState<{ step: TourStepId; auto: boolean } | null>(null);
  const autoTour = useRef(false);
  const openTour = useCallback((auto: boolean) => {
    setDrawerOpen(false);
    autoTour.current = auto;
    setTour({ step: "welcome", auto });
  }, []);
  // Whether the tour is open by itself now: already, or just opened.
  const startAutoTour = useCallback((): boolean => {
    if (autoTour.current) return true;
    if (mobile || !claimAutoTour(() => window.localStorage)) return false;
    openTour(true);
    return true;
  }, [mobile, openTour]);
  const moveTour = useCallback((step: TourStepId) => setTour((cur) => (cur ? { ...cur, step } : cur)), []);
  // Skip, Done, Esc, Back or a click.
  const closeTour = useCallback(() => {
    setTour(null);
    autoTour.current = false;
  }, []);
  // The session ended: the tour goes with it.
  useEffect(() => {
    if (authenticated) return;
    setTour(null);
    autoTour.current = false;
  }, [authenticated]);

  // A sign-in lands on the feed, with the tour the first time. Browser
  // notifications (issue #43) are asked for from a button (PushCard, at
  // the top of the feed): asked right after a sign-in, Safari refused (it
  // needs a click), and the flag that it had been asked stopped any try
  // after that.
  const handleSignedIn = () => {
    setTab("Social");
    startAutoTour();
  };

  // Back to a section (backStack.ts): what App shows for it now - signed
  // out, the landing page for one that needs a session; Images for People
  // without face recognition; the feed for the landing page once signed in.
  const sectionFor = (want: TabKey): TabKey => {
    if (!authenticated && cAuthOnlyTabs.includes(want)) return cLandingTab;
    if (want === "People" && faces === false) return "PhotoGallery";
    if (want === "SignIn" && !setupShown) return authenticated ? "Social" : cLandingTab;
    if (want === "Profile" && authenticated) return "Social";
    return want;
  };
  const sectionForRef = useRef(sectionFor);
  sectionForRef.current = sectionFor;
  useEffect(() => followBackSection((t) => {
    const shown = sectionForRef.current(asTabKey(t) ?? "PhotoGallery"); // i18n-ignore: a tab's key
    setTab(shown);
    return shown;
  }), []);

  // The device writes this browser's pushes in the language the page shows
  // (docs/i18n.md, last_ui_language): a subscription it already has goes
  // again at each sign-in and whenever that language changes.
  const shownLanguage = useLanguage().effective;
  const demo = useDemo();
  useEffect(() => {
    if (authenticated && !mobile && !demo) void reregisterPush();
  }, [authenticated, mobile, shownLanguage, demo]);

  // Issue #38/#39: a device with no owner secret yet lands straight on
  // setup (the SignIn tab) — nobody should have to know to go click
  // "Sign In" first just to see that a brand-new device needs configuring.
  // Setup is all that tab is: anyone else signs in from the top bar, so a
  // device that isn't new moves off it (a reload during setup, or a tab
  // remembered from before). Asked again once the device answers after
  // the unreachable screen, when the first check met it. Whatever this
  // knows, the top bar's field never sends a password to a new device (it
  // would take it as its own, with no owner name or storage set up): the
  // device's own answer to that sign-in brings the setup up (showSetup).
  const [setupInfo, setSetupInfo] = useState<DeviceSetupInfo | null>(null);
  useEffect(() => {
    if (setupInfo || deviceStatus) return;
    let live = true;
    void checkDeviceSetup().then((info) => {
      if (!live || !info) return;
      setSetupInfo(info);
      setSetupShown(info.isNewDevice);
      setTab((current) => (info.isNewDevice ? "SignIn" : current === "SignIn" ? cLandingTab : current));
    });
    return () => { live = false; };
  }, [setupInfo, deviceStatus]);
  const showSetup = (isPrimary: boolean, container: boolean) => {
    setSetupInfo({ isNewDevice: true, isPrimary, container });
    setSetupShown(true);
    setSignInInterrupted(null);
    setTab("SignIn");
  };
  const finishSetup = () => {
    setSetupShown(false);
    setSetupInfo((info) => info && { ...info, isNewDevice: false });
    handleSignedIn();
  };

  // Signed out, the top bar has the password field and Sign In - not while
  // a stored session is being restored, nor over a new device's setup,
  // whose password is chosen there. On a phone the field is folded into
  // its Sign In button until pressed, then takes the bar, the logo's room
  // included.
  const showSignIn = !authenticated && !restoringSession && !(setupShown && tab === "SignIn");
  const [signInOpen, setSignInOpen] = useState(false);
  useEffect(() => { if (authenticated) setSignInOpen(false); }, [authenticated]);
  const signInFieldOut = showSignIn && narrow && signInOpen;

  // A sign-in that didn't come from the top bar's field (handleSignedIn):
  // a session restored or a password replayed while the page showed the
  // public profile, or its setup tab with no setup up. Neither has anything
  // for someone signed in - Profile renders nothing then - so the page
  // lands on the feed as after any sign-in. Setup's own sign-in stays for
  // its storage and WiFi steps.
  useEffect(() => {
    if (!authenticated) return;
    setTab((current) => (current === "Profile" || (current === "SignIn" && !setupShown) ? "Social" : current));
  }, [authenticated, setupShown]);

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
    // rotates a fresh token into storage (see useWS.authWithToken). A
    // restore that got no answer (a blip, the bridge saying the device
    // can't be reached) keeps "Signing in…" and tries again
    // (net/sessionRestore.ts): at once when the browser is back online or
    // the device answers again (the unreachable screen clearing).
    useEffect(() => {
      if (!loadPersistedToken()) {
        // Nothing to restore: whatever tab was last open, if it needs a
        // session we have no way to get one without the password.
        landIfSignedOut();
        return;
      }
      const restore = restoreSession({
        token: loadPersistedToken,
        redeem: (token) => useWS.authWithToken(token),
        wake: (now) => {
          const onOnline = () => now();
          window.addEventListener("online", onOnline);
          let was = getDeviceStatus();
          const off = subscribeDeviceStatus((status) => {
            if (was && !status) now();
            was = status;
          });
          return () => { window.removeEventListener("online", onOnline); off(); };
        },
        onNoAnswer: (attempt, e) => console.error(`Restoring the session got no answer (try ${attempt}):`, e),
      });
      void restore.done.then((outcome) => {
        if (outcome === "stopped") return;
        // Issue #53: unlike a fresh manual sign-in (handleSignedIn, which
        // jumps to Social), this is a reload — `tab` was already
        // initialized from the last-open view, and clobbering that back to
        // Social on every reload is exactly the bug this issue is about.
        // Only step in if the restored tab is "SignIn" itself (a reload
        // during setup), which isn't a sensible place to land now that
        // this succeeded.
        if (outcome === "signed_in") setTab((current) => (current === "SignIn" ? "Social" : current));
        // Issue #105: an expired or already-spent token is the ordinary
        // end of a session, not an error state to sit in.
        else landIfSignedOut();
        setRestoringSession(false);
      });
      return restore.stop;
    }, []);
  }

  // Issue #104: a shared link (/#download=<uuid>_<secret>, ?download= before) fetches the zip
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
  const downloadLink = useDownloadLink(sp);
  const startedDownloadRef = useRef<string | null>(null);
  const [downloadError, setDownloadError] = useState<UiText | null>(null);
  const [downloadProgress, setDownloadProgress] = useState<number | null>(null);
  const [downloadDone, setDownloadDone] = useState(false);
  // F11 of the 2026-10 review: the archive is put together in this page's
  // memory before the browser saves it. A big one asks first, once its
  // first part says how big (BigFileDialog); Cancel stops it there.
  const [bigAsk, setBigAsk] = useState<{ bytes: number; answer: (go: boolean) => void } | null>(null);
  const [downloadCancelled, setDownloadCancelled] = useState(false);
  useEffect(() => {
    if (!downloadLink) return;
    if (startedDownloadRef.current === downloadLink) return;
    startedDownloadRef.current = downloadLink;

    (async () => {
      const parts = downloadLink.split("_").filter(Boolean);
      if (parts.length < 2) {
        setDownloadError(() => say("web.shell.link.malformed"));
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
            // The device's own words, in the language the request carried.
            setDownloadError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.shell.link.invalid")));
            return;
          }
          const chunk = resp.payload.respFileChunk;
          total = Number(chunk.size);
          if (chunk.data.length === 0 && offset < total) {
            setDownloadError(() => say("web.shell.link.stopped"));
            return;
          }
          parts.push(chunk.data);
          offset += chunk.data.length;
          if (total > 0) setDownloadProgress(offset / total);
          if (parts.length === 1 && offset < total && isTooBigForMemory(total)) {
            const go = await new Promise<boolean>((answer) => setBigAsk({ bytes: total, answer }));
            setBigAsk(null);
            if (!go) {
              setDownloadCancelled(true);
              return;
            }
          }
        }
        if (total === 0) {
          setDownloadError(() => say("web.shell.link.empty"));
          return;
        }

        const url = URL.createObjectURL(new Blob(parts as BlobPart[], { type: "application/zip" }));
        const a = document.createElement("a");
        a.href = url;
        a.download = "shared.zip";
        document.body.appendChild(a);
        a.click();
        a.remove();
        // The tab has nothing more to do. Images and Files open it for the
        // ZIP, so it may close itself - after a moment, for the browser to
        // take the file (one that refuses, a tab someone opened by hand,
        // shows that the download started instead of 100% for good).
        setDownloadDone(true);
        window.setTimeout(() => {
          window.close();
          window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
        }, 2000);
      } catch (err: any) {
        // Reported inline rather than through alert(), which blocks the
        // whole tab until someone dismisses it - and this view has nothing
        // else to show, so the message is the page.
        const message: string = err?.message || "";
        setDownloadError(() => (message ? asIs(message) : say("web.shell.link.failed")));
      }
    })();
  }, [downloadLink]);

  // The tour by itself, the first time the signed-in app shows here: after
  // a restored session too (after a sign-in handleSignedIn opens it first,
  // to hold the notification prompt).
  const inSetup = setupShown && tab === "SignIn";
  const shellShown = authenticated && !inSetup && !deviceStatus && !downloadLink && window.location.pathname !== "/shared";
  useEffect(() => { if (shellShown) startAutoTour(); }, [shellShown, startAutoTour]);

  // If this is a download, that's the whole page. The wait here is the
  // longest of the lot - the device builds the archive and sends it over
  // the relay before the browser sees a byte - so it gets a spinner
  // rather than a line of static text that could equally mean "stuck".
  // Issue #180: a shared gallery's page (/shared#<uuid>.<secret>) - for
  // anyone with the link, signed in or not; nothing else of the app shows.
  if (window.location.pathname === "/shared") {
    return <Part><SharedGalleryView /></Part>;
  }

  if (!!downloadLink) {
    return (
      <div className="download-view">
        {downloadError
          ? <p className="sf-note error">{downloadError(t)}</p>
          : downloadCancelled
          ? <p className="sf-note">{t("web.shell.link.cancelled")}</p>
          : downloadDone
          ? <p className="sf-note">{t("web.shell.link.done")}</p>
          : <Spinner label={downloadProgress === null
              ? t("web.shell.link.preparing")
              : t("web.shell.link.progress", { percent: percentText(t, Math.round(downloadProgress * 100)) })} />}
        {bigAsk && (
          <BigFileDialog bytes={bigAsk.bytes} kind="save" onGo={() => bigAsk.answer(true)} onCancel={() => bigAsk.answer(false)} />
        )}
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
  const signedOutPlaceholder = (
    <div className="page-wait">
      {restoringSession
        ? <Spinner label={t("web.shell.session.signing_in")} />
        : <p>{t("web.shell.session.ended")}</p>}
    </div>
  );

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
          <button className="tb-menu" data-tour="menu-button" onClick={toggleMenu} aria-label={menuShown ? t("web.shell.top.hide_menu") : t("web.shell.top.show_menu")} aria-expanded={menuShown}>
            <MenuIcon size={24} />
          </button>
        )}
        {!mobile && !signInFieldOut && (
          <div className={`tb-brand${authenticated ? " signed-in" : ""}`}>
            <img src={logo} className="tb-logo" alt="Off The Cloud" />
          </div>
        )}
        {/* Search the photos and files from any section, like Google
            Photos: picking a tag or a person shows Images, a folder or a
            file Files. */}
        {authenticated
          ? <TopSearch onShowPhotos={() => goTo("PhotoGallery")} onShowFiles={() => goTo("AdminPannel")} />
          : !signInFieldOut && <div className="tb-fill" />}
        {/* The page's own action: a new post, on the feed. Below 700px
            only its "+" shows (index.css), named by a tooltip. */}
        {authenticated && tab === "Social" && openComposer && (
          <button
            className="tb-action"
            data-tour="new-post"
            onClick={() => openComposer()}
            aria-label={t("web.shell.top.new_post")}
            data-tip={actionIconOnly && canHover ? t("web.shell.top.new_post") : undefined}
          >
            <span aria-hidden="true">+</span><span className="tb-action-label"> {t("web.shell.top.new_post")}</span>
          </button>
        )}
        {/* The account, at the right end as in Google Photos: the
            profile's picture, opening a menu with Settings and Sign out.
            Fetched again as Settings, where the profile is edited, opens
            and closes. */}
        {authenticated && (
          <AccountMenu
            onOpenSettings={() => goTo("Settings")}
            onShowTour={inSetup ? undefined : () => openTour(false)}
            refreshKey={tab === "Settings"}
            tip={canHover}
          />
        )}
        {/* The language, before signing in: Settings, where it lives, needs
            a session. Below 700px only its globe shows, as the page action
            only its "+", and none while a phone's password field is out. */}
        {showSignIn && !signInFieldOut && <LanguagePicker iconOnly={actionIconOnly} />}
        {showSignIn && (
          <TopSignIn
            deviceHost={deviceHost(endpoint)}
            compact={narrow}
            open={signInOpen}
            onOpenChange={setSignInOpen}
            onNewDevice={showSetup}
            onSignedIn={handleSignedIn}
            interrupted={signInInterrupted}
            onInterrupted={setSignInInterrupted}
          />
        )}
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
            // Files in the menu is the folder, not the results of a search.
            if (next === "AdminPannel") leaveFilesSearch();
            goTo(next);
          }}
          onCloseDrawer={() => setDrawerOpen(false)}
          onExpand={() => (wide ? setMenuExpanded(true) : setDrawerOpen(true))}
        />
      )}
      <div className={`app-body${authenticated ? ` menu-${menuLayout}` : ""}${menuSlides ? " menu-slides" : ""}`}>
      {authenticated && <UpdateBanner onOpenSettings={() => goTo("Settings")} />}
      {authenticated && demo && <DemoBanner />}
      {demo && <DemoRefusedNotice />}
      {servedByDevice && <DeviceUpdatedNotice />}
      <ReconnectingBanner active={authenticated} />
      {/* Images, People and Collections space their own headers; every
          other page starts a gap below the top bar's line. */}
      <main className={cOwnTopTabs.includes(tab) ? undefined : "main-gap"}>
        {!authenticated && !restoringSession && demo && <DemoWelcome onSignedIn={handleSignedIn} />}
        {/* Issue #84: "Profile" is only ever the anonymous-visitor landing
            page now - the editable form moved into Settings, and
            Friendships (what this used to show once signed in) has its
            own tab below, reached from the Social header's button. */}
        {tab === "Profile" && !authenticated && <ProfileCard authenticated={authenticated} />}
        {tab === "Friends" && authenticated && <FriendshipsManager />}
        {/* While a session is restored the feed waits with the rest (a
            restore that got no answer keeps trying). */}
        {tab === "Social" && (authenticated || !restoringSession ? (
          <Social
            authenticated={authenticated}
            openPubUuid={openPubUuid}
            openCommentUuid={openCommentUuid}
            onOpened={() => { setOpenPubUuid(null); setOpenCommentUuid(null); }}
            onRegisterOpenComposer={(open) => setOpenComposer(() => open)}
          />
        ) : signedOutPlaceholder)}
        {tab === "SignIn" && setupShown && <SignIn isPrimary={setupInfo?.isPrimary ?? true} container={setupInfo?.container ?? false} onDone={finishSetup} />}
        {/* Issue #53 follow-up: these three are authenticated-only views
            with no meaningful signed-out state (unlike Profile/Social
            above) — they used to mount unconditionally regardless of
            `authenticated`, which only ever mattered in practice once a
            reload could restore straight into one of them (see #53's tab
            persistence): the component would mount and fire its data
            request before the auto-auth from #46 had resolved, surfacing
            as a bare "not authenticated" error instead of just waiting. */}
        {/* Files, People and Settings load when first opened (Part). Images
            going to Collections - out of a collection, or after deleting
            it - stands in for the collection's entry rather than adding
            one: Back doesn't open a collection that was left. */}
        {tab === "AdminPannel" && (authenticated ? <Part><FilesExplorer initialPath="/" /></Part> : signedOutPlaceholder)}
        {tab === "PhotoGallery" && (authenticated ? <PhotoGallery onShowCollections={() => setTab("Collections")} /> : signedOutPlaceholder)}
        {tab === "People" && (authenticated ? faces && <Part><PeopleView onOpenPhotos={() => goTo("PhotoGallery")} /></Part> : signedOutPlaceholder)}
        {tab === "Collections" && (authenticated ? <CollectionsView onOpenPhotos={() => goTo("PhotoGallery")} /> : signedOutPlaceholder)}
        {tab === "Settings" && (authenticated ? <Part><SettingsForm onShowTour={() => openTour(false)} /></Part> : signedOutPlaceholder)}
        {tab === "Notifications" && (authenticated ? (
          <NotificationsPage
            onOpenPost={(pubUuid, commentUuid) => {
              setOpenPubUuid(pubUuid);
              setOpenCommentUuid(commentUuid);
              goTo("Social");
            }}
            onOpenFriendRequests={() => goTo("Friends")}
            onOpenSettings={() => goTo("Settings")}
            onAcknowledged={clearNotificationCount}
          />
        ) : signedOutPlaceholder)}
      </main>
      </div>
      {tour && authenticated && !inSetup && <Tour step={tour.step} faces={faces} onStep={moveTour} onClose={closeTour} />}
    </>
  )
}

// The device's address (the endpoint's host): what a password manager files
// its password under.
function deviceHost(endpoint: string): string {
  try {
    return new URL(endpoint).host;
  } catch {
    return window.location.host;
  }
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
