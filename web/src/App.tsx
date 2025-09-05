import { useState } from 'react'
import logo from './assets/off_the_cloud.png'
import './App.css'
import Home from "./views/Home";
import { useWS } from "./net/useWS";
import { RespEnvelope } from "./proto/messages";
import SignIn from "./views/SignIn";

type Page = "Home" | "SignIn";

function App() {
  const [page, setPage] = useState<Page>("Home");
  const [authenticated, setAuthenticated] = useState(false);
  const { connected, request } = useWS('ws');

  const sendAuth = async (key: string) => {
    console.log('SedAuth...', key);
    const resp: RespEnvelope = await request(e => {
      console.log('GotAuth...');
      (e as any).reqAuth = { uuid: "asdsadas", key: key, create: false };
    });
    console.log("auth resp", resp);
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
      </div>
      <main>
        {page === "Home" && <Home />}
        {page === "SignIn" && <SignIn onAuth={sendAuth} />}
      </main>
    </>
  )
}

export default App
