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
import { encryptForConnection } from "../net/pwCrypto";
import type { RespEnvelope } from "../proto/messages";
import { Trans, useT } from "../i18n";
import { asIs, say, type PlainKey, type UiText } from "./uiText";
import "./UpdatePanel.css";
import "./BridgePanel.css";
import { refuseOnDemo } from "../net/demo";

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

// Keys, translated when shown; a provider this build doesn't know shows its id.
const providerLabels = new Map<string, PlainKey>([["apple", "web.settings.bridge.continue_apple"], ["google", "web.settings.bridge.continue_google"]]);

// Where the bridge agrees to send a provider sign-in back to - its
// validReturnURL: the device's own name, a .local name, or a private or
// loopback address. The quick answer; anything else (a router's own names,
// "pit.otc") is asked of the bridge itself, which has the full rule.
function returnAllowed(host: string): boolean {
  if (host === "otc" || host === "localhost" || host.endsWith(".local")) return true;
  const m = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
}

export default function BridgePanel({ onStatus }: { onStatus?: (enabled: boolean) => void }) {
  const t = useT();
  const [access, setAccess] = useState<Access | null>(null);
  const [hidden, setHidden] = useState(false);
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<UiText | null>(null);
  const onStatusRef = useRef(onStatus);
  // Issue #182 follow-up: whether Google/Apple sign-in can come back to
  // this page's address - asked of the bridge when the quick check says no.
  const [providerReturnOk, setProviderReturnOk] = useState(() => returnAllowed(window.location.hostname));
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

  useEffect(() => {
    if (providerReturnOk || !access?.bridge || !access.providers?.length) return;
    let alive = true;
    const ret = encodeURIComponent(window.location.origin + window.location.pathname);
    fetch(`https://${access.bridge}/api/account/return-allowed?return=${ret}`, { cache: "no-store" })
      .then(r => r.json())
      .then(j => { if (alive && j?.ok) setProviderReturnOk(true); })
      .catch(() => { /* stays hidden: the setup code still works */ });
    return () => { alive = false; };
  }, [access?.bridge, access?.providers, providerReturnOk]);

  // Switching on ends with the device restarting: poll until it's back on
  // the bridge.
  useEffect(() => {
    if (!access?.pending) return;
    const timer = window.setInterval(() => void refresh(), 3000);
    return () => window.clearInterval(timer);
  }, [access?.pending, refresh]);

  const redeem = useCallback(async (setupCode: string) => {
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqBridgeSignIn", reqBridgeSignIn: { email: "", password: "", setupCode } };
      });
      if (resp.payload?.$case !== "respBridgeSignedIn") {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.bridge.code_failed")));
        return;
      }
      setToken(resp.payload.respBridgeSignedIn.setupToken);
      setEmail(resp.payload.respBridgeSignedIn.email);
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
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

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setBusy(true);
    setError(null);
    try {
      // Sealed for this connection's key, as a sign-in's password is: the
      // bridge relays the request. The device serving this page reads it
      // (only an older one's own, older page sends the plain field).
      const passwordSealed = await encryptForConnection(useWS.request, password);
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqBridgeSignIn", reqBridgeSignIn: { email: email.trim(), password: "", setupCode: "", passwordSealed } };
      });
      if (resp.payload?.$case !== "respBridgeSignedIn") {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.bridge.sign_in_failed")));
        return;
      }
      setToken(resp.payload.respBridgeSignedIn.setupToken);
      setEmail(resp.payload.respBridgeSignedIn.email);
      setPassword("");
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
    } finally {
      setBusy(false);
    }
  };

  const enable = async () => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqEnableBridge", reqEnableBridge: { name: name.trim().toLowerCase(), setupToken: token } };
      });
      if (resp.payload?.$case !== "respBridgeAccess") {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.bridge.enable_failed")));
        return;
      }
      const a = resp.payload.respBridgeAccess;
      setAccess({ ...a, providers: a.providers ?? [] });
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
    } finally {
      setBusy(false);
    }
  };

  const startProvider = (provider: string) => {

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
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
    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    if (!window.confirm(t("web.settings.bridge.leave_confirm", { domain: `${access?.domain}`, address: localUrl }))) return;
    setBusy(true);
    setError(null);
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqDisableBridge", reqDisableBridge: {} };
      });
      if (resp.payload?.$case !== "respBridgeAccess") {
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.bridge.leave_failed")));
        return;
      }
      const a = resp.payload.respBridgeAccess;
      setAccess({ ...a, providers: a.providers ?? [] });
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
    } finally {
      setBusy(false);
    }
  };

  if (hidden || !access) return null;

  if (access.enabled) {
    return (
      <section className="sf-section">
        <h3>{t("web.settings.bridge.title")}</h3>
        <p className="up-note">
          <Trans k="web.settings.bridge.reachable" args={{ domain: access.domain }} parts={{
            link: (s) => <a href={`https://${access.domain}`} target="_blank" rel="noreferrer">{s}</a>,
          }} />
        </p>
        <p className="up-note">
          <Trans k="web.settings.bridge.leave_hint" parts={{
            account: (s) => <a href={`https://${access.bridge}/account?delete=1`} target="_blank" rel="noreferrer">{s}</a>,
            privacy: (s) => <a href={`https://${access.bridge}/privacy`} target="_blank" rel="noreferrer">{s}</a>,
          }} />
        </p>
        <div className="up-actions">
          <button className="sf-btn sf-danger" disabled={busy} onClick={() => void leave()}>
            {busy ? t("web.settings.bridge.leaving") : t("web.settings.bridge.leave")}
          </button>
        </div>
        {error && <p className="up-error">{error(t)}</p>}
      </section>
    );
  }

  const providers = providerReturnOk ? access.providers : [];
  const nameValid = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name.trim().toLowerCase());

  return (
    <section className="sf-section">
      <h3>{t("web.settings.bridge.title")}</h3>

      {access.pending && access.leftReason === "left" ? (
        <p className="up-note">
          <Trans k="web.settings.bridge.pending_leave" args={{ bridge: access.bridge, address: localUrl }} parts={{
            link: (s) => <a href={localUrl}>{s}</a>,
          }} />
        </p>
      ) : access.pending ? (
        <p className="up-note">{t("web.settings.bridge.pending_join", { bridge: access.bridge })}</p>
      ) : (
        <>
          {access.leftReason === "released" && (
            <p className="up-error">{t("web.settings.bridge.released", { bridge: access.bridge })}</p>
          )}
          <p className="up-note">{t("web.settings.bridge.intro", { bridge: access.bridge })}</p>

          {!token ? (
            <>
              {providers.length > 0 && (
                <div className="bp-providers">
                  {providers.map(p => (
                    <button key={p} className={`bp-sso bp-${p}`} disabled={busy} onClick={() => startProvider(p)}>
                      {providerLabels.has(p) ? t(providerLabels.get(p)!) : p}
                    </button>
                  ))}
                </div>
              )}

              <div className="sf-row">
                <label htmlFor="bp-email">{t("web.settings.bridge.email")}</label>
                <input id="bp-email" className="sf-input" type="email" autoComplete="username"
                  value={email} onChange={e => setEmail(e.target.value)} />
              </div>
              <div className="sf-row">
                <label htmlFor="bp-password">{t("common.password")}</label>
                <input id="bp-password" className="sf-input" type="password" autoComplete="current-password"
                  value={password} onChange={e => setPassword(e.target.value)}
                  onKeyDown={e => { if (e.key === "Enter" && email && password) void signIn(); }} />
              </div>
              <div className="up-actions">
                <button className="sf-btn" disabled={busy || !email.trim() || !password} onClick={() => void signIn()}>
                  {busy ? t("web.settings.bridge.signing_in") : t("web.settings.bridge.sign_in")}
                </button>
              </div>

              <p className="up-note">
                <Trans k="web.settings.bridge.setup_hint" args={{ address: `${access.bridge}/account` }} parts={{
                  account: (s) => <a href={`https://${access.bridge}/account`} target="_blank" rel="noreferrer">{s}</a>,
                  terms: (s) => <a href={`https://${access.bridge}/terms`} target="_blank" rel="noreferrer">{s}</a>,
                  privacy: (s) => <a href={`https://${access.bridge}/privacy`} target="_blank" rel="noreferrer">{s}</a>,
                }} />
              </p>
              <div className="sf-row">
                <label htmlFor="bp-code">{t("web.settings.bridge.setup_code")}</label>
                <div className="sf-secret-row">
                  <input id="bp-code" className="sf-input" value={code} onChange={e => setCode(e.target.value)} />
                  <button className="sf-btn small" type="button" disabled={busy || !code.trim()}
                    onClick={() => void redeem(code.trim())}>{t("web.settings.bridge.use")}</button>
                </div>
              </div>
            </>
          ) : (
            <>
              <p className="up-note">
                <Trans k="web.settings.bridge.signed_in_as" args={{ email }} parts={{ b: (s) => <strong>{s}</strong> }} />{" "}
                <button className="bp-link" type="button" onClick={() => { setToken(""); setEmail(""); }}>
                  {t("web.settings.bridge.other_account")}
                </button>
              </p>
              <div className="sf-row">
                <label htmlFor="bp-name">{t("web.settings.bridge.name")}</label>
                <div className="bp-name">
                  <input id="bp-name" className="sf-input" value={name} autoCapitalize="none" spellCheck={false}
                    placeholder={t("web.settings.bridge.name_example")} onChange={e => setName(e.target.value)}
                    onKeyDown={e => { if (e.key === "Enter" && nameValid) void enable(); }} />
                  <span className="bp-tld">.{access.bridge}</span>
                </div>
              </div>
              <p className="up-note">{t("web.settings.bridge.name_hint")}</p>
              <div className="up-actions">
                <button className="sf-btn" disabled={busy || !nameValid} onClick={() => void enable()}>
                  {busy ? t("web.settings.bridge.joining") : t("web.settings.bridge.join")}
                </button>
              </div>
            </>
          )}
        </>
      )}

      {access.error && !access.pending && <p className="up-error">{t("web.settings.bridge.last_failed", { detail: access.error })}</p>}
      {error && <p className="up-error">{error(t)}</p>}
    </section>
  );
}
