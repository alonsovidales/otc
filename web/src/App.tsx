import { useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import Home from "./views/Home";
import { useWS } from "./net/useWS";
import { RespEnvelope } from "./proto/messages";
import SignIn from "./views/SignIn";
import AdminPannel from "./views/AdminPannel";
import StatusWidget from "./components/StatusWidget";
import "./components/StatusWidget.css";

type Page = "Home" | "SignIn" | "AdminPannel";

function App() {
  const [page, setPage] = useState<Page>("Home");
  const [authenticated, setAuthenticated] = useState(false);
  console.log('Use WS App');
  const { connected, request } = useWS('ws');
  console.log('Use WS App-', connected);

  const sendAuth = async (key: string) => {
    console.log('SedAuth...', key);
    const resp: RespEnvelope = await request(e => {
      console.log('GotAuth...');
      (e as any).payload = { $case: "reqAuth", reqAuth: { key, create: true } };
    });
    console.log("auth resp", resp);
    if (resp.payload.respAck.ok) {
      setAuthenticated(true);
      setPage("AdminPannel");
    }
  };

  return (
    <>
      <div className="header">
        <a onClick={() => setPage("Home")}>
          <img src={logo} className="logo" alt="Off The Cloud logo" />
        </a>
        {!authenticated && 
          <button className="top_sign_in" onClick={() => setPage("SignIn")}>
            Sign In
          </button>
        }
        {authenticated && 
          <StatusWidget className="top_sign_in" wsUrl={import.meta.env.VITE_WS_URL} />
        }
      </div>
      <main>
        {page === "Home" && <Home />}
        {page === "SignIn" && <SignIn onAuth={sendAuth} />}
        {page === "AdminPannel" && <AdminPannel />}
      </main>
    </>
  )
}

export default App
