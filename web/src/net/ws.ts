import { ReqEnvelope, RespEnvelope } from "../proto/messages";

type RespListener = (env: RespEnvelope) => void;

export class WSClient {
  private ws?: WebSocket;
  private nextId = 1;
  private waiters = new Map<number, (env: RespEnvelope) => void>();
  private listeners: Set<RespListener> = new Set();
  public connected = false;

  onMessage(fn: RespListener) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  async connect(url: string): Promise<void> {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) return;

    await new Promise<void>((resolve, reject) => {
      //const ws = new WebSocket(url);
      const ws = new WebSocket('ws://otc:8080/ws');
      ws.binaryType = "arraybuffer";

      ws.onopen = () => { this.connected = true; resolve(); };
      ws.onerror = (e) => reject(e);
      ws.onclose = () => { this.connected = false; };
      ws.onmessage = (ev) => {
        try {
          const env = RespEnvelope.decode(new Uint8Array(ev.data as ArrayBuffer));
          const cont = this.waiters.get(env.id);
          if (cont) { this.waiters.delete(env.id); cont(env); }
          this.listeners.forEach(fn => fn(env));
        } catch (err) {
          console.error("WS decode error", err);
        }
      };

      this.ws = ws;
    });
  }

  close() { this.ws?.close(); }

  request(build: (e: Partial<ReqEnvelope>) => void): Promise<RespEnvelope> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("WS not connected"));
    }
    const id = this.nextId++;
    const draft: Partial<ReqEnvelope> = { id };
    build(draft);
    const req = ReqEnvelope.fromPartial(draft);
    const bytes = ReqEnvelope.encode(req).finish();
    console.log("Bytes:", bytes);
    this.ws.send(bytes);
    return new Promise<RespEnvelope>((resolve) => this.waiters.set(id, resolve));
  }
}

// ✅ export a singleton instance to use in the hook
export const wsClient = new WSClient();
