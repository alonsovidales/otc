// SPDX-License-Identifier: AGPL-3.0-or-later

// Issue #56: a domain that's registered on the bridge but whose device
// has no live connection used to surface as "Unable to fetch the
// connection's public key" - the bridge's real explanation was dropped on
// the way (see deviceStatus.ts and the bridge's own default-case handler).
// This is what gets shown instead, with the same unplugged-cloud icon the
// bridge's own unavailable.html uses (issue #97) so the two halves of the
// same situation - device down before the app loads, device down while
// it's open - look like the same thing rather than two unrelated errors.
//
// Issue #207: the same screen, in its own words and with a disk for its
// icon, while the device answers that it can't reach its storage disks
// (storage_unavailable): not "unreachable" - it answered - and not a wrong
// password - it checked nothing.
//
// It lies over the app, which stays as it is underneath (App.tsx): a modal
// dialog, so the page behind takes no click, key or focus meanwhile, and
// what was typed or open there - a post being written, a dialog, a
// download - is still there when the device answers again. The words are
// the web app's own in the language on screen, never the bridge's (its
// relay texts are always English): only the device's own explanation of
// its storage is shown as it comes.
import { useEffect, useId, useLayoutEffect, useRef } from "react";
import { useWS } from "../net/useWS";
import { cStorageUnavailable, recheckMs, type DeviceStatus } from "../net/deviceStatus";
import { useT } from "../i18n";
import LanguagePicker from "./LanguagePicker";
import "./DeviceUnreachable.css";

// How often to check whether the device is answering again (recheckMs):
// cheap enough to be frequent (one GetPubKey, no auth, no device work
// beyond generating a keypair) and slow enough not to hammer the bridge
// with one client's reconnect attempts - every 30 s for a device that
// can't reach its storage, which starts again by itself once it can.

export default function DeviceUnreachable({ status }: { status: DeviceStatus }) {
  const t = useT();
  const disabled = status.code === "account_disabled";
  const noStorage = status.code === cStorageUnavailable;
  const every = recheckMs(status.code);

  // The page behind may have nothing that asks the device anything (a
  // section with no poll, the composer waiting) - so without this, nothing
  // would ever issue the request whose success is what clears this
  // screen, and it would sit here until a manual reload even after the
  // device came back. Caught exactly that way in testing, when this screen
  // still replaced the app: the device returned, and the page kept
  // insisting it hadn't.
  //
  // Any successful response clears the state on its own (see
  // deviceStatus.noteResponse), so this only has to make a request, not
  // interpret one. Not polled for a disabled account: that state lasts
  // until the owner does something about it, and retrying every few
  // seconds would be asking a question already answered. Back online, or
  // the tab shown again, it asks at once.
  useEffect(() => {
    if (every == null) return;
    const ask = () => {
      useWS.request((e) => {
        e.payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
      }).catch(() => {
        // Still down - the socket may not even be connectable yet. The
        // next tick tries again; there is nothing useful to report here
        // that this screen isn't already saying.
      });
    };
    const id = setInterval(ask, every);
    const onShown = () => { if (document.visibilityState === "visible") ask(); };
    window.addEventListener("online", ask);
    document.addEventListener("visibilitychange", onShown);
    return () => {
      clearInterval(id);
      window.removeEventListener("online", ask);
      document.removeEventListener("visibilitychange", onShown);
    };
  }, [every]);

  // Over everything, the page behind inert; what had the focus there has
  // it again once the device answers. The browser may close a modal dialog
  // itself on a second Escape: it comes back while it is still needed.
  const dialogRef = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const d = dialogRef.current;
    if (!d) return;
    const back = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!d.open) d.showModal();
    // The screen itself, not the language list (the first control), which
    // a modal dialog would focus: a screen reader starts at its words.
    d.focus({ preventScroll: true });
    // Keys pressed here are this screen's: the page's own shortcuts (a
    // viewer's arrows, a dialog's Escape) listen on the document and the
    // window, which this keeps them from.
    const stop = (e: KeyboardEvent) => e.stopPropagation();
    d.addEventListener("keydown", stop);
    return () => {
      d.removeEventListener("keydown", stop);
      if (d.open) d.close();
      if (back?.isConnected && back !== document.body) back.focus({ preventScroll: true });
    };
  }, []);
  const titleId = useId();
  const textId = useId();

  const title = disabled
    ? t("web.shell.unreachable.disabled")
    : noStorage ? t("web.shell.unreachable.storage_title") : t("web.shell.unreachable.title");
  // Ours, in the language on screen: the bridge words device_unreachable
  // and account_disabled in English only. The device that can't reach its
  // storage says why itself (in the request's language), ours when it
  // sent nothing.
  const message = disabled
    ? t("web.shell.unreachable.disabled_text")
    : noStorage ? status.message || t("web.shell.unreachable.storage_text") : t("web.shell.unreachable.text");
  return (
    <dialog
      ref={dialogRef}
      className="du-root"
      // Explicit, for the page's own key handling (Files' Escape looks for it).
      role="dialog"
      tabIndex={-1}
      aria-labelledby={titleId}
      aria-describedby={textId}
      onCancel={(e) => e.preventDefault()}
      onClose={() => { const d = dialogRef.current; if (d?.isConnected && !d.open) d.showModal(); }}
    >
      {/* The language, where the top bar has it: this screen covers the
          app, Settings included, until the device answers again. */}
      <LanguagePicker className="du-lang" />
      <div className="du-card">
        {disabled ? (
          <svg className="du-icon" width="56" height="56" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="1.5" />
            <path d="M5.6 5.6l12.8 12.8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        ) : noStorage ? (
          <svg className="du-icon" width="56" height="56" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <rect x="3" y="13" width="18" height="7" rx="2" stroke="currentColor" strokeWidth="1.5" />
            <path
              d="M4.5 13.5 7.1 6a2 2 0 0 1 1.9-1.4h6a2 2 0 0 1 1.9 1.4l2.6 7.5"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinejoin="round"
            />
            <circle cx="17" cy="16.5" r="1" fill="currentColor" />
            <path d="M3 3l18 18" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        ) : (
          <svg className="du-icon" width="56" height="56" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path
              d="M17.5 18H7a4.5 4.5 0 0 1-.6-8.96 5.5 5.5 0 0 1 10.53 1.48A3.75 3.75 0 0 1 17.5 18Z"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinejoin="round"
            />
            <path d="M3 3l18 18" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        )}
        <h2 id={titleId}>{title}</h2>
        <p id={textId}>{message}</p>
        {/* For an owner whose router's name or password changed: the
            device can't join it any more, and this is all they see. Right
            after the bridge's words, before "keeps checking", as on the
            phones. Not when the device answered: its network is fine. */}
        {!disabled && !noStorage && (
          <p className="du-hint">{t("web.shell.unreachable.wifi_hint")}</p>
        )}
        {!disabled && (
          <p className="du-note">{noStorage ? t("web.shell.unreachable.storage_note") : t("web.shell.unreachable.note")}</p>
        )}
      </div>
    </dialog>
  );
}
