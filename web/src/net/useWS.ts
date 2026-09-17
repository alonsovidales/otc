// SPDX-License-Identifier: AGPL-3.0-or-later

import { wsClient } from "./ws";
import { ReqEnvelope, RespEnvelope } from "../proto/messages";
import { encryptForConnection, savePersistedKey, clearPersistedKey } from "./pwCrypto";

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
          // Issue #46: keep the browser signed in across reloads — see
          // pwCrypto.ts for why this replays the password rather than a
          // session token (the protocol doesn't have one).
          savePersistedKey(key);
          if (setAuth) {
            await setAuth(true);
          }

          return true;
        }

        // Wrong/stale password (e.g. it was changed elsewhere, or a
        // leftover key from a previous device) — don't keep retrying it on
        // every future reload.
        clearPersistedKey();

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

  const init = (url: string, setAuthenticated: ((e: boolean) => void)) => {
    urlRef = url;
    setAuth = setAuthenticated;
  };

  const connected = () => {
    return wsClient.connected;
  };

  return { connected, request, sendAuth, init, ws: wsClient };
}

export const useWS = UseWS();
