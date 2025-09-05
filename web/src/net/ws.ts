import { ReqEnvelope, RespEnvelope } from "../proto/messages";

type Listener = (env: ReqEnvelope) => void;

export class WSClient {
  private ws?: WebSocket;
  private nextId = 1;
  private waiters = new Map<number, (env: RespEnvelope) => void>();
  private listeners: Set<Listener> = new Set();
  public connected = false;

  onMessage(fn: Listener) { this.listeners.add(fn); return () => this.listeners.delete(fn); }

  async connect(url: string): Promise<void> {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) return;
    await new Promise<void>((resolve, reject) => {
      const ws = new WebSocket('ws://otc:8080/ws');
      console.log('Connected to otc, but use url:', url);
      //const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      ws.onopen = () => { this.connected = true; resolve(); };
      ws.onerror = (e) => reject(e);
      ws.onclose = () => { this.connected = false; };
      ws.onmessage = (ev) => {
        try {
          const env = RespEnvelope.decode(new Uint8Array(ev.data as ArrayBuffer));
          // resolve waiter if matching id
          const cont = this.waiters.get(env.id);
          if (cont) { this.waiters.delete(env.id); cont(env); }
          // and notify listeners (for push messages)
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
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return Promise.reject(new Error("WS not connected"));
    const id = this.nextId++;
    const obj: Partial<ReqEnvelope> = { id };
    build(obj);
    const env = ReqEnvelope.fromPartial(obj);
    const bytes = ReqEnvelope.encode(env).finish();
    console.log('Req obj:', obj, bytes);
    this.ws.send(bytes);
    return new Promise<RespEnvelope>((resolve) => this.waiters.set(id, resolve));
  }
}

// singleton (optional)
export const wsClient = new WSClient();
