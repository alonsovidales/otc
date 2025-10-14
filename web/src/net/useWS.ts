import { wsClient } from "./ws";
import { ReqEnvelope, RespEnvelope } from "../proto/messages";

export function UseWS() {
  let isConnected = false;
  let lastAuthRef: string = '';
  let urlRef: string = '';
  let setAuth: ((e: boolean) => void) | null = null;

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

    // TODO: listen for push messages (server-initiated)
    wsClient.onMessage((env: RespEnvelope) => {
      // handle push notifications if you need
      console.log("push", env);
    });

    return cancelled;
  };

  const request = (async (req: (e: Partial<ReqEnvelope>) => void) => {
    console.log('Request!!!', wsClient.connected);
    if (!wsClient || !wsClient.connected) {
      await connect();
      console.log('Connect!!!', wsClient.connected, lastAuthRef);
      if (wsClient.connected && lastAuthRef !== '') {
        console.log('Auth!!!');
        sendAuth(lastAuthRef);
      }
    }

    return wsClient.request.bind(wsClient)(req);
  });

  const sendAuth = async (key: string) => {
    lastAuthRef = key;
    if (!isConnected || !wsClient.connected) {
      await connect();
    }

    const resp: RespEnvelope = await request(e => {
      console.log('GotAuth...');
      (e as any).payload = { $case: "reqAuth", reqAuth: { key, create: true } };
    });
    console.log("auth resp", resp);
    if (resp.payload?.$case === "respAck" && resp.payload.respAck.ok) {
      if (setAuth) {
        await setAuth(true);
      }

      return true;
    }

    if (window.__OTC_CONFIG!) {
      // Open the settings on error when we are in the mobile app
      (window as any).webkit?.messageHandlers?.native?.postMessage({
        action: "openSettings"
      });
    }

    return false;
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
