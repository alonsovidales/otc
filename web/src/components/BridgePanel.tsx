// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #145: switching the bridge on for a device set up without it.
//
// The same steps as the setup wizard's account and name steps, later: sign
// in to an Off The Cloud account, pick a name, and the device reserves it
// and restarts onto the bridge. Signing in goes through the device (email
// and password, or a setup code from the account page) - or, where the
// bridge will send the browser back here (this page on the home network:
// a .local name or a private address), through Apple or Google, which
// return with a setup code in the address.
//
// Primary-only, like the Tailscale and update panels: it changes how the
// whole machine is reached.
import { useCallback, useEffect, useRef, useState } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";
import "./UpdatePanel.css";
import "./BridgePanel.css";

type Access = {
  enabled: boolean;
  domain: string;
  bridge: string;
  pending: boolean;
  error: string;
  providers: string[];
  leftReason?: string;
  localAddress?: string;
};

const providerLabels: Record<string, string> = { apple: "Continue with Apple", google: "Continue with Google" };

// Where the bridge agrees to send a provider sign-in back to - its
// validReturnURL: the device's own name, a .local name, or a private or
// loopback address.
function returnAllowed(host: string): boolean {
  if (host === "otc" || host === "localhost" || host.endsWith(".local")) return true;
  const m = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
}

