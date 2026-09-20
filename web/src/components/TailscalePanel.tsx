// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #80: reaching this device over Tailscale Funnel instead of the
// bridge.
//
// In Settings rather than the setup wizard, deliberately: this is a
// decision someone makes once the device is already working, and the one
// place in the wizard it could have gone was after the WiFi step - which
// drops the very connection the page is on, so most people would never
// have seen it.
//
// Primary-only, like the update and users panels. Funnel publishes the
// whole machine, which is not an additional user's to turn on, and is
// also why it can only ever serve one user: one machine, one public name.
import { useCallback, useEffect, useState } from "react";
import { useWS } from "../net/useWS";
import type { RespEnvelope } from "../proto/messages";
import "./UpdatePanel.css";

type TailscaleState = {
  installed: boolean;
  loggedIn: boolean;
  funnelOn: boolean;
  publicUrl: string;
  loginUrl: string;
  error: string;
};

export default function TailscalePanel() {
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [state, setState] = useState<TailscaleState | null>(null);
  const [authKey, setAuthKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [popupBlocked, setPopupBlocked] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const resp: RespEnvelope = await useWS.request(e => {
          (e as any).payload = { $case: "reqGetInstanceRole", reqGetInstanceRole: {} };
        });
        setIsPrimary(resp.payload?.$case === "respInstanceRole" && resp.payload.respInstanceRole.isPrimary);
      } catch {
        setIsPrimary(false);
      }
    })();
  }, []);

  const read = (resp: RespEnvelope): TailscaleState | null => {
    if (resp.payload?.$case !== "respTailscaleStatus") return null;
    const t = resp.payload.respTailscaleStatus;
    return {
      installed: t.installed,
      loggedIn: t.loggedIn,
      funnelOn: t.funnelOn,
      publicUrl: t.publicUrl,
      loginUrl: t.loginUrl,
      error: t.error,
    };
  };

  const refresh = useCallback(async () => {
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetTailscaleStatus", reqGetTailscaleStatus: {} };
      });
      setState(read(resp));
    } catch (e: any) {
      setError(e?.message ?? String(e));
    }
  }, []);

  useEffect(() => {
    if (isPrimary) void refresh();
  }, [isPrimary, refresh]);

  // Authorising happens in another tab, so coming back to this one is the
  // moment the device has something new to say - without this the panel
  // keeps showing "authorise this device" long after you have.
  useEffect(() => {
    if (!isPrimary) return;
    const onFocus = () => void refresh();
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [isPrimary, refresh]);

  const configure = async (enable: boolean) => {
    setBusy(true);
    setNote(null);
    setError(null);
    setPopupBlocked(false);
    // Opened synchronously, inside the click, and pointed somewhere only
    // once the device answers: a window.open after an await has lost the
    // user gesture a browser requires, and gets blocked.
    const authWindow = enable ? window.open("", "_blank") : null;
    let navigated = false;
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = {
          $case: "reqSetupTailscale",
          reqSetupTailscale: { authKey: authKey.trim(), enable },
        };
      });
      const next = read(resp);
      if (!next) {
        setError(resp.errorMessage || "Could not change the setting.");
        return;
      }
      setState(next);
      if (next.error) { setError(next.error); return; }
      if (enable && next.loggedIn) setPopupBlocked(false);
      if (!enable) { setNote("Funnel is off. This device is reachable through the bridge again."); return; }
      if (!next.installed) {
        setError("Tailscale isn't installed on this device, so Funnel can't be turned on here.");
        return;
      }
      if (next.loginUrl) {
        if (authWindow) {
          authWindow.location.href = next.loginUrl;
          navigated = true;
          setNote("Authorise this device in the window that just opened, then press Enable again.");
        } else {
          // Blocked, so the link has to be offered after all.
          setPopupBlocked(true);
          setNote("Authorise this device in Tailscale, then press Enable again.");
        }
        return;
      }
      if (next.funnelOn) { setNote(`Done — this device is reachable at ${next.publicUrl}`); return; }
      setNote("Tailscale is connected, but Funnel didn't come up. Check that Funnel is enabled for your tailnet.");
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      // Anything other than a pending login leaves a blank tab behind.
      // Tracked with a flag rather than by reading the window's location,
      // which throws once it has been pointed at another origin.
      if (authWindow && !navigated && !authWindow.closed) {
        authWindow.close();
      }
      setBusy(false);
    }
  };

  if (isPrimary === false) return null;

  return (
    <section className="sf-section">
      <h3>Tailscale Funnel</h3>

      {state?.funnelOn && (
        <p className="up-note">
          Served over Tailscale Funnel at{" "}
          <a href={state.publicUrl} target="_blank" rel="noreferrer">{state.publicUrl}</a>.
        </p>
      )}

      {state && !state.installed ? (
        <p className="up-note">
          Tailscale isn't installed on this device, so Funnel isn't available here.
        </p>
      ) : (
        <>
          <p className="up-note">
            Tailscale Funnel is an alternative to the Off The Cloud bridge, it publishes this
            device on your own <code>ts.net</code> address but with the next limitations:
          </p>
          <ul className="up-releases">
            <li><strong>The social side won't work.</strong> Friends find each other through the
              bridge, so sharing and friends' timelines are unavailable.</li>
            <li><strong>One user only.</strong> Extra users need a web address each, and Funnel
              gives this machine a single one.</li>
            <li>Tailscale caps how much traffic can pass through Funnel, and doesn't publish
              the limit.</li>
            <li>You'll need a Tailscale account, with Funnel enabled for your tailnet.</li>
          </ul>
          <p className="up-note">
            It suits a device you want purely as your own private NAS — your files and photos,
            reachable from anywhere, nothing shared with anyone.
          </p>

          {!state?.funnelOn && (
            <div className="sf-row">
              <label htmlFor="sf-ts-key">Auth key (optional)</label>
              <input
                id="sf-ts-key"
                className="sf-input"
                type="text"
                placeholder="tskey-auth-…"
                value={authKey}
                onChange={e => setAuthKey(e.target.value)}
              />
            </div>
          )}

          {popupBlocked && state?.loginUrl && (
            <p className="up-note">
              Your browser blocked the window —{" "}
              <a href={state.loginUrl} target="_blank" rel="noreferrer">
                authorise this device in Tailscale
              </a>{" "}
              instead.
            </p>
          )}

          <div className="up-actions">
            {state?.funnelOn ? (
              <button className="sf-btn" onClick={() => void configure(false)} disabled={busy}>
                {busy ? "Working…" : "Turn Funnel off"}
              </button>
            ) : (
              <button className="sf-btn" onClick={() => void configure(true)} disabled={busy}>
                {busy ? "Setting up…" : "Enable Tailscale Funnel"}
              </button>
            )}
          </div>

          {!state?.funnelOn && (
            <p className="up-note">
              With a key from your Tailscale admin console this finishes on its own. Without
              one, you'll be taken to Tailscale to authorise this device.
            </p>
          )}
        </>
      )}

      {note && <p className="up-note">{note}</p>}
      {error && <p className="up-error">{error}</p>}
    </section>
  );
}
