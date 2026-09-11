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
import "./components/StatusWidget.css";
import type { ReqEnvelope, RespEnvelope } from "./proto/messages";
import { useSearchParams } from "react-router-dom";
import { isNewDevice, loadPersistedKey } from "./net/pwCrypto";
import { promptForPushIfNeverAsked } from "./net/webPush";
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
        if (await isNewDevice(useWS.request)) setTab("SignIn");
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
    // reload doesn't drop back to the sign-in form.
    useEffect(() => {
      const key = loadPersistedKey();
      if (!key) return;
      (async () => {
        try {
          const ok = await useWS.sendAuth(key);
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

  return (
    <>
      <div className="header">
        {!mobile &&
          <a>
             <img src={logo} className="logo" alt="Off The Cloud logo" />
          </a>
        }

        {authenticated &&
          <div style={{ flex: 1, display: "block", justifyContent: "center" }}>
            <div style={{ flex: 1, display: "flex", justifyContent: "center" }}>
              <TopTabs value={tab} onChange={setTab} />
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
        {tab === "Profile" && authenticated && <FriendshipsManager />}
        {tab === "Profile" && !authenticated && <ProfileCard authenticated={authenticated} />}
        {tab === "Social" && <Social authenticated={authenticated} />}
        {tab === "SignIn" && <SignIn
          onAuth={async (key) => await useWS.sendAuth(key)}
          onDone={handleSignedIn}
        />}
        {tab === "AdminPannel" && <FilesExplorer initialPath="/" />}
        {tab === "PhotoGallery" && <PhotoGallery />}
        {tab === "Settings" && <SettingsForm />}
      </main>
    </>
  )
}

export default App