export default function BridgePanel({ onStatus }: { onStatus?: (enabled: boolean) => void }) {
  const [access, setAccess] = useState<Access | null>(null);
  const [hidden, setHidden] = useState(false);
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const onStatusRef = useRef(onStatus);
  onStatusRef.current = onStatus;

  const refresh = useCallback(async () => {
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetBridgeAccess", reqGetBridgeAccess: {} };
      });
      if (resp.payload?.$case !== "respBridgeAccess") {
        // An additional user's instance: not theirs to change.
        setHidden(true);
        return;
      }
      const a = resp.payload.respBridgeAccess;
      setAccess({ ...a, providers: a.providers ?? [] });
      onStatusRef.current?.(a.enabled);
    } catch {
      // A restart onto the bridge drops this connection; the next poll
      // picks it up again.
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  // Switching on ends with the device restarting: poll until it's back on
  // the bridge.
  useEffect(() => {
    if (!access?.pending) return;
    const t = window.setInterval(() => void refresh(), 3000);
    return () => window.clearInterval(t);
  }, [access?.pending, refresh]);

  const redeem = useCallback(async (setupCode: string) => {
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqBridgeSignIn", reqBridgeSignIn: { email: "", password: "", setupCode } };
      });
      if (resp.payload?.$case !== "respBridgeSignedIn") {
        setError(resp.errorMessage || "That setup code didn't work.");
        return;
      }
      setToken(resp.payload.respBridgeSignedIn.setupToken);
      setEmail(resp.payload.respBridgeSignedIn.email);
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  }, []);

  // Back from Apple or Google: the bridge appended the setup code to this
  // page's address. Taken off it straight away, so a reload or a shared
  // link doesn't carry it.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const returned = params.get("setup_token");
    if (!returned) return;
    params.delete("setup_token");
    params.delete("bridge_signin");
    const rest = params.toString();
    window.history.replaceState(null, "", window.location.pathname + (rest ? `?${rest}` : "") + window.location.hash);
    void redeem(returned);
  }, [redeem]);

  const signIn = async () => {
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqBridgeSignIn", reqBridgeSignIn: { email: email.trim(), password, setupCode: "" } };
      });
      if (resp.payload?.$case !== "respBridgeSignedIn") {
        setError(resp.errorMessage || "Could not sign in.");
        return;
      }
      setToken(resp.payload.respBridgeSignedIn.setupToken);
      setEmail(resp.payload.respBridgeSignedIn.email);
      setPassword("");
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  const enable = async () => {
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqEnableBridge", reqEnableBridge: { name: name.trim().toLowerCase(), setupToken: token } };
      });
      if (resp.payload?.$case !== "respBridgeAccess") {
        setError(resp.errorMessage || "Could not switch the bridge on.");
        return;
      }
      const a = resp.payload.respBridgeAccess;
      setAccess({ ...a, providers: a.providers ?? [] });
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  const startProvider = (provider: string) => {
    if (!access) return;
    const back = `${window.location.origin}${window.location.pathname}?bridge_signin=1`;
    window.location.href =
      `https://${access.bridge}/account/auth/${provider}/start?return=${encodeURIComponent(back)}`;
  };

  // The device's address at home (issue #182): .local names don't resolve
  // everywhere, so its own LAN address comes first.
  const localUrl = access?.localAddress ? `http://${access.localAddress}` : "http://otc.local:8080";

  // Issue #182: leaving the bridge. The device gives its name back and
  // restarts local-only; this page, if it came through the bridge, stops
  // answering - the device is then at http://otc.local:8080 at home.
  const leave = async () => {
    if (!window.confirm(
      `Take this device off the bridge? ${access?.domain} is given back and the device restarts. ` +
      `It keeps everything on it and works at home, at ${localUrl}, but is no longer ` +
      "reachable from outside or by your friends. You can join again later.")) return;
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqDisableBridge", reqDisableBridge: {} };
      });
      if (resp.payload?.$case !== "respBridgeAccess") {
        setError(resp.errorMessage || "Could not leave the bridge.");
        return;
      }
      const a = resp.payload.respBridgeAccess;
      setAccess({ ...a, providers: a.providers ?? [] });
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  if (hidden || !access) return null;

  if (access.enabled) {
    return (
      <section className="sf-section">
        <h3>Bridge access</h3>
        <p className="up-note">
          Reachable from anywhere at{" "}
          <a href={`https://${access.domain}`} target="_blank" rel="noreferrer">{access.domain}</a>.
        </p>
        <p className="up-note">
          Leaving the bridge gives the name back; the device keeps working at home. To delete your
          Off The Cloud account as well, leave first, then use{" "}
          <a href={`https://${access.bridge}/account?delete=1`} target="_blank" rel="noreferrer">your account page</a>.
          {" "}<a href={`https://${access.bridge}/privacy`} target="_blank" rel="noreferrer">Privacy</a>
        </p>
        <div className="up-actions">
          <button className="sf-btn sf-danger" disabled={busy} onClick={() => void leave()}>
            {busy ? "Leaving…" : "Leave the bridge"}
          </button>
        </div>
        {error && <p className="up-error">{error}</p>}
      </section>
    );
  }

  const providers = returnAllowed(window.location.hostname) ? access.providers : [];
  const nameValid = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name.trim().toLowerCase());

  return (
    <section className="sf-section">
      <h3>Bridge access</h3>

      {access.pending && access.leftReason === "left" ? (
        <p className="up-note">
          Leaving {access.bridge}… the device restarts, which takes about a minute. From then on
          open it at home, at <a href={localUrl}>{localUrl}</a>.
        </p>
      ) : access.pending ? (
        <p className="up-note">
          Joining {access.bridge}… the device restarts to finish, which takes about a minute. You
          may need to sign in again afterwards.
        </p>
      ) : (
        <>
          {access.leftReason === "released" && (
            <p className="up-error">
              This device left the bridge: {access.bridge} no longer knew its name (its account was
              deleted, or the name released). Everything on it is still here.
            </p>
          )}
          <p className="up-note">
            This device is only reachable on your home network. Through the {access.bridge} bridge
            it gets its own address, so you can reach it from anywhere and add friends. You'll need
            an Off The Cloud account.
          </p>

          {!token ? (
            <>
              {providers.length > 0 && (
                <div className="bp-providers">
                  {providers.map(p => (
                    <button key={p} className={`bp-sso bp-${p}`} disabled={busy} onClick={() => startProvider(p)}>
                      {providerLabels[p] ?? p}
                    </button>
                  ))}
                </div>
              )}

              <div className="sf-row">
                <label htmlFor="bp-email">Email</label>
                <input id="bp-email" className="sf-input" type="email" autoComplete="username"
                  value={email} onChange={e => setEmail(e.target.value)} />
              </div>
              <div className="sf-row">
                <label htmlFor="bp-password">Password</label>
                <input id="bp-password" className="sf-input" type="password" autoComplete="current-password"
                  value={password} onChange={e => setPassword(e.target.value)}
                  onKeyDown={e => { if (e.key === "Enter" && email && password) void signIn(); }} />
              </div>
              <div className="up-actions">
                <button className="sf-btn" disabled={busy || !email.trim() || !password} onClick={() => void signIn()}>
                  {busy ? "Signing in…" : "Sign in"}
                </button>
              </div>

              <p className="up-note">
                No account yet, or you sign in another way? Create one or sign in at{" "}
                <a href={`https://${access.bridge}/account`} target="_blank" rel="noreferrer">
                  {access.bridge}/account
                </a>
                , choose “Get a setup code” and paste it here.{" "}
                <a href={`https://${access.bridge}/privacy`} target="_blank" rel="noreferrer">Privacy</a>
              </p>
              <div className="sf-row">
                <label htmlFor="bp-code">Setup code</label>
                <div className="sf-secret-row">
                  <input id="bp-code" className="sf-input" value={code} onChange={e => setCode(e.target.value)} />
                  <button className="sf-btn small" type="button" disabled={busy || !code.trim()}
                    onClick={() => void redeem(code.trim())}>Use</button>
                </div>
              </div>
            </>
          ) : (
            <>
              <p className="up-note">
                Signed in as <strong>{email}</strong>.{" "}
                <button className="bp-link" type="button" onClick={() => { setToken(""); setEmail(""); }}>
                  Use another account
                </button>
              </p>
              <div className="sf-row">
                <label htmlFor="bp-name">Device name</label>
                <div className="bp-name">
                  <input id="bp-name" className="sf-input" value={name} autoCapitalize="none" spellCheck={false}
                    placeholder="myhome" onChange={e => setName(e.target.value)}
                    onKeyDown={e => { if (e.key === "Enter" && nameValid) void enable(); }} />
                  <span className="bp-tld">.{access.bridge}</span>
                </div>
              </div>
              <p className="up-note">
                Lower-case letters, digits and hyphens. A name your account already holds moves to
                this device.
              </p>
              <div className="up-actions">
                <button className="sf-btn" disabled={busy || !nameValid} onClick={() => void enable()}>
                  {busy ? "Joining…" : "Join the bridge"}
                </button>
              </div>
            </>
          )}
        </>
      )}

      {access.error && !access.pending && <p className="up-error">The last attempt failed: {access.error}</p>}
      {error && <p className="up-error">{error}</p>}
    </section>
  );
}
