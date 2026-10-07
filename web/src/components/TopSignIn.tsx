// SPDX-License-Identifier: AGPL-3.0-or-later

// Signing in from the top bar: the device's password and Sign In at the
// right end of the bar while nobody is signed in (App.tsx), instead of a
// page of its own. Enter or Sign In signs in right there; a refusal is said
// under the field with the text selected to type again, and nothing else on
// the page changes. On a phone the bar has no room for the logo and a field
// both, so the field stays folded into the Sign In button until that is
// pressed, and the logo makes way while it is out.
//
// A form password managers understand: the password (current-password) and
// a hidden username, the device's address, so a password is saved and
// filled per device.
//
// Only Enter or Sign In sends the password, and it is never sent to a new
// device (signInWithPassword refuses on the device's own answer): App shows
// its setup instead. Nor is it sent again later by itself: when the bridge
// answers for an unreachable device, its full-page screen takes the place
// of this one, and the password comes back in the field with a note once
// the device is back (App keeps it meanwhile).
import { useEffect, useId, useRef, useState } from "react";
import { flushSync } from "react-dom";
import "./TopSignIn.css";
import Spinner from "./Spinner";
import { signInWithPassword, type SignInResult } from "../net/signIn";
import { getDeviceStatus } from "../net/deviceStatus";

type Refusal = Extract<SignInResult, { ok: false }>;

// A sign-in the unreachable screen cut off, kept by App until the field is
// back.
export interface Interrupted {
  password: string;
}

const cInterruptedNote: Refusal = {
  ok: false,
  reason: "unreachable",
  message: "Your device stopped answering, so you weren't signed in. Try again now that it's back.",
};

