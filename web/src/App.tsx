import { useEffect, useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import Social from "./views/Social";
import { useWS } from "./net/useWS";
import SignIn from "./views/SignIn";
import AdminPannel from "./views/AdminPannel";
import StatusWidget from "./components/StatusWidget";
import PhotoGallery from "./components/PhotoGallery";
import TopTabs from "./components/TopTabs";
import type { TabKey } from "./components/TopTabs";
import "./components/StatusWidget.css";

declare global { interface Window { __OTC_CONFIG?: { endpoint: string; password: string; deviceId: string; }; } }

function App() {
  const cfg = window.__OTC_CONFIG!;
  const [tab, setTab] = useState<TabKey>("Social");
  const [authenticated, setAuthenticated] = useState(false);
  console.log('Use WS App');
  let endpoint = 'ws://otc:8080/ws';

  if (cfg) {
    endpoint = cfg.endpoint;
  }
  useWS.init(endpoint, setAuthenticated);

  if (cfg) {
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

  return (
    <>
      <div className="header">
        <a onClick={() => setTab("Social")}>
          <img src={logo} className="logo" alt="Off The Cloud logo" />
        </a>
        <div className="header">
          <a>
            <img src={logo} className="logo" alt="Off The Cloud logo" />
          </a>

          {/* Tabs centered; tweak layout to fit your header */}
          {authenticated &&
            <div style={{ flex: 1, display: "flex", justifyContent: "center" }}>
              <TopTabs value={tab} onChange={setTab} />
            </div>
          }
          {!authenticated && 
            <button className="top_sign_in" onClick={() => setTab("SignIn")}>
              Sign In
            </button>
          }
          {authenticated && 
            <StatusWidget className="top_sign_in" />
          }
        </div>
      </div>
      <main>
        {tab === "Social" && <Social />}
        {tab === "SignIn" && <SignIn onAuth={async (key) => {
          if (await useWS.sendAuth(key)) {
            setTab("Social");
          }
        }} />}
        {tab === "AdminPannel" && <AdminPannel />}
        {tab === "PhotoGallery" && <PhotoGallery />}
      </main>
    </>
  )
}

export default App
