// SPDX-License-Identifier: AGPL-3.0-or-later

import { wsClient } from "./ws";
import { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { encryptForConnection, savePersistedToken, clearPersistedToken } from "./pwCrypto";

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
  wsClient.onMessage(() => {
    // handle push notifications if you need
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
    if (!wsClient || !wsClient.connected) {
      await connect();
      if (wsClient.connected && lastAuthRef !== '' && !authPromise) {
        void sendAuth(lastAuthRef);
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

  const sendAuth = (key: string): Promise<boolean> => {
    lastAuthRef = key;
    if (authPromise) return authPromise;

    authPromise = (async () => {
      try {
        if (!isConnected || !wsClient.connected) {
          await connect();
        }

        const encryptedKey = await encryptForConnection(rawRequest, key);
        const resp: RespEnvelope = await rawRequest(e => {
          (e as any).payload = { $case: "reqAuth", reqAuth: { key: encryptedKey, create: true } };
        });
        if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
          // Issue #46/#101: keep the browser signed in across reloads —
          // with a token the device issues for this session, never the
          // password that was just used to establish it.
          await refreshSessionToken();
          if (setAuth) {
            await setAuth(true);
          }

          return true;
        }

        // Wrong/stale password (e.g. it was changed elsewhere, or a
        // leftover key from a previous device) — don't keep retrying it on
        // every future reload.
        clearPersistedToken();

        if (window.__OTC_CONFIG!) {
          // Open the settings on error when we are in the mobile app
          (window as any).webkit?.messageHandlers?.native?.postMessage({
            action: "openSettings"
          });
        }

        return false;
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
      try {
        if (!isConnected || !wsClient.connected) {
          await connect();
        }

        const resp: RespEnvelope = await rawRequest(e => {
          (e as any).payload = { $case: "reqAuthWithToken", reqAuthWithToken: { token } };
        });
        if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
          // Redeeming consumes the token (see session.RedeemToken), so the
          // one in storage is spent the moment this succeeds — replace it
          // with a fresh one, which also slides the TTL forward another
          // hour for a tab that keeps getting reloaded.
          await refreshSessionToken();
          if (setAuth) {
            await setAuth(true);
          }

          return true;
        }

        // Expired, unknown, or already-redeemed — fall back to the normal
        // sign-in form rather than retrying it on every future reload.
        clearPersistedToken();
        return false;
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

  return { connected, request, sendAuth, authWithToken, refreshSessionToken, init, ws: wsClient };
}

export const useWS = UseWS();
