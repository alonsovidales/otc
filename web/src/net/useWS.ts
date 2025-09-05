import { useEffect, useState, useCallback } from "react";
import { wsClient } from "./ws";

export function useWS(url: string) {
  const [connected, setConnected] = useState(wsClient.connected);

  useEffect(() => {
    let cancelled = false;
    console.log('Connecting to:', url);
    wsClient.connect(url).then(() => !cancelled && setConnected(true)).catch(console.error);
    const off = wsClient.onMessage(() => {}); // attach if you want global push handling
    return () => { cancelled = true; off(); wsClient.close(); };
  }, [url]);

  const request = useCallback(wsClient.request.bind(wsClient), []);

  //return { connected, request, ws: wsClient };
  return { connected, request };
}
