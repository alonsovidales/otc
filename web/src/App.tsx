import { useEffect, useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import Social from "./views/Social";
import { useWS } from "./net/useWS";
import SignIn from "./views/SignIn";
import AdminPannel from "./views/AdminPannel";
import StatusWidget from "./components/StatusWidget";
import PhotoGallery from "./components/PhotoGallery";
import SettingsForm from "./components/SettingsForm";
import ProfileCard from "./components/ProfileCard";
import TopTabs from "./components/TopTabs";
import type { TabKey } from "./components/TopTabs";
import "./components/StatusWidget.css";

declare global { interface Window { __OTC_CONFIG?: { endpoint: string; password: string; deviceId: string; }; } }

function App() {
  const cfg = window.__OTC_CONFIG!;
  const [tab, setTab] = useState<TabKey>("Profile");
  const [authenticated, setAuthenticated] = useState(false);
  let protoWs = 'ws://';
  if (window.location.protocol === 'https:') {
    protoWs = 'wss://';
  }
  //let endpoint = protoWs + window.location.host + '/ws';
  let endpoint = protoWs + 'cala.off-the.cloud/ws';
  const mobile = !!cfg;

  if (mobile) {
    endpoint = cfg.endpoint;
  }
  useWS.init(endpoint, setAuthenticated);

  if (mobile) {
    useEffect(() => {
      (async () => {
        try {
          const ok = await useWS.sendAuth(cfg.password);
          if (ok) setTab("AdminPannel");
        } catch (e) {
          console.error("Auto-auth from container failed:", e);
        }
      })();
    }, [useWS]);
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
        {tab === "Profile" && <ProfileCard authenticated={authenticated} />}
        {tab === "Social" && <Social />}
        {tab === "SignIn" && <SignIn onAuth={async (key) => {
          if (await useWS.sendAuth(key)) {
            setTab("Profile");
          }
        }} />}
        {tab === "AdminPannel" && <AdminPannel />}
        {tab === "PhotoGallery" && <PhotoGallery />}
        {tab === "Settings" && <SettingsForm />}
      </main>
    </>
  )
}

export default App
