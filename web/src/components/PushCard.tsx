// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Browser notifications, asked for from a button (issue #43; UX 9 of the
// 2026-10 review). The app used to ask by itself right after a sign-in (or
// once the tour closed): Safari refuses a permission prompt no click asked
// for, and the otc_push_prompted flag left behind stopped any try after
// that. Now a card at the top of the feed has "Get notified when family
// posts": its click asks the browser (enablePush asks first, inside it),
// and Settings' Notifications switch stays where they are turned off and
// on. Shown while this browser can have them and hasn't decided
// (pushUndecided: Notification.permission "default") - until they are
// turned on or refused, or Not now hides it for good in this browser
// (otc_push_card_dismissed). A prompt closed without an answer leaves it,
// to try again. Not on a demo device (its pushes aren't anyone's), nor in
// the phone apps' web view, which have their own.
import { useEffect, useState } from "react";
import { enablePush, pushUndecided } from "../net/webPush";
import { useDemo } from "./useDemo";
import { useT } from "../i18n";
import { asIs, say, type UiText } from "./uiText";
import "./PushCard.css";

const cDismissedKey = "otc_push_card_dismissed";
// How long "Notifications are on" stays before the card goes.
const cOnShownMs = 6000;

function dismissed(): boolean {
  try {
    return localStorage.getItem(cDismissedKey) === "1";
  } catch {
    return false;
  }
}

const BellIcon = () => (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M6.5 16.5h11l-1.3-1.9a3 3 0 0 1-.5-1.7V10a3.7 3.7 0 0 0-7.4 0v2.9a3 3 0 0 1-.5 1.7z" />
    <path d="M10.4 19a1.8 1.8 0 0 0 3.2 0" />
  </svg>
);

export default function PushCard() {
  const t = useT();
  const demo = useDemo();
  const [state, setState] = useState<"ask" | "busy" | "on" | "gone">(() =>
    (!window.__OTC_CONFIG && pushUndecided() && !dismissed() ? "ask" : "gone"));
  const [error, setError] = useState<UiText | null>(null);
  useEffect(() => {
    if (state !== "on") return;
    const timer = window.setTimeout(() => setState("gone"), cOnShownMs);
    return () => window.clearTimeout(timer);
  }, [state]);
  if (demo || state === "gone") return null;

  const turnOn = () => {
    setState("busy");
    setError(null);
    enablePush().then(
      (on) => setState(on ? "on" : pushUndecided() ? "ask" : "gone"),
      (err: unknown) => {
        const words = (err as { message?: unknown } | null)?.message;
        setError(() => (words ? asIs(String(words)) : say("web.social.push.failed")));
        setState("ask");
      },
    );
  };
  const notNow = () => {
    try {
      localStorage.setItem(cDismissedKey, "1");
    } catch {
      // Not kept: it shows again on the next visit.
    }
    setState("gone");
  };

  return (
    <div className="pn-card">
      <span className="pn-icon"><BellIcon /></span>
      <div className="pn-body">
        {state !== "on" && (
          <div className="pn-actions">
            <button type="button" className="pn-on" onClick={turnOn} disabled={state === "busy"}>
              {state === "busy" ? t("web.social.push.turning_on") : t("web.social.push.turn_on")}
            </button>
            <button type="button" className="pn-later" onClick={notNow} disabled={state === "busy"}>
              {t("web.social.push.not_now")}
            </button>
          </div>
        )}
        {/* There from the start, so "on" is announced when it comes. */}
        <p className="pn-status" role="status">{state === "on" ? t("web.social.push.on") : null}</p>
        {error && <p className="pn-error" role="alert">{error(t)}</p>}
      </div>
    </div>
  );
}
