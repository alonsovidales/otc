import { useCallback, useEffect, useState } from "react";
import { wsClient } from "./ws"; // <-- make sure ws.ts exports: export const wsClient = new WSClient();
import type { RespEnvelope } from "../proto/messages";

/**
 * React hook to manage a single WS connection.
 * - Connects on mount (or when url changes)
 * - Exposes connection state and request() function
 */
export function useWS(url: string) {
  const [connected, setConnected] = useState<boolean>(wsClient.connected);

  useEffect(() => {
    let cancelled = false;

    wsClient
      .connect(url)
      .then(() => {
        if (!cancelled) setConnected(true);
      })
      .catch((err) => {
        console.error("WS connect error:", err);
        if (!cancelled) setConnected(false);
      });

    // optional: listen for push messages (server-initiated)
    const off = wsClient.onMessage((env: RespEnvelope) => {
      // handle push notifications if you need
      console.log("push", env);
    });

    return () => {
      cancelled = true;
      off();            // remove listener
      wsClient.close(); // close socket on unmount / url change
      setConnected(false);
    };
  }, [url]);

  // stable reference to request() so components can call it
  const request = useCallback(wsClient.request.bind(wsClient), []);

  return { connected, request, ws: wsClient };
}

export default useWS;
