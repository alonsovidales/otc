// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import { useWS } from "./net/useWS";
import SignIn from "./views/SignIn";
import FilesExplorer from "./components/FilesExplorer";
import StatusWidget from "./components/StatusWidget";
import Social from "./components/Social";
import PhotoGallery from "./components/PhotoGallery";
import SettingsForm from "./components/SettingsForm";
import ProfileCard from "./components/ProfileCard";
import FriendshipsManager from "./components/FriendshipsManager";
import TopTabs from "./components/TopTabs";
import type { TabKey } from "./components/TopTabs";
import NotificationsPage, { useNotificationCount } from "./components/NotificationsPage";
import "./components/StatusWidget.css";
import type { ReqEnvelope, RespEnvelope } from "./proto/messages";
import { useSearchParams } from "react-router-dom";
import { getDeviceSetupInfo, loadPersistedToken } from "./net/pwCrypto";
import { promptForPushIfNeverAsked } from "./net/webPush";
import DeviceUnreachable from "./components/DeviceUnreachable";
import { getDeviceStatus, subscribeDeviceStatus } from "./net/deviceStatus";
import type { DeviceStatus } from "./net/deviceStatus";
import { loadLastTab, saveLastTab } from "./net/uiState";

declare global { interface Window { __OTC_CONFIG?: { endpoint: string; password: string; deviceId: string; }; } }

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

  // Issue #56: the bridge's verdict on whether this device is reachable at
  // all, reported from ws.ts's socket callbacks (outside the component
  // tree) via deviceStatus's little store.
  const [deviceStatus, setDeviceStatus] = useState<DeviceStatus | null>(getDeviceStatus());
  useEffect(() => subscribeDeviceStatus(setDeviceStatus), []);

  let protoWs = 'ws://';
  if (window.location.protocol === 'https:') {
    protoWs = 'wss://';
  }
  let endpoint = protoWs + window.location.host + '/ws';
  if (window.location.host.startsWith('localhost')) {
    endpoint = protoWs + 'cala.off-the.cloud/ws';
  }
  //let endpoint = protoWs + 'cala.off-the.cloud/ws';
  const mobile = !!cfg;

  if (mobile) {
    endpoint = cfg.endpoint;
  }
  useWS.init(endpoint, setAuthenticated);

  // Issue #53: keep localStorage's "last tab" in sync with whatever's
  // actually showing, so the *next* reload restores it.
  useEffect(() => {
    saveLastTab(tab);
  }, [tab]);

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
      if (!token) return;
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
        } catch (e) {
          console.error("Auto-auth from stored session failed:", e);
        }
      })();
    }, []);
  }

  // If this is a download, just download and don't render anything
  const downloadLink = sp.get("download");
  if (!!downloadLink) {
    (async () => {
      const parts = downloadLink.split("_").filter(Boolean);
      if (parts.length >= 2) {
        const [first, second] = parts;
        console.log(`first: ${first}\nsecond: ${second}`);

        const resp: RespEnvelope = await useWS.request((e: Partial<ReqEnvelope>) => {
          (e as any).payload = { $case: "reqDownloadSharedLink", reqDownloadSharedLink: {
            uuid: first,
            secret: second,
          } };
        });

        console.log('Response:', resp);

        if (resp.payload?.$case === "respSharedFiles") {
          const bytes: Uint8Array | undefined = resp.payload.respSharedFiles.content;
          if (!bytes || bytes.length === 0) {
            alert("Server did not return content; implement chunked download or ensure resp_file.content is set.");
            return;
          }
          const blob = new Blob([bytes], { type: "application/zip" });
          const a = document.createElement("a");
          a.href = URL.createObjectURL(blob);
          a.download = 'shared.zip';
          document.body.appendChild(a);
          a.click();
          a.remove();
          URL.revokeObjectURL(a.href);
        } else {
          alert('Error:' + resp.errorMessage);
        }
      }
    })();

    return (<>Downloading... please wait</>);
  }

  if (tab === "Settings") {
    (window as any).webkit?.messageHandlers?.native?.postMessage({
      action: "openSettings"
    });
  }

  // Issue #56: nothing behind this is usable while the device is
  // unreachable - every view's data comes from it - so this stands in
  // front of the whole app rather than alongside it, and clears itself
  // the moment a request succeeds again (see deviceStatus.noteResponse).
  if (deviceStatus) {
    return <DeviceUnreachable status={deviceStatus} />;
  }

  return (
    <>
      <div className="header">
        {!mobile &&
          <a>
             <img src={logo} className="logo" alt="Off The Cloud logo" />
          </a>
        }

        {authenticated &&
          // alignSelf: stretch overrides .header's own align-items:center
          // just for this child, so it spans the header's full height
          // (set by the logo) instead of shrinking to its own content -
          // that's what lets justifyContent:space-between push the usage
          // bar all the way down to the header's own bottom edge instead
          // of floating centered partway down it.
          <div style={{ flex: 1, alignSelf: "stretch", display: "flex", flexDirection: "column", justifyContent: "space-between", paddingRight: 210 }}>
            {/* paddingRight (on this whole column, so it covers the nav row
                AND the status bar below it the same way) matches the
                logo's own footprint (200px width + 10px left margin) on
                this row's *other* side, so both center on the header's
                full width - the same reference the timeline below centers
                itself in - rather than only on the leftover space after
                the logo, which used to land them visibly off-center from
                the feed underneath. */}
            <div style={{ flex: 1, display: "flex", justifyContent: "center" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <TopTabs value={tab} onChange={setTab} notificationCount={notificationCount} />
                {/* Issue #84: Friendships is no longer its own top-level
                    tab - reached from here instead, left of "+", only
                    while actually looking at the feed they both act on. */}
                {tab === "Social" && (
                  <button className="top-tab" onClick={() => setTab("Friends")} aria-label="Friends">
                    <svg width="17" height="17" viewBox="0 0 24 24" aria-hidden="true">
                      <circle cx="9" cy="8" r="3" stroke="currentColor" strokeWidth="1.5" fill="none" />
                      <path d="M3.5 19c0-3 2.5-5 5.5-5s5.5 2 5.5 5" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" />
                      <circle cx="17" cy="9" r="2.5" stroke="currentColor" strokeWidth="1.5" fill="none" />
                      <path d="M15.5 14.2c2.4.3 4 2 4 4.8" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" />
                    </svg>
                  </button>
                )}
                {tab === "Social" && openComposer && (
                  <button className="top-new-post-btn" onClick={() => openComposer()} aria-label="New post">+</button>
                )}
              </div>
            </div>
            <StatusWidget />
          </div>
        }
        {!authenticated &&
          <button className="top_sign_in" onClick={() => setTab("SignIn")}>
            Sign In
          </button>
        }
      </div>
      <main>
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
        {tab === "AdminPannel" && (authenticated ? <FilesExplorer initialPath="/" /> : <p>Signing in…</p>)}
        {tab === "PhotoGallery" && (authenticated ? <PhotoGallery /> : <p>Signing in…</p>)}
        {tab === "Settings" && (authenticated ? <SettingsForm /> : <p>Signing in…</p>)}
        {tab === "Notifications" && (authenticated ? (
          <NotificationsPage
            onOpenPost={(pubUuid, commentUuid) => {
              setOpenPubUuid(pubUuid);
              setOpenCommentUuid(commentUuid);
              setTab("Social");
            }}
            onOpenFriendRequests={() => setTab("Friends")}
            onAcknowledged={clearNotificationCount}
          />
        ) : <p>Signing in…</p>)}
      </main>
    </>
  )
}

export default App
