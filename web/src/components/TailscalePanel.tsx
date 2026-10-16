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
import { Trans, useT } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import "./UpdatePanel.css";
import { refuseOnDemo } from "../net/demo";

type TailscaleState = {
  installed: boolean;
  loggedIn: boolean;
  funnelOn: boolean;
  publicUrl: string;
  loginUrl: string;
  error: string;
};

export default function TailscalePanel() {
  const t = useT();
  const [isPrimary, setIsPrimary] = useState<boolean | null>(null);
  const [state, setState] = useState<TailscaleState | null>(null);
  const [authKey, setAuthKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<UiText | null>(null);
  const [error, setError] = useState<UiText | null>(null);
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
    const s = resp.payload.respTailscaleStatus;
    return {
      installed: s.installed,
      loggedIn: s.loggedIn,
      funnelOn: s.funnelOn,
      publicUrl: s.publicUrl,
      loginUrl: s.loginUrl,
      error: s.error,
    };
  };

  const refresh = useCallback(async () => {
    try {
      const resp: RespEnvelope = await useWS.request(e => {
        (e as any).payload = { $case: "reqGetTailscaleStatus", reqGetTailscaleStatus: {} };
      });
      setState(read(resp));
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
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

    if (refuseOnDemo()) return; // a demo device (net/demo.ts)
    setBusy(true);
    setNote(null);
    setError(null);
    setPopupBlocked(false);
    // Opened synchronously, inside the click, and pointed somewhere only
    // once the device answers: a window.open after an await has lost the
    // user gesture a browser requires, and gets blocked.
    const authWindow = enable ? window.open("", "_blank") : null;
    // The login page gets no handle back into the app (see FilesExplorer).
    if (authWindow) authWindow.opener = null;
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
        setError(() => (resp.errorMessage ? asIs(resp.errorMessage) : say("web.settings.tailscale.change_failed")));
        return;
      }
      setState(next);
      if (next.error) { setError(() => asIs(next.error)); return; }
      if (enable && next.loggedIn) setPopupBlocked(false);
      if (!enable) { setNote(() => say("web.settings.tailscale.off")); return; }
      if (!next.installed) {
        setError(() => say("web.settings.tailscale.not_installed_enable"));
        return;
      }
      if (next.loginUrl) {
        if (authWindow) {
          authWindow.location.href = next.loginUrl;
          navigated = true;
          setNote(() => say("web.settings.tailscale.authorise_window"));
        } else {
          // Blocked, so the link has to be offered after all.
          setPopupBlocked(true);
          setNote(() => say("web.settings.tailscale.authorise"));
        }
        return;
      }
      if (next.funnelOn) {
        const done: UiText = (tr) => tr("web.settings.tailscale.done", { address: next.publicUrl });
        setNote(() => done);
        return;
      }
      setNote(() => say("web.settings.tailscale.no_funnel"));
    } catch (e: any) {
      setError(() => asIs(e?.message ?? String(e)));
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
          <Trans k="web.settings.tailscale.served_at" args={{ address: state.publicUrl }} parts={{
            link: (s) => <a href={state.publicUrl} target="_blank" rel="noreferrer">{s}</a>,
          }} />
        </p>
      )}

      {state && !state.installed ? (
        <p className="up-note">{t("web.settings.tailscale.not_installed")}</p>
      ) : (
        <>
          <p className="up-note">
            <Trans k="web.settings.tailscale.intro" parts={{ code: (s) => <code>{s}</code> }} />
          </p>
          <ul className="up-releases">
            <li><Trans k="web.settings.tailscale.limit_social" parts={{ b: (s) => <strong>{s}</strong> }} /></li>
            <li><Trans k="web.settings.tailscale.limit_one_user" parts={{ b: (s) => <strong>{s}</strong> }} /></li>
            <li>{t("web.settings.tailscale.limit_traffic")}</li>
            <li>{t("web.settings.tailscale.limit_account")}</li>
          </ul>
          <p className="up-note">{t("web.settings.tailscale.suits")}</p>

          {!state?.funnelOn && (
            <div className="sf-row">
              <label htmlFor="sf-ts-key">{t("web.settings.tailscale.auth_key")}</label>
              <input
                id="sf-ts-key"
                className="sf-input"
                type="text"
                placeholder="tskey-auth-…" // i18n-ignore: the start of every Tailscale auth key
                value={authKey}
                onChange={e => setAuthKey(e.target.value)}
              />
            </div>
          )}

          {popupBlocked && state?.loginUrl && (
            <p className="up-note">
              <Trans k="web.settings.tailscale.popup_blocked" parts={{
                link: (s) => <a href={state.loginUrl} target="_blank" rel="noreferrer">{s}</a>,
              }} />
            </p>
          )}

          <div className="up-actions">
            {state?.funnelOn ? (
              <button className="sf-btn" onClick={() => void configure(false)} disabled={busy}>
                {busy ? t("web.settings.general.working") : t("web.settings.tailscale.turn_off")}
              </button>
            ) : (
              <button className="sf-btn" onClick={() => void configure(true)} disabled={busy}>
                {busy ? t("web.settings.tailscale.setting_up") : t("web.settings.tailscale.enable")}
              </button>
            )}
          </div>

          {!state?.funnelOn && (
            <p className="up-note">{t("web.settings.tailscale.key_hint")}</p>
          )}
        </>
      )}

      {note && <p className="up-note">{note(t)}</p>}
      {error && <p className="up-error">{error(t)}</p>}
    </section>
  );
}
