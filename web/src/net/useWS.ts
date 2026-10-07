// SPDX-License-Identifier: AGPL-3.0-or-later

import { wsClient } from "./ws";
import { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { encryptWithPubKey, savePersistedToken, loadPersistedToken, clearPersistedToken } from "./pwCrypto";
import { isDeviceStatusCode } from "./deviceStatus";

// The device refused a password and refuses any more from this address
// for a while (issue #117). Unlike a sign-in nobody answered, that is a
// verdict, so a typed password that met it is not kept (see sendAuth).
class LockedOut extends Error {}

// What sendAuth throws, having sent nothing, for a password that is not
// to go to a new device (refuseNewDevice): one with no owner password yet
// takes the first password it is sent as its own (issue #39), so a sign-in
// typed on the page would skip its setup.
export class NewDevice extends Error {
  readonly isPrimary: boolean;
  constructor(isPrimary: boolean) {
    super("This device isn't set up yet.");
    this.isPrimary = isPrimary;
  }
}

export interface SendAuthOptions {
  // Not for a new device: the device's answer to the GetPubKey this
  // password is encrypted with says whether it is new, and if so sendAuth
  // throws NewDevice instead of sending it.
  refuseNewDevice?: boolean;
  // Typed by someone who is there to try again (the page's sign-in and
  // setup): kept for request()'s replay only once the device has accepted
  // it, never after no answer. Replayed later it would sign in, or count a
  // failed attempt, behind their back - or, on a new device, become its
  // password - and it would go ahead of the next one they type.
  keepOnlyIfAccepted?: boolean;
}

// The bridge's own reply when it could not hand a request to the device
// (no live connection to it, or the account is switched off - see
// deviceStatus.ts), as the error a sign-in throws for it. The device never
// saw the password or token, so this is no verdict on it: the credential
// is kept for another try, as when the socket drops.
function bridgeAnswer(resp: RespEnvelope): Error | null {
  if (resp.payload?.$case !== "respAck" || !isDeviceStatusCode(resp.payload.respAck.code)) return null;
  return new Error(resp.payload.respAck.errorMsg || resp.errorMessage || "The device did not answer.");
}

// How long a refused token waits for another tab's successor (see
// authWithToken): the tab that redeemed the same token first stores the
// next one a round trip after its own redemption succeeded.
const cSuccessorWaitMs = 2000;

// The token in storage once it is no longer `spent`, or when waitMs is
// over: a successor another tab stored, null if storage was emptied, or
// `spent` itself if nothing new came.
async function tokenAfter(spent: string, waitMs: number): Promise<string | null> {
  const until = Date.now() + waitMs;
  for (;;) {
    const stored = loadPersistedToken();
    if (stored !== spent || Date.now() >= until) return stored;
    await new Promise((r) => setTimeout(r, 100));
  }
}

export function UseWS() {
  let isConnected = false;
  let lastAuthRef: string = '';
  let urlRef: string = '';
  let setAuth: ((e: boolean) => void) | null = null;
  // Tracks an in-flight sendAuth() call so every other request() waits for
  // it before sending anything. Authenticating needs two full round trips
  // of its own (reqGetPubKey, then reqAuth) after the socket opens - long
  // enough that some other component's own first request (Social's initial
  // feed fetch on mount, say) used to reach the still-unauthenticated
  // connection first, get rejected, and never retry (fetchPage silently
  // drops anything that isn't the expected response). Reproduced live as
  // the Social feed sitting empty until switching tabs happened to remount
  // it well after the real auth handshake had time to finish - which read
  // as the feed just being slow, when the first attempt had actually
  // already failed outright.
  let authPromise: Promise<boolean> | null = null;
  // True from a sign-in (password or stored token) until the device says
  // the session is gone. request() redeems the stored token on a
  // reconnect only then: on first load App.tsx's own effect is the one
  // redeemer, so the same single-use token is never redeemed twice at once.
  let hadSession = false;
  // Set when a sign-in got no answer from the device while the socket
  // stayed open: the bridge keeps a connection it could not pair with the
  // device open, so the next request() signs in again on it instead of
  // waiting for a reconnect. Without that it would reach the device, once
  // it is back, on a connection with no session.
  let authOwed = false;

  // Registered exactly once per useWS instance (this function only ever
  // runs once - see the singleton export at the bottom of this file).
  // This used to live inside connect() below and get re-registered on
  // every call, one extra never-unsubscribed listener per concurrent
  // caller that hit "not connected yet" at once (several components can
  // each call request() before the first connect() resolves - see
  // ws.ts's own comment on the same race for the socket itself). Left
  // unbounded, duplicate listeners multiply on every reconnect; enough of
  // them synchronously re-processing every incoming message (this app
  // polls status every couple seconds) was enough to lock up the tab's
  // main thread - reproduced live as an unresponsive composer after a
  // Publish click during issue #87's testing.
  wsClient.onMessage((env) => {
    // Issue #105: the device says "not authenticated" (with this code -
    // see cCodeNotAuthenticated) when a request arrives on a connection
    // that has no session. Mid-session that means the session is simply
    // gone: the usual cause is the device restarting, which discards
    // every session token it was holding (see session/tokens.go).
    //
    // Only acted on when there's nothing left to recover with. A browser
    // that signed in with a password keeps it in memory for exactly this
    // (lastAuthRef, replayed by request() on reconnect), so that session
    // heals itself and must not be torn down here. One restored from a
    // stored token heals on reconnect by redeeming the token its last
    // redemption stored; it gets here only when that failed too (the
    // device restarted, or the token expired or was revoked), so there is
    // genuinely no way back without the password, and leaving the app
    // "signed in" over a dead session just produces views whose data
    // never loads.
    if (env.payload?.$case !== "respAck") return;
    if (env.payload.respAck.code !== "not_authenticated") return;
    if (lastAuthRef !== "") return;

    hadSession = false;
    clearPersistedToken();
    if (setAuth) void setAuth(false);
  });

  const connect = async () => {
    let cancelled = false;

    await wsClient
      .connect(urlRef)
      .then(() => {
        if (!cancelled) isConnected = true;
      })
      .catch((err) => {
        console.error("WS connect error:", err);
        if (!cancelled) isConnected = false;
      });

    return cancelled;
  };

  // Bypasses the authPromise wait below - used only by sendAuth's own
  // bootstrap messages (reqGetPubKey, reqAuth), which must never wait on
  // the very authPromise they're the one resolving (that would deadlock).
  const rawRequest = (req: (e: Partial<ReqEnvelope>) => void) => wsClient.request.bind(wsClient)(req);

  const request = (async (req: (e: Partial<ReqEnvelope>) => void) => {
    const reconnect = !wsClient || !wsClient.connected;
    if (reconnect || authOwed) {
      if (reconnect) await connect();
      if (wsClient.connected && !authPromise) {
        authOwed = false;
        if (lastAuthRef !== '') {
          // A replay the device rejects (the password was changed on
          // another device) is tried once, not on every click: sendAuth
          // forgets the password, and this tab signs out the way a dead
          // token session does (#105). Each retry used to count as a
          // failed attempt for this address - through the bridge, the
          // household's public IP - until it was locked out.
          sendAuth(lastAuthRef).then((ok) => {
            if (!ok && lastAuthRef === '') {
              hadSession = false;
              if (setAuth) void setAuth(false);
            }
          }, () => {
            // too_many_attempts, a dropped socket or the bridge answering
            // for an unreachable device: the password is kept (a blocked
            // address is refused before anything is counted), and the
            // await below hands the error to this request's caller.
          });
        } else if (hadSession) {
          // A session restored from a stored token: tokens outlive the
          // connection on the device, so the one the last redemption
          // stored brings this one back. Without this a socket drop
          // (sleep, a WiFi change, a bridge redeploy) signed the tab out
          // and deleted a token that still worked.
          const token = loadPersistedToken();
          if (token) {
            authWithToken(token).catch(() => {
              // No answer (see sendAuth's above): the token is kept and
              // tried again, and the await below hands the error on.
            });
          }
        }
      }
    }
    // Whether this call is the one that just kicked off sendAuth above, or
    // one that arrived while some other caller's auth was already in
    // flight, either way: never send the actual payload ahead of it.
    if (authPromise) await authPromise;

    return rawRequest(req);
  });

  // Issue #101: mints a fresh session token for the connection's
  // now-authenticated session and persists it in place of what used to be
  // the password itself. Best-effort on purpose: failing to get a token
  // only costs this browser its next reload (it falls back to the sign-in
  // form), so it must never turn an otherwise successful sign-in into a
  // failed one.
  const refreshSessionToken = async () => {
    try {
      const resp: RespEnvelope = await rawRequest(e => {
        (e as any).payload = { $case: "reqIssueSessionToken", reqIssueSessionToken: {} };
      });
      if (resp.payload?.$case === "respSessionToken" && resp.payload.respSessionToken.sessionToken) {
        const { token, expiresAtUnixMs } = resp.payload.respSessionToken.sessionToken;
        savePersistedToken(token, Number(expiresAtUnixMs));
      } else {
        console.error("Device did not return a session token:", resp.errorMessage);
      }
    } catch (e) {
      console.error("Could not obtain a session token:", e);
    }
  };

  // The password is remembered for request()'s reconnect replay once the
  // device has accepted it, or when no answer came at all (unless
  // keepOnlyIfAccepted): one the device refused (a typo, or a password that
  // is no longer the current one) is never replayed.
  const sendAuth = (key: string, opts: SendAuthOptions = {}): Promise<boolean> => {
    // A sign-in already under way (a reconnect's replay, the container's
    // launch) answers for its own credential, not one just typed: that
    // waits for it, and is sent itself unless the other one signed in -
    // a refusal then would end the session it just made.
    if (authPromise && opts.keepOnlyIfAccepted) {
      return authPromise.then(
        (ok) => (ok ? Promise.reject(new Error("Signed in meanwhile.")) : sendAuth(key, opts)),
        () => sendAuth(key, opts),
      );
    }
    if (authPromise) return authPromise;

    authPromise = (async () => {
      authOwed = false;
      try {
        if (!isConnected || !wsClient.connected) {
          await connect();
        }

        const keyResp: RespEnvelope = await rawRequest(e => {
          e.payload = { $case: "reqGetPubKey", reqGetPubKey: {} };
        });
        // The same answer the password is encrypted with, so nothing can
        // come between this check and the password going out.
        if (opts.refuseNewDevice && keyResp.payload?.$case === "respPubKey" && keyResp.payload.respPubKey.isNewDevice) {
          throw new NewDevice(keyResp.payload.respPubKey.isPrimary);
        }
        const encryptedKey = encryptWithPubKey(keyResp, key);
        const resp: RespEnvelope = await rawRequest(e => {
          (e as any).payload = { $case: "reqAuth", reqAuth: { key: encryptedKey, create: true } };
        });
        if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
          lastAuthRef = key;
          // Issue #46/#101: keep the browser signed in across reloads —
          // with a token the device issues for this session, never the
          // password that was just used to establish it.
          await refreshSessionToken();
          hadSession = true;
          if (setAuth) {
            await setAuth(true);
          }

          return true;
        }

        // The bridge, not the device, answered: the device went away
        // between the key and the password (a node restarting, the device
        // re-dialing). Not a wrong password.
        const unanswered = bridgeAnswer(resp);
        if (unanswered) throw unanswered;

        // Issue #117: locked out for a while - say so, with the time,
        // rather than "incorrect password". Thrown so the sign-in form's
        // existing error path shows the device's own message.
        if (resp.payload?.$case === "respAck" && resp.payload.respAck.code === "too_many_attempts") {
          throw new LockedOut(resp.payload.respAck.errorMsg || "Too many attempts. Try again in a minute.");
        }

        // Wrong/stale password (e.g. it was changed elsewhere, or a
        // leftover key from a previous device) — don't keep retrying it on
        // every future reload, nor on every reconnect. Only this key is
        // forgotten: a typo never wipes a password that did work.
        clearPersistedToken();
        if (lastAuthRef === key) lastAuthRef = '';

        if (window.__OTC_CONFIG!) {
          // Open the settings on error when we are in the mobile app
          (window as any).webkit?.messageHandlers?.native?.postMessage({
            action: "openSettings"
          });
        }

        return false;
      } catch (e) {
        // A new device answered; nothing was sent.
        if (e instanceof NewDevice) throw e;
        // No answer about the password (the socket dropped, or the bridge
        // answered for the device): it is tried again on the next
        // reconnect, or the next request if the socket is still open, as
        // it always was - the mobile container signs in only once, at
        // launch. If the device then refuses it, it is forgotten (see
        // request()).
        //
        // A lockout is different for a typed password: the attempt that
        // starts one is a wrong password the device counted, and replaying
        // it would fail every request with "Too many attempts" until the
        // lockout ends, then count once more. The person retypes it
        // anyway. The container's own password is still kept, so a launch
        // during someone else's lockout heals once it ends; one that did
        // work is never dropped here. Nor is one typed on the page with
        // keepOnlyIfAccepted kept at all: the person tries again.
        const containerKey = key === window.__OTC_CONFIG?.password;
        if (!(e instanceof LockedOut)) authOwed = wsClient.connected;
        if (lastAuthRef === '' && !opts.keepOnlyIfAccepted && (!(e instanceof LockedOut) || containerKey)) lastAuthRef = key;
        throw e;
      } finally {
        authPromise = null;
      }
    })();

    return authPromise;
  };

  // Issue #101: the reload path's counterpart to sendAuth — same contract
  // (resolves true once this connection is authenticated), but redeems a
  // stored session token instead of a password nobody kept. Sets
  // authPromise for exactly the same reason sendAuth does: every other
  // component's first request has to wait behind it rather than racing an
  // unauthenticated connection (see authPromise's own comment above).
  const authWithToken = (token: string): Promise<boolean> => {
    if (authPromise) return authPromise;

    authPromise = (async () => {
      authOwed = false;
      try {
        if (!isConnected || !wsClient.connected) {
          await connect();
        }

        let current = token;
        for (let attempt = 0; attempt < 2; attempt++) {
          const resp: RespEnvelope = await rawRequest(e => {
            (e as any).payload = { $case: "reqAuthWithToken", reqAuthWithToken: { token: current } };
          });
          if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
            // Redeeming consumes the token (see session.RedeemToken), so the
            // one in storage is spent the moment this succeeds — replace it
            // with a fresh one, which also slides the TTL forward another
            // hour for a tab that keeps getting reloaded.
            await refreshSessionToken();
            hadSession = true;
            if (setAuth) {
              await setAuth(true);
            }

            return true;
          }

          // The bridge answered for a device it could not reach (a node
          // restarting, the device not yet re-dialed): the token was never
          // looked at and still works, so it stays for another try.
          const unanswered = bridgeAnswer(resp);
          if (unanswered) throw unanswered;

          // Expired, unknown, or already-redeemed — fall back to the normal
          // sign-in form rather than retrying it on every future reload.
          // Two tabs that reloaded or reconnected together read the same
          // token. When the other one redeemed it first, its successor
          // reaches storage a round trip after this refusal does, so this
          // tab waits a moment for it and tries it once, instead of
          // signing out and clearing storage just before the successor
          // lands. Only a token nothing replaced is cleared.
          const next = await tokenAfter(current, attempt === 0 ? cSuccessorWaitMs : 0);
          if (next === current) clearPersistedToken();
          if (!next || next === current) return false;
          current = next;
        }
        return false;
      } catch (e) {
        authOwed = wsClient.connected;
        throw e;
      } finally {
        authPromise = null;
      }
    })();

    return authPromise;
  };

  const init = (url: string, setAuthenticated: ((e: boolean) => void)) => {
    urlRef = url;
    setAuth = setAuthenticated;
  };

  const connected = () => {
    return wsClient.connected;
  };

  // After Change Password: a reconnect replays the new password, not the
  // old one the device now refuses. A session restored from a token holds
  // no password (#101) and keeps holding none.
  const passwordChanged = (newKey: string) => {
    if (lastAuthRef !== '') lastAuthRef = newKey;
  };

  return { connected, request, sendAuth, authWithToken, refreshSessionToken, passwordChanged, init, ws: wsClient };
}

export const useWS = UseWS();
