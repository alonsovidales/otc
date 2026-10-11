// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Signing this browser out: the account menu's Sign out (AccountMenu.tsx)
// and Settings' Sign Out. Once only: a second call while the first runs
// (a double click, or both buttons) waits for that one.

import type { ReqEnvelope } from "../proto/messages";
import { useWS } from "./useWS";
import { clearPersistedToken } from "./pwCrypto";
import { clearPrivateUiState } from "./uiState";
import { unregisterPushOnSignOut } from "./webPush";
import { languageChoice } from "../i18n/choice";

// How long each best-effort step may keep the page waiting for the device:
// a device that stopped answering must not leave this browser signed in.
const STEP_MS = 8000;

let running: Promise<void> | null = null;

export function signOut(): Promise<void> {
  running ??= run();
  return running;
}

// Resolves when `p` settles or after STEP_MS, whichever comes first.
function bounded(p: Promise<unknown>, what: string): Promise<void> {
  return new Promise((resolve) => {
    const t = window.setTimeout(() => {
      console.error(`Sign out: gave up waiting to ${what}`);
      resolve();
    }, STEP_MS);
    p.then(
      () => { window.clearTimeout(t); resolve(); },
      (err) => { window.clearTimeout(t); console.error(`Could not ${what} on sign out:`, err); resolve(); },
    );
  });
}

async function run(): Promise<void> {
  // Issue #131: forget this browser's push subscription first, so the
  // device stops pushing to a browser no longer signed in. Before the
  // tokens go, since it needs the session.
  await bounded(unregisterPushOnSignOut(), "unregister push");
  // Issue #101: tell the device to drop every token descending from this
  // login, so a copy of one sitting in another tab's storage stops working
  // right now rather than whenever its TTL happens to run out. Best-effort
  // like the step above: clearing this browser's own storage and reloading
  // happens either way.
  await bounded(useWS.request((e: Partial<ReqEnvelope>) => {
    e.payload = { $case: "reqRevokeSessionToken", reqRevokeSessionToken: {} };
  }), "revoke session tokens");
  clearPersistedToken();
  clearPrivateUiState();
  // The language stays (otc_language): the signed-out page and the next
  // sign-in keep it, as Log Out does in the apps. Only a change the device
  // never got is dropped, so it can't reach whoever signs in next.
  languageChoice.dropPending();
  window.location.reload();
}