interface Props {
  // The device's address: the username a password manager files the
  // password under.
  deviceHost: string;
  // A phone's bar: the field folds into the Sign In button.
  compact: boolean;
  // Whether the field is out, when compact (App hides the logo meanwhile).
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The device turned out to be new (issue #39): nothing was sent, and App
  // shows its setup.
  onNewDevice: (isPrimary: boolean) => void;
  // Lands the page, as after any sign-in (App's handleSignedIn).
  onSignedIn: () => void;
  // A sign-in the unreachable screen cut off (see Interrupted), read as
  // this mounts, and the call that hands one to App or (null) takes it
  // back.
  interrupted: Interrupted | null;
  onInterrupted: (interrupted: Interrupted | null) => void;
}

export default function TopSignIn({
  deviceHost, compact, open, onOpenChange, onNewDevice, onSignedIn, interrupted, onInterrupted,
}: Props) {
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<Refusal | null>(null);
  // The guard against a second Enter while the first is being checked:
  // state would only catch it after a render.
  const busyRef = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const id = useId();
  const fieldId = `${id}-password`;
  const noteId = `${id}-note`;
  const folded = compact && !open;

  // Back after the unreachable screen: the password that was being sent,
  // and why nothing came of it.
  const [resumed] = useState(interrupted);
  useEffect(() => {
    if (!resumed || !inputRef.current) return;
    inputRef.current.value = resumed.password;
    setRefusal(cInterruptedNote);
    onInterrupted(null);
  }, [resumed, onInterrupted]);

  // A refusal leaves the field focused with the password selected, so the
  // next keystroke starts it again. Not when nothing answered: the same
  // password is worth another Enter, so the caret goes to its end.
  useEffect(() => {
    const input = inputRef.current;
    if (!refusal || !input) return;
    input.focus({ preventScroll: true });
    if (refusal.reason === "unreachable") input.setSelectionRange(input.value.length, input.value.length);
    else input.select();
  }, [refusal]);

  const unfold = () => {
    // Rendered and focused within the press, or a phone won't raise its
    // keyboard for it.
    flushSync(() => onOpenChange(true));
    inputRef.current?.focus();
  };

  const fold = () => {
    if (busyRef.current) return;
    if (inputRef.current) inputRef.current.value = "";
    setRefusal(null);
    onOpenChange(false);
    buttonRef.current?.focus();
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busyRef.current) return;
    const input = inputRef.current;
    // Read from the field rather than from state: a password manager may
    // have filled it.
    const password = input?.value ?? "";
    if (!password) {
      input?.focus();
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setRefusal(null);
    onInterrupted(null);
    try {
      const result = await signInWithPassword(password);
      if (result.ok) {
        offerToSave(deviceHost, password);
        onSignedIn();
        return;
      }
      if (result.reason === "new-device") {
        onNewDevice(result.isPrimary);
        return;
      }
      // The bridge answered for the device: its unreachable screen is about
      // to take the page, this field with it, so App keeps the password.
      if (result.reason === "unreachable" && getDeviceStatus()?.code === "device_unreachable") {
        onInterrupted({ password });
      }
      setRefusal(result);
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  return (
    <form
      className={`tsi${compact ? " compact" : ""}${folded ? "" : " is-open"}`}
      onSubmit={submit}
      aria-busy={busy}
      // Not the browser's own "fill this in" bubble: an empty Sign In
      // just puts the cursor in the field.
      noValidate
    >
      {compact && open && (
        <button type="button" className="tsi-close" aria-label="Close sign in" onClick={fold}>
          <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
            strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M19 12H5m6-6-6 6 6 6" />
          </svg>
        </button>
      )}
      {/* The account the password belongs to, for password managers only. */}
      <input type="text" name="username" autoComplete="username" defaultValue={deviceHost} hidden tabIndex={-1} />
      <div className={`tsi-field${refusal ? " is-invalid" : ""}`}>
        <input
          ref={inputRef}
          id={fieldId}
          className="tsi-input"
          type="password"
          name="password"
          autoComplete="current-password"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          enterKeyHint="go"
          // A space keeps :placeholder-shown, which is what lowers the label
          // into the empty field.
          placeholder=" "
          aria-invalid={refusal ? true : undefined}
          aria-describedby={refusal ? noteId : undefined}
          onInput={() => { if (refusal) setRefusal(null); }}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return;
            if (compact) { e.preventDefault(); fold(); }
            else if (refusal) setRefusal(null);
          }}
        />
        <label htmlFor={fieldId} className="tsi-label">Password</label>
      </div>
      <button
        ref={buttonRef}
        type={folded ? "button" : "submit"}
        className={`tsi-submit${busy ? " is-busy" : ""}`}
        aria-disabled={busy || undefined}
        // Folded, it opens the field; once that is out it is the submit.
        aria-expanded={folded ? false : undefined}
        aria-controls={folded ? fieldId : undefined}
        onClick={folded ? unfold : undefined}
        // Pressed while the field has the cursor, the field keeps it - and
        // a phone its keyboard - so a refusal selects the password in place
        // to type again.
        onMouseDown={(e) => { if (!folded && document.activeElement === inputRef.current) e.preventDefault(); }}
      >
        <span className="tsi-submit-label">Sign In</span>
        {busy && <span className="tsi-submit-spin"><Spinner /></span>}
      </button>
      <span className="tsi-sr" role="status">{busy ? "Signing in…" : ""}</span>
      {refusal && (
        <p id={noteId} className="tsi-note" role="alert">{refusal.message}</p>
      )}
    </form>
  );
}

// Chrome and Edge offer to save a password typed into a form only when the
// page navigates or a fetch follows, and signing in here does neither (it is
// the device's socket), so the password is handed to the password manager
// once it has worked. Elsewhere this does nothing.
type PasswordCredentialInit = { id: string; password: string };
function offerToSave(id: string, password: string) {
  const Ctor = (window as unknown as { PasswordCredential?: new (init: PasswordCredentialInit) => Credential }).PasswordCredential;
  if (!Ctor || !navigator.credentials?.store) return;
  try {
    navigator.credentials.store(new Ctor({ id, password })).catch(() => { /* declined or unavailable */ });
  } catch {
    // An id or password the browser won't take: nothing is offered.
  }
}
